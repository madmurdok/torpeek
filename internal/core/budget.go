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
// Checked in order roof, clock, bytes - and the order IS the policy for what
// a run reports when more than one ceiling is true at once, decided once
// here rather than left for every consumer to re-derive:
//
//   - The roof is asked FIRST, and the answer it gives is StopRoof rather
//     than StopBudget or StopTime. A run that crossed its own ceiling and the
//     roof at the same moment is reported as stopped by the roof on purpose:
//     the roof is the ceiling that also stopped every other run on the
//     client, and that is the fact the person needs. See StopRoof.
//   - The CLOCK is asked before the byte ceiling. A run that crossed both its
//     own ceilings by the time anyone asked reports StopTime, never
//     StopBudget: raising the traffic ceiling cannot finish a run whose time
//     already ran out, so "reached its traffic limit" would be true but the
//     wrong advice, while "ran out of time" is true and the whole reason
//     StopTime exists apart from StopBudget - see StopTime's own doc, which
//     also names what this replaced (TOR-161).
//
// None of these three reasons ever blur into one.
func (t *BudgetTracker) Exhausted() (bool, StopReason) {
	if t.client != nil && t.roof.Reached(t.client.Downloaded()) {
		return true, StopRoof
	}

	bytes, elapsed := t.Spent()

	if t.budget.MaxTime > 0 && elapsed >= t.budget.MaxTime {
		return true, StopTime
	}
	if t.budget.MaxBytes > 0 && bytes >= t.budget.MaxBytes {
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
// earlier, stopped one: enough that finishing takes ONE press, and never more
// than a plain run of the same files would have been allowed anyway.
//
// # What this replaced, and the measurement that replaced it
//
// TOR-152 computed this by prorating the stopped run's own receipt - spent
// bytes over the points that spending bought, times the points still owed -
// taken as the larger of that and bytesPerFile's share of the remainder. Its
// doc argued no fudge factor was needed, because spent already contains each
// run's fixed overhead, so prorating it re-pays that overhead once.
//
// Two manifests off one real set falsify both halves of that (TOR-166):
//
//	file 00: limit_bytes 8388608, downloaded_bytes 20971520, limit_hit "budget"
//	file 01: limit_bytes 81788928, downloaded_bytes 79396864, limit_hit ""
//
// The 8388608 is bytesPerFile/20 rounded up to a MiB - the share arm, for a
// single missing point of a twenty-point plan, since two points already price
// at 15 MiB. That round then downloaded 20971520: two and a half times the
// ceiling it was handed, and 2.67x what the plan's own per-point figure
// claims a point is worth. The round that DID finish cleared its ceiling by
// 2392064 bytes, 2.9% - on a torrent whose other receipt has one point
// costing 20971520.
//
// The prorating argument fails as arithmetic, not just in measurement. With
// spent = F + c*captured for a fixed overhead F, prorating gives
// F*remaining/captured + c*remaining, so the overhead arrives SCALED BY
// remaining/captured - a fraction of one copy whenever fewer points are owed
// than were taken, which is the ordinary top-up. Pieces are discarded after
// every run (REQUIREMENTS.md 2.9), so the next round re-reads the container
// and the keyframe index IN FULL. And the points still owed are not average
// points: a run stops at its ceiling having taken the cheap ones, so the
// average under-prices what is left, twice over.
//
// # Why the ordinary ceiling is the floor, and not a fudge factor
//
// The deciding fact is not the size of the error but its DIRECTION against
// doing nothing. This figure travels as RunRequest.MaxBytes, and zero there
// is not "no traffic" - it is budgetFor scaling the ceiling to the file
// count, DefaultBudget(files). So 8388608 was not a raise at all: the same
// run, started by pressing nothing, would have had 157286400. A control
// labelled "more traffic" was handing out nineteen twentieths LESS.
//
// So the floor is what those files get anyway: DefaultBudget(short).MaxBytes,
// for the files still short, which is exactly the set request() narrows the
// run to. That number is not invented and not padded - it is this project's
// own measured figure for a file's worth of frames (REQUIREMENTS.md section
// 7), overhead included, and it DOMINATES what a top-up needs by an argument
// rather than by a margin: the top-up runs the same files at the same plan,
// pays the same fixed overhead, and fetches strictly FEWER points, because
// the frames already on disk are reused for free (core.reusableFrames). If a
// fresh run of these files could finish inside that ceiling, a top-up of them
// certainly can.
//
// It also means pressing the button can never cost more than not pressing it,
// which is the property that makes the figure safe to state and safe to
// consent to. It needs no receipt, so a run that stopped too early to have a
// meaningful average - two points off a cold swarm - is priced the same way
// as any other. And it is STABLE: the same set offers the same figure every
// time, instead of a number that wanders with each round's receipt.
//
// # The receipt, kept for the one thing it can still do
//
// A torrent genuinely dearer than the project's 150 MB per file is the case
// the ordinary ceiling does not cover, and the set's own receipt is the only
// thing that knows about it. So the measured estimate is kept as a RAISE
// ABOVE that floor and never as a reduction below it: when the receipt says
// the remaining points cost more than a whole fresh run of these files would
// be allowed, the offer follows the receipt.
//
// It prices remaining+1 points rather than remaining. Exhausted is polled
// BETWEEN capture points, never inside one, so a round always overshoots its
// ceiling by as much as the point in flight costs - which means an offer
// sized to the work exactly is met ON the last point instead of after it, and
// gets recorded as a budget stop for work it actually completed. One point's
// worth of headroom is the size of that overshoot, taken from the mechanism
// rather than picked.
//
// Rounded UP to a whole MiB, because this number is shown to a person and
// consented to before it is spent, and a ceiling is not a measurement. Capped
// at maxRunBytes for the reason DefaultBudget caps there: a top-up is still
// one run, and that cap is what stops a pathological receipt from turning it
// into a blank cheque.
//
// WHAT IT DOES NOT DO is bound itself by the CLIENT-WIDE ROOF, unchanged from
// TOR-152: the roof is not this run's to reason about, it lives on the
// client's own counter, and BudgetTracker.Exhausted asks it FIRST and answers
// StopRoof. A caller that wants to show the roof alongside this figure should
// say so separately - see TopUpFloor for the one question the roof does need
// answering, and internal/web's TopUp.price for both.
//
// It does not know about an explicitly configured -max-bytes either. The
// floor is what a run gets when that flag was left to scale (the default);
// where an operator has named a smaller per-run ceiling by hand, a top-up
// raises above it exactly as TOR-152's figure already could.
//
// Zero out means there is nothing to top up: no points are missing, or no
// file is short enough to run.
func TopUpBytes(remaining, short, captured int, spent int64) int64 {
	if remaining <= 0 || short <= 0 {
		return 0
	}

	// What these files get with no raise at all, which is the floor a raise
	// may not go under.
	want := DefaultBudget(short).MaxBytes

	if captured > 0 && spent > 0 {
		// One expression rather than a per-point figure multiplied back up:
		// integer division twice would round a 10.9 MB point down to 10 and
		// lose most of a megabyte per point on the way.
		if measured := spent * int64(remaining+1) / int64(captured); measured > want {
			want = measured
		}
	}

	const mib = 1 << 20
	want = (want + mib - 1) / mib * mib
	if want > maxRunBytes {
		want = maxRunBytes
	}
	return want
}

// TopUpFloor is the LEAST finishing a stopped set can cost, for a caller that
// has to decide whether it can be finished at all.
//
// It is the prorated average TOR-152 offered as a ceiling, and the whole
// point of it is that TOR-166 measured that figure coming in far under what
// the round then spent: 8388608 offered against 20971520 downloaded. A number
// proven to under-price the work is a bad ceiling and a SOUND FLOOR, and it
// is sound for reasons rather than by luck - the remaining points are the
// expensive tail an average under-prices, and a full copy of the run's fixed
// overhead is owed on top of it, so the true cost is above this and not
// below.
//
// The one caller is internal/web's TopUp.price, deciding what to do about a
// client-wide roof smaller than the offer. Clamping to the roof is honest
// while the roof can still cover the work. Below this figure it is not: the
// run is then guaranteed to stop short, and a clamped offer would be another
// instalment of an allowance that can never reach the end. That case is said
// plainly instead.
//
// Zero means there is nothing to say: nothing missing, or no receipt to
// reason from - which is not a claim that finishing is free.
func TopUpFloor(remaining, captured int, spent int64) int64 {
	if remaining <= 0 || captured <= 0 || spent <= 0 {
		return 0
	}
	return spent * int64(remaining) / int64(captured)
}
