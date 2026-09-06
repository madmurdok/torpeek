package core

import (
	"context"
	"sync"
	"time"
)

// Budget caps what a whole run may spend. Both ceilings apply to the run, not
// to each file: a season pack should not multiply the limit by twenty
// (REQUIREMENTS.md section 2.6).
type Budget struct {
	// MaxBytes is the traffic ceiling. Zero means unlimited.
	MaxBytes int64
	// MaxTime is the wall-clock ceiling. Zero means unlimited.
	MaxTime time.Duration
	// WarnAt is the fraction of either ceiling that triggers a warning, so a
	// client can say "this may not finish" before it stops. Zero disables it.
	WarnAt float64
}

// Roof caps what a whole CLIENT may receive, across every run sharing it.
//
// Budget above is per run, and a per-run ceiling multiplies by the number of
// runs - which is the very thing Budget's own comment rejects one level down
// ("a season pack should not multiply the limit by twenty"). Since TOR-128 one
// long-lived client holds every public torrent, so runs can genuinely overlap,
// and four runs at 2 GB each are eight gigabytes of somebody's allowance
// however carefully each one was capped. The roof is the ceiling that does not
// multiply (REQUIREMENTS.md 2.6).
//
// It is a second ceiling, not a replacement: a run is held to its own budget
// AND to the roof over the client it shares, and the two stop it for
// different, separately reported reasons (StopBudget against StopRoof).
//
// # What it counts
//
// Received bytes, off the client-wide counter (swarm.Pool.Downloaded), for
// exactly the reason 2.6 gives for the per-run ceiling: a limit protects a
// link and a quota, and bytes already on the wire do not care who asked for
// them. It is therefore NOT acceptance criterion 2's figure, which judges what
// was ORDERED and is deliberately the opposite choice (section 8); the two
// must not be run together. Nor does it count what was UPLOADED - nothing does
// (swarm.Torrent.Uploaded), and the roof does not quietly change that.
//
// # What it does not have
//
// A time ceiling. A run has one because a run is a piece of work that should
// finish; a client is a process that stays up for hours, and a wall clock over
// that would only ever say "you have been running a while".
type Roof struct {
	// MaxBytes is the client-wide traffic ceiling, over the whole life of the
	// client. Zero means unlimited, and is the default - see
	// DefaultRoof for why torpeek does not pick a number here.
	MaxBytes int64
	// WarnAt is the fraction of the roof that triggers a warning, so a person
	// hears "everything is about to stop" before it does. Zero disables it.
	//
	// It matters more here than at the run level, and that is the whole
	// argument for the roof having one: a run that hits its own ceiling was
	// stopped by its own spending, while a run stopped by the roof may have
	// been stopped by the three runs beside it. Nobody should meet that for
	// the first time as four runs ending at once.
	WarnAt float64
}

// Reached reports whether received bytes have met the roof. An unlimited roof
// is never reached.
func (r Roof) Reached(received int64) bool {
	return r.MaxBytes > 0 && received >= r.MaxBytes
}

// DefaultRoof is no roof at all, and that is a decision rather than an
// omission.
//
// torpeek can estimate what a preview COSTS - 150 MB per file is a measured
// number, which is why section 7 can default a per-run ceiling and be right
// about it. It cannot estimate what a person's LINK OR QUOTA can afford: that
// is a fact about their connection and their month, and no number here would
// be derived from anything. The two failures are not symmetric either. A
// per-run ceiling guessed too low costs one incomplete preview, reported as
// such; a roof guessed too low stops every run on the client, including ones
// that had barely started, and reads as the tool being broken.
//
// So the roof is a number the person supplies - `-max-client-bytes`, the same
// config-and-flags route section 2.6 gives the per-run ceilings - and the
// mechanism ships set to unlimited.
//
// So it is unset in the shipped default, and the shipped default is also a
// WIDENED queue: web.DefaultMaxActiveTorrents is 5 (REQUIREMENTS.md 3.3).
// Those two facts together mean five runs can each spend up to one run's own
// ceiling with nothing over the client to stop them - the multiplication
// this type exists to close, left open on purpose.
//
// It is left open because the default targets a machine its owner is sitting
// at, where the link and the quota are theirs to spend, and because the
// alternative that was tried is worse: TOR-130 shipped a width of 1 that
// refused to start when widened without a roof, and that made the release's
// own feature cost two flags to reach. What replaced the refusal is
// disclosure - serveWeb prints what the width multiplies to
// (cli/web.go, queueWidthNotice), once, at the moment it becomes true.
//
// Where the quota is somebody else's, this is still the answer, and it is
// still the only ceiling that does not multiply. On a managed host the
// numbers to set are a roof here and a width of 1 (REQUIREMENTS.md 4.1).
func DefaultRoof() Roof {
	return Roof{WarnAt: defaultWarnAt}
}

