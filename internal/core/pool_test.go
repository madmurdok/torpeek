package core

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// oneTorrentPort allocates a port set of exactly one port, which is what makes
// "one client for every public torrent" a claim a test can fail: two runs that
// each took a client of their own could not both have this port.
func oneTorrentPort(t *testing.T) (*swarm.PortPool, int) {
	t.Helper()

	port := torrenttest.FreePort(t)
	set, err := swarm.ParsePortSet(strconv.Itoa(port))
	if err != nil {
		t.Fatalf("parse port set: %v", err)
	}
	return swarm.NewPortPool(set), port
}

// sharedPool builds the pool a server would build: one for the whole process,
// over one allocated BitTorrent port, offline.
func sharedPool(t *testing.T) (*swarm.Pool, *swarm.PortPool, int) {
	t.Helper()

	ports, port := oneTorrentPort(t)

	cfg := swarm.DefaultConfig(t.TempDir())
	cfg.DHT = false // offline
	cfg.MetadataTimeout = 20 * time.Second
	cfg.Ports = ports

	pool := swarm.NewPool(cfg)
	t.Cleanup(func() { pool.Close() })
	return pool, ports, port
}

// outcome is what one run left behind.
type outcome struct {
	startErr error
	metadata *MetadataReady
	frames   []string
	done     *Done
	failures []Failed
}

// drain reads one run's whole event stream, signalling once on ready as soon
// as the torrent is open - which is the moment the run is provably attached,
// and the only safe moment for another goroutine to act on that.
func drain(events <-chan Event, ready func()) outcome {
	var out outcome
	for ev := range events {
		switch e := ev.(type) {
		case MetadataReady:
			out.metadata = &e
			if ready != nil {
				ready()
				ready = nil
			}
		case FrameReady:
			out.frames = append(out.frames, e.Path)
		case Failed:
			out.failures = append(out.failures, e)
		case Done:
			out.done = &e
		}
	}
	return out
}

