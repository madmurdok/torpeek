package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/madmurdok/torpeek/internal/core"
)

// TOR-181: ticking a file's checkbox starts that file's frames, so the button
// under the list is gone - and a tick GROWS ONE RUN rather than starting one
// per file.
//
// The Go half of this file is real behaviour: it drives the registry through
// its own HTTP routes and its own event socket, and it is where the three
// windows a tick can land in are proved apart. The app.js/app.css half reads
// the SERVED script and stylesheet as text, because this repository ships no
// JS runner (see columns_test.go's note) - it catches a control deleted,
// renamed or silently regated, and it cannot catch a wrong pixel. A real
// browser is what this ticket's own report used for that, on a torrent
// holding several video files.

// ---------------------------------------------------------------------------
// The three windows.

// TestASecondTickGrowsTheQueuedRunRatherThanStartingAnother is decision one
// of this ticket, in the window where it is cheapest to be wrong: the pass
// has been accepted and has not started, so its request is still the
// server's to edit.
//
// Two ticks, one runner call, both files in it. N separate runs would have
// stood two per-run traffic ceilings side by side for one torrent - the
// multiplication core.Roof exists to bound - and would have put two rows on
// a page that promises one torrent one row (TOR-140).
//
// The queue width is 1 and another torrent holds the slot, which is what
// keeps the decided entry queued long enough for a second tick to reach it.
// With the shipped width of 5 the first tick dispatches immediately and the
// second one lands in the running window instead - which is the next test,
// and the reason that window has to work at all.
func TestASecondTickGrowsTheQueuedRunRatherThanStartingAnother(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 4)
	lister.holds(oneSource, 1)
	runs := newFakeRuns()
	srv, ts := newTestServerWithConfigAndLister(t,
		Config{MaxActiveTorrents: 1}, runs.runner, lister.list)

	// The parked torrent first, so its metadata pass gets the only slot.
	parked := parkOne(t, srv, ts.URL)
	// And now something to occupy that slot while the ticks land.
	blocker := startRun(t, ts.URL, oneSource)
	waitFor(t, func() bool { return runs.started(oneSource) })

	if resp := decideRun(t, ts.URL, parked.id, []string{"1"}, 7); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the first tick: status %d, want 202: %s", resp.StatusCode, readAll(t, resp))
	}
	waitFor(t, func() bool { return runInfo(t, srv, parked.id).State == RunQueued })
	before := runInfo(t, srv, parked.id).QueuePosition
	if before != 1 {
		t.Fatalf("the decided torrent is at queue position %d, want 1 - the rest of this "+
			"test cannot tell a kept position from a re-stamped one", before)
	}

	// A THIRD TORRENT BEHIND IT, before the second tick, and it is what makes
	// the assertion below discriminate: re-enqueueing the decided entry would
	// stamp it with a fresh arrival and send it behind this one (position 2),
	// where growing a request in place leaves it at 1.
	const thirdSource = "magnet:?xt=urn:btih:third"
	lister.holds(thirdSource, 1)
	third := startRun(t, ts.URL, thirdSource)
	waitFor(t, func() bool { return runInfo(t, srv, third.id).QueuePosition == 2 })

	if resp := decideRun(t, ts.URL, parked.id, []string{"3"}, 20); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the second tick: status %d, want 202: %s", resp.StatusCode, readAll(t, resp))
	}

	// Still one entry, still queued, and still where it was: a tick asks for
	// one more file, it does not re-arrive at the back of its level.
	if got := runInfo(t, srv, parked.id).State; got != RunQueued {
		t.Fatalf("the grown run is %s, want it still queued behind the blocker", got)
	}
	if got := runInfo(t, srv, parked.id).QueuePosition; got != before {
		t.Errorf("the second tick moved the run from queue position %d to %d - growing a "+
			"selection is not re-arriving, and a torrent that already waited must not "+
			"be sent to the back of its level for asking for one more file", before, got)
	}
	if got := len(srv.snapshot()); got != 3 {
		t.Fatalf("the server holds %d runs, want 3 (the parked one, the blocker and the "+
			"third) - a tick must not mint a row of its own", got)
	}

	// One runner call for both files, once the slot comes free.
	runs.finish(t, oneSource)
	waitFor(t, func() bool { return runInfo(t, srv, blocker.id).State == RunDone })
	waitFor(t, func() bool { return runs.started(manySource) })

	req := runs.stream(t, manySource).req
	if got := strings.Join(req.Files, ","); got != "1,3" {
		t.Errorf("the runner was handed files %q, want \"1,3\" - one pass for both ticks", got)
	}
	// The count of the tick that OPENED the pass, not the one that joined it:
	// the row priced file 1 at 7 frames and that promise has to survive
	// somebody retyping the intake box before the second tick.
	if req.Count != 7 {
		t.Errorf("the pass runs at %d frames per file, want 7 - the figure locks to the "+
			"tick that opened the pass, so a later tick's count cannot re-price it", req.Count)
	}
	if got := runs.count(); got != 2 {
		t.Fatalf("the runner was called %d times, want 2 (the blocker and one grown pass)", got)
	}
}

