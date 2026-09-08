package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/swarm"
)

// The two torrents every test here works with: one that holds several video
// files and therefore parks, and one that holds a single file and therefore
// does not.
const (
	manySource = "magnet:?xt=urn:btih:many"
	// A SECOND multi-video torrent, so one test can park two and have the
	// second queue behind the first (TOR-197 needs a pass that is accepted
	// and not started, which one torrent alone cannot produce).
	otherManySource = "magnet:?xt=urn:btih:othermany"
	oneSource       = "magnet:?xt=urn:btih:one"
)

// fakeLister stands in for a metadata pass: it answers what a torrent holds
// without a torrent, a port or a swarm, the same way fakeRuns stands in for
// the engine. A source it was never told about answers an error, which is
// what a real listing does for a source it cannot open.
type fakeLister struct {
	mu       sync.Mutex
	contents map[string]core.Contents
	failures map[string]error
	calls    []string
}

func newFakeLister() *fakeLister {
	return &fakeLister{
		contents: make(map[string]core.Contents),
		failures: make(map[string]error),
	}
}

// holds says this source is a torrent with n video files in it.
func (f *fakeLister) holds(source string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contents[source] = core.Contents{
		Name:     "release for " + source,
		InfoHash: fmt.Sprintf("%040x", len(source)),
		Videos:   fakeVideos(n),
	}
}

// holdsMixed says this source is a torrent with n video files in it AND the
// extras named - a .nfo, a sample, whatever a real release carries beside the
// episodes. The extras go into Files only, never Videos, which is exactly the
// shape core.List produces (TOR-180): Videos is what swarm.SelectVideos would
// keep, Files is everything the torrent holds.
func (f *fakeLister) holdsMixed(source string, n int, extras ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	videos := fakeVideos(n)
	all := append([]swarm.FileInfo(nil), videos...)
	for i, path := range extras {
		all = append(all, swarm.FileInfo{
			Index: n + i, Path: path, Length: int64(i+1) << 10,
		})
	}
	f.contents[source] = core.Contents{
		Name:     "release for " + source,
		InfoHash: fmt.Sprintf("%040x", len(source)),
		Videos:   videos,
		Files:    all,
	}
}

// fails says listing this source does not work.
func (f *fakeLister) fails(source string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[source] = err
}

func (f *fakeLister) list(ctx context.Context, source string) (core.Contents, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, source)
	if err := f.failures[source]; err != nil {
		return core.Contents{}, err
	}
	contents, ok := f.contents[source]
	if !ok {
		return core.Contents{}, fmt.Errorf("no such torrent: %s", source)
	}
	return contents, nil
}

// count is how many metadata passes have happened, which is what proves a
// decided torrent does not pay for a second one.
func (f *fakeLister) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeVideos is a torrent's video files, indexed the way swarm.Select names
// them.
func fakeVideos(n int) []swarm.FileInfo {
	out := make([]swarm.FileInfo, n)
	for i := range out {
		out[i] = swarm.FileInfo{
			Index:  i,
			Path:   fmt.Sprintf("release/episode-%02d.mkv", i),
			Length: int64(i+1) << 20,
		}
	}
	return out
}

// decideRun posts one picker's answer.
func decideRun(t *testing.T, base, id string, files []string, count int) *http.Response {
	t.Helper()

	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = strconv.Quote(f)
	}
	body := fmt.Sprintf(`{"id":%s,"files":[%s],"count":%d}`,
		strconv.Quote(id), joinComma(quoted), count)
	return post(t, base, "/runs/decide", body)
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

// parkOne adds the multi-file torrent and waits for it to park, which is
// where most of these tests begin.
func parkOne(t *testing.T, srv *Server, base string) startedRun {
	t.Helper()

	run := startRun(t, base, manySource)
	waitFor(t, func() bool { return runInfo(t, srv, run.id).State == RunNeedsAction })
	return run
}