// TestTwoPublicRunsFetchFramesFromOneClient is the acceptance test for
// TOR-128: two public torrents fetching frames at the same time, out of one
// client on one port, each writing its own results.
//
// The overlap is observed rather than assumed - peak counts how many torrents
// the pool held at once, and two runs merely taking turns would peak at one.
func TestTwoPublicRunsFetchFramesFromOneClient(t *testing.T) {
	tools := locateTools(t)

	firstPath, firstSeeder := multiFileTorrent(t, tools, 1, 20, "300k")
	secondPath, secondSeeder := multiFileTorrent(t, tools, 1, 20, "300k")

	pool, ports, port := sharedPool(t)

	cfgs := make([]Config, 2)
	for i, pair := range []struct{ path, seeder string }{
		{firstPath, firstSeeder}, {secondPath, secondSeeder},
	} {
		cfg := runConfig(t, pair.path, pair.seeder)
		cfg.Swarm.Peers = []string{pair.seeder}
		cfg.Plan = frames.Plan{Count: 2, Start: 0.2, End: 0.8}
		// The same one allocated port the pool was built over. Inert while
		// Torrents is honoured - the pool decides the port, not the run - and
		// the thing that refuses a run which went and built a client of its
		// own anyway.
		cfg.Swarm.Ports = ports
		// The one line that stops a run from configuring a client of its own.
		cfg.Torrents = pool
		cfgs[i] = cfg
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	var peak int64
	watching := make(chan struct{})
	watched := make(chan struct{})
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
	results := make([]outcome, 2)
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

	for i, r := range results {
		if r.startErr != nil {
			t.Fatalf("run %d did not start: %v", i, r.startErr)
		}
		for _, f := range r.failures {
			t.Errorf("run %d reported a failure: file %d, code %s: %v", i, f.File, f.Code, f.Err)
		}
		if r.done == nil {
			t.Fatalf("run %d produced no Done event", i)
		}
		if r.done.Reason != StopCompleted {
			t.Errorf("run %d stopped for %q, want %q", i, r.done.Reason, StopCompleted)
		}
		if len(r.frames) != cfgs[i].Plan.Count {
			t.Errorf("run %d produced %d frames, want %d", i, len(r.frames), cfgs[i].Plan.Count)
		}
		// Each writes its own results, into its own output root.
		for _, path := range r.frames {
			if !strings.HasPrefix(path, cfgs[i].OutputRoot+string(filepath.Separator)) {
				t.Errorf("run %d wrote %s, which is outside its own output root %s",
					i, path, cfgs[i].OutputRoot)
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("run %d: %s is not on disk: %v", i, path, err)
			}
		}
	}

	if results[0].metadata != nil && results[1].metadata != nil &&
		results[0].metadata.InfoHash == results[1].metadata.InfoHash {
		t.Fatal("both runs opened the same torrent, so nothing here is about two of them")
	}
	if got := atomic.LoadInt64(&peak); got != 2 {
		t.Errorf("the pool held at most %d torrents at once, want 2 - the runs did not overlap", got)
	}
	if got := pool.ListenPort(); got != port {
		t.Errorf("the shared client is on port %d, want the one allocated port %d", got, port)
	}
	if free := ports.Free(); free != 0 {
		t.Errorf("%d of 1 allocated port is free, want 0 - the client outlives the runs", free)
	}
	if !pool.Up() {
		t.Error("the shared client went down when the runs ended")
	}
	if got := pool.Attached(); got != 0 {
		t.Errorf("%d torrents are still attached after both runs ended, want 0", got)
	}

	t.Logf("two runs, %d + %d frames, one client on port %d out of a set of %d",
		len(results[0].frames), len(results[1].frames), port, ports.Size())
}

// TestCancellingOneRunLeavesTheOtherAndTheClientUp: a run's cancellation
// reaches its own torrent and nothing else. Before the pool, cancelling took
// down the client - which was fine only because there was never a second run
// in it.
func TestCancellingOneRunLeavesTheOtherAndTheClientUp(t *testing.T) {
	tools := locateTools(t)

	doomedPath, doomedSeeder := multiFileTorrent(t, tools, 1, 20, "300k")
	survivorPath, survivorSeeder := multiFileTorrent(t, tools, 1, 20, "300k")

	pool, ports, port := sharedPool(t)

	doomedCfg := runConfig(t, doomedPath, doomedSeeder)
	doomedCfg.Swarm.Peers = []string{doomedSeeder}
	doomedCfg.Plan = frames.Plan{Count: 8, Start: 0.05, End: 0.95}
	doomedCfg.Swarm.Ports = ports
	doomedCfg.Torrents = pool

	survivorCfg := runConfig(t, survivorPath, survivorSeeder)
	survivorCfg.Swarm.Peers = []string{survivorSeeder}
	survivorCfg.Plan = frames.Plan{Count: 2, Start: 0.2, End: 0.8}
	survivorCfg.Swarm.Ports = ports
	survivorCfg.Torrents = pool

	outer, cancelOuter := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancelOuter()
	doomedCtx, cancelDoomed := context.WithCancel(outer)
	defer cancelDoomed()

	engine := NewEngine(tools)

	// Nothing is cancelled until both runs are provably attached, so the
	// cancellation cannot race an attach that has not happened yet. Each
	// run's signal fires at most once, and fires either way, so a run that
	// dies before it ever opens its torrent cannot hang the test.
	var open sync.WaitGroup
	open.Add(2)
	signal := func() func() {
		var once sync.Once
		return func() { once.Do(open.Done) }
	}

	var doomed, survivor outcome
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		opened := signal()
		defer opened()
		events, err := engine.Run(doomedCtx, doomedCfg)
		if err != nil {
			doomed.startErr = err
			return
		}
		doomed = drain(events, opened)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		opened := signal()
		defer opened()
		events, err := engine.Run(outer, survivorCfg)
		if err != nil {
			survivor.startErr = err
			return
		}
		survivor = drain(events, opened)
	}()

	open.Wait()
	if got := pool.Attached(); got != 2 {
		t.Errorf("the pool holds %d torrents once both runs are open, want 2", got)
	}
	cancelDoomed()

	wg.Wait()

	if doomed.startErr != nil {
		t.Fatalf("the cancelled run did not start: %v", doomed.startErr)
	}
	if survivor.startErr != nil {
		t.Fatalf("the surviving run did not start: %v", survivor.startErr)
	}

	if doomed.done == nil {
		t.Fatal("the cancelled run produced no Done event")
	}
	if doomed.done.Reason != StopCancelled {
		t.Errorf("the cancelled run stopped for %q, want %q", doomed.done.Reason, StopCancelled)
	}

	for _, f := range survivor.failures {
		t.Errorf("the surviving run reported a failure: file %d, code %s: %v", f.File, f.Code, f.Err)
	}
	if survivor.done == nil {
		t.Fatal("the surviving run produced no Done event")
	}
	if survivor.done.Reason != StopCompleted {
		t.Errorf("the surviving run stopped for %q, want %q", survivor.done.Reason, StopCompleted)
	}
	if len(survivor.frames) != survivorCfg.Plan.Count {
		t.Errorf("the surviving run produced %d frames, want %d",
			len(survivor.frames), survivorCfg.Plan.Count)
	}

	if !pool.Up() {
		t.Error("cancelling one run took the shared client down")
	}
	if got := pool.ListenPort(); got != port {
		t.Errorf("the shared client is on port %d after a cancellation, want %d", got, port)
	}
	if got := pool.Attached(); got != 0 {
		t.Errorf("%d torrents are still attached, want 0", got)
	}
}

