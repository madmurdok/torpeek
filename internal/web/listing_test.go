package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/core"
)

// writeRun writes a run record directly where the engine would -
// <root>/<infohash>/<params>/run.json - the same layout cache.LoadRun and
// the walk both read.
func writeRun(t *testing.T, root, infohash, params string, run cache.Run) {
	t.Helper()

	dir := filepath.Join(root, infohash, params)
	if err := cache.SaveRun(dir, run); err != nil {
		t.Fatalf("write run.json at %s: %v", dir, err)
	}
}

// listRuns calls GET /runs and decodes the merged list.
func listRuns(t *testing.T, base string) []RunSummary {
	t.Helper()

	resp := get(t, base, "/runs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /runs: status %d, want 200", resp.StatusCode)
	}

	var body struct {
		Runs []RunSummary `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET /runs: %v", err)
	}
	return body.Runs
}

// TestListsRunsFromDiskAndTheLiveQueue is TOR-54's acceptance criterion,
// word for word: a run a PREVIOUS process left on disk - proving this reads
// the disk rather than memory - alongside a live queued one, against a
// fresh server pointed at that output root.
func TestListsRunsFromDiskAndTheLiveQueue(t *testing.T) {
	root := t.TempDir()
	const diskHash = "aaaa000000000000000000000000000000000a"

	// A run tree written before this server process existed. Nothing here
	// goes through the registry - it is the previous process's disk state.
	writeRun(t, root, diskHash, "deadbeef", cache.Run{
		Version:   cache.Version,
		Tool:      "0.5.0",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Source:    "magnet:?xt=urn:btih:" + diskHash,
		InfoHash:  diskHash,
		Name:      "Previous Process Run",
		Videos:    []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 100, Offset: 0}},
		Complete:  []int{0},
	})

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	// One slot, so a queue actually forms: the shipped width is 5
	// (DefaultMaxActiveTorrents, TOR-149).
	cfg.MaxActiveTorrents = 1
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	running := startRun(t, ts.URL, "magnet:?xt=urn:btih:bbbb")
	if running.state != "running" {
		t.Fatalf("first run is %q, want running - the slot was free", running.state)
	}
	queued := startRun(t, ts.URL, "magnet:?xt=urn:btih:cccc")
	if queued.state != "queued" {
		t.Fatalf("second run is %q, want queued - it is the live queued run the criterion asks for", queued.state)
	}

	rows := listRuns(t, ts.URL)
	if len(rows) != 3 {
		t.Fatalf("GET /runs listed %d rows, want 3 (one on disk, one running, one queued): %+v", len(rows), rows)
	}

	var sawDisk, sawQueued, sawRunning bool
	for _, row := range rows {
		switch {
		case row.InfoHash == diskHash:
			sawDisk = true
			if row.ID != "" {
				t.Errorf("the disk-only row carries a live id %q", row.ID)
			}
			if row.Name != "Previous Process Run" {
				t.Errorf("disk row name = %q, want %q", row.Name, "Previous Process Run")
			}
			if row.Files != 1 || row.Complete != 1 {
				t.Errorf("disk row files/complete = %d/%d, want 1/1", row.Files, row.Complete)
			}
			if row.Params != "deadbeef" {
				t.Errorf("disk row params = %q, want deadbeef", row.Params)
			}
		case row.ID == queued.id:
			sawQueued = true
			if row.State != "queued" {
				t.Errorf("queued row state = %q, want queued", row.State)
			}
		case row.ID == running.id:
			sawRunning = true
			if row.State != "running" {
				t.Errorf("running row state = %q, want running", row.State)
			}
		}
	}
	if !sawDisk {
		t.Errorf("GET /runs did not list the run a previous process left on disk: %+v", rows)
	}
	if !sawQueued {
		t.Errorf("GET /runs did not list the live queued run: %+v", rows)
	}
	if !sawRunning {
		t.Errorf("GET /runs did not list the live running run: %+v", rows)
	}
}

// TestListsA04ShapedRunWithoutSourceOrPlan: TOR-52 added source and plan
// without bumping cache.Version, so a record from before that task must
// still be listed rather than skipped as unreadable.
func TestListsA04ShapedRunWithoutSourceOrPlan(t *testing.T) {
	root := t.TempDir()
	const hash = "b0b0000000000000000000000000000000000b"

	// Typed by hand, the same way cache_test.go's run04Shape is: no
	// "source", no "plan" key at all, not merely empty ones.
	const run04Shape = `{
  "version": 1,
  "tool": "0.4.0",
  "created_at": "2026-08-01T12:00:00Z",
  "infohash": "` + hash + `",
  "name": "Old Format Run",
  "private": false,
  "videos": [
    {"index": 0, "path": "movie.mkv", "bytes": 100, "offset": 0}
  ],
  "complete": [0]
}`
	dir := filepath.Join(root, hash, "deadbeef")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, cache.Name), []byte(run04Shape), 0o600); err != nil {
		t.Fatalf("write 0.4.0-shaped run.json: %v", err)
	}

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfig(t, cfg, (&fakeRun{}).runner)

	rows := listRuns(t, ts.URL)
	for _, row := range rows {
		if row.InfoHash != hash {
			continue
		}
		if row.Source != "" {
			t.Errorf("source = %q, want \"\" - a 0.4.0 record never had one", row.Source)
		}
		if row.Name != "Old Format Run" {
			t.Errorf("name = %q, want %q", row.Name, "Old Format Run")
		}
		if row.Files != 1 || row.Complete != 1 {
			t.Errorf("files/complete = %d/%d, want 1/1", row.Files, row.Complete)
		}
		return
	}
	t.Fatalf("a 0.4.0-shaped run.json was skipped rather than listed: %+v", rows)
}

// TestListRunsSkipsWhatItCannotRead: OutputRoot is a user's directory, not
// something only this process writes into. Malformed JSON, a stray file
// where a run directory is expected, an unrelated folder - none of it should
// fail the request, only be absent from the result.
func TestListRunsSkipsWhatItCannotRead(t *testing.T) {
	root := t.TempDir()

	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	strayHash := filepath.Join(root, "strayhash")
	if err := os.MkdirAll(strayHash, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(strayHash, "README"), []byte("not a run"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	badDir := filepath.Join(root, "badhash", "deadbeef")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, cache.Name), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed run.json: %v", err)
	}

	writeRun(t, root, "goodhash", "deadbeef", cache.Run{
		Version:  cache.Version,
		InfoHash: "goodhash",
		Name:     "Good Run",
		Videos:   []cache.File{{Index: 0, Path: "a.mkv"}},
		Complete: []int{0},
	})

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfig(t, cfg, (&fakeRun{}).runner)

	rows := listRuns(t, ts.URL)
	if len(rows) != 1 || rows[0].Name != "Good Run" {
		t.Fatalf("rows = %+v, want exactly the one legitimate run", rows)
	}
}

// TestMergesALiveFinishedRunWithItsOwnDiskRecord is the merge itself: a run
// that is both live (in the registry, finished) and already on disk (its own
// record, written under the infohash the registry also learned) must appear
// once, carrying the live State/ID and the disk Files/Complete/Params.
func TestMergesALiveFinishedRunWithItsOwnDiskRecord(t *testing.T) {
	root := t.TempDir()
	const (
		hash   = "cccc000000000000000000000000000000000c"
		source = "magnet:?xt=urn:btih:" + hash
	)

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	srv, ts := newTestServerWithConfig(t, cfg, fake.runner)

	run := startRun(t, ts.URL, source)
	fake.send(t, source, core.MetadataReady{Name: "Merged Run", InfoHash: hash})
	waitFor(t, func() bool { return runInfo(t, srv, run.id).InfoHash == hash })

	// The engine writes its own record before the run's stream ends
	// (saveRunRecord, engine.go); this stands in for that.
	writeRun(t, root, hash, "deadbeef", cache.Run{
		Version:  cache.Version,
		InfoHash: hash,
		Name:     "Merged Run",
		Videos:   []cache.File{{Index: 0, Path: "a.mkv"}, {Index: 1, Path: "b.mkv"}},
		Complete: []int{0},
	})

	fake.finish(t, source)
	waitFor(t, func() bool { return runInfo(t, srv, run.id).State == RunDone })

	rows := listRuns(t, ts.URL)
	var matches []RunSummary
	for _, row := range rows {
		if row.InfoHash == hash {
			matches = append(matches, row)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("run %s appears %d times, want once (merged): %+v", hash, len(matches), matches)
	}

	row := matches[0]
	if row.ID != run.id {
		t.Errorf("merged row id = %q, want the live id %q", row.ID, run.id)
	}
	if row.State != "done" {
		t.Errorf("merged row state = %q, want done", row.State)
	}
	if row.Files != 2 || row.Complete != 1 {
		t.Errorf("merged row files/complete = %d/%d, want 2/1 - from the disk record", row.Files, row.Complete)
	}
	if row.Params != "deadbeef" {
		t.Errorf("merged row params = %q, want deadbeef", row.Params)
	}
}

// rowsFor pulls every row of one infohash out of a listing. The COUNT is the
// point of it: TOR-162 is a ticket about how many rows one torrent gets, so
// the tests below assert on the length of this and then on the row itself,
// rather than reaching for the first match and never noticing a second.
func rowsFor(rows []RunSummary, hash string) []RunSummary {
	var out []RunSummary
	for _, row := range rows {
		if row.InfoHash == hash {
			out = append(out, row)
		}
	}
	return out
}

// TestALiveRunAgainstAnExistingRecordIsStillOneRow is TOR-54's
// TestQueuedOrRunningNeverMergesWithADiskRecord, INVERTED, the same way
// TOR-140 inverted TOR-139's queue-ranking test rather than deleting it.
//
// The old test asserted TWO rows here and was right to, given the rule it
// guarded: a live entry merged only once it was final, so a run going against
// a directory that already had a record was listed beside that record. What
// it did not say is that the shape it constructs - a record on disk, then an
// ordinary live run of the same torrent - is a REGENERATE (TOR-68), and a
// reopen (TOR-55), and since TOR-152 a top-up. So this test is the evidence
// the ticket asked for: the duplicate was NOT a top-up regression, it was
// asserted as intended behaviour from the day listRuns was written, and
// top-up only made it easy to hit.
//
// The subject is unchanged and so is the half that was always right: a live
// run must never be HIDDEN behind a disk snapshot. That is now checked on the
// merged row itself - it has to carry the live id, the live state and the
// live name, and the disk record may only fill in what the live entry has
// nothing to say about.
func TestALiveRunAgainstAnExistingRecordIsStillOneRow(t *testing.T) {
	root := t.TempDir()
	const (
		hash   = "d0d0000000000000000000000000000000000d"
		source = "magnet:?xt=urn:btih:" + hash
	)

	writeRun(t, root, hash, "deadbeef", cache.Run{
		Version:  cache.Version,
		InfoHash: hash,
		Name:     "Stale Snapshot",
		Videos:   []cache.File{{Index: 0, Path: "a.mkv"}},
		Complete: []int{0},
	})

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	srv, ts := newTestServerWithConfig(t, cfg, fake.runner)

	run := startRun(t, ts.URL, source)
	if run.state != "running" {
		t.Fatalf("run is %q, want running", run.state)
	}
	fake.send(t, source, core.MetadataReady{Name: "Live Run", InfoHash: hash})
	waitFor(t, func() bool { return runInfo(t, srv, run.id).InfoHash == hash })

	// Listed WHILE the run is going: the stream is still open, nothing has
	// been finished, and this is the exact window the old rule refused to
	// merge in.
	matches := rowsFor(listRuns(t, ts.URL), hash)
	if len(matches) != 1 {
		t.Fatalf("torrent %s appears %d times while a run against its own directory is going, want 1: %+v",
			hash, len(matches), matches)
	}

	row := matches[0]
	if row.ID != run.id {
		t.Errorf("the one row's id = %q, want the live run's %q - the live entry must not be the half "+
			"that disappears into the merge", row.ID, run.id)
	}
	if row.State != "running" {
		t.Errorf("the one row's state = %q, want running - a merge that reported the disk record's silence "+
			"would hide a run that is going", row.State)
	}
	if row.Name != "Live Run" {
		t.Errorf("the one row's name = %q, want the live entry's own confirmed %q rather than the record's "+
			"older reading", row.Name, "Live Run")
	}
	if row.Params != "deadbeef" {
		t.Errorf("the one row's params = %q, want deadbeef - the merge's whole job is to tell the live row "+
			"which directory it is filling", row.Params)
	}
}

// TestATopUpInFlightIsStillOneRow is TOR-162's acceptance criterion, and the
// load-bearing word in it is DURING. A test that listed after the top-up
// finished would pass against the rule this ticket REPLACED - a final entry
// merged even then - so it would prove nothing whatsoever. Everything here is
// asserted with the run's event stream still open.
//
// It also covers what the test above cannot: the merged counts. A top-up runs
// against a set that is half-taken, so the row has to report that set's own
// standing (16 of 20 per file, two files short) while the run filling it is
// still going - which before this ticket was reported on a SECOND row, the
// one a person could not act on.
func TestATopUpInFlightIsStillOneRow(t *testing.T) {
	root := t.TempDir()
	const (
		hash   = "3333bb22cc33dd44ee55ff66aa77bb88cc99dd11"
		params = "deadbeefdeadbeef"
		source = "magnet:?xt=urn:btih:" + hash
	)
	writePartialSet(t, root, hash, params, source, budgetCost(), takenPerFile, takenPerFile)

	fake, srv, base := serverOver(t, root, 0)

	// A finished run of this torrent, so the page's row has a live id to top
	// up from - which is the path that re-arms the entry in place
	// (Server.again) rather than minting a second one.
	first := startRun(t, base, source)
	waitFor(t, func() bool { return fake.started(source) })
	fake.send(t, source, core.MetadataReady{Name: "Season 1", InfoHash: hash})
	fake.finish(t, source)
	waitFor(t, func() bool { return stateOf(t, srv, first.id) == RunDone })

	resp := post(t, base, "/runs/topup", `{"infohash":"`+hash+`","id":"`+first.id+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/topup: status %d, want 202: %s", resp.StatusCode, readAll(t, resp))
	}
	// The second call to the runner: the top-up is now IN FLIGHT, and its
	// stream stays open for the rest of this test.
	waitFor(t, func() bool { return fake.count() == 2 })
	if got := stateOf(t, srv, first.id); got.final() {
		t.Fatalf("the topped-up entry is already %s - this test has to list while the run is going", got)
	}

	matches := rowsFor(listRuns(t, base), hash)
	if len(matches) != 1 {
		t.Fatalf("torrent %s appears %d times while a top-up is in flight, want 1 - this is the "+
			"three-rows-for-two-torrents the ticket was found by: %+v", hash, len(matches), matches)
	}

	row := matches[0]
	if row.ID != first.id {
		t.Errorf("the one row's id = %q, want the re-armed entry's %q", row.ID, first.id)
	}
	if row.State != string(RunRunning) {
		t.Errorf("the one row's state = %q, want running", row.State)
	}
	if row.Params != params {
		t.Errorf("the one row's params = %q, want %q - the set being filled", row.Params, params)
	}
	if row.Files != 2 || row.Selected != 2 || row.Complete != 0 {
		t.Errorf("the one row reports files/selected/complete = %d/%d/%d, want 2/2/0 - the standing of the "+
			"set this run is filling, which is what the second row used to carry",
			row.Files, row.Selected, row.Complete)
	}
	if !row.Partial() {
		t.Error("the one row is not partial, but the set it is filling is 16 of 20 frames short on both " +
			"files - that verdict is the only thing on the row that says why a top-up is running at all")
	}
}

