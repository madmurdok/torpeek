package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
	"github.com/madmurdok/torpeek/internal/sheet"
)

// ErrNoSuchFrame is what DeleteFrame reports when the run, the file or the
// frame it was asked to act on is not there. All four ways of naming nothing
// collapse into one sentinel on purpose: a caller turning this into an HTTP
// status answers 404 to every one of them, and the message says which it was
// for a person reading it.
var ErrNoSuchFrame = errors.New("no such frame")

// ErrSheetStale says the frame is gone and the contact sheet that depicted it
// is not: the delete happened, the derived artefact did not follow.
//
// It is a separate sentinel because the two facts have different consequences
// and reporting them as one made the page lie. A caller that treats every
// error from DeleteFrame as a failed delete tells a person their frame is
// still there while it is not, and only a reload corrects it - which is the
// same class of untruth as a sheet that keeps depicting a frame nobody has
// (TOR-78). Whoever handles this must report the delete as done and the
// sheet as stale.
var ErrSheetStale = errors.New("the contact sheet could not be rebuilt")

// ErrNoSuchFile is ClearFile's counterpart to ErrNoSuchFrame: the run, or the
// file inside it, is not there.
//
// A SECOND SENTINEL RATHER THAN A REUSE, and ErrNoSuchFrame's own doc is the
// reason rather than the objection. That doc argues for one sentinel because
// "a caller turning this into an HTTP status answers 404 to every one of
// them" - an argument about STATUS, and it still holds: web's deleteStatus
// answers 404 to this one too, so there is exactly one status for both. What
// it also says is that "the message says which it was for a person reading
// it", and that half is what a reuse would break here: web writes err.Error()
// straight into the response body, so a whole-file clear of a file that is
// not there would tell a person "no such frame" about something that is not a
// frame. One noun each, one status for both.
var ErrNoSuchFile = errors.New("no such file")

// ErrClearIncomplete says the clear HAPPENED and something of the file is
// still on disk.
//
// It is ErrSheetStale one size up, and for the identical reason (TOR-78): a
// caller that treats every error from ClearFile as a failed clear tells a
// person their frames are still there while the run record and the manifest
// say otherwise, and only a reload corrects it. Whoever handles this must
// report the clear as done and say what is left, beside the file.
//
// The line it is drawn at is the run record. Everything ClearFile does before
// writing that record can still be refused with nothing changed; everything
// after it is folded into this, because from that write onwards the file is
// no longer part of what the result set claims to hold, whatever bytes
// survive.
var ErrClearIncomplete = errors.New("the file was cleared and something of it is still on disk")

