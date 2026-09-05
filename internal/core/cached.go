package core

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
	"github.com/madmurdok/torpeek/internal/probe"
	"github.com/madmurdok/torpeek/internal/swarm"
)

// serveFromCache replays a finished run from disk, reporting whether it could.
//
// It runs before any session is opened, which is the whole point: the
// requirement is not "a cheap rerun" but a rerun that does not go to the
// network at all, and the only way to be sure of that is to never connect. The
// infohash comes from the magnet or the .torrent file, both readable offline,
// so the result's location is known without asking anyone.
//
// Anything unexpected - a record from another format, a missing manifest, a
// frame deleted from under it - is a miss rather than an error. A cache that
// argues with the caller is worse than one that quietly steps aside.
//
// Nothing is published until the whole selection is confirmed usable
// (loadCacheHit): a caller here falls back to swarm.Open on a miss, and a
// client that had already been told metadata_ready for a run that then goes
// to the network would see it announced twice.
func (e *Engine) serveFromCache(cfg Config, src swarm.Source, bus *Bus, started time.Time) bool {
	hash, err := src.InfoHash()
	if err != nil {
		return false
	}

	layout := output.Layout{
		Root:     cfg.OutputRoot,
		InfoHash: hash.HexString(),
		Params:   ParamsKey(cfg),
	}

	record, ok := cache.LoadRun(layout.RunDir())
	if !ok {
		return false
	}

	videos := videosFromRecord(record)

	selected, err := swarm.Select(videos, cfg.Files)
	if err != nil || len(selected) == 0 {
		// A selection naming nothing is a real error, but it is the live run's
		// to report - with the torrent's own file list rather than a cached
		// one that may be stale.
		return false
	}

	hit, ok := loadCacheHit(layout, record, selected)
	if !ok {
		return false
	}

	hit.publish(videos, bus, started)
	return true
}

// ReplayRun replays the run recorded at root/infoHash/params, publishing the
// same event sequence serveFromCache does for an identical live request - but
// addressed by where the run lives on disk instead of derived from a live
// Config and Source.
//
// That is the whole reason it exists apart from serveFromCache: reopening a
// finished run has no live Config to derive a Source or a Plan from, and a
// record written before TOR-52 never saved a Source at all - rebuilding a
// Config from an empty one and re-parsing it would either fail outright or,
// worse, resolve to a slightly different ParamsKey than the directory this
// call was actually given, which would silently fall back to the swarm the
// way serveFromCache's own caller does on a miss. infoHash and params already
// name everything Layout needs, exactly the pair a disk-only GET /runs row
// carries (TOR-54) for a run this process never minted an id for, so nothing
// here parses a Source at all.
//
// Unlike serveFromCache, it is not proving a specific selection is still
// satisfied - there is no live request's cfg.Files to match here, only "show
// what this run produced." That is exactly record.Complete: a file the
// original run asked for but never finished is correctly left out, rather
// than a partial file turning the whole reopen into a miss the way
// serveFromCache's exact-selection match deliberately does.
//
// Every miss reported here - no record, nothing complete, a frame gone from
// under a manifest - is published as a run-scoped Failed rather than returned
// as a Go error, the same way a live run's own failures reach a client: as an
// event on its stream. A caller must still never fall back to the network on
// it - this func does not, and neither may whoever calls it.
func (e *Engine) ReplayRun(root, infoHash, params string, bus *Bus) {
	started := time.Now()
	layout := output.Layout{Root: root, InfoHash: infoHash, Params: params}

	miss := func(reason string) {
		bus.Publish(Failed{File: -1, Code: CodeStorage, Err: errors.New(reason)})
	}

	record, ok := cache.LoadRun(layout.RunDir())
	if !ok {
		miss("no cached run at " + layout.RunDir())
		return
	}

	videos := videosFromRecord(record)
	selected := completedVideos(videos, record.Complete)
	if len(selected) == 0 {
		miss("the run at " + layout.RunDir() + " has no complete file")
		return
	}

	hit, ok := loadCacheHit(layout, record, selected)
	if !ok {
		miss("a frame from the run at " + layout.RunDir() + " is missing on disk")
		return
	}

	hit.publish(videos, bus, started)
}

