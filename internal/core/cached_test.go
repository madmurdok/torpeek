package core

import (
	"os"
	"testing"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
	"github.com/madmurdok/torpeek/internal/swarm"
)

// buildCachedRun writes a run.json and one manifest.json per file directly to
// disk with output.Writer - the same writer a live run uses - so ReplayRun is
// exercised against a real on-disk layout without a torrent or ffmpeg: Replay
// never touches either, only cache.LoadRun, cache.LoadManifest and
// cache.Usable, which is exactly what these tests mean to prove.
//
// files is keyed by file index; each manifest's own Frames carry a
// placeholder Path that is overwritten with the real file WriteFrame just
// wrote, the same way the engine fills it in during a live run.
func buildCachedRun(t *testing.T, root, infoHash, params string, run cache.Run, files map[int]manifest.Manifest) output.Layout {
	t.Helper()

	layout := output.Layout{Root: root, InfoHash: infoHash, Params: params}
	writer, err := output.NewWriter(layout)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	for idx, m := range files {
		for i, f := range m.Frames {
			if f.Path == "" {
				continue
			}
			path, err := writer.WriteFrame(idx, m.File.Path, f.Index, []byte{0xFF, 0xD8, byte(i)}, "jpg")
			if err != nil {
				t.Fatalf("write frame %d of file %d: %v", f.Index, idx, err)
			}
			m.Frames[i].Path = path
		}

		// Through the writer a live run uses, so what lands on disk is the
		// real recorded shape - frame paths relative to their manifest -
		// rather than whatever this test happens to hold in memory.
		if _, err := writer.WriteManifest(idx, m.File.Path, m); err != nil {
			t.Fatalf("write manifest for file %d: %v", idx, err)
		}
	}

	if err := cache.SaveRun(layout.RunDir(), run); err != nil {
		t.Fatalf("save run.json: %v", err)
	}
	return layout
}

// fileManifest is a minimal, usable one-file manifest: exactly what
// cache.Usable requires and nothing more.
func fileManifest(index int, path string, frameCount int) manifest.Manifest {
	frames := make([]manifest.Frame, frameCount)
	for i := range frames {
		ms := int64(i * 1000)
		frames[i] = manifest.Frame{
			Index: i, RequestedMS: ms, ActualMS: &ms,
			Path: "placeholder", Width: 640, Height: 360,
		}
	}
	return manifest.Manifest{
		Version: manifest.Version,
		File:    manifest.File{Index: index, Path: path, Bytes: 1 << 20, DurationMS: 60_000, Container: "matroska"},
		Video:   manifest.Video{Codec: "h264", Width: 640, Height: 360},
		Frames:  frames,
	}
}

// TestReplayServesARunWithNoRecordedSource is TOR-55's central claim: a
// record addressed purely by infoHash and params, never Source or Plan (the
// shape cache.Run had before TOR-52, and still can have - TOR-52 did not
// bump cache.Version), replays exactly as well as a newer one. Nothing here
// parses a Source at all.
func TestReplayServesARunWithNoRecordedSource(t *testing.T) {
	const (
		infoHash = "e4d37e62d14ba96d29b9e760148803b458aee5b6"
		params   = "deadbeef"
	)
	root := t.TempDir()

	m := fileManifest(0, "movie.mkv", 3)
	layout := buildCachedRun(t, root, infoHash, params, cache.Run{
		Version:  cache.Version,
		Tool:     "0.4.0",
		InfoHash: infoHash,
		Name:     "No Source Run",
		// Source and Plan are deliberately left at their zero value - a
		// record written before TOR-52 never had them.
		Videos:   []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}},
		Complete: []int{0},
	}, map[int]manifest.Manifest{0: m})
	if layout.RunDir() == "" {
		t.Fatal("empty run dir")
	}

	engine := NewEngine(ffmpeg.Tools{})
	events := collect(t, engine.Replay(root, infoHash, params))

	var (
		metadata *MetadataReady
		started  int
		frames   int
		done     *Done
	)
	for _, ev := range events {
		switch e := ev.(type) {
		case MetadataReady:
			metadata = &e
		case FileStarted:
			started++
		case FrameReady:
			frames++
		case Failed:
			t.Fatalf("replay failed: %s: %v", e.Code, e.Err)
		case Done:
			done = &e
		}
	}

	if metadata == nil || metadata.InfoHash != infoHash || metadata.Name != "No Source Run" {
		t.Fatalf("metadata = %+v, want infohash %q and name %q", metadata, infoHash, "No Source Run")
	}
	if started != 1 {
		t.Errorf("file_started fired %d times, want 1", started)
	}
	if frames != 3 {
		t.Errorf("frame_ready fired %d times, want 3", frames)
	}
	if done == nil {
		t.Fatal("no terminal event")
	}
	if done.DownloadedByte != 0 {
		t.Errorf("downloaded %d bytes; a replay must never touch the network", done.DownloadedByte)
	}
	if done.Reason != StopCompleted {
		t.Errorf("done reason = %q, want %q", done.Reason, StopCompleted)
	}
}

