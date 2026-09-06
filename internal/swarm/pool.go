package swarm

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
)

var (
	// ErrPoolClosed means the pool has been shut down and will not bring
	// anything else online.
	ErrPoolClosed = errors.New("torrent pool is closed")

	// ErrTorrentBusy means this torrent is already attached. It is a refusal
	// rather than a queue: two runs over one infohash would share a single
	// anacrolix torrent - the same Stats the budget is metered off, the same
	// piece claims, the same <DataDir>/<infohash>/ - so whichever finished
	// first would discard the other's pieces and both would be billed for
	// each other's traffic. Before there was a pool this was merely
	// unreachable (one run at a time); saying so is what keeps it unreachable
	// now that it is not.
	ErrTorrentBusy = errors.New("this torrent is already being worked on")
)

// Pool is where a run gets its torrent from.
//
// It holds one long-lived client for every PUBLIC torrent - one client, one
// port, DHT as Config asked for it - because that is what a client is: DHT
// and PEX are
// client-wide switches and a client binds one port, so the number of public
// torrents fetching at once has nothing to do with the number of clients or
// ports (REQUIREMENTS.md 4.1). That client starts with the first public
// torrent and lives until Close. It is emphatically NOT a run's to close: a
// run attaches and detaches, and a run that fails detaches like any other.
//
// A torrent that cannot join it gets a client of its own, exactly as every
// torrent did before this type existed. That is every torrent whose privacy
// is not settled before it goes online, and every private one:
//
//   - a .torrent that states private=false joins the shared client;
//   - a magnet with trackers is probed on a throwaway client with DHT off -
//     the same probe Open has always done - and joins the shared client only
//     once the metadata says it is public;
//   - a private torrent, and a magnet with no trackers that had to reach the
//     DHT before its flag could be read, stay on a client of their own.
//
// The rule is one sentence: nothing enters the shared client until it is
// known public. A private torrent sharing a client with a public one is the
// single failure this whole arrangement exists to make impossible, and
// "probably public" is not knowing.
//
// # What this costs in ports
//
// One for the shared client while it is up, plus one, transiently, for the
// tracker probe of a magnet whose privacy is not yet known. On a managed host
// that means a magnet needs two ports at the moment it is opened, which is
// why section 4.1 asks for N greater than one; a single-port allocation can
// still serve a .torrent, but not a magnet while the shared client is up. It
// is reported as ErrNoPortAvailable, which names the limit.
type Pool struct {
	// cfg is the template every client here is built from. Its Ports field
	// is the one thing shared between them: it is what stops the shared
	// client, a probe and a private torrent from asking for the same port.
	cfg Config

	mu sync.Mutex
	// sess is the shared public client, nil until the first public torrent
	// arrives. Nothing here starts a client speculatively - a server that is
	// up and idle should not be holding a port, or announcing itself to the
	// DHT, on the chance that somebody pastes a magnet.
	sess *Session
	// held is every infohash currently attached, pooled or on its own. It is
	// what makes ErrTorrentBusy possible, and it is keyed by the hash the
	// SOURCE names, which is known before anything goes online - so a second
	// attach is refused before it has cost a port or a connection.
	held map[string]struct{}
	// alone is every client the pool started for a torrent that could not
	// join the shared one, keyed the same way held is. It exists so
	// Downloaded can see their traffic: a private torrent's client is still
	// this pool's client, spending this pool's link.
	alone map[string]*Session
	// retired is the received-bytes total of every client this pool has
	// already closed. A client's counters go when its client reference does
	// (Session.Received reports zero after Close), so each one's figure is
	// taken just before it is closed and added here - otherwise detaching a
	// torrent, or shutting the pool down, would hand its traffic back and the
	// roof would bound nothing.
	retired int64
	closed  bool
}

// NewPool returns a pool whose clients are built from cfg.
//
// cfg.Ports, when set, is shared by every client the pool starts, which is
// the whole reason a pool takes a Config rather than each run bringing its
// own: two clients must never be handed the same port.
func NewPool(cfg Config) *Pool {
	return &Pool{
		cfg:   cfg,
		held:  make(map[string]struct{}),
		alone: make(map[string]*Session),
	}
}

// Attach brings src online and hands back the torrent, out of the shared
// public client where that is allowed and out of a client of its own where it
// is not.
//
// peers are addresses to introduce to THIS torrent, on top of Config.Peers.
// They are per attachment rather than per pool because the shared client
// holds many torrents and a peer is only ever a peer for one of them.
//
// The returned attachment must be detached. Detaching is not closing: it
// removes one torrent from the client and discards that torrent's pieces,
// and the client carries on holding whatever else is attached to it.
func (p *Pool) Attach(ctx context.Context, src Source, peers ...string) (*Attachment, error) {
	hash, err := src.InfoHash()
	if err != nil {
		return nil, err
	}
	key := hash.HexString()

	if err := p.reserve(key); err != nil {
		return nil, err
	}

	att, err := p.attach(ctx, src, key, peers)
	if err != nil {
		p.release(key)
		return nil, err
	}
	return att, nil
}

