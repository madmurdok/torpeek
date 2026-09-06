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
	// The Queue column must sort by the figure it DISPLAYS. That subject is
	// unchanged since TOR-140; which figure it is changed in TOR-156, when
	// the cell's own number became the arrival ordinal (queuePosition moved
	// to the labelled second line under it, and queuePosition() itself is
	// still checked just above, because that line still needs it). A header
	// left sorting by the position would now reorder the table by a number
	// most rows do not have while printing one they all do.
	if !strings.Contains(js, `case "priority": return arrivalOrdinal(entry);`) {
		t.Error(`app.js's sortValue() does not answer the "priority" column from arrivalOrdinal() - the Queue ` +
			`header would sort by something other than the figure it displays`)
	}
	// And the ordinal's own absent case, the same null-not-zero shape
	// queuePosition() is held to above: only a row read off disk has none,
	// and a 0 would sort it as though it had been added before everything.
	if !strings.Contains(js, "return entry.arrival > 0 ? entry.arrival : null;") {
		t.Error("app.js's arrivalOrdinal() does not answer null for a row with no ordinal - a 0 would sort " +
			"and render as a real place in a 1-based count")
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

// TestExactlyOneThPerColumnIsEverBuilt guards TOR-157's own precondition
// before its own tests get to it: the detail row's colspan (RUN_TABLE_COLUMNS,
// read off "#run-table thead th" at load) is only ever correct if nothing
// past buildLiveColumnHeaders() creates another <th> - a resize handle that
// turned out to be a header cell of its own, say, rather than a plain <span>
// living inside one, would inflate the count RUN_TABLE_COLUMNS reads without
// TestLiveColumnsAreWiredIntoBothHeadersAndSorting or anything else here
// noticing, since both still agree on nine sortable columns either way.
func TestExactlyOneThPerColumnIsEverBuilt(t *testing.T) {
	js := appJS(t)

	if n := strings.Count(js, `document.createElement("th")`); n != 1 {
		t.Errorf(`app.js calls document.createElement("th") %d times, want exactly 1 (inside `+
			"buildLiveColumnHeaders) - a second call would add a column the detail row's colspan "+
			"was never told about", n)
	}
}

// TestColumnWidthsKeyOffElSortHeaders is TOR-157's own version of the
// LIVE_COLUMNS "one list rather than two" guard: el.sortHeaders (already
// exactly the nine resizable headers - the actions column carries no
// data-sort) is what decides which columns get a width to persist. A second,
// hand-written list of column keys here could drift from LIVE_COLUMNS the
// header set actually came from, the same way a hard-coded colspan drifted
// from the header count before RUN_TABLE_COLUMNS existed.
func TestColumnWidthsKeyOffElSortHeaders(t *testing.T) {
	js := appJS(t)

	if !strings.Contains(js, `Array.from(el.sortHeaders, (th) => th.dataset.sort)`) {
		t.Error("app.js's resizableColumnKeys() does not derive its column list from el.sortHeaders - a " +
			"hand-written list here could silently drift from the headers buildLiveColumnHeaders() actually built")
	}
	if !strings.Contains(js, `th.style.width = "var(--col-w-" + key + ")";`) {
		t.Error(`app.js does not set each sortable header's own width from its --col-w-* token - without this ` +
			`table-layout: fixed would have nothing but the CSS default to size that column with, and a stored or ` +
			`dragged width would never reach the page`)
	}
}

// TestStoredColumnWidthsDegradeGracefully is this ticket's own acceptance
// criterion: a stored value must fall back to the default widths when it is
// empty, unreadable, or - the one this ticket calls out by name - naming a
// column that no longer exists (an older column set than the page currently
// renders). Read as served text because there is no JS runner here (see this
// file's opening note); what a real browser did with an actually-corrupt
// value is recorded in the summary at the bottom of this file instead.
func TestStoredColumnWidthsDegradeGracefully(t *testing.T) {
	js := appJS(t)

	m := regexp.MustCompile(`(?s)function loadColumnWidths\(\) \{.*?\n\}`).FindString(js)
	if m == "" {
		t.Fatal("app.js has no loadColumnWidths() function to check")
	}

	for _, want := range []string{
		// Unreadable localStorage (private window, site data blocked) and a
		// missing key both fall back to the same empty map.
		"try {",
		`if (!raw) return {};`,
		// Malformed JSON throws inside the try, caught below.
		"} catch (err) {",
		"return {};",
		// Not an object at all (a bare number or string JSON-decodes fine
		// but isn't a map of columns).
		`if (!parsed || typeof parsed !== "object") return {};`,
		// THE ticket's own named case: a key from an older column set.
		"const known = new Set(resizableColumnKeys());",
		"if (!known.has(key)) continue;",
		// A width that doesn't parse as a finite number (corrupted, or not
		// a number at all) is dropped rather than applied as NaN.
		"if (Number.isFinite(width)) widths[key] = clampColumnWidth(width);",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("app.js's loadColumnWidths() does not contain %q - a stored value could render a broken "+
				"table instead of degrading to the default widths: %q", want, m)
		}
	}

	// The read itself, and the whole function, must be inside the try - not
	// just the JSON.parse - since localStorage.getItem itself is what throws
	// in a private window.
	if !strings.Contains(m, "localStorage.getItem(COLUMN_WIDTHS_KEY)") {
		t.Error("app.js's loadColumnWidths() does not read COLUMN_WIDTHS_KEY from localStorage at all")
	}

	// savePanelWidth's own try/catch pattern, reused for the column map:
	// a write that throws (private window, quota) must not crash the drag
	// it was trying to persist.
	if !strings.Contains(js, "function saveColumnWidths(widths) {") ||
		!strings.Contains(js, "localStorage.setItem(COLUMN_WIDTHS_KEY, JSON.stringify(widths));") {
		t.Error("app.js's saveColumnWidths() does not write the whole widths map back to COLUMN_WIDTHS_KEY as JSON")
	}
}

