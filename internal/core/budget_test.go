package core

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeMeter reports whatever traffic a test wants it to.
type fakeMeter struct {
	mu    sync.Mutex
	bytes int64
}

func (m *fakeMeter) Downloaded() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bytes
}

func (m *fakeMeter) set(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bytes = n
}

// fakeClock advances only when a test says so.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestDefaultBudgetScalesWithFiles covers the reasoning recorded in section 7:
// a fixed ceiling cannot serve both a single film and a season pack.
func TestDefaultBudgetScalesWithFiles(t *testing.T) {
	cases := []struct {
		files    int
		wantByte int64
	}{
		{files: 0, wantByte: bytesPerFile}, // treated as one
		{files: 1, wantByte: bytesPerFile},
		{files: 4, wantByte: 4 * bytesPerFile},
		{files: 20, wantByte: maxRunBytes}, // capped, not 3 GB
		{files: 200, wantByte: maxRunBytes},
	}

	for _, tc := range cases {
		got := DefaultBudget(tc.files)
		if got.MaxBytes != tc.wantByte {
			t.Errorf("DefaultBudget(%d).MaxBytes = %d, want %d", tc.files, got.MaxBytes, tc.wantByte)
		}
		if got.MaxTime != defaultRunTime {
			t.Errorf("DefaultBudget(%d).MaxTime = %s, want %s", tc.files, got.MaxTime, defaultRunTime)
		}
	}

	// The point of scaling: twenty episodes must not run out on the second.
	twenty := DefaultBudget(20)
	if twenty.MaxBytes <= DefaultBudget(1).MaxBytes {
		t.Error("a twenty-file run has no more budget than a one-file run")
	}
}

func TestExhaustedOnTraffic(t *testing.T) {
	meter := &fakeMeter{}
	clock := newFakeClock()
	tracker := newBudgetTracker(Budget{MaxBytes: 1000}, meter, Roof{}, nil, clock.Now)

	if done, _ := tracker.Exhausted(); done {
		t.Fatal("exhausted before spending anything")
	}

	meter.set(999)
	if done, _ := tracker.Exhausted(); done {
		t.Error("exhausted one byte short of the ceiling")
	}

	meter.set(1000)
	done, reason := tracker.Exhausted()
	if !done {
		t.Error("not exhausted at the ceiling")
	}
	if reason != StopBudget {
		t.Errorf("reason = %q, want %q", reason, StopBudget)
	}
}

func TestExhaustedOnTime(t *testing.T) {
	clock := newFakeClock()
	tracker := newBudgetTracker(Budget{MaxTime: time.Minute}, &fakeMeter{}, Roof{}, nil, clock.Now)

	clock.advance(59 * time.Second)
	if done, _ := tracker.Exhausted(); done {
		t.Error("exhausted a second short of the ceiling")
	}

	clock.advance(time.Second)
	done, reason := tracker.Exhausted()
	if !done || reason != StopTime {
		t.Errorf("Exhausted() = (%v, %q), want (true, %q)", done, reason, StopTime)
	}
}

// TestExhaustedTellsTrafficApartFromTime is TOR-161's own unit test: the two
// ceilings BudgetTracker.Exhausted used to fold into one reason (StopBudget
// for either) must now report reasons that DIFFER, not just reasons that
// happen to be correct in isolation - a test that only checked one arm could
// pass by coincidence if both cases still returned the same value. Both
// arms are built from otherwise-identical trackers so the only thing that
// differs between them is which ceiling was crossed.
func TestExhaustedTellsTrafficApartFromTime(t *testing.T) {
	trafficClock := newFakeClock()
	traffic := newBudgetTracker(Budget{MaxBytes: 1000, MaxTime: time.Hour},
		&fakeMeter{bytes: 1000}, Roof{}, nil, trafficClock.Now)

	timeClock := newFakeClock()
	clockOnly := newBudgetTracker(Budget{MaxBytes: 1 << 40, MaxTime: time.Minute},
		&fakeMeter{}, Roof{}, nil, timeClock.Now)
	timeClock.advance(time.Minute)

	trafficDone, trafficReason := traffic.Exhausted()
	timeDone, timeReason := clockOnly.Exhausted()

	if !trafficDone || !timeDone {
		t.Fatalf("both trackers should report exhausted: traffic=%v time=%v", trafficDone, timeDone)
	}
	if trafficReason == timeReason {
		t.Fatalf("a byte-exhausted tracker and a time-exhausted tracker both reported %q - "+
			"the two ceilings are not told apart", trafficReason)
	}
	if trafficReason != StopBudget {
		t.Errorf("byte ceiling reported %q, want %q", trafficReason, StopBudget)
	}
	if timeReason != StopTime {
		t.Errorf("time ceiling reported %q, want %q", timeReason, StopTime)
	}
}

