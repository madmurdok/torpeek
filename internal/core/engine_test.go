package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/bridge"
	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/torrenttest"
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

// TestLandedNearUsesRealRunData feeds the guard the timestamps two real runs
// actually produced: the pairs from an MP4 where seeking worked, and the pairs
// from an AVI where every point came back at zero and was reported a success.
func TestLandedNearUsesRealRunData(t *testing.T) {
	sec := func(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

	// Twenty points across a 888s film: the spacing is the tolerance.
	good := seekTolerance([]time.Duration{sec(44.4), sec(86.5)})
	for _, c := range []struct{ requested, actual float64 }{
		{44.4, 43.7}, {128.5, 126.3}, {170.6, 162.1}, {296.8, 289.3}, {843.7, 833.6},
	} {
		if !landedNear(sec(c.requested), sec(c.actual), good) {
			t.Errorf("frame decoded at %.1fs for a point at %.1fs was rejected, but a keyframe "+
				"a few seconds early is ordinary", c.actual, c.requested)
		}
	}

	// The AVI: every point decoded position 0.
	bad := seekTolerance([]time.Duration{sec(175.3), sec(341.4)})
	for _, requested := range []float64{175.3, 341.4, 1836.1, 3330.8} {
		if landedNear(sec(requested), 0, bad) {
			t.Errorf("a frame decoded at 0s was accepted for a point at %.1fs", requested)
		}
	}
}

func TestSeekToleranceNeverGoesBelowAKeyframeInterval(t *testing.T) {
	sec := func(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

	// A dense plan over a short clip: points can sit closer together than the
	// file's keyframes, and that must not turn ordinary frames into misses.
	if got := seekTolerance([]time.Duration{sec(3), sec(5.85)}); got != minSeekTolerance {
		t.Errorf("tolerance = %s for points 2.85s apart, want the %s floor", got, minSeekTolerance)
	}
	if got := seekTolerance(nil); got != minSeekTolerance {
		t.Errorf("tolerance = %s with no points, want the %s floor", got, minSeekTolerance)
	}
	if got := seekTolerance([]time.Duration{sec(175.3), sec(341.4)}); got != sec(166.1) {
		t.Errorf("tolerance = %s, want the spacing between the points", got)
	}
}

// TestRunProcessesOnlyTheSelectedFile is the point of the selection: testing
// or previewing one file of a pack must not cost a run over all of them.
func TestRunProcessesOnlyTheSelectedFile(t *testing.T) {
	tools := locateTools(t)
	torrentPath, seeder := multiFileTorrent(t, tools, 3, 20, "200k")

	cfg := runConfig(t, torrentPath, seeder)
	cfg.Swarm.Peers = []string{seeder}
	cfg.Files = []string{"episode-2"}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var (
		metadata *MetadataReady
		started  []string
		byFile   = map[int]int{}
		done     *Done
	)
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case MetadataReady:
			metadata = &e
		case FileStarted:
			started = append(started, e.Path)
		case FrameReady:
			byFile[e.File]++
		case Failed:
			t.Errorf("unexpected failure: %s: %v", e.Code, e.Err)
		case Done:
			done = &e
		}
	}

	if metadata == nil || done == nil {
		t.Fatal("run produced no metadata or no terminal event")
	}
	if len(metadata.Videos) != 3 {
		t.Errorf("metadata reports %d video files, want all 3 the torrent holds", len(metadata.Videos))
	}
	if len(metadata.Selected) != 1 {
		t.Errorf("metadata reports %d selected, want 1", len(metadata.Selected))
	}
	if len(started) != 1 {
		t.Fatalf("started %d files (%v), want only the selected one", len(started), started)
	}
	if !strings.Contains(started[0], "episode-2") {
		t.Errorf("started %q, want episode-2", started[0])
	}
	if len(byFile) != 1 || byFile[metadata.Selected[0]] != cfg.Plan.Count {
		t.Errorf("frames by file = %v, want %d frames for file %v only",
			byFile, cfg.Plan.Count, metadata.Selected)
	}
	if done.Files != 1 {
		t.Errorf("Done reports %d files, want 1", done.Files)
	}
}

