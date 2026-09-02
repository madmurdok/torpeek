package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/wire"
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
			if len(e.Selected) != len(e.Videos) {
				fmt.Fprintf(stdout, ", %d selected", len(e.Selected))
			}
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
			if e.SheetPath != "" {
				fmt.Fprintf(stdout, "  sheet: %s\n", e.SheetPath)
			}

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

		if err := enc.Encode(wire.Event(ev)); err != nil {
			fmt.Fprintf(stderr, "torpeek: writing events: %v\n", err)
			return ExitFailed
		}
	}

	return code
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

// listFiles prints what a torrent holds and returns without taking a frame.
func listFiles(ctx context.Context, cfg core.Config, asJSON bool, stdout, stderr io.Writer) int {
	contents, err := (&core.Engine{}).List(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		return ExitFailed
	}

	if asJSON {
		videos := make([]map[string]any, 0, len(contents.Videos))
		for _, f := range contents.Videos {
			videos = append(videos, map[string]any{
				"index": f.Index, "path": f.Path, "bytes": f.Length,
			})
		}
		if err := json.NewEncoder(stdout).Encode(map[string]any{
			"type": "contents", "name": contents.Name, "infohash": contents.InfoHash,
			"private": contents.Private, "videos": videos, "downloaded": contents.Downloaded,
		}); err != nil {
			fmt.Fprintf(stderr, "torpeek: writing contents: %v\n", err)
			return ExitFailed
		}
		return ExitOK
	}

	fmt.Fprintf(stdout, "%s\n", contents.Name)
	if len(contents.Videos) == 0 {
		fmt.Fprintln(stderr, "  no video files")
		return ExitFailed
	}
	for _, f := range contents.Videos {
		fmt.Fprintf(stdout, "  %3d  %9s  %s\n", f.Index, humanBytes(f.Length), f.Path)
	}
	fmt.Fprintf(stdout, "\nname one with -file, by index or by pattern; %s fetched to find out\n",
		humanBytes(contents.Downloaded))
	return ExitOK
}
