package web

import (
	"regexp"
	"strings"
	"testing"
)

// TOR-194 turned the run table into a custom element, and these are the guards
// that did not exist before it - the same finding TOR-193 recorded for the
// compare dialog, one level up: the mechanisms this file checks were spread
// through app.js with nothing watching most of them, so a move could have lost
// any of them silently.
//
// WHAT IS ALREADY GUARDED ELSEWHERE and deliberately not repeated here: the
// six columns' wiring into sorting (columns_test.go), the stored widths'
// graceful fallback and the drag that must not sort or stick
// (columns_test.go), the queue cell's two facts (listing_test.go), the detail
// width's three causes (rundetailwidth_test.go), the accordion's refusal to
// touch a file inside it (perfiledetail_test.go). All five were retargeted
// onto run-table.js by this ticket rather than rewritten - the subjects did
// not change, only the file they live in.
//
// WHAT THIS FILE ADDS is the element itself (is it registered, does it wrap
// the page's own markup, does it fail loudly on a missing part, does it refuse
// an incomplete service set), the boundary the ticket asked to be stated (the
// table owns the row, the page owns what the row opens onto), the ticker that
// must not outlive it, and - the one the acceptance criteria name outright -
// ABSENT IS NOT ZERO for every live figure, checked on the ROW rendering
// rather than only on the derivation behind it.
//
// Text guards, like the rest of this package's front-end tests (see
// columns_test.go's opening note): they catch a mechanism deleted, renamed or
// regated, and they cannot see a pointer move or a column resize. The browser
// pass TOR-194 asks for is recorded in the ticket's own report.
//
// EVERY ONE OF THEM READS THE MODULE WITH ITS COMMENTS STRIPPED, and that is
// not tidiness - it is the defect the falsification run for this ticket found
// twice. `// customElements.define("run-table", RunTable);` still CONTAINS the
// string a Contains check is looking for, so commenting out the registration
// left the guard green while the element was not registered at all; the same
// held for clearInterval. A guard that a `//` in front of the line satisfies
// is weaker than its own error message, which is exactly what TOR-193
// recorded one ticket earlier for a different reason.

// liveJS is one of the shipped modules with its comments removed - the same
// two passes TOR-193's own guard uses (block comments, then line comments).
// Line-comment stripping is safe on these files because none of them contains
// a "//" inside a string literal; a URL or a protocol-relative path appearing
// in one later would need this to become smarter rather than to be dropped.
func liveJS(t *testing.T, js string) string {
	t.Helper()
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(js, "")
	return regexp.MustCompile(`(?m)//[^\n]*`).ReplaceAllString(live, "")
}