// TestRunRefusesASelectionThatMatchesNothing: the caller has to be able to
// tell "you asked for a file that is not here" from "there was nothing worth
// taking", which look the same from outside.
func TestRunRefusesASelectionThatMatchesNothing(t *testing.T) {
	tools := locateTools(t)
	torrentPath, seeder := multiFileTorrent(t, tools, 2, 15, "200k")

	cfg := runConfig(t, torrentPath, seeder)
	cfg.Swarm.Peers = []string{seeder}
	cfg.Files = []string{"episode-9"}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var (
		sawMetadata bool
		failure     *Failed
		frames      int
	)
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case MetadataReady:
			sawMetadata = true
			if len(e.Videos) != 2 {
				t.Errorf("metadata reports %d videos, want the 2 the torrent holds", len(e.Videos))
			}
		case FrameReady:
			frames++
		case Failed:
			failure = &e
		}
	}

	if !sawMetadata {
		t.Error("no metadata event - a caller whose selection missed still needs to see what was there")
	}
	if frames != 0 {
		t.Errorf("%d frames were taken despite the selection matching nothing", frames)
	}
	if failure == nil {
		t.Fatal("run did not fail on a selection matching nothing")
	}
	if failure.Code != CodeNoFileMatch {
		t.Errorf("failure code = %q, want %q", failure.Code, CodeNoFileMatch)
	}
	if !strings.Contains(failure.Err.Error(), "episode-1.mkv") {
		t.Errorf("failure %q does not say what could have been named instead", failure.Err)
	}
}

// TestBudgetScalesWithTheSelection, not with what the torrent happens to hold.
func TestBudgetScalesWithTheSelection(t *testing.T) {
	cfg := Config{} // no explicit budget, so the default applies

	one := budgetFor(cfg, 1)
	twenty := budgetFor(cfg, 20)

	if one.MaxBytes != DefaultBudget(1).MaxBytes {
		t.Errorf("one selected file gets %d bytes, want one file's worth (%d)",
			one.MaxBytes, DefaultBudget(1).MaxBytes)
	}
	if twenty.MaxBytes <= one.MaxBytes {
		t.Errorf("twenty files get %d bytes, not more than one file's %d",
			twenty.MaxBytes, one.MaxBytes)
	}

	explicit := Config{Budget: Budget{MaxBytes: 7 << 20}}
	if got := budgetFor(explicit, 20); got.MaxBytes != 7<<20 {
		t.Errorf("an explicit ceiling became %d; it must be left alone", got.MaxBytes)
	}
}

// fakeAvailability stands in for the swarm so the shifting rules can be tested
// without one - including the case where there is no swarm yet.
type fakeAvailability struct {
	// known says whether the swarm has told us anything yet.
	known bool
	// holes are byte ranges within the file that nobody holds.
	holes [][2]int64
	// pieceLength sets how wide a region reachability is judged over.
	pieceLength int64
}

func (f fakeAvailability) Known() bool { return f.known }

func (f fakeAvailability) PieceLength() int64 { return f.pieceLength }

func (f fakeAvailability) OverFileRange(_ swarm.FileInfo, off, length int64) int {
	for _, h := range f.holes {
		if off < h[1] && h[0] < off+length {
			return 0
		}
	}
	return 3
}

func TestShiftCandidatesTryNearestFirstAndBothSides(t *testing.T) {
	at := 100 * time.Second
	got := shiftCandidates(at, 10*time.Second, 300*time.Second)

	want := []time.Duration{
		100 * time.Second,
		110 * time.Second, 90 * time.Second,
		120 * time.Second, 80 * time.Second,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %s, want %s (%v)", i, got[i], want[i], got)
		}
	}

	// Nothing outside the file, and nothing at all without a step.
	near := shiftCandidates(5*time.Second, 10*time.Second, 30*time.Second)
	for _, c := range near {
		if c <= 0 || c >= 30*time.Second {
			t.Errorf("candidate %s falls outside the file", c)
		}
	}
	if got := shiftCandidates(at, 0, 300*time.Second); len(got) != 1 || got[0] != at {
		t.Errorf("with no step the only candidate should be the point itself, got %v", got)
	}
}

// TestReachableWithNoPeersSaysYes is the trap this rule exists around: a map
// read before any peer has sent a bitfield reports every piece missing, and
// believing it would skip every frame of a perfectly healthy run.
func TestReachableSaysYesUntilTheSwarmHasSpoken(t *testing.T) {
	file := swarm.FileInfo{Index: 0, Path: "movie.mkv", Length: 100 << 20}
	nobody := fakeAvailability{known: false, pieceLength: 1 << 20, holes: [][2]int64{{0, 100 << 20}}}

	if !reachable(nobody, swarm.MinTraffic, file, time.Hour, 30*time.Minute) {
		t.Error("a point was called unreachable before the swarm had said anything")
	}
}

func TestReachableFollowsTheHoles(t *testing.T) {
	const length = 100 << 20
	file := swarm.FileInfo{Index: 0, Path: "movie.mkv", Length: length}
	duration := time.Hour

	// Nobody holds the second quarter of the file.
	avail := fakeAvailability{known: true, pieceLength: 1 << 20, holes: [][2]int64{{length / 4, length / 2}}}

	if reachable(avail, swarm.MinTraffic, file, duration, 20*time.Minute) {
		t.Error("a point inside the hole was called reachable")
	}
	if !reachable(avail, swarm.MinTraffic, file, duration, 45*time.Minute) {
		t.Error("a point in a served region was called unreachable")
	}

	// A file of unknown length or duration cannot be judged, so it is not.
	if !reachable(avail, swarm.MinTraffic, swarm.FileInfo{}, duration, time.Minute) {
		t.Error("a file with no length should not be judged")
	}
	if !reachable(avail, swarm.MinTraffic, file, 0, time.Minute) {
		t.Error("a file with no duration should not be judged")
	}
}

