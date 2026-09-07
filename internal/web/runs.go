package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
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
	// Arrival is "this was the Nth torrent added to this server", 1-based,
	// and it is NOT QueuePosition by another name (TOR-156). See
	// runEntry.arrival for the whole distinction and why the two are
	// separate fields rather than one number reused.
	Arrival int
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
	priority Priority
	queueSeq uint64
	// arrival is "this was the Nth torrent added to this server", 1-based,
	// stamped once when the entry is CREATED and never touched again
	// (Server.arrivalSeq). TOR-156 is the ticket that added it, and its whole
	// content is that this is a different fact from queueSeq and from
	// QueuePosition, however alike three counters look sitting next to each
	// other:
	//
	//   - ARRIVAL ORDINAL, this field: the order torrents were added. Stable
	//     for the life of the row, unaffected by priority, and still true of
	//     a row that finished an hour ago. It is what the project owner asked
	//     to see - "в каком порядке торренты добавлены" - and the only one of
	//     the three that means anything on a row which is not waiting.
	//   - QUEUE POSITION (Server.queuePositionLocked): how many are ahead of
	//     you. Exists only while waiting, moves when a priority changes or
	//     when something ahead of you finishes, and is meaningless the moment
	//     a run starts.
	//   - queueSeq: the tiebreak WITHIN a priority level, re-stamped on every
	//     enqueue precisely so that a parked torrent coming back through
	//     DecideRun rejoins at the back rather than at the front (see the
	//     field above).
	//
	// Which is exactly why this cannot be queueSeq: a torrent that parks for
	// a file selection and is then decided gets a NEW queueSeq, and an
	// ordinal built on it would move "the third torrent I added" down the
	// list at the moment a person acted on it - the same staleness TOR-140
	// found in queuedAt, mirrored. It cannot be a row's index either:
	// keepFinishedRuns trims the oldest finished entries, and an ordinal
	// counted from a position would silently renumber every survivor when
	// they fall off. A counter that only ever goes up is the only shape that
	// survives both.
	//
	// PER PROCESS, and it has to be: the counter lives in this server, so a
	// run found only on disk - from a previous process, or one this one has
	// since trimmed - has no ordinal and reports zero. That is honest rather
	// than a gap. "The third torrent you added" is a fact about a session; a
	// number minted by yesterday's process and read back today would answer a
	// question nobody asked. A disk row already carries no id and no state
	// for the same reason, and the listing reports this one absent the same
	// way (RunSummary.Arrival).
	arrival   int
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
		// Read straight off the entry, unlike queuePosition beside it: an
		// arrival ordinal is a property of the entry (stamped at creation and
		// never revised), where a queue position is a property of the queue
		// and only the server can answer it. That asymmetry is the two facts
		// being different facts, in the shape of a function signature.
		Arrival: e.arrival,
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
		Stall: renderStall(p.Stall),
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

// ---------------------------------------------------------------------------
// TOR-152: running a row again. Two jobs, and they are not the same one.
//
// TOP UP is for a run that PRODUCED something and stopped at a ceiling. The
// frames it took are on disk and stay there; the next run over the same
// parameters fills the gaps and pays only for them (core.reusableFrames,
// core.serveFromCache). What was missing was never the plumbing - it was an
// affordance on the row, and a way to say "with a bigger ceiling this time".
// TopUp below is what a page is told BEFORE it presses that, and every field
// in it exists so the sentence on screen is a fact off disk rather than a
// promise.
//
// RETRY is for a run that produced NOTHING - a magnet whose metadata never
// arrived. There is no record on disk to reason from, so there is nothing to
// preview and nothing to raise: it is the same request again, at the same
// ceiling. Server.RetryRun is that one, and it lives in server.go because it
// needs the registry entry rather than the disk.