// Close shuts the shared client down and refuses anything further.
//
// It does not wait for attachments: a caller that closes the pool while a run
// is still going has already cancelled that run, and the detach that follows
// finds its torrent gone and simply discards its pieces (Torrent.Drop returns
// at once for a torrent the client already closed). What Close must never be
// is something a run reaches for - see the type's own comment.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	sess := p.sess
	p.sess = nil
	// Under the same lock that drops the client, and before it is closed:
	// Session.Received goes to zero once Close has run, and taking the figure
	// here means no reader of Downloaded ever sees the shared client's
	// traffic vanish. What it misses is whatever arrives during the teardown
	// itself, which is a closing socket's worth.
	if sess != nil {
		p.retired += sess.Received()
	}
	p.mu.Unlock()

	// Outside the lock on purpose: Session.Close polls for its port to come
	// free, and a detach racing this shutdown should not wait that out.
	if sess == nil {
		return nil
	}
	return sess.Close()
}

// Downloaded is the useful data every client this pool has run has received,
// in bytes - the figure a client-wide traffic roof is held against
// (REQUIREMENTS.md 2.6).
//
// It satisfies the same one-method interface core's budget meters do, and
// deliberately carries the same name as Torrent.Downloaded, because the two
// are the same measurement at two scales: one torrent's arrivals, and every
// arrival on every client the pool owns. What separates them is what a run's
// ceiling and a client's roof are each protecting - see Session.Received for
// why the roof cannot be a sum of the per-torrent figures.
//
// Cumulative over the pool's whole life. Detaching a torrent does not give
// its traffic back and neither does closing the client that carried it; a
// quota that fell as work finished would not be a quota.
//
// Received bytes only. Nothing here counts what was UPLOADED - see
// Torrent.Uploaded, which says the same thing about the per-run ceiling.
func (p *Pool) Downloaded() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	total := p.retired
	if p.sess != nil {
		total += p.sess.Received()
	}
	for _, sess := range p.alone {
		total += sess.Received()
	}
	return total
}

// Up reports whether the shared public client is running. False before the
// first public torrent and after Close.
func (p *Pool) Up() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sess != nil
}

// ListenPort is the port the shared public client is bound to, or 0 when it
// is not running. Every public torrent in the pool is on this one port, which
// is the claim worth being able to check rather than assert.
func (p *Pool) ListenPort() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess == nil {
		return 0
	}
	return p.sess.ListenPort()
}

// UsesDHT reports whether the shared public client has DHT running.
func (p *Pool) UsesDHT() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sess != nil && p.sess.UsesDHT()
}

// Attached is how many torrents are attached to this pool right now, on the
// shared client and on clients of their own alike.
func (p *Pool) Attached() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.held)
}

func (p *Pool) reserve(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrPoolClosed
	}
	if _, busy := p.held[key]; busy {
		return fmt.Errorf("%w: %s", ErrTorrentBusy, key)
	}
	p.held[key] = struct{}{}
	return nil
}

func (p *Pool) release(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.held, key)
}

// aloneOn builds the attachment for a torrent that got a client to itself,
// and registers that client so Downloaded can see its traffic. Every
// attachment carrying a Session of its own is built here, so there is one
// place where "this client is the pool's too" is said.
func (p *Pool) aloneOn(key string, t *Torrent, sess *Session) *Attachment {
	p.mu.Lock()
	p.alone[key] = sess
	p.mu.Unlock()
	return &Attachment{pool: p, key: key, t: t, sess: sess}
}

// retire folds a client's received-bytes figure into the pool's running total
// and forgets the client. Called just BEFORE that client is closed, because
// Session.Received reports zero once it has been.
//
// Both steps under one lock, which is what stops the figure being counted
// twice (a Downloaded between them would see the client in alone AND its bytes
// in retired) or not at all (a Downloaded between them the other way round
// would see neither). Holding p.mu is also what makes reading the session
// safe: Downloaded is the only other reader, and it holds the same lock.
func (p *Pool) retire(key string, sess *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retired += sess.Received()
	delete(p.alone, key)
}

