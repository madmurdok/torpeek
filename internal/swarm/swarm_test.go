package swarm

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// freePort asks the OS for a port nothing is listening on, then releases it -
// good enough for a test that immediately rebinds it itself; a real race
// against another process grabbing it first is not a concern here.
func freePort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

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

// TestListenPortIsHonoured is the swarm half of TOR-28's acceptance
// criterion: pin ListenPort and check the client actually ends up on that
// port, not a random one. Offline, like TestOpenPrivateTorrentStaysOffDHT -
// a .torrent file needs no network for metadata.
func TestListenPortIsHonoured(t *testing.T) {
	src, err := ParseSource(writeTorrentFile(t, false, ""))
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	port := freePort(t)

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false // no network wanted; the pin is what is under test
	cfg.ListenPort = port
	cfg.MetadataTimeout = 10 * time.Second

	session, _, err := Open(context.Background(), cfg, src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer session.Close()

	if got := session.ListenPort(); got != port {
		t.Errorf("session listens on %d, want the pinned %d", got, port)
	}
}

// TestPinnedListenPortFailsLoudlyWhenTaken is the other half: a managed host
// pinning a port expects a clash to be a startup error, not a silent fall
// back to some other port anacrolix happened to find free.
func TestPinnedListenPortFailsLoudlyWhenTaken(t *testing.T) {
	port := freePort(t)

	src1, err := ParseSource(writeTorrentFile(t, false, ""))
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.ListenPort = port
	cfg.MetadataTimeout = 10 * time.Second

	first, _, err := Open(context.Background(), cfg, src1)
	if err != nil {
		t.Fatalf("first session (pinning the port): %v", err)
	}
	defer first.Close()

	src2, err := ParseSource(writeTorrentFile(t, false, ""))
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	cfg2 := cfg
	cfg2.DataDir = t.TempDir()

	second, _, err := Open(context.Background(), cfg2, src2)
	if err == nil {
		second.Close()
		t.Fatalf("second session on the already-pinned port %d opened without error, want a bind failure", port)
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

func TestSelectNamesFilesByIndexAndPattern(t *testing.T) {
	files := []FileInfo{
		{Index: 2, Path: "Season 1/S01E01 - Pilot.mkv", Length: 900},
		{Index: 5, Path: "Season 1/S01E02 - Second.mkv", Length: 950},
		{Index: 8, Path: "Extras/Behind The Scenes.MP4", Length: 300},
	}

	indices := func(got []FileInfo) []int {
		out := make([]int, 0, len(got))
		for _, f := range got {
			out = append(out, f.Index)
		}
		return out
	}
	equal := func(a, b []int) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	for _, c := range []struct {
		name  string
		specs []string
		want  []int
	}{
		{"no specs keeps everything", nil, []int{2, 5, 8}},
		{"torrent index", []string{"5"}, []int{5}},
		{"substring", []string{"pilot"}, []int{2}},
		{"substring is case-insensitive", []string{"BEHIND"}, []int{8}},
		{"glob on the base name", []string{"*.mkv"}, []int{2, 5}},
		{"glob on the whole path", []string{"Extras/*"}, []int{8}},
		{"several specs keep torrent order", []string{"8", "pilot"}, []int{2, 8}},
		{"overlapping specs do not duplicate", []string{"2", "pilot", "*.mkv"}, []int{2, 5}},
		{"blank specs are ignored", []string{"", "  ", "5"}, []int{5}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Select(files, c.specs)
			if err != nil {
				t.Fatalf("Select(%v): %v", c.specs, err)
			}
			if !equal(indices(got), c.want) {
				t.Errorf("Select(%v) = %v, want %v", c.specs, indices(got), c.want)
			}
		})
	}
}

// TestSelectRefusesASpecThatMatchesNothing: an empty result and a run that
// found nothing worth taking look identical from outside, so the caller has to
// be told which happened - and told what it could have asked for.
func TestSelectRefusesASpecThatMatchesNothing(t *testing.T) {
	files := []FileInfo{
		{Index: 2, Path: "Season 1/S01E01 - Pilot.mkv"},
		{Index: 5, Path: "Season 1/S01E02 - Second.mkv"},
	}

	_, err := Select(files, []string{"pilot", "S01E09"})
	if err == nil {
		t.Fatal("Select accepted a spec matching nothing")
	}
	if !errors.Is(err, ErrNoFileMatch) {
		t.Errorf("error %v is not ErrNoFileMatch, so callers cannot classify it", err)
	}
	for _, want := range []string{"S01E09", "2:S01E01 - Pilot.mkv", "5:S01E02 - Second.mkv"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestWindowSizeRoundsToWholePieces: a window is a claim, so an intent in
// bytes becomes a claim in whole pieces, because pieces are what the swarm
// trades in. The floor matters most - a window under one piece was measured
// at 62.7s against 3.4s, since the decoder keeps returning for the rest of a
// piece already on its way.
func TestWindowSizeRoundsToWholePieces(t *testing.T) {
	const mib = 1 << 20

	for _, c := range []struct {
		name        string
		intent      int64
		pieceLength int64
		want        int64
	}{
		{"exactly one piece", 1 * mib, 1 * mib, 1 * mib},
		{"under a piece is raised to one", 512 << 10, 1 * mib, 1 * mib},
		{"far under a piece is still one", 64 << 10, 4 * mib, 4 * mib},
		{"rounded up to the next whole piece", 3 * mib, 2 * mib, 4 * mib},
		{"already whole is left alone", 6 * mib, 2 * mib, 6 * mib},
		{"an unknown piece length leaves the intent", 3 * mib, 0, 3 * mib},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := Profile{Window: c.intent}
			if got := p.WindowSize(c.pieceLength); got != c.want {
				t.Errorf("WindowSize(%d) = %d, want %d", c.pieceLength, got, c.want)
			}
		})
	}
}

// TestReadaheadSizeIsNotRoundedUp pins TOR-95's distinction: a window is a
// claim, so rounding it up to a whole piece costs nothing that was not
// already being paid, but a readahead is only a hint about how far ahead to
// prefetch, and rounding a hint up turns it into a claim nobody made.
//
// Before this fix, ReadaheadSize floored MinTraffic's 256 KiB readahead to a
// full 1 MiB piece via the same alignUp a window goes through, so every
// reader prefetched a whole piece past its read position. Because
// internal/bridge deliberately claims only the head of each range ffmpeg
// asks for (see torrentContent.Fetch), that manufactured prefetch reached
// past the claim - exactly where Window.Release's CancelPieces cannot follow
// it, since Release only cancels the pieces its own window claimed. Measured
// on a local seeder with 1 MiB pieces: 15 MiB of distinct claimed pieces
// downloaded 20.7-20.9 MiB with the floor applied, and a bit-stable 15.8 MiB
// once ReadaheadSize stopped rounding.
//
// If ReadaheadSize is ever made to round up again - say, by delegating to
// alignUp the way WindowSize does - this test must fail.
func TestReadaheadSizeIsNotRoundedUp(t *testing.T) {
	const mib = 1 << 20

	for _, c := range []struct {
		name        string
		intent      int64
		pieceLength int64
	}{
		{"MinTraffic's own numbers: 256 KiB readahead, 1 MiB pieces", 256 << 10, 1 * mib},
		{"far under a piece stays far under", 64 << 10, 4 * mib},
		{"between pieces stays unrounded", 3 * mib, 2 * mib},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := Profile{Readahead: c.intent}
			if got := p.ReadaheadSize(c.pieceLength); got != c.intent {
				t.Errorf("ReadaheadSize(%d) with pieceLength %d = %d, want the unrounded intent %d; "+
					"a readahead is a hint, not a claim, and must not go through alignUp",
					c.intent, c.pieceLength, got, c.intent)
			}
		})
	}
}

// TestProfilesDifferInWhatTheyClaim pins the trade the two profiles exist to
// make. The numbers themselves belong to TOR-44; what must not drift is the
// direction.
func TestProfilesDifferInWhatTheyClaim(t *testing.T) {
	const pieceLength = 1 << 20

	if MinTime.WindowSize(pieceLength) <= MinTraffic.WindowSize(pieceLength) {
		t.Errorf("min-time claims %d, min-traffic %d; the fast profile must claim more",
			MinTime.WindowSize(pieceLength), MinTraffic.WindowSize(pieceLength))
	}
	if MinTime.ReadaheadSize(pieceLength) <= MinTraffic.ReadaheadSize(pieceLength) {
		t.Errorf("min-time reads ahead %d, min-traffic %d; the fast profile must read further",
			MinTime.ReadaheadSize(pieceLength), MinTraffic.ReadaheadSize(pieceLength))
	}
	if !MinTime.Responsive || MinTraffic.Responsive {
		t.Error("min-time trades verification for latency, min-traffic does not")
	}
}
