package frames

import (
	"bytes"
	"context"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/torpeek/torpeek/internal/bridge"
	"github.com/torpeek/torpeek/internal/ffmpeg"
	"github.com/torpeek/torpeek/internal/swarm"
	"github.com/torpeek/torpeek/internal/torrenttest"
)

const pieceLength = 256 << 10

// torrentedVideo renders a clip, puts it in a torrent, serves it from a
// loopback seeder and publishes it on a bridge. It returns the URL ffmpeg
// should read and the torrent, for asking what the reading cost.
func torrentedVideo(t *testing.T, tools ffmpeg.Tools, seconds int) (url string, tor *swarm.Torrent) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "movie.mkv")
	renderCtx, cancelRender := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancelRender()

	if _, err := tools.Run(renderCtx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration="+strconv.Itoa(seconds),
		"-c:v", "libx264", "-g", "50", "-pix_fmt", "yuv420p", "-b:v", "1500k",
		path,
	); err != nil {
		t.Fatalf("render video: %v", err)
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read video: %v", err)
	}

	fixture := torrenttest.BuildFromBytes(t, "movie.mkv", payload, pieceLength)
	seeder := fixture.StartSeeder(t)

	src, err := swarm.ParseSource(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	cfg := swarm.DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.MetadataTimeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	session, tor, err := swarm.Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	if n := tor.AddPeers(seeder); n != 1 {
		t.Fatalf("AddPeers added %d peers, want 1", n)
	}

	b, err := bridge.Start(bridge.DefaultConfig())
	if err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	t.Cleanup(func() { b.Close() })

	url, withdraw, err := b.Publish(bridge.FromTorrent(tor, swarm.MinTraffic), 0)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	t.Cleanup(withdraw)

	return url, tor
}

// TestFrameFromTorrentMiddle is the acceptance test for TOR-11: a frame from
// the middle of a torrent, without the file ever being fully downloaded.
func TestFrameFromTorrentMiddle(t *testing.T) {
	tools := locateTools(t)
	url, tor := torrentedVideo(t, tools, 120)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	frame, err := NewExtractor(tools).Frame(ctx, url, 90*time.Second)
	if err != nil {
		t.Fatalf("Frame over bridge: %v", err)
	}

	if frame.Width != 640 || frame.Height != 360 {
		t.Errorf("frame is %dx%d, want the source's 640x360", frame.Width, frame.Height)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(frame.Data)); err != nil {
		t.Fatalf("frame data is not decodable: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	downloaded := tor.Downloaded()
	total := tor.Length()

	t.Logf("frame at 90s: %dx%d, %d KiB image, cost %d KiB of a %d KiB file (%.1f%%)",
		frame.Width, frame.Height, len(frame.Data)/1024,
		downloaded/1024, total/1024, 100*float64(downloaded)/float64(total))

	if downloaded >= total {
		t.Errorf("downloaded %d of %d bytes - the whole file was fetched for one frame", downloaded, total)
	}
	// A frame should cost a handful of windows, not a sizeable share of the
	// file. Absolute, because this must not grow with the file's length.
	const maxBytes = 6 << 20
	if downloaded > maxBytes {
		t.Errorf("one frame cost %d bytes, want under %d", downloaded, int64(maxBytes))
	}
}

// TestInputSeekBeatsOutputSeek checks the claim the extractor is built on:
// putting -ss before -i makes ffmpeg jump to the keyframe, while putting it
// after decodes everything up to that point - which over a torrent means
// fetching everything up to that point too.
func TestInputSeekBeatsOutputSeek(t *testing.T) {
	tools := locateTools(t)

	measure := func(inputSeek bool) int64 {
		t.Helper()

		url, tor := torrentedVideo(t, tools, 120)
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		args := []string{"-hide_banner", "-loglevel", "error"}
		if inputSeek {
			args = append(args, "-ss", "90", "-i", url)
		} else {
			args = append(args, "-i", url, "-ss", "90")
		}
		args = append(args, "-frames:v", "1", "-f", "image2pipe", "-c:v", "mjpeg", "-")

		if _, err := tools.Run(ctx, "ffmpeg", args...); err != nil {
			t.Fatalf("decode (inputSeek=%v): %v", inputSeek, err)
		}
		time.Sleep(500 * time.Millisecond)
		return tor.Downloaded()
	}

	input := measure(true)
	output := measure(false)

	t.Logf("-ss before -i cost %d KiB; -ss after -i cost %d KiB", input/1024, output/1024)

	if input >= output {
		t.Errorf("input seeking cost %d bytes and output seeking %d - the ordering this code relies on is not helping",
			input, output)
	}
}

// TestSecondFrameReusesPieces checks the requirement that a run does not pay
// twice for the same bytes: the header and index a second frame needs are
// already on disk from the first, so it must cost noticeably less.
func TestSecondFrameReusesPieces(t *testing.T) {
	tools := locateTools(t)
	url, tor := torrentedVideo(t, tools, 120)
	e := NewExtractor(tools)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	if _, err := e.Frame(ctx, url, 30*time.Second); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	afterFirst := tor.Downloaded()

	if _, err := e.Frame(ctx, url, 60*time.Second); err != nil {
		t.Fatalf("second frame: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	afterSecond := tor.Downloaded()

	first := afterFirst
	second := afterSecond - afterFirst

	t.Logf("first frame cost %d KiB, second cost %d KiB", first/1024, second/1024)

	if second >= first {
		t.Errorf("second frame cost %d bytes against the first's %d - pieces are not being reused",
			second, first)
	}
}