// retireClient is retire for a client that was never registered under a key:
// the tracker probe of a magnet whose privacy is not yet known, which is
// started and thrown away inside a single attach.
//
// It is very nearly nothing - a probe fetches metadata, and metadata chunks
// are counted by anacrolix as MetadataChunksRead rather than as the useful
// piece data Received reports. It is folded in anyway, because "every client
// this pool has run" is the claim Downloaded makes, and a claim with an
// unstated exception in it is worse than a slightly smaller number.
func (p *Pool) retireClient(sess *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retired += sess.Received()
}

func (p *Pool) attach(ctx context.Context, src Source, key string, peers []string) (*Attachment, error) {
	route := routeFor(p.cfg, src)
	if route.err != nil {
		return nil, route.err
	}

	// Known public before a single connection: straight into the shared
	// client. This is the only source kind that can take this path, and that
	// is the point - see the type comment.
	if private, known := src.Privacy(); known && !private {
		return p.attachShared(ctx, src, key, nil, peers, nil)
	}

	if !route.probeTrackersFirst {
		return p.attachAlone(ctx, src, key, peers)
	}
	return p.probeThenAttach(ctx, src, key, peers)
}

// attachAlone is the unchanged path: this torrent gets a client to itself,
// configured by exactly the rules Open has always applied.
func (p *Pool) attachAlone(ctx context.Context, src Source, key string, peers []string) (*Attachment, error) {
	cfg := p.cfg
	cfg.Peers = mergePeers(p.cfg.Peers, peers)

	sess, t, err := Open(ctx, cfg, src)
	if err != nil {
		return nil, err
	}
	return p.aloneOn(key, t, sess), nil
}

// probeThenAttach handles a magnet with trackers: fetch the metadata with DHT
// off, then decide where the torrent lives.
//
// This is Open's own restart, rewritten around the pool. The probe is
// identical - same client settings, same order, same fallback to openBlind
// when the trackers stay silent - and the only thing that changed is where a
// torrent that turns out PUBLIC goes: into the shared client, instead of into
// a second client of its own.
//
// The probe's port travels with it. Open held one lease across its restart so
// the two clients that briefly follow each other onto a port were one claim
// rather than two; here the same lease either becomes the shared client's
// (when the pool has no client yet) or is handed back (when it already has
// one). Either way nothing else can slip onto that port in between.
func (p *Pool) probeThenAttach(ctx context.Context, src Source, key string, peers []string) (*Attachment, error) {
	claim, err := p.claimPort()
	if err != nil {
		return nil, err
	}

	cfg := p.cfg
	// The port is settled and the lease is held here, so nothing below may
	// ask the pool for another one.
	cfg.ListenPort, cfg.Ports = claim.port, nil
	cfg.Peers = mergePeers(p.cfg.Peers, peers)

	probe, err := newSession(cfg, false)
	if err != nil {
		claim.release()
		return nil, err
	}

	t, err := probe.add(ctx, src, nil)
	if err != nil {
		// The trackers stayed silent, so DHT is the only way left - and we
		// still do not know whether this torrent is private. It therefore
		// keeps a client of its own, as it always has.
		p.retireClient(probe)
		probe.Close()

		blind, bt, berr := openBlind(ctx, cfg, src)
		if berr != nil {
			claim.release()
			return nil, berr
		}
		blind.lease = claim.lease
		return p.aloneOn(key, bt, blind), nil
	}

	if t.Private() {
		// DHT was never on for this client, which is exactly what a private
		// torrent needs, so it stays where it is.
		probe.lease = claim.lease
		return p.aloneOn(key, t, probe), nil
	}

	// Public: hand the metadata already in hand to the shared client so
	// nothing is fetched twice, and close the probe before anything binds
	// its port again (Session.Close waits for that).
	mi := carriedOverMetainfo(t.t.Metainfo())
	p.retireClient(probe)
	probe.Close()

	return p.attachShared(ctx, src, key, &mi, peers, &claim)
}

// attachShared adds one known-public torrent to the shared client, starting
// that client if this is the first.
//
// spare, when non-nil, is a port claim the caller already holds and no longer
// needs: the shared client takes it over if it has no port yet, and it is
// handed back otherwise.
func (p *Pool) attachShared(ctx context.Context, src Source, key string,
	mi *metainfo.MetaInfo, peers []string, spare *portClaim) (*Attachment, error) {

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		spare.release()
		return nil, ErrPoolClosed
	}

	if p.sess == nil {
		if err := p.startLocked(spare); err != nil {
			return nil, err
		}
	} else {
		spare.release()
	}

	// Under the lock, and safe to be: this never waits on the swarm. Both
	// callers arrive with the info bytes already in hand - a .torrent carries
	// them and the probe just fetched them - so add's metadata wait is
	// already satisfied when it looks.
	t, err := p.sess.addTo(ctx, src, mi, mergePeers(p.cfg.Peers, peers))
	if err != nil {
		return nil, err
	}
	return &Attachment{pool: p, key: key, t: t}, nil
}

