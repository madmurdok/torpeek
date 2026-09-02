package wire

import (
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/probe"
)

// TestFileStartedCarriesTrackDetail guards the summary panel's data: the
// web UI (TOR-25) renders audio tracks, subtitles, bitrate and resolution
// straight from this event, so the fields have to be the full track list,
// not just a count.
func TestFileStartedCarriesTrackDetail(t *testing.T) {
	ev := core.FileStarted{
		File: 1,
		Path: "movie.mkv",
		Media: probe.MediaInfo{
			FormatName: "matroska,webm",
			Duration:   90 * time.Minute,
			Size:       123456,
			BitRate:    5_000_000,
			Video: probe.VideoStream{
				Codec: "h264", Profile: "High", Width: 1920, Height: 1080,
				FPS: 23.976, BitRate: 4_500_000,
			},
			Audio: []probe.AudioStream{
				{Index: 1, Codec: "aac", Language: "eng", Title: "Commentary", Channels: 2, BitRate: 128_000, Default: true},
				{Index: 2, Codec: "ac3", Language: "rus", Channels: 6},
			},
			Subtitles: []probe.SubtitleStream{
				{Index: 0, Codec: "subrip", Language: "eng", Forced: false, Default: true},
			},
		},
		Plan: []time.Duration{0, time.Second},
	}

	m := Event(ev)

	if m["type"] != "file_started" {
		t.Fatalf("type = %v, want file_started", m["type"])
	}
	if m["width"] != 1920 || m["height"] != 1080 {
		t.Errorf("resolution = %vx%v, want 1920x1080", m["width"], m["height"])
	}
	if m["bitrate"] != int64(5_000_000) {
		t.Errorf("bitrate = %v, want 5000000", m["bitrate"])
	}
	if m["video_bitrate"] != int64(4_500_000) {
		t.Errorf("video_bitrate = %v, want 4500000", m["video_bitrate"])
	}

	audio, ok := m["audio"].([]map[string]any)
	if !ok || len(audio) != 2 {
		t.Fatalf("audio = %v, want 2 tracks", m["audio"])
	}
	if audio[0]["language"] != "eng" || audio[0]["title"] != "Commentary" || audio[0]["default"] != true {
		t.Errorf("audio[0] = %v", audio[0])
	}

	subs, ok := m["subtitles"].([]map[string]any)
	if !ok || len(subs) != 1 {
		t.Fatalf("subtitles = %v, want 1 track", m["subtitles"])
	}
	if subs[0]["language"] != "eng" || subs[0]["default"] != true {
		t.Errorf("subtitles[0] = %v", subs[0])
	}
}

// TestFileStartedWithNoTracksIsEmptyNotNil keeps the JSON shape stable when a
// file has no audio or subtitles: an empty array, not a null field a client
// would have to special-case.
func TestFileStartedWithNoTracksIsEmptyNotNil(t *testing.T) {
	m := Event(core.FileStarted{File: 0, Path: "x.mkv"})

	audio, ok := m["audio"].([]map[string]any)
	if !ok || audio == nil || len(audio) != 0 {
		t.Errorf("audio = %#v, want an empty, non-nil slice", m["audio"])
	}
	subs, ok := m["subtitles"].([]map[string]any)
	if !ok || subs == nil || len(subs) != 0 {
		t.Errorf("subtitles = %#v, want an empty, non-nil slice", m["subtitles"])
	}
}