// TestAQueuedTopUpIsStillOneRow is the state the replaced rule was actually
// WRITTEN for: an entry that is waiting for a slot, has written nothing, and
// yet already names a directory that has a record. Worth its own test because
// queued is the one state where the old reasoning ("it has not written a
// record yet, whatever its infohash") is literally true and still does not
// justify a second row.
//
// The entry knows its infohash here only because a top-up RE-ARMS the row it
// was started from, and that row learned it from its own first run
// (Server.again keeps what belongs to the torrent). A top-up started against
// a disk-only row mints a fresh entry instead, which has no infohash until
// its own metadata_ready - and for that window nothing can merge, because the
// key does not exist yet. app.js's liveRowFor had the identical hole for the
// identical reason, so removing it loses nothing.
func TestAQueuedTopUpIsStillOneRow(t *testing.T) {
	root := t.TempDir()
	const (
		hash   = "3333bb22cc33dd44ee55ff66aa77bb88cc99dd22"
		params = "deadbeefdeadbeef"
		source = "magnet:?xt=urn:btih:" + hash
		hog    = "magnet:?xt=urn:btih:9999bb22cc33dd44ee55ff66aa77bb88cc99dd22"
	)
	writePartialSet(t, root, hash, params, source, budgetCost(), takenPerFile, takenPerFile)

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	// One slot, so the top-up below has to wait for it rather than start.
	cfg.MaxActiveTorrents = 1
	srv, ts := newTestServerWithConfig(t, cfg, fake.runner)

	first := startRun(t, ts.URL, source)
	waitFor(t, func() bool { return fake.started(source) })
	fake.send(t, source, core.MetadataReady{Name: "Season 1", InfoHash: hash})
	fake.finish(t, source)
	waitFor(t, func() bool { return stateOf(t, srv, first.id) == RunDone })

	// Something else takes the only slot and keeps it: its stream stays open
	// for the rest of the test, so nothing dispatches behind it.
	startRun(t, ts.URL, hog)
	waitFor(t, func() bool { return fake.started(hog) })

	resp := post(t, ts.URL, "/runs/topup", `{"infohash":"`+hash+`","id":"`+first.id+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/topup: status %d, want 202: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := stateOf(t, srv, first.id); got != RunQueued {
		t.Fatalf("the topped-up entry is %s, want queued - this test needs the queued window", got)
	}

	matches := rowsFor(listRuns(t, ts.URL), hash)
	if len(matches) != 1 {
		t.Fatalf("torrent %s appears %d times while its top-up waits for a slot, want 1: %+v",
			hash, len(matches), matches)
	}
	if row := matches[0]; row.ID != first.id || row.State != string(RunQueued) {
		t.Errorf("the one row is %+v, want the queued live entry %q", row, first.id)
	}
}

// TestTwoCapturePlansOfOneTorrentStayTwoRows is the line TOR-162 deliberately
// did NOT cross. Dropping finality from the merge key does not touch the other
// half of the rule: an infohash naming more than one result set has no single
// record to pair a live entry with, TOR-54 chose to list them all rather than
// guess, and this pins that choice so a later widening of the key cannot
// silently swallow a set a person is looking at.
func TestTwoCapturePlansOfOneTorrentStayTwoRows(t *testing.T) {
	root := t.TempDir()
	const (
		hash   = "d1d1000000000000000000000000000000000d11"
		source = "magnet:?xt=urn:btih:" + hash
	)

	for _, params := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"} {
		writeRun(t, root, hash, params, cache.Run{
			Version:  cache.Version,
			InfoHash: hash,
			Name:     "Two Plans",
			Videos:   []cache.File{{Index: 0, Path: "a.mkv"}},
			Selected: []int{0},
			Complete: []int{0},
		})
	}

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	srv, ts := newTestServerWithConfig(t, cfg, fake.runner)

	run := startRun(t, ts.URL, source)
	fake.send(t, source, core.MetadataReady{Name: "Two Plans", InfoHash: hash})
	waitFor(t, func() bool { return runInfo(t, srv, run.id).InfoHash == hash })

	matches := rowsFor(listRuns(t, ts.URL), hash)
	if len(matches) != 3 {
		t.Fatalf("torrent %s appears %d times, want 3 - the live row plus both sets on disk, since no one "+
			"record can be paired with the live entry: %+v", hash, len(matches), matches)
	}
	for _, row := range matches {
		if row.ID == run.id && row.Params != "" {
			t.Errorf("the live row was paired with set %q anyway - merging on a guess is what this rule "+
				"refuses: %+v", row.Params, row)
		}
	}
}