// TestRunShiftsPastAnUnavailableRegion is the acceptance criterion, against a
// swarm that genuinely cannot serve part of the file: the seeder's copy is
// wrong there, so those pieces fail its own verification and it never offers
// them. The frame set must still be complete, and the moved point must say so.
func TestRunShiftsPastAnUnavailableRegion(t *testing.T) {
	tools := locateTools(t)

	dir := t.TempDir()
	renderCtx, renderCancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer renderCancel()
	if _, err := tools.Run(renderCtx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration=60",
		"-c:v", "libx264", "-g", "50", "-pix_fmt", "yuv420p", "-b:v", "1500k",
		filepath.Join(dir, "movie.mkv"),
	); err != nil {
		t.Fatalf("render clip: %v", err)
	}

	fixture := torrenttest.BuildDir(t, dir, 256<<10)
	// Nobody holds the middle of the file. A point planned there has to move.
	seeder := fixture.StartSeederWithHole(t, 0.42, 0.58)

	cfg := runConfig(t, fixture.TorrentPath, seeder)
	cfg.Swarm.Peers = []string{seeder}
	cfg.Plan = frames.Plan{Count: 3, Start: 0.1, End: 0.9}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var (
		ready   []FrameReady
		skipped []FrameSkipped
		done    *Done
	)
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case FrameReady:
			ready = append(ready, e)
		case FrameSkipped:
			skipped = append(skipped, e)
		case Failed:
			t.Errorf("unexpected failure: %s: %v", e.Code, e.Err)
		case Done:
			done = &e
		}
	}

	for _, s := range skipped {
		t.Logf("skipped frame %d at %s: %s: %s", s.Index, s.Requested, s.Code, s.Reason)
	}
	if len(ready) != cfg.Plan.Count {
		t.Fatalf("produced %d frames, want the full set of %d despite the hole",
			len(ready), cfg.Plan.Count)
	}
	if done == nil || done.Reason != StopCompleted {
		t.Errorf("run did not complete: %+v", done)
	}

	var shifted int
	for _, f := range ready {
		if _, err := os.Stat(f.Path); err != nil {
			t.Errorf("frame %d is not on disk: %v", f.Index, err)
		}
		if f.Shift == ShiftUnavailable {
			shifted++
			if f.Actual == f.Requested {
				t.Errorf("frame %d is marked shifted but came from the requested %s",
					f.Index, f.Requested)
			}
			t.Logf("frame %d asked for %s, taken at %s, marked %q",
				f.Index, f.Requested.Round(time.Second), f.Actual.Round(time.Second), f.Shift)
		}
	}
	if shifted == 0 {
		t.Error("no frame was marked shifted, so the hole was never noticed")
	}

	// The other half of the criterion: the shift has to survive into the
	// record, not just the event stream.
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(ready[0].Path)), manifest.Name))
	if err != nil {
		t.Fatalf("no manifest next to the frames: %v", err)
	}
	var m manifest.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest is not readable: %v", err)
	}

	if len(m.Frames) != cfg.Plan.Count {
		t.Errorf("manifest lists %d capture points, want all %d", len(m.Frames), cfg.Plan.Count)
	}
	var markedShifted int
	for _, f := range m.Frames {
		if f.Shift == manifest.ShiftUnavailable {
			markedShifted++
			if f.ActualMS == nil || *f.ActualMS == f.RequestedMS {
				t.Errorf("frame %d is marked shifted but records no different timestamp", f.Index)
			}
		}
	}
	if markedShifted != shifted {
		t.Errorf("manifest marks %d frames shifted, the run reported %d", markedShifted, shifted)
	}

	// The availability map should carry the hole that caused all this.
	var holes int
	for _, bucket := range m.Torrent.Availability {
		if bucket == 0 {
			holes++
		}
	}
	if len(m.Torrent.Availability) == 0 {
		t.Error("manifest carries no availability map")
	} else if holes == 0 {
		t.Errorf("availability map %v shows no gap, though a third of the file is unserved",
			m.Torrent.Availability)
	}

	if m.Cost.DownloadedBytes <= 0 || m.Cost.LimitBytes <= 0 {
		t.Errorf("manifest cost is empty: %+v", m.Cost)
	}
	if m.Video.Width != 640 || m.Video.Height != 360 || m.Video.Codec == "" {
		t.Errorf("manifest video summary is wrong: %+v", m.Video)
	}
	if m.Tool == "" || m.Version != manifest.Version {
		t.Errorf("manifest does not identify itself: version=%d tool=%q", m.Version, m.Tool)
	}
}

