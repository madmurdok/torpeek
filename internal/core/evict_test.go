package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"github.com/madmurdok/torpeek/internal/bridge"
	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/output"
	"github.com/madmurdok/torpeek/internal/swarm"
)

// TestRunEvictsStaleSetsAfterFinishing is TOR-43's acceptance criterion at
// the level Engine.run actually wires it in: a Config with a ceiling set
// must, after a run finishes and writes its own record, remove an unrelated
// stale set that pushes the tree over that ceiling - while never touching
// the run it just produced.
//
// The stale set is planted directly on disk rather than produced by a second
// live run: it only needs to look like a finished result set to Scan
// (cache.SaveRun plus a payload file), and building it that way keeps this
// test to one real swarm run instead of two.
func TestRunEvictsStaleSetsAfterFinishing(t *testing.T) {
	tools := locateTools(t)
	fixture := oneClipTorrent(t, tools, 8)
	seeder := fixture.StartSeeder(t)

	root := t.TempDir()

	// A stale, unrelated set: old enough to be the obvious oldest-first
	// pick, and large enough on its own to force eviction regardless of how
	// big the live run's own result turns out to be.
	staleHash := "1111111111111111111111111111111111111a"
	staleParams := "1111111111111111"
	staleDir := filepath.Join(root, staleHash, staleParams)
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatalf("create stale set dir: %v", err)
	}
	if err := cache.SaveRun(staleDir, cache.Run{
		Version: cache.Version, InfoHash: staleHash,
		CreatedAt: time.Now().Add(-30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("save stale run.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "payload.bin"), make([]byte, 5<<20), 0o600); err != nil {
		t.Fatalf("write stale payload: %v", err)
	}

	cfg := DefaultConfig(fixture.TorrentPath, root, t.TempDir())
	cfg.Swarm.DHT = false
	cfg.Swarm.MetadataTimeout = 10 * time.Second
	cfg.Swarm.Peers = []string{seeder}
	cfg.Profile = swarm.MinTraffic
	cfg.Plan = frames.Plan{Count: 1, Start: 0.1, End: 0.9}
	cfg.Budget = Budget{MaxBytes: 64 << 20, MaxTime: 2 * time.Minute, WarnAt: 0.8}
	cfg.Parallelism = 1
	cfg.Bridge = bridge.DefaultConfig()

	// The stale set alone (5 MiB) is well over this; the live run's own
	// result - a single frame, its sheet, manifest and .torrent - is not
	// expected to come anywhere near it, so eviction has exactly one
	// plausible set to remove.
	cfg.CacheCeiling = 1 << 20

	mi, err := metainfo.LoadFromFile(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("read the fixture's infohash: %v", err)
	}
	layout := output.Layout{Root: root, InfoHash: mi.HashInfoBytes().HexString(), Params: ParamsKey(cfg)}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	events, runErr := NewEngine(tools).Run(ctx, cfg)
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	var (
		done   *Done
		failed []Failed
	)
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case Done:
			done = &e
		case Failed:
			failed = append(failed, e)
		}
	}

	if done == nil {
		t.Fatal("the run published no Done")
	}
	for _, f := range failed {
		t.Errorf("the run reported a failure (%s: %v)", f.Code, f.Err)
	}
	if len(done.Warnings) != 0 {
		t.Errorf("Done.Warnings = %v, want none - eviction should have succeeded quietly", done.Warnings)
	}

	// Read the state back rather than trusting Done: the stale set must
	// actually be gone from disk.
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Errorf("stale set %s should have been evicted, stat returned err=%v", staleDir, err)
	}

	// And the run this process just finished must be completely unharmed -
	// it is the protected set, and it must still be a usable cache hit.
	record, ok := cache.LoadRun(layout.RunDir())
	if !ok {
		t.Fatalf("no run record at %s - the live run's own set must survive eviction", layout.RunDir())
	}
	if len(record.Complete) != 1 {
		t.Errorf("record.Complete = %v, want the one file this run finished", record.Complete)
	}
}