// TestAbsentLiveFiguresRenderAsAbsentInTheRow is the criterion written
// straight into the ticket, and it is about the RENDERING rather than the
// derivation: state.js's helpers already answer "a dash, not a zero" and
// TestAbsentLiveFiguresAreNullNeverZero holds them to it - but a row that
// wrote entry.live.peers into the cell itself would print 0 for a queued
// torrent whatever those helpers say, and internal/web/listing.go's tally
// (eight applications as of TOR-180) exists because "0 peers, 0 KB/s" on a
// queued row is indistinguishable from a running torrent that found nobody.
//
// TWO THINGS PER CELL, because either alone leaves the bug reachable: the TEXT
// has to come from the helper that knows how to say "absent", and the cell's
// data-absent (which is what dims it - table.css) has to be decided by a real
// absence test rather than by the figure being falsy. A cell showing a dash
// but not dimmed, or dimmed while showing a number, is the same complaint
// TOR-156 was written from.
func TestAbsentLiveFiguresRenderAsAbsentInTheRow(t *testing.T) {
	row := jsMethod(t, liveJS(t, runTableJS(t)), "syncRow")

	// The exact pair of lines per column. Written out in full rather than
	// checked for the helper's name alone: `entry.rowPeers.textContent =
	// hasLive(entry) ? entry.live.peers : 0;` contains "hasLive(entry)" too,
	// and that is precisely the regression.
	for _, want := range []string{
		"entry.rowPeers.textContent = peersCellText(entry);",
		"entry.rowPeers.dataset.absent = String(!hasLive(entry));",
		"entry.rowSeeds.textContent = seedsCellText(entry);",
		"entry.rowSeeds.dataset.absent = String(!hasLive(entry));",
		// The two rates carry a second absence of their own: a run's first
		// heartbeat has peers and seeds but no rate yet, so hasLive alone is
		// not enough and the reading is checked for null on its own.
		"const downBps = hasLive(entry) ? entry.live.download_bps : null;",
		"entry.rowDown.textContent = rateCellText(downBps);",
		"entry.rowDown.dataset.absent = String(downBps == null);",
		"const upBps = hasLive(entry) ? entry.live.upload_bps : null;",
		"entry.rowUp.textContent = rateCellText(upBps);",
		"entry.rowUp.dataset.absent = String(upBps == null);",
		// Availability can be absent while entry.live is present - a torrent
		// nobody has asked for bytes yet - so its own reading decides.
		"entry.rowAvail.textContent = availabilityCellText(entry);",
		"entry.rowAvailCell.dataset.absent = String(!availabilityReading(entry));",
		// And the queue column's figure is the arrival ordinal (TOR-156), so
		// that is what its dimming follows. Also checked in listing_test.go,
		// from the other side: there, that the figure and the dimming agree.
		"entry.rowQueue.textContent = queueCellText(entry);",
		"entry.rowQueueCell.dataset.absent = String(arrivalOrdinal(entry) === null);",
	} {
		if !strings.Contains(row, want) {
			t.Errorf("run-table.js's syncRow does not contain %q - a live figure would be rendered or "+
				"dimmed by something other than whether it is actually absent, and a queued row showing "+
				"0 peers at 0 KB/s reads exactly like a running one that found nobody", want)
		}
	}

	// AND NOTHING IN THE ROW MAY WRITE A READING STRAIGHT INTO A CELL. The
	// list above says what each cell must do; this says there is no tenth
	// line doing it the other way, for a column added later by somebody who
	// never read either.
	direct := regexp.MustCompile(`textContent = [^;\n]*entry\.live\.`).FindAllString(row, -1)
	if len(direct) > 0 {
		t.Errorf("syncRow writes a live reading directly into a cell's text: %q. Every one of them goes "+
			"through state.js's own cell helper, which is the single place that knows a missing reading "+
			"renders as a dash rather than as a zero", direct)
	}

	// The dimming has to be a String() of a BOOLEAN test, never of the figure:
	// String(entry.live.peers) would put the number in data-absent, and
	// table.css's [data-absent="true"] would then never match at all - the
	// cell would print a dash and read at full strength.
	absent := regexp.MustCompile(`dataset\.absent = ([^;\n]+);`).FindAllStringSubmatch(row, -1)
	if len(absent) != 6 {
		t.Fatalf("syncRow sets data-absent on %d cells, want 6 (peers, seeds, both rates, availability, "+
			"queue) - the scan is broken or a column stopped saying whether it has anything to say",
			len(absent))
	}
	for _, m := range absent {
		if !strings.HasPrefix(m[1], "String(!") && !strings.Contains(m[1], "== null") &&
			!strings.Contains(m[1], "=== null") {
			t.Errorf("data-absent is set from %q, which is not an absence test - it has to be a negation "+
				"or an explicit null comparison, or a real measured zero would dim its own cell", m[1])
		}
	}
}

