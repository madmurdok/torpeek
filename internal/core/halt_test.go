package core

import (
	"context"
	"testing"
	"time"
)

// TestHaltReasonReportsTimeForAnExpiredContext is TOR-161's unit test for the
// OTHER path StopBudget used to answer for a clock stop: haltReason's own
// ctx.Err() branch, which fires when BudgetTracker.Context's deadline
// expires on its own rather than through Exhausted() being polled between
// capture points. Before TOR-161 this returned StopBudget unconditionally,
// which is doubly wrong to name here since the branch fires ONLY when the
// context outlived its deadline - and the only deadline BudgetTracker.Context
// ever sets is the time budget - so the answer was never actually about
// bytes to begin with.
//
// Both arms: a tracker whose context has expired (the clock's own doing)
// against the same tracker asked through the ordinary Exhausted() path with
// its byte ceiling crossed instead - proving haltReason's two ways of
// stopping report DIFFERENT reasons rather than one value that happens to
// satisfy whichever assertion is checked.
func TestHaltReasonReportsTimeForAnExpiredContext(t *testing.T) {
	// A genuinely short REAL duration, the same technique
	// TestContextExpiresWithTheTimeBudget uses and for the same reason:
	// context.WithTimeout schedules against the real clock regardless of
	// what clock a BudgetTracker was built with, so advancing a fakeClock
	// after Context() has already computed its timeout would not cancel
	// this context any sooner - there would be nothing here to wait for
	// except the real duration passed to WithTimeout.
	tracker := NewBudgetTracker(Budget{MaxTime: 150 * time.Millisecond}, &fakeMeter{}, Roof{}, nil)

	ctx, cancel := tracker.Context(context.Background())
	defer cancel()

	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("context never expired with the time budget")
	}

	reason, halt := haltReason(ctx, tracker)
	if !halt {
		t.Fatal("haltReason did not ask to stop for an expired context")
	}
	if reason != StopTime {
		t.Errorf("haltReason on an expired context = %q, want %q - the only deadline "+
			"BudgetTracker.Context ever sets is the time budget", reason, StopTime)
	}

	// The other arm: bytes exhausted, no expired context in sight. This is
	// what proves the StopTime above is the deadline's doing and not
	// haltReason defaulting to it regardless of cause.
	bytesClock := newFakeClock()
	bytesMeter := &fakeMeter{}
	bytesTracker := newBudgetTracker(Budget{MaxBytes: 1000, MaxTime: time.Hour},
		bytesMeter, Roof{}, nil, bytesClock.Now)
	bytesMeter.set(1000)

	reason, halt = haltReason(context.Background(), bytesTracker)
	if !halt {
		t.Fatal("haltReason did not ask to stop for an exhausted byte ceiling")
	}
	if reason != StopBudget {
		t.Errorf("haltReason on a byte-exhausted tracker = %q, want %q", reason, StopBudget)
	}
}

// TestHaltReasonReportsCancelledForAnUnrelatedCancellation keeps the
// ctx.Err() branch from over-reporting: a context cancelled by its caller -
// not by a deadline - must still say StopCancelled, never StopTime.
func TestHaltReasonReportsCancelledForAnUnrelatedCancellation(t *testing.T) {
	clock := newFakeClock()
	tracker := newBudgetTracker(Budget{MaxTime: time.Hour}, &fakeMeter{}, Roof{}, nil, clock.Now)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reason, halt := haltReason(ctx, tracker)
	if !halt {
		t.Fatal("haltReason did not ask to stop for a cancelled context")
	}
	if reason != StopCancelled {
		t.Errorf("haltReason on a caller-cancelled context = %q, want %q", reason, StopCancelled)
	}
}