// TestExhaustedReportsTimeWhenBothCeilingsAreCrossed is TOR-161's
// both-exceeded decision, moved to the source: a run that crossed its own
// byte ceiling AND its own time ceiling by the moment it is asked must
// report StopTime, on the grounds TOR-152 already gave when it made this
// choice itself - a bigger traffic allowance cannot finish a run whose clock
// already ran out, so reporting the traffic ceiling would be true but
// useless advice.
//
// The first arm (bytes alone) is what proves the second arm's StopTime is
// the CLOCK's doing and not some unconditional default: the same tracker,
// with only the clock NOT yet crossed, reports StopBudget.
func TestExhaustedReportsTimeWhenBothCeilingsAreCrossed(t *testing.T) {
	clock := newFakeClock()
	meter := &fakeMeter{}
	tracker := newBudgetTracker(Budget{MaxBytes: 1000, MaxTime: time.Minute}, meter, Roof{}, nil, clock.Now)

	meter.set(1000)
	if done, reason := tracker.Exhausted(); !done || reason != StopBudget {
		t.Fatalf("bytes alone: Exhausted() = (%v, %q), want (true, %q)", done, reason, StopBudget)
	}

	clock.advance(time.Minute)
	done, reason := tracker.Exhausted()
	if !done || reason != StopTime {
		t.Errorf("both ceilings crossed: Exhausted() = (%v, %q), want (true, %q) - "+
			"a run out of time is not one more traffic can finish, whatever its "+
			"byte ceiling also says", done, reason, StopTime)
	}
}

func TestUnlimitedBudgetNeverExhausts(t *testing.T) {
	meter := &fakeMeter{bytes: 1 << 40}
	clock := newFakeClock()
	tracker := newBudgetTracker(Budget{}, meter, Roof{}, nil, clock.Now)

	clock.advance(24 * time.Hour)
	if done, _ := tracker.Exhausted(); done {
		t.Error("a budget with no ceilings reported itself exhausted")
	}
}

// TestWarningFiresOnce: a warning repeated every tick is noise the client
// would have to de-duplicate itself.
func TestWarningFiresOnce(t *testing.T) {
	meter := &fakeMeter{}
	clock := newFakeClock()
	tracker := newBudgetTracker(Budget{MaxBytes: 1000, WarnAt: 0.8}, meter, Roof{}, nil, clock.Now)

	meter.set(700)
	if w := tracker.Warning(); w != nil {
		t.Error("warned below the threshold")
	}

	meter.set(800)
	first := tracker.Warning()
	if first == nil {
		t.Fatal("no warning at the threshold")
	}
	if first.SpentBytes != 800 || first.LimitBytes != 1000 {
		t.Errorf("warning = %+v, want 800 of 1000 bytes", first)
	}

	meter.set(900)
	if again := tracker.Warning(); again != nil {
		t.Error("warned twice for the same budget")
	}
}

func TestWarningOnTimeAlone(t *testing.T) {
	clock := newFakeClock()
	tracker := newBudgetTracker(Budget{MaxTime: 10 * time.Minute, WarnAt: 0.8}, &fakeMeter{}, Roof{}, nil, clock.Now)

	clock.advance(7 * time.Minute)
	if w := tracker.Warning(); w != nil {
		t.Error("warned below the time threshold")
	}

	clock.advance(time.Minute)
	if w := tracker.Warning(); w == nil {
		t.Error("no warning after crossing 80% of the time ceiling")
	}
}

