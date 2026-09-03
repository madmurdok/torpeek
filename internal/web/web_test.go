package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/madmurdok/torpeek/internal/core"
)

// fakeRun stands in for a run: the server is a client of an event stream and
// nothing about it should need a torrent to test.
type fakeRun struct {
	events chan core.Event
	ctx    context.Context
	err    error
	starts int
}

func (f *fakeRun) runner(ctx context.Context, req RunRequest) (<-chan core.Event, error) {
	f.starts++
	if f.err != nil {
		return nil, f.err
	}
	f.ctx = ctx
	f.events = make(chan core.Event, 16)
	return f.events, nil
}

func testServer(t *testing.T, runner Runner) *httptest.Server {
	t.Helper()

	_, ts := newTestServer(t, runner)
	return ts
}

// newTestServer is testServer for a test that also needs the server itself -
// to read the registry, or to close it while the test watches.
func newTestServer(t *testing.T, runner Runner) (*Server, *httptest.Server) {
	t.Helper()
	return newTestServerWithConfig(t, DefaultConfig(), runner)
}

// newTestServerWithConfig is newTestServer for a test that needs a
// non-default Config - OutputRoot, most often, for the GET /runs tests that
// need a server pointed at a specific directory. Its replayer is nil: a test
// that exercises reopening a run (TOR-55) wants newTestServerWithReplayer
// instead.
func newTestServerWithConfig(t *testing.T, cfg Config, runner Runner) (*Server, *httptest.Server) {
	t.Helper()
	return newTestServerWithReplayer(t, cfg, runner, nil)
}

// newTestServerWithReplayer is newTestServerWithConfig plus a Replayer, for
// the tests that reopen a run from disk (TOR-55). Its deleter is nil, which
// is what TestDeleteFrameWithoutADeleterIsUnavailable relies on.
func newTestServerWithReplayer(t *testing.T, cfg Config, runner Runner, replayer Replayer) (*Server, *httptest.Server) {
	t.Helper()
	return newTestServerWith(t, cfg, runner, replayer, nil)
}

// newTestServerWith is the full form, for the tests that delete a frame
// (TOR-70) and so need all three injected closures.
func newTestServerWith(t *testing.T, cfg Config, runner Runner, replayer Replayer, deleter Deleter) (*Server, *httptest.Server) {
	t.Helper()

	srv := newServer(context.Background(), cfg, runner, replayer, deleter)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return srv, ts
}

// dial opens the event socket against a server whose UI root is at base -
// which may itself carry a query string (an access token, as srv.URL() now
// can), preserved on the socket URL exactly the way app.js's own url()
// helper preserves it for a real browser.
func dial(t *testing.T, base string) *websocket.Conn {
	t.Helper()

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse base URL %q: %v", base, err)
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/events"

	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial %s: %v (status %d)", u.String(), err, status)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// dialExpectingRejection is dial without the t.Fatalf on failure: it is used
// by the tests that want to see the socket refused.
func dialExpectingRejection(t *testing.T, base string) (*http.Response, error) {
	t.Helper()

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse base URL %q: %v", base, err)
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/events"

	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err == nil {
		conn.Close()
	}
	return resp, err
}

// next reads one event off the socket, failing rather than hanging.
func next(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read event: %v", err)
	}

	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return event
}

