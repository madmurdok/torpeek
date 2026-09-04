package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// TestMinTrafficCostsLessThanMinTime is the trade the two profiles exist to
// make, measured rather than asserted from their constants.
//
// The payload is raw video on purpose. An encoded testsrc collapses to almost
// nothing - 300 seconds of 720p came out 23.4 MiB - and on a file that small
// every profile simply downloads all of it, which measures nothing. Frames
// that do not compress give a file big enough for a window to be a small part
// of it, which is the only condition under which the profiles differ at all.
func TestMinTrafficCostsLessThanMinTime(t *testing.T) {
	tools := locateTools(t)

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()

	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=60",
		"-c:v", "rawvideo", "-pix_fmt", "yuv420p",
		filepath.Join(dir, "movie.mkv"),
	); err != nil {
		t.Fatalf("render: %v", err)
	}

	fixture := torrenttest.BuildDir(t, dir, 1<<20)
	seeder := fixture.StartSeeder(t)

	// What one profile cost, in both figures: what arrived, and what the run
	// ordered (TOR-94). The second is the interesting one to compare between
	// profiles, because it is the part torpeek decides.
	type cost struct {
		downloaded    int64
		claimed       int64
		claimedPieces int
		elapsed       time.Duration
		frames        int
	}

	measure := func(profile swarm.Profile) cost {
		t.Helper()

		cfg := DefaultConfig(fixture.TorrentPath, t.TempDir(), t.TempDir())
		cfg.Swarm.DHT = false
		cfg.Swarm.MetadataTimeout = 10 * time.Second
		cfg.Swarm.Peers = []string{seeder}
		cfg.Profile = profile
		cfg.Plan = frames.Plan{Count: 6, Start: 0.05, End: 0.95}
		cfg.Parallelism = 1
		cfg.Budget = Budget{MaxBytes: 512 << 20, MaxTime: 3 * time.Minute, WarnAt: 0.8}

		events, err := NewEngine(tools).Run(ctx, cfg)
		if err != nil {
			t.Fatalf("run %s: %v", profile.Name, err)
		}

		var got cost
		for _, ev := range collect(t, events) {
			switch e := ev.(type) {
			case FrameReady:
				got.frames++
			case FrameSkipped:
				// Not a failure on its own - the frame-count check below
				// decides that - but never silent. A skipped point is the
				// one way this test can measure two different runs and
				// still look like it compared them.
				t.Logf("%s: skipped frame %d at %s: %s: %s",
					profile.Name, e.Index, e.Requested.Round(time.Second), e.Code, e.Reason)
			case Failed:
				t.Errorf("%s: %s: %v", profile.Name, e.Code, e.Err)
			case Done:
				got.downloaded, got.elapsed = e.DownloadedByte, e.Elapsed
				got.claimed, got.claimedPieces = e.ClaimedByte, e.ClaimedPieces
			}
		}
		return got
	}

	minTime := measure(swarm.MinTime)
	minTraffic := measure(swarm.MinTraffic)

	timeBytes, timeElapsed, timeFrames := minTime.downloaded, minTime.elapsed, minTime.frames
	trafficBytes, trafficElapsed, trafficFrames := minTraffic.downloaded, minTraffic.elapsed, minTraffic.frames

	t.Logf("min-time:    %6.1f MiB in %s for %d frames (%.2f MiB/frame), ordered %.1f MiB in %d pieces",
		float64(timeBytes)/(1<<20), timeElapsed.Round(100*time.Millisecond), timeFrames,
		float64(timeBytes)/(1<<20)/float64(timeFrames),
		float64(minTime.claimed)/(1<<20), minTime.claimedPieces)
	t.Logf("min-traffic: %6.1f MiB in %s for %d frames (%.2f MiB/frame), ordered %.1f MiB in %d pieces",
		float64(trafficBytes)/(1<<20), trafficElapsed.Round(100*time.Millisecond), trafficFrames,
		float64(trafficBytes)/(1<<20)/float64(trafficFrames),
		float64(minTraffic.claimed)/(1<<20), minTraffic.claimedPieces)
	t.Logf("difference:  %6.1f MiB arrived (%.0f%% of min-time), %.1f MiB ordered",
		float64(timeBytes-trafficBytes)/(1<<20),
		100*float64(timeBytes-trafficBytes)/float64(timeBytes),
		float64(minTime.claimed-minTraffic.claimed)/(1<<20))

	// The claimed figure is what acceptance criterion 2's verdict is now taken
	// on (TOR-94), so it has to be shown capable of reading something other
	// than one number: two profiles that order deliberately different amounts
	// must produce different figures here, in the right order. A counter left
	// unwired, or one summing claims instead of distinct pieces and so
	// saturating at the file, would pass every other check in this file.
	if minTraffic.claimed == 0 || minTime.claimed == 0 {
		t.Errorf("a profile ordered nothing (min-time %d, min-traffic %d bytes); "+
			"the claimed figure is not being counted at all",
			minTime.claimed, minTraffic.claimed)
	}
	if minTraffic.claimed >= minTime.claimed {
		t.Errorf("min-traffic ordered %d bytes in %d pieces and min-time %d in %d; "+
			"the thrifty profile must ORDER less, not merely receive less",
			minTraffic.claimed, minTraffic.claimedPieces, minTime.claimed, minTime.claimedPieces)
	}

	if timeFrames != trafficFrames {
		t.Fatalf("the profiles produced different frame counts (%d and %d); "+
			"they must answer the same question at different prices",
			timeFrames, trafficFrames)
	}
	if trafficBytes >= timeBytes {
		t.Errorf("min-traffic took %d bytes and min-time %d; the thrifty profile must cost less",
			trafficBytes, timeBytes)
	}

	// The ceilings below are what stands in for the acceptance run, which
	// costs real traffic on a real swarm and gets run once per release. What
	// transfers between the two is the cost of a capture point, not the cost
	// of a run: the profiles pull a fixed neighbourhood around each point
	// regardless of how big the file is or how many points there are. At the
	// 8+6 MiB constants this harness measured 9.61 MiB/frame where the
	// acceptance torrent measured 8.6, so a frame here is about 12% dearer
	// than a frame there, and a ceiling here is the stricter of the two.
	//
	// 7.0 MiB/frame is the guard for min-time. It is not the criterion
	// converted - 150 MB over 20 frames would allow 7.15 - but the measured
	// 5.76 with a fifth of headroom for run-to-run variance, which lands the
	// acceptance run near 103 MiB. The old 8+6 constants measure 9.61 here
	// and fail it, which is the regression this exists to catch.
	perFrame := func(bytes int64, frames int) float64 {
		return float64(bytes) / (1 << 20) / float64(frames)
	}
	if got := perFrame(timeBytes, timeFrames); got > 7.0 {
		t.Errorf("min-time cost %.2f MiB/frame, over the 7.0 ceiling; "+
			"20 frames at this price miss the 150 MB acceptance criterion", got)
	}
	// min-traffic is not what this task tunes, but it must not pay for
	// min-time's cut. Measured 3.44-3.61 MiB/frame across runs.
	if got := perFrame(trafficBytes, trafficFrames); got > 4.5 {
		t.Errorf("min-traffic cost %.2f MiB/frame, over the 4.5 ceiling; "+
			"the thrifty profile regressed", got)
	}
}