// TestAParkedTorrentDoesNotHoldTheQueueSlot is TOR-67's acceptance criterion,
// and the whole reason the metadata pass is a phase of a run rather than a
// run of its own.
//
// A torrent parked waiting for a person could easily hold the single slot -
// s.running is taken before the listing starts, because a listing binds the
// same pinned port a run does - and then the wait, which has no bound and no
// timer, would stop every other torrent on the server. So the proof is not
// that the first torrent parks: it is that a torrent added AFTERWARDS runs to
// completion while the first one is still waiting.
func TestAParkedTorrentDoesNotHoldTheQueueSlot(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 3)
	lister.holds(oneSource, 1)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	parked := parkOne(t, srv, ts.URL)

	// Added while the first one waits, and not queued behind it: it takes the
	// slot, runs, and finishes.
	second := startRun(t, ts.URL, oneSource)
	waitFor(t, func() bool { return runs.started(oneSource) })
	runs.finish(t, oneSource)
	waitFor(t, func() bool { return runInfo(t, srv, second.id).State == RunDone })

	if got := runInfo(t, srv, parked.id).State; got != RunNeedsAction {
		t.Fatalf("the first torrent is %s after the second finished, want %s", got, RunNeedsAction)
	}
	if runs.started(manySource) {
		t.Fatal("the parked torrent reached the runner without anyone choosing files")
	}
	if runs.count() != 1 {
		t.Fatalf("the runner was called %d times, want 1 - only the single-file torrent ran", runs.count())
	}
}

// TestDecidingReQueuesTheSameEntry: ticking boxes does not start a second
// run. The entry that parked is the entry that runs - one torrent, one row,
// one history - and the runner is handed exactly the files that were ticked,
// nothing more.
func TestDecidingReQueuesTheSameEntry(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 4)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	parked := parkOne(t, srv, ts.URL)

	resp := decideRun(t, ts.URL, parked.id, []string{"1", "3"}, 7)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/decide: status %d, want 202", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["id"] != parked.id {
		t.Fatalf("the decision answered run %v, want the parked run %s", body["id"], parked.id)
	}
	// The slot was free, and the answer says so rather than guessing: the
	// decision dispatches before it returns, the same way starting a run
	// does.
	if body["state"] != string(RunRunning) {
		t.Errorf("the decision answered state %v, want %s", body["state"], RunRunning)
	}

	waitFor(t, func() bool { return runs.started(manySource) })
	got := runs.stream(t, manySource).req
	if len(got.Files) != 2 || got.Files[0] != "1" || got.Files[1] != "3" {
		t.Fatalf("the runner was given files %v, want exactly [1 3]", got.Files)
	}
	if got.Count != 7 {
		t.Errorf("the runner was given a count of %d, want the 7 the picker sent", got.Count)
	}

	// One torrent, one entry: deciding must not mint a second run.
	if entries := srv.snapshot(); len(entries) != 1 {
		t.Fatalf("the registry holds %d runs after a decision, want 1: %v", len(entries), entries)
	}
	if runInfo(t, srv, parked.id).State != RunRunning {
		t.Fatalf("the decided run is %s, want it running", runInfo(t, srv, parked.id).State)
	}
	// And it does not pay for the metadata twice.
	if lister.count() != 1 {
		t.Errorf("the torrent was listed %d times, want once", lister.count())
	}
}

// TestASingleVideoStartsWithoutParking: there is nothing to ask about when a
// torrent holds one video file, so it goes straight from the metadata pass
// into the run - without releasing the slot in between, which is what stops a
// torrent queued behind it from jumping in front of one that has already paid
// for its metadata.
func TestASingleVideoStartsWithoutParking(t *testing.T) {
	lister := newFakeLister()
	lister.holds(oneSource, 1)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	conn := dial(t, ts.URL)
	next(t, conn) // the idle connection marker

	run := startRun(t, ts.URL, oneSource)

	// Read the stream first, so a torrent that parks says so here rather
	// than only as a runner that is never called: queued, running for the
	// listing, running again once the runner had it - and no needs_action
	// anywhere in it.
	for _, want := range []string{"run_state", "run_state", "run_state"} {
		ev := next(t, conn)
		if ev["type"] != want {
			t.Fatalf("a single-file torrent published %v, want a %s", ev, want)
		}
		if ev["state"] == string(RunNeedsAction) {
			t.Fatalf("a single-file torrent parked: %v", ev)
		}
	}

	waitFor(t, func() bool { return runs.started(oneSource) })
	if got := runInfo(t, srv, run.id).State; got != RunRunning {
		t.Fatalf("a single-file torrent is %s, want it running", got)
	}
}

