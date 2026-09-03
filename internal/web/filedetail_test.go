package web

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
)

// framesAt is a one-file manifest whose frames sit at the given timecodes -
// the shape TOR-69 cares about, where oneFrameManifest is about a run
// existing at all.
func framesAt(index int, path string, times ...int64) manifest.Manifest {
	m := manifest.Manifest{
		Version: manifest.Version,
		File:    manifest.File{Index: index, Path: path, Bytes: 1 << 20, Container: "matroska"},
		Video:   manifest.Video{Codec: "h264", Width: 640, Height: 360},
	}
	for i, at := range times {
		ms := at
		m.Frames = append(m.Frames, manifest.Frame{
			Index: i, RequestedMS: ms, ActualMS: &ms, Path: "placeholder",
			Width: 640, Height: 360,
		})
	}
	return m
}

// writeSet writes one result set for one file: a run.json whose plan says
// count, and that file's manifest with real frame files underneath. Two
// calls with different params produce the sibling directories a
// regeneration at another count leaves behind (TOR-68).
func writeSet(t *testing.T, root, infoHash, params string, count, index int, path string, times ...int64) {
	t.Helper()

	run := cache.Run{
		Version:   cache.Version,
		Tool:      "test",
		CreatedAt: time.Now(),
		InfoHash:  infoHash,
		Name:      "Season 1",
		Plan:      cache.Plan{Count: count, Start: 0.05, End: 0.95, Profile: "min-traffic", Format: "jpeg"},
		Videos:    []cache.File{{Index: index, Path: path, Bytes: 1 << 20}},
		Selected:  []int{index},
		Complete:  []int{index},
	}
	buildCachedRun(t, root, infoHash, params, run, framesAt(index, path, times...))
}

