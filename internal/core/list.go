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
	// Downloaded is what learning this cost, which should be metadata only.
	Downloaded int64
}

// List reports a torrent's video files and stops there.
//
// Choosing which file to look at should not cost what looking at it costs, so
// this fetches the metadata and closes the session without opening the bridge
// or touching a piece of payload.
func (e *Engine) List(ctx context.Context, cfg Config) (Contents, error) {
	src, err := swarm.ParseSource(cfg.Source)
	if err != nil {
		return Contents{}, Fail(CodeInternal, err)
	}

	session, torrent, err := swarm.Open(ctx, cfg.Swarm, src)
	if err != nil {
		return Contents{}, Fail(CodeOf(err), err)
	}
	defer func() {
		// Order matters, for the reason Run's own defer spells out:
		// DiscardPieces is only safe once Close has returned.
		session.Close()

		// A metadata-only session fetches no piece, yet it can still leave a
		// directory behind: anacrolix builds a torrent's tree at
		// OpenTorrent time and creates every zero-length file in it
		// (storage.CreateNativeZeroLengthFile, which does its own MkdirAll),
		// so a release carrying one empty file leaves <DataDir>/<infohash>/
		// on disk after a listing that downloaded nothing. Measured, not
		// assumed: a single-file fixture leaves nothing, a fixture with one
		// zero-length file leaves the tree.
		//
		// The CLI hides that behind the scratch directory it removes on exit
		// (Options.config). A long-lived server has no such umbrella and
		// lists a torrent every time someone adds one, into a data directory
		// the operator may well have named with -data - so choosing which
		// file to capture must not slowly fill it with empty trees.
		_ = session.DiscardPieces(torrent.InfoHash())
	}()

	downloaded := torrent.Downloaded()

	return Contents{
		Name:       torrent.Name(),
		InfoHash:   torrent.InfoHash(),
		Private:    torrent.Private(),
		Videos:     torrent.Videos(),
		Downloaded: downloaded,
	}, nil
}