// TestAListingThatFailsFailsTheRunAndFreesTheSlot: the listing is the run's
// first phase, so a listing that cannot happen is a run that failed - said on
// that run's own stream, exactly the way a runner that refuses to start is -
// and it must not strand the slot it was holding.
func TestAListingThatFailsFailsTheRunAndFreesTheSlot(t *testing.T) {
	lister := newFakeLister()
	lister.fails(manySource, errors.New("no metadata after 60s"))
	lister.holds(oneSource, 1)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	conn := dial(t, ts.URL)
	next(t, conn)

	broken := startRun(t, ts.URL, manySource)
	waitFor(t, func() bool { return runInfo(t, srv, broken.id).State == RunFailed })

	if got := runInfo(t, srv, broken.id).Err; got != "no metadata after 60s" {
		t.Errorf("the failed run reports %q, want the listing's own error", got)
	}
	for _, want := range []string{"run_state", "run_state", "failed", "run_state"} {
		if ev := next(t, conn); ev["type"] != want {
			t.Fatalf("a failed listing published %v, want a %s", ev, want)
		}
	}

	// The slot is free: the next torrent runs without anything ending first.
	second := startRun(t, ts.URL, oneSource)
	waitFor(t, func() bool { return runs.started(oneSource) })
	if got := runInfo(t, srv, second.id).State; got != RunRunning {
		t.Fatalf("the next torrent is %s after a failed listing, want it running", got)
	}
}

