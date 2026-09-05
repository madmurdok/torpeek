// Package swarm wraps the BitTorrent session: adding a magnet or .torrent,
// fetching arbitrary piece ranges by priority, reading swarm availability off
// peer bitfields, and accounting the run's time and traffic budget.
//
// It is the only package that imports anacrolix/torrent.
//
// Requirements: sections 2.1, 2.4, 2.6 and 4.
package swarm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	alog "github.com/anacrolix/log"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// Config holds the session settings torpeek cares about. Defaults follow
// REQUIREMENTS.md section 7.
type Config struct {
	// DataDir stages fetched pieces. Raw pieces are dropped after a run;
	// only results are cached.
	DataDir string

	// Upload serves already-held pieces back to the swarm while a run is
	// active. On by default: peers reciprocate, which helps min-time.
	Upload bool

	// DHT allows the distributed hash table and PEX. A torrent carrying the
	// private flag overrides this to off, whatever the value here.
	DHT bool

	// ListenPort is the BitTorrent port for this one client. anacrolix binds
	// TCP, uTP and (when DHT is on) the DHT server to this same port -
	// pinning it pins all three, which is what section 4.1's "no port
	// outside the allocated range" actually requires. Zero lets the OS choose
	// one port for all of them, which is fine locally but not on a managed
	// host with a fixed range, so callers there must set it. A pinned port
	// already taken is a startup error, not silently retried on a different
	// one - anacrolix only falls back to another port when ListenPort is
	// zero.
	//
	// Set this when the caller has already decided which port this client
	// gets. A caller running several clients sets Ports instead and leaves
	// this at zero; setting both is an error rather than a precedence rule,
	// because there is no reading of "here is a port, and also here is where
	// to get one" that is not somebody's mistake.
	ListenPort int

	// Ports, when set, is where this client's ListenPort comes from: Open
	// leases one port for the client's whole life and Close hands it back.
	//
	// It exists because a client is the unit that owns a port - DHT and PEX
	// are client-wide switches, so a private torrent cannot share a client
	// with a public one, and every extra client is another port out of the
	// allocation. One pool shared across a process is what keeps two clients
	// from asking for the same port, and what makes running out of ports a
	// stated refusal (ErrNoPortAvailable) instead of a silent fall back to an
	// OS-assigned port outside the allocated range.
	//
	// Nil means the caller is managing the port itself through ListenPort,
	// which is every single-client caller and every test that pins or
	// ignores the port.
	Ports *PortPool

	// MetadataTimeout bounds the wait for metadata.
	MetadataTimeout time.Duration

	// Peers are addresses to contact directly, for a swarm no tracker or DHT
	// will hand over - a known seedbox, or a test's own seeder.
	Peers []string
}

// DefaultConfig returns the session defaults for a run staging data in dataDir.
func DefaultConfig(dataDir string) Config {
	return Config{
		DataDir:         dataDir,
		Upload:          true,
		DHT:             true,
		MetadataTimeout: 60 * time.Second,
	}
}

const defaultMetadataTimeout = 60 * time.Second

var (
	// ErrNoMetadata means the swarm never sent us the torrent's info.
	ErrNoMetadata = errors.New("metadata not received")
	// ErrPrivacyUnresolvable means the only way to fetch metadata would be
	// DHT, which we refuse to touch before knowing the torrent is public.
	ErrPrivacyUnresolvable = errors.New("magnet has no trackers and DHT is disabled")
)

