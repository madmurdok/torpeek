package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

	srv := newServer(context.Background(), DefaultConfig(), runner)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return ts
}

// dial opens the event socket against a server whose UI root is at base.
func dial(t *testing.T, base string) *websocket.Conn {
	t.Helper()

	address := "ws" + strings.TrimPrefix(strings.TrimSuffix(base, "/"), "http") + "/events"
	conn, resp, err := websocket.DefaultDialer.Dial(address, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial %s: %v (status %d)", address, err, status)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
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

// uploadTorrent posts a multipart form the way a browser's FormData would
// for a dropped .torrent, so the upload tests exercise the same encoding the
// frontend sends.
func uploadTorrent(t *testing.T, base string, content []byte, mode string) *http.Response {
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

	active := next(t, conn)
	if active["type"] != "run_state" || active["active"] != true {
		t.Fatalf("after an upload, got %v, want an active run_state", active)
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

// TestUploadRespectsOneRunAtATime is the same rule TestOneRunAtATime checks
// for the magnet path, on the upload path.
func TestUploadRespectsOneRunAtATime(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	if resp := post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc"}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs: status %d, want 202", resp.StatusCode)
	}

	resp := uploadTorrent(t, ts.URL, []byte("bytes"), "")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("POST /runs/upload while a run is active: status %d, want 409", resp.StatusCode)
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
	for _, want := range []string{"run_state", "metadata_ready", "file_started"} {
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
	if got := next(t, conn); got["type"] != "run_state" {
		t.Fatalf("first replayed message is %v, want run_state", got)
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

// TestOneRunAtATime keeps two tabs from competing for the same output
// directory and the same traffic budget.
func TestOneRunAtATime(t *testing.T) {
	fake := &fakeRun{}
	ts := testServer(t, fake.runner)

	if resp := post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:abc"}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first POST /runs: status %d, want 202", resp.StatusCode)
	}
	resp := post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:def"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second POST /runs: status %d, want 409", resp.StatusCode)
	}
	if fake.starts != 1 {
		t.Errorf("the runner was called %d times, want 1", fake.starts)
	}

	// Once the run ends, the next one is allowed.
	close(fake.events)
	waitFor(t, func() bool {
		again := post(t, ts.URL, "/runs", `{"source":"magnet:?xt=urn:btih:def"}`)
		return again.StatusCode == http.StatusAccepted
	})
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

func TestRunnerFailureIsReported(t *testing.T) {
	fake := &fakeRun{err: errors.New("no such torrent file")}
	ts := testServer(t, fake.runner)

	resp := post(t, ts.URL, "/runs", `{"source":"/nope.torrent"}`)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /runs: status %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body, "no such torrent file") {
		t.Errorf("response %q does not name the failure", body)
	}
}

// TestWorksUnderABasePath is the seam TOR-29 will configure. The prefix is not
// yet a setting, but the UI must already be shaped so that adding one is a
// StripPrefix and nothing more: no absolute route, no absolute asset
// reference, no absolute socket URL (REQUIREMENTS.md section 3.3).
func TestWorksUnderABasePath(t *testing.T) {
	fake := &fakeRun{}
	srv := newServer(context.Background(), DefaultConfig(), fake.runner)
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

	srv, err := Start(context.Background(), cfg, fake.runner)
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

	srv, err := Start(context.Background(), cfg, fake.runner)
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
