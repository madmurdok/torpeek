package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/probe"
)

// TestTheDynamicRangeIsNamed is the half of TOR-108 the frames cannot do
// themselves. A person previewing an HDR remux who is told nothing has exactly
// one reading of a frame that looks wrong, and it is that torpeek is broken -
// so the run says what the source was and what was done about it. Where
// nothing could be done, naming the source is the whole of what we have.
func TestTheDynamicRangeIsNamed(t *testing.T) {
	cases := []struct {
		name     string
		video    probe.VideoStream
		contains string
		absent   string
	}{{
		name: "an HDR10 source says it was converted",
		video: probe.VideoStream{
			Width: 3840, Height: 2160, Codec: "hevc",
			ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", ColorSpace: "bt2020nc",
		},
		contains: "hdr10 tone mapped to SDR",
	}, {
		name: "an HLG source likewise",
		video: probe.VideoStream{
			Width: 1920, Height: 1080, Codec: "hevc",
			ColorTransfer: "arib-std-b67", ColorPrimaries: "bt2020", ColorSpace: "bt2020nc",
		},
		contains: "hlg tone mapped to SDR",
	}, {
		name: "Dolby Vision profile 5 is named and the frames are not vouched for",
		video: probe.VideoStream{
			Width: 3840, Height: 2160, Codec: "hevc",
			ColorTransfer: "smpte2084", DolbyVisionProfile: 5,
		},
		contains: "dolby-vision-p5 (frames not colour-accurate)",
	}, {
		name: "an ordinary SDR source says nothing about colour at all",
		video: probe.VideoStream{
			Width: 1920, Height: 1080, Codec: "h264",
			ColorTransfer: "bt709", ColorPrimaries: "bt709", ColorSpace: "bt709",
		},
		absent: "tone mapped",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := make(chan core.Event, 2)
			events <- core.FileStarted{
				File: 0,
				Path: "movie.mkv",
				Media: probe.MediaInfo{
					Duration: 90 * time.Minute,
					Video:    tc.video,
				},
			}
			events <- core.Done{Files: 1, Frames: 1}
			close(events)

			var stdout, stderr bytes.Buffer
			if code := reportText(events, &stdout, &stderr, 1); code != ExitOK {
				t.Fatalf("exit code = %d, want %d", code, ExitOK)
			}
			text := stdout.String()

			if tc.contains != "" && !strings.Contains(text, tc.contains) {
				t.Errorf("report does not mention %q:\n%s", tc.contains, text)
			}
			if tc.absent != "" && strings.Contains(text, tc.absent) {
				t.Errorf("report mentions %q about an SDR file:\n%s", tc.absent, text)
			}
		})
	}
}
