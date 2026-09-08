package web

import (
	"regexp"
	"strings"
	"testing"
)

// TOR-205: connectedCallback throws on a subtree that has not finished
// arriving, and leaves the element permanently dead.
//
// TOR-192 established the pattern docs/front-end.md's point 3 records: find
// every part with this.querySelector, throw by name on the first one
// missing. Right about a wrapper deleted from index.html - a wiring error -
// and wrong about a consumer whose HTML streams: an element can be connected
// with part of its subtree still on the way, and the old throw made that
// permanent, because connectedCallback does not run again on its own.
//
// WHICH ELEMENTS ACTUALLY NEEDED A FIX, and this is worth stating rather than
// assuming from "all six do the loud-throw pattern". Only three of the six
// can ever be connected against an INCOMPLETE subtree in the first place:
// run-table, frame-panel and compare-dialog wrap markup that index.html (or,
// for a streaming consumer, some other host document) parses and PARSES
// PROGRESSIVELY. The other three - run-detail, file-list, file-detail - build
// their own markup with `this.innerHTML = TEMPLATE` inside build(), which is
// synchronous: the instant that assignment returns, every part it wrote
// exists, in the same turn, with nothing left to arrive later. There is no
// "not yet" for those three to distinguish from "never" - a missing part
// there is, exactly as TOR-192 first reasoned, a wiring error and nothing
// else, so their throw is correct AS IS and TestNoSelfBuildingElementGrewA
// SettleMechanism below is what keeps that decision checked rather than
// assumed.
//
// THE MECHANISM the three streaming elements got (frame-panel.js's own
// header block carries the full reasoning; this file's tests check it did
// not drift from that header) is a MutationObserver on the element's own
// childList and subtree, created ONLY the first time wire() finds a part
// missing and disconnected the instant wiring succeeds - so on torpeek's own
// page, where index.html is fully parsed before app.js's deferred
// `type="module"` script ever calls customElements.define, the observer is
// never created at all (checked live in the browser pass at the bottom of
// columns_test.go, not here). A genuinely missing part still fails loudly:
// SETTLE_TIMEOUT_MS after first connect, with nothing wired, partsNeverArrived
// console.error()s the still-missing names and throws.
//
// Text guards, like the rest of this package's front-end tests (see
// columns_test.go's opening note and runtable_test.go's): they catch the
// mechanism deleted, renamed or regated, and they cannot see a MutationObserver
// actually fire. THAT needs a DOM, which plain node does not have (unlike
// state.js/events.js, these three modules are not DOM-free by design - the
// whole point of this ticket is what they do with document content) - so the
// behavioural half (an element connected early wires itself once its subtree
// arrives; the same element still throws if a part never does) is the browser
// pass recorded at the bottom of columns_test.go, driven for real against all
// six elements, not pretended here.

// settlingModules is the three elements that stream their markup and
// therefore needed TOR-205's fix, each with its module source and its own
// element tag (used in its throw/console.error messages).
func settlingModules(t *testing.T) []struct{ name, tag, src string } {
	t.Helper()
	return []struct{ name, tag, src string }{
		{"frame-panel.js", "frame-panel", liveJS(t, framePanelJS(t))},
		{"compare-dialog.js", "compare-dialog", liveJS(t, compareDialogJS(t))},
		{"run-table.js", "run-table", liveJS(t, runTableJS(t))},
	}
}

// TestConnectedCallbackOnlyCallsWire is the shape of the move itself: the
// part-finding TOR-192 put directly in connectedCallback now lives in wire(),
// so connectedCallback can be called from more than one place (the DOM
// reaction, and indirectly - via wire() - a MutationObserver callback and a
// deadline timer) without three copies of the same logic.
func TestConnectedCallbackOnlyCallsWire(t *testing.T) {
	for _, mod := range settlingModules(t) {
		connected := jsMethod(t, mod.src, "connectedCallback")
		if strings.TrimSpace(connected) != "connectedCallback() {\n    this.wire();" {
			t.Errorf("%s's connectedCallback is not exactly `this.wire();` - got %q. Every place that "+
				"might need to re-attempt wiring (the observer's callback, the deadline) calls wire() "+
				"directly instead of re-deriving connectedCallback's own logic, and this is what keeps "+
				"connectedCallback itself trivial enough to read at a glance", mod.name, connected)
		}
	}
}

