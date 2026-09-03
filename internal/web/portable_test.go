package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madmurdok/torpeek/internal/cache"
)

// TOR-60 at the HTTP boundary. The results tree is a library now - listed,
// kept, reopened later - and the two requests a panel makes of one it did not
// watch being produced are GET /runs and the reopen behind it, plus the
// per-file view that mints a URL for every frame it finds. All three read
// paths out of manifests written by some earlier process, possibly on another
// machine, so all three are asked here about a tree that has been moved.

// copyResults copies a results tree the way somebody would copy a directory to
// another disk, leaving the original exactly where it was.
func copyResults(t *testing.T, from, to string) {
	t.Helper()

	if err := filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	}); err != nil {
		t.Fatalf("copy %s to %s: %v", from, to, err)
	}
}

// stampFrames rewrites every frame under a tree with content naming it, so a
// served image proves which tree answered rather than merely which path was
// printed.
func stampFrames(t *testing.T, root, content string) {
	t.Helper()

	stamped := 0
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(filepath.Dir(path)) != "frames" {
			return nil
		}
		stamped++
		return os.WriteFile(path, []byte(content), 0o600)
	}); err != nil {
		t.Fatalf("stamp the frames under %s: %v", root, err)
	}
	if stamped == 0 {
		t.Fatalf("no frames found under %s", root)
	}
}

// TestAMovedResultsTreeIsListedAndReopened is the acceptance criterion as a
// person meets it: the same directory, somewhere else, opened through the UI.
// Listing already worked before TOR-60 and is asserted anyway - it reads
// run.json, which holds no paths at all, and that is exactly why the listing
// could call a run intact while reopening it answered failed.
func TestAMovedResultsTreeIsListedAndReopened(t *testing.T) {
	const (
		infoHash = "6666666666666666666666666666666666dead"
		params   = "deadbeef"
	)
	base := t.TempDir()
	from, to := filepath.Join(base, "seedbox"), filepath.Join(base, "laptop")

	buildCachedRun(t, from, infoHash, params, cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "Carried Home",
		Videos: []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}}, Complete: []int{0},
	}, oneFrameManifest(0, "movie.mkv"))

	if err := os.Rename(from, to); err != nil {
		t.Fatalf("move the results tree: %v", err)
	}
	stampFrames(t, to, "carried home")

	cfg := DefaultConfig()
	cfg.OutputRoot = to
	_, ts := newTestServerWithReplayer(t, cfg, (&fakeRun{}).runner, realReplayer(to))

	rows := listRuns(t, ts.URL)
	if len(rows) != 1 || rows[0].InfoHash != infoHash || rows[0].Complete != 1 {
		t.Fatalf("GET /runs = %+v, want the moved run listed as complete", rows)
	}

	conn := dial(t, ts.URL)
	next(t, conn) // idle run_state

	id, state, status := reopenRun(t, ts.URL, infoHash, params)
	if status != http.StatusAccepted || state != "done" {
		t.Fatalf("reopen: status %d state %q, want 202 done - the tree is all there, just elsewhere", status, state)
	}

	var frameURL string
	for i := 0; i < 20 && frameURL == ""; i++ {
		event := next(t, conn)
		if event["run"] == id && event["type"] == "frame_ready" {
			frameURL, _ = event["url"].(string)
		}
	}
	if frameURL == "" {
		t.Fatal("no frame_ready with a url arrived for the reopened run")
	}

	resp := get(t, ts.URL, "/"+frameURL)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", frameURL, resp.StatusCode)
	}
	if body := readAll(t, resp); body != "carried home" {
		t.Errorf("the served frame reads %q, want the moved tree's own bytes", body)
	}
}

// TestFileDetailOfACopyNeverServesTheOriginal covers the other reader of
// recorded frame paths (Server.fileDetail), against the silent failure: with
// the original still in place, a copy must serve its own frames. The two trees
// differ only in what is inside the frames, so the assertion is on the bytes
// that came back over HTTP.
func TestFileDetailOfACopyNeverServesTheOriginal(t *testing.T) {
	const (
		infoHash = "77777777777777777777777777777777777777aa"
		params   = "aaaa1111"
	)
	base := t.TempDir()
	original, copied := filepath.Join(base, "original"), filepath.Join(base, "copy")

	writeSet(t, original, infoHash, params, 3, 6, "Sintel/sintel.mp4", 43700, 308900, 576300)
	copyResults(t, original, copied)
	stampFrames(t, original, "the original")
	stampFrames(t, copied, "the copy")

	cfg := DefaultConfig()
	cfg.OutputRoot = copied
	_, ts := newTestServerWithConfig(t, cfg, (&fakeRun{}).runner)

	detail, status := getFileDetail(t, ts.URL, infoHash, "6")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	if len(detail.Frames) != 3 {
		t.Fatalf("frames = %+v, want the copy's three", detail.Frames)
	}
	for _, frame := range detail.Frames {
		resp := get(t, ts.URL, "/"+frame.URL)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200", frame.URL, resp.StatusCode)
		}
		if body := readAll(t, resp); body != "the copy" {
			t.Errorf("frame at %s served %q, want the copy's own bytes", frame.URL, body)
		}
	}

	// The listing that mints those URLs must not be naming the original
	// either, whatever the bytes happened to say.
	m, ok := cache.LoadManifest(fileDirOf(t, copied, infoHash, params, 6, "Sintel/sintel.mp4"))
	if !ok {
		t.Fatal("the copy's manifest was not readable")
	}
	for _, f := range m.Frames {
		if strings.HasPrefix(f.Path, original) {
			t.Errorf("frame %d of the copy resolves to %q, inside the original tree", f.Index, f.Path)
		}
	}
}