// DeleteFrame removes one frame from a finished run: its record in the file's
// manifest and its file on disk, leaving the rest of the run servable.
//
// It lives in core because core is the only package that writes under
// OutputRoot (internal/output and internal/cache are its own), and because
// the invariants a delete can break are cache's, not a UI's:
//
//   - The record goes with the file. cache.Usable stats every record a
//     manifest holds and loadCacheHit bails at the first failure, so removing
//     the file alone would turn the whole run - every surviving frame with it -
//     into a miss. Removing both leaves nothing to stat and the run stays a
//     hit.
//   - The manifest is written BEFORE the file is unlinked. A crash between the
//     two leaves a frame nothing references, which is harmless; the reverse
//     order leaves a manifest promising a file that is gone, which is exactly
//     the state that makes the run a miss.
//   - Survivors are never renumbered. reusableFrames keys on Frame.Index and
//     cross-checks the recorded request against points[Index], so shifting the
//     numbers down would make every record after the hole unusable and would
//     have the next run overwrite existing frames/NNN.jpg. A hole in the
//     %03d sequence costs nothing: nothing enumerates the frames directory,
//     everything reads the manifest.
//
// The delete is permanent for this result set. The file stays in run.json's
// Complete while any frame remains, so a rerun with the same parameters is
// still a hit that costs no network and serves what is left, rather than
// refetching what somebody deliberately removed; asking for another frame
// count is a different ParamsKey and therefore a different set.
//
// Nothing is locked. Nothing in this project locks the output tree, and the
// web server's single run slot means the process is not editing a run it is
// also writing - a run in flight writes its manifest once, at the end of the
// file, and a person cannot be looking at frames that do not exist yet.
func DeleteFrame(root, infoHash, params string, fileIndex, frameIndex int) error {
	layout := output.Layout{Root: root, InfoHash: infoHash, Params: params}

	record, ok := cache.LoadRun(layout.RunDir())
	if !ok {
		return fmt.Errorf("%w: no run recorded at %s", ErrNoSuchFrame, layout.RunDir())
	}

	// The record is what says where a file's results live - the path it holds
	// is what output.FileSlug turned into the directory name - rather than
	// this func re-deriving a layout of its own.
	path, ok := recordedVideoPath(record, fileIndex)
	if !ok {
		return fmt.Errorf("%w: the run at %s holds no file %d", ErrNoSuchFrame, layout.RunDir(), fileIndex)
	}

	fileDir := layout.FileDir(fileIndex, path)
	m, ok := cache.LoadManifest(fileDir)
	if !ok {
		return fmt.Errorf("%w: no readable manifest in %s", ErrNoSuchFrame, fileDir)
	}

	kept := make([]manifest.Frame, 0, len(m.Frames))
	var gone *manifest.Frame
	for _, f := range m.Frames {
		if gone == nil && f.Index == frameIndex {
			doomed := f
			gone = &doomed
			continue
		}
		kept = append(kept, f)
	}
	if gone == nil {
		return fmt.Errorf("%w: file %d of the run at %s has no frame %d",
			ErrNoSuchFrame, fileIndex, layout.RunDir(), frameIndex)
	}

	// Checked before anything is written, not at the unlink: a manifest is an
	// ordinary file in a directory a person owns, and this is the only place
	// in the project that removes a path one names. A record pointing outside
	// the run it belongs to was not written by any run of ours, so it is
	// refused outright rather than acted on and then reported.
	if gone.Path != "" && !within(layout.RunDir(), gone.Path) {
		return fmt.Errorf("frame %d of file %d points at %s, outside the run at %s",
			frameIndex, fileIndex, gone.Path, layout.RunDir())
	}

	writer, err := output.NewWriter(layout)
	if err != nil {
		return err
	}

	// The empty-manifest case is settled first, before the manifest that
	// would be empty exists. cache.Usable refuses a manifest with no frames
	// at all, so a file left in Complete with an empty manifest fails the
	// whole run; taking it out of Complete makes it silently absent from a
	// replay instead, which is what a file the run never finished already
	// does (TestReplaySelectsOnlyCompletedFiles). Crashing after this and
	// before the manifest is written leaves a whole file merely unlisted,
	// where the other order would leave the run unopenable.
	if len(kept) == 0 {
		record.Complete = withoutIndex(record.Complete, fileIndex)
		if err := cache.SaveRun(layout.RunDir(), record); err != nil {
			return fmt.Errorf("update the run record: %w", err)
		}
	}

	m.Frames = kept
	// Through the same writer a run's own manifest goes through, so an edited
	// manifest is byte for byte what a run would have written - the relative
	// frame paths included, which is also how a manifest still holding the
	// absolute paths of an older release quietly becomes portable the first
	// time somebody deletes a frame from it.
	if _, err := writer.WriteManifest(fileIndex, path, m); err != nil {
		return err
	}

	if gone.Path != "" {
		if err := os.Remove(gone.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove frame %d of file %d: %w", frameIndex, fileIndex, err)
		}
	}

	// Wrapped, not returned as it comes: everything above this line has
	// already happened, and a caller has to be able to tell "nothing was
	// deleted" from "the frame went and its sheet did not".
	if err := rebuildSheet(writer, fileDir, fileIndex, path, m); err != nil {
		return fmt.Errorf("%w: %w", ErrSheetStale, err)
	}
	return nil
}