// startLocked brings the shared public client up. The caller must hold p.mu.
func (p *Pool) startLocked(spare *portClaim) error {
	claim := spare
	if claim == nil {
		got, err := p.claimPort()
		if err != nil {
			return err
		}
		claim = &got
	}

	cfg := p.cfg
	cfg.ListenPort, cfg.Ports = claim.port, nil

	sess, err := newSession(cfg, p.cfg.DHT)
	if err != nil {
		claim.release()
		return err
	}
	sess.lease = claim.lease
	p.sess = sess
	return nil
}

// portClaim is a settled port together with the lease it came from, which are
// not the same thing: a caller that pinned Config.ListenPort has a port and no
// lease, and one drawing on an unmanaged pool has a lease carrying port 0.
type portClaim struct {
	lease *PortLease
	port  int
}

func (p *Pool) claimPort() (portClaim, error) {
	lease, port, err := p.cfg.leasePort()
	if err != nil {
		return portClaim{}, err
	}
	return portClaim{lease: lease, port: port}, nil
}

// release hands the port back. Nil-safe, so a caller that may or may not have
// a spare claim can call it unconditionally.
func (c *portClaim) release() {
	if c == nil {
		return
	}
	c.lease.Release()
}

func mergePeers(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	return append(out, extra...)
}

// Attachment is one run's hold on one torrent.
//
// It exists so a run has something to let go of that is not a client. Before
// the pool the two were the same object and a run's failure path closed the
// client it happened to be using; now a run detaches, and what that means -
// drop one torrent out of a client somebody else owns, or close a client this
// torrent had to itself - is this type's business rather than the run's.
type Attachment struct {
	pool *Pool
	key  string
	t    *Torrent

	// sess is non-nil only for a torrent that could not join the shared
	// client, and is then a client held for this attachment alone. Nil means
	// the torrent is in the pool's shared client, which detaching must not
	// touch beyond removing this one torrent from it.
	sess *Session

	once sync.Once
	err  error
}

// Torrent is what was attached.
func (a *Attachment) Torrent() *Torrent { return a.t }

// Pooled reports whether this torrent is in the shared public client rather
// than on a client of its own.
func (a *Attachment) Pooled() bool { return a.sess == nil }

// ListenPort is the BitTorrent port this torrent's client is bound to.
func (a *Attachment) ListenPort() int {
	if a.sess != nil {
		return a.sess.ListenPort()
	}
	return a.pool.ListenPort()
}

// WentOnlineBlind reports that DHT was used before the private flag could be
// checked - only possible for a magnet carrying no trackers, and therefore
// only ever for a torrent on a client of its own.
func (a *Attachment) WentOnlineBlind() bool {
	return a.sess != nil && a.sess.WentOnlineBlind()
}

// Detach lets this torrent go and discards its pieces. Idempotent.
//
// For a pooled torrent that is a Drop, not a Close: the client stays up
// holding whatever else is attached to it, and every sibling torrent's pieces
// stay exactly where they are (storage.NewFileByInfoHash namespaces each
// torrent under <DataDir>/<infohash>/, which is what makes removing one
// subtree a local act). For a torrent on a client of its own it is the same
// Close-then-discard a run has always done.
//
// Either way the pieces go. Raw pieces are staging data and never a result
// (REQUIREMENTS.md 2.9), and a server that stays up for hours has to drop
// them per torrent rather than per process.
func (a *Attachment) Detach() error {
	a.once.Do(func() { a.err = a.detach() })
	return a.err
}

func (a *Attachment) detach() error {
	defer a.pool.release(a.key)

	// The torrent's own hash, not the source's: the storage namespaces the
	// piece subtree by what the client resolved, and that is what has to be
	// removed.
	infoHash := a.t.InfoHash()

	if a.sess != nil {
		// Before the close, not after: Session.Received reports zero once the
		// client reference is gone, and the pool's roof is owed these bytes
		// whether the torrent that spent them is still here or not
		// (Pool.Downloaded). Only the teardown's own traffic is missed.
		a.pool.retire(a.key, a.sess)

		err := a.sess.Close()
		if discardErr := a.sess.DiscardPieces(infoHash); err == nil {
			err = discardErr
		}
		return err
	}

	// Drop returns only once this torrent's storage has been closed - it
	// waits on the same WaitGroup Client.Close waits on, for the same
	// Torrent.close - which is the boundary discardPieces documents.
	a.t.t.Drop()
	return discardPieces(a.pool.cfg.DataDir, infoHash)
}