// TopUp is what GET /runs/{infohash}/sets/{params}/topup answers: whether
// this result set can be finished, what finishing it would be allowed to
// spend, and which ceiling stopped it last time.
//
// It reads every selected file's manifest, which is exactly what walkRuns
// refuses to do for the listing (see its doc: fifty runs would become fifty
// times the file reads). That is affordable here for the same reason
// fileDetail's frame-by-frame read is: this answers one row, once, when a
// person is looking at it.
type TopUp struct {
	InfoHash string `json:"infohash"`
	Params   string `json:"params"`

	// Count is the plan's frames per file, from the record's own Plan - the
	// denominator every Captured below is short of.
	Count int `json:"count"`
	// Files is one entry per file this set ever asked for, complete ones
	// included, so a page can show the multiplication rather than a total
	// that hides it (TOR-50: n is PER FILE).
	Files []TopUpFile `json:"files"`
	// Captured and Remaining are the totals across Files: points that have a
	// frame on disk, and points that do not. Remaining is what a top-up is
	// being priced for.
	Captured  int `json:"captured"`
	Remaining int `json:"remaining"`

	// SpentBytes is what the run that last wrote these manifests had received
	// by the time it finished each file (manifest.Cost.DownloadedBytes, taken
	// at its largest across the set). CeilingBytes is the ceiling it was held
	// to. Both are reported rather than derived on the page: the number a
	// person met is not the number in the flag's help, and this is the place
	// that can say so.
	SpentBytes   int64 `json:"spent_bytes"`
	CeilingBytes int64 `json:"ceiling_bytes,omitempty"`

	// StoppedBy is manifest.Cost.LimitHit as recorded - "budget" for the
	// run's own traffic ceiling, "time" for its own clock, "traffic_roof" for
	// the client-wide one, empty for a set nothing stopped (core.StopReason,
	// TOR-161). Limit is the same fact translated into the vocabulary this
	// package and app.js already share: "traffic", "time" or "roof". A
	// client should act on Limit, not StoppedBy - see LimitTime's own doc for
	// why a record from before TOR-161 can still say "budget" for a run the
	// clock actually stopped.
	StoppedBy string `json:"stopped_by,omitempty"`
	Limit     string `json:"limit,omitempty"`

	// OfferBytes is the traffic ceiling a top-up started from here would run
	// with, and ZERO MEANS THE ORDINARY CEILING rather than "nothing": a run
	// with no raise is exactly RunRequest.MaxBytes left unset, which is what
	// every other run on this server already does. RaiseHelps says which of
	// the two a zero is - see its own doc.
	OfferBytes int64 `json:"offer_bytes"`
	// RaiseHelps reports whether raising THIS RUN's ceiling is the right
	// lever at all. False for a set the clock stopped, and for one the
	// client-wide roof stopped: in both cases a bigger per-run traffic
	// allowance changes nothing, and offering one would be a lie a person
	// only finds out by spending. A top-up is still allowed in both cases -
	// the gaps may well fill under the ordinary ceiling if whatever was in
	// the way has moved - it simply carries no raise.
	RaiseHelps bool `json:"raise_helps"`

	// RoofBytes is the client-wide ceiling this server was started with
	// (core.Roof, -max-client-bytes), zero when there is none, and
	// RoofCapped says the offer above was cut down to it. The roof is NOT
	// something a top-up can walk around: every run is held to it at
	// runtime whatever this says (core.BudgetTracker.Exhausted asks it
	// first). Reporting it here only means the figure on screen never
	// promises more than the roof would allow.
	RoofBytes  int64 `json:"roof_bytes,omitempty"`
	RoofCapped bool  `json:"roof_capped,omitempty"`

	// Refused is why this set cannot be topped up at all, empty when it can.
	// A sentence rather than a code: every one of these is a dead end a
	// person has to read and act on themselves, and there is nothing for a
	// client to branch on.
	Refused string `json:"refused,omitempty"`
}

// TopUpFile is one video file's standing in the set: how many of the plan's
// points have a frame on disk, and how many were asked for.
type TopUpFile struct {
	Index int    `json:"index"`
	Path  string `json:"path"`
	// Captured counts points with a FRAME, never points the manifest merely
	// records - a failed point is recorded and has nothing to show, and
	// counting it would report a holed run as whole (the same miscount
	// TOR-110 and TOR-124 each fixed one level up).
	Captured int `json:"captured"`
	Planned  int `json:"planned"`
}

// Short reports whether this file still owes points.
func (f TopUpFile) Short() bool { return f.Captured < f.Planned }

