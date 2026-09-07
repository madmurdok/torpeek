package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
)

// TOR-140: a waiting torrent's place in the queue can be changed without
// cancelling it. The pain in the ticket's own words is that the only way to
// reorder was to cancel a download and add it again, losing its place
// entirely.
//
// Every test here drives the queue with at least three waiters behind one
// running torrent, because a queue that is already in the wanted order proves
// nothing: the arrangement each test starts from is asserted BEFORE the
// reorder, so a change that did nothing at all would fail on the second
// assertion rather than sail through the first.

// queueOrder is the ids in the queue, front first - the order dispatch will
// actually take them in. It reads s.waiting directly rather than inferring an
// order from what happens to start next, so a test can assert on the queue
// while nothing is moving.
func queueOrder(srv *Server) []string {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	out := make([]string, 0, len(srv.waiting))
	for _, entry := range srv.waiting {
		out = append(out, entry.id)
	}
	return out
}

// sameOrder reports whether got is exactly want, and is used with named
// sources rather than raw ids so a failure message says which torrent is
// where.
func sameOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// setPriority drives POST /runs/priority the way a page does, and returns the
// raw response so a test can check the status as well as the body.
func setPriority(t *testing.T, base, id string, priority Priority) *http.Response {
	t.Helper()
	body := `{"id":` + strconv.Quote(id) + `,"priority":` + strconv.Itoa(int(priority)) + `}`
	return post(t, base, "/runs/priority", body)
}

// TestRaisingAPriorityChangesWhoGoesNextWithoutCancellingAnything is the
// acceptance criterion, whole: the queue is reordered from outside, nothing is
// cancelled to do it, and the torrent that actually starts next is the one
// priority named rather than the one arrival named.
//
// The arms are demonstrably different before anything is asked for: the queue
// starts B, C, D and the torrent promoted is D, the LAST of them. A no-op
// implementation would leave B in front, and B is what starts next without
// this change.
func TestRaisingAPriorityChangesWhoGoesNextWithoutCancellingAnything(t *testing.T) {
	const (
		running = "magnet:?xt=urn:btih:aaa"
		first   = "magnet:?xt=urn:btih:bbb"
		second  = "magnet:?xt=urn:btih:ccc"
		third   = "magnet:?xt=urn:btih:ddd"
	)

	runs := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, Config{MaxActiveTorrents: 1}, runs.runner)

	a := startRun(t, ts.URL, running)
	b := startRun(t, ts.URL, first)
	c := startRun(t, ts.URL, second)
	d := startRun(t, ts.URL, third)

	if a.state != "running" {
		t.Fatalf("the first run is %q, want running", a.state)
	}
	// The arms-apart check: without a reorder, B is next and D is last.
	if got := queueOrder(srv); !sameOrder(got, []string{b.id, c.id, d.id}) {
		t.Fatalf("the queue starts as %v, want %v (B, C, D in arrival order) - a queue already in the "+
			"wanted order would prove nothing about the reorder below", got, []string{b.id, c.id, d.id})
	}
	if got := runInfo(t, srv, b.id).QueuePosition; got != 1 {
		t.Fatalf("B is at position %d before the reorder, want 1", got)
	}
	if got := runInfo(t, srv, d.id).QueuePosition; got != 3 {
		t.Fatalf("D is at position %d before the reorder, want 3", got)
	}

	resp := setPriority(t, ts.URL, d.id, PriorityHigh)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /runs/priority: status %d, want 200", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if got, _ := body["queue_position"].(float64); got != 1 {
		t.Errorf("the reorder answered queue_position %v, want 1 - the response must report where the "+
			"torrent actually landed", body["queue_position"])
	}

	// D is in front now, and B and C keep their order behind it.
	if got := queueOrder(srv); !sameOrder(got, []string{d.id, b.id, c.id}) {
		t.Fatalf("the queue is %v after raising D, want %v", got, []string{d.id, b.id, c.id})
	}

	// NOTHING WAS CANCELLED to achieve it - the whole point of the ticket.
	// All three are still queued, and the runner has still only ever been
	// called once.
	for name, run := range map[string]startedRun{"B": b, "C": c, "D": d} {
		if got := runInfo(t, srv, run.id).State; got != RunQueued {
			t.Errorf("%s is %s after the reorder, want still %s - reordering must cancel nothing", name, got, RunQueued)
		}
	}
	if got := runs.count(); got != 1 {
		t.Errorf("the runner has been called %d times, want 1 - reordering must not start anything either", got)
	}

	// And the one that starts when the slot frees is the one priority named.
	runs.finish(t, running)
	waitFor(t, func() bool { return runInfo(t, srv, d.id).State == RunRunning })
	if got := runInfo(t, srv, b.id).State; got != RunQueued {
		t.Errorf("B is %s once the slot freed, want still %s - D was promoted past it", got, RunQueued)
	}
	if runs.started(first) {
		t.Error("B was started when the slot freed - the queue was reordered and D should have gone first")
	}
}

