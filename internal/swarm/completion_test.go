package swarm

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// The message anacrolix's storage/piece-completion.go emits when the
// persistent piece completion could not be opened and it silently fell back
// to an in-memory one (TOR-59). It goes through the process-wide slog default,
// not the per-client logger, which is why a test has to catch it there - and
// why torpeek's per-client log filter never hid it from a terminal.
//
// The two tests below reproduce the warning one-for-one when Session.Close
// stops closing its storage (a leaked storage holds the bbolt flock for the
// life of the process, and the next opener times out after a second), and
// never with it - 126 runs, 120 of them at load averages between 84 and 265,
// produced none. The log check says the library did not complain; the
// read-back after Close says what the client actually used, which is the half
// a log line cannot show.
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

// requireExclusiveCompletionDB skips a test whose whole premise is that the
// build's default piece completion database refuses a second opener while
// one is held. That is bbolt, which anacrolix uses only when cgo is off
// (storage/default-dir-piece-completion-boltdb.go); with cgo on it uses
// sqlite, which happily shares the file, and the failure these tests exist
// to catch cannot happen at all - a pass there would say nothing. The
// shipped binary is built with CGO_ENABLED=0 (see the Makefile), so that is
// the build the tests mean.
func requireExclusiveCompletionDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	first, err := storage.NewDefaultPieceCompletionForDir(dir)
	if err != nil {
		t.Fatalf("open the default piece completion in an empty dir: %v", err)
	}
	defer first.Close()
	second, err := storage.NewDefaultPieceCompletionForDir(dir)
	if err == nil {
		second.Close()
		t.Skip("this build's default piece completion shares its file between openers; " +
			"the exclusive-lock failure under test needs the CGO_ENABLED=0 build (make check)")
	}
}

// completionOnDisk opens the data dir's persistent piece completion after the
// sessions using it have closed, and reads back one piece. If a session was
// still holding it the open fails; if a session had fallen back to in-memory
// bookkeeping the piece reads back as unknown.
func completionOnDisk(t *testing.T, dataDir, infoHash string, piece int) storage.Completion {
	t.Helper()
	pc, err := storage.NewDefaultPieceCompletionForDir(dataDir)
	if err != nil {
		t.Fatalf("open the piece completion db after every session closed: %v - something still holds it", err)
	}
	defer pc.Close()
	c, err := pc.Get(metainfo.PieceKey{InfoHash: metainfo.NewHashFromHex(infoHash), Index: piece})
	if err != nil {
		t.Fatalf("read piece %d back from the completion db: %v", piece, err)
	}
	return c
}

// TestPublicMagnetRestartKeepsPersistentPieceCompletion is TOR-59's shape: a
// public magnet makes Open close its DHT-less client and start a second one
// over the same data dir. The second one's storage has to get the persistent
// piece completion, not a warning and an in-memory fallback.
func TestPublicMagnetRestartKeepsPersistentPieceCompletion(t *testing.T) {
	requireExclusiveCompletionDB(t)
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

	if got := logs.matching(pieceCompletionWarning); len(got) != 0 {
		t.Errorf("the restart produced %d piece completion warning(s): %q", len(got), got)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	piece := readOffset / testPieceLength
	if c := completionOnDisk(t, cfg.DataDir, infoHash, piece); !c.Ok || !c.Complete {
		t.Errorf("piece %d reads back as %+v after the run; the restarted client was not using the persistent completion", piece, c)
	}
}

// TestSessionsOverOneDataDirKeepPersistentPieceCompletion is the other shape
// the warning was seen in: two runs, one after the other, over one data
// dir in one process - what cancel-and-resume does.
func TestSessionsOverOneDataDirKeepPersistentPieceCompletion(t *testing.T) {
	requireExclusiveCompletionDB(t)
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
		if err := session.Close(); err != nil {
			t.Fatalf("run %d: close: %v", run, err)
		}
	}

	if got := logs.matching(pieceCompletionWarning); len(got) != 0 {
		t.Errorf("%d sequential runs produced %d piece completion warning(s): %q", runs, len(got), got)
	}
	for run := 0; run < runs; run++ {
		piece := run * (4 << 20) / testPieceLength
		if c := completionOnDisk(t, cfg.DataDir, infoHash, piece); !c.Ok || !c.Complete {
			t.Errorf("run %d's piece %d reads back as %+v; that run was not using the persistent completion", run, piece, c)
		}
	}
}