// The three values TopUp.Limit takes, and the empty string for a set nothing
// stopped. Named here rather than written as literals in three places,
// because app.js branches on all three and the page's whole argument - which
// lever is the right one - rests on telling them apart.
const (
	// LimitTraffic is the run's own byte ceiling: the one a top-up raises.
	LimitTraffic = "traffic"
	// LimitTime is the run's own wall clock. Since TOR-161, core records this
	// as its own reason (core.StopTime) and absorbCost reads it directly -
	// the elapsed-against-limit comparison this used to rest on entirely is
	// now only a fallback, for a manifest.Cost written before that change,
	// when core.StopBudget covered both ceilings and "time" could not appear
	// in LimitHit at all.
	LimitTime = "time"
	// LimitRoof is the client-wide roof (core.StopRoof).
	LimitRoof = "roof"
)

// topUpFor reads one result set off disk and prices finishing it.
//
// ok=false means there is no such set - a malformed address, or no run.json
// where one was named - which is a 404 rather than a refusal. A set that
// exists but cannot be topped up comes back ok=true with Refused set: the
// difference matters to a page, which draws the sentence in the second case
// and nothing at all in the first.
//
// roofBytes is Config.RoofBytes, the client-wide ceiling this server was
// started with.
func topUpFor(root, infoHash, params string, roofBytes int64) (TopUp, bool) {
	if root == "" || !validInfoHash(infoHash) || !validParams(params) {
		return TopUp{}, false
	}

	layout := output.Layout{Root: root, InfoHash: infoHash, Params: params}
	record, ok := cache.LoadRun(layout.RunDir())
	if !ok {
		return TopUp{}, false
	}

	out := TopUp{InfoHash: infoHash, Params: params, Count: record.Plan.Count, RoofBytes: roofBytes}

	// A record that cannot say what it was asked for cannot be finished
	// either: the plan is what decides where the capture points fall, and a
	// run at a guessed plan would write a DIFFERENT result set (its params
	// key is a hash of exactly these fields) and reuse none of the frames
	// this one already has. Both of these read back zero on a record written
	// before the field existed - see cache.Run.Plan and cache.Run.Source.
	switch {
	case record.Plan.Count <= 0:
		out.Refused = "this run was recorded before torpeek kept the capture plan, " +
			"so there is no way to ask for the same frames again"
		return out, true
	case record.Source == "":
		out.Refused = "this run was recorded before torpeek kept the source, " +
			"so there is nothing to run again without pasting the link"
		return out, true
	case record.Selected == nil:
		out.Refused = "this run was recorded before torpeek kept which files were asked for, " +
			"so there is no way to tell a file that was never picked from one that failed"
		return out, true
	}

	byIndex := make(map[int]cache.File, len(record.Videos))
	for _, v := range record.Videos {
		byIndex[v.Index] = v
	}

	for _, index := range record.Selected {
		video, known := byIndex[index]
		if !known {
			// A selection naming a file the record does not list. Nothing
			// here can price it and nothing can capture it either.
			continue
		}
		file := TopUpFile{Index: index, Path: video.Path, Planned: record.Plan.Count}
		if m, loaded := cache.LoadManifest(layout.FileDir(index, video.Path)); loaded {
			file.Captured = capturedFrames(m)
			out.absorbCost(m.Cost)
		}
		if file.Captured > file.Planned {
			// A manifest holding more points than the plan asked for is a
			// set whose run.json and manifests disagree. Believing the
			// larger number would report a negative remainder.
			file.Captured = file.Planned
		}
		out.Files = append(out.Files, file)
		out.Captured += file.Captured
		out.Remaining += file.Planned - file.Captured
	}

	if len(out.Files) == 0 {
		out.Refused = "this run's record names no files that are still on disk"
		return out, true
	}
	if out.Remaining == 0 {
		out.Refused = "every frame this run asked for is already on disk"
		return out, true
	}

	out.price(roofBytes)
	return out, true
}

// capturedFrames counts the points of one manifest that actually have a
// frame, which is not len(Frames): a failed point is recorded too, and
// counting it would call a holed set whole (manifest.Frame.Shift).
func capturedFrames(m manifest.Manifest) int {
	n := 0
	for _, f := range m.Frames {
		if f.Shift != manifest.ShiftFailed && f.Path != "" {
			n++
		}
	}
	return n
}