// TestThePageKeepsNoMergeRuleOfItsOwn is the third acceptance criterion:
// once the listing holds the invariant, app.js's liveRowFor has to GO rather
// than sit there as a second implementation of the same rule. Two copies of
// one rule is what this ticket exists to close - the rule was in the client,
// where a second consumer of GET /runs could not see it - so leaving the
// workaround behind would leave the ticket half-done.
//
// Read as served text, the way every other front-end guard in this package
// works (see columns_test.go's own opening note on why there is no JS runner
// here).
func TestThePageKeepsNoMergeRuleOfItsOwn(t *testing.T) {
	js := appJS(t)

	for _, gone := range []string{"function liveRowFor(", "liveRowFor(row)"} {
		if strings.Contains(js, gone) {
			t.Errorf("app.js still contains %q - the page is still deciding which rows are the same torrent, "+
				"beside a listing that now decides it for every consumer", gone)
		}
	}
}

// findByHash is the small lookup every TOR-72 test below needs: pull the one
// row for an infohash out of the full listing, failing loudly if it is not
// there rather than letting a nil-slice index panic obscure the point of the
// test.
func findByHash(t *testing.T, rows []RunSummary, hash string) RunSummary {
	t.Helper()
	for _, row := range rows {
		if row.InfoHash == hash {
			return row
		}
	}
	t.Fatalf("no row for infohash %s in %+v", hash, rows)
	return RunSummary{}
}

// findByID is findByHash's counterpart for a row that has not told the
// registry its infohash yet - queued, or running before its own metadata has
// arrived (TOR-117's own gap) - where InfoHash is empty and ID is the only
// key a live row can be found by.
func findByID(t *testing.T, rows []RunSummary, id string) RunSummary {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no row for id %s in %+v", id, rows)
	return RunSummary{}
}

// TestDiskRowIsDoneWhenSelectedFilesAllComplete is TOR-72's acceptance
// criterion for a disk-only row, the "not partial" half: three of six files
// deliberately chosen (Selected), all three complete, two files nobody ever
// asked for left over. That is Done, not Partial - Files (6) is deliberately
// not the number Partial is judged against.
func TestDiskRowIsDoneWhenSelectedFilesAllComplete(t *testing.T) {
	root := t.TempDir()
	const hash = "1111000000000000000000000000000000000f"

	writeRun(t, root, hash, "deadbeef", cache.Run{
		Version:  cache.Version,
		InfoHash: hash,
		Name:     "Chosen Three",
		Videos: []cache.File{
			{Index: 0, Path: "a.mkv"}, {Index: 1, Path: "b.mkv"}, {Index: 2, Path: "c.mkv"},
			{Index: 3, Path: "d.mkv"}, {Index: 4, Path: "e.mkv"}, {Index: 5, Path: "f.mkv"},
		},
		Selected: []int{0, 2, 4},
		Complete: []int{0, 2, 4},
	})

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfig(t, cfg, (&fakeRun{}).runner)

	row := findByHash(t, listRuns(t, ts.URL), hash)
	if row.Files != 6 || row.Selected != 3 || row.Complete != 3 {
		t.Fatalf("row files/selected/complete = %d/%d/%d, want 6/3/3", row.Files, row.Selected, row.Complete)
	}
	if row.Partial() {
		t.Errorf("row is Partial, want Done: a narrower selection that fully finished is not a partial result: %+v", row)
	}
}