// TestRunStepsPastABlankFrame is the acceptance criterion for TOR-12: a
// capture point that lands on a black frame must not be written as-is. The
// clip's opening four seconds are pure black, its remainder ordinary
// testsrc content, and the plan's one capture point sits at 1.5s - deep
// inside the black lead-in, with a keyframe every second so the point does
// not simply land past the black region by luck.
func TestRunStepsPastABlankFrame(t *testing.T) {
	tools := locateTools(t)

	dir := t.TempDir()
	renderCtx, renderCancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer renderCancel()
	if _, err := tools.Run(renderCtx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=black:s=640x360:d=4:r=25",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration=26",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[out]", "-map", "[out]",
		"-c:v", "libx264", "-g", "25", "-pix_fmt", "yuv420p", "-b:v", "1500k",
		filepath.Join(dir, "movie.mkv"),
	); err != nil {
		t.Fatalf("render clip: %v", err)
	}

	fixture := torrenttest.BuildDir(t, dir, 256<<10)
	seeder := fixture.StartSeeder(t)

	cfg := runConfig(t, fixture.TorrentPath, seeder)
	cfg.Swarm.Peers = []string{seeder}
	// One point, deliberately placed at 1.5s - the middle of the window
	// Points() picks for Count 1 is (Start+End)/2 of the duration, so this
	// window centres on 1.5s out of a 30s file.
	cfg.Plan = frames.Plan{Count: 1, Start: 0.045, End: 0.055}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var (
		ready   []FrameReady
		skipped []FrameSkipped
		done    *Done
	)
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case FrameReady:
			ready = append(ready, e)
		case FrameSkipped:
			skipped = append(skipped, e)
		case Failed:
			t.Errorf("unexpected failure: %s: %v", e.Code, e.Err)
		case Done:
			done = &e
		}
	}

	for _, s := range skipped {
		t.Logf("skipped frame %d at %s: %s: %s", s.Index, s.Requested, s.Code, s.Reason)
	}
	if len(ready) != 1 {
		t.Fatalf("produced %d frames, want the single planned point despite the black lead-in", len(ready))
	}
	if done == nil || done.Reason != StopCompleted {
		t.Errorf("run did not complete: %+v", done)
	}

	frame := ready[0]
	if frame.Shift != ShiftBlank {
		t.Errorf("frame was not marked %q, got %q", ShiftBlank, frame.Shift)
	}
	if frame.Actual == frame.Requested {
		t.Errorf("frame is marked stepped but came from the requested %s", frame.Requested)
	}
	t.Logf("frame asked for %s, taken at %s, marked %q",
		frame.Requested.Round(time.Millisecond), frame.Actual.Round(time.Millisecond), frame.Shift)

	data, err := os.ReadFile(frame.Path)
	if err != nil {
		t.Fatalf("frame is not on disk: %v", err)
	}
	blank, err := frames.IsBlank(data)
	if err != nil {
		t.Fatalf("the written frame does not even decode: %v", err)
	}
	if blank {
		t.Error("the written frame is still blank - stepping did not escape the black region")
	}

	// The other half of the criterion: the shift has to survive into the
	// record, not just the event stream.
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(frame.Path)), manifest.Name))
	if err != nil {
		t.Fatalf("no manifest next to the frame: %v", err)
	}
	var m manifest.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest is not readable: %v", err)
	}
	if len(m.Frames) != 1 {
		t.Fatalf("manifest lists %d capture points, want 1", len(m.Frames))
	}
	if m.Frames[0].Shift != manifest.ShiftBlank {
		t.Errorf("manifest marks the frame %q, want %q", m.Frames[0].Shift, manifest.ShiftBlank)
	}
	if m.Frames[0].ActualMS == nil || *m.Frames[0].ActualMS == m.Frames[0].RequestedMS {
		t.Error("manifest records no timestamp different from the request, though the frame was stepped")
	}
}

