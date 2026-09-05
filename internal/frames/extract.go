package frames

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registered so DecodeConfig can size what we produced
	_ "image/png"
	"strconv"
	"time"

	"github.com/madmurdok/torpeek/internal/ffmpeg"
)

// ErrNoFrame means ffmpeg read the file but produced no image at that point.
var ErrNoFrame = errors.New("no frame decoded")

// Format is the image encoding a frame is saved in.
type Format string

const (
	// JPEG keeps a full-resolution frame in tens of kilobytes.
	JPEG Format = "jpeg"
	// PNG is lossless, for when the frame is evidence about encoding quality.
	PNG Format = "png"
)

// Frame is one decoded image and what is known about it.
type Frame struct {
	Data          []byte
	Width, Height int
	// Requested is the timestamp asked for. The frame actually decoded is at
	// or after the preceding keyframe; the caller, which knows that keyframe,
	// records the true timecode.
	Requested time.Duration
}

// Extractor turns a timestamp into an image by invoking ffmpeg against a URL.
type Extractor struct {
	tools  ffmpeg.Tools
	Format Format

	// Quality is the JPEG quality scale ffmpeg uses, where 2 is near-lossless
	// and 31 is worst. Frames are kept at source resolution, so this is the
	// only size dial.
	Quality int

	// ProbeSize caps what ffmpeg reads before it starts decoding. Widened on
	// retry: a window too small to decode is the one failure worth retrying.
	ProbeSize int64

	// Retries is how many widened attempts follow a failure.
	Retries int

	// toneMap is the colour conversion for the file being read, set through
	// WithToneMap. Unexported and only ever written by that copy, because one
	// extractor serves every file of a run at once while colour is a property
	// of a single file.
	toneMap ToneMap
}

// NewExtractor returns an extractor with defaults suited to reading over a
// torrent rather than a local disk.
func NewExtractor(tools ffmpeg.Tools) *Extractor {
	return &Extractor{
		tools:     tools,
		Format:    JPEG,
		Quality:   2,
		ProbeSize: 5 << 20,
		Retries:   2,
	}
}

// WithToneMap returns a copy of the extractor that converts frames out of the
// given high dynamic range transfer on the way to an image (TOR-108). The zero
// ToneMap converts nothing, which is what an SDR file gets and what every file
// got before this existed.
//
// A copy rather than a setter: engine.Engine builds one extractor per run and
// hands it to every file's goroutine at once, so a field written per file
// would be a data race and, worse, would sometimes tone map the wrong file.
func (e *Extractor) WithToneMap(t ToneMap) *Extractor {
	copied := *e
	copied.toneMap = t
	return &copied
}

// Frame decodes a single image at or after the keyframe preceding at.
//
// The seek goes before -i deliberately: input seeking makes ffmpeg jump
// straight to the keyframe instead of decoding its way there from the start of
// the file, which over a torrent is the difference between fetching one window
// and fetching everything up to that point.
func (e *Extractor) Frame(ctx context.Context, url string, at time.Duration) (Frame, error) {
	if at < 0 {
		at = 0
	}

	probeSize := e.ProbeSize
	var lastErr error

	for attempt := 0; attempt <= e.Retries; attempt++ {
		data, err := e.decode(ctx, url, at, probeSize)
		if err == nil && len(data) > 0 {
			cfg, _, decodeErr := image.DecodeConfig(bytes.NewReader(data))
			if decodeErr != nil {
				return Frame{}, fmt.Errorf("ffmpeg produced %d bytes that are not an image: %w", len(data), decodeErr)
			}
			return Frame{
				Data:      data,
				Width:     cfg.Width,
				Height:    cfg.Height,
				Requested: at,
			}, nil
		}

		if err == nil {
			err = ErrNoFrame
		}
		lastErr = err

		if ctx.Err() != nil {
			return Frame{}, ctx.Err()
		}

		// The one failure worth retrying: too little of the stream was read to
		// find a decodable picture. Doubling costs another window at most.
		probeSize *= 2
	}

	return Frame{}, fmt.Errorf("decode frame at %s: %w", at, lastErr)
}

func (e *Extractor) decode(ctx context.Context, url string, at time.Duration, probeSize int64) ([]byte, error) {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-probesize", strconv.FormatInt(probeSize, 10),
	}

	// -ss before -i is input seeking: jump to the keyframe, do not decode up
	// to it. Placing it after -i would read the whole file to that point.
	if at > 0 {
		args = append(args, "-ss", formatSeconds(at))
	}

	args = append(args, "-i", url)

	// An HDR source read as if it were SDR comes out flat and grey, which
	// reads as our bug rather than as the file's dynamic range (TOR-108). The
	// chain that fixes it is in hdr.go; it is only ever added for a stream
	// whose colour metadata says it needs one, so an SDR file's command line
	// and its cost are exactly what they were.
	if vf := e.toneMap.Filter(e.Format); vf != "" {
		args = append(args, "-vf", vf)
	}

	args = append(args,
		"-frames:v", "1",
		"-f", "image2pipe",
	)

	switch e.Format {
	case PNG:
		args = append(args, "-c:v", "png")
	default:
		args = append(args, "-c:v", "mjpeg", "-q:v", strconv.Itoa(e.Quality))
	}

	// Write to stdout: a frame is small enough to stay in memory, and a
	// temporary file would need cleaning up on every failure path.
	args = append(args, "-")

	return e.tools.Run(ctx, "ffmpeg", args...)
}

// formatSeconds renders a timestamp as plain seconds, which is what -ss takes.
func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}
