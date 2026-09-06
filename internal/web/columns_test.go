package web

import (
	"regexp"
	"strings"
	"testing"
)

// TOR-139 added six sortable columns to the run table - peers, seeds, both
// rates, availability and queue position - and the two rules the ticket
// itself is about: ABSENT IS NOT ZERO (a queued row's missing reading must
// never render or sort as though it were a measured zero), and availability
// is copies per piece, not a percentage.
//
// There is no JS test runner in this repository (see internal/web's other
// _test.go files, none of which run app.js - they all read it as served
// text, the same way TestServesEmbeddedFrontend does for "WebSocket" and
// ".grid"). These tests follow that precedent: they assert on the exact
// source app.js ships, which is a real - if narrower - guard than executing
// it would be. What they do NOT and cannot cover is documented on each test
// and summarized at the bottom of this file.

// appJS returns the embedded app.js source, the same way stylesheet(t) in
// theme_test.go reads app.css - from the embedded FS, because that is the
// copy that ships.
func appJS(t *testing.T) string {
	t.Helper()
	b, err := embedded.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("reading the embedded app.js: %v", err)
	}
	return string(b)
}

// TestLiveColumnsAreWiredIntoBothHeadersAndSorting guards against the two
// halves of TOR-139 drifting apart: LIVE_COLUMNS is what builds each <th> (so
// a column with no header cell cannot be clicked to sort at all), and the
// matching case in sortValue's switch is what makes clicking it actually
// reorder anything (a header with no case falls through to sortValue's
// "when" default, which would silently sort every new column exactly like
// the date column instead of by its own figure). A column present in one and
// not the other is a column that looks wired but is not.
func TestLiveColumnsAreWiredIntoBothHeadersAndSorting(t *testing.T) {
	js := appJS(t)

	for _, key := range []string{"peers", "seeds", "download_bps", "upload_bps", "availability", "priority"} {
		if !strings.Contains(js, `key: "`+key+`"`) {
			t.Errorf("app.js's LIVE_COLUMNS declares no %q column - it would have no header cell, so nothing to click", key)
		}
		if !strings.Contains(js, `case "`+key+`":`) {
			t.Errorf("app.js's sortValue() has no case for %q - its header would exist but clicking it would fall "+
				"through to the default (When) ordering instead of sorting by its own figure", key)
		}
	}
}

// TestAvailabilityHeaderNamesItsUnitOnThePage is the acceptance criterion
// written directly into the ticket: "Availability's unit is legible from the
// header." Copies per piece commonly exceeds 1.0 (a healthy swarm might read
// 3.2), which reads as nonsense to anyone who assumes a percentage - so the
// unit has to be on the page itself, not only in a title attribute nobody
// hovers over lookng for it.
func TestAvailabilityHeaderNamesItsUnitOnThePage(t *testing.T) {
	js := appJS(t)

	// unit: "..." is what buildLiveColumnHeaders() (app.js) turns into a
	// second, visible line under the "Avail" label - see LIVE_COLUMNS and
	// .run-th-unit in app.css. A tooltip-only mention would not satisfy this:
	// the whole point is that a person never has to open one.
	if !regexp.MustCompile(`key:\s*"availability"[^}]*unit:\s*"copies/piece"`).MatchString(js) {
		t.Error(`app.js's availability column declares no unit: "copies/piece" - the header would show only ` +
			`"Avail" with nothing to say the figure is not a percentage`)
	}
}

// TestAbsentLiveFiguresAreNullNeverZero is ABSENT IS NOT ZERO, read directly
// off sortValue's own switch: each of peers/seeds/download_bps/upload_bps/
// availability must fall back to null, literally, when entry.live (or the
// specific reading within it) is missing - never a bare 0, which sortValue
// would then treat as a real, comparable measurement instead of excluding it
// (see compareEntries' own absence handling, checked separately below).
//
// This is a whole-line match against the exact source rather than a looser
// substring, on purpose: "return hasLive(entry) ? entry.live.peers : 0"
// contains every word "... : null" does except the one that matters, so a
// substring check for "hasLive(entry)" alone would stay green through
// exactly the regression this test exists to catch.
func TestAbsentLiveFiguresAreNullNeverZero(t *testing.T) {
	js := appJS(t)

	for _, want := range []string{
		`case "peers": return hasLive(entry) ? entry.live.peers : null;`,
		`case "seeds": return hasLive(entry) ? entry.live.seeds : null;`,
		`case "download_bps": return hasLive(entry) && entry.live.download_bps != null ? entry.live.download_bps : null;`,
		`case "upload_bps": return hasLive(entry) && entry.live.upload_bps != null ? entry.live.upload_bps : null;`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js's sortValue() does not contain %q - an absent reading may be falling back to 0 "+
				"(a real, comparable value) instead of null (excluded from comparison, see compareEntries)", want)
		}
	}

	// availability's case is a small block rather than one line (it reads
	// entry.live.swarm, which can be absent even when entry.live itself is
	// present - a torrent that has not been asked for bytes yet), so this
	// checks the block returns null on both of the ways it can be absent.
	m := regexp.MustCompile(`case "availability": \{([^}]*)\}`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal(`app.js's sortValue() has no case "availability": { ... } block`)
	}
	if !strings.Contains(m[1], "return s ? s.copies_per_piece : null;") {
		t.Errorf("app.js's availability sortValue case does not return null when the swarm reading is absent: %q", m[1])
	}
}

