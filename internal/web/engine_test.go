package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/bridge"
	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// TestServesAFrameFromARealRun is the acceptance criterion end to end, minus
// the browser: a real engine takes a frame out of a torrent served on
// loopback, the frame reaches a connected client as an event, and the URL in
// that event serves the bytes that are on disk.
func TestServesAFrameFromARealRun(t *testing.T) {
	tools, err := ffmpeg.LocateIn()
	if err != nil {
		t.Skipf("no ffmpeg available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	dir := t.TempDir()
	clip := filepath.Join(dir, "episode-1.mkv")
	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration="+strconv.Itoa(20),
		"-c:v", "libx264", "-g", "50", "-pix_fmt", "yuv420p", "-b:v", "400k",
		clip,
	); err != nil {
		t.Fatalf("render clip: %v", err)
	}

	fixture := torrenttest.BuildDir(t, dir, 256<<10)
	seeder := fixture.StartSeeder(t)

	cfg := core.DefaultConfig(fixture.TorrentPath, t.TempDir(), t.TempDir())
	cfg.Swarm.DHT = false // offline
	cfg.Swarm.MetadataTimeout = 10 * time.Second
	cfg.Swarm.Peers = []string{seeder}
	cfg.Profile = swarm.MinTraffic
	cfg.Plan = frames.Plan{Count: 2, Start: 0.2, End: 0.8}
	cfg.Budget = core.Budget{MaxBytes: 64 << 20, MaxTime: 4 * time.Minute, WarnAt: 0.8}
	cfg.Parallelism = 1
	cfg.Bridge = bridge.DefaultConfig()

	engine := core.NewEngine(tools)
	runner := func(ctx context.Context, req RunRequest) (<-chan core.Event, error) {
		run := cfg
		run.Source = req.Source
		return engine.Run(ctx, run)
	}

	srv := newServer(ctx, DefaultConfig(), runner, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
		// Close cancels the run in the slot; it does not wait for it. This
		// test deliberately returns after the FIRST frame, so the run is still
		// going - and still writing frames into the output directory that
		// t.TempDir is about to remove. RemoveAll racing a frame being written
		// fails with "directory not empty": measured on release-1.2.0, two runs
		// in eight, before any of TOR-128's changes. Waiting for the slot to
		// empty is what makes the cleanup that follows this one safe.
		waitForTheSlotToEmpty(t, srv)
	})

	conn := dial(t, ts.URL)
	next(t, conn) // idle run_state

	if resp := post(t, ts.URL, "/runs", `{"source":`+strconv.Quote(fixture.TorrentPath)+`}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs: status %d, want 202", resp.StatusCode)
	}

	// Read until the first frame lands, so the test does not depend on which
	// heartbeats happen to arrive first.
	var frame map[string]any
	for i := 0; i < 200 && frame == nil; i++ {
		event := next(t, conn)
		switch event["type"] {
		case "frame_ready":
			frame = event
		case "failed", "done":
			t.Fatalf("the run ended before a frame arrived: %v", event)
		}
	}
	if frame == nil {
		t.Fatal("no frame_ready arrived")
	}

	url, _ := frame["url"].(string)
	path, _ := frame["path"].(string)
	if url == "" || path == "" {
		t.Fatalf("frame_ready is missing a url or a path: %v", frame)
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the frame the run wrote: %v", err)
	}
	if len(onDisk) < 2 || onDisk[0] != 0xFF || onDisk[1] != 0xD8 {
		t.Fatalf("%s is not a JPEG: % x", path, onDisk[:min(4, len(onDisk))])
	}

	resp, err := http.Get(ts.URL + "/" + url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	served := readAll(t, resp)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", url, resp.StatusCode)
	}
	if served != string(onDisk) {
		t.Errorf("the server returned %d bytes for %s, but the frame on disk is %d",
			len(served), url, len(onDisk))
	}
	if got := resp.Header.Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("frame served as %q, want image/jpeg", got)
	}
}

// waitForTheSlotToEmpty blocks until no run is in the server's slot, which is
// the observable end of the run's own goroutine: pump releases the slot as the
// last thing it does. Reported rather than waited out for ever, because a run
// that never ends is a bug worth naming and not a reason to hang the suite.
func waitForTheSlotToEmpty(t *testing.T, srv *Server) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for {
		srv.mu.Lock()
		running := srv.running
		srv.mu.Unlock()

		if running == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Error("a run was still in the slot 30s after the server was closed")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
