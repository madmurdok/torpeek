package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/hdrtest"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/probe"
	"github.com/madmurdok/torpeek/internal/torrenttest"
)

func TestToneMapOfReadsAProbedStream(t *testing.T) {
	hdr := ToneMapOf(probe.VideoStream{
		ColorTransfer: "smpte2084", ColorPrimaries: "bt2020",
		ColorSpace: "bt2020nc", ColorRange: "tv",
	})
	if hdr.Source != "hdr10" || !hdr.Applies() {
		t.Errorf("HDR10 stream decided as %+v", hdr)
	}
	if got := toneMappedTo(hdr); got != "bt709" {
		t.Errorf("toneMappedTo = %q, want %q", got, "bt709")
	}

	sdr := ToneMapOf(probe.VideoStream{ColorTransfer: "bt709", ColorPrimaries: "bt709"})
	if sdr.Source != "" || sdr.Applies() {
		t.Errorf("SDR stream decided as %+v", sdr)
	}
	if got := toneMappedTo(sdr); got != "" {
		t.Errorf("toneMappedTo = %q on an SDR stream", got)
	}

	// A source we can name but not render. The manifest has to say the first
	// thing without claiming the second.
	dv := ToneMapOf(probe.VideoStream{ColorTransfer: "smpte2084", DolbyVisionProfile: 5})
	if dv.Source != "dolby-vision-p5" || dv.Applies() {
		t.Errorf("Dolby Vision profile 5 decided as %+v", dv)
	}
	if got := toneMappedTo(dv); got != "" {
		t.Errorf("toneMappedTo = %q on a stream nothing here can convert", got)
	}
}

// TestRunToneMapsOnlyTheHDRFile is the wiring, and the one thing a unit test
// cannot reach: one extractor serves every file of a run, in parallel, so a
// colour decision written onto the shared one would either race or convert the
// wrong file. The torrent therefore holds two files with the same picture in
// it, one graded for HDR10 and one for SDR, and both have to come out right in
// the same run.
func TestRunToneMapsOnlyTheHDRFile(t *testing.T) {
	tools := hdrtest.Tools(t, "../..")

	dir := t.TempDir()
	// Named so the sort order the engine sees does not decide which arm is
	// which; the assertions key off the path.
	hdrtest.WriteClip(t, tools, hdrtest.PQ, filepath.Join(dir, "graded-hdr10.mkv"), 8)
	hdrtest.WriteClip(t, tools, hdrtest.SDR, filepath.Join(dir, "graded-sdr.mkv"), 8)

	fixture := torrenttest.BuildDir(t, dir, 256<<10)
	seeder := fixture.StartSeeder(t)

	cfg := runConfig(t, fixture.TorrentPath, seeder)
	cfg.Swarm.Peers = []string{seeder}
	cfg.Plan = frames.Plan{Count: 1, Start: 0.4, End: 0.6}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	ready := map[string]FrameReady{}
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case FrameReady:
			ready[arm(e.Path)] = e
		case Failed:
			t.Errorf("unexpected failure: %s: %v", e.Code, e.Err)
		case FrameSkipped:
			t.Errorf("frame %d skipped: %s: %s", e.Index, e.Code, e.Reason)
		}
	}

	if len(ready) != 2 {
		t.Fatalf("got frames for %d arms, want one HDR10 and one SDR: %v", len(ready), ready)
	}

	stats := map[string]hdrtest.Stats{}
	for name, frame := range ready {
		data, err := os.ReadFile(frame.Path)
		if err != nil {
			t.Fatalf("%s frame is not on disk: %v", name, err)
		}
		stats[name] = hdrtest.Measure(t, data)
		t.Logf("%s: luma_mean=%.1f luma_spread=%.1f colorfulness=%.1f",
			name, stats[name].LumaMean, stats[name].LumaSpread, stats[name].Colorfulness)
	}

	// The two files hold the same picture, one graded for each. If the run
	// handled the HDR one, both frames look alike; if it did not, or if it
	// converted the SDR one by mistake, they do not.
	hdr, sdr := stats["hdr10"], stats["sdr"]
	if hdr.Colorfulness < 0.66*sdr.Colorfulness {
		t.Errorf("the HDR10 file came out at %.1f colourfulness against the SDR grade's %.1f: it was not tone mapped",
			hdr.Colorfulness, sdr.Colorfulness)
	}
	if sdr.Colorfulness < 100 {
		t.Errorf("the SDR file came out at %.1f colourfulness, so something was done to it that should not have been",
			sdr.Colorfulness)
	}

	// And the run has to say which it did, per file: a frame cannot explain
	// itself, and "torpeek is broken" is the reading available to anyone who
	// is told nothing (TOR-108).
	//
	// These are also the assertions that catch the opposite mistake. An
	// extractor that tone mapped every file, not only the HDR one, was run as
	// a control: the SDR frame's colourfulness moved from 120.2 to 120.1 and
	// its mean luminance from 166.6 to 171.2, which no pixel measurement here
	// would call a failure. The manifest said dynamic_range "hdr10" about a
	// BT.709 file, and that is unmistakable.
	for name, want := range map[string]manifest.Video{
		"hdr10": {DynamicRange: "hdr10", ToneMappedTo: "bt709", ColorTransfer: "smpte2084"},
		"sdr":   {DynamicRange: "", ToneMappedTo: "", ColorTransfer: "bt709"},
	} {
		// Frames land in <file dir>/frames/, so the manifest is one level up.
		m := readManifest(t, filepath.Dir(filepath.Dir(ready[name].Path)))
		if m.Video.DynamicRange != want.DynamicRange {
			t.Errorf("%s manifest dynamic_range = %q, want %q", name, m.Video.DynamicRange, want.DynamicRange)
		}
		if m.Video.ToneMappedTo != want.ToneMappedTo {
			t.Errorf("%s manifest tone_mapped_to = %q, want %q", name, m.Video.ToneMappedTo, want.ToneMappedTo)
		}
		if m.Video.ColorTransfer != want.ColorTransfer {
			t.Errorf("%s manifest color_transfer = %q, want %q", name, m.Video.ColorTransfer, want.ColorTransfer)
		}
		if m.Video.DolbyVisionProfile != 0 {
			t.Errorf("%s manifest claims Dolby Vision profile %d", name, m.Video.DolbyVisionProfile)
		}
	}
}

// arm says which of the two fixtures a written frame came from, by the file
// directory the writer named after the source path.
func arm(framePath string) string {
	if strings.Contains(framePath, "hdr10") {
		return "hdr10"
	}
	return "sdr"
}