// TestPriorityKeepsArrivalOrderWithinALevel is the other half of the ordering
// rule, and the half a "sort by priority" that forgot its tiebreak would get
// wrong: two torrents at the same level run in the order they joined the
// queue, not in the order somebody happened to press the button.
//
// C is raised BEFORE D, and C also arrived before D, so this alone would not
// tell a stable tiebreak from a "most recently raised goes first" one. The
// second half does: B is raised LAST and arrived FIRST, so a most-recent rule
// would put B in front of both, and arrival order puts it behind neither.
func TestPriorityKeepsArrivalOrderWithinALevel(t *testing.T) {
	const (
		running = "magnet:?xt=urn:btih:aaa"
		first   = "magnet:?xt=urn:btih:bbb"
		second  = "magnet:?xt=urn:btih:ccc"
		third   = "magnet:?xt=urn:btih:ddd"
	)

	runs := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, Config{MaxActiveTorrents: 1}, runs.runner)

	startRun(t, ts.URL, running)
	b := startRun(t, ts.URL, first)
	c := startRun(t, ts.URL, second)
	d := startRun(t, ts.URL, third)

	if got := queueOrder(srv); !sameOrder(got, []string{b.id, c.id, d.id}) {
		t.Fatalf("the queue starts as %v, want B, C, D", got)
	}

	// Raise the last two, latest first among the two of them.
	for _, id := range []string{d.id, c.id} {
		if resp := setPriority(t, ts.URL, id, PriorityHigh); resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /runs/priority for %s: status %d, want 200", id, resp.StatusCode)
		}
	}
	// C before D even though D was raised first: within a level the order is
	// the order they joined the queue.
	if got := queueOrder(srv); !sameOrder(got, []string{c.id, d.id, b.id}) {
		t.Fatalf("the queue is %v after raising D then C, want %v - within one level the tiebreak is arrival, "+
			"not which was reprioritised most recently", got, []string{c.id, d.id, b.id})
	}

	// Now raise B, which arrived FIRST. If the tiebreak were "most recently
	// raised", B would now be in front of C and D; arrival order puts it in
	// front of both, because it joined the queue before either.
	if resp := setPriority(t, ts.URL, b.id, PriorityHigh); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /runs/priority for B: status %d, want 200", resp.StatusCode)
	}
	if got := queueOrder(srv); !sameOrder(got, []string{b.id, c.id, d.id}) {
		t.Fatalf("the queue is %v with all three at high, want %v - three torrents at one level run in the "+
			"order they arrived", got, []string{b.id, c.id, d.id})
	}
}

