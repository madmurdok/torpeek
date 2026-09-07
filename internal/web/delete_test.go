package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/output"
)

// deleteParams is a result set spelled the way core.ParamsKey spells one -
// sixteen hex characters. The older fixtures here use shorter names because
// nothing validated them; the delete route does, since it removes from the
// directory they address.
const deleteParams = "aaaa1111bbbb2222"

// realDeleter wires the real core.DeleteFrame and core.ClearFile, not fakes,
// so these tests prove the whole path a browser takes: the route, the guards,
// and the removal itself with every cache rule it has to keep.
//
// It is the same pair cli/web.go's outputDeleter wires for the running
// program, spelled here rather than shared, because a test that borrowed the
// production type would stop being able to substitute one half of it - which
// is exactly what TestAClearWhoseFramesCannotAllGoStillHappened needs.
func realDeleter(root string) Deleter {
	return coreDeleter{root: root}
}

type coreDeleter struct{ root string }

func (d coreDeleter) DeleteFrame(infoHash, params string, fileIndex, frameIndex int) error {
	return core.DeleteFrame(d.root, infoHash, params, fileIndex, frameIndex)
}

func (d coreDeleter) ClearFile(infoHash, params string, fileIndex int) error {
	return core.ClearFile(d.root, infoHash, params, fileIndex)
}

