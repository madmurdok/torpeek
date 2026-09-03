package web

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/output"
)

// savedTorrentBytes is a bencoded dictionary, not JPEG bytes or a placeholder
// string: what is being checked below is that the server does not let Go's
// content sniffer decide what this is, and the sniffer's answer for bencode is
// "text/plain" precisely because it starts with printable ASCII.
var savedTorrentBytes = []byte("d8:announce20:http://tracker/annce")

// writeSavedTorrent puts a .torrent where a run would have left one, using
// the very writer a run uses - so a test cannot pass by agreeing with itself
// about a path output.Layout does not actually name.
func writeSavedTorrent(t *testing.T, root, infoHash, params string) string {
	t.Helper()

	writer, err := output.NewWriter(output.Layout{Root: root, InfoHash: infoHash, Params: params})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	path, err := writer.WriteTorrent(savedTorrentBytes)
	if err != nil {
		t.Fatalf("write the saved torrent: %v", err)
	}
	return path
}

// awaitDone reads this run's own done event off the socket, ignoring
// everything that belongs to another run or to another stage of this one.
func awaitDone(t *testing.T, conn *websocket.Conn, runID string) map[string]any {
	t.Helper()

	for i := 0; i < 40; i++ {
		event := next(t, conn)
		if event["run"] == runID && event["type"] == "done" {
			return event
		}
	}
	t.Fatalf("no done event arrived for run %s", runID)
	return nil
}

// TestAFinishedRunOffersItsTorrentAsADownload is the serving half of TOR-73.
//
// Two things are being proved at once and they fail differently. The URL has
// to exist at all - it is minted from the path the run's own done event
// announced, which is what lets the link appear exactly when the file is
// there - and the response has to be a download rather than a page: Go has no
// mime type for .torrent, so without an explicit Content-Type the sniffer
// calls bencode text and the browser renders it.
func TestAFinishedRunOffersItsTorrentAsADownload(t *testing.T) {
	root := t.TempDir()
	torrentPath := writeSavedTorrent(t, root, strings.Repeat("a", 40), "0123456789abcdef")

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	conn := dial(t, ts.URL)
	next(t, conn) // the connection marker

	const source = "magnet:?xt=urn:btih:kept"
	run := startRun(t, ts.URL, source)
	fake.send(t, source, core.Done{Reason: core.StopCompleted, Files: 1, Frames: 3, TorrentPath: torrentPath})
	fake.finish(t, source)

	done := awaitDone(t, conn, run.id)
	url, _ := done["torrent_url"].(string)
	if url == "" {
		t.Fatalf("the done event announced no torrent_url: %v", done)
	}

	resp := get(t, ts.URL, "/"+url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", url, resp.StatusCode)
	}
	// The premise, checked rather than assumed, because whether this
	// assertion can fail depends on the host: Go's own mime table has no
	// .torrent entry, but a machine carrying a system mime database that does
	// (macOS ships one) would have ServeFile set the right header by
	// accident. What never varies is what the sniffer falls back to, and that
	// is the reason the header is set explicitly instead of relied upon.
	if sniffed := http.DetectContentType(savedTorrentBytes); !strings.HasPrefix(sniffed, "text/") {
		t.Fatalf("this test's premise is gone: bencode now sniffs as %q rather than text", sniffed)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/x-bittorrent" {
		t.Errorf("Content-Type is %q, want application/x-bittorrent - a host whose mime database "+
			"does not know .torrent would otherwise serve the sniffer's answer, which is text", got)
	}
	disposition := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment;") {
		t.Errorf("Content-Disposition is %q, want an attachment - the link has to save, not display", disposition)
	}
	if want := filepath.Base(torrentPath); !strings.Contains(disposition, want) {
		t.Errorf("Content-Disposition is %q, want it to name %q - a download called anything else "+
			"collides with every other run's in the same folder", disposition, want)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the body: %v", err)
	}
	if string(body) != string(savedTorrentBytes) {
		t.Errorf("served %q, want %q", body, savedTorrentBytes)
	}
}