// Budget defaults from REQUIREMENTS.md section 7.
const (
	// bytesPerFile is what one file's worth of frames is expected to cost.
	bytesPerFile = 150 << 20
	// maxRunBytes caps the total however many files there are. Exported as
	// MaxRunBytes below, because it is the per-run ceiling a caller has to
	// name when it multiplies by the number of concurrent runs (cli's own
	// startup line, TOR-149).
	maxRunBytes = 2 << 30
	// defaultRunTime is the wall-clock ceiling for a run.
	defaultRunTime = 10 * time.Minute
	// defaultWarnAt is how full a budget must be before a client hears about it.
	defaultWarnAt = 0.8
)

// DefaultBudget scales the traffic ceiling with the number of files to process
// and caps the total.
//
// A fixed number cannot serve both cases: one file needs perhaps 150 MB, while
// a twenty-episode pack would hit a fixed ceiling on the second episode and
// report nineteen files as unprocessed.
func DefaultBudget(files int) Budget {
	if files < 1 {
		files = 1
	}

	maxBytes := int64(files) * bytesPerFile
	if maxBytes > maxRunBytes {
		maxBytes = maxRunBytes
	}

	return Budget{
		MaxBytes: maxBytes,
		MaxTime:  defaultRunTime,
		WarnAt:   defaultWarnAt,
	}
}

// MaxRunBytes is the traffic ceiling one run is capped at when -max-bytes was
// left to scale with the file count. Exported so a caller can state what
// several concurrent runs add up to without writing the number itself
// (internal/cli's queue-width line, TOR-149); the scaling and the cap stay
// DefaultBudget's own business.
const MaxRunBytes = maxRunBytes

// Meter reports how much traffic a run has spent. swarm.Torrent satisfies it.
type Meter interface {
	Downloaded() int64
}

// BudgetTracker watches a run against both ceilings it is held to: its own
// budget, and the roof over the client it shares with every other run.
//
// Both live here rather than in two objects a caller would have to remember to
// ask separately, because "must this run stop, and why" is one question with
// one answer, and every place that asks it - the file loop, each capture
// point, the manifest - has to get the same answer for the same reason.
//
// The roof's state is not in here: it is the client's counter, which every
// run's tracker reads. So runs sharing a client agree about the roof without
// sharing anything, and the only per-tracker roof state is whether THIS run
// has already said its piece about it (each run warns its own client once).
//
// It reports rather than enforces: stopping is the orchestrator's job, since
// only it knows what "finish the frame in flight, then stop" means. The
// tracker's contract is to answer the same question the same way from any
// goroutine.
type BudgetTracker struct {
	budget Budget
	meter  Meter
	// roof and client are the second ceiling and the client-wide counter it
	// is read from. A nil client, or a zero Roof, is a tracker with no roof
	// over it - which is every caller that has no client to speak of.
	roof   Roof
	client Meter
	clock  func() time.Time
	start  time.Time

	mu          sync.Mutex
	warned      bool
	warnedAbove bool
}

// NewBudgetTracker starts tracking now, against the run's own budget and the
// roof over the client its torrent came out of.
//
// client is where the roof's figure comes from - the swarm.Pool this run
// attached to, whose Downloaded is every byte every client it owns has
// received. Nil means there is no roof to hold this run to, which is what a
// caller with no client passes; so does a zero Roof.
func NewBudgetTracker(budget Budget, meter Meter, roof Roof, client Meter) *BudgetTracker {
	return newBudgetTracker(budget, meter, roof, client, time.Now)
}

// newBudgetTracker takes a clock so time limits are testable without waiting.
func newBudgetTracker(budget Budget, meter Meter, roof Roof, client Meter,
	clock func() time.Time) *BudgetTracker {

	return &BudgetTracker{
		budget: budget,
		meter:  meter,
		roof:   roof,
		client: client,
		clock:  clock,
		start:  clock(),
	}
}

// Received is what the whole client has taken off the wire, or zero when this
// run is under no roof. It is not this run's spending and is never comparable
// with Spent - see Roof.
func (t *BudgetTracker) Received() int64 {
	if t.client == nil {
		return 0
	}
	return t.client.Downloaded()
}

// Spent reports traffic and elapsed time so far.
func (t *BudgetTracker) Spent() (bytes int64, elapsed time.Duration) {
	if t.meter != nil {
		bytes = t.meter.Downloaded()
	}
	return bytes, t.clock().Sub(t.start)
}