// TestColumnDragNeverTriggersSort is the trap this ticket names directly:
// every header is a sort control (TOR-139), so the resize handle app.js
// appends inside each one sits inside a click target that reorders the
// table. TOR-140's raise/lower buttons hit the identical problem for the
// accordion and fixed it with stopPropagation; this checks the same fix
// landed on every one of the handle's own events, not just the drag start -
// a plain click (pointerdown+pointerup with no movement) still bubbles a
// separate click event that pointerdown's own stopPropagation does not
// touch.
func TestColumnDragNeverTriggersSort(t *testing.T) {
	js := appJS(t)

	// el.sortHeaders is walked twice in app.js - once to wire up sorting
	// (click/keydown, near updateSortIndicators) and once, here, to build
	// the resize handles - so the match is anchored on `const key =
	// th.dataset.sort;`, unique to this second loop, rather than on the
	// `for (const th of el.sortHeaders)` line the two share.
	block := regexp.MustCompile(`(?s)for \(const th of el\.sortHeaders\) \{\n  const key = th\.dataset\.sort;.*?\n\}`).
		FindString(js)
	if block == "" {
		t.Fatal("app.js has no `for (const th of el.sortHeaders) { ... }` block wiring up the resize handles")
	}

	if !strings.Contains(block, `handle.className = "col-resizer";`) {
		t.Fatal("app.js's el.sortHeaders loop does not create a .col-resizer handle - nothing below would be " +
			"testing what this test thinks it is")
	}

	for _, want := range []string{
		// pointerdown: stops the drag itself from reaching the header.
		`handle.addEventListener("pointerdown", (event) => {`,
		// pointermove: stopped too, since a pointer captured on the handle
		// still dispatches move events the header never asked for.
		`handle.addEventListener("pointermove", (event) => {`,
		// A plain click - the case pointerdown's own stopPropagation cannot
		// reach, since click is a separate event fired after pointerup.
		`handle.addEventListener("click", (event) => event.stopPropagation());`,
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the resize-handle wiring does not contain %q", want)
		}
	}

	// One call each in pointerdown, pointermove and endColumnDrag (shared by
	// both pointerup and pointercancel) - the click listener's own call is
	// already checked above by its exact line instead, since as a one-line
	// arrow body ("(event) => event.stopPropagation())") it reads
	// "stopPropagation());", not "stopPropagation();", and would not count
	// here.
	if n := strings.Count(block, "event.stopPropagation();"); n < 3 {
		t.Errorf("the resize-handle wiring calls event.stopPropagation() only %d times across pointerdown, "+
			"pointermove and endColumnDrag - expected at least 3, so a drag cannot also reorder the table", n)
	}
}

