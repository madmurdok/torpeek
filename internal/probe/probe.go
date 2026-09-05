// Package probe wraps ffprobe. It answers two questions about a file published
// by the bridge: what is in it (duration, audio and subtitle tracks, quality
// figures) and where a keyframe for a given timestamp sits in bytes.
//
// This is deliberately not a container parser. ffprobe reports the byte
// position of a keyframe for any container ffmpeg understands, which is why
// torpeek carries no MP4/MKV/AVI index code of its own - see ARCHITECTURE.md.
//
// Requirements: sections 2.4, 2.7 and 2.8.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/madmurdok/torpeek/internal/ffmpeg"
)

// ErrNoIndex means ffprobe could not make sense of the container well enough
// to seek in it - no duration, or no keyframe where one was asked for. This is
// the condition that gates the sequential fallback (requirements 2.7).
var ErrNoIndex = errors.New("container has no usable index")

// Reason names, in a form a caller can switch on, why a container had no
// usable index. The message text in Error() may be reworded; Reason will not
// change under it - it is what core.processFile matches on to decide whether
// the sequential fallback even applies (only ReasonNoDuration means the file
// still has readable frames, merely no length to plan them across), and what
// it would report as the honest reason a run refused to guess.
type Reason string

const (
	// ReasonNoVideoStream: nothing to take a frame from - a container, but
	// not a video one.
	ReasonNoVideoStream Reason = "no_video_stream"
	// ReasonNoDuration: ffprobe named no length to spread capture points
	// over. The file itself may be perfectly readable start to finish; this
	// is the one reason the sequential fallback exists for.
	ReasonNoDuration Reason = "no_duration"
	// ReasonKeyframeTimestamp: a keyframe packet was found but states
	// neither a PTS nor a DTS, so it cannot be placed in time.
	ReasonKeyframeTimestamp Reason = "keyframe_no_timestamp"
	// ReasonKeyframePosition: no keyframe with a usable byte position was
	// found where one was looked for.
	ReasonKeyframePosition Reason = "keyframe_no_position"
)

// NoIndexError is ErrNoIndex carrying a machine-readable Reason, so a caller
// can act on why probing failed without parsing Error's free text.
//
// Unwrap returns the ErrNoIndex sentinel itself rather than a wrapped copy,
// so errors.Is(err, ErrNoIndex) keeps working for every caller that only
// checks the sentinel - core.CodeOf among them.
type NoIndexError struct {
	Reason Reason
	// Detail is free text: what ffprobe reported, or where. May be empty.
	Detail string
}

func (e *NoIndexError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%s: %s", ErrNoIndex, e.Reason)
	}
	return fmt.Sprintf("%s: %s: %s", ErrNoIndex, e.Reason, e.Detail)
}

func (e *NoIndexError) Unwrap() error { return ErrNoIndex }

// Prober runs ffprobe against bridge URLs.
type Prober struct {
	tools ffmpeg.Tools

	// ProbeSize and AnalyzeDuration cap how much ffprobe reads before it
	// answers. They are the main lever we have over how much a probe costs,
	// since ffmpeg decides for itself what to read.
	ProbeSize       int64
	AnalyzeDuration time.Duration
}

// New returns a prober using the given tools, with limits suited to reading
// over a torrent rather than a local disk.
func New(tools ffmpeg.Tools) *Prober {
	return &Prober{
		tools:           tools,
		ProbeSize:       5 << 20,
		AnalyzeDuration: 5 * time.Second,
	}
}

// MediaInfo is what a file turns out to contain.
type MediaInfo struct {
	FormatName string
	Duration   time.Duration
	Size       int64
	BitRate    int64

	Video     VideoStream
	Audio     []AudioStream
	Subtitles []SubtitleStream
}