func post(t *testing.T, base, path string, body string) *http.Response {
	t.Helper()

	resp, err := http.Post(strings.TrimSuffix(base, "/")+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func get(t *testing.T, base, path string) *http.Response {
	t.Helper()

	resp, err := http.Get(strings.TrimSuffix(base, "/") + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// uploadTorrent posts a multipart form the way a browser's FormData would
// for a dropped .torrent, so the upload tests exercise the same encoding the
// frontend sends.
func uploadTorrent(t *testing.T, base string, content []byte, mode string) *http.Response {
	t.Helper()

	return uploadTorrentWithCount(t, base, content, mode, "")
}

// uploadTorrentWithCount is uploadTorrent for the tests that also send the
// intake line's frame count (TOR-68), which the drop path reads off the same
// form. An empty count writes no field at all, which is what a page with an
// untouched field sends.
func uploadTorrentWithCount(t *testing.T, base string, content []byte, mode, count string) *http.Response {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("torrent", "release.torrent")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if mode != "" {
		if err := mw.WriteField("mode", mode); err != nil {
			t.Fatalf("write mode field: %v", err)
		}
	}
	if count != "" {
		if err := mw.WriteField("count", count); err != nil {
			t.Fatalf("write count field: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	resp, err := http.Post(strings.TrimSuffix(base, "/")+"/runs/upload", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatalf("POST /runs/upload: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestUploadedTorrentStartsARun is the drag-and-drop path converging on the
// same run machinery a pasted magnet uses: StartRun is called with a
// filesystem path to the staged upload, and the run that follows looks like
// any other to a connected client.
func TestUploadedTorrentStartsARun(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	conn := dial(t, ts.URL)
	next(t, conn) // idle run_state

	resp := uploadTorrent(t, ts.URL, []byte("d8:announce...e"), "min-traffic")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/upload: status %d, want 202", resp.StatusCode)
	}
	if fake.starts != 1 {
		t.Fatalf("the runner was called %d times, want 1", fake.starts)
	}

	// Accepted first, started second: every run is queued before it takes the
	// slot, even when the slot was free all along.
	if queued := next(t, conn); queued["type"] != "run_state" || queued["state"] != "queued" {
		t.Fatalf("after an upload, got %v, want a queued run_state", queued)
	}
	active := next(t, conn)
	if active["type"] != "run_state" || active["active"] != true || active["state"] != "running" {
		t.Fatalf("after an upload, got %v, want a running run_state", active)
	}
	// The source shown to a person is a label, not the server's temp path -
	// nobody typed that path and it means nothing to them.
	if source, _ := active["source"].(string); strings.Contains(source, os.TempDir()) {
		t.Errorf("run_state leaked the server temp path: %q", source)
	}
}

// TestUploadStagesTheFileWherePassedToTheRunner proves the path the runner
// receives is real and readable at the moment the run starts - the contract
// swarm.ParseSource depends on - and that nothing on the request looks like
// the raw multipart encoding leaking through.
func TestUploadStagesTheFileWherePassedToTheRunner(t *testing.T) {
	var gotSource string
	runner := func(ctx context.Context, req RunRequest) (<-chan core.Event, error) {
		gotSource = req.Source
		data, err := os.ReadFile(req.Source)
		if err != nil {
			t.Errorf("runner could not read the staged upload: %v", err)
		} else if string(data) != "torrent-bytes" {
			t.Errorf("staged file holds %q, want the uploaded bytes", data)
		}
		return nil, errors.New("stop here")
	}
	ts := testServer(t, runner)

	uploadTorrent(t, ts.URL, []byte("torrent-bytes"), "")

	if gotSource == "" {
		t.Fatal("the runner was never called")
	}
	if filepath.Ext(gotSource) != ".torrent" {
		t.Errorf("staged path %q does not look like a .torrent file", gotSource)
	}
}

// TestUploadedFileOutlivesStartRun guards a real bug: swarm.Open re-reads a
// file Source from disk (AddTorrentFromFile) from inside the run's own
// goroutine, well after the runner call inside StartRun has already
// returned - so a temp file removed as soon as StartRun returns is gone
// before the run ever gets to read it. The fix keeps the file until the run
// itself ends; this asserts that ordering directly rather than trusting a
// synchronous-looking call chain.
func TestUploadedFileOutlivesStartRun(t *testing.T) {
	events := make(chan core.Event, 1)
	started := make(chan string, 1)
	runner := func(ctx context.Context, req RunRequest) (<-chan core.Event, error) {
		started <- req.Source
		return events, nil
	}
	ts := testServer(t, runner)

	resp := uploadTorrent(t, ts.URL, []byte("torrent-bytes"), "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/upload: status %d, want 202", resp.StatusCode)
	}

	path := <-started
	// The file must still be there after StartRun/startRun has returned to
	// the handler and the handler has responded - this is the exact window
	// AddTorrentFromFile runs in for a real engine.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("staged upload was removed before the run could read it: %v", err)
	}

	close(events)
	waitFor(t, func() bool {
		_, err := os.Stat(path)
		return errors.Is(err, os.ErrNotExist)
	})
}

// TestUploadWithoutAFileIsRejected keeps a malformed drop from silently
// starting a run.
func TestUploadWithoutAFileIsRejected(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("mode", "min-time")
	mw.Close()

	resp, err := http.Post(ts.URL+"/runs/upload", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatalf("POST /runs/upload: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /runs/upload with no file: status %d, want 400", resp.StatusCode)
	}
	if fake.starts != 0 {
		t.Errorf("the runner was called %d times with no file, want 0", fake.starts)
	}
}

// TestUploadWhileARunIsGoingIsQueued is the same rule TestASecondRunWaitsForTheSlot
// checks for the magnet path, on the upload path: a drop onto a busy server is
// accepted and waits, rather than being refused.
func TestUploadWhileARunIsGoingIsQueued(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	if resp := post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc"}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs: status %d, want 202", resp.StatusCode)
	}

	resp := uploadTorrent(t, ts.URL, []byte("bytes"), "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/upload while a run is going: status %d, want 202", resp.StatusCode)
	}
	if got := decodeBody(t, resp)["state"]; got != "queued" {
		t.Errorf("the dropped .torrent was reported as %v, want queued", got)
	}
	if fake.starts != 1 {
		t.Errorf("the runner was called %d times, want 1 - the second run has not started", fake.starts)
	}
}

// TestServesEmbeddedFrontend is the acceptance criterion in miniature: the
// binary has to be able to show a UI with nothing beside it on disk.
func TestServesEmbeddedFrontend(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	for _, tc := range []struct{ path, contains string }{
		{"/", "<title>torpeek</title>"},
		{"/app.js", "WebSocket"},
		{"/app.css", ".grid"},
	} {
		resp, err := http.Get(ts.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		body := readAll(t, resp)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200", tc.path, resp.StatusCode)
		}
		if !strings.Contains(body, tc.contains) {
			t.Errorf("GET %s does not contain %q; got %d bytes", tc.path, tc.contains, len(body))
		}
	}

	// The frontend is what is served, not the package it lives in.
	resp, err := http.Get(ts.URL + "/server.go")
	if err != nil {
		t.Fatalf("GET /server.go: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /server.go: status %d, want 404", resp.StatusCode)
	}
}

// TestStreamsEventsToConnectedClient is the pipe this task exists to build:
// what the core emits reaches a browser, in order, in the vocabulary the CLI
// already writes as NDJSON.
func TestStreamsEventsToConnectedClient(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	conn := dial(t, ts.URL)

	if got := next(t, conn); got["type"] != "run_state" || got["active"] != false {
		t.Fatalf("first message is %v, want an idle run_state", got)
	}

	if resp := post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc","mode":"min-traffic"}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs: status %d, want 202", resp.StatusCode)
	}

	if got := next(t, conn); got["type"] != "run_state" || got["state"] != "queued" {
		t.Fatalf("after starting, got %v, want a queued run_state", got)
	}
	if got := next(t, conn); got["type"] != "run_state" || got["active"] != true {
		t.Fatalf("after starting, got %v, want an active run_state", got)
	}

	fake.events <- core.MetadataReady{Name: "Some Release", InfoHash: "abc", Selected: []int{0}}
	fake.events <- core.FrameSkipped{File: 0, Index: 3, Requested: 90 * time.Second, Code: core.CodeInternal, Reason: "no keyframe"}
	fake.events <- core.Done{Reason: core.StopCompleted, Files: 1, Frames: 19, Elapsed: 42 * time.Second}
	close(fake.events)

	metadata := next(t, conn)
	if metadata["type"] != "metadata_ready" || metadata["name"] != "Some Release" {
		t.Errorf("metadata event is %v", metadata)
	}

	skipped := next(t, conn)
	if skipped["type"] != "frame_skipped" || skipped["index"] != float64(3) || skipped["requested_ms"] != float64(90000) {
		t.Errorf("skipped event is %v", skipped)
	}

	done := next(t, conn)
	if done["type"] != "done" || done["reason"] != "completed" || done["frames"] != float64(19) {
		t.Errorf("done event is %v", done)
	}

	if final := next(t, conn); final["type"] != "run_state" || final["active"] != false {
		t.Errorf("after the run, got %v, want an idle run_state", final)
	}
}