// TestTheRunTableIsAnElementWrappingThePagesOwnMarkup is the pattern itself,
// checked rather than trusted: registered natively, wrapping the markup
// index.html still holds, finding its parts inside itself, and reached from
// app.js by tag. frame-panel.js's own guard (stylesheet_test.go) does the same
// for the panel; this is the table's.
func TestTheRunTableIsAnElementWrappingThePagesOwnMarkup(t *testing.T) {
	js := liveJS(t, runTableJS(t))
	page := liveJS(t, appJS(t))

	if !strings.Contains(js, `customElements.define("run-table", RunTable);`) {
		t.Error(`run-table.js never calls customElements.define("run-table", ...) - the <run-table> in ` +
			"index.html would stay an unknown element, the six live columns would never be built, and " +
			"the table would render three columns wide with no sorting and no rows")
	}

	// LIGHT DOM. A shadow root would cut table.css off from the markup below
	// it (frame-panel.js's header carries the full reasoning), and for a
	// <table> it would also mean a stylesheet in a string - the one thing
	// TOR-190's split exists to prevent.
	if strings.Contains(js, "attachShadow") {
		t.Error("run-table.js attaches a shadow root - table.css's rules would not reach the table " +
			"inside it, and getting them back means either a second copy of the stylesheet in a " +
			"template literal or a fetch at load time in a project with no build step")
	}

	// THE MARKUP STAYS IN THE PAGE, wrapped. The element finds its parts with
	// this.querySelector, so markup outside the wrapper is markup it cannot
	// see - and each of these six is dereferenced by a method.
	html, err := embedded.ReadFile("assets/index.html")
	if err != nil {
		t.Fatalf("reading the embedded index.html: %v", err)
	}
	markup := string(html)
	if !strings.Contains(markup, "<run-table>") || !strings.Contains(markup, "</run-table>") {
		t.Fatal("index.html has no <run-table> wrapper - customElements.define would upgrade nothing, " +
			"and every part lookup below would be looking inside an element that is not there")
	}
	open := strings.Index(markup, "<run-table>")
	shut := strings.Index(markup, "</run-table>")
	if shut < open {
		t.Fatal("index.html closes </run-table> before it opens - the wrapper is inside out")
	}
	wrapped := markup[open:shut]
	for _, part := range []string{
		`<table id="run-table"`,
		`<tbody id="run-list">`,
		`<p id="run-list-empty"`,
		`<div class="run-table-wrap">`,
		`th scope="col" class="run-actions-header"`,
		`data-sort="name"`,
	} {
		if !strings.Contains(wrapped, part) {
			t.Errorf("index.html's <run-table> does not contain %q - run-table.js finds every part with "+
				"this.querySelector, so markup outside the wrapper is invisible to it and "+
				"connectedCallback throws on the missing part", part)
		}
	}
	// #run-list-empty is the reason the wrapper goes round the whole section
	// rather than round the <table>: it is one of the element's parts and it
	// sits outside .run-table-wrap.
	wrapIdx := strings.Index(wrapped, `<div class="run-table-wrap">`)
	emptyIdx := strings.Index(wrapped, `<p id="run-list-empty"`)
	if emptyIdx < wrapIdx {
		t.Error("index.html puts #run-list-empty before .run-table-wrap - not wrong in itself, but this " +
			"test's own reason for the wrapper being outside the section is that the empty note sits " +
			"after the scroll wrap; check the wrapper still contains both")
	}

	// A MISSING PART IS A WIRING ERROR. buildLiveColumnHeaders used to return
	// quietly on a missing header row, which meant a page that had lost its
	// thead rendered three columns and said nothing at all.
	connected := jsMethod(t, js, "connectedCallback")
	for _, part := range []string{"table", "headRow", "actionsHeader", "list", "emptyNote", "wrap"} {
		if !strings.Contains(connected, part+": this."+part+",") {
			t.Errorf("connectedCallback does not check %q for absence - every method below dereferences "+
				"it, so a missing part has to name itself here rather than surface as \"cannot read "+
				"property of null\" from whichever handler fires first", part)
		}
	}
	if !strings.Contains(connected, `throw new Error("run-table: no " + name + " inside the element");`) {
		t.Error("connectedCallback does not throw on a missing part - the loop above would be collecting " +
			"names and doing nothing with them")
	}

	// AND THE PAGE REACHES IT BY TAG, not by id: the element IS the thing
	// being asked for, the same way el.framePanel and el.compareDialog are.
	if !strings.Contains(page, `runTable: document.querySelector("run-table"),`) {
		t.Error(`app.js does not look up the table as document.querySelector("run-table") - the four ` +
			"lookups it replaced (the tbody, the empty note, the nine headers and the scroll wrap) " +
			"are the element's own parts now")
	}
	for _, gone := range []string{
		`runList: document.getElementById("run-list")`,
		`runListEmpty: document.getElementById("run-list-empty")`,
		`sortHeaders: document.querySelectorAll`,
		`runTableWrap: document.querySelector(".run-table-wrap")`,
	} {
		if strings.Contains(page, gone) {
			t.Errorf("app.js still holds %q - a second handle on one of the table's own parts is how the "+
				"two halves come to write the same element from two files", gone)
		}
	}

	// The wrapper must not change the layout. An unknown element is inline by
	// default, which would put a block <section> inside an inline box.
	if !regexp.MustCompile(`(?m)^run-table \{[^}]*display:\s*(block|contents)`).MatchString(stylesheet(t)) {
		t.Error("no `run-table { display: block }` (or contents) rule in the served stylesheet - a custom " +
			"element is display: inline until told otherwise, so the wrapper would put the runs section " +
			"inside an inline box")
	}
}