// absorbCost folds one file's recorded cost into the set's, keeping the
// largest spending figure and the ceiling that goes with it.
//
// The LARGEST rather than the sum, and that is the whole reason this is a
// method rather than three assignments. manifest.Cost is the RUN's cost at
// the moment that file finished, not the file's own share of it (see its
// doc), so a two-file set records the same run's spending twice, at two
// moments. Summing would double it; the largest is the last honest reading
// this set has of what its run had received.
//
// The stop reason is absorbed with the roof winning over the run's own two,
// the same precedence BudgetTracker.Exhausted applies when both are true at
// once: the roof also stopped every other run on the client, and that is the
// fact a person needs first.
func (t *TopUp) absorbCost(c manifest.Cost) {
	if c.DownloadedBytes > t.SpentBytes {
		t.SpentBytes = c.DownloadedBytes
		t.CeilingBytes = c.LimitBytes
	}
	if c.LimitHit == "" {
		return
	}
	if t.StoppedBy == string(core.StopRoof) {
		return
	}
	t.StoppedBy = c.LimitHit
	switch {
	case c.LimitHit == string(core.StopRoof):
		t.Limit = LimitRoof
	case c.LimitHit == string(core.StopTime):
		// Since TOR-161 core reports the clock as its own reason - read
		// directly, nothing left to infer.
		t.Limit = LimitTime
	case c.LimitMS > 0 && c.ElapsedMS >= c.LimitMS:
		// Fallback for a manifest.Cost written before TOR-161, when core
		// folded the clock into "budget" too and comparing the elapsed
		// figure against the ceiling beside it was the only way to recover
		// which one actually happened. Never reached for a record written
		// after that change: the engine now checks the clock BEFORE the byte
		// ceiling (BudgetTracker.Exhausted), so a "budget" it writes never
		// again has ElapsedMS >= LimitMS - if the clock had also run out, it
		// would have written "time" instead. This case exists only to give
		// an old, ambiguous "budget" record a best-effort reading; it cannot
		// be certain, only likely.
		t.Limit = LimitTime
	default:
		t.Limit = LimitTraffic
	}
}

// shortFiles counts the files this set still owes points on - the ones
// request() narrows the run to, and therefore the file count the ceiling that
// run gets is scaled to (core.budgetFor, core.DefaultBudget).
func (t TopUp) shortFiles() int {
	n := 0
	for _, f := range t.Files {
		if f.Short() {
			n++
		}
	}
	return n
}

// price fills in what a top-up would be allowed to spend, whether raising
// this run's own ceiling is the right lever at all, and whether finishing is
// possible under the client-wide roof at all.
//
// The raise is withheld in exactly the two cases where it would be a lie: a
// run the CLOCK stopped, and one the CLIENT-WIDE ROOF stopped. Neither is
// this run's traffic ceiling, so neither moves when it moves - a person who
// pressed a button labelled "more traffic" on either would have consented to
// a cost for nothing (core.StopRoof's own doc makes this distinction, and it
// is carried end to end precisely so a client can act on it).
//
// A top-up is still offered in both cases, with no raise: the ordinary
// ceiling is what every other run gets, the frames already taken are still
// reused, and whatever was in the way - a full roof on a process since
// restarted, a swarm that was slow that afternoon - may simply have moved.
//
// THE ROOF IS ASKED FIRST, and it is the one question that can end this
// before any of that (TOR-166). A roof under the very least finishing can
// cost is not a figure to clamp to - it is a run that cannot be completed,
// and offering it anyway would be selling one more instalment of an allowance
// that never reaches the end. See core.TopUpFloor for why the prorated
// average is a sound floor precisely because it was a bad ceiling.
func (t *TopUp) price(roofBytes int64) {
	if floor := core.TopUpFloor(t.Remaining, t.Captured, t.SpentBytes); roofBytes > 0 && floor > roofBytes {
		t.Refused = "the client-wide traffic roof of " + mbAtMost(roofBytes) +
			" is below the " + mbAtLeast(floor) + " finishing this would cost at " +
			"the very least, so no run can complete it however often it is " +
			"started - raise -max-client-bytes, or ask for fewer frames"
		return
	}

	t.RaiseHelps = t.Limit == LimitTraffic || t.Limit == ""
	if !t.RaiseHelps {
		return
	}

	t.OfferBytes = core.TopUpBytes(t.Remaining, t.shortFiles(), t.Captured, t.SpentBytes)
	if roofBytes > 0 && t.OfferBytes > roofBytes {
		// Not a way around the roof, and not pretending to be: the run would
		// be stopped at the roof anyway (BudgetTracker.Exhausted asks it
		// first), so promising more than it on the screen would be a figure
		// nothing could honour. What the roof still has LEFT is not knowable
		// from here - that lives on the pool's own counter, which this
		// package has no handle on - so this bounds the offer by the whole
		// roof rather than by its headroom, and the page says the roof
		// applies rather than that this much is available.
		//
		// Safe to clamp only because the check above already established the
		// roof can cover what finishing costs at the very least; below that
		// this is a refusal, not a smaller offer.
		t.OfferBytes = roofBytes
		t.RoofCapped = true
	}
}

