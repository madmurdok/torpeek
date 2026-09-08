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
// The numbers are intents, not raised straight to the swarm: Window is a
// claim and gets rounded up to whole pieces (WindowSize), which is why it
// reads as a byte size rather than a piece count - "about this much", and the
// torrent decides what that means. Readahead is only a hint about how far
// ahead to want data and is passed through as written (ReadaheadSize; TOR-95)
// - rounding a hint up turns it into a claim nobody made.
//
// What a capture point costs is set by how far the two reach past the wanted
// offset together, not by either alone. Measured on the local seeder, per
// frame: 2+2 MiB cost 4.65 MiB, 2+4 cost 5.76, 4+4 cost 6.98, 8+6 cost 9.61.
// Readahead beyond the window is the part that buys nothing - a 1 MiB and a
// 2 MiB readahead behind a 4 MiB window both cost 5.76, because the window
// already covers them - so min-time trails its readahead inside its claim
// rather than past it.
var (
	MinTime = Profile{
		Name: "min-time",
		// Deliberately no further than the window: past it, readahead
		// pulls pieces the decoder never asks for. 8 MiB behind a 2 MiB
		// window cost 8.26 MiB/frame against 4.65 for 2 MiB.
		Readahead: 2 << 20,
		// Four pieces on the acceptance torrent: enough in flight at once
		// to be the fast profile, where six put 20 capture points 22 MiB
		// over the 150 MB criterion for five seconds of the two minutes
		// allowed.
		Window:     4 << 20,
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

// ReadaheadSize resolves the readahead intent - and, unlike WindowSize, does
// NOT round it up to a whole piece (TOR-95).
//
// A window is a claim: the swarm is going to be asked for those bytes, so
// rounding it up to a whole piece costs nothing that was not already being
// paid. A readahead is only a hint about how far ahead the reader wants to
// keep prefetching - and alignUp's floor of one whole piece turned every
// reader into one that prefetches a full piece past its read position, on any
// torrent whose piece length exceeds the readahead intent (256 KiB on this
// torrent's 1 MiB pieces, i.e. every read).
//
// That manufactured prefetch reaches past what the caller actually asked for,
// which matters because of what TOR-88 found in internal/bridge: the bridge
// deliberately claims only the head of each range ffmpeg requests, not the
// whole range, to avoid ordering an entire file to satisfy a request that
// reads a few hundred kilobytes of it. A readahead rounded up to a full piece
// reaches outside that head-only claim - exactly where Window.Release's
// CancelPieces does not follow it, since Release only cancels the pieces the
// window itself claimed. So the rounding did not just prefetch more than
// asked; it prefetched into bytes no claim named and no Release could pull
// back. Passing the intent through unrounded keeps the hint a hint.
func (p Profile) ReadaheadSize(pieceLength int64) int64 {
	return p.Readahead
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
	t.noteClaimed(pieces)
	// The same order, recorded a second way and for a different question:
	// noteClaimed counts what the RUN ordered, this attributes it to whatever
	// capture point is open on this file (StartClaimLog). Two records because
	// the two are not derivable from each other - the counter is deliberately
	// de-duplicated across the whole run, so a header piece re-read at every
	// point would belong to the first point alone if this reused it.
	t.logClaim(file, pieces)

	return &Window{t: t, pieces: pieces}, nil
}

// noteClaimed records an order against the run's claimed figure (Claimed, in
// torrent.go), which is what acceptance criterion 2 is judged on.
//
// It sits inside Claim because Claim is the single funnel: every piece torpeek
// orders in this project is ordered here, by ReadRange below or by
// internal/bridge's Fetch, which is where everything ffmpeg asks for arrives.
// Two things deliberately do NOT reach this counter, and they are the whole
// reason it exists:
//
//   - a reader's readahead (Reader, below). That is a hint about how far ahead
//     to want data, not an order for particular pieces, and since TOR-95 it is
//     not even rounded up to one. Counting it would fold the manufactured
//     prefetch back into the figure meant to exclude it.
//   - anything a peer sends unasked. There is nothing to count at claim time,
//     which is the point: 1.5-74.5 MiB of it has been measured arriving on
//     unchanged code, some of it after Release (TOR-88).
func (t *Torrent) noteClaimed(pieces PieceRange) {
	t.claimMu.Lock()
	defer t.claimMu.Unlock()

	if t.claimedSeen == nil {
		t.claimedSeen = make(map[int]struct{}, pieces.Len())
	}
	for i := pieces.Begin; i < pieces.End; i++ {
		if _, seen := t.claimedSeen[i]; seen {
			continue
		}
		t.claimedSeen[i] = struct{}{}
		t.claimedByte += t.pieceBytes(i)
	}
}

// logClaim attributes one order to the capture point currently open on that
// file (StartClaimLog), in the file's own byte offsets. A no-op when no log is
// open - the container inspection at the start of a file, and anything a read
// does between points, belongs to no frame.
//
// It records the order UNCONDITIONALLY, unlike noteClaimed above, which skips
// a piece the run has already claimed. That difference is the point rather
// than an inconsistency: the counter is asking how much the run ordered in
// total, where this is asking what each point ordered, and every ffprobe call
// re-reads the container header. De-duplicating here would hand the header to
// whichever point happened to be taken first and tell the rest they came from
// nowhere near it.
func (t *Torrent) logClaim(file int, pieces PieceRange) {
	t.claimMu.Lock()
	defer t.claimMu.Unlock()

	if _, open := t.claimLog[file]; !open {
		return
	}
	begin, end, ok := t.fileBytesOf(file, pieces)
	if !ok {
		return
	}
	t.claimLog[file] = append(t.claimLog[file], [2]int64{begin, end})
}

// fileBytesOf clips a claimed piece range onto one file's own byte
// coordinates, reporting ok=false when the two do not overlap at all.
//
// Pieces do not respect file boundaries - a torrent's files are laid end to
// end and a piece spanning the join belongs to both - so the first and last
// piece of a claim commonly reach outside the file being read from. Clipping
// rather than dropping those is what keeps the record honest in the direction
// that matters: the bytes of THIS file that a claim covered, which is what
// the frame is a fact about. Nothing is lost by it, because a claim is whole
// pieces and expanding the clipped range back out lands on the very same ones
// (manifest.Frame.ByteRanges argues the round trip).
//
// Answered from the geometry cached at construction, so the arithmetic stays
// testable without a client - the same reason pieceBytes below is.
func (t *Torrent) fileBytesOf(file int, pieces PieceRange) (begin, end int64, ok bool) {
	if file < 0 || file >= len(t.files) || t.pieceLength <= 0 {
		return 0, 0, false
	}
	f := t.files[file]
	if f.Length <= 0 {
		return 0, 0, false
	}

	begin = int64(pieces.Begin)*t.pieceLength - f.Offset
	end = int64(pieces.End)*t.pieceLength - f.Offset
	if begin < 0 {
		begin = 0
	}
	if end > f.Length {
		end = f.Length
	}
	if end <= begin {
		// Entirely outside this file: a claim over one of the torrent's other
		// files, which is a real thing to see here because the claim funnel
		// is the torrent's and a run captures several files at once.
		return 0, 0, false
	}
	return begin, end, true
}

// pieceBytes is what one piece actually weighs: pieceLength for every piece
// but the torrent's last, which is whatever is left over.
//
// Answered from the geometry cached at construction rather than from the
// client, so the arithmetic stays testable without a swarm - the same reason
// pieceLength and numPieces are cached at all. The tail matters here and
// nowhere else: a claim covering the final piece of a torrent whose length is
// not a whole multiple of the piece length would otherwise report bytes that
// cannot exist.
func (t *Torrent) pieceBytes(i int) int64 {
	if t.pieceLength <= 0 || i < 0 || (t.numPieces > 0 && i >= t.numPieces) {
		return 0
	}
	if t.numPieces > 0 && i == t.numPieces-1 {
		if tail := t.length - int64(t.numPieces-1)*t.pieceLength; tail > 0 && tail < t.pieceLength {
			return tail
		}
	}
	return t.pieceLength
}

// Pieces reports which pieces this window covers.
func (w *Window) Pieces() PieceRange { return w.pieces }

// Release drops the window's claim. Pieces already downloaded stay on disk and
// cost nothing to reuse; what stops is asking peers for the rest.
//
// It does not touch the run's claimed figure (Claimed, in torrent.go), and
// must not: that figure is the set of pieces this run ordered at least once,
// and an order the run placed was placed whether or not it was later
// withdrawn. Releasing 44 pieces back down to nothing would report a run that
// asked for nothing - and on the acceptance torrent it would do so 82 times
// for a single piece.
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
	return addPeers(t.t, addrs...)
}

// addPeers is the same thing before there is a *Torrent to hang it off.
//
// Introducing a peer needs nothing from the metadata, which is why this is
// reachable while the info is still nil - the case where peers matter most,
// since on a magnet with no tracker and no DHT they are the only route the
// metadata itself can arrive by.
func addPeers(t *torrent.Torrent, addrs ...string) int {
	peers := make([]torrent.PeerInfo, 0, len(addrs))
	for _, a := range addrs {
		peers = append(peers, torrent.PeerInfo{
			Addr:    stringAddr(a),
			Source:  torrent.PeerSourceDirect,
			Trusted: true,
		})
	}
	return t.AddPeers(peers)
}

// stringAddr adapts a host:port string to the client's peer address interface.
type stringAddr string

func (s stringAddr) String() string { return string(s) }
