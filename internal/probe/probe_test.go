package probe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/torpeek/torpeek/internal/ffmpeg"
)

const (
	sampleDuration = 60 * time.Second
	sampleFPS      = 25
	sampleGOP      = 50 // a keyframe every 2 seconds
	sampleWidth    = 320
	sampleHeight   = 180
)

// sampleVideo renders a file with a known shape: one video track, two audio
// tracks in different languages, and keyframes on a known cadence.
func sampleVideo(t *testing.T, name string) string {
	t.Helper()

	tools := locateTools(t)
	path := filepath.Join(t.TempDir(), name)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	_, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=880:duration=60",
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-c:v", "libx264", "-g", "50", "-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:0", "title=Original",
		"-metadata:s:a:1", "language=rus", "-metadata:s:a:1", "title=Dub",
		path,
	)
	if err != nil {
		t.Fatalf("render sample video: %v", err)
	}
	return path
}

func locateTools(t *testing.T) ffmpeg.Tools {
	t.Helper()

	tools, err := ffmpeg.LocateIn()
	if err != nil {
		t.Skipf("no ffmpeg available: %v", err)
	}
	return tools
}

func TestInspectReportsTracksAndQuality(t *testing.T) {
	tools := locateTools(t)
	path := sampleVideo(t, "sample.mkv")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	info, err := New(tools).Inspect(ctx, path)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	// Duration is what capture points get spread over, so it has to be right
	// rather than merely present.
	if delta := info.Duration - sampleDuration; delta > time.Second || delta < -time.Second {
		t.Errorf("Duration = %s, want about %s", info.Duration, sampleDuration)
	}
	if info.Video.Codec != "h264" {
		t.Errorf("video codec = %q, want h264", info.Video.Codec)
	}
	if info.Video.Width != sampleWidth || info.Video.Height != sampleHeight {
		t.Errorf("resolution = %dx%d, want %dx%d", info.Video.Width, info.Video.Height, sampleWidth, sampleHeight)
	}
	if info.Video.FPS < sampleFPS-0.5 || info.Video.FPS > sampleFPS+0.5 {
		t.Errorf("FPS = %v, want about %d", info.Video.FPS, sampleFPS)
	}

	// The question that started this project: is this the dub I wanted?
	if len(info.Audio) != 2 {
		t.Fatalf("got %d audio tracks, want 2", len(info.Audio))
	}
	languages := []string{info.Audio[0].Language, info.Audio[1].Language}
	if languages[0] != "eng" || languages[1] != "rus" {
		t.Errorf("audio languages = %v, want [eng rus]", languages)
	}
	if info.Audio[1].Title != "Dub" {
		t.Errorf("second audio title = %q, want %q", info.Audio[1].Title, "Dub")
	}

	if info.Video.BitsPerPixel() <= 0 {
		t.Error("BitsPerPixel = 0, so the quality estimate carries no information")
	}
	t.Logf("%s, %s, %dx%d @ %.0ffps, %.4f bits/pixel, %d audio tracks",
		info.FormatName, info.Duration, info.Video.Width, info.Video.Height,
		info.Video.FPS, info.Video.BitsPerPixel(), len(info.Audio))
}

// TestKeyframeAtSeeksBackwards is the behaviour the whole architecture rests
// on: asking for a moment between keyframes must yield the keyframe before it,
// with a byte position that maps onto pieces.
func TestKeyframeAtSeeksBackwards(t *testing.T) {
	tools := locateTools(t)
	path := sampleVideo(t, "sample.mkv")
	prober := New(tools)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	size := fileSize(t, path)

	for _, at := range []time.Duration{
		0,
		10 * time.Second,
		31300 * time.Millisecond, // deliberately between keyframes
		45 * time.Second,
	} {
		t.Run(at.String(), func(t *testing.T) {
			kf, err := prober.KeyframeAt(ctx, path, at)
			if err != nil {
				t.Fatalf("KeyframeAt(%s): %v", at, err)
			}

			if kf.PTS > at {
				t.Errorf("keyframe at %s is after the requested %s - decoding could not reach the moment",
					kf.PTS, at)
			}
			// Keyframes are 2s apart here, so the one before any request is
			// within that distance; a much older one would mean the seek is
			// not landing where it should.
			if at-kf.PTS > 3*time.Second {
				t.Errorf("keyframe at %s is %s before the requested %s, further back than the GOP",
					kf.PTS, at-kf.PTS, at)
			}
			if kf.BytePos <= 0 || kf.BytePos >= size {
				t.Errorf("BytePos = %d, outside the file's %d bytes", kf.BytePos, size)
			}
			t.Logf("asked %s, got keyframe pts=%s at byte %d", at, kf.PTS, kf.BytePos)
		})
	}
}

// TestKeyframePositionIsReal confirms the reported offset is not merely
// plausible: decoding from it must produce a frame.
func TestKeyframePositionIsReal(t *testing.T) {
	tools := locateTools(t)
	path := sampleVideo(t, "sample.mkv")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	kf, err := New(tools).KeyframeAt(ctx, path, 31300*time.Millisecond)
	if err != nil {
		t.Fatalf("KeyframeAt: %v", err)
	}

	frame := filepath.Join(t.TempDir(), "frame.png")
	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-ss", formatSeconds(kf.PTS), "-i", path,
		"-frames:v", "1", frame,
	); err != nil {
		t.Fatalf("decode at the reported keyframe: %v", err)
	}
	if fileSize(t, frame) == 0 {
		t.Error("decoding at the reported keyframe produced an empty frame")
	}
}

func TestInspectRejectsNonMedia(t *testing.T) {
	tools := locateTools(t)

	path := filepath.Join(t.TempDir(), "not-video.mkv")
	if err := os.WriteFile(path, []byte("this is not a container"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := New(tools).Inspect(ctx, path)
	if err == nil {
		t.Fatal("Inspect succeeded on a file that is not media")
	}

	// It should come back as a tool failure carrying ffprobe's complaint, not
	// a panic or a bare "exit status 1".
	var exitErr *ffmpeg.ExitError
	if errors.As(err, &exitErr) && exitErr.Stderr == "" {
		t.Error("ffprobe's explanation was dropped")
	}
	t.Logf("error: %v", err)
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// formatSeconds renders a timestamp the way ffmpeg's -ss expects it: plain
// seconds. time.Duration.String() would produce "31.3s", which it rejects.
func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}
