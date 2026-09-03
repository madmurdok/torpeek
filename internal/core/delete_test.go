package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
	"github.com/madmurdok/torpeek/internal/sheet"
	"github.com/madmurdok/torpeek/internal/swarm"
)

// deleteHash is a real-shaped infohash, so the same fixture can be addressed
// both by DeleteFrame (which only joins it into a path) and through a magnet
// URI, which refuses anything that is not a digest.
const deleteHash = "e4d37e62d14ba96d29b9e760148803b458aee5b6"

// writeSheetFor composes the contact sheet a run would have written for these
// records and puts it where one would be, so a test can tell a rebuilt sheet
// from a stale one.
func writeSheetFor(t *testing.T, layout output.Layout, fileIndex int, path string, m manifest.Manifest) []byte {
	t.Helper()

	points := make([]time.Duration, len(m.Frames))
	for i, f := range m.Frames {
		points[i] = time.Duration(f.RequestedMS) * time.Millisecond
	}
	data, err := sheet.Build(points, m.Frames, m.Video.Width, m.Video.Height)
	if err != nil {
		t.Fatalf("build the original sheet: %v", err)
	}

	writer, err := output.NewWriter(layout)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if _, err := writer.WriteFile(fileIndex, path, output.SheetName, data); err != nil {
		t.Fatalf("write the original sheet: %v", err)
	}
	return data
}

// readManifest reads one back and fails the test if it cannot, since every
// assertion here is about what a manifest says after a delete.
func readManifest(t *testing.T, fileDir string) manifest.Manifest {
	t.Helper()

	m, ok := cache.LoadManifest(fileDir)
	if !ok {
		t.Fatalf("no readable manifest in %s", fileDir)
	}
	return m
}

