package swarm

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/torrent"

	"github.com/madmurdok/torpeek/internal/torrenttest"
)

const (
	testPayloadSize = 8 << 20   // 8 MiB
	testPieceLength = 256 << 10 // 256 KiB, so the payload is 32 pieces
)

// TestReadRangeFetchesOnlyTheWindow is the acceptance test for TOR-4: reading a
// range from the middle of a torrent must cost the pieces covering that range,
// not the file.
func TestReadRangeFetchesOnlyTheWindow(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mkv", testPayloadSize, testPieceLength)
	seederAddr := fixture.StartSeeder(t)

	src, err := ParseSource(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false // stay entirely offline
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

	const (
		readOffset = 4 << 20 // middle of the file
		readLength = 64 << 10
	)

	got, err := tor.ReadRange(ctx, 0, readOffset, readLength, MinTraffic)
	if err != nil {
		t.Fatalf("read range: %v", err)
	}

	want := fixture.Payload[readOffset : readOffset+readLength]
	if !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes that do not match the payload", len(got))
	}

	downloaded := tor.Downloaded()

	// What the run ORDERED, which is the figure acceptance criterion 2 is
	// judged on (TOR-94). ReadRange claimed and then RELEASED before it
	// returned, and the figure has to survive that: it is the set of pieces
	// this run asked for at least once, not the set it is asking for now.
	//
	// Exact rather than approximate, because the geometry is exact: a 64 KiB
	// read at a piece-aligned 4 MiB is widened to min-traffic's 1 MiB window,
	// which is four of this fixture's 256 KiB pieces (16..20).
	claimedPieces, claimedByte := tor.Claimed()
	if claimedPieces != 4 || claimedByte != 1<<20 {
		t.Errorf("Claimed() = %d pieces / %d bytes, want 4 / %d - the window is four pieces here",
			claimedPieces, claimedByte, int64(1<<20))
	}

	// Measuring right after the read is not enough: an unbounded claim would
	// still be fetching in the background and the counter would not have
	// caught up yet. Releasing the window must actually stop the transfer, so
	// the number is taken again after a pause and must not have run away.
	time.Sleep(500 * time.Millisecond)
	settled := tor.Downloaded()

	t.Logf("downloaded %d bytes during the read, %d after settling (%.1f%% of the file) to read %d bytes at offset %d; claimed %d bytes in %d pieces",
		downloaded, settled, 100*float64(settled)/float64(testPayloadSize), readLength, readOffset,
		claimedByte, claimedPieces)

	// The window plus readahead is a handful of pieces; a quarter of the file
	// is a generous ceiling that still fails loudly if the whole file is
	// pulled, which is the regression this guards.
	const maxBytes = int64(testPayloadSize / 4)
	if settled > maxBytes {
		t.Errorf("downloaded %d bytes after settling, want at most %d - the fetcher is not limiting itself to the window",
			settled, maxBytes)
	}
	if downloaded == 0 {
		t.Error("downloaded nothing, so this test proved nothing about the fetcher")
	}
}

func TestPieceRangeFor(t *testing.T) {
	tor := &Torrent{
		files: []FileInfo{
			{Index: 0, Path: "a.mkv", Length: 1000, Offset: 0},
			{Index: 1, Path: "b.mkv", Length: 1000, Offset: 1000},
		},
		pieceLength: 256,
		numPieces:   8,
	}

	cases := []struct {
		name       string
		file       int
		off, count int64
		want       PieceRange
	}{
		{name: "start of first file", file: 0, off: 0, count: 1, want: PieceRange{0, 1}},
		{name: "spanning two pieces", file: 0, off: 250, count: 20, want: PieceRange{0, 2}},
		{name: "second file is offset", file: 1, off: 0, count: 1, want: PieceRange{3, 4}},
		{name: "whole second file", file: 1, off: 0, count: 1000, want: PieceRange{3, 8}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tor.PieceRangeFor(tc.file, tc.off, tc.count)
			if err != nil {
				t.Fatalf("PieceRangeFor: %v", err)
			}
			if got != tc.want {
				t.Errorf("PieceRangeFor(%d, %d, %d) = %+v, want %+v", tc.file, tc.off, tc.count, got, tc.want)
			}
		})
	}
}

