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

	return rebuildSheet(writer, fileDir, fileIndex, path, m)
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
// ever taken out of run.json's Complete. It exists for a single case - a file
// whose last frame was deleted, which cache.Usable would otherwise fail the
// whole run over - and keeps the list's sorted, non-nil shape so a record
// edited here still serializes the way a run's own does.
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