// TestRerunIsServedFromDiskWithoutTheSwarm is acceptance criterion 3: the same
// torrent and the same parameters twice must cost nothing the second time.
//
// The seeder is stopped before the second run, so a cache that quietly went to
// the network would not merely be slow - it would fail outright. That is the
// point: "issued no requests" is asserted by removing the only thing that
// could have answered them.
func TestRerunIsServedFromDiskWithoutTheSwarm(t *testing.T) {
	tools := locateTools(t)
	torrentPath, seeder := multiFileTorrent(t, tools, 2, 20, "200k")

	out := t.TempDir()
	data := t.TempDir()

	first := DefaultConfig(torrentPath, out, data)
	first.Swarm.DHT = false
	first.Swarm.MetadataTimeout = 10 * time.Second
	first.Swarm.Peers = []string{seeder}
	first.Profile = swarm.MinTraffic
	first.Plan = frames.Plan{Count: 3, Start: 0.1, End: 0.9}
	first.Budget = Budget{MaxBytes: 64 << 20, MaxTime: 4 * time.Minute, WarnAt: 0.8}
	first.Parallelism = 2

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, first)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	var (
		firstFrames int
		firstDone   *Done
		paths       []string
	)
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case FrameReady:
			firstFrames++
			paths = append(paths, e.Path)
		case Failed:
			t.Fatalf("first run failed: %s: %v", e.Code, e.Err)
		case Done:
			firstDone = &e
		}
	}
	if firstDone == nil || firstFrames != 6 {
		t.Fatalf("first run produced %d frames: %+v", firstFrames, firstDone)
	}
	if firstDone.DownloadedByte == 0 {
		t.Fatal("the first run downloaded nothing, so the second proves nothing")
	}

	// The second run is left with nobody to ask: no peers, no DHT, no
	// trackers, and a fresh piece directory so it cannot quietly re-read what
	// the first one fetched. Its ceiling is seconds, so a cache miss fails
	// loudly instead of grinding.
	if err := os.RemoveAll(data); err != nil {
		t.Fatalf("remove piece directory: %v", err)
	}

	second := first
	second.Swarm.DataDir = t.TempDir()
	second.Swarm.Peers = nil
	second.Swarm.MetadataTimeout = 3 * time.Second
	second.Budget = Budget{MaxBytes: 8 << 20, MaxTime: 8 * time.Second, WarnAt: 0.8}

	rerunCtx, rerunCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rerunCancel()

	events, err = NewEngine(tools).Run(rerunCtx, second)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	var (
		secondFrames int
		secondPaths  []string
		secondDone   *Done
		metadata     *MetadataReady
		started      int
	)
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case MetadataReady:
			metadata = &e
		case FileStarted:
			started++
		case FrameReady:
			secondFrames++
			secondPaths = append(secondPaths, e.Path)
		case Failed:
			t.Errorf("second run failed: %s: %v", e.Code, e.Err)
		case Done:
			secondDone = &e
		}
	}

	if secondDone == nil {
		t.Fatal("second run produced no terminal event")
	}
	if secondDone.DownloadedByte != 0 {
		t.Errorf("second run downloaded %d bytes; an identical rerun must not go to the swarm",
			secondDone.DownloadedByte)
	}
	if secondDone.Reason != StopCompleted {
		t.Errorf("second run ended as %q, want a completed run", secondDone.Reason)
	}
	if secondFrames != firstFrames {
		t.Errorf("second run reported %d frames, first reported %d", secondFrames, firstFrames)
	}
	if started != 2 || metadata == nil || len(metadata.Videos) != 2 {
		t.Errorf("a cached run should look like a live one: %d files started, metadata %+v",
			started, metadata)
	}
	// Compared as sets: a live run works on files in parallel, so its events
	// interleave, while a replay is file by file. The order is not the promise
	// - the frames are.
	sort.Strings(paths)
	sort.Strings(secondPaths)
	for i := range secondPaths {
		if secondPaths[i] != paths[i] {
			t.Errorf("cached run served %q where the first run wrote %q", secondPaths[i], paths[i])
		}
	}

	// TOR-52: the record the first run left behind must carry the source it
	// came from and its plan in readable form - not just what serving a
	// rerun from disk needs, which is everything asserted above already.
	layout := output.Layout{Root: first.OutputRoot, InfoHash: metadata.InfoHash, Params: ParamsKey(first)}
	record, ok := cache.LoadRun(layout.RunDir())
	if !ok {
		t.Fatal("no run record for the first run")
	}
	if record.Source != torrentPath {
		t.Errorf("record source = %q, want the path a person could rerun: %q", record.Source, torrentPath)
	}
	wantPlan := cache.Plan{
		Count: first.Plan.Count, Start: first.Plan.Start, End: first.Plan.End,
		Profile: first.Profile.Name, Format: string(first.Format), Sequential: first.Sequential,
	}
	if record.Plan != wantPlan {
		t.Errorf("record plan = %+v, want %+v", record.Plan, wantPlan)
	}
}

