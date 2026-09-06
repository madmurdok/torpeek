package swarm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/torrenttest"
)

// poolPayloadSize is deliberately smaller than fetch_test's: these tests care
// about how many clients and ports are involved, not about window arithmetic,
// and four of them run a pair of seeders each.
const (
	poolPayloadSize = 2 << 20   // 2 MiB
	poolPieceLength = 256 << 10 // 8 pieces
)

func poolSource(t *testing.T, path string) Source {
	t.Helper()

	src, err := ParseSource(path)
	if err != nil {
		t.Fatalf("parse source %s: %v", path, err)
	}
	return src
}

// oneManagedPort is a port set holding exactly one port. It is the sharpest
// statement of what a pool is for: however many public torrents there are,
// they cost one port between them, so a set of one is enough for all of them.
func oneManagedPort(t *testing.T) (PortSet, int) {
	t.Helper()

	port := freePort(t)
	set, err := ParsePortSet(strconv.Itoa(port))
	if err != nil {
		t.Fatalf("parse port set: %v", err)
	}
	return set, port
}

func poolConfig(t *testing.T) Config {
	t.Helper()

	cfg := DefaultConfig(t.TempDir())
	cfg.DHT = false // stay entirely offline
	cfg.MetadataTimeout = 20 * time.Second
	return cfg
}

// TestPoolHoldsTwoPublicTorrentsInOneClientOnOnePort is the acceptance test
// for TOR-128: two public torrents fetching at the same time, out of one
// client, on one port - against a loopback swarm rather than by argument.
//
// The port set holds a single port on purpose. Before the pool, each torrent
// took a client and so a port of its own, and the second attach here would
// have been refused with ErrNoPortAvailable rather than merely landing
// somewhere else - which is what makes this test able to fail.
func TestPoolHoldsTwoPublicTorrentsInOneClientOnOnePort(t *testing.T) {
	first := torrenttest.Build(t, "one.mkv", poolPayloadSize, poolPieceLength)
	second := torrenttest.Build(t, "two.mkv", poolPayloadSize, poolPieceLength)
	firstSeeder := first.StartSeeder(t)
	secondSeeder := second.StartSeeder(t)

	set, port := oneManagedPort(t)
	ports := NewPortPool(set)

	cfg := poolConfig(t)
	cfg.Ports = ports

	pool := NewPool(cfg)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	a, err := pool.Attach(ctx, poolSource(t, first.TorrentPath), firstSeeder)
	if err != nil {
		t.Fatalf("attach the first torrent: %v", err)
	}
	defer a.Detach()

	b, err := pool.Attach(ctx, poolSource(t, second.TorrentPath), secondSeeder)
	if err != nil {
		t.Fatalf("attach the second torrent: %v", err)
	}
	defer b.Detach()

	if !a.Pooled() || !b.Pooled() {
		t.Fatalf("Pooled() = %v and %v, want both in the shared client", a.Pooled(), b.Pooled())
	}
	if got := len(pool.sess.cl.Torrents()); got != 2 {
		t.Errorf("the shared client holds %d torrents, want 2", got)
	}
	if a.ListenPort() != port || b.ListenPort() != port {
		t.Errorf("ports are %d and %d, want both on the one allocated port %d",
			a.ListenPort(), b.ListenPort(), port)
	}
	if free := ports.Free(); free != 0 {
		t.Errorf("%d of 1 allocated port is free while two torrents are attached, want 0", free)
	}

	// Both fetch at the same time, each from its own seeder, and each gets
	// back its own payload - the part that would still be true if they were
	// merely taking turns is not the part being claimed.
	const (
		readOffset = 1 << 20
		readLength = 64 << 10
	)

	type result struct {
		got  []byte
		want []byte
		err  error
	}
	results := make([]result, 2)

	var wg sync.WaitGroup
	for i, pair := range []struct {
		att     *Attachment
		fixture torrenttest.Fixture
	}{{a, first}, {b, second}} {
		wg.Add(1)
		go func(i int, att *Attachment, want []byte) {
			defer wg.Done()
			got, err := att.Torrent().ReadRange(ctx, 0, readOffset, readLength, MinTraffic)
			results[i] = result{got: got, want: want[readOffset : readOffset+readLength], err: err}
		}(i, pair.att, pair.fixture.Payload)
	}
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Fatalf("torrent %d read: %v", i, r.err)
		}
		if !bytes.Equal(r.got, r.want) {
			t.Errorf("torrent %d read the wrong bytes", i)
		}
	}

	t.Logf("two torrents, one client, port %d, %d ports allocated", port, ports.Size())
}