// TestReplayMissesWhenNoRunIsThere covers infoHash/params that name nothing
// on disk at all - a typo, or a listing row from a directory since removed.
// It must fail loudly as a run-scoped Failed, never silently produce an empty
// success.
func TestReplayMissesWhenNoRunIsThere(t *testing.T) {
	root := t.TempDir()

	engine := NewEngine(ffmpeg.Tools{})
	events := collect(t, engine.Replay(root, "0000000000000000000000000000000000dead", "cafebabe"))

	if len(events) != 1 {
		t.Fatalf("got %d events, want exactly one Failed: %+v", len(events), events)
	}
	failed, ok := events[0].(Failed)
	if !ok || failed.File != -1 {
		t.Fatalf("event = %#v, want a run-scoped Failed", events[0])
	}
}

// TestReplayMissesWhenAFrameIsGone is cache.Usable's own contract, exercised
// through Replay: a manifest is a promise about files on disk, and a promise
// about a deleted file must be treated as no promise - reported as a Failed,
// not as a run that quietly served fewer frames than it claims to.
//
// Nothing may be published before that is known, either: this asserts the
// stream is exactly one event, not metadata_ready/file_started followed by a
// failure, which would tell a client half a run happened.
func TestReplayMissesWhenAFrameIsGone(t *testing.T) {
	const (
		infoHash = "1111111111111111111111111111111111dead"
		params   = "deadbeef"
	)
	root := t.TempDir()

	m := fileManifest(0, "movie.mkv", 2)
	layout := buildCachedRun(t, root, infoHash, params, cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "Deleted Frame Run",
		Videos:   []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}},
		Complete: []int{0},
	}, map[int]manifest.Manifest{0: m})

	loaded, ok := cache.LoadManifest(layout.FileDir(0, "movie.mkv"))
	if !ok || len(loaded.Frames) == 0 {
		t.Fatalf("could not load the manifest just written: ok=%v", ok)
	}
	if err := os.Remove(loaded.Frames[0].Path); err != nil {
		t.Fatalf("delete a frame: %v", err)
	}

	engine := NewEngine(ffmpeg.Tools{})
	events := collect(t, engine.Replay(root, infoHash, params))

	if len(events) != 1 {
		t.Fatalf("got %d events, want exactly one Failed (nothing published before a hit is confirmed): %+v", len(events), events)
	}
	failed, ok := events[0].(Failed)
	if !ok || failed.File != -1 {
		t.Fatalf("event = %#v, want a run-scoped Failed", events[0])
	}
}

