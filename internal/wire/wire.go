// Package wire is the JSON form of a core event.
//
// It lives apart from any one client because more than one speaks JSON: the
// CLI writes these objects as NDJSON, the web server sends the same objects
// over a WebSocket. Two hand-written mappings of the same event stream drift
// within a release, and then two clients disagree about what happened during
// the same run.
//
// The core itself must stay unaware of how it is displayed (REQUIREMENTS.md
// section 3.1), so the encoding sits here rather than beside the events.
package wire

import (
	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/probe"
	"github.com/madmurdok/torpeek/internal/swarm"
)

// Event renders one event as the object clients exchange.
//
// The shape carries an explicit type field: a consumer should not have to
// guess an event from which keys happen to be present. A caller is free to
// add keys of its own to the returned map - the web server attaches URLs for
// the files an event names - which is an addition to this vocabulary rather
// than a second one.
//
// run says which run the event belongs to, under the "run" key. Nothing else
// in the vocabulary can answer that: "file" is a torrent-file index that
// restarts at 0 every run, "index" is a frame index, and "infohash" names a
// torrent rather than a run - two runs of the same magnet share it. A server
// holding several runs at once (internal/web) needs the key on every event or
// a client cannot tell whose frame it just received.
//
// An empty run omits the key rather than writing "run":"". A stream that only
// ever describes one run - the CLI's NDJSON, which is a single run's output
// by construction - has nothing to disambiguate, and a blank identifier there
// would read as a run whose id happens to be empty.
func Event(run string, ev core.Event) map[string]any {
	m := event(ev)
	if run != "" {
		m["run"] = run
	}
	return m
}

func event(ev core.Event) map[string]any {
	switch e := ev.(type) {
	case core.MetadataReady:
		return map[string]any{
			"type": "metadata_ready", "name": e.Name, "infohash": e.InfoHash,
			"private": e.Private, "videos": videoFiles(e.Videos), "blind_dht": e.BlindDHT,
			"selected": e.Selected,
		}
	case core.FileStarted:
		// The summary panel (REQUIREMENTS.md section 3.3) needs more than the
		// video's own dimensions: audio tracks, subtitles, bitrate. probe
		// already inspected all of that before this event fired - the gap was
		// only that this map used to throw most of it away.
		return map[string]any{
			"type": "file_started", "file": e.File, "path": e.Path,
			"duration_ms": e.Media.Duration.Milliseconds(),
			"format":      e.Media.FormatName, "size": e.Media.Size, "bitrate": e.Media.BitRate,
			"width": e.Media.Video.Width, "height": e.Media.Video.Height,
			"codec": e.Media.Video.Codec, "profile": e.Media.Video.Profile,
			"fps": e.Media.Video.FPS, "video_bitrate": e.Media.Video.BitRate,
			"audio": audioTracks(e.Media.Audio), "subtitles": subtitleTracks(e.Media.Subtitles),
			"planned": len(e.Plan),
		}
	case core.FrameReady:
		return map[string]any{
			"type": "frame_ready", "file": e.File, "index": e.Index,
			"requested_ms": e.Requested.Milliseconds(), "actual_ms": e.Actual.Milliseconds(),
			"shift": string(e.Shift), "path": e.Path, "width": e.Width, "height": e.Height,
		}
	case core.FrameSkipped:
		return map[string]any{
			"type": "frame_skipped", "file": e.File, "index": e.Index,
			"requested_ms": e.Requested.Milliseconds(), "code": string(e.Code), "reason": e.Reason,
		}
	case core.Progress:
		return map[string]any{
			"type": "progress", "file": e.File, "frames_done": e.FramesDone,
			"frames_total": e.FramesTotal, "downloaded": e.DownloadedByte,
			"elapsed_ms": e.Elapsed.Milliseconds(), "peers": e.Peers, "seeds": e.Seeds,
		}
	case core.BudgetWarning:
		return map[string]any{
			"type": "budget_warning", "spent": e.SpentBytes, "limit": e.LimitBytes,
			"elapsed_ms": e.Elapsed.Milliseconds(), "limit_ms": e.LimitTime.Milliseconds(),
		}
	case core.FileDone:
		return map[string]any{
			"type": "file_done", "file": e.File, "path": e.Path,
			"frames": e.Frames, "skipped": e.Skipped,
			"manifest_path": e.ManifestPath, "sheet_path": e.SheetPath,
		}
	case core.Done:
		return map[string]any{
			"type": "done", "reason": string(e.Reason), "files": e.Files,
			"frames": e.Frames, "downloaded": e.DownloadedByte,
			"elapsed_ms": e.Elapsed.Milliseconds(),
		}
	case core.Failed:
		msg := ""
		if e.Err != nil {
			msg = e.Err.Error()
		}
		return map[string]any{
			"type": "failed", "file": e.File, "code": string(e.Code), "error": msg,
		}
	default:
		return map[string]any{"type": "unknown"}
	}
}

// videoFiles renders every video file the torrent holds, so a picker can be
// built from the same event that used to only report a count.
//
// index, path and length are all of swarm.FileInfo the run knows at this
// point - Offset is an internal detail no client needs, and anything else
// (duration, resolution) only exists after probing, which costs traffic a
// picker should not have to spend before someone has even chosen a file.
func videoFiles(files []swarm.FileInfo) []map[string]any {
	out := make([]map[string]any, len(files))
	for i, f := range files {
		out[i] = map[string]any{"index": f.Index, "path": f.Path, "length": f.Length}
	}
	return out
}

// audioTracks renders every audio track for the summary panel - the answer to
// "is this the dub and the language I wanted" (probe.AudioStream's own doc).
func audioTracks(tracks []probe.AudioStream) []map[string]any {
	out := make([]map[string]any, len(tracks))
	for i, t := range tracks {
		out[i] = map[string]any{
			"index": t.Index, "codec": t.Codec, "language": t.Language,
			"title": t.Title, "channels": t.Channels, "bitrate": t.BitRate,
			"default": t.Default,
		}
	}
	return out
}

// subtitleTracks renders every subtitle track for the summary panel.
func subtitleTracks(tracks []probe.SubtitleStream) []map[string]any {
	out := make([]map[string]any, len(tracks))
	for i, t := range tracks {
		out[i] = map[string]any{
			"index": t.Index, "codec": t.Codec, "language": t.Language,
			"title": t.Title, "forced": t.Forced, "default": t.Default,
		}
	}
	return out
}