// TestWireBailsQuietlyRatherThanThrowingOnAMissingPart is TOR-205's actual
// change: the loop that used to throw immediately on any missing part now
// collects the missing names and hands them to awaitParts() instead - no
// throw on this path at all, which is what "quietly" means.
func TestWireBailsQuietlyRatherThanThrowingOnAMissingPart(t *testing.T) {
	for _, mod := range settlingModules(t) {
		wire := jsMethod(t, mod.src, "wire")

		if !strings.Contains(wire, ".filter(([, node]) => !node).map(([name]) => name);") {
			t.Errorf("%s's wire() does not collect the missing part names - awaitParts() and "+
				"partsNeverArrived() both need that list to say which part is still absent", mod.name)
		}
		if !strings.Contains(wire, "if (missing.length) {\n      this.awaitParts(missing);\n      return;\n    }") {
			t.Errorf("%s's wire() does not bail into awaitParts() when a part is missing - the exact "+
				"shape matters here: a return with nothing else means this attempt does no partial "+
				"wiring and leaves every field exactly as it was found", mod.name)
		}
		// THE GUARD AGAINST THE DEGENERATE FIX: a version of this that still
		// throws on the very first miss (just later in the method, or wrapped
		// in a comment saying "quietly") would satisfy every English sentence
		// in this ticket while reintroducing the exact bug. So the absence of
		// a synchronous throw ANYWHERE in wire() itself is checked directly,
		// not inferred from the presence of awaitParts().
		if strings.Contains(wire, "throw ") {
			t.Errorf("%s's wire() throws - the whole point of TOR-205 is that a missing part inside "+
				"wire() is not necessarily a wiring error any more, so wire() itself must never throw; "+
				"a genuinely missing part is partsNeverArrived()'s job, on its own deadline", mod.name)
		}
	}
}

// TestWireIsIdempotent is the same guard TestTheDetailElementsSurviveTheTablesResort
// holds run-detail.js/file-list.js/file-detail.js's build() to, applied to
// wire(): awaitParts()'s observer calls wire() again on every mutation batch
// until it succeeds, so a second (or hundredth) call after wiring has already
// completed has to do nothing - re-running the listener registrations would
// double up every one of them.
func TestWireIsIdempotent(t *testing.T) {
	for _, mod := range settlingModules(t) {
		wire := jsMethod(t, mod.src, "wire")
		if !regexp.MustCompile(`^\s*wire\(\) \{\n\s*if \(this\.wired\) return;`).MatchString(wire) {
			t.Errorf("%s's wire() does not return early when this.wired is already true - a mutation "+
				"firing after wiring has already succeeded would re-run every addEventListener call: "+
				"%q", mod.name, firstLines(wire, 3))
		}
		if !strings.Contains(wire, "this.wired = true;") {
			t.Errorf("%s's wire() never sets this.wired - nothing would make the idempotency guard above "+
				"actually idempotent", mod.name)
		}
	}
}

// TestAwaitPartsCreatesOneObserverOnTheElementsOwnSubtree checks the mechanism
// itself: a MutationObserver watching THIS element (never document, which
// would fire for every mutation on the whole page) with both childList and
// subtree true (a missing part can be nested under a part that already
// arrived, not only a direct child), created once and guarded against being
// created a second time while still waiting.
func TestAwaitPartsCreatesOneObserverOnTheElementsOwnSubtree(t *testing.T) {
	for _, mod := range settlingModules(t) {
		await := jsMethod(t, mod.src, "awaitParts")
		if !strings.Contains(await, "if (this.partsObserver) return;") {
			t.Errorf("%s's awaitParts() does not guard against a second observer while one is already "+
				"pending - wire() calls awaitParts() again on every failed attempt while streaming, and "+
				"without this guard each one would create and leak its own MutationObserver", mod.name)
		}
		if !strings.Contains(await, "this.partsObserver = new MutationObserver(() => this.wire());") {
			t.Errorf("%s's awaitParts() does not create a MutationObserver that calls this.wire() - "+
				"nothing would re-attempt wiring when the subtree changes", mod.name)
		}
		if !strings.Contains(await, "this.partsObserver.observe(this, { childList: true, subtree: true });") {
			t.Errorf("%s's awaitParts() does not observe(this, {childList:true, subtree:true}) - "+
				"observing document (or anything other than this element) would fire on every "+
				"unrelated mutation on the page, and childList alone would miss a part nested under "+
				"one that already arrived", mod.name)
		}
		if !strings.Contains(await, "this.partsDeadline = setTimeout(() => this.partsNeverArrived(), SETTLE_TIMEOUT_MS);") {
			t.Errorf("%s's awaitParts() does not set the deadline timer - an observer alone cannot "+
				"tell \"still arriving\" from \"never coming\": a subtree that stops changing fires no "+
				"mutation this element is watching for, so nothing would ever call partsNeverArrived", mod.name)
		}
	}
}