// TestTheRunTableRefusesAnIncompleteServiceSet is compare-dialog.js's own
// decision applied here (TOR-193's setServices), and the reason is the same:
// four of the row's gestures reach code only app.js can hold - three POSTs and
// the redraw of the detail a row has just opened - and a page that forgot to
// wire one should fail while it is being written, not at the first click on a
// row nobody is watching the console for.
func TestTheRunTableRefusesAnIncompleteServiceSet(t *testing.T) {
	js := liveJS(t, runTableJS(t))

	if !strings.Contains(js, `const SERVICES = ["toggleRun", "cancelRun", "setPriority", "detailShown"];`) {
		t.Error("run-table.js does not declare its four services in one list - the check below can only " +
			"be as complete as the list it walks")
	}
	if !strings.Contains(js, `throw new Error("run-table: setServices needs a " + name + "() function");`) {
		t.Error("setServices does not throw on a missing service - the first press of ▲, ✕ or a row " +
			"itself would call undefined, mid-run, with nothing said at wiring time")
	}
	// The whole point of the list is that the check walks it rather than
	// naming each service again.
	if !strings.Contains(js, "for (const name of SERVICES) {") {
		t.Error("setServices does not walk SERVICES - a service added to the list would then not be " +
			"checked, which is the drift the one-list shape exists to prevent")
	}

	// And the page hands over all four. A name that exists on one side only
	// is exactly what the throw above turns into a load-time failure - but
	// only if the call is there to make.
	// Read as the four NAMES rather than as the call verbatim, because
	// TOR-195 turned detailShown from an alias into a one-line call onto the
	// detail element: the contract the table declares is unchanged, and this
	// test is about the contract.
	page := liveJS(t, appJS(t))
	if !strings.Contains(page, "setRunTableServices({") {
		t.Fatal("app.js does not wire the table's services in its bootstrap - the element would " +
			"throw at the first row click instead")
	}
	for _, want := range []string{"toggleRun,", "cancelRun,", "setPriority,", "detailShown:"} {
		if !strings.Contains(page, want) {
			t.Errorf("app.js's setRunTableServices call does not hand over %q - the element would "+
				"throw at the first press of the control that needs it", want)
		}
	}
}

