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

// TestStallClockStartsOnFirstCauseAndKeepsRunning is stallClock's ordinary
// case: the first sighting of a cause starts the clock at zero, and a later
// sighting of the SAME cause reports the duration since that start, not
// since the later sighting - the whole point of tracking a start time rather
// than merely echoing back whatever "since" a caller last passed in.
func TestStallClockStartsOnFirstCauseAndKeepsRunning(t *testing.T) {
	var c stallClock
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	first := c.observe(t0, CodeNoPeers)
	if first == nil {
		t.Fatal("first observe(NoPeers) = nil, want a Stall")
	}
	if first.Code != CodeNoPeers || first.Since != 0 {
		t.Errorf("first reading = %+v, want {no_peers 0}", first)
	}

	later := c.observe(t0.Add(45*time.Second), CodeNoPeers)
	if later == nil {
		t.Fatal("later observe(NoPeers) = nil, want a Stall")
	}
	if later.Code != CodeNoPeers || later.Since != 45*time.Second {
		t.Errorf("later reading = %+v, want {no_peers 45s} - the SAME cause "+
			"repeated must not reset its own start", later)
	}
}

// TestStallClockRestartsOnADifferentCause is the rule this ticket calls out
// by name: "a duration that quietly restarts on every heartbeat would look
// like a fresh problem forever" is the bug to avoid, but the mirror image is
// a real bug too - two DIFFERENT causes back to back are two shorter waits,
// not one long one that happens to have changed its story. A clock that
// failed to restart here would report an unavailable swarm as having been
// true since the run had no peers at all, which is false.
func TestStallClockRestartsOnADifferentCause(t *testing.T) {
	var c stallClock
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	c.observe(t0, CodeNoPeers)
	c.observe(t0.Add(30*time.Second), CodeNoPeers)

	// Peers arrived, but the pieces asked for are not among them - a
	// different cause, ten seconds after it became true.
	switched := c.observe(t0.Add(40*time.Second), CodeUnavailable)
	if switched == nil {
		t.Fatal("observe after a cause change = nil, want a Stall")
	}
	if switched.Code != CodeUnavailable {
		t.Fatalf("code = %s, want %s", switched.Code, CodeUnavailable)
	}
	if switched.Since != 0 {
		t.Errorf("since = %v, want 0 - a new cause starts its own clock, it "+
			"does not inherit the old one's elapsed time", switched.Since)
	}
}

// TestStallClockClearsOnSuccess is the third rule: an empty code - real
// progress - clears the clock outright rather than merely pausing it, so a
// cause that returns after a genuine success is treated as new, not as a
// resumption of the old wait.
func TestStallClockClearsOnSuccess(t *testing.T) {
	var c stallClock
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	c.observe(t0, CodeReadStalled)
	if got := c.observe(t0.Add(10*time.Second), ""); got != nil {
		t.Fatalf("observe(\"\") = %+v, want nil - a success clears the reading", got)
	}

	// The same cause, back again after the clock was cleared, starts fresh
	// rather than resuming at the ten seconds it had already reached.
	resumed := c.observe(t0.Add(70*time.Second), CodeReadStalled)
	if resumed == nil || resumed.Since != 0 {
		t.Fatalf("resumed reading = %+v, want {read_stalled 0} - a cleared "+
			"clock must not remember the wait it had before the success", resumed)
	}
}

// TestStallClockPeekReportsWithoutMutating is what lets a heartbeat with
// nothing definitive of its own to add (engine.go's startFileHeartbeat,
// mid-read) still report an honest, growing duration: peek must read the
// clock's current start time without resetting it, however many times it is
// called, and observe must still see the ORIGINAL start the next time it has
// real evidence.
func TestStallClockPeekReportsWithoutMutating(t *testing.T) {
	var c stallClock
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	c.observe(t0, CodeNoPeers)

	p1 := c.peek(t0.Add(5 * time.Second))
	p2 := c.peek(t0.Add(9 * time.Second))
	if p1 == nil || p1.Since != 5*time.Second {
		t.Fatalf("first peek = %+v, want {no_peers 5s}", p1)
	}
	if p2 == nil || p2.Since != 9*time.Second {
		t.Fatalf("second peek = %+v, want {no_peers 9s}", p2)
	}

	// observe, given the SAME cause after two peeks, must still report the
	// duration from the ORIGINAL t0 - not from whichever peek ran last.
	again := c.observe(t0.Add(11*time.Second), CodeNoPeers)
	if again == nil || again.Since != 11*time.Second {
		t.Fatalf("observe after peeking = %+v, want {no_peers 11s} - peek "+
			"must never have moved the clock's own start", again)
	}
}

// TestStallClockPeekIsNilBeforeAnyCause is peek's other half: with nothing
// ever observed, there is no clock running to report, and peek must say so
// (nil) rather than inventing a zero-duration reading.
func TestStallClockPeekIsNilBeforeAnyCause(t *testing.T) {
	var c stallClock
	if got := c.peek(time.Now()); got != nil {
		t.Fatalf("peek on a clock that has never observed a cause = %+v, want nil", got)
	}
}

