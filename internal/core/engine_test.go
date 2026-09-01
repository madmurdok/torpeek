package core

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/torpeek/torpeek/internal/bridge"
	"github.com/torpeek/torpeek/internal/ffmpeg"
	"github.com/torpeek/torpeek/internal/frames"
	"github.com/torpeek/torpeek/internal/swarm"
	"github.com/torpeek/torpeek/internal/torrenttest"
)

func locateTools(t *testing.T) ffmpeg.Tools {
	t.Helper()

	tools, err := ffmpeg.LocateIn()
	if err != nil {
		t.Skipf("no ffmpeg available: %v", err)
	}
	return tools
}

// multiFileTorrent renders several clips into one torrent, so the orchestrator
// is exercised the way a season pack would exercise it.
func multiFileTorrent(t *testing.T, tools ffmpeg.Tools, clips int, seconds int, bitrate string) (torrentPath, seeder string) {
	t.Helper()

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	for i := 0; i < clips; i++ {
		path := filepath.Join(dir, "episode-"+strconv.Itoa(i+1)+".mkv")
		if _, err := tools.Run(ctx, "ffmpeg",
			"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration="+strconv.Itoa(seconds),
			"-c:v", "libx264", "-g", "50", "-pix_fmt", "yuv420p", "-b:v", bitrate,
			path,
		); err != nil {
			t.Fatalf("render clip %d: %v", i, err)
		}
	}
	// A file that is not video, to check it is skipped rather than probed.
	if err := os.WriteFile(filepath.Join(dir, "readme.nfo"), []byte("release notes"), 0o600); err != nil {
		t.Fatalf("write nfo: %v", err)
	}

	fixture := torrenttest.BuildDir(t, dir, 256<<10)
	return fixture.TorrentPath, fixture.StartSeeder(t)
}

func runConfig(t *testing.T, torrentPath, seeder string) Config {
	t.Helper()

	cfg := DefaultConfig(torrentPath, t.TempDir(), t.TempDir())
	cfg.Swarm.DHT = false // offline
	cfg.Swarm.MetadataTimeout = 10 * time.Second
	cfg.Profile = swarm.MinTraffic
	cfg.Plan = frames.Plan{Count: 3, Start: 0.1, End: 0.9}
	cfg.Budget = Budget{MaxBytes: 64 << 20, MaxTime: 4 * time.Minute, WarnAt: 0.8}
	cfg.Parallelism = 2
	cfg.Bridge = bridge.DefaultConfig()
	return cfg
}

// collect drains an event stream, wiring in the seeder as soon as metadata
// arrives - there is no tracker or DHT in these tests.
func collect(t *testing.T, events <-chan Event) []Event {
	t.Helper()

	var seen []Event
	for ev := range events {
		seen = append(seen, ev)
	}
	return seen
}

// TestRunProducesFramesForEveryVideoFile is the acceptance test for TOR-21.
func TestRunProducesFramesForEveryVideoFile(t *testing.T) {
	tools := locateTools(t)
	torrentPath, seeder := multiFileTorrent(t, tools, 2, 30, "200k")

	cfg := runConfig(t, torrentPath, seeder)
	cfg.Swarm.Peers = []string{seeder}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var (
		metadata *MetadataReady
		started  int
		ready    int
		fileDone int
		done     *Done
		failures []Failed
		byFile   = map[int]int{}
	)

	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case MetadataReady:
			metadata = &e
		case FileStarted:
			started++
		case FrameReady:
			ready++
			byFile[e.File]++
			if _, err := os.Stat(e.Path); err != nil {
				t.Errorf("FrameReady points at %s, which is not on disk: %v", e.Path, err)
			}
		case FileDone:
			fileDone++
		case Failed:
			failures = append(failures, e)
		case Done:
			done = &e
		}
	}

	for _, f := range failures {
		t.Errorf("run reported failure: file %d, code %s: %v", f.File, f.Code, f.Err)
	}

	if metadata == nil {
		t.Fatal("no MetadataReady event")
	}
	if len(metadata.Videos) != 2 {
		t.Fatalf("selected %d video files, want 2 (the .nfo must be ignored)", len(metadata.Videos))
	}
	if started != 2 || fileDone != 2 {
		t.Errorf("started %d files and finished %d, want 2 and 2", started, fileDone)
	}
	if len(byFile) != 2 {
		t.Errorf("frames arrived for %d files, want 2", len(byFile))
	}
	for file, count := range byFile {
		if count != cfg.Plan.Count {
			t.Errorf("file %d produced %d frames, want %d", file, count, cfg.Plan.Count)
		}
	}
	if done == nil {
		t.Fatal("no Done event")
	}
	if done.Reason != StopCompleted {
		t.Errorf("Done.Reason = %q, want %q", done.Reason, StopCompleted)
	}
	if done.Frames != ready {
		t.Errorf("Done reports %d frames, but %d FrameReady events arrived", done.Frames, ready)
	}

	t.Logf("2 files x %d frames in %s for %d KiB",
		cfg.Plan.Count, done.Elapsed.Round(time.Millisecond), done.DownloadedByte/1024)
}

