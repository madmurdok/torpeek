package core

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// roofTorrent renders one clip big enough for a ceiling on ARRIVALS to mean
// something, and hands back a torrent and a seeder for it.
//
// It exists instead of multiFileTorrent, whose clips are two to four
// megabytes, because at that size a run receives the WHOLE torrent before it
// reaches its first capture point - measured, 10,591,117 bytes of a
// 10,591,117 byte torrent, three runs out of three. A traffic ceiling is
// polled between capture points on purpose (BudgetTracker.Context says why:
// cancelling mid-write would lose the frame it was about to keep), so against
// a fixture that arrives in one gulp any ceiling can only stop the WORK, never
// the traffic, and a test built on one would be measuring nothing.
//
// The recipe is chosen for bytes per second of rendering, not for realism:
// testsrc2 at 720p defeats x264 far better than testsrc does, so 30 seconds of
// it at -preset ultrafast is 58 MB in about 1.6 seconds of CPU. Of that, an
// unroofed 8-frame min-traffic run receives 25-28 MB, which leaves the roof
// somewhere real to sit.
func roofTorrent(t *testing.T, tools ffmpeg.Tools) (torrentPath, seeder string, size int64) {
	t.Helper()

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	path := filepath.Join(dir, "clip.mkv")
	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=25:duration="+strconv.Itoa(roofClipSeconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-pix_fmt", "yuv420p",
		"-b:v", "20000k", path,
	); err != nil {
		t.Fatalf("render the clip: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the clip: %v", err)
	}

	fixture := torrenttest.BuildDir(t, dir, 1<<20)
	return fixture.TorrentPath, fixture.StartSeeder(t), info.Size()
}

const roofClipSeconds = 30

