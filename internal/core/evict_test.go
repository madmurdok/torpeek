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
