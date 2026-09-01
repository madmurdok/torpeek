package bridge

import (
	"context"
	"fmt"
	"io"

	"github.com/madmurdok/torpeek/internal/swarm"
)

// FromTorrent adapts a torrent to Content, fetching through the profile's
// piece windows.
func FromTorrent(t *swarm.Torrent, p swarm.Profile) Content {
	return torrentContent{t: t, profile: p}
}

type torrentContent struct {
	t       *swarm.Torrent
	profile swarm.Profile
}

func (c torrentContent) Length(file int) (int64, error) {
	files := c.t.Files()
	if file < 0 || file >= len(files) {
		return 0, fmt.Errorf("file index %d out of range", file)
	}
	return files[file].Length, nil
}

func (c torrentContent) Name(file int) string {
	files := c.t.Files()
	if file < 0 || file >= len(files) {
		return "stream.bin"
	}
	return files[file].Name()
}

// Fetch claims the pieces at the head of the range, then streams it.
//
// Only the head is claimed, never the whole requested range. ffmpeg asks for
// "this offset to the end of the file" and then reads as little of it as it
// needs - claiming all of that would order the entire file from the swarm to
// satisfy a request that stops after a few hundred kilobytes. Measured: on a
// 7.3 MiB file, claiming the full range cost 100% of it to probe. Everything
// past the head is pulled by the reader's readahead as the client actually
// reads, which is the profile's job.
func (c torrentContent) Fetch(ctx context.Context, file int, off, length int64) (io.ReadCloser, error) {
	head := length
	if c.profile.Window > 0 && head > c.profile.Window {
		head = c.profile.Window
	}

	window, err := c.t.Claim(file, off, head, c.profile)
	if err != nil {
		return nil, err
	}

	r, err := c.t.Reader(ctx, file, c.profile)
	if err != nil {
		window.Release()
		return nil, err
	}
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		r.Close()
		window.Release()
		return nil, fmt.Errorf("seek to %d: %w", off, err)
	}

	return &windowedReader{
		Reader: io.LimitReader(r, length),
		closer: r,
		window: window,
	}, nil
}

// windowedReader ties the lifetime of a piece claim to the response body.
type windowedReader struct {
	io.Reader
	closer io.Closer
	window *swarm.Window
}

func (w *windowedReader) Close() error {
	err := w.closer.Close()
	w.window.Release()
	return err
}