// TestTheTableOwnsTheRowAndTheDetailIsNotItsBusiness is the boundary this
// ticket was asked to state, as a test rather than as a paragraph - and it is
// the seam TOR-195 has to be able to trust: the table builds both <tr>s and
// hands back the colspanned cell, and what goes INTO that cell is nothing to
// do with it.
//
// Checked in both directions, because either one alone leaves the seam able to
// rot: the table must not reach into a detail, and the page must not write a
// row cell.
func TestTheTableOwnsTheRowAndTheDetailIsNotItsBusiness(t *testing.T) {
	js := liveJS(t, runTableJS(t))
	page := liveJS(t, appJS(t))

	// THE ROW IS THE TABLE'S, both of them. The detail's <tr> is a row, its
	// cell's colSpan is a fact about the header count, and hiding it is what
	// this level's accordion does.
	newRow := jsMethod(t, js, "newRow")
	for _, want := range []string{
		`detailRow.className = "run-detail-row";`,
		"detailRow.hidden = true;",
		`detailCell.className = "run-detail-cell";`,
		"detailCell.colSpan = this.columns;",
		"this.list.append(row, detailRow);",
		"detailCell,",
	} {
		if !strings.Contains(newRow, want) {
			t.Errorf("run-table.js's newRow does not contain %q - the detail's own row, its colspan and "+
				"the cell handed back are the table's half of TOR-194's boundary, and TOR-195 mounts "+
				"into exactly that cell", want)
		}
	}

	// AND THE DETAIL'S INSIDES ARE NOT. Every one of these is an entry field
	// app.js's own half declares; a mention of any of them here would mean the
	// table had started reading or writing a detail, which is the tangle
	// TOR-195 would then have to unpick.
	// SINCE TOR-195 THESE ARE FIELDS OF THREE OTHER ELEMENTS rather than of
	// the entry, which makes the ban stronger rather than weaker: the table
	// could not read one now even if it tried, and naming one here would mean
	// somebody had put it back on the shared record.
	for _, forbidden := range []string{
		"detailEl", "detailBadge", "detailTitle", "detailCancel", "detailError",
		"torrentSummary", "torrentActions", "torrentSave", "torrentSend", "torrentNote",
		"againEl", "againLine", "againGo", "againRetry", "againCost", "againNote",
		"pickerEl", "pickerList", "pickerAll", "pickerArmed", "pickerRows",
		"fileEntries", "fentry",
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("run-table.js names %q, which is a field of the DETAIL a row opens onto - the table "+
				"hands over a cell and learns nothing about what went in it (see the module's own "+
				"header, and TOR-195)", forbidden)
		}
	}
	// The same rule for the markup: the table builds no detail content, so it
	// has no innerHTML at all.
	if strings.Contains(js, "innerHTML") {
		t.Error("run-table.js assigns innerHTML - every element it builds is a row cell built with " +
			"createElement; a template string here would be detail markup on the wrong side of the line")
	}

	// AND THE PAGE WRITES EXACTLY ONE ROW ELEMENT, enumerated rather than
	// forbidden outright, because there genuinely is one: aria-controls names
	// the detail's own id, which only the detail can mint. Its pair,
	// aria-expanded, is the row's state and is set by the table.
	rowRefs := regexp.MustCompile(`(?:entry|rowParts)\.(row[A-Za-z]*|detailRowEl)\b`).FindAllString(page, -1)
	for _, ref := range rowRefs {
		if ref != "rowParts.rowToggle" {
			t.Errorf("app.js reaches for %q - since TOR-194 every row element is written by the table, "+
				"and the one exception is rowParts.rowToggle's aria-controls (the id is the detail's). "+
				"Two files writing one cell is how a redraw comes to disagree with itself", ref)
		}
	}
	if len(rowRefs) == 0 {
		t.Error("app.js reaches for no row element at all, so the scan above verified nothing - the " +
			"aria-controls line it is anchored on has either moved or been renamed")
	}
	// SINCE TOR-195 THE ID IS ASKED FOR rather than reached for: the detail is
	// an element, and regionId is the one thing about it the row's half of the
	// wiring needs. Same line, same one exception, one less field on the
	// shared record.
	if !strings.Contains(page, `rowParts.rowToggle.setAttribute("aria-controls", detail.regionId);`) {
		t.Error("app.js does not name the detail's region on the row's toggle - a disclosure control " +
			"that names nothing leaves the region it opens unannounced")
	}
	if !strings.Contains(js, `main.setAttribute("aria-expanded", "false");`) {
		t.Error("run-table.js's newRow does not start the toggle closed - aria-expanded is the row's own " +
			"state, and a row whose detail is hidden while its button says nothing is a control that " +
			"lies to anyone not looking at the screen")
	}
}