func TestRemaining(t *testing.T) {
	meter := &fakeMeter{bytes: 400}
	clock := newFakeClock()
	tracker := newBudgetTracker(Budget{MaxBytes: 1000, MaxTime: time.Minute}, meter, Roof{}, nil, clock.Now)

	clock.advance(20 * time.Second)
	bytes, remaining := tracker.Remaining()
	if bytes != 600 {
		t.Errorf("remaining bytes = %d, want 600", bytes)
	}
	if remaining != 40*time.Second {
		t.Errorf("remaining time = %s, want 40s", remaining)
	}

	// Overspending must clamp at zero rather than report a negative budget.
	meter.set(5000)
	clock.advance(time.Hour)
	bytes, remaining = tracker.Remaining()
	if bytes != 0 || remaining != 0 {
		t.Errorf("Remaining() = (%d, %s), want (0, 0s) once overspent", bytes, remaining)
	}
}

func TestContextExpiresWithTheTimeBudget(t *testing.T) {
	tracker := NewBudgetTracker(Budget{MaxTime: 150 * time.Millisecond}, &fakeMeter{}, Roof{}, nil)

	ctx, cancel := tracker.Context(context.Background())
	defer cancel()

	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("context outlived the time budget")
	}
}

func TestContextAlreadyExpired(t *testing.T) {
	clock := newFakeClock()
	tracker := newBudgetTracker(Budget{MaxTime: time.Minute}, &fakeMeter{}, Roof{}, nil, clock.Now)
	clock.advance(2 * time.Minute)

	ctx, cancel := tracker.Context(context.Background())
	defer cancel()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Error("context from an already-spent budget was not cancelled")
	}
}

func TestTrackerIsSafeForConcurrentUse(t *testing.T) {
	meter := &fakeMeter{}
	tracker := NewBudgetTracker(Budget{MaxBytes: 1 << 20, WarnAt: 0.8}, meter, Roof{}, nil)

	var wg sync.WaitGroup
	warnings := make(chan *BudgetWarning, 32)

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			meter.set(int64(n) * 100_000)
			tracker.Exhausted()
			tracker.Spent()
			tracker.Remaining()
			if w := tracker.Warning(); w != nil {
				warnings <- w
			}
		}(i)
	}
	wg.Wait()
	close(warnings)

	if got := len(warnings); got > 1 {
		t.Errorf("%d goroutines each got a warning, want at most one overall", got)
	}
}

// TestRoofStopsARunItsOwnBudgetWouldNot is the unit half of TOR-131: the two
// ceilings are independent, and the roof stops a run that is nowhere near its
// own limit.
//
// The second arm is what stops this being a tautology. The same run against
// the same meters with no roof over it is not exhausted at all, so the stop is
// the roof's doing and not the budget's.
func TestRoofStopsARunItsOwnBudgetWouldNot(t *testing.T) {
	const (
		runSpent   = 100
		runCeiling = 100_000 // a thousand times what this run has spent
		clientRead = 9_000
		roof       = 8_000
	)

	run := &fakeMeter{}
	run.set(runSpent)
	client := &fakeMeter{}
	client.set(clientRead)

	budget := Budget{MaxBytes: runCeiling, MaxTime: time.Hour}

	unroofed := NewBudgetTracker(budget, run, Roof{}, nil)
	if exhausted, reason := unroofed.Exhausted(); exhausted {
		t.Fatalf("with no roof the run is exhausted (%s); the arms are not different, "+
			"so nothing below is about the roof", reason)
	}

	roofed := NewBudgetTracker(budget, run, Roof{MaxBytes: roof}, client)
	exhausted, reason := roofed.Exhausted()
	if !exhausted {
		t.Fatalf("the client has received %d of a %d roof and the run carries on", clientRead, roof)
	}
	if reason != StopRoof {
		t.Errorf("stopped for %q, want %q - a run stopped by the client's roof must not "+
			"report the reason a run over its own ceiling reports", reason, StopRoof)
	}

	// The run's own figures are untouched by any of this: the roof is not a
	// second way of spending its budget.
	if spent, _ := roofed.Spent(); spent != runSpent {
		t.Errorf("Spent() = %d, want this run's own %d", spent, runSpent)
	}
	if got := roofed.Received(); got != clientRead {
		t.Errorf("Received() = %d, want the client's %d", got, clientRead)
	}
	if got := roofed.RoofLimit(); got != roof {
		t.Errorf("RoofLimit() = %d, want %d", got, roof)
	}
}