// TestRunSkipsEvictionWithNoCeilingSet is the default REQUIREMENTS.md 2.9
// promises: a Config that never set CacheCeiling must leave every existing
// set alone, however old or large, exactly as if cache.Evict had never been
// called.
func TestRunSkipsEvictionWithNoCeilingSet(t *testing.T) {
	tools := locateTools(t)
	fixture := oneClipTorrent(t, tools, 8)
	seeder := fixture.StartSeeder(t)

	root := t.TempDir()

	staleHash := "2222222222222222222222222222222222222b"
	staleParams := "2222222222222222"
	staleDir := filepath.Join(root, staleHash, staleParams)
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatalf("create stale set dir: %v", err)
	}
	if err := cache.SaveRun(staleDir, cache.Run{
		Version: cache.Version, InfoHash: staleHash,
		CreatedAt: time.Now().Add(-30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("save stale run.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "payload.bin"), make([]byte, 5<<20), 0o600); err != nil {
		t.Fatalf("write stale payload: %v", err)
	}

	cfg := DefaultConfig(fixture.TorrentPath, root, t.TempDir())
	cfg.Swarm.DHT = false
	cfg.Swarm.MetadataTimeout = 10 * time.Second
	cfg.Swarm.Peers = []string{seeder}
	cfg.Profile = swarm.MinTraffic
	cfg.Plan = frames.Plan{Count: 1, Start: 0.1, End: 0.9}
	cfg.Budget = Budget{MaxBytes: 64 << 20, MaxTime: 2 * time.Minute, WarnAt: 0.8}
	cfg.Parallelism = 1
	cfg.Bridge = bridge.DefaultConfig()
	// cfg.CacheCeiling left at its zero value.

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	events, runErr := NewEngine(tools).Run(ctx, cfg)
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	collect(t, events)

	if _, err := os.Stat(staleDir); err != nil {
		t.Errorf("stale set %s should have been left alone with no ceiling set, stat err=%v", staleDir, err)
	}
}

// TestRunEvictionProtectsASiblingRunsDirectory is TOR-132's acceptance
// criterion at the level the bug actually lives: not whether cache.Evict
// honours a list of live directories (evict_test.go in the cache package
// covers that), but whether Engine.run hands it every OTHER run's directory
// as well as its own.
//
// The sibling is planted in the shape that makes this dangerous rather than
// merely untidy - a RESUMED run. A fresh run has no run.json yet, so Scan
// reports Aged == false and skips it whatever the live list says; a resumed
// run reuses the directory its own earlier, incomplete attempt left behind,
// run.json and all, so it is aged, its CreatedAt is old, and it is the
// obvious oldest-first pick for the whole time it is being written back
// into. Only the explicit live list keeps it.
//
// A stale set that nobody marked live is planted alongside it and must still
// be removed. Without it a passing run would prove nothing: "protected" and
// "eviction never fired at all" look identical from the outside.
func TestRunEvictionProtectsASiblingRunsDirectory(t *testing.T) {
	tools := locateTools(t)
	fixture := oneClipTorrent(t, tools, 8)
	seeder := fixture.StartSeeder(t)

	root := t.TempDir()

	// Both planted sets look the same to Scan: aged, and old enough to be
	// picked before anything this run produces. The only difference between
	// them is that one is named live below and the other is not.
	plant := func(hash, params string, age time.Duration) string {
		dir := filepath.Join(root, hash, params)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create set dir %s: %v", dir, err)
		}
		if err := cache.SaveRun(dir, cache.Run{
			Version: cache.Version, InfoHash: hash,
			CreatedAt: time.Now().Add(-age),
		}); err != nil {
			t.Fatalf("save run.json in %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload.bin"), make([]byte, 5<<20), 0o600); err != nil {
			t.Fatalf("write payload in %s: %v", dir, err)
		}
		return dir
	}

	// The sibling is the OLDER of the two, so age alone would have it go
	// first: the live list has to be what saves it, not luck in the
	// ordering.
	sibling := plant("3333333333333333333333333333333333333c", "3333333333333333", 60*24*time.Hour)
	stale := plant("4444444444444444444444444444444444444d", "4444444444444444", 30*24*time.Hour)

	cfg := DefaultConfig(fixture.TorrentPath, root, t.TempDir())
	cfg.Swarm.DHT = false
	cfg.Swarm.MetadataTimeout = 10 * time.Second
	cfg.Swarm.Peers = []string{seeder}
	cfg.Profile = swarm.MinTraffic
	cfg.Plan = frames.Plan{Count: 1, Start: 0.1, End: 0.9}
	cfg.Budget = Budget{MaxBytes: 64 << 20, MaxTime: 2 * time.Minute, WarnAt: 0.8}
	cfg.Parallelism = 1
	cfg.Bridge = bridge.DefaultConfig()
	// Under either planted set on its own, so eviction has to reach for both
	// and is stopped only by the live list.
	cfg.CacheCeiling = 1 << 20

	engine := NewEngine(tools)
	// Stands in for a second run on this same Engine that is mid-write while
	// the run below finishes - which is what markLive means and all Evict
	// ever learns about it.
	engine.markLive(sibling)
	defer engine.unmarkLive(sibling)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	events, runErr := engine.Run(ctx, cfg)
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}

	var done *Done
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case Done:
			done = &e
		case Failed:
			t.Errorf("the run reported a failure (%s: %v)", e.Code, e.Err)
		}
	}
	if done == nil {
		t.Fatal("the run published no Done")
	}
	if len(done.Warnings) != 0 {
		t.Errorf("Done.Warnings = %v, want none - eviction should have succeeded quietly", done.Warnings)
	}

	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("the live sibling %s must survive a finishing run's eviction, stat err=%v", sibling, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the stale set %s should have been evicted, stat returned err=%v", stale, err)
	}
}

// TestLiveDirsRefcountsARepeatedDirectory covers the one case a plain set
// would get wrong: two runs can resolve to the same RunDir (same infohash,
// same ParamsKey) and overlap, and the first of them to finish must not
// unmark a directory the other is still writing.
func TestLiveDirsRefcountsARepeatedDirectory(t *testing.T) {
	e := NewEngine(ffmpeg.Tools{})
	const dir = "/tmp/torpeek-refcount/aaaa/bbbb"

	e.markLive(dir)
	e.markLive(dir)
	e.unmarkLive(dir)

	if got := e.liveDirs(); len(got) != 1 || got[0] != dir {
		t.Errorf("liveDirs() = %v after two marks and one unmark, want [%s] - the second run is still writing", got, dir)
	}

	e.unmarkLive(dir)
	if got := e.liveDirs(); len(got) != 0 {
		t.Errorf("liveDirs() = %v after both runs unmarked, want empty", got)
	}
}
