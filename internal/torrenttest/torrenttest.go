// Package torrenttest builds throwaway torrents and serves them on loopback,
// so tests can exercise real fetching end to end without touching the network.
//
// It is test scaffolding that lives outside _test.go files only because more
// than one package needs it.
//
// Since TOR-101 one of those callers is archivecheck/, which runs a release
// archive's own torpeek binary as a child process against a seeder started
// here. That is why this package is worth keeping general rather than folding
// into whichever _test.go needed it last: it is now also the reason the
// archive gate does not have to import anacrolix/torrent itself, which
// ARCHITECTURE.md reserves for internal/swarm.
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

	return Fixture{
		Dir:         dir,
		TorrentPath: writeTorrent(t, dir, pieceLength, false),
		Payload:     payload,
		FileName:    name,
	}
}

// BuildPrivate is Build with the BEP 27 private flag set.
//
// It exists for the one case a .torrent fixture cannot reproduce: a magnet
// whose torrent turns out private only once the metadata has arrived. That is
// the branch of Session.Open that decides not to restart with DHT, and the
// guarantee it protects is worth a test that actually walks it.
func BuildPrivate(t *testing.T, name string, size, pieceLength int64) Fixture {
	t.Helper()

	dir := t.TempDir()
	payload := deterministicBytes(size)

	if err := os.WriteFile(filepath.Join(dir, name), payload, 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	return Fixture{
		Dir:         dir,
		TorrentPath: writeTorrent(t, dir, pieceLength, true),
		Payload:     payload,
		FileName:    name,
	}
}

// writeTorrent builds the info for a directory already on disk and writes the
// .torrent describing it, returning the path. No announce URL, so nothing in a
// test can reach a tracker.
func writeTorrent(t *testing.T, dir string, pieceLength int64, private bool) string {
	t.Helper()

	info := metainfo.Info{PieceLength: pieceLength}
	if err := info.BuildFromFilePath(dir); err != nil {
		t.Fatalf("build info: %v", err)
	}
	if private {
		info.Private = &private
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
	return torrentPath
}

// StartSeeder serves the fixture on loopback and returns its host:port. The
// seeder is fully verified before this returns - an unhashed seeder has
// nothing to offer and a leecher would simply time out against it.
func (f Fixture) StartSeeder(t *testing.T) string {
	t.Helper()

	// The torrent's name is the payload directory's own name, so its files
	// live at DataDir/<name>/... - the seeder's DataDir is the parent.
	return startSeeding(t, f.TorrentPath, filepath.Dir(f.Dir), true)
}

// startSeeding runs a seeder over dataDir. complete says whether to wait for
// every byte to verify: a seeder with a deliberate hole never will.
func startSeeding(t *testing.T, torrentPath, dataDir string, complete bool) string {
	t.Helper()

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dataDir
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

	// A seeder that is still hashing announces its pieces as it goes, so a
	// leecher asking early sees a map that is merely incomplete and reads it
	// as a swarm full of holes. Both kinds of seeder must finish verifying
	// before anyone is told about them.
	deadline := time.Now().Add(60 * time.Second)
	var last int64 = -1
	stable := 0
	for {
		completed := tor.BytesCompleted()
		if complete {
			if completed >= tor.Length() {
				break
			}
		} else {
			if completed == last {
				stable++
			} else {
				stable, last = 0, completed
			}
			// Held something, missing something, and unchanged for a while:
			// hashing is done and the hole is real.
			if stable >= 5 && completed > 0 && tor.BytesMissing() > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("seeder never settled: %d of %d bytes verified, %d missing",
				completed, tor.Length(), tor.BytesMissing())
		}
		time.Sleep(100 * time.Millisecond)
	}

	for _, a := range cl.ListenAddrs() {
		if tcp, ok := a.(*net.TCPAddr); ok {
			return net.JoinHostPort("127.0.0.1", strconv.Itoa(tcp.Port))
		}
	}
	t.Fatal("seeder has no TCP listen address")
	return ""
}

// StartSeederWithHole serves a copy of the fixture in which one stretch of the
// payload is wrong, so the pieces covering it fail verification and the seeder
// genuinely does not have them. from and to are fractions of the file.
//
// This is the only honest way to test behaviour against an incomplete swarm:
// a seeder cannot be asked to withhold pieces it holds, but it will never
// offer pieces whose hash does not match.
func (f Fixture) StartSeederWithHole(t *testing.T, from, to float64) string {
	t.Helper()

	// The torrent's name is the directory's name, so the copy has to keep it.
	parent := t.TempDir()
	holed := filepath.Join(parent, filepath.Base(f.Dir))
	if err := os.MkdirAll(holed, 0o700); err != nil {
		t.Fatalf("create seeder directory: %v", err)
	}

	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		t.Fatalf("read payload directory: %v", err)
	}

	var largest string
	var largestSize int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		if info.Size() > largestSize {
			largest, largestSize = e.Name(), info.Size()
		}
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(f.Dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if e.Name() == largest {
			start := int(float64(len(content)) * from)
			end := int(float64(len(content)) * to)
			for i := start; i < end && i < len(content); i++ {
				content[i] ^= 0xFF
			}
		}
		if err := os.WriteFile(filepath.Join(holed, e.Name()), content, 0o600); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}

	return startSeeding(t, f.TorrentPath, parent, false)
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

	return Fixture{
		Dir:         dir,
		TorrentPath: writeTorrent(t, dir, pieceLength, false),
		Payload:     payload,
		FileName:    name,
	}
}

// BuildDir makes a torrent over a directory the caller has already filled,
// for tests that need several files in one torrent.
func BuildDir(t *testing.T, dir string, pieceLength int64) Fixture {
	t.Helper()

	return Fixture{Dir: dir, TorrentPath: writeTorrent(t, dir, pieceLength, false)}
}

// Magnet is the fixture as a magnet URI: an infohash and nothing else that
// matters, so a session opened from it reaches add() with no metadata yet.
//
// That is the state a .torrent fixture can never reproduce - it hands over the
// info bytes at add time - and it is the state a magnet always starts in. The
// tracker in the URI is a dead loopback address included only to satisfy the
// privacy routing, which refuses a trackerless magnet unless DHT is allowed;
// nothing in a test should reach a tracker, and nothing here does. Metadata
// arrives over BEP 9 from whatever peer the caller supplies.
func (f Fixture) Magnet(t *testing.T) string {
	t.Helper()

	mi, err := metainfo.LoadFromFile(f.TorrentPath)
	if err != nil {
		t.Fatalf("load fixture torrent: %v", err)
	}
	return "magnet:?xt=urn:btih:" + mi.HashInfoBytes().HexString() +
		"&tr=http://127.0.0.1:1/announce"
}
