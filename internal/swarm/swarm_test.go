package swarm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// freePort is a port nothing is listening on, on any of the four sockets a
// client binds. See torrenttest.FreePort for why the obvious one-line version
// is not enough - it is the difference between this package's port-pinning
// tests passing and failing about one run in three.
func freePort(t *testing.T) int {
	t.Helper()

	return torrenttest.FreePort(t)
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

// TestMagnetDisplayName is TOR-117's acceptance criterion for the one
// building block the web panel's provisional name is built on: a magnet's
// own dn=, and nothing else, with no network and no session - source.go's
// own doc comment on MagnetDisplayName.
func TestMagnetDisplayName(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		wantName string
		wantOK   bool
	}{
		{
			name:     "a magnet with dn=",
			source:   "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Sintel&tr=http%3A%2F%2Ftracker.invalid%2Fannounce",
			wantName: "Sintel", wantOK: true,
		},
		{
			// A real torrent name carries spaces and punctuation percent-
			// encoded in the URI - exactly what ParseMagnetUri exists to
			// undo, so this proves MagnetDisplayName rides on that rather
			// than doing its own string surgery.
			name:     "a magnet whose dn= is percent-encoded",
			source:   "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Sintel.2010.1080p",
			wantName: "Sintel.2010.1080p", wantOK: true,
		},
		{
			name:   "a magnet with no dn=",
			source: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&tr=http%3A%2F%2Ftracker.invalid%2Fannounce",
			wantOK: false,
		},
		{
			// A .torrent-sourced run has nothing here at all: a dn= is a
			// magnet-only convention, and a filesystem path is not a magnet
			// URI regardless of what it is named.
			name:   "a .torrent path, not a magnet",
			source: "/tmp/some-upload/movie.torrent",
			wantOK: false,
		},
		{
			name:   "not a magnet at all",
			source: "",
			wantOK: false,
		},
		{
			name:   "a magnet that fails to parse",
			source: "magnet:?xt=urn:btih:not-valid-hex&dn=Sintel",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := MagnetDisplayName(tc.source)
			if ok != tc.wantOK || got != tc.wantName {
				t.Fatalf("MagnetDisplayName(%q) = (%q, %v), want (%q, %v)", tc.source, got, ok, tc.wantName, tc.wantOK)
			}
		})
	}
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

// freePortSet asks the OS for n ports nothing is listening on and returns
// them as a PortSet, the way an operator would have typed the list their host
// allocated. They are distinct, and free on every socket a client binds - see
// torrenttest.FreePorts for why the second half is not a precaution.
func freePortSet(t *testing.T, n int) PortSet {
	t.Helper()

	spec := make([]string, 0, n)
	for _, port := range torrenttest.FreePorts(t, n) {
		spec = append(spec, strconv.Itoa(port))
	}

	set, err := ParsePortSet(strings.Join(spec, ","))
	if err != nil {
		t.Fatalf("parse the set just collected: %v", err)
	}
	return set
}

