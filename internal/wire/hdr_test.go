package wire

import (
	"testing"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/probe"
)

// TestFileStartedCarriesTheDynamicRange keeps the three surfaces saying the
// same thing. A consumer of this stream has the same problem a person reading
// the terminal has: an HDR frame looks broken and cannot explain itself, so
// the event says what the source was and whether it was converted (TOR-108).
func TestFileStartedCarriesTheDynamicRange(t *testing.T) {
	cases := []struct {
		name         string
		video        probe.VideoStream
		dynamicRange string
		toneMapped   bool
	}{
		{"hdr10", probe.VideoStream{ColorTransfer: "smpte2084", ColorPrimaries: "bt2020"}, "hdr10", true},
		{"hlg", probe.VideoStream{ColorTransfer: "arib-std-b67", ColorPrimaries: "bt2020"}, "hlg", true},
		{"dolby vision profile 5", probe.VideoStream{ColorTransfer: "smpte2084", DolbyVisionProfile: 5}, "dolby-vision-p5", false},
		{"sdr", probe.VideoStream{ColorTransfer: "bt709"}, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Event("", core.FileStarted{
				File: 0, Path: "movie.mkv",
				Media: probe.MediaInfo{Video: tc.video},
			})
			if got := m["dynamic_range"]; got != tc.dynamicRange {
				t.Errorf("dynamic_range = %v, want %q", got, tc.dynamicRange)
			}
			if got := m["tone_mapped"]; got != tc.toneMapped {
				t.Errorf("tone_mapped = %v, want %v", got, tc.toneMapped)
			}
		})
	}
}
