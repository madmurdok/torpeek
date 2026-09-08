package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
)

// TOR-183: clearing one file of one result set.
//
// These tests are about the two things a clear can get wrong and no amount of
// UI can correct: what is left on disk, and what the run record says
// afterwards. The record is read back off disk in every one of them rather
// than inspected in memory, because a clear that changed nothing on disk
// would satisfy an in-memory assertion perfectly.

// clearParams is a result set spelled the way core.ParamsKey spells one, so
// the same fixture is addressable through web's own validated routes.
const clearParams = "00112233445566ff"

// oneFileRun is the record a run of one video file writes: asked for, and
// came out whole.
func oneFileRun(path string) cache.Run {
	return cache.Run{
		Version: cache.Version, InfoHash: deleteHash, Name: "Clear One File",
		Plan:     cache.Plan{Count: 4, Start: 0.1, End: 0.9},
		Videos:   []cache.File{{Index: 0, Path: path, Bytes: 1 << 20}},
		Selected: []int{0}, Complete: []int{0},
	}
}

// readRun reads a run record back off disk and fails the test if it cannot,
// since every assertion here is about what the record says after a clear.
func readRun(t *testing.T, layout output.Layout) cache.Run {
	t.Helper()

	run, ok := cache.LoadRun(layout.RunDir())
	if !ok {
		t.Fatalf("no readable run record in %s", layout.RunDir())
	}
	return run
}

// TestClearFileTakesTheFramesTheSheetAndTheManifestTogether is the ticket's
// own sentence in one place: everything the file had is gone, and the set it
// belonged to is still readable.
//
// The three are asserted separately on purpose, for the reason
// TestDeleteFrameTakesTheRecordAndTheFileTogether gives about its own two:
// removing the frames alone leaves a manifest promising files that are gone,
// which turns the whole run into a cache miss and takes the OTHER files with
// it; removing the manifest alone leaves pictures on disk that nothing will
// ever clean up, and a person who pressed "clear frames" can still find them.
// Neither alone is this feature.
func TestClearFileTakesTheFramesTheSheetAndTheManifestTogether(t *testing.T) {
	root := t.TempDir()

	m := fileManifest(0, "movie.mkv", 4)
	layout := buildCachedRun(t, root, deleteHash, clearParams, oneFileRun("movie.mkv"),
		map[int]manifest.Manifest{0: m})

	fileDir := layout.FileDir(0, "movie.mkv")
	before := readManifest(t, fileDir)
	writeSheetFor(t, layout, 0, "movie.mkv", before)

	// Every path this is supposed to unlink, held before the call - so the
	// assertions below name the actual files rather than re-deriving where
	// they should have been from the same code under test.
	framesDir := layout.FramesDir(0, "movie.mkv")
	doomed := make([]string, 0, len(before.Frames))
	for _, f := range before.Frames {
		if _, err := os.Stat(f.Path); err != nil {
			t.Fatalf("the frame to clear is not on disk: %v", err)
		}
		doomed = append(doomed, f.Path)
	}
	if _, err := os.Stat(filepath.Join(fileDir, output.SheetName)); err != nil {
		t.Fatalf("the sheet to clear is not on disk: %v", err)
	}

	if err := ClearFile(root, deleteHash, clearParams, 0); err != nil {
		t.Fatalf("ClearFile: %v", err)
	}

	for _, path := range doomed {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still on disk after the clear (err = %v) - the frames are "+
				"what a person pressed the button about", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(fileDir, manifest.Name),
		filepath.Join(fileDir, output.SheetName),
		framesDir,
		// And the file's own directory, emptied and therefore taken away:
		// "the file is clean" is not true of a tree that still has a folder
		// named after it.
		fileDir,
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the clear (err = %v)", path, err)
		}
	}

	// The set itself is still a set: run.json is readable, and the record
	// says the file is no longer complete.
	run := readRun(t, layout)
	if len(run.Complete) != 0 {
		t.Errorf("run.json still calls file(s) %v complete, want none - the one file it "+
			"had has nothing on disk", run.Complete)
	}
}