// TestReplaySelectsOnlyCompletedFiles: reopening has no selection of its own
// to match against, unlike a live rerun - it shows what the run actually
// produced. A file the original run held but never finished (in Videos, not
// in Complete, no manifest at all) must be silently absent rather than
// turning the whole reopen into a miss.
// TestReplayLeavesOutAFileWithNothingOnDisk was TestReplaySelectsOnlyCompletedFiles
// until TOR-124, and the rename is the point rather than tidying: the rule it
// guards is no longer "only files record.Complete names" but "only files that
// have something to show". This fixture's second file satisfies neither - it
// has no manifest on disk at all - so the assertions are unchanged and the
// name now says which rule they test.
func TestReplayLeavesOutAFileWithNothingOnDisk(t *testing.T) {
	const (
		infoHash = "2222222222222222222222222222222222dead"
		params   = "deadbeef"
	)
	root := t.TempDir()

	m0 := fileManifest(0, "episode-1.mkv", 2)
	buildCachedRun(t, root, infoHash, params, cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "Partial Run",
		Videos: []cache.File{
			{Index: 0, Path: "episode-1.mkv", Bytes: 1 << 20},
			{Index: 1, Path: "episode-2.mkv", Bytes: 1 << 20},
		},
		// Only file 0 ever finished; file 1 has no manifest on disk at all.
		Complete: []int{0},
	}, map[int]manifest.Manifest{0: m0})

	engine := NewEngine(ffmpeg.Tools{})
	events := collect(t, engine.Replay(root, infoHash, params))

	var (
		metadata *MetadataReady
		started  []int
		done     *Done
	)
	for _, ev := range events {
		switch e := ev.(type) {
		case MetadataReady:
			metadata = &e
		case FileStarted:
			started = append(started, e.File)
		case Failed:
			t.Fatalf("replay failed: %s: %v", e.Code, e.Err)
		case Done:
			done = &e
		}
	}

	if metadata == nil || len(metadata.Videos) != 2 {
		t.Fatalf("metadata = %+v, want both torrent files listed", metadata)
	}
	if len(metadata.Selected) != 1 || metadata.Selected[0] != 0 {
		t.Errorf("selected = %v, want only file 0 - the one that finished", metadata.Selected)
	}
	if len(started) != 1 || started[0] != 0 {
		t.Errorf("file_started fired for %v, want only file 0", started)
	}
	if done == nil || done.Files != 1 {
		t.Errorf("done = %+v, want exactly 1 file", done)
	}
}

// holedManifest is a manifest the way a run that could not reach every piece
// writes one: some points with a frame, some recorded as having produced
// nothing and why. It is the shape TOR-118 found the manifest already had and
// TOR-124 made reopenable.
func holedManifest(index int, path string, points int, failed map[int]string) manifest.Manifest {
	m := fileManifest(index, path, points)
	for i := range m.Frames {
		code, isFailed := failed[i]
		if !isFailed {
			continue
		}
		// Exactly what engine.go writes for a point it gave up on: no path, no
		// actual, ShiftFailed, and the code in Error.
		m.Frames[i].Path = ""
		m.Frames[i].ActualMS = nil
		m.Frames[i].Shift = manifest.ShiftFailed
		m.Frames[i].Error = code
		m.Frames[i].Width = 0
		m.Frames[i].Height = 0
	}
	return m
}

