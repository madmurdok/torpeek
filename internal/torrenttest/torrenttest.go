// Package torrenttest builds throwaway torrents and serves them on loopback,
// so tests can exercise real fetching end to end without touching the network.
//
// It is test scaffolding that lives outside _test.go files only because more
// than one package needs it.
package torrenttest

import (
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

// Fixture is a payload on disk plus the .torrent describing it.
type Fixture struct {
	// Dir holds the payload; it is also the torrent's name.
	Dir string
	// TorrentPath is the .torrent file, with no announce URL so nothing in a
	// test can reach a tracker.
	TorrentPath string
	// Payload is the exact file content, for comparing against what was read.
	Payload []byte
	// FileName is the payload file's name inside the torrent.
	FileName string
}

// Build writes a deterministic payload of size bytes and a torrent over it.
func Build(t *testing.T, name string, size, pieceLength int64) Fixture {
	t.Helper()

	dir := t.TempDir()
	payload := deterministicBytes(size)

	if err := os.WriteFile(filepath.Join(dir, name), payload, 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	info := metainfo.Info{PieceLength: pieceLength}
	if err := info.BuildFromFilePath(dir); err != nil {
		t.Fatalf("build info: %v", err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("marshal info: %v", err)
	}

	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	torrentPath := filepath.Join(t.TempDir(), "fixture.torrent")
	f, err := os.Create(torrentPath)
	if err != nil {
		t.Fatalf("create torrent file: %v", err)
	}
	defer f.Close()
	if err := mi.Write(f); err != nil {
		t.Fatalf("write torrent file: %v", err)
	}

	return Fixture{Dir: dir, TorrentPath: torrentPath, Payload: payload, FileName: name}
}

// StartSeeder serves the fixture on loopback and returns its host:port. The
// seeder is fully verified before this returns - an unhashed seeder has
// nothing to offer and a leecher would simply time out against it.
func (f Fixture) StartSeeder(t *testing.T) string {
	t.Helper()

	// The torrent's name is the payload directory's own name, so its files
	// live at DataDir/<name>/... - the seeder's DataDir is the parent.
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = filepath.Dir(f.Dir)
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

	tor, err := cl.AddTorrentFromFile(f.TorrentPath)
	if err != nil {
		t.Fatalf("seeder add torrent: %v", err)
	}
	<-tor.GotInfo()
	tor.VerifyData()

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

// deterministicBytes fills a buffer with a cheap, repeatable pattern. Random
// data would work too, but a pattern makes a mismatch readable in a diff.
func deterministicBytes(size int64) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i*31 + i/251)
	}
	return b
}

// BuildFromBytes makes a torrent over content the caller already has, for
// tests that need a real media file rather than a synthetic payload.
func BuildFromBytes(t *testing.T, name string, payload []byte, pieceLength int64) Fixture {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), payload, 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	info := metainfo.Info{PieceLength: pieceLength}
	if err := info.BuildFromFilePath(dir); err != nil {
		t.Fatalf("build info: %v", err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("marshal info: %v", err)
	}

	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	torrentPath := filepath.Join(t.TempDir(), "fixture.torrent")
	f, err := os.Create(torrentPath)
	if err != nil {
		t.Fatalf("create torrent file: %v", err)
	}
	defer f.Close()
	if err := mi.Write(f); err != nil {
		t.Fatalf("write torrent file: %v", err)
	}

	return Fixture{Dir: dir, TorrentPath: torrentPath, Payload: payload, FileName: name}
}