func TestProfileByName(t *testing.T) {
	for _, want := range []Profile{MinTime, MinTraffic} {
		got, err := ProfileByName(want.Name)
		if err != nil {
			t.Fatalf("ProfileByName(%q): %v", want.Name, err)
		}
		if got != want {
			t.Errorf("ProfileByName(%q) = %+v, want %+v", want.Name, got, want)
		}
	}
	if _, err := ProfileByName("fastest"); err == nil {
		t.Error("ProfileByName accepted an unknown profile")
	}
}

// TestProfileWindowGeometry checks the one thing a profile actually decides at
// fetch time: how much gets claimed around a wanted offset.
//
// An earlier version of this test compared bytes downloaded by each profile
// against a local seeder and found min-time using *less* traffic than
// min-traffic - not because it is thriftier, but because it reads responsively,
// returns sooner and releases the window sooner. Against a loopback seeder
// there is no latency for min-time to hide, so that comparison measured which
// arm finished first, not which one is frugal. The honest traffic comparison
// needs a real swarm and belongs with the acceptance measurements.
func TestProfileWindowGeometry(t *testing.T) {
	tor := &Torrent{
		files:       []FileInfo{{Index: 0, Path: "movie.mkv", Length: 64 << 20, Offset: 0}},
		pieceLength: 1 << 20,
		numPieces:   64,
	}

	const (
		off  = 32 << 20
		want = 64 << 10 // a small read, so the profile's window decides
	)

	frugal, err := tor.WindowFor(0, off, want, MinTraffic)
	if err != nil {
		t.Fatalf("WindowFor(min-traffic): %v", err)
	}
	fast, err := tor.WindowFor(0, off, want, MinTime)
	if err != nil {
		t.Fatalf("WindowFor(min-time): %v", err)
	}

	t.Logf("%s claims %d pieces, %s claims %d pieces", MinTraffic.Name, frugal.Len(), MinTime.Name, fast.Len())

	if frugal.Len() >= fast.Len() {
		t.Errorf("%s claims %d pieces and %s claims %d - the profiles do not differ",
			MinTraffic.Name, frugal.Len(), MinTime.Name, fast.Len())
	}
	if frugal.Len() < 1 {
		t.Error("min-traffic claimed nothing, which cannot satisfy any read")
	}
}

// TestMagnetReachesMetadataWithoutCrashing is the regression test for TOR-48.
//
// A magnet arrives at Session.add with no metadata at all, and the code there
// built a *Torrent - a type whose premise is that the metadata IS known -
// purely to introduce the peers that metadata would arrive from. Reading the
// file list off a nil Info panicked, so a magnet crashed the process before it
// downloaded a byte. Every earlier test used a .torrent, which hands the info
// over at add time and can never reach that state.
//
// The peer is supplied through the config rather than after Open returns,
// because that is the path with the defect: peers added before the wait are
// the only way metadata arrives when there is no tracker and no DHT.
func TestMagnetReachesMetadataWithoutCrashing(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mkv", testPayloadSize, testPieceLength)
	seederAddr := fixture.StartSeeder(t)

	src, err := ParseSource(fixture.Magnet(t))
	if err != nil {
		t.Fatalf("parse magnet: %v", err)
	}
	if !src.IsMagnet() {
		t.Fatal("the fixture's magnet did not parse as a magnet")
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false // the dead tracker in the URI is never reached either
	cfg.MetadataTimeout = 30 * time.Second
	cfg.Peers = []string{seederAddr}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, tor, err := Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open from a magnet: %v", err)
	}
	defer session.Close()

	files := tor.Files()
	if len(files) != 1 {
		t.Fatalf("magnet run sees %d files, want 1", len(files))
	}
	if files[0].Length != testPayloadSize {
		t.Errorf("file is %d bytes, want %d - the metadata that arrived is not the fixture's",
			files[0].Length, testPayloadSize)
	}
}

