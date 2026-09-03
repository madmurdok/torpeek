package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// TestQueuedOrRunningNeverMergesWithADiskRecord: only a final live entry may
// merge. A live run must never be hidden behind a disk snapshot while it is
// still going - the queued/running row always carries its own live id and
// state, whatever a same-infohash disk record says.
func TestQueuedOrRunningNeverMergesWithADiskRecord(t *testing.T) {
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

	rows := listRuns(t, ts.URL)
	var matches []RunSummary
	for _, row := range rows {
		if row.InfoHash == hash {
			matches = append(matches, row)
		}
	}
	if len(matches) != 2 {
		t.Fatalf("run %s appears %d times while live, want 2 (the running row and the untouched disk row): %+v", hash, len(matches), matches)
	}
	for _, row := range matches {
		if row.ID == run.id && row.State != "running" {
			t.Errorf("the live row's state was overwritten: %+v", row)
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
