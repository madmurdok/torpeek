package swarm

import (
	"context"
	"fmt"
	"io"

	"github.com/anacrolix/torrent"
)

// Profile is the single dial separating min-time from min-traffic. Keeping it
// one value rather than branching per call site means the two modes cannot
// drift apart as the pipeline grows.
type Profile struct {
	Name string

	// Readahead is how far beyond a read the client keeps prefetching.
	Readahead int64

	// Window is how many bytes around a wanted offset get raised priority.
	// It has to cover a keyframe plus enough of what follows to decode one
	// frame; too narrow and ffmpeg comes back for more, which costs a round
	// trip and usually another piece anyway.
	Window int64

	// Responsive hands data over as chunks arrive instead of waiting for the
	// whole piece to be verified. Faster, at the cost of trusting unverified
	// bytes - acceptable for a preview frame, not for a download.
	Responsive bool
}

// The two profiles from REQUIREMENTS.md section 2.5.
//
// What a profile actually controls is how much is claimed around each capture
// point and how eagerly it is handed to the decoder. It does not control how
// many requests are in flight - that is the torrent library's own scheduling,
// and section 2.5's "more parallel requests" is served by claiming a wider
// window, which is what gives the library more to ask for at once.
//
// The numbers are intents. What reaches the swarm is these rounded up to whole
// pieces, which is why they read as byte sizes rather than piece counts: the
// intent is "about this much", and the torrent decides what that means.
var (
	MinTime = Profile{
		Name:       "min-time",
		Readahead:  8 << 20,
		Window:     6 << 20,
		Responsive: true,
	}

	MinTraffic = Profile{
		Name:       "min-traffic",
		Readahead:  256 << 10,
		Window:     1 << 20,
		Responsive: false,
	}
)

// WindowSize is how many bytes to claim around a read, resolved against the
// torrent's own geometry rather than taken as written.
//
// Pieces are the unit the swarm actually trades in, so an intent expressed in
// bytes is rounded up to whole ones - claiming half a piece costs the whole
// piece anyway, and asking for less than one is worse than asking for one.
// Measured: a 512 KiB window on a torrent with 1 MiB pieces took 62.7s where a
// 1 MiB window took 3.4s, because the decoder kept coming back for the rest of
// a piece already being fetched.
func (p Profile) WindowSize(pieceLength int64) int64 {
	return alignUp(p.Window, pieceLength)
}

// ReadaheadSize resolves the readahead intent the same way.
func (p Profile) ReadaheadSize(pieceLength int64) int64 {
	return alignUp(p.Readahead, pieceLength)
}

func alignUp(intent, pieceLength int64) int64 {
	if pieceLength <= 0 {
		return intent
	}
	if intent <= pieceLength {
		return pieceLength
	}
	return ((intent + pieceLength - 1) / pieceLength) * pieceLength
}

// ProfileByName resolves a profile from a flag value.
func ProfileByName(name string) (Profile, error) {
	switch name {
	case MinTime.Name:
		return MinTime, nil
	case MinTraffic.Name:
		return MinTraffic, nil
	default:
		return Profile{}, fmt.Errorf("unknown profile %q, want %q or %q", name, MinTime.Name, MinTraffic.Name)
	}
}

// PieceRange is a half-open range of piece indices, [Begin, End).
type PieceRange struct {
	Begin, End int
}

// Len is how many pieces the range covers.
func (r PieceRange) Len() int { return r.End - r.Begin }

// PieceRangeFor maps a byte range inside a file onto the pieces that hold it.
//
// This is where the cost floor comes from: the range is expanded to whole
// pieces because a piece is the swarm's unit of exchange, so a 200 KB frame
// still costs at least one piece.
func (t *Torrent) PieceRangeFor(file int, off, length int64) (PieceRange, error) {
	if file < 0 || file >= len(t.files) {
		return PieceRange{}, fmt.Errorf("file index %d out of range", file)
	}
	f := t.files[file]

	pieceLen := t.PieceLength()
	if pieceLen <= 0 {
		return PieceRange{}, fmt.Errorf("torrent has no piece length yet")
	}

	if off < 0 {
		off = 0
	}
	if off > f.Length {
		off = f.Length
	}
	if length <= 0 || off+length > f.Length {
		length = f.Length - off
	}

	begin := int((f.Offset + off) / pieceLen)
	end := int((f.Offset+off+length-1)/pieceLen) + 1
	if length == 0 {
		end = begin + 1
	}
	if end > t.NumPieces() {
		end = t.NumPieces()
	}
	return PieceRange{Begin: begin, End: end}, nil
}