// TestUnlimitedRoofNeverStops covers DefaultRoof: shipping unlimited is the
// decision, so a zero MaxBytes must not become a zero-byte ceiling.
func TestUnlimitedRoofNeverStops(t *testing.T) {
	client := &fakeMeter{}
	client.set(1 << 40)

	if DefaultRoof().Reached(1 << 40) {
		t.Error("the default roof stopped a client, and the default is no roof")
	}

	tracker := NewBudgetTracker(Budget{MaxBytes: 1 << 20}, &fakeMeter{}, DefaultRoof(), client)
	if exhausted, reason := tracker.Exhausted(); exhausted {
		t.Errorf("exhausted (%s) under an unlimited roof with a run that has spent nothing", reason)
	}
	if tracker.RoofLimit() != 0 {
		t.Errorf("RoofLimit() = %d, want 0 for no roof", tracker.RoofLimit())
	}
}

// TestRoofAndRunWarningsAreToldApart: both ceilings warn, each once, and the
// two warnings say whose numbers they carry.
//
// The scope is the whole point. Without it the client-scoped warning reports
// the CLIENT's spending in the field a reader takes for the RUN's, which is
// how a run that has spent 100 bytes comes to look like the one at fault.
func TestRoofAndRunWarningsAreToldApart(t *testing.T) {
	const (
		runCeiling = 1_000
		roof       = 10_000
	)

	run := &fakeMeter{}
	client := &fakeMeter{}
	tracker := NewBudgetTracker(
		Budget{MaxBytes: runCeiling, WarnAt: 0.8}, run,
		Roof{MaxBytes: roof, WarnAt: 0.8}, client)

	if w := tracker.Warning(); w != nil {
		t.Fatalf("warned at the start: %+v", w)
	}

	// The client fills up while this run has spent almost nothing.
	run.set(100)
	client.set(8_500)

	w := tracker.Warning()
	if w == nil {
		t.Fatal("the client crossed its warning fraction and nothing was said")
	}
	if w.Scope != LimitClient {
		t.Errorf("scope is %q, want %q", w.Scope, LimitClient)
	}
	if w.SpentBytes != 8_500 || w.LimitBytes != roof {
		t.Errorf("client warning carries %d of %d, want the client's %d of %d",
			w.SpentBytes, w.LimitBytes, 8_500, roof)
	}
	if w.LimitTime != 0 {
		t.Errorf("client warning carries a time ceiling of %s; the roof has none", w.LimitTime)
	}

	if again := tracker.Warning(); again != nil {
		t.Errorf("the client warned twice: %+v", again)
	}

	// Now this run's own ceiling fills up too. A separate warning, separately
	// latched, carrying this run's numbers.
	run.set(900)

	w = tracker.Warning()
	if w == nil {
		t.Fatal("the run crossed its own warning fraction and nothing was said")
	}
	if w.Scope != LimitRun {
		t.Errorf("scope is %q, want %q", w.Scope, LimitRun)
	}
	if w.SpentBytes != 900 || w.LimitBytes != runCeiling {
		t.Errorf("run warning carries %d of %d, want this run's %d of %d",
			w.SpentBytes, w.LimitBytes, 900, runCeiling)
	}

	if again := tracker.Warning(); again != nil {
		t.Errorf("warned a third time: %+v", again)
	}
}

// ---- TOR-152: pricing what is left of a stopped run. ----