// TestSendingASavedTorrentToTheWatchDirectory is the second feature: not
// where the browser is, but where the UI is. A copy lands in the directory a
// torrent client on this host is already reading.
//
// The temporary-file check is not incidental. A watch directory is by
// definition being watched, so a client opens what appears in it immediately;
// a partial file is a corrupt torrent, and some clients remember refusing it.
func TestSendingASavedTorrentToTheWatchDirectory(t *testing.T) {
	root := t.TempDir()
	watch := t.TempDir()
	torrentPath := writeSavedTorrent(t, root, strings.Repeat("b", 40), "0123456789abcdef")

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	cfg.WatchDir = watch
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	conn := dial(t, ts.URL)
	next(t, conn)

	const source = "magnet:?xt=urn:btih:watched"
	run := startRun(t, ts.URL, source)
	fake.send(t, source, core.Done{Reason: core.StopCompleted, TorrentPath: torrentPath})
	fake.finish(t, source)

	url, _ := awaitDone(t, conn, run.id)["torrent_url"].(string)
	if url == "" {
		t.Fatal("the done event announced no torrent_url")
	}

	// GET /defaults is how the page learns it may draw the button at all.
	defaults := decodeBody(t, get(t, ts.URL, "/defaults"))
	if defaults["watch"] != true {
		t.Errorf("GET /defaults says watch=%v, want true - the page draws the button from this", defaults["watch"])
	}

	resp := post(t, ts.URL, "/"+url+"/watch", "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s/watch: status %d, want 200", url, resp.StatusCode)
	}
	dest, _ := decodeBody(t, resp)["path"].(string)
	if want := filepath.Join(watch, filepath.Base(torrentPath)); dest != want {
		t.Errorf("answered path %q, want %q", dest, want)
	}

	landed, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read what landed in the watch directory: %v", err)
	}
	if string(landed) != string(savedTorrentBytes) {
		t.Errorf("the watch directory holds %q, want %q", landed, savedTorrentBytes)
	}

	entries, err := os.ReadDir(watch)
	if err != nil {
		t.Fatalf("read the watch directory: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the watch directory holds %v, want only the torrent - a leftover partial file "+
			"is one a watching client may pick up and refuse", names)
	}
}

// TestWithoutAWatchDirectoryThereIsNothingToPress: the flag is absent, so the
// page must never draw the button, and the route behind it refuses rather
// than inventing a destination.
func TestWithoutAWatchDirectoryThereIsNothingToPress(t *testing.T) {
	root := t.TempDir()
	torrentPath := writeSavedTorrent(t, root, strings.Repeat("c", 40), "0123456789abcdef")

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	conn := dial(t, ts.URL)
	next(t, conn)

	const source = "magnet:?xt=urn:btih:unwatched"
	run := startRun(t, ts.URL, source)
	fake.send(t, source, core.Done{Reason: core.StopCompleted, TorrentPath: torrentPath})
	fake.finish(t, source)

	url, _ := awaitDone(t, conn, run.id)["torrent_url"].(string)
	if url == "" {
		t.Fatal("the done event announced no torrent_url")
	}

	defaults := decodeBody(t, get(t, ts.URL, "/defaults"))
	if defaults["watch"] != false {
		t.Errorf("GET /defaults says watch=%v, want false - the page must not draw a button that cannot work",
			defaults["watch"])
	}

	resp := post(t, ts.URL, "/"+url+"/watch", "{}")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("POST %s/watch with no watch directory: status %d, want 503", url, resp.StatusCode)
	}

	// The download still works: it is the other feature, and it needs no flag.
	if got := get(t, ts.URL, "/"+url).StatusCode; got != http.StatusOK {
		t.Errorf("GET %s: status %d, want 200 - the download does not depend on a watch directory", url, got)
	}
}

// TestOnlyASavedTorrentReachesTheWatchDirectory is the scope check on the
// send route. Every frame, sheet and manifest of every run is published into
// the same handle registry, so "send this to my torrent client" has to mean a
// torrent - a handle a page had lying around must not put a JPEG into a
// directory a client is reading.
func TestOnlyASavedTorrentReachesTheWatchDirectory(t *testing.T) {
	root := t.TempDir()
	watch := t.TempDir()

	frame := filepath.Join(root, "frame.jpg")
	if err := os.WriteFile(frame, []byte("jpeg-bytes"), 0o600); err != nil {
		t.Fatalf("write a frame: %v", err)
	}

	fake := newFakeRuns()
	cfg := DefaultConfig()
	cfg.OutputRoot = root
	cfg.WatchDir = watch
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	conn := dial(t, ts.URL)
	next(t, conn)

	const source = "magnet:?xt=urn:btih:frames"
	run := startRun(t, ts.URL, source)
	fake.send(t, source, core.FrameReady{File: 0, Index: 0, Path: frame})

	var frameURL string
	for i := 0; i < 40 && frameURL == ""; i++ {
		event := next(t, conn)
		if event["run"] == run.id && event["type"] == "frame_ready" {
			frameURL, _ = event["url"].(string)
		}
	}
	if frameURL == "" {
		t.Fatal("no frame_ready with a url arrived")
	}
	t.Cleanup(func() { fake.finish(t, source) })

	if got := post(t, ts.URL, "/"+frameURL+"/watch", "{}").StatusCode; got != http.StatusNotFound {
		t.Errorf("POST %s/watch: status %d, want 404 - a frame is not a torrent", frameURL, got)
	}
	if got := post(t, ts.URL, "/files/deadbeef/watch", "{}").StatusCode; got != http.StatusNotFound {
		t.Errorf("POST /files/deadbeef/watch: status %d, want 404 - that handle was never published", got)
	}

	entries, err := os.ReadDir(watch)
	if err != nil {
		t.Fatalf("read the watch directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the watch directory holds %d entries after two refused requests, want none", len(entries))
	}
}