// TestPublicMagnetSurvivesTheDHTRestart is the regression test for TOR-49.
//
// A magnet carrying a tracker is fetched with DHT off so a private torrent can
// never reach the DHT; once the metadata says the torrent is public, Open
// tears the client down and restarts it with DHT on, handing over the metainfo
// already in hand rather than fetching it twice. That hand-over was broken:
// anacrolix's Torrent.Metainfo always allocates the PieceLayers map, a v1
// torrent fills none of it, and AddTorrent reads a non-nil map as "this
// torrent has piece layers" and rejects every multi-piece file with
// "no piece root set for file". The result was that a plain public magnet -
// the default path of the whole program - failed on the restart.
//
// Nothing before this test could catch it. Every other test either uses a
// .torrent, whose privacy is known before the first client starts so there is
// no restart, or a magnet with DHT off, which skips the restart too. Reaching
// the restart needs DHT asked for, so the client here is given a DHT server
// with no starting nodes: running, and with nowhere to bootstrap to.
func TestPublicMagnetSurvivesTheDHTRestart(t *testing.T) {
	withOfflineDHT(t)

	fixture := torrenttest.Build(t, "movie.mkv", testPayloadSize, testPieceLength)
	seederAddr := fixture.StartSeeder(t)

	src, err := ParseSource(fixture.Magnet(t))
	if err != nil {
		t.Fatalf("parse magnet: %v", err)
	}

	// The route this test exists for: probe the tracker with DHT off, then
	// restart with DHT because the torrent turned out public.
	route := routeFor(Config{DHT: true}, src)
	if !route.probeTrackersFirst || route.dht {
		t.Fatalf("route = %+v, want the first client without DHT and a restart after the privacy check", route)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = true // required to reach the restart at all
	cfg.MetadataTimeout = 30 * time.Second
	cfg.Peers = []string{seederAddr}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	session, tor, err := Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open a public magnet with DHT on: %v", err)
	}
	defer session.Close()

	if !session.DHTEnabled() {
		t.Error("the restarted session has DHT off; the restart is the only reason it exists")
	}
	if session.WentOnlineBlind() {
		t.Error("session went online blind, but the magnet carried a tracker to probe first")
	}
	if tor.Private() {
		t.Fatal("the fixture reports itself private, so this run never took the restart")
	}
	if got := len(tor.Files()); got != 1 {
		t.Fatalf("restarted session sees %d files, want 1", got)
	}

	// Constructed is not the same as usable: the torrent carried over to the
	// second client has to be able to fetch.
	const (
		readOffset = 4 << 20
		readLength = 64 << 10
	)
	got, err := tor.ReadRange(ctx, 0, readOffset, readLength, MinTraffic)
	if err != nil {
		t.Fatalf("read from the restarted session: %v", err)
	}
	if !bytes.Equal(got, fixture.Payload[readOffset:readOffset+readLength]) {
		t.Fatalf("read %d bytes that do not match the payload", len(got))
	}
}

// TestPrivateMagnetNeverRestartsOntoDHT is the other half of TOR-49, and the
// guarantee that must not bend: acceptance criterion 5.
//
// The .torrent case is covered elsewhere and is the easy one - the flag is
// known before any client starts. This is the hard one: the flag is unknown at
// add time and only arrives with the metadata, so the whole privacy decision
// rests on Open reading it and declining to restart. DHT is asked for here,
// and must still be refused.
func TestPrivateMagnetNeverRestartsOntoDHT(t *testing.T) {
	withOfflineDHT(t)

	fixture := torrenttest.BuildPrivate(t, "movie.mkv", testPayloadSize, testPieceLength)
	seederAddr := fixture.StartSeeder(t)

	src, err := ParseSource(fixture.Magnet(t))
	if err != nil {
		t.Fatalf("parse magnet: %v", err)
	}
	if _, known := src.Privacy(); known {
		t.Fatal("the magnet already knows the private flag, so this is not the path under test")
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = true // asked for, and must still be refused
	cfg.MetadataTimeout = 30 * time.Second
	cfg.Peers = []string{seederAddr}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	session, tor, err := Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open a private magnet: %v", err)
	}
	defer session.Close()

	if !tor.Private() {
		t.Fatal("the fixture is not private, so nothing here was tested")
	}
	if session.DHTEnabled() {
		t.Error("session enabled DHT for a private torrent")
	}
	if session.UsesDHT() {
		t.Error("a DHT server is running for a private torrent")
	}
	if session.WentOnlineBlind() {
		t.Error("session went online blind for a magnet that carried a tracker")
	}
}