// TestTheStallTickerCannotOutliveTheTable is the one thing the move to an
// element made strictly better rather than merely tidier, so it is worth a
// standing check: TOR-141's "how long has this been stalled" line is redrawn
// once a second, and at module scope in app.js that interval ran for the life
// of the page whatever happened to the table. Inside the element it has a
// lifecycle - and an interval that survives its table walks state.runs and
// writes into cells nobody can see.
func TestTheStallTickerCannotOutliveTheTable(t *testing.T) {
	js := liveJS(t, runTableJS(t))

	if !strings.Contains(js, "this.stallTimer = setInterval(this.onStallTick, 1000);") {
		t.Error("run-table.js does not start the stall ticker in connectedCallback - TOR-141's duration " +
			"would only be redrawn when some other event happened to redraw the row, which is the " +
			"'reads as broken rather than as nothing to report' complaint that ticket answers")
	}
	dis := jsMethod(t, js, "disconnectedCallback")
	if !strings.Contains(dis, "clearInterval(this.stallTimer);") {
		t.Error("disconnectedCallback does not clear the stall ticker - it would keep firing every " +
			"second against a table that is no longer on the page, holding every entry alive with it")
	}
	// The handler is a bound field for the same reason the resize handler is
	// (rundetailwidth_test.go checks that one): an inline arrow could not be
	// taken off again.
	if !strings.Contains(js, "this.onStallTick = () => this.refreshStallDurations();") {
		t.Error("run-table.js does not keep the ticker's callback as an instance field - setInterval's " +
			"handle is what clearInterval needs, and a callback rebuilt at each use is a callback " +
			"nothing can match")
	}
	// And what it redraws is still the row's own meta line, from state.
	tick := jsMethod(t, js, "refreshStallDurations")
	for _, want := range []string{
		"for (const entry of state.runs.values()) {",
		"entry.rowMeta.textContent = metaLabel(entry, now);",
	} {
		if !strings.Contains(tick, want) {
			t.Errorf("refreshStallDurations does not contain %q - it recomputes one line from numbers "+
				"already on the entry, and never invents a reading a heartbeat has not reported", want)
		}
	}
}

// TestTheDragEndKeepsTheElementAsIts_this pins the shape of one function,
// which is not normally worth a test - except that this one was wrong in the
// shipped code and nothing could see it.
//
// endColumnDrag runs as a listener on the resize handle, so written as
// `function endColumnDrag(...)` its `this` is the <span>, and
// `this.saveColumnWidths(...)` throws TypeError on every drag that ends. The
// statements before it still run, so the drag LOOKS finished - the "dragging"
// class comes off and the column keeps its new width on screen. What silently
// does not happen is the save, so no width ever reaches localStorage and
// TOR-157's whole point (drag a border and it is remembered) is gone; and
// syncRunDetailWidth never runs, so TOR-174's recheck after a column drag goes
// with it.
//
// A CONTENT CHECK CANNOT CATCH THE BUG ITSELF. `this.saveColumnWidths(...)`
// reads exactly as it should whichever way the enclosing function is written -
// that is why the original passed every guard in this file and only a real
// drag in a browser, plus the console, found it. So this test does the one
// thing text CAN do: it pins the arrow form, and says why, so the next person
// to "tidy" it into a declaration is told what that costs.
func TestTheDragEndKeepsTheElementAsIts_this(t *testing.T) {
	// COMMENTS STRIPPED, and not as a formality: the comment this fix left in
	// run-table.js quotes `function endColumnDrag(...)` while explaining why
	// that spelling is wrong, so a check over the raw text matches the prose
	// and fails on the fixed file. Two guards in this same batch were written
	// against raw text and passed on a commented-out subject for the mirror
	// image of this reason.
	js := liveJS(t, runTableJS(t))

	if strings.Contains(js, "function endColumnDrag(") {
		t.Error("endColumnDrag is a function declaration. It is registered with " +
			"handle.addEventListener, so `this` inside it is the <span> handle rather than " +
			"the element, and this.saveColumnWidths(...) throws TypeError on every drag that " +
			"ends - silently, because the lines before it have already removed the class and " +
			"the column keeps its width on screen. No width is ever saved and " +
			"syncRunDetailWidth never runs")
	}
	if !strings.Contains(js, "const endColumnDrag = (event) =>") {
		t.Error("endColumnDrag is no longer `const endColumnDrag = (event) =>`. It has to " +
			"close over the element's `this` lexically, because it is used as a listener on " +
			"the handle in four places (pointerup, pointercancel, lostpointercapture, and " +
			"the no-button-held branch of pointermove) and every one of them would otherwise " +
			"call it with the handle as `this`")
	}

	// And the two calls that make the binding matter, so this test fails if the
	// body is gutted rather than only if the wrapper is rewritten.
	for _, want := range []string{"this.saveColumnWidths(this.columnWidths);", "this.syncRunDetailWidth();"} {
		if !strings.Contains(js, want) {
			t.Errorf("the drag's end no longer calls %q - the arrow form above exists so that "+
				"this call resolves against the element, and without the call there is "+
				"nothing for it to resolve", want)
		}
	}
}