// Window is a byte range whose pieces are being fetched with raised priority.
// Release must be called, or those pieces keep competing with later windows.
type Window struct {
	t        *Torrent
	pieces   PieceRange
	released bool
}

// WindowFor is the piece range a profile claims to satisfy a read: the request
// widened to the profile's window, then rounded out to whole pieces.
//
// Kept separate from Claim so the geometry is testable without a client - it is
// the only thing the profile actually controls at fetch time.
func (t *Torrent) WindowFor(file int, off, length int64, p Profile) (PieceRange, error) {
	if window := p.WindowSize(t.pieceLength); window > length {
		length = window
	}
	return t.PieceRangeFor(file, off, length)
}

// Claim raises the priority of every piece backing a byte range and asks the
// client to download them.
func (t *Torrent) Claim(file int, off, length int64, p Profile) (*Window, error) {
	pieces, err := t.WindowFor(file, off, length, p)
	if err != nil {
		return nil, err
	}

	t.t.DownloadPieces(pieces.Begin, pieces.End)
	for i := pieces.Begin; i < pieces.End; i++ {
		t.t.Piece(i).SetPriority(torrent.PiecePriorityNow)
	}

	return &Window{t: t, pieces: pieces}, nil
}

// Pieces reports which pieces this window covers.
func (w *Window) Pieces() PieceRange { return w.pieces }

// Release drops the window's claim. Pieces already downloaded stay on disk and
// cost nothing to reuse; what stops is asking peers for the rest.
func (w *Window) Release() {
	if w == nil || w.released {
		return
	}
	w.released = true

	for i := w.pieces.Begin; i < w.pieces.End; i++ {
		w.t.t.Piece(i).SetPriority(torrent.PiecePriorityNone)
	}
	w.t.t.CancelPieces(w.pieces.Begin, w.pieces.End)
}

// Reader opens a seekable reader over one file, configured for the profile.
// The caller closes it.
func (t *Torrent) Reader(ctx context.Context, file int, p Profile) (torrent.Reader, error) {
	if file < 0 || file >= len(t.files) {
		return nil, fmt.Errorf("file index %d out of range", file)
	}

	r := t.t.Files()[file].NewReader()
	r.SetContext(ctx)
	r.SetReadahead(p.ReadaheadSize(t.pieceLength))
	if p.Responsive {
		r.SetResponsive()
	}
	return r, nil
}

// ReadRange fetches exactly [off, off+length) of a file, prioritising the
// pieces that hold it and dropping the claim afterwards.
//
// This is the primitive the HTTP bridge is built on: everything ffmpeg asks
// for arrives through here, which is what keeps a run's traffic to the windows
// it actually needs instead of the whole file.
func (t *Torrent) ReadRange(ctx context.Context, file int, off, length int64, p Profile) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("length must be positive, got %d", length)
	}

	w, err := t.Claim(file, off, length, p)
	if err != nil {
		return nil, err
	}
	defer w.Release()

	r, err := t.Reader(ctx, file, p)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek to %d: %w", off, err)
	}

	buf := make([]byte, length)
	n, err := io.ReadFull(r, buf)
	if err != nil && !(err == io.ErrUnexpectedEOF && n > 0) {
		return nil, fmt.Errorf("read %d bytes at %d: %w", length, off, err)
	}
	return buf[:n], nil
}

// AddPeers introduces peers by address, for cases where no tracker or DHT will
// hand them over.
func (t *Torrent) AddPeers(addrs ...string) int {
	peers := make([]torrent.PeerInfo, 0, len(addrs))
	for _, a := range addrs {
		peers = append(peers, torrent.PeerInfo{
			Addr:    stringAddr(a),
			Source:  torrent.PeerSourceDirect,
			Trusted: true,
		})
	}
	return t.t.AddPeers(peers)
}

// stringAddr adapts a host:port string to the client's peer address interface.
type stringAddr string

func (s stringAddr) String() string { return string(s) }