// TestWiringSuccessTearsDownTheObserver is the other half of the cost
// argument this ticket asks to be stated: the observer is disconnected THE
// INSTANT wiring succeeds, not left running for the rest of the element's
// life, which is what keeps its cost bounded to the (rare) window between an
// early connect and the subtree finishing.
func TestWiringSuccessTearsDownTheObserver(t *testing.T) {
	for _, mod := range settlingModules(t) {
		wire := jsMethod(t, mod.src, "wire")
		if !strings.Contains(wire, "this.stopAwaitingParts();\n    this.wired = true;") {
			t.Errorf("%s's wire() does not call stopAwaitingParts() immediately before marking itself "+
				"wired - an observer (and a deadline timer) left running after a successful wiring is a "+
				"leak, and on a normally-parsed page (where the first attempt always succeeds) this is "+
				"the only place that would ever need to tear one down", mod.name)
		}

		stop := jsMethod(t, mod.src, "stopAwaitingParts")
		if !strings.Contains(stop, "this.partsObserver.disconnect();") {
			t.Errorf("%s's stopAwaitingParts() does not disconnect the observer", mod.name)
		}
		if !strings.Contains(stop, "clearTimeout(this.partsDeadline);") {
			t.Errorf("%s's stopAwaitingParts() does not clear the deadline timer - left running, it "+
				"would fire partsNeverArrived() after a wiring that already succeeded", mod.name)
		}
	}
}

// TestAGenuinelyMissingPartStillFailsLoudly is criterion 2: the two cases -
// a subtree that arrives late, and a part that never arrives - must be
// distinguishable, and this is the "never" half. partsNeverArrived() is what
// TOR-192's throw became: still a throw, still by name, just not synchronous
// with connectedCallback any more (a throw from a timer callback reaches no
// caller the way the original one did, which is why console.error runs
// first and names the part on its own).
func TestAGenuinelyMissingPartStillFailsLoudly(t *testing.T) {
	for _, mod := range settlingModules(t) {
		never := jsMethod(t, mod.src, "partsNeverArrived")

		// ONE LAST TRY FIRST, so a mutation and the deadline racing in the same
		// tick cannot report a failure for a wiring that in fact just succeeded.
		if !strings.Contains(never, "this.wire();\n    if (this.wired) return;") {
			t.Errorf("%s's partsNeverArrived() does not re-attempt wire() before giving up - a mutation "+
				"queued for the same tick as the deadline could otherwise be reported as a permanent "+
				"failure for a subtree that had, in fact, just finished arriving", mod.name)
		}
		if !strings.Contains(never, "console.error(message);") {
			t.Errorf("%s's partsNeverArrived() does not console.error before throwing - a throw from a "+
				"setTimeout callback reaches no caller (unlike the original connectedCallback throw), "+
				"so this is what still names the missing part in a build with no uncaught-error "+
				"reporting wired up to see the exception itself", mod.name)
		}
		if !strings.Contains(never, "throw new Error(message);") {
			t.Errorf("%s's partsNeverArrived() does not throw - a part still missing after the deadline "+
				"is TOR-192's original case (a genuine wiring error), and trading its loud failure away "+
				"is exactly what this ticket forbids", mod.name)
		}
		if !strings.Contains(never, `"`+mod.tag+`: no " + missing.join(", ") + " inside the element, "`) {
			t.Errorf("%s's partsNeverArrived() message does not name the module (%q) and the still-"+
				"missing parts - a thrown error with no context is exactly the \"cannot read property "+
				"of null\" TOR-192's original guard existed to avoid, reintroduced on a delay", mod.name, mod.tag)
		}
	}
}

