package swarm

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// The message anacrolix's storage/piece-completion.go emits when the
// persistent piece completion could not be opened and it silently fell back
// to an in-memory one. It goes through the process-wide slog default, not the
// per-client logger, which is why a test has to catch it there - and why
// torpeek's per-client anacrolix/log filter never hid it from a terminal.
//
// Since TOR-155 torpeek does not ask for the persistent completion at all -
// pieceStorage hands every client an in-memory one deliberately, and says
// there what that was measured to cost and to save. So this message must
// never again appear for a torpeek data dir, and if it does, something has
// put a client back on a database that is one file per data DIRECTORY, which
// is the whole bug.
//
// The two tests here cover the two SEQUENTIAL client shapes TOR-59 was about:
// one client following another over one data dir. Two clients alive AT ONCE -
// the ordinary case since TOR-128, and what TOR-155 was actually about - is
// covered by TestTwoSessionsAliveAtOnceOverOneDataDir and
// TestPoolClientsAliveAtOnceKeepTheirPieceCompletion.
//
// Each test checks three separable things. The scoped log check says the
// library did not complain about our directory (completionWarningsFor
// explains why the scoping is load-bearing rather than tidiness). The
// client-side read-back says the completion the client actually got was
// working, which is the half a log line cannot show. And
// requireOneDirectoryPerTorrent says no persistent database was created at
// all - the half that can also fail in a cgo build, where sqlite shares its
// file and never warns.
const pieceCompletionWarning = "couldn't open piece completion db"

// slogCapture stands in for the process-wide slog default and keeps every
// record it is handed, so a test can ask what the library said instead of
// hoping to see it on stderr.
type slogCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (c *slogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *slogCapture) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" ")
		b.WriteString(a.String())
		return true
	})
	c.mu.Lock()
	c.msgs = append(c.msgs, b.String())
	c.mu.Unlock()
	return nil
}

func (c *slogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *slogCapture) WithGroup(string) slog.Handler      { return c }

func (c *slogCapture) matching(substr string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, m := range c.msgs {
		if strings.Contains(m, substr) {
			out = append(out, m)
		}
	}
	return out
}

// captureSlog swaps the process-wide slog default for the test's duration.
// Tests in this package do not run in parallel (they share
// tuneClientForTest), so nothing else can observe the swap.
func captureSlog(t *testing.T) *slogCapture {
	t.Helper()
	prev := slog.Default()
	c := &slogCapture{}
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// pieceCompleteInClient asks the LIVE client whether it holds a piece, which
// reads the completion its storage actually handed it rather than any file on
// disk.
//
// This is what replaced reading the answer back out of the persistent
// database after Close (TOR-155): there is no database to read now, and there
// was never much point in it - what a client needs is completion that works
// while it is running, and that is exactly what this asks. It is also a
// sharper question than the old one, because it fails for any broken
// completion rather than only for a missing persistent one.
func pieceCompleteInClient(t *testing.T, tor *Torrent, piece int) bool {
	t.Helper()

	return tor.t.Piece(piece).State().Complete
}

// TestPublicMagnetRestartKeepsWorkingPieceCompletion is TOR-59's shape: a
// public magnet makes Open close its DHT-less client and start a second one
// over the same data dir. The second one has to get working piece completion,
// not a warning and a silent fallback.
func TestPublicMagnetRestartKeepsWorkingPieceCompletion(t *testing.T) {
	withOfflineDHT(t)
	logs := captureSlog(t)

	fixture := torrenttest.Build(t, "movie.mkv", testPayloadSize, testPieceLength)
	seederAddr := fixture.StartSeeder(t)

	src, err := ParseSource(fixture.Magnet(t))
	if err != nil {
		t.Fatalf("parse magnet: %v", err)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = true
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
		t.Fatal("the session has DHT off, so this run never took the restart under test")
	}

	const (
		readOffset = 4 << 20
		readLength = 64 << 10
	)
	if _, err := tor.ReadRange(ctx, 0, readOffset, readLength, MinTraffic); err != nil {
		t.Fatalf("read from the restarted session: %v", err)
	}
	infoHash := tor.InfoHash()

	if piece := readOffset / testPieceLength; !pieceCompleteInClient(t, tor, piece) {
		t.Errorf("the restarted client does not hold piece %d, which it has just read; "+
			"its piece completion is not working", piece)
	}
	if got := completionWarningsFor(logs, cfg.DataDir); len(got) != 0 {
		t.Errorf("the restart produced %d piece completion warning(s): %q", len(got), got)
	}
	requireOneDirectoryPerTorrent(t, cfg.DataDir, infoHash)

	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestSessionsOverOneDataDirKeepWorkingPieceCompletion is the other shape the
// warning was seen in: two runs, one after the other, over one data dir in
// one process - what cancel-and-resume does.
func TestSessionsOverOneDataDirKeepWorkingPieceCompletion(t *testing.T) {
	logs := captureSlog(t)

	fixture := torrenttest.Build(t, "movie.mkv", testPayloadSize, testPieceLength)
	seederAddr := fixture.StartSeeder(t)

	src, err := ParseSource(fixture.TorrentPath)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false
	cfg.MetadataTimeout = 30 * time.Second
	cfg.Peers = []string{seederAddr}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const (
		readLength = 64 << 10
		runs       = 2
	)
	var infoHash string
	for run := 0; run < runs; run++ {
		session, tor, err := Open(ctx, cfg, src)
		if err != nil {
			t.Fatalf("run %d: open: %v", run, err)
		}
		readOffset := int64(run) * 4 << 20
		if _, err := tor.ReadRange(ctx, 0, readOffset, readLength, MinTraffic); err != nil {
			session.Close()
			t.Fatalf("run %d: read: %v", run, err)
		}
		infoHash = tor.InfoHash()
		// While this run's client is still up, because that is the only
		// moment its own completion exists to be asked about.
		if piece := int(readOffset / testPieceLength); !pieceCompleteInClient(t, tor, piece) {
			session.Close()
			t.Fatalf("run %d: the client does not hold piece %d, which it has just read; "+
				"its piece completion is not working", run, piece)
		}
		if err := session.Close(); err != nil {
			t.Fatalf("run %d: close: %v", run, err)
		}
	}

	if got := completionWarningsFor(logs, cfg.DataDir); len(got) != 0 {
		t.Errorf("%d sequential runs produced %d piece completion warning(s): %q", runs, len(got), got)
	}
	requireOneDirectoryPerTorrent(t, cfg.DataDir, infoHash)
}