// TestReplaysTheRunToALateClient covers a reload halfway through: a page that
// connects late must see the whole run, not only the rest of it.
func TestReplaysTheRunToALateClient(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc"}`)
	fake.events <- core.MetadataReady{Name: "Earlier", InfoHash: "abc", Selected: []int{0}}
	fake.events <- core.FileStarted{File: 0, Path: "movie.mkv"}

	// Both events are through the server before anyone is listening.
	waitFor(t, func() bool { return len(fake.events) == 0 })

	conn := dial(t, ts.URL)
	// The connection marker, then the run: accepted, started, and what it has
	// said so far.
	for _, want := range []string{"run_state", "run_state", "run_state", "metadata_ready", "file_started"} {
		if got := next(t, conn); got["type"] != want {
			t.Fatalf("replayed %v, want a %s", got, want)
		}
	}

	fake.events <- core.FileDone{File: 0, Path: "movie.mkv", Frames: 20}
	if got := next(t, conn); got["type"] != "file_done" {
		t.Errorf("after the replay, got %v, want file_done", got)
	}
}

// TestHeartbeatsCollapseInTheReplay keeps a ten minute run from replaying
// hundreds of stale progress ticks to a page that just opened.
func TestHeartbeatsCollapseInTheReplay(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc"}`)
	for i := 1; i <= 5; i++ {
		fake.events <- core.Progress{File: 0, FramesDone: i, FramesTotal: 20}
	}
	waitFor(t, func() bool { return len(fake.events) == 0 })

	conn := dial(t, ts.URL)
	for i := 0; i < 3; i++ {
		if got := next(t, conn); got["type"] != "run_state" {
			t.Fatalf("replayed message %d is %v, want run_state", i, got)
		}
	}

	progress := next(t, conn)
	if progress["type"] != "progress" || progress["frames_done"] != float64(5) {
		t.Fatalf("replayed %v, want only the newest heartbeat", progress)
	}

	// Nothing else is waiting: the four earlier ticks were replaced in place.
	fake.events <- core.Done{Reason: core.StopCompleted}
	if got := next(t, conn); got["type"] != "done" {
		t.Errorf("after the heartbeat, got %v, want done", got)
	}
}

// TestServesAFileTheRunAnnounced covers the frame route without a torrent;
// TestServesAFrameFromARealRun covers it with one.
func TestServesAFileTheRunAnnounced(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	frame := filepath.Join(t.TempDir(), "000.jpg")
	if err := os.WriteFile(frame, []byte("not really a jpeg"), 0o600); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	conn := dial(t, ts.URL)
	next(t, conn) // idle run_state
	post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc"}`)
	next(t, conn) // queued run_state
	next(t, conn) // active run_state

	fake.events <- core.FrameReady{File: 0, Index: 0, Path: frame, Width: 640, Height: 360}

	event := next(t, conn)
	url, _ := event["url"].(string)
	if url == "" {
		t.Fatalf("frame_ready carries no url: %v", event)
	}
	if strings.HasPrefix(url, "/") {
		t.Errorf("frame url %q is rooted at the site root, which breaks under a base path", url)
	}
	if event["path"] != frame {
		t.Errorf("frame_ready dropped the path the CLI also reports: %v", event)
	}

	resp, err := http.Get(ts.URL + "/" + url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body := readAll(t, resp)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", url, resp.StatusCode)
	}
	if body != "not really a jpeg" {
		t.Errorf("served %q, want the frame's bytes", body)
	}

	// Only what a run announced is reachable.
	unknown, err := http.Get(ts.URL + "/files/0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("GET unknown file: %v", err)
	}
	unknown.Body.Close()
	if unknown.StatusCode != http.StatusNotFound {
		t.Errorf("an unannounced file returned %d, want 404", unknown.StatusCode)
	}
}

// TestASecondRunWaitsForTheSlot keeps two tabs from competing for the same
// output directory and the same traffic budget - the rule that has always
// held here - while no longer turning the second tab away. One run at a time
// is now kept by the queue rather than by a 409.
func TestASecondRunWaitsForTheSlot(t *testing.T) {
	fake := newFakeRuns()
	ts := testServer(t, fake.runner)

	first := startRun(t, ts.URL, "magnet:?xt=urn:btih:abc")
	if first.state != "running" {
		t.Fatalf("the first run is %q, want running - the slot was free", first.state)
	}

	second := startRun(t, ts.URL, "magnet:?xt=urn:btih:def")
	if second.state != "queued" {
		t.Fatalf("the second run is %q, want queued", second.state)
	}
	if second.id == first.id {
		t.Fatalf("both runs were given the same id %q", first.id)
	}
	if got := fake.count(); got != 1 {
		t.Errorf("the runner was called %d times, want 1 - only one run may hold the slot", got)
	}

	// The queued run starts on its own, when the first one's stream ends -
	// nobody has to ask again.
	fake.finish(t, "magnet:?xt=urn:btih:abc")
	waitFor(t, func() bool { return fake.count() == 2 })
}

// TestCancelStopsTheRun is what the UI's cancel button has to reach: the run's
// context, so frames already written survive (REQUIREMENTS.md section 2.10).
func TestCancelStopsTheRun(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc"}`)

	if resp := post(t, ts.URL, "/runs/cancel", ""); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/cancel: status %d, want 202", resp.StatusCode)
	}

	select {
	case <-fake.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the run did not cancel its context")
	}
}

func TestCancelWithoutARunIsAConflict(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	if resp := post(t, ts.URL, "/runs/cancel", ""); resp.StatusCode != http.StatusConflict {
		t.Errorf("POST /runs/cancel with no run: status %d, want 409", resp.StatusCode)
	}
}

