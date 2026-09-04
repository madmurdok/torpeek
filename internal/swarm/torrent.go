package swarm

import (
	"bytes"
	"fmt"

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

	tor := &Torrent{t: t, private: private, files: files, numPieces: t.NumPieces()}
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
func (t *Torrent) Length() int64 { return t.t.Length() }

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