// TestCacheMissesWhenAFrameIsGone: a manifest is a promise about files on
// disk, and a promise about a deleted file has to be treated as no promise.
func TestCacheMissesWhenAFrameIsGone(t *testing.T) {
	tools := locateTools(t)
	torrentPath, seeder := multiFileTorrent(t, tools, 1, 20, "200k")

	out := t.TempDir()
	cfg := runConfig(t, torrentPath, seeder)
	cfg.OutputRoot = out
	cfg.Swarm.Peers = []string{seeder}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	var written []string
	for _, ev := range collect(t, events) {
		if f, ok := ev.(FrameReady); ok {
			written = append(written, f.Path)
		}
	}
	if len(written) == 0 {
		t.Fatal("first run produced no frames")
	}

	if err := os.Remove(written[0]); err != nil {
		t.Fatalf("remove a frame: %v", err)
	}

	events, err = NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	var done *Done
	for _, ev := range collect(t, events) {
		if d, ok := ev.(Done); ok {
			done = &d
		}
	}
	if done == nil {
		t.Fatal("second run produced no terminal event")
	}

	// Downloaded bytes cannot tell the two apart here - a cache hit would also
	// download nothing, and (TOR-56) the first run's own pieces are gone by
	// the time this second run starts anyway, so a live rerun downloads the
	// missing point again from the seeder, still live. What settles it is the
	// file: a cache hit would have served the manifest and left the gap.
	info, err := os.Stat(written[0])
	if err != nil {
		t.Fatalf("the deleted frame was not produced again: %v", err)
	}
	if info.Size() == 0 {
		t.Error("the frame came back empty")
	}
}

// TestSecondRunFillsTheGapsRatherThanStartingOver is REQUIREMENTS.md section
// 2.10: a run stopped by its budget keeps what it produced, and the next one
// takes only the points that are missing.
//
// Reuse is proven by modification time. A frame that was rewritten would carry
// a new one; the ones from the first run must be untouched.
func TestSecondRunFillsTheGapsRatherThanStartingOver(t *testing.T) {
	tools := locateTools(t)
	torrentPath, seeder := multiFileTorrent(t, tools, 1, 60, "600k")

	out := t.TempDir()
	data := t.TempDir()

	base := DefaultConfig(torrentPath, out, data)
	base.Swarm.DHT = false
	base.Swarm.MetadataTimeout = 10 * time.Second
	base.Swarm.Peers = []string{seeder}
	base.Profile = swarm.MinTraffic
	base.Plan = frames.Plan{Count: 8, Start: 0.1, End: 0.9}
	base.Parallelism = 1

	first := base
	first.Budget = Budget{MaxBytes: 64 << 20, MaxTime: 4 * time.Minute, WarnAt: 0.8}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Stopped by cancelling after a few frames rather than by a byte ceiling:
	// where a budget lands depends on how the clip happened to compress, and a
	// test that needs "some but not all" cannot be left to that. This is also
	// the other half of section 2.10 - a cancelled run keeps its frames.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	events, err := NewEngine(tools).Run(runCtx, first)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	const stopAfter = 3
	firstPaths := map[int]string{}
	var firstDone *Done
	for ev := range events {
		switch e := ev.(type) {
		case FrameReady:
			firstPaths[e.Index] = e.Path
			if len(firstPaths) == stopAfter {
				stop()
			}
		case Done:
			firstDone = &e
		}
	}
	if firstDone == nil {
		t.Fatal("first run produced no terminal event")
	}
	if len(firstPaths) == 0 || len(firstPaths) >= base.Plan.Count {
		t.Fatalf("first run produced %d of %d frames; the test needs it stopped partway",
			len(firstPaths), base.Plan.Count)
	}
	t.Logf("first run: %d of %d frames, %d bytes, reason %q",
		len(firstPaths), base.Plan.Count, firstDone.DownloadedByte, firstDone.Reason)

	stamps := map[int]time.Time{}
	for index, path := range firstPaths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("frame %d is not on disk after a stopped run: %v", index, err)
		}
		stamps[index] = info.ModTime()
	}

	// Enough budget to finish, and a moment's gap so a rewrite would show.
	second := base
	second.Budget = Budget{MaxBytes: 64 << 20, MaxTime: 4 * time.Minute, WarnAt: 0.8}
	time.Sleep(1100 * time.Millisecond)

	events, err = NewEngine(tools).Run(ctx, second)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	secondPaths := map[int]string{}
	var secondDone *Done
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case FrameReady:
			secondPaths[e.Index] = e.Path
		case Failed:
			t.Errorf("second run failed: %s: %v", e.Code, e.Err)
		case Done:
			secondDone = &e
		}
	}

	if secondDone == nil || secondDone.Reason != StopCompleted {
		t.Fatalf("second run did not complete: %+v", secondDone)
	}
	if len(secondPaths) != base.Plan.Count {
		t.Errorf("second run reported %d frames, want the full set of %d",
			len(secondPaths), base.Plan.Count)
	}

	for index, when := range stamps {
		info, err := os.Stat(secondPaths[index])
		if err != nil {
			t.Errorf("frame %d disappeared: %v", index, err)
			continue
		}
		if !info.ModTime().Equal(when) {
			t.Errorf("frame %d was written again (%s then %s); a second run should keep what it has",
				index, when.Format(time.RFC3339Nano), info.ModTime().Format(time.RFC3339Nano))
		}
	}

	// And the record now describes a whole file.
	m, ok := cache.LoadManifest(filepath.Dir(filepath.Dir(secondPaths[0])))
	if !ok {
		t.Fatal("no manifest after the second run")
	}
	if len(m.Frames) != base.Plan.Count {
		t.Errorf("manifest lists %d points, want %d", len(m.Frames), base.Plan.Count)
	}
	for _, f := range m.Frames {
		if f.Shift == manifest.ShiftFailed {
			t.Errorf("point %d is still unfilled: %s", f.Index, f.Error)
		}
	}
}