// TestDiskRowIsPartialWhenASelectedFileHasNoFrames is the other half: one of
// the files someone actually asked for came out with no frames. Complete is
// short of Selected, which is exactly what Partial means - regardless of how
// many other, never-requested files the torrent holds.
func TestDiskRowIsPartialWhenASelectedFileHasNoFrames(t *testing.T) {
	root := t.TempDir()
	const hash = "2222000000000000000000000000000000000f"

	writeRun(t, root, hash, "deadbeef", cache.Run{
		Version:  cache.Version,
		InfoHash: hash,
		Name:     "One Selected File Failed",
		Videos: []cache.File{
			{Index: 0, Path: "a.mkv"}, {Index: 1, Path: "b.mkv"}, {Index: 2, Path: "c.mkv"},
		},
		Selected: []int{0, 2},
		Complete: []int{0},
	})

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfig(t, cfg, (&fakeRun{}).runner)

	row := findByHash(t, listRuns(t, ts.URL), hash)
	if row.Selected != 2 || row.Complete != 1 {
		t.Fatalf("row selected/complete = %d/%d, want 2/1", row.Selected, row.Complete)
	}
	if !row.Partial() {
		t.Errorf("row is Done, want Partial: a selected file (index 2) has no frames: %+v", row)
	}
}

// TestListRunsWirePartialField is TOR-80's guarantee that the verdict is on
// the wire itself, not only reachable by calling RunSummary.Partial() on a
// struct a Go test happens to hold. Every earlier test in this file decodes
// GET /runs back into a []RunSummary and calls Partial() on the result,
// which would keep passing even if MarshalJSON's "partial" key were deleted
// entirely - json.Unmarshal silently ignores a field with no matching
// destination, and Partial() would still recompute the same answer from the
// decoded Selected/Complete. That is exactly the gap TOR-80 closes: app.js
// does not decode into a Go struct and call a method, it reads whatever key
// the response body actually has. So this test reads the raw body as a
// generic map, the same shape app.js's fetch().then(r => r.json()) sees, and
// asserts the "partial" key is present with the right boolean - byte-level,
// not struct-level.
func TestListRunsWirePartialField(t *testing.T) {
	root := t.TempDir()
	const (
		doneHash    = "5555000000000000000000000000000000000f"
		partialHash = "6666000000000000000000000000000000000f"
	)

	writeRun(t, root, doneHash, "deadbeef", cache.Run{
		Version: cache.Version, InfoHash: doneHash, Name: "Wire Done",
		Videos:   []cache.File{{Index: 0, Path: "a.mkv"}},
		Selected: []int{0}, Complete: []int{0},
	})
	writeRun(t, root, partialHash, "deadbeef", cache.Run{
		Version: cache.Version, InfoHash: partialHash, Name: "Wire Partial",
		Videos:   []cache.File{{Index: 0, Path: "a.mkv"}, {Index: 1, Path: "b.mkv"}},
		Selected: []int{0, 1}, Complete: []int{0},
	})

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfig(t, cfg, (&fakeRun{}).runner)

	resp := get(t, ts.URL, "/runs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /runs: status %d, want 200", resp.StatusCode)
	}

	var body struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET /runs as raw maps: %v", err)
	}

	byHash := func(hash string) map[string]any {
		for _, row := range body.Runs {
			if row["infohash"] == hash {
				return row
			}
		}
		t.Fatalf("no row for infohash %s in %+v", hash, body.Runs)
		return nil
	}

	done := byHash(doneHash)
	if v, ok := done["partial"]; !ok || v != false {
		t.Errorf(`row["partial"] = %#v, ok=%v, want false: %+v`, v, ok, done)
	}

	partial := byHash(partialHash)
	if v, ok := partial["partial"]; !ok || v != true {
		t.Errorf(`row["partial"] = %#v, ok=%v, want true: %+v`, v, ok, partial)
	}
}

// TestPreTOR65RecordDegradesHonestly covers a record written before TOR-65:
// Selected is absent (nil), not merely empty, because the field itself did
// not exist yet. cache.Run.SelectedCount's documented fallback is Videos -
// the same denominator a listing compared Complete against before Selected
// existed - so this must read exactly as it always did: Partial when
// Complete is short of the full file count, Done when it is not. It must not
// read as "nothing was selected" (which would make every such record with
// any complete file read as impossibly over 100% selected, or as never
// partial no matter how incomplete it is).
func TestPreTOR65RecordDegradesHonestly(t *testing.T) {
	root := t.TempDir()
	const (
		wholeHash   = "3333000000000000000000000000000000000f"
		partialHash = "4444000000000000000000000000000000000f"
	)

	// No Selected key at all - cache.Run's zero value for that field is nil,
	// and this is written the same way a 0.4.0-shaped record is elsewhere in
	// this file: by hand, so nothing here accidentally sends "[]" instead.
	writeRun(t, root, wholeHash, "deadbeef", cache.Run{
		Version:  cache.Version,
		InfoHash: wholeHash,
		Name:     "Old Record, Fully Captured",
		Videos:   []cache.File{{Index: 0, Path: "a.mkv"}, {Index: 1, Path: "b.mkv"}},
		Complete: []int{0, 1},
	})
	writeRun(t, root, partialHash, "deadbeef", cache.Run{
		Version:  cache.Version,
		InfoHash: partialHash,
		Name:     "Old Record, One File Short",
		Videos:   []cache.File{{Index: 0, Path: "a.mkv"}, {Index: 1, Path: "b.mkv"}},
		Complete: []int{0},
	})

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfig(t, cfg, (&fakeRun{}).runner)

	rows := listRuns(t, ts.URL)

	whole := findByHash(t, rows, wholeHash)
	if whole.Selected != 2 {
		t.Fatalf("whole record selected = %d, want 2 (fallback to Videos)", whole.Selected)
	}
	if whole.Partial() {
		t.Errorf("fully-captured old record reads Partial, want Done: %+v", whole)
	}

	partial := findByHash(t, rows, partialHash)
	if partial.Selected != 2 {
		t.Fatalf("partial record selected = %d, want 2 (fallback to Videos)", partial.Selected)
	}
	if !partial.Partial() {
		t.Errorf("one-file-short old record reads Done, want Partial: %+v", partial)
	}
}

// TestLiveRowReadsDoneOrPartial is TOR-72's acceptance criterion for a live
// row (one still carrying its own registry ID and State), the counterpart of
// the disk-row tests above: the same Selected-vs-Complete arithmetic must
// hold once a live, finished entry has merged with its own disk record
// (TestMergesALiveFinishedRunWithItsOwnDiskRecord is the merge itself; this
// is what TOR-72 layers on top of it).
func TestLiveRowReadsDoneOrPartial(t *testing.T) {
	root := t.TempDir()
	const (
		doneHash   = "5555000000000000000000000000000000000f"
		doneSource = "magnet:?xt=urn:btih:" + doneHash
		partHash   = "6666000000000000000000000000000000000f"
		partSource = "magnet:?xt=urn:btih:" + partHash
	)

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	srv, ts := newTestServerWithConfig(t, cfg, fake.runner)

	doneRun := startRun(t, ts.URL, doneSource)
	fake.send(t, doneSource, core.MetadataReady{Name: "Live Done", InfoHash: doneHash})
	waitFor(t, func() bool { return runInfo(t, srv, doneRun.id).InfoHash == doneHash })
	writeRun(t, root, doneHash, "deadbeef", cache.Run{
		Version:  cache.Version,
		InfoHash: doneHash,
		Name:     "Live Done",
		Videos:   []cache.File{{Index: 0, Path: "a.mkv"}, {Index: 1, Path: "b.mkv"}},
		Selected: []int{0},
		Complete: []int{0},
	})
	fake.finish(t, doneSource)
	waitFor(t, func() bool { return runInfo(t, srv, doneRun.id).State == RunDone })

	partRun := startRun(t, ts.URL, partSource)
	fake.send(t, partSource, core.MetadataReady{Name: "Live Partial", InfoHash: partHash})
	waitFor(t, func() bool { return runInfo(t, srv, partRun.id).InfoHash == partHash })
	writeRun(t, root, partHash, "deadbeef", cache.Run{
		Version:  cache.Version,
		InfoHash: partHash,
		Name:     "Live Partial",
		Videos:   []cache.File{{Index: 0, Path: "a.mkv"}, {Index: 1, Path: "b.mkv"}},
		Selected: []int{0, 1},
		Complete: []int{0},
	})
	fake.finish(t, partSource)
	waitFor(t, func() bool { return runInfo(t, srv, partRun.id).State == RunDone })

	rows := listRuns(t, ts.URL)

	done := findByHash(t, rows, doneHash)
	if done.ID != doneRun.id || done.State != "done" {
		t.Fatalf("done row id/state = %q/%q, want the live id and state done: %+v", done.ID, done.State, done)
	}
	if done.Partial() {
		t.Errorf("live row with its one selected file complete reads Partial, want Done: %+v", done)
	}

	partial := findByHash(t, rows, partHash)
	if partial.ID != partRun.id || partial.State != "done" {
		t.Fatalf("partial row id/state = %q/%q, want the live id and state done: %+v", partial.ID, partial.State, partial)
	}
	if !partial.Partial() {
		t.Errorf("live row with a selected, incomplete file reads Done, want Partial: %+v", partial)
	}
}

