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

// TestDoneReasonTextTellsTimeApartFromTraffic is TOR-161's CLI-side unit
// test: core.StopBudget and core.StopTime used to be one reason
// (core.StopBudget covered both the run's own traffic ceiling and its own
// clock), and report.go already told StopBudget apart from StopRoof, so this
// is the same treatment extended to the third reason.
//
// Both arms run through reportText with nothing else different about the
// Done event, so the only thing that can make the two outputs differ is the
// switch in reportText itself - a test that only checked one reason's
// wording could pass even if both cases had collapsed back onto the same
// sentence.
func TestDoneReasonTextTellsTimeApartFromTraffic(t *testing.T) {
	cases := []struct {
		reason core.StopReason
		want   string
		bad    []string
	}{
		{
			reason: core.StopBudget,
			want:   "stopped at this run's own traffic limit",
			bad:    []string{"time limit"},
		},
		{
			reason: core.StopTime,
			want:   "stopped at this run's own time limit",
			bad:    []string{"traffic limit"},
		},
	}

	for _, tc := range cases {
		events := make(chan core.Event, 1)
		events <- core.Done{Reason: tc.reason, Files: 1, Frames: 4}
		close(events)

		var stdout, stderr bytes.Buffer
		code := reportText(events, &stdout, &stderr, 4)

		if code != ExitPartial {
			t.Errorf("reason %q: exit code = %d, want %d", tc.reason, code, ExitPartial)
		}
		if !strings.Contains(stderr.String(), tc.want) {
			t.Errorf("reason %q: stderr = %q, want it to contain %q", tc.reason, stderr.String(), tc.want)
		}
		for _, bad := range tc.bad {
			if strings.Contains(stderr.String(), bad) {
				t.Errorf("reason %q: stderr = %q, must not contain %q - "+
					"a traffic stop and a time stop must not share wording",
					tc.reason, stderr.String(), bad)
			}
		}
	}
}

// TestDoneReasonJSONExitsPartialForTime is the NDJSON counterpart: a caller
// reading only the exit code, not the event stream, must still be able to
// tell "stopped early but kept results" from success for a run the CLOCK
// stopped, exactly as it already can for StopBudget and StopRoof.
func TestDoneReasonJSONExitsPartialForTime(t *testing.T) {
	events := make(chan core.Event, 1)
	events <- core.Done{Reason: core.StopTime, Files: 1, Frames: 4}
	close(events)

	var stdout, stderr bytes.Buffer
	code := reportJSON(events, &stdout, &stderr)

	if code != ExitPartial {
		t.Errorf("exit code = %d, want %d for a run stopped by its own clock", code, ExitPartial)
	}
}
