package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/madmurdok/torpeek/internal/core"
)

// reportText writes progress a person reads. Frames are announced as they land
// so a long run visibly advances.
func reportText(events <-chan core.Event, stdout, stderr io.Writer) int {
	code := ExitOK

	for ev := range events {
		switch e := ev.(type) {
		case core.MetadataReady:
			fmt.Fprintf(stdout, "%s\n", e.Name)
			fmt.Fprintf(stdout, "  %d video file(s)", len(e.Videos))
			if e.Private {
				fmt.Fprint(stdout, ", private torrent (DHT and PEX off)")
			}
			fmt.Fprintln(stdout)
			if e.BlindDHT {
				fmt.Fprintln(stderr, "  warning: this magnet carries no trackers, so DHT was used before the private flag could be checked")
			}

		case core.FileStarted:
			fmt.Fprintf(stdout, "\n%s\n", e.Path)
			fmt.Fprintf(stdout, "  %s, %dx%d", e.Media.Duration.Round(time.Second), e.Media.Video.Width, e.Media.Video.Height)
			if e.Media.Video.Codec != "" {
				fmt.Fprintf(stdout, ", %s", e.Media.Video.Codec)
			}
			if bpp := e.Media.Video.BitsPerPixel(); bpp > 0 {
				fmt.Fprintf(stdout, ", %.3f bits/pixel", bpp)
			}
			fmt.Fprintln(stdout)
			for _, a := range e.Media.Audio {
				fmt.Fprintf(stdout, "  audio: %s", describeTrack(a.Language, a.Title, a.Codec))
				if a.Channels > 0 {
					fmt.Fprintf(stdout, ", %d ch", a.Channels)
				}
				fmt.Fprintln(stdout)
			}
			for _, s := range e.Media.Subtitles {
				fmt.Fprintf(stdout, "  subtitles: %s\n", describeTrack(s.Language, s.Title, s.Codec))
			}

		case core.FrameReady:
			note := ""
			switch e.Shift {
			case core.ShiftUnavailable:
				note = fmt.Sprintf(" (shifted from %s: pieces unavailable)", e.Requested.Round(time.Second))
			case core.ShiftBlank:
				note = fmt.Sprintf(" (stepped from %s: blank frame)", e.Requested.Round(time.Second))
			}
			fmt.Fprintf(stdout, "  frame %02d  %s%s\n", e.Index, e.Actual.Round(time.Second), note)

		case core.FrameSkipped:
			fmt.Fprintf(stderr, "  frame %02d  %s skipped (%s)\n", e.Index, e.Requested.Round(time.Second), e.Code)

		case core.BudgetWarning:
			fmt.Fprintf(stderr, "  warning: %s used of %s\n", humanBytes(e.SpentBytes), humanBytes(e.LimitBytes))

		case core.FileDone:
			fmt.Fprintf(stdout, "  %d frames", e.Frames)
			if e.Skipped > 0 {
				fmt.Fprintf(stdout, ", %d skipped", e.Skipped)
			}
			fmt.Fprintln(stdout)

		case core.Failed:
			if e.File < 0 {
				fmt.Fprintf(stderr, "torpeek: %s: %v\n", e.Code, e.Err)
				code = ExitFailed
			} else {
				fmt.Fprintf(stderr, "  file %d failed: %s: %v\n", e.File, e.Code, e.Err)
			}

		case core.Done:
			fmt.Fprintf(stdout, "\n%d frames from %d file(s) in %s, %s downloaded\n",
				e.Frames, e.Files, e.Elapsed.Round(time.Second), humanBytes(e.DownloadedByte))
			switch e.Reason {
			case core.StopBudget:
				fmt.Fprintln(stderr, "stopped at a limit; what was produced is kept")
				code = ExitPartial
			case core.StopCancelled:
				fmt.Fprintln(stderr, "cancelled; what was produced is kept")
				code = ExitPartial
			default:
				if e.Frames == 0 && code == ExitOK {
					code = ExitFailed
				}
			}
		}
	}

	return code
}

// reportJSON writes one event per line, for a caller that is a program.
//
// The wire form carries an explicit type field: a consumer should not have to
// guess an event from which keys happen to be present.
func reportJSON(events <-chan core.Event, stdout, stderr io.Writer) int {
	enc := json.NewEncoder(stdout)
	code := ExitOK

	for ev := range events {
		if failed, ok := ev.(core.Failed); ok && failed.File < 0 {
			code = ExitFailed
		}
		if done, ok := ev.(core.Done); ok {
			switch done.Reason {
			case core.StopBudget, core.StopCancelled:
				code = ExitPartial
			default:
				if done.Frames == 0 && code == ExitOK {
					code = ExitFailed
				}
			}
		}

		if err := enc.Encode(wire(ev)); err != nil {
			fmt.Fprintf(stderr, "torpeek: writing events: %v\n", err)
			return ExitFailed
		}
	}

	return code
}

// wire is the JSON shape of an event.
func wire(ev core.Event) any {
	switch e := ev.(type) {
	case core.MetadataReady:
		return map[string]any{
			"type": "metadata_ready", "name": e.Name, "infohash": e.InfoHash,
			"private": e.Private, "videos": len(e.Videos), "blind_dht": e.BlindDHT,
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

func describeTrack(language, title, codec string) string {
	if language == "" {
		language = "und"
	}
	out := language
	if title != "" {
		out += " \"" + title + "\""
	}
	if codec != "" {
		out += ", " + codec
	}
	return out
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
