package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// TOR-188 exists because TOR-179's own fixture measured something nobody had
// asked about: on a ~670 KB clip, three of four capture points recorded only
// the file's head, [[0, 32768)], and one recorded where it actually seeked.
//
// That is not an attribution bug. A range is claimed only where ffmpeg issues
// a request, the bridge deliberately claims the HEAD of each requested range
// (TOR-88), and on a small file ffmpeg opens `bytes=0-` and reads forward -
// everything past the head arriving as reader readahead, which every claim
// figure in this project excludes by design.
//
// But TOR-179's whole purpose is the strip for a FILM, and that rests on an
// assertion nobody measured: that a film-sized file forces every point to
// seek, so each records its own distinct range instead of collapsing to the
// head. If that is false, TOR-179 ships a mechanism that is correct, tested,
// and still draws a strip lit only at the front - the exact complaint it was
// written to fix.
//
// scaleCase is one size to try. Piece size travels with it deliberately: a
// real torrent of a 400 MB file does not use 16 KiB pieces, and the claim's
// granularity is the piece, so holding piece size fixed while growing the file
// would measure a fixture nobody ships.
//
// THE ANSWER, MEASURED (TOR-188): the assertion holds, and the threshold is far
// below any film. At 669 KB with 16 KiB pieces, 2 of 4 points recorded the head
// alone. At 3.2 MB with 256 KiB pieces, and again at 6.4 MB with 512 KiB
// pieces, ALL FOUR points recorded their own distinct middle range and none
// collapsed to the head. So the threshold sits between 0.67 MB and 3.2 MB -
// three orders of magnitude under a 400 MB episode - and TOR-179's strip is
// safe on anything a person would actually preview.
//
// What each point records above the threshold is head + tail + its own seek:
// the container's header at the front, its cues/index at the end, and the
// bytes around the timestamp it went to. The head and tail are honest, not
// noise - every point's ffprobe really does re-read both.
type scaleCase struct {
	name      string
	seconds   int
	bitrate   string
	pieceSize int64
	// locatedEveryPoint is the assertion, and it is only true above the
	// threshold below. Where it is false the case is the experiment's own
	// CONTROL arm: it demonstrates in the same run that the guard can fail,
	// which is why there is no separate mutation to perform for it.
	locatedEveryPoint bool
}

// renderSized builds a single-file torrent of roughly the requested size and
// starts a loopback seeder for it. Same shape as fineGrainedTorrent, with the
// duration, bitrate and piece size as parameters rather than baked in.
func renderSized(t *testing.T, tools ffmpeg.Tools, c scaleCase) (torrentPath, seeder string, bytes int64) {
	t.Helper()

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()

	path := filepath.Join(dir, "episode-1.mkv")
	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=640x360:rate=25:duration=%d", c.seconds),
		"-c:v", "libx264", "-g", "50", "-pix_fmt", "yuv420p", "-b:v", c.bitrate,
		path,
	); err != nil {
		t.Fatalf("%s: render clip: %v", c.name, err)
	}

	fixture := torrenttest.BuildDir(t, dir, c.pieceSize)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: stat clip: %v", c.name, err)
	}
	return fixture.TorrentPath, fixture.StartSeeder(t), info.Size()
}

// TestHowFileSizeChangesWhatAPointRecords is the MEASUREMENT, not the guard.
// It asserts nothing about which sizes collapse - it prints what each size
// records, so the threshold is read off a run rather than guessed. The guard
// that pins the answer comes after, once the numbers say what to pin.
//
// Skipped unless -run names it: each case renders a clip, builds a torrent,
// seeds it on loopback and does a full four-point capture, so the whole set
// costs minutes rather than seconds and has no business in every make check.
func TestHowFileSizeChangesWhatAPointRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement, not a guard: -short skips it")
	}
	tools := locateTools(t)

	// Sizes are the MEASURED output of these encoder settings, not the
	// targets: -b:v is a request, and testsrc compresses so well that x264
	// lands far under it (asking 1600k for 60s produced 3.2 MB, not 12 MB).
	// The names say what came out, because a label that disagrees with the
	// measurement is worse than no label.
	cases := []scaleCase{
		{name: "669KB-16KiB-pieces", seconds: 30, bitrate: "200k", pieceSize: 16 << 10,
			locatedEveryPoint: false},
		{name: "3.2MB-256KiB-pieces", seconds: 60, bitrate: "1600k", pieceSize: 256 << 10,
			locatedEveryPoint: true},
		{name: "6.4MB-512KiB-pieces", seconds: 120, bitrate: "4000k", pieceSize: 512 << 10,
			locatedEveryPoint: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			torrentPath, seeder, size := renderSized(t, tools, c)

			cfg := runConfig(t, torrentPath, seeder)
			cfg.Swarm.Peers = []string{seeder}
			cfg.Profile = swarm.Profile{
				Name:      swarm.MinTraffic.Name,
				Readahead: c.pieceSize,
				Window:    c.pieceSize,
			}
			cfg.Plan = frames.Plan{Count: 4, Start: 0.1, End: 0.9}

			ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
			defer cancel()

			events, err := NewEngine(tools).Run(ctx, cfg)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			for _, ev := range collect(t, events) {
				if f, ok := ev.(Failed); ok {
					t.Fatalf("run failed: %s: %v", f.Code, f.Err)
				}
			}

			m, run := loadOnlyManifest(t, cfg.OutputRoot)

			t.Logf("FILE %d bytes, piece %d, run claimed %v", size, c.pieceSize, run.Claimed)
			distinct := map[string]int{}
			headOnly := 0
			for _, f := range m.Frames {
				if f.Shift == manifest.ShiftFailed || f.Path == "" {
					continue
				}
				t.Logf("  point %2d at %6d ms -> %v", f.Index, f.ActualMS, f.ByteRanges)
				distinct[fmt.Sprint(f.ByteRanges)]++
				// "Collapsed to the head" means the only thing this point
				// recorded starts at byte 0 - it never showed where it went.
				if len(f.ByteRanges) == 1 && f.ByteRanges[0][0] == 0 {
					headOnly++
				}
			}
			t.Logf("  => %d distinct range sets across the captured points, %d recorded the head alone",
				len(distinct), headOnly)

			if !c.locatedEveryPoint {
				// The control arm. It is here to be BELOW the threshold, so
				// its collapse is the evidence that the assertion above is
				// discriminating rather than vacuous. Asserting the exact
				// count would be brittle - the encode is not bit-identical
				// run to run, and this arm has been seen at 2 and at 3 of
				// four - so it asserts only the direction that matters.
				if headOnly == 0 {
					t.Errorf("the control arm located every point at %d bytes, so it is no longer "+
						"below the threshold and the guard above proves nothing; re-measure and "+
						"shrink it", size)
				}
				return
			}

			// Above the threshold every point must say where IT went, which is
			// the whole premise TOR-179's strip rests on.
			if headOnly != 0 {
				t.Errorf("%d of the captured points recorded only the file's head at %d bytes: "+
					"a strip drawn from these would light the front of the file and nothing else, "+
					"which is the complaint TOR-179 was written to fix", headOnly, size)
			}
			if len(distinct) != 4 {
				t.Errorf("%d distinct range sets across 4 capture points at %d bytes; points sharing "+
					"a set cannot be told apart on the strip", len(distinct), size)
			}
		})
	}
}