func frameIndices(m manifest.Manifest) []int {
	out := make([]int, 0, len(m.Frames))
	for _, f := range m.Frames {
		out = append(out, f.Index)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDeleteFrameTakesTheRecordAndTheFileTogether is the whole point of
// TOR-70 in one place: both halves go, and everything that made the run
// servable is still true afterwards.
//
// The two halves are asserted separately on purpose. Removing only the file
// is what a person can already do with rm, and it costs them the whole run
// (TestReplayMissesWhenAFrameIsGone); removing only the record leaves an
// orphan on disk that nothing will ever clean up. Neither alone is this
// feature.
func TestDeleteFrameTakesTheRecordAndTheFileTogether(t *testing.T) {
	const params = "00112233445566ff"
	root := t.TempDir()

	run := cache.Run{
		Version: cache.Version, InfoHash: deleteHash, Name: "Delete One Frame",
		Plan:     cache.Plan{Count: 5, Start: 0.1, End: 0.9},
		Videos:   []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}},
		Selected: []int{0}, Complete: []int{0},
	}
	layout := buildCachedRun(t, root, deleteHash, params, run,
		map[int]manifest.Manifest{0: fileManifest(0, "movie.mkv", 5)})

	fileDir := layout.FileDir(0, "movie.mkv")
	before := readManifest(t, fileDir)
	staleSheet := writeSheetFor(t, layout, 0, "movie.mkv", before)

	runJSON := filepath.Join(layout.RunDir(), cache.Name)
	recordBefore, err := os.ReadFile(runJSON)
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}

	doomed := before.Frames[1].Path
	if _, err := os.Stat(doomed); err != nil {
		t.Fatalf("the frame to delete is not on disk: %v", err)
	}

	if err := DeleteFrame(root, deleteHash, params, 0, 1); err != nil {
		t.Fatalf("DeleteFrame: %v", err)
	}

	// The file.
	if _, err := os.Stat(doomed); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("frame 1 is still on disk at %s (stat error %v)", doomed, err)
	}

	// The record - and only that one. The survivors keep their own numbers:
	// reusableFrames matches a recorded request against points[Index], so a
	// renumbered survivor would be unusable to the next run and would have it
	// overwrite an existing frames/NNN.jpg.
	after := readManifest(t, fileDir)
	if want := []int{0, 2, 3, 4}; !equalInts(frameIndices(after), want) {
		t.Fatalf("surviving indices = %v, want %v - survivors must never be renumbered",
			frameIndices(after), want)
	}
	for _, f := range after.Frames {
		want := before.Frames[f.Index]
		if f.RequestedMS != want.RequestedMS || f.Path != want.Path {
			t.Errorf("frame %d = %d ms at %s, want %d ms at %s",
				f.Index, f.RequestedMS, f.Path, want.RequestedMS, want.Path)
		}
		if _, err := os.Stat(f.Path); err != nil {
			t.Errorf("frame %d was taken down with its neighbour: %v", f.Index, err)
		}
	}

	// And the run is still a run: this is what removing the file alone
	// destroys, and what removing the record with it preserves.
	if !cache.Usable(after) {
		t.Error("the manifest is no longer usable, so the whole run has become a cache miss")
	}

	// The contact sheet is composed again from what is left. Byte equality
	// against a sheet built here from the survivors pins the pairing rule
	// too: sheet.Build looks records up by Frame.Index over the positions of
	// points, so the survivors are renumbered for the sheet alone - handing
	// it the original plan's points would leave a red "not attempted"
	// placeholder where somebody deliberately removed a frame.
	wantPoints := make([]time.Duration, 0, len(after.Frames))
	wantTiles := make([]manifest.Frame, 0, len(after.Frames))
	for i, f := range after.Frames {
		wantPoints = append(wantPoints, time.Duration(f.RequestedMS)*time.Millisecond)
		tile := f
		tile.Index = i
		wantTiles = append(wantTiles, tile)
	}
	want, err := sheet.Build(wantPoints, wantTiles, after.Video.Width, after.Video.Height)
	if err != nil {
		t.Fatalf("build the expected sheet: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(fileDir, output.SheetName))
	if err != nil {
		t.Fatalf("read the rebuilt sheet: %v", err)
	}
	if bytes.Equal(got, staleSheet) {
		t.Error("sheet.jpg is byte for byte the one written for five frames: it was not rebuilt")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("sheet.jpg is %d bytes, want the %d-byte sheet of the four surviving frames",
			len(got), len(want))
	}

	// run.json is not touched at all while the file still has frames: it is
	// still complete, and a rerun with these parameters must stay a hit.
	recordAfter, err := os.ReadFile(runJSON)
	if err != nil {
		t.Fatalf("read run.json back: %v", err)
	}
	if !bytes.Equal(recordBefore, recordAfter) {
		t.Errorf("run.json changed:\nbefore: %s\nafter:  %s", recordBefore, recordAfter)
	}
}

// TestReplayAfterADeleteServesTheSurvivors is the acceptance criterion the
// ticket is actually about: reopening the run shows the frames that are left
// rather than failing over the one that is gone.
func TestReplayAfterADeleteServesTheSurvivors(t *testing.T) {
	const params = "0011223344556601"
	root := t.TempDir()

	layout := buildCachedRun(t, root, deleteHash, params, cache.Run{
		Version: cache.Version, InfoHash: deleteHash, Name: "Replay After Delete",
		Videos:   []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}},
		Selected: []int{0}, Complete: []int{0},
	}, map[int]manifest.Manifest{0: fileManifest(0, "movie.mkv", 4)})
	if layout.RunDir() == "" {
		t.Fatal("empty run dir")
	}

	if err := DeleteFrame(root, deleteHash, params, 0, 2); err != nil {
		t.Fatalf("DeleteFrame: %v", err)
	}

	var (
		indices []int
		done    *Done
	)
	for _, ev := range collect(t, NewEngine(ffmpeg.Tools{}).Replay(root, deleteHash, params)) {
		switch e := ev.(type) {
		case FrameReady:
			indices = append(indices, e.Index)
		case Failed:
			t.Fatalf("replay failed after a delete: %s: %v", e.Code, e.Err)
		case Done:
			done = &e
		}
	}

	if want := []int{0, 1, 3}; !equalInts(indices, want) {
		t.Errorf("replayed frames %v, want %v", indices, want)
	}
	if done == nil || done.Frames != 3 {
		t.Fatalf("done = %+v, want a completed replay of three frames", done)
	}
	if done.DownloadedByte != 0 {
		t.Errorf("replay downloaded %d bytes", done.DownloadedByte)
	}
}

