package frames

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/ffmpeg"
)

// blankFixture renders a one-file clip with the given lavfi source and reads
// back one frame the same way the extractor produces frames in production:
// through ffmpeg's mjpeg encoder, not a raw lavfi still. That is what makes
// the numbers in blank.go's comment trustworthy - they describe what the
// real pipeline hands to IsBlank, not an idealized source frame.
func blankFixture(t *testing.T, tools ffmpeg.Tools, source string) []byte {
	t.Helper()

	path := filepath.Join(t.TempDir(), "clip.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", source,
		"-c:v", "libx264", "-g", "25", "-pix_fmt", "yuv420p",
		path,
	); err != nil {
		t.Fatalf("render fixture: %v", err)
	}

	frame, err := NewExtractor(tools).Frame(ctx, path, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("extract frame: %v", err)
	}
	return frame.Data
}

func TestIsBlankOnABlackFrame(t *testing.T) {
	tools := locateTools(t)
	data := blankFixture(t, tools, "color=black:s=640x360:d=1:r=25")

	blank, err := IsBlank(data)
	if err != nil {
		t.Fatalf("IsBlank: %v", err)
	}
	if !blank {
		t.Error("a pure black frame was not detected as blank")
	}
}

func TestIsBlankOnAFlatGreyFrame(t *testing.T) {
	tools := locateTools(t)
	data := blankFixture(t, tools, "color=c=gray:s=640x360:d=1:r=25")

	blank, err := IsBlank(data)
	if err != nil {
		t.Fatalf("IsBlank: %v", err)
	}
	if !blank {
		t.Error("a flat grey card was not detected as blank")
	}
}

// TestIsBlankOnADarkButRealFrame is the case blank.go's comment warns about:
// a low-mean frame that still carries real picture structure must not be
// caught by the same rule that catches a black frame. Mean alone would flag
// it; spread must not.
func TestIsBlankOnADarkButRealFrame(t *testing.T) {
	tools := locateTools(t)
	data := blankFixture(t, tools, "testsrc=size=640x360:rate=25:duration=1")
	// Darkened in a second pass so the source keeps its gradients and color
	// bars, only dimmer - the shape of a genuinely dark scene, not a flat one.
	path := filepath.Join(t.TempDir(), "in.jpg")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write intermediate frame: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dimmed, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", path,
		"-vf", "eq=brightness=-0.8",
		"-c:v", "mjpeg", "-q:v", "2", "-f", "image2pipe", "-",
	)
	if err != nil {
		t.Fatalf("dim frame: %v", err)
	}

	blank, err := IsBlank(dimmed)
	if err != nil {
		t.Fatalf("IsBlank: %v", err)
	}
	if blank {
		t.Error("a dark but real frame was flagged blank; mean alone must not decide this")
	}
}

func TestIsBlankOnAnOrdinaryFrame(t *testing.T) {
	tools := locateTools(t)
	data := blankFixture(t, tools, "testsrc=size=640x360:rate=25:duration=1")

	blank, err := IsBlank(data)
	if err != nil {
		t.Fatalf("IsBlank: %v", err)
	}
	if blank {
		t.Error("an ordinary testsrc frame was flagged blank")
	}
}
