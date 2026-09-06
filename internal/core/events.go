// Package core is the silent library core: it owns the run, orchestrates the
// other packages and emits a typed event stream. It never prints and knows
// nothing about how results are displayed, so the TUI, the web UI and any
// future service can all sit on top of it as equal clients.
//
// Requirements: section 3.1.
package core

import (
	"time"

	"github.com/madmurdok/torpeek/internal/probe"
	"github.com/madmurdok/torpeek/internal/swarm"
)

// Event is something that happened during a run. The set is closed: clients
// switch over it, so an unhandled case is a compile-time question rather than
// a silently ignored string.
type Event interface {
	event()
}

// MetadataReady means the torrent's contents are known and the video files
// have been picked out. Nothing has been downloaded beyond the metadata.
type MetadataReady struct {
	Name     string
	InfoHash string
	Private  bool
	// Videos are every video file the torrent holds, in torrent order.
	Videos []swarm.FileInfo
	// Selected are the torrent indices actually being worked on. It equals
	// every entry of Videos unless the run named a subset; a client showing
	// progress needs the number it is counting towards, not the number the
	// torrent happens to contain.
	Selected []int
	// BlindDHT reports that DHT was used before the private flag could be
	// checked - only possible for a magnet with no trackers. Surfaced rather
	// than hidden, because on a private tracker it matters.
	BlindDHT bool
}

// FileStarted means work has begun on one video file and what it contains is
// now known.
type FileStarted struct {
	File  int
	Path  string
	Media probe.MediaInfo
	// Plan is the timestamps frames will be taken at, before any shifting.
	Plan []time.Duration
}

// FrameReady means a frame is on disk and can be shown immediately - the
// requirement that the UI fills in as it goes rather than after the run.
type FrameReady struct {
	File  int
	Index int
	// Requested is where the frame was asked for; Actual is where it came
	// from. They differ when a shift happened, and Shift says why.
	Requested time.Duration
	Actual    time.Duration
	Shift     ShiftReason
	Path      string
	Width     int
	Height    int
}

// FrameSkipped means a capture point produced nothing and why.
type FrameSkipped struct {
	File      int
	Index     int
	Requested time.Duration
	Code      ErrorCode
	Reason    string
}

// Progress is the periodic heartbeat a progress bar or piece map is drawn
// from. It carries no meaning a client must act on.
type Progress struct {
	File           int
	FramesDone     int
	FramesTotal    int
	DownloadedByte int64
	Elapsed        time.Duration
	Peers          int
	Seeds          int

	// Swarm is this heartbeat's live availability reading - what the SWARM
	// HOLDS right now, never what this run has ordered from it. That other
	// question is Done.ClaimedByte/ClaimedPieces/ClaimedRanges, answered once
	// at the end rather than per heartbeat; two figures of the same shape
	// meaning opposite things is the trap to avoid wherever both reach a
	// reader (TOR-111), so a client must keep the two visually apart.
	//
	// Nil means unknown, never zero copies. That is true for two different
	// reasons a client must not conflate: swarm.Torrent.Availability's own
	// doc records that a client does not dial anyone until something asks
	// for bytes - measured, "PeerConns stayed empty for twenty seconds after
	// adding a live seeder, and filled the moment a range was asked for" -
	// so a torrent that has not started fetching yet has nothing here even
	// while running; and a queued run publishes no Progress at all, since it
	// has no client to read from, which already reports unknown for free by
	// never setting this field.
	Swarm *SwarmAvailability
}

// SwarmAvailability is a live copies-per-piece reading, built from
// swarm.Torrent.Availability(). See newSwarmAvailability for how the two
// unknown-vs-zero cases above are told apart.
//
// CopiesPerPiece is the mean number of connected peers holding each piece,
// which is swarm.Availability's own unit - NOT a percentage, and it commonly
// exceeds 1.0. 0.8 means pieces are missing from the swarm; 3.2 means it is
// healthy. A client must label it as a copy count rather than let a bare
// number be misread as a fraction.
//
// Unavailable is how many of NumPieces no connected peer holds at all - zero
// on a healthy swarm, and what makes a capture point need shifting when it
// is not.
type SwarmAvailability struct {
	CopiesPerPiece float64
	Unavailable    int
	NumPieces      int
}

// swarmSnapshot is the part of swarm.Availability newSwarmAvailability needs.
// Named here rather than taken concretely so the conversion can be tested
// without standing up a swarm - the same reasoning engine.go's own
// availabilityMap interface uses, and swarm.Availability satisfies both
// without change.
type swarmSnapshot interface {
	Known() bool
	NumPieces() int
	Unavailable() int
	At(piece int) int
}

// newSwarmAvailability turns one swarm.Availability sample into the shape a
// live reader gets, or nil when the sample is not yet known.
//
// Known() is swarm.Availability's own line between ignorance and a fact - "a
// connected peer is not the same as a peer that has said what it holds" - and
// this defers to it entirely rather than re-deciding the question from peer
// or piece counts here.
func newSwarmAvailability(a swarmSnapshot) *SwarmAvailability {
	if !a.Known() {
		return nil
	}
	n := a.NumPieces()
	if n == 0 {
		return nil
	}

	sum := 0
	for piece := 0; piece < n; piece++ {
		sum += a.At(piece)
	}

	return &SwarmAvailability{
		CopiesPerPiece: float64(sum) / float64(n),
		Unavailable:    a.Unavailable(),
		NumPieces:      n,
	}
}