// TestClassifyPointFailureTellsNoPeersApartFromUnavailable is the discipline
// this ticket itself demands: a test that distinguishes causes has to be
// shown able to tell them apart, not merely pass whichever one it is given.
// Both branches are asserted here, from the SAME code
// (CodeUnavailable) - only the peer count differs, and it alone is what has
// to flip the answer, which is the whole reason CodeNoPeers exists as
// something other than a synonym for CodeUnavailable (classifyPointFailure's
// own doc, and errors.go's CodeUnavailable comment: "the pieces needed are
// not held by any connected peer" - a claim that presumes a peer to ask).
func TestClassifyPointFailureTellsNoPeersApartFromUnavailable(t *testing.T) {
	if got := classifyPointFailure(CodeUnavailable, 0); got != CodeNoPeers {
		t.Errorf("classifyPointFailure(Unavailable, 0 peers) = %s, want %s - "+
			"CodeUnavailable presumes a peer to ask, and there is none", got, CodeNoPeers)
	}
	if got := classifyPointFailure(CodeUnavailable, 3); got != CodeUnavailable {
		t.Errorf("classifyPointFailure(Unavailable, 3 peers) = %s, want %s - "+
			"with peers actually connected, CodeUnavailable is the honest answer", got, CodeUnavailable)
	}
}

// TestClassifyPointFailureTellsNoPeersApartFromReadStalled is the same
// distinction for the other reclassified code: a read that timed out with
// nobody connected is a different statement from one that timed out with
// peers present but slow or withholding, and the two must not collapse into
// one CodeReadStalled the way they would if peers were ignored.
func TestClassifyPointFailureTellsNoPeersApartFromReadStalled(t *testing.T) {
	if got := classifyPointFailure(CodeReadStalled, 0); got != CodeNoPeers {
		t.Errorf("classifyPointFailure(ReadStalled, 0 peers) = %s, want %s", got, CodeNoPeers)
	}
	if got := classifyPointFailure(CodeReadStalled, 1); got != CodeReadStalled {
		t.Errorf("classifyPointFailure(ReadStalled, 1 peer) = %s, want %s - "+
			"a connected peer that still did not deliver is genuinely a read "+
			"stall, not a peer-count problem", got, CodeReadStalled)
	}
}

// TestClassifyPointFailureClearsOnRealProgress is classifyPointFailure's
// other rule: an outcome that means data actually flowed - a seek that
// missed its mark, or no failure at all - must not be reported as a stall
// cause, whatever the peer count says. Two different codes are checked
// against the SAME zero-peer count that turns the other two codes into
// CodeNoPeers, specifically to show peers alone is not what decides this
// branch - only the point's own outcome does.
func TestClassifyPointFailureClearsOnRealProgress(t *testing.T) {
	if got := classifyPointFailure(CodeSeekFailed, 0); got != "" {
		t.Errorf("classifyPointFailure(SeekFailed, 0 peers) = %s, want \"\" - "+
			"a landed read is not a stall, even with nobody else connected", got)
	}
	if got := classifyPointFailure("", 0); got != "" {
		t.Errorf("classifyPointFailure(\"\", 0 peers) = %s, want \"\"", got)
	}
}

// TestClassifyLiveStallTellsNoPeersApartFromUnavailableSwarm is the same
// distinction classifyPointFailure draws, asked instead from torrent-level
// facts alone - no attempt has finished yet to ask about (the case
// startFileHeartbeat exists for, ticking while a single read is still in
// flight). Zero peers must read as CodeNoPeers regardless of what the
// availability reading says; a fully unavailable swarm with peers present
// must read as CodeUnavailable; and a healthy or not-yet-known swarm must
// read as "nothing definitive" ("") rather than guessing.
func TestClassifyLiveStallTellsNoPeersApartFromUnavailableSwarm(t *testing.T) {
	fullyUnavailable := &SwarmAvailability{NumPieces: 10, Unavailable: 10}

	if got := classifyLiveStall(0, fullyUnavailable); got != CodeNoPeers {
		t.Errorf("classifyLiveStall(0 peers, fully unavailable swarm) = %s, want %s - "+
			"zero peers is the more basic fact and must win", got, CodeNoPeers)
	}
	if got := classifyLiveStall(4, fullyUnavailable); got != CodeUnavailable {
		t.Errorf("classifyLiveStall(4 peers, fully unavailable swarm) = %s, want %s", got, CodeUnavailable)
	}
	if got := classifyLiveStall(4, nil); got != "" {
		t.Errorf("classifyLiveStall(4 peers, unknown swarm) = %s, want \"\" - "+
			"an unread availability must not be reported as certainty either way "+
			"(the same rule newSwarmAvailability's own doc gives)", got)
	}
	partiallyAvailable := &SwarmAvailability{NumPieces: 10, Unavailable: 3}
	if got := classifyLiveStall(4, partiallyAvailable); got != "" {
		t.Errorf("classifyLiveStall(4 peers, partially available swarm) = %s, want \"\" - "+
			"some pieces are held, which is not the fully-unavailable case", got)
	}
}
