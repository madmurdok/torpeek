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

// Budget defaults from REQUIREMENTS.md section 7.
const (
	// bytesPerFile is what one file's worth of frames is expected to cost.
	bytesPerFile = 150 << 20
	// maxRunBytes caps the total however many files there are.
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

// Meter reports how much traffic a run has spent. swarm.Torrent satisfies it.
type Meter interface {
	Downloaded() int64
}

// BudgetTracker watches a run against its budget.
//
// It reports rather than enforces: stopping is the orchestrator's job, since
// only it knows what "finish the frame in flight, then stop" means. The
// tracker's contract is to answer the same question the same way from any
// goroutine.
type BudgetTracker struct {
	budget Budget
	meter  Meter
	clock  func() time.Time
	start  time.Time

	mu     sync.Mutex
	warned bool
}

// NewBudgetTracker starts tracking now.
func NewBudgetTracker(budget Budget, meter Meter) *BudgetTracker {
	return newBudgetTracker(budget, meter, time.Now)
}

// newBudgetTracker takes a clock so time limits are testable without waiting.
func newBudgetTracker(budget Budget, meter Meter, clock func() time.Time) *BudgetTracker {
	return &BudgetTracker{
		budget: budget,
		meter:  meter,
		clock:  clock,
		start:  clock(),
	}
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

func (t *BudgetTracker) Exhausted() (bool, StopReason) {
	bytes, elapsed := t.Spent()

	if t.budget.MaxBytes > 0 && bytes >= t.budget.MaxBytes {
		return true, StopBudget
	}
	if t.budget.MaxTime > 0 && elapsed >= t.budget.MaxTime {
		return true, StopBudget
	}
	return false, StopCompleted
}

// Warning returns a BudgetWarning the first time the run crosses WarnAt on
// either ceiling, and nil afterwards - a warning repeated every tick is noise
// a client would have to de-duplicate itself.
func (t *BudgetTracker) Warning() *BudgetWarning {
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
