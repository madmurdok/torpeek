package web

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
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

// buildHoledRun is buildCachedRun for a manifest that already has some
// frames with no path - a failed capture point, per manifest.ShiftFailed.
// buildCachedRun cannot be reused as-is: it calls writer.WriteFrame for
// every entry in m.Frames unconditionally, which would hand a failed point a
// file it never had. Here, only a frame with a non-empty Path gets one
// written; a failed point is left exactly as the caller set it up.
func buildHoledRun(t *testing.T, root, infoHash, params string, run cache.Run, m manifest.Manifest) output.Layout {
	t.Helper()

	layout := output.Layout{Root: root, InfoHash: infoHash, Params: params}
	writer, err := output.NewWriter(layout)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	for i, f := range m.Frames {
		if f.Path == "" {
			continue
		}
		path, err := writer.WriteFrame(m.File.Index, m.File.Path, f.Index, []byte("jpeg-bytes-"+m.File.Path), "jpg")
		if err != nil {
			t.Fatalf("write frame %d: %v", f.Index, err)
		}
		m.Frames[i].Path = path
	}

	if _, err := writer.WriteManifest(m.File.Index, m.File.Path, m); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := cache.SaveRun(layout.RunDir(), run); err != nil {
		t.Fatalf("save run.json: %v", err)
	}
	return layout
}

