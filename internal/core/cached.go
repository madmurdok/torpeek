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

	// true: this is a live request being answered off disk, so the answer has
	// to be the whole of what was asked for.
	hit, ok := loadCacheHit(layout, record, selected, true)
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
	selected := producedVideos(layout, videos)
	if len(selected) == 0 {
		miss("the run at " + layout.RunDir() + " has no frames on disk")
		return
	}

	// false: reopening shows what the run produced, gaps included. The gaps
	// are published as the skips they were and the page marks them (TOR-110),
	// so a partial result can no longer be mistaken for a whole one - which is
	// the premise the old refusal rested on.
	hit, ok := loadCacheHit(layout, record, selected, false)
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

// videosFromRecord rebuilds the torrent's video list from what a run
// recorded, in the shape swarm.Select and MetadataReady both expect.
func videosFromRecord(record cache.Run) []swarm.FileInfo {
	return fileInfoFrom(record.Videos)
}

// filesFromRecord rebuilds the torrent's WHOLE file list (TOR-180), and
// answers nil for a record written before cache.Run.Files existed rather
// than an empty list - that field's own rule, carried through to
// MetadataReady.Files, which draws the same distinction for the same reason.
// A replay of an older run therefore publishes no file list at all, and the
// page falls back to the videos, instead of being told the torrent is empty.
func filesFromRecord(record cache.Run) []swarm.FileInfo {
	if record.Files == nil {
		return nil
	}
	return fileInfoFrom(record.Files)
}

func fileInfoFrom(files []cache.File) []swarm.FileInfo {
	out := make([]swarm.FileInfo, 0, len(files))
	for _, f := range files {
		out = append(out, swarm.FileInfo{
			Index: f.Index, Path: f.Path, Length: f.Bytes, Offset: f.Offset,
		})
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

// loadCacheHit validates that every file in selected has a manifest that is
// readable and still backed by frames on disk, reporting ok=false at the first
// one that is not - so a caller commits to publishing only once the run is
// confirmed intact. cache.Usable is not weakened here: it remains the sole
// judge of "usable", and since TOR-124 that judgement is about the disk alone.
//
// requireComplete is the difference between this function's two callers, and
// it is the whole of TOR-124. serveFromCache passes true: somebody asked for
// twenty frames of a file, and handing them the five a previous run managed
// would be answering a question they did not put - Run.Complete's own doc
// argues exactly that, and it is right for that path. ReplayRun passes false:
// nobody is asking for a frame count there, only to be shown what a recorded
// run produced, and a file that produced five of twelve produced something.
// Reopening it used to be refused, so the run this release most improved was
// the one run nobody could open.
//
// Both paths still refuse a file whose manifest is gone, unreadable, or
// promises frames the disk no longer has.
func loadCacheHit(layout output.Layout, record cache.Run, selected []swarm.FileInfo,
	requireComplete bool) (cacheHit, bool) {

	complete := make(map[int]bool, len(record.Complete))
	for _, i := range record.Complete {
		complete[i] = true
	}

	manifests := make([]manifest.Manifest, 0, len(selected))
	for _, file := range selected {
		if requireComplete && !complete[file.Index] {
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

// producedVideos is ReplayRun's stand-in for a live request's cfg.Files, in
// videos' own order: every file this run has anything to show for, whether or
// not it came out whole.
//
// It replaces a selector that narrowed to Run.Complete (TOR-124). ReplayRun's
// own contract is "show what this run produced", and Complete answers a
// narrower question - "which files came out WHOLE" - so reading it there
// contradicted the sentence above it. A file with five frames of twelve is
// left in; a file with none is left out, because there is nothing to show and
// cache.Usable says so.
//
// It reads each manifest to decide, where the old one read a list of indices,
// and that cost is why it is only on this path: reopening is a person clicking
// once, while serveFromCache runs on every live request and has a cheaper
// question to answer.
func producedVideos(layout output.Layout, videos []swarm.FileInfo) []swarm.FileInfo {
	out := make([]swarm.FileInfo, 0, len(videos))
	for _, f := range videos {
		m, ok := cache.LoadManifest(layout.FileDir(f.Index, f.Path))
		if !ok || !cache.Usable(m) {
			continue
		}
		out = append(out, f)
	}
	return out
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
		// Off the record rather than off `videos`: the two lists are not the
		// same thing (TOR-180), and this one is absent for a record that
		// predates the field - see filesFromRecord.
		Files:    filesFromRecord(h.record),
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
			// A point that produced nothing is republished as the skip it
			// was, not as a frame with no path (TOR-124). This func's whole
			// contract is that a replay is the event sequence a live run
			// produces, and a live run publishes FrameSkipped here - so a
			// FrameReady with an empty Path was the one place the replay
			// said something the original run never said. A client acting on
			// it would try to show a frame that does not exist; the web UI
			// resolved that empty path against its own page.
			//
			// The manifest keeps the CODE but not the free-text reason, which
			// was never recorded, so the reason is left empty rather than
			// invented. The code is the part a client matches on
			// (core.ErrorCode's own doc).
			if f.Shift == manifest.ShiftFailed || f.Path == "" {
				bus.Publish(FrameSkipped{
					File:      m.File.Index,
					Index:     f.Index,
					Requested: time.Duration(f.RequestedMS) * time.Millisecond,
					Code:      ErrorCode(f.Error),
				})
				continue
			}

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

		// Frames is what the file HAS, which since TOR-124 need not be every
		// point the manifest records: len(m.Frames) counts the gaps too, and
		// on a holed run that reported a file of twelve frames where five
		// exist - the same miscount TOR-110 fixed in the page's own summary.
		produced := 0
		for _, f := range m.Frames {
			if f.Shift != manifest.ShiftFailed && f.Path != "" {
				produced++
			}
		}

		bus.Publish(FileDone{
			File:         m.File.Index,
			Path:         m.File.Path,
			Frames:       produced,
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
