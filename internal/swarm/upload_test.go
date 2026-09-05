package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// TestUploadedCountsPayloadAndNotProtocol is the only assertion that tells the
// right counter from the wrong one, and it is why this test reads a range
// rather than just calling the accessor.
//
// The fixture's peer is a SEEDER: it holds the whole file and wants nothing
// from us, so no piece data can leave. Meanwhile we send it a handshake, a
// bitfield and a stream of requests, so the library's all-bytes-written
// counter is definitely not zero. BytesWrittenData - payload only - is. Wiring
// Uploaded() to BytesWritten instead would pass a "does it compile" test and
// fail this one.
func TestUploadedCountsPayloadAndNotProtocol(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mkv", testPayloadSize, testPieceLength)
	seederAddr := fixture.StartSeeder(t)

	src, err := ParseSource(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.MetadataTimeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, tor, err := Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer session.Close()

	if got := tor.Uploaded(); got != 0 {
		t.Errorf("Uploaded() = %d before any peer, want 0", got)
	}

	if n := tor.AddPeers(seederAddr); n != 1 {
		t.Fatalf("AddPeers added %d peers, want 1", n)
	}
	if _, err := tor.ReadRange(ctx, 0, 0, 64<<10, MinTraffic); err != nil {
		t.Fatalf("read range: %v", err)
	}

	// Something actually crossed the wire, or the rest of this proves nothing.
	if down := tor.Downloaded(); down <= 0 {
		t.Fatalf("Downloaded() = %d after reading a range; nothing was transferred, "+
			"so this test cannot tell the counters apart", down)
	}

	if up := tor.Uploaded(); up != 0 {
		t.Errorf("Uploaded() = %d against a seeder that wants nothing from us. "+
			"Payload cannot have left; a non-zero figure here means the accessor is "+
			"reading total bytes written rather than piece data", up)
	}
}

// TestUploadedIsZeroWithUploadOff pins the flag's effect on the figure rather
// than on the library's setting: -upload=false is the only lever a person has
// over outbound traffic, since the budget counts received bytes only
// (REQUIREMENTS.md 2.6), so it has to be visible in what this reports.
func TestUploadedIsZeroWithUploadOff(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mkv", testPayloadSize, testPieceLength)
	seederAddr := fixture.StartSeeder(t)

	src, err := ParseSource(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.Upload = false
	cfg.MetadataTimeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, tor, err := Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer session.Close()

	if n := tor.AddPeers(seederAddr); n != 1 {
		t.Fatalf("AddPeers added %d peers, want 1", n)
	}
	if _, err := tor.ReadRange(ctx, 0, 0, 64<<10, MinTraffic); err != nil {
		t.Fatalf("read range: %v", err)
	}
	if down := tor.Downloaded(); down <= 0 {
		t.Fatalf("Downloaded() = %d; nothing was transferred", down)
	}

	if up := tor.Uploaded(); up != 0 {
		t.Errorf("Uploaded() = %d with Upload off, want 0", up)
	}
}
