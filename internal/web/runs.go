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

// queueable reports whether this run's PRIORITY still decides anything -
// which is the one question Server.SetRunPriority, listing.go and
// runStateFieldsLocked all have to agree on, so it is written here once
// rather than as three copies of the same pair of states (TOR-140).
//
// True for exactly the two states that describe a torrent which has not
// started downloading and still might: queued, waiting for a slot, and
// needs-action, waiting for a person to say which files to capture -
// DecideRun puts that one straight back in the queue, carrying whatever
// priority it was given while it waited, so refusing to set one there would
// only mean setting it a second later.
//
// False for running and replaying, and that is the whole preemption
// decision in one method: a torrent already spending traffic is not
// something priority reaches. See SetRunPriority for the argument.
func (s RunState) queueable() bool {
	return s == RunQueued || s == RunNeedsAction
}

// Priority is how a person says which waiting torrent should go first
// (TOR-140). Higher runs earlier; equal priorities keep strict arrival
// order, so a registry nobody has reprioritised behaves exactly as the FIFO
// queue always did.
//
// A SMALL BAND OF NAMED LEVELS rather than a free integer, and rather than a
// position a person drags. Three reasons, in the order they decided it:
//
//   - A dragged position cannot live in this table. Since TOR-139 every
//     column sorts, and the table opens sorted by date NEWEST FIRST - the
//     reverse of queue order. Dragging a row to "third from the top" means
//     nothing when the rows are ordered by peers, or by name, and it would
//     mean the opposite of what it looks like under the default sort. A
//     level is a property of the torrent, so it reads the same whatever
//     order the rows happen to be in.
//   - A level survives new arrivals. A torrent added later can be sent to
//     the front by setting one value, where a drag can only place it
//     relative to rows that already exist - this ticket's own argument for
//     the number over the position.
//   - The band is bounded because an unbounded one has no legible scale.
//     "What does 7 mean" has no answer; high/normal/low does. Widening the
//     band later is a change to these constants and nothing else: the field
//     is an integer on the wire precisely so that adding levels does not
//     change the shape of anything.
//
// What it deliberately does NOT give: an arbitrary permutation of the
// queue. Within one level the order stays strictly FIFO, so three torrents
// all set high run in the order they arrived. Any one of them can still be
// made to go next - raise it, or lower the others - which is what the
// acceptance criterion asks for; producing every permutation by hand is the
// drag interaction this table cannot host.
type Priority int

const (
	// PriorityLow is "when there is nothing better to do": behind every
	// normal torrent, however much later they arrive.
	PriorityLow Priority = -1
	// PriorityNormal is what every run starts at, and the zero value on
	// purpose - a registry where nobody has touched a priority is a queue
	// ordered by arrival alone, byte for byte the behaviour before TOR-140.
	PriorityNormal Priority = 0
	// PriorityHigh is "this one next".
	PriorityHigh Priority = 1
)

// valid reports whether p is one of the levels this release has. Out of band
// is refused rather than clamped: a client sending 9 has misunderstood the
// scale, and silently storing 1 for it would hide that until the day the
// band widens and 9 starts meaning something else.
func (p Priority) valid() bool {
	return p >= PriorityLow && p <= PriorityHigh
}