// TestCancellingAParkedTorrentEndsIt: a parked torrent waits forever by
// design, so cancelling is the only way out other than deciding - and it has
// to work on an entry that holds no context at all. entry.cancel is nil for
// one (listThenRun dropped it when it gave the slot back), so a cancel that
// called it would panic instead of cancelling.
func TestCancellingAParkedTorrentEndsIt(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 3)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	parked := parkOne(t, srv, ts.URL)

	resp := post(t, ts.URL, "/runs/cancel", `{"id":`+strconv.Quote(parked.id)+`}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/cancel on a parked torrent: status %d, want 202", resp.StatusCode)
	}
	if got := runInfo(t, srv, parked.id).State; got != RunCancelled {
		t.Fatalf("the cancelled torrent is %s, want %s", got, RunCancelled)
	}
	if runs.count() != 0 {
		t.Errorf("the runner was called %d times for a cancelled torrent, want 0", runs.count())
	}

	// And it is over for good: a decision arriving afterwards is refused
	// rather than reviving it.
	if resp := decideRun(t, ts.URL, parked.id, []string{"0"}, 0); resp.StatusCode != http.StatusConflict {
		t.Errorf("deciding a cancelled torrent: status %d, want 409", resp.StatusCode)
	}
}

// TestClosingEndsAParkedTorrent: a parked torrent is in neither the queue nor
// the slot, so Close has to find it in the registry itself. Left alone it
// would sit in needs-action on a stopped server, waiting for a decision no
// route is left to accept - and its staged upload would never be removed.
func TestClosingEndsAParkedTorrent(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 2)
	srv, ts := newTestServerWithLister(t, newFakeRuns().runner, lister.list)

	parked := parkOne(t, srv, ts.URL)

	if err := srv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := runInfo(t, srv, parked.id).State; got != RunCancelled {
		t.Fatalf("a parked torrent is %s after the server closed, want %s", got, RunCancelled)
	}
}

// TestDecideAnswersTheRequestItWasGiven walks the four ways a tick is refused
// and the one way it is taken.
//
// TOR-181 rewrote the second row of this table rather than adding to it. The
// refusal it used to assert was "this run is not waiting to be told
// anything", tested against a RUNNING torrent - and a running torrent is now
// exactly a row a tick may grow (into its next pass, DecideRun), so the case
// as written no longer describes a refusal at all. What replaced it is the
// state that genuinely has no run left to grow: one that has finished, where
// a further file is a new run with a new ceiling and must not be spent on a
// tick.
func TestDecideAnswersTheRequestItWasGiven(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 3)
	lister.holds(oneSource, 1)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	parked := parkOne(t, srv, ts.URL)
	// A second torrent, run to completion, to tick at.
	settled := startRun(t, ts.URL, oneSource)
	waitFor(t, func() bool { return runInfo(t, srv, settled.id).State == RunRunning })
	runs.finish(t, oneSource)
	waitFor(t, func() bool { return runInfo(t, srv, settled.id).State == RunDone })

	for _, tc := range []struct {
		name  string
		id    string
		files []string
		want  int
	}{
		{"a run this server does not hold", "deadbeef", []string{"0"}, http.StatusNotFound},
		{"a run that has settled, so there is nothing left to grow", settled.id, []string{"0"}, http.StatusConflict},
		{"an empty selection", parked.id, nil, http.StatusBadRequest},
		{"a file this torrent does not have", parked.id, []string{"9"}, http.StatusBadRequest},
		{"a file named by something that is not an index", parked.id, []string{"*.mkv"}, http.StatusBadRequest},
		{"the files it actually holds", parked.id, []string{"0", "2"}, http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := decideRun(t, ts.URL, tc.id, tc.files, 0)
			if resp.StatusCode != tc.want {
				t.Fatalf("POST /runs/decide with %v: status %d, want %d",
					tc.files, resp.StatusCode, tc.want)
			}
		})
	}

	// Every refusal above left the torrent exactly where it was, which is
	// what made the last case still possible.
	if got := runInfo(t, srv, parked.id).State; got != RunQueued && got != RunRunning {
		t.Fatalf("the decided torrent is %s, want it queued or running", got)
	}
}

// TestAPageThatConnectsLaterReplaysTheFileList: needs_action is a record on
// the run's own stream, so it lands in that run's backlog and a page opened
// long afterwards rebuilds the picker from the replay - with no extra request
// and nothing to ask for.
func TestAPageThatConnectsLaterReplaysTheFileList(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 3)
	srv, ts := newTestServerWithLister(t, newFakeRuns().runner, lister.list)

	parked := parkOne(t, srv, ts.URL)

	// Nobody was watching while any of that happened.
	conn := dial(t, ts.URL)
	if got := next(t, conn); got["type"] != "run_state" {
		t.Fatalf("the replay opens with %v, want the connection marker", got)
	}

	var files map[string]any
	for _, want := range []string{"run_state", "run_state", "needs_action", "run_state"} {
		ev := next(t, conn)
		if ev["type"] != want {
			t.Fatalf("replaying the parked torrent: got %v, want a %s", ev, want)
		}
		if ev["run"] != parked.id {
			t.Fatalf("replayed %v, which belongs to another run", ev)
		}
		if want == "needs_action" {
			files = ev
		}
	}

	videos, _ := files["videos"].([]any)
	if len(videos) != 3 {
		t.Fatalf("the replayed file list holds %d files, want 3: %v", len(videos), files["videos"])
	}
	first, _ := videos[0].(map[string]any)
	if first["index"] != float64(0) || first["path"] != "release/episode-00.mkv" {
		t.Errorf("the first replayed file is %v, want index 0 of the fixture", first)
	}
	if first["length"] != float64(1<<20) {
		t.Errorf("the first replayed file is %v bytes, want the fixture's size", first["length"])
	}
	if files["name"] == nil || files["infohash"] == nil {
		t.Errorf("the file list carries no name or infohash: %v", files)
	}

	// It is a needs_action, deliberately not a metadata_ready: nothing has
	// been captured, and a page told otherwise would show a run in progress.
	if files["selected"] != nil {
		t.Errorf("the file list carries a \"selected\" list: %v", files)
	}
}

// TestTheParkedFileListCarriesEveryFileNotOnlyTheVideos is TOR-180 at the one
// screen where a person is actually deciding something, which is the last
// place that should show only part of what the torrent holds: the .nfo and
// the sample are how they find out that the file they were after is not a
// film at all.
//
// Both keys, checked together, because the two lists do different jobs and
// widening the wrong one is the failure that would look like success:
// "videos" is what a tick may name and the only list a decision is validated
// against (runEntry.holdsFile), so a non-video index appearing in it would be
// a tick this server refuses.
func TestTheParkedFileListCarriesEveryFileNotOnlyTheVideos(t *testing.T) {
	lister := newFakeLister()
	lister.holdsMixed(manySource, 2, "release/release.nfo", "release/sample.mkv")
	srv, ts := newTestServerWithLister(t, newFakeRuns().runner, lister.list)

	parked := parkOne(t, srv, ts.URL)

	conn := dial(t, ts.URL)
	if got := next(t, conn); got["type"] != "run_state" {
		t.Fatalf("the replay opens with %v, want the connection marker", got)
	}
	var record map[string]any
	for range 4 {
		ev := next(t, conn)
		if ev["type"] == "needs_action" && ev["run"] == parked.id {
			record = ev
			break
		}
	}
	if record == nil {
		t.Fatal("the parked run's history carries no needs_action record")
	}

	all, _ := record["files"].([]any)
	if len(all) != 4 {
		t.Fatalf("the record's files hold %d entries, want all 4 the torrent holds: %v",
			len(all), record["files"])
	}
	paths := map[string]bool{}
	for _, entry := range all {
		f, _ := entry.(map[string]any)
		path, _ := f["path"].(string)
		paths[path] = true
		if f["length"] == nil {
			t.Errorf("a listed file carries no length: %v - the list shows a size per row", f)
		}
	}
	for _, want := range []string{"release/release.nfo", "release/sample.mkv"} {
		if !paths[want] {
			t.Errorf("the record's files do not hold %s: %v", want, record["files"])
		}
	}

	videos, _ := record["videos"].([]any)
	if len(videos) != 2 {
		t.Fatalf("the record's videos hold %d entries, want only the 2 capturable ones "+
			"- a tick may name nothing else, and this server refuses anything else "+
			"(runEntry.holdsFile): %v", len(videos), record["videos"])
	}
	for _, entry := range videos {
		v, _ := entry.(map[string]any)
		if v["path"] == "release/release.nfo" || v["path"] == "release/sample.mkv" {
			t.Errorf("the videos list names %v, which cannot be captured", v["path"])
		}
	}
}

// TestARequestThatAlreadyNamesFilesNeverPaysForAListing: Regenerate, and
// every decided selection, already say which file to take. Paying for a
// metadata pass to offer a choice nobody is waiting to make would only delay
// the run.
func TestARequestThatAlreadyNamesFilesNeverPaysForAListing(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 5)
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, lister.list)

	resp := post(t, ts.URL, "/runs",
		`{"source":`+strconv.Quote(manySource)+`,"files":["2"],"count":3}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs with a selection: status %d, want 202", resp.StatusCode)
	}
	id, _ := decodeBody(t, resp)["id"].(string)

	// No waiting for it: a run that needs no listing reaches the runner
	// inside dispatch, which StartRun calls synchronously, so the runner has
	// already been called by the time the POST answers. A listing would have
	// pushed that into a goroutine and left this false.
	if !runs.started(manySource) {
		t.Fatal("a request naming its own files had not reached the runner when the POST answered - it was listed first")
	}
	if lister.count() != 0 {
		t.Fatalf("a request naming its own files was listed %d times, want 0", lister.count())
	}
	if got := runInfo(t, srv, id).State; got != RunRunning {
		t.Fatalf("a request naming its own files is %s, want it running", got)
	}
}