// deleteFrame issues the request the page's cross issues. query is appended
// verbatim so a test can also send a malformed result set, or none at all.
func deleteFrame(t *testing.T, base, infoHash, index, frame, query string) *http.Response {
	t.Helper()

	target := strings.TrimSuffix(base, "/") + "/runs/" + infoHash + "/files/" + index + "/frames/" + frame + query
	req, err := http.NewRequest(http.MethodDelete, target, nil)
	if err != nil {
		t.Fatalf("build DELETE %s: %v", target, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", target, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// deletedFile decodes the file detail a successful delete answers with.
func deletedFile(t *testing.T, resp *http.Response) FileDetail {
	t.Helper()

	var body struct {
		File FileDetail `json:"file"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode the delete response: %v", err)
	}
	return body.File
}

// TestDeleteFrameAnswersWhatTheFileHasLeft is the endpoint's contract: the
// frame goes, and the response is the same body the GET on that file answers
// with, recomputed from disk - so a page re-renders from what is actually
// there instead of from its own idea of what the delete did.
//
// The reread at the end is the part that matters: a response assembled in
// memory would pass every assertion above it while nothing on disk had
// changed.
func TestDeleteFrameAnswersWhatTheFileHasLeft(t *testing.T) {
	root := t.TempDir()
	writeSet(t, root, detailHash, deleteParams, 4, 6, "Sintel/sintel.mp4", 43700, 308900, 576300, 833600)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	before, status := getFileDetail(t, ts.URL, detailHash, "6")
	if status != http.StatusOK || len(before.Frames) != 4 {
		t.Fatalf("before: status %d, timecodes %v", status, timecodes(before.Frames))
	}
	doomed := before.Frames[1]
	if doomed.TimeMS != 308900 || doomed.Params != deleteParams {
		t.Fatalf("frame to delete = %+v, want the 308900 one of %s", doomed, deleteParams)
	}

	resp := deleteFrame(t, ts.URL, detailHash, "6", "1", "?params="+deleteParams)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: status %d, want 200", resp.StatusCode)
	}

	want := []int64{43700, 576300, 833600}
	if got := timecodes(deletedFile(t, resp).Frames); !sameTimes(got, want) {
		t.Errorf("the delete answered %v, want %v", got, want)
	}

	// The frame's own URL was minted before the delete and still resolves to
	// a path; the file behind it is what is gone, which handleFile re-stats.
	if r := get(t, ts.URL, "/"+doomed.URL); r.StatusCode != http.StatusNotFound {
		t.Errorf("GET %s after the delete: status %d, want 404", doomed.URL, r.StatusCode)
	}

	after, status := getFileDetail(t, ts.URL, detailHash, "6")
	if status != http.StatusOK {
		t.Fatalf("after: status %d, want 200", status)
	}
	if got := timecodes(after.Frames); !sameTimes(got, want) {
		t.Errorf("read back from disk: %v, want %v", got, want)
	}
}

// TestDeleteFrameLeavesTheOtherFileAlone is the acceptance criterion said
// outright: a torrent's other files are not collateral damage. One run
// directory holds both files' results, and the delete rewrites a manifest and
// may rewrite run.json, so "it only touched the one directory" is worth
// asserting rather than assuming.
func TestDeleteFrameLeavesTheOtherFileAlone(t *testing.T) {
	root := t.TempDir()

	run := cache.Run{
		Version: cache.Version, Tool: "test", CreatedAt: time.Now(),
		InfoHash: detailHash, Name: "Season 1",
		Plan: cache.Plan{Count: 3, Start: 0.05, End: 0.95, Profile: "min-traffic", Format: "jpeg"},
		Videos: []cache.File{
			{Index: 6, Path: "Season 1/episode-1.mkv", Bytes: 1 << 20},
			{Index: 7, Path: "Season 1/episode-2.mkv", Bytes: 1 << 20},
		},
		Selected: []int{6, 7}, Complete: []int{6, 7},
	}
	buildCachedRun(t, root, detailHash, deleteParams, run, framesAt(6, "Season 1/episode-1.mkv", 1000, 2000, 3000))
	buildCachedRun(t, root, detailHash, deleteParams, run, framesAt(7, "Season 1/episode-2.mkv", 1000, 2000, 3000))

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	neighbourBefore, status := getFileDetail(t, ts.URL, detailHash, "7")
	if status != http.StatusOK || len(neighbourBefore.Frames) != 3 {
		t.Fatalf("file 7 before: status %d, timecodes %v", status, timecodes(neighbourBefore.Frames))
	}

	resp := deleteFrame(t, ts.URL, detailHash, "6", "1", "?params="+deleteParams)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: status %d, want 200", resp.StatusCode)
	}
	if got := timecodes(deletedFile(t, resp).Frames); !sameTimes(got, []int64{1000, 3000}) {
		t.Fatalf("file 6 = %v, want the 2000 one gone", got)
	}

	neighbourAfter, status := getFileDetail(t, ts.URL, detailHash, "7")
	if status != http.StatusOK {
		t.Fatalf("file 7 after: status %d, want 200", status)
	}
	if got := timecodes(neighbourAfter.Frames); !sameTimes(got, timecodes(neighbourBefore.Frames)) {
		t.Errorf("file 7 = %v, want %v - the other file must not have been touched",
			got, timecodes(neighbourBefore.Frames))
	}
	for _, f := range neighbourAfter.Frames {
		if r := get(t, ts.URL, "/"+f.URL); r.StatusCode != http.StatusOK {
			t.Errorf("file 7's frame at %d: status %d, want 200", f.TimeMS, r.StatusCode)
		}
	}
}

// TestTheRunStillOpensAfterADelete is the failure this whole change exists to
// avoid, checked where a person would meet it: reopening the run.
//
// A SECOND, fresh server over the same directory is how "after a restart" is
// testable - it has never held this run, never announced a frame of it, and
// knows nothing but what is on disk, which is exactly the position the next
// process to start is in. Deleting the frame file alone would make this a
// failed run (TestReopeningARunWithDeletedFramesFails pins that, and stays
// true); deleting the record with it keeps the run whole.
func TestTheRunStillOpensAfterADelete(t *testing.T) {
	root := t.TempDir()
	writeSet(t, root, detailHash, deleteParams, 4, 6, "Sintel/sintel.mp4", 43700, 308900, 576300, 833600)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, first := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	resp := deleteFrame(t, first.URL, detailHash, "6", "1", "?params="+deleteParams)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: status %d, want 200", resp.StatusCode)
	}

	_, second := newTestServerWith(t, cfg, (&fakeRun{}).runner, realReplayer(root), nil)

	id, state, status := reopenRun(t, second.URL, detailHash, deleteParams)
	if status != http.StatusAccepted || id == "" {
		t.Fatalf("POST /runs/reopen: status %d id %q, want 202 and an id", status, id)
	}
	if state != "done" {
		t.Fatalf("reopened run state = %q, want done - one deleted frame must not cost the other three", state)
	}

	detail, status := getFileDetail(t, second.URL, detailHash, "6")
	if status != http.StatusOK {
		t.Fatalf("file detail on the fresh server: status %d, want 200", status)
	}
	if got := timecodes(detail.Frames); !sameTimes(got, []int64{43700, 576300, 833600}) {
		t.Errorf("the reopened run shows %v, want the three survivors", got)
	}
}

// TestDeletingTheLastFrameAnswersAnEmptyList: the file that has nothing left
// still gets an answer, not the 404 the GET reports for it. The delete
// happened; "there is nothing left" is what the request that emptied it is
// owed, and the page replaces its grid with that.
func TestDeletingTheLastFrameAnswersAnEmptyList(t *testing.T) {
	root := t.TempDir()
	writeSet(t, root, detailHash, deleteParams, 1, 6, "Sintel/sintel.mp4", 43700)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	resp := deleteFrame(t, ts.URL, detailHash, "6", "0", "?params="+deleteParams)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: status %d, want 200", resp.StatusCode)
	}
	detail := deletedFile(t, resp)
	if len(detail.Frames) != 0 || detail.Index != 6 {
		t.Fatalf("answered %+v, want file 6 with no frames", detail)
	}

	if _, status := getFileDetail(t, ts.URL, detailHash, "6"); status != http.StatusNotFound {
		t.Errorf("GET the emptied file: status %d, want 404 - there is no such file to show now", status)
	}
}

// TestDeleteFrameWithoutADeleterIsUnavailable mirrors
// TestReopenWithoutAReplayerIsUnavailable: a server built without the closure
// (every test that does not need one) refuses rather than panicking.
func TestDeleteFrameWithoutADeleterIsUnavailable(t *testing.T) {
	root := t.TempDir()
	writeSet(t, root, detailHash, deleteParams, 4, 6, "Sintel/sintel.mp4", 43700, 833600)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWithConfig(t, cfg, (&fakeRun{}).runner)

	resp := deleteFrame(t, ts.URL, detailHash, "6", "0", "?params="+deleteParams)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("DELETE with no deleter: status %d, want 503", resp.StatusCode)
	}

	if _, status := getFileDetail(t, ts.URL, detailHash, "6"); status != http.StatusOK {
		t.Errorf("the frames are gone after a refused delete: status %d", status)
	}
}

// TestDeleteFrameRefusesWhatNamesNoFrame covers both guards on the strings
// that reach the filesystem, and the difference between the two answers: a
// path segment that cannot name a resource is a 404, the way the GET on this
// same path already answers one; the result set is a request parameter, so a
// malformed one is a 400 about the request.
//
// Nothing may be removed on any of them, which is asserted once at the end
// against the whole table.
func TestDeleteFrameRefusesWhatNamesNoFrame(t *testing.T) {
	root := t.TempDir()
	writeSet(t, root, detailHash, deleteParams, 4, 6, "Sintel/sintel.mp4", 43700, 308900, 576300, 833600)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	set := "?params=" + deleteParams
	cases := []struct {
		name               string
		hash, index, frame string
		query              string
		want               int
	}{
		{"an infohash that is not hex", "not-a-hex-digest-not-a-hex-digest-not-ab", "6", "0", set, http.StatusNotFound},
		{"a torrent with nothing on disk", "0000000000000000000000000000000000000000", "6", "0", set, http.StatusNotFound},
		{"a file index no set lists", detailHash, "3", "0", set, http.StatusNotFound},
		{"a file index that is not a number", detailHash, "sixth", "0", set, http.StatusNotFound},
		{"a negative file index", detailHash, "-1", "0", set, http.StatusNotFound},
		{"a frame index the manifest does not hold", detailHash, "6", "9", set, http.StatusNotFound},
		{"a frame index that is not a number", detailHash, "6", "first", set, http.StatusNotFound},
		{"a result set that was never written", detailHash, "6", "0", "?params=ffffffffffffffff", http.StatusNotFound},
		{"no result set at all", detailHash, "6", "0", "", http.StatusBadRequest},
		{"a result set that is not hex", detailHash, "6", "0", "?params=../../../../etc", http.StatusBadRequest},
		{"a result set of the wrong length", detailHash, "6", "0", "?params=aaaa1111", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := deleteFrame(t, ts.URL, c.hash, c.index, c.frame, c.query)
			if resp.StatusCode != c.want {
				t.Errorf("status %d, want %d", resp.StatusCode, c.want)
			}
		})
	}

	detail, status := getFileDetail(t, ts.URL, detailHash, "6")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	want := []int64{43700, 308900, 576300, 833600}
	if got := timecodes(detail.Frames); !sameTimes(got, want) {
		t.Errorf("timecodes = %v, want %v - a refused delete must remove nothing", got, want)
	}
}

// TestValidParamsRefusesAnythingButAResultSetName pins the guard itself, the
// way TestValidInfoHashRefusesAnythingButADigest does for the other one: the
// HTTP table above can only reach it through a router that cleans some paths
// of its own accord, and a query parameter never reaches the router at all.
func TestValidParamsRefusesAnythingButAResultSetName(t *testing.T) {
	if !validParams(deleteParams) {
		t.Errorf("%q was refused as a result set", deleteParams)
	}
	for _, bad := range []string{
		"", "aaaa1111", "aaaa1111bbbb22223", "../../../../etc/passwd",
		"AAAA1111BBBB2222", // upper case is not what params dirs are named
		"aaaa1111bbbb222/", "aaaa1111bbbb222.", "aaaa1111bbbb222z",
	} {
		if validParams(bad) {
			t.Errorf("%q was accepted as a result set", bad)
		}
	}
}

// sameTimes compares two timecode lists element by element.
func sameTimes(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestADeleteWhoseSheetCannotBeRebuiltStillHappened is TOR-78: the contact
// sheet is derived from the frames, so its failure is news about the sheet,
// not about the delete - which by then has already happened, because
// core.DeleteFrame rebuilds the sheet only after the manifest is written and
// the frame unlinked.
//
// The sheet is made unwritable by putting a non-empty DIRECTORY where the
// file belongs: the manifest write, which happens first and in the same
// directory, still succeeds, and only the sheet's own rename fails. Denying
// the directory's permissions instead would have failed the manifest write
// too, and then there would be no delete to report.
func TestADeleteWhoseSheetCannotBeRebuiltStillHappened(t *testing.T) {
	root := t.TempDir()
	writeSet(t, root, detailHash, deleteParams, 4, 6, "Sintel/sintel.mp4", 43700, 308900, 576300, 833600)

	fileDir := fileDirOf(t, root, detailHash, deleteParams, 6, "Sintel/sintel.mp4")
	sheet := filepath.Join(fileDir, output.SheetName)
	if err := os.Remove(sheet); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clear the sheet: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sheet, "occupied"), 0o755); err != nil {
		t.Fatalf("put a directory where the sheet goes: %v", err)
	}

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	resp := deleteFrame(t, ts.URL, detailHash, "6", "1", "?params="+deleteParams)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: status %d, want 200 - the frame is gone, whatever became of the sheet", resp.StatusCode)
	}

	body := decodeBody(t, resp)
	if warning, _ := body["warning"].(string); warning == "" {
		t.Error("the response carries no warning; a sheet that still depicts a deleted frame has to be reported somewhere")
	}

	file, ok := body["file"].(map[string]any)
	if !ok {
		t.Fatalf("no file in the answer: %+v", body)
	}
	frames, _ := file["frames"].([]any)
	if len(frames) != 3 {
		t.Errorf("the answer lists %d frames, want the three survivors", len(frames))
	}

	// And on disk, which is what the page would see on a reload.
	m, loaded := cache.LoadManifest(fileDir)
	if !loaded {
		t.Fatal("the manifest is unreadable after the delete")
	}
	if len(m.Frames) != 3 {
		t.Errorf("the manifest holds %d records, want 3 - the delete must have happened", len(m.Frames))
	}
}
