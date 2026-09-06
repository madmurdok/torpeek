package core

import (
	"context"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/torrenttest"
)

const (
	availPayloadSize = 8 << 20   // 8 MiB
	availPieceLength = 256 << 10 // 256 KiB, so the payload is 32 pieces
)

// TestNewSwarmAvailabilityUnknownWithNoPeers is half of TOR-135's acceptance
// criterion: a torrent with a client but nobody connected reports unknown,
// never zero copies. swarm.Availability.Known() is false the moment a torrent
// opens - nobody has said what they hold yet - and newSwarmAvailability must
// pass that straight through as nil rather than a zeroed reading.
func TestNewSwarmAvailabilityUnknownWithNoPeers(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mkv", availPayloadSize, availPieceLength)

	src, err := swarm.ParseSource(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	cfg := swarm.DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.MetadataTimeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, tor, err := swarm.Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer session.Close()

	if got := newSwarmAvailability(tor.Availability()); got != nil {
		t.Fatalf("newSwarmAvailability with no peers = %+v, want nil (unknown, not zero)", got)
	}
}

// TestNewSwarmAvailabilityFromALoopbackSeeder is the other half: a swarm with
// a known number of seeds (one) reports a real reading, in copies-per-piece,
// once it has had a reason to talk to that seeder.
//
// The read-a-range-first step is not ceremony (see swarm's own
// TestAvailabilityFollowsThePeers, whose shape this mirrors): a torrent with
// nothing to fetch does not dial the peers it knows about, so availability
// stays unknown until something asks for bytes.
func TestNewSwarmAvailabilityFromALoopbackSeeder(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mkv", availPayloadSize, availPieceLength)
	seederAddr := fixture.StartSeeder(t)

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
		t.Fatalf("open: %v", err)
	}
	defer session.Close()

	if n := tor.AddPeers(seederAddr); n != 1 {
		t.Fatalf("AddPeers added %d peers, want 1", n)
	}
	if _, err := tor.ReadRange(ctx, 0, 0, 64<<10, swarm.MinTraffic); err != nil {
		t.Fatalf("read range: %v", err)
	}

	got, ok := waitForKnownAvailability(ctx, tor)
	if !ok {
		t.Fatal("the seeder never showed up in the availability map")
	}

	if got == nil {
		t.Fatal("newSwarmAvailability = nil once the seeder is known, want a reading")
	}
	if got.NumPieces != tor.NumPieces() {
		t.Errorf("NumPieces = %d, want %d", got.NumPieces, tor.NumPieces())
	}
	if got.Unavailable != 0 {
		t.Errorf("Unavailable = %d, want 0 - a lone seeder holds the whole file", got.Unavailable)
	}
	// A single seeder holding everything means every piece has exactly one
	// holder, so the mean is exactly 1.0 - a value comfortably distinct from
	// a percentage and from zero either way.
	if got.CopiesPerPiece != 1 {
		t.Errorf("CopiesPerPiece = %v, want 1 (one seeder, one copy of every piece)", got.CopiesPerPiece)
	}
}

// waitForKnownAvailability samples until the seeder's bitfield has arrived -
// a peer is connected before it has said what it holds, so the first sample
// after AddPeers is legitimately still unknown.
func waitForKnownAvailability(ctx context.Context, tor *swarm.Torrent) (*SwarmAvailability, bool) {
	for ctx.Err() == nil {
		if got := newSwarmAvailability(tor.Availability()); got != nil && got.Unavailable == 0 {
			return got, true
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil, false
}
