package core

import (
	"testing"
	"time"
)

// TestRateSampleUnknownBeforeASecondReading is half of TOR-134's "absent,
// not zero" requirement (Progress.DownloadRate's own doc, TOR-119/TOR-111/
// TOR-135): a rate with no interval behind it yet must not be reported as
// zero, which would read as "stalled" for a run that has simply not had a
// second heartbeat.
func TestRateSampleUnknownBeforeASecondReading(t *testing.T) {
	var r rateSample

	if got := r.next(0, 0); got != nil {
		t.Fatalf("first sample = %v, want nil (no interval to divide by yet)", *got)
	}
}

// TestRateSampleDividesByRealElapsedTime is the ordinary case: a plain,
// evenly-spaced heartbeat reports the delta divided by the real gap between
// the two readings.
func TestRateSampleDividesByRealElapsedTime(t *testing.T) {
	var r rateSample

	r.next(0, 0)
	got := r.next(10<<20, 2*time.Second) // 10 MiB over 2s

	if got == nil {
		t.Fatal("second sample = nil, want a rate")
	}
	want := float64(10<<20) / 2
	if *got != want {
		t.Errorf("rate = %v B/s, want %v B/s", *got, want)
	}
}

// TestRateSampleStallThenBurstIsNotReportedAsTheBurstSpeed is TOR-134's
// central acceptance criterion: a run that goes quiet for twenty seconds and
// then receives a burst of bytes must not report the burst's own peak rate
// as its speed for the whole window - the delta has to be divided by the
// REAL elapsed time (stall included), not a nominal heartbeat period.
//
// Sequence: heartbeat at t=0 with 0 bytes (establishes the baseline), a
// twenty-second stall with nothing arriving, then one heartbeat at t=21s
// carrying a 10 MiB burst that itself took roughly one second to arrive. A
// rate careless about the interval - dividing the burst by a nominal
// per-heartbeat period, or by the burst's own duration alone - would report
// something close to 10 MiB/s. The correct figure divides by the entire
// 21-second window since the last heartbeat: about 499 KiB/s, an order of
// magnitude lower, and the number that actually describes what happened.
func TestRateSampleStallThenBurstIsNotReportedAsTheBurstSpeed(t *testing.T) {
	var r rateSample

	r.next(0, 0) // baseline heartbeat

	burstBytes := int64(10 << 20)
	burstOnlyRate := float64(burstBytes) / 1.0 // what a burst-duration-only rate would say

	got := r.next(burstBytes, 21*time.Second) // stall (20s) + burst (~1s)
	if got == nil {
		t.Fatal("sample after the stall+burst = nil, want a rate")
	}

	want := float64(burstBytes) / 21.0
	if *got != want {
		t.Errorf("rate = %v B/s, want %v B/s (delta divided by the real 21s window)", *got, want)
	}

	// The number a test guarding against the bug actually has to fail on:
	// nothing close to the burst's own standalone rate.
	if *got > burstOnlyRate/2 {
		t.Errorf("rate = %v B/s is close to the burst's own peak rate of %v B/s - "+
			"the stall was not counted", *got, burstOnlyRate)
	}
}

// TestRateSampleNonAdvancingClockIsUnknown covers a repeated or rewound
// heartbeat: with no positive interval to divide by, the honest answer is
// unknown, not a divide-by-zero or a negative rate.
func TestRateSampleNonAdvancingClockIsUnknown(t *testing.T) {
	var r rateSample

	r.next(100, 5*time.Second)
	if got := r.next(200, 5*time.Second); got != nil {
		t.Fatalf("sample with no elapsed advance = %v, want nil", *got)
	}
	if got := r.next(50, 4*time.Second); got != nil {
		t.Fatalf("sample with elapsed going backwards = %v, want nil", *got)
	}
}

// TestRunSpeedSharesOneClockAcrossDownloadAndUpload checks runSpeed's own
// job: both directions are measured against the SAME elapsed reading on one
// heartbeat, and each tracks its own byte delta independently.
func TestRunSpeedSharesOneClockAcrossDownloadAndUpload(t *testing.T) {
	s := &runSpeed{}

	if down, up := s.sample(0, 0, 0); down != nil || up != nil {
		t.Fatalf("first sample = (%v, %v), want (nil, nil)", down, up)
	}

	down, up := s.sample(4<<20, 1<<20, 2*time.Second)
	if down == nil || up == nil {
		t.Fatalf("second sample = (%v, %v), want two rates", down, up)
	}
	if want := float64(4<<20) / 2; *down != want {
		t.Errorf("download rate = %v, want %v", *down, want)
	}
	if want := float64(1<<20) / 2; *up != want {
		t.Errorf("upload rate = %v, want %v", *up, want)
	}
}
