package core

import (
	"context"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/bridge"
	"github.com/madmurdok/torpeek/internal/frames"
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

// TestProgressCarriesLiveAvailability is TOR-135's acceptance criterion at
// the level that matters: not that newSwarmAvailability converts a snapshot
// correctly (the tests above cover that), but that the reading survives onto
// the event a consumer actually reads.
//
// It also pins the UNIT at the far end, the thing most likely to be quietly
// misread later: with one seeder holding everything, every piece has exactly
// one copy, so CopiesPerPiece is 1.0 - not 100, not 0.01. Anything that
// starts treating this figure as a percentage breaks here rather than in a
// UI.
//
// The fixture has to be big, and that is a measurement rather than caution.
// On the small clips the rest of this package uses, the run has the WHOLE
// torrent before its first capture point, so anacrolix has already dropped
// the seeder by the time the first heartbeat fires: measured, peers=0 seeds=0
// swarm=nil on both heartbeats of an eight-second clip, and the same for a
// sixty-second one. That is availability correctly reporting unknown - there
// is genuinely nobody connected any more - so a test built on such a fixture
// would be asking for a reading at the one moment there cannot be one. Hence
// roofTorrent, which is large enough that the run is still fetching while it
// works.
func TestProgressCarriesLiveAvailability(t *testing.T) {
	tools := locateTools(t)
	torrentPath, seeder, _ := roofTorrent(t, tools)

	cfg := DefaultConfig(torrentPath, t.TempDir(), t.TempDir())
	cfg.Swarm.DHT = false
	cfg.Swarm.MetadataTimeout = 10 * time.Second
	cfg.Swarm.Peers = []string{seeder}
	cfg.Profile = swarm.MinTraffic
	cfg.Plan = frames.Plan{Count: 8, Start: 0.05, End: 0.95}
	cfg.Budget = Budget{MaxBytes: 512 << 20, MaxTime: 3 * time.Minute}
	cfg.Parallelism = 1
	cfg.Bridge = bridge.DefaultConfig()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	events, err := NewEngine(tools).Run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var (
		heartbeats int
		known      *SwarmAvailability
	)
	for _, ev := range collect(t, events) {
		switch e := ev.(type) {
		case Progress:
			heartbeats++
			if e.Swarm != nil && known == nil {
				reading := *e.Swarm
				known = &reading
			}
		case Failed:
			t.Errorf("the run reported a failure (%s: %v)", e.Code, e.Err)
		}
	}

	if heartbeats == 0 {
		t.Fatal("the run published no Progress at all - nothing to carry a reading")
	}
	if known == nil {
		t.Fatalf("no Progress carried an availability reading across %d heartbeats, "+
			"against a fixture this run is still fetching from", heartbeats)
	}

	// One seeder with the whole torrent: one copy of every piece, and nothing
	// missing from the swarm.
	if known.CopiesPerPiece != 1 {
		t.Errorf("CopiesPerPiece = %v, want exactly 1 - one seeder holding every piece. "+
			"A value near 100 would mean someone started reporting a percentage",
			known.CopiesPerPiece)
	}
	if known.Unavailable != 0 {
		t.Errorf("Unavailable = %d, want 0 - the seeder holds the whole torrent", known.Unavailable)
	}
	if known.NumPieces <= 0 {
		t.Errorf("NumPieces = %d, want the torrent's real piece count", known.NumPieces)
	}
}