// TestFileDetailCarriesTheFailureReasonForAPointThatProducedNothing is
// TOR-118's acceptance criterion, at the half that was actually broken: the
// manifest already distinguishes a swarm-could-not-supply point
// (manifest.ShiftFailed, Error "unavailable") from a read-timeout point
// (manifest.ShiftFailed, Error "read_stalled") - verified against a real
// holed run's manifest before this test was written, where a 12-point plan
// held exactly this split. What this proves is that the distinction survives
// being read back through GET /runs/{infohash}/files/{index}, and that every
// planned point reaches the page - not just the one that produced a frame,
// which is what internal/web/listing.go's fileDetail dropped before TOR-118.
func TestFileDetailCarriesTheFailureReasonForAPointThatProducedNothing(t *testing.T) {
	root := t.TempDir()

	actual0 := int64(43700)
	m := manifest.Manifest{
		Version: manifest.Version,
		File:    manifest.File{Index: 6, Path: "Sintel/sintel.mp4", Bytes: 1 << 20, Container: "matroska"},
		Video:   manifest.Video{Codec: "h264", Width: 640, Height: 360},
		Frames: []manifest.Frame{
			{Index: 0, RequestedMS: 43700, ActualMS: &actual0, Path: "placeholder", Width: 640, Height: 360},
			{Index: 1, RequestedMS: 82646, Shift: manifest.ShiftFailed, Error: "unavailable"},
			{Index: 2, RequestedMS: 112105, Shift: manifest.ShiftFailed, Error: "read_stalled"},
		},
	}

	run := cache.Run{
		Version: cache.Version, Tool: "test", CreatedAt: time.Now(),
		InfoHash: detailHash, Name: "Sintel",
		Plan:     cache.Plan{Count: 3, Start: 0.05, End: 0.95, Profile: "min-traffic", Format: "jpeg"},
		Videos:   []cache.File{{Index: 6, Path: "Sintel/sintel.mp4", Bytes: 1 << 20}},
		Selected: []int{6}, Complete: []int{6},
	}
	buildHoledRun(t, root, detailHash, "cccc3333", run, m)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	fake := &fakeRun{}
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	detail, status := getFileDetail(t, ts.URL, detailHash, "6")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	if len(detail.Frames) != 3 {
		t.Fatalf("frames = %d, want 3 - all three planned points, not just the one that has a URL: %+v",
			len(detail.Frames), detail.Frames)
	}

	byIndex := map[int]FrameRef{}
	for _, f := range detail.Frames {
		byIndex[f.Index] = f
	}

	if got := byIndex[0]; got.URL == "" || got.Error != "" {
		t.Errorf("frame 0 (succeeded) = %+v, want a URL and no error", got)
	}

	unavailable := byIndex[1]
	if unavailable.URL != "" || unavailable.Error != "unavailable" || unavailable.TimeMS != 82646 {
		t.Errorf("frame 1 (swarm could not supply it) = %+v, want no URL, error \"unavailable\", time_ms 82646", unavailable)
	}

	stalled := byIndex[2]
	if stalled.URL != "" || stalled.Error != "read_stalled" || stalled.TimeMS != 112105 {
		t.Errorf("frame 2 (read timed out) = %+v, want no URL, error \"read_stalled\", time_ms 112105", stalled)
	}

	if unavailable.Error == stalled.Error {
		t.Fatal("the two failure causes collapsed into the same value - the exact bug TOR-118 found")
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

// TOR-111, re-sourced by TOR-179: one file's stretch of the torrent, drawn
// from where its own frames were taken rather than from a run's claim log.

// sourced builds one captured frame recording the file byte ranges it came
// from - the shape core.processFile writes (manifest.Frame.ByteRanges).
func sourced(index int, ranges ...[2]int64) manifest.Frame {
	ms := int64(index) * 1000
	return manifest.Frame{
		Index: index, RequestedMS: ms, ActualMS: &ms, Path: "frames/00.jpg",
		Width: 640, Height: 360, ByteRanges: ranges,
	}
}

// unsourced is the same captured frame written before the field existed: a
// real frame, on disk, with nothing to say about where it came from.
func unsourced(index int) manifest.Frame {
	f := sourced(index)
	f.ByteRanges = nil
	return f
}

// failed is a point that produced nothing - no frame, and so no provenance
// for one either (manifest.ShiftFailed: "Path is empty and Error says why").
func failed(index int) manifest.Frame {
	return manifest.Frame{
		Index: index, RequestedMS: int64(index) * 1000,
		Shift: manifest.ShiftFailed, Error: "unavailable",
	}
}

func TestReachOfTurnsFrameByteRangesIntoTheFilesOwnPieces(t *testing.T) {
	const piece = 256 << 10

	// A file starting 10 pieces into the torrent and spanning 20 of them, so
	// a bug that forgot the offset would show up as a shifted picture rather
	// than as an equal one.
	file := cache.File{Index: 1, Path: "b.mkv", Offset: 10 * piece, Bytes: 20 * piece}

	got := reachOf([]manifest.Frame{
		// The container header, which every point's ffprobe re-reads.
		sourced(0, [2]int64{0, piece}),
		// One byte inside a piece still costs the whole piece: that is the
		// expansion swarm.PieceRangeFor makes on the way out, run backwards.
		sourced(1, [2]int64{5 * piece, 5*piece + 1}),
		// Overlaps the frame above; the two are one stretch, not two.
		sourced(2, [2]int64{5*piece + 1, 8 * piece}),
		// The last piece of the file.
		sourced(3, [2]int64{19 * piece, 20 * piece}),
	}, file, piece)

	if got == nil {
		t.Fatal("reachOf returned nil for a set whose frames say where they came from")
	}
	if got.FirstPiece != 10 || got.Pieces != 20 || got.PieceBytes != piece {
		t.Errorf("geometry = first %d, pieces %d, bytes %d; want 10, 20, %d",
			got.FirstPiece, got.Pieces, got.PieceBytes, piece)
	}

	// Offsets from FirstPiece, not absolute indices: the drawing's origin is
	// the file, not the torrent.
	want := [][2]int{{0, 1}, {5, 8}, {19, 20}}
	if len(got.Claimed) != len(want) {
		t.Fatalf("claimed = %v, want %v", got.Claimed, want)
	}
	for i := range want {
		if got.Claimed[i] != want[i] {
			t.Fatalf("claimed = %v, want %v", got.Claimed, want)
		}
	}
	if got.ClaimedPieces != 5 {
		t.Errorf("claimed_pieces = %d, want 5 - and it must equal the ranges' own sum",
			got.ClaimedPieces)
	}

	sum := 0
	for _, r := range got.Claimed {
		sum += r[1] - r[0]
	}
	if sum != got.ClaimedPieces {
		t.Errorf("the ranges cover %d pieces but claimed_pieces says %d", sum, got.ClaimedPieces)
	}
	if got.Captured != 4 || got.Located != 4 {
		t.Errorf("captured/located = %d/%d, want 4/4 - every point produced a frame and every "+
			"frame recorded its ranges, so the coverage is the whole story",
			got.Captured, got.Located)
	}
}

// TestReachOfSaysNothingRatherThanZero is the distinction TOR-119 already had
// to make on disk and this inherits: a set with nothing recorded to reason
// from cannot say the run touched nothing, and a strip drawn from it would
// assert exactly that. The first case is the TOR-52 trap's own shape here -
// manifest.Frame.ByteRanges was added without bumping manifest.Version, so
// every manifest already on disk stays a hit and simply cannot answer.
func TestReachOfSaysNothingRatherThanZero(t *testing.T) {
	const piece = 256 << 10
	file := cache.File{Index: 0, Path: "a.mkv", Offset: 0, Bytes: 10 * piece}
	captured := []manifest.Frame{sourced(0, [2]int64{0, piece})}

	for _, tc := range []struct {
		name   string
		frames []manifest.Frame
		file   cache.File
		piece  int64
	}{
		{"a manifest written before the frames recorded where they came from",
			[]manifest.Frame{unsourced(0), unsourced(1)}, file, piece},
		{"a manifest with no captured point at all",
			[]manifest.Frame{failed(0), failed(1)}, file, piece},
		{"a manifest with no frames at all", nil, file, piece},
		{"a manifest with no piece length", captured, file, 0},
		{"a file of no length", captured, cache.File{Index: 0, Path: "a.mkv"}, piece},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reachOf(tc.frames, tc.file, tc.piece); got != nil {
				t.Errorf("reachOf = %+v, want nil - there is nothing to draw from", got)
			}
		})
	}
}