// Session owns a BitTorrent client for the lifetime of one run.
type Session struct {
	cl  *torrent.Client
	cfg Config
	// store is the storage this session handed its client, kept so it can be
	// closed again. anacrolix closes each TORRENT's storage when the client
	// closes (Client.Close waits on its closeGroup for exactly that), but it
	// never closes the ClientImpl it was given - reasonably, since it did not
	// make it. We did, so we do.
	//
	// Proof, since this is a claim about somebody else's code: client.go
	// registers an onClose for the storage it builds itself, inside
	// `if storageImpl == nil`, and nothing anywhere closes a
	// cfg.DefaultStorage that was handed in. So before this, every session
	// left its piece-completion database open for the life of the process,
	// and a client that failed to start leaked one outright.
	//
	// This is also the whole of TOR-59's "couldn't open piece completion db:
	// timeout". That db is bbolt, held under an exclusive flock with a
	// one-second timeout, and a leaked storage held it for the life of the
	// process - so the second client of a public-magnet restart, and the
	// second run over one data dir, timed out on the lock and silently fell
	// back to in-memory bookkeeping. completion_test.go pins both shapes, and
	// they separate the arms cleanly: with this Close removed every run warns
	// and the db stays held; with it, none of 126 runs did, 120 of them at
	// load averages between 84 and 265. It looked unexplained only because
	// bbolt is the default piece completion solely when cgo is off - the
	// shipped build - while a bare `go test` has cgo on and gets sqlite,
	// which shares the file and never warns. Build with CGO_ENABLED=0 (make
	// check) to see any of it.
	store storage.ClientImplCloser
	// lease is this session's claim on a port out of Config.Ports, held for
	// the client's whole life and handed back by Close. Nil when the caller
	// pinned ListenPort itself, or configured no pool at all.
	lease   *PortLease
	dhtOn   bool
	blindly bool // DHT was used before the private flag could be checked
}

// leasePort settles which port this config's client binds, taking one out of
// the pool when there is one.
//
// Returning the lease separately from the port is what lets Open hold a
// single lease across the restart it may have to do: the port is decided
// once, and the two clients that briefly follow each other onto it are the
// same claim, not two.
func (c Config) leasePort() (*PortLease, int, error) {
	if c.Ports == nil {
		return nil, c.ListenPort, nil
	}
	if c.ListenPort != 0 {
		return nil, 0, fmt.Errorf("swarm: ListenPort %d and Ports are both set; a client takes its port from one or the other", c.ListenPort)
	}

	lease, err := c.Ports.Acquire()
	if err != nil {
		return nil, 0, err
	}
	return lease, lease.Port(), nil
}

// Open starts a session and adds the source, returning the torrent once its
// metadata is known.
//
// The order of operations is the whole point. anacrolix does not act on the
// BEP 27 private flag, and DHT/PEX are client-wide switches, so the flag has to
// be settled before a client with DHT ever starts. See routeFor for the rules;
// this function only carries them out.
func Open(ctx context.Context, cfg Config, src Source) (*Session, *Torrent, error) {
	route := routeFor(cfg, src)
	if route.err != nil {
		return nil, nil, route.err
	}

	// One lease for the whole of Open, including the restart below. Leasing
	// per client instead would mean the restart asking a pool that may have
	// been emptied in the meantime by another torrent, and failing halfway
	// through a torrent that had already been given a port.
	lease, port, err := cfg.leasePort()
	if err != nil {
		return nil, nil, err
	}
	cfg.ListenPort = port

	s, t, err := openOnPort(ctx, cfg, src, route)
	if err != nil {
		lease.Release()
		return nil, nil, err
	}

	// The surviving session owns the lease, so its Close is what hands the
	// port back. Sessions Open discards along the way never hold it.
	s.lease = lease
	return s, t, nil
}

// openOnPort is Open's body once the port is settled: cfg.ListenPort is the
// port every client it starts will bind.
func openOnPort(ctx context.Context, cfg Config, src Source, route onlineRoute) (*Session, *Torrent, error) {
	s, err := newSession(cfg, route.dht)
	if err != nil {
		return nil, nil, err
	}
	s.blindly = route.blind

	t, err := s.add(ctx, src, nil)
	if err != nil {
		s.Close()
		if !route.probeTrackersFirst {
			return nil, nil, err
		}
		// The trackers stayed silent, so DHT is the only way left - and we
		// still do not know whether this torrent is private.
		return openBlind(ctx, cfg, src)
	}

	if !route.probeTrackersFirst || t.Private() {
		return s, t, nil
	}

	// Public torrent: restart with DHT, handing over the metadata already in
	// hand so nothing is fetched twice.
	mi := carriedOverMetainfo(t.t.Metainfo())
	s.Close()

	withDHT, err := newSession(cfg, true)
	if err != nil {
		return nil, nil, err
	}
	t, err = withDHT.add(ctx, src, &mi)
	if err != nil {
		withDHT.Close()
		return nil, nil, err
	}
	return withDHT, t, nil
}