// TestColumnDragEndsWhenPointerCaptureIsLost is TOR-165: a drag whose
// pointerup/pointercancel never reaches the handle - because the capture was
// lost some other way, e.g. the handle leaving the document (per spec, the
// baseline trigger for lostpointercapture) - must not leave the drag "stuck"
// active forever. Before this ticket the handle's "dragging" class was the
// only state a drag left behind, and nothing here ever cleared it back off.
// buildLiveColumnHeaders() only builds this table's header once today, so a
// header rebuild is not a live way to hit this in the current app - the
// browser repro below instead reproduced a real capture loss straight from
// this repo's own browser-automation tooling, no header rebuild involved.
// Reuses TestColumnDragNeverTriggersSort's own block extraction, since both
// tests are about the same wiring.
//
// Text assertions cannot see a pointer actually move or a capture actually
// get lost - that is the browser pass recorded right below, not this test.
func TestColumnDragEndsWhenPointerCaptureIsLost(t *testing.T) {
	js := appJS(t)

	block := regexp.MustCompile(`(?s)for \(const th of el\.sortHeaders\) \{\n  const key = th\.dataset\.sort;.*?\n\}`).
		FindString(js)
	if block == "" {
		t.Fatal("app.js has no `for (const th of el.sortHeaders) { ... }` block wiring up the resize handles")
	}

	// Half one: lostpointercapture is the event that fires when the capture
	// set in pointerdown ends WITHOUT a pointerup/pointercancel reaching the
	// handle. Nothing listened for it before this fix.
	if !strings.Contains(block, `handle.addEventListener("lostpointercapture", endColumnDrag);`) {
		t.Error(`app.js's resize-handle wiring does not end the drag on "lostpointercapture" - a capture lost ` +
			"any way other than pointerup/pointercancel on the handle itself would leave the handle lit and " +
			"dragging forever")
	}

	// Half two, and the one that matters more per the ticket: pointermove
	// must require the primary button still be held (event.buttons bit 0)
	// and end the drag itself when it is not - so a plain hover with no
	// button down is never computed as a continued drag, which is what
	// makes ANY future way of losing the pointer harmless instead of sticky,
	// not just the lostpointercapture case above.
	if !strings.Contains(block, "if (!(event.buttons & 1)) {") {
		t.Error("app.js's pointermove handler does not check event.buttons for the primary button - a hover " +
			"with no button held would be computed as though it were a continued drag")
	}
	if !strings.Contains(block, "endColumnDrag(event);\n      return;") {
		t.Error("app.js's pointermove handler does not end the drag when event.buttons shows no button held - " +
			"it would keep reading a stale start point instead of stopping")
	}
}

// TOR-165's browser pass (its own acceptance criterion: "Verified in a
// browser by interrupting a drag, not only by asserting the served text"),
// against a real binary built from this branch, serving on 127.0.0.1:8811:
//
//   - reading the wiring first: buildLiveColumnHeaders() builds the header
//     row exactly once, at page load, and nothing else in app.js ever
//     touches a .col-resizer node afterward - so "this table re-renders its
//     header row on events," the mechanism the ticket named, is not a live
//     path in the CURRENT code. The rest of the ticket's read was accurate:
//     colDragStartX/colDragStartWidth were the sole state (this task turned
//     them into a null-until-dragging `drag` object instead, per-handle by
//     closure either way), pointermove trusted the "dragging" class alone,
//     and nothing listened for lostpointercapture.
//   - a real capture loss was still found without forcing one: on the
//     UNFIXED binary, a single plain left-click on the NAME/ADDED resize
//     handle (browser automation via CDP, no drag distance at all) left the
//     handle's classList as "col-resizer dragging" - pointerup's own event
//     target had already become the <th> rather than the handle, meaning
//     capture was already gone before pointerup fired, exactly the gap the
//     ticket describes, from an ordinary click rather than any header
//     rebuild. A subsequent hover (mousemove, event.buttons: 0, no click)
//     at a point 27px to the right then widened --col-w-name from
//     356.734375px to 383.734375px - the column visibly grew under a
//     hover with no button held, matching "afterwards merely moving the
//     cursor over it dragged the column further right" from the ticket
//     verbatim, confirmed by screenshot (the divider sat lit and the NAME
//     column had visibly widened).
//   - the exact same sequence (fresh page load, one plain click on the
//     handle, then a hover 27px right of it) against the FIXED binary left
//     handle.className as plain "col-resizer" after the click - the same
//     capture loss happened, but lostpointercapture (or the event.buttons
//     guard - either half independently ends it) cleared it - and the
//     following hover left --col-w-name unchanged at 20rem, confirmed by
//     screenshot (divider unlit, NAME column back at its original width).
//
// No console errors during the pass. Server started and stopped cleanly on
// port 8811; `uptime`'s load average at the time (13-25, a shared, heavily
// loaded machine) is why timings are not reported - nothing here depended on
// wall-clock speed.

