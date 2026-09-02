package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// RunState is where a run is in its life.
//
// queued, running and replaying are the live states; done, failed and
// cancelled are final and never change again. A client can rely on that: a
// run it has seen end will not come back.
type RunState string

const (
	// RunQueued means the run is accepted and waiting for the slot. Nothing
	// has been asked of the swarm yet and no context exists - which is why
	// cancelling a queued run is a registry edit rather than a cancel() call.
	RunQueued RunState = "queued"
	// RunRunning means the run owns the single slot.
	RunRunning RunState = "running"
	// RunReplaying means the run is being read back from disk rather than
	// downloaded - a cache hit's own state, distinct from both of the above.
	// It never queues (ReopenRun never touches s.waiting) because it never
	// competes for the network or the traffic budget the queue exists to
	// protect, and calling it "running" would say a run holds the swarm slot
	// it never asked for and never took. Brief in practice - replaying is a
	// handful of local file reads, done well before the state ends up
	// RunDone or RunFailed - but the honest answer if anything is asked
	// while it lasts.
	RunReplaying RunState = "replaying"
	// RunDone means the run's event stream ended normally.
	RunDone RunState = "done"
	// RunFailed means the run could not be started, or ended on a run-scoped
	// core.Failed (File < 0).
	RunFailed RunState = "failed"
	// RunCancelled means someone asked for it to stop - while queued, so it
	// never started, or while running, so it stopped early. Frames already
	// written stay on disk (REQUIREMENTS.md section 2.10).
	RunCancelled RunState = "cancelled"
)

// final reports whether the state can still change.
func (s RunState) final() bool {
	return s == RunDone || s == RunFailed || s == RunCancelled
}

// RunInfo is a snapshot of one registry entry, safe to read outside the lock.
type RunInfo struct {
	ID     string
	State  RunState
	Source string
	// InfoHash is filled in when the run's own metadata_ready announces it,
	// so it is empty for a queued run and for one that never got that far.
	// It is what ties a live run to the record it leaves on disk, which is
	// keyed by infohash and a hash of the plan (output.Layout), not by ID.
	InfoHash string
	// Err is the failure this run ended on, in the form a client shows.
	Err       string
	QueuedAt  time.Time
	StartedAt time.Time
	EndedAt   time.Time
}

// runEntry is one run in the registry: what it is, where it got to, and the
// two closures that end it.
type runEntry struct {
	id string
	// source is what a person is shown, which is not always req.Source: an
	// uploaded .torrent carries a server-side temp path nobody typed.
	source string
	req    RunRequest
	state  RunState
	// cancel is nil until the run starts - a queued run has no context yet.
	cancel context.CancelFunc
	// cancelled records that a stop was asked for, so pump can tell a run
	// that ended because someone cancelled it from one that simply finished.
	cancelled bool
	// cleanup runs once, after the run's event stream ends. It exists for
	// handleUploadTorrent's staged temp file: swarm.Open re-reads a file
	// Source from disk (AddTorrentFromFile) from inside the run's own
	// goroutine, not during the synchronous ParseSource call StartRun already
	// waited on - so the file has to outlive StartRun's return and can only
	// be removed once the run that might still be reading it is over. A run
	// that never starts - refused, cancelled while queued, or dropped when
	// the server closes - has nothing that could still be reading it, so its
	// cleanup runs at that moment instead.
	cleanup   func()
	infoHash  string
	err       error
	queuedAt  time.Time
	startedAt time.Time
	endedAt   time.Time
}

// info snapshots the entry. The caller must hold the server's lock.
func (e *runEntry) info() RunInfo {
	out := RunInfo{
		ID: e.id, State: e.state, Source: e.source, InfoHash: e.infoHash,
		QueuedAt: e.queuedAt, StartedAt: e.startedAt, EndedAt: e.endedAt,
	}
	if e.err != nil {
		out.Err = e.err.Error()
	}
	return out
}

// newRunID names a run for as long as the process holds it.
//
// Opaque and random rather than derived from the request: the natural key for
// a run on disk is its infohash plus a hash of the plan (output.Layout), and
// neither is knowable when a run is accepted - the infohash of a magnet
// arrives with the metadata, minutes later, and a queued run has not asked
// the swarm anything at all. An id that cannot exist at accept time cannot be
// the id a POST returns. So a live run's identity is this, and its identity
// on disk stays the layout key; RunInfo.InfoHash is what joins them once the
// run's own metadata_ready has said what it is.
func newRunID() string {
	var b [8]byte
	// crypto/rand.Read never returns an error (its own documentation): it
	// fills b entirely or the program is already dead.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