// TestDetachingOneTorrentLeavesTheClientAndItsSiblingUp: dropping a torrent
// out of a shared client is not closing the client.
func TestDetachingOneTorrentLeavesTheClientAndItsSiblingUp(t *testing.T) {
	first := torrenttest.Build(t, "one.mkv", poolPayloadSize, poolPieceLength)
	second := torrenttest.Build(t, "two.mkv", poolPayloadSize, poolPieceLength)
	firstSeeder := first.StartSeeder(t)
	secondSeeder := second.StartSeeder(t)

	set, _ := oneManagedPort(t)
	ports := NewPortPool(set)

	cfg := poolConfig(t)
	cfg.Ports = ports
	dataDir := cfg.DataDir

	pool := NewPool(cfg)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	a, err := pool.Attach(ctx, poolSource(t, first.TorrentPath), firstSeeder)
	if err != nil {
		t.Fatalf("attach the first torrent: %v", err)
	}
	b, err := pool.Attach(ctx, poolSource(t, second.TorrentPath), secondSeeder)
	if err != nil {
		t.Fatalf("attach the second torrent: %v", err)
	}
	defer b.Detach()

	// Both hold pieces on disk, so there is something for a detach to get
	// wrong about a sibling.
	for i, att := range []*Attachment{a, b} {
		if _, err := att.Torrent().ReadRange(ctx, 0, 0, 64<<10, MinTraffic); err != nil {
			t.Fatalf("torrent %d read: %v", i, err)
		}
	}

	goneHash, keptHash := a.Torrent().InfoHash(), b.Torrent().InfoHash()
	port := pool.ListenPort()

	if err := a.Detach(); err != nil {
		t.Fatalf("detach the first torrent: %v", err)
	}

	if !pool.Up() {
		t.Fatal("detaching one torrent took the shared client down")
	}
	if got := pool.ListenPort(); got != port {
		t.Errorf("the client is on port %d after a detach, was %d", got, port)
	}
	if got := len(pool.sess.cl.Torrents()); got != 1 {
		t.Errorf("the shared client holds %d torrents after one detach, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(dataDir, goneHash)); !os.IsNotExist(err) {
		t.Errorf("the detached torrent's pieces are still at %s (%v)", filepath.Join(dataDir, goneHash), err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, keptHash)); err != nil {
		t.Errorf("the sibling's pieces were taken with it: %v", err)
	}

	// The sibling is not merely still listed - it still fetches.
	got, err := b.Torrent().ReadRange(ctx, 0, 1<<20, 64<<10, MinTraffic)
	if err != nil {
		t.Fatalf("the sibling stopped fetching after its neighbour detached: %v", err)
	}
	if want := second.Payload[1<<20 : 1<<20+64<<10]; !bytes.Equal(got, want) {
		t.Error("the sibling read the wrong bytes after its neighbour detached")
	}

	// Detaching is not what hands the port back: the client outlives the last
	// torrent and belongs to whoever built the pool.
	if err := b.Detach(); err != nil {
		t.Fatalf("detach the second torrent: %v", err)
	}
	if !pool.Up() {
		t.Error("the client went down with the last torrent; only Close may take it down")
	}
	if free := ports.Free(); free != 0 {
		t.Errorf("%d ports are free while the client is still up, want 0", free)
	}

	if err := pool.Close(); err != nil {
		t.Fatalf("close the pool: %v", err)
	}
	if pool.Up() {
		t.Error("the client is still up after Close")
	}
	if free := ports.Free(); free != 1 {
		t.Errorf("%d of 1 port came back after Close, want 1", free)
	}
}

// TestPrivateTorrentNeverJoinsThePool: the shared client has whatever DHT
// setting the pool was built with and holds public torrents from every run, so
// a private torrent cannot be in it. It gets a client of its own, exactly as
// it did before the pool existed.
func TestPrivateTorrentNeverJoinsThePool(t *testing.T) {
	public := torrenttest.Build(t, "public.mkv", poolPayloadSize, poolPieceLength)
	private := torrenttest.BuildPrivate(t, "private.mkv", poolPayloadSize, poolPieceLength)

	// A DHT that genuinely runs but has nowhere to bootstrap to: without it
	// the shared client would have no DHT for a private torrent to have to
	// stay out of, and the test would pass on a technicality.
	withOfflineDHT(t)

	cfg := poolConfig(t)
	cfg.DHT = true // the setting a private torrent has to override

	pool := NewPool(cfg)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	open, err := pool.Attach(ctx, poolSource(t, public.TorrentPath))
	if err != nil {
		t.Fatalf("attach the public torrent: %v", err)
	}
	defer open.Detach()

	if !open.Pooled() {
		t.Fatal("a .torrent stating private=false did not join the shared client")
	}
	if !pool.UsesDHT() {
		t.Fatal("the shared public client has no DHT, so this test proves nothing about keeping a private torrent out of it")
	}

	shut, err := pool.Attach(ctx, poolSource(t, private.TorrentPath))
	if err != nil {
		t.Fatalf("attach the private torrent: %v", err)
	}
	defer shut.Detach()

	if shut.Pooled() {
		t.Fatal("a private torrent joined the shared public client")
	}
	if shut.Torrent().Private() != true {
		t.Fatal("the fixture is not private, so this test proves nothing")
	}
	if shut.sess.UsesDHT() {
		t.Error("the private torrent's own client has DHT running")
	}
	if shut.ListenPort() == open.ListenPort() {
		t.Errorf("both torrents are on port %d; a private torrent needs a client, and so a port, of its own",
			shut.ListenPort())
	}
}

// TestPoolRefusesTheSameTorrentTwice: two attachments over one infohash would
// share one anacrolix torrent - one set of stats to meter a budget off, one
// piece subtree to discard - so the second is refused by name rather than
// silently entangled with the first.
func TestPoolRefusesTheSameTorrentTwice(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mkv", poolPayloadSize, poolPieceLength)

	pool := NewPool(poolConfig(t))
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	first, err := pool.Attach(ctx, poolSource(t, fixture.TorrentPath))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, err := pool.Attach(ctx, poolSource(t, fixture.TorrentPath)); !errors.Is(err, ErrTorrentBusy) {
		t.Fatalf("second attach returned %v, want ErrTorrentBusy", err)
	}

	// Refused, not consumed: letting the first one go frees the torrent.
	if err := first.Detach(); err != nil {
		t.Fatalf("detach: %v", err)
	}
	again, err := pool.Attach(ctx, poolSource(t, fixture.TorrentPath))
	if err != nil {
		t.Fatalf("attach after detach: %v", err)
	}
	again.Detach()
}

// TestAFailedAttachLeavesThePoolServingEverythingElse: a torrent that cannot
// be brought online is one torrent's failure, not the client's.
func TestAFailedAttachLeavesThePoolServingEverythingElse(t *testing.T) {
	fixture := torrenttest.Build(t, "movie.mkv", poolPayloadSize, poolPieceLength)
	seeder := fixture.StartSeeder(t)

	set, port := oneManagedPort(t)
	ports := NewPortPool(set)

	cfg := poolConfig(t)
	cfg.Ports = ports
	// Short, because the failing attach below is a metadata wait that has to
	// run out: nobody will ever answer for that magnet.
	cfg.MetadataTimeout = 2 * time.Second

	pool := NewPool(cfg)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	held, err := pool.Attach(ctx, poolSource(t, fixture.TorrentPath), seeder)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer held.Detach()

	// A magnet nobody can answer: DHT is off and its only tracker is a dead
	// loopback address, so this waits out MetadataTimeout and fails. It takes
	// a client of its own to do it, which on a one-port set means it cannot
	// even get that far - either way the failure is its own.
	orphan := torrenttest.Build(t, "orphan.mkv", poolPayloadSize, poolPieceLength)
	_, err = pool.Attach(ctx, poolSource(t, orphan.Magnet(t)))
	if err == nil {
		t.Fatal("attaching an unanswerable magnet succeeded")
	}
	if !errors.Is(err, ErrNoMetadata) && !errors.Is(err, ErrNoPortAvailable) {
		t.Fatalf("attach failed with %v, want ErrNoMetadata or ErrNoPortAvailable", err)
	}

	if !pool.Up() {
		t.Fatal("a failed attach took the shared client down")
	}
	if got := pool.ListenPort(); got != port {
		t.Errorf("the client is on port %d after a failed attach, want %d", got, port)
	}
	if got := pool.Attached(); got != 1 {
		t.Errorf("%d torrents are attached after a failure, want 1", got)
	}

	got, err := held.Torrent().ReadRange(ctx, 0, 1<<20, 64<<10, MinTraffic)
	if err != nil {
		t.Fatalf("the surviving torrent stopped fetching: %v", err)
	}
	if want := fixture.Payload[1<<20 : 1<<20+64<<10]; !bytes.Equal(got, want) {
		t.Error("the surviving torrent read the wrong bytes")
	}
}

// TestPoolKeepsTheTrafficOfEveryTorrentItHasLetGo is the measurement half of
// TOR-131: what a client-wide traffic roof is read from.
//
// A roof could be summed from the live torrents' own Downloaded figures
// instead, and this is the test that says why it must not be. Both torrents
// here are detached before the last read - one out of the shared client, one a
// private torrent that had a client of its own and was closed with it - and
// after that the sum over live torrents is zero, because there are none.
// Pool.Downloaded still reports everything they received. Those bytes crossed
// the person's link; a quota that gave them back when the work finished would
// bound nothing at all (REQUIREMENTS.md 2.6).
func TestPoolKeepsTheTrafficOfEveryTorrentItHasLetGo(t *testing.T) {
	public := torrenttest.Build(t, "public.mkv", poolPayloadSize, poolPieceLength)
	private := torrenttest.BuildPrivate(t, "private.mkv", poolPayloadSize, poolPieceLength)
	publicSeeder := public.StartSeeder(t)
	privateSeeder := private.StartSeeder(t)

	pool := NewPool(poolConfig(t))
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if got := pool.Downloaded(); got != 0 {
		t.Fatalf("a pool that has attached nothing has received %d bytes", got)
	}

	const (
		readOffset = 1 << 20
		readLength = 256 << 10
	)

	var spent int64
	for _, c := range []struct {
		name   string
		path   string
		seeder string
		pooled bool
	}{
		{"public", public.TorrentPath, publicSeeder, true},
		{"private", private.TorrentPath, privateSeeder, false},
	} {
		att, err := pool.Attach(ctx, poolSource(t, c.path), c.seeder)
		if err != nil {
			t.Fatalf("attach the %s torrent: %v", c.name, err)
		}
		if att.Pooled() != c.pooled {
			t.Fatalf("the %s torrent's Pooled() is %v; this test needs one torrent on the shared "+
				"client and one on a client of its own, or it only covers half the accounting",
				c.name, att.Pooled())
		}

		if _, err := att.Torrent().ReadRange(ctx, 0, readOffset, readLength, MinTraffic); err != nil {
			t.Fatalf("read from the %s torrent: %v", c.name, err)
		}

		got := att.Torrent().Downloaded()
		if got <= 0 {
			t.Fatalf("the %s torrent reports %d bytes received after a %d byte read",
				c.name, got, readLength)
		}
		spent += got

		if err := att.Detach(); err != nil {
			t.Fatalf("detach the %s torrent: %v", c.name, err)
		}
	}

	if got := pool.Attached(); got != 0 {
		t.Fatalf("%d torrents are still attached, so nothing here is about letting go", got)
	}

	// The claim, in one line: everything both of them received, after both of
	// them are gone and one of their clients has been closed.
	if got := pool.Downloaded(); got < spent {
		t.Errorf("the pool reports %d bytes received; the two torrents received %d between them "+
			"before they were detached, and detaching does not give traffic back", got, spent)
	}

	t.Logf("two torrents received %d bytes and were let go; the pool still reports %d",
		spent, pool.Downloaded())
}