// carriedOverMetainfo makes the metainfo anacrolix generates for a torrent
// whose info has already arrived safe to hand straight back to AddTorrent.
//
// Torrent.Metainfo fills PieceLayers from Torrent.pieceLayers(), which always
// allocates the map and then only records files carrying a BitTorrent v2
// pieces root. A v1 torrent has none, so the map comes back non-nil and empty.
// AddTorrent reads any non-nil map as "this torrent came with piece layers",
// walks every multi-piece file and rejects each one with "no piece root set
// for file" - which is why a plain v1 magnet produced a v2 complaint, once per
// file. A .torrent read off disk never hits this: its bencode has no
// "piece layers" key, so the field stays nil and the check is skipped.
// Dropping an empty map is exactly what makes the round trip match that.
//
// A genuine v2 torrent is untouched: its files do carry roots, so any layers
// actually held are kept and still validated, and a v2 torrent holding none is
// no worse off - AddTorrent only warns for a missing entry, it is the empty
// map that turns silence into an error.
func carriedOverMetainfo(mi metainfo.MetaInfo) metainfo.MetaInfo {
	if len(mi.PieceLayers) == 0 {
		mi.PieceLayers = nil
	}
	return mi
}

// openBlind is the honest-but-imperfect path: DHT before the privacy check,
// reachable only when a magnet carries no working trackers.
func openBlind(ctx context.Context, cfg Config, src Source) (*Session, *Torrent, error) {
	s, err := newSession(cfg, true)
	if err != nil {
		return nil, nil, err
	}
	s.blindly = true

	t, err := s.add(ctx, src, nil)
	if err != nil {
		s.Close()
		return nil, nil, err
	}
	return s, t, nil
}

// UsesDHT reports whether this session's client has DHT running. For a private
// torrent it must be false - that is BEP 27, and it is checkable rather than
// merely intended.
func (s *Session) UsesDHT() bool {
	return s.dhtOn && len(s.cl.DhtServers()) > 0
}

// tuneClientForTest, when set, adjusts the anacrolix config just before a
// client starts. It is nil in production and only ever set from a _test.go
// file in this package.
//
// It exists because the one path worth testing here - Open's restart, which
// only happens when DHT was asked for - cannot be reached with DHT off, and a
// test must not reach the real DHT. Handing the second client a DHT server
// with no starting nodes gives a DHT that is genuinely running (DhtServers is
// non-empty, so UsesDHT is true) yet has nowhere to bootstrap to, which is how
// anacrolix's own tests keep a DHT-enabled client offline.
var tuneClientForTest func(*torrent.ClientConfig)

func newSession(cfg Config, dht bool) (*Session, error) {
	tc := torrent.NewDefaultClientConfig()
	// The library logs read failures to stderr, including the ones we cause
	// on purpose by cancelling a reader when a window is released. The core
	// stays silent and its clients decide what a person sees, so nothing below
	// Critical is allowed through.
	tc.Logger = alog.Default.FilterLevel(alog.Critical)
	store := storage.NewFileByInfoHash(cfg.DataDir)
	tc.DefaultStorage = store
	tc.NoUpload = !cfg.Upload
	tc.NoDHT = !dht
	tc.DisablePEX = !dht
	tc.ListenPort = cfg.ListenPort
	// We only ever hold slivers of a file, so there is nothing to seed once
	// the run is over.
	tc.Seed = false
	// Left at its default, anacrolix probes the LAN for a UPnP/NAT-PMP router
	// on every session (an SSDP broadcast per interface, from an OS-assigned
	// UDP port) to open an inbound mapping for ListenPort. That is traffic
	// with no torrent in it - section 4 promises none - and on the seedbox
	// this whole feature targets (section 4.1) there is no home router to
	// answer it anyway. It also means "pin the port" would not actually be
	// "nothing else is listening" (TOR-28's acceptance criterion): these
	// probes bind their own ephemeral port each time, unpinned by design.
	tc.NoDefaultPortForwarding = true

	if tuneClientForTest != nil {
		tuneClientForTest(tc)
	}

	cl, err := torrent.NewClient(tc)
	if err != nil {
		// The storage was opened before the client and nothing else will
		// close it now. Left behind, its flock on .torrent.bolt.db outlives
		// the failure for the life of the process, so the next attempt -
		// after a pinned port frees up, say - would be the one that silently
		// falls back to in-memory bookkeeping.
		store.Close()
		return nil, fmt.Errorf("start torrent session: %w", err)
	}
	return &Session{cl: cl, cfg: cfg, store: store, dhtOn: dht}, nil
}