// String names the level for a message a person reads.
func (p Priority) String() string {
	switch {
	case p > PriorityNormal:
		return "high"
	case p < PriorityNormal:
		return "low"
	default:
		return "normal"
	}
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
	// Name is the torrent's own name, confirmed by this run's metadata -
	// either the metadata pass a needs-action or single-file run paid for
	// (listThenRun, entry.contents) or the core.MetadataReady a run that
	// skipped listing gets from the engine directly (pump) - never guessed.
	// Empty until one of those has actually happened: a queued run, and a
	// running run still waiting on its own metadata, have nothing confirmed
	// to report here yet (TOR-117). RunSummary.ProvisionalName is what a
	// listing offers meanwhile, for a magnet whose source names it, and this
	// field is deliberately not it - a caller that wants the honest "do we
	// actually know" answer reads this one.
	Name string
	// Err is the failure this run ended on, in the form a client shows.
	Err       string
	QueuedAt  time.Time
	StartedAt time.Time
	EndedAt   time.Time

	// Live is this entry's most recent core.Progress reading, kept by
	// runEntry.applyProgress as heartbeats stream past - see that method's
	// own doc, and Live's own doc (listing.go) for exactly which entries
	// get one. Nil for a queued or needs-action entry, which has no client
	// yet, and for a running entry before its first heartbeat.
	Live *Live

	// Priority is the level this entry is queued at (TOR-140). Always a
	// real value - PriorityNormal is the zero value and the default - but
	// it only DECIDES anything while State.queueable() holds; a caller
	// deciding whether to show or offer it should ask that, not this.
	Priority Priority
	// QueuePosition is this entry's 1-based place in the waiting list, in
	// the order dispatch will actually take them: priority first, arrival
	// order within a level. Zero means this run is not waiting for a slot
	// at all - running, parked for a file selection, finished, replaying -
	// and is the reason it is not a pointer: "not in the queue" is a
	// position no entry can hold, so zero cannot be mistaken for one, and
	// listing.go omits the field entirely rather than reporting it (the
	// same absent-is-not-zero rule Live follows).
	QueuePosition int
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
	// name is the torrent's own confirmed name, set the moment it is learned
	// - by listThenRun from contents.Name, or by pump from a core.MetadataReady
	// a run that skipped listing gets straight from the engine (TOR-117).
	// Kept as its own field rather than always read out of contents because
	// the second path never populates contents at all (needsListingLocked
	// is false for it) - info() and runStateFieldsLocked need one place to
	// read regardless of which of the two happened.
	name string
	// listed records that the metadata pass has already happened for this
	// entry, so a torrent that parks and is then decided goes straight to
	// its run instead of paying for the same listing a second time. Distinct
	// from contents != nil on purpose: it is the question dispatch asks, and
	// asking it by the presence of a field would tie the two together for no
	// reason.
	listed   bool
	infoHash string
	err      error
	// priority is the level this entry waits at (TOR-140), and queueSeq is
	// its tiebreak within that level: a counter the server hands out every
	// time this entry ENTERS the waiting list, never at creation.
	//
	// The counter rather than queuedAt, and that distinction is load-bearing
	// rather than a micro-optimisation. A torrent that parked for a file
	// selection and was then decided re-enters the queue behind everything
	// waiting - that is what DecideRun has always done, by appending - but
	// its queuedAt is from when it was first accepted, minutes or hours
	// earlier. Ordering by queuedAt would silently promote every decided
	// torrent to the front of its level; ordering by the moment it actually
	// joined the queue reproduces today's behaviour exactly.
	priority  Priority
	queueSeq  uint64
	queuedAt  time.Time
	startedAt time.Time
	endedAt   time.Time
	// live is this run's most recent core.Progress reading, kept up to date
	// by applyProgress rather than anywhere else - see that method's own
	// doc. Nil until the run's first heartbeat.
	live *Live
}

// info snapshots the entry. The caller must hold the server's lock.
//
// queuePosition is passed in rather than read off the entry because it is
// not a property of the entry at all - it is where this entry sits in the
// server's waiting list, which only the server can answer
// (Server.queuePositionLocked). Server.infoLocked is the one place the two
// are joined, and every caller goes through it.
func (e *runEntry) info(queuePosition int) RunInfo {
	out := RunInfo{
		ID: e.id, State: e.state, Source: e.source, InfoHash: e.infoHash,
		Name:     e.name,
		QueuedAt: e.queuedAt, StartedAt: e.startedAt, EndedAt: e.endedAt,
		Live:     e.live,
		Priority: e.priority, QueuePosition: queuePosition,
	}
	if e.err != nil {
		out.Err = e.err.Error()
	}
	return out
}

// applyProgress folds one core.Progress heartbeat into the entry's live
// reading - peers, seeds, both rates and the availability reading. This is
// the registry's own answer to "keep the latest reading as it streams past"
// (TOR-136): the entry that already absorbs core.MetadataReady into
// infoHash/name (pump's event switch, server.go) is where a Progress
// reading belongs too, rather than a second place this state is kept.
//
// The caller must hold s.mu, the same rule every other entry mutation here
// follows.
//
// A whole new *Live replaces the old one rather than being edited field by
// field: info() hands its pointer to a reader running outside the lock, and
// replacing the pointer wholesale - never mutating what it already points
// to - is what keeps that snapshot honest even while a later heartbeat
// moves the entry on to a newer reading.
//
// pump's event switch does not yet call this for a core.Progress event: the
// one new case belongs in server.go, out of reach here (TOR-130 has that
// file in flight for the queue widening, and touching it was explicitly out
// of this task's scope). Everything downstream of the entry - info,
// listRuns, the wire shape itself - is ready for it the moment that one case
// is added:
//
//	case core.Progress:
//	    s.mu.Lock()
//	    entry.applyProgress(e)
//	    s.mu.Unlock()
func (e *runEntry) applyProgress(p core.Progress) {
	e.live = &Live{
		Peers: p.Peers, Seeds: p.Seeds,
		DownloadBps: p.DownloadRate, UploadBps: p.UploadRate,
		Swarm: renderAvailability(p.Swarm),
	}
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
