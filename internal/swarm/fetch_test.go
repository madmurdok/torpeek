package swarm

import (
	"bytes"
	"context"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

const (
	testPayloadSize = 8 << 20   // 8 MiB
	testPieceLength = 256 << 10 // 256 KiB, so the payload is 32 pieces
)

// buildPayloadTorrent writes a deterministic payload and a .torrent describing
// it, returning the payload bytes and the path to the torrent file.
func buildPayloadTorrent(t *testing.T) (payloadDir, torrentPath string, payload []byte) {
	t.Helper()

	payloadDir = t.TempDir()
	payload = make([]byte, testPayloadSize)
	rng := rand.New(rand.NewSource(1))
	if _, err := rng.Read(payload); err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(payloadDir, "movie.mkv"), payload, 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	info := metainfo.Info{PieceLength: testPieceLength}
	if err := info.BuildFromFilePath(payloadDir); err != nil {
		t.Fatalf("build info: %v", err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("marshal info: %v", err)
	}

	// No announce URL: nothing in this test may reach the network.
	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	torrentPath = filepath.Join(t.TempDir(), "payload.torrent")
	f, err := os.Create(torrentPath)
	if err != nil {
		t.Fatalf("create torrent file: %v", err)
	}
	defer f.Close()
	if err := mi.Write(f); err != nil {
		t.Fatalf("write torrent file: %v", err)
	}
	return payloadDir, torrentPath, payload
}

// startSeeder serves the payload from disk on loopback and returns its address.
func startSeeder(t *testing.T, payloadDir, torrentPath string) string {
	t.Helper()

	// The torrent's name is the payload directory's own name, so its files
	// live at DataDir/<name>/... - the seeder's DataDir is the parent.
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = filepath.Dir(payloadDir)
	cfg.Seed = true
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.DisablePEX = true
	cfg.ListenPort = 0

	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatalf("start seeder: %v", err)
	}
	t.Cleanup(func() { cl.Close() })

	tor, err := cl.AddTorrentFromFile(torrentPath)
	if err != nil {
		t.Fatalf("seeder add torrent: %v", err)
	}
	<-tor.GotInfo()
	tor.VerifyData()

	// VerifyData is asynchronous; a seeder that has not finished hashing has
	// nothing to offer, and the leecher would just time out.
	deadline := time.Now().Add(30 * time.Second)
	for tor.BytesCompleted() < tor.Length() {
		if time.Now().After(deadline) {
			t.Fatalf("seeder verified only %d of %d bytes", tor.BytesCompleted(), tor.Length())
		}
		time.Sleep(50 * time.Millisecond)
	}

	for _, a := range cl.ListenAddrs() {
		if tcp, ok := a.(*net.TCPAddr); ok {
			return net.JoinHostPort("127.0.0.1", strconv.Itoa(tcp.Port))
		}
	}
	t.Fatal("seeder has no TCP listen address")
	return ""
}

// TestReadRangeFetchesOnlyTheWindow is the acceptance test for TOR-4: reading a
// range from the middle of a torrent must cost the pieces covering that range,
// not the file.
func TestReadRangeFetchesOnlyTheWindow(t *testing.T) {
	payloadDir, torrentPath, payload := buildPayloadTorrent(t)
	seederAddr := startSeeder(t, payloadDir, torrentPath)

	src, err := ParseSource(torrentPath)
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

	want := payload[readOffset : readOffset+readLength]
	if !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes that do not match the payload", len(got))
	}

	downloaded := tor.Downloaded()

	// Measuring right after the read is not enough: an unbounded claim would
	// still be fetching in the background and the counter would not have
	// caught up yet. Releasing the window must actually stop the transfer, so
	// the number is taken again after a pause and must not have run away.
	time.Sleep(500 * time.Millisecond)
	settled := tor.Downloaded()

	t.Logf("downloaded %d bytes during the read, %d after settling (%.1f%% of the file) to read %d bytes at offset %d",
		downloaded, settled, 100*float64(settled)/float64(testPayloadSize), readLength, readOffset)

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
