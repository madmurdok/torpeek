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
// This is the same "reach into the served source" guard
// TestTheServedPageOffersToppingUpAndRetrying (again_test.go) uses. It
// catches the fix being reverted or never wired into the events that carry a
// landed frame; it CANNOT prove the bar actually renders correctly - that is
// the browser pass TOR-167's own notes require.
//
// SINCE TOR-191 IT READS events.js: applyFrameProgress folds a message's own
// count into what the grid has already proven landed, which is a state change
// driven by a message, so it moved with the handlers that call it. The row's
// two fields it writes (entry.framesDone/framesTotal) are declared in
// state.js's newRunState, and renderRunProgress in app.js is what draws them.
func TestTopUpProgressReadsLandedFramesNotJustTheHeartbeat(t *testing.T) {
	js, err := embedded.ReadFile("assets/events.js")
	if err != nil {
		t.Fatalf("reading the embedded event layer: %v", err)
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
			t.Errorf("the served events.js never mentions %q", want)
		}
	}

	// The old bug's exact shape: the heartbeat's own frames_done assigned
	// straight onto entry.framesDone, with nothing else able to raise it
	// once a replay landed frames the heartbeat never mentioned.
	if strings.Contains(page, "entry.framesDone = ev.frames_done") {
		t.Error("the served events.js still sets entry.framesDone straight from the heartbeat alone; " +
			"a top-up's replayed frames need a way to raise it too (TOR-167)")
	}

	// And the row's bar reads those two fields rather than a message. The bar
	// lives in the row's status cell, so since TOR-194 it is the table
	// element's - a method now, hence jsMethod rather than jsFunc.
	if bar := jsMethod(t, runTableJS(t), "renderRunProgress"); !strings.Contains(bar, "entry.framesTotal") ||
		!strings.Contains(bar, "entry.framesDone") {
		t.Error("run-table.js's renderRunProgress no longer draws from entry.framesDone/framesTotal - " +
			"the bar and the line above it would be two readings of one thing again")
	}
}
