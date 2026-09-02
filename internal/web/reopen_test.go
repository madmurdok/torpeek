package web

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
)

// buildCachedRun writes a run.json and one file's manifest.json straight to
// disk with output.Writer - the same writer a live run uses - so a reopen
// test has a real, cache.Usable run to address without a torrent or ffmpeg:
// TOR-55's whole point is that neither is touched to replay one.
func buildCachedRun(t *testing.T, root, infoHash, params string, run cache.Run, m manifest.Manifest) output.Layout {
	t.Helper()

	layout := output.Layout{Root: root, InfoHash: infoHash, Params: params}
	writer, err := output.NewWriter(layout)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	for i, f := range m.Frames {
		path, err := writer.WriteFrame(m.File.Index, m.File.Path, f.Index, []byte("jpeg-bytes-"+m.File.Path), "jpg")
		if err != nil {
			t.Fatalf("write frame %d: %v", f.Index, err)
		}
		m.Frames[i].Path = path
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if _, err := writer.WriteFile(m.File.Index, m.File.Path, manifest.Name, data); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := cache.SaveRun(layout.RunDir(), run); err != nil {
		t.Fatalf("save run.json: %v", err)
	}
	return layout
}

func oneFrameManifest(index int, path string) manifest.Manifest {
	ms := int64(1000)
	return manifest.Manifest{
		Version: manifest.Version,
		File:    manifest.File{Index: index, Path: path, Bytes: 1 << 20, Container: "matroska"},
		Video:   manifest.Video{Codec: "h264", Width: 640, Height: 360},
		Frames: []manifest.Frame{
			{Index: 0, RequestedMS: ms, ActualMS: &ms, Path: "placeholder", Width: 640, Height: 360},
		},
	}
}

// realReplayer wires the real core.Engine.Replay - not a fake - so a test
// using it proves the actual reopening path, cache logic included, rather
// than only the web package's own plumbing around it.
func realReplayer(root string) Replayer {
	engine := core.NewEngine(ffmpeg.Tools{})
	return func(infoHash, params string) <-chan core.Event {
		return engine.Replay(root, infoHash, params)
	}
}

func reopenRun(t *testing.T, base, infoHash, params string) (id, state string, status int) {
	t.Helper()

	resp := post(t, base, "/runs/reopen", `{"infohash":"`+infoHash+`","params":"`+params+`"}`)
	body := decodeBody(t, resp)
	id, _ = body["id"].(string)
	state, _ = body["state"].(string)
	return id, state, resp.StatusCode
}

// TestReopenBypassesTheQueueWhileAnotherRunIsInProgress is TOR-55's
// acceptance criterion, word for word: a run is in flight (holding the
// single slot, fakeRun's runner blocks until told to finish), and a
// different, finished run is reopened while it is still going. A queued
// reopen would still be sitting in "queued" when this test's deadline hits,
// since the live run never finishes on its own; ReopenRun instead returns
// the run's real, final outcome synchronously, which is only possible if it
// never entered the queue at all.
func TestReopenBypassesTheQueueWhileAnotherRunIsInProgress(t *testing.T) {
	const (
		infoHash = "3333333333333333333333333333333333dead"
		params   = "deadbeef"
	)
	root := t.TempDir()
	buildCachedRun(t, root, infoHash, params, cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "Reopened While Busy",
		Videos: []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}}, Complete: []int{0},
	}, oneFrameManifest(0, "movie.mkv"))

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithReplayer(t, cfg, fake.runner, realReplayer(root))

	live := startRun(t, ts.URL, "magnet:?xt=urn:btih:busy")
	if live.state != "running" {
		t.Fatalf("live run is %q, want running - the slot was free", live.state)
	}
	t.Cleanup(func() { fake.finish(t, "magnet:?xt=urn:btih:busy") })

	id, state, status := reopenRun(t, ts.URL, infoHash, params)
	if status != http.StatusAccepted {
		t.Fatalf("POST /runs/reopen: status %d, want 202", status)
	}
	if id == "" {
		t.Fatal("reopen answered no id")
	}
	// This is the assertion that matters: the response already carries the
	// run's finished outcome. A queue-bound reopen could not have reached
	// this state without the live run finishing first, which it never does
	// in this test.
	if state != "done" {
		t.Fatalf("reopened run state = %q, want done - it must not have waited for the live run's slot", state)
	}

	rows := listRuns(t, ts.URL)
	var sawLive, sawReopened bool
	for _, row := range rows {
		switch row.ID {
		case live.id:
			sawLive = true
			if row.State != "running" {
				t.Errorf("the live run's own state changed to %q while the reopen ran", row.State)
			}
		case id:
			sawReopened = true
			if row.State != "done" {
				t.Errorf("reopened row state = %q, want done", row.State)
			}
		}
	}
	if !sawLive || !sawReopened {
		t.Fatalf("GET /runs did not list both runs: %+v", rows)
	}
}