// mbAtLeast and mbAtMost write a byte figure the way a sentence a person
// reads wants it, in megabytes rather than mebibytes to match the units the
// flags and the page's own cost lines already use.
//
// They round in OPPOSITE directions, and which one a caller wants is decided
// by what a reader would be misled into believing. A cost rounds UP, so the
// sentence never names a smaller bill than the one that arrives; an allowance
// rounds DOWN, so it never names more headroom than there is. Rounding both
// the same way would let "8 MB is below 82 MB" print as "9 MB is below 82 MB"
// and quietly hand the reader a roof they do not have.
func mbAtLeast(bytes int64) string {
	const mb = 1000 * 1000
	return strconv.FormatInt((bytes+mb-1)/mb, 10) + " MB"
}

func mbAtMost(bytes int64) string {
	const mb = 1000 * 1000
	return strconv.FormatInt(bytes/mb, 10) + " MB"
}

// request turns a priced top-up into the run that fills its gaps.
//
// Three things travel that a fresh request does not carry, and each is why
// this is built here rather than by the page:
//
//   - FILES narrowed to the ones still short. A file that came out whole
//     would be re-inspected for nothing: its container would be read again
//     (pieces are discarded after every run, REQUIREMENTS.md 2.9) to reuse
//     frames that were never in doubt.
//   - WINDOW, the part of the recorded plan a request has no other field
//     for. core.ParamsKey hashes the count, the window, the profile, the
//     format and the sequential switch, so a top-up that took this server's
//     current flags instead would write a SIBLING result set - a new
//     directory, no frames reused, full price - which is the one outcome
//     this whole feature exists to avoid. Reproducing the record's own plan
//     is what guarantees the run lands in the same directory.
//   - MAXBYTES, the raise itself, which is zero for a set no raise would
//     help (see price).
func (t TopUp) request(record cache.Run) RunRequest {
	files := make([]string, 0, len(t.Files))
	for _, f := range t.Files {
		if f.Short() {
			files = append(files, strconv.Itoa(f.Index))
		}
	}

	return RunRequest{
		Source: record.Source,
		Mode:   record.Plan.Profile,
		Files:  files,
		Count:  record.Plan.Count,
		Window: &CaptureWindow{
			Start:      record.Plan.Start,
			End:        record.Plan.End,
			Format:     record.Plan.Format,
			Sequential: record.Plan.Sequential,
		},
		MaxBytes: t.OfferBytes,
	}
}

// resolveSet answers which result set an infohash alone names, for a caller
// that cannot name one itself.
//
// THE RULE IS listRuns' OWN, deliberately, and copying it here rather than
// inventing a second one is the point: a live row merges with a disk record
// only when its infohash names exactly ONE params directory, so that is
// exactly when a page's row and a directory on disk are known to be the same
// thing. An infohash naming two capture plans of one torrent is listed as two
// rows there and is refused here, rather than one of them being picked - a
// top-up that guessed would fill in the set the person was not looking at.
//
// It reads only directory entries and each run.json, the same cost walkRuns
// pays per directory, and only for one infohash.
func resolveSet(root, infoHash string) (string, bool) {
	if root == "" || !validInfoHash(infoHash) {
		return "", false
	}

	dirs, err := os.ReadDir(filepath.Join(root, infoHash))
	if err != nil {
		return "", false
	}

	found := ""
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		if _, ok := cache.LoadRun(filepath.Join(root, infoHash, dir.Name())); !ok {
			continue
		}
		if found != "" {
			return "", false
		}
		found = dir.Name()
	}
	return found, found != ""
}
