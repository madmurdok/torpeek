package core

import (
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

	videos := make([]swarm.FileInfo, 0, len(record.Videos))
	for _, f := range record.Videos {
		videos = append(videos, swarm.FileInfo{
			Index: f.Index, Path: f.Path, Length: f.Bytes, Offset: f.Offset,
		})
	}

	selected, err := swarm.Select(videos, cfg.Files)
	if err != nil || len(selected) == 0 {
		// A selection naming nothing is a real error, but it is the live run's
		// to report - with the torrent's own file list rather than a cached
		// one that may be stale.
		return false
	}

	complete := make(map[int]bool, len(record.Complete))
	for _, i := range record.Complete {
		complete[i] = true
	}

	manifests := make([]manifest.Manifest, 0, len(selected))
	for _, file := range selected {
		if !complete[file.Index] {
			return false
		}
		m, ok := cache.LoadManifest(layout.FileDir(file.Index, file.Path))
		if !ok || !cache.Usable(m) {
			return false
		}
		manifests = append(manifests, m)
	}

	bus.Publish(MetadataReady{
		Name:     record.Name,
		InfoHash: record.InfoHash,
		Private:  record.Private,
		Videos:   videos,
		Selected: indicesOf(selected),
	})

	frames := 0
	for _, m := range manifests {
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

		bus.Publish(FileDone{
			File:   m.File.Index,
			Path:   m.File.Path,
			Frames: len(m.Frames),
		})
	}

	bus.Publish(Done{
		Files:  len(manifests),
		Frames: frames,
		// Nothing was downloaded, and the elapsed time is the cost of reading
		// a few files - which is the answer the criterion is asking for.
		DownloadedByte: 0,
		Elapsed:        time.Since(started),
		Reason:         StopCompleted,
	})
	return true
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
