package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/output"
)

// TOR-184: un-ticking a file whose frames are still being fetched means STOP,
// where un-ticking a finished one offers to clear what is on disk (TOR-183).
// One control, two acts, and the wrong one is destructive - so the whole of
// this file is about which act a box performs and how a person knows which
// before pressing it.
//
// THE ANSWER THE TICKET ASKED FOR, established against the engine rather than
// assumed: PER-FILE STOPPING DOES NOT EXIST for a file the engine has been
// handed. core.Engine.Run returns an event channel and nothing else, its only
// handle is the context it was started with, core.Engine.run derives ONE
// runCtx from that and passes the same one to every processFile goroutine, the
// traffic budget is sized once from len(selected) before the clock starts, and
// core.haltReason answers one question for the whole run. There is no back
// edge to name a file on. So the gesture stops the RUN there, and the row says
// so first.
//
// It does exist for the file no pass has been handed yet - a tick that landed
// mid-fetch and waits on the entry for the next pass (runEntry.pending). That
// one is exact: nothing has been asked of the swarm, no budget was sized, no
// goroutine holds it. Server.UntickFile is that stop, and the two are told
// apart by run_state's own "fetching" set rather than by the page guessing.
//
// The Go half drives the real routes and the real registry. The app.js half
// reads the SERVED script as text, for the reason tick_test.go's heading
// gives - this repository ships no JS runner - so it catches a control
// deleted, renamed or silently regated and cannot catch a wrong pixel. A real
// browser is what this ticket's own report used for that, on a torrent
// genuinely mid-fetch.

// untickFile posts the request an un-ticked box sends.
func untickFile(t *testing.T, base, id, file string) *http.Response {
	t.Helper()
	return post(t, base, "/runs/untick",
		`{"id":`+quote(id)+`,"file":`+quote(file)+`}`)
}

// quote is strconv.Quote under a name that says why it is here: every one of
// these bodies is assembled by hand so a test can send a malformed one, and
// an id is opaque hex a test must not assume is JSON-safe.
func quote(s string) string {
	out := `"`
	for _, r := range s {
		switch r {
		case '"', '\\':
			out += `\` + string(r)
		default:
			out += string(r)
		}
	}
	return out + `"`
}

// serverWithListerOver is a server that both parks torrents and writes to an
// output root, with a real deleter behind the clear - the combination the
// cancel-then-clear sequence needs and no existing helper offers.
func serverWithListerOver(t *testing.T, root string, runner Runner, lister Lister) (*Server, *httptest.Server) {
	t.Helper()

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	srv := newServer(context.Background(), cfg, runner, nil, realDeleter(root), lister)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return srv, ts
}

// ---------------------------------------------------------------------------
// The per-file stop, and the one place it exists.

// TestUnTickingAFileWaitingForTheNextPassDropsThatFileAlone is the honest
// per-file stop: the file was ticked while a pass was already in flight, so
// the engine has never heard of it and taking it off the entry stops exactly
// it.
//
// THE ASSERTION THAT DISCRIMINATES is that the row stops REPORTING the file.
// runEntry.asked never shrinks on its own and tickedLocked unions it with
// pending, so a drop that only emptied pending would leave the file reported
// as ticked - the box would snap back to checked and the gesture would read
// as a control that does nothing. The pass in flight and the run itself are
// checked to be untouched in the same breath, because "stopped one file" and
// "stopped the run" are the two answers this ticket exists to keep apart.
func TestUnTickingAFileWaitingForTheNextPassDropsThatFileAlone(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 4)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	parked := parkOne(t, srv, ts.URL)
	if resp := decideRun(t, ts.URL, parked.id, []string{"1"}, 5); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the first tick: status %d, want 202", resp.StatusCode)
	}
	waitFor(t, func() bool { return runs.started(manySource) })

	// The tick that lands mid-fetch and therefore waits.
	if resp := decideRun(t, ts.URL, parked.id, []string{"3"}, 5); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("a tick during the fetch: status %d, want 202", resp.StatusCode)
	}
	if got := pendingOf(srv, parked.id); len(got) != 1 || got[0] != "3" {
		t.Fatalf("the row holds pending = %v, want [3] - the rest of this test cannot "+
			"tell a drop from a tick that never landed", got)
	}
	if got := tickedOf(srv, parked.id); len(got) != 2 {
		t.Fatalf("the row reports ticked = %v, want both files - and this is what the "+
			"drop below has to change", got)
	}

	resp := untickFile(t, ts.URL, parked.id, "3")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("un-ticking the deferred file: status %d, want 200: %s",
			resp.StatusCode, readAll(t, resp))
	}

	if got := pendingOf(srv, parked.id); len(got) != 0 {
		t.Errorf("pending is still %v - the file goes on waiting for a pass nobody wants", got)
	}
	// The row's own report, which is the half a box is drawn from.
	if got := tickedOf(srv, parked.id); len(got) != 1 || got[0] != 1 {
		t.Errorf("the row reports ticked = %v, want [1] - runEntry.asked keeps every tick "+
			"this row ever took, so a drop that leaves it there re-checks the box a person "+
			"just cleared", got)
	}

	// AND THE RUN IS EXACTLY AS IT WAS, which is the whole difference between
	// this and a cancel.
	if got := runInfo(t, srv, parked.id).State; got != RunRunning {
		t.Errorf("the row is %s after dropping a file that had not started, want it still "+
			"running - dropping one file must not stop the pass in flight", got)
	}
	select {
	case <-runs.context(t, manySource).Done():
		t.Error("the run's context was cancelled by an un-tick of a file it is not " +
			"fetching - that is a cancel, and it would stop the file somebody did want")
	default:
	}
	if got := strings.Join(runs.stream(t, manySource).req.Files, ","); got != "1" {
		t.Errorf("the pass in flight was handed %q, want \"1\" still", got)
	}

	// And nothing starts when the pass ends, because there is nothing left
	// waiting - the drop is what pendingPass finds.
	runs.finish(t, manySource)
	waitFor(t, func() bool { return runInfo(t, srv, parked.id).State == RunDone })
	if got := runs.count(); got != 1 {
		t.Errorf("the runner was called %d times, want 1 - the dropped file must not "+
			"start a pass of its own after the fetch it was waiting behind", got)
	}
}

