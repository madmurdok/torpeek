package wire

import (
	"errors"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/probe"
	"github.com/madmurdok/torpeek/internal/swarm"
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

	m := Event("", ev)

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
	m := Event("", core.FileStarted{File: 0, Path: "x.mkv"})

	audio, ok := m["audio"].([]map[string]any)
	if !ok || audio == nil || len(audio) != 0 {
		t.Errorf("audio = %#v, want an empty, non-nil slice", m["audio"])
	}
	subs, ok := m["subtitles"].([]map[string]any)
	if !ok || subs == nil || len(subs) != 0 {
		t.Errorf("subtitles = %#v, want an empty, non-nil slice", m["subtitles"])
	}
}

// TestMetadataReadyCarriesFileList guards the file picker's only data
// source (TOR-66): a frontend cannot offer indices, paths or sizes to pick
// from if the wire boundary still collapses the file list to a count.
func TestMetadataReadyCarriesFileList(t *testing.T) {
	ev := core.MetadataReady{
		Name:     "Release",
		InfoHash: "abc",
		Videos: []swarm.FileInfo{
			{Index: 0, Path: "movie/episode-1.mkv", Length: 1_000_000, Offset: 0},
			{Index: 1, Path: "movie/episode-2.mkv", Length: 2_000_000, Offset: 1_000_000},
		},
		Selected: []int{0, 1},
	}

	m := Event("", ev)

	videos, ok := m["videos"].([]map[string]any)
	if !ok || len(videos) != 2 {
		t.Fatalf("videos = %#v, want 2 file entries", m["videos"])
	}
	if videos[0]["index"] != 0 || videos[0]["path"] != "movie/episode-1.mkv" || videos[0]["length"] != int64(1_000_000) {
		t.Errorf("videos[0] = %v", videos[0])
	}
	if videos[1]["index"] != 1 || videos[1]["path"] != "movie/episode-2.mkv" || videos[1]["length"] != int64(2_000_000) {
		t.Errorf("videos[1] = %v", videos[1])
	}
	if _, has := videos[0]["offset"]; has {
		t.Errorf("videos[0] carries offset, which no picker needs: %v", videos[0])
	}
}

// TestMetadataReadyWithNoVideosIsEmptyNotNil keeps the JSON shape stable
// the same way file_started's audio/subtitles arrays do: an empty array, not
// a null field a client would have to special-case.
func TestMetadataReadyWithNoVideosIsEmptyNotNil(t *testing.T) {
	m := Event("", core.MetadataReady{Name: "Release", InfoHash: "abc"})

	videos, ok := m["videos"].([]map[string]any)
	if !ok || videos == nil || len(videos) != 0 {
		t.Errorf("videos = %#v, want an empty, non-nil slice", m["videos"])
	}
}

// TestEveryEventCarriesTheRun is what a server holding more than one run at a
// time needs: not "most events" but every one of them. Nothing else in the
// vocabulary can stand in - "file" restarts at 0 in every run, "index" is a
// frame, and "infohash" names a torrent, which two runs can share.
func TestEveryEventCarriesTheRun(t *testing.T) {
	events := []core.Event{
		core.MetadataReady{Name: "Release", InfoHash: "abc", Selected: []int{0}},
		core.FileStarted{File: 0, Path: "movie.mkv"},
		core.FrameReady{File: 0, Index: 1, Path: "001.jpg"},
		core.FrameSkipped{File: 0, Index: 2, Code: core.CodeInternal},
		core.Progress{File: 0, FramesDone: 1, FramesTotal: 2},
		core.BudgetWarning{SpentBytes: 1, LimitBytes: 2},
		core.FileDone{File: 0, Path: "movie.mkv", Frames: 2},
		core.Done{Reason: core.StopCompleted},
		core.Failed{File: -1, Code: core.CodeInternal, Err: errors.New("boom")},
	}

	for _, ev := range events {
		m := Event("run-7", ev)
		if m["run"] != "run-7" {
			t.Errorf("%T rendered as %v, which does not say which run it belongs to", ev, m)
		}
	}
}

// TestNoRunLeavesTheKeyOut keeps the CLI's NDJSON as it was: a stream that is
// one run by construction has nothing to disambiguate, and "run":"" would
// read as a run whose id is empty.
func TestNoRunLeavesTheKeyOut(t *testing.T) {
	m := Event("", core.Done{Reason: core.StopCompleted})
	if _, ok := m["run"]; ok {
		t.Errorf("event rendered with no run still carries %v", m)
	}
}