// TestReachOfLetsAFailedPointSayNothingAboutTheFile is the acceptance
// criterion's own sentence: a failed point has no frame and so no range, and
// that absence is not ignorance about the file. Counting it towards "nobody
// knows" would let one unreachable point hide what the others plainly record,
// which is the opposite mistake to the one this ticket exists to fix.
func TestReachOfLetsAFailedPointSayNothingAboutTheFile(t *testing.T) {
	const piece = 256 << 10
	file := cache.File{Index: 0, Path: "a.mkv", Offset: 0, Bytes: 10 * piece}

	got := reachOf([]manifest.Frame{
		sourced(0, [2]int64{0, piece}),
		failed(1),
		sourced(2, [2]int64{8 * piece, 9 * piece}),
	}, file, piece)

	if got == nil {
		t.Fatal("reachOf = nil; two frames said where they came from, so the answer is not silence")
	}
	if got.Captured != 2 || got.Located != 2 {
		t.Errorf("captured/located = %d/%d, want 2/2 - the failed point is not a point that "+
			"could have answered, so it belongs in neither count", got.Captured, got.Located)
	}
	want := [][2]int{{0, 1}, {8, 9}}
	if len(got.Claimed) != len(want) || got.Claimed[0] != want[0] || got.Claimed[1] != want[1] {
		t.Errorf("claimed = %v, want %v", got.Claimed, want)
	}
}