// TestTopUpBytesPricesAStoppedSetFromWhatItsFilesStillOwe is the measured
// case, and the numbers are the ones TOR-152 was filed from: a run over two
// selected files, twenty frames asked of each, sixteen taken of each, stopped
// at a 300 MB ceiling having received 324583424 bytes.
//
// TOR-152 asserted the opposite of what this asserts now, and the change is
// the whole of TOR-166. It required the figure to come in UNDER the ceiling
// the run had stopped at, on the reasoning that four points in twenty must
// not be priced like twenty. The premise is sound and the conclusion was
// wrong, because the two numbers are not the same kind of thing: what four
// points COST is indeed a fraction of what twenty cost, but what a run is
// ALLOWED is a ceiling, and a ceiling under the ordinary one is not a raise -
// it hands the run less than pressing nothing would. Both files here are
// still short, so the ordinary ceiling for this top-up is a two-file run's,
// which is the 300 MB ceiling the stopped run met. Equal, and that is the
// right answer rather than a coincidence: the top-up runs the same two files
// at the same plan, and reuses thirty-two of their frames off disk.
func TestTopUpBytesPricesAStoppedSetFromWhatItsFilesStillOwe(t *testing.T) {
	const (
		short     = 2                // both files still owe points
		captured  = 32               // sixteen of each of two files
		spent     = int64(324583424) // manifest cost, downloaded_bytes
		remaining = 8                // four points still owed on each file
	)

	got := TopUpBytes(remaining, short, captured, spent)

	// The measured estimate is the floor of what the work costs, so an offer
	// under it stops the top-up before it finishes.
	perPoint := spent / captured
	if got < remaining*perPoint {
		t.Errorf("TopUpBytes = %d, which is under the %d the eight remaining points "+
			"measured at %d each; a ceiling below what the work costs stops the "+
			"top-up before it finishes", got, remaining*perPoint, perPoint)
	}
	// And never under what these two files get with no raise at all, nor over
	// it, since this receipt prices the remaining work well inside it.
	ordinary := DefaultBudget(short).MaxBytes
	if got < ordinary {
		t.Errorf("TopUpBytes = %d, under the %d a plain run of the same %d files is "+
			"given (MaxBytes unset -> budgetFor -> DefaultBudget) - a raise that "+
			"lowers the ceiling is not a raise", got, ordinary, short)
	}
	if got > ordinary {
		t.Errorf("TopUpBytes = %d, over the %d a plain run of those %d files is given, "+
			"though this set's receipt prices the eight remaining points at %d - "+
			"handing out more than a fresh run could ask for makes the figure a "+
			"blank cheque rather than a ceiling",
			got, ordinary, short, remaining*perPoint)
	}
	if got%(1<<20) != 0 {
		t.Errorf("TopUpBytes = %d, which is not a whole number of MiB - a figure a "+
			"person consents to before it is spent should be a round one", got)
	}
}

// TestTopUpBytesNeedsNoReceiptAtAll is the floor, and TOR-166 widened what
// it covers. A run stopped so early that its own average is meaningless - two
// points off a cold swarm - was already the case TOR-152 kept a fallback for.
// Now the fallback IS the answer in every ordinary case, so a set with no
// usable receipt is not a special case at all: it gets what a plain run of
// its still-short files gets, which is the only non-invented number here
// (REQUIREMENTS.md section 7).
func TestTopUpBytesNeedsNoReceiptAtAll(t *testing.T) {
	const (
		short     = 1
		captured  = 2
		remaining = 18
	)
	// A thousand bytes for two points is not a per-point cost, it is noise.
	noisy := TopUpBytes(remaining, short, captured, 1000)
	// And no receipt whatsoever is the same answer rather than a smaller one.
	none := TopUpBytes(remaining, short, 0, 0)

	want := DefaultBudget(short).MaxBytes
	if noisy < want {
		t.Errorf("TopUpBytes = %d off a receipt of 1000 bytes for two points, want at "+
			"least the %d a plain run of that one file gets - a measured average "+
			"off two points must not be allowed to lower the ceiling", noisy, want)
	}
	if none != want {
		t.Errorf("TopUpBytes with no receipt at all = %d, want %d - the figure that "+
			"needs no receipt is the same one either way", none, want)
	}
}