// VideoStream describes the video track frames are taken from.
type VideoStream struct {
	Index   int
	Codec   string
	Profile string
	Width   int
	Height  int
	FPS     float64
	BitRate int64

	// ColorTransfer, ColorPrimaries and ColorSpace are the stream's colour
	// tags as ffprobe spells them - "smpte2084", "arib-std-b67", "bt2020",
	// "bt2020nc" and the rest - and ColorRange is "tv" or "pc". They are
	// carried verbatim rather than mapped onto an enum here because the
	// decision they feed is itself a set of ffmpeg filter arguments
	// (frames.ToneMapFor), and a round trip through our own vocabulary would
	// only add a place for the two spellings to drift apart.
	//
	// An empty string means the stream stated nothing, which is a different
	// thing from stating a default: a file with no transfer is left alone.
	ColorTransfer  string
	ColorPrimaries string
	ColorSpace     string
	ColorRange     string

	// DolbyVisionProfile is dv_profile from the stream's Dolby Vision
	// configuration record, and 0 when it carries none. Profiles 4 and 5 have
	// an IPT-PQ-c2 base layer that no bundled build can render faithfully, so
	// this is what keeps the HDR10 tone map off data it would mangle.
	DolbyVisionProfile int
}

// BitsPerPixel is the cheap tell for an over-compressed rip or an upscale: the
// same bitrate spread over four times the pixels is not the same quality.
func (v VideoStream) BitsPerPixel() float64 {
	pixels := float64(v.Width) * float64(v.Height) * v.FPS
	if pixels == 0 || v.BitRate == 0 {
		return 0
	}
	return float64(v.BitRate) / pixels
}

// AudioStream is one audio track - the answer to "is this the dub I wanted".
type AudioStream struct {
	Index    int
	Codec    string
	Language string
	Title    string
	Channels int
	BitRate  int64
	Default  bool
}

// SubtitleStream is one subtitle track.
type SubtitleStream struct {
	Index    int
	Codec    string
	Language string
	Title    string
	Forced   bool
	Default  bool
}

// Keyframe is a seek target: where a keyframe sits in time and in bytes.
type Keyframe struct {
	// PTS is the keyframe's presentation timestamp, at or before the time
	// that was asked for.
	PTS time.Duration
	// BytePos is its offset in the file, which maps onto pieces.
	BytePos int64
}

// Inspect reports what the file at url contains.
func (p *Prober) Inspect(ctx context.Context, url string) (MediaInfo, error) {
	args := append(p.limits(), "-print_format", "json", "-show_format", "-show_streams", url)

	out, err := p.tools.Run(ctx, "ffprobe", args...)
	if err != nil {
		return MediaInfo{}, fmt.Errorf("inspect: %w", err)
	}
	return parseInspect(out)
}

// dolbyVisionSideData is what ffprobe calls the Dolby Vision configuration
// record in a stream's side_data_list. Matched on the name because that is
// all the JSON gives: the record is otherwise indistinguishable from any
// other side data entry.
const dolbyVisionSideData = "DOVI configuration record"