// TestRunDiscardsItsOwnPiecesButKeepsResults is the acceptance criterion for
// TOR-56: a run's raw pieces are staging data (REQUIREMENTS.md 2.9), and by
// the time the run has reported Done, its own <DataDir>/<infohash>/ subtree
// must be gone - not merely correct in theory, but actually gone from the
// tree - while its frames and manifest, and the data directory itself
// (unlike the old behaviour, described only as "when the process exits"),
// survive it. It measures the tree before and after, not the code.
func TestRunDiscardsItsOwnPiecesButKeepsResults(t *testing.T) {
	tools := locateTools(t)
	torrentPath, seeder := multiFileTorrent(t, tools, 1, 20, "200k")

	cfg := runConfig(t, torrentPath, seeder)
	cfg.Swarm.Peers = []string{seeder}
	dataDir := cfg.Swarm.DataDir

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var (
		infoHash string
		frame    string
		done     *Done
	)
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case MetadataReady:
			infoHash = e.InfoHash
		case FrameReady:
			frame = e.Path
		case Failed:
			t.Errorf("unexpected failure: %s: %v", e.Code, e.Err)
		case Done:
			done = &e
		}
	}
	if infoHash == "" {
		t.Fatal("no MetadataReady event, so there is no infohash to check")
	}
	if frame == "" || done == nil {
		t.Fatal("run produced no frame or no terminal event")
	}

	pieces := filepath.Join(dataDir, infoHash)
	if _, err := os.Stat(pieces); !os.IsNotExist(err) {
		t.Errorf("piece directory %s still exists after the run ended: %v", pieces, err)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Errorf("the data directory itself was removed, but it is not this run's to remove: %v", err)
	}
	if info, err := os.Stat(frame); err != nil || info.Size() == 0 {
		t.Errorf("frame %s did not survive piece cleanup: %v", frame, err)
	}
	manifestPath := filepath.Join(filepath.Dir(filepath.Dir(frame)), manifest.Name)
	if info, err := os.Stat(manifestPath); err != nil || info.Size() == 0 {
		t.Errorf("manifest %s did not survive piece cleanup: %v", manifestPath, err)
	}
}

// TestCancelledRunAlsoDiscardsItsPieces: a run stopped halfway is not exempt
// - REQUIREMENTS.md 2.10 only promises that its frames survive, never that
// its pieces do, and resume (serveFromCache, reusableFrames) reads only the
// output directory, never the piece cache, so there is nothing for a later
// run to lose by this.
func TestCancelledRunAlsoDiscardsItsPieces(t *testing.T) {
	tools := locateTools(t)
	torrentPath, seeder := multiFileTorrent(t, tools, 1, 60, "1500k")

	cfg := runConfig(t, torrentPath, seeder)
	cfg.Swarm.Peers = []string{seeder}
	cfg.Plan = frames.Plan{Count: 12, Start: 0.05, End: 0.95}
	dataDir := cfg.Swarm.DataDir

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var (
		infoHash string
		kept     []string
		done     *Done
	)
	for ev := range events {
		switch e := ev.(type) {
		case MetadataReady:
			infoHash = e.InfoHash
		case FrameReady:
			kept = append(kept, e.Path)
			if len(kept) == 2 {
				cancel()
			}
		case Done:
			done = &e
		}
	}

	if infoHash == "" {
		t.Fatal("no MetadataReady event")
	}
	if done == nil || done.Reason != StopCancelled {
		t.Fatalf("run did not report itself cancelled: %+v", done)
	}
	if len(kept) < 2 {
		t.Fatalf("only %d frames arrived before cancelling", len(kept))
	}

	pieces := filepath.Join(dataDir, infoHash)
	if _, err := os.Stat(pieces); !os.IsNotExist(err) {
		t.Errorf("piece directory %s still exists after a cancelled run: %v", pieces, err)
	}
	for _, p := range kept {
		if info, err := os.Stat(p); err != nil || info.Size() == 0 {
			t.Errorf("frame %s was lost when pieces were discarded: %v", p, err)
		}
	}
}