// TestAClearedFileStaysAskedForAndStopsBeingComplete is the decision TOR-183
// left open, asserted as behaviour rather than left in a comment.
//
// Selected is WHAT WAS ASKED FOR and Complete is WHAT CAME OUT. Clearing
// bytes off a disk cannot change what somebody once asked for, so only the
// second becomes untrue and only the second is edited - and the consequence
// is what settles it rather than the symmetry: a top-up walks Selected
// (web.topUpFor), so a file still listed there with no manifest is one it
// offers to take again. Drop it from Selected too and a top-up concludes the
// file was never wanted, which leaves whoever cleared it no way back but a
// whole new run with a whole new ceiling.
func TestAClearedFileStaysAskedForAndStopsBeingComplete(t *testing.T) {
	root := t.TempDir()

	run := oneFileRun("movie.mkv")
	run.Videos = append(run.Videos, cache.File{Index: 1, Path: "extra.mkv", Bytes: 1 << 20})
	run.Selected = []int{0, 1}
	run.Complete = []int{0, 1}
	layout := buildCachedRun(t, root, deleteHash, clearParams, run, map[int]manifest.Manifest{
		0: fileManifest(0, "movie.mkv", 4),
		1: fileManifest(1, "extra.mkv", 4),
	})

	if err := ClearFile(root, deleteHash, clearParams, 1); err != nil {
		t.Fatalf("ClearFile: %v", err)
	}

	after := readRun(t, layout)
	if !equalInts(after.Selected, []int{0, 1}) {
		t.Errorf("run.json now says %v was asked for, want [0 1] - a delete cannot make it "+
			"untrue that somebody asked for the file, and a top-up reads exactly this list "+
			"to decide what it may take again", after.Selected)
	}
	if !equalInts(after.Complete, []int{0}) {
		t.Errorf("run.json says %v came out whole, want [0] - a file left listed as complete "+
			"with nothing on disk makes the set report itself Done while part of it is gone",
			after.Complete)
	}
}

// TestAClearedFileIsWhatATopUpSEESAsMissing is the reversibility claim above,
// checked from the reader's own side rather than asserted about the record.
//
// It reproduces exactly what web.topUpFor does with the two lists - walk
// Selected, count the frames each file's manifest actually has - because that
// is the machinery the whole "Selected stays" decision rests on, and a claim
// about another package's reading is worth checking rather than trusting.
func TestAClearedFileIsWhatATopUpSeesAsMissing(t *testing.T) {
	root := t.TempDir()

	run := oneFileRun("movie.mkv")
	layout := buildCachedRun(t, root, deleteHash, clearParams, run,
		map[int]manifest.Manifest{0: fileManifest(0, "movie.mkv", 4)})

	if err := ClearFile(root, deleteHash, clearParams, 0); err != nil {
		t.Fatalf("ClearFile: %v", err)
	}

	after := readRun(t, layout)
	if len(after.Selected) == 0 {
		t.Fatal("nothing is listed as asked for, so a top-up has nothing to walk")
	}

	remaining := 0
	for _, index := range after.Selected {
		path, ok := recordedVideoPath(after, index)
		if !ok {
			t.Fatalf("the record names file %d as selected and does not list it", index)
		}
		captured := 0
		if m, loaded := cache.LoadManifest(layout.FileDir(index, path)); loaded {
			for _, f := range m.Frames {
				if f.Shift != manifest.ShiftFailed && f.Path != "" {
					captured++
				}
			}
		}
		remaining += after.Plan.Count - captured
	}
	if remaining != after.Plan.Count {
		t.Errorf("a top-up would find %d frames outstanding, want the plan's whole %d - a "+
			"cleared file has to read as asked for and entirely uncaptured, or there is no "+
			"way back from the button", remaining, after.Plan.Count)
	}
}