// TestQueuedAndRunningRowsCarryProvisionalNameFromMagnetDn is TOR-117's
// acceptance criterion, straight off the payload the ticket was filed
// against: a running row with nothing confirmed yet - no name, no counts -
// carries the magnet's own dn= instead, visibly marked provisional (Name
// stays empty; ProvisionalName is what carries it), and a queued row behind
// it gets the same treatment rather than a special case for "running" alone.
func TestQueuedAndRunningRowsCarryProvisionalNameFromMagnetDn(t *testing.T) {
	root := t.TempDir()
	const (
		runningHash   = "e4d37e6200000000000000000000000000000000"
		runningSource = "magnet:?xt=urn:btih:" + runningHash + "&dn=Sintel&tr=http%3A%2F%2Ftracker.invalid%2Fannounce"
		queuedHash    = "f5e48f7300000000000000000000000000000000"
		queuedSource  = "magnet:?xt=urn:btih:" + queuedHash + "&dn=Big+Buck+Bunny"
	)

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	// One slot, so a queue actually forms: the shipped width is 5
	// (DefaultMaxActiveTorrents, TOR-149).
	cfg.MaxActiveTorrents = 1
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	running := startRun(t, ts.URL, runningSource)
	if running.state != "running" {
		t.Fatalf("first run is %q, want running - the slot was free", running.state)
	}
	queued := startRun(t, ts.URL, queuedSource)
	if queued.state != "queued" {
		t.Fatalf("second run is %q, want queued", queued.state)
	}

	rows := listRuns(t, ts.URL)

	// Looked up by ID, not InfoHash: neither run has told the registry its
	// infohash yet (that arrives with core.MetadataReady, per RunInfo's own
	// doc comment) - which is exactly the point being tested, so InfoHash
	// is not a usable key here.
	runningRow := findByID(t, rows, running.id)
	if runningRow.Name != "" {
		t.Errorf("running row Name = %q, want empty - nothing has confirmed it yet", runningRow.Name)
	}
	if runningRow.ProvisionalName != "Sintel" {
		t.Errorf("running row ProvisionalName = %q, want %q (the magnet's own dn=)", runningRow.ProvisionalName, "Sintel")
	}
	if runningRow.Files != 0 || runningRow.Complete != 0 || runningRow.Selected != 0 {
		t.Errorf("running row counts = %d/%d/%d, want 0/0/0 - this is the exact gap TOR-117 is about", runningRow.Files, runningRow.Complete, runningRow.Selected)
	}

	queuedRow := findByID(t, rows, queued.id)
	if queuedRow.Name != "" {
		t.Errorf("queued row Name = %q, want empty", queuedRow.Name)
	}
	if queuedRow.ProvisionalName != "Big Buck Bunny" {
		t.Errorf("queued row ProvisionalName = %q, want %q (dn= percent-decoded)", queuedRow.ProvisionalName, "Big Buck Bunny")
	}
}

// TestConfirmedNameSupersedesProvisionalNameOnceMetadataArrives is the other
// half: once the torrent's own metadata says a name, that is what Name
// reports, and ProvisionalName goes back to empty rather than sitting beside
// it - a client is never asked to choose between the two (see
// RunSummary.ProvisionalName). The magnet's dn= here is deliberately wrong,
// the way anyone's dn= might be, so this also proves the confirmed name wins
// even when it disagrees with what the magnet claimed.
func TestConfirmedNameSupersedesProvisionalNameOnceMetadataArrives(t *testing.T) {
	root := t.TempDir()
	const (
		hash   = "a1a1000000000000000000000000000000000000"
		source = "magnet:?xt=urn:btih:" + hash + "&dn=Guessed+Wrong"
	)

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	srv, ts := newTestServerWithConfig(t, cfg, fake.runner)

	run := startRun(t, ts.URL, source)
	if run.state != "running" {
		t.Fatalf("run is %q, want running", run.state)
	}

	// By ID, not InfoHash: the registry has not learned it yet at this point.
	before := findByID(t, listRuns(t, ts.URL), run.id)
	if before.Name != "" || before.ProvisionalName != "Guessed Wrong" {
		t.Fatalf("before metadata: Name=%q ProvisionalName=%q, want empty/%q", before.Name, before.ProvisionalName, "Guessed Wrong")
	}

	fake.send(t, source, core.MetadataReady{Name: "The Torrent's Real Name", InfoHash: hash})
	waitFor(t, func() bool { return runInfo(t, srv, run.id).InfoHash == hash })

	after := findByHash(t, listRuns(t, ts.URL), hash)
	if after.Name != "The Torrent's Real Name" {
		t.Errorf("after metadata: Name = %q, want the confirmed name", after.Name)
	}
	if after.ProvisionalName != "" {
		t.Errorf("after metadata: ProvisionalName = %q, want empty - Name is confirmed now, nothing left for it to stand in for", after.ProvisionalName)
	}
}

// TestNeedsActionRowCarriesConfirmedNameFromItsOwnMetadataPass covers the
// other live state TOR-117 says must not be special-cased away: a torrent
// parked in needs-action has already paid for its metadata pass
// (listThenRun), so it has a confirmed Name from entry.contents even though
// nothing has been written to disk yet - no merge, no ProvisionalName
// fallback needed, because there is something better to report.
func TestNeedsActionRowCarriesConfirmedNameFromItsOwnMetadataPass(t *testing.T) {
	root := t.TempDir()
	const source = "magnet:?xt=urn:btih:b2b2000000000000000000000000000000000000&dn=Guessed+Wrong"

	lister := newFakeLister()
	lister.holds(source, 2) // more than one video file, so it parks

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfigAndLister(t, cfg, fake.runner, lister.list)

	run := startRun(t, ts.URL, source)
	waitFor(t, func() bool {
		rows := listRuns(t, ts.URL)
		for _, row := range rows {
			if row.ID == run.id {
				return row.State == "needs-action"
			}
		}
		return false
	})

	row := findByHash(t, listRuns(t, ts.URL), fmt.Sprintf("%040x", len(source)))
	if row.State != "needs-action" {
		t.Fatalf("row state = %q, want needs-action", row.State)
	}
	// fakeLister.holds names it "release for " + source - see needsaction_test.go.
	wantName := "release for " + source
	if row.Name != wantName {
		t.Errorf("needs-action row Name = %q, want %q (its own metadata pass, not the disk - there is no disk record yet)", row.Name, wantName)
	}
	if row.ProvisionalName != "" {
		t.Errorf("needs-action row ProvisionalName = %q, want empty - Name is already confirmed, dn= has nothing left to offer", row.ProvisionalName)
	}
}

// TestUploadedTorrentRowCarriesNoProvisionalName is the ".torrent-sourced run
// has instead" half of TOR-117's own instructions: a dn= is a magnet-only
// convention (swarm.MagnetDisplayName), so a dropped .torrent's row - whose
// Source is a label ("dropped .torrent"), never a magnet URI - must not
// carry a ProvisionalName invented from it. There is genuinely nothing to
// offer here; the row falls back the way it always did (app.js's source/id
// fallback), and this proves the API does not manufacture something that
// looks like one.
func TestUploadedTorrentRowCarriesNoProvisionalName(t *testing.T) {
	root := t.TempDir()
	fake := &fakeRun{}
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	resp := uploadTorrent(t, ts.URL, []byte("d8:announce...e"), "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/upload: status %d, want 202", resp.StatusCode)
	}

	rows := listRuns(t, ts.URL)
	if len(rows) != 1 {
		t.Fatalf("GET /runs listed %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].ProvisionalName != "" {
		t.Errorf("uploaded-.torrent row ProvisionalName = %q, want empty - a .torrent's Source has no dn= to read", rows[0].ProvisionalName)
	}
	if rows[0].Name != "" {
		t.Errorf("uploaded-.torrent row Name = %q, want empty - nothing has confirmed it yet", rows[0].Name)
	}
}

