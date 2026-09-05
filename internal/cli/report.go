package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/wire"
)

// reportText writes progress a person reads. Frames are announced as they land
// so a long run visibly advances.
//
// count is the run's frames-per-file, which the events deliberately do not
// carry: it is this caller's own flag, and MetadataReady describes the torrent
// rather than the request. It is here for one line - see the cost report on
// MetadataReady, TOR-50.
func reportText(events <-chan core.Event, stdout, stderr io.Writer, count int) int {
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
			// The cost, before any of it is spent. -n is frames PER video
			// file and section 2.2 processes every one of them, so on a
			// torrent bundling six quality variants the number in the flag
			// means six times what it looks like: -n 6 against Sintel cost
			// 223 MB where one file costs about 11 MB, and nobody chose the
			// 20x (TOR-50). MetadataReady arrives before a single frame is
			// fetched, which makes this the last moment the number is still
			// only a plan.
			//
			// Frames, not megabytes: the picker in the web UI says
			// "N file(s) x n = N frames" and this is deliberately the same
			// arithmetic in the same order, because two interfaces quoting
			// two different estimates of one run is the drift TOR-80 spent a
			// ticket removing. A traffic figure would also have to guess the
			// fetch window, and a guess is worse here than a count that is
			// exactly right.
			if count > 0 {
				fmt.Fprintf(stdout, "  %d file(s) x %d = %d frames to fetch\n", len(e.Selected), count, len(e.Selected)*count)
			}
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
			// Said out loud because the frames cannot say it themselves. A
			// person previewing an HDR remux who is told nothing has only one
			// reading of a flat grey frame available, and it is that torpeek
			// is broken (TOR-108). Where the conversion could not be done,
			// naming the source is the whole of what we can offer.
			if tm := core.ToneMapOf(e.Media.Video); tm.Source != "" {
				if tm.Applies() {
					fmt.Fprintf(stdout, ", %s tone mapped to SDR", tm.Source)
				} else {
					fmt.Fprintf(stdout, ", %s (frames not colour-accurate)", tm.Source)
				}
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
			if e.TorrentPath != "" {
				// Worth a line of its own: it is the one output of a run that
				// is useful somewhere other than torpeek - hand it to a client
				// and the torrent that was just previewed starts downloading.
				// The caveat belongs here too, where the path is, rather than
				// only in a doc comment nobody reading a terminal will open.
				fmt.Fprintf(stdout, "torrent: %s (info dictionary as received, so the infohash matches; the creation date, comment and created-by are generated)\n", e.TorrentPath)
			}
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

		if err := enc.Encode(wire.Event("", ev)); err != nil {
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

// parseSize is humanBytes' inverse: it turns what a person types for
// -cache-max-size, like "20G" or "512MB", into bytes. A bare number is
// bytes; a trailing K/M/G/T (case-insensitive, a trailing "B" tolerated
// either way) multiplies by the same power of 1024 humanBytes prints with -
// so a ceiling typed as "20G" and a size this package later reports as
// "20.0 GiB" name the same number of bytes. An empty string is 0, the "no
// ceiling" default (REQUIREMENTS.md 2.9), so the flag's own default value
// does not need a special case here.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	digits := s
	mult := int64(1)
	upper := strings.ToUpper(s)
	suffixes := []struct {
		suffix string
		mult   int64
	}{
		{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10},
		{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10},
		{"B", 1},
	}
	for _, sx := range suffixes {
		if strings.HasSuffix(upper, sx.suffix) {
			digits = strings.TrimSpace(s[:len(s)-len(sx.suffix)])
			mult = sx.mult
			break
		}
	}

	n, err := strconv.ParseFloat(digits, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q: want a number with an optional K/M/G/T suffix", s)
	}
	return int64(n * float64(mult)), nil
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