// ClearFile removes everything one file of one result set has on disk - its
// frames, its contact sheet and its manifest - and takes it out of the run
// record's Complete, leaving the rest of the set servable.
//
// It is DeleteFrame's sibling rather than a loop over it, and that is a
// decision about what the intermediate states are rather than about
// efficiency. DeleteFrame rewrites the manifest and recomposes the sheet
// after every single removal, so clearing twelve frames through it would
// write twelve manifests and build eleven sheets of pictures that are on
// their way out - and would pass through eleven states in which the manifest
// truthfully describes a file nobody asked to keep half of. There is one
// state here: the file is part of this result, or it is not.
//
// WHETHER Selected IS ALSO TOUCHED, which the ticket left open (TOR-183):
// NO. The two lists answer different questions - Selected is what was ASKED
// FOR, Complete is what CAME OUT - and clearing bytes off a disk cannot
// change what somebody once asked for, so only the second becomes untrue and
// only the second is edited.
//
// The consequence is what settles it rather than the symmetry. With the file
// still in Selected and out of Complete, a top-up (TOR-152, web.topUpFor)
// walks Selected, finds no manifest for this file, counts nought captured of
// the plan's count and offers to take it again - so this clear is REVERSIBLE
// through machinery that already exists. Dropping it from Selected too would
// have a top-up conclude the file was never wanted and skip it, leaving
// whoever cleared it no way back but a whole new run with a whole new
// ceiling.
//
// And Complete has to go, because it is what a LISTING judges a result by
// (web.partial compares the two) and what a live rerun's cache hit requires
// per file (loadCacheHit with requireComplete). A file left listed as
// complete with nothing on disk is a set that reports itself Done while a
// twelfth of it is missing.
//
// THE ORDER, and it is DeleteFrame's order for DeleteFrame's own reasons:
//
//   - Every frame path the manifest holds is checked to be inside this run
//     BEFORE anything is written, and a record naming a file somewhere else
//     is refused outright rather than acted on and then reported (see
//     within, and DeleteFrame's own note on the same check). A manifest that
//     could not be re-anchored inside its run points at frames this clear
//     would leave standing, so a clear that acted on the rest would report
//     success over a file whose pictures are still there.
//   - The run record is written FIRST, before a single byte goes. Crashing
//     after it leaves a whole file merely unlisted - which is what a file the
//     run never finished already looks like - while the other order would
//     leave run.json promising a complete file whose manifest is gone, and
//     that is the state this ticket calls worse than no clear at all.
//   - The manifest goes before the frames, which is the same invariant
//     DeleteFrame keeps one frame at a time: a crash between the two leaves
//     frames nothing references, which is harmless, where the reverse leaves
//     a manifest promising files that are gone, which is what turns the whole
//     run into a cache miss (cache.Usable).
//   - The sheet goes after the frames it depicts, for the reason
//     rebuildSheet's own doc gives: it is derived, so a failure on it must not
//     be able to leave the clear itself half done.
//
// WHAT IT REPORTS. Nil when the file's directory is gone and nothing of it is
// left. ErrNoSuchFile when there was nothing there to act on, and a plain
// error when a path is refused or the record could not be written - in every
// one of those the disk is untouched. ErrClearIncomplete once the record has
// been written and something survived: the file is out of the result either
// way, and the caller has to be able to tell that from "nothing happened"
// (see that sentinel, and TOR-78).
//
// It is idempotent, and the record is rewritten even when the disk is already
// swept, deliberately: that is exactly the state a crashed clear leaves - the
// bytes gone, the file still listed - and a second press has to be able to
// repair it rather than deciding there is nothing to do.
//
// Nothing is locked, for the reason DeleteFrame states at greater length: the
// web server offers this only on a file of a settled row, so the process is
// not clearing a file it is also writing.
func ClearFile(root, infoHash, params string, fileIndex int) error {
	layout := output.Layout{Root: root, InfoHash: infoHash, Params: params}

	record, ok := cache.LoadRun(layout.RunDir())
	if !ok {
		return fmt.Errorf("%w: no run recorded at %s", ErrNoSuchFile, layout.RunDir())
	}

	// The record is what says where a file's results live, exactly as in
	// DeleteFrame: the path it holds is what output.FileSlug turned into the
	// directory name, rather than this func re-deriving a layout of its own.
	path, ok := recordedVideoPath(record, fileIndex)
	if !ok {
		return fmt.Errorf("%w: the run at %s holds no file %d", ErrNoSuchFile, layout.RunDir(), fileIndex)
	}

	fileDir := layout.FileDir(fileIndex, path)

	// A MISSING MANIFEST IS NOT AN ERROR HERE, which is where this parts
	// company with DeleteFrame: that one needs a record to remove, while this
	// one needs the file to end up with nothing, and a file selected by a run
	// that never finished it has no manifest to begin with. What the manifest
	// is read for is the check below.
	if m, loaded := cache.LoadManifest(fileDir); loaded {
		for _, f := range m.Frames {
			if f.Path != "" && !within(layout.RunDir(), f.Path) {
				return fmt.Errorf("frame %d of file %d points at %s, outside the run at %s",
					f.Index, fileIndex, f.Path, layout.RunDir())
			}
		}
	}

	// THE COMMIT POINT. Nothing above this line has changed anything; nothing
	// below it may be reported as a refusal.
	record.Complete = withoutIndex(record.Complete, fileIndex)
	if err := cache.SaveRun(layout.RunDir(), record); err != nil {
		return fmt.Errorf("update the run record: %w", err)
	}

	var left []error
	if err := os.Remove(filepath.Join(fileDir, manifest.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		left = append(left, fmt.Errorf("remove the manifest of file %d: %w", fileIndex, err))
	}
	// RemoveAll on the frames DIRECTORY rather than a walk of the manifest's
	// records, and both halves of that are deliberate. The directory is
	// derived entirely from root, infohash, params and output.FileSlug - never
	// from anything a manifest says - so it is safe by construction in the way
	// within's own doc describes, and it also sweeps a frame the manifest has
	// forgotten: a per-frame delete that crashed between its two writes leaves
	// exactly such an orphan (DeleteFrame's order), and a clear that left it
	// standing would be a clear a person can see the result of.
	if err := os.RemoveAll(layout.FramesDir(fileIndex, path)); err != nil {
		left = append(left, fmt.Errorf("remove the frames of file %d: %w", fileIndex, err))
	}
	if err := os.Remove(filepath.Join(fileDir, output.SheetName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		left = append(left, fmt.Errorf("remove the contact sheet of file %d: %w", fileIndex, err))
	}
	// THE READ-BACK IS THE REPORT, and it is asked only when nothing above it
	// has already answered. Every removal above says whether its call was
	// ACCEPTED; this says whether the file is actually gone, which is the
	// question ErrClearIncomplete exists to carry - so it is asked once, on
	// the directory, instead of trusted three times.
	//
	// Skipped when something already failed, because then it has nothing to
	// add and something to cost: the browser check for TOR-183 read a warning
	// beside a file that stated one stuck contact sheet twice in different
	// words, once as "remove the sheet: directory not empty" and once as "the
	// directory still holds sheet.jpg", which is a report a person has to
	// read twice to find out it is one problem. The case this is FOR is the
	// one where all three calls above claimed to succeed - the only case
	// where "accepted" and "gone" can disagree, and the only one where
	// nothing else would ever say so.
	if len(left) == 0 {
		if err := removeEmptied(fileDir); err != nil {
			left = append(left, err)
		}
	}

	if len(left) > 0 {
		return fmt.Errorf("%w: %w", ErrClearIncomplete, errors.Join(left...))
	}
	return nil
}

// removeEmptied takes away a file's own result directory once ClearFile has
// emptied it, and reports what is in the way when it has not.
//
// The listing is the load-bearing half rather than the removal: it is read
// AFTER the three removals above, so it is the one thing that can tell a
// clear that worked from three calls that returned nil over a file still on
// disk - a symlinked frame RemoveAll declined to follow, a half-written
// .tmp-* from a crashed atomic write (output.writeAtomic leaves them in the
// destination directory), anything a later release puts here. The names go
// into the message because a person reading a warning beside a file needs to
// know what to go and look at.
//
// An absent directory is success, not an error: it is what a file cleared
// twice looks like, and what a file the run never wrote anything for looks
// like.
func removeEmptied(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s back to check it is empty: %w", dir, err)
	}
	if len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		return fmt.Errorf("%s still holds %s", dir, strings.Join(names, ", "))
	}
	if err := os.Remove(dir); err != nil {
		return fmt.Errorf("remove the emptied %s: %w", dir, err)
	}
	return nil
}