// TestUnTickingAFileTheEngineIsFetchingIsNotAPerFileStop is the limit this
// ticket had to establish rather than assume, in the one place a route can
// prove it: the server refuses, in words a page can show, and stops nothing.
//
// It is deliberately NOT "the server cancels the run for you". A page that
// asked to stop one file and got a whole torrent stopped underneath it would
// be the destructive misreading of one control this ticket exists to prevent
// - so the run-scoped act stays the run-scoped route (/runs/cancel), which
// the page calls only after the row has said what it would do.
func TestUnTickingAFileTheEngineIsFetchingIsNotAPerFileStop(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 4)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	parked := parkOne(t, srv, ts.URL)
	if resp := decideRun(t, ts.URL, parked.id, []string{"1"}, 5); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the tick: status %d, want 202", resp.StatusCode)
	}
	waitFor(t, func() bool { return runs.started(manySource) })

	resp := untickFile(t, ts.URL, parked.id, "1")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("un-ticking the file being fetched: status %d, want 409 - the request was "+
			"understood and the row is simply past the point where one file of it can be "+
			"stopped: %s", resp.StatusCode, readAll(t, resp))
	}
	body := readAll(t, resp)
	// The sentence, not just the status: a refusal a person cannot act on is
	// the thing refuseTick's own doc refuses to ship, and this one has to name
	// the act that does work.
	for _, want := range []string{"plan it was handed", "Cancelling the run", "stay on disk"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal is %q and does not say %q - it has to name what will "+
				"stop the fetch and what survives it, or it is a dead end", body, want)
		}
	}

	if got := runInfo(t, srv, parked.id).State; got != RunRunning {
		t.Errorf("the row is %s after a refused un-tick, want it still running", got)
	}
	select {
	case <-runs.context(t, manySource).Done():
		t.Error("the refused un-tick cancelled the run anyway - a page that asked to stop " +
			"one file must not have a torrent stopped underneath it")
	default:
	}
	if got := tickedOf(srv, parked.id); len(got) != 1 || got[0] != 1 {
		t.Errorf("the row reports ticked = %v, want [1] - a refusal must leave the row "+
			"claiming exactly what it is fetching", got)
	}
}

// TestRefuseUntickWalksWhatTheEngineCanActuallyDo is refuseUntick's own table
// as a unit, because three of its cases cannot be reached through the routes
// without contriving a race: a queued pass, a run that has settled, and a
// running row whose request names no files and therefore takes all of them.
//
// The sentences are the point as much as the verdict. The page draws its
// boxes from the two sets run_state carries, so a person meets these only
// when a message landed between the render and the click - and that is
// exactly when a refusal has to explain itself.
func TestRefuseUntickWalksWhatTheEngineCanActuallyDo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry *runEntry
		spec  string
		want  string
	}{
		{
			name:  "waiting for the next pass",
			entry: &runEntry{id: "a", state: RunRunning, req: RunRequest{Files: []string{"1"}}, pending: []string{"3"}},
			spec:  "3",
			want:  "",
		},
		{
			name:  "in the pass the engine holds",
			entry: &runEntry{id: "b", state: RunRunning, req: RunRequest{Files: []string{"1"}}},
			spec:  "1",
			want:  "plan it was handed",
		},
		{
			// An empty selection means every video file rather than none, so
			// on a running row this is the widest possible pass - and every
			// file in it is one the engine was handed.
			name:  "a running row that named no files at all",
			entry: &runEntry{id: "c", state: RunRunning, videos: []int{0, 1}},
			spec:  "0",
			want:  "plan it was handed",
		},
		{
			// ALLOWED SINCE TOR-197, and this case is why the verdict list
			// changed rather than grew. TOR-184 refused it over a real trap:
			// a request narrowed to nothing means ALL of them, so dropping
			// the last file would widen the run instead of stopping it.
			// UntickFile now answers that by putting the row back in
			// RunNeedsAction, where an empty selection already means
			// "nothing decided yet" - so the widening is impossible by
			// construction and there is nothing left here to refuse.
			name:  "a pass accepted and not started",
			entry: &runEntry{id: "d", state: RunQueued, req: RunRequest{Files: []string{"1", "3"}}},
			spec:  "1",
			want:  "",
		},
		{
			// The same gesture on the LAST file, which is the one the old
			// refusal was really about. Still allowed here: refuseUntick
			// answers whether the file can come out, and what emptying the
			// request MEANS is UntickFile's to arrange - see
			// TestNarrowingAQueuedPassToNothingParksItInsteadOfWideningIt.
			name:  "the last file of a pass accepted and not started",
			entry: &runEntry{id: "d2", state: RunQueued, req: RunRequest{Files: []string{"1"}}},
			spec:  "1",
			want:  "",
		},
		{
			// And the guard that replaced the refusal: a non-final state
			// that is not queueable either is a state this method has not
			// been taught, and narrowing blind there would be worse than
			// refusing. No such state exists today, which is the point of
			// asserting it - it is the case a third one would land in.
			name: "a non-final state that is neither queueable nor known",
			entry: &runEntry{id: "d3", state: RunState("some-new-state"),
				req: RunRequest{Files: []string{"1"}}},
			spec: "1",
			want: "neither waiting to start nor settled",
		},
		{
			name:  "a row that has settled",
			entry: &runEntry{id: "e", state: RunDone, asked: []string{"1"}},
			spec:  "1",
			want:  "goes with a clear",
		},
		{
			// Asked before the queued case on purpose: a replay's request
			// does carry files, so the queued sentence would call them "the
			// pass it will start with", which a replay never has.
			name:  "a replay, which fetches nothing",
			entry: &runEntry{id: "g", state: RunReplaying, req: RunRequest{Files: []string{"1"}}},
			spec:  "1",
			want:  "read back from disk",
		},
		{
			name:  "a file this row never asked for",
			entry: &runEntry{id: "f", state: RunNeedsAction},
			spec:  "2",
			want:  "not waiting to fetch",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.entry.refuseUntick(tc.spec)
			switch {
			case tc.want == "" && got != "":
				t.Errorf("refuseUntick refused with %q, want it accepted - this is the one "+
					"file no pass has been handed, and refusing it leaves the gesture with "+
					"no honest meaning at all", got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Errorf("refuseUntick answered %q, want a sentence containing %q", got, tc.want)
			}
		})
	}
}

