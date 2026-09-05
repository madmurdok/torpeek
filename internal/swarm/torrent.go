package swarm

import (
	"bytes"
	"fmt"
	"sort"
	"sync"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// Torrent is a torrent whose metadata is known, exposing only what torpeek
// needs. Piece-level operations arrive with the fetcher (TOR-4).
type Torrent struct {
	t       *torrent.Torrent
	private bool
	files   []FileInfo

	// Cached from the metadata: these never change once info is known, and
	// caching them keeps the piece arithmetic testable without a client.
	pieceLength int64
	numPieces   int
	length      int64

	// What this run ordered from the swarm, as distinct pieces. Written only
	// by noteClaimed (fetch.go), which Claim is the single funnel into; read
	// by Claimed below. Guarded because claims are issued from every capture
	// point in flight and from every range ffmpeg has open at once.
	claimMu     sync.Mutex
	claimedSeen map[int]struct{}
	claimedByte int64
}

func newTorrent(t *torrent.Torrent) *Torrent {
	info := t.Info()

	private := false
	if info != nil && info.Private != nil {
		private = *info.Private
	}

	files := make([]FileInfo, 0, len(t.Files()))
	for i, f := range t.Files() {
		files = append(files, FileInfo{
			Index:  i,
			Path:   f.Path(),
			Length: f.Length(),
			Offset: f.Offset(),
		})
	}

	tor := &Torrent{
		t:         t,
		private:   private,
		files:     files,
		numPieces: t.NumPieces(),
		length:    t.Length(),
	}
	if info != nil {
		tor.pieceLength = info.PieceLength
	}
	return tor
}

// Name is the torrent's display name.
func (t *Torrent) Name() string { return t.t.Name() }

// InfoHash identifies the torrent and keys the result cache.
func (t *Torrent) InfoHash() string { return t.t.InfoHash().HexString() }

// Magnet is a magnet URI for this torrent - not necessarily anything anyone
// typed, but a URI that reopens this very torrent from wherever a copy of
// the tree ends up, which is what makes it worth recording as a run's source
// (core.recordedSource, TOR-86).
//
// It carries the torrent's own announce list, and that is the load-bearing
// part rather than a nicety. A magnet with nothing but an infohash can only
// find peers through DHT, and a PRIVATE torrent must never touch DHT - it is
// acceptance criterion 5 and the session enforces it by construction. So a
// trackerless magnet would be a recorded source that either finds nobody or
// could only work by breaking the one guarantee this project treats as
// absolute. With the trackers copied, a private torrent reopens the way it
// was opened in the first place: routeFor probes them with DHT off and only
// then decides (TOR-49).
//
// The display name rides along because a magnet is meant to be pasted by a
// person, and one that says nothing but forty hex characters tells them
// nothing about what they are about to fetch.
func (t *Torrent) Magnet() string {
	mi := t.t.Metainfo()
	return metainfo.Magnet{
		InfoHash:    t.t.InfoHash(),
		DisplayName: t.t.Name(),
		Trackers:    mi.UpvertedAnnounceList().DistinctValues(),
	}.String()
}

// Private reports the BEP 27 flag. When true, this torrent must never touch
// DHT or PEX - the session guarantees that by construction.
func (t *Torrent) Private() bool { return t.private }

// Files lists every file in the torrent, in torrent order.
func (t *Torrent) Files() []FileInfo { return t.files }

// Videos lists the files worth taking frames from.
func (t *Torrent) Videos() []FileInfo { return SelectVideos(t.files) }

// Length is the total size of the torrent's contents.
func (t *Torrent) Length() int64 { return t.length }

// PieceLength is the swarm's unit of exchange, and so the floor on what any
// single frame can cost.
func (t *Torrent) PieceLength() int64 { return t.pieceLength }

// NumPieces is how many pieces the torrent is split into.
func (t *Torrent) NumPieces() int { return t.numPieces }

// Downloaded is the useful data received from the swarm so far, in bytes. This
// is what the run's traffic budget is measured against.
func (t *Torrent) Downloaded() int64 {
	stats := t.t.Stats()
	return stats.BytesReadUsefulData.Int64()
}

// Uploaded is the piece data this run has SENT to peers, in bytes - the mirror
// of Downloaded, off the same stats the library keeps for the other direction
// (BytesWrittenData against BytesReadUsefulData). Payload only: handshakes,
// requests and the rest of the protocol are not in it, which is what makes it
// comparable with Downloaded rather than merely adjacent to it.
//
// This happens because torpeek asks for it. Config.Upload is on by default and
// says why - peers reciprocate, which helps min-time - and `-upload=false`
// turns it off.
//
// NOTHING CAPS IT, and that is worth knowing rather than discovering on a
// bill. The traffic budget counts RECEIVED bytes, deliberately and for a
// stated reason (REQUIREMENTS.md 2.6: a limit protects the link and the quota,
// and bytes already on the wire do not care who asked for them), so a ceiling
// of 150 MB bounds what arrives and says nothing about what leaves. On a link
// or a seedbox quota that charges both directions, the only lever is the flag.
// This figure existing is what lets a person see the problem at all; capping it
// would be a decision nobody has taken yet.
func (t *Torrent) Uploaded() int64 {
	stats := t.t.Stats()
	return stats.BytesWrittenData.Int64()
}

// Claimed is what this run ASKED the swarm for: how many distinct pieces some
// Claim has covered, and what they weigh.
//
// It is the other half of Downloaded, and the gap between the two is the
// interesting quantity rather than an error term. Downloaded is every useful
// byte that ARRIVED, whoever asked for it; Claimed is what torpeek itself
// ordered. On the acceptance torrent min-traffic claims a deterministic 44
// pieces, to the byte, every run, while what arrived has been measured from
// 45.5 to 118.6 MiB on byte-identical code - reader prefetch, and chunks that
// keep coming from several peers after a Release, between 2.3 and 12.3 MiB of
// it while no request is open at all (docs/tor-88-min-traffic-spread.md).
// Acceptance criterion 2 is judged on THIS figure for exactly that reason
// (REQUIREMENTS.md section 8, TOR-94). A run's traffic BUDGET is deliberately
// not (section 2.6): a budget protects a link and a quota, and bytes already
// on the wire cost the same whoever asked for them.
//
// Cumulative and monotonic. A piece claimed, released and claimed again - one
// piece goes through that 82 times in a single acceptance run - counts once,
// and Release never takes anything back off this figure. What it measures is
// the set of pieces the run ordered at least once, which is the part that is
// deterministic; how long each order stood is a different question and not
// this one.
//
// This is a measurement seam, not a protocol. It reaches core.Done, where the
// acceptance harness reads it, and - since TOR-119 - the run record on disk by
// way of ClaimedRanges below; it deliberately does not travel into
// internal/wire's event JSON or into the manifest's cost record (section 2.8),
// neither of which anybody has asked to grow a second traffic number.
func (t *Torrent) Claimed() (pieces int, bytes int64) {
	t.claimMu.Lock()
	defer t.claimMu.Unlock()
	return len(t.claimedSeen), t.claimedByte
}

// ClaimedRanges is Claimed's count broken out into WHERE those pieces are:
// the same set, coalesced into ascending, non-touching half-open ranges.
// Empty for a run that claimed nothing.
//
// It answers the question the count cannot - 44 of 270 pieces is the argument
// of the whole product, and a count alone cannot say the 44 were spread across
// the film rather than sitting in a lump at the front (TOR-119, drawn by
// TOR-111). The manifest's torrent.availability is not this data and looks
// like it should be: that is a 64-bucket summary of what the SWARM held, not
// what this run ordered.
//
// WHY RANGES rather than the raw indices or a bitmap, decided on the record
// size each produces rather than on what is easiest to emit. Claims arrive as
// half-open PieceRanges (noteClaimed), so contiguity is how the data already
// comes, and the three shapes cost, in JSON bytes, against a run.json that
// weighs 582 to 1414 bytes today:
//
//	                                   indices   ranges   bitmap
//	44 of 270 pieces, ~20 windows         123B     222B      50B
//	40 of 10000 pieces, ~20 windows       112B     302B    1670B
//	2000 of 10000, degraded to one run   8892B      17B    1670B
//	all 10000 pieces, one run           48891B      17B    1670B
//
// No shape wins everywhere. Indices are cheapest when claims are sparse and
// scattered, and catastrophic when they are not - 49 KB on a 600-byte record,
// which a run that degrades to sequential reading can reach. An exact bitmap
// is cheapest of all in the small case but costs ceil(pieces/8) whatever
// happened, so a 40 GB remux pays 1.7 KB to describe twenty claimed pieces -
// charging the record for the part of the torrent the run went out of its way
// NOT to touch, which is precisely backwards for this project. Ranges cost
// what the run DID: one entry per contiguous stretch ordered, 222 bytes in
// the real sparse case and 17 in the degenerate one. The sparse case is where
// ranges lose, and losing by 99 bytes on a 600-byte record is the cheap half
// of the trade.
//
// The invariant worth holding onto: the range lengths sum to exactly the count
// Claimed reports. Two views of one set, and a test asserts they agree.
func (t *Torrent) ClaimedRanges() []PieceRange {
	t.claimMu.Lock()
	defer t.claimMu.Unlock()

	if len(t.claimedSeen) == 0 {
		return nil
	}
	idx := make([]int, 0, len(t.claimedSeen))
	for i := range t.claimedSeen {
		idx = append(idx, i)
	}
	sort.Ints(idx)

	out := []PieceRange{{Begin: idx[0], End: idx[0] + 1}}
	for _, i := range idx[1:] {
		// Ascending and de-duplicated by the map, so the only question at
		// each step is whether this piece continues the run being built.
		if last := &out[len(out)-1]; i == last.End {
			last.End++
			continue
		}
		out = append(out, PieceRange{Begin: i, End: i + 1})
	}
	return out
}

// Peers reports how many peers are connected and how many of them are seeds.
func (t *Torrent) Peers() (connected, seeds int) {
	stats := t.t.Stats()
	return stats.ActivePeers, stats.ConnectedSeeders
}

// TorrentFile renders this torrent as the bytes of a loadable .torrent file.
//
// The info dictionary is copied byte for byte from what the swarm actually
// sent - for a magnet, the BEP 9 metadata anacrolix kept exactly as it
// arrived - rather than re-encoded from the parsed struct, so the infohash of
// the saved file is the infohash of the torrent. That is the only property
// that makes saving one worth doing at all, and it is the property a test can
// check by loading the file back rather than by trusting the write.
//
// Everything OUTSIDE the info dictionary is synthesised, not recovered, and
// that difference is worth stating rather than implying: anacrolix's
// newMetaInfo stamps a creation date of now, a comment of "dynamic metainfo
// from client" and a created-by of "https://github.com/anacrolix/torrent"
// (torrent.go), because the original wrapper is simply not part of what a
// magnet fetches. The announce list is the real one - a magnet's tr=
// parameters, or a .torrent's own - so the saved file still finds its swarm.
// A .torrent written by torpeek is therefore a faithful TORRENT and an
// unfaithful FILE: it will never be byte-identical to the one somebody
// originally published, and nothing should compare it that way.
//
// carriedOverMetainfo is what keeps the result loadable rather than merely
// written. anacrolix always allocates PieceLayers and a v1 torrent fills none
// of it, while bencode's omitempty tests a map with IsNil - so an
// empty-but-non-nil map is not omitted, it is written out as
// "piece layers": de, and reads back as a torrent claiming v2 piece layers
// with no roots to match: exactly the state TOR-49 found AddTorrent
// rejecting file by file. UrlList is the same shape of problem one field
// over (newMetaInfo allocates it empty for a torrent with no web seeds); it
// is harmless to a loader, but it is a key describing something that does not
// exist, so it is dropped here rather than written.
func (t *Torrent) TorrentFile() ([]byte, error) {
	mi := carriedOverMetainfo(t.t.Metainfo())
	if len(mi.UrlList) == 0 {
		mi.UrlList = nil
	}
	if len(mi.InfoBytes) == 0 {
		// Unreachable through *Torrent, whose whole premise is that the
		// metadata has arrived (newTorrent reads the file list at
		// construction). Checked anyway, because the failure it guards
		// against is a .torrent with no info dictionary written to disk and
		// discovered only by whoever later tried to load it.
		return nil, fmt.Errorf("torrent %q has no info dictionary to save", t.t.Name())
	}

	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		return nil, fmt.Errorf("encode the torrent file: %w", err)
	}
	return buf.Bytes(), nil
}