// parseInspect turns one ffprobe -show_format -show_streams document into a
// MediaInfo.
//
// Split out of Inspect so it can be tested against output captured from a
// real ffprobe run rather than only against files this project can render.
// That matters for exactly one field: a Dolby Vision configuration record
// cannot be produced by any encoder in the bundled builds, so the branch that
// reads dv_profile would otherwise be code nothing ever executes.
func parseInspect(out []byte) (MediaInfo, error) {
	var raw struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
			Size       string `json:"size"`
			BitRate    string `json:"bit_rate"`
		} `json:"format"`
		Streams []struct {
			Index        int    `json:"index"`
			CodecName    string `json:"codec_name"`
			CodecType    string `json:"codec_type"`
			Profile      string `json:"profile"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			AvgFrameRate string `json:"avg_frame_rate"`
			BitRate      string `json:"bit_rate"`
			Channels     int    `json:"channels"`

			ColorRange     string `json:"color_range"`
			ColorSpace     string `json:"color_space"`
			ColorTransfer  string `json:"color_transfer"`
			ColorPrimaries string `json:"color_primaries"`

			SideData []struct {
				Type      string `json:"side_data_type"`
				DVProfile int    `json:"dv_profile"`
			} `json:"side_data_list"`

			Disposition struct {
				Default int `json:"default"`
				Forced  int `json:"forced"`
			} `json:"disposition"`
			Tags struct {
				Language string `json:"language"`
				Title    string `json:"title"`
			} `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return MediaInfo{}, fmt.Errorf("parse ffprobe output: %w", err)
	}

	// A file with no stated duration is reported as zero, as before: the
	// planner already treats that as "nothing to plan across".
	duration, _ := parseSeconds(raw.Format.Duration)

	info := MediaInfo{
		FormatName: raw.Format.FormatName,
		Duration:   duration,
		Size:       parseInt(raw.Format.Size),
		BitRate:    parseInt(raw.Format.BitRate),
	}

	haveVideo := false
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			// The first video stream is the feature; later ones are cover art
			// or thumbnails, which are not what anyone wants a frame from.
			if haveVideo {
				continue
			}
			haveVideo = true
			info.Video = VideoStream{
				Index:   s.Index,
				Codec:   s.CodecName,
				Profile: s.Profile,
				Width:   s.Width,
				Height:  s.Height,
				FPS:     parseRational(s.AvgFrameRate),
				BitRate: parseInt(s.BitRate),

				ColorTransfer:  s.ColorTransfer,
				ColorPrimaries: s.ColorPrimaries,
				ColorSpace:     s.ColorSpace,
				ColorRange:     s.ColorRange,
			}
			for _, sd := range s.SideData {
				if strings.EqualFold(sd.Type, dolbyVisionSideData) {
					info.Video.DolbyVisionProfile = sd.DVProfile
					break
				}
			}
		case "audio":
			info.Audio = append(info.Audio, AudioStream{
				Index:    s.Index,
				Codec:    s.CodecName,
				Language: s.Tags.Language,
				Title:    s.Tags.Title,
				Channels: s.Channels,
				BitRate:  parseInt(s.BitRate),
				Default:  s.Disposition.Default == 1,
			})
		case "subtitle":
			info.Subtitles = append(info.Subtitles, SubtitleStream{
				Index:    s.Index,
				Codec:    s.CodecName,
				Language: s.Tags.Language,
				Title:    s.Tags.Title,
				Forced:   s.Disposition.Forced == 1,
				Default:  s.Disposition.Default == 1,
			})
		}
	}

	if !haveVideo {
		return info, &NoIndexError{Reason: ReasonNoVideoStream}
	}
	if info.Duration <= 0 {
		// Without a duration there is nothing to spread capture points over.
		return info, &NoIndexError{Reason: ReasonNoDuration}
	}
	// A container with no video bitrate of its own still has the format's.
	if info.Video.BitRate == 0 {
		info.Video.BitRate = info.BitRate
	}
	return info, nil
}

// packetRow is one row of the packet=pts_time,dts_time,pos,flags query that
// KeyframeAt and SequentialKeyframes both read - the fields either needs to
// place a keyframe in time and in bytes.
type packetRow struct {
	PTSTime string `json:"pts_time"`
	DTSTime string `json:"dts_time"`
	Pos     string `json:"pos"`
	Flags   string `json:"flags"`
}