// TestClearFileLeavesTheOtherFilesResultsAlone: a season pack is the whole
// reason this is per file, and the sibling has to come out of it untouched -
// its frames, its manifest and its standing in the record.
func TestClearFileLeavesTheOtherFilesResultsAlone(t *testing.T) {
	root := t.TempDir()

	run := oneFileRun("s01e01.mkv")
	run.Videos = append(run.Videos, cache.File{Index: 1, Path: "s01e02.mkv", Bytes: 1 << 20})
	run.Selected = []int{0, 1}
	run.Complete = []int{0, 1}
	layout := buildCachedRun(t, root, deleteHash, clearParams, run, map[int]manifest.Manifest{
		0: fileManifest(0, "s01e01.mkv", 3),
		1: fileManifest(1, "s01e02.mkv", 3),
	})

	keptDir := layout.FileDir(1, "s01e02.mkv")
	kept := readManifest(t, keptDir)

	if err := ClearFile(root, deleteHash, clearParams, 0); err != nil {
		t.Fatalf("ClearFile: %v", err)
	}

	if got := readManifest(t, keptDir); len(got.Frames) != len(kept.Frames) {
		t.Errorf("the other file's manifest lists %d frames, want its original %d",
			len(got.Frames), len(kept.Frames))
	}
	for _, f := range kept.Frames {
		if _, err := os.Stat(f.Path); err != nil {
			t.Errorf("the other file lost %s: %v", f.Path, err)
		}
	}
	if !equalInts(readRun(t, layout).Complete, []int{1}) {
		t.Errorf("run.json says %v came out whole, want just the file that was not cleared",
			readRun(t, layout).Complete)
	}
}

// TestTheRunStillOpensAfterAClear is the invariant a clear must not break: a
// person clears one episode of a season pack and reopens the set, and every
// other episode is still there. The cleared one is simply absent rather than
// announced as a failure nobody recorded.
//
// WHAT THIS DOES NOT PROVE, said out loud because the mutation that catches
// it is not the one a reader would guess. ClearFile removes the manifest
// BEFORE the frames, and the argument for that order is about the crash
// window between the two - a manifest left promising files that are gone
// fails cache.Usable and takes the whole run with it. No test here can
// observe that: it needs the process to die between two statements. What this
// one catches is a clear that reaches the wrong file's frames, leaving a
// manifest beside pictures that are gone - the same end state the bad order
// would produce, arrived at by a bug a test CAN cause
// (C5-clears-the-wrong-files-frames in this ticket's falsification table).
func TestTheRunStillOpensAfterAClear(t *testing.T) {
	root := t.TempDir()

	run := oneFileRun("kept.mkv")
	run.Videos = append(run.Videos, cache.File{Index: 1, Path: "gone.mkv", Bytes: 1 << 20})
	run.Selected = []int{0, 1}
	run.Complete = []int{0, 1}
	buildCachedRun(t, root, deleteHash, clearParams, run, map[int]manifest.Manifest{
		0: fileManifest(0, "kept.mkv", 3),
		1: fileManifest(1, "gone.mkv", 3),
	})

	if err := ClearFile(root, deleteHash, clearParams, 1); err != nil {
		t.Fatalf("ClearFile: %v", err)
	}

	engine := NewEngine(ffmpeg.Tools{})
	started := map[int]int{}
	frames := map[int]int{}
	var done bool
	for _, ev := range collect(t, engine.Replay(root, deleteHash, clearParams)) {
		switch e := ev.(type) {
		case FileStarted:
			started[e.File]++
		case FrameReady:
			frames[e.File]++
		case Failed:
			t.Fatalf("the run no longer opens after a clear: %s: %v", e.Code, e.Err)
		case Done:
			done = true
		}
	}
	if !done {
		t.Fatal("the replay produced no terminal event")
	}
	if started[0] != 1 || frames[0] != 3 {
		t.Errorf("the surviving file announced %d starts and %d frames, want 1 and 3 - a "+
			"clear of its neighbour must not cost it anything", started[0], frames[0])
	}
	if started[1] != 0 || frames[1] != 0 {
		t.Errorf("the cleared file still announces %d starts and %d frames, want none",
			started[1], frames[1])
	}
}