// TestWithoutAListerNothingParks is the other half of Lister's contract: a
// server built without one behaves exactly as it did before TOR-67, which is
// what keeps every other test in this package on the old path.
func TestWithoutAListerNothingParks(t *testing.T) {
	runs := newFakeRuns()
	srv, ts := newTestServerWithLister(t, runs.runner, nil)

	run := startRun(t, ts.URL, manySource)
	waitFor(t, func() bool { return runs.started(manySource) })
	if got := runInfo(t, srv, run.id).State; got != RunRunning {
		t.Fatalf("with no lister the run is %s, want it running", got)
	}
}

// TestNeedsActionIsNotAFinalState guards what the state means to everything
// that reads it: trim must never drop a torrent that is only waiting to be
// told what to take, and a client must not file it away as over.
func TestNeedsActionIsNotAFinalState(t *testing.T) {
	if RunNeedsAction.final() {
		t.Fatal("needs-action reports itself final; trim would drop a torrent still waiting for a person")
	}
}

// TestAParkedTorrentIsNotActive: nothing more is coming for it until someone
// acts, which is exactly what run_state's active flag has always meant.
func TestAParkedTorrentIsNotActive(t *testing.T) {
	lister := newFakeLister()
	lister.holds(manySource, 2)
	srv, ts := newTestServerWithLister(t, newFakeRuns().runner, lister.list)

	parked := parkOne(t, srv, ts.URL)

	conn := dial(t, ts.URL)
	next(t, conn)
	for i := 0; i < 4; i++ {
		ev := next(t, conn)
		if ev["type"] == "run_state" && ev["state"] == string(RunNeedsAction) {
			if ev["active"] != false {
				t.Fatalf("the parked torrent reports active=%v, want false: %v", ev["active"], ev)
			}
			if ev["run"] != parked.id {
				t.Fatalf("the parked run_state belongs to %v, want %s", ev["run"], parked.id)
			}
			return
		}
	}
	t.Fatal("no run_state announced the parking")
}
