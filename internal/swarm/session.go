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

	// ListenPort is the BitTorrent port. Zero lets the OS choose, which is
	// fine locally but not on a managed host that allocates a fixed range
	// (section 4.1), so callers there must set it.
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
	mi := t.t.Metainfo()
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

func newSession(cfg Config, dht bool) (*Session, error) {
	tc := torrent.NewDefaultClientConfig()
	tc.DefaultStorage = storage.NewFileByInfoHash(cfg.DataDir)
	tc.NoUpload = !cfg.Upload
	tc.NoDHT = !dht
	tc.DisablePEX = !dht
	tc.ListenPort = cfg.ListenPort
	// We only ever hold slivers of a file, so there is nothing to seed once
	// the run is over.
	tc.Seed = false

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

	tor := newTorrent(t)
	// Directly known peers are added before waiting: without a tracker or DHT
	// they are the only way metadata arrives at all.
	if len(s.cfg.Peers) > 0 {
		tor.AddPeers(s.cfg.Peers...)
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
