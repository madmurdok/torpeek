package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
)

// TOR-183: un-ticking a finished file offers to clear what it has on disk,
// and pressing that deletes the frames, the contact sheet and the manifest.
//
// The Go half drives the real route against a real results tree with the real
// core.ClearFile behind it, so what it proves is what a browser would do. The
// app.js/app.css half reads the SERVED script and stylesheet as text, for the
// reason tick_test.go's own heading gives - this repository ships no JS
// runner - so it catches a control deleted, renamed or silently regated, and
// it cannot catch a wrong pixel. A real browser is what this ticket's report
// used for that.

// clearParams and clearOther are two result sets of one torrent, spelled the
// way core.ParamsKey spells one, because the route validates a params it is
// given.
const (
	clearParams = "aaaa1111bbbb3333"
	clearOther  = "cccc4444dddd5555"
)

// clearFile issues the request the row's Clear frames button issues. query is
// appended verbatim so a test can also name one result set, or send a
// malformed one.
func clearFile(t *testing.T, base, infoHash, index, query string) *http.Response {
	t.Helper()

	target := strings.TrimSuffix(base, "/") + "/runs/" + infoHash + "/files/" + index + "/frames" + query
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

// clearableRun is a run record complete enough for a top-up to price, which
// writeSet's own is deliberately not: it has no Source, and topUpFor refuses
// a record that cannot say what to run again. Everything here is what a real
// run writes.
func clearableRun(infoHash string, count int, files ...cache.File) cache.Run {
	indices := make([]int, 0, len(files))
	for _, f := range files {
		indices = append(indices, f.Index)
	}
	return cache.Run{
		Version:   cache.Version,
		Tool:      "test",
		CreatedAt: time.Now(),
		Source:    "magnet:?xt=urn:btih:" + infoHash,
		InfoHash:  infoHash,
		Name:      "Season 1",
		Plan:      cache.Plan{Count: count, Start: 0.05, End: 0.95, Profile: "min-traffic", Format: "jpeg"},
		Videos:    files,
		Selected:  indices,
		Complete:  indices,
	}
}

// writeSheet puts a contact sheet where one belongs, since the fixtures here
// write frames and a manifest and no sheet - and a clear that is supposed to
// take the sheet has to be given one to take.
func writeSheet(t *testing.T, fileDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fileDir, output.SheetName), []byte{0xFF, 0xD8, 0x09}, 0o644); err != nil {
		t.Fatalf("write a contact sheet: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The route, against a real tree.

// TestClearFileTakesTheWholeFileAndAnswersWhatIsLeft is the endpoint's
// contract: everything the file had is gone from disk, and the response is
// the same body the GET on that file answers with, recomputed afterwards - so
// a page re-renders from what is actually there rather than from its own idea
// of what the clear did.
//
// The reread at the end is the part that matters. A response assembled in
// memory would pass every assertion above it while nothing on disk had
// changed, which is the failure this whole feature is one press away from.
func TestClearFileTakesTheWholeFileAndAnswersWhatIsLeft(t *testing.T) {
	root := t.TempDir()
	buildCachedRun(t, root, detailHash, clearParams,
		clearableRun(detailHash, 3, cache.File{Index: 6, Path: "Sintel/sintel.mp4", Bytes: 1 << 20}),
		framesAt(6, "Sintel/sintel.mp4", 43700, 308900, 576300))

	fileDir := fileDirOf(t, root, detailHash, clearParams, 6, "Sintel/sintel.mp4")
	writeSheet(t, fileDir)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	resp := clearFile(t, ts.URL, detailHash, "6", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE the file's frames: status %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	// Decoded once: the response body is a stream, and reading it twice is
	// how an assertion comes to be made against nothing.
	body := decodeBody(t, resp)
	if warning, _ := body["warning"].(string); warning != "" {
		t.Errorf("a clean sweep reported %q; there was nothing in its way", warning)
	}
	file, ok := body["file"].(map[string]any)
	if !ok {
		t.Fatalf("no file in the answer: %+v", body)
	}
	if frames, _ := file["frames"].([]any); len(frames) != 0 {
		t.Errorf("the answer still lists %d frames", len(frames))
	}
	// Explicitly empty rather than absent, because the page replaces its grid
	// from these lists and a JSON null reads as "no answer" where what is
	// meant is "no frames".
	if _, present := file["frames"]; !present {
		t.Error("the answer omits \"frames\" entirely")
	}
	// Asked again, from disk, because the answer above was composed by the
	// same call that did the deleting.
	if reread, status := getFileDetail(t, ts.URL, detailHash, "6"); status != http.StatusNotFound {
		t.Errorf("GET the file again: status %d with %d frames, want 404 - a file with "+
			"nothing on disk anywhere is what fileDetail reports as no such file",
			status, len(reread.Frames))
	}

	// ON DISK, which is what a page would see on a reload and the only thing
	// the person actually asked for.
	if _, err := os.Stat(fileDir); !os.IsNotExist(err) {
		t.Errorf("%s survived the clear (err = %v) - the frames, the sheet and the "+
			"manifest were all inside it", fileDir, err)
	}
	run, ok := cache.LoadRun(output.Layout{Root: root, InfoHash: detailHash, Params: clearParams}.RunDir())
	if !ok {
		t.Fatal("the run record is no longer readable; a clear must not cost the set itself")
	}
	if len(run.Complete) != 0 {
		t.Errorf("run.json still counts %v complete", run.Complete)
	}
	if len(run.Selected) != 1 || run.Selected[0] != 6 {
		t.Errorf("run.json says %v was asked for, want [6] still - see core.ClearFile for "+
			"why what was ASKED FOR survives a delete of what came out", run.Selected)
	}
}

// TestOnePressClearsEverySetTheFileHasFramesIn is the decision
// Server.ClearFile carries: the button sits on a FILE's row, and the grid
// under that row is the merged grid of every result set (TOR-69), so clearing
// one of them would leave frames on screen the person just asked to be rid of
// and the button standing beside them.
//
// THE TWO SETS SHARE EVERY TIMECODE, which is what makes this test
// discriminate rather than merely pass. fileDetail MERGES its frames by
// timecode, so a second set captured at the same points is deduplicated out
// of the response entirely - an implementation that read the sets off that
// answer would clear one of the two and report the file clean. The sets are
// therefore found by walking the parameter directories (setsHoldingFrames),
// and this is the fixture that tells the two implementations apart.
func TestOnePressClearsEverySetTheFileHasFramesIn(t *testing.T) {
	root := t.TempDir()
	const path = "Sintel/sintel.mp4"
	for _, params := range []string{clearParams, clearOther} {
		buildCachedRun(t, root, detailHash, params,
			clearableRun(detailHash, 2, cache.File{Index: 6, Path: path, Bytes: 1 << 20}),
			framesAt(6, path, 43700, 576300))
	}

	first := fileDirOf(t, root, detailHash, clearParams, 6, path)
	second := fileDirOf(t, root, detailHash, clearOther, 6, path)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	// The premise: before the clear, the merged answer really does hide the
	// second set's frames behind the first's. Asserted rather than assumed,
	// so this test cannot quietly stop being about anything.
	before, status := getFileDetail(t, ts.URL, detailHash, "6")
	if status != http.StatusOK {
		t.Fatalf("GET the file: status %d, want 200", status)
	}
	if len(before.Frames) != 2 {
		t.Fatalf("the merged answer lists %d frames, want 2 - the two sets are supposed to "+
			"coincide exactly, and the rest of this test rests on that", len(before.Frames))
	}
	seen := map[string]bool{}
	for _, f := range before.Frames {
		seen[f.Params] = true
	}
	if len(seen) != 1 {
		t.Fatalf("the merged answer names %d result sets, want 1 - if the merge no longer "+
			"hides the second set, this test no longer tells the two implementations apart", len(seen))
	}

	if resp := clearFile(t, ts.URL, detailHash, "6", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: status %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	for _, dir := range []string{first, second} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s survived (err = %v) - one press has to leave the file with nothing "+
				"on this torrent, or the button cannot honestly go away", dir, err)
		}
	}
}

// TestClearingOneNamedSetLeavesTheOthers: the per-set primitive is still
// reachable, which is what keeps the merged behaviour above a decision of the
// page's rather than the only thing the server can do.
func TestClearingOneNamedSetLeavesTheOthers(t *testing.T) {
	root := t.TempDir()
	const path = "Sintel/sintel.mp4"
	buildCachedRun(t, root, detailHash, clearParams,
		clearableRun(detailHash, 2, cache.File{Index: 6, Path: path, Bytes: 1 << 20}),
		framesAt(6, path, 43700, 576300))
	buildCachedRun(t, root, detailHash, clearOther,
		clearableRun(detailHash, 2, cache.File{Index: 6, Path: path, Bytes: 1 << 20}),
		framesAt(6, path, 111000, 222000))

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	if resp := clearFile(t, ts.URL, detailHash, "6", "?params="+clearParams); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear one set: status %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	if _, err := os.Stat(fileDirOf(t, root, detailHash, clearParams, 6, path)); !os.IsNotExist(err) {
		t.Errorf("the named set survived (err = %v)", err)
	}
	kept := fileDirOf(t, root, detailHash, clearOther, 6, path)
	if _, err := os.Stat(filepath.Join(kept, manifest.Name)); err != nil {
		t.Errorf("the other set lost its manifest: %v", err)
	}
	detail, status := getFileDetail(t, ts.URL, detailHash, "6")
	if status != http.StatusOK {
		t.Fatalf("GET the file after clearing one set: status %d, want 200", status)
	}
	if len(detail.Frames) != 2 {
		t.Errorf("the file lists %d frames, want the other set's 2", len(detail.Frames))
	}
}

// TestASetWithOnlyFailedPointsIsNotSweptByAClear is a refusal rather than a
// feature, and it is deliberate: a set whose every planned point failed has a
// manifest and no pictures, and those records are the only account anybody
// has of WHY the points produced nothing (TOR-118 draws them in the grid).
//
// A clear is about the frames a person can see. Sweeping a set that has none
// would destroy something they did not ask to destroy and cannot get back -
// a failure is not something a rerun reproduces on demand - and the button
// that reaches here is never offered for such a file in the first place,
// because it counts pictures too.
func TestASetWithOnlyFailedPointsIsNotSweptByAClear(t *testing.T) {
	root := t.TempDir()
	const path = "Sintel/sintel.mp4"

	// The set with pictures, and the set with only reasons.
	buildCachedRun(t, root, detailHash, clearParams,
		clearableRun(detailHash, 2, cache.File{Index: 6, Path: path, Bytes: 1 << 20}),
		framesAt(6, path, 43700, 576300))

	// buildHoledRun rather than buildCachedRun, and that is the whole
	// fixture: buildCachedRun writes a real frame file for every record it is
	// given, so it would quietly give this "failed" point a picture and the
	// test would be about nothing.
	failed := framesAt(6, path, 900000)
	failed.Frames[0].Path = ""
	failed.Frames[0].ActualMS = nil
	failed.Frames[0].Shift = manifest.ShiftFailed
	failed.Frames[0].Error = "read_stalled"
	buildHoledRun(t, root, detailHash, clearOther,
		clearableRun(detailHash, 1, cache.File{Index: 6, Path: path, Bytes: 1 << 20}),
		failed)

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	if resp := clearFile(t, ts.URL, detailHash, "6", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: status %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	if _, err := os.Stat(fileDirOf(t, root, detailHash, clearParams, 6, path)); !os.IsNotExist(err) {
		t.Errorf("the set that had pictures survived (err = %v)", err)
	}
	kept := fileDirOf(t, root, detailHash, clearOther, 6, path)
	m, ok := cache.LoadManifest(kept)
	if !ok {
		t.Fatal("the failure records were swept with the pictures - they are the only " +
			"account of why those points produced nothing, and nothing can recreate them")
	}
	if len(m.Frames) != 1 || m.Frames[0].Error == "" {
		t.Errorf("the surviving manifest lists %d frames and the first says %q, want the "+
			"one failed point with its reason", len(m.Frames), m.Frames[0].Error)
	}
}

// TestATopUpAfterAClearOffersTheFileAgain is the whole reason Selected is
// left alone, checked through the reader it was left alone FOR rather than
// asserted about the record.
//
// A person who clears a file has to have a way back that is not a fresh run
// with a fresh ceiling. topUpFor walks Selected and counts each file's
// captured frames off its manifest, so a cleared file - still asked for, no
// manifest at all - reads as the whole plan outstanding, and finishing it
// lands in the same result set at the same plan.
func TestATopUpAfterAClearOffersTheFileAgain(t *testing.T) {
	root := t.TempDir()
	const path = "Sintel/sintel.mp4"
	buildCachedRun(t, root, detailHash, clearParams,
		clearableRun(detailHash, 3, cache.File{Index: 6, Path: path, Bytes: 1 << 20}),
		framesAt(6, path, 43700, 308900, 576300))

	// Before: nothing outstanding, so nothing to top up. Asserted, so the
	// assertion after the clear is a change rather than a coincidence.
	if before, ok := topUpFor(root, detailHash, clearParams, 0); !ok {
		t.Fatal("topUpFor cannot read the set at all")
	} else if before.Remaining != 0 || !before.Complete {
		t.Fatalf("before the clear the set is %d frames short (complete=%v), want a whole "+
			"one - this test measures what the clear changes", before.Remaining, before.Complete)
	}

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))
	if resp := clearFile(t, ts.URL, detailHash, "6", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: status %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	after, ok := topUpFor(root, detailHash, clearParams, 0)
	if !ok {
		t.Fatal("topUpFor can no longer read the set after a clear")
	}
	if after.Refused != "" {
		t.Fatalf("a top-up after a clear is refused: %q - the file is still listed as "+
			"asked for precisely so this stays possible", after.Refused)
	}
	if after.Remaining != 3 {
		t.Errorf("a top-up offers to take %d frames, want the plan's whole 3 - a cleared "+
			"file has to read as asked for and entirely uncaptured", after.Remaining)
	}
	if len(after.Files) != 1 || after.Files[0].Index != 6 || after.Files[0].Captured != 0 {
		t.Errorf("the top-up's file list is %+v, want file 6 with nothing captured", after.Files)
	}
}

// TestAClearWhoseSheetCannotGoStillHappened is TOR-78's shape at a file's
// size: the run record and the manifest already say the file is not part of
// this result, so answering "the clear failed" would leave a page showing
// frames that nothing accounts for, corrected only by a reload.
//
// The sheet is made unremovable by putting a non-empty DIRECTORY where the
// file belongs, exactly as TestADeleteWhoseSheetCannotBeRebuiltStillHappened
// does one operation over: it fails that removal alone, where denying the
// directory's permissions would have failed the manifest's too and left
// nothing to report.
func TestAClearWhoseSheetCannotGoStillHappened(t *testing.T) {
	root := t.TempDir()
	const path = "Sintel/sintel.mp4"
	buildCachedRun(t, root, detailHash, clearParams,
		clearableRun(detailHash, 2, cache.File{Index: 6, Path: path, Bytes: 1 << 20}),
		framesAt(6, path, 43700, 576300))

	fileDir := fileDirOf(t, root, detailHash, clearParams, 6, path)
	if err := os.MkdirAll(filepath.Join(fileDir, output.SheetName, "occupied"), 0o755); err != nil {
		t.Fatalf("put a directory where the sheet goes: %v", err)
	}

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	resp := clearFile(t, ts.URL, detailHash, "6", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: status %d, want 200 - the frames are gone, whatever became of the "+
			"sheet: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	warning, _ := body["warning"].(string)
	if warning == "" {
		t.Fatal("the response carries no warning; a person has to be told what is still on " +
			"disk, and they can only be told beside the file they pressed it on")
	}
	if !strings.Contains(warning, output.SheetName) {
		t.Errorf("the warning is %q and does not name what is in the way", warning)
	}
	// AND IT SAYS THE SENTENCE ONCE. This is a correction the browser found:
	// core.ClearFile's partial failure arrives already carrying the sentinel,
	// and wrapping it again here printed "the file was cleared and something
	// of it is still on disk" twice, one straight after the other, in a
	// warning a person reads beside their file.
	if n := strings.Count(warning, "still on disk"); n != 1 {
		t.Errorf("the warning states its own sentence %d times, want once: %q", n, warning)
	}

	// And the clear happened: the frames are gone and the record no longer
	// counts the file.
	if _, err := os.Stat(filepath.Join(fileDir, "frames")); !os.IsNotExist(err) {
		t.Errorf("the frames directory survived (err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(fileDir, manifest.Name)); !os.IsNotExist(err) {
		t.Errorf("the manifest survived (err = %v)", err)
	}
	run, ok := cache.LoadRun(output.Layout{Root: root, InfoHash: detailHash, Params: clearParams}.RunDir())
	if !ok {
		t.Fatal("the run record is unreadable")
	}
	if len(run.Complete) != 0 {
		t.Errorf("run.json still counts %v complete after a clear that reported only the "+
			"sheet - the record is written before any byte goes for exactly this reason",
			run.Complete)
	}
}

// TestAClearWhereOneSetIsRefusedOutrightStillReportsWhatHappened is the other
// shape a partial clear takes, and the one the sentinel has to be ADDED for:
// one set cleared, another refused before it touched anything. No single error
// says "partly happened" there - the refusal says nothing happened, and it is
// right about its own set - and yet the file IS out of one result. So the
// answer is a 200 with the file's detail and a warning, because refusing
// would leave the cleared set's frames on screen with nothing accounting for
// them (TOR-78, again).
//
// The refused set holds a manifest naming a frame OUTSIDE its own run, which
// is the one shape core.ClearFile refuses outright (see within's own doc: an
// older manifest whose absolute path could not be re-anchored). It is written
// by hand rather than through output.Writer, because relativizing every path
// against the directory it lands in is precisely that writer's job.
func TestAClearWhereOneSetIsRefusedOutrightStillReportsWhatHappened(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	const path = "Sintel/sintel.mp4"

	// The set that clears.
	buildCachedRun(t, root, detailHash, clearParams,
		clearableRun(detailHash, 2, cache.File{Index: 6, Path: path, Bytes: 1 << 20}),
		framesAt(6, path, 43700, 576300))
	// And the set that will not.
	buildCachedRun(t, root, detailHash, clearOther,
		clearableRun(detailHash, 2, cache.File{Index: 6, Path: path, Bytes: 1 << 20}),
		framesAt(6, path, 111000, 222000))

	stray := filepath.Join(outside, "stolen.jpg")
	if err := os.WriteFile(stray, []byte{0xFF, 0xD8, 0x03}, 0o644); err != nil {
		t.Fatalf("write a frame outside the run: %v", err)
	}
	refusedDir := fileDirOf(t, root, detailHash, clearOther, 6, path)
	m, ok := cache.LoadManifest(refusedDir)
	if !ok {
		t.Fatalf("no manifest in %s", refusedDir)
	}
	m.Frames[1].Path = stray
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("encode the manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(refusedDir, manifest.Name), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	resp := clearFile(t, ts.URL, detailHash, "6", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: status %d, want 200 - one set is gone, so refusing would leave "+
			"its frames on a page with nothing accounting for them: %s",
			resp.StatusCode, readAll(t, resp))
	}
	warning, _ := decodeBody(t, resp)["warning"].(string)
	if warning == "" {
		t.Fatal("the response carries no warning; one of the two sets was refused")
	}
	if !strings.Contains(warning, "still on disk") {
		t.Errorf("the warning is %q and does not read as a clear that partly happened - "+
			"that sentinel is what makes this a 200 rather than a refusal", warning)
	}
	if !strings.Contains(warning, stray) {
		t.Errorf("the warning is %q and does not say what was refused or why", warning)
	}

	if _, err := os.Stat(fileDirOf(t, root, detailHash, clearParams, 6, path)); !os.IsNotExist(err) {
		t.Errorf("the set that could be cleared survived (err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(refusedDir, manifest.Name)); err != nil {
		t.Errorf("the refused set lost its manifest: %v - a refusal must change nothing", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("the frame outside the run was removed: %v", err)
	}
}

// TestClearingAFileWithNothingOnDiskIsNotAFailure: the postcondition asked
// for already holds, and answering 404 would make a second press from a stale
// page an error about the state somebody wanted.
func TestClearingAFileWithNothingOnDiskIsNotAFailure(t *testing.T) {
	root := t.TempDir()
	const path = "Sintel/sintel.mp4"
	buildCachedRun(t, root, detailHash, clearParams,
		clearableRun(detailHash, 2, cache.File{Index: 6, Path: path, Bytes: 1 << 20}),
		framesAt(6, path, 43700, 576300))

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	if resp := clearFile(t, ts.URL, detailHash, "6", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("the first clear: status %d, want 200", resp.StatusCode)
	}
	resp := clearFile(t, ts.URL, detailHash, "6", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the second clear: status %d, want 200 - a file that is already clean is "+
			"the answer to \"clear this file\", not an error: %s",
			resp.StatusCode, readAll(t, resp))
	}
	if warning, _ := decodeBody(t, resp)["warning"].(string); warning != "" {
		t.Errorf("clearing an already-clean file warned %q", warning)
	}
}

// TestClearFileWithoutADeleterIsUnavailable mirrors the per-frame route's own
// guard: a server with nothing wired up to remove anything refuses rather
// than pretending. 503, because it is the server that cannot, not the
// request that is wrong.
func TestClearFileWithoutADeleterIsUnavailable(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OutputRoot = t.TempDir()
	_, ts := newTestServerWithConfig(t, cfg, (&fakeRun{}).runner)

	resp := clearFile(t, ts.URL, detailHash, "6", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("DELETE with no deleter: status %d, want 503", resp.StatusCode)
	}
}

// TestClearFileRefusesWhatNamesNoFile keeps the same split the per-frame
// DELETE makes: a path segment that cannot name a resource is a 404, and a
// malformed result set - a request parameter, not part of the address - is a
// 400 about the request.
func TestClearFileRefusesWhatNamesNoFile(t *testing.T) {
	root := t.TempDir()
	buildCachedRun(t, root, detailHash, clearParams,
		clearableRun(detailHash, 2, cache.File{Index: 6, Path: "Sintel/sintel.mp4", Bytes: 1 << 20}),
		framesAt(6, "Sintel/sintel.mp4", 43700, 576300))

	cfg := DefaultConfig()
	cfg.OutputRoot = root
	_, ts := newTestServerWith(t, cfg, (&fakeRun{}).runner, nil, realDeleter(root))

	for _, c := range []struct {
		name     string
		infoHash string
		index    string
		query    string
		want     int
	}{
		{"a file index that is not a number", detailHash, "six", "", http.StatusNotFound},
		{"a negative file index", detailHash, "-1", "", http.StatusNotFound},
		// The infohash is the only string a request here puts into a path
		// (see validInfoHash), so anything but a digest has to be refused
		// before it reaches the filesystem. Spelled without dot segments on
		// purpose: an http.Client normalizes those out of a URL before the
		// request is sent, so "../.." would never reach the handler and the
		// case would prove nothing.
		{"an infohash that is not a digest", "notahexdigestatall", "6", "", http.StatusNotFound},
		{"a set that is not a result set name", detailHash, "6", "?params=nonsense", http.StatusBadRequest},
		{"a set that does not exist", detailHash, "6", "?params=ffffffffffffffff", http.StatusNotFound},
		// Not a refusal: nothing on disk anywhere is the state the request
		// asked for, and answering 404 would make a second press from a stale
		// page an error about what somebody wanted.
		{"a file no set lists", detailHash, "99", "", http.StatusOK},
	} {
		resp := clearFile(t, ts.URL, c.infoHash, c.index, c.query)
		if resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d: %s", c.name, resp.StatusCode, c.want, readAll(t, resp))
		}
	}

	// Nothing above touched the file that IS there, which is the assertion
	// that makes the statuses worth having.
	if _, err := os.Stat(fileDirOf(t, root, detailHash, clearParams, 6, "Sintel/sintel.mp4")); err != nil {
		t.Errorf("a refused clear removed something anyway: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The page.

// TestUnTickingAFinishedFileOffersToClearItsFrames is the ticket's own first
// sentence, and the three things that have to be true at once for the offer
// to appear.
//
// The gate is not "was this file selected". It is asked for, AND the row has
// settled, AND the file has frames on disk - which is the distinction the
// ticket insists on: the page has to know a file HAS FRAMES, not merely that
// somebody ticked it.
func TestUnTickingAFinishedFileOffersToClearItsFrames(t *testing.T) {
	js := servedScript(t)

	// The button is built into the file's own row, at its end.
	render := jsFunc(t, js, "renderFileList")
	if !strings.Contains(render, `clear.className = "picker-clear"`) {
		t.Fatal("renderFileList builds no .picker-clear on a file's row - the ticket puts " +
			"the button at the end of the row it is about")
	}
	if !strings.Contains(render, "row.append(clear)") {
		t.Error("the clear is not appended to the file's own row")
	}
	if !regexp.MustCompile(`(?s)if \(video\) \{[^}]*clear\.className`).MatchString(render) {
		t.Error("the clear is built for untickable rows too - a .nfo has no frames, " +
			"because no tick can name it")
	}

	// The un-tick is what reveals it, and it is page-local: nothing is asked
	// of the server and nothing is spent.
	tick := jsFunc(t, js, "tickFile")
	if !strings.Contains(tick, "entry.unticked.add(index)") {
		t.Fatal("un-ticking a finished file records nothing, so there is nothing for the " +
			"row to draw the offer from")
	}
	if !strings.Contains(tick, "if (entry.picked.has(index) && FINAL.has(entry.state) && framesOnDisk(entry, index) > 0)") {
		t.Error("the un-tick branch does not check all three of asked-for, settled and " +
			"having frames - un-ticking a file mid-fetch means STOP (TOR-184), and " +
			"offering a clear there would put a destructive button on a live capture")
	}
	if strings.Contains(tick, "post(") {
		t.Error("tickFile posts something on an un-tick - un-ticking a finished file " +
			"changes nothing on the server, which is exactly why cache.Run.Selected " +
			"survives it")
	}

	// And it goes when the file is clean. Both halves of the lifecycle live
	// in updateFileCosts, the one place that re-states every row.
	costs := jsFunc(t, js, "updateFileCosts")
	if !strings.Contains(costs, "row.clear.hidden = !offering") {
		t.Fatal("the button's visibility is not decided from the offer, so nothing takes " +
			"it away when there is nothing left to clear")
	}
	if !strings.Contains(costs, "const offering = clearable && entry.unticked.has(index)") {
		t.Error("the offer is not read together with \"and there is something to clear\" - " +
			"read alone, an un-tick left in the set would put the button back on a file " +
			"whose frames a top-up has since restored")
	}
	if !strings.Contains(costs, "row.box.checked = asked && !offering") {
		t.Error("the box does not stay un-ticked while the offer is on screen, so the " +
			"gesture would appear to undo itself")
	}
}

// TestTheClearSaysHowManyFramesItWillDelete: this is the most destructive
// press on a file's row, and a control labelled "Clear" alone would not say
// how much. The figure comes from the same count the summary beside it shows,
// so the two cannot disagree - "Clear 12 frames" next to "8 frames" would be
// two answers to one question and only one of them could be right.
func TestTheClearSaysHowManyFramesItWillDelete(t *testing.T) {
	js := servedScript(t)

	costs := jsFunc(t, js, "updateFileCosts")
	if !strings.Contains(costs, `row.clear.textContent = "Clear " + (frames === 1 ? "1 frame" : frames + " frames")`) {
		t.Error("the button does not name the number of frames it would delete")
	}
	if !strings.Contains(costs, "const frames = framesOnDisk(entry, index)") {
		t.Error("the figure on the button is not read from what the file HAS")
	}

	// One count, shared, rather than two spellings of it.
	summary := jsFunc(t, js, "updateFileSummary")
	if !strings.Contains(summary, "capturedCells(fentry)") {
		t.Error("the row's summary no longer counts through capturedCells, so the button " +
			"beside it and the figure beside that are two independent counts of one thing")
	}
	// framesOnDisk and capturedCells are derivations over a file's own state,
	// so they are state.js's since TOR-191 - the count the button quotes is
	// not a fact about the DOM.
	derive := stateJS(t)
	on := jsFunc(t, derive, "framesOnDisk")
	if !strings.Contains(on, "capturedCells(fentry)") {
		t.Error("framesOnDisk does not go through capturedCells either")
	}
	cells := jsFunc(t, derive, "capturedCells")
	if !strings.Contains(cells, "gridCells(fentry).filter((cell) => cell.url).length") {
		t.Error("capturedCells no longer counts cells that have a picture - counting the " +
			"map's size instead would report a holed run of five frames as twelve, since " +
			"a point that produced nothing is recorded too (TOR-118)")
	}

	// And what it will and will not do, in words, because the way back
	// (a top-up) is the reason one press is acceptable at all.
	if !strings.Contains(costs, "a top-up can take it again") {
		t.Error("the button's title does not say that the file stays asked for - which is " +
			"the whole of what makes this reversible, and it is not obvious")
	}
}

// TestUnTickingAFinishedFileStillOffersTheClearRatherThanStoppingAnything is
// this ticket's half of a control TOR-184 gave two more meanings, and it is
// kept as an ORDER assertion because that is where the two could come to
// disagree.
//
// It was written as "un-ticking anything unfinished is refused", which was
// the honest answer while nothing could stop a fetch. TOR-184 made two of
// those cases act - a file waiting for the next pass is dropped, a file being
// fetched stops the run - so the refusal is no longer what an un-tick falls
// through to for them. What must still be true is that the clear is reached
// only when neither stop applies (a settled row has nothing fetching and
// nothing deferred, so this is a statement about the reading order, not a
// clash), and that the one remaining refusal still puts the box back.
func TestUnTickingAFinishedFileStillOffersTheClearRatherThanStoppingAnything(t *testing.T) {
	js := servedScript(t)
	fn := jsFunc(t, js, "tickFile")

	if !strings.Contains(fn, "box.checked = true;") {
		t.Fatal("an un-tick that is not an offer no longer puts the box back, so a box " +
			"would sit cleared beside a file this row is still holding")
	}
	if !strings.Contains(fn, "entry.unticked.add(index)") {
		t.Fatal("un-ticking a finished file no longer records the offer, so the Clear " +
			"frames button can never appear")
	}
	// Every branch, in the order the function reads them.
	drop := strings.Index(fn, "dropFile(entry, index)")
	stop := strings.Index(fn, "stopFetch(entry, index, box)")
	offer := strings.Index(fn, "entry.unticked.add(index)")
	refusal := strings.Index(fn, "nothing is fetching this file")
	if drop < 0 || stop < 0 || offer < 0 || refusal < 0 {
		t.Fatalf("cannot locate all four un-tick branches: drop at %d, stop at %d, offer "+
			"at %d, refusal at %d", drop, stop, offer, refusal)
	}
	if !(drop < stop && stop < offer && offer < refusal) {
		t.Errorf("the branches read drop@%d stop@%d offer@%d refusal@%d. The two stops "+
			"must come first - they are the cases where something is actually happening to "+
			"the file - and the refusal must stay what an un-tick falls through to, not "+
			"something the offer has to be squeezed past", drop, stop, offer, refusal)
	}
}

// TestTheRowStillNamesItsOwnCheckbox is TOR-182's trap, re-checked because
// this ticket puts a SECOND labelable element in that row.
//
// A <label> with no `for` labels its first labelable DESCENDANT, and `button`
// is one. TOR-182 lost the whole of TOR-181's tick to exactly that when the
// disclosure went in first; the fix was naming the control explicitly, and
// that fix is what makes adding another button to this row safe at all.
func TestTheRowStillNamesItsOwnCheckbox(t *testing.T) {
	js := servedScript(t)
	fn := jsFunc(t, js, "renderFileList")

	if !strings.Contains(fn, "box.id = \"file-tick-\"") || !strings.Contains(fn, "row.htmlFor = box.id") {
		t.Fatal("the row no longer names its checkbox by id. With two buttons in the row " +
			"the label would fall to whichever is first in the DOM, and clicking the " +
			"file's NAME would press a button instead of ticking it")
	}
	// And the new button does not also open the file's detail, which the
	// disclosure in the same row deliberately does.
	if !regexp.MustCompile(`(?s)clear\.addEventListener\("click", \(event\) => \{\s*event\.stopPropagation\(\)`).MatchString(fn) {
		t.Error("the clear does not stop its click from bubbling to the row, so pressing " +
			"it would also toggle the file's detail open or closed - the same guard " +
			".run-cancel and the priority buttons keep one level up")
	}
}

// TestAClearThatOnlyPartlyHappenedIsReportedBesideTheFile is the acceptance
// criterion in TOR-78's shape, one level more specific than TOR-78 needed to
// be: a page-global error line saying some frames could not be removed does
// not say WHICH file's, and a season pack has twenty-five rows.
func TestAClearThatOnlyPartlyHappenedIsReportedBesideTheFile(t *testing.T) {
	js := servedScript(t)
	css := stylesheet(t)

	fn := jsFunc(t, js, "clearFrames")
	if !strings.Contains(fn, "if (answer.warning) setFileNote(entry, index, answer.warning)") {
		t.Fatal("a warning from the server is not put beside the file it is about")
	}
	if strings.Contains(fn, "showError(answer.warning)") {
		t.Error("the warning also goes to the page's own error line. One report, in the " +
			"place the criterion names: two copies of one sentence in two places is how a " +
			"person comes to think two things went wrong")
	}
	// The grid is replaced from what the server read back, never edited here.
	if !strings.Contains(fn, "fentry.frames = new Map()") ||
		!strings.Contains(fn, "for (const f of (detail && detail.frames) || [])") {
		t.Error("the grid is not replaced from the server's own re-read of disk - a page " +
			"that removed its own tiles and hoped the two agreed is how a frame comes to " +
			"be on screen that is not on disk")
	}
	// A clear that left frames behind leaves the button too: the warning says
	// what is in the way, and pressing again is worth doing.
	if !strings.Contains(fn, "if (framesOnDisk(entry, index) === 0) entry.unticked.delete(index)") {
		t.Error("the offer is withdrawn regardless of whether the file actually came out " +
			"clean, so a partly-failed clear would take away the button that could finish it")
	}
	// A failure that changed nothing is reported in the same place, not in
	// two places for one button.
	if !regexp.MustCompile(`(?s)catch \(err\) \{[^}]*setFileNote\(entry, index, String\(err`).MatchString(fn) {
		t.Error("a failed clear does not report beside the file")
	}

	// The note is a sibling of the row rather than inside it: the row is a
	// <label>, and a whole sentence in it would join the checkbox's name.
	render := jsFunc(t, js, "renderFileList")
	if !strings.Contains(render, `note.className = "picker-note"`) || !strings.Contains(render, "item.append(note)") {
		t.Error("the note is not built as a sibling of the row inside the list item")
	}
	if strings.Contains(render, "row.append(note)") {
		t.Error("the note is inside the row, which is a <label> - its sentence would " +
			"become part of what a screen reader announces for the checkbox")
	}
	note := cssRule(t, css, ".picker-note {")
	if !strings.Contains(note, "var(--bad)") {
		t.Errorf(".picker-note is %q, want var(--bad) - it reports something lost", note)
	}
	// A clear can report several problems at once - more than one result set,
	// or one set with more than one artefact stuck - and errors.Join
	// separates them with a newline. Without this they arrive on screen as
	// one run-on sentence.
	if !strings.Contains(note, "white-space: pre-line") {
		t.Errorf(".picker-note is %q and collapses the newlines errors.Join puts between "+
			"two problems, so two problems read as one", note)
	}
}

// TestTheDestructiveButtonDoesNotWearTheAccentUnderThePointer is the cursor
// cascade's lesson (TOR-182, and tick_test.go's own note on asserting order
// rather than describing it) applied to a colour.
//
// `button:hover:not(:disabled) { border-color: var(--accent) }` earlier in
// app.css would otherwise turn the one control that deletes a file's results
// cyan under the pointer - and the accent means "this is live, or this is
// where you are" and wears nothing else. This is settled by SPECIFICITY
// rather than by order, which is the stronger of the two: (0,3,0) beats
// (0,2,1) wherever the rule sits, where an order-based fix would silently
// stop working if somebody moved the block.
func TestTheDestructiveButtonDoesNotWearTheAccentUnderThePointer(t *testing.T) {
	css := stylesheet(t)

	rest := cssRule(t, css, ".picker-clear {")
	if !strings.Contains(rest, "var(--bad)") {
		t.Errorf(".picker-clear is %q, want var(--bad) - --warn is \"this is about to cost "+
			"you\", which the price on this same row already says, and this is not about "+
			"cost but about loss", rest)
	}
	if strings.Contains(rest, "var(--accent)") {
		t.Errorf(".picker-clear is %q and wears the accent, which means \"live, or where "+
			"you are\" (:root)", rest)
	}

	// The override, at the specificity that makes it unconditional.
	hover := cssRule(t, css, ".picker-clear:hover:not(:disabled) {")
	if !strings.Contains(hover, "var(--bad)") {
		t.Errorf(".picker-clear:hover:not(:disabled) is %q and does not restate --bad, so "+
			"the shared button rule's accent border wins", hover)
	}
	// And the shared rule it is overriding still exists, so this test cannot
	// pass by the problem having quietly gone away somewhere else.
	if !strings.Contains(css, "button:hover:not(:disabled) { border-color: var(--accent); }") {
		t.Error("the shared button hover rule this override exists for is gone or " +
			"reworded; if it really went, this override and its specificity argument " +
			"should be re-examined rather than left claiming to solve a problem nobody has")
	}

	// The hidden companion, because .picker-clear is hidden by attribute.
	if got := cssRule(t, css, ".picker-clear[hidden] {"); !strings.Contains(got, "display: none") {
		t.Errorf(".picker-clear[hidden] is %q, want display: none", got)
	}
}

// TestAClearedFileTakesItsReachStripWithIt: renderReach has no "nothing"
// state - handed an empty list it says "where the frames came from was not
// recorded for this set", which after a clear is a sentence about a set that
// no longer exists. So the strip goes off screen rather than being redrawn.
func TestAClearedFileTakesItsReachStripWithIt(t *testing.T) {
	js := servedScript(t)
	fn := jsFunc(t, js, "clearFrames")

	if !regexp.MustCompile(`(?s)if \(capturedCells\(fentry\) === 0\) \{\s*fentry\.reach\.hidden = true;`).MatchString(fn) {
		t.Error("a file with no frames left keeps its reach strip, which describes where " +
			"frames that are gone came from")
	}
	if !strings.Contains(fn, "renderReach(fentry, (detail && detail.sets) || [])") {
		t.Error("a file that still has frames does not redraw its reach strip from the " +
			"sets that are left, so the strip would go on describing a set that was cleared")
	}
	// AND THE CONTACT SHEET LINK, which the browser found and no text check
	// here did: the file's own file_done put it on screen and nothing else
	// ever removed it, so after a clear it offered a picture that had just
	// been deleted - a link whose only possible answer is a 404.
	//
	// SINCE TOR-191 IT GOES THROUGH THE FIELD, not the element: the link is
	// drawn from fentry.sheetURL, so emptying that and redrawing is the same
	// act as file_done putting it there rather than a second, opposite piece
	// of DOM handling to keep in step with the first. Both halves are
	// checked, because the field alone with no redraw would leave the link on
	// screen and the redraw alone with no field change would put it back.
	if !strings.Contains(fn, `fentry.sheetURL = "";`) ||
		!strings.Contains(fn, "renderFileLinks(fentry)") {
		t.Error("a cleared file keeps its contact-sheet link, which now points at a file " +
			"the clear removed - the field has to be emptied AND the link redrawn from it")
	}
	links := jsFunc(t, js, "renderFileLinks")
	if !strings.Contains(links, "fentry.links.hidden = links.length === 0") ||
		!strings.Contains(links, "fentry.links.replaceChildren(") {
		t.Error("renderFileLinks does not take the link off screen when there is no sheet " +
			"to link - both hidden AND emptied, so nothing later unhides a link to " +
			"something that is gone")
	}
	if !strings.Contains(fn, "fentry.plan = []") {
		t.Error("the plan survives a clear, so the grid would keep laying itself out from " +
			"points nothing can ever fill")
	}
}

// TestTheClearIsAddressedAtTheFileRatherThanOneOfItsSets: the page sends no
// params, because the grid it is clearing beside is every set's frames merged
// (Server.ClearFile carries the argument). It is asserted here because the
// alternative - sending the params of whichever frame happened to be first -
// would clear one set and leave the button standing.
func TestTheClearIsAddressedAtTheFileRatherThanOneOfItsSets(t *testing.T) {
	js := servedScript(t)
	fn := jsFunc(t, js, "clearFrames")

	if !strings.Contains(fn, `const path = ["runs", entry.infohash, "files", index, "frames"].join("/")`) {
		t.Fatal("the clear no longer addresses the file's frames as a whole")
	}
	if strings.Contains(fn, "searchParams.set(\"params\"") {
		t.Error("the clear names one result set. The row's grid merges every set this " +
			"torrent has for the file (TOR-69), so clearing one would leave frames on " +
			"screen and the button standing beside them")
	}
	// And the per-frame delete still DOES name one, which is what makes the
	// difference above a decision rather than an omission: a cross on a
	// thumbnail is about one frame of one set, and it has the set to name.
	if !strings.Contains(jsFunc(t, js, "deleteFrame"), `target.searchParams.set("params", frame.params)`) {
		t.Error("the per-frame delete no longer names its result set; if that changed, the " +
			"reasoning about why a whole-file clear does not name one has to be re-made")
	}
}

// TestTheClearGoesThroughTheRowsOwnState is a guard against the thing that
// would break this feature invisibly: entry.picked is ASSIGNED from every
// run_state message, so an un-tick recorded there would be undone by the next
// one to arrive - which for a settled row can be a queue change nobody caused.
func TestTheClearGoesThroughTheRowsOwnState(t *testing.T) {
	js := servedScript(t)
	events := eventsJS(t)
	derive := stateJS(t)

	if !strings.Contains(events, "entry.picked = new Set(ev.ticked || [])") {
		t.Fatal("events.js's run_state handler no longer assigns entry.picked; if that " +
			"changed, the reasoning below about why the un-tick needs its own set has to " +
			"be re-made")
	}
	if regexp.MustCompile(`entry\.picked\.delete\(index\)[^\n]*\n[^\n]*unticked`).MatchString(js) {
		t.Error("an un-tick removes the file from entry.picked - the server's own answer, " +
			"which the next run_state would put straight back")
	}
	// AND run_state MUST NOT TOUCH entry.unticked AT ALL, which is the other
	// half and the one the split made worth stating: the handler is now a
	// function of its own, so "it does not write this field" is a thing that
	// can be read off one place. TestRunStateAssignsTheTicksAndNeverTouchesThe
	// LocalUnTick (eventstate_test.go) runs the same claim for real.
	// Comments stripped: applyRunState's own doc names this field precisely to
	// say it does not touch it, so a substring check over the prose would
	// answer the opposite of the question.
	if strings.Contains(stripJSComments(jsFunc(t, events, "applyRunState")), "entry.unticked") {
		t.Error("events.js's run_state handler writes entry.unticked - it is the one piece " +
			"of tick state the server does not own, and anything this message did to it " +
			"would undo a local un-tick on the very next message")
	}
	// Comments stripped, as everywhere a check like this reads a function body.
	if !strings.Contains(stripJSComments(jsFunc(t, derive, "resetRunState")),
		"entry.unticked.clear()") {
		t.Error("resetRunState does not clear the offers, so a reconnecting page would " +
			"keep offering to clear a file it has yet to be told anything about")
	}

	// One declaration of the field, which is what TestNoRunEntryFieldIsDeclaredTwice
	// guards generally and what a bug found by a mutation run cost once already.
	// It is state.js's half of the entry now.
	if n := strings.Count(derive, "unticked: new Set()"); n != 1 {
		t.Errorf("state.js's newRunState declares unticked %d times, want exactly 1", n)
	}
	if strings.Contains(js, "unticked: new Set()") {
		t.Error("app.js declares unticked as well - two halves of one object, so the " +
			"second silently wins and a local un-tick is lost on the first render")
	}
}