// Exhausted reports whether a ceiling has been reached, and which one.
// Limits are the ceilings this run is held to, for a record of it to name.
func (t *BudgetTracker) Limits() (bytes int64, wall time.Duration) {
	return t.budget.MaxBytes, t.budget.MaxTime
}

// RoofLimit is the client-wide ceiling this run is held to, zero when there is
// none, for a record of it to name beside the run's own.
func (t *BudgetTracker) RoofLimit() int64 {
	if t.client == nil {
		return 0
	}
	return t.roof.MaxBytes
}

// Exhausted reports whether a ceiling has been reached, and which one.
//
// The roof is asked FIRST, and the answer it gives is StopRoof rather than
// StopBudget. A run that crossed both at the same moment is reported as
// stopped by the roof on purpose: the roof is the ceiling that also stopped
// every other run on the client, and that is the fact the person needs. The
// two reasons never blur into one - see StopRoof.
func (t *BudgetTracker) Exhausted() (bool, StopReason) {
	if t.client != nil && t.roof.Reached(t.client.Downloaded()) {
		return true, StopRoof
	}

	bytes, elapsed := t.Spent()

	if t.budget.MaxBytes > 0 && bytes >= t.budget.MaxBytes {
		return true, StopBudget
	}
	if t.budget.MaxTime > 0 && elapsed >= t.budget.MaxTime {
		return true, StopBudget
	}
	return false, StopCompleted
}

// Warning returns a BudgetWarning the first time the run crosses WarnAt on a
// ceiling, and nil afterwards - a warning repeated every tick is noise a
// client would have to de-duplicate itself.
//
// The two ceilings latch separately, so a run hears about each of them once,
// and one call returns at most one warning: the roof's when it is due, because
// "everything on this client is about to stop" is the news that outranks "this
// run is about to stop", and the run's own on a later tick if it is still due.
// They are told apart by Scope, which every warning carries - a client that
// showed them the same way would be saying a run overspent when its neighbours
// did.
func (t *BudgetTracker) Warning() *BudgetWarning {
	if warning := t.roofWarning(); warning != nil {
		return warning
	}
	return t.runWarning()
}

