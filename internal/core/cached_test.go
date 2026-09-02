package core

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
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

		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatalf("marshal manifest for file %d: %v", idx, err)
		}
		if _, err := writer.WriteFile(idx, m.File.Path, manifest.Name, data); err != nil {
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
func TestReplaySelectsOnlyCompletedFiles(t *testing.T) {
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