// TestRunStateCarriesProvisionalNameThenConfirmedName is TOR-117's guarantee
// for a page that is already open, not only for one that reloads: the very
// first run_state a new run publishes (queued/running, reset:true) carries
// the same provisional_name a GET /runs row would, and the run_state that
// eventually announces the run is done carries name instead - the live
// socket and the polled listing never disagree about which of the two a
// client should show (runStateFieldsLocked and listRuns share the one rule).
func TestRunStateCarriesProvisionalNameThenConfirmedName(t *testing.T) {
	root := t.TempDir()
	const (
		hash   = "c3c3000000000000000000000000000000000000"
		source = "magnet:?xt=urn:btih:" + hash + "&dn=Guessed+Wrong"
	)

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	conn := dial(t, ts.URL)
	if got := next(t, conn); got["type"] != "run_state" {
		t.Fatalf("first message is %v, want the connection marker", got)
	}

	run := startRun(t, ts.URL, source)

	first := next(t, conn)
	if first["type"] != "run_state" || first["reset"] != true {
		t.Fatalf("first message for the new run is %v, want a reset run_state", first)
	}
	if _, ok := first["name"]; ok {
		t.Errorf(`run_state carries "name" = %#v before any metadata arrived, want it absent: %+v`, first["name"], first)
	}
	if got, ok := first["provisional_name"]; !ok || got != "Guessed Wrong" {
		t.Errorf(`run_state["provisional_name"] = %#v (present=%v), want %q: %+v`, got, ok, "Guessed Wrong", first)
	}

	fake.send(t, source, core.MetadataReady{Name: "The Torrent's Real Name", InfoHash: hash})
	writeRun(t, root, hash, "deadbeef", cache.Run{
		Version: cache.Version, InfoHash: hash, Name: "The Torrent's Real Name",
		Videos: []cache.File{{Index: 0, Path: "a.mkv"}}, Selected: []int{0}, Complete: []int{0},
	})
	fake.finish(t, source)

	done := waitRunState(t, conn, run.id, "done")
	if got, ok := done["name"]; !ok || got != "The Torrent's Real Name" {
		t.Errorf(`run_state["name"] = %#v (present=%v), want %q: %+v`, got, ok, "The Torrent's Real Name", done)
	}
	if _, ok := done["provisional_name"]; ok {
		t.Errorf(`run_state carries "provisional_name" = %#v once the name is confirmed, want it absent: %+v`, done["provisional_name"], done)
	}
}

// waitRunState reads the socket until it sees a run_state for id in the
// given state, discarding every other message (this run's earlier
// lifecycle states, another run's events, anything else on the wire) along
// the way - the finishing run_state this file's tests care about is not
// necessarily the first message to arrive for id.
func waitRunState(t *testing.T, conn *websocket.Conn, id, state string) map[string]any {
	t.Helper()

	for i := 0; i < 50; i++ {
		ev := next(t, conn)
		if ev["type"] == "run_state" && ev["run"] == id && ev["state"] == state {
			return ev
		}
	}
	t.Fatalf("never saw a run_state for %s in state %q within 50 messages", id, state)
	return nil
}

// TestLiveRunStateCarriesPartialWhenItFinishes is TOR-87's acceptance
// criterion, word for word: a page open while a run finishes learns the same
// verdict a reload would - shown here by reading run_state straight off the
// socket and asserting the fields a badge would be built from, never by
// reasoning about a DOM (this package has none to reason about; app.js's own
// copy of this contract is unchanged, per TOR-80, from reading exactly these
// fields off the wire rather than recomputing them).
//
// The run's own selection (two files) does not all come out complete (one
// does) - the exact shape Partial() calls true - and the disk record that
// says so is written before fake.finish() the same way saveRunRecord writes
// it before a real engine's Done event, per this ticket's own design note:
// the honest source for these counts is the record the run just wrote.
func TestLiveRunStateCarriesPartialWhenItFinishes(t *testing.T) {
	root := t.TempDir()
	const (
		infoHash = "7777000000000000000000000000000000000f"
		source   = "magnet:?xt=urn:btih:" + infoHash
	)

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	srv, ts := newTestServerWithConfig(t, cfg, fake.runner)

	conn := dial(t, ts.URL)
	if got := next(t, conn); got["type"] != "run_state" {
		t.Fatalf("first message is %v, want the connection marker", got)
	}

	run := startRun(t, ts.URL, source)
	fake.send(t, source, core.MetadataReady{Name: "Live Partial", InfoHash: infoHash, Selected: []int{0, 1}})
	waitFor(t, func() bool { return runInfo(t, srv, run.id).InfoHash == infoHash })

	writeRun(t, root, infoHash, "deadbeef", cache.Run{
		Version:  cache.Version,
		InfoHash: infoHash,
		Name:     "Live Partial",
		Videos:   []cache.File{{Index: 0, Path: "a.mkv"}, {Index: 1, Path: "b.mkv"}},
		Selected: []int{0, 1},
		Complete: []int{0},
	})
	fake.finish(t, source)

	ev := waitRunState(t, conn, run.id, "done")
	if got, ok := ev["files"]; !ok || got != float64(2) {
		t.Errorf(`run_state["files"] = %#v (present=%v), want 2: %+v`, got, ok, ev)
	}
	if got, ok := ev["complete"]; !ok || got != float64(1) {
		t.Errorf(`run_state["complete"] = %#v (present=%v), want 1: %+v`, got, ok, ev)
	}
	if got, ok := ev["selected"]; !ok || got != float64(2) {
		t.Errorf(`run_state["selected"] = %#v (present=%v), want 2: %+v`, got, ok, ev)
	}
	if got, ok := ev["partial"]; !ok || got != true {
		t.Errorf(`run_state["partial"] = %#v (present=%v), want true: %+v`, got, ok, ev)
	}
}

// TestLiveRunStateOmitsCountsWhenDiskRecordIsAmbiguous is the design decision
// runRecordCounts documents: an infohash naming more than one params
// directory (two capture plans of the same torrent) has no single record for
// the finishing run_state to answer from, so it carries none of
// files/complete/selected/partial at all - the same "list both, merge
// neither" choice listRuns makes for GET /runs (see its own comment on the
// merge condition). Guessing which of the two a reload would have shown
// would risk a live badge disagreeing with the reload it is supposed to
// match, which is worse than the pre-TOR-87 gap this ticket closed.
func TestLiveRunStateOmitsCountsWhenDiskRecordIsAmbiguous(t *testing.T) {
	root := t.TempDir()
	const (
		infoHash = "8888000000000000000000000000000000000f"
		source   = "magnet:?xt=urn:btih:" + infoHash
	)

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	srv, ts := newTestServerWithConfig(t, cfg, fake.runner)

	conn := dial(t, ts.URL)
	if got := next(t, conn); got["type"] != "run_state" {
		t.Fatalf("first message is %v, want the connection marker", got)
	}

	run := startRun(t, ts.URL, source)
	fake.send(t, source, core.MetadataReady{Name: "Ambiguous", InfoHash: infoHash, Selected: []int{0}})
	waitFor(t, func() bool { return runInfo(t, srv, run.id).InfoHash == infoHash })

	// Two different capture plans of the same torrent - core.ParamsKey's own
	// case for two params directories under one infohash.
	writeRun(t, root, infoHash, "deadbeef", cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "Ambiguous",
		Videos: []cache.File{{Index: 0, Path: "a.mkv"}}, Selected: []int{0}, Complete: []int{0},
	})
	writeRun(t, root, infoHash, "beefdead", cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "Ambiguous",
		Videos: []cache.File{{Index: 0, Path: "a.mkv"}}, Selected: []int{0},
	})
	fake.finish(t, source)

	ev := waitRunState(t, conn, run.id, "done")
	if _, ok := ev["files"]; ok {
		t.Errorf(`run_state carries "files" = %#v for an ambiguous infohash, want it absent: %+v`, ev["files"], ev)
	}
	if _, ok := ev["partial"]; ok {
		t.Errorf(`run_state carries "partial" = %#v for an ambiguous infohash, want it absent: %+v`, ev["partial"], ev)
	}
}