// TestConcurrentRunsCannotTogetherExceedTheRoof is the acceptance test for
// TOR-131: three runs fetch at once out of one client, and the client-wide
// roof - not any of their own ceilings - is what stops them.
//
// Four things have to hold together, and each is checked because each of them
// is a way this test could be true for the wrong reason:
//
//  1. the runs overlapped. Three runs taking turns would make the roof a
//     sequential quota and the word "concurrent" a decoration, so the peak
//     number of torrents the pool held at once is watched rather than assumed;
//  2. they were stopped, and stopped for the roof - StopRoof, never
//     StopBudget;
//  3. THE PER-RUN LIMITS ALONE WOULD NOT HAVE STOPPED THEM. Each run's own
//     ceiling is set to nine times the entire content of the torrent it is
//     fetching, so no run here can reach its own limit however greedy it is:
//     there are not that many bytes to be had from one seeder. This is the
//     half that stops the test being a tautology, and it rests on the fixture
//     size rather than on how the runs happened to go;
//  4. no run reached the roof on its own either. Every run is checked to have
//     received less than the whole roof when it stopped, so what crossed it
//     was the three of them adding up - which is the multiplication a per-run
//     ceiling cannot see, and the entire reason this ticket exists.
//
// What is NOT claimed is that the total lands exactly on the roof. A traffic
// ceiling is polled between capture points, so whatever a run already had in
// flight still arrives; the bound below is deliberately generous about that
// and still far under what these three runs receive unroofed (82 MB, measured
// - about two and a half times the roof).
func TestConcurrentRunsCannotTogetherExceedTheRoof(t *testing.T) {
	tools := locateTools(t)

	const runs = 3

	type fixture struct {
		path, seeder string
		size         int64
	}
	fixtures := make([]fixture, runs)
	for i := range fixtures {
		path, seeder, size := roofTorrent(t, tools)
		fixtures[i] = fixture{path, seeder, size}
	}
	torrentSize := fixtures[0].size

	// Both ceilings are derived from the fixture rather than tuned until this
	// went green:
	//
	//   - runCeiling is nine times one torrent's whole content, so it is
	//     unreachable by construction (see point 3 above);
	//   - roofBytes is a little over one unroofed run's measured 25-28 MB, so
	//     one run cannot cross it while three comfortably do. Point 4 checks
	//     the first half of that against what actually happened rather than
	//     trusting the measurement.
	roofBytes := int64(32 << 20)
	runCeiling := 9 * torrentSize

	if roofBytes >= runs*torrentSize {
		t.Fatalf("the roof (%d) is at least everything the %d torrents hold (%d each); "+
			"nothing could cross it and this test would pass by never running",
			roofBytes, runs, torrentSize)
	}

	pool, ports, _ := sharedPool(t)

	cfgs := make([]Config, runs)
	for i, f := range fixtures {
		cfg := runConfig(t, f.path, f.seeder)
		cfg.Swarm.Peers = []string{f.seeder}
		cfg.Swarm.Ports = ports
		cfg.Torrents = pool
		cfg.Profile = swarm.MinTraffic
		cfg.Plan = frames.Plan{Count: 8, Start: 0.05, End: 0.95}
		cfg.Budget = Budget{MaxBytes: runCeiling, MaxTime: 4 * time.Minute}
		cfg.Roof = Roof{MaxBytes: roofBytes}
		cfgs[i] = cfg
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()

	var peak int64
	watching, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-watching:
				return
			case <-tick.C:
				if n := int64(pool.Attached()); n > atomic.LoadInt64(&peak) {
					atomic.StoreInt64(&peak, n)
				}
			}
		}
	}()

	engine := NewEngine(tools)
	results := make([]outcome, runs)
	begin := make(chan struct{})

	var wg sync.WaitGroup
	for i := range cfgs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin
			events, err := engine.Run(ctx, cfgs[i])
			if err != nil {
				results[i].startErr = err
				return
			}
			results[i] = drain(events, nil)
		}(i)
	}
	close(begin)
	wg.Wait()

	close(watching)
	<-watched

	total := pool.Downloaded()

	stoppedByRoof, refusedByRoof, completed := 0, 0, 0
	var largest int64

	for i, r := range results {
		if r.startErr != nil {
			t.Fatalf("run %d did not start: %v", i, r.startErr)
		}

		refused := false
		for _, f := range r.failures {
			if f.File < 0 && f.Code == CodeTrafficRoof {
				// The other half of the same rule: a run reaching the engine
				// after the roof is already full is refused before it opens a
				// connection (engine.run).
				refusedByRoof++
				refused = true
				continue
			}
			t.Errorf("run %d reported a failure that is not the roof: file %d, code %s: %v",
				i, f.File, f.Code, f.Err)
		}
		if refused {
			t.Logf("run %d: refused before attaching, the roof was already full", i)
			continue
		}

		if r.done == nil {
			t.Fatalf("run %d produced no Done event", i)
		}

		spent := r.done.DownloadedByte
		t.Logf("run %d: %s, %d frames, received %d - %.1f%% of its own %d ceiling, "+
			"%.1f%% of the %d roof", i, r.done.Reason, len(r.frames), spent,
			100*float64(spent)/float64(runCeiling), runCeiling,
			100*float64(spent)/float64(roofBytes), roofBytes)

		// (3) Its own ceiling was never in reach.
		if spent >= runCeiling {
			t.Errorf("run %d received %d of its own %d ceiling; the per-run limit was reachable "+
				"after all, so this test cannot tell the roof from the budget", i, spent, runCeiling)
		}
		if r.done.Reason == StopBudget {
			t.Errorf("run %d stopped for %q; no run here can reach its own ceiling, so this is "+
				"the wrong ceiling reporting", i, StopBudget)
		}

		switch r.done.Reason {
		case StopRoof:
			stoppedByRoof++
		case StopCompleted:
			completed++
		}
		if spent > largest {
			largest = spent
		}
	}

	// (2) Something was stopped, and by the roof.
	if stoppedByRoof+refusedByRoof == 0 {
		t.Fatalf("no run was stopped or refused by the roof; the three received %d bytes "+
			"together against a roof of %d", total, roofBytes)
	}
	if total < roofBytes {
		t.Errorf("the client received %d bytes against a roof of %d, so whatever stopped these "+
			"runs, the roof was never reached", total, roofBytes)
	}

	// (1) They really did overlap.
	if got := atomic.LoadInt64(&peak); got < 2 {
		t.Errorf("the pool held at most %d torrent(s) at once; the runs took turns, so nothing "+
			"here is about several of them at a time", got)
	}

	// (4) And none of them could have done it alone. This is the whole ticket
	// in one assertion: each run stayed under the roof by itself, and the roof
	// still stopped them, because a client-wide ceiling is crossed by runs
	// ADDING UP - which is exactly what a per-run ceiling cannot notice.
	if largest >= roofBytes {
		t.Errorf("the greediest run received %d on its own against a roof of %d, so one run "+
			"could have crossed it alone and this says nothing about runs adding up",
			largest, roofBytes)
	}

	// The polled ceiling's honest bound: what was already in flight still
	// arrives. Unroofed these three receive about 82 MB, so this still tells
	// a roofed run from an unroofed one.
	if slack := 2 * roofBytes; total > slack {
		t.Errorf("the client received %d bytes against a roof of %d; a poll between capture "+
			"points cannot un-receive what is in flight, but %d is more overshoot than that "+
			"explains", total, roofBytes, total-roofBytes)
	}

	t.Logf("three runs over one client received %d bytes together against a roof of %d "+
		"(%.2fx); %d stopped at it, %d were refused by it, %d finished first; the greediest "+
		"had %d of its own, %.0f%% of the roof, and its own ceiling was %d",
		total, roofBytes, float64(total)/float64(roofBytes), stoppedByRoof, refusedByRoof,
		completed, largest, 100*float64(largest)/float64(roofBytes), runCeiling)
}
