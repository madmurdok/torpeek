package swarm

// Availability is how many connected peers hold each piece, as it stood at one
// moment. It is a snapshot rather than a live view: peers arrive and leave
// constantly, and a decision made from a value that changes underneath is
// worse than one made from a value with a known age.
type Availability struct {
	counts      []int
	peers       int
	pieceLength int64
	length      int64
}

// Availability samples what the connected peers claim to hold.
//
// A peer that announced HaveAll counts for every piece, which is what the
// library's own bitmap already expands to once the metadata is known.
//
// It reports nothing until the client has a reason to talk to anyone. A
// torrent with every piece at priority None never dials the peers it knows
// about - measured: PeerConns stayed empty for twenty seconds after adding a
// live seeder, and filled the moment a range was asked for. So a caller that
// wants to decide something from availability has to have started fetching
// first; in a run, probing the container does that before any capture point
// needs judging.
func (t *Torrent) Availability() Availability {
	counts := make([]int, t.numPieces)

	conns := t.t.PeerConns()
	for _, conn := range conns {
		pieces := conn.PeerPieces()
		if pieces == nil {
			continue
		}
		for i := range counts {
			if pieces.ContainsInt(i) {
				counts[i]++
			}
		}
	}

	return Availability{
		counts:      counts,
		peers:       len(conns),
		pieceLength: t.pieceLength,
		length:      t.t.Length(),
	}
}

// Peers is how many connections the sample folded. Zero means the answer is
// "nobody is connected", not "nobody has anything".
func (a Availability) Peers() int { return a.peers }

// NumPieces is the length of the map.
func (a Availability) NumPieces() int { return len(a.counts) }

// At is how many peers hold one piece. An index outside the torrent is zero.
func (a Availability) At(piece int) int {
	if piece < 0 || piece >= len(a.counts) {
		return 0
	}
	return a.counts[piece]
}

// AtOffset is the availability of the piece holding a byte offset.
func (a Availability) AtOffset(off int64) int {
	if a.pieceLength <= 0 {
		return 0
	}
	return a.At(int(off / a.pieceLength))
}

// OverRange is the least available piece across a byte range, which is the
// number that decides whether a region can be fetched at all: a range is only
// as reachable as its scarcest piece. The range is clamped to the torrent, and
// an empty or backwards range reports zero.
func (a Availability) OverRange(start, end int64) int {
	if a.pieceLength <= 0 || end <= start {
		return 0
	}
	if start < 0 {
		start = 0
	}
	if a.length > 0 && end > a.length {
		end = a.length
	}
	if end <= start {
		return 0
	}

	first := int(start / a.pieceLength)
	last := int((end - 1) / a.pieceLength)

	least := -1
	for piece := first; piece <= last; piece++ {
		if n := a.At(piece); least < 0 || n < least {
			least = n
		}
	}
	if least < 0 {
		return 0
	}
	return least
}

// OverFileRange is OverRange for an offset inside one file, which is how
// every caller above the swarm thinks: ffprobe reports positions within the
// file it was handed, while pieces are numbered across the whole torrent.
func (a Availability) OverFileRange(f FileInfo, off, length int64) int {
	if off < 0 {
		off = 0
	}
	if off > f.Length {
		return 0
	}
	if off+length > f.Length {
		length = f.Length - off
	}
	return a.OverRange(f.Offset+off, f.Offset+off+length)
}

// PieceLength is the torrent's piece size, which is the unit anything about
// availability is really measured in.
func (a Availability) PieceLength() int64 { return a.pieceLength }

// Known reports whether the sample carries information at all.
//
// A map read before any peer has sent its bitfield says every piece is
// missing, which is ignorance rather than a fact about the swarm - and a
// connected peer is not the same as a peer that has said what it holds, so
// counting connections is not enough to tell the two apart.
func (a Availability) Known() bool {
	if a.peers == 0 {
		return false
	}
	for _, n := range a.counts {
		if n > 0 {
			return true
		}
	}
	return false
}

// Unavailable counts the pieces no connected peer holds. On a healthy swarm it
// is zero; anything else is what makes capture points need shifting.
func (a Availability) Unavailable() int {
	missing := 0
	for _, n := range a.counts {
		if n == 0 {
			missing++
		}
	}
	return missing
}

// Coarse summarises availability across one file as a fixed number of buckets,
// for a manifest to record or a UI to draw. Each bucket carries its scarcest
// piece, for the same reason OverRange does: a stretch of the file is only as
// reachable as its least held piece.
//
// Buckets are capped at the number of pieces the file spans, so a short file
// does not get a map finer than the data it describes.
func (a Availability) Coarse(f FileInfo, buckets int) []int {
	if buckets <= 0 || f.Length <= 0 || a.pieceLength <= 0 {
		return nil
	}

	spanned := int((f.Offset+f.Length-1)/a.pieceLength) - int(f.Offset/a.pieceLength) + 1
	if buckets > spanned {
		buckets = spanned
	}

	out := make([]int, buckets)
	for i := range out {
		start := f.Offset + f.Length*int64(i)/int64(buckets)
		end := f.Offset + f.Length*int64(i+1)/int64(buckets)
		out[i] = a.OverRange(start, end)
	}
	return out
}
