package web

import (
	"errors"
	"fmt"
	"testing"
)

// TestMaxActiveTorrentsDefaultsToTheShippedWidth proves
// DefaultMaxActiveTorrents applies even to a Config{} nobody touched, and
// pins what that default IS - which is a product decision (5, TOR-149), not
// an implementation detail. Several of this package's own tests are about
// queueing and therefore set the width to 1 themselves rather than leaning
// on whatever this happens to be; that is deliberate, so that changing the
// default again breaks this test alone and not thirty others.
func TestMaxActiveTorrentsDefaultsToTheShippedWidth(t *testing.T) {
	runs := newFakeRuns()
	_, ts := newTestServerWithConfig(t, Config{}, runs.runner)

	if DefaultMaxActiveTorrents < 2 {
		t.Fatalf("DefaultMaxActiveTorrents = %d; this test assumes the shipped default is a widened queue",
			DefaultMaxActiveTorrents)
	}

	states := make([]string, 0, DefaultMaxActiveTorrents+1)
	for i := 0; i <= DefaultMaxActiveTorrents; i++ {
		states = append(states, startRun(t, ts.URL,
			fmt.Sprintf("magnet:?xt=urn:btih:%040d", i)).state)
	}

	for i := 0; i < DefaultMaxActiveTorrents; i++ {
		if states[i] != "running" {
			t.Errorf("run %d of %d is %q, want running - a bare Config{} must take the shipped width",
				i+1, DefaultMaxActiveTorrents, states[i])
		}
	}
	if last := states[DefaultMaxActiveTorrents]; last != "queued" {
		t.Errorf("the run past the width is %q, want queued", last)
	}
}

// TestQueueWidensUpToTheCap is TOR-130's central acceptance criterion: with
// the width raised past 1, that many torrents fetch at once, and only the
// one past the cap waits - proving the queue actually widened, not just that
// its cap field exists.
func TestQueueWidensUpToTheCap(t *testing.T) {
	runs := newFakeRuns()
	_, ts := newTestServerWithConfig(t, Config{MaxActiveTorrents: 2}, runs.runner)

	first := startRun(t, ts.URL, "magnet:?xt=urn:btih:aaa")
	second := startRun(t, ts.URL, "magnet:?xt=urn:btih:bbb")
	third := startRun(t, ts.URL, "magnet:?xt=urn:btih:ccc")

	if first.state != "running" {
		t.Errorf("the first run is %q, want running", first.state)
	}
	if second.state != "running" {
		t.Errorf("the second run is %q, want running - the cap is 2", second.state)
	}
	if third.state != "queued" {
		t.Errorf("the third run is %q, want queued - the cap is 2", third.state)
	}
	if got := runs.count(); got != 2 {
		t.Errorf("the runner was called %d times, want 2 - a cap of 2 must let exactly two through", got)
	}

	// The queued one starts on its own once one of the first two ends,
	// exactly as the single-slot queue always let the next waiter in.
	runs.finish(t, "magnet:?xt=urn:btih:aaa")
	waitFor(t, func() bool { return runs.started("magnet:?xt=urn:btih:ccc") })
}