// TestTwoSessionsTakeTwoPortsFromTheSet is the thing that has to be true
// before more than one client can exist at all: hand one pool to two
// sessions and they end up on two different ports, both of them ports the
// operator actually allocated. Offline, like the pinned-port tests above - a
// .torrent file needs no network for metadata.
func TestTwoSessionsTakeTwoPortsFromTheSet(t *testing.T) {
	set := freePortSet(t, 2)
	pool := NewPortPool(set)

	open := func() *Session {
		t.Helper()
		src, err := ParseSource(writeTorrentFile(t, false, ""))
		if err != nil {
			t.Fatalf("parse source: %v", err)
		}
		cfg := DefaultConfig(t.TempDir())
		cfg.DHT = false
		cfg.MetadataTimeout = 10 * time.Second
		cfg.Ports = pool

		session, _, err := Open(context.Background(), cfg, src)
		if err != nil {
			t.Fatalf("open a session out of the set %s: %v", set, err)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}

	first, second := open(), open()

	allocated := map[int]bool{}
	for _, port := range set.Ports() {
		allocated[port] = true
	}
	for i, session := range []*Session{first, second} {
		if !allocated[session.ListenPort()] {
			t.Errorf("session %d listens on %d, which is not in the allocated set %s", i+1, session.ListenPort(), set)
		}
	}
	if first.ListenPort() == second.ListenPort() {
		t.Errorf("both sessions took port %d; two clients cannot share one", first.ListenPort())
	}
	if got := pool.Free(); got != 0 {
		t.Errorf("pool has %d ports free after two of two were taken, want 0", got)
	}
}

// TestOpenRefusesWhenTheSetIsTooSmall is the refusal the whole design turns
// on: a set with fewer ports than clients does not quietly produce a client
// on some other port, it produces an error saying the allocation ran out.
// A silent OS-assigned port here would be a port outside the range, which
// REQUIREMENTS.md 4.1 forbids outright.
func TestOpenRefusesWhenTheSetIsTooSmall(t *testing.T) {
	set := freePortSet(t, 1)
	pool := NewPortPool(set)

	src1, err := ParseSource(writeTorrentFile(t, false, ""))
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.MetadataTimeout = 10 * time.Second
	cfg.Ports = pool

	first, _, err := Open(context.Background(), cfg, src1)
	if err != nil {
		t.Fatalf("first session, taking the only port: %v", err)
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
		port := second.ListenPort()
		second.Close()
		t.Fatalf("second session opened on port %d out of a one-port set; the set is %s, so this port is either shared or outside the allocation", port, set)
	}
	if !errors.Is(err, ErrNoPortAvailable) {
		t.Fatalf("refusal is not ErrNoPortAvailable, so no caller can tell an exhausted allocation from a broken one: %v", err)
	}
	if !strings.Contains(err.Error(), set.String()) {
		t.Errorf("refusal does not name the allocated set %s, so it does not say what the limit is: %v", set, err)
	}
}

// TestClosingASessionReturnsItsPortToTheSet: a set of one is a set that can
// still serve any number of torrents one after another. Without this the
// allocation would shrink with every run until the process was out of ports.
//
// The loop runs more times than the bookkeeping strictly needs because
// rebinding the same pinned port over and over is the shape a one-port
// allocation actually has. It is not, on its own, what proves Close waits
// for the socket - see TestClosingASessionFreesItsPortBeforeItReturns for
// that, which is the test that fails when the wait is taken out.
func TestClosingASessionReturnsItsPortToTheSet(t *testing.T) {
	set := freePortSet(t, 1)
	pool := NewPortPool(set)
	port := set.Ports()[0]

	const cycles = 12
	for attempt := 1; attempt <= cycles; attempt++ {
		src, err := ParseSource(writeTorrentFile(t, false, ""))
		if err != nil {
			t.Fatalf("parse source: %v", err)
		}
		cfg := DefaultConfig(t.TempDir())
		cfg.DHT = false
		cfg.MetadataTimeout = 10 * time.Second
		cfg.Ports = pool

		session, _, err := Open(context.Background(), cfg, src)
		if err != nil {
			t.Fatalf("run %d of %d out of a one-port set: %v", attempt, cycles, err)
		}
		if got := session.ListenPort(); got != port {
			t.Errorf("run %d listens on %d, want the only allocated port %d", attempt, got, port)
		}
		if err := session.Close(); err != nil {
			t.Fatalf("close run %d: %v", attempt, err)
		}
		if free := pool.Free(); free != 1 {
			t.Fatalf("after run %d closed the pool has %d ports free, want 1", attempt, free)
		}
	}
}

// TestUnconfiguredPortsStillLetTheOSChoose is decision three, at the session
// level: with nothing allocated, several clients coexist on ports the OS
// picked, and nothing runs out. This is what keeps a laptop and this very
// test suite from needing a port range - and it stays a different state from
// the managed one, which PortPool.Managed reports.
func TestUnconfiguredPortsStillLetTheOSChoose(t *testing.T) {
	pool := NewPortPool(PortSet{})
	if pool.Managed() {
		t.Fatal("an unconfigured pool claims to be managing an allocation")
	}

	ports := map[int]bool{}
	for i := 0; i < 3; i++ {
		src, err := ParseSource(writeTorrentFile(t, false, ""))
		if err != nil {
			t.Fatalf("parse source: %v", err)
		}
		cfg := DefaultConfig(t.TempDir())
		cfg.DHT = false
		cfg.MetadataTimeout = 10 * time.Second
		cfg.Ports = pool

		session, _, err := Open(context.Background(), cfg, src)
		if err != nil {
			t.Fatalf("session %d with nothing configured: %v", i+1, err)
		}
		defer session.Close()

		port := session.ListenPort()
		if port == 0 {
			t.Fatalf("session %d reports port 0; the OS was supposed to choose a real one", i+1)
		}
		if ports[port] {
			t.Fatalf("session %d also got port %d", i+1, port)
		}
		ports[port] = true
	}
}

// TestOpenRefusesBothAPinnedPortAndAPool: the two ways of naming a port are
// alternatives, and a caller that sets both has a bug worth hearing about
// rather than a precedence rule to learn.
func TestOpenRefusesBothAPinnedPortAndAPool(t *testing.T) {
	src, err := ParseSource(writeTorrentFile(t, false, ""))
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	set := freePortSet(t, 1)
	pool := NewPortPool(set)

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.MetadataTimeout = 10 * time.Second
	cfg.ListenPort = freePort(t)
	cfg.Ports = pool

	session, _, err := Open(context.Background(), cfg, src)
	if err == nil {
		session.Close()
		t.Fatal("Open accepted both a pinned ListenPort and a pool")
	}
	if pool.Free() != 1 {
		t.Errorf("the refused Open kept a port: %d free of %d", pool.Free(), pool.Size())
	}
}

// TestClosingASessionFreesItsPortBeforeItReturns pins the guarantee a port
// pool depends on and that anacrolix does not give: once Close returns, the
// port is bindable, so whoever takes it out of the pool next is not racing
// the previous client's sockets.
//
// Client.Close waits (closeGroup.Wait) only for torrents and their storage.
// Its listening sockets go through `cl.onClose = append(cl.onClose, func() {
// go s.Close() })` - a goroutine nothing joins - so the port can still be
// held when Close returns. Measured on this machine: it was, in 1 of 30
// cycles, for a few hundred microseconds. Rare enough that reopening in a
// loop usually gets away with it, which is exactly why the guarantee is
// asserted directly here instead of being left to a race that mostly does
// not happen.
func TestClosingASessionFreesItsPortBeforeItReturns(t *testing.T) {
	set := freePortSet(t, 1)
	port := set.Ports()[0]

	const cycles = 30
	for i := 1; i <= cycles; i++ {
		src, err := ParseSource(writeTorrentFile(t, false, ""))
		if err != nil {
			t.Fatalf("parse source: %v", err)
		}
		cfg := DefaultConfig(t.TempDir())
		cfg.DHT = false
		cfg.MetadataTimeout = 10 * time.Second
		cfg.Ports = NewPortPool(set)

		session, _, err := Open(context.Background(), cfg, src)
		if err != nil {
			t.Fatalf("cycle %d of %d: %v", i, cycles, err)
		}
		if err := session.Close(); err != nil {
			t.Fatalf("cycle %d of %d, close: %v", i, cycles, err)
		}

		if !portFree(port) {
			t.Fatalf("cycle %d of %d: Close returned with port %d still bound, so the next client out of the pool would fail to start", i, cycles, port)
		}
	}
}

// bareMagnet is a magnet for an existing fixture carrying nothing but the
// infohash: no tr=, so there is no tracker to probe and the private flag
// cannot be settled before a client with DHT starts. That is the one source
// shape that reaches onlineRoute.blind, and it is exactly what
// torrenttest.Fixture.Magnet deliberately does not produce - that helper adds
// a dead loopback tracker precisely so a test stays off this route.
func bareMagnet(t *testing.T, torrentPath string) string {
	t.Helper()

	mi, err := metainfo.LoadFromFile(torrentPath)
	if err != nil {
		t.Fatalf("load fixture torrent: %v", err)
	}
	return "magnet:?xt=urn:btih:" + mi.HashInfoBytes().HexString()
}

// TestAPrivateTorrentIsRefusedByTheAddItself is where acceptance criterion 5
// stops being incidental (TOR-129).
//
// The client here is built by hand with DHT on and then offered a private
// .torrent - a combination routeFor would never produce, and that is the
// point. While one client held one torrent, "a private torrent's client has
// no DHT" was a fact about how the client was configured, and nothing had to
// enforce it. A client shared between torrents means the client's DHT is no
// longer this torrent's business, so the add is where the guarantee has to
// live. This test asks the question a wrong caller would: it puts the private
// torrent in front of the door and checks the door is shut.
//
// The public torrent at the end is what stops this passing for the wrong
// reason - a guard that refused everything would look identical.
func TestAPrivateTorrentIsRefusedByTheAddItself(t *testing.T) {
	withOfflineDHT(t)

	private, err := ParseSource(writeTorrentFile(t, true, "http://tracker.invalid/announce"))
	if err != nil {
		t.Fatalf("parse the private source: %v", err)
	}
	public, err := ParseSource(writeTorrentFile(t, false, "http://tracker.invalid/announce"))
	if err != nil {
		t.Fatalf("parse the public source: %v", err)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = true
	cfg.MetadataTimeout = 10 * time.Second

	s, err := newSession(cfg, true)
	if err != nil {
		t.Fatalf("start a client with DHT: %v", err)
	}
	defer s.Close()

	if !s.UsesDHT() {
		t.Fatal("no DHT server is running, so refusing a private torrent on this client would prove nothing")
	}

	if _, err := s.add(context.Background(), private, nil); !errors.Is(err, ErrPrivateOnDHTClient) {
		t.Fatalf("adding a private torrent to a DHT client returned %v, want ErrPrivateOnDHTClient", err)
	}
	if got := len(s.cl.Torrents()); got != 0 {
		t.Errorf("the client holds %d torrents after the refusal, want 0 - refusing BEFORE the add "+
			"is what keeps the infohash from being announced at all", got)
	}

	if _, err := s.add(context.Background(), public, nil); err != nil {
		t.Fatalf("a public torrent on the same client was refused too: %v", err)
	}
	if got := len(s.cl.Torrents()); got != 1 {
		t.Errorf("the client holds %d torrents after a public add, want 1", got)
	}
}

// TestABlindMagnetIsRefusedOnlyWhenItTurnsOutPrivate walks the decision
// onlineRoute.blind documents, on both arms.
//
// A magnet with no trackers has to reach the DHT to fetch the metadata that
// carries the flag it is being asked about; there is no ordering that avoids
// it. What is decided is what happens next, and the two arms here are the
// whole of it: a torrent that turns out public carries on, on the client that
// is already running; one that turns out private is refused outright, with
// nothing of it left attached to a client that announces.
//
// Both arms are offline - the DHT server has no starting nodes, and the
// metadata arrives over BEP 9 from a loopback seeder introduced directly.
func TestABlindMagnetIsRefusedOnlyWhenItTurnsOutPrivate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		private bool
	}{
		{name: "public carries on", private: false},
		{name: "private is refused", private: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withOfflineDHT(t)

			build := torrenttest.Build
			if tc.private {
				build = torrenttest.BuildPrivate
			}
			fixture := build(t, "movie.mkv", testPayloadSize, testPieceLength)
			seeder := fixture.StartSeeder(t)

			src, err := ParseSource(bareMagnet(t, fixture.TorrentPath))
			if err != nil {
				t.Fatalf("parse the bare magnet: %v", err)
			}
			if _, known := src.Privacy(); known {
				t.Fatal("the magnet settles the flag by itself, so this is not the path under test")
			}

			cfg := DefaultConfig(t.TempDir())
			cfg.DHT = true // without it the route is refused outright, before any of this
			cfg.MetadataTimeout = 30 * time.Second
			cfg.Peers = []string{seeder}

			if route := routeFor(cfg, src); !route.blind || !route.dht {
				t.Fatalf("route = %+v, want the blind route: DHT on, flag unread", route)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			session, tor, err := Open(ctx, cfg, src)
			if session != nil {
				defer session.Close()
			}

			if !tc.private {
				if err != nil {
					t.Fatalf("open a public bare magnet: %v", err)
				}
				if tor.Private() {
					t.Fatal("the public fixture reports itself private, so this arm tested nothing")
				}
				if !session.WentOnlineBlind() {
					t.Error("the session does not report going online blind, but the magnet had no trackers")
				}
				if !session.UsesDHT() {
					t.Error("no DHT is running, so this arm did not walk the blind route")
				}
				return
			}

			if !errors.Is(err, ErrPrivateOnDHTClient) {
				t.Fatalf("Open of a private bare magnet returned err=%v (session=%v, torrent=%v), "+
					"want ErrPrivateOnDHTClient", err, session != nil, tor != nil)
			}

			// Refused, and nothing of it left staged on the way out.
			hash, err := src.InfoHash()
			if err != nil {
				t.Fatalf("infohash: %v", err)
			}
			staged := filepath.Join(cfg.DataDir, hash.HexString())
			if _, err := os.Stat(staged); !os.IsNotExist(err) {
				t.Errorf("the refused torrent left %s behind (%v)", staged, err)
			}
		})
	}
}

