package core

import (
	"context"

	"github.com/madmurdok/torpeek/internal/swarm"
)

// Contents is what a torrent holds, without having taken a frame from it.
type Contents struct {
	Name     string
	InfoHash string
	Private  bool
	// Videos are the video files in torrent order, with the indices a file
	// selection names them by.
	Videos []swarm.FileInfo
	// Files is EVERY file the torrent holds, in torrent order - the .nfo, the
	// artwork, the subtitles, and the sample that swarm.SelectVideos drops
	// (TOR-180). Videos is the subset of it that can actually be captured, so
	// the two are not interchangeable: a selection still names an index out
	// of Videos, which is the only list runEntry.holdsFile validates a
	// decision against.
	//
	// Why both, rather than this one plus a per-entry flag: "which files can
	// be captured" is swarm's judgement, not a property of a path (a
	// video-extension file small enough beside its siblings is a sample and
	// is deliberately not in Videos), so a consumer must be told the answer
	// rather than re-derive it from a name. Sending the two lists is how it
	// is told - one is the whole truth about the torrent, the other is what
	// this program will do with it.
	Files []swarm.FileInfo
	// Downloaded is what learning this cost, which should be metadata only.
	Downloaded int64
}

// List reports what a torrent holds - every file, and which of them are
// video - and stops there.
//
// Choosing which file to look at should not cost what looking at it costs, so
// this fetches the metadata and detaches again without opening the bridge or
// touching a piece of payload.
//
// The client-wide traffic roof does NOT refuse a listing, and that is a
// decision rather than an oversight (Config.Roof). A listing touches no
// payload, so it adds nothing to the figure the roof is read against
// (BytesReadUsefulData counts piece data; metadata chunks are counted
// separately by anacrolix and are not in it). Refusing one would therefore
// cost a person the ability to LOOK at a torrent without saving them a byte -
// and looking is how somebody with a full roof decides what to raise it for.
func (e *Engine) List(ctx context.Context, cfg Config) (Contents, error) {
	src, err := swarm.ParseSource(cfg.Source)
	if err != nil {
		return Contents{}, Fail(CodeInternal, err)
	}

	pool, ownPool := poolFor(cfg)
	if ownPool {
		defer pool.Close()
	}

	attachment, err := pool.Attach(ctx, src, cfg.Swarm.Peers...)
	if err != nil {
		return Contents{}, Fail(CodeOf(err), err)
	}
	torrent := attachment.Torrent()

	// Detaching, never closing: a listing on a long-lived server runs against
	// the same shared client the run after it will use, and taking that
	// client down between the two would cost a restart - and, with DHT on,
	// a fresh bootstrap - for every torrent somebody adds.
	//
	// It still discards this torrent's pieces, and that is not merely tidy. A
	// metadata-only attachment fetches no piece, yet it can still leave a
	// directory behind: anacrolix builds a torrent's tree at OpenTorrent time
	// and creates every zero-length file in it
	// (storage.CreateNativeZeroLengthFile, which does its own MkdirAll), so a
	// release carrying one empty file leaves <DataDir>/<infohash>/ on disk
	// after a listing that downloaded nothing. Measured, not assumed: a
	// single-file fixture leaves nothing, a fixture with one zero-length file
	// leaves the tree.
	//
	// The CLI hides that behind the scratch directory it removes on exit
	// (Options.config). A long-lived server has no such umbrella and lists a
	// torrent every time someone adds one, into a data directory the operator
	// may well have named with -data - so choosing which file to capture must
	// not slowly fill it with empty trees.
	defer attachment.Detach()

	downloaded := torrent.Downloaded()

	return Contents{
		Name:       torrent.Name(),
		InfoHash:   torrent.InfoHash(),
		Private:    torrent.Private(),
		Videos:     torrent.Videos(),
		Files:      torrent.Files(),
		Downloaded: downloaded,
	}, nil
}