// TestAbsentValuesSortToTheEndRegardlessOfDirection is the sorting trap the
// ticket names directly: a column where most rows are absent has to decide
// where those rows go, and treating absence as the lowest value would bury a
// running torrent with a real zero reading among the queued rows that have
// none - backwards from what a person sorting "fewest peers first" wants.
// TOR-139's decision is that an absent row sinks to the end on EVERY sort,
// ascending or descending alike.
//
// This checks the decision is actually unconditional: compareEntries must
// decide aAbsent/bAbsent and return before it ever reaches the
// `dir === "desc"` flip - a later refactor that moved the absence check
// after that flip would make an absent row's position depend on sort
// direction, which is exactly the bug this test exists to catch, and a
// regex for "aAbsent" alone would not notice the reordering.
func TestAbsentValuesSortToTheEndRegardlessOfDirection(t *testing.T) {
	js := appJS(t)

	m := regexp.MustCompile(`(?s)function compareEntries\(a, b\) \{.*?\n\}`).FindString(js)
	if m == "" {
		t.Fatal("app.js has no compareEntries(a, b) function to check")
	}

	absentIdx := strings.Index(m, "return aAbsent ? 1 : -1;")
	if absentIdx < 0 {
		t.Fatal(`compareEntries does not contain "return aAbsent ? 1 : -1;" - the absent-goes-last decision`)
	}
	dirIdx := strings.Index(m, `dir === "desc"`)
	if dirIdx < 0 {
		t.Fatal(`compareEntries does not reverse for a descending sort at all (no "dir === "desc"")`)
	}
	if absentIdx > dirIdx {
		t.Error("compareEntries checks dir before it decides whether either value is absent - an absent row's " +
			"position would then depend on sort direction instead of always sinking to the end")
	}

	if !strings.Contains(m, "if (aAbsent && bAbsent) return 0") {
		t.Error("compareEntries does not special-case two absent rows against each other - without it they " +
			"would be ordered by whichever of aAbsent/bAbsent the ternary checks first, which is not a decision, " +
			"it's an accident of comparator order")
	}
}

// TestQueueColumnRendersTheServersOwnPositionAndNeverDerivesOne is TOR-139's
// TestQueueColumnDerivesFromReportedFieldsNotAnInventedPriority, INVERTED on
// purpose, because TOR-140 reversed the fact it guarded.
//
// The old test forbade reading row.priority or ev.priority and required a
// queueRank() that ranked queued rows by their own reported queued time. That
// was right while it stood: GET /runs reported no priority, the queue was
// strict FIFO, and a page reading a field the server did not send would have
// been inventing one. TOR-140 makes the position a real, reorderable
// server-side fact, so the field now exists and deriving one locally is the
// defect - the derivation would draw one order while the server dispatched
// another, with nothing on screen to say which was real.
//
// The assertion is therefore rewritten rather than deleted: same subject
// (there is exactly ONE notion of queue position, and the page renders it
// rather than deciding it), opposite direction.
func TestQueueColumnRendersTheServersOwnPositionAndNeverDerivesOne(t *testing.T) {
	js := appJS(t)

	// The derived version has to be GONE, not merely unused: two notions of
	// queue position is exactly what a reorder makes disagree, which is the
	// whole reason this ticket had to retire one of them. A mention inside a
	// comment explaining the retirement is not a second notion, so this looks
	// for the call/definition shape rather than the bare word.
	for _, gone := range []string{"function queueRank(", "queueRank(entry)", "refreshQueuePositions("} {
		if strings.Contains(js, gone) {
			t.Errorf("app.js still contains %q - TOR-139's client-side queue ranking must be retired now that "+
				"the server reports a reorderable position, not kept alongside it", gone)
		}
	}

	// And the server's own two fields have to be the ones that are read -
	// from the listing at page load, and from run_state for every row a
	// reorder, a cancel or a start moved afterwards.
	for _, want := range []string{"row.queue_position", "ev.queue_position", "row.priority", "ev.priority"} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js never reads %q - the queue column would not be showing what the server actually holds", want)
		}
	}

	// queuePosition() is what answers both the cell's text and the "priority"
	// sort key, and its absent case must be null rather than 0: the queue is
	// 1-based, so zero means "not waiting at all" and sorting it as a real
	// value would file a finished run among the waiting ones (see
	// compareEntries' own absence handling).
	if !strings.Contains(js, "return entry.queuePosition > 0 ? entry.queuePosition : null;") {
		t.Error("app.js's queuePosition() does not answer null for a row with no position - a 0 would sort and " +
			"render as a real place in a 1-based queue")
	}
	if !strings.Contains(js, `case "priority": return queuePosition(entry);`) {
		t.Error(`app.js's sortValue() does not answer the "priority" column from queuePosition() - the Queue ` +
			`header would sort by something other than the position it displays`)
	}
}