// TestWebseedsAreDisabled guards a workaround that looks like a preference and
// would be deleted as one.
//
// anacrolix's updateWebseedRequests asserts that the webseed requests it
// collects from the client equal the same set recomputed per torrent, and
// panics when they differ - on its own timer goroutine, so no recover of ours
// can catch it. That never bit while one client held one torrent for one run;
// it killed 1.2.0's acceptance run inside criterion 1 once a single long-lived
// client began holding every public torrent (Pool). Upstream still ships the
// assertion, so this is not a line to remove after a pin bump without reading
// webseed-requesting.go first.
//
// Asserted on the config rather than by provoking the panic, because a test
// that provokes it takes the test binary down with it.
func TestWebseedsAreDisabled(t *testing.T) {
	var got *torrent.ClientConfig

	prev := tuneClientForTest
	tuneClientForTest = func(tc *torrent.ClientConfig) {
		got = tc
		if prev != nil {
			prev(tc)
		}
	}
	t.Cleanup(func() { tuneClientForTest = prev })

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false
	session, err := newSession(cfg, false)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	if got == nil {
		t.Fatal("the test hook never saw a client config")
	}
	if !got.DisableWebseeds {
		t.Error("DisableWebseeds is false: a torrent publishing an HTTP mirror can now " +
			"panic the whole process on anacrolix's webseed timer, taking every other " +
			"fetching torrent and the queue with it")
	}
}