// TestFetchingIsThePassInFlightNotEverythingTheRowHasTaken is the field a
// page's whole decision hangs on, and the reason it is sent rather than
// derived.
//
// "ticked minus deferred" is the shape it looks like, and it is wrong in one
// direction that matters: runEntry.asked keeps every file an EARLIER pass on
// this row captured, so that difference includes files nothing is fetching.
// Un-ticking one of those must not stop the run - the run is fetching
// something else - which is exactly the destructive misreading this field
// exists to prevent. The fixture has one file in each of the three places so
// the two answers differ.
func TestFetchingIsThePassInFlightNotEverythingTheRowHasTaken(t *testing.T) {
	entry := &runEntry{
		id:      "a",
		state:   RunRunning,
		videos:  []int{1, 3, 5},
		asked:   []string{"1", "3", "5"},
		req:     RunRequest{Files: []string{"3"}},
		pending: []string{"5"},
	}

	if got := entry.fetchingLocked(); len(got) != 1 || got[0] != 3 {
		t.Errorf("fetchingLocked = %v, want [3] - file 1 was taken by an earlier pass and "+
			"file 5 has not started, so neither is something a stop could reach", got)
	}
	// The comparison this is written against, shown to actually differ: a
	// derivation from the row's own two lists answers [1 3] here.
	if ticked := entry.tickedLocked(); len(ticked) != 3 {
		t.Fatalf("tickedLocked = %v, want all three - without that this test cannot show "+
			"the two answers apart", ticked)
	}

	// And nothing is fetching in any other state, however full req.Files is:
	// a queued pass is a plan nobody has started.
	for _, state := range []RunState{RunQueued, RunNeedsAction, RunReplaying, RunDone, RunCancelled, RunFailed} {
		entry.state = state
		if got := entry.fetchingLocked(); got != nil {
			t.Errorf("a %s row reports fetching = %v, want nothing - a page told those were "+
				"being fetched would offer to stop a fetch that is not happening", state, got)
		}
	}
}

// TestRunStateCarriesThePassInFlightSoAPageKnowsWhatAStopWouldStop: the page
// cannot ask which files the engine is holding, and it must not guess (see
// the test above). So it rides on run_state, for the reason every other tick
// field does - the hub replays a run's LAST run_state to a reconnecting page,
// so a field overwritten by that run's own next state can never be replayed
// stale.
func TestRunStateCarriesThePassInFlightSoAPageKnowsWhatAStopWouldStop(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 4)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	conn := dial(t, ts.URL)
	next(t, conn)

	parked := parkOne(t, srv, ts.URL)
	ev := waitForRunState(t, conn, string(RunNeedsAction))
	if _, ok := ev["fetching"]; ok {
		t.Errorf("a parked row reports fetching = %v; nothing has been decided there, let "+
			"alone handed to the engine", ev["fetching"])
	}

	if resp := decideRun(t, ts.URL, parked.id, []string{"2"}, 11); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the tick: status %d, want 202", resp.StatusCode)
	}
	waitFor(t, func() bool { return runs.started(manySource) })

	ev = waitForRunState(t, conn, string(RunRunning))
	if got := indices(t, ev["fetching"]); len(got) != 1 || got[0] != 2 {
		t.Errorf("a running row reports fetching = %v, want [2] - the pass the engine was "+
			"handed", ev["fetching"])
	}

	// A tick mid-fetch joins neither: it is deferred, and the pass in flight
	// is unchanged. Both sets on one message, which is what lets the page draw
	// one row's two files as two different acts.
	if resp := decideRun(t, ts.URL, parked.id, []string{"0"}, 11); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("a tick during the fetch: status %d, want 202", resp.StatusCode)
	}
	ev = waitForRunState(t, conn, string(RunRunning))
	if got := indices(t, ev["fetching"]); len(got) != 1 || got[0] != 2 {
		t.Errorf("the row reports fetching = %v, want [2] still - a tick cannot join the "+
			"plan the engine is working from", ev["fetching"])
	}
	if got := indices(t, ev["deferred"]); len(got) != 1 || got[0] != 0 {
		t.Errorf("the row reports deferred = %v, want [0]", ev["deferred"])
	}

	// And the moment it stops, nothing is being fetched - which is what turns
	// every one of those boxes from a stop into TOR-183's clear.
	if _, err := srv.CancelRun(parked.id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	ev = waitForRunState(t, conn, string(RunCancelled))
	if _, ok := ev["fetching"]; ok {
		t.Errorf("a cancelled row still reports fetching = %v - every box on it would go "+
			"on offering to stop a run that has already stopped", ev["fetching"])
	}
	if got := indices(t, ev["ticked"]); len(got) != 2 {
		t.Errorf("a cancelled row reports ticked = %v, want both files still - a cancel "+
			"does not un-ask for a file, and the clear that follows sits on a box that is "+
			"still checked", ev["ticked"])
	}
}