// TestLoweringTheCapStopsNobody is the ticket's other central requirement:
// SetMaxActiveTorrents(n) below the number of torrents already running must
// not cancel any of them, and a queued run must wait until the count drains
// under the new, lower width on its own - never for the running ones to be
// stopped for it.
//
// Each step calls srv.dispatch() itself after confirming (via the just-freed
// entry's own state, which releaseSlotLocked updates in the same locked
// section) that a slot was actually released, rather than sleeping and
// hoping pump's own asynchronous dispatch() call has already run by then -
// dispatch is idempotent, so re-invoking it here only makes the check
// deterministic; it does not change what is being tested.
func TestLoweringTheCapStopsNobody(t *testing.T) {
	const (
		sourceA = "magnet:?xt=urn:btih:aaa"
		sourceB = "magnet:?xt=urn:btih:bbb"
		sourceC = "magnet:?xt=urn:btih:ccc"
		sourceD = "magnet:?xt=urn:btih:ddd"
	)

	runs := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, Config{MaxActiveTorrents: 3}, runs.runner)

	a := startRun(t, ts.URL, sourceA)
	b := startRun(t, ts.URL, sourceB)
	c := startRun(t, ts.URL, sourceC)
	d := startRun(t, ts.URL, sourceD)

	for _, r := range []startedRun{a, b, c} {
		if r.state != "running" {
			t.Fatalf("run %s is %q, want running - the cap is 3", r.id, r.state)
		}
	}
	if d.state != "queued" {
		t.Fatalf("the fourth run is %q, want queued - the cap is 3 and three are already running", d.state)
	}

	if err := srv.SetMaxActiveTorrents(1); err != nil {
		t.Fatalf("SetMaxActiveTorrents(1): %v", err)
	}

	// Lowering the cap must not have touched anything already going: still
	// three running, none cancelled, the fourth still waiting.
	for _, r := range []startedRun{a, b, c} {
		if got := runInfo(t, srv, r.id).State; got != RunRunning {
			t.Errorf("run %s is %s after lowering the cap, want %s - lowering must stop nobody", r.id, got, RunRunning)
		}
	}
	select {
	case <-runs.context(t, sourceA).Done():
		t.Error("run A's context was cancelled by lowering the cap - lowering must stop nobody already running")
	default:
	}
	if got := runInfo(t, srv, d.id).State; got != RunQueued {
		t.Errorf("the fourth run is %s right after lowering the cap, want %s", got, RunQueued)
	}

	// A finishes: two are still running (B, C), which is still >= the new
	// cap of 1, so the fourth must keep waiting.
	runs.finish(t, sourceA)
	waitFor(t, func() bool { return runInfo(t, srv, a.id).State == RunDone })
	srv.dispatch()
	if got := runInfo(t, srv, d.id).State; got != RunQueued {
		t.Errorf("the fourth run is %s after one of three finished (cap 1), want still %s", got, RunQueued)
	}
	if got := runs.count(); got != 3 {
		t.Errorf("the runner was called %d times, want still 3", got)
	}

	// B finishes: one is still running (C), still >= the new cap of 1, so
	// the fourth must still keep waiting.
	runs.finish(t, sourceB)
	waitFor(t, func() bool { return runInfo(t, srv, b.id).State == RunDone })
	srv.dispatch()
	if got := runInfo(t, srv, d.id).State; got != RunQueued {
		t.Errorf("the fourth run is %s after two of three finished (cap 1), want still %s", got, RunQueued)
	}
	if got := runs.count(); got != 3 {
		t.Errorf("the runner was called %d times, want still 3", got)
	}

	// C finishes: nothing is running any more, which is finally under the
	// new cap of 1, so the fourth run starts - draining under the new width
	// on its own, exactly as promised.
	runs.finish(t, sourceC)
	waitFor(t, func() bool { return runInfo(t, srv, c.id).State == RunDone })
	srv.dispatch()
	if got := runInfo(t, srv, d.id).State; got != RunRunning {
		t.Errorf("the fourth run is %s once the count drained under the new cap, want %s", got, RunRunning)
	}
	if got := runs.count(); got != 4 {
		t.Errorf("the runner was called %d times, want 4 once the fourth run finally started", got)
	}
}