// TestTopUpBytesNeverExceedsWhatAFreshRunCouldSpend is the cap, and it is
// what keeps a pathological receipt from becoming a blank cheque: a run that
// somehow spent a gigabyte on one frame would otherwise price its remaining
// nineteen at nineteen gigabytes. DefaultBudget caps every run at
// maxRunBytes; a top-up is still one run.
func TestTopUpBytesNeverExceedsWhatAFreshRunCouldSpend(t *testing.T) {
	got := TopUpBytes(19, 1, 1, 1<<30)

	if got > maxRunBytes {
		t.Errorf("TopUpBytes = %d, over the %d ceiling a run can ever be given "+
			"(DefaultBudget's own cap) - no top-up may be allowed more than a "+
			"fresh run could ask for", got, int64(maxRunBytes))
	}
	if got != maxRunBytes {
		t.Errorf("TopUpBytes = %d, want exactly the cap %d for a run whose measured "+
			"cost prices the rest above it", got, int64(maxRunBytes))
	}
}

// TestTopUpBytesOffersNothingWhenNothingIsMissing keeps the zero meaningful:
// no points owed is no traffic to ask for, and a caller with no file short
// enough to run gets the same answer rather than a guess.
func TestTopUpBytesOffersNothingWhenNothingIsMissing(t *testing.T) {
	if got := TopUpBytes(0, 2, 40, 324583424); got != 0 {
		t.Errorf("TopUpBytes with nothing missing = %d, want 0", got)
	}
	if got := TopUpBytes(8, 0, 0, 0); got != 0 {
		t.Errorf("TopUpBytes with no file short enough to run = %d, want 0", got)
	}
}

// ---- TOR-166: the two rounds the manifests recorded, and what they cost. ----
//
// These are not invented numbers. They are the two manifest.Cost records the
// ticket was filed from, off one result set of TWO files at twenty frames
// each, and between them they falsify TOR-152's claim that prorating a
// receipt is the conservative direction:
//
//	file 00: limit_bytes 8388608 (8 MiB), downloaded_bytes 20971520 (20 MiB), limit_hit "budget"
//	file 01: limit_bytes 81788928 (78 MiB), downloaded_bytes 79396864 (75.7 MiB), limit_hit ""
//
// The 8 MiB ceiling identifies its own arithmetic exactly: it is the DEFAULT'S
// OWN SHARE for ONE missing point of a twenty-point plan, bytesPerFile/20 =
// 7864320 rounded up to a whole MiB. No other remainder can produce it - two
// points already price at 15 MiB - so that round was priced for a single
// point, and it spent 20971520 bytes on it: 2.5x the ceiling it was handed,
// and 2.67x what the plan's own per-point figure says a point is worth.
//
// The round that DID finish is the control, and it is thinner than it looks:
// 79396864 of 81788928 is 97.1% of its ceiling, 2392064 bytes of headroom, on
// a torrent whose other receipt shows a single point costing 20971520. It
// finished, but not with any margin the formula earned.
const (
	// plannedPerFile is the plan's frames per file, from the set both
	// receipts belong to.
	plannedPerFile = 20

	// Round A is file 00's receipt: one point still owed, priced at the
	// default's share, and what that round actually downloaded.
	roundACeiling  = int64(8388608)
	roundACost     = int64(20971520)
	roundARemain   = 1
	roundACaptured = 39 // 20 of file 01, 19 of file 00
	roundAShort    = 1

	// Round B is file 01's receipt: ten points still owed, priced off the
	// measured average, and what that round actually downloaded.
	roundBCeiling  = int64(81788928)
	roundBCost     = int64(79396864)
	roundBRemain   = 10
	roundBCaptured = 30 // 20 of file 00, 10 of file 01
	roundBShort    = 1

	// roundASpent is what the set's receipt said when round A was priced -
	// the other recorded round's own spending, this set's only other
	// measurement. roundBSpent is the figure that makes round B's measured
	// arm land on the ceiling it was actually handed.
	roundASpent = roundBCost
	roundBSpent = int64(245366784)
)