// indexlessTorrent renders a raw MPEG-4 Part 2 elementary stream: ffprobe
// opens it fine and reports the video track's codec, resolution and frame
// rate, but no "duration" field at all - exactly the container-with-no-index
// case REQUIREMENTS.md 2.7 gates the sequential fallback on.
//
// This is deliberately not a raw H.264 (-f h264) stream, which was tried
// first: ffprobe reports no per-packet timestamp whatsoever for one (not
// even for the keyframe at byte 0), and ffmpeg's own input seeking ("-ss"
// before "-i", exactly what frames.Extractor uses) fails outright against
// it - "could not seek to position 0.000" - even asking for the very start.
// A raw h264 stream cannot be sequentially sampled through the existing,
// unchanged capture pipeline at all, only decoded start-to-finish as one
// long pass, which is a second decode path the task explicitly rules out.
// Raw m4v does not have that problem: ffmpeg derives real, seekable
// per-keyframe presentation timestamps from the bitstream's own timing, so
// KeyframeAt and frames.Extractor.Frame work on it exactly as they do on any
// other file - only Inspect's duration is missing.
func indexlessTorrent(t *testing.T, tools ffmpeg.Tools, seconds, gop int) (torrentPath, seeder string) {
	t.Helper()

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration="+strconv.Itoa(seconds),
		"-c:v", "mpeg4", "-g", strconv.Itoa(gop), "-pix_fmt", "yuv420p",
		"-f", "m4v",
		filepath.Join(dir, "movie.m4v"),
	); err != nil {
		t.Fatalf("render indexless clip: %v", err)
	}

	fixture := torrenttest.BuildDir(t, dir, 256<<10)
	return fixture.TorrentPath, fixture.StartSeeder(t)
}

// TestSequentialFallbackGatesOnTheFlag is the acceptance criterion for
// TOR-8: the same indexless file must fail with a stable, typed error by
// default, and produce frames once -sequential opts into spending traffic on
// a guess.
func TestSequentialFallbackGatesOnTheFlag(t *testing.T) {
	tools := locateTools(t)
	torrentPath, seeder := indexlessTorrent(t, tools, 10, 25)

	run := func(t *testing.T, sequential bool) []Event {
		t.Helper()

		cfg := runConfig(t, torrentPath, seeder)
		cfg.Swarm.Peers = []string{seeder}
		cfg.Plan = frames.Plan{Count: 3, Start: 0.05, End: 0.95}
		cfg.Sequential = sequential

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		events, err := NewEngine(tools).Run(ctx, cfg)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return collect(t, events)
	}

	t.Run("without the flag, a stable typed error", func(t *testing.T) {
		var failed *Failed
		for _, ev := range run(t, false) {
			switch e := ev.(type) {
			case Failed:
				failed = &e
			case FrameReady:
				t.Errorf("produced frame %+v without -sequential", e)
			}
		}
		if failed == nil {
			t.Fatal("no Failed event, though the file has no usable index")
		}
		if failed.Code != CodeUnprobeable {
			t.Errorf("Code = %q, want %q", failed.Code, CodeUnprobeable)
		}
		t.Logf("failed as expected: %s: %v", failed.Code, failed.Err)
	})

	t.Run("with the flag, a non-empty frame set", func(t *testing.T) {
		var (
			ready []FrameReady
			done  *Done
		)
		for _, ev := range run(t, true) {
			switch e := ev.(type) {
			case FrameReady:
				ready = append(ready, e)
			case Failed:
				t.Errorf("unexpected failure: %s: %v", e.Code, e.Err)
			case Done:
				done = &e
			}
		}
		if len(ready) == 0 {
			t.Fatal("no frames produced with -sequential, though the fixture has keyframes to read")
		}
		if done == nil {
			t.Fatal("no Done event")
		}
		for _, f := range ready {
			info, err := os.Stat(f.Path)
			if err != nil {
				t.Errorf("frame %d is not on disk: %v", f.Index, err)
				continue
			}
			if info.Size() == 0 {
				t.Errorf("frame %d is empty", f.Index)
			}
			t.Logf("frame %d requested %s, taken at %s", f.Index, f.Requested, f.Actual)
		}

		// The frames are real, but the file's own timeline is nonexistent -
		// the points therefore have to cluster near the start rather than
		// spread out, which is the whole reason a reader needs the manifest
		// to say so.
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(ready[0].Path)), manifest.Name))
		if err != nil {
			t.Fatalf("no manifest next to the frames: %v", err)
		}
		var m manifest.Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("manifest is not readable: %v", err)
		}
		if !m.Cost.Sequential {
			t.Error("manifest does not mark the run as sequential")
		}
		if len(m.Frames) == 0 {
			t.Error("manifest lists no frames")
		}
	})
}
