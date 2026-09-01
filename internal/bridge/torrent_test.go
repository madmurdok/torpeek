package bridge

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/torpeek/torpeek/internal/swarm"
	"github.com/torpeek/torpeek/internal/torrenttest"
)

const (
	payloadSize = 8 << 20
	pieceLength = 256 << 10
)

// TestBridgeOverTorrent is the acceptance test for TOR-32: an HTTP Range
// request against a torrented file returns the exact bytes, and costs a window
// rather than the file.
func TestBridgeOverTorrent(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mkv", payloadSize, pieceLength)
	seeder := fixture.StartSeeder(t)

	src, err := swarm.ParseSource(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	cfg := swarm.DefaultConfig(t.TempDir())
	cfg.DHT = false // stay entirely offline
	cfg.MetadataTimeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, tor, err := swarm.Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer session.Close()

	if n := tor.AddPeers(seeder); n != 1 {
		t.Fatalf("AddPeers added %d peers, want 1", n)
	}

	b, err := Start(DefaultConfig())
	if err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	defer b.Close()

	url, withdraw, err := b.Publish(FromTorrent(tor, swarm.MinTraffic), 0)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	defer withdraw()

	const off, length = 4 << 20, 64 << 10
	body, resp := get(t, url, fmt.Sprintf("bytes=%d-%d", off, off+length-1))

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if !bytes.Equal(body, fixture.Payload[off:off+length]) {
		t.Fatal("bytes served over HTTP do not match the torrent's payload")
	}

	// Same reasoning as the fetcher's own test: sample after a pause, because
	// an unbounded fetch would still be running when the response returns.
	time.Sleep(500 * time.Millisecond)
	downloaded := tor.Downloaded()

	t.Logf("served %d bytes over HTTP at a cost of %d bytes from the swarm (%.1f%% of the file)",
		length, downloaded, 100*float64(downloaded)/float64(payloadSize))

	if maxBytes := int64(payloadSize / 4); downloaded > maxBytes {
		t.Errorf("downloaded %d bytes, want at most %d - the bridge is not limiting itself to the window",
			downloaded, maxBytes)
	}
}

// TestBridgeSuffixRangeOverTorrent covers how ffmpeg reaches an index that
// lives at the end of the file - an MP4 without faststart, for instance.
func TestBridgeSuffixRangeOverTorrent(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mp4", payloadSize, pieceLength)
	seeder := fixture.StartSeeder(t)

	src, err := swarm.ParseSource(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	cfg := swarm.DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.MetadataTimeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, tor, err := swarm.Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer session.Close()
	tor.AddPeers(seeder)

	b, err := Start(DefaultConfig())
	if err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	defer b.Close()

	url, withdraw, err := b.Publish(FromTorrent(tor, swarm.MinTraffic), 0)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	defer withdraw()

	const tail = 32 << 10
	body, resp := get(t, url, fmt.Sprintf("bytes=-%d", tail))

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if !bytes.Equal(body, fixture.Payload[payloadSize-tail:]) {
		t.Error("suffix range did not return the torrent's final bytes")
	}
}