// Replay starts ReplayRun in its own goroutine and hands back the event
// stream it produces, the same shape Run returns for a live one - a channel
// that closes when the replay is over, its last event always Done or Failed.
// That shape is what lets a caller holding only an event-stream contract
// (internal/web's Runner, and the Replayer it is paired with) use this
// exactly like a live run.
func (e *Engine) Replay(root, infoHash, params string) <-chan Event {
	bus := NewBus(DefaultBuffer)
	events, _ := bus.Subscribe()

	go func() {
		defer bus.Close()
		e.ReplayRun(root, infoHash, params, bus)
	}()

	return events
}

// videosFromRecord rebuilds the torrent's file list from what a run recorded,
// in the shape swarm.Select and MetadataReady both expect.
func videosFromRecord(record cache.Run) []swarm.FileInfo {
	videos := make([]swarm.FileInfo, 0, len(record.Videos))
	for _, f := range record.Videos {
		videos = append(videos, swarm.FileInfo{
			Index: f.Index, Path: f.Path, Length: f.Bytes, Offset: f.Offset,
		})
	}
	return videos
}

// completedVideos narrows videos to the indices record.Complete names, in
// videos' own order. It is ReplayRun's stand-in for a live request's
// cfg.Files: reopening has no selection of its own, only what the run it is
// replaying actually finished.
func completedVideos(videos []swarm.FileInfo, complete []int) []swarm.FileInfo {
	done := make(map[int]bool, len(complete))
	for _, i := range complete {
		done[i] = true
	}
	out := make([]swarm.FileInfo, 0, len(complete))
	for _, f := range videos {
		if done[f.Index] {
			out = append(out, f)
		}
	}
	return out
}

// cacheHit is a selection of files from a run record that has already been
// confirmed replayable: every one of them is in record.Complete and its
// manifest still points at whole files on disk. Once built, publishing it
// cannot discover a miss partway through - that is checked here, before
// either serveFromCache or ReplayRun writes a single event.
type cacheHit struct {
	layout    output.Layout
	record    cache.Run
	selected  []swarm.FileInfo
	manifests []manifest.Manifest
}

// loadCacheHit validates that every file in selected is both complete and has
// a usable manifest, reporting ok=false at the first one that is not - a
// deleted frame, a missing manifest, an incomplete file - so a caller commits
// to publishing only once the whole run is confirmed intact (cache.Usable is
// not weakened here: it is the sole judge of "usable").
func loadCacheHit(layout output.Layout, record cache.Run, selected []swarm.FileInfo) (cacheHit, bool) {
	complete := make(map[int]bool, len(record.Complete))
	for _, i := range record.Complete {
		complete[i] = true
	}

	manifests := make([]manifest.Manifest, 0, len(selected))
	for _, file := range selected {
		if !complete[file.Index] {
			return cacheHit{}, false
		}
		m, ok := cache.LoadManifest(layout.FileDir(file.Index, file.Path))
		if !ok || !cache.Usable(m) {
			return cacheHit{}, false
		}
		manifests = append(manifests, m)
	}

	return cacheHit{layout: layout, record: record, selected: selected, manifests: manifests}, true
}