// TestLoweringAPriorityMovesARowBehindEveryNormalOne is the downward
// direction, which a comparison written the wrong way round would get exactly
// backwards while the upward one still passed.
func TestLoweringAPriorityMovesARowBehindEveryNormalOne(t *testing.T) {
	const (
		running = "magnet:?xt=urn:btih:aaa"
		first   = "magnet:?xt=urn:btih:bbb"
		second  = "magnet:?xt=urn:btih:ccc"
		third   = "magnet:?xt=urn:btih:ddd"
	)

	runs := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, Config{MaxActiveTorrents: 1}, runs.runner)

	startRun(t, ts.URL, running)
	b := startRun(t, ts.URL, first)
	c := startRun(t, ts.URL, second)
	d := startRun(t, ts.URL, third)

	if got := queueOrder(srv); !sameOrder(got, []string{b.id, c.id, d.id}) {
		t.Fatalf("the queue starts as %v, want B, C, D", got)
	}

	// B is at the FRONT: lowering it has to move it all the way to the back,
	// which is the largest move available and the one a no-op cannot fake.
	if resp := setPriority(t, ts.URL, b.id, PriorityLow); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /runs/priority: status %d, want 200", resp.StatusCode)
	}
	if got := queueOrder(srv); !sameOrder(got, []string{c.id, d.id, b.id}) {
		t.Fatalf("the queue is %v after lowering B, want %v - a low torrent waits behind every normal one, "+
			"however much later they arrived", got, []string{c.id, d.id, b.id})
	}

	// A torrent arriving AFTER the demotion still goes in front of it: that
	// is what makes this a level rather than a position.
	e := startRun(t, ts.URL, "magnet:?xt=urn:btih:eee")
	if got := queueOrder(srv); !sameOrder(got, []string{c.id, d.id, e.id, b.id}) {
		t.Fatalf("the queue is %v after a new normal torrent arrived, want %v - it belongs in front of the "+
			"low one, not behind it", got, []string{c.id, d.id, e.id, b.id})
	}
}

// TestPriorityCannotPreemptARunningTorrent is the decision this ticket had to
// make and state: priority orders the torrents that are WAITING and never
// interrupts one that is already downloading. See Server.SetRunPriority for
// the argument - the short form is that giving up a slot releases the torrent
// and erases the pieces it has pulled (REQUIREMENTS.md 3.3), so a preemption
// would destroy work and the traffic that bought it, with no state to come
// back from.
//
// It is refused, not silently ignored: a request that looks like it did
// something and did not is worse than an error that explains the rule.
func TestPriorityCannotPreemptARunningTorrent(t *testing.T) {
	const (
		running = "magnet:?xt=urn:btih:aaa"
		waiting = "magnet:?xt=urn:btih:bbb"
	)

	runs := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, Config{MaxActiveTorrents: 1}, runs.runner)

	a := startRun(t, ts.URL, running)
	b := startRun(t, ts.URL, waiting)
	if a.state != "running" || b.state != "queued" {
		t.Fatalf("expected one running and one queued, got %q and %q", a.state, b.state)
	}

	resp := setPriority(t, ts.URL, a.id, PriorityHigh)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST /runs/priority on a running torrent: status %d, want 409", resp.StatusCode)
	}

	// The refusal has to be reported through the same error the method
	// returns, so a caller that is not the page gets the same answer.
	_, err := srv.SetRunPriority(a.id, PriorityHigh)
	if err == nil {
		t.Fatal("SetRunPriority on a running torrent returned no error - priority must not reach a run that is " +
			"already spending traffic")
	}

	// Nothing was stopped, and nothing was started in its place.
	if got := runInfo(t, srv, a.id).State; got != RunRunning {
		t.Errorf("the running torrent is %s after a refused reorder, want still %s", got, RunRunning)
	}
	select {
	case <-runs.context(t, running).Done():
		t.Error("the running torrent's context was cancelled by a priority request - priority must never " +
			"preempt a run, and above all must never do it by cancelling one")
	default:
	}
	if got := runInfo(t, srv, b.id).State; got != RunQueued {
		t.Errorf("the waiting torrent is %s, want still %s", got, RunQueued)
	}
	if got := runs.count(); got != 1 {
		t.Errorf("the runner has been called %d times, want 1", got)
	}
}

// TestPriorityRefusesALevelOutsideTheBand: the band is three levels, and a
// client sending something else has misunderstood the scale. Clamping would
// hide that until the day the band widens and the value starts meaning
// something.
func TestPriorityRefusesALevelOutsideTheBand(t *testing.T) {
	runs := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, Config{MaxActiveTorrents: 1}, runs.runner)

	startRun(t, ts.URL, "magnet:?xt=urn:btih:aaa")
	b := startRun(t, ts.URL, "magnet:?xt=urn:btih:bbb")

	for _, level := range []Priority{2, -2, 99} {
		resp := setPriority(t, ts.URL, b.id, level)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST /runs/priority with %d: status %d, want 400", level, resp.StatusCode)
		}
		if _, err := srv.SetRunPriority(b.id, level); !errors.Is(err, errBadRequest) {
			t.Errorf("SetRunPriority(%d) = %v, want an errBadRequest", level, err)
		}
		if got := runInfo(t, srv, b.id).Priority; got != PriorityNormal {
			t.Errorf("the entry's priority is %d after a refused request, want %d - a refusal must change nothing",
				got, PriorityNormal)
		}
	}
}

