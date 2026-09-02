package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// TestAvailabilityFollowsThePeers is both halves of the acceptance criterion:
// nothing is available before anyone is connected, and everything is once a
// seeder is. The map is a function of the peer set, so it has to move with it.
//
// The step in the middle is not ceremony. A torrent client with every piece at
// priority None has no reason to talk to anyone and does not connect at all -
// measured, not assumed: PeerConns stayed empty for twenty seconds after
// AddPeers until something asked for bytes.
func TestAvailabilityFollowsThePeers(t *testing.T) {
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

	before := tor.Availability()
	if before.NumPieces() != tor.NumPieces() {
		t.Errorf("map covers %d pieces, torrent has %d", before.NumPieces(), tor.NumPieces())
	}
	if before.Peers() != 0 {
		t.Errorf("Peers() = %d before connecting to anyone", before.Peers())
	}
	if before.Unavailable() != before.NumPieces() {
		t.Errorf("%d of %d pieces unavailable with no peers, want all of them",
			before.Unavailable(), before.NumPieces())
	}

	if n := tor.AddPeers(seederAddr); n != 1 {
		t.Fatalf("AddPeers added %d peers, want 1", n)
	}

	// A client with nothing to ask for does not dial anybody, so availability
	// only becomes knowable once the run wants bytes. Reading a small range is
	// what any real run does first anyway.
	if _, err := tor.ReadRange(ctx, 0, 0, 64<<10, MinTraffic); err != nil {
		t.Fatalf("read range: %v", err)
	}

	after, ok := waitForAvailability(ctx, tor)
	if !ok {
		t.Fatal("the seeder never showed up in the availability map")
	}

	if after.Peers() < 1 {
		t.Errorf("Peers() = %d after the seeder connected", after.Peers())
	}
	if after.Unavailable() != 0 {
		t.Errorf("%d pieces unavailable from a seeder holding the whole file",
			after.Unavailable())
	}
	for piece := 0; piece < after.NumPieces(); piece++ {
		if after.At(piece) < 1 {
			t.Fatalf("piece %d has availability %d, want at least the one seeder",
				piece, after.At(piece))
		}
	}

	// The same answer, reached by byte offset and by range.
	if got := after.AtOffset(testPayloadSize / 2); got < 1 {
		t.Errorf("AtOffset(middle) = %d, want at least 1", got)
	}
	if got := after.OverRange(0, testPayloadSize); got < 1 {
		t.Errorf("OverRange(whole file) = %d, want at least 1", got)
	}
	if got := after.OverRange(10, 10); got != 0 {
		t.Errorf("OverRange of an empty range = %d, want 0", got)
	}

	file := FileInfo{Index: 0, Path: "movie.mkv", Length: testPayloadSize}
	coarse := after.Coarse(file, 16)
	if len(coarse) != 16 {
		t.Fatalf("Coarse gave %d buckets, want 16", len(coarse))
	}
	for i, n := range coarse {
		if n < 1 {
			t.Errorf("bucket %d reports %d, want the seeder's copy", i, n)
		}
	}
}

// waitForAvailability samples until the seeder's bitfield has arrived. A peer
// is connected before it has told us what it holds, so the first sample after
// AddPeers is legitimately empty.
func waitForAvailability(ctx context.Context, tor *Torrent) (Availability, bool) {
	for ctx.Err() == nil {
		if a := tor.Availability(); a.Peers() > 0 && a.Unavailable() == 0 {
			return a, true
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	return Availability{}, false
}

func TestCoarseNeverFinerThanThePiecesItDescribes(t *testing.T) {
	// Four pieces of 256 KiB, and a file covering all of them.
	a := Availability{
		counts:      []int{3, 0, 2, 1},
		peers:       3,
		pieceLength: testPieceLength,
		length:      4 * testPieceLength,
	}
	file := FileInfo{Length: 4 * testPieceLength}

	if got := len(a.Coarse(file, 64)); got != 4 {
		t.Errorf("asked for 64 buckets over 4 pieces, got %d; a map cannot be finer than its data", got)
	}

	buckets := a.Coarse(file, 4)
	want := []int{3, 0, 2, 1}
	for i := range want {
		if buckets[i] != want[i] {
			t.Errorf("bucket %d = %d, want %d (%v)", i, buckets[i], want[i], buckets)
		}
	}

	// A bucket spanning several pieces reports the scarcest of them: a region
	// is only as reachable as its least held piece.
	if got := a.Coarse(file, 2); got[0] != 0 || got[1] != 1 {
		t.Errorf("two buckets = %v, want [0 1] - the minimum of each half", got)
	}
	if got := a.Unavailable(); got != 1 {
		t.Errorf("Unavailable() = %d, want 1", got)
	}
}
