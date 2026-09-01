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
	tracker := newBudgetTracker(Budget{MaxBytes: 1000}, meter, clock.Now)

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
	tracker := newBudgetTracker(Budget{MaxTime: time.Minute}, &fakeMeter{}, clock.Now)

	clock.advance(59 * time.Second)
	if done, _ := tracker.Exhausted(); done {
		t.Error("exhausted a second short of the ceiling")
	}

	clock.advance(time.Second)
	done, reason := tracker.Exhausted()
	if !done || reason != StopBudget {
		t.Errorf("Exhausted() = (%v, %q), want (true, %q)", done, reason, StopBudget)
	}
}

func TestUnlimitedBudgetNeverExhausts(t *testing.T) {
	meter := &fakeMeter{bytes: 1 << 40}
	clock := newFakeClock()
	tracker := newBudgetTracker(Budget{}, meter, clock.Now)

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
	tracker := newBudgetTracker(Budget{MaxBytes: 1000, WarnAt: 0.8}, meter, clock.Now)

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
	tracker := newBudgetTracker(Budget{MaxTime: 10 * time.Minute, WarnAt: 0.8}, &fakeMeter{}, clock.Now)

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
	tracker := newBudgetTracker(Budget{MaxBytes: 1000, MaxTime: time.Minute}, meter, clock.Now)

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
	tracker := NewBudgetTracker(Budget{MaxTime: 150 * time.Millisecond}, &fakeMeter{})

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
	tracker := newBudgetTracker(Budget{MaxTime: time.Minute}, &fakeMeter{}, clock.Now)
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
	tracker := NewBudgetTracker(Budget{MaxBytes: 1 << 20, WarnAt: 0.8}, meter)

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
