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

	cost := func(profile swarm.Profile) (int64, time.Duration, int) {
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

		var bytes int64
		var elapsed time.Duration
		got := 0
		for _, ev := range collect(t, events) {
			switch e := ev.(type) {
			case FrameReady:
				got++
			case Failed:
				t.Errorf("%s: %s: %v", profile.Name, e.Code, e.Err)
			case Done:
				bytes, elapsed = e.DownloadedByte, e.Elapsed
			}
		}
		return bytes, elapsed, got
	}

	timeBytes, timeElapsed, timeFrames := cost(swarm.MinTime)
	trafficBytes, trafficElapsed, trafficFrames := cost(swarm.MinTraffic)

	t.Logf("min-time:    %6.1f MiB in %s for %d frames (%.2f MiB/frame)",
		float64(timeBytes)/(1<<20), timeElapsed.Round(100*time.Millisecond), timeFrames,
		float64(timeBytes)/(1<<20)/float64(timeFrames))
	t.Logf("min-traffic: %6.1f MiB in %s for %d frames (%.2f MiB/frame)",
		float64(trafficBytes)/(1<<20), trafficElapsed.Round(100*time.Millisecond), trafficFrames,
		float64(trafficBytes)/(1<<20)/float64(trafficFrames))
	t.Logf("difference:  %6.1f MiB (%.0f%% of min-time)",
		float64(timeBytes-trafficBytes)/(1<<20),
		100*float64(timeBytes-trafficBytes)/float64(timeBytes))

	if timeFrames != trafficFrames {
		t.Fatalf("the profiles produced different frame counts (%d and %d); "+
			"they must answer the same question at different prices",
			timeFrames, trafficFrames)
	}
	if trafficBytes >= timeBytes {
		t.Errorf("min-traffic took %d bytes and min-time %d; the thrifty profile must cost less",
			trafficBytes, timeBytes)
	}
}