// BudgetWarning means a limit is close enough that the run may not finish.
type BudgetWarning struct {
	SpentBytes int64
	LimitBytes int64
	Elapsed    time.Duration
	LimitTime  time.Duration
}

// FileDone means one video file's frames, contact sheet and manifest are
// complete.
type FileDone struct {
	File         int
	Path         string
	Frames       int
	Skipped      int
	ManifestPath string
	SheetPath    string
}

// Done is the last event of a successful run.
type Done struct {
	Reason         StopReason
	Files          int
	Frames         int
	DownloadedByte int64
	Elapsed        time.Duration

	// ClaimedByte and ClaimedPieces are what the run ORDERED from the swarm -
	// the total size of the distinct pieces some Claim covered, and how many
	// there were. DownloadedByte above is what arrived, whoever asked for it.
	//
	// The two are not the same number and the gap between them is the
	// interesting one: on the acceptance torrent min-traffic claims a
	// deterministic 44 pieces every run while what arrives has been measured
	// from 45.5 to 118.6 MiB on byte-identical code, so a verdict taken on
	// arrivals is a verdict the swarm casts (docs/tor-88-min-traffic-spread.md,
	// TOR-94). Acceptance criterion 2 is judged on the claimed figure and
	// reports the other two beside it.
	//
	// A measurement seam, not a protocol. It stops here, where the acceptance
	// harness reads it: internal/wire's event JSON, the CLI summary and the
	// manifest's cost record (REQUIREMENTS.md 2.8) all still carry the one
	// traffic number a person spending bandwidth asked for, and a client is
	// free to ignore these two. Both are zero for a run served from cache,
	// which claimed nothing because it went nowhere.
	ClaimedByte   int64
	ClaimedPieces int

	// ClaimedRanges is where those pieces are: the same set ClaimedPieces
	// counts, coalesced into ascending half-open ranges, so a client can say
	// the 44 were spread across the film rather than bunched at the front.
	// The range lengths sum to ClaimedPieces exactly - two views of one set.
	//
	// Nil for a run served from cache, which claimed nothing because it went
	// nowhere. Unlike the two figures above this one does reach disk, in the
	// run record (cache.Run.Claimed); swarm.Torrent.ClaimedRanges carries the
	// argument for the shape, costed against the record it produces.
	ClaimedRanges []swarm.PieceRange

	// TorrentPath is where the run kept its own .torrent, so a client can
	// offer it without knowing anything about the output layout - the same
	// service FileDone's ManifestPath and SheetPath do for one video file.
	// It rides on Done rather than beside them because there is one per RUN,
	// not one per file, and Done is the only run-scoped event a finished run
	// is guaranteed to end on.
	//
	// Empty means this run has none to offer: a run whose write failed (see
	// Warnings), or a cached run recorded before torpeek kept one at all. A
	// client must treat the empty case as "no link", never as "the file is
	// at the usual place".
	TorrentPath string

	// Warnings names what this run could not produce while still producing
	// what it was asked for - a .torrent it failed to write, today.
	//
	// It exists because the alternative was worse and shipped once: a
	// run-scoped Failed. That is the same event a source that cannot be
	// opened produces, so a run whose twenty frames all landed read as
	// "failed" in the panel because a 37 KB side artefact did not (TOR-79).
	// A warning on the event a finished run is guaranteed to end on says
	// both true things at once - it finished, and this is missing - where
	// Failed said one false one.
	//
	// Frames, manifests and run.json are not candidates for this: a run that
	// cannot write those has not produced what it was asked for, and
	// reporting that as a warning would be the same lie in the other
	// direction.
	Warnings []string
}

// Failed is the last event of a run that could not continue. Per-file
// problems are FrameSkipped or a file-scoped Failed; a run-scoped Failed has
// File set to -1.
type Failed struct {
	File int
	Code ErrorCode
	Err  error
}

func (MetadataReady) event() {}
func (FileStarted) event()   {}
func (FrameReady) event()    {}
func (FrameSkipped) event()  {}
func (Progress) event()      {}
func (BudgetWarning) event() {}
func (FileDone) event()      {}
func (Done) event()          {}
func (Failed) event()        {}

// ShiftReason says why a frame did not come from where it was asked for. The
// two are easy to confuse and mean different things to a viewer: one is about
// the swarm, the other about the picture.
type ShiftReason string

const (
	// ShiftNone means the frame came from the requested timestamp.
	ShiftNone ShiftReason = ""
	// ShiftUnavailable means the pieces there were not held by any peer, so a
	// nearby available position was used instead. Decided before any traffic
	// was spent.
	ShiftUnavailable ShiftReason = "shifted"
	// ShiftBlank means the frame there was black or flat, so a neighbouring
	// keyframe was used. Decided after decoding.
	ShiftBlank ShiftReason = "stepped"
)

// StopReason says why a run ended.
type StopReason string

const (
	StopCompleted StopReason = "completed"
	StopBudget    StopReason = "budget"
	StopCancelled StopReason = "cancelled"
)
