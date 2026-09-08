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
// It asserts on the exact source events.js ships - the "done" handler is
// that module's since TOR-191, because branching on a message is what the
// event layer is for. TestEveryEventTypeTheServerCanPublishIsHandled
// (eventstate_test.go) drives a real done event with reason "time" through
// it and reads the line back out of the log, which is the executing half of
// this same subject.
func TestDoneReasonSentencesTellTimeApartFromTraffic(t *testing.T) {
	src := eventsJS(t)

	if !strings.Contains(src, `ev.reason === "time"`) {
		t.Fatal(`events.js's "done" handler never branches on ev.reason === "time" - ` +
			`a run core reports as core.StopTime falls through unnamed`)
	}

	const (
		trafficSentence = "stopped at this run's own traffic limit"
		timeSentence    = "stopped at this run's own time limit"
	)
	if !strings.Contains(src, trafficSentence) {
		t.Errorf("events.js never states %q for a budget stop", trafficSentence)
	}
	if !strings.Contains(src, timeSentence) {
		t.Errorf("events.js never states %q for a time stop", timeSentence)
	}
	if trafficSentence == timeSentence {
		t.Fatal("the traffic and time sentences are identical - this assertion is broken, not the page")
	}
}