// TestAReopenedRunOffersTheSameTorrent: a run read back from disk announces
// its .torrent exactly as a live one does, so the link does not depend on
// which process happened to capture the run. Nothing here primes the server -
// the run is only on disk, the same as after a restart.
func TestAReopenedRunOffersTheSameTorrent(t *testing.T) {
	const (
		infoHash = "7777777777777777777777777777777777777777"
		params   = "0123456789abcdef"
	)
	root := t.TempDir()
	buildCachedRun(t, root, infoHash, params, cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "Reopened With A Torrent",
		Videos: []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}}, Complete: []int{0},
	}, oneFrameManifest(0, "movie.mkv"))
	torrentPath := writeSavedTorrent(t, root, infoHash, params)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithReplayer(t, cfg, (&fakeRun{}).runner, realReplayer(root))

	conn := dial(t, ts.URL)
	next(t, conn)

	id, state, status := reopenRun(t, ts.URL, infoHash, params)
	if status != http.StatusAccepted || state != "done" {
		t.Fatalf("reopen: status %d state %q, want 202 done", status, state)
	}

	url, _ := awaitDone(t, conn, id)["torrent_url"].(string)
	if url == "" {
		t.Fatal("the reopened run announced no torrent_url; a run on disk must offer the same link a live one does")
	}

	resp := get(t, ts.URL, "/"+url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the body: %v", err)
	}
	if string(body) != string(savedTorrentBytes) {
		t.Errorf("the reopened run served %q, want the bytes at %s", body, torrentPath)
	}
}

// TestAReopenedRunWithNoSavedTorrentOffersNoLink is the other half of the
// same rule, and the reason the link is announced rather than assumed: a run
// captured before torpeek kept a .torrent has none, and that has to stay a
// cache hit with no link - not a miss, and not a link to nothing.
func TestAReopenedRunWithNoSavedTorrentOffersNoLink(t *testing.T) {
	const (
		infoHash = "8888888888888888888888888888888888888888"
		params   = "0123456789abcdef"
	)
	root := t.TempDir()
	buildCachedRun(t, root, infoHash, params, cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "Captured Before TOR-73",
		Videos: []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}}, Complete: []int{0},
	}, oneFrameManifest(0, "movie.mkv"))

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithReplayer(t, cfg, (&fakeRun{}).runner, realReplayer(root))

	conn := dial(t, ts.URL)
	next(t, conn)

	id, state, status := reopenRun(t, ts.URL, infoHash, params)
	if status != http.StatusAccepted || state != "done" {
		t.Fatalf("reopen: status %d state %q, want 202 done - a run with no .torrent is still a hit", status, state)
	}

	done := awaitDone(t, conn, id)
	if _, ok := done["torrent_url"]; ok {
		t.Errorf("the run announced torrent_url=%v with no .torrent on disk", done["torrent_url"])
	}
	if _, err := os.Stat(output.Layout{Root: root, InfoHash: infoHash, Params: params}.TorrentPath()); !os.IsNotExist(err) {
		t.Fatalf("this test's premise is wrong: a .torrent exists at the layout's path (%v)", err)
	}

	// The rest of the run is untouched: the manifest is still readable, which
	// is what "no link" must not have cost.
	if _, ok := cache.LoadManifest(output.Layout{Root: root, InfoHash: infoHash, Params: params}.
		FileDir(0, "movie.mkv")); !ok {
		t.Error("the reopened run's manifest is no longer readable")
	}
}
