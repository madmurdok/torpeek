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