// TestRaisingTheCapFillsEveryFreeSlotAtOnce guards dispatch's own loop, not
// just SetMaxActiveTorrents: raising the width from 1 to 3 while two runs
// already sit waiting must start BOTH of them out of the single dispatch()
// call SetMaxActiveTorrents makes, not just one - a dispatch that returns
// after its first successful start, instead of looping back to try for more
// room, would leave one of the two stranded in the queue until some other,
// unrelated event happened to call dispatch() again. Two starting requests
// each calling dispatch() once (as TestQueueWidensUpToTheCap's requests do)
// would not catch this: this test needs several waiters already queued
// BEFORE the one dispatch() call that must fill more than one slot.
func TestRaisingTheCapFillsEveryFreeSlotAtOnce(t *testing.T) {
	runs := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, Config{MaxActiveTorrents: 1}, runs.runner)

	first := startRun(t, ts.URL, "magnet:?xt=urn:btih:aaa")
	second := startRun(t, ts.URL, "magnet:?xt=urn:btih:bbb")
	third := startRun(t, ts.URL, "magnet:?xt=urn:btih:ccc")
	if first.state != "running" {
		t.Fatalf("the first run is %q, want running", first.state)
	}
	if second.state != "queued" || third.state != "queued" {
		t.Fatalf("the second and third runs are %q and %q, want both queued with a cap of 1", second.state, third.state)
	}

	if err := srv.SetMaxActiveTorrents(3); err != nil {
		t.Fatalf("SetMaxActiveTorrents(3): %v", err)
	}

	if got := runInfo(t, srv, second.id).State; got != RunRunning {
		t.Errorf("the second run is %s right after raising the cap to 3, want %s", got, RunRunning)
	}
	if got := runInfo(t, srv, third.id).State; got != RunRunning {
		t.Errorf("the third run is %s right after raising the cap to 3, want %s - one dispatch() call must fill every free slot, not just one", got, RunRunning)
	}
	if got := runs.count(); got != 3 {
		t.Errorf("the runner was called %d times, want 3 - raising the cap must start every waiter room now allows", got)
	}
}

// TestSetMaxActiveTorrentsRejectsNonPositive: a width under 1 is not a
// narrower queue, it is a broken one - this queue always eventually runs an
// accepted request (REQUIREMENTS.md 3.3), and "accept nothing new" is a
// different feature.
func TestSetMaxActiveTorrentsRejectsNonPositive(t *testing.T) {
	srv, _ := newTestServerWithConfig(t, Config{}, (&fakeRun{}).runner)

	for _, n := range []int{0, -1} {
		if err := srv.SetMaxActiveTorrents(n); err == nil {
			t.Errorf("SetMaxActiveTorrents(%d) = nil, want an error", n)
		}
	}
}

// TestCancelRunWithNoIDIsAmbiguousWithMoreThanOneRunning: an empty id used to
// mean "the run in the slot" when there was only ever one to mean. With the
// queue widened, guessing which of several running torrents an empty id
// meant would risk stopping the wrong person's download, so it is refused
// as a bad request instead.
func TestCancelRunWithNoIDIsAmbiguousWithMoreThanOneRunning(t *testing.T) {
	runs := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, Config{MaxActiveTorrents: 2}, runs.runner)

	a := startRun(t, ts.URL, "magnet:?xt=urn:btih:aaa")
	b := startRun(t, ts.URL, "magnet:?xt=urn:btih:bbb")
	if a.state != "running" || b.state != "running" {
		t.Fatalf("both runs should be running with a cap of 2: got %q and %q", a.state, b.state)
	}

	_, err := srv.CancelRun("")
	if err == nil {
		t.Fatal("CancelRun(\"\") with two running = nil error, want one naming the ambiguity")
	}
	if !errors.Is(err, errBadRequest) {
		t.Errorf("CancelRun(\"\") with two running = %v, want an errBadRequest", err)
	}

	// Neither run was touched by the refused, ambiguous request.
	if got := runInfo(t, srv, a.id).State; got != RunRunning {
		t.Errorf("run A is %s after an ambiguous cancel, want still %s", got, RunRunning)
	}
	if got := runInfo(t, srv, b.id).State; got != RunRunning {
		t.Errorf("run B is %s after an ambiguous cancel, want still %s", got, RunRunning)
	}
}