// TestTheQueueCanBeReorderedFromTheRow is TOR-140's acceptance criterion as
// far as served text can carry it: a waiting row has to offer a way to change
// its place without cancelling it, and that way has to be the absolute-level
// POST the server exposes.
//
// There is no JS runner here (see this file's own opening note), so what this
// proves is that the wiring exists in the shipped source - the buttons, their
// handlers, and the request they make. What it cannot prove is that a click
// reaches them; that was checked in a browser instead, and is recorded at the
// bottom of this file.
func TestTheQueueCanBeReorderedFromTheRow(t *testing.T) {
	js := appJS(t)

	for _, want := range []string{
		`raise.className = "run-priority run-priority-up";`,
		`lower.className = "run-priority run-priority-down";`,
		"setPriority(entry, entry.priority + 1)",
		"setPriority(entry, entry.priority - 1)",
		`await post("runs/priority", { id: entry.id, priority: want });`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js does not contain %q - a waiting row would have no way to change its own place in "+
				"the queue, which is the whole of this ticket", want)
		}
	}

	// The level sent must be ABSOLUTE, clamped to the band, never a step: two
	// clicks against a stale row would otherwise walk a torrent somewhere
	// nobody asked for, and a retried request would apply the step twice.
	if !strings.Contains(js, "const want = Math.max(PRIORITY_LOW, Math.min(PRIORITY_HIGH, priority));") {
		t.Error("app.js's setPriority() does not clamp to an absolute level in the band - it may be sending a " +
			"step, which is not idempotent against a stale view")
	}

	// The reorder buttons must not also toggle the row open, the same way
	// Cancel must not: a person moving three torrents around would otherwise
	// leave three details expanded behind them.
	block := regexp.MustCompile(`(?s)raise\.addEventListener\("click".*?\}\);`).FindString(js)
	if !strings.Contains(block, "event.stopPropagation();") {
		t.Errorf("the raise button's click handler does not stop propagation, so reordering a row would also "+
			"expand or collapse it: %q", block)
	}
}

// WHAT THESE TESTS DO NOT COVER, in one place - the summary the top of this
// file promises, written when TOR-140 added the first tests here that guard a
// CONTROL rather than a rendering rule.
//
// Every test here reads app.js as served text. That catches a rule deleted, a
// field renamed, an absent reading falling back to 0, a derivation left in
// place beside the fact that replaced it - real regressions, all of them, and
// all of them cheap. What it cannot catch is anything that only exists when
// the file RUNS:
//
//   - that buildLiveColumnHeaders() actually inserts its six <th>s before the
//     actions header, in the order LIVE_COLUMNS lists them;
//   - that clicking ▲ on a waiting row reaches setPriority at all, that the
//     row does not also expand, and that the button is disabled at the top of
//     the band rather than merely told to be;
//   - that a run_state arriving for a row NOBODY clicked repaints that row's
//     position (server.go's queueRecordsLocked sends one and has its own Go
//     test, but that the page redraws on it is not checked here);
//   - anything about layout: whether "high" fits the Queue cell on one line,
//     whether the wider actions column pushes the table past the pane.
//
// TOR-140 checked all four by hand in a real browser rather than leaving them
// as known gaps, against the actual binary (four magnets queued behind one
// running torrent, -max-active-torrents at its default 1). What that pass
// showed, since a claim of "driven once in a browser" is worth no more than
// what it names:
//
//   - the header row came out NAME/ADDED/STATUS/PEERS/SEEDS/DOWN/UP/AVAIL/
//     QUEUE plus the unlabelled actions column, in LIVE_COLUMNS' order;
//   - one ▲ click moved a row from position 3 to 1, marked it "high", disabled
//     its own ▲, repainted the two rows it passed to 2 and 3, and did not
//     expand the row it was clicked in; one ▼ on the front row sent it to the
//     back marked "low" with its ▼ then disabled;
//   - a second tab, never clicked in, followed the reorder - including the row
//     nobody had touched changing position - and a full page reload (the
//     GET /runs path rather than the socket) rendered the same order, so the
//     two paths agree;
//   - the running torrent's metadata timed out mid-pass, freeing the slot, and
//     the run that started was the PROMOTED one rather than the row that had
//     been at position 1 - the acceptance criterion end to end, in the real
//     process rather than against a fake runner;
//   - "high" and "low" each fit on one line under the position and left the
//     row height unchanged, and the table still fit the pane.
//
// The console carried no errors or exceptions throughout.
