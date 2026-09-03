package output

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testLayout(t *testing.T) Layout {
	t.Helper()
	return Layout{
		Root:     t.TempDir(),
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
		Params:   "n20-w5-95-mintraffic",
	}
}

func TestWriteFrameLandsWhereTheLayoutSays(t *testing.T) {
	layout := testLayout(t)
	w, err := NewWriter(layout)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	path, err := w.WriteFrame(0, "Show/Season 1/Episode 01.mkv", 7, []byte("jpeg-bytes"), "jpg")
	if err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	want := filepath.Join(layout.RunDir(), "00-episode-01", "frames", "007.jpg")
	if path != want {
		t.Errorf("frame written to %q, want %q", path, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read frame back: %v", err)
	}
	if string(data) != "jpeg-bytes" {
		t.Errorf("frame contents = %q, want %q", data, "jpeg-bytes")
	}
}

// TestFramesAppearImmediately backs the requirement that a UI fills in as the
// run goes and a cancelled run keeps what it had: each frame must be readable
// before the next one is written.
func TestFramesAppearImmediately(t *testing.T) {
	w, err := NewWriter(testLayout(t))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := w.WriteFrame(0, "movie.mkv", i, []byte{byte(i)}, "jpg"); err != nil {
			t.Fatalf("WriteFrame %d: %v", i, err)
		}

		entries, err := os.ReadDir(w.Layout().FramesDir(0, "movie.mkv"))
		if err != nil {
			t.Fatalf("read frames dir: %v", err)
		}
		if len(entries) != i+1 {
			t.Fatalf("after writing %d frames the directory holds %d", i+1, len(entries))
		}
	}
}

// TestWriteIsAtomic: the web UI watches this directory, so a reader must never
// find a half-written image, and a run killed mid-write must not leave a
// corrupt frame that resume would mistake for a real one.
func TestWriteIsAtomic(t *testing.T) {
	w, err := NewWriter(testLayout(t))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	dir := w.Layout().FramesDir(0, "movie.mkv")
	payload := make([]byte, 1<<20)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// A reader scanning the directory throughout the write.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue // directory may not exist yet
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".") {
					continue // temporary files are hidden by design
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				if info.Size() != int64(len(payload)) {
					t.Errorf("reader saw %s at %d bytes, want the full %d - the write was not atomic",
						e.Name(), info.Size(), len(payload))
					return
				}
			}
		}
	}()

	for i := 0; i < 20; i++ {
		if _, err := w.WriteFrame(0, "movie.mkv", i, payload, "jpg"); err != nil {
			t.Fatalf("WriteFrame %d: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestWriteFrameRejectsEmptyData(t *testing.T) {
	w, err := NewWriter(testLayout(t))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := w.WriteFrame(0, "movie.mkv", 0, nil, "jpg"); err == nil {
		t.Error("WriteFrame accepted an empty frame")
	}
}

func TestNoTemporaryFilesLeftBehind(t *testing.T) {
	w, err := NewWriter(testLayout(t))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	if _, err := w.WriteFrame(0, "movie.mkv", 0, []byte("x"), "jpg"); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	entries, err := os.ReadDir(w.Layout().FramesDir(0, "movie.mkv"))
	if err != nil {
		t.Fatalf("read frames dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temporary file %s survived a successful write", e.Name())
		}
	}
}

func TestWriteFileForSheetsAndManifests(t *testing.T) {
	w, err := NewWriter(testLayout(t))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	path, err := w.WriteFile(1, "movie.mkv", "manifest.json", []byte(`{"frames":[]}`))
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if filepath.Base(path) != "manifest.json" {
		t.Errorf("wrote %q, want it named manifest.json", path)
	}
	if dir := filepath.Base(filepath.Dir(path)); dir != "01-movie" {
		t.Errorf("manifest sits in %q, want the file's own directory", dir)
	}
}

func TestFileSlug(t *testing.T) {
	cases := []struct {
		index int
		path  string
		want  string
	}{
		{index: 0, path: "movie.mkv", want: "00-movie"},
		{index: 1, path: "Show/Season 1/Episode 01.mkv", want: "01-episode-01"},
		{index: 2, path: "Some.Movie.2024.1080p.BluRay.x264.mkv", want: "02-some-movie-2024-1080p-bluray-x264"},
		{index: 3, path: `Windows\Path\file.mp4`, want: "03-file"},
		{index: 4, path: "../../etc/passwd.mkv", want: "04-passwd"},
		{index: 5, path: "Фильм.mkv", want: "05-фильм"},
		{index: 6, path: "???.mkv", want: "06-file"},
		{index: 7, path: "  spaced   out  .mkv", want: "07-spaced-out"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := FileSlug(tc.index, tc.path); got != tc.want {
				t.Errorf("FileSlug(%d, %q) = %q, want %q", tc.index, tc.path, got, tc.want)
			}
		})
	}
}

