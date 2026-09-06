package web

import (
	"strings"
	"testing"
)

// TOR-142 added the third strip inside an expanded file: the file's own piece
// span as a track, the claimed capture points as ticks on it, and the swarm's
// availability as a chip beside it. These guard the two decisions that make
// it honest rather than merely drawn, both of which a well-meaning edit would
// undo without noticing.
//
// Following this package's own precedent (columns_test.go says why): there is
// no JS runner here, so these read the served source. That catches a deletion
// or a rename and cannot catch a wrong answer - TOR-148 is the ticket about
// closing that gap. The drawing itself was checked in a browser, live and
// reopened from disk, down to a ~250px pane.

// TestTheSwarmReadingIsAChipAndNeverAFillOnTheTrack is the load-bearing one.
//
// entry.live.swarm is ONE mean copies-per-piece for the whole torrent
// (core.Progress.Swarm's own doc), not a figure per position. The track is
// the file's piece span, so anything painted along it claims to say something
// about a POSITION. Painting the swarm reading there would therefore claim a
// resolution the data does not have - and worse, it would put "what the swarm
// holds" and "what this run ordered" on one axis in one colour, which is
// exactly the conflation Progress.Swarm's doc warns a client against.
//
// So the shapes have to stay different: a track with ticks for the claims, a
// chip for the swarm. This test fails if the chip stops being its own element
// or starts sharing the track's position axis.
func TestTheSwarmReadingIsAChipAndNeverAFillOnTheTrack(t *testing.T) {
	css := stylesheet(t)

	// The chip is its own box, outside the track.
	for _, sel := range []string{".avail-swarm", ".avail-swarm-dot", ".avail-track", ".avail-point"} {
		if !strings.Contains(css, sel) {
			t.Errorf("app.css no longer has %s - the strip's parts must stay separate elements", sel)
		}
	}

	// The claims are drawn in the accent, which is this page's "what the tool
	// did" colour; the swarm's health is never that colour, so the two facts
	// cannot be read as one.
	if !strings.Contains(css, ".avail-swarm[data-health=\"unknown\"]") {
		t.Error("the swarm chip has no unknown state - absent is not zero, and a chip " +
			"with no unknown state has to invent a health it does not know")
	}
	for _, health := range []string{"ok", "warn", "bad"} {
		if !strings.Contains(css, ".avail-swarm[data-health=\""+health+"\"]") {
			t.Errorf("the swarm chip has no %q state", health)
		}
	}
	if strings.Contains(css, ".avail-track[data-health") {
		t.Error("the track carries a data-health attribute: the swarm reading has been " +
			"moved onto the position axis, where it claims a per-position resolution " +
			"it does not have (core.Progress.Swarm)")
	}
}

// TestTheCapturePointsComeFromClaimedPiecesNotATimecode guards the decision
// TOR-111 made and this release was told to keep: where the frames came from
// is drawn from the CLAIMED ranges, which are a measurement, never from
// multiplying a timecode by a bitrate no container promises.
//
// The tell is fentry.reachData - the same Reach the piece strip below already
// uses, so the two strips cannot disagree about the same file. If the ticks
// ever start being computed from a duration, this is what stops being true.
func TestTheCapturePointsComeFromClaimedPiecesNotATimecode(t *testing.T) {
	js := appJS(t)

	// fentry.reachData specifically, not the bare word: the field can exist
	// on the entry while renderAvail has quietly stopped reading it, and a
	// substring check for "reachData" alone passes in exactly that case -
	// measured, when this guard was falsified by renaming only the reads.
	if !strings.Contains(js, "fentry.reachData") {
		t.Fatal("renderAvail no longer reads fentry.reachData - the capture-point ticks " +
			"have stopped coming from the claimed ranges the piece strip uses")
	}
	if strings.Count(js, "reachData") < 3 {
		t.Errorf("reachData appears %d times: it is set where renderReach picks the "+
			"Reach, cleared on reset, and read by renderAvail, so fewer than three "+
			"means one of those three sites is gone", strings.Count(js, "reachData"))
	}
	if !strings.Contains(js, "function renderAvail(") {
		t.Fatal("renderAvail is gone")
	}

	// A track with no unknown state would have to render "no claims recorded"
	// as either full or empty, and both are lies about a run that simply has
	// not claimed anything yet.
	if !strings.Contains(stylesheet(t), ".avail-track[data-known=\"false\"]") {
		t.Error("the track has no not-known-yet state, so a run with no claims recorded " +
			"must render as full or as empty - absent is not zero")
	}
}

// TestTheStripReusesTheColumnsOwnAvailabilitySentence keeps one wording for
// one fact. The six live columns already answer "what does the swarm hold"
// and "why is there no reading" through availabilityReading and
// availabilityCellTitle; the strip calls the same two functions rather than
// phrasing it a second time, so the page cannot say "no reading" one way in a
// column and another way in the drawing six inches below it.
func TestTheStripReusesTheColumnsOwnAvailabilitySentence(t *testing.T) {
	js := appJS(t)

	for _, fn := range []string{"availabilityReading", "availabilityCellTitle"} {
		if strings.Count(js, fn) < 2 {
			t.Errorf("%s is referenced fewer than twice: the strip has stopped reusing "+
				"the columns' own wording and is phrasing the same fact itself", fn)
		}
	}
}
