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
)

// Event renders one event as the object clients exchange.
//
// The shape carries an explicit type field: a consumer should not have to
// guess an event from which keys happen to be present. A caller is free to
// add keys of its own to the returned map - the web server attaches URLs for
// the files an event names - which is an addition to this vocabulary rather
// than a second one.
func Event(ev core.Event) map[string]any {
	switch e := ev.(type) {
	case core.MetadataReady:
		return map[string]any{
			"type": "metadata_ready", "name": e.Name, "infohash": e.InfoHash,
			"private": e.Private, "videos": len(e.Videos), "blind_dht": e.BlindDHT,
			"selected": e.Selected,
		}
	case core.FileStarted:
		return map[string]any{
			"type": "file_started", "file": e.File, "path": e.Path,
			"duration_ms": e.Media.Duration.Milliseconds(),
			"width":       e.Media.Video.Width, "height": e.Media.Video.Height,
			"codec": e.Media.Video.Codec, "audio": len(e.Media.Audio),
			"subtitles": len(e.Media.Subtitles), "planned": len(e.Plan),
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