// TestATickDuringAFetchStartsAsTheNextPassOnTheSameRow is the window the
// ticket asks about and the one that decides whether this feature works at
// all: dispatch is synchronous, so on the shipped queue width the first tick
// starts the run before the POST has even answered - and every tick after it
// arrives at a RUNNING row.
//
// The engine works from a plan it was handed, so such a tick cannot join the
// pass in flight. It waits on the entry and starts the moment that pass ends,
// on the SAME entry: same id, same row, same history. The passes are
// therefore sequential, which is the whole of the ceiling argument - one
// per-run budget is live for this torrent at a time, each scaled to the files
// that pass actually asks for.
func TestATickDuringAFetchStartsAsTheNextPassOnTheSameRow(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 4)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	parked := parkOne(t, srv, ts.URL)

	if resp := decideRun(t, ts.URL, parked.id, []string{"1"}, 5); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the first tick: status %d, want 202", resp.StatusCode)
	}
	waitFor(t, func() bool { return runs.started(manySource) })
	first := runs.stream(t, manySource)
	if got := strings.Join(first.req.Files, ","); got != "1" {
		t.Fatalf("the first pass was handed %q, want just \"1\"", got)
	}

	// The tick that lands mid-fetch. Accepted, and it must not have started
	// anything yet - a second concurrent run for one torrent is exactly what
	// this design refuses.
	if resp := decideRun(t, ts.URL, parked.id, []string{"3"}, 9); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("a tick during the fetch: status %d, want 202", resp.StatusCode)
	}
	if got := runs.count(); got != 1 {
		t.Fatalf("the runner has been called %d times, want 1 - a tick during a fetch "+
			"must not open a second run beside the first", got)
	}
	if got := runInfo(t, srv, parked.id).State; got != RunRunning {
		t.Fatalf("the row is %s, want it still running its first pass", got)
	}

	// The pass ends, and the deferred file starts on this same entry.
	runs.finish(t, manySource)
	waitFor(t, func() bool { return runs.count() == 2 })
	waitFor(t, func() bool {
		state := runInfo(t, srv, parked.id).State
		return state == RunQueued || state == RunRunning
	})

	if got := len(srv.snapshot()); got != 1 {
		t.Fatalf("the server holds %d runs, want 1 - the next pass re-arms the entry "+
			"rather than minting a second row for one torrent (TOR-140)", got)
	}
	second := runs.stream(t, manySource)
	if second == first {
		t.Fatal("the second pass reused the first pass's stream; this test cannot tell them apart")
	}
	if got := strings.Join(second.req.Files, ","); got != "3" {
		t.Errorf("the next pass was handed %q, want just \"3\" - the frames the first "+
			"pass took are on disk, and re-asking for them would pay for them twice", got)
	}
	// The count of the tick that opened THIS pass, which is a new pass and so
	// a new figure - unlike a tick joining one already forming.
	if second.req.Count != 9 {
		t.Errorf("the next pass runs at %d frames per file, want 9 - it is a pass this "+
			"tick opened, so it gets the number that tick was priced at", second.req.Count)
	}
	// And no raise carried over. Nothing raised this one, but the assertion
	// is the standing rule: a ceiling is consented to once, for one question
	// the server priced off disk (RetryRun's own words).
	if second.req.MaxBytes != 0 {
		t.Errorf("the next pass carries MaxBytes = %d, want 0 - a pass a tick opened is "+
			"an ordinary one, and its ceiling is whatever core.budgetFor scales to the "+
			"files it asks for", second.req.MaxBytes)
	}
}

// TestATickIsRefusedWhereThereIsNoRunToGrow walks refuseTick's own table as a
// unit, because two of its cases cannot be reached through the routes: a
// torrent dropped onto the page as a file (whose staged copy is removed the
// moment its run ends, so a deferred tick would have nothing to open) and a
// replay.
//
// The sentences are the point as much as the refusal. run_state carries them
// so a disabled checkbox says exactly what the POST would have answered
// (updateFileCosts), and a refusal with no reason is one a person cannot act
// on.
func TestATickIsRefusedWhereThereIsNoRunToGrow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entry   runEntry
		refused bool
		says    string
	}{
		{
			name:  "a parked torrent, which is the window this ticket is about",
			entry: runEntry{id: "a", state: RunNeedsAction, videos: []int{0, 1}},
		},
		{
			name:  "one queued, whose request is still the server's to edit",
			entry: runEntry{id: "b", state: RunQueued, videos: []int{0, 1}},
		},
		{
			name:  "one fetching, where the tick waits for the next pass",
			entry: runEntry{id: "c", state: RunRunning, videos: []int{0, 1}},
		},
		{
			name:    "one whose metadata has not arrived, so there is no list to name",
			entry:   runEntry{id: "d", state: RunQueued},
			refused: true,
			says:    "has not been told what its torrent holds",
		},
		{
			name: "a dropped .torrent, fetching, whose staged copy goes when it ends",
			entry: runEntry{id: "e", state: RunRunning, videos: []int{0, 1},
				req: RunRequest{Label: "release.torrent"}},
			refused: true,
			says:    "staged copy",
		},
		{
			name:    "a replay, which reads disk and fetches nothing",
			entry:   runEntry{id: "f", state: RunReplaying, videos: []int{0, 1}},
			refused: true,
			says:    "read back from disk",
		},
		{
			name:    "one that finished, where another file is a new run",
			entry:   runEntry{id: "g", state: RunDone, videos: []int{0, 1}},
			refused: true,
			says:    "no run left for a tick to grow",
		},
		{
			name:    "one that failed",
			entry:   runEntry{id: "h", state: RunFailed, videos: []int{0, 1}},
			refused: true,
			says:    "no run left for a tick to grow",
		},
		{
			name:    "one that was cancelled",
			entry:   runEntry{id: "i", state: RunCancelled, videos: []int{0, 1}},
			refused: true,
			says:    "no run left for a tick to grow",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.entry.refuseTick()
			if tc.refused && got == "" {
				t.Fatalf("a %s row takes a tick, and there is nothing there for one to grow",
					tc.entry.state)
			}
			if !tc.refused && got != "" {
				t.Fatalf("a %s row refuses a tick with %q, and it is one of the three "+
					"windows a tick is meant to land in", tc.entry.state, got)
			}
			if tc.says != "" && !strings.Contains(got, tc.says) {
				t.Errorf("the refusal is %q, which does not say %q - the sentence is what "+
					"a disabled box shows, and one without a reason cannot be acted on",
					got, tc.says)
			}
		})
	}
}