// TestSlugCannotEscapeTheRunDirectory: torrent paths are attacker-controlled
// in the sense that nobody vetted them, so a name must never become a path.
func TestSlugCannotEscapeTheRunDirectory(t *testing.T) {
	layout := testLayout(t)
	w, err := NewWriter(layout)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	for _, hostile := range []string{
		"../../../../tmp/escape.mkv",
		"/etc/passwd.mkv",
		`..\..\windows\system32\evil.mkv`,
	} {
		path, err := w.WriteFrame(0, hostile, 0, []byte("x"), "jpg")
		if err != nil {
			t.Fatalf("WriteFrame(%q): %v", hostile, err)
		}

		rel, err := filepath.Rel(layout.RunDir(), path)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("path %q for %q escaped the run directory", path, hostile)
		}
	}
}

func TestNewWriterRejectsEmptyRoot(t *testing.T) {
	if _, err := NewWriter(Layout{InfoHash: "abc", Params: "p"}); err == nil {
		t.Error("NewWriter accepted an empty root")
	}
}

// TestWriteTorrentLandsInTheRunDirectory is the per-run half of the layout:
// the .torrent belongs beside run.json, not inside any one video file's
// directory, and it is named after the infohash so it stays meaningful once
// it is copied out of here - into a browser's downloads folder, or into a
// torrent client's watch directory (TOR-73).
func TestWriteTorrentLandsInTheRunDirectory(t *testing.T) {
	layout := testLayout(t)
	w, err := NewWriter(layout)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	path, err := w.WriteTorrent([]byte("d8:announce0:e"))
	if err != nil {
		t.Fatalf("WriteTorrent: %v", err)
	}

	want := filepath.Join(layout.RunDir(), layout.InfoHash+".torrent")
	if path != want {
		t.Errorf("torrent written to %q, want %q", path, want)
	}
	if path != layout.TorrentPath() {
		t.Errorf("WriteTorrent put the file at %q but Layout.TorrentPath says %q - "+
			"a reader looking for it would not find it", path, layout.TorrentPath())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the torrent back: %v", err)
	}
	if string(data) != "d8:announce0:e" {
		t.Errorf("torrent contents = %q, want %q", data, "d8:announce0:e")
	}

	// The same guarantee every other write here makes, and it matters more
	// for this one: the UI hands this file to a torrent client, which will
	// refuse a half-written one.
	entries, err := os.ReadDir(layout.RunDir())
	if err != nil {
		t.Fatalf("read the run directory: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temporary file %q left behind in the run directory", e.Name())
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the torrent: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("torrent mode is %v, want 0644 - it is written for someone else to read", info.Mode().Perm())
	}
}

// TestWriteTorrentRejectsEmptyData: an empty file would still be a file, and
// a run directory holding a zero-byte .torrent is worse than one holding
// none - the UI would offer a link to something no client can load.
func TestWriteTorrentRejectsEmptyData(t *testing.T) {
	w, err := NewWriter(testLayout(t))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := w.WriteTorrent(nil); err == nil {
		t.Fatal("WriteTorrent accepted an empty torrent")
	}
}