// publish replays a validated hit as the exact event sequence a live run
// produces: MetadataReady, then FileStarted/FrameReady*/FileDone per file,
// then Done with DownloadedByte 0 - the cost of reading a few files off disk,
// which is the answer "no network" asks for. Nothing was claimed either: a
// rerun that never opened a session ordered no pieces from anybody.
func (h cacheHit) publish(videos []swarm.FileInfo, bus *Bus, started time.Time) {
	bus.Publish(MetadataReady{
		Name:     h.record.Name,
		InfoHash: h.record.InfoHash,
		Private:  h.record.Private,
		Videos:   videos,
		Selected: indicesOf(h.selected),
	})

	frames := 0
	for _, m := range h.manifests {
		bus.Publish(FileStarted{
			File:  m.File.Index,
			Path:  m.File.Path,
			Media: mediaFromManifest(m),
			Plan:  planFromManifest(m),
		})

		for _, f := range m.Frames {
			actual := time.Duration(0)
			if f.ActualMS != nil {
				actual = time.Duration(*f.ActualMS) * time.Millisecond
			}
			bus.Publish(FrameReady{
				File:      m.File.Index,
				Index:     f.Index,
				Requested: time.Duration(f.RequestedMS) * time.Millisecond,
				Actual:    actual,
				Shift:     ShiftReason(f.Shift),
				Path:      f.Path,
				Width:     f.Width,
				Height:    f.Height,
			})
			frames++
		}

		// A cached rerun serves the same artefacts a live run would have
		// written, sheet included - but the sheet is a newer artefact than
		// the manifest format itself, so a result cached before TOR-16
		// simply won't have one on disk. Reporting a path only when it is
		// actually there keeps a stale cache a hit rather than a miss.
		dir := h.layout.FileDir(m.File.Index, m.File.Path)
		sheetPath := filepath.Join(dir, output.SheetName)
		if _, err := os.Stat(sheetPath); err != nil {
			sheetPath = ""
		}

		bus.Publish(FileDone{
			File:         m.File.Index,
			Path:         m.File.Path,
			Frames:       len(m.Frames),
			ManifestPath: filepath.Join(dir, manifest.Name),
			SheetPath:    sheetPath,
		})
	}

	// A run reopened from disk offers the very same .torrent a live one does,
	// so the link does not quietly depend on whether this process happened to
	// be the one that captured the run. It is stated only when the file is
	// actually there, for the same reason the sheet above is: a run recorded
	// before torpeek kept one has none, and that has to stay a cache hit
	// without a link rather than become a miss.
	torrentPath := h.layout.TorrentPath()
	if _, err := os.Stat(torrentPath); err != nil {
		torrentPath = ""
	}

	bus.Publish(Done{
		Files:  len(h.manifests),
		Frames: frames,
		// Nothing was downloaded and nothing was claimed; the elapsed time is
		// the cost of reading a few files - which is the answer the criterion
		// is asking for.
		DownloadedByte: 0,
		ClaimedByte:    0,
		ClaimedPieces:  0,
		ClaimedRanges:  nil,
		Elapsed:        time.Since(started),
		Reason:         StopCompleted,
		TorrentPath:    torrentPath,
	})
}

// mediaFromManifest rebuilds what a client was told the first time, so a
// cached run and a live one look the same to whoever is watching.
func mediaFromManifest(m manifest.Manifest) probe.MediaInfo {
	info := probe.MediaInfo{
		FormatName: m.File.Container,
		Duration:   time.Duration(m.File.DurationMS) * time.Millisecond,
		Size:       m.File.Bytes,
		Video: probe.VideoStream{
			Codec:   m.Video.Codec,
			Profile: m.Video.Profile,
			Width:   m.Video.Width,
			Height:  m.Video.Height,
			FPS:     m.Video.FPS,
			BitRate: m.Video.BitRate,
		},
	}
	for _, a := range m.Audio {
		info.Audio = append(info.Audio, probe.AudioStream{
			Index: a.Index, Codec: a.Codec, Language: a.Language,
			Title: a.Title, Channels: a.Channels, Default: a.Default,
		})
	}
	for _, s := range m.Subtitles {
		info.Subtitles = append(info.Subtitles, probe.SubtitleStream{
			Index: s.Index, Codec: s.Format, Language: s.Language,
			Title: s.Title, Forced: s.Forced, Default: s.Default,
		})
	}
	return info
}

func planFromManifest(m manifest.Manifest) []time.Duration {
	out := make([]time.Duration, 0, len(m.Frames))
	for _, f := range m.Frames {
		out = append(out, time.Duration(f.RequestedMS)*time.Millisecond)
	}
	return out
}