// rebuildSheet composes the contact sheet again from what the file has left.
//
// The sheet is derived, so it is rebuilt after the manifest and the frame are
// already gone: a failure here is worth reporting, but it must not be able to
// leave the delete itself half done. sheet.Build needs no ffmpeg - it reads
// the frames back off disk and lays them out in pure Go - so this works on a
// replay-only process exactly as it does inside a run.
//
// A file with nothing left loses its sheet rather than keeping one that
// depicts frames that no longer exist (and that sheet.Build could not
// recompose anyway: it refuses an empty plan).
func rebuildSheet(writer *output.Writer, fileDir string, fileIndex int, path string, m manifest.Manifest) error {
	if len(m.Frames) == 0 {
		if err := os.Remove(filepath.Join(fileDir, output.SheetName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove the contact sheet of file %d: %w", fileIndex, err)
		}
		return nil
	}

	points, tiles := sheetTiles(m.Frames)
	data, err := sheet.Build(points, tiles, m.Video.Width, m.Video.Height)
	if err != nil {
		return fmt.Errorf("compose sheet: %w", err)
	}
	_, err = writer.WriteFile(fileIndex, path, output.SheetName, data)
	return err
}

// sheetTiles turns the surviving records into the pair sheet.Build wants.
//
// sheet.Build does NOT walk records in order: it indexes them by Frame.Index
// and then draws, for every position i in points, whatever record carries
// Index i - a position with no record becoming a dark "not attempted"
// placeholder. So points is an index space, not a list of the timecodes on
// hand, and handing it the surviving records with the original plan's points
// would leave a red placeholder where the deleted frame was, claiming a
// capture point that failed where in fact somebody removed it.
//
// The tiles are therefore renumbered 0..n-1 for the sheet alone - a copy that
// never touches the manifest, whose own indices must never move (see
// DeleteFrame) - and points is each survivor's own requested timecode in that
// same order, which is exactly the pairing planFromManifest already assumes.
// A sheet rebuilt this way is the run's remaining frames and nothing else.
//
// The one thing this cannot reproduce is the tail of a plan a budget stop cut
// short: those points had no record to begin with, so a rebuild from the
// manifest has nothing to place them from and the sheet loses their
// placeholders. The manifest, which is what anything machine-readable
// consults, is unchanged.
func sheetTiles(records []manifest.Frame) ([]time.Duration, []manifest.Frame) {
	tiles := make([]manifest.Frame, len(records))
	copy(tiles, records)
	// A run writes its records in plan order; sorting says so rather than
	// trusting it, since the order is what the sheet's own layout becomes.
	sort.SliceStable(tiles, func(i, j int) bool { return tiles[i].Index < tiles[j].Index })

	points := make([]time.Duration, len(tiles))
	for i := range tiles {
		points[i] = time.Duration(tiles[i].RequestedMS) * time.Millisecond
		tiles[i].Index = i
	}
	return points, tiles
}

// recordedVideoPath finds one video file's path in a run record.
func recordedVideoPath(record cache.Run, index int) (string, bool) {
	for _, v := range record.Videos {
		if v.Index == index {
			return v.Path, true
		}
	}
	return "", false
}

// withoutIndex is mergeIndices' one counterpart: the only place anything is
// ever taken out of run.json's Complete. It keeps the list's sorted, non-nil
// shape so a record edited here still serializes the way a run's own does.
//
// TWO CALLERS, AND NEITHER TOUCHES Selected. DeleteFrame reaches it for a
// file whose LAST frame was deleted, which cache.Usable would otherwise fail
// the whole run over; ClearFile reaches it for every file it clears, which is
// the same fact arrived at in one step. That both stop at Complete is the
// stated answer to TOR-183's open question - see ClearFile's own doc for why
// what was ASKED FOR cannot be made untrue by a delete, and for what keeping
// it buys.
func withoutIndex(indices []int, drop int) []int {
	out := make([]int, 0, len(indices))
	for _, i := range indices {
		if i != drop {
			out = append(out, i)
		}
	}
	sort.Ints(out)
	return out
}

// within reports whether path sits inside dir, which is how a manifest record
// is checked to be describing a frame of its own run before it is unlinked.
//
// It compares the two as spelled, without resolving symlinks, and that is
// deliberate now rather than merely unexamined (TOR-77). Since a manifest
// records a frame relative to itself (TOR-60), the path reaching here was
// built by joining onto the very directory the caller named, so the two
// sides cannot disagree about spelling however the caller spelled it - a
// delete through /tmp of a run recorded under /private/tmp works, and
// TestDeleteFrameThroughARootSpelledAnotherWay holds that.
//
// What is left for this to refuse is a record that names a file somewhere
// else entirely - an older manifest whose absolute path could not be
// re-anchored inside the run it belongs to. That is precisely the record
// that must not be unlinked, so resolving symlinks here would only make it
// easier to act on.
//
// What reaches it changed with TOR-60, and the contract is worth stating
// exactly. A frame path now arrives from cache.LoadManifest already resolved
// against the file directory this very call derived from root/infoHash/params
// (manifest.Resolved), so for anything a 0.8.0 run wrote the two arguments
// are built by joining onto one and the same root string and this can no
// longer answer no: it is comparing a prefix against itself, whatever
// spelling of the root the caller was started with.
//
// It still earns its place for the one shape that does not: a manifest from
// an earlier release whose absolute path could not be re-anchored inside the
// run, which is a record naming a file somewhere else entirely and exactly
// what must not be unlinked. That is the only case left where this compares
// two independently-spelled paths - and the case TOR-77 is about, since
// /tmp and /private/tmp are one directory that Clean cannot make one string.
func within(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !filepath.IsAbs(rel) &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
