package swarm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// writeTorrentFile builds a real .torrent over a small payload directory.
func writeTorrentFile(t *testing.T, private bool, announce string) string {
	t.Helper()

	payload := t.TempDir()
	if err := os.WriteFile(filepath.Join(payload, "movie.mkv"), make([]byte, 64*1024), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	info := metainfo.Info{PieceLength: 16 * 1024}
	if err := info.BuildFromFilePath(payload); err != nil {
		t.Fatalf("build info: %v", err)
	}
	if private {
		p := true
		info.Private = &p
	}

	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("marshal info: %v", err)
	}

	mi := metainfo.MetaInfo{InfoBytes: infoBytes, Announce: announce}
	path := filepath.Join(t.TempDir(), "test.torrent")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create torrent file: %v", err)
	}
	defer f.Close()
	if err := mi.Write(f); err != nil {
		t.Fatalf("write torrent file: %v", err)
	}
	return path
}

func TestSourcePrivacy(t *testing.T) {
	cases := []struct {
		name        string
		private     bool
		wantPrivate bool
		wantKnown   bool
	}{
		{name: "private torrent file", private: true, wantPrivate: true, wantKnown: true},
		{name: "public torrent file", private: false, wantPrivate: false, wantKnown: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := ParseSource(writeTorrentFile(t, tc.private, "http://tracker.invalid/announce"))
			if err != nil {
				t.Fatalf("parse source: %v", err)
			}
			private, known := src.Privacy()
			if private != tc.wantPrivate || known != tc.wantKnown {
				t.Fatalf("Privacy() = (%v, %v), want (%v, %v)", private, known, tc.wantPrivate, tc.wantKnown)
			}
		})
	}

	t.Run("magnet hides the flag", func(t *testing.T) {
		src, err := ParseSource("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&tr=http%3A%2F%2Ftracker.invalid%2Fannounce")
		if err != nil {
			t.Fatalf("parse magnet: %v", err)
		}
		if _, known := src.Privacy(); known {
			t.Fatal("Privacy() reported known for a magnet, but metadata has not arrived yet")
		}
		if got := len(src.Trackers()); got != 1 {
			t.Fatalf("Trackers() = %d, want 1", got)
		}
	})
}

// TestRouteFor covers the guarantee that must never regress: a torrent known to
// be private is never routed onto the DHT, whatever the config asks for.
func TestRouteFor(t *testing.T) {
	privateFile, err := ParseSource(writeTorrentFile(t, true, "http://tracker.invalid/announce"))
	if err != nil {
		t.Fatalf("parse private source: %v", err)
	}
	publicFile, err := ParseSource(writeTorrentFile(t, false, "http://tracker.invalid/announce"))
	if err != nil {
		t.Fatalf("parse public source: %v", err)
	}
	magnetWithTracker, err := ParseSource("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&tr=http%3A%2F%2Ftracker.invalid%2Fannounce")
	if err != nil {
		t.Fatalf("parse magnet with tracker: %v", err)
	}
	bareMagnet, err := ParseSource("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatalf("parse bare magnet: %v", err)
	}

	cases := []struct {
		name      string
		dhtAsked  bool
		src       Source
		wantDHT   bool
		wantProbe bool
		wantBlind bool
		wantErr   error
	}{
		{name: "private file never gets DHT", dhtAsked: true, src: privateFile, wantDHT: false},
		{name: "private file with DHT off", dhtAsked: false, src: privateFile, wantDHT: false},
		{name: "public file gets DHT", dhtAsked: true, src: publicFile, wantDHT: true},
		{name: "public file respects DHT off", dhtAsked: false, src: publicFile, wantDHT: false},
		{name: "magnet with tracker probes first", dhtAsked: true, src: magnetWithTracker, wantDHT: false, wantProbe: true},
		{name: "magnet with tracker, DHT off", dhtAsked: false, src: magnetWithTracker, wantDHT: false},
		{name: "bare magnet goes blind", dhtAsked: true, src: bareMagnet, wantDHT: true, wantBlind: true},
		{name: "bare magnet without DHT fails", dhtAsked: false, src: bareMagnet, wantErr: ErrPrivacyUnresolvable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routeFor(Config{DHT: tc.dhtAsked}, tc.src)
			if got.err != tc.wantErr {
				t.Fatalf("err = %v, want %v", got.err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if got.dht != tc.wantDHT {
				t.Errorf("dht = %v, want %v", got.dht, tc.wantDHT)
			}
			if got.probeTrackersFirst != tc.wantProbe {
				t.Errorf("probeTrackersFirst = %v, want %v", got.probeTrackersFirst, tc.wantProbe)
			}
			if got.blind != tc.wantBlind {
				t.Errorf("blind = %v, want %v", got.blind, tc.wantBlind)
			}
		})
	}
}

// TestOpenPrivateTorrentStaysOffDHT runs the real client. A .torrent needs no
// network to yield metadata, so this stays offline and fast.
func TestOpenPrivateTorrentStaysOffDHT(t *testing.T) {
	src, err := ParseSource(writeTorrentFile(t, true, "http://tracker.invalid/announce"))
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = true // asked for, and must still be refused
	cfg.MetadataTimeout = 10 * time.Second

	session, tor, err := Open(context.Background(), cfg, src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer session.Close()

	if session.DHTEnabled() {
		t.Error("session enabled DHT for a private torrent")
	}
	if session.WentOnlineBlind() {
		t.Error("session went online blind for a torrent whose flag was known upfront")
	}
	if !tor.Private() {
		t.Error("torrent did not report itself private")
	}
	if got := len(tor.Videos()); got != 1 {
		t.Errorf("Videos() = %d, want 1", got)
	}
	if tor.Downloaded() != 0 {
		t.Errorf("Downloaded() = %d, want 0 - metadata must cost no payload bytes", tor.Downloaded())
	}
}

func TestSelectVideos(t *testing.T) {
	files := []FileInfo{
		{Index: 0, Path: "Show.S01E01.mkv", Length: 900_000_000},
		{Index: 1, Path: "Show.S01E02.mkv", Length: 880_000_000},
		{Index: 2, Path: "Sample/show-sample.mkv", Length: 20_000_000},
		{Index: 3, Path: "poster.jpg", Length: 400_000},
		{Index: 4, Path: "info.nfo", Length: 2_000},
		{Index: 5, Path: "trailer.mp4", Length: 30_000_000},
	}

	got := SelectVideos(files)
	if len(got) != 2 {
		t.Fatalf("SelectVideos() returned %d files, want 2: %+v", len(got), got)
	}
	for i, want := range []string{"Show.S01E01.mkv", "Show.S01E02.mkv"} {
		if got[i].Path != want {
			t.Errorf("file %d = %q, want %q", i, got[i].Path, want)
		}
	}
}

// A short film named "sample" is still the feature: the name alone must not
// disqualify a file that is as big as everything else in the torrent.
func TestSelectVideosKeepsLargeSampleNamedFile(t *testing.T) {
	files := []FileInfo{
		{Index: 0, Path: "free-sample-movie.mkv", Length: 800_000_000},
		{Index: 1, Path: "extras.mkv", Length: 900_000_000},
	}
	if got := len(SelectVideos(files)); got != 2 {
		t.Fatalf("SelectVideos() = %d files, want 2 - a large file named sample is not a sample", got)
	}
}
