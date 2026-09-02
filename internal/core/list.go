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
	defer session.Close()

	downloaded := torrent.Downloaded()

	return Contents{
		Name:       torrent.Name(),
		InfoHash:   torrent.InfoHash(),
		Private:    torrent.Private(),
		Videos:     torrent.Videos(),
		Downloaded: downloaded,
	}, nil
}