// TestReopenedRunsFrameURLsResolveWithNoPriorHistory is the fileSet claim:
// the id -> path registry is populated only from events this process has
// seen (internal/web/files.go's doc comment), so this server never having
// held the run before - the same as after a restart - must not stop its
// frame URL from resolving. Nothing here primes the server with the run
// first; infoHash and params come straight from what was written to disk,
// exactly as a disk-only GET /runs row would hand them to a panel.
func TestReopenedRunsFrameURLsResolveWithNoPriorHistory(t *testing.T) {
	const (
		infoHash = "4444444444444444444444444444444444dead"
		params   = "deadbeef"
	)
	root := t.TempDir()
	buildCachedRun(t, root, infoHash, params, cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "Reopened After Restart",
		Videos: []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}}, Complete: []int{0},
	}, oneFrameManifest(0, "movie.mkv"))

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithReplayer(t, cfg, (&fakeRun{}).runner, realReplayer(root))

	conn := dial(t, ts.URL)
	next(t, conn) // idle run_state

	id, state, status := reopenRun(t, ts.URL, infoHash, params)
	if status != http.StatusAccepted || state != "done" {
		t.Fatalf("reopen: status %d state %q, want 202 done", status, state)
	}

	var frameURL string
	for i := 0; i < 20 && frameURL == ""; i++ {
		event := next(t, conn)
		if event["run"] != id {
			continue
		}
		if event["type"] == "frame_ready" {
			frameURL, _ = event["url"].(string)
		}
	}
	if frameURL == "" {
		t.Fatal("no frame_ready with a url arrived for the reopened run")
	}

	resp, err := http.Get(ts.URL + "/" + frameURL)
	if err != nil {
		t.Fatalf("GET %s: %v", frameURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200 - the id this process just minted must resolve", frameURL, resp.StatusCode)
	}
}

// TestReopeningARunWithDeletedFramesFails is the cache.Usable contract at
// the API boundary: a run whose frames were removed from under it must
// surface as a failed run, never as a reopen that quietly pretends the
// frames are still there.
func TestReopeningARunWithDeletedFramesFails(t *testing.T) {
	const (
		infoHash = "5555555555555555555555555555555555dead"
		params   = "deadbeef"
	)
	root := t.TempDir()
	layout := buildCachedRun(t, root, infoHash, params, cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "Deleted Frames Run",
		Videos: []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}}, Complete: []int{0},
	}, oneFrameManifest(0, "movie.mkv"))

	loaded, ok := cache.LoadManifest(layout.FileDir(0, "movie.mkv"))
	if !ok || len(loaded.Frames) == 0 {
		t.Fatalf("could not read back the manifest just written: ok=%v", ok)
	}
	if err := os.Remove(loaded.Frames[0].Path); err != nil {
		t.Fatalf("delete a frame: %v", err)
	}

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithReplayer(t, cfg, (&fakeRun{}).runner, realReplayer(root))

	id, state, status := reopenRun(t, ts.URL, infoHash, params)
	if status != http.StatusAccepted {
		t.Fatalf("POST /runs/reopen: status %d, want 202 - a cache miss is reported on the run's own stream, not as a request error", status)
	}
	if id == "" {
		t.Fatal("reopen answered no id")
	}
	if state != "failed" {
		t.Fatalf("reopened run state = %q, want failed - its frames were deleted", state)
	}
}

// TestReopenRequiresInfohashAndParams: an empty address is a request error,
// the same way a blank source is for POST /runs.
func TestReopenRequiresInfohashAndParams(t *testing.T) {
	_, ts := newTestServerWithReplayer(t, DefaultConfig(), (&fakeRun{}).runner, realReplayer(t.TempDir()))

	resp := post(t, ts.URL, "/runs/reopen", `{"infohash":"","params":""}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /runs/reopen with no address: status %d, want 400", resp.StatusCode)
	}
}

// TestReopenWithoutAReplayerIsUnavailable: a server built with no Replayer
// (newTestServerWithConfig, most tests) must refuse rather than panic.
func TestReopenWithoutAReplayerIsUnavailable(t *testing.T) {
	_, ts := newTestServer(t, (&fakeRun{}).runner)

	resp := post(t, ts.URL, "/runs/reopen", `{"infohash":"a","params":"b"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("POST /runs/reopen with no replayer: status %d, want 503", resp.StatusCode)
	}
}