// requireOneDirectoryPerTorrent asserts that the data dir holds exactly one
// entry per torrent, each a directory named by that torrent's infohash, and
// nothing else at all.
//
// It pins two separate things at once, which is why it is worth a helper.
//
// The first is the layout DiscardPieces removes by name. torpeek supplies its
// own TorrentDirMaker now (session.go's infoHashDir), because NewFileOpts is
// the only anacrolix constructor that takes a piece completion and the
// infohash path maker that used to come with NewFileByInfoHash is not
// exported. Owning that function means owning the risk of it drifting, and
// DiscardPieces' safety argument - the subtree belongs to one torrent -
// drifts with it.
//
// The second is TOR-155 itself, and this is the half that can fail in every
// build. A client that opens a PERSISTENT piece completion leaves its
// database in the top level of the data dir: `.torrent.bolt.db` for bbolt
// (CGO_ENABLED=0, the shipped build) and `.torrent.db` for sqlite (cgo on,
// a bare `go test`). Neither is an infohash, so either one fails this. That
// matters because the actual TOR-155 symptom - the WARN and the second's
// wait on the flock - only ever appears in the bbolt build, and a test that
// could only fail there would be silently vacuous in the build most people
// run.
func requireOneDirectoryPerTorrent(t *testing.T, dataDir string, infoHashes ...string) {
	t.Helper()

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("read the data dir: %v", err)
	}

	want := make(map[string]bool, len(infoHashes))
	for _, h := range infoHashes {
		want[h] = true
	}

	got := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("the data dir holds %q, which is not a torrent's directory; "+
				"a piece completion database here means a client opened the persistent "+
				"one (see pieceStorage)", e.Name())
			continue
		}
		if !want[e.Name()] {
			t.Errorf("the data dir holds a directory %q that is not any of these torrents' infohashes %v; "+
				"DiscardPieces removes <DataDir>/<infohash>/ by name and needs that shape exactly",
				e.Name(), infoHashes)
		}
		got[e.Name()] = true
	}
	for _, h := range infoHashes {
		if !got[h] {
			t.Errorf("the data dir has no directory for torrent %s", h)
		}
	}
}

