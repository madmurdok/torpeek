package swarm

import (
	"github.com/anacrolix/torrent"
)

// Torrent is a torrent whose metadata is known, exposing only what torpeek
// needs. Piece-level operations arrive with the fetcher (TOR-4).
type Torrent struct {
	t       *torrent.Torrent
	private bool
	files   []FileInfo
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

	return &Torrent{t: t, private: private, files: files}
}

// Name is the torrent's display name.
func (t *Torrent) Name() string { return t.t.Name() }

// InfoHash identifies the torrent and keys the result cache.
func (t *Torrent) InfoHash() string { return t.t.InfoHash().HexString() }

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
func (t *Torrent) PieceLength() int64 {
	if info := t.t.Info(); info != nil {
		return info.PieceLength
	}
	return 0
}

// NumPieces is how many pieces the torrent is split into.
func (t *Torrent) NumPieces() int { return t.t.NumPieces() }

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