// add attaches the source to this session's client and waits for metadata.
// A non-nil mi short-circuits the wait by supplying the info bytes directly.
func (s *Session) add(ctx context.Context, src Source, mi *metainfo.MetaInfo) (*Torrent, error) {
	var (
		t   *torrent.Torrent
		err error
	)

	switch {
	case mi != nil:
		t, err = s.cl.AddTorrent(mi)
	case src.IsMagnet():
		t, err = s.cl.AddMagnet(src.String())
	default:
		t, err = s.cl.AddTorrentFromFile(src.String())
	}
	if err != nil {
		return nil, fmt.Errorf("add torrent: %w", err)
	}

	timeout := s.cfg.MetadataTimeout
	if timeout <= 0 {
		timeout = defaultMetadataTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Directly known peers are added before waiting: without a tracker or DHT
	// they are the only way metadata arrives at all.
	//
	// Deliberately not through a *Torrent. That type's whole premise is that
	// the metadata is known - it caches the file list and the piece geometry
	// at construction - and here it is exactly what has not arrived yet. A
	// magnet reaches this line with a nil Info, and building one crashed the
	// run before it started (TOR-48). Introducing a peer needs none of that.
	if len(s.cfg.Peers) > 0 {
		addPeers(t, s.cfg.Peers...)
	}

	select {
	case <-t.GotInfo():
	case <-waitCtx.Done():
		return nil, fmt.Errorf("%w after %s", ErrNoMetadata, timeout)
	}

	return newTorrent(t), nil
}

// DHTEnabled reports whether this session's client has DHT and PEX on.
func (s *Session) DHTEnabled() bool { return s.dhtOn }

// ListenPort reports the port this session's client is actually bound to -
// what Config.ListenPort resolved to, whether it was pinned or left at zero
// for the OS to assign. Exists so a caller (or a test) can observe the real
// socket rather than trust the config that asked for it.
func (s *Session) ListenPort() int { return s.cl.LocalPort() }

// WentOnlineBlind reports that DHT was used before the private flag could be
// checked - only possible for a magnet carrying no trackers. Callers should
// surface this rather than hide it.
func (s *Session) WentOnlineBlind() bool { return s.blindly }

// Close shuts the client down.
func (s *Session) Close() error {
	if s.cl == nil {
		return nil
	}
	errs := s.cl.Close()
	s.cl = nil

	// After the client, never before: Client.Close is what closes the
	// torrents whose data this storage holds, and closing it out from under
	// them would be closing a file somebody is still writing to.
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			errs = append(errs, err)
		}
		s.store = nil
	}

	// Last of all, and only once the socket has actually gone: whoever binds
	// this port next - Open's own restart, or the next client out of the
	// pool - must not race the close.
	waitPortFree(s.cfg.ListenPort)
	s.lease.Release()
	s.lease = nil

	return errors.Join(errs...)
}

// How long Close waits for a pinned port to come free, and how often it looks.
// The wait is normally a poll or two; the ceiling exists so a port that never
// frees cannot hang a caller.
const (
	portFreeWait = 2 * time.Second
	portFreePoll = 5 * time.Millisecond
)

