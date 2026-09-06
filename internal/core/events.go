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
}

// LimitScope says WHOSE ceiling a warning is about. The two are easy to
// confuse and mean opposite things to the person reading them: one says this
// run is spending a lot, the other says the client is, which may be entirely
// the doing of the runs beside it.
type LimitScope string

const (
	// LimitRun is the run's own budget (REQUIREMENTS.md 2.6, Budget).
	LimitRun LimitScope = "run"
	// LimitClient is the roof over every run sharing a client (Roof). A
	// warning at this scope is about traffic this run did not necessarily
	// cause and cannot stop, and the stop that follows it is StopRoof.
	LimitClient LimitScope = "client"
)

// BudgetWarning means a limit is close enough that the run may not finish.
type BudgetWarning struct {
	// Scope is whose ceiling this is. SpentBytes and LimitBytes are read
	// against it: at LimitRun they are this run's, at LimitClient they are
	// the whole client's, and a client that ignored this field would report
	// the second set as the first.
	Scope LimitScope
	// SpentBytes and LimitBytes are the traffic figure and its ceiling, at
	// Scope.
	SpentBytes int64
	LimitBytes int64
	// Elapsed is always this run's, at either scope: a run is the only thing
	// here with a start.
	Elapsed time.Duration
	// LimitTime is the time ceiling, and is zero at LimitClient - the roof
	// has none (see Roof).
	LimitTime time.Duration
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
	// StopBudget means this run reached a ceiling of its OWN - the traffic or
	// wall-clock limit sized for it (Budget, REQUIREMENTS.md 2.6).
	StopBudget StopReason = "budget"
	// StopRoof means the CLIENT reached the traffic roof over every run
	// sharing it (Roof), so this run stopped along with all of them.
	//
	// A separate reason rather than a second flavour of StopBudget, because
	// the difference is the only thing a person can act on. A run stopped for
	// StopBudget asked for too much and its own numbers say so; a run stopped
	// for StopRoof may have spent almost nothing and been stopped by its
	// neighbours, and its own limits are no explanation at all. Telling a
	// person to narrow a run that was already narrow is the wrong advice, and
	// one reason for both is how it would be given.
	StopRoof      StopReason = "traffic_roof"
	StopCancelled StopReason = "cancelled"
)