// pricedRound is one recorded round: the state the offer was priced from, the
// ceiling that was actually handed out, and what the round then cost.
type pricedRound struct {
	name                         string
	remaining, captured, short   int
	spent                        int64
	recordedCeiling, recordedRun int64
}

// recordedRounds is the two receipts as states to re-price.
func recordedRounds() []pricedRound {
	return []pricedRound{
		{
			name:      "file 00: one point left, priced at the default's share",
			remaining: roundARemain, captured: roundACaptured, short: roundAShort,
			spent: roundASpent, recordedCeiling: roundACeiling, recordedRun: roundACost,
		},
		{
			name:      "file 01: ten points left, priced off the measured average",
			remaining: roundBRemain, captured: roundBCaptured, short: roundBShort,
			spent: roundBSpent, recordedCeiling: roundBCeiling, recordedRun: roundBCost,
		},
	}
}

// offerFor is the ONE place these cases touch the pricing API, so the
// assertions below stay byte-identical across a change of its shape.
func offerFor(r pricedRound) int64 {
	return TopUpBytes(r.remaining, r.short, r.captured, r.spent)
}

// TestTheRecordedRoundsRepriceToTheCeilingsTheyWereHanded is the fidelity
// anchor for every case below: if these states did not reproduce the ceilings
// the manifests actually recorded, they would be numbers somebody made up
// rather than the measurement, and nothing built on them would mean anything.
//
// It is deliberately an assertion about the OLD arithmetic, kept after the
// fix: 8388608 is bytesPerFile/20 rounded up to a MiB, and 81788928 is
// 245366784 x 10 / 30 rounded up to a MiB. That is how the two rounds were
// identified in the first place, and it is what makes the states below the
// ticket's own measurement instead of a plausible-looking fixture.
func TestTheRecordedRoundsRepriceToTheCeilingsTheyWereHanded(t *testing.T) {
	const mib = 1 << 20
	roundUp := func(v int64) int64 { return (v + mib - 1) / mib * mib }

	share := roundUp(int64(bytesPerFile) * roundARemain / plannedPerFile)
	if share != roundACeiling {
		t.Errorf("the default's share of %d for %d of %d points is %d, but the "+
			"manifest recorded a ceiling of %d - round A is not the state that "+
			"produced that receipt", bytesPerFile, roundARemain, plannedPerFile,
			share, roundACeiling)
	}

	measured := roundUp(roundBSpent * roundBRemain / roundBCaptured)
	if measured != roundBCeiling {
		t.Errorf("the measured average of %d over %d points, prorated to %d, is %d, "+
			"but the manifest recorded a ceiling of %d - round B is not the state "+
			"that produced that receipt", roundBSpent, roundBCaptured, roundBRemain,
			measured, roundBCeiling)
	}
}

// TestTopUpCoversWhatTheRecordedRoundsActuallyCost is the falsification, run
// as an assertion. An offer is only worth stating if the round it authorises
// can finish under it, so each recorded round is re-priced from the state it
// was priced from and the offer is held against what that round then spent.
//
// The two arms are demonstrably different, which is what stops this passing
// for any figure at all: round B's recorded ceiling COVERS its recorded cost
// (81788928 >= 79396864) and round A's does not (8388608 < 20971520). A
// pricing that changed nothing fails exactly one of them; one that answered
// maxRunBytes to everything passes both and fails the ceiling assertion
// below instead.
func TestTopUpCoversWhatTheRecordedRoundsActuallyCost(t *testing.T) {
	for _, r := range recordedRounds() {
		t.Run(r.name, func(t *testing.T) {
			got := offerFor(r)

			if got < r.recordedRun {
				t.Errorf("offered %d to finish %d point(s), and that round went on to "+
					"download %d - a ceiling under what the work costs stops the "+
					"top-up short and asks for another press. The manifest recorded "+
					"the offer as %d",
					got, r.remaining, r.recordedRun, r.recordedCeiling)
			}

			// The other direction, so this cannot be satisfied by handing out
			// the cap: a top-up may never be allowed more than a plain run of
			// the same files would be, unless the set's own receipt says the
			// remaining work costs more than that.
			ordinary := DefaultBudget(r.short).MaxBytes
			measured := r.spent * int64(r.remaining) / int64(r.captured)
			if got > ordinary && measured <= ordinary {
				t.Errorf("offered %d for %d file(s) whose own receipt prices the "+
					"remaining work at %d - over the %d a plain run of those files "+
					"gets, which makes the figure a blank cheque rather than a "+
					"ceiling", got, r.short, measured, ordinary)
			}
		})
	}
}

