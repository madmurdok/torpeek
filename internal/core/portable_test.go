package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
)

// This file is TOR-60's: a results tree is a library, and a library that
// cannot be moved, backed up and restored, or synced between a laptop and a
// seedbox is a different and worse thing than one that can.
//
// Every other test in this package builds its fixture in place, where an
// absolute path recorded in a manifest is correct by construction and nothing
// can go wrong. These build one and then MOVE it, which is the only way the
// question is asked at all.

// copyTree copies a directory recursively, the way somebody would copy a
// results directory to another disk - the original left exactly where it was.
func copyTree(t *testing.T, from, to string) {
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

// rewriteFrames replaces every frame file under a results tree with content
// that says which tree it belongs to, so a test can prove WHICH copy answered
// rather than infer it from a path.
func rewriteFrames(t *testing.T, root, content string) int {
	t.Helper()

	written := 0
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".jpg" || filepath.Base(filepath.Dir(path)) != "frames" {
			return nil
		}
		written++
		return os.WriteFile(path, []byte(content), 0o600)
	}); err != nil {
		t.Fatalf("rewrite the frames under %s: %v", root, err)
	}
	if written == 0 {
		t.Fatalf("no frames found under %s", root)
	}
	return written
}

// replayedFrames replays a run and returns the frame paths it published,
// failing the test if the replay did not finish whole.
func replayedFrames(t *testing.T, root, infoHash, params string) []string {
	t.Helper()

	var (
		paths  []string
		done   bool
		failed []string
	)
	for _, ev := range collect(t, NewEngine(ffmpeg.Tools{}).Replay(root, infoHash, params)) {
		switch e := ev.(type) {
		case FrameReady:
			paths = append(paths, e.Path)
		case Done:
			done = true
		case Failed:
			failed = append(failed, e.Err.Error())
		}
	}
	if len(failed) > 0 {
		t.Fatalf("the replay failed: %s", strings.Join(failed, "; "))
	}
	if !done {
		t.Fatal("the replay never finished")
	}
	return paths
}

// portableRun is a small finished run on disk: three frames of one file, the
// record that says it came out whole.
func portableRun(t *testing.T, root, infoHash, params string) output.Layout {
	t.Helper()

	return buildCachedRun(t, root, infoHash, params, cache.Run{
		Version:  cache.Version,
		Tool:     "0.8.0",
		InfoHash: infoHash,
		Name:     "Moved Around",
		Videos:   []cache.File{{Index: 0, Path: "season/episode-1.mkv", Bytes: 1 << 20}},
		Selected: []int{0},
		Complete: []int{0},
	}, map[int]manifest.Manifest{0: fileManifest(0, "season/episode-1.mkv", 3)})
}