// TestSharedBudgetStopsTheRun: the ceiling applies across files, and a stopped
// run keeps what it already produced (section 2.6).
func TestSharedBudgetStopsTheRun(t *testing.T) {
	tools := locateTools(t)
	// Bigger clips, so a budget can land in the middle: with tiny files the
	// initial probe alone costs about as much as the whole file, leaving no
	// room between "no frames at all" and "everything finished".
	torrentPath, seeder := multiFileTorrent(t, tools, 2, 60, "1500k")

	cfg := runConfig(t, torrentPath, seeder)
	cfg.Swarm.Peers = []string{seeder}
	cfg.Parallelism = 1
	cfg.Plan = frames.Plan{Count: 8, Start: 0.1, End: 0.9}
	cfg.Budget = Budget{MaxBytes: 4 << 20}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	frameCount := 0
	var done *Done
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case FrameReady:
			frameCount++
			if _, err := os.Stat(e.Path); err != nil {
				t.Errorf("frame %s vanished: %v", e.Path, err)
			}
		case Done:
			done = &e
		}
	}

	if done == nil {
		t.Fatal("no Done event")
	}
	if done.Reason != StopBudget {
		t.Errorf("Done.Reason = %q, want %q", done.Reason, StopBudget)
	}
	if frameCount == 0 {
		t.Error("the run stopped without keeping any frames")
	}
	if frameCount >= 2*cfg.Plan.Count {
		t.Errorf("produced %d frames despite a budget too small for %d", frameCount, 2*cfg.Plan.Count)
	}
	if done.DownloadedByte < cfg.Budget.MaxBytes {
		t.Errorf("stopped for budget at %d bytes, below the %d ceiling", done.DownloadedByte, cfg.Budget.MaxBytes)
	}
	t.Logf("budget stopped the run after %d frames and %d KiB", frameCount, done.DownloadedByte/1024)
}

// TestCancellationKeepsWhatWasProduced backs section 2.10: cancelling must not
// throw away finished frames.
func TestCancellationKeepsWhatWasProduced(t *testing.T) {
	tools := locateTools(t)
	torrentPath, seeder := multiFileTorrent(t, tools, 1, 60, "1500k")

	cfg := runConfig(t, torrentPath, seeder)
	cfg.Swarm.Peers = []string{seeder}
	cfg.Plan = frames.Plan{Count: 12, Start: 0.05, End: 0.95}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var paths []string
	var done *Done

	for ev := range events {
		switch e := ev.(type) {
		case FrameReady:
			paths = append(paths, e.Path)
			if len(paths) == 2 {
				cancel() // stop mid-run, after real work has happened
			}
		case Done:
			done = &e
		}
	}

	if done == nil {
		t.Fatal("no Done event after cancellation")
	}
	if done.Reason != StopCancelled {
		t.Errorf("Done.Reason = %q, want %q", done.Reason, StopCancelled)
	}
	if len(paths) < 2 {
		t.Fatalf("only %d frames arrived before cancelling", len(paths))
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("frame %s was lost by cancellation: %v", p, err)
		}
	}
	t.Logf("cancelled after %d frames, all still on disk", len(paths))
}

func TestRunRejectsATorrentWithNoVideo(t *testing.T) {
	tools := locateTools(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.nfo"), []byte("nothing to see"), 0o600); err != nil {
		t.Fatalf("write nfo: %v", err)
	}
	fixture := torrenttest.BuildDir(t, dir, 64<<10)

	cfg := runConfig(t, fixture.TorrentPath, "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var failure *Failed
	for _, ev := range collect(t, events) {
		if f, ok := ev.(Failed); ok {
			failure = &f
		}
	}

	if failure == nil {
		t.Fatal("no Failed event for a torrent with no video")
	}
	if failure.Code != CodeNoVideo {
		t.Errorf("code = %q, want %q", failure.Code, CodeNoVideo)
	}
}

func TestParamsKeyDistinguishesWhatChangesTheResult(t *testing.T) {
	base := DefaultConfig("magnet:?xt=urn:btih:x", "/out", "/data")

	same := base
	same.Budget = Budget{MaxBytes: 1}
	same.Parallelism = 16
	if ParamsKey(base) != ParamsKey(same) {
		t.Error("budget and parallelism changed the cache key, but they do not change the result")
	}

	for name, mutate := range map[string]func(*Config){
		"count":   func(c *Config) { c.Plan.Count = 5 },
		"window":  func(c *Config) { c.Plan.End = 0.8 },
		"profile": func(c *Config) { c.Profile = swarm.MinTraffic },
		"format":  func(c *Config) { c.Format = frames.PNG },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if ParamsKey(base) == ParamsKey(changed) {
				t.Errorf("%s does change the result but not the cache key", name)
			}
		})
	}
}