// TestARowThatNamesNoFilesIsAlreadyTakingAllOfThem is the trap this design
// walked into and had to be told about: an empty RunRequest.Files means EVERY
// video file, so a single-video torrent - or any run started without a
// selection - is already capturing whatever a tick would name.
//
// Writing the index in would NARROW such a run from every video to that one,
// which is the exact opposite of what a tick means, and reporting "nothing
// ticked" for it would draw an empty checkbox beside the very file being
// downloaded.
func TestARowThatNamesNoFilesIsAlreadyTakingAllOfThem(t *testing.T) {
	lister := newFakeLister()
	lister.holds(oneSource, 1)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	conn := dial(t, ts.URL)
	next(t, conn) // the idle connection marker

	run := startRun(t, ts.URL, oneSource)
	waitFor(t, func() bool { return runs.started(oneSource) })

	// A stale page ticking the file this run is already taking: accepted and
	// changed nothing, which is the same idempotent answer a double click
	// gets. A refusal here would be a refusal a person cannot act on.
	if resp := decideRun(t, ts.URL, run.id, []string{"0"}, 0); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ticking a file this run already takes: status %d, want 202: %s",
			resp.StatusCode, readAll(t, resp))
	}
	if got := runs.stream(t, oneSource).req.Files; len(got) != 0 {
		t.Fatalf("the run in flight now names files %v - it was started with none, which "+
			"means ALL of them, and naming one narrows it to that one", got)
	}
	// AND NOTHING IS WAITING FOR A SECOND PASS. This is where the narrowing
	// would actually show: the pass in flight is a copy the engine already
	// holds, so an accepted tick cannot change it - what it would do is queue
	// a pass for a file this run is capturing anyway, and pay for it twice.
	// Read straight off the entry rather than by waiting to see whether a
	// second run appears, because a negative proved by waiting is a test that
	// passes for the wrong reason on a slow machine.
	if got := pendingOf(srv, run.id); len(got) != 0 {
		t.Fatalf("the row holds pending = %v after a tick it already covers - that is a "+
			"whole second pass queued for a file this run is taking anyway", got)
	}
	if got := runs.count(); got != 1 {
		t.Fatalf("the runner was called %d times, want 1 - the tick asked for nothing new", got)
	}
	if got := runInfo(t, srv, run.id).State; got != RunRunning {
		t.Fatalf("the row is %s after a tick it already covers, want it still running - an "+
			"idempotent tick changes nothing at all", got)
	}

	// And the row reports the file as asked for, because it is.
	//
	// Scanned for rather than read off the first "running" message, and the
	// reason is worth knowing: a run publishes running TWICE - once in
	// dispatch, when it takes the slot for its metadata pass, and again in
	// beginRun once the listing is over. The first of those honestly reports
	// nothing ticked and no tick possible, because the torrent has not yet
	// said what it holds.
	found := false
	for i := 0; i < 200 && !found; i++ {
		ev := next(t, conn)
		if ev["type"] != "run_state" || ev["ticked"] == nil {
			continue
		}
		found = true
		if got := indices(t, ev["ticked"]); len(got) != 1 || got[0] != 0 {
			t.Fatalf("a running single-video row reports ticked = %v, want [0] - an empty "+
				"selection is every video file, not none of them", ev["ticked"])
		}
	}
	if !found {
		t.Fatal("no run_state ever reported what this row had asked for, so a single-video " +
			"torrent draws an empty checkbox beside the very file it is downloading")
	}
}