// withOfflineDHT lets a test ask for DHT without reaching the real one: the
// client gets a DHT server with an empty starting-node list, so it runs, is
// visible to UsesDHT, and has nowhere to bootstrap to. This is how anacrolix
// keeps its own DHT-enabled client tests offline.
func withOfflineDHT(t *testing.T) {
	t.Helper()

	tuneClientForTest = func(tc *torrent.ClientConfig) {
		tc.DhtStartingNodes = func(string) dht.StartingNodesGetter {
			return func() ([]dht.Addr, error) { return nil, nil }
		}
	}
	t.Cleanup(func() { tuneClientForTest = nil })
}

// TestClaimedCountsEachPieceOnce is the accounting behind acceptance
// criterion 2 (TOR-94), on geometry rather than on a swarm.
//
// The distinctness is the whole point: a single acceptance run claims 326
// pieces' worth of windows covering 44 distinct pieces, one of them claimed
// and released 82 times, and every re-read after the first is free. A counter
// that added up claims instead of pieces would report 326 MiB for a run that
// orders 44.
func TestClaimedCountsEachPieceOnce(t *testing.T) {
	const pieceLength = 1 << 20
	// Ten pieces, the last one short: 9 MiB plus 256 KiB.
	const length = 9*pieceLength + 256<<10

	tor := &Torrent{
		files:       []FileInfo{{Index: 0, Path: "movie.mkv", Length: length, Offset: 0}},
		pieceLength: pieceLength,
		numPieces:   10,
		length:      length,
	}

	steps := []struct {
		name       string
		claim      PieceRange
		wantPieces int
		wantBytes  int64
	}{
		{"first claim", PieceRange{0, 2}, 2, 2 * pieceLength},
		{"overlapping claim adds only what is new", PieceRange{1, 3}, 3, 3 * pieceLength},
		{"the same claim again adds nothing", PieceRange{1, 3}, 3, 3 * pieceLength},
		{"a disjoint claim adds all of itself", PieceRange{5, 7}, 5, 5 * pieceLength},
		{"the last piece is only as big as what is left", PieceRange{9, 10}, 6, 5*pieceLength + 256<<10},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			tor.noteClaimed(step.claim)

			pieces, bytes := tor.Claimed()
			if pieces != step.wantPieces || bytes != step.wantBytes {
				t.Errorf("after claiming %+v: Claimed() = %d pieces / %d bytes, want %d / %d",
					step.claim, pieces, bytes, step.wantPieces, step.wantBytes)
			}
		})
	}

	// Claiming the same piece a hundred more times must not move it, which is
	// the 82-re-reads case at the scale it actually happens.
	for i := 0; i < 100; i++ {
		tor.noteClaimed(PieceRange{1, 3})
	}
	if pieces, bytes := tor.Claimed(); pieces != 6 || bytes != 5*pieceLength+256<<10 {
		t.Errorf("a hundred repeat claims moved the figure to %d pieces / %d bytes", pieces, bytes)
	}
}

// TestClaimedRangesSaysWhereNotOnlyHowMany is TOR-119's own case: the count
// cannot distinguish 44 pieces spread across a film from 44 sitting in a lump
// at the front, and that distinction is the argument of the product.
func TestClaimedRangesSaysWhereNotOnlyHowMany(t *testing.T) {
	const pieceLength = 256 << 10
	newTor := func() *Torrent {
		return &Torrent{
			files:       []FileInfo{{Index: 0, Path: "movie.mkv", Length: 20 * pieceLength}},
			pieceLength: pieceLength,
			numPieces:   20,
			length:      20 * pieceLength,
		}
	}

	for _, tc := range []struct {
		name   string
		claims []PieceRange
		want   []PieceRange
	}{
		{"nothing claimed says nothing rather than zero", nil, nil},
		{"one claim is one range", []PieceRange{{3, 5}}, []PieceRange{{3, 5}}},
		{
			"two claims that touch are one range, because the pieces are contiguous",
			[]PieceRange{{3, 5}, {5, 7}}, []PieceRange{{3, 7}},
		},
		{
			"two claims with a gap stay two ranges - this is the whole point",
			[]PieceRange{{0, 2}, {10, 12}}, []PieceRange{{0, 2}, {10, 12}},
		},
		{
			"claims arriving out of order come back ascending",
			[]PieceRange{{10, 12}, {0, 2}, {5, 6}},
			[]PieceRange{{0, 2}, {5, 6}, {10, 12}},
		},
		{
			"overlapping claims coalesce rather than repeat",
			[]PieceRange{{2, 6}, {4, 8}}, []PieceRange{{2, 8}},
		},
		{
			"the same claim eighty-two times is still one range",
			func() []PieceRange {
				var r []PieceRange
				for i := 0; i < 82; i++ {
					r = append(r, PieceRange{7, 9})
				}
				return r
			}(),
			[]PieceRange{{7, 9}},
		},
		{
			"a sparse sweep across the file, which is what a real run looks like",
			[]PieceRange{{0, 1}, {4, 5}, {8, 9}, {12, 13}, {16, 17}},
			[]PieceRange{{0, 1}, {4, 5}, {8, 9}, {12, 13}, {16, 17}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tor := newTor()
			for _, c := range tc.claims {
				tor.noteClaimed(c)
			}

			got := tor.ClaimedRanges()
			if len(got) != len(tc.want) {
				t.Fatalf("ClaimedRanges() = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ClaimedRanges() = %+v, want %+v", got, tc.want)
				}
			}

			// The invariant that ties the new view to the old one: they are
			// two readings of one set, so they cannot disagree about its size.
			pieces, _ := tor.Claimed()
			sum := 0
			for _, r := range got {
				sum += r.Len()
			}
			if sum != pieces {
				t.Errorf("the ranges cover %d pieces but Claimed() counts %d - "+
					"two views of one set have drifted apart", sum, pieces)
			}
		})
	}
}