// TestPriorityOnARunThisServerDoesNotHoldIs404 keeps the reorder route
// answering the same way every other route addressed by run id does.
func TestPriorityOnARunThisServerDoesNotHoldIs404(t *testing.T) {
	_, ts := newTestServerWithConfig(t, Config{}, newFakeRuns().runner)

	if resp := setPriority(t, ts.URL, "nosuchrun", PriorityHigh); resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /runs/priority for an unknown run: status %d, want 404", resp.StatusCode)
	}
}

// TestGetRunsReportsTheQueuePositionItActuallyHolds is what retires app.js's
// derived rank: the position is a field the server sends, it follows a
// reorder, and it is ABSENT rather than zero for a row the queue has nothing
// to say about.
//
// Before TOR-140 a page could recover the order by ranking queued rows by
// their queued time, because the queue was strict FIFO. This test is the
// proof that it no longer can: after the reorder, the row reported at
// position 1 is the row with the LATEST queued time of the three.
func TestGetRunsReportsTheQueuePositionItActuallyHolds(t *testing.T) {
	const (
		running = "magnet:?xt=urn:btih:aaa"
		first   = "magnet:?xt=urn:btih:bbb"
		second  = "magnet:?xt=urn:btih:ccc"
		third   = "magnet:?xt=urn:btih:ddd"
	)

	runs := newFakeRuns()
	_, ts := newTestServerWithConfig(t, Config{MaxActiveTorrents: 1}, runs.runner)

	a := startRun(t, ts.URL, running)
	b := startRun(t, ts.URL, first)
	c := startRun(t, ts.URL, second)
	d := startRun(t, ts.URL, third)

	rows := runRows(t, ts.URL)
	if got := rows[b.id]["queue_position"]; got != float64(1) {
		t.Fatalf("B is reported at queue_position %v before the reorder, want 1", got)
	}
	if got := rows[d.id]["queue_position"]; got != float64(3) {
		t.Fatalf("D is reported at queue_position %v before the reorder, want 3", got)
	}
	if got, ok := rows[b.id]["priority"]; !ok || got != float64(PriorityNormal) {
		t.Fatalf("B is reported at priority %v (present: %v), want %d - normal is a real answer and must be "+
			"sent, not omitted as a zero", got, ok, PriorityNormal)
	}
	// A running row is not in the queue, so it carries neither field: zero
	// would read as "normal priority, position 0", a standing it does not
	// have (RunSummary.Priority's own doc).
	if _, ok := rows[a.id]["queue_position"]; ok {
		t.Errorf("the running row reports a queue_position (%v) - it is not in the queue", rows[a.id]["queue_position"])
	}
	if _, ok := rows[a.id]["priority"]; ok {
		t.Errorf("the running row reports a priority (%v) - the queue has nothing to say about it", rows[a.id]["priority"])
	}

	if resp := setPriority(t, ts.URL, d.id, PriorityHigh); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /runs/priority: status %d, want 200", resp.StatusCode)
	}

	rows = runRows(t, ts.URL)
	for id, want := range map[string]float64{d.id: 1, b.id: 2, c.id: 3} {
		if got := rows[id]["queue_position"]; got != want {
			t.Errorf("run %s is reported at queue_position %v after the reorder, want %v", id, got, want)
		}
	}
	if got := rows[d.id]["priority"]; got != float64(PriorityHigh) {
		t.Errorf("D is reported at priority %v, want %d", got, PriorityHigh)
	}
}

// runRows reads GET /runs and keys the rows by run id, for the live rows that
// have one.
func runRows(t *testing.T, base string) map[string]map[string]any {
	t.Helper()

	resp := get(t, base, "/runs")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /runs: status %d, want 200", resp.StatusCode)
	}

	var body struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET /runs: %v", err)
	}

	out := make(map[string]map[string]any, len(body.Runs))
	for _, row := range body.Runs {
		if id, ok := row["id"].(string); ok {
			out[id] = row
		}
	}
	return out
}