// TestACancelKeepsTheFramesAndTheClearThenTakesThem is this ticket's hard
// constraint and the sequence it makes possible, in one test because the
// second half is only meaningful if the first held.
//
// TOR-152's top-up and this ticket's own acceptance criterion 4 both rest on a
// stopped run keeping what it made: a cancel is NOT a clear. So the frames
// written before the stop are still on disk afterwards, a top-up counts them
// as captured and asks only for the rest, and the clear button that then
// legitimately appears on the same row - for the same file - takes them when
// it is pressed.
//
// The record and frames are written while the run is in flight, which is what
// core.Engine.run does for real (its saveRunRecord runs on every exit,
// "written even for a stopped run", and its .torrent is written before a
// single frame is fetched for exactly this reason). What is under test here is
// that nothing in the web layer's cancel path removes them and that the two
// affordances downstream read them.
func TestACancelKeepsTheFramesAndTheClearThenTakesThem(t *testing.T) {
	root := t.TempDir()
	const (
		// A real infohash's own length, because topUpFor validates it
		// (validInfoHash) before it reads anything - a short one is refused as
		// a malformed request and this test would prove nothing.
		hash   = "b1946ac92492d2347c6235b4d2611184b1946ac9"
		params = "eeee2222ffff6666"
		path   = "release/episode-01.mkv"
		index  = 1
	)

	lister := newFakeLister()
	lister.holds(manySource, 4)
	runs := newFakeRuns()
	srv, ts := serverWithListerOver(t, root, runs.runner, lister.list)

	parked := parkOne(t, srv, ts.URL)
	if resp := decideRun(t, ts.URL, parked.id, []string{"1"}, 3); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the tick: status %d, want 202", resp.StatusCode)
	}
	waitFor(t, func() bool { return runs.started(manySource) })

	// THE ROW AND THE RECORD HAVE TO BE THE SAME TORRENT, which is what the
	// entry only learns from its run's own core.MetadataReady - and this
	// fixture is a fake runner, so nothing sends one unless the test does.
	//
	// It is not decoration. The first falsification pass here inserted a
	// "cancel also clears the file" regression into CancelRun and the test
	// PASSED, because the entry's infoHash was empty and the inserted clear
	// had nothing to name: the mutation was inert, and a green test against
	// an inert mutation measures nothing. With the row joined to the record
	// the way a real run joins them, that same regression fails this test.
	runs.send(t, manySource, core.MetadataReady{
		Name: "Season 1", InfoHash: hash,
		Videos: fakeVideos(4),
	})
	waitFor(t, func() bool { return runInfo(t, srv, parked.id).InfoHash == hash })

	// What the run has produced so far: two of the plan's three points, and a
	// record that claims none complete - a stopped run's own shape.
	buildCachedRun(t, root, hash, params, cache.Run{
		Version:   cache.Version,
		Tool:      "test",
		CreatedAt: time.Now(),
		Source:    manySource,
		InfoHash:  hash,
		Name:      "Season 1",
		Plan:      cache.Plan{Count: 3, Start: 0.05, End: 0.95, Profile: "min-traffic", Format: "jpeg"},
		Videos:    []cache.File{{Index: index, Path: path, Bytes: 1 << 20}},
		Selected:  []int{index},
	}, framesAt(index, path, 43700, 308900))

	fileDir := fileDirOf(t, root, hash, params, index, path)
	before, err := os.ReadDir(fileDir)
	if err != nil {
		t.Fatalf("read the file's directory before the cancel: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("the fixture wrote nothing; this test cannot tell a kept frame from a " +
			"directory that was always empty")
	}

	// The stop, through the route the un-ticked box calls.
	resp := post(t, ts.URL, "/runs/cancel", `{"id":`+quote(parked.id)+`}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/cancel: status %d, want 202: %s", resp.StatusCode, readAll(t, resp))
	}
	waitFor(t, func() bool { return runInfo(t, srv, parked.id).State == RunCancelled })

	// ON DISK, which is the only form of "the frames survive" a person can
	// check, and the form a later run reads.
	after, err := os.ReadDir(fileDir)
	if err != nil {
		t.Fatalf("read the file's directory after the cancel: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the file held %d entries before the cancel and %d after - a cancel is "+
			"not a clear, and TOR-152's top-up rests on exactly this", len(before), len(after))
	}

	// AND THEY ARE STILL REUSABLE, which is the claim that matters: a top-up
	// prices what is missing off these manifests, so counting them proves the
	// stop left work a later run can build on rather than bytes on a disk.
	offer, status := getTopUp(t, ts.URL, hash, params)
	if status != http.StatusOK {
		t.Fatalf("GET the top-up offer after a cancel: status %d, want 200", status)
	}
	if offer.Refused != "" {
		t.Fatalf("the top-up is refused after a cancel: %q - a stopped run's own result set "+
			"is the case this offer exists for", offer.Refused)
	}
	if offer.Captured != 2 || offer.Remaining != 1 {
		t.Errorf("the top-up counts %d captured and %d remaining, want 2 and 1 - the frames "+
			"the stopped run wrote have to be counted, or a top-up pays for them twice",
			offer.Captured, offer.Remaining)
	}

	// AND THE CLEAR THEN TAKES THEM, on the same row, for the same file. The
	// button appearing after a cancel is not a leak of TOR-183's offer into a
	// state it was gated out of: the row has settled, nothing is fetching, and
	// deleting what is on disk is once again the only thing an un-tick could
	// mean.
	cleared := clearFile(t, ts.URL, hash, "1", "")
	if cleared.StatusCode != http.StatusOK {
		t.Fatalf("clearing the cancelled run's file: status %d, want 200: %s",
			cleared.StatusCode, readAll(t, cleared))
	}
	if _, err := os.Stat(fileDir); !os.IsNotExist(err) {
		t.Errorf("%s survived the clear (err = %v) - cancel then clear has to reach the "+
			"same frames a cancel deliberately kept", fileDir, err)
	}
	run, ok := cache.LoadRun(output.Layout{Root: root, InfoHash: hash, Params: params}.RunDir())
	if !ok {
		t.Fatal("the run record is no longer readable; a clear must not cost the set itself")
	}
	if len(run.Selected) != 1 || run.Selected[0] != index {
		t.Errorf("run.json says %v was asked for, want [%d] still - what was ASKED FOR "+
			"survives both a cancel and a clear", run.Selected, index)
	}
}

// TestAnUnTickCannotNameAFileTheTorrentDoesNotHold: the same check a tick
// goes through, and for the same reason - a page left open across two
// torrents would name an index this one does not have, which is a fact about
// the request rather than about the row, so it is a 400 and not a 409.
func TestAnUnTickCannotNameAFileTheTorrentDoesNotHold(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 4)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	parked := parkOne(t, srv, ts.URL)

	if resp := untickFile(t, ts.URL, parked.id, "99"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("un-ticking a file the torrent does not hold: status %d, want 400: %s",
			resp.StatusCode, readAll(t, resp))
	}
	if resp := untickFile(t, ts.URL, parked.id, "episode-01.mkv"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("un-ticking by path: status %d, want 400 - every tick this page makes is "+
			"an index, and a pattern is not something a box can name", resp.StatusCode)
	}
	if resp := untickFile(t, ts.URL, "nosuchrun", "1"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("un-ticking on a run this server does not hold: status %d, want 404",
			resp.StatusCode)
	}
	_ = srv
}

// tickedOf reads one entry's own report of what it has been asked to capture,
// under the server's lock - the exact list run_state carries and a box is
// drawn from.
func tickedOf(srv *Server, id string) []int {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	entry := srv.runs[id]
	if entry == nil {
		return nil
	}
	return entry.tickedLocked()
}

// ---------------------------------------------------------------------------
// The page: which act, and whether a person can tell before pressing.

// TestTheRowSaysWhichActAnUnTickWillBeBeforeItIsPressed is this ticket's
// acceptance criterion in the place it can be checked: "the two gestures must
// be distinguishable to a person BEFORE they act, not only afterwards".
//
// FOUR SENTENCES, one per verdict, because the checkbox itself cannot say
// which of them it is - it is the same box in every state, and only the row
// around it can carry the difference. The one that matters is the stop's: it
// has to say that the box acts AT ONCE (unlike the clear's, which only
// reveals a button), that it reaches the whole run, and that nothing on disk
// is lost - which is the fear the scope will produce and the fact that makes
// it survivable.
func TestTheRowSaysWhichActAnUnTickWillBeBeforeItIsPressed(t *testing.T) {
	// RETARGETED ONTO file-list.js BY TOR-195: the four verdicts, their four
	// sentences and the heading that says the stop's scope on screen are all
	// the LIST's - the middle of the three nested detail elements - and each
	// went there as a method.
	js := fileListJS(t)
	fn := jsMethod(t, js, "updateFileCosts")

	// The stop, and every part of the claim. Each phrase is looked for whole,
	// so it has to sit inside ONE JS string literal - which is why the
	// sentence is written as several short ones rather than wrapped mid-clause
	// (a concatenation splits the text this scan reads, and a phrase broken
	// across a `" + "` would be invisible here while reading perfectly on
	// screen).
	for _, want := range []string{
		"STOPS THIS TORRENT'S FETCH",
		"one file of it cannot be stopped on its own",
		"Nothing is deleted",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("the row's sentence for a file being fetched does not say %q - the "+
				"un-tick there stops a whole torrent at once, and a person who is not told "+
				"that meets it afterwards", want)
		}
	}
	// The scope is stated as a COUNT read from the same set the verdict came
	// from, so the sentence cannot claim a scope the gesture does not have.
	if !strings.Contains(fn, "entry.fetching.size > 1") ||
		!strings.Contains(fn, "files of this pass with it") {
		t.Error("the sentence does not say how many files a stop would take with it, so a " +
			"season pack's twenty-file pass reads exactly like a one-file one")
	}

	// The drop, whose whole point is that it is NOT the stop above.
	for _, want := range []string{"stops this file alone", "costs nothing"} {
		if !strings.Contains(fn, want) {
			t.Errorf("the row's sentence for a file waiting for the next pass does not say "+
				"%q - it is the one un-tick with no run-wide cost, and reading it as the "+
				"stop above is the mistake this ticket is written about", want)
		}
	}

	// TOR-183's, unchanged, and the inert case's - which is the one whose dead
	// box looks arbitrary sitting between three live ones.
	if !strings.Contains(fn, "un-tick it to be offered a clear") {
		t.Error("the clear's own sentence is gone; a finished file's un-tick changes " +
			"nothing by itself and the row is where that is said")
	}
	if !strings.Contains(fn, "nothing is fetching it now") {
		t.Error("a file an earlier pass captured says nothing about why its box is dead " +
			"while its neighbours' are live")
	}

	// AND ONE OF THEM IS ON SCREEN WITHOUT BEING HOVERED, which the four
	// titles above are not. A title is unreachable on a touch screen and to
	// anybody who never hovers, so the scope of the stop is also stated once
	// for the row in the list's own heading.
	head := jsMethod(t, js, "fileListTitle")
	if !strings.Contains(head, "entry.fetching.size > 0") {
		t.Fatal("the file list's heading no longer changes while the row is fetching, so " +
			"the only warning about a run-stopping box is in a hover title")
	}
	if !strings.Contains(head, "stops this torrent's fetch") ||
		!strings.Contains(head, "frames already taken stay") {
		t.Errorf("the heading is %q and does not say what un-ticking a fetching file does; "+
			"this is the one carrier of that a person reads without acting", head)
	}
	// It is words, not a colour or a cursor - the rule filelist_test.go's own
	// TestAnUntickableFileIsNotToldByColourAlone states for the neighbouring
	// case, and the reason this ticket adds no stylesheet rule at all.
	if strings.Contains(fn, "row.item.dataset.untick") || strings.Contains(fn, "picker-stop") {
		t.Error("the verdict has grown a hook for a stylesheet to paint. Nothing here is " +
			"told by colour: the sentences carry it, and a class would invite a rule that " +
			"says it a second, weaker way")
	}
}

// TestStoppingIsOneGestureAndSpendingIsTwo is the asymmetry this ticket
// decided, asserted so that a later reading of "be careful with destructive
// things" cannot quietly reverse it.
//
// Select all ARMS: it says what it is about to spend and waits for a second
// press, because it SPENDS - twenty files of traffic on one click - and this
// page keeps its one confirmation gesture for spending. A stop spends nothing
// and destroys nothing: the traffic already sent is not recoverable whatever
// anybody clicks next, the frames written stay on disk, and a dropped file can
// be ticked again for free. A speed bump in front of a harmless act only
// trains people to click through the one in front of the harmful one.
func TestStoppingIsOneGestureAndSpendingIsTwo(t *testing.T) {
	js := fileListJS(t)
	stop := jsMethod(t, js, "stopFetch")
	drop := jsMethod(t, js, "dropFile")

	// One press. Not an arming, not a confirm, and not a revealed button of
	// its own - the last would be the clear's shape, which works there
	// precisely because un-ticking a finished file does nothing by itself.
	for name, fn := range map[string]string{"stopFetch": stop, "dropFile": drop} {
		for _, forbidden := range []string{"confirm(", "entry.armed", "press again"} {
			if strings.Contains(fn, forbidden) {
				t.Errorf("%s contains %q - stopping is not spending, and a confirmation in "+
					"front of it is a speed bump in front of a harmless act", name, forbidden)
			}
		}
	}
	if !strings.Contains(stop, "cancelRun(entry.id)") {
		t.Error("stopFetch does not stop the run, which is the only stop core.Engine has " +
			"for a file it is already fetching")
	}
	// And the arming it is deliberately unlike is still there, so the
	// asymmetry above is a live comparison rather than a claim about code that
	// has since gone.
	if !strings.Contains(jsMethod(t, js, "syncSelectAll"), "this.pickerAll.dataset.armed") {
		t.Error("Select all no longer arms. If that changed, the argument for this " +
			"ticket's one-press stop has to be re-made rather than left standing on a " +
			"comparison with something that is gone")
	}
}

// TestTheStopPutsTheBoxBackBecauseACancelDoesNotUnAskForTheFile: the box is
// already cleared by the time tickFile runs (it is a change listener), and
// leaving it that way would be the one thing on screen claiming the row no
// longer wants this file. It does: the run asked for it, the frames it took
// stay, and run_state redraws the row moments later with the box ticked and a
// Clear frames beside it.
func TestTheStopPutsTheBoxBackBecauseACancelDoesNotUnAskForTheFile(t *testing.T) {
	fn := jsMethod(t, fileListJS(t), "stopFetch")

	if !strings.Contains(fn, "box.checked = true") {
		t.Error("stopFetch leaves the box cleared, so a row whose run has just been " +
			"stopped shows the file as not asked for - and TOR-183's clear is offered " +
			"from a box that is CHECKED, so the offer would never appear")
	}
	// And it does not touch the row's own idea of what was asked for: that is
	// the server's answer, assigned from every run_state (entry.picked), and a
	// local edit would be undone by the next message anyway - after having
	// briefly shown the wrong thing.
	if strings.Contains(fn, "entry.picked.delete") {
		t.Error("stopFetch edits entry.picked. A cancel does not un-ask for the file, and " +
			"run_state assigns that set on every message")
	}
	if strings.Contains(fn, `post("runs/untick"`) {
		t.Error("stopFetch asks the server to un-tick the file as well as cancelling the " +
			"run. UntickFile refuses a file the engine is fetching by design, so this " +
			"would be an error line beside a cancel that worked")
	}
}

// TestTheDropIsRolledBackIfThePassTookTheFileFirst: dropFile guesses
// optimistically, for the frame between the click and the socket, and the
// guess can be wrong for a real reason rather than a hypothetical one - the
// pass in flight can end in that moment and take the deferred file into
// itself (Server.pendingPass), so by the time the POST lands the file IS
// being fetched and the server refuses.
func TestTheDropIsRolledBackIfThePassTookTheFileFirst(t *testing.T) {
	fn := jsMethod(t, fileListJS(t), "dropFile")

	if !strings.Contains(fn, `post("runs/untick", { id: entry.id, file: String(index) })`) {
		t.Fatal("dropFile no longer asks the server to take the file out of the next pass")
	}
	if !strings.Contains(fn, "entry.picked.delete(index)") ||
		!strings.Contains(fn, "entry.deferred.delete(index)") {
		t.Error("dropFile does not clear the box optimistically, so the one feedback there " +
			"is between the click and the socket is nothing at all")
	}
	if !strings.Contains(fn, "entry.picked.add(index)") ||
		!strings.Contains(fn, "entry.deferred.add(index)") {
		t.Error("a refused drop is not put back, so the box would say a file is not wanted " +
			"while the engine is holding it")
	}
	// The rollback after the failure, not before it.
	deleted := strings.Index(fn, "entry.picked.delete(index)")
	restored := strings.Index(fn, "entry.picked.add(index)")
	if deleted < 0 || restored < 0 || deleted > restored {
		t.Errorf("the optimistic clear is at %d and the rollback at %d; the rollback "+
			"belongs in the catch, after the guess it undoes", deleted, restored)
	}
}

// TestThePageReadsThePassInFlightFromTheServer is the guard against the thing
// that would break this feature invisibly: a page that derived the fetching
// set from what it already had would answer "stop the run" for a file an
// earlier pass captured, and the person would lose the fetch that was going.
func TestThePageReadsThePassInFlightFromTheServer(t *testing.T) {
	// Read out of the run_state handler itself since TOR-191, and out of the
	// state module's own reset - and run for real, against a message carrying
	// all three sets and then a reconnect, by
	// TestThePassInFlightIsReadFromTheServerAndNotDerived and
	// TestAReconnectLeavesNoStalePassBehind in eventstate_test.go. Those two
	// are what can tell a WRONG answer from missing text; these two are what
	// name the line that would have to change to produce one.
	if !strings.Contains(jsFunc(t, eventsJS(t), "applyRunState"),
		"entry.fetching = new Set(ev.fetching || [])") {
		t.Fatal("the page no longer takes the pass in flight from run_state. Deriving it " +
			"from picked minus deferred includes every file an earlier pass captured, and " +
			"un-ticking one of those would stop a run fetching something else")
	}
	// Emptied with the rest of a run's content, so a reconnecting page cannot
	// offer to stop a fetch from a history it is in the middle of re-reading.
	//
	// COMMENTS STRIPPED, which TOR-191's falsification run is the reason for:
	// commenting the line out satisfied this check word for word, because a
	// comment still contains the substring. The executable half of the same
	// subject is TestResetRunStateEmptiesEverythingAReplayWillStateAgain,
	// which calls the function instead of reading it.
	if !strings.Contains(stripJSComments(jsFunc(t, stateJS(t), "resetRunState")),
		"entry.fetching.clear()") {
		t.Error("resetRunState no longer clears entry.fetching - it is assigned from " +
			"every run_state, so this is what stops a stale one surviving a reconnect")
	}
}

// TestTheStopStillLetsTheEngineEndOnItsOwnTerms is a small assertion with a
// large subject: the page's stop is /runs/cancel, and that route deliberately
// does not kill anything. CancelRun cancels the run's context and lets the
// run write what it has and close its own stream, which is what leaves the
// frames on disk for the clear and the top-up to find.
//
// It is asserted on the ROUTE rather than on the outcome, because the outcome
// belongs to core and is proved where the frames are - see
// TestACancelKeepsTheFramesAndTheClearThenTakesThem above. This only fixes
// which door the page knocks on.
func TestTheStopStillLetsTheEngineEndOnItsOwnTerms(t *testing.T) {
	// cancelRun stays in app.js, deliberately, and TOR-195 is what makes that
	// worth saying: THREE controls now reach it from three different modules -
	// the row's own ✕ (run-table.js's service), the detail header's Cancel
	// (run-detail.js's) and an un-tick mid-fetch (file-list.js's stopFetch) -
	// so it is injected as one service rather than spelled three times, and
	// there is still exactly one place a cancel is sent from.
	if !strings.Contains(jsFunc(t, appJS(t), "cancelRun"), `post("runs/cancel", { id })`) {
		t.Fatal("cancelRun no longer posts to /runs/cancel, which is the one route that " +
			"stops a run by cancelling its context rather than by killing it")
	}
	for _, mod := range []struct{ name, src string }{
		{"run-table.js", runTableJS(t)},
		{"run-detail.js", runDetailJS(t)},
		{"file-list.js", fileListJS(t)},
	} {
		if !strings.Contains(mod.src, `"cancelRun"`) {
			t.Errorf("%s does not name cancelRun among the services it refuses to run "+
				"without - a control that stops a torrent would call an injected function "+
				"that was never injected", mod.name)
		}
		if strings.Contains(mod.src, `post("runs/cancel"`) {
			t.Errorf("%s sends the cancel itself instead of going through the one injected "+
				"service - three spellings of one destructive request is three places to "+
				"get it wrong", mod.name)
		}
	}
	// core.Event is what the run ends on either way; the flag the server reads
	// to tell a stop from a completion is the one CancelRun sets, and pump
	// reads core.StopCancelled for the same question. Named here so a change
	// to either is visible from the page's side of the boundary.
	if _, ok := any(core.StopCancelled).(core.StopReason); !ok {
		t.Error("core.StopCancelled is no longer a StopReason; pump reads it to tell a " +
			"stopped run from a finished one, which is what makes a cancelled row settle")
	}
}

// TestNarrowingAQueuedPassToNothingParksItInsteadOfWideningIt is TOR-197 at
// the one boundary the trap lives on, and it is written as two steps because
// only the second one is dangerous.
//
// TOR-184 refused this gesture rather than get it subtly wrong, and its
// reason was real: an empty RunRequest.Files means EVERY video file
// (asksEveryVideo), so dropping the LAST box would not narrow the pass to
// nothing - it would widen it from one file to every video the torrent
// holds, silently, in the direction that spends the most traffic.
//
// The answer is not a rule about counting boxes. asksEveryVideo reads the
// STATE as well as the length, and in RunNeedsAction an empty selection
// already means the opposite - nothing decided yet - which is exactly what
// no boxes ticked IS. So the row goes back to where it was before the first
// box was pressed and the widening is impossible by construction.
//
// THE QUEUE SLOT IS THE HALF THAT COULD STILL WIDEN IT. dispatch() pops
// s.waiting by position and never reads state, so a row left in the queue
// would be started later with the empty request this narrowing created -
// the same widening, one dispatch further along. That is what the runner
// count at the end is for.
func TestNarrowingAQueuedPassToNothingParksItInsteadOfWideningIt(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 4)
	lister.holds(otherManySource, 4)
	runs := newFakeRuns()

	cfg := DefaultConfig()
	// One slot, so the second torrent's pass is accepted and NOT started -
	// which is the only state this ticket is about.
	cfg.MaxActiveTorrents = 1
	srv, ts := newTestServerWithConfigAndLister(t, cfg, runs.runner, lister.list)

	// BOTH TORRENTS ARE PARKED FIRST, and the order is forced rather than
	// chosen: listThenRun does the metadata listing INSIDE the slot, so a
	// second torrent posted while the first is fetching never reaches
	// needs-action at all - it waits in the queue with nothing decided. A
	// parked torrent does not hold the slot (TOR-67), which is what lets
	// both of them park before either is decided.
	holding := parkOne(t, srv, ts.URL)
	parked := parkOneOf(t, srv, ts.URL, otherManySource)

	// Now the slot's occupant, so nothing below can start.
	if resp := decideRun(t, ts.URL, holding.id, []string{"0"}, 5); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("deciding the run that takes the slot: status %d, want 202", resp.StatusCode)
	}
	waitFor(t, func() bool { return runs.started(manySource) })

	// And the one under test: decided, so it carries a real selection, and
	// queued, so the engine has been handed nothing.
	if resp := decideRun(t, ts.URL, parked.id, []string{"1", "3"}, 5); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("deciding the queued run: status %d, want 202", resp.StatusCode)
	}
	waitFor(t, func() bool { return runInfo(t, srv, parked.id).State == RunQueued })

	// STEP ONE: two files to one. Not the dangerous case - the request is
	// still non-empty afterwards, so nothing about asksEveryVideo changes.
	if resp := untickFile(t, ts.URL, parked.id, "1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("narrowing a queued pass from two files to one: status %d, want 200: %s",
			resp.StatusCode, readAll(t, resp))
	}
	if got := requestedOf(srv, parked.id); len(got) != 1 || got[0] != "3" {
		t.Fatalf("the queued pass asks for %v, want [3] - the rest of this test cannot tell "+
			"a narrowing from a request that never changed", got)
	}
	if got := runInfo(t, srv, parked.id).State; got != RunQueued {
		t.Errorf("the row is %s after narrowing to one file, want it still queued - it has a "+
			"selection and is waiting for the slot, which is unchanged", got)
	}

	// STEP TWO: one file to none, which is what TOR-184 refused.
	if resp := untickFile(t, ts.URL, parked.id, "3"); resp.StatusCode != http.StatusOK {
		t.Fatalf("narrowing a queued pass down to no files: status %d, want 200: %s",
			resp.StatusCode, readAll(t, resp))
	}

	// THE RUN'S OWN RECORD, read back, which is what the criterion asks for:
	// what it would actually be asked for, not what a page thinks it asked.
	if got := requestedOf(srv, parked.id); len(got) != 0 {
		t.Errorf("the row still asks for %v after the last box came out", got)
	}
	if got := runInfo(t, srv, parked.id).State; got != RunNeedsAction {
		t.Errorf("the row is %s after its last file came out, want %s - an empty request "+
			"means every video file in any other state, so parking it is what keeps the "+
			"narrowing honest", got, RunNeedsAction)
	}
	if asksEverything(srv, parked.id) {
		t.Error("the row reports asksEveryVideo after being narrowed to nothing - this is " +
			"the widening TOR-184 refused the whole gesture to avoid: one file becomes " +
			"every video the torrent holds")
	}
	if got := tickedOf(srv, parked.id); len(got) != 0 {
		t.Errorf("the row reports ticked = %v, want none - every box a person cleared would "+
			"otherwise snap back to checked", got)
	}

	// AND IT IS OUT OF THE QUEUE. Finishing the run that holds the slot is
	// what makes dispatch look for the next waiter; if the parked row were
	// still in s.waiting it would be started here, with the empty request -
	// which is the widening again, one dispatch later.
	runs.finish(t, manySource)
	waitFor(t, func() bool { return runInfo(t, srv, holding.id).State == RunDone })
	if runs.started(otherManySource) {
		t.Error("the narrowed row started as soon as the slot freed - it was left in the " +
			"queue, so dispatch handed the engine a request that means every video file")
	}
	if got := runs.count(); got != 1 {
		t.Errorf("the runner was called %d times, want 1 - only the run that held the slot "+
			"should ever have started", got)
	}

	// The person can still pick again, which is the whole point of parking
	// rather than cancelling: nothing was spent and nothing was lost.
	if resp := decideRun(t, ts.URL, parked.id, []string{"2"}, 5); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("deciding the parked row a second time: status %d, want 202", resp.StatusCode)
	}
	waitFor(t, func() bool { return runs.started(otherManySource) })
	if got := strings.Join(runs.stream(t, otherManySource).req.Files, ","); got != "2" {
		t.Errorf("the pass that finally started was handed %q, want \"2\" - the file the "+
			"person chose after narrowing, and nothing else", got)
	}
}

// requestedOf is what the row would actually be asked for - RunRequest.Files
// itself, read back under the lock that writes it.
//
// Distinct from pendingOf and tickedOf on purpose, and TOR-197 is why the
// distinction matters: pending is what waits BEHIND a pass, ticked is every
// file the row has ever been asked for, and this is the pass the engine will
// be handed. The trap this ticket answers lives in this field alone - empty
// here means every video file, in every state but one.
func requestedOf(srv *Server, id string) []string {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	entry := srv.runs[id]
	if entry == nil {
		return nil
	}
	return append([]string(nil), entry.req.Files...)
}

// asksEverything is the predicate the whole ticket turns on, read through the
// server rather than recomputed in the test - a copy of the rule here would
// agree with itself and not with the code.
func asksEverything(srv *Server, id string) bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	entry := srv.runs[id]
	if entry == nil {
		return false
	}
	return entry.asksEveryVideo()
}

// parkOneOf is parkOne for a named source, so one test can park two torrents
// and have the second one queue behind the first.
func parkOneOf(t *testing.T, srv *Server, base, source string) startedRun {
	t.Helper()

	run := startRun(t, base, source)
	waitFor(t, func() bool { return runInfo(t, srv, run.id).State == RunNeedsAction })
	return run
}