// TestClaimedRangesCoalesceALongSequentialSweep is the case that decided the
// shape. A run that degrades to sequential reading claims a long contiguous
// stretch, which as raw indices would be tens of kilobytes on a record that
// weighs a few hundred bytes, and as ranges is one entry.
func TestClaimedRangesCoalesceALongSequentialSweep(t *testing.T) {
	const pieceLength = 256 << 10
	tor := &Torrent{
		files:       []FileInfo{{Index: 0, Path: "remux.mkv", Length: 2000 * pieceLength}},
		pieceLength: pieceLength,
		numPieces:   2000,
		length:      2000 * pieceLength,
	}

	// Claimed a window at a time, the way a reader walking forward does.
	for begin := 0; begin < 2000; begin += 4 {
		tor.noteClaimed(PieceRange{begin, begin + 4})
	}

	got := tor.ClaimedRanges()
	if len(got) != 1 || got[0] != (PieceRange{0, 2000}) {
		t.Fatalf("ClaimedRanges() = %+v (%d entries), want one range covering the file",
			got, len(got))
	}
	if pieces, _ := tor.Claimed(); pieces != 2000 {
		t.Errorf("Claimed() counts %d pieces, want 2000", pieces)
	}
}

// TOR-179: the per-capture-point claim log. Cumulative figures cannot say
// where ONE frame came from, so the footprint is captured around each point -
// StartClaimLog through EndClaimLog - and reported in the file's own bytes.

// twoFileTorrent is the geometry every test below is judged on: two ten-piece
// files laid end to end, so a claim can straddle the join, sit wholly in
// another file, or reach past this one's end - all three of which happen for
// real, since pieces do not respect file boundaries and one claim funnel
// serves every file of a run at once.
func twoFileTorrent(pieceLength int64) *Torrent {
	return &Torrent{
		files: []FileInfo{
			{Index: 0, Path: "ep-1.mkv", Length: 10 * pieceLength, Offset: 0},
			{Index: 1, Path: "ep-2.mkv", Length: 10 * pieceLength, Offset: 10 * pieceLength},
		},
		pieceLength: pieceLength,
		numPieces:   20,
		length:      20 * pieceLength,
	}
}

func TestTheClaimLogSaysWhichPartOfTheFileOnePointOrdered(t *testing.T) {
	const piece = 256 << 10
	tor := twoFileTorrent(piece)

	tor.StartClaimLog(1)
	tor.logClaim(1, PieceRange{10, 12}) // the head of file 1
	tor.logClaim(1, PieceRange{9, 11})  // straddles the join: only piece 10 is ours
	tor.logClaim(1, PieceRange{15, 16}) // wholly inside
	tor.logClaim(1, PieceRange{0, 5})   // wholly inside file 0 - nothing to record here
	tor.logClaim(1, PieceRange{19, 20}) // the file's last piece
	tor.logClaim(0, PieceRange{3, 4})   // another file, whose own point is not open

	got := tor.EndClaimLog(1)
	want := [][2]int64{{0, 2 * piece}, {5 * piece, 6 * piece}, {9 * piece, 10 * piece}}
	if len(got) != len(want) {
		t.Fatalf("EndClaimLog(1) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EndClaimLog(1) = %v, want %v", got, want)
		}
	}

	// The log is one-shot: the point is over, and a second read must not hand
	// the next point the last one's footprint.
	if again := tor.EndClaimLog(1); again != nil {
		t.Errorf("EndClaimLog(1) a second time = %v, want nil - the log is closed", again)
	}
}

