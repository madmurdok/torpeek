package swarm

// onlineRoute is how a source has to be brought online without ever letting a
// private torrent touch the DHT.
//
// This is a pure decision, separated from Open so it can be tested exhaustively
// without starting a client or a single network connection - the guarantee it
// encodes is the one thing here that must not regress silently.
type onlineRoute struct {
	// dht is the setting for the first client started.
	dht bool
	// probeTrackersFirst means metadata is fetched with DHT off, and the
	// client is restarted with DHT only if the torrent turns out public.
	probeTrackersFirst bool
	// blind means DHT is used before the private flag could be checked. One
	// kind of source reaches it: a magnet with no working trackers, whose
	// metadata has nowhere to come from but the DHT while the flag it is
	// being asked about lives inside that metadata. Two code paths carry it -
	// openOnPort for a magnet carrying no trackers at all, openBlind for one
	// whose trackers stayed silent - and what happens when the metadata comes
	// back saying "private" is decided here, once, for both.
	//
	// # The decision
	//
	// The DHT lookup happens, because for this source nothing else fetches
	// metadata at all. Everything after it does not. The instant the metadata
	// says private, Session.addTo drops the torrent, discards its staging
	// subtree and refuses the add with ErrPrivateOnDHTClient; the client is
	// closed and never reaches a caller. So no piece of a private torrent's
	// content is ever requested over a client with DHT or PEX on, no private
	// torrent stays attached to one past the add call in which its flag first
	// became readable, and the run ends in a refusal naming the reason rather
	// than proceeding quietly. There is no run over a private torrent on this
	// route: that is what keeps acceptance criterion 5 an absolute rather
	// than an "except". The announce that had already gone out by then is the
	// irreducible cost of a trackerless magnet, is reported by
	// Session.WentOnlineBlind, and is exactly why this is a refusal instead
	// of a warning.
	//
	// # The trap being avoided
	//
	// With a pool of clients around there is a cheaper-looking way to get
	// that metadata: hand the magnet to the shared PUBLIC client, which
	// already has DHT on and already holds a port, and move the torrent onto
	// a client of its own if it turns out private. It is wrong, and it is
	// wrong before the move is ever reached - by then the shared client has
	// announced a private torrent's infohash to the DHT and exposed it to
	// PEX, in the same client announcing other people's public swarms.
	// Moving it afterwards cannot unsend a request. Pool.attachShared refuses
	// it at the door for that reason: nothing enters the shared client until
	// it is known public, and a magnet on this route is not known anything.
	//
	// # The other way, and why not
	//
	// The metadata is in hand by the time the flag is read, so this could
	// restart on a client with DHT off - the mirror of the restart the public
	// path does in the other direction - and carry on serving the run. It
	// would not help. A magnet that had no trackers to probe still has none
	// once the metadata arrives: BEP 9 transfers the info dictionary, and an
	// announce list is not in it. The restarted client could reach a peer
	// only where the operator named one outright (Config.Peers), so for
	// everybody else it would trade a refusal that names BEP 27 for a run
	// that stalls and times out saying nothing - while the DHT announce has
	// happened either way. The answer for a private torrent is a source that
	// settles the flag offline: the .torrent itself, or a magnet carrying its
	// tracker. Both route through the probe and never come here.
	blind bool
	// err, when set, means the source cannot be served under this config.
	err error
}

func routeFor(cfg Config, src Source) onlineRoute {
	private, known := src.Privacy()

	// A .torrent states the flag outright: one client, configured correctly.
	if known {
		return onlineRoute{dht: cfg.DHT && !private}
	}

	// A magnet with trackers: fetch metadata from them with DHT off, then
	// decide. A private torrent never reaches the DHT this way.
	if len(src.Trackers()) > 0 {
		return onlineRoute{dht: false, probeTrackersFirst: cfg.DHT}
	}

	// A magnet with no trackers: metadata is unreachable without DHT. Going
	// online blind is what that costs, and what it does NOT buy is the right
	// to keep going if the metadata says private - see the blind field.
	if !cfg.DHT {
		return onlineRoute{err: ErrPrivacyUnresolvable}
	}
	return onlineRoute{dht: true, blind: true}
}