// TestRerunAfterADeleteIsStillACacheHit is the decision this feature was
// designed around: the file stays in run.json's Complete, so asking for the
// very same thing again is answered off disk, at no network cost, with what
// is left - rather than refetching a frame somebody deliberately removed.
//
// serveFromCache is exercised directly because it is the half of a rerun this
// is about: it decides hit or miss before a session is ever opened, which is
// the only way "no network" can be a fact rather than a hope.
func TestRerunAfterADeleteIsStillACacheHit(t *testing.T) {
	root := t.TempDir()

	cfg := DefaultConfig("magnet:?xt=urn:btih:"+deleteHash, root, t.TempDir())
	cfg.Plan = frames.Plan{Count: 4, Start: 0.1, End: 0.9}
	params := ParamsKey(cfg)

	src, err := swarm.ParseSource(cfg.Source)
	if err != nil {
		t.Fatalf("parse the magnet: %v", err)
	}

	buildCachedRun(t, root, deleteHash, params, cache.Run{
		Version: cache.Version, InfoHash: deleteHash, Name: "Rerun After Delete",
		Plan:     cache.Plan{Count: cfg.Plan.Count, Start: cfg.Plan.Start, End: cfg.Plan.End},
		Videos:   []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}},
		Selected: []int{0}, Complete: []int{0},
	}, map[int]manifest.Manifest{0: fileManifest(0, "movie.mkv", 4)})

	if err := DeleteFrame(root, deleteHash, params, 0, 0); err != nil {
		t.Fatalf("DeleteFrame: %v", err)
	}

	bus := NewBus(DefaultBuffer)
	events, _ := bus.Subscribe()
	hit := NewEngine(ffmpeg.Tools{}).serveFromCache(cfg, src, bus, time.Now())
	bus.Close()

	if !hit {
		t.Fatal("an identical rerun is a miss after one frame was deleted; it must still be served from disk")
	}

	var (
		frameCount int
		done       *Done
	)
	for ev := range events {
		switch e := ev.(type) {
		case FrameReady:
			frameCount++
		case Failed:
			t.Fatalf("cached rerun failed: %s: %v", e.Code, e.Err)
		case Done:
			done = &e
		}
	}
	if frameCount != 3 {
		t.Errorf("the rerun served %d frames, want the 3 that survived", frameCount)
	}
	if done == nil || done.DownloadedByte != 0 {
		t.Fatalf("done = %+v, want a completed rerun that downloaded nothing", done)
	}
}

// TestDeleteFrameRefusesWhatItCannotFind: every way of naming nothing is one
// error a caller can match on, and none of them may touch what is there. A
// delete that half-happens on a bad address is worse than one that refuses.
func TestDeleteFrameRefusesWhatItCannotFind(t *testing.T) {
	const params = "0011223344556602"
	root := t.TempDir()

	layout := buildCachedRun(t, root, deleteHash, params, cache.Run{
		Version: cache.Version, InfoHash: deleteHash, Name: "Nothing To Delete",
		Videos:   []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}},
		Selected: []int{0}, Complete: []int{0},
	}, map[int]manifest.Manifest{0: fileManifest(0, "movie.mkv", 3)})

	fileDir := layout.FileDir(0, "movie.mkv")
	before := readManifest(t, fileDir)

	cases := []struct {
		name                  string
		infoHash, params      string
		fileIndex, frameIndex int
	}{
		{"a frame index no manifest holds", deleteHash, params, 0, 7},
		{"a file the run does not list", deleteHash, params, 4, 0},
		{"a result set that was never written", deleteHash, "ffffffffffffffff", 0, 0},
		{"a torrent with nothing on disk", "0000000000000000000000000000000000000000", params, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := DeleteFrame(root, c.infoHash, c.params, c.fileIndex, c.frameIndex)
			if !errors.Is(err, ErrNoSuchFrame) {
				t.Fatalf("error = %v, want one a caller can match as ErrNoSuchFrame", err)
			}
		})
	}

	after := readManifest(t, fileDir)
	if !equalInts(frameIndices(after), frameIndices(before)) {
		t.Errorf("frames = %v after four refused deletes, want %v",
			frameIndices(after), frameIndices(before))
	}
	for _, f := range after.Frames {
		if _, err := os.Stat(f.Path); err != nil {
			t.Errorf("frame %d went missing on a refused delete: %v", f.Index, err)
		}
	}
}

