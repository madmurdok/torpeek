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
	"time"

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
			"private": e.Private, "videos": VideoFiles(e.Videos), "blind_dht": e.BlindDHT,
			"selected": e.Selected,
		}
	case core.FileStarted:
		tm := core.ToneMapOf(e.Media.Video)
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
			// The plan itself, not only its size (TOR-110). A page that knows
			// where every capture point WILL be can lay the whole grid out
			// before the first piece is fetched, so it stops shoving itself
			// around for the minute a run takes - and a run is mostly
			// waiting, so that is the state a person looks at longest.
			//
			// It is the plan BEFORE any shifting (core.FileStarted.Plan's own
			// doc), which is what makes it usable as an identity: a frame
			// that lands somewhere else still belongs to the point it was
			// asked for, and frame_ready's index says which. Sent alongside
			// `planned` rather than replacing it, because a count is what a
			// log line wants and a list is what a layout wants.
			"plan": planMS(e.Plan),
			// What the source's dynamic range is, and whether the frames were
			// converted out of it. A consumer reading this stream sees the
			// same two facts the CLI prints and the manifest records, because
			// a frame that came out of an HDR source cannot say either for
			// itself and looks broken without them (TOR-108). Empty and false
			// for an ordinary SDR file, which is every file this stream used
			// to carry.
			"dynamic_range": tm.Source, "tone_mapped": tm.Applies(),
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
		m := map[string]any{
			"type": "progress", "file": e.File, "frames_done": e.FramesDone,
			"frames_total": e.FramesTotal, "downloaded": e.DownloadedByte,
			"uploaded":   e.UploadedByte,
			"elapsed_ms": e.Elapsed.Milliseconds(),
			"peers":      e.Peers, "seeds": e.Seeds,
		}
		// download_bps/upload_bps are this heartbeat's INSTANTANEOUS speed
		// in bytes per second - the byte delta since the previous heartbeat
		// divided by the real time between the two, never a nominal period
		// (core.Progress.DownloadRate's own doc). A stall followed by a
		// burst therefore reports the burst averaged over the whole stalled
		// window, never as a sustained peak. Present only from the run's
		// second heartbeat on: a client must read a missing key as "not yet
		// known", not as "stopped" or "0 B/s" - the same "absent, not zero"
		// rule core.Progress.Swarm's own doc argues for (TOR-119, TOR-111,
		// TOR-135), applied here for the same reason.
		if e.DownloadRate != nil {
			m["download_bps"] = *e.DownloadRate
		}
		if e.UploadRate != nil {
			m["upload_bps"] = *e.UploadRate
		}
		// swarm is the live availability reading core.Progress.Swarm
		// carries, under the identical "absent, not zero" rule
		// download_bps/upload_bps just followed above, and for the
		// identical reason (core.Progress.Swarm's own doc, TOR-119,
		// TOR-111, TOR-135): a torrent that has not been asked for
		// bytes yet has no reading, and 0 copies would read as "the
		// swarm holds nothing" - the opposite of "we do not know".
		//
		// copies_per_piece names the unit so nobody can read it as a
		// fraction - it is swarm.Availability's own unit, copies PER
		// PIECE, and commonly exceeds 1.0 (0.8 means pieces are
		// missing from the swarm, 3.2 means it is healthy).
		if e.Swarm != nil {
			m["swarm"] = map[string]any{
				"copies_per_piece": e.Swarm.CopiesPerPiece,
				"unavailable":      e.Swarm.Unavailable,
				"pieces":           e.Swarm.NumPieces,
			}
		}
		// stall is TOR-141's own reading: which of the distinct "nothing
		// is landing" causes explains THIS file right now, and how long
		// - continuously, not merely "as of ever" - it has been true
		// (core.Progress.Stall's own doc has the rule that keeps the
		// duration honest across a changing cause). Absent under the
		// same "absent, not zero" discipline every other optional key on
		// this event already follows: it means the run IS progressing at
		// this heartbeat, never that nothing is known.
		//
		// code is one of core's own ErrorCode strings (CodeNoPeers,
		// CodeUnavailable, CodeNoMetadata, CodeReadStalled) - the same
		// vocabulary "code" already carries on frame_skipped and failed,
		// so a client that already reads those needs no second lookup
		// table for this one. since_ms, not a bare "since", to match
		// every other duration this vocabulary carries (elapsed_ms,
		// requested_ms, duration_ms).
		if e.Stall != nil {
			m["stall"] = map[string]any{
				"code": string(e.Stall.Code), "since_ms": e.Stall.Since.Milliseconds(),
			}
		}
		return m
	case core.BudgetWarning:
		// scope says whether spent/limit are this run's or the whole
		// client's, and is the difference between "you asked for a lot" and
		// "the machine is nearly out" (core.LimitScope). Always present, so
		// a consumer reads one key rather than inferring the scope from
		// limit_ms happening to be zero.
		return map[string]any{
			"type": "budget_warning", "scope": string(e.Scope),
			"spent": e.SpentBytes, "limit": e.LimitBytes,
			"elapsed_ms": e.Elapsed.Milliseconds(), "limit_ms": e.LimitTime.Milliseconds(),
		}
	case core.FileDone:
		return map[string]any{
			"type": "file_done", "file": e.File, "path": e.Path,
			"frames": e.Frames, "skipped": e.Skipped,
			"manifest_path": e.ManifestPath, "sheet_path": e.SheetPath,
		}
	case core.Done:
		// torrent_path is the run's own .torrent, the run-scoped sibling of
		// file_done's manifest_path and sheet_path. Always present, empty
		// when the run has none to offer (core.Done explains when that is),
		// so a consumer reads one key rather than testing for its absence.
		done := map[string]any{
			"type": "done", "reason": string(e.Reason), "files": e.Files,
			"frames": e.Frames, "downloaded": e.DownloadedByte,
			"elapsed_ms": e.Elapsed.Milliseconds(), "torrent_path": e.TorrentPath,
		}
		// Present only when there is something to warn about, unlike
		// torrent_path above: an absent path is a fact a reader must handle
		// either way, while an absent warning is nothing at all, and a
		// consumer that sees the key knows without checking a length that
		// this run left something out (TOR-79).
		if len(e.Warnings) > 0 {
			done["warnings"] = e.Warnings
		}
		return done
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

// VideoFiles renders every video file the torrent holds, so a picker can be
// built from the same event that used to only report a count.
//
// index, path and length are all of swarm.FileInfo the run knows at this
// point - Offset is an internal detail no client needs, and anything else
// (duration, resolution) only exists after probing, which costs traffic a
// picker should not have to spend before someone has even chosen a file.
//
// Exported because metadata_ready is no longer the only message that carries
// this list: a torrent parked for someone to choose files (needs_action,
// TOR-67) sends the very same one, and that record is the web server's own
// rather than a core event, so it is built outside this package. Two hand-
// written shapes for one list would drift exactly the way this package
// exists to prevent - and the page would then need two ways to read a file.
func VideoFiles(files []swarm.FileInfo) []map[string]any {
	out := make([]map[string]any, len(files))
	for i, f := range files {
		out[i] = map[string]any{"index": f.Index, "path": f.Path, "length": f.Length}
	}
	return out
}

// audioTracks renders every audio track for the summary panel - the answer to
// planMS is a capture plan as milliseconds, the unit every other timestamp on
// this stream already uses. Never nil: a file with no plan sends an empty
// array rather than a null, so a consumer can iterate it without asking.
func planMS(plan []time.Duration) []int64 {
	out := make([]int64, 0, len(plan))
	for _, at := range plan {
		out = append(out, at.Milliseconds())
	}
	return out
}

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