// TestReplayServesAHoledRun is TOR-124's acceptance criterion. A run with
// unreachable capture points was refused outright - "has no complete file" -
// so the run this release most improved was the one nobody could open. It now
// replays what it produced, with the gaps published as the skips they were.
func TestReplayServesAHoledRun(t *testing.T) {
	const (
		infoHash = "4e1827ec34783a07358081c635a4e0beab1c11df"
		params   = "af97f78c"
	)
	root := t.TempDir()

	// Five frames of twelve, and the two causes the manifest keeps apart -
	// the split a real holed run on disk actually has.
	failed := map[int]string{
		5: "unavailable", 6: "unavailable",
		7: "read_stalled", 8: "read_stalled", 9: "read_stalled",
		10: "read_stalled", 11: "read_stalled",
	}
	m := holedManifest(0, "movie.mkv", 12, failed)
	buildCachedRun(t, root, infoHash, params, cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "001",
		Videos: []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}},
		// Deliberately EMPTY: the file never came out whole, which is exactly
		// what Complete records, and exactly what used to refuse the reopen.
		Complete: nil,
	}, map[int]manifest.Manifest{0: m})

	engine := NewEngine(ffmpeg.Tools{})
	events := collect(t, engine.Replay(root, infoHash, params))

	var (
		started  *FileStarted
		ready    []FrameReady
		skipped  []FrameSkipped
		fileDone *FileDone
		done     *Done
	)
	for _, ev := range events {
		switch e := ev.(type) {
		case FileStarted:
			started = &e
		case FrameReady:
			ready = append(ready, e)
		case FrameSkipped:
			skipped = append(skipped, e)
		case FileDone:
			fileDone = &e
		case Failed:
			t.Fatalf("a run with five frames on disk was refused: %s: %v", e.Code, e.Err)
		case Done:
			done = &e
		}
	}

	if started == nil {
		t.Fatal("no file_started: the holed file was not offered at all")
	}
	if len(started.Plan) != 12 {
		t.Errorf("the plan carries %d points, want all 12 - the page reserves a cell "+
			"per point from this (TOR-110)", len(started.Plan))
	}

	if len(ready) != 5 {
		t.Errorf("%d frames published, want 5", len(ready))
	}
	for _, r := range ready {
		if r.Path == "" {
			t.Errorf("frame %d was published with no path; a point that produced "+
				"nothing must be a skip, not a frame", r.Index)
		}
	}

	if len(skipped) != 7 {
		t.Fatalf("%d skips published, want 7", len(skipped))
	}
	codes := map[ErrorCode]int{}
	for _, sk := range skipped {
		codes[sk.Code]++
		if want := failed[sk.Index]; string(sk.Code) != want {
			t.Errorf("point %d was skipped with code %q, want %q from the manifest",
				sk.Index, sk.Code, want)
		}
	}
	if codes[CodeUnavailable] != 2 || codes[CodeReadStalled] != 5 {
		t.Errorf("codes = %v, want 2 unavailable and 5 read_stalled - the two causes "+
			"kept apart, which is the whole reason TOR-118 exists", codes)
	}

	if fileDone == nil || fileDone.Frames != 5 {
		t.Errorf("file_done = %+v, want Frames 5 - what the file HAS, not how many "+
			"points it recorded", fileDone)
	}
	if done == nil || done.Frames != 5 {
		t.Errorf("done = %+v, want Frames 5", done)
	}
}

// TestACacheHitStillRefusesAHoledRun is the other half of TOR-124 and the
// regression that would matter most. Reopening asks "show me what this run
// produced"; a live request asks for a specific number of frames of a
// specific file, and answering it off disk with five of the twelve somebody
// asked for would be answering a question they did not put. Run.Complete's
// own doc argues exactly that, and it stays true on this path.
func TestACacheHitStillRefusesAHoledRun(t *testing.T) {
	const infoHash = "4e1827ec34783a07358081c635a4e0beab1c11df"
	root := t.TempDir()

	cfg := DefaultConfig("ignored.torrent", root, t.TempDir())
	cfg.Plan = frames.Plan{Count: 12, Start: 0.05, End: 0.95}
	params := ParamsKey(cfg)

	m := holedManifest(0, "movie.mkv", 12, map[int]string{5: "unavailable", 6: "read_stalled"})
	buildCachedRun(t, root, infoHash, params, cache.Run{
		Version: cache.Version, InfoHash: infoHash, Name: "001",
		Videos:   []cache.File{{Index: 0, Path: "movie.mkv", Bytes: 1 << 20}},
		Complete: nil,
	}, map[int]manifest.Manifest{0: m})

	// The record IS reopenable now, which is what makes this test worth
	// having: the two paths read the same directory and must answer
	// differently.
	replayed := collect(t, NewEngine(ffmpeg.Tools{}).Replay(root, infoHash, params))
	servedByReplay := false
	for _, ev := range replayed {
		if _, ok := ev.(FileStarted); ok {
			servedByReplay = true
		}
	}
	if !servedByReplay {
		t.Fatal("the fixture is not reopenable, so this test cannot show the difference")
	}

	layout := output.Layout{Root: root, InfoHash: infoHash, Params: params}
	record, ok := cache.LoadRun(layout.RunDir())
	if !ok {
		t.Fatal("the fixture wrote no readable record")
	}
	selected := []swarm.FileInfo{{Index: 0, Path: "movie.mkv", Length: 1 << 20}}

	if _, ok := loadCacheHit(layout, record, selected, true); ok {
		t.Error("a live request was answered off disk with a holed result; it must go " +
			"to the swarm and try the missing points again")
	}
	if _, ok := loadCacheHit(layout, record, selected, false); !ok {
		t.Error("reopening the same directory was refused, so the two paths are not " +
			"actually distinguished")
	}
}
