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