// TestClearFileRefusesAManifestThatPointsOutsideItsRun is the check every
// path is put through BEFORE anything is written, and the whole reason it
// happens first: such a record names frames this clear cannot reach, so
// sweeping the rest and reporting success would tell a person their file is
// gone while its pictures are still where they can see them.
//
// It is the one shape `within` still has to refuse (see its own doc): a
// manifest from an earlier release whose absolute path could not be
// re-anchored inside the run it was read from.
func TestClearFileRefusesAManifestThatPointsOutsideItsRun(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	layout := buildCachedRun(t, root, deleteHash, clearParams, oneFileRun("movie.mkv"),
		map[int]manifest.Manifest{0: fileManifest(0, "movie.mkv", 3)})

	fileDir := layout.FileDir(0, "movie.mkv")
	stray := filepath.Join(outside, "stolen.jpg")
	if err := os.WriteFile(stray, []byte{0xFF, 0xD8, 0x01}, 0o644); err != nil {
		t.Fatalf("write the frame outside the run: %v", err)
	}

	// Rewritten by hand rather than through output.Writer, because the
	// writer's whole job is to make this impossible (WriteManifest
	// relativizes every path against the directory it lands in) - the only
	// way such a record exists is a manifest from before TOR-60.
	m := readManifest(t, fileDir)
	survivors := make([]string, 0, len(m.Frames))
	for _, f := range m.Frames {
		survivors = append(survivors, f.Path)
	}
	m.Frames[1].Path = stray
	writeRawManifest(t, fileDir, m)

	err := ClearFile(root, deleteHash, clearParams, 0)
	if err == nil {
		t.Fatal("ClearFile accepted a manifest naming a frame outside the run")
	}
	if errors.Is(err, ErrClearIncomplete) {
		t.Errorf("the refusal is reported as %v, which says the clear HAPPENED - it must "+
			"not have, because nothing may be removed on the strength of a record that "+
			"describes another run's disk", ErrClearIncomplete)
	}

	// And nothing was touched, which is what "refused" has to mean.
	if _, statErr := os.Stat(stray); statErr != nil {
		t.Errorf("the frame outside the run was removed anyway: %v", statErr)
	}
	for _, path := range survivors[:1] {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("a frame inside the run was removed by a refused clear: %v", statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(fileDir, manifest.Name)); statErr != nil {
		t.Errorf("the manifest was removed by a refused clear: %v", statErr)
	}
	if !equalInts(readRun(t, layout).Complete, []int{0}) {
		t.Error("a refused clear edited the run record; the check that refuses it runs " +
			"before the record is written for exactly this reason")
	}
}

// TestAClearThatCouldNotTakeEverythingStillHappened is TOR-78's shape at a
// file's size, and the reason ErrClearIncomplete exists: the record and the
// manifest already say the file is not part of this result, so a caller told
// "the clear failed" would report frames as present that nothing accounts
// for, and only a reload would correct it.
//
// The sheet is made unremovable by putting a non-empty DIRECTORY where the
// file belongs - the same technique
// TestADeleteWhoseSheetCannotBeRebuiltStillHappened uses one level up, and
// for the same reason: it fails exactly one of the removals, where denying
// the directory's permissions would have failed the manifest's too and left
// nothing to report.
func TestAClearThatCouldNotTakeEverythingStillHappened(t *testing.T) {
	root := t.TempDir()

	layout := buildCachedRun(t, root, deleteHash, clearParams, oneFileRun("movie.mkv"),
		map[int]manifest.Manifest{0: fileManifest(0, "movie.mkv", 3)})

	fileDir := layout.FileDir(0, "movie.mkv")
	before := readManifest(t, fileDir)
	sheetPath := filepath.Join(fileDir, output.SheetName)
	if err := os.MkdirAll(filepath.Join(sheetPath, "occupied"), 0o755); err != nil {
		t.Fatalf("put a non-empty directory where the sheet goes: %v", err)
	}

	err := ClearFile(root, deleteHash, clearParams, 0)
	if err == nil {
		t.Fatal("ClearFile reported a clean sweep over a sheet it could not remove")
	}
	if !errors.Is(err, ErrClearIncomplete) {
		t.Fatalf("ClearFile answered %v, want it wrapped in %v - a caller has to be able "+
			"to tell \"nothing happened\" from \"it happened and something is left\"",
			err, ErrClearIncomplete)
	}
	// The message has to name what is in the way, because it is read by a
	// person beside the file rather than parsed by anything.
	if !strings.Contains(err.Error(), output.SheetName) {
		t.Errorf("the warning is %q and does not name %s - it is the only thing telling "+
			"somebody what to go and look at", err, output.SheetName)
	}
	// AND IT SAYS IT ONCE. The browser check for this ticket read a warning
	// that stated one stuck sheet twice in different words - the removal's own
	// error, and then the directory read-back naming the same file - which is
	// a report a person has to read twice to discover it is one problem. The
	// read-back is skipped when something has already answered.
	if n := strings.Count(err.Error(), output.SheetName); n != 1 {
		t.Errorf("the warning names %s %d times, want once: %q", output.SheetName, n, err)
	}
	if n := strings.Count(err.Error(), ErrClearIncomplete.Error()); n != 1 {
		t.Errorf("the warning states %q %d times, want once: %q",
			ErrClearIncomplete, n, err)
	}

	// AND THE CLEAR HAPPENED, which is the whole claim: everything that could
	// go, went, and the record no longer counts the file.
	for _, f := range before.Frames {
		if _, statErr := os.Stat(f.Path); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("%s survived a clear that reported only the sheet (err = %v)", f.Path, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(fileDir, manifest.Name)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the manifest survived (err = %v)", statErr)
	}
	if len(readRun(t, layout).Complete) != 0 {
		t.Error("run.json still counts the file complete - the record is written BEFORE any " +
			"byte goes precisely so a failure later cannot leave this untrue")
	}
}

// TestAClearThatLeftSomethingNobodyRemovedSaysSo is what the directory
// read-back exists for, and the only case that now reaches it: all three
// removals reported success and the file is STILL not gone.
//
// It is the difference between checking a return code and checking the
// effect. Nothing above the read-back knows about a file it was not told to
// remove - a frame orphaned by a per-frame delete that died between its two
// writes, a half-written .tmp-* from an atomic write that never finished
// (output.writeAtomic leaves those in the destination directory) - so without
// this a clear would answer nil over a directory a person can still open and
// find pictures in.
//
// This is also the discrimination check for the gate that skips the read-back
// when something already failed (see ClearFile): if that gate skipped it
// always, this test would fail.
func TestAClearThatLeftSomethingNobodyRemovedSaysSo(t *testing.T) {
	root := t.TempDir()

	layout := buildCachedRun(t, root, deleteHash, clearParams, oneFileRun("movie.mkv"),
		map[int]manifest.Manifest{0: fileManifest(0, "movie.mkv", 3)})

	fileDir := layout.FileDir(0, "movie.mkv")
	before := readManifest(t, fileDir)
	stray := filepath.Join(fileDir, ".frame-orphaned")
	if err := os.WriteFile(stray, []byte{0xFF, 0xD8, 0x02}, 0o644); err != nil {
		t.Fatalf("leave an orphan behind: %v", err)
	}

	err := ClearFile(root, deleteHash, clearParams, 0)
	if !errors.Is(err, ErrClearIncomplete) {
		t.Fatalf("ClearFile answered %v, want %v - the three removals all succeeded and "+
			"the file is still on disk, which only reading the directory back can tell",
			err, ErrClearIncomplete)
	}
	if !strings.Contains(err.Error(), ".frame-orphaned") {
		t.Errorf("the warning is %q and does not name what is left", err)
	}

	// Everything it WAS asked to remove went, and the directory stayed
	// because it is not empty.
	for _, f := range before.Frames {
		if _, statErr := os.Stat(f.Path); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("%s survived (err = %v)", f.Path, statErr)
		}
	}
	if _, statErr := os.Stat(stray); statErr != nil {
		t.Errorf("the orphan was removed after all: %v - a clear removes the frames, the "+
			"sheet and the manifest, and reports anything else rather than deciding for "+
			"somebody what else in their directory may go", statErr)
	}
	if len(readRun(t, layout).Complete) != 0 {
		t.Error("run.json still counts the file complete")
	}
}

// TestClearFileNamesNothingItCannotFind: a clear addressed at a set that is
// not there, or a file the set does not list, reports ErrNoSuchFile - which
// web answers 404 to - rather than inventing an empty success.
//
// Both arms in one test because they are one sentinel and one status, and the
// only thing that differs is the sentence.
func TestClearFileNamesNothingItCannotFind(t *testing.T) {
	root := t.TempDir()
	buildCachedRun(t, root, deleteHash, clearParams, oneFileRun("movie.mkv"),
		map[int]manifest.Manifest{0: fileManifest(0, "movie.mkv", 2)})

	for _, c := range []struct {
		name   string
		params string
		index  int
		says   string
	}{
		{"no such set", "ffffffffffffffff", 0, "no run recorded"},
		{"no such file in the set", clearParams, 7, "holds no file 7"},
	} {
		err := ClearFile(root, deleteHash, c.params, c.index)
		if !errors.Is(err, ErrNoSuchFile) {
			t.Errorf("%s: ClearFile answered %v, want %v", c.name, err, ErrNoSuchFile)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: the message is %q and does not say which it was", c.name, err)
		}
		// The noun matters because web writes this straight into a response
		// body a person reads: a whole-file clear must not report "no such
		// frame" (see ErrNoSuchFile's own doc for why it is a second
		// sentinel rather than a reuse).
		if strings.Contains(err.Error(), "no such frame") {
			t.Errorf("%s: the message is %q and calls a file a frame", c.name, err)
		}
	}
}

// TestClearingAnAlreadyCleanFileRepairsItsRecord is why the record is
// rewritten even when there is nothing on disk left to remove.
//
// The state it describes is not hypothetical: it is exactly what a clear that
// died between its own writes leaves behind - the bytes gone, the file still
// listed as complete - and it is the state the ticket calls worse than no
// clear at all, since a listing would go on reporting the set whole. A second
// press has to be able to repair it, so "the disk is already swept" cannot be
// allowed to mean "there is nothing to do".
func TestClearingAnAlreadyCleanFileRepairsItsRecord(t *testing.T) {
	root := t.TempDir()

	layout := buildCachedRun(t, root, deleteHash, clearParams, oneFileRun("movie.mkv"),
		map[int]manifest.Manifest{0: fileManifest(0, "movie.mkv", 3)})

	// The first clear takes the disk with it; the record is then put back the
	// way a crash between the two writes would have left it.
	if err := ClearFile(root, deleteHash, clearParams, 0); err != nil {
		t.Fatalf("the first clear: %v", err)
	}
	crashed := readRun(t, layout)
	crashed.Complete = []int{0}
	if err := cache.SaveRun(layout.RunDir(), crashed); err != nil {
		t.Fatalf("restore the record a crashed clear would have left: %v", err)
	}

	if err := ClearFile(root, deleteHash, clearParams, 0); err != nil {
		t.Fatalf("clearing an already-swept file: %v, want it to succeed and fix the "+
			"record - an idempotent clear is the only thing that can repair this", err)
	}
	if len(readRun(t, layout).Complete) != 0 {
		t.Errorf("run.json still counts file 0 complete after a second clear, so nothing " +
			"can ever repair a record left behind by a clear that died mid-way")
	}
}

// writeRawManifest puts a manifest on disk WITHOUT going through
// output.Writer, so a test can write the one shape the writer exists to make
// impossible: a record holding an absolute path outside its own run.
func writeRawManifest(t *testing.T, fileDir string, m manifest.Manifest) {
	t.Helper()

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("encode the manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, manifest.Name), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
}