// TestColumnWidthTokensMatchThePanelWidthFamily is the ticket's own
// instruction, checked directly: "a column-width equivalent belongs in the
// same family" as --panel-width. Same mechanism as TestStoredColumnWidthsDegradeGracefully's
// sibling tests above, but on the stylesheet side - the CSS half of what
// makes a dragged width actually move a border on screen.
func TestColumnWidthTokensMatchThePanelWidthFamily(t *testing.T) {
	css := stylesheet(t)

	for _, key := range []string{
		"name", "when", "status", "peers", "seeds", "download_bps", "upload_bps", "availability", "priority",
	} {
		token := "--col-w-" + key + ":"
		if !strings.Contains(css, token) {
			t.Errorf("app.css declares no %q token in :root - app.js's applyColumnWidth(%q, ...) would be "+
				"setting a custom property nothing in the stylesheet ever reads a default from", token, key)
		}
	}

	// table-layout: fixed is what makes a header's own width authoritative
	// for the whole column regardless of a row's content - without it, a
	// dragged column could be overridden right back open by a long name or
	// status line, the same shrink problem .run-cell-name's old max-width: 0
	// trick existed to solve for exactly one column.
	if !regexp.MustCompile(`\.run-table\s*\{[^}]*table-layout:\s*fixed`).MatchString(css) {
		t.Error("app.css's .run-table rule does not set table-layout: fixed - a column's width would still be " +
			"whatever its content wants regardless of what app.js sets --col-w-* to")
	}

	// The actions column has no data-sort and so no --col-w-* token of its
	// own (see resizableColumnKeys()) - table-layout: fixed still needs an
	// explicit width somewhere on its header, or that column (and the ones
	// with an explicit width) would fight over the fixed grid's leftover
	// space in a way nothing here chose on purpose.
	if !regexp.MustCompile(`\.run-table\s+thead\s+th\.run-actions-header\s*\{[^}]*width:\s*3\.8rem`).MatchString(css) {
		t.Error("app.css's .run-actions-header rule does not set an explicit width - table-layout: fixed reads " +
			"column widths off the header row alone, and this header has no --col-w-* token to fall back to")
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

// TestFileDoneNoLongerLinksManifest is TOR-171: the page has no use for the
// raw JSON manifest a finished file's event carries, so onFileDone must not
// turn ev.manifest_url into a link the way it still does for ev.sheet_url -
// unrelated to this file's own ticket, but placed here because app.js's
// served-text tests all live in this one file. manifest_url itself stays on
// the wire (server.go's record() is unchanged) for whatever else reads the
// NDJSON stream - this test is only about what the page renders from it.
func TestFileDoneNoLongerLinksManifest(t *testing.T) {
	js := appJS(t)

	if strings.Contains(js, `link(ev.manifest_url`) {
		t.Error("app.js's onFileDone still turns ev.manifest_url into a link - the page should not offer a " +
			"manifest link at all, live or reopened from disk")
	}

	// The contact sheet link is the one an end user does want, and must
	// survive this change untouched.
	if !strings.Contains(js, `link(ev.sheet_url, "contact sheet")`) {
		t.Error("app.js's onFileDone no longer links ev.sheet_url as \"contact sheet\" - that link should stay")
	}
}

// TOR-171's browser check, same session and binary as TOR-165's pass above:
// no completed file was available to inspect through the real event path
// (the one seeded run in this environment was FAILED, with no frames), so
// the single-link-row rendering ("whatever separates two links must not
// leave a dangling separator" once the manifest link is gone, since a
// finished file usually offers only the contact sheet now) was checked
// directly against app.css's actual rule for it - .file-links is `display:
// flex; gap: 1rem` - by rendering one <a> inside a .file-links element on
// the live page and reading its layout back: one child, and a zoomed
// screenshot showing only "contact sheet" with nothing beside it. flex gap
// only inserts space BETWEEN children, so a single child leaves nothing to
// dangle by construction - confirmed rather than assumed.
//
// What this did not verify: manifest_url actually still arriving over a
// live NDJSON stream end to end (server.go's record() is unchanged, so this
// is read off the source, not observed on the wire, in this pass).