// rawRuns is listRuns's counterpart for tests that need to see whether a key
// is PRESENT, not merely what it decodes to once RunSummary has unmarshalled
// it - a "live" key holding JSON null and a missing "live" key both decode
// to a nil *Live, so only the raw map tells the two apart the way
// TestListRunsWirePartialField's byHash already does for "partial".
func rawRuns(t *testing.T, base string) []map[string]any {
	t.Helper()

	resp := get(t, base, "/runs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /runs: status %d, want 200", resp.StatusCode)
	}
	var body struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET /runs as raw maps: %v", err)
	}
	return body.Runs
}

// byID is rawRuns's lookup, the raw-map counterpart of findByID.
func byID(t *testing.T, rows []map[string]any, id string) map[string]any {
	t.Helper()
	for _, row := range rows {
		if row["id"] == id {
			return row
		}
	}
	t.Fatalf("no row for id %s in %+v", id, rows)
	return nil
}

// TestLiveRowCarriesLiveFigures is TOR-136's acceptance criterion for the
// row that actually has a client: peers, seeds, both rates and availability
// all reach GET /runs together once the registry has absorbed one
// heartbeat.
//
// The heartbeat is folded into the registry directly via
// entry.applyProgress rather than sent through fake.send: pump's event
// switch (server.go) does not yet call applyProgress for a core.Progress
// event - that one remaining case belongs to server.go, which this task was
// scoped to leave untouched (TOR-130 has it in flight). This test is the
// registry-and-listing half of TOR-136: it proves a reading, once folded
// in, reaches GET /runs in the right shape - applyProgress's own doc
// (runs.go) carries the one-case wiring pump still needs.
func TestLiveRowCarriesLiveFigures(t *testing.T) {
	fake := newFakeRuns()
	cfg := DefaultConfig()
	srv, ts := newTestServerWithConfig(t, cfg, fake.runner)

	run := startRun(t, ts.URL, "magnet:?xt=urn:btih:eeee000000000000000000000000000000000e")

	dl, ul := 1234.5, 678.9
	srv.mu.Lock()
	entry, ok := srv.runs[run.id]
	if !ok {
		srv.mu.Unlock()
		t.Fatalf("run %s is not in the registry", run.id)
	}
	entry.applyProgress(core.Progress{
		Peers: 7, Seeds: 2,
		DownloadRate: &dl, UploadRate: &ul,
		Swarm: &core.SwarmAvailability{CopiesPerPiece: 2.4, Unavailable: 3, NumPieces: 100},
	})
	srv.mu.Unlock()

	row := findByID(t, listRuns(t, ts.URL), run.id)
	if row.Live == nil {
		t.Fatalf("live row carries no live figures: %+v", row)
	}
	if row.Live.Peers != 7 || row.Live.Seeds != 2 {
		t.Errorf("live.peers/seeds = %d/%d, want 7/2", row.Live.Peers, row.Live.Seeds)
	}
	if row.Live.DownloadBps == nil || *row.Live.DownloadBps != dl {
		t.Errorf("live.download_bps = %v, want %v", row.Live.DownloadBps, dl)
	}
	if row.Live.UploadBps == nil || *row.Live.UploadBps != ul {
		t.Errorf("live.upload_bps = %v, want %v", row.Live.UploadBps, ul)
	}
	if row.Live.Swarm == nil {
		t.Fatalf("live.swarm is nil, want a reading")
	}
	if row.Live.Swarm.CopiesPerPiece != 2.4 || row.Live.Swarm.Unavailable != 3 || row.Live.Swarm.Pieces != 100 {
		t.Errorf("live.swarm = %+v, want {2.4 3 100}", row.Live.Swarm)
	}
}

// TestQueuedAndDiskRowsCarryNoLiveFigures is TOR-136's other half: a row with
// no client at all must not merely show zeros, its "live" key must be
// entirely absent from the wire - the raw map is what proves that, since a
// decoded *Live is nil either way whether the key was missing or null.
func TestQueuedAndDiskRowsCarryNoLiveFigures(t *testing.T) {
	root := t.TempDir()
	const diskHash = "ffff000000000000000000000000000000000f"

	writeRun(t, root, diskHash, "deadbeef", cache.Run{
		Version: cache.Version, InfoHash: diskHash, Name: "Disk Only",
		Videos: []cache.File{{Index: 0, Path: "a.mkv"}}, Complete: []int{0},
	})

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	// One slot, so a queue actually forms: the shipped width is 5
	// (DefaultMaxActiveTorrents, TOR-149).
	cfg.MaxActiveTorrents = 1
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	// The slot is held by a first run so the second one asked for here stays
	// queued - a queued entry has no client, and it never will until it
	// starts.
	first := startRun(t, ts.URL, "magnet:?xt=urn:btih:0000")
	if first.state != "running" {
		t.Fatalf("first run is %q, want running - the slot was free", first.state)
	}
	queued := startRun(t, ts.URL, "magnet:?xt=urn:btih:1111")
	if queued.state != "queued" {
		t.Fatalf("second run is %q, want queued", queued.state)
	}

	rows := rawRuns(t, ts.URL)

	queuedRow := byID(t, rows, queued.id)
	if _, has := queuedRow["live"]; has {
		t.Errorf(`queued row carries a "live" key, want it absent: %+v`, queuedRow)
	}

	var diskRow map[string]any
	for _, row := range rows {
		if row["infohash"] == diskHash {
			diskRow = row
		}
	}
	if diskRow == nil {
		t.Fatalf("no row for disk-only infohash %s in %+v", diskHash, rows)
	}
	if _, has := diskRow["live"]; has {
		t.Errorf(`disk-only row carries a "live" key, want it absent: %+v`, diskRow)
	}
}

// TestLiveRowMeasuringZeroPeersStillCarriesLiveFigures is the sharpest edge
// of TOR-136's acceptance criterion: a live row whose first heartbeat found
// nobody must still carry a "live" key, with peers and seeds genuinely 0 -
// distinguishable, at the wire, from the queued/disk rows above that carry
// no such key at all. Collapsing the two into the same absent shape is
// exactly the bug TOR-136 exists to prevent: a queued torrent and a running
// torrent that found nobody are opposite situations.
func TestLiveRowMeasuringZeroPeersStillCarriesLiveFigures(t *testing.T) {
	fake := newFakeRuns()
	cfg := DefaultConfig()
	srv, ts := newTestServerWithConfig(t, cfg, fake.runner)

	run := startRun(t, ts.URL, "magnet:?xt=urn:btih:2222")

	srv.mu.Lock()
	entry, ok := srv.runs[run.id]
	if !ok {
		srv.mu.Unlock()
		t.Fatalf("run %s is not in the registry", run.id)
	}
	// No rate, no swarm reading yet - only the first heartbeat, which found
	// nobody. Peers/Seeds are genuine measurements, not sentinels: unlike
	// DownloadRate/UploadRate/Swarm they are plain ints on core.Progress,
	// always sent, so 0 here means "checked, found nobody" from the very
	// first heartbeat on.
	entry.applyProgress(core.Progress{Peers: 0, Seeds: 0})
	srv.mu.Unlock()

	rows := rawRuns(t, ts.URL)
	row := byID(t, rows, run.id)

	live, has := row["live"].(map[string]any)
	if !has {
		t.Fatalf(`live row with a zero-peer heartbeat carries no "live" key, want one present: %+v`, row)
	}
	if live["peers"] != float64(0) || live["seeds"] != float64(0) {
		t.Errorf(`live = %+v, want peers=0 seeds=0`, live)
	}
	if _, has := live["download_bps"]; has {
		t.Errorf(`live carries "download_bps" before a second heartbeat: %+v`, live)
	}
	if _, has := live["upload_bps"]; has {
		t.Errorf(`live carries "upload_bps" before a second heartbeat: %+v`, live)
	}
	if _, has := live["swarm"]; has {
		t.Errorf(`live carries "swarm" before the availability reading is known: %+v`, live)
	}
}

