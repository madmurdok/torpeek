package swarm

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// saveAndReload writes what TorrentFile produced and reads it back with the
// metainfo package, which is the only proof the acceptance criterion accepts:
// writing a file says nothing about whether anything can load it, and the two
// failures this feature can actually have - an info dictionary that was
// re-encoded rather than copied, and a wrapper key that makes a loader refuse
// the file - are both invisible until something reads it back.
func saveAndReload(t *testing.T, tor *Torrent) (string, []byte, *metainfo.MetaInfo) {
	t.Helper()

	data, err := tor.TorrentFile()
	if err != nil {
		t.Fatalf("render the torrent file: %v", err)
	}

	path := filepath.Join(t.TempDir(), "saved.torrent")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write the saved torrent: %v", err)
	}

	mi, err := metainfo.LoadFromFile(path)
	if err != nil {
		t.Fatalf("load the saved torrent back: %v", err)
	}
	return path, data, mi
}

// assertSavedTorrentIsTheSameTorrent is the shared body of the two arms
// below: whatever the run was given, the file it leaves behind must be the
// same torrent, and must be loadable by the very same code path a source on
// the command line goes through.
func assertSavedTorrentIsTheSameTorrent(t *testing.T, tor *Torrent) {
	t.Helper()

	if len(tor.Files()) == 0 {
		t.Fatal("the torrent under test holds no files, so comparing file lists below would prove nothing")
	}

	path, data, mi := saveAndReload(t, tor)

	if got := mi.HashInfoBytes().HexString(); got != tor.InfoHash() {
		t.Fatalf("the saved file's infohash is %s, the torrent's is %s - "+
			"the info dictionary was not carried over byte for byte", got, tor.InfoHash())
	}

	// The TOR-49 trap, checked against the BYTES rather than against the
	// struct that comes back from loading them, and the difference is the
	// whole point of checking it at all: anacrolix always allocates
	// PieceLayers, a v1 torrent fills none of it, and bencode's omitempty
	// tests a map by IsNil - so an empty-but-non-nil map is not omitted, it
	// is written out as "piece layers": de. Reading that back through this
	// same library hands out a nil map again, so a reloaded MetaInfo cannot
	// tell the two files apart; what differs is what is on disk, and what is
	// on disk is a v1 torrent announcing a v2 structure it has no roots for -
	// exactly the claim that made AddTorrent reject the in-memory hand-over
	// file by file. url-list is the identical shape of mistake one field
	// over: a key describing web seeds this torrent does not have.
	for _, key := range []string{"piece layers", "url-list"} {
		if bytes.Contains(data, []byte(key)) {
			t.Errorf("the saved file carries a %q key describing something this torrent does not have; "+
				"anacrolix allocates both empty and bencode's omitempty does not drop an empty map or slice", key)
		}
	}

	// Loadable is not the same claim as parseable, so it is checked against
	// the thing that actually loads a source: ParseSource plus Open, exactly
	// what a person handing this file back to torpeek would go through. No
	// peers and no seeder are needed - a .torrent carries its own info - so
	// this proves the file alone is enough.
	src, err := ParseSource(path)
	if err != nil {
		t.Fatalf("the saved file does not parse as a source: %v", err)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.Upload = false
	cfg.MetadataTimeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	reopened, reopenedTorrent, err := Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("the saved file could not be opened as a torrent: %v", err)
	}
	defer reopened.Close()

	if reopenedTorrent.InfoHash() != tor.InfoHash() {
		t.Errorf("reopening the saved file gives infohash %s, want %s",
			reopenedTorrent.InfoHash(), tor.InfoHash())
	}
	if got, want := len(reopenedTorrent.Files()), len(tor.Files()); got != want {
		t.Fatalf("reopened torrent holds %d files, the original holds %d", got, want)
	}
	for i, f := range reopenedTorrent.Files() {
		if original := tor.Files()[i]; f.Path != original.Path || f.Length != original.Length {
			t.Errorf("file %d of the reopened torrent is %s (%d bytes), want %s (%d bytes)",
				i, f.Path, f.Length, original.Path, original.Length)
		}
	}
}

// TestSavedTorrentFromAMagnetKeepsTheInfohash is the half of TOR-73 that
// could not be taken for granted: a magnet has no .torrent to copy, so the
// file has to be rebuilt from the info dictionary the swarm sent over BEP 9.
// The infohash IS that dictionary's hash, so a rebuild that re-encoded the
// parsed struct instead of carrying the bytes would produce a file naming a
// different torrent - which is why this loads the file back and compares,
// rather than checking that a write returned no error.
func TestSavedTorrentFromAMagnetKeepsTheInfohash(t *testing.T) {
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
	cfg.DHT = false // the dead tracker in the magnet is never reached either
	cfg.MetadataTimeout = 30 * time.Second
	cfg.Peers = []string{seederAddr}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, tor, err := Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open from a magnet: %v", err)
	}
	defer session.Close()

	assertSavedTorrentIsTheSameTorrent(t, tor)

	// The wrapper is synthesised, not recovered, and that is stated here as
	// an expectation rather than only in a doc comment: a magnet never
	// fetches the creation date, the comment or the created-by, so the saved
	// file cannot be - and must not be claimed to be - the publisher's own.
	_, _, mi := saveAndReload(t, tor)
	if mi.CreationDate == 0 {
		t.Error("the saved file has no creation date; it is stamped at write time, not recovered")
	}
	if mi.CreatedBy == "" {
		t.Error("the saved file has no created-by; the library's own string is what is written there")
	}

	// The trackers are the one part of the wrapper that IS real: without them
	// the saved file would find no swarm, which would make it a torrent
	// nobody can use.
	trackers := mi.UpvertedAnnounceList().DistinctValues()
	if len(trackers) == 0 {
		t.Error("the saved file carries no announce URL, so it could not find its swarm")
	}
}