// TestTheClaimLogRecordsNothingBetweenPoints is what keeps the record
// attributable: the container inspection at the start of a file, and any read
// the bridge serves between capture points, belongs to no frame - so a claim
// arriving with no log open must be dropped rather than banked for whichever
// point opens next.
//
// Both gaps are checked, and the second is the one that needs the open test
// rather than merely a fresh log. A stray claim BEFORE the first point is
// wiped by StartClaimLog resetting the file's entry, so it would be dropped
// even by a logger that recorded everything; a stray claim AFTER a point
// closed is only dropped because nothing is open to record it.
func TestTheClaimLogRecordsNothingBetweenPoints(t *testing.T) {
	const piece = 256 << 10
	tor := twoFileTorrent(piece)

	// The inspect at the head of the file, before any point is taken.
	tor.logClaim(0, PieceRange{0, 4})

	tor.StartClaimLog(0)
	tor.logClaim(0, PieceRange{6, 7})
	got := tor.EndClaimLog(0)

	want := [][2]int64{{6 * piece, 7 * piece}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("EndClaimLog(0) = %v, want %v - a read taken before the point opened was "+
			"attributed to it", got, want)
	}

	// The gap after that point closed: a read the bridge is still serving
	// while the engine writes the frame and moves on.
	tor.logClaim(0, PieceRange{1, 2})

	tor.StartClaimLog(0)
	tor.logClaim(0, PieceRange{8, 9})
	got = tor.EndClaimLog(0)

	want = [][2]int64{{8 * piece, 9 * piece}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("EndClaimLog(0) = %v, want %v - a read taken between two points was "+
			"attributed to the next one", got, want)
	}
}

// TestTheClaimLogKeepsTheHeaderForEveryPointThatReadIt is the difference from
// noteClaimed, and the reason the two are separate records rather than one.
// The counter de-duplicates across the whole run, deliberately (Claimed's own
// doc: what the run ordered at least once). Every capture point's ffprobe
// re-reads the container header, so a log built on the counter's own
// bookkeeping would hand the header to the first point and tell every other
// one it came from nowhere near the front of the file.
func TestTheClaimLogKeepsTheHeaderForEveryPointThatReadIt(t *testing.T) {
	const piece = 256 << 10
	tor := twoFileTorrent(piece)

	header := PieceRange{0, 1}
	for point := 0; point < 3; point++ {
		tor.StartClaimLog(0)
		tor.logClaim(0, header)
		tor.noteClaimed(header)
		tor.logClaim(0, PieceRange{3 + point, 4 + point})
		tor.noteClaimed(PieceRange{3 + point, 4 + point})

		got := tor.EndClaimLog(0)
		want := [][2]int64{{0, piece}, {int64(3+point) * piece, int64(4+point) * piece}}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("point %d: EndClaimLog(0) = %v, want %v", point, got, want)
		}
	}

	// And the run-wide counter is untouched by all that repetition: one header
	// piece plus three distinct ones.
	if pieces, _ := tor.Claimed(); pieces != 4 {
		t.Errorf("Claimed() counts %d pieces, want 4 - the per-point log must not double-count "+
			"into the run's own figure", pieces)
	}
}

// TestTheClaimLogCoalescesWhatOnePointRead: a point orders a window at a time
// and re-reads the same few kilobytes several times over, so the record is
// merged into stretches - a picture of where it reached, not a bag of reads.
func TestTheClaimLogCoalescesWhatOnePointRead(t *testing.T) {
	const piece = 256 << 10
	tor := twoFileTorrent(piece)

	tor.StartClaimLog(0)
	// Out of order, touching, overlapping, and one wholly inside another -
	// every shape the merge has to survive.
	tor.logClaim(0, PieceRange{5, 6})
	tor.logClaim(0, PieceRange{1, 4})
	tor.logClaim(0, PieceRange{4, 5})
	tor.logClaim(0, PieceRange{2, 3})
	tor.logClaim(0, PieceRange{1, 2})
	tor.logClaim(0, PieceRange{8, 9})

	got := tor.EndClaimLog(0)
	want := [][2]int64{{piece, 6 * piece}, {8 * piece, 9 * piece}}
	if len(got) != len(want) {
		t.Fatalf("EndClaimLog(0) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EndClaimLog(0) = %v, want %v", got, want)
		}
	}
}

