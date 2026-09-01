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
	// blind means DHT is used before the private flag could be checked.
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

	// A magnet with no trackers: metadata is unreachable without DHT.
	if !cfg.DHT {
		return onlineRoute{err: ErrPrivacyUnresolvable}
	}
	return onlineRoute{dht: true, blind: true}
}