// TestTopUpIsNeverLessThanNotPressingItAtAll is the root cause, stated as the
// property that closes it.
//
// A top-up's raise travels as RunRequest.MaxBytes, and zero there is not
// "nothing" - it is core.budgetFor scaling the ceiling to the file count,
// DefaultBudget(files). So an offer BELOW that number is not a raise at all:
// it hands the run a TIGHTER ceiling than it would have had if the button had
// never been pressed. Round A is exactly that: 8388608 offered where doing
// nothing gives 157286400, a button labelled "more traffic" that took away
// nineteen twentieths of it.
func TestTopUpIsNeverLessThanNotPressingItAtAll(t *testing.T) {
	for _, r := range recordedRounds() {
		t.Run(r.name, func(t *testing.T) {
			ordinary := DefaultBudget(r.short).MaxBytes
			if got := offerFor(r); got < ordinary {
				t.Errorf("offered %d as a RAISE, under the %d the same run gets with "+
					"no raise at all (MaxBytes unset -> budgetFor -> DefaultBudget(%d)) "+
					"- pressing the button makes the ceiling smaller than leaving it "+
					"alone", got, ordinary, r.short)
			}
		})
	}
}

// pressesToFinish counts how many times a person has to press "top up" before
// the set is done, given what one round of finishing it actually costs.
//
// The model is the observed one and nothing more: a round is offered a
// ceiling, and it finishes only if that ceiling covers what the round costs.
// A round that does not finish still spends - the manifest for round A
// records 20971520 downloaded against an 8388608 ceiling - and leaves the set
// exactly as short as it found it, which is what the owner reported and what
// the next offer is then priced from.
//
// limit caps the loop, because a formula can have a FIXED POINT below the
// true cost and then no number of presses ever finishes.
func pressesToFinish(r pricedRound, limit int) int {
	for n := 1; n <= limit; n++ {
		if offerFor(r) >= r.recordedRun {
			return n
		}
		if r.spent < r.recordedRun {
			r.spent = r.recordedRun
		}
	}
	return limit + 1
}

// TestFinishingTakesOnePress is the acceptance criterion, demonstrated rather
// than argued. Both recorded rounds must be finishable by pressing once.
//
// The two arms are again demonstrably different under the pricing that
// shipped: round B finishes on the first press, and round A never finishes at
// all. Its offer does not merely fall short once - once the round's own
// 20971520 is on the receipt, the measured arm reads 20971520/39 = 537731 per
// point, still under the default's share, so the next offer is 8388608 again,
// and the one after that, for ever. That is what "pressed it a second time"
// looks like when it is followed to its end: not a formula that needs a
// bigger multiplier, but one with a fixed point below the cost of the work.
func TestFinishingTakesOnePress(t *testing.T) {
	const limit = 5

	for _, r := range recordedRounds() {
		t.Run(r.name, func(t *testing.T) {
			switch presses := pressesToFinish(r, limit); {
			case presses > limit:
				t.Errorf("still not finished after %d presses, each one spending %d "+
					"and stopping on the ceiling - the offer has a fixed point below "+
					"what finishing costs, so no number of presses ever gets there",
					limit, r.recordedRun)
			case presses != 1:
				t.Errorf("finishing took %d presses, want 1 - a control that has to be "+
					"pressed repeatedly spends the allowance in instalments and makes "+
					"the owner supervise it", presses)
			}
		})
	}
}