// completionWarningsFor is the piece-completion warnings naming one data dir,
// and it is scoped to that dir for a reason worth reading before trusting
// either test that calls it.
//
// torrenttest's seeder leaves ClientConfig.DefaultStorage nil, so anacrolix
// builds it a storage.NewFile(cfg.DataDir) - persistent piece completion and
// all - and StartSeeder points a seeder at the PARENT of its fixture
// directory, which for two fixtures built in one test is the same t.TempDir()
// root. So any test running two seeders already warns once, from the second
// seeder, over the scaffolding's own directory. Measured: two StartSeeder
// calls and no torpeek client at all produce exactly one
// "couldn't open piece completion db dir=<test temp root> err=timeout".
//
// An unfiltered check would report that as torpeek's, and worse, would report
// it whether torpeek's own clients were fine or not. Scoping to the data dir
// under test is what makes a warning here mean what the test says it means.
// (The seeder's own second-long stall is real but is test scaffolding's cost,
// not the product's.)
func completionWarningsFor(logs *slogCapture, dataDir string) []string {
	var out []string
	for _, m := range logs.matching(pieceCompletionWarning) {
		if strings.Contains(m, " dir="+dataDir+" ") || strings.HasSuffix(m, " dir="+dataDir) {
			out = append(out, m)
		}
	}
	return out
}

