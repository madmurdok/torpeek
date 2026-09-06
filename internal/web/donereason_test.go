package web

import (
	"strings"
	"testing"
)

// TestDoneReasonSentencesTellTimeApartFromTraffic guards app.js's "done"
// handler (TOR-161): before this change the handler only branched on
// "traffic_roof" and "budget", so a run core now reports as "time" fell
// through both branches and the log line named nothing at all beyond the
// generic "done: time, ..." summary line every reason already gets. This
// follows the precedent columns_test.go documents at the top of this
// package's own test files: there is no JS test runner here, so these
// assert on the exact source app.js ships (the same copy TestServesEmbeddedFrontend
// reads for other strings), which is a narrower guard than executing the
// code but a real one.
func TestDoneReasonSentencesTellTimeApartFromTraffic(t *testing.T) {
	src := appJS(t)

	if !strings.Contains(src, `ev.reason === "time"`) {
		t.Fatal(`app.js's "done" handler never branches on ev.reason === "time" - ` +
			`a run core reports as core.StopTime falls through unnamed`)
	}

	const (
		trafficSentence = "stopped at this run's own traffic limit"
		timeSentence    = "stopped at this run's own time limit"
	)
	if !strings.Contains(src, trafficSentence) {
		t.Errorf("app.js never states %q for a budget stop", trafficSentence)
	}
	if !strings.Contains(src, timeSentence) {
		t.Errorf("app.js never states %q for a time stop", timeSentence)
	}
	if trafficSentence == timeSentence {
		t.Fatal("the traffic and time sentences are identical - this assertion is broken, not the page")
	}
}
