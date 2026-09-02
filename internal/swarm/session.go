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

	// ListenPort is the BitTorrent port. anacrolix binds TCP, uTP and (when
	// DHT is on) the DHT server to this same port - pinning it pins all
	// three, which is what section 4.1's "no port outside the allocated
	// range" actually requires. Zero lets the OS choose one port for all of
	// them, which is fine locally but not on a managed host with a fixed
	// range, so callers there must set it. A pinned port already taken is a
	// startup error, not silently retried on a different one - anacrolix
	// only falls back to another port when ListenPort is zero.
	ListenPort int

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
	cl      *torrent.Client
	cfg     Config
	dhtOn   bool
	blindly bool // DHT was used before the private flag could be checked
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
	tc.DefaultStorage = storage.NewFileByInfoHash(cfg.DataDir)
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
		return nil, fmt.Errorf("start torrent session: %w", err)
	}
	return &Session{cl: cl, cfg: cfg, dhtOn: dht}, nil
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
	return errors.Join(errs...)
}
