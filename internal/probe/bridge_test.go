package probe

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/bridge"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// biggerSampleVideo renders a file large enough that "how much did probing
// cost" is a meaningful question.
func biggerSampleVideo(t *testing.T, name string) []byte {
	t.Helper()

	tools := locateTools(t)
	path := filepath.Join(t.TempDir(), name)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	_, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration=120",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=120",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-g", "50", "-pix_fmt", "yuv420p", "-b:v", "1500k",
		"-c:a", "aac", "-metadata:s:a:0", "language=eng",
		path,
	)
	if err != nil {
		t.Fatalf("render sample video: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sample video: %v", err)
	}
	return data
}

// TestProbeOverBridge is the end-to-end proof of the architecture decision:
// ffprobe reads a torrented file over the HTTP bridge, reports what is in it
// and where keyframes live - with no container parser of our own, and without
// downloading the file.
func TestProbeOverBridge(t *testing.T) {
	tools := locateTools(t)
	payload := biggerSampleVideo(t, "movie.mkv")

	const pieceLength = 256 << 10
	fixture := torrenttest.BuildFromBytes(t, "movie.mkv", payload, pieceLength)
	seeder := fixture.StartSeeder(t)

	src, err := swarm.ParseSource(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	cfg := swarm.DefaultConfig(t.TempDir())
	cfg.DHT = false // entirely offline
	cfg.MetadataTimeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	session, tor, err := swarm.Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer session.Close()

	if n := tor.AddPeers(seeder); n != 1 {
		t.Fatalf("AddPeers added %d peers, want 1", n)
	}

	b, err := bridge.Start(bridge.DefaultConfig())
	if err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	defer b.Close()

	logged := &loggingContent{inner: bridge.FromTorrent(tor, swarm.MinTraffic)}
	url, withdraw, err := b.Publish(logged, 0)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	defer withdraw()

	prober := New(tools)

	info, err := prober.Inspect(ctx, url)
	if err != nil {
		t.Fatalf("inspect over bridge: %v", err)
	}
	afterInspect := tor.Downloaded()
	logged.report(t, "inspect", int64(len(payload)))

	if delta := info.Duration - 120*time.Second; delta > 2*time.Second || delta < -2*time.Second {
		t.Errorf("Duration = %s, want about 2m", info.Duration)
	}
	if info.Video.Width != 640 || info.Video.Height != 360 {
		t.Errorf("resolution = %dx%d, want 640x360", info.Video.Width, info.Video.Height)
	}
	if len(info.Audio) != 1 || info.Audio[0].Language != "eng" {
		t.Errorf("audio = %+v, want one eng track", info.Audio)
	}

	// A keyframe deep in the file: this is the request that has to reach into
	// the middle of the torrent rather than read from the front.
	kf, err := prober.KeyframeAt(ctx, url, 90*time.Second)
	if err != nil {
		t.Fatalf("keyframe over bridge: %v", err)
	}
	if kf.PTS > 90*time.Second {
		t.Errorf("keyframe pts %s is after the requested 90s", kf.PTS)
	}
	if kf.BytePos <= 0 || kf.BytePos >= int64(len(payload)) {
		t.Errorf("BytePos = %d, outside the file's %d bytes", kf.BytePos, len(payload))
	}

	logged.report(t, "inspect+keyframe", int64(len(payload)))
	time.Sleep(500 * time.Millisecond)
	total := tor.Downloaded()

	t.Logf("file %d KiB in %d-KiB pieces: inspect cost %d KiB, inspect+keyframe cost %d KiB (%.1f%% of the file)",
		len(payload)/1024, pieceLength/1024,
		afterInspect/1024, total/1024, 100*float64(total)/float64(len(payload)))

	// The premise is not "a fraction of the file" but "a fixed handful of
	// megabytes, whatever the file's size" - that is what makes this viable on
	// a 10 GB feature. Measured across 7, 15 and 29 MiB samples the cost held
	// at roughly 1.3 MiB to inspect and 2.4-2.8 MiB including a keyframe, so
	// an absolute ceiling is the honest check; a percentage would pass on a
	// large file no matter how badly this regressed.
	const maxBytes = 5 << 20
	if total > maxBytes {
		t.Errorf("probing cost %d bytes, want under %d - ffmpeg is reading far more than the index",
			total, int64(maxBytes))
	}
}