// TestAProgressEventReachesTheLiveRow closes TOR-136's last gap, and it is the
// one test the rest of that ticket could not have: every other live-row test
// calls runEntry.applyProgress by hand, because server.go was being rebuilt
// for the queue width while they were written. Calling it by hand proves the
// shape downstream is right and says nothing about whether anything ever
// calls it - and for a while nothing did, so GET /runs reported every live
// row's figures as absent while every one of those tests passed.
//
// So this one puts a real core.Progress onto a run's event stream and reads
// the row back off the HTTP endpoint, with pump the only thing in between.
func TestAProgressEventReachesTheLiveRow(t *testing.T) {
	fake := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, DefaultConfig(), fake.runner)
	_ = srv

	const source = "magnet:?xt=urn:btih:f00d000000000000000000000000000000000f"
	run := startRun(t, ts.URL, source)
	stream := fake.stream(t, source)

	dl, ul := 4096.0, 512.0
	stream.events <- core.Progress{
		Peers: 5, Seeds: 3,
		DownloadRate: &dl, UploadRate: &ul,
		Swarm: &core.SwarmAvailability{CopiesPerPiece: 1.5, Unavailable: 0, NumPieces: 64},
	}

	// pump reads the channel on its own goroutine, so the row is not expected
	// to carry the reading the instant the send returns.
	deadline := time.Now().Add(10 * time.Second)
	for {
		row := findByID(t, listRuns(t, ts.URL), run.id)
		if row.Live != nil {
			if row.Live.Peers != 5 || row.Live.Seeds != 3 {
				t.Errorf("live.peers/seeds = %d/%d, want 5/3", row.Live.Peers, row.Live.Seeds)
			}
			if row.Live.DownloadBps == nil || *row.Live.DownloadBps != dl {
				t.Errorf("live.download_bps = %v, want %v", row.Live.DownloadBps, dl)
			}
			if row.Live.Swarm == nil || row.Live.Swarm.CopiesPerPiece != 1.5 {
				t.Errorf("live.swarm = %+v, want copies_per_piece 1.5", row.Live.Swarm)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a Progress event travelled through pump and the live row still " +
				"carries no figures - applyProgress is not being called")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAFinishedRunDropsItsLiveFigures is TestAProgressEventReachesTheLiveRow's
// mirror (TOR-154). A run that has ended must not go on reporting the last
// heartbeat it ever had - peers, seeds, both rates, the swarm reading and the
// stall reading all have to read absent, the same way a disk-only row
// (listing.go) always has.
//
// The first half is not optional. A test that only checked the row after the
// stream closed would pass just as well against a build where the figures
// never reached the row at all - proving "gone" means nothing unless the same
// row is first proven to have HAD them, so this drives a real core.Progress
// through pump exactly as the test above does, confirms the row carries it,
// and only then closes the stream and confirms the row carries nothing.
func TestAFinishedRunDropsItsLiveFigures(t *testing.T) {
	fake := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, DefaultConfig(), fake.runner)
	_ = srv

	const source = "magnet:?xt=urn:btih:f00dfeed000000000000000000000000000000"
	run := startRun(t, ts.URL, source)
	stream := fake.stream(t, source)

	dl, ul := 4096.0, 512.0
	stream.events <- core.Progress{
		Peers: 5, Seeds: 3,
		DownloadRate: &dl, UploadRate: &ul,
		Swarm: &core.SwarmAvailability{CopiesPerPiece: 1.5, Unavailable: 0, NumPieces: 64},
		Stall: &core.Stall{Code: core.CodeNoPeers, Since: 4 * time.Minute},
	}

	// First half: the reading has to actually arrive before there is
	// anything interesting about it disappearing.
	deadline := time.Now().Add(10 * time.Second)
	for {
		row := findByID(t, listRuns(t, ts.URL), run.id)
		if row.Live != nil {
			if row.Live.Peers != 5 || row.Live.Seeds != 3 {
				t.Fatalf("live.peers/seeds = %d/%d, want 5/3", row.Live.Peers, row.Live.Seeds)
			}
			if row.Live.DownloadBps == nil || *row.Live.DownloadBps != dl {
				t.Fatalf("live.download_bps = %v, want %v", row.Live.DownloadBps, dl)
			}
			if row.Live.Swarm == nil {
				t.Fatalf("live.swarm is absent before the run ended")
			}
			if row.Live.Stall == nil {
				t.Fatalf("live.stall is absent before the run ended")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a Progress event travelled through pump and the live row still " +
				"carries no figures - applyProgress is not being called")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Second half: end the run the way a real engine run ends - close the
	// event stream - and read the row back the same way a page would.
	fake.finish(t, source)

	deadline = time.Now().Add(10 * time.Second)
	for {
		row := findByID(t, listRuns(t, ts.URL), run.id)
		if row.State == string(RunDone) {
			if row.Live != nil {
				t.Fatalf("a finished run still carries live figures: %+v", row.Live)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never reached state %q, last seen %q", RunDone, row.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAStalledRunReportsItsStallOnGetRuns closes the seam TOR-141 left open
// on purpose: it built the Stall reading all the way to Live but could not
// fill it, because runs.go belonged to another ticket at the time. The
// reading reached a live page through the WebSocket regardless, so nothing
// looked broken — the gap was only visible in the instant after a page load
// or a reconnect, which is exactly when somebody opens the tab to find out
// why nothing is happening.
//
// The same gap, one layer out, cost TOR-147 and then TOR-136 a follow-up
// ticket each. This is the test that would have caught all three.
func TestAStalledRunReportsItsStallOnGetRuns(t *testing.T) {
	fake := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, DefaultConfig(), fake.runner)

	run := startRun(t, ts.URL, "magnet:?xt=urn:btih:5741100000000000000000000000000000000a")

	srv.mu.Lock()
	entry, ok := srv.runs[run.id]
	if !ok {
		srv.mu.Unlock()
		t.Fatalf("run %s is not in the registry", run.id)
	}
	entry.applyProgress(core.Progress{
		Peers: 0, Seeds: 0,
		Stall: &core.Stall{Code: core.CodeNoPeers, Since: 4*time.Minute + 12*time.Second},
	})
	srv.mu.Unlock()

	row := findByID(t, listRuns(t, ts.URL), run.id)
	if row.Live == nil || row.Live.Stall == nil {
		t.Fatalf("a stalled run carries no stall reading on GET /runs: %+v", row.Live)
	}
	if row.Live.Stall.Code != string(core.CodeNoPeers) {
		t.Errorf("stall.code = %q, want %q", row.Live.Stall.Code, core.CodeNoPeers)
	}
	// The duration is the load-bearing half: a cause with no time attached
	// cannot tell "normal" from "the answer".
	if want := (4*time.Minute + 12*time.Second).Milliseconds(); row.Live.Stall.SinceMS != want {
		t.Errorf("stall.since_ms = %d, want %d", row.Live.Stall.SinceMS, want)
	}
}

// TestAProgressingRunCarriesNoStall is the other arm, and without it the test
// above would pass for a build that reported every run as stalled.
func TestAProgressingRunCarriesNoStall(t *testing.T) {
	fake := newFakeRuns()
	srv, ts := newTestServerWithConfig(t, DefaultConfig(), fake.runner)

	run := startRun(t, ts.URL, "magnet:?xt=urn:btih:5741100000000000000000000000000000000b")

	srv.mu.Lock()
	entry := srv.runs[run.id]
	entry.applyProgress(core.Progress{Peers: 6, Seeds: 3})
	srv.mu.Unlock()

	row := findByID(t, listRuns(t, ts.URL), run.id)
	if row.Live == nil {
		t.Fatalf("a live row carries no figures at all: %+v", row)
	}
	if row.Live.Stall != nil {
		t.Errorf("a progressing run carries a stall reading: %+v", row.Live.Stall)
	}
}
