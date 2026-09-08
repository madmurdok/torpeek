package web

import (
	"strings"
	"testing"
)

// TOR-142 added a third strip inside an expanded file, on its own row above
// the piece strip TOR-111 already drew: the file's own piece span as a
// track, the claimed capture points as ticks on it, and the swarm's
// availability as a chip beside it. TOR-153 removed that row - the owner,
// looking at the running UI, could not tell it apart from the piece strip
// immediately below: "41 of 197 pieces claimed" and "41 of 197 pieces
// ordered (21%)" are one fact in two words, drawn as two near-identical rows
// of cyan marks. Only the swarm chip answered a genuinely different
// question, so it is the only part that survives, moved onto the piece
// strip's own row.
//
// These guard the two decisions that make the merged row honest rather than
// merely drawn, both of which a well-meaning edit would undo without
// noticing.
//
// Following this package's own precedent (columns_test.go says why): there is
// no JS runner here, so these read the served source. That catches a deletion
// or a rename and cannot catch a wrong answer - TOR-148 is the ticket about
// closing that gap. The drawing itself was checked in a browser, live and
// reopened from disk, down to a ~250px pane.

// TestTheSwarmReadingIsAChipAndNeverAFillOnTheStrip is the load-bearing one,
// carried over from TOR-142 and repointed at the strip the chip now sits on.
//
// entry.live.swarm is ONE mean copies-per-piece for the whole torrent
// (core.Progress.Swarm's own doc), not a figure per position. The reach
// strip is the file's piece span, so anything painted along it claims to say
// something about a POSITION. Painting the swarm reading there would
// therefore claim a resolution the data does not have - and worse, it would
// put "what the swarm holds" and "what this run ordered" on one axis in one
// colour, which is exactly the conflation Progress.Swarm's doc warns a
// client against.
//
// So the shapes have to stay different: a strip of blocks for the claims, a
// chip for the swarm. This test fails if the chip stops being its own
// element or starts sharing the strip's own axis.
func TestTheSwarmReadingIsAChipAndNeverAFillOnTheStrip(t *testing.T) {
	css := stylesheet(t)

	// The chip is its own box, outside the strip.
	for _, sel := range []string{".avail-swarm", ".avail-swarm-dot", ".reach-strip", ".reach-block"} {
		if !strings.Contains(css, sel) {
			t.Errorf("app.css no longer has %s - the row's parts must stay separate elements", sel)
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
	if strings.Contains(css, ".reach-block[data-health") {
		t.Error("a reach block carries a data-health attribute: the swarm reading has been " +
			"moved onto the position axis, where it claims a per-position resolution " +
			"it does not have (core.Progress.Swarm)")
	}
}

// TestARunWithNoClaimsRecordedRendersAsNeitherFullNorEmpty carries over the
// other half of TOR-142's guard. Before TOR-153, an unknown extent hatched
// the now-removed track; the track is gone, but a set that hasn't recorded
// where its frames came from still has nothing to say about WHERE it
// reached, and the merged row must not lie about that by rendering full or
// empty.
//
// WHICH unknown that is has moved since, though the guard has not: TOR-179
// re-sourced the strip from each frame's own record in the manifest
// (manifest.Frame.ByteRanges), so the unknown case is now a set captured
// before that field existed rather than a run.json written before TOR-119
// kept a claim log. Same rendering, same reason, a different record behind
// it - and web.reachOf is where the two are told apart.
//
// It also must not go back to hiding the whole row the way TOR-111's strip
// used to when nothing had been claimed: the swarm chip now lives on this
// row too, and that reading doesn't depend on this file's own claims, so the
// row has to stay visible for the chip even while the strip itself is
// hatched.
func TestARunWithNoClaimsRecordedRendersAsNeitherFullNorEmpty(t *testing.T) {
	// RETARGETED ONTO file-detail.js BY TOR-195: the reach strip is one
	// FILE's, so it went with the rest of that file's detail into the
	// innermost of the three nested elements - as a method, which is why the
	// anchor below is `renderReach(sets) {` rather than a `function`.
	js := fileDetailJS(t)

	if !strings.Contains(js, "renderReach(sets) {") {
		t.Fatal("renderReach is gone")
	}
	// The known/unknown toggle has to come from the measured data (whether
	// any set actually carries a reach), not be a css-only claim with
	// nothing in the JS actually setting it per file.
	if !strings.Contains(js, "reachStrip.dataset.known") {
		t.Error("renderReach no longer sets reachStrip's data-known - the not-known-yet " +
			"state has stopped being computed from whether a claim was actually recorded")
	}

	if !strings.Contains(stylesheet(t), ".reach-strip[data-known=\"false\"]") {
		t.Error("the strip has no not-known-yet state, so a run with no claims recorded " +
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
	// SINCE TOR-191 both functions are state.js's - a swarm reading and the
	// sentence about it are derivations over an entry - and the strip's chip
	// is file-detail.js's renderAvail (TOR-195). So "reused rather than
	// re-phrased" is now
	// checkable directly, by reading the one function that draws the chip,
	// instead of by counting mentions in one file and hoping two of them were
	// the definition and a call.
	chip := jsMethod(t, fileDetailJS(t), "renderAvail")
	for _, fn := range []string{"availabilityReading(entry)", "availabilityCellTitle(entry)"} {
		if !strings.Contains(chip, fn) {
			t.Errorf("renderAvail does not call %s: the strip has stopped reusing the "+
				"columns' own wording and is phrasing the same fact itself", fn)
		}
	}

	// And the sentence itself lives in exactly one place, so there is nothing
	// for the chip and the column to disagree about. The distinctive clause is
	// enough to find it: the whole sentence is long, and both readers get it
	// from the same function.
	const sentence = "copies per piece, on average, across the swarm"
	if n := strings.Count(stateJS(t), sentence); n != 1 {
		t.Errorf("state.js states %q %d times, want exactly 1 - two copies of one sentence "+
			"is two chances to change only one of them", sentence, n)
	}
	if strings.Contains(fileDetailJS(t), sentence) {
		t.Error("file-detail.js phrases the availability sentence itself as well - the column " +
			"and the chip six inches below it would be able to word the same fact differently")
	}
}