// getFileDetail calls the endpoint and decodes it.
func getFileDetail(t *testing.T, base, infoHash string, index string) (FileDetail, int) {
	t.Helper()

	resp := get(t, base, "/runs/"+infoHash+"/files/"+index)
	if resp.StatusCode != http.StatusOK {
		return FileDetail{}, resp.StatusCode
	}

	var body struct {
		File FileDetail `json:"file"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode file detail: %v", err)
	}
	return body.File, resp.StatusCode
}

// fileDirOf is where one file's results live under a set - the same
// composition output.Layout makes, used here to reach a frame on disk.
func fileDirOf(t *testing.T, root, infoHash, params string, index int, path string) string {
	t.Helper()

	return output.Layout{Root: root, InfoHash: infoHash, Params: params}.FileDir(index, path)
}

func timecodes(frames []FrameRef) []int64 {
	out := make([]int64, 0, len(frames))
	for _, f := range frames {
		out = append(out, f.TimeMS)
	}
	return out
}

const detailHash = "e4d37e62d14ba96d29b9e760148803b458aee5b6"

// TestFileDetailMergesEveryResultSetOrderedByTime is TOR-69's acceptance
// criterion: a file captured twice with different counts comes back as ONE
// list ordered by timecode. The two sets deliberately share both window
// edges, because frames.Plan pins the first and last point to the window -
// so every regeneration coincides with the first set there, and those
// frames must appear once rather than twice.
//
// The server is fresh and nothing was ever announced on its event stream,
// which is the whole reason this request exists: a sibling set's frames were
// never named by any event, so they are unreachable through the socket.
func TestFileDetailMergesEveryResultSetOrderedByTime(t *testing.T) {
	root := t.TempDir()
	writeSet(t, root, detailHash, "aaaa1111", 4, 6, "Sintel/sintel.mp4", 43700, 308900, 576300, 833600)
	writeSet(t, root, detailHash, "bbbb2222", 6, 6, "Sintel/sintel.mp4", 43700, 203100, 361700, 523400, 710000, 833600)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	fake := &fakeRun{}
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	detail, status := getFileDetail(t, ts.URL, detailHash, "6")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}

	want := []int64{43700, 203100, 308900, 361700, 523400, 576300, 710000, 833600}
	got := timecodes(detail.Frames)
	if len(got) != len(want) {
		t.Fatalf("timecodes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("timecodes = %v, want %v", got, want)
		}
	}

	if detail.Path != "Sintel/sintel.mp4" || detail.Index != 6 {
		t.Errorf("file = %d %q, want 6 Sintel/sintel.mp4", detail.Index, detail.Path)
	}
	if len(detail.Sets) != 2 {
		t.Fatalf("sets = %+v, want both result sets", detail.Sets)
	}
	counts := map[int]int{detail.Sets[0].Count: detail.Sets[0].Frames, detail.Sets[1].Count: detail.Sets[1].Frames}
	if counts[4] != 4 || counts[6] != 6 {
		t.Errorf("sets = %+v, want a 4-frame and a 6-frame set", detail.Sets)
	}

	// Every URL has to resolve on this same server, with no run ever having
	// been pumped through it - the ids are minted by the request itself.
	for _, f := range detail.Frames {
		if resp := get(t, ts.URL, "/"+f.URL); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", f.URL, resp.StatusCode)
		}
	}
}

// TestFileDetailLeavesOutAFrameGoneFromDisk is where this list deliberately
// parts ways with cache.Usable, which refuses a whole manifest over one
// missing frame (and so refuses the whole run - see
// TestReopeningARunWithDeletedFramesFails). Showing what is left is not a
// cache decision, and a person who deleted one frame should still see the
// others.
func TestFileDetailLeavesOutAFrameGoneFromDisk(t *testing.T) {
	root := t.TempDir()
	writeSet(t, root, detailHash, "aaaa1111", 4, 6, "Sintel/sintel.mp4", 43700, 308900, 576300, 833600)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	fake := &fakeRun{}
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	before, _ := getFileDetail(t, ts.URL, detailHash, "6")
	if len(before.Frames) != 4 {
		t.Fatalf("timecodes = %v, want four frames before the delete", timecodes(before.Frames))
	}

	m, ok := cache.LoadManifest(fileDirOf(t, root, detailHash, "aaaa1111", 6, "Sintel/sintel.mp4"))
	if !ok {
		t.Fatal("manifest just written is not readable")
	}
	if err := os.Remove(m.Frames[1].Path); err != nil {
		t.Fatalf("remove a frame: %v", err)
	}

	after, status := getFileDetail(t, ts.URL, detailHash, "6")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200 - the remaining frames are still showable", status)
	}
	want := []int64{43700, 576300, 833600}
	got := timecodes(after.Frames)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("timecodes = %v, want %v - the deleted frame gone, the rest kept", got, want)
	}
}

// TestFileDetailIsNotFoundForWhatDoesNotExist covers the three ways a
// request can name nothing, including the one that matters for safety: the
// infohash is the only string a request puts into a path here (everywhere
// else this package serves paths the event stream named - see files.go), so
// anything but a hex digest is refused before it reaches the filesystem.
func TestFileDetailIsNotFoundForWhatDoesNotExist(t *testing.T) {
	root := t.TempDir()
	writeSet(t, root, detailHash, "aaaa1111", 4, 6, "Sintel/sintel.mp4", 43700, 833600)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	fake := &fakeRun{}
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	cases := []struct{ name, hash, index string }{
		{"an index no set lists", detailHash, "3"},
		{"an index that is not a number", detailHash, "not-a-number"},
		{"a negative index", detailHash, "-1"},
		{"an unknown torrent", "0000000000000000000000000000000000000000", "6"},
		{"an infohash that is not hex", "not-a-hex-digest-not-a-hex-digest-not-ab", "6"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, status := getFileDetail(t, ts.URL, c.hash, c.index); status != http.StatusNotFound {
				t.Errorf("status %d, want 404", status)
			}
		})
	}
}

// TestValidInfoHashRefusesAnythingButADigest pins the guard itself, since
// the HTTP test above can only reach it through a router that already
// cleans some paths of its own accord.
func TestValidInfoHashRefusesAnythingButADigest(t *testing.T) {
	if !validInfoHash(detailHash) {
		t.Error("a real infohash was refused")
	}
	for _, bad := range []string{
		"", "e4d37e62", "../../../../../../../../../../etc/passwd",
		"E4D37E62D14BA96D29B9E760148803B458AEE5B6", // upper case is not what run dirs are named
		"e4d37e62d14ba96d29b9e760148803b458aee5b/", "e4d37e62d14ba96d29b9e760148803b458aee5b.",
	} {
		if validInfoHash(bad) {
			t.Errorf("%q was accepted as an infohash", bad)
		}
	}
}