func TestStartRunRejectsABlankSource(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	if resp := post(t, ts.URL, "/runs", `{"source":"   "}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /runs with a blank source: status %d, want 400", resp.StatusCode)
	}
	if fake.starts != 0 {
		t.Errorf("the runner was called %d times for a blank source, want 0", fake.starts)
	}
}

// TestRunnerFailureIsReported: a run that cannot be started is now a run that
// failed, not a request that was refused. It has to be: a queued run reaches
// the runner minutes after the request that asked for it was answered, so the
// failure can only reach a client one way, and that way has to be the same
// for every run.
func TestRunnerFailureIsReported(t *testing.T) {
	fake := &fakeRun{err: errors.New("no such torrent file")}
	ts := testServer(t, fake.runner)

	conn := dial(t, ts.URL)
	next(t, conn) // the connection marker

	resp := post(t, ts.URL, "/runs", `{"source":"/nope.torrent"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs: status %d, want 202", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("POST /runs answered %v, with no run id", body)
	}
	if body["state"] != "failed" {
		t.Errorf("POST /runs answered %v, want a failed state", body)
	}

	if got := next(t, conn); got["state"] != "queued" || got["run"] != id {
		t.Fatalf("first message is %v, want the queued state of run %s", got, id)
	}

	failure := next(t, conn)
	if failure["type"] != "failed" || failure["run"] != id {
		t.Fatalf("got %v, want a failed event for run %s", failure, id)
	}
	if msg, _ := failure["error"].(string); !strings.Contains(msg, "no such torrent file") {
		t.Errorf("the failed event %v does not name the failure", failure)
	}

	final := next(t, conn)
	if final["type"] != "run_state" || final["state"] != "failed" || final["run"] != id {
		t.Errorf("got %v, want run %s to end as failed", final, id)
	}
}

// TestWorksUnderABasePath is the seam TOR-29 will configure. The prefix is not
// yet a setting, but the UI must already be shaped so that adding one is a
// StripPrefix and nothing more: no absolute route, no absolute asset
// reference, no absolute socket URL (REQUIREMENTS.md section 3.3).
func TestWorksUnderABasePath(t *testing.T) {
	fake := &fakeRun{}
	srv := newServer(context.Background(), DefaultConfig(), fake.runner, nil, nil)
	t.Cleanup(func() { srv.Close() })

	mounted := http.NewServeMux()
	mounted.Handle("/torpeek", http.StripPrefix("/torpeek", srv.Handler()))
	mounted.Handle("/torpeek/", http.StripPrefix("/torpeek", srv.Handler()))

	ts := httptest.NewServer(mounted)
	t.Cleanup(ts.Close)

	// The prefix itself must move the browser to the directory form, or every
	// relative reference in the page would resolve one level too high.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(ts.URL + "/torpeek")
	if err != nil {
		t.Fatalf("GET /torpeek: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("GET /torpeek: status %d, want 301", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); location != "/torpeek/" {
		t.Errorf("GET /torpeek redirects to %q, want /torpeek/", location)
	}

	for _, path := range []string{"/torpeek/", "/torpeek/app.js", "/torpeek/app.css"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", path, resp.StatusCode)
		}
	}

	// The event socket lives under the same prefix, reached relatively.
	conn := dial(t, ts.URL+"/torpeek/")
	if got := next(t, conn); got["type"] != "run_state" {
		t.Errorf("under a base path the socket said %v, want run_state", got)
	}

	// And so does everything the page asks of the server.
	if resp := post(t, ts.URL, "/torpeek/runs", `{"source":"magnet:?xt=urn:btih:abc"}`); resp.StatusCode != http.StatusAccepted {
		t.Errorf("POST /torpeek/runs: status %d, want 202", resp.StatusCode)
	}
}

// TestTheFrontendUsesNoAbsolutePaths guards the same seam from the other side:
// a single leading slash in the markup would survive every test above, because
// they all ask for the right URL themselves.
func TestTheFrontendUsesNoAbsolutePaths(t *testing.T) {
	for _, name := range []string{"assets/index.html", "assets/app.js"} {
		data, err := embedded.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, bad := range []string{`href="/`, `src="/`, `"/events"`, `"/runs"`, `"/files/`} {
			if strings.Contains(string(data), bad) {
				t.Errorf("%s contains %q, which would break under a base path", name, bad)
			}
		}
	}
}

// TestConfiguredBasePathIsServedEndToEnd is TestWorksUnderABasePath's
// counterpart at the level a person actually configures it: Config.BasePath
// through a real Start(), not a hand-mounted mux. TestWorksUnderABasePath
// already proves the StripPrefix seam works; this proves -base-path (which
// sets exactly this field) reaches it, including URL() reporting the
// prefixed address a person is told to open.
//
// This is also, deliberately, the exact scenario needsToken exists for: a
// loopback bind with a base path configured, standing in for the seedbox
// behind nginx that TOR-30 targets (REQUIREMENTS.md section 4.1). So this
// test also proves Start auto-generated a token here and enforces it - a
// bind-address-only rule would have left this configuration open, which is
// precisely the case TOR-30's decision writeup calls out as the one a naive
// rule gets wrong.
func TestConfiguredBasePathIsServedEndToEnd(t *testing.T) {
	fake := &fakeRun{}

	port := freeWebPort(t)
	addr := net.JoinHostPort("127.0.0.1", port)

	cfg := DefaultConfig()
	cfg.Addr = addr
	cfg.BasePath = "torpeek" // no leading slash: normalizeBasePath's job

	srv, err := Start(context.Background(), cfg, fake.runner, nil, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	if srv.cfg.Token == "" {
		t.Fatal("a base path was configured but Start did not require a token")
	}

	root := "http://" + addr
	if want := root + "/torpeek/?token=" + srv.cfg.Token; srv.URL() != want {
		t.Fatalf("URL() = %q, want %q", srv.URL(), want)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(root + "/torpeek")
	if err != nil {
		t.Fatalf("GET /torpeek: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("GET /torpeek: status %d, want 301", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); location != "/torpeek/" {
		t.Errorf("GET /torpeek redirects to %q, want /torpeek/", location)
	}

	// The shell itself is not gated (authGuard's doc comment says why), so
	// it loads with no token at all.
	for _, path := range []string{"/torpeek/", "/torpeek/app.js", "/torpeek/app.css"} {
		resp, err := http.Get(root + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", path, resp.StatusCode)
		}
	}

	// But the API and the socket - what this base-path deployment exists to
	// protect - refuse an unauthenticated request...
	if resp := post(t, root, "/torpeek/runs", `{"source":"magnet:?xt=urn:btih:abc"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /torpeek/runs with no token: status %d, want 401", resp.StatusCode)
	}
	if resp, err := dialExpectingRejection(t, root+"/torpeek/"); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Errorf("dial with no token: err=%v status=%d, want a 401 rejection", err, status)
	}

	// ...and accept one carrying the token srv.URL() itself printed.
	conn := dial(t, srv.URL())
	if got := next(t, conn); got["type"] != "run_state" {
		t.Errorf("under the configured base path the socket said %v, want run_state", got)
	}

	if resp := post(t, root, "/torpeek/runs?token="+srv.cfg.Token, `{"source":"magnet:?xt=urn:btih:abc"}`); resp.StatusCode != http.StatusAccepted {
		t.Errorf("POST /torpeek/runs?token=...: status %d, want 202", resp.StatusCode)
	}
}

// TestNormalizeBasePath covers the forms -base-path can arrive in: with or
// without a leading/trailing slash, blank, whitespace, or nothing at all
// (meaning the site root).
func TestNormalizeBasePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"/", ""},
		{"torpeek", "/torpeek"},
		{"/torpeek", "/torpeek"},
		{"/torpeek/", "/torpeek"},
		{"  /torpeek/  ", "/torpeek"},
		{"/a/b/", "/a/b"},
	}
	for _, tc := range cases {
		if got := normalizeBasePath(tc.in); got != tc.want {
			t.Errorf("normalizeBasePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestEventSocketAcceptsSameHostOrigin is the ordinary case, no proxy
// involved: a page served by this origin opens the socket.
func TestEventSocketAcceptsSameHostOrigin(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	address := "ws" + strings.TrimPrefix(ts.URL, "http") + "/events"
	header := http.Header{"Origin": {ts.URL}}
	conn, resp, err := websocket.DefaultDialer.Dial(address, header)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial with a matching Origin: %v (status %d)", err, status)
	}
	conn.Close()
}

// TestEventSocketRejectsForeignOrigin is the case checkOrigin exists to
// stop: a hostile page open in the same browser trying to open this socket.
func TestEventSocketRejectsForeignOrigin(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	address := "ws" + strings.TrimPrefix(ts.URL, "http") + "/events"
	header := http.Header{"Origin": {"http://evil.example"}}
	_, resp, err := websocket.DefaultDialer.Dial(address, header)
	if err == nil {
		t.Fatal("dial with a foreign Origin succeeded, want a rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Errorf("status = %d, want 403", status)
	}
}

// TestEventSocketAcceptsOriginMatchingForwardedHost is the reverse-proxy
// case: r.Host is whatever the proxy forwards as Host (nginx's own default,
// absent an explicit proxy_set_header Host, is the upstream address), while
// the browser's Origin names the public host the proxy also sends along as
// X-Forwarded-Host.
func TestEventSocketAcceptsOriginMatchingForwardedHost(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	address := "ws" + strings.TrimPrefix(ts.URL, "http") + "/events"
	header := http.Header{
		"Origin":           {"https://user.host.example"},
		"X-Forwarded-Host": {"user.host.example"},
	}
	conn, resp, err := websocket.DefaultDialer.Dial(address, header)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial with Origin matching X-Forwarded-Host: %v (status %d)", err, status)
	}
	conn.Close()
}

// TestEventSocketRejectsOriginNotMatchingForwardedHost keeps the fallback
// from widening acceptance to "anything with a Forwarded-Host header" - it
// only ever adds X-Forwarded-Host itself to the accepted set.
func TestEventSocketRejectsOriginNotMatchingForwardedHost(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	address := "ws" + strings.TrimPrefix(ts.URL, "http") + "/events"
	header := http.Header{
		"Origin":           {"http://evil.example"},
		"X-Forwarded-Host": {"user.host.example"},
	}
	_, resp, err := websocket.DefaultDialer.Dial(address, header)
	if err == nil {
		t.Fatal("dial with an Origin matching neither Host nor X-Forwarded-Host succeeded, want a rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Errorf("status = %d, want 403", status)
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

// TestStartHonoursThePinnedAddr is the web half of TOR-28's acceptance
// criterion: Start must bind exactly the Addr it was given, not something
// nearby - a managed host tells a person "this is your port", and that has
// to be provably true, not asserted.
func TestStartHonoursThePinnedAddr(t *testing.T) {
	fake := &fakeRun{}

	port := freeWebPort(t)
	addr := net.JoinHostPort("127.0.0.1", port)

	cfg := DefaultConfig()
	cfg.Addr = addr

	srv, err := Start(context.Background(), cfg, fake.runner, nil, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	if want := "http://" + addr + "/"; srv.URL() != want {
		t.Errorf("URL() = %q, want %q", srv.URL(), want)
	}

	// Prove it by connecting to the pinned address, not by trusting the
	// string Start reported.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial the pinned addr %s: %v", addr, err)
	}
	conn.Close()
}

// TestStartFailsLoudlyWhenAddrIsTaken: a pinned port already in use must be a
// startup error, never a silent bind elsewhere.
func TestStartFailsLoudlyWhenAddrIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	fake := &fakeRun{}
	cfg := DefaultConfig()
	cfg.Addr = addr

	srv, err := Start(context.Background(), cfg, fake.runner, nil, nil)
	if err == nil {
		srv.Close()
		t.Fatalf("Start on the already-occupied %s succeeded, want an error", addr)
	}
}

// freeWebPort asks the OS for a free port and releases it immediately - the
// caller rebinds it right away, so the gap is not a practical race here.
func freeWebPort(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	return port
}

// waitFor polls until cond holds, so a test never depends on a sleep being
// long enough.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

// fakeRuns is fakeRun for a server that holds more than one: it hands out a
// fresh event channel per run, keyed by the source that asked for it, so a
// test can drive two runs at once. fakeRun keeps one channel, which a second
// start silently overwrites.
//
// Each run's channel closes on its own when the run's context is cancelled,
// the way a real engine's does - so a cancelled run ends here too, rather
// than leaving pump waiting on a channel nobody will ever close.
type fakeRuns struct {
	mu     sync.Mutex
	runs   map[string]*fakeStream
	starts []string
}

type fakeStream struct {
	events chan core.Event
	ctx    context.Context
	once   sync.Once
}

func newFakeRuns() *fakeRuns {
	return &fakeRuns{runs: make(map[string]*fakeStream)}
}

func (f *fakeRuns) runner(ctx context.Context, req RunRequest) (<-chan core.Event, error) {
	stream := &fakeStream{events: make(chan core.Event, 32), ctx: ctx}

	f.mu.Lock()
	f.runs[req.Source] = stream
	f.starts = append(f.starts, req.Source)
	f.mu.Unlock()

	go func() {
		<-ctx.Done()
		stream.once.Do(func() { close(stream.events) })
	}()
	return stream.events, nil
}

// count is how many runs have reached the runner, which is the only proof
// that a queued run has not quietly started.
func (f *fakeRuns) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func (f *fakeRuns) started(source string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.runs[source]
	return ok
}

func (f *fakeRuns) stream(t *testing.T, source string) *fakeStream {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()
	stream, ok := f.runs[source]
	if !ok {
		t.Fatalf("no run has been started for %q", source)
	}
	return stream
}

func (f *fakeRuns) send(t *testing.T, source string, events ...core.Event) {
	t.Helper()

	stream := f.stream(t, source)
	for _, ev := range events {
		stream.events <- ev
	}
}

// finish ends a run's stream the way a completed engine run does.
func (f *fakeRuns) finish(t *testing.T, source string) {
	t.Helper()

	stream := f.stream(t, source)
	stream.once.Do(func() { close(stream.events) })
}

func (f *fakeRuns) context(t *testing.T, source string) context.Context {
	t.Helper()
	return f.stream(t, source).ctx
}

// startedRun is what POST /runs answers: an id, and whether the run took the
// slot or is waiting for it.
type startedRun struct{ id, state string }

func startRun(t *testing.T, base, source string) startedRun {
	t.Helper()

	resp := post(t, base, "/runs", `{"source":`+strconv.Quote(source)+`}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs for %s: status %d, want 202", source, resp.StatusCode)
	}
	body := decodeBody(t, resp)
	id, _ := body["id"].(string)
	state, _ := body["state"].(string)
	if id == "" {
		t.Fatalf("POST /runs answered %v, which names no run", body)
	}
	return startedRun{id: id, state: state}
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode the response body: %v", err)
	}
	return body
}

// runInfo finds one run in the registry snapshot.
func runInfo(t *testing.T, srv *Server, id string) RunInfo {
	t.Helper()

	for _, info := range srv.snapshot() {
		if info.ID == id {
			return info
		}
	}
	t.Fatalf("run %s is not in the registry", id)
	return RunInfo{}
}

// TestTwoRunsBackToBackDoNotContaminateEachOther is TOR-53's acceptance
// criterion: both runs complete, the second was queued rather than refused,
// and neither one's events nor its replay history end up in the other.
//
// The replay is where a shared history shows itself. Two runs both start at
// file 0, so with one progressAt keyed by file index alone, the second run's
// heartbeat overwrites the first run's in place - the first run's replay
// loses its progress and gains one belonging to a run it never was.
func TestTwoRunsBackToBackDoNotContaminateEachOther(t *testing.T) {
	const (
		sourceA = "magnet:?xt=urn:btih:aaa"
		sourceB = "magnet:?xt=urn:btih:bbb"
	)

	fake := newFakeRuns()
	srv, ts := newTestServer(t, fake.runner)

	live := dial(t, ts.URL)
	if got := next(t, live); got["type"] != "run_state" {
		t.Fatalf("first message is %v, want the connection marker", got)
	}

	first := startRun(t, ts.URL, sourceA)
	if first.state != "running" {
		t.Fatalf("the first run is %q, want running", first.state)
	}
	second := startRun(t, ts.URL, sourceB)
	if second.state != "queued" {
		t.Fatalf("the second run is %q, want queued - it must wait, not be refused", second.state)
	}

	fake.send(t, sourceA,
		core.MetadataReady{Name: "First", InfoHash: "aaa", Selected: []int{0}},
		core.Progress{File: 0, FramesDone: 1, FramesTotal: 2},
		core.Progress{File: 0, FramesDone: 2, FramesTotal: 2},
		core.Done{Reason: core.StopCompleted, Files: 1, Frames: 2},
	)
	fake.finish(t, sourceA)

	// The queued run takes the slot by itself once the first is over.
	waitFor(t, func() bool { return fake.started(sourceB) })

	fake.send(t, sourceB,
		core.MetadataReady{Name: "Second", InfoHash: "bbb", Selected: []int{0}},
		core.Progress{File: 0, FramesDone: 9, FramesTotal: 9},
		core.Done{Reason: core.StopCompleted, Files: 1, Frames: 9},
	)
	fake.finish(t, sourceB)

	waitFor(t, func() bool {
		return runInfo(t, srv, first.id).State == RunDone &&
			runInfo(t, srv, second.id).State == RunDone
	})
	if got := runInfo(t, srv, first.id).InfoHash; got != "aaa" {
		t.Errorf("the first run recorded infohash %q, want aaa", got)
	}
	if got := runInfo(t, srv, second.id).InfoHash; got != "bbb" {
		t.Errorf("the second run recorded infohash %q, want bbb", got)
	}

	// What the live socket saw: every message after the marker names the run
	// it belongs to, and each run's own sequence is exactly what it emitted.
	byRun := map[string][]string{}
	for i := 0; i < 13; i++ {
		ev := next(t, live)
		id, _ := ev["run"].(string)
		if id != first.id && id != second.id {
			t.Fatalf("live message %v belongs to no run of this test", ev)
		}
		kind, _ := ev["type"].(string)
		if kind == "run_state" {
			kind = "run_state:" + ev["state"].(string)
		}
		byRun[id] = append(byRun[id], kind)
	}
	wantFirst := []string{
		"run_state:queued", "run_state:running", "metadata_ready",
		"progress", "progress", "done", "run_state:done",
	}
	wantSecond := []string{
		"run_state:queued", "run_state:running", "metadata_ready",
		"progress", "done", "run_state:done",
	}
	if got := byRun[first.id]; !slices.Equal(got, wantFirst) {
		t.Errorf("the first run's live stream was %v, want %v", got, wantFirst)
	}
	if got := byRun[second.id]; !slices.Equal(got, wantSecond) {
		t.Errorf("the second run's live stream was %v, want %v", got, wantSecond)
	}

	// And what a page opening now replays: the same two runs, each whole,
	// each still its own - with its heartbeats collapsed to the last one of
	// that run, not of whichever run ticked last.
	replay := dial(t, ts.URL)
	if got := next(t, replay); got["type"] != "run_state" {
		t.Fatalf("replay opens with %v, want the connection marker", got)
	}
	for _, want := range []struct {
		run    startedRun
		name   string
		frames float64
	}{
		{first, "First", 2},
		{second, "Second", 9},
	} {
		for _, kind := range []string{"run_state", "run_state", "metadata_ready", "progress", "done", "run_state"} {
			ev := next(t, replay)
			if ev["type"] != kind {
				t.Fatalf("replaying run %s: got %v, want a %s", want.run.id, ev, kind)
			}
			if ev["run"] != want.run.id {
				t.Fatalf("replaying run %s: %v belongs to another run", want.run.id, ev)
			}
			switch kind {
			case "metadata_ready":
				if ev["name"] != want.name {
					t.Errorf("run %s replayed the metadata of %v, want %q", want.run.id, ev["name"], want.name)
				}
			case "progress":
				if ev["frames_done"] != want.frames {
					t.Errorf("run %s replayed a heartbeat at %v frames, want %v - a heartbeat of another run",
						want.run.id, ev["frames_done"], want.frames)
				}
			}
		}
	}
}

// TestCancellingAQueuedRunLeavesTheRunningOneAlone: a queued run has no
// context to cancel, so stopping it is taking it out of the queue - and doing
// that must not touch the run holding the slot, nor the runs behind it.
func TestCancellingAQueuedRunLeavesTheRunningOneAlone(t *testing.T) {
	const (
		sourceA = "magnet:?xt=urn:btih:aaa"
		sourceB = "magnet:?xt=urn:btih:bbb"
		sourceC = "magnet:?xt=urn:btih:ccc"
	)

	fake := newFakeRuns()
	srv, ts := newTestServer(t, fake.runner)

	running := startRun(t, ts.URL, sourceA)
	queued := startRun(t, ts.URL, sourceB)
	behind := startRun(t, ts.URL, sourceC)

	resp := post(t, ts.URL, "/runs/cancel", `{"id":"`+queued.id+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/cancel for a queued run: status %d, want 202", resp.StatusCode)
	}
	if got := decodeBody(t, resp)["state"]; got != "cancelled" {
		t.Errorf("the cancelled run is reported as %v, want cancelled", got)
	}

	if got := runInfo(t, srv, queued.id).State; got != RunCancelled {
		t.Errorf("the queued run is %q, want cancelled", got)
	}
	if got := runInfo(t, srv, running.id).State; got != RunRunning {
		t.Errorf("the running run is %q, want running - cancelling a queued run must not touch it", got)
	}
	if got := runInfo(t, srv, behind.id).State; got != RunQueued {
		t.Errorf("the run behind it is %q, want queued", got)
	}

	select {
	case <-fake.context(t, sourceA).Done():
		t.Fatal("cancelling a queued run cancelled the running one")
	default:
	}
	if got := fake.count(); got != 1 {
		t.Errorf("the runner was called %d times, want 1", got)
	}

	// When the slot frees up it goes to the run behind, never to the
	// cancelled one.
	fake.finish(t, sourceA)
	waitFor(t, func() bool { return fake.started(sourceC) })
	if fake.started(sourceB) {
		t.Error("the cancelled run was started anyway")
	}
}

func TestCancellingAnUnknownRunIsNotFound(t *testing.T) {
	fake := newFakeRuns()
	ts := testServer(t, fake.runner)

	if resp := post(t, ts.URL, "/runs/cancel", `{"id":"nosuchrun"}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /runs/cancel for an unknown run: status %d, want 404", resp.StatusCode)
	}
}

func TestCancellingAFinishedRunIsAConflict(t *testing.T) {
	const source = "magnet:?xt=urn:btih:aaa"

	fake := newFakeRuns()
	srv, ts := newTestServer(t, fake.runner)

	run := startRun(t, ts.URL, source)
	fake.finish(t, source)
	waitFor(t, func() bool { return runInfo(t, srv, run.id).State == RunDone })

	if resp := post(t, ts.URL, "/runs/cancel", `{"id":"`+run.id+`"}`); resp.StatusCode != http.StatusConflict {
		t.Errorf("POST /runs/cancel for a finished run: status %d, want 409", resp.StatusCode)
	}
}

// TestFinishedRunsFallOutOfMemory: a finished run outlives its own completion
// - the panel shows runs that are over - but not forever. This process runs
// for hours, and every finished run holds its whole replay.
func TestFinishedRunsFallOutOfMemory(t *testing.T) {
	fake := newFakeRuns()
	srv, ts := newTestServer(t, fake.runner)

	var ids []string
	for i := 0; i < keepFinishedRuns+3; i++ {
		source := fmt.Sprintf("magnet:?xt=urn:btih:%02d", i)
		run := startRun(t, ts.URL, source)
		ids = append(ids, run.id)
		waitFor(t, func() bool { return fake.started(source) })
		fake.finish(t, source)
		waitFor(t, func() bool {
			for _, info := range srv.snapshot() {
				if info.ID == run.id {
					return info.State == RunDone
				}
			}
			return false
		})
	}

	held := srv.snapshot()
	if len(held) != keepFinishedRuns {
		t.Fatalf("the registry holds %d finished runs, want %d", len(held), keepFinishedRuns)
	}
	if held[0].ID != ids[3] {
		t.Errorf("the oldest run held is %s, want %s - the oldest three should have gone", held[0].ID, ids[3])
	}

	// A dropped run's history goes with it: it is not replayed to a page
	// connecting now.
	conn := dial(t, ts.URL)
	if got := next(t, conn); got["type"] != "run_state" {
		t.Fatalf("replay opens with %v, want the connection marker", got)
	}
	// Three records per run held: accepted, started, finished.
	seen := map[string]bool{}
	for i := 0; i < keepFinishedRuns*3; i++ {
		if id, ok := next(t, conn)["run"].(string); ok {
			seen[id] = true
		}
	}
	if len(seen) != keepFinishedRuns {
		t.Errorf("the replay covers %d runs, want %d", len(seen), keepFinishedRuns)
	}
	for _, gone := range ids[:3] {
		if seen[gone] {
			t.Errorf("run %s was trimmed from the registry but is still replayed", gone)
		}
	}
}

// TestTrimNeverDropsALiveRun is the other half of the rule, at the level it
// is decided: a run still queued or running stays, however old it is, because
// dropping it would forget the one thing that can still be cancelled.
func TestTrimNeverDropsALiveRun(t *testing.T) {
	fake := &fakeRun{}
	srv, _ := newTestServer(t, fake.runner)

	live := &runEntry{id: "live", state: RunRunning, source: "the oldest run"}
	srv.runs[live.id] = live
	srv.order = append(srv.order, live.id)

	var newest string
	for i := 0; i < keepFinishedRuns+5; i++ {
		entry := &runEntry{id: fmt.Sprintf("done-%02d", i), state: RunDone}
		srv.runs[entry.id] = entry
		srv.order = append(srv.order, entry.id)
		newest = entry.id
	}

	srv.trim()

	held := srv.snapshot()
	if len(held) != keepFinishedRuns+1 {
		t.Fatalf("trim left %d runs, want %d finished plus the live one", len(held), keepFinishedRuns)
	}
	if held[0].ID != live.id {
		t.Errorf("the oldest run held is %s, want the live one", held[0].ID)
	}
	if held[len(held)-1].ID != newest {
		t.Errorf("trim dropped the newest finished run %s", newest)
	}
}

// TestClosingCancelsTheQueueAndItsStagedUploads: a queued run will never
// start once the server stops, so nothing would ever run the cleanup that
// removes the .torrent staged for it - the file would outlive the process
// that made it.
func TestClosingCancelsTheQueueAndItsStagedUploads(t *testing.T) {
	const source = "magnet:?xt=urn:btih:aaa"

	fake := newFakeRuns()
	srv, ts := newTestServer(t, fake.runner)

	running := startRun(t, ts.URL, source)

	before := stagedUploads(t)
	resp := uploadTorrent(t, ts.URL, []byte("torrent-bytes"), "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs/upload: status %d, want 202", resp.StatusCode)
	}
	queued := decodeBody(t, resp)
	if queued["state"] != "queued" {
		t.Fatalf("the dropped .torrent is %v, want queued", queued["state"])
	}

	staged := ""
	for dir := range stagedUploads(t) {
		if !before[dir] {
			staged = dir
		}
	}
	if staged == "" {
		t.Fatal("the upload staged nothing on disk")
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("closing the server left the queued run's staged upload behind: %v", err)
	}
	if got := runInfo(t, srv, queued["id"].(string)).State; got != RunCancelled {
		t.Errorf("the queued run is %q after Close, want cancelled", got)
	}
	select {
	case <-fake.context(t, source).Done():
	case <-time.After(5 * time.Second):
		t.Error("closing the server did not cancel the run holding the slot")
	}
	if got := runInfo(t, srv, running.id).ID; got != running.id {
		t.Errorf("the running run vanished from the registry: %q", got)
	}
}

// stagedUploads lists the temp directories handleUploadTorrent makes, so a
// test can name the one its own upload created.
func stagedUploads(t *testing.T) map[string]bool {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "torpeek-upload-*"))
	if err != nil {
		t.Fatalf("list staged uploads: %v", err)
	}
	out := make(map[string]bool, len(matches))
	for _, dir := range matches {
		out[dir] = true
	}
	return out
}

// TestStartRunRejectsANegativeCount: a count that frames.Plan.Validate would
// refuse is refused about the request instead, before anything is queued. The
// alternative is a 202 followed minutes later by a failed run, for a number
// the client could see was wrong the moment it sent it.
func TestStartRunRejectsANegativeCount(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	resp := post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc","count":-1}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /runs with a negative count: status %d, want 400", resp.StatusCode)
	}
	if fake.starts != 0 {
		t.Errorf("the runner was called %d times for a negative count, want 0", fake.starts)
	}
}

// TestStartRunAcceptsAnAbsentCount pins the other half of treating zero as
// "not stated": it is not a rejection, it is how a page that never touched
// the field asks for the server's own -n.
func TestStartRunAcceptsAnAbsentCount(t *testing.T) {
	var got int
	runner := func(ctx context.Context, req RunRequest) (<-chan core.Event, error) {
		got = req.Count
		return nil, errors.New("stop here")
	}
	ts := testServer(t, runner)

	resp := post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("POST /runs with no count: status %d, want 202", resp.StatusCode)
	}
	if got != 0 {
		t.Errorf("Count = %d, want 0 - the request said nothing", got)
	}
}

// TestStartRunCarriesTheFrameCount is the wire half of TOR-68: the number
// typed beside the mode select has to arrive on the RunRequest the runner
// sees, which is where runConfig turns it into core.Config.Plan.Count.
func TestStartRunCarriesTheFrameCount(t *testing.T) {
	var got int
	runner := func(ctx context.Context, req RunRequest) (<-chan core.Event, error) {
		got = req.Count
		return nil, errors.New("stop here")
	}
	ts := testServer(t, runner)

	post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc","count":6}`)

	if got != 6 {
		t.Errorf("Count = %d, want 6", got)
	}
}

// TestUploadCarriesTheFrameCount: the drop path builds its RunRequest by
// hand, so the count has to be read off the multipart form there too - a
// dropped .torrent reads the same intake field as a pasted magnet.
func TestUploadCarriesTheFrameCount(t *testing.T) {
	var got int
	runner := func(ctx context.Context, req RunRequest) (<-chan core.Event, error) {
		got = req.Count
		return nil, errors.New("stop here")
	}
	ts := testServer(t, runner)

	uploadTorrentWithCount(t, ts.URL, []byte("torrent-bytes"), "", "6")

	if got != 6 {
		t.Errorf("Count = %d, want 6 from the multipart form", got)
	}
}

// TestUploadWithAnUnreadableCountFallsBackToTheDefault: a form value that is
// not a number is no count at all rather than a refused drop. A drop is not
// the place to argue about a form field, and "no count" already means
// exactly what the server's own default means.
func TestUploadWithAnUnreadableCountFallsBackToTheDefault(t *testing.T) {
	got := -1
	runner := func(ctx context.Context, req RunRequest) (<-chan core.Event, error) {
		got = req.Count
		return nil, errors.New("stop here")
	}
	ts := testServer(t, runner)

	resp := uploadTorrentWithCount(t, ts.URL, []byte("torrent-bytes"), "", "not-a-number")
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("upload with an unreadable count: status %d, want 202", resp.StatusCode)
	}
	if got != 0 {
		t.Errorf("Count = %d, want 0 - an unreadable field is no count", got)
	}
}

// TestDefaultsReportsTheServersFrameCount is what lets the intake field show
// the number actually in force. The assets are static, served straight out
// of the embed with no templating step, so a default written into the HTML
// would quietly disagree with a server started as -n 6; the page asks
// instead.
func TestDefaultsReportsTheServersFrameCount(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultCount = 6
	fake := &fakeRun{}
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	resp := get(t, ts.URL, "/defaults")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /defaults: status %d, want 200", resp.StatusCode)
	}
	if count, ok := decodeBody(t, resp)["count"].(float64); !ok || int(count) != 6 {
		t.Errorf("count = %v, want 6", decodeBody(t, resp)["count"])
	}
}
