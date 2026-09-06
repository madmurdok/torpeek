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

// TestTopUpBytesPricesWhatIsLeftFromWhatWasSpent is the measured case, and
// the numbers are the ones the ticket was filed from: a run over two selected
// files, twenty frames asked of each, sixteen taken of each, stopped at a
// 300 MB ceiling having received 324583424 bytes.
//
// The property being asserted is not one number but the whole argument for a
// computed figure over a multiplier: what is left costs a FRACTION of what
// the run already spent, so a top-up must ask for far less than the ceiling
// it stopped at - where doubling that ceiling (the multiplier this rejects)
// would have asked for six times the work.
func TestTopUpBytesPricesWhatIsLeftFromWhatWasSpent(t *testing.T) {
	const (
		planned   = 20
		captured  = 32               // sixteen of each of two files
		spent     = int64(324583424) // manifest cost, downloaded_bytes
		ceiling   = int64(314572800) // manifest cost, limit_bytes
		remaining = 8                // four points still owed on each file
	)

	got := TopUpBytes(remaining, planned, spent, captured)

	// The measured estimate: what a point cost this torrent, times the points
	// still owed, rounded up to a whole MiB.
	perPoint := spent / captured
	if got < remaining*perPoint {
		t.Errorf("TopUpBytes = %d, which is under the %d the eight remaining points "+
			"measured at %d each; a ceiling below what the work costs stops the "+
			"top-up before it finishes", got, remaining*perPoint, perPoint)
	}
	// And it must be a fraction of the ceiling that stopped the run, or the
	// whole argument for pricing this rather than multiplying the old ceiling
	// falls over.
	if got >= ceiling {
		t.Errorf("TopUpBytes = %d, which is not less than the %d ceiling the run "+
			"stopped at - finishing four points in twenty must not cost what "+
			"twenty cost", got, ceiling)
	}
	if got%(1<<20) != 0 {
		t.Errorf("TopUpBytes = %d, which is not a whole number of MiB - a figure a "+
			"person consents to before it is spent should be a round one", got)
	}
}

// TestTopUpBytesFallsBackOnTheDefaultsOwnShare is the floor. A run stopped
// so early that its own average is meaningless - two points off a cold swarm
// - must still be offered enough to work with, and the only non-invented
// number available is the project's own measured per-file figure (section 7)
// prorated to the points still missing.
func TestTopUpBytesFallsBackOnTheDefaultsOwnShare(t *testing.T) {
	const (
		planned   = 20
		captured  = 2
		remaining = 18
	)
	// A thousand bytes for two points is not a per-point cost, it is noise.
	got := TopUpBytes(remaining, planned, 1000, captured)

	want := int64(bytesPerFile) * remaining / planned
	if got < want {
		t.Errorf("TopUpBytes = %d, want at least %d - the default's own share of "+
			"%d per file, prorated to %d of %d points. A measured average off two "+
			"points cannot be the whole answer", got, want, bytesPerFile, remaining, planned)
	}
}

// TestTopUpBytesNeverExceedsWhatAFreshRunCouldSpend is the cap, and it is
// what keeps a pathological receipt from becoming a blank cheque: a run that
// somehow spent a gigabyte on one frame would otherwise price its remaining
// nineteen at nineteen gigabytes. DefaultBudget caps every run at
// maxRunBytes; a top-up is still one run.
func TestTopUpBytesNeverExceedsWhatAFreshRunCouldSpend(t *testing.T) {
	got := TopUpBytes(19, 20, 1<<30, 1)

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
// no points owed is no traffic to ask for, and a caller with no plan to
// reason from gets the same answer rather than a guess.
func TestTopUpBytesOffersNothingWhenNothingIsMissing(t *testing.T) {
	if got := TopUpBytes(0, 20, 324583424, 40); got != 0 {
		t.Errorf("TopUpBytes with nothing missing = %d, want 0", got)
	}
	if got := TopUpBytes(8, 0, 0, 0); got != 0 {
		t.Errorf("TopUpBytes with no plan and no receipt = %d, want 0", got)
	}
}