// TestManifestOnDiskRecordsNoPathOutsideItsOwnDirectory is the writer's half
// of TOR-60, and the cheapest one to state: whatever a run holds in memory,
// what reaches disk may not name the directory the run happened to be in.
func TestManifestOnDiskRecordsNoPathOutsideItsOwnDirectory(t *testing.T) {
	const (
		infoHash = "aa11bb22cc33dd44ee55ff6677889900aabbccdd"
		params   = "00112233445566aa"
	)
	root := t.TempDir()
	layout := portableRun(t, root, infoHash, params)

	fileDir := layout.FileDir(0, "season/episode-1.mkv")
	raw, err := os.ReadFile(filepath.Join(fileDir, manifest.Name))
	if err != nil {
		t.Fatalf("read the manifest back: %v", err)
	}
	if strings.Contains(string(raw), root) {
		t.Errorf("the manifest names the directory it was written in:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"path": "frames/000.jpg"`) {
		t.Errorf("frame 0 is not recorded relative to its manifest:\n%s", raw)
	}
}

// TestReplayServesAMovedRun is the acceptance criterion: a results tree moved
// to a different directory can be reopened, serving frames from its new
// location, with nothing left at the old one.
func TestReplayServesAMovedRun(t *testing.T) {
	const (
		infoHash = "bb11cc22dd33ee44ff5500667788990011223344"
		params   = "00112233445566bb"
	)
	base := t.TempDir()
	from, to := filepath.Join(base, "from"), filepath.Join(base, "to")

	portableRun(t, from, infoHash, params)
	if err := os.Rename(from, to); err != nil {
		t.Fatalf("move the results tree: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatalf("the original is still there, so this proves nothing: %v", err)
	}

	paths := replayedFrames(t, to, infoHash, params)
	if len(paths) != 3 {
		t.Fatalf("replayed %d frames, want 3", len(paths))
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, to) {
			t.Errorf("frame served from %q, want a path under the tree that was opened, %q", path, to)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("frame %q is not there: %v", path, err)
		}
	}
}

// TestReplayOfACopyNeverServesTheOriginal is the failure the ticket calls
// worse than the loud one, because it is silent: while the original directory
// still exists, reopening a COPY used to succeed and serve the frames from the
// original location. The person is looking at files they did not point the
// tool at, and would not know.
//
// The two trees are identical except for the bytes inside the frames, so which
// one answered is a fact read off disk rather than something inferred from a
// path.
func TestReplayOfACopyNeverServesTheOriginal(t *testing.T) {
	const (
		infoHash = "cc11dd22ee33ff445566778899001122334455aa"
		params   = "00112233445566cc"
	)
	base := t.TempDir()
	original, copied := filepath.Join(base, "original"), filepath.Join(base, "copy")

	portableRun(t, original, infoHash, params)
	copyTree(t, original, copied)
	rewriteFrames(t, original, "the original")
	rewriteFrames(t, copied, "the copy")

	for _, path := range replayedFrames(t, copied, infoHash, params) {
		if strings.HasPrefix(path, original) {
			t.Fatalf("the copy served %q, a frame of the original tree", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the served frame %s: %v", path, err)
		}
		if string(data) != "the copy" {
			t.Errorf("the frame at %s reads %q, want the copy's own bytes", path, data)
		}
	}

	// And the copy stands on its own once the original is gone, which is the
	// loud half of the same bug: the listing said the run was intact and
	// reopening it answered failed.
	if err := os.RemoveAll(original); err != nil {
		t.Fatalf("remove the original: %v", err)
	}
	if paths := replayedFrames(t, copied, infoHash, params); len(paths) != 3 {
		t.Fatalf("replayed %d frames from the copy once the original was deleted, want 3", len(paths))
	}
}

// TestDeleteFrameInAMovedRun: a moved run is not read-only. DeleteFrame
// refuses a record pointing outside the run it was addressed through
// (within), and that check has to keep saying yes when the run is opened
// through a directory it was not captured in.
func TestDeleteFrameInAMovedRun(t *testing.T) {
	const (
		infoHash = "dd11ee22ff33445566778899001122334455aabb"
		params   = "00112233445566dd"
	)
	base := t.TempDir()
	from, to := filepath.Join(base, "from"), filepath.Join(base, "to")

	portableRun(t, from, infoHash, params)
	if err := os.Rename(from, to); err != nil {
		t.Fatalf("move the results tree: %v", err)
	}

	if err := DeleteFrame(to, infoHash, params, 0, 1); err != nil {
		t.Fatalf("delete a frame from a moved run: %v", err)
	}
	if paths := replayedFrames(t, to, infoHash, params); len(paths) != 2 {
		t.Fatalf("replayed %d frames after the delete, want 2", len(paths))
	}
}

// manifest07Shape and run07Shape are a REAL result written by 0.7.0 - captured
// from a run of that code against a local swarm, not typed out from the
// current structs - so the compatibility they prove is with what is actually
// on people's disks rather than with this build's own idea of an old file.
//
// The frame paths are absolute, which is what every release before 0.8.0
// recorded, and they name a directory that no longer exists on any machine.
// That is the point: the run must still open, out of whatever directory it is
// found in.
const manifest07Shape = `{
  "version": 1,
  "tool": "0.7.0",
  "created_at": "2026-09-03T21:24:55.101231Z",
  "torrent": {
    "infohash": "492c5104523bc34d8add0781dfcb749f51d79775",
    "name": "001",
    "piece_length": 262144,
    "private": false,
    "peers": 0,
    "seeds": 0,
    "availability": [
      0,
      0
    ]
  },
  "file": {
    "index": 0,
    "path": "001/episode-1.mkv",
    "bytes": 411653,
    "duration_ms": 20000,
    "container": "matroska,webm"
  },
  "video": {
    "codec": "h264",
    "profile": "High",
    "width": 640,
    "height": 360,
    "fps": 25,
    "bit_rate": 164661,
    "bits_per_pixel": 0.028586979166666665
  },
  "audio": [],
  "subtitles": [],
  "frames": [
    {
      "index": 0,
      "requested_ms": 2000,
      "actual_ms": 2000,
      "path": "/var/folders/ps/r478n82d1zb82phf7k1dsy6w0000gp/T/TestCaptureRealManifestFixture2958625297/003/492c5104523bc34d8add0781dfcb749f51d79775/1f34641acf26018c/00-episode-1/frames/000.jpg",
      "shift": "",
      "width": 640,
      "height": 360,
      "error": ""
    },
    {
      "index": 1,
      "requested_ms": 10000,
      "actual_ms": 10000,
      "path": "/var/folders/ps/r478n82d1zb82phf7k1dsy6w0000gp/T/TestCaptureRealManifestFixture2958625297/003/492c5104523bc34d8add0781dfcb749f51d79775/1f34641acf26018c/00-episode-1/frames/001.jpg",
      "shift": "",
      "width": 640,
      "height": 360,
      "error": ""
    },
    {
      "index": 2,
      "requested_ms": 18000,
      "actual_ms": 18000,
      "path": "/var/folders/ps/r478n82d1zb82phf7k1dsy6w0000gp/T/TestCaptureRealManifestFixture2958625297/003/492c5104523bc34d8add0781dfcb749f51d79775/1f34641acf26018c/00-episode-1/frames/002.jpg",
      "shift": "",
      "width": 640,
      "height": 360,
      "error": ""
    }
  ],
  "cost": {
    "downloaded_bytes": 411666,
    "elapsed_ms": 2863,
    "limit_bytes": 67108864,
    "limit_ms": 240000,
    "limit_hit": "",
    "sequential": false
  }
}`

const run07Shape = `{
  "version": 1,
  "tool": "0.7.0",
  "created_at": "2026-09-03T21:24:55.12613Z",
  "source": "/var/folders/ps/r478n82d1zb82phf7k1dsy6w0000gp/T/TestCaptureRealManifestFixture2958625297/003/492c5104523bc34d8add0781dfcb749f51d79775/1f34641acf26018c/492c5104523bc34d8add0781dfcb749f51d79775.torrent",
  "infohash": "492c5104523bc34d8add0781dfcb749f51d79775",
  "name": "001",
  "private": false,
  "plan": {
    "count": 3,
    "start": 0.1,
    "end": 0.9,
    "profile": "min-traffic",
    "format": "jpeg",
    "sequential": false
  },
  "videos": [
    {
      "index": 0,
      "path": "001/episode-1.mkv",
      "bytes": 411653,
      "offset": 0
    }
  ],
  "selected": [
    0
  ],
  "complete": [
    0
  ]
}`

const (
	// The infohash and params directory the captured run actually lived in,
	// and the file it was over. A fixture laid out anywhere else than where
	// its own record says would not be the same test.
	sevenHash   = "492c5104523bc34d8add0781dfcb749f51d79775"
	sevenParams = "1f34641acf26018c"
	sevenFile   = "001/episode-1.mkv"
)

// TestReplayOfARunWrittenByAnEarlierRelease is the other half of the
// acceptance criterion, and the trap TOR-52 left behind: manifest.Version is
// checked the way cache.Version is, so anything that made an older manifest
// unreadable would silently turn every result already on disk into a miss.
// A 0.7.0 manifest holds absolute paths; it must still load and replay - and,
// since the directory it names is not there, it must do so out of the
// directory it is found in.
func TestReplayOfARunWrittenByAnEarlierRelease(t *testing.T) {
	if !strings.Contains(manifest07Shape, `"tool": "0.7.0"`) {
		t.Fatal("the fixture is no longer a 0.7.0 manifest - it must stay the captured one")
	}

	root := t.TempDir()
	layout := output.Layout{Root: root, InfoHash: sevenHash, Params: sevenParams}
	if err := os.MkdirAll(layout.RunDir(), 0o755); err != nil {
		t.Fatalf("create the run directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.RunDir(), cache.Name), []byte(run07Shape), 0o600); err != nil {
		t.Fatalf("write the 0.7.0 run record: %v", err)
	}

	fileDir := layout.FileDir(0, sevenFile)
	if err := os.MkdirAll(filepath.Join(fileDir, "frames"), 0o755); err != nil {
		t.Fatalf("create the frames directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, manifest.Name), []byte(manifest07Shape), 0o600); err != nil {
		t.Fatalf("write the 0.7.0 manifest: %v", err)
	}
	for i := 0; i < 3; i++ {
		name := filepath.Join(fileDir, "frames", []string{"000.jpg", "001.jpg", "002.jpg"}[i])
		if err := os.WriteFile(name, []byte("jpeg-bytes"), 0o600); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
	}

	paths := replayedFrames(t, root, sevenHash, sevenParams)
	if len(paths) != 3 {
		t.Fatalf("replayed %d frames of a 0.7.0 run, want 3", len(paths))
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, root) {
			t.Errorf("a 0.7.0 run served %q, outside the tree it was opened from (%q)", path, root)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("frame %q is not there: %v", path, err)
		}
	}
}
