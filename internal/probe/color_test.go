package probe

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// dolbyVisionProbeOutput is ffprobe's own output, captured verbatim from a run
// against an MP4 whose video sample entry carries a Dolby Vision
// configuration record, and then trimmed to the fields Inspect reads.
//
// A fixture rather than a rendered file because there is no honest way to
// render one. No encoder in any bundled build - nor in libx265, which asks for
// an RPU file it cannot generate - produces a Dolby Vision stream, and
// ffmpeg's mp4 muxer writes no dvcC box even when libx265 is told
// dolby-vision-profile=5: the resulting file probes as ordinary HDR10, with
// codec_tag hev1 and no side data at all. The file this came from was made by
// splicing a dvcC box into an HDR10 clip by hand, which forges the
// *signalling* and nothing else - the coded picture is still BT.2020 YUV, so
// it says nothing about how a profile 5 picture looks and everything about
// what ffprobe reports for one. Reading dv_profile is the whole of what this
// code does with it, so that is a fair test of it and the only one available.
const dolbyVisionProbeOutput = `{
  "streams": [
    {
      "index": 0,
      "codec_name": "hevc",
      "profile": "Main 10",
      "codec_type": "video",
      "codec_tag_string": "dvh1",
      "width": 640,
      "height": 360,
      "color_range": "tv",
      "color_space": "bt2020nc",
      "color_transfer": "smpte2084",
      "color_primaries": "bt2020",
      "avg_frame_rate": "25/1",
      "bit_rate": "139907",
      "side_data_list": [
        {
          "side_data_type": "DOVI configuration record",
          "dv_version_major": 1,
          "dv_version_minor": 0,
          "dv_profile": 5,
          "dv_level": 22,
          "rpu_present_flag": 1,
          "el_present_flag": 0,
          "bl_present_flag": 1,
          "dv_bl_signal_compatibility_id": 0,
          "dv_md_compression": "none"
        }
      ]
    }
  ],
  "format": {
    "format_name": "mov,mp4,m4a,3gp,3g2,mj2",
    "duration": "12.000000",
    "size": "217063",
    "bit_rate": "144708"
  }
}`

func TestInspectReadsTheDolbyVisionProfile(t *testing.T) {
	info, err := parseInspect([]byte(dolbyVisionProbeOutput))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if got := info.Video.DolbyVisionProfile; got != 5 {
		t.Errorf("DolbyVisionProfile = %d, want 5", got)
	}
	// The colour tags come from the same document, and a profile 5 stream that
	// claims BT.2020 PQ is exactly the case the profile has to override.
	if got := info.Video.ColorTransfer; got != "smpte2084" {
		t.Errorf("ColorTransfer = %q, want %q", got, "smpte2084")
	}
	if got := info.Video.ColorPrimaries; got != "bt2020" {
		t.Errorf("ColorPrimaries = %q, want %q", got, "bt2020")
	}
	if got := info.Video.ColorSpace; got != "bt2020nc" {
		t.Errorf("ColorSpace = %q, want %q", got, "bt2020nc")
	}
	if got := info.Video.ColorRange; got != "tv" {
		t.Errorf("ColorRange = %q, want %q", got, "tv")
	}
}

// TestInspectLeavesTheDolbyVisionProfileAtZero is the other half: a file with
// no such record must not come out looking like one, or every HDR10 remux
// would be refused the tone map.
func TestInspectLeavesTheDolbyVisionProfileAtZero(t *testing.T) {
	const plainHDR10 = `{
  "streams": [{"index":0,"codec_name":"hevc","codec_type":"video","width":640,"height":360,
    "avg_frame_rate":"25/1","color_range":"tv","color_space":"bt2020nc",
    "color_transfer":"smpte2084","color_primaries":"bt2020"}],
  "format": {"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"12.000000"}
}`

	info, err := parseInspect([]byte(plainHDR10))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := info.Video.DolbyVisionProfile; got != 0 {
		t.Errorf("DolbyVisionProfile = %d, want 0", got)
	}
	if got := info.Video.ColorTransfer; got != "smpte2084" {
		t.Errorf("ColorTransfer = %q, want %q", got, "smpte2084")
	}
}

// TestInspectReportsColourTagsFromARealFile is the same fields read off a file
// ffprobe actually opened, so the fixtures above cannot drift away from what
// ffprobe emits without something failing.
//
// The clip is tagged rather than converted: nothing here depends on the
// picture, only on the tags surviving the container and coming back out of
// ffprobe with the spelling frames.ToneMapFor switches on.
func TestInspectReportsColourTagsFromARealFile(t *testing.T) {
	tools := locateTools(t)

	path := filepath.Join(t.TempDir(), "tagged.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=4",
		"-vf", "format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc:range=tv",
		"-c:v", "ffv1",
		path,
	); err != nil {
		t.Fatalf("render tagged clip: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("clip was not written: %v", err)
	}

	info, err := New(tools).Inspect(ctx, path)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	for _, want := range []struct{ field, got, expected string }{
		{"ColorTransfer", info.Video.ColorTransfer, "smpte2084"},
		{"ColorPrimaries", info.Video.ColorPrimaries, "bt2020"},
		{"ColorSpace", info.Video.ColorSpace, "bt2020nc"},
		{"ColorRange", info.Video.ColorRange, "tv"},
	} {
		if want.got != want.expected {
			t.Errorf("%s = %q, want %q", want.field, want.got, want.expected)
		}
	}
	if info.Video.DolbyVisionProfile != 0 {
		t.Errorf("DolbyVisionProfile = %d on a file with no such record", info.Video.DolbyVisionProfile)
	}
}