// TestTheClaimLogClipsToTheFilesLastByte: a file's last byte usually sits
// inside a piece it shares with the next file, and a claim on that piece must
// be recorded as reaching the end of THIS file rather than past it - the
// record is the frame's own truth about the file, so an offset outside it
// would be unreadable against the file's own length.
func TestTheClaimLogClipsToTheFilesLastByte(t *testing.T) {
	const piece = 256 << 10
	// One file ending one byte into piece 3, with a second file after it.
	tor := &Torrent{
		files: []FileInfo{
			{Index: 0, Path: "a.mkv", Length: 3*piece + 1, Offset: 0},
			{Index: 1, Path: "b.mkv", Length: piece, Offset: 3*piece + 1},
		},
		pieceLength: piece,
		numPieces:   5,
		length:      4*piece + 1,
	}

	tor.StartClaimLog(0)
	tor.logClaim(0, PieceRange{3, 5})
	got := tor.EndClaimLog(0)

	want := [2]int64{3 * piece, 3*piece + 1}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("EndClaimLog(0) = %v, want %v - the record must stop at the file's own end", got, want)
	}
}

// TestNoClaimLogAtAllReportsNothing keeps the two silences the same: a file
// nobody opened a log for, and a point that ordered nothing. Both are "no
// provenance", which manifest.Frame.ByteRanges records as absent rather than
// as a frame that came from nowhere in particular.
//
// The middle case is what logClaim's open test is actually load-bearing for,
// and it is not covered by StartClaimLog resetting the entry: a claim arriving
// with no log open must not create one, or the map grows an entry per file
// that nothing ever reads and a later read of a closed log answers with
// somebody else's traffic.
func TestNoClaimLogAtAllReportsNothing(t *testing.T) {
	const piece = 256 << 10
	tor := twoFileTorrent(piece)

	if got := tor.EndClaimLog(0); got != nil {
		t.Errorf("EndClaimLog(0) with no log open = %v, want nil", got)
	}

	// The file's own container inspection, which belongs to no capture point.
	tor.logClaim(0, PieceRange{0, 4})
	if got := tor.EndClaimLog(0); got != nil {
		t.Errorf("EndClaimLog(0) after a claim nobody was recording = %v, want nil - "+
			"the claim opened a log of its own", got)
	}

	tor.StartClaimLog(0)
	if got := tor.EndClaimLog(0); got != nil {
		t.Errorf("EndClaimLog(0) after a point that ordered nothing = %v, want nil", got)
	}
}

// TestTwoFilesCapturedAtOnceKeepTheirOwnFootprints is why the log is keyed by
// file. A run captures several files at a time (core.Config.Parallelism)
// through one torrent and therefore one claim funnel, so without the key one
// file's reads would be recorded as another file's provenance.
func TestTwoFilesCapturedAtOnceKeepTheirOwnFootprints(t *testing.T) {
	const piece = 256 << 10
	tor := twoFileTorrent(piece)

	tor.StartClaimLog(0)
	tor.StartClaimLog(1)
	tor.logClaim(0, PieceRange{2, 3})
	tor.logClaim(1, PieceRange{16, 17})
	tor.logClaim(0, PieceRange{4, 5})

	first, second := tor.EndClaimLog(0), tor.EndClaimLog(1)
	wantFirst := [][2]int64{{2 * piece, 3 * piece}, {4 * piece, 5 * piece}}
	wantSecond := [][2]int64{{6 * piece, 7 * piece}}

	if len(first) != 2 || first[0] != wantFirst[0] || first[1] != wantFirst[1] {
		t.Errorf("file 0 = %v, want %v", first, wantFirst)
	}
	if len(second) != 1 || second[0] != wantSecond[0] {
		t.Errorf("file 1 = %v, want %v", second, wantSecond)
	}
}
