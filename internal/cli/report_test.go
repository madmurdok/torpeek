package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/swarm"
)

// TestTheCostIsReportedBeforeTheFirstFrame is TOR-50's acceptance criterion at
// the one level where it can be asserted: -n is frames PER video file, so a
// torrent bundling quality variants costs a multiple of what the flag looks
// like, and the only useful moment to say so is before any of it is spent.
//
// The ordering assertion is the test, not the wording one. A cost line printed
// after the frames have landed would still contain the right number and would
// still be worthless.
func TestTheCostIsReportedBeforeTheFirstFrame(t *testing.T) {
	events := make(chan core.Event, 3)
	events <- core.MetadataReady{
		Name: "bundle",
		Videos: []swarm.FileInfo{
			{Index: 0, Path: "1080p.mkv"},
			{Index: 1, Path: "720p.mkv"},
			{Index: 2, Path: "480p.mkv"},
		},
		Selected: []int{0, 2},
	}
	events <- core.FrameReady{File: 0, Index: 0, Actual: 12 * time.Second}
	events <- core.Done{Files: 2, Frames: 12}
	close(events)

	var stdout, stderr bytes.Buffer
	if code := reportText(events, &stdout, &stderr, 6); code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	text := stdout.String()
	t.Logf("stdout:\n%s", text)

	const want = "2 file(s) x 6 = 12 frames to fetch"
	cost := strings.Index(text, want)
	if cost < 0 {
		t.Fatalf("output never states the cost %q:\n%s", want, text)
	}
	frame := strings.Index(text, "frame 00")
	if frame < 0 {
		t.Fatalf("output never announces the frame, so the ordering cannot be judged:\n%s", text)
	}
	if cost > frame {
		t.Errorf("the cost is reported at byte %d, after the first frame at %d - "+
			"a cost a person reads once the traffic is spent is not a cost report:\n%s", cost, frame, text)
	}
}

// TestTheCostCountsSelectedFilesNotEveryFile keeps the reported number the one
// this run will actually pay: -file narrows the selection, and quoting the
// torrent's whole file count would overstate the cost of every narrowed run.
func TestTheCostCountsSelectedFilesNotEveryFile(t *testing.T) {
	events := make(chan core.Event, 2)
	events <- core.MetadataReady{
		Name: "bundle",
		Videos: []swarm.FileInfo{
			{Index: 0, Path: "1080p.mkv"},
			{Index: 1, Path: "720p.mkv"},
			{Index: 2, Path: "480p.mkv"},
		},
		Selected: []int{1},
	}
	events <- core.Done{Files: 1, Frames: 4}
	close(events)

	var stdout, stderr bytes.Buffer
	reportText(events, &stdout, &stderr, 4)
	text := stdout.String()
	t.Logf("stdout:\n%s", text)

	if want := "1 file(s) x 4 = 4 frames to fetch"; !strings.Contains(text, want) {
		t.Errorf("output does not state %q:\n%s", want, text)
	}
	if bad := "3 file(s)"; strings.Contains(text, bad+" x") {
		t.Errorf("the cost line counts every video file, not the %d selected:\n%s", 1, text)
	}
}
