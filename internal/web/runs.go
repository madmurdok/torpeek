package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/madmurdok/torpeek/internal/core"
)

// RunState is where a run is in its life.
//
// Three kinds of state, not two. queued, running and replaying are live: the
// server is working on this run or about to be, and more events are coming
// on their own. done, failed and cancelled are final and never change again;
// a client can rely on that, a run it has seen end will not come back.
//
// needs-action is neither, and it is the first state that is neither. The
// torrent's file list has arrived and nothing whatsoever happens next until
// a person says which files to capture: no event is coming, nothing is being
// downloaded, and no timer will end the wait. It is also the only state that
// goes backwards - needs-action returns to queued when POST /runs/decide
// carries the selection - so "has this run finished" and "is this run doing
// something" are no longer each other's opposite. Ask final() for the first
// question and run_state's own active flag for the second.
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
	// RunNeedsAction means the torrent turned out to hold more than one video
	// file and is waiting for someone to say which of them to capture
	// (REQUIREMENTS.md section 3.3). The metadata pass that produced the list
	// is over; the capture has not begun.
	//
	// What it holds while it waits is nothing: no session, no context - the
	// listing's own was released when it parked - and above all not the
	// single slot, which went back to the queue before this state was
	// entered (listThenRun). That is the whole point of the state: the
	// torrent behind this one runs to completion while this one waits, and
	// the waiting can last as long as a person takes, because it costs a map
	// entry and a file list.
	//
	// It ends only by someone acting on it: POST /runs/decide puts it back in
	// the queue with a selection, or a cancel ends it for good. Nothing times
	// it out, deliberately - a bound would have to be either short enough to
	// throw away a torrent someone is still thinking about or long enough to
	// be no bound at all, and RunQueued already carries the same open-ended
	// promise (trim never drops a non-final entry).
	RunNeedsAction RunState = "needs-action"
)

// final reports whether the state can still change. needs-action is not
// final: it is a run that has not happened yet, and a client that treated it
// as over would stop showing a torrent that is only waiting to be told what
// to take.
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
	cleanup func()
	// contents is what the metadata pass found: the torrent's name, its
	// infohash and every video file in it, with the indices a selection
	// names them by. Set only for an entry that paid for a listing, and kept
	// afterwards because a decision has to be checked against the files the
	// torrent actually holds rather than against whatever a page that has
	// been open since yesterday sends. It is a handful of names and lengths;
	// no payload, no session, nothing that has to be closed.
	contents *core.Contents
	// listed records that the metadata pass has already happened for this
	// entry, so a torrent that parks and is then decided goes straight to
	// its run instead of paying for the same listing a second time. Distinct
	// from contents != nil on purpose: it is the question dispatch asks, and
	// asking it by the presence of a field would tie the two together for no
	// reason.
	listed    bool
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