// TestSavedTorrentFromAFileKeepsTheInfohash is the other arm, and it is not
// redundant with the magnet one: this is the source whose run.json used to
// name a file that had already been deleted (an upload staged in a temp
// directory), so it is the arm the "make it true" half of TOR-73 rests on.
// The route through swarm is different too - AddTorrentFromFile rather than
// BEP 9 - and only the run-time metainfo is shared.
func TestSavedTorrentFromAFileKeepsTheInfohash(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mkv", testPayloadSize, testPieceLength)

	src, err := ParseSource(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("parse the fixture .torrent: %v", err)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.Upload = false
	cfg.MetadataTimeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, tor, err := Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open from a .torrent: %v", err)
	}
	defer session.Close()

	assertSavedTorrentIsTheSameTorrent(t, tor)

	// The saved file is a faithful torrent, not a copy of the file it came
	// from: the wrapper is regenerated even here, where an original wrapper
	// did exist. Asserted so nobody later "fixes" the difference by
	// comparing bytes.
	original, err := os.ReadFile(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("read the fixture .torrent: %v", err)
	}
	saved, err := tor.TorrentFile()
	if err != nil {
		t.Fatalf("render the torrent file: %v", err)
	}
	if string(saved) == string(original) {
		t.Log("the saved file happens to be byte-identical to the fixture's; that is not promised")
	}
}

// TestSavedTorrentKeepsInfoKeysTorpeekDoesNotModel is what makes "byte for
// byte" a claim with teeth rather than a phrase in a doc comment.
//
// The other two arms cannot tell a copied info dictionary from one re-encoded
// out of metainfo.Info, because their fixtures were themselves produced by
// bencoding that very struct: a round trip through it is the identity there,
// and both arms pass with the bytes thrown away and rebuilt. Real torrents
// are not like that - a private tracker's info dictionary carries keys this
// library has no field for, and re-encoding silently drops them, which
// changes the infohash and so changes which torrent the saved file names.
//
// So this arm builds an info dictionary carrying "publisher", a key
// metainfo.Info does not model, and asks the same question. A re-encoding
// implementation cannot pass it.
func TestSavedTorrentKeepsInfoKeysTorpeekDoesNotModel(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mkv", testPayloadSize, testPieceLength)

	original, err := metainfo.LoadFromFile(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("load the fixture .torrent: %v", err)
	}

	// Decoded to a plain map rather than to metainfo.Info, precisely because
	// the struct is what loses the extra key. The encoder sorts map keys, so
	// what comes back out is still valid bencode.
	var dict map[string]any
	if err := bencode.Unmarshal(original.InfoBytes, &dict); err != nil {
		t.Fatalf("decode the fixture's info dictionary: %v", err)
	}
	dict["publisher"] = "torpeek-test"
	infoBytes, err := bencode.Marshal(dict)
	if err != nil {
		t.Fatalf("re-encode the info dictionary: %v", err)
	}

	path := filepath.Join(t.TempDir(), "with-publisher.torrent")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create the .torrent: %v", err)
	}
	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	if err := mi.Write(f); err != nil {
		f.Close()
		t.Fatalf("write the .torrent: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close the .torrent: %v", err)
	}

	src, err := ParseSource(path)
	if err != nil {
		t.Fatalf("parse the .torrent with an extra info key: %v", err)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.Upload = false
	cfg.MetadataTimeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, tor, err := Open(ctx, cfg, src)
	if err != nil {
		t.Fatalf("open the .torrent with an extra info key: %v", err)
	}
	defer session.Close()

	// The premise, stated rather than assumed: this fixture's dictionary
	// really is one the struct cannot reproduce. Without this the arm could
	// quietly degrade into a copy of the one above.
	if info := tor.t.Info(); info != nil {
		reencoded, err := bencode.Marshal(*info)
		if err != nil {
			t.Fatalf("re-encode the parsed info: %v", err)
		}
		if bytes.Equal(reencoded, infoBytes) {
			t.Fatal("re-encoding the parsed info reproduced the original bytes, " +
				"so this fixture cannot tell a copy from a rebuild")
		}
	}

	assertSavedTorrentIsTheSameTorrent(t, tor)

	_, data, _ := saveAndReload(t, tor)
	if !bytes.Contains(data, []byte("9:publisher")) {
		t.Error("the saved file lost the publisher key, so its info dictionary was rebuilt rather than copied")
	}
}
