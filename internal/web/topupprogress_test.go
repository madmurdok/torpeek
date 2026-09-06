package web

import (
	"strings"
	"testing"
)

// TestTopUpProgressReadsLandedFramesNotJustTheHeartbeat guards TOR-167: a
// top-up's row progress bar (and the "N/M frames" line above it) must read a
// file's landed-frame count from the frame_ready/frame_skipped stream - the
// same one the file's own grid is built from - not solely from
// core.Progress's own frames_done.
//
// WHY THAT MATTERS. core.Progress's frames_done counts only frames THIS run
// captured fresh. A top-up mostly REPLAYS frames an earlier run already
// wrote to disk (TOR-166), and engine.go's processFile publishes a
// frame_ready for a reused point but no Progress heartbeat for it - only a
// real capture gets one. So a top-up whose still-missing points fall early
// in the plan can report a heartbeat as low as "1 of 20" and never correct
// itself afterwards: every point past that is a silent replay, while the
// file finishes completely on disk behind a bar that never heard about it.
// The grid beside the bar does not have this gap - frame_ready fires for a
// landed point whether replayed or freshly captured - which is exactly the
// signal the row's own bar/line must also read from.
//
// This is the same "reach into app.js" guard TestTheServedPageOffersToppingUpAndRetrying
// (again_test.go) uses: there is no JS runner in this project, so served
// text is what can be asserted here. It catches the fix being reverted or
// never wired into the events that carry a landed frame; it CANNOT prove the
// bar actually renders correctly - that is the browser pass TOR-167's own
// notes require.
func TestTopUpProgressReadsLandedFramesNotJustTheHeartbeat(t *testing.T) {
	js, err := embedded.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("reading the embedded page: %v", err)
	}
	page := string(js)

	for _, want := range []string{
		// The fuller signal exists as its own function, not inlined once
		// where a future edit could touch one call site and miss the rest.
		"function applyFrameProgress(",
		// Wired into a landed frame's own event, not only the sparser
		// heartbeat - a top-up's replayed frames arrive exactly here.
		"applyFrameProgress(entry, ev.file)",
		// The heartbeat's own count still matters as a FLOOR (Math.max),
		// never as the only source a bare assignment would make it.
		"Math.max(landed, wireDone",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the served app.js never mentions %q", want)
		}
	}

	// The old bug's exact shape: the heartbeat's own frames_done assigned
	// straight onto entry.framesDone, with nothing else able to raise it
	// once a replay landed frames the heartbeat never mentioned.
	if strings.Contains(page, "entry.framesDone = ev.frames_done") {
		t.Error("the served app.js still sets entry.framesDone straight from the heartbeat alone; " +
			"a top-up's replayed frames need a way to raise it too (TOR-167)")
	}
}