// roofWarning is the client-wide half: this run's one notice that the roof
// over every run is filling up.
func (t *BudgetTracker) roofWarning() *BudgetWarning {
	if t.client == nil || t.roof.MaxBytes <= 0 || t.roof.WarnAt <= 0 {
		return nil
	}

	received := t.client.Downloaded()
	if float64(received) < float64(t.roof.MaxBytes)*t.roof.WarnAt {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.warnedAbove {
		return nil
	}
	t.warnedAbove = true

	// Elapsed is this run's, and LimitTime is zero because the roof has no
	// time ceiling to be a fraction of (see Roof). The bytes are the whole
	// client's, which is exactly why Scope has to travel with them: read as
	// this run's spending they would be a lie.
	_, elapsed := t.Spent()
	return &BudgetWarning{
		Scope:      LimitClient,
		SpentBytes: received,
		LimitBytes: t.roof.MaxBytes,
		Elapsed:    elapsed,
	}
}

// runWarning is the per-run half, unchanged in what it says or when.
func (t *BudgetTracker) runWarning() *BudgetWarning {
	if t.budget.WarnAt <= 0 {
		return nil
	}

	bytes, elapsed := t.Spent()

	crossed := false
	if t.budget.MaxBytes > 0 && float64(bytes) >= float64(t.budget.MaxBytes)*t.budget.WarnAt {
		crossed = true
	}
	if t.budget.MaxTime > 0 && float64(elapsed) >= float64(t.budget.MaxTime)*t.budget.WarnAt {
		crossed = true
	}
	if !crossed {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.warned {
		return nil
	}
	t.warned = true

	return &BudgetWarning{
		Scope:      LimitRun,
		SpentBytes: bytes,
		LimitBytes: t.budget.MaxBytes,
		Elapsed:    elapsed,
		LimitTime:  t.budget.MaxTime,
	}
}

// Remaining reports what is left of each ceiling. An unlimited ceiling reports
// its zero value, which callers should read as "no limit" rather than "spent".
func (t *BudgetTracker) Remaining() (bytes int64, remaining time.Duration) {
	spentBytes, elapsed := t.Spent()

	if t.budget.MaxBytes > 0 {
		if bytes = t.budget.MaxBytes - spentBytes; bytes < 0 {
			bytes = 0
		}
	}
	if t.budget.MaxTime > 0 {
		if remaining = t.budget.MaxTime - elapsed; remaining < 0 {
			remaining = 0
		}
	}
	return bytes, remaining
}

// Context returns a context that is cancelled when the time ceiling is
// reached, so work already blocked on the network stops with the run rather
// than after it.
//
// Traffic is deliberately not wired in here: it needs polling, and a run
// cancelled mid-write would lose the frame it was about to keep. The
// orchestrator checks Exhausted between capture points instead.
func (t *BudgetTracker) Context(parent context.Context) (context.Context, context.CancelFunc) {
	if t.budget.MaxTime <= 0 {
		return context.WithCancel(parent)
	}

	_, remaining := t.Remaining()
	if remaining <= 0 {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, cancel
	}
	return context.WithTimeout(parent, remaining)
}

// TopUpBytes is the traffic ceiling to give a run that is FINISHING an
// earlier, stopped one: enough for the capture points still missing, and
// deliberately not a byte more than that.
//
// # Why a computed absolute figure, and not the two obvious alternatives
//
// A person who accepts more traffic is not asking for "unlimited" - they are
// asking for enough to finish. That rules out both of the shapes this could
// have taken:
//
//   - A MULTIPLIER ("run it again at twice the ceiling") multiplies a number
//     the person does not have in their head. The ceiling a run actually met
//     is DefaultBudget's - bytesPerFile TIMES the files selected, capped -
//     not the per-file figure the flag's help mentions, which is exactly the
//     trap TOR-50 named one level up. Doubling 300 MB to finish four points
//     out of twenty asks for six times what the work costs, and it asks for
//     it in units nobody can check.
//   - A FRESH FIGURE TYPED BY HAND is the same guess moved onto the person.
//     They have no way to price a capture point; torpeek does, because the
//     run that stopped left the receipt on disk.
//
// So the figure is computed from what the stopped run actually did, and it is
// the LARGER of two independent estimates - never the sum, never an average:
//
//   - MEASURED. spent bytes divided by the points that spending bought, times
//     the points still owed. This is the only estimate that knows anything
//     about THIS torrent: its bitrate, its piece length, how far a seek has
//     to reach in this container. Note that spent includes each run's fixed
//     overhead - the container inspection and the keyframe index, re-read
//     from scratch because pieces are discarded after every run
//     (REQUIREMENTS.md 2.9) - so prorating it to the remaining points already
//     re-pays that overhead once. That is why there is no fudge factor here:
//     the average is not a per-point marginal cost, it is a per-point cost
//     with the fixed part folded in, which is the conservative direction.
//   - THE DEFAULT'S OWN SHARE. bytesPerFile is this project's measured figure
//     for one file's worth of frames (section 7); one point of a plan of
//     planned is therefore worth bytesPerFile/planned of it. This is the
//     floor, and it is what answers a run that stopped so early its measured
//     average is meaningless - two points captured off a cold swarm, say.
//
// Rounded UP to a whole MiB, because this number is shown to a person and
// consented to before it is spent, and a ceiling is not a measurement. Capped
// at maxRunBytes for the reason DefaultBudget caps there: a top-up is still
// one run, and no run may be allowed more than the most a fresh one could
// ever ask for. That cap is also what stops a pathological receipt (a run
// that spent a gigabyte on one frame) from turning a top-up into a blank
// cheque.
//
// WHAT IT DOES NOT DO is bound itself by the CLIENT-WIDE ROOF. The roof is
// not this run's to reason about - it is the ceiling over every run sharing
// one client, its figure lives on that client's own counter, and
// BudgetTracker.Exhausted asks it FIRST and answers StopRoof rather than
// StopBudget (see Roof). A top-up is held to it exactly as any other run is,
// with no way around it, and a caller that also wants to SHOW the roof
// alongside this figure should say so separately rather than fold the two
// into one number a reader could not take apart again.
//
// Zero out means there is nothing to top up: no points are missing, or the
// caller has no plan to reason from.
func TopUpBytes(remaining, planned int, spent int64, captured int) int64 {
	if remaining <= 0 {
		return 0
	}

	var measured int64
	if captured > 0 && spent > 0 {
		// One expression rather than a per-point figure multiplied back up:
		// integer division twice would round a 10.9 MB point down to 10 and
		// lose most of a megabyte per point on the way.
		measured = spent * int64(remaining) / int64(captured)
	}

	var share int64
	if planned > 0 {
		share = bytesPerFile * int64(remaining) / int64(planned)
	}

	want := measured
	if share > want {
		want = share
	}
	if want <= 0 {
		return 0
	}

	const mib = 1 << 20
	want = (want + mib - 1) / mib * mib
	if want > maxRunBytes {
		want = maxRunBytes
	}
	return want
}