// TestADecidedTorrentRejoinsTheQueueBehindItsLevel guards the tiebreak's own
// definition. It is the arrival at the QUEUE that orders two entries at one
// level, not the moment the request was first accepted - and a parked torrent
// is the one case where the two differ by minutes or hours.
//
// Ordering by the original accept time instead would silently promote every
// decided torrent to the front, which is not what the queue did before this
// change: DecideRun appended, and appending put it at the back.
func TestADecidedTorrentRejoinsTheQueueBehindItsLevel(t *testing.T) {
	runs := newFakeRuns()
	lister := newFakeLister()
	lister.holds(manySource, 3)
	lister.holds(oneSource, 1)

	srv, ts := newTestServerWithConfigAndLister(t, Config{MaxActiveTorrents: 1}, runs.runner, lister.list)

	// The multi-file torrent is accepted FIRST, so its queuedAt is the
	// earliest of the three. It takes the slot for its metadata pass and then
	// parks, giving the slot back.
	parked := startRun(t, ts.URL, manySource)
	waitFor(t, func() bool { return runInfo(t, srv, parked.id).State == RunNeedsAction })

	// Two ordinary torrents arrive while it waits for a person. One takes the
	// freed slot, the other queues.
	startRun(t, ts.URL, oneSource)
	later := startRun(t, ts.URL, "magnet:?xt=urn:btih:later")
	if got := queueOrder(srv); !sameOrder(got, []string{later.id}) {
		t.Fatalf("the queue is %v, want just the later torrent - the parked one holds no place while it waits", got)
	}
	if got := runInfo(t, srv, parked.id).QueuePosition; got != 0 {
		t.Fatalf("the parked torrent reports queue position %d, want 0 - it is not in the queue", got)
	}

	// Now it is decided. It rejoins at the BACK, behind the torrent that
	// arrived long after it, because that is when it joined the queue.
	resp := decideRun(t, ts.URL, parked.id, []string{"0"}, 0)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/decide: status %d, want 202", resp.StatusCode)
	}
	if got := queueOrder(srv); !sameOrder(got, []string{later.id, parked.id}) {
		t.Fatalf("the queue is %v after the decision, want %v - a decided torrent joins the queue now, not "+
			"when it was first accepted", got, []string{later.id, parked.id})
	}
}

// TestAPriorityGivenToAParkedTorrentSurvivesTheDecision: a person looking at
// a file picker can say "and put this one first" before ticking the boxes,
// and the level is what the torrent comes back into the queue at.
//
// The parked torrent holds no POSITION while it waits - it is not in the
// queue - which is exactly why the level has to be remembered on the entry
// rather than expressed as a place.
func TestAPriorityGivenToAParkedTorrentSurvivesTheDecision(t *testing.T) {
	runs := newFakeRuns()
	lister := newFakeLister()
	lister.holds(manySource, 3)
	lister.holds(oneSource, 1)

	srv, ts := newTestServerWithConfigAndLister(t, Config{MaxActiveTorrents: 1}, runs.runner, lister.list)

	parked := startRun(t, ts.URL, manySource)
	waitFor(t, func() bool { return runInfo(t, srv, parked.id).State == RunNeedsAction })

	startRun(t, ts.URL, oneSource)
	later := startRun(t, ts.URL, "magnet:?xt=urn:btih:later")

	if resp := setPriority(t, ts.URL, parked.id, PriorityHigh); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /runs/priority on a parked torrent: status %d, want 200", resp.StatusCode)
	}
	if got := runInfo(t, srv, parked.id).Priority; got != PriorityHigh {
		t.Fatalf("the parked torrent's priority is %d, want %d", got, PriorityHigh)
	}
	// Still no place in the queue: it is waiting for a person, not for a slot.
	if got := runInfo(t, srv, parked.id).QueuePosition; got != 0 {
		t.Fatalf("the parked torrent reports queue position %d, want 0", got)
	}

	if resp := decideRun(t, ts.URL, parked.id, []string{"0"}, 0); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/decide: status %d, want 202", resp.StatusCode)
	}
	// It rejoins in FRONT of the torrent that has been waiting - the level it
	// was given while parked is the level it comes back at.
	if got := queueOrder(srv); !sameOrder(got, []string{parked.id, later.id}) {
		t.Fatalf("the queue is %v after the decision, want %v - a priority set while parked must survive it",
			got, []string{parked.id, later.id})
	}
}