// TestAFailedRunLeavesThePoolUpForTheNextOne: a run's failure path used to
// call Session.Close, which is now the pool's client. It must not.
func TestAFailedRunLeavesThePoolUpForTheNextOne(t *testing.T) {
	tools := locateTools(t)

	// A torrent holding nothing worth taking frames from: the run attaches,
	// finds no video and fails - after the shared client is already up.
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "notes.nfo"), []byte("nothing to see"), 0o600); err != nil {
		t.Fatalf("write nfo: %v", err)
	}
	broken := torrenttest.BuildDir(t, empty, 64<<10)

	goodPath, goodSeeder := multiFileTorrent(t, tools, 1, 20, "300k")

	pool, ports, port := sharedPool(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	engine := NewEngine(tools)

	brokenCfg := runConfig(t, broken.TorrentPath, "")
	brokenCfg.Swarm.Ports = ports
	brokenCfg.Torrents = pool

	events, err := engine.Run(ctx, brokenCfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	failed := drain(events, nil)

	if len(failed.failures) == 0 {
		t.Fatal("the run over a torrent with no video did not fail, so nothing here is about a failure")
	}
	if code := failed.failures[0].Code; code != CodeNoVideo {
		t.Fatalf("the run failed with %q, want %q", code, CodeNoVideo)
	}
	if !pool.Up() {
		t.Fatal("a failed run took the shared client down")
	}
	if got := pool.ListenPort(); got != port {
		t.Errorf("the shared client is on port %d after a failed run, want %d", got, port)
	}
	if got := pool.Attached(); got != 0 {
		t.Errorf("%d torrents are still attached after the failed run, want 0", got)
	}

	// The next run reaches the very same client, on the very same port.
	goodCfg := runConfig(t, goodPath, goodSeeder)
	goodCfg.Swarm.Peers = []string{goodSeeder}
	goodCfg.Plan = frames.Plan{Count: 2, Start: 0.2, End: 0.8}
	goodCfg.Swarm.Ports = ports
	goodCfg.Torrents = pool

	events, err = engine.Run(ctx, goodCfg)
	if err != nil {
		t.Fatalf("Run after a failure: %v", err)
	}
	good := drain(events, nil)

	for _, f := range good.failures {
		t.Errorf("the run after the failure reported: file %d, code %s: %v", f.File, f.Code, f.Err)
	}
	if good.done == nil || good.done.Reason != StopCompleted {
		t.Fatalf("the run after the failure did not complete: %+v", good.done)
	}
	if len(good.frames) != goodCfg.Plan.Count {
		t.Errorf("the run after the failure produced %d frames, want %d",
			len(good.frames), goodCfg.Plan.Count)
	}
	if got := pool.ListenPort(); got != port {
		t.Errorf("the shared client moved to port %d, want %d", got, port)
	}
	if free := ports.Free(); free != 0 {
		t.Errorf("%d of 1 allocated port is free, want 0", free)
	}
}