// TestReachOfSaysHowManyFramesCouldAnswer is the state a top-up onto an older
// set actually lands in: the points it re-took record where they came from and
// the ones it reused, written before the field existed, do not. The union is
// then a floor rather than an answer, and the counts are what let the caption
// say so instead of presenting a partial coverage as the whole reach.
func TestReachOfSaysHowManyFramesCouldAnswer(t *testing.T) {
	const piece = 256 << 10
	file := cache.File{Index: 0, Path: "a.mkv", Offset: 0, Bytes: 10 * piece}

	got := reachOf([]manifest.Frame{
		unsourced(0),
		unsourced(1),
		sourced(2, [2]int64{4 * piece, 5 * piece}),
	}, file, piece)

	if got == nil {
		t.Fatal("reachOf = nil; one frame did say where it came from")
	}
	if got.Captured != 3 || got.Located != 1 {
		t.Errorf("captured/located = %d/%d, want 3/1", got.Captured, got.Located)
	}
	if got.ClaimedPieces != 1 {
		t.Errorf("claimed_pieces = %d, want 1 - only the frame that can answer is drawn, and "+
			"the counts are what admit the rest is unknown", got.ClaimedPieces)
	}
}

// TestReachOfHandlesAFileEndingMidPiece is the off-by-one this arithmetic
// invites: a file's last byte usually sits inside a piece it shares with the
// next file, and that piece belongs to both.
func TestReachOfHandlesAFileEndingMidPiece(t *testing.T) {
	const piece = 256 << 10
	// Starts at the very start of piece 0 and ends one byte into piece 3.
	file := cache.File{Index: 0, Path: "a.mkv", Offset: 0, Bytes: 3*piece + 1}

	got := reachOf([]manifest.Frame{sourced(0, [2]int64{0, 3*piece + 1})}, file, piece)
	if got == nil {
		t.Fatal("reachOf returned nil")
	}
	if got.Pieces != 4 {
		t.Errorf("pieces = %d, want 4 - the file's last byte is in piece 3", got.Pieces)
	}
	if got.ClaimedPieces != 4 {
		t.Errorf("claimed_pieces = %d, want 4 - the frame came from all of the file", got.ClaimedPieces)
	}
}

// TestReachOfKeepsARecordThatLandsNowhereApartFromNoRecord is the same
// silence-against-zero line one step further in: here the frames DO record
// ranges and none of them lands inside this file, which no run of ours writes
// but a record off disk could hold. That is an answer - none of it came from
// here - and a strip of untouched blocks is the truthful drawing of it.
func TestReachOfKeepsARecordThatLandsNowhereApartFromNoRecord(t *testing.T) {
	const piece = 256 << 10
	file := cache.File{Index: 0, Path: "a.mkv", Offset: 0, Bytes: 5 * piece}

	got := reachOf([]manifest.Frame{sourced(0, [2]int64{9 * piece, 10 * piece})}, file, piece)
	if got == nil {
		t.Fatal("reachOf = nil; the frame said where it came from, so the answer is zero, not silence")
	}
	if got.Pieces != 5 || len(got.Claimed) != 0 || got.ClaimedPieces != 0 {
		t.Errorf("reach = %+v, want 5 pieces and none of them drawn", got)
	}
	if got.Located != 1 {
		t.Errorf("located = %d, want 1 - the frame answered, the answer was just outside", got.Located)
	}
}