// TestTheDeadlineIsStatedAndSharedByAllThree checks criterion 5's letter: the
// chosen mechanism's cost is a single named constant, not a number folded
// into an expression where a reader has to go looking for it, and the three
// modules agree with each other rather than each guessing its own number
// (which would make "how long does this wait" a per-file question with no
// reason to differ).
func TestTheDeadlineIsStatedAndSharedByAllThree(t *testing.T) {
	values := map[string]string{}
	for _, mod := range settlingModules(t) {
		m := regexp.MustCompile(`const SETTLE_TIMEOUT_MS = (\d+);`).FindStringSubmatch(mod.src)
		if m == nil {
			t.Fatalf("%s declares no `const SETTLE_TIMEOUT_MS = <number>;` - the deadline TOR-205's "+
				"guard rests on has to be a single named number, not folded into setTimeout's own call",
				mod.name)
		}
		values[mod.name] = m[1]
	}
	first := ""
	for name, v := range values {
		if first == "" {
			first = v
		} else if v != first {
			t.Errorf("SETTLE_TIMEOUT_MS is %sms in %s but %sms elsewhere - the three elements share one "+
				"mechanism and one piece of reasoning for its cost (frame-panel.js's own header), so a "+
				"different number in one of them is either an unstated reason or a drift", v, name, first)
		}
	}
}

// TestDisconnectedCallbackStopsAwaitingParts is the leak a partly-connected
// element could otherwise cause: removed from the page while still waiting
// for its subtree, an observer (and a timer) left running would keep firing
// - and, for the timer, eventually throw - for an element nothing can see
// any more.
func TestDisconnectedCallbackStopsAwaitingParts(t *testing.T) {
	for _, mod := range settlingModules(t) {
		teardown := jsMethod(t, mod.src, "disconnectedCallback")
		if !strings.Contains(teardown, "this.stopAwaitingParts();") {
			t.Errorf("%s's disconnectedCallback does not call stopAwaitingParts() - an element removed "+
				"from the page while still waiting for its subtree would leave its MutationObserver (and "+
				"deadline timer) running for nothing, and the timer would eventually throw for an "+
				"element nothing can see any more", mod.name)
		}
	}
}

// TestNoSelfBuildingElementGrewASettleMechanism is the other half of "which
// elements actually needed a fix" (this file's own header). run-detail.js,
// file-list.js and file-detail.js build their own markup synchronously
// (`this.innerHTML = TEMPLATE` inside build()) - there is no window in which
// they are connected with part of that markup missing, so TOR-192's original
// throw is still exactly correct for them and grafting on a MutationObserver
// would be pure cost with nothing behind it to justify it.
func TestNoSelfBuildingElementGrewASettleMechanism(t *testing.T) {
	for _, mod := range detailModules(t) {
		live := liveJS(t, mod.src)
		for _, absent := range []string{"MutationObserver", "awaitParts", "partsNeverArrived", "SETTLE_TIMEOUT_MS"} {
			if strings.Contains(live, absent) {
				t.Errorf("%s contains %q - this element builds its own markup synchronously in build(), "+
					"so it can never be connected with part of its subtree missing, and TOR-205's settle "+
					"mechanism would be cost with no bug behind it to justify it (see settle_test.go's own "+
					"header for the full reasoning)", mod.name, absent)
			}
		}
		// AND THE ORIGINAL GUARD IS STILL EXACTLY WHAT IT WAS: a synchronous
		// throw naming the missing part, checked already by
		// TestEachDetailElementFindsItsPartsAndFailsLoudly (detailtree_test.go)
		// - this test's own job is only the negative (no settle mechanism
		// grew here), not a second copy of that positive check.
	}
}
