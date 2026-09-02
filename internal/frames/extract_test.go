package frames

import (
	"bytes"
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/ffmpeg"
)

func locateTools(t *testing.T) ffmpeg.Tools {
	t.Helper()

	tools, err := ffmpeg.LocateIn()
	if err != nil {
		t.Skipf("no ffmpeg available: %v", err)
	}
	return tools
}

// sampleVideo renders a 60s clip with keyframes every two seconds.
func sampleVideo(t *testing.T, tools ffmpeg.Tools) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sample.mkv")

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration=60",
		"-c:v", "libx264", "-g", "50", "-pix_fmt", "yuv420p",
		path,
	); err != nil {
		t.Fatalf("render sample video: %v", err)
	}
	return path
}

func TestFrameAtSourceResolution(t *testing.T) {
	tools := locateTools(t)
	path := sampleVideo(t, tools)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	frame, err := NewExtractor(tools).Frame(ctx, path, 30*time.Second)
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}

	if frame.Width != 640 || frame.Height != 360 {
		t.Errorf("frame is %dx%d, want the source's 640x360", frame.Width, frame.Height)
	}
	if len(frame.Data) == 0 {
		t.Fatal("frame carries no data")
	}

	// The bytes must be a real image, not whatever ffmpeg printed.
	if _, format, err := image.DecodeConfig(bytes.NewReader(frame.Data)); err != nil {
		t.Errorf("frame data is not decodable: %v", err)
	} else if format != "jpeg" {
		t.Errorf("format = %q, want jpeg", format)
	}
	t.Logf("frame at 30s: %dx%d, %d KiB", frame.Width, frame.Height, len(frame.Data)/1024)
}

func TestFramePNG(t *testing.T) {
	tools := locateTools(t)
	path := sampleVideo(t, tools)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	e := NewExtractor(tools)
	e.Format = PNG

	frame, err := e.Frame(ctx, path, 10*time.Second)
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(frame.Data)); err != nil {
		t.Errorf("frame data is not decodable: %v", err)
	} else if format != "png" {
		t.Errorf("format = %q, want png", format)
	}
}

// TestFramesDifferAcrossTimestamps guards against the failure that would be
// invisible otherwise: twenty identical frames still look like a working tool.
func TestFramesDifferAcrossTimestamps(t *testing.T) {
	tools := locateTools(t)
	path := sampleVideo(t, tools)
	e := NewExtractor(tools)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	first, err := e.Frame(ctx, path, 5*time.Second)
	if err != nil {
		t.Fatalf("Frame at 5s: %v", err)
	}
	second, err := e.Frame(ctx, path, 40*time.Second)
	if err != nil {
		t.Fatalf("Frame at 40s: %v", err)
	}

	if bytes.Equal(first.Data, second.Data) {
		t.Error("frames at 5s and 40s are byte-identical - the seek is not moving")
	}
}

func TestFrameFailsOnNonMedia(t *testing.T) {
	tools := locateTools(t)

	path := filepath.Join(t.TempDir(), "broken.mkv")
	if err := os.WriteFile(path, []byte("not a container"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	e := NewExtractor(tools)
	e.Retries = 0 // no point widening a window over a file that is not media

	if _, err := e.Frame(ctx, path, 5*time.Second); err == nil {
		t.Fatal("Frame succeeded on a file that is not media")
	}
}

func TestFrameRespectsContext(t *testing.T) {
	tools := locateTools(t)
	path := sampleVideo(t, tools)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead

	if _, err := NewExtractor(tools).Frame(ctx, path, 5*time.Second); err == nil {
		t.Error("Frame returned success for a cancelled context")
	}
}