// waitPortFree blocks until nothing is listening on port, or the ceiling is
// reached. Port 0 - the OS chose it, nobody will ask for it by name - returns
// at once.
//
// This is not belt and braces, it is a documented hole in Client.Close. That
// method waits (closeGroup.Wait) for the work it queues on torrents and their
// storage, but the listening sockets are not part of that: client.go registers
// them as `cl.onClose = append(cl.onClose, func() { go s.Close() })`, a
// goroutine nothing ever waits on. So Client.Close can return with the port
// still bound, and the next bind on it - which on a managed host is a
// certainty rather than a possibility, because the whole point of a pinned
// range is that the ports come back around - fails with "address already in
// use" and, since the port is pinned, fails the run rather than sliding onto
// another port.
//
// Timing out is deliberately silent and hands the port back anyway: a port
// still held after two seconds is something the next bind will report
// honestly, and dropping it from the set for the life of the process would
// turn a transient into a permanent loss of capacity.
func waitPortFree(port int) {
	if port == 0 {
		return
	}

	deadline := time.Now().Add(portFreeWait)
	for {
		if portFree(port) {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(portFreePoll)
	}
}

// portFree reports whether every socket a client puts on its port is
// bindable again.
//
// Each address family is probed by name rather than through a wildcard
// "tcp"/"udp" listen, and that is not thoroughness for its own sake: on
// darwin net.Listen("tcp", ":p") binds v4 only (supportsIPv4map is false
// there), so a wildcard probe reports a port free while anacrolix's own
// tcp6 socket is still on it - which is exactly the socket the next client
// fails to bind, with "subsequent listen: listen tcp6 :p: bind: address
// already in use". Measured, not reasoned: with the wildcard probe, 40
// open/close cycles on OS-assigned ports hit that failure.
//
// Only EADDRINUSE counts as busy. A host with IPv6 switched off refuses a
// tcp6 bind for a reason that has nothing to do with this port, and reading
// that as "still busy" would make every close wait out the full ceiling.
// Windows reports its own WSAEADDRINUSE, which this does not recognise, so
// there the wait simply never triggers and Close behaves as it did before.
func portFree(port int) bool {
	addr := net.JoinHostPort("", strconv.Itoa(port))
	for _, network := range []string{"tcp4", "tcp6", "udp4", "udp6"} {
		if portBusy(network, addr) {
			return false
		}
	}
	return true
}

func portBusy(network, addr string) bool {
	var (
		closer io.Closer
		err    error
	)
	if strings.HasPrefix(network, "udp") {
		closer, err = net.ListenPacket(network, addr)
	} else {
		closer, err = net.Listen(network, addr)
	}
	if err != nil {
		return errors.Is(err, syscall.EADDRINUSE)
	}
	closer.Close()
	return false
}

// DiscardPieces removes this run's own piece subtree for one torrent, never
// the data directory itself (REQUIREMENTS.md 2.9: raw pieces are staging
// data, not a result worth keeping).
//
// storage.NewFileByInfoHash, which newSession always configures as the
// client's storage, namespaces every torrent under <DataDir>/<infohash>/ -
// see anacrolix's storage/file-paths.go infoHashPathMaker. That is exactly
// what makes dropping one run's own subtree safe: it cannot reach a sibling
// torrent's pieces, and a directory the caller named with -data keeps
// existing, only lighter.
//
// Call only after Close has returned, and only then: while the client is
// live, pieces are still being written to (and, with Upload on, read from
// for peers) by goroutines this call knows nothing about. Close is safe to
// treat as that boundary - not by assumption, but because of what was
// actually traced through anacrolix v1.61.0's client.go and torrent.go:
// Client.Close() drops every torrent via dropTorrent(t, &closeGroup), and
// Torrent.close() queues its storage.Close call onto that same
// *sync.WaitGroup rather than firing it and forgetting it; Client.Close()
// calls closeGroup.Wait() before it returns. So by the time Close returns,
// every torrent's storage.Close has already run. (Client.Close() does also
// contain a literal `func() { go s.Close() }` in its onClose list, and ruling
// out "that's the async storage close" was the first thing worth checking -
// but that s is a listen socket from client.go's own sockets loop, not
// storage; it does not apply here.) The file storage backend itself holds nothing
// open between calls anyway: storage/file-io-classic.go opens and closes an
// os.File around each individual read or write rather than keeping one for
// the run's lifetime, so there is no lingering handle on a piece file for
// Close's wait to even need to cover - it is a genuine guarantee, not one
// this happens to lean on without headroom.
func (s *Session) DiscardPieces(infoHash string) error {
	if s.cfg.DataDir == "" || infoHash == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(s.cfg.DataDir, infoHash))
}