// TestTheStripIsDrawnFromTheFramesNotTheLastRunsClaimLog is TOR-179's own
// regression, end to end through the endpoint the page actually calls.
//
// The set on disk is exactly the shape a TOP-UP leaves behind (TOR-152): four
// frames spread across the file, and a run.json whose claim log holds the one
// piece at the very end that the last run needed to fill the gap. Before this,
// the strip was drawn from that log and showed a block at the end of a file
// whose frames plainly cover all of it - the disagreement the owner reported.
// It must now report the frames' own coverage, and the stale log must make no
// difference to it.
func TestTheStripIsDrawnFromTheFramesNotTheLastRunsClaimLog(t *testing.T) {
	const piece = 256 << 10
	const bytes = 20 * piece

	root := t.TempDir()
	m := manifest.Manifest{
		Version: manifest.Version,
		Torrent: manifest.Torrent{InfoHash: detailHash, PieceLength: piece},
		File:    manifest.File{Index: 0, Path: "Lupin/ep.mkv", Bytes: bytes, Container: "matroska"},
		Video:   manifest.Video{Codec: "h264", Width: 640, Height: 360},
		Frames: []manifest.Frame{
			sourced(0, [2]int64{0, piece}),
			sourced(1, [2]int64{5 * piece, 6 * piece}),
			sourced(2, [2]int64{10 * piece, 11 * piece}),
			sourced(3, [2]int64{19 * piece, 20 * piece}),
		},
	}
	run := cache.Run{
		Version: cache.Version, Tool: "test", InfoHash: detailHash, Name: "Lupin",
		Plan:     cache.Plan{Count: 4, Profile: "min-traffic", Format: "jpeg"},
		Videos:   []cache.File{{Index: 0, Path: "Lupin/ep.mkv", Bytes: bytes, Offset: 0}},
		Selected: []int{0},
		Complete: []int{0},
		// The last run's own traversal: it filled one point at the end, so
		// this is all it claimed. Deliberately disjoint from three of the four
		// frames, so drawing from it cannot be mistaken for drawing from them.
		Claimed: [][2]int{{19, 20}},
	}
	buildCachedRun(t, root, detailHash, "aaaa1111", run, m)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	fake := &fakeRun{}
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	detail, status := getFileDetail(t, ts.URL, detailHash, "0")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	if len(detail.Sets) != 1 || detail.Sets[0].Reach == nil {
		t.Fatalf("sets = %+v, want one carrying a reach", detail.Sets)
	}

	got := detail.Sets[0].Reach
	want := [][2]int{{0, 1}, {5, 6}, {10, 11}, {19, 20}}
	if len(got.Claimed) != len(want) {
		t.Fatalf("claimed = %v, want %v - the strip is drawn from run.json's stale claim log, "+
			"not from the frames", got.Claimed, want)
	}
	for i := range want {
		if got.Claimed[i] != want[i] {
			t.Fatalf("claimed = %v, want %v", got.Claimed, want)
		}
	}
	if got.ClaimedPieces != 4 {
		t.Errorf("claimed_pieces = %d, want 4 - one per frame", got.ClaimedPieces)
	}
	if got.Captured != 4 || got.Located != 4 {
		t.Errorf("captured/located = %d/%d, want 4/4", got.Captured, got.Located)
	}
}

// TestAManifestWithNoFrameProvenanceSendsNoReachAtAll is the payload half of
// "absent is unknown": the set is real and servable, its frames are on disk,
// and it simply predates manifest.Frame.ByteRanges. The endpoint must omit
// reach entirely rather than send one claiming zero coverage, because that is
// the only shape the page can tell apart from a run that touched nothing
// (app.js's renderReach hatches the strip on it).
func TestAManifestWithNoFrameProvenanceSendsNoReachAtAll(t *testing.T) {
	const piece = 256 << 10

	root := t.TempDir()
	m := manifest.Manifest{
		Version: manifest.Version,
		Torrent: manifest.Torrent{InfoHash: detailHash, PieceLength: piece},
		File:    manifest.File{Index: 0, Path: "Lupin/ep.mkv", Bytes: 20 * piece, Container: "matroska"},
		Video:   manifest.Video{Codec: "h264", Width: 640, Height: 360},
		Frames:  []manifest.Frame{unsourced(0), unsourced(1)},
	}
	run := cache.Run{
		Version: cache.Version, Tool: "test", InfoHash: detailHash, Name: "Lupin",
		Plan:     cache.Plan{Count: 2, Profile: "min-traffic", Format: "jpeg"},
		Videos:   []cache.File{{Index: 0, Path: "Lupin/ep.mkv", Bytes: 20 * piece, Offset: 0}},
		Selected: []int{0},
		Complete: []int{0},
	}
	buildCachedRun(t, root, detailHash, "aaaa1111", run, m)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	fake := &fakeRun{}
	_, ts := newTestServerWithConfig(t, cfg, fake.runner)

	resp := get(t, ts.URL, "/runs/"+detailHash+"/files/0")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), `"reach"`) {
		t.Errorf("the payload carries a reach for a set that cannot say where its frames "+
			"came from; a zeroed one would draw as a run that touched none of the file:\n%s", body)
	}
}