// readPackets runs the packet query shared by KeyframeAt and
// SequentialKeyframes, for the given -read_intervals spec.
func (p *Prober) readPackets(ctx context.Context, url, intervals string) ([]packetRow, error) {
	args := append(p.limits(),
		"-select_streams", "v:0",
		// AVI often carries no PTS on a packet, only DTS. Asking for both
		// is the difference between a real timestamp and none at all.
		"-show_entries", "packet=pts_time,dts_time,pos,flags",
		"-read_intervals", intervals,
		"-print_format", "json",
		url,
	)

	out, err := p.tools.Run(ctx, "ffprobe", args...)
	if err != nil {
		return nil, fmt.Errorf("read packets: %w", err)
	}

	var raw struct {
		Packets []packetRow `json:"packets"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse ffprobe packets: %w", err)
	}
	return raw.Packets, nil
}

// KeyframeAt finds the keyframe at or before a timestamp, and where it sits in
// the file.
//
// ffprobe seeks backwards to the preceding keyframe, which is exactly the
// semantics needed: decoding can start there and reach the wanted moment.
func (p *Prober) KeyframeAt(ctx context.Context, url string, at time.Duration) (Keyframe, error) {
	if at < 0 {
		at = 0
	}

	// A handful of packets is enough to find the keyframe the seek landed
	// on, without asking ffprobe to walk the file.
	packets, err := p.readPackets(ctx, url, fmt.Sprintf("%.3f%%+#8", at.Seconds()))
	if err != nil {
		return Keyframe{}, fmt.Errorf("keyframe at %s: %w", at, err)
	}

	for _, pkt := range packets {
		if !strings.HasPrefix(pkt.Flags, "K") {
			continue
		}
		pos := parseInt(pkt.Pos)
		if pos < 0 {
			// Some containers report no position; without one there is
			// nothing to map onto pieces.
			continue
		}

		// A keyframe is not reordered, so its DTS is its PTS - which makes
		// DTS a correct substitute where the container states only that one,
		// as AVI does for H.264. Without either, this packet cannot be placed
		// in time at all, and saying "zero" would send the decoder to the
		// start of the file with every capture point.
		pts, ok := parseSeconds(pkt.PTSTime)
		if !ok {
			if pts, ok = parseSeconds(pkt.DTSTime); !ok {
				return Keyframe{}, &NoIndexError{
					Reason: ReasonKeyframeTimestamp,
					Detail: fmt.Sprintf("keyframe at byte %d near %s carries no timestamp", pos, at),
				}
			}
		}

		return Keyframe{PTS: pts, BytePos: pos}, nil
	}

	return Keyframe{}, &NoIndexError{
		Reason: ReasonKeyframePosition,
		Detail: fmt.Sprintf("no keyframe with a byte position near %s", at),
	}
}

// sequentialWindow is how many packets SequentialKeyframes reads on its
// first attempt; sequentialRetries is how many times that window widens,
// fourfold, when it did not hold enough keyframes. A GOP can be anywhere
// from a handful of frames to several hundred - exactly the thing not known
// without an index - so this converges on either end rather than assuming a
// cadence.
const (
	sequentialWindow  = 200
	sequentialRetries = 4
)

// SequentialKeyframes lists up to count keyframes from the start of the
// file, in presentation order - the index-free way to place capture points
// when a container states no duration (REQUIREMENTS.md 2.7).
//
// It shares KeyframeAt's packet query, but not its all-or-nothing per-packet
// decision: a packet lacking a timestamp or position here is simply not a
// candidate, and the scan keeps going, because the goal is to gather as many
// usable keyframes as the window holds rather than to answer for one
// specific moment.
func (p *Prober) SequentialKeyframes(ctx context.Context, url string, count int) ([]Keyframe, error) {
	if count < 1 {
		count = 1
	}

	window := sequentialWindow
	for attempt := 0; ; attempt++ {
		packets, err := p.readPackets(ctx, url, fmt.Sprintf("%%+#%d", window))
		if err != nil {
			return nil, fmt.Errorf("sequential keyframes: %w", err)
		}

		var out []Keyframe
		for _, pkt := range packets {
			if !strings.HasPrefix(pkt.Flags, "K") {
				continue
			}
			pos := parseInt(pkt.Pos)
			if pos < 0 {
				continue
			}
			pts, ok := parseSeconds(pkt.PTSTime)
			if !ok {
				if pts, ok = parseSeconds(pkt.DTSTime); !ok {
					continue
				}
			}
			out = append(out, Keyframe{PTS: pts, BytePos: pos})
			if len(out) == count {
				return out, nil
			}
		}

		// Fewer packets came back than were asked for: the file ran out
		// before the window did, so reading a wider window would not find
		// anything more.
		if len(packets) < window || attempt >= sequentialRetries {
			if len(out) == 0 {
				return nil, &NoIndexError{
					Reason: ReasonKeyframePosition,
					Detail: fmt.Sprintf("no keyframe found in the first %d packets", len(packets)),
				}
			}
			return out, nil
		}
		window *= 4
	}
}

// limits are the arguments every invocation shares.
func (p *Prober) limits() []string {
	args := []string{"-v", "error"}
	if p.ProbeSize > 0 {
		args = append(args, "-probesize", strconv.FormatInt(p.ProbeSize, 10))
	}
	if p.AnalyzeDuration > 0 {
		// ffprobe counts this in microseconds.
		args = append(args, "-analyzeduration", strconv.FormatInt(p.AnalyzeDuration.Microseconds(), 10))
	}
	return args
}

// parseSeconds reads one of ffprobe's second-valued fields. The second result
// is false when the field was absent or unreadable, which callers must not
// confuse with a genuine zero.
func parseSeconds(s string) (time.Duration, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return time.Duration(f * float64(time.Second)), true
}

func parseInt(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// parseRational reads ffprobe's "25/1" frame rates. A zero denominator means
// the format did not state one.
func parseRational(s string) float64 {
	num, den, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return 0
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}