// TestDeletingTheLastFrameDropsTheFileFromComplete is the one corner where a
// delete has to touch run.json.
//
// cache.Usable refuses a manifest with no frames at all, so a file left in
// Complete with an empty one would fail loadCacheHit and take the whole run
// down with it - the exact failure this feature exists to avoid, reached from
// the other end. Taking the index out of Complete makes that file silently
// absent from a replay instead, which is what a file the original run never
// finished already does (TestReplaySelectsOnlyCompletedFiles).
func TestDeletingTheLastFrameDropsTheFileFromComplete(t *testing.T) {
	const params = "0011223344556603"
	root := t.TempDir()

	layout := buildCachedRun(t, root, deleteHash, params, cache.Run{
		Version: cache.Version, InfoHash: deleteHash, Name: "Last Frame",
		Videos: []cache.File{
			{Index: 0, Path: "episode-1.mkv", Bytes: 1 << 20},
			{Index: 1, Path: "episode-2.mkv", Bytes: 1 << 20},
		},
		Selected: []int{0, 1}, Complete: []int{0, 1},
	}, map[int]manifest.Manifest{
		0: fileManifest(0, "episode-1.mkv", 1),
		1: fileManifest(1, "episode-2.mkv", 3),
	})

	emptied := layout.FileDir(0, "episode-1.mkv")
	untouched := layout.FileDir(1, "episode-2.mkv")
	writeSheetFor(t, layout, 0, "episode-1.mkv", readManifest(t, emptied))
	neighbourBefore := readManifest(t, untouched)

	if err := DeleteFrame(root, deleteHash, params, 0, 0); err != nil {
		t.Fatalf("DeleteFrame: %v", err)
	}

	record, ok := cache.LoadRun(layout.RunDir())
	if !ok {
		t.Fatal("run.json is no longer readable")
	}
	if !equalInts(record.Complete, []int{1}) {
		t.Errorf("complete = %v, want [1] - a file with no frames left cannot be served", record.Complete)
	}
	if !equalInts(record.Selected, []int{0, 1}) {
		t.Errorf("selected = %v, want [0 1] - what was asked for did not change", record.Selected)
	}

	if m := readManifest(t, emptied); len(m.Frames) != 0 {
		t.Errorf("the emptied manifest still holds %d frames", len(m.Frames))
	}
	// The sheet went with the frames: it depicted one that no longer exists,
	// and sheet.Build cannot compose an empty plan to replace it with.
	if _, err := os.Stat(filepath.Join(emptied, output.SheetName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sheet.jpg survived a file with no frames (stat error %v)", err)
	}

	// The other file of the same run is not collateral damage.
	neighbourAfter := readManifest(t, untouched)
	if !equalInts(frameIndices(neighbourAfter), frameIndices(neighbourBefore)) {
		t.Errorf("the other file's frames = %v, want %v",
			frameIndices(neighbourAfter), frameIndices(neighbourBefore))
	}
	for _, f := range neighbourAfter.Frames {
		if _, err := os.Stat(f.Path); err != nil {
			t.Errorf("the other file's frame %d went missing: %v", f.Index, err)
		}
	}

	// And the run still opens, showing what is left of it.
	var started []int
	for _, ev := range collect(t, NewEngine(ffmpeg.Tools{}).Replay(root, deleteHash, params)) {
		switch e := ev.(type) {
		case FileStarted:
			started = append(started, e.File)
		case Failed:
			t.Fatalf("replay failed after the last frame of one file went: %s: %v", e.Code, e.Err)
		}
	}
	if !equalInts(started, []int{1}) {
		t.Errorf("replay started files %v, want only file 1", started)
	}
}

// TestDeleteFrameRefusesARecordPointingOutsideItsRun pins the one guard
// between a JSON file a person owns and an unlink.
//
// Every other path this project reads comes from a manifest and is only ever
// stat'ed or served; this is the sole place one is removed, so a record
// naming something outside the run it belongs to was not written by any run
// of ours and is refused before the manifest is touched at all. A delete that
// removed the file and then reported the problem would be exactly backwards.
func TestDeleteFrameRefusesARecordPointingOutsideItsRun(t *testing.T) {
	const params = "0011223344556604"
	root := t.TempDir()

	layout := buildCachedRun(t, root, deleteHash, params, cache.Run{
		Version: cache.Version, InfoHash: deleteHash, Name: "Pointing Elsewhere",
		Videos:   []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}},
		Selected: []int{0}, Complete: []int{0},
	}, map[int]manifest.Manifest{0: fileManifest(0, "movie.mkv", 3)})

	fileDir := layout.FileDir(0, "movie.mkv")
	m := readManifest(t, fileDir)

	// Somewhere a run would never write: outside the output root entirely.
	elsewhere := filepath.Join(t.TempDir(), "not-a-frame.jpg")
	if err := os.WriteFile(elsewhere, []byte("someone else's file"), 0o600); err != nil {
		t.Fatalf("write the file outside the run: %v", err)
	}
	m.Frames[1].Path = elsewhere

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("encode the doctored manifest: %v", err)
	}
	writer, err := output.NewWriter(layout)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if _, err := writer.WriteFile(0, "movie.mkv", manifest.Name, append(data, '\n')); err != nil {
		t.Fatalf("write the doctored manifest: %v", err)
	}

	err = DeleteFrame(root, deleteHash, params, 0, 1)
	if err == nil {
		t.Fatal("DeleteFrame acted on a record pointing outside its own run")
	}
	if errors.Is(err, ErrNoSuchFrame) {
		t.Errorf("error = %v, want one that says the record is wrong, not that the frame is absent", err)
	}

	if _, statErr := os.Stat(elsewhere); statErr != nil {
		t.Errorf("the file outside the run was removed: %v", statErr)
	}
	if after := readManifest(t, fileDir); !equalInts(frameIndices(after), []int{0, 1, 2}) {
		t.Errorf("frames = %v, want [0 1 2] - nothing may be written on a refused delete",
			frameIndices(after))
	}
}