// TestARunThatSkippedTheListingStillLearnsWhatItCanBeAskedFor closes the gap
// that made a tick refuse on exactly the rows whose file list the page was
// already showing.
//
// A request that NAMES its files pays for no metadata pass (needsListingLocked
// is false), so entry.contents - the listing's own record - stays nil for the
// whole life of such an entry. Regenerate, a top-up and a retry are all that
// shape. Validating a tick against contents therefore refused every one of
// them, while the page happily drew their file list from the engine's own
// metadata_ready. entry.videos is the fix: pump reads the same event and the
// row becomes askable the moment the torrent has said what it holds.
func TestARunThatSkippedTheListingStillLearnsWhatItCanBeAskedFor(t *testing.T) {
	lister := newFakeLister()
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	resp := post(t, ts.URL, "/runs", `{"source":"`+oneSource+`","files":["0"]}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs with a selection: status %d, want 202: %s",
			resp.StatusCode, readAll(t, resp))
	}
	id, _ := decodeBody(t, resp)["id"].(string)
	waitFor(t, func() bool { return runs.started(oneSource) })
	if got := lister.count(); got != 0 {
		t.Fatalf("the listing ran %d times for a request that already named its files - "+
			"this test is about the path that never lists", got)
	}

	// Before the engine has said anything, there is no list to name an index
	// out of, and the refusal says exactly that rather than blaming the state.
	if resp := decideRun(t, ts.URL, id, []string{"1"}, 0); resp.StatusCode != http.StatusConflict {
		t.Fatalf("a tick before any metadata: status %d, want 409: %s",
			resp.StatusCode, readAll(t, resp))
	}

	runs.send(t, oneSource, core.MetadataReady{
		Name:     "release for one",
		InfoHash: "0000000000000000000000000000000000000001",
		Videos:   fakeVideos(3),
		Selected: []int{0},
	})
	waitFor(t, func() bool { return refusalOf(srv, id) == "" })

	if resp := decideRun(t, ts.URL, id, []string{"1"}, 0); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("a tick after metadata_ready: status %d, want 202: %s",
			resp.StatusCode, readAll(t, resp))
	}
	// And still only the indices the engine named: the check is a real check,
	// not a shortcut that stopped checking.
	if resp := decideRun(t, ts.URL, id, []string{"9"}, 0); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a tick naming a file this torrent does not hold: status %d, want 400",
			resp.StatusCode)
	}
}

// refusalOf reads refuseTick's verdict for one row under the server's own
// lock, which is the rule that method states.
func refusalOf(srv *Server, id string) string {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	entry := srv.runs[id]
	if entry == nil {
		return "no such run"
	}
	return entry.refuseTick()
}

// TestRunStateSaysWhatTheRowHasAskedForAndWhetherItCanBeAskedForMore is the
// wire half. The page draws a live checkbox only where the server says a tick
// would be taken, and it must not re-derive that rule for itself - refuseTick
// has a case the page cannot see (the dropped .torrent above).
func TestRunStateSaysWhatTheRowHasAskedForAndWhetherItCanBeAskedForMore(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 4)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	conn := dial(t, ts.URL)
	next(t, conn)

	parked := parkOne(t, srv, ts.URL)

	// Parked: tickable, nothing asked for yet.
	ev := waitForRunState(t, conn, string(RunNeedsAction))
	if ev["tickable"] != true {
		t.Fatalf("a parked row reports tickable = %v, want true - it is the whole window "+
			"this ticket is written about", ev["tickable"])
	}
	if _, ok := ev["ticked"]; ok {
		t.Errorf("a parked row already reports ticked = %v; nothing is pre-ticked there on "+
			"purpose, since the count is per file (TOR-50)", ev["ticked"])
	}

	if resp := decideRun(t, ts.URL, parked.id, []string{"2"}, 11); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the tick: status %d, want 202", resp.StatusCode)
	}
	waitFor(t, func() bool { return runs.started(manySource) })

	// Running: the file it is taking, the locked count, still tickable.
	ev = waitForRunState(t, conn, string(RunRunning))
	if got := indices(t, ev["ticked"]); len(got) != 1 || got[0] != 2 {
		t.Errorf("a running row reports ticked = %v, want [2]", ev["ticked"])
	}
	if ev["count"] != float64(11) {
		t.Errorf("the row reports count = %v, want 11 - the page prices its remaining "+
			"files at what the pass is locked to, not at whatever the intake box now shows",
			ev["count"])
	}
	if ev["tickable"] != true {
		t.Errorf("a running row reports tickable = %v, want true - a tick there waits for "+
			"the next pass rather than being refused", ev["tickable"])
	}

	// A tick mid-fetch is reported as deferred, which is the one thing about
	// a tick a reader cannot see for themselves.
	if resp := decideRun(t, ts.URL, parked.id, []string{"0"}, 11); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("a tick during the fetch: status %d, want 202", resp.StatusCode)
	}
	ev = waitForRunState(t, conn, string(RunRunning))
	if got := indices(t, ev["ticked"]); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Errorf("the row reports ticked = %v, want [0 2] - every file this row has asked "+
			"for, whichever pass it lands in", ev["ticked"])
	}
	if got := indices(t, ev["deferred"]); len(got) != 1 || got[0] != 0 {
		t.Errorf("the row reports deferred = %v, want [0] - the page has no other way to "+
			"tell a tick that joined the pass in flight from one waiting for the next",
			ev["deferred"])
	}

	// Cancelled: not tickable, and the reason is a sentence.
	if _, err := srv.CancelRun(parked.id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	ev = waitForRunState(t, conn, string(RunCancelled))
	if ev["tickable"] != false {
		t.Fatalf("a cancelled row reports tickable = %v, want false", ev["tickable"])
	}
	if why, _ := ev["tick_refusal"].(string); !strings.Contains(why, "no run left") {
		t.Errorf("a cancelled row's tick_refusal is %q, want the sentence a disabled box "+
			"can show", why)
	}
}

// TestACancelAnswersTheTicksThatWereWaiting: somebody who stops a torrent has
// said the one thing that means "do not start the next pass", so files ticked
// while it was fetching are dropped rather than held for a restart nobody
// asked for - and the row stops claiming them.
//
// The decision itself is asserted on pendingPass, not on "no second run ever
// appeared": a negative like that can only be checked by waiting long enough
// to be convinced, which is a test that passes for the wrong reason on a slow
// machine (see waitFor's own note on sleeps). What the live half then proves
// is that a real cancel reaches that decision - the pending files are gone
// from the entry, and the row is still cancelled rather than queued again.
func TestACancelAnswersTheTicksThatWereWaiting(t *testing.T) {
	// The decision, exactly.
	for _, tc := range []struct {
		name  string
		entry *runEntry
		srv   *Server
	}{
		{
			name:  "a cancel",
			entry: &runEntry{id: "a", state: RunCancelled, cancelled: true, pending: []string{"3"}},
			srv:   &Server{},
		},
		{
			name:  "a server on its way down",
			entry: &runEntry{id: "b", state: RunDone, pending: []string{"3"}},
			srv:   &Server{stopped: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.srv.pendingPass(tc.entry); ok {
				t.Error("pendingPass offered a pass anyway - starting it is the one thing " +
					"that was just asked not to happen")
			}
			if len(tc.entry.pending) != 0 {
				t.Errorf("pending is still %v - a row that keeps them goes on reporting "+
					"\"in the next pass\" about files nothing will ever fetch", tc.entry.pending)
			}
		})
	}

	// And a real cancel reaches it.
	lister := newFakeLister()
	lister.holds(manySource, 4)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	parked := parkOne(t, srv, ts.URL)
	if resp := decideRun(t, ts.URL, parked.id, []string{"1"}, 5); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the first tick: status %d, want 202", resp.StatusCode)
	}
	waitFor(t, func() bool { return runs.started(manySource) })
	if resp := decideRun(t, ts.URL, parked.id, []string{"3"}, 5); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("a tick during the fetch: status %d, want 202", resp.StatusCode)
	}
	if got := pendingOf(srv, parked.id); len(got) != 1 || got[0] != "3" {
		t.Fatalf("the row holds pending = %v, want [3] - the rest of this cannot tell a "+
			"cancel that dropped them from a tick that never landed", got)
	}

	if _, err := srv.CancelRun(parked.id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// pendingPass runs at the end of the stream and empties this either way,
	// so an empty slice means the decision has been taken - which is exactly
	// the moment the state below is worth reading.
	waitFor(t, func() bool { return len(pendingOf(srv, parked.id)) == 0 })

	if got := runInfo(t, srv, parked.id).State; got != RunCancelled {
		t.Fatalf("the row is %s after a cancel, want it cancelled - a pass started from "+
			"the dropped ticks would have put it back in the queue", got)
	}
	if got := runs.count(); got != 1 {
		t.Fatalf("the runner was called %d times after a cancel, want 1", got)
	}
}

// pendingOf reads one entry's pending list under the server's own lock,
// which is where every other reader of it goes (runEntry.pending).
func pendingOf(srv *Server, id string) []string {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	entry := srv.runs[id]
	if entry == nil {
		return nil
	}
	return append([]string(nil), entry.pending...)
}

// TestAPendingPassNeverInheritsARaise is the ceiling rule as a unit, because
// the only way to reach it through the routes is to top a run up and tick it
// in the same instant.
//
// A raise is consented to once, for one question the server priced off disk
// (TopUp.request, RetryRun's own reset). A pass that a TICK opened is an
// ordinary one, and its ceiling is whatever core.budgetFor scales to the
// files it actually asks for - which is the arithmetic this whole design
// rests on.
func TestAPendingPassNeverInheritsARaise(t *testing.T) {
	srv := &Server{}
	entry := &runEntry{
		id:      "a",
		state:   RunDone,
		pending: []string{"3"},
		req:     RunRequest{Source: "magnet:?xt=urn:btih:x", Files: []string{"1"}, MaxBytes: 9 << 30},
	}

	req, ok := srv.pendingPass(entry)
	if !ok {
		t.Fatal("pendingPass refused a pass with a file waiting for it")
	}
	if got := strings.Join(req.Files, ","); got != "3" {
		t.Errorf("the pass asks for %q, want \"3\" - only what has not been fetched", got)
	}
	if req.MaxBytes != 0 {
		t.Errorf("the pass carries MaxBytes = %d, want 0", req.MaxBytes)
	}
	if len(entry.pending) != 0 {
		t.Errorf("pending is still %v - it has to be claimed exactly once, or pump would "+
			"start the same pass at the end of every stream", entry.pending)
	}
	if _, ok := srv.pendingPass(entry); ok {
		t.Error("pendingPass offered the same pass twice")
	}
}

// TestTheCeilingArithmeticFavoursOneGrownRun is the honesty check on decision
// one, and it is the reason a tick grows a run instead of starting one.
//
// Both shapes cost the same while the files are few - the ceiling is linear
// in them - and they diverge exactly where it matters: one run is CAPPED
// (core.MaxRunBytes), where N runs are N ceilings side by side with nothing
// over the pair but the client-wide roof.
func TestTheCeilingArithmeticFavoursOneGrownRun(t *testing.T) {
	one := core.DefaultBudget(1).MaxBytes
	if one <= 0 {
		t.Fatalf("core.DefaultBudget(1) has no ceiling (%d); the rest of this cannot mean anything", one)
	}

	// Few files: growing costs exactly what starting one run per file would,
	// which is what makes the choice free of any penalty.
	if got, want := core.DefaultBudget(4).MaxBytes, 4*one; got != want {
		t.Errorf("a four-file pass is allowed %d, and four one-file runs %d - the per-run "+
			"ceiling is meant to scale with the files a run actually works on "+
			"(core.budgetFor)", got, want)
	}

	// Many files: the grown run stops at the cap, and the pile of separate
	// runs does not.
	many := int(core.MaxRunBytes/one) + 2
	grown := core.DefaultBudget(many).MaxBytes
	if grown != core.MaxRunBytes {
		t.Errorf("a %d-file pass is allowed %d, want the cap %d", many, grown, core.MaxRunBytes)
	}
	if separate := int64(many) * one; separate <= grown {
		t.Errorf("%d separate runs would be allowed %d and one grown run %d - if the pile "+
			"were not the larger figure, this ticket's whole reason for growing one run "+
			"would be gone", many, separate, grown)
	}
}

// waitForRunState reads the socket until this run reaches the named state.
// The stream carries a run_state for every queue movement as well as for
// every state change (TOR-140), so a test asking about one state cannot
// assume it is the next message.
func waitForRunState(t *testing.T, conn *websocket.Conn, state string) map[string]any {
	t.Helper()

	for i := 0; i < 200; i++ {
		ev := next(t, conn)
		if ev["type"] == "run_state" && ev["state"] == state {
			return ev
		}
	}
	t.Fatalf("no run_state reporting %q arrived in 200 messages", state)
	return nil
}

// indices reads one of run_state's index lists back as ints. JSON numbers
// arrive as float64, and a test comparing them as `any` would pass on the
// wrong type as readily as on the right value.
func indices(t *testing.T, raw any) []int {
	t.Helper()

	list, ok := raw.([]any)
	if !ok {
		if raw == nil {
			return nil
		}
		t.Fatalf("%v is not a list of indices", raw)
	}
	out := make([]int, 0, len(list))
	for _, item := range list {
		n, ok := item.(float64)
		if !ok {
			t.Fatalf("%v holds %v, which is not a number", raw, item)
		}
		out = append(out, int(n))
	}
	return out
}

// ---------------------------------------------------------------------------
// The page.

// TestTheButtonUnderTheFileListIsGone is the ticket's own first sentence, and
// the one thing a text check can prove outright: the control is not in the
// script or the stylesheet that ship.
//
// The INTAKE's Take Frames is a different button and stays. It is how a
// torrent gets added at all, and confusing the two would remove the only way
// into the program - so it is asserted present in the same test that asserts
// the other gone.
func TestTheButtonUnderTheFileListIsGone(t *testing.T) {
	js := servedScript(t)
	css := stylesheet(t)

	// Comments name the removed things in prose, on purpose - a reader has to
	// be able to find out where the button went.
	live := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(js, "")
	for _, gone := range []string{"picker-go", "picker-foot", "picker-none", "pickerGo", "pickerFoot", "pickerNone"} {
		if strings.Contains(live, gone) {
			t.Errorf("app.js still builds or reads %q - ticking a file starts it, so a "+
				"button under the list is a second click for something already done", gone)
		}
	}
	liveCSS := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
	for _, gone := range []string{".picker-go", ".picker-foot", ".picker-none"} {
		if strings.Contains(liveCSS, gone) {
			t.Errorf("app.css still styles %q, which app.js no longer builds", gone)
		}
	}

	// The tick is what starts it now: the checkbox's own listener.
	fn := jsFunc(t, js, "renderFileList")
	if !regexp.MustCompile(`box\.addEventListener\("change", \(\) => tickFile\(`).MatchString(fn) {
		t.Error("a checkbox's change no longer calls tickFile - the tick IS the decision " +
			"since TOR-181, and nothing else on the row starts a capture")
	}

	html, err := embedded.ReadFile("assets/index.html")
	if err != nil {
		t.Fatalf("reading the embedded index.html: %v", err)
	}
	if !strings.Contains(string(html), `<button id="go" type="submit">Take frames</button>`) {
		t.Error("the intake's own Take Frames button is gone. It is NOT the button this " +
			"ticket removes - it is how a torrent gets added at all, and without it there " +
			"is no way into the program")
	}
}

// TestEveryTickableRowSaysWhatItWillSpendBeforeItIsTicked is the ticket's
// second half: "a cost shown where a button used to be is a cost nobody
// sees". The figure has to be on the file's own row, because that is where
// the decision is now, and it has to be there BEFORE the tick.
//
// TOR-50's trap is the reason any of this exists: -n is frames per video
// file, so a torrent of six quality variants costs six times the intake's
// number, and the row is where that multiplication is agreed to one file at
// a time.
func TestEveryTickableRowSaysWhatItWillSpendBeforeItIsTicked(t *testing.T) {
	js := servedScript(t)
	css := stylesheet(t)

	// The span is built into the row, not into a foot below the list.
	render := jsFunc(t, js, "renderFileList")
	if !strings.Contains(render, `cost.className = "picker-cost"`) {
		t.Fatal("renderFileList builds no .picker-cost on a file's row - the figure moved " +
			"there when the button under the list went")
	}
	if !strings.Contains(render, "row.append(cost, asked)") {
		t.Error("the cost span is not appended to the file's own row - a figure anywhere " +
			"else is the cost nobody sees that this ticket is about")
	}

	// And only for a row a tick can reach: there is no price for something
	// that cannot be bought.
	if !regexp.MustCompile(`(?s)if \(video\) \{[^}]*cost\.className`).MatchString(render) {
		t.Error("the cost span is built for untickable rows too - a .nfo has no price, " +
			"because no tick can name it")
	}

	// The figure itself, and the one number it may quote.
	costs := jsFunc(t, js, "updateFileCosts")
	if !strings.Contains(costs, "row.cost.textContent = framesLabel(n)") {
		t.Error("updateFileCosts does not write the frame count onto the row")
	}
	if !strings.Contains(costs, "const n = passCount(entry)") {
		t.Error("updateFileCosts does not read passCount - a row with a pass already " +
			"forming must quote the count that pass is LOCKED to, not whatever the intake " +
			"box now shows, or the price changes under a person after they read it")
	}
	// An asked-for row shows no price: it has been bought.
	if !regexp.MustCompile(`(?s)if \(asked\) \{[^}]*row\.cost\.textContent = "";`).MatchString(costs) {
		t.Error("a file already asked for still shows a price - the figure is what a tick " +
			"WILL spend, and that question is settled for this one")
	}

	// It follows the intake box live, because a person may well retype the
	// count while reading the list.
	listener := jsListener(t, js, `el.count.addEventListener("input"`)
	if !strings.Contains(listener, "updateFileCosts(entry)") {
		t.Error("retyping the intake count no longer restates the rows' prices")
	}

	// THE ACCENT IS NOT WHAT SAYS IT. The accent means "this is live, or this
	// is where you are" and wears nothing else; a caution about somebody's
	// allowance is --warn, which is what .run-again-cost beside it already
	// uses for the same promise.
	rule := cssRule(t, css, ".picker-cost {")
	if !strings.Contains(rule, "var(--warn)") {
		t.Errorf(".picker-cost is %q, want var(--warn) - the same colour .run-again-cost "+
			"wears for the same kind of statement", rule)
	}
	if strings.Contains(rule, "var(--accent)") {
		t.Errorf(".picker-cost is %q, and the accent means \"live, or where you are\" "+
			"(:root) - a column of glowing figures down a season pack would read as a "+
			"column of things happening", rule)
	}
}

// TestSelectAllStatesItsTotalAndAsksBeforeSpending is the third decision this
// ticket left open. Select all used to stage a selection somebody then
// confirmed with the button under the list; with that button gone it spends
// traffic on every video file at once, which makes it the most expensive
// click on the page.
//
// It is kept, it states the total, and it asks - in the button itself rather
// than in a dialog, so the figure is in the page's own type and can be seen
// in a screenshot of what the page actually said.
func TestSelectAllStatesItsTotalAndAsksBeforeSpending(t *testing.T) {
	js := servedScript(t)
	css := stylesheet(t)

	arm := jsFunc(t, js, "armSelectAll")
	if !strings.Contains(arm, "if (entry.armed) {") || !strings.Contains(arm, "entry.armed = true") {
		t.Fatal("armSelectAll does not arm and then act on a second press - one press that " +
			"starts every file is the silent one-click bill this ticket refuses to leave")
	}
	if !regexp.MustCompile(`(?s)if \(entry\.armed\) \{\s*const files = untickedVideos\(entry\)`).MatchString(arm) {
		t.Error("the second press does not re-read what it would start - a tick or a " +
			"run_state may have landed since the first, and starting a stale list would " +
			"spend on a file somebody else's tick already started")
	}

	sync := jsFunc(t, js, "syncSelectAll")
	// The total, in the label and in the sentence beside it. Both, because a
	// label that changed is a hint and this needs an instruction.
	if !strings.Contains(sync, `"Start all " + remaining.length + " — " + total + " frames"`) {
		t.Error("the armed button does not name the total it would spend")
	}
	if !strings.Contains(sync, `" = " + total + " frames"`) {
		t.Error("the armed sentence does not state the multiplication - the count is PER " +
			"FILE (TOR-50), and the product is the number that surprises people")
	}
	if !strings.Contains(sync, "press again to confirm") {
		t.Error("nothing tells a person that the next press is the one that spends")
	}
	if !strings.Contains(sync, "Esc to cancel") {
		t.Error("an armed control with no way out is one somebody comes back to and " +
			"presses without re-reading")
	}
	if !strings.Contains(js, `entry.pickerAll.addEventListener("blur", () => disarmSelectAll(entry))`) {
		t.Error("Select all stays armed when focus leaves it")
	}
	// A tick disarms it too: somebody who ticks one file has answered the
	// question by doing something else.
	if !strings.Contains(jsFunc(t, js, "tickFile"), "disarmSelectAll(entry)") {
		t.Error("ticking a single file leaves Select all armed - which would put a " +
			"whole-torrent bill under the next stray press")
	}

	// Three carriers for the armed state and only one of them is colour.
	if !strings.Contains(sync, `entry.pickerAll.dataset.armed = "true"`) {
		t.Error("nothing marks the armed button for the stylesheet")
	}
	rule := cssRule(t, css, `.picker-all[data-armed="true"] {`)
	if !strings.Contains(rule, "var(--warn)") {
		t.Errorf("an armed Select all is %q, want var(--warn) - the largest sum on the page", rule)
	}

	// And Select none is not standing beside it pretending a tick can be
	// taken back. TOR-184 owns that.
	if strings.Contains(regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(js, ""), "Select none") {
		t.Error("Select none is still on the page - a tick has started a fetch, so a " +
			"control that clears boxes would claim to stop something it cannot")
	}
}

// TestATickThatIsRefusedLeavesNoBoxClaimingAFetch: the box is checked
// optimistically, because a click whose only feedback waits on a websocket
// reads as a click that did nothing - so the refusal has to put it back.
// A box left ticked after one would say a file is being captured when
// nothing is.
func TestATickThatIsRefusedLeavesNoBoxClaimingAFetch(t *testing.T) {
	js := servedScript(t)
	fn := jsFunc(t, js, "startFiles")

	if !strings.Contains(fn, `await post("runs/decide"`) {
		t.Fatal("startFiles does not post the tick")
	}
	if !strings.Contains(fn, "count: passCount(entry)") {
		t.Error("startFiles sends a count it did not display - the promise a price makes " +
			"is that pressing the thing beside it costs THAT")
	}
	if !regexp.MustCompile(`(?s)catch \(err\) \{.*entry\.picked\.delete\(index\).*entry\.deferred\.delete\(index\)`).MatchString(fn) {
		t.Error("a refused tick is not rolled back out of picked and deferred")
	}
	if !regexp.MustCompile(`(?s)catch \(err\) \{.*updateFileCosts\(entry\)`).MatchString(fn) {
		t.Error("a refused tick does not redraw the rows, so the box stays ticked beside a " +
			"file nothing is fetching")
	}

	// And the boxes are the server's answer, not the page's intention. Read
	// off the whole script rather than out of apply(): these two assignments
	// are unique in it, and the run_state handler is a block inside a
	// function rather than a function of its own, so jsFunc has nothing to
	// bound.
	if !strings.Contains(js, "entry.picked = new Set(ev.ticked || [])") {
		t.Error("run_state's \"ticked\" is not written into entry.picked - a tick that was " +
			"refused, or a second tab ticking the same torrent, would leave the boxes " +
			"disagreeing with what is actually being fetched")
	}
	if !strings.Contains(js, "entry.tickable = !!ev.tickable") {
		t.Error("the page does not read the server's own tickable verdict")
	}
}

// TestABoxThatCannotBeTickedSaysWhyAndDoesNotLookClickable is the same
// discipline TOR-180 applied to a non-video row, one state further on: a
// control that cannot be used must say so without relying on colour, and
// must not light up under the pointer as though it could.
func TestABoxThatCannotBeTickedSaysWhyAndDoesNotLookClickable(t *testing.T) {
	js := servedScript(t)
	css := stylesheet(t)

	fn := jsFunc(t, js, "updateFileCosts")
	if !strings.Contains(fn, "row.box.disabled = asked || !entry.tickable") {
		t.Fatal("a box is not disabled for a file already asked for, or on a row the " +
			"server would refuse - a live box the server then refuses is a control that lies")
	}
	if !strings.Contains(fn, "row.row.title = entry.tickRefusal") {
		t.Error("a disabled box carries no reason - the server sends its own sentence " +
			"(run_state's tick_refusal) precisely so the box can show exactly what the " +
			"POST would have answered")
	}
	if !strings.Contains(fn, `row.item.dataset.tick = "closed"`) ||
		!strings.Contains(fn, `row.item.dataset.tick = "asked"`) ||
		!strings.Contains(fn, `row.item.dataset.tick = "open"`) {
		t.Error("a row does not mark which of the three tick states it is in, so the " +
			"stylesheet cannot tell a clickable row from one that only looks like one")
	}

	for _, sel := range []string{
		`.picker-item[data-tick="asked"] .picker-file,`,
		`.picker-item[data-tick="asked"] .picker-file:hover,`,
	} {
		if !strings.Contains(css, sel) {
			t.Errorf("app.css has no %q rule - a row that lights up under the pointer is a "+
				"row that looks like it does something", sel)
		}
	}
	if got := cssRule(t, css, `.picker-item[data-tick="asked"] .picker-file,`); !strings.Contains(got, "cursor: default") {
		t.Errorf("an unclickable row's cursor rule is %q, want cursor: default", got)
	}
}

// TestASecondPassDoesNotClearTheTicksTheFirstOneTook is a bug the browser
// found and the text checks did not, so it gets a guard of its own.
//
// metadata_ready's "selected" is the pass IN FLIGHT. When a tick that arrived
// mid-fetch starts as a second pass, that pass is re-armed with only the
// deferred files (Server.pendingPass), so its metadata_ready names only
// those - and renderFileList assigning the set from it cleared the tick
// beside a file the FIRST pass had already captured. On screen that reads as
// the capture having been undone.
//
// The cumulative answer is the server's, and it has one for exactly this
// reason (runEntry.asked). The page only has to add.
func TestASecondPassDoesNotClearTheTicksTheFirstOneTook(t *testing.T) {
	js := servedScript(t)
	fn := jsFunc(t, js, "renderFileList")

	if regexp.MustCompile(`entry\.picked\s*=\s*new Set\(ev\.selected`).MatchString(fn) {
		t.Error("renderFileList assigns entry.picked from ev.selected. That is the pass " +
			"in flight, not everything this row has asked for, so a second pass's " +
			"metadata_ready unticks whatever the first pass captured")
	}
	if !strings.Contains(fn, "for (const index of ev.selected || []) entry.picked.add(index)") {
		t.Error("renderFileList does not ADD ev.selected to what this row has asked for - " +
			"and it must not assign it either (see above), so there is nothing left that " +
			"would keep a first pass's ticks on screen")
	}

	// The one thing that still empties the set, so a union cannot accumulate
	// across a reconnect: the reset that opens a run's history.
	if !strings.Contains(jsFunc(t, js, "resetRunContent"), "entry.picked.clear()") {
		t.Error("resetRunContent no longer clears entry.picked - with renderFileList only " +
			"adding, this is the only thing that can ever empty it, and without it a " +
			"reconnecting page would keep ticks the server has moved past")
	}
}

// TestTheDeferredTickIsTheOnlyThingARowSaysInWords: the ticked box already
// carries "asked for", so a span repeating "in this run" beside twenty rows
// would be a column of the same three words. What a reader genuinely cannot
// see is the tick that did NOT join the pass in flight.
func TestTheDeferredTickIsTheOnlyThingARowSaysInWords(t *testing.T) {
	js := servedScript(t)
	fn := jsFunc(t, js, "updateFileCosts")

	if !strings.Contains(fn, `row.state.textContent = deferred ? "in the next pass" : ""`) {
		t.Error("a row does not say - and only - that a tick is waiting for the next pass")
	}
	if !strings.Contains(fn, "const deferred = entry.deferred.has(index)") {
		t.Error("the row does not read the deferred set, so it cannot tell a tick that " +
			"joined the pass in flight from one that is waiting for the next")
	}
}