// TestTwoSessionsAliveAtOnceOverOneDataDir is TOR-155 at the level the
// problem lives at: not two runs one after the other (completion_test.go's
// TestSessionsOverOneDataDirKeepPersistentPieceCompletion has that shape),
// but two clients up at the same time, both pointed at one data dir. Since
// TOR-128 that is the ordinary case rather than an edge one - a pool holds a
// long-lived shared client and starts more beside it.
//
// It is deliberately below the pool, on Open, because every client torpeek
// ever starts is built by newSession and any direct caller of Open has the
// same exposure. TestPoolClientsAliveAtOnceKeepTheirPieceCompletion covers
// the production topology on top of this.
//
// What it would catch, and did: with the storage built by
// storage.NewFileByInfoHash the second session's bbolt open waits out its
// one-second flock timeout, warns on the process-wide slog default, and
// silently degrades. Both halves are checked - the warning, and the database
// file the open leaves behind - because only the second can fail in a cgo
// build, where sqlite shares the file and never warns.
func TestTwoSessionsAliveAtOnceOverOneDataDir(t *testing.T) {
	logs := captureSlog(t)

	first := torrenttest.Build(t, "first.mkv", poolPayloadSize, poolPieceLength)
	second := torrenttest.Build(t, "second.mkv", poolPayloadSize, poolPieceLength)

	dataDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const readLength = 64 << 10

	open := func(f torrenttest.Fixture) (*Session, *Torrent) {
		t.Helper()

		src, err := ParseSource(f.TorrentPath)
		if err != nil {
			t.Fatalf("parse source: %v", err)
		}
		cfg := DefaultConfig(dataDir)
		cfg.DHT = false
		cfg.MetadataTimeout = 20 * time.Second
		cfg.Peers = []string{f.StartSeeder(t)}

		s, tor, err := Open(ctx, cfg, src)
		if err != nil {
			t.Fatalf("open %s: %v", f.FileName, err)
		}
		return s, tor
	}

	// Both up at the same time, and both stay up: the second is opened
	// before the first is closed, which is the whole point.
	firstSession, firstTorrent := open(first)
	defer firstSession.Close()
	secondSession, secondTorrent := open(second)
	defer secondSession.Close()

	for _, c := range []struct {
		name    string
		torrent *Torrent
		want    []byte
	}{
		{first.FileName, firstTorrent, first.Payload[:readLength]},
		{second.FileName, secondTorrent, second.Payload[:readLength]},
	} {
		got, err := c.torrent.ReadRange(ctx, 0, 0, readLength, MinTraffic)
		if err != nil {
			t.Fatalf("read %s while both clients are up: %v", c.name, err)
		}
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s read back %d wrong bytes while both clients are up", c.name, len(got))
		}
	}

	if got := completionWarningsFor(logs, dataDir); len(got) != 0 {
		t.Errorf("two clients over one data dir produced %d piece completion warning(s): %q",
			len(got), got)
	}
	requireOneDirectoryPerTorrent(t, dataDir, firstTorrent.InfoHash(), secondTorrent.InfoHash())
}
