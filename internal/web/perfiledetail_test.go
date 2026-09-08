package web

import (
	"regexp"
	"strings"
	"testing"
)

// TOR-182: a video file in the list opens its own detail one level in - the
// metadata disclosure, the progress bar, the frames and the buttons that used
// to sit in a `.files` section under the list now hang off that file's own
// row, and only one of them may be open at a time.
//
// These read the script and the stylesheet as served, the same boundary
// filelist_test.go and rundetailwidth_test.go draw for their own pairs: this
// repository ships no JS runner (see columns_test.go's note), so a text check
// catches plumbing deleted, renamed or silently regated by a later refactor,
// and it cannot catch a wrong pixel or a click that toggles twice. A real
// browser is what TOR-182's own report used for that, on a torrent holding
// five files and two videos with frames on disk, and its measurement of the
// innermost level against the pane is what answers TOR-174 for this shape.

// jsFuncWithDoc is jsFunc plus the contiguous block of // comments directly
// above the function, which in this file is where most of the reasoning
// actually lives - jsFunc deliberately starts at the `function` keyword, so a
// check for a ticket reference run through it would report every doc comment
// as missing.
//
// Like jsFunc it reads whichever of the three shipped modules it is handed
// (TOR-191), which is what lets the table below say where each piece of
// reasoning now lives rather than assuming all of it is in app.js.
func jsFuncWithDoc(t *testing.T, js, name string) string {
	t.Helper()

	body := jsFunc(t, js, name)
	head := strings.Index(js, body)
	if head < 0 {
		t.Fatalf("jsFunc's answer for %s is not in the module handed to it", name)
	}

	lines := strings.Split(js[:head], "\n")
	// The last element is whatever precedes `function` on its own line, which
	// is nothing; walk back from the line before it.
	first := len(lines) - 1
	for first > 0 && strings.HasPrefix(strings.TrimSpace(lines[first-1]), "//") {
		first--
	}
	return strings.Join(lines[first:], "\n") + body
}

// TestAVideoFileOpensItsOwnDetailOneLevelIn is the move itself: the slot the
// detail is built into belongs to the file's own row in the list, and the
// container that used to hold the file cards below the list is gone rather
// than left empty beside it.
func TestAVideoFileOpensItsOwnDetailOneLevelIn(t *testing.T) {
	js := servedScript(t)
	css := stylesheet(t)

	render := jsFunc(t, js, "renderFileList")

	// The slot, built with the row rather than by whatever fills it later:
	// the disclosure has to be able to name the region it opens from the
	// start (aria-controls), and a region minted later is one the control
	// pointed at nothing for.
	if !strings.Contains(render, `detail.className = "file-detail"`) {
		t.Fatal("renderFileList builds no .file-detail slot on a file's row - the whole " +
			"of this ticket is that a file's detail hangs off its own row in the list")
	}
	if !strings.Contains(render, "detail.hidden = true") {
		t.Error("the detail slot is not built closed - every file after the first starts " +
			"collapsed, and a slot that begins open would show the first file's detail " +
			"under every row for the frame before setFileExpanded runs")
	}
	if !strings.Contains(render, "open.setAttribute(\"aria-controls\", detail.id)") {
		t.Error("the row's disclosure does not name the region it opens - a control that " +
			"opens something has to say what, and the id is why the slot is built here")
	}

	// SIBLING, NOT DESCENDANT. This is the same structural decision the run
	// row makes by putting its detail in a second <tr>: everything in a
	// detail - a thumbnail, Regenerate, the per-frame delete - is outside the
	// row that toggles it, so using the detail cannot close it.
	if !strings.Contains(render, "item.append(detail)") {
		t.Error("the detail slot is not appended to the row's own <li> - it has to be a " +
			"sibling of the clickable row and a child of the item that holds both")
	}
	if regexp.MustCompile(`row\.append\([^)]*\bdetail\b`).MatchString(render) {
		t.Error("the detail slot is appended INSIDE the clickable row. Every click in it " +
			"would then bubble to the row's own toggle and collapse the thing being " +
			"used - the failure newRunEntry's second <tr> exists to avoid, one level down")
	}

	// And fileBlock builds into that slot, not into a container of its own.
	block := jsFunc(t, js, "fileBlock")
	if !strings.Contains(block, "const listRow = entry.pickerRows.get(index)") {
		t.Fatal("fileBlock does not look up the file's row - the row is where its detail " +
			"goes now, so the row is what it needs before it can build anything")
	}
	if !strings.Contains(block, "const body = listRow.detail") {
		t.Error("fileBlock does not build into the row's own detail slot")
	}
	if !strings.Contains(block, "if (!listRow) {") || !strings.Contains(block, "return null") {
		t.Error("fileBlock does not cope with a file the list has no row for. It cannot " +
			"happen for any sequence the server publishes (MetadataReady precedes the " +
			"first FileStarted on both paths), but this page must not throw on an " +
			"invariant that lives in another package")
	}

	// The old home, gone from both files rather than left as an empty
	// section that a later ticket would wonder about.
	live := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(js, "")
	for _, gone := range []string{`class="files"`, "filesEl", "file-toggle", "file-body"} {
		if strings.Contains(live, gone) {
			t.Errorf("app.js still builds or reads %q - the file cards moved into the "+
				"list's own rows, and the container that held them has nothing left to "+
				"hold", gone)
		}
	}
	liveCSS := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
	for _, gone := range []string{".files", ".file-toggle", ".file-title", ".file-body", ".file-summary"} {
		if hasSelector(liveCSS, gone) {
			t.Errorf("app.css still styles %q, which app.js no longer builds", gone)
		}
	}
	// And what replaced them is really there, with the one thing collapse
	// depends on: no `display` of its own, so the UA's [hidden] rule wins.
	if !hasSelector(liveCSS, ".file-detail") {
		t.Fatal("app.css has no .file-detail rule outside a comment")
	}
	if got := cssRule(t, css, ".file-detail {"); strings.Contains(got, "display:") {
		t.Errorf(".file-detail declares display (%q). A class rule beats the UA's "+
			"[hidden] { display: none } at equal specificity, so app.js hiding this "+
			"container would do nothing - the trap this file has now paid for five times", got)
	}
	if got := cssRule(t, css, ".file-detail[hidden] {"); !strings.Contains(got, "display: none") {
		t.Errorf(".file-detail[hidden] is %q, want display: none. It is redundant while "+
			"the rule above declares no display, and it is kept precisely so that "+
			"adding one there cannot silently break the accordion", got)
	}
}

// TestTheRowOwnsTheToggleAndTheTickStillOwnsTheBox is the collision this
// ticket had to avoid: the row already meant "spend traffic on this file"
// (TOR-181), and it now also means "open what that traffic bought".
//
// They cannot collide, because a file gets a detail exactly when it has been
// asked for and updateFileCosts disables the box of every asked-for file. The
// one hazard left is mechanical: activating a <label> dispatches a second,
// synthetic click on the control it labels, so a handler that did not exclude
// the box by TARGET would fire twice per click on the file's name and toggle
// the detail straight back closed.
func TestTheRowOwnsTheToggleAndTheTickStillOwnsTheBox(t *testing.T) {
	js := servedScript(t)
	render := jsFunc(t, js, "renderFileList")

	listener := regexp.MustCompile(`(?s)row\.addEventListener\("click", \(event\) => \{(.*?)\n      \}\);`).
		FindStringSubmatch(render)
	if listener == nil {
		t.Fatal("renderFileList registers no click listener on the file's row - the " +
			"ticket's words are that clicking a video FILE opens its detail, and a " +
			"0.7em triangle is not the file")
	}
	if !strings.Contains(listener[1], "if (event.target === box) return;") {
		t.Errorf("the row's click handler is %q and does not exclude the checkbox by "+
			"target. A <label>'s activation dispatches a second click on its own "+
			"control, which bubbles to this same handler - so a click on the name "+
			"toggles the detail open and then closed again", listener[1])
	}
	if !strings.Contains(listener[1], "toggleFileDetail(entry, file.index)") {
		t.Errorf("the row's click handler is %q, and does not go through "+
			"toggleFileDetail - which is where reading the other result sets off disk "+
			"on first open lives", listener[1])
	}

	// The box keeps the meaning TOR-181 gave it, and nothing about the row
	// becoming a disclosure may take that away.
	if !regexp.MustCompile(`box\.addEventListener\("change", \(\) => tickFile\(`).MatchString(render) {
		t.Error("a checkbox's change no longer calls tickFile - the tick IS the decision " +
			"since TOR-181, and the row growing a second meaning must not cost the first")
	}

	// THE LABEL NAMES ITS CONTROL, and this is the bug the browser found that
	// none of the text checks could. A <label> with no `for` labels its FIRST
	// LABELABLE DESCENDANT, and `button` is labelable - so the disclosure
	// button, which sits first in the row, quietly became the label's control:
	// clicking the file's name activated the button, the button dispatched a
	// second click on the same row, and one click toggled the detail open and
	// shut. The quieter half is worse: the checkbox stopped being the label's
	// control at all, so clicking the name of a tickable row no longer ticked
	// it, which is TOR-181 undone by DOM order.
	if !strings.Contains(render, "row.htmlFor = box.id") {
		t.Error("the row's <label> does not name the control it labels. Without `for` the " +
			"label picks its first labelable DESCENDANT, which is the disclosure button " +
			"sitting ahead of the checkbox - so clicking the name toggles the detail " +
			"twice and never ticks the file")
	}
	if !regexp.MustCompile(`box\.id = "file-tick-" \+ \(\+\+detailSeq\)`).MatchString(render) {
		t.Error("the checkbox has no id for the label to name, or one that is not unique " +
			"per row - two rows sharing an id means one label pointing at the other " +
			"row's box")
	}

	// A file that has said nothing has nothing to open, so the row does
	// nothing rather than showing an empty panel.
	toggle := jsFunc(t, js, "toggleFileDetail")
	if !strings.Contains(toggle, "if (!fentry) return") {
		t.Error("toggleFileDetail opens a detail for a file with no block yet - until a " +
			"tick starts a file there is no metadata, no plan and no frames, and an " +
			"empty panel says the file has nothing rather than that nothing has run")
	}
	if !strings.Contains(toggle, "loadFileDetail(entry, fentry, index)") {
		t.Error("opening a file no longer reads its other result sets off disk - that is " +
			"the only way a regeneration's frames can be reached at all (loadFileDetail)")
	}
}

// TestOnlyOneFileDetailIsOpenAtOnce is the decision this ticket was asked to
// make, and the level it is made at is the whole of the answer.
//
// The row level deliberately allows several torrents open at once and gives
// three reasons (setRunExpanded). None is repealed here: what changes is the
// worst case. Twenty-five video files in a season pack, times two expanded
// torrents, is fifty frame grids on one page - and unlike a pane that has to
// be rebuilt, a collapsed file loses nothing by closing, because its detail
// keeps being updated whether or not it is on screen.
func TestOnlyOneFileDetailIsOpenAtOnce(t *testing.T) {
	js := servedScript(t)

	fn := jsFunc(t, js, "setFileExpanded")
	if !strings.Contains(fn, "fentry.entry.fileEntries.values()") {
		t.Fatal("setFileExpanded does not look at this torrent's other files - a " +
			"single-open accordion is exactly the sibling it has to close")
	}
	if !regexp.MustCompile(`if \(other !== fentry && other\.expanded\) setFileExpanded\(other, false\)`).
		MatchString(fn) {
		t.Error("setFileExpanded does not collapse the file that was open - without it " +
			"this level is multi-open, and a season pack inside two expanded torrents " +
			"is fifty frame grids on one page")
	}
	if !strings.Contains(fn, "if (expanded) {") {
		t.Error("setFileExpanded closes the siblings unconditionally - closing a file " +
			"must not also close the others, or nothing would ever be open and the " +
			"recursion above would not terminate")
	}

	// AND THE LEVEL ABOVE IS UNTOUCHED. The bound belongs on the inner level
	// precisely so the outer one keeps what it is for: two torrents side by
	// side, one of them mid-run.
	//
	// setRunExpanded is a method of the table element since TOR-194 - the
	// accordion's mechanism is three attributes on row elements, so it moved
	// with the rows. What it must not read did not change with the move.
	run := jsMethod(t, runTableJS(t), "setRunExpanded")
	for _, forbidden := range []string{"state.runs", "fileEntries", "setFileExpanded"} {
		if strings.Contains(run, forbidden) {
			t.Errorf("setRunExpanded now reads %q. It must not: several torrents may be "+
				"open at once (see its own heading for the three reasons), and closing "+
				"one to open another is what destroys work in progress", forbidden)
		}
	}
}

// TestCollapsingATorrentLeavesItsOpenFileOpen is the question the report has
// to answer, and the answer is that nothing happens: collapsing a row hides
// its detail row and changes nothing inside it, so re-opening the torrent
// shows the same file open, with whatever arrived meanwhile in its grid.
//
// That is not a coincidence - it is the discipline both accordions already
// keep, that no event and no other toggle may change a person's expansion.
func TestCollapsingATorrentLeavesItsOpenFileOpen(t *testing.T) {
	js := servedScript(t)

	// The table element's, since TOR-194 (see the sibling test above).
	run := jsMethod(t, runTableJS(t), "setRunExpanded")
	if !strings.Contains(run, "entry.detailRowEl.hidden = !expanded") {
		t.Fatal("setRunExpanded no longer collapses by hiding the detail's own row - " +
			"this test is anchored on that being the only thing it does to the content")
	}

	// EVERYTHING IT WRITES, enumerated rather than spot-checked, because the
	// property this test is about is a negative one: a torrent closing must
	// touch its own row and its own detail row and nothing inside them. A
	// check for a particular forbidden call would miss the next way of
	// writing the same mistake; a check on every left-hand side cannot.
	//
	// Anywhere on the line, not only at its start - a write folded into a
	// one-line `for (...) x.y = false;` is exactly the shape a mutation run
	// found this check blind to. Comments first, because several of them name
	// fields in prose. The trailing [^=>] is what keeps ===, !== and => out.
	body := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(run, "")
	writes := regexp.MustCompile(`([A-Za-z_][\w.]*)\s*=([^=>])`).FindAllStringSubmatch(body, -1)
	if len(writes) < 3 {
		t.Fatalf("only %d assignments were found in setRunExpanded; the scan is broken, "+
			"not the function", len(writes))
	}
	for _, w := range writes {
		if strings.Contains(w[1], "fentry") || strings.Contains(w[1], "file") {
			t.Errorf("setRunExpanded writes %s. A torrent closing must not collapse the "+
				"file somebody left open inside it - the detail row goes off screen with "+
				"everything in it exactly as it was, which is what makes re-opening the "+
				"torrent show the same file open, with whatever landed meanwhile in its "+
				"grid", w[1])
		}
	}
	// And nothing it CALLS may do it either.
	for _, forbidden := range []string{"setFileExpanded(", "fileEntries", "toggleFileDetail("} {
		if strings.Contains(run, forbidden) {
			t.Errorf("setRunExpanded calls or reads %q, so closing a torrent reaches "+
				"inside it", forbidden)
		}
	}

	// And the other path a row closes by.
	toggle := jsFunc(t, js, "toggleRun")
	for _, forbidden := range []string{"fentry", "fileEntries", "setFileExpanded", "toggleFileDetail"} {
		if strings.Contains(toggle, forbidden) {
			t.Errorf("toggleRun names %q - clicking a torrent's row must say nothing "+
				"about which of its files is open", forbidden)
		}
	}
}

// TestASecondFileListMessageDoesNotWipeTheDetailsInsideIt is the regression
// this move creates if nobody guards it, and it is not hypothetical: a top-up
// and a retry (TOR-152) both mint a run whose events land on the same entry
// (claimReopenedRun), and each publishes its own metadata_ready. Before
// TOR-182 that redrew an identical list; after it, rebuilding the rows
// detaches every frame grid, every open disclosure and every element
// fileBlock is holding a reference to - on a row whose frames are on screen.
func TestASecondFileListMessageDoesNotWipeTheDetailsInsideIt(t *testing.T) {
	js := servedScript(t)
	// SINCE TOR-191 THE DECISION IS THE EVENT LAYER'S, and that is the whole
	// improvement: renderFileList used to compute the signature and return
	// early, so "did it redraw" was a fact about a function's control flow.
	// Now events.js either asks the page for a rebuild or does not, which is
	// a fact something can COUNT - and
	// TestASecondIdenticalFileListRebuildsNothing (eventstate_test.go) counts
	// it, against four real messages, which is the only way to tell a
	// suppressed rebuild from a suppressed everything.
	fn := jsFunc(t, eventsJS(t), "applyFileList")

	// The signature is the list ITSELF, not a counter or a length: two
	// different lists of the same length must not compare equal.
	sig := regexp.MustCompile(`(?s)const sig = (.*?);\n`).FindStringSubmatch(fn)
	if sig == nil {
		t.Fatal("renderFileList computes no signature of the list it is about to draw, " +
			"so it cannot tell a new list from the one already on screen - and the " +
			"redraw now costs every nested detail")
	}
	if !strings.Contains(sig[1], "entry.fileList.map(") {
		t.Errorf("the signature is %q and is not derived from the file list itself - a "+
			"message count or a length would call two different lists the same", sig[1])
	}
	if !strings.Contains(sig[1], "file.index") || !strings.Contains(sig[1], "file.path") {
		t.Errorf("the signature is %q and does not cover both what each row is keyed by "+
			"(index) and what it shows (path)", sig[1])
	}
	if !strings.Contains(sig[1], "entry.fileListKnown") {
		t.Errorf("the signature is %q and ignores whether the whole list was known - the "+
			"video list stands in when it was not (ABSENT IS NOT EMPTY above), and the "+
			"same paths mean a different list in those two cases", sig[1])
	}

	if !regexp.MustCompile(`if \(sig !== "" && sig === entry\.fileListSig\) return;`).MatchString(fn) {
		t.Error("applyFileList does not skip the rebuild for a list it has already " +
			"drawn. The empty-signature half matters too: an empty list must not match " +
			"the next empty one and suppress the first real draw")
	}
	// The repeat message must not go silent: everything about a row that is
	// not its SHAPE - the boxes, the prices, the frame, the title - still has
	// to follow it. That is the syncEntry both callers end on, so it is read
	// out of them rather than out of the early return.
	for _, caller := range []string{"applyMetadataReady", "applyNeedsAction"} {
		body := jsFunc(t, eventsJS(t), caller)
		if !strings.Contains(body, "applyFileList(entry, ev);") {
			t.Errorf("%s does not go through applyFileList - the signature would not be "+
				"consulted at all for the message it carries", caller)
		}
		if !strings.Contains(body, "view.syncEntry(entry);") {
			t.Errorf("%s does not re-sync the row - the list's shape is unchanged on a "+
				"repeat, but the ticks, the prices and the parked frame all still have "+
				"to follow the message that arrived", caller)
		}
	}

	// AND WHEN IT DOES REBUILD, the blocks go with the rows that held them.
	// Every fentry points at elements inside a row a rebuild detaches, so a
	// map left standing would have fileBlock hand back a block whose DOM is
	// off the page - and that file's detail would never appear again, in
	// silence, for the rest of the row's life. The order is load-bearing and
	// now spans two files: the file entries are the only handle on those
	// blocks, so they go BEFORE the page is asked to detach the rows.
	if !regexp.MustCompile(`(?s)entry\.fileEntries\.clear\(\);\s*\n\s*entry\.autoExpanded = false;\s*\n\s*view\.rebuildFileList\(entry\);`).MatchString(fn) {
		t.Errorf("applyFileList does not clear the file entries and re-arm the auto-expand "+
			"immediately before asking for the rebuild - they are references into the rows "+
			"that rebuild detaches, and the latch is what makes only the FIRST file open "+
			"itself: %q", fn)
	}
	// The page's own half: a fresh map, since every bundle in the old one
	// points into a row that is about to go.
	if !strings.Contains(jsFunc(t, js, "renderFileList"), "entry.pickerRows = new Map();") {
		t.Error("renderFileList reuses the row map across a rebuild - its bundles are " +
			"references into the rows it is replacing")
	}

	// A genuine fresh start still rebuilds, which is what keeps this from
	// being a way to lose the list entirely.
	// Comments stripped: both lines below are named in resetRunState's own
	// prose as well as executed by it, and a comment satisfies strings.Contains.
	reset := stripJSComments(jsFunc(t, stateJS(t), "resetRunState"))
	if !strings.Contains(reset, `entry.fileListSig = ""`) {
		t.Error("resetRunState does not clear the signature. Left behind, it tells the " +
			"replayed metadata_ready that follows a reset \"you already drew this\", and " +
			"the row comes back from a reconnect with an empty list")
	}
	if !strings.Contains(reset, "entry.fileEntries.clear()") {
		t.Error("resetRunState no longer clears the file entries - they are the only " +
			"handle on blocks that are about to be detached with the rows holding them")
	}
}

// TestTheMovedBlocksKeepTheReasonsAttachedToThem is the ticket's own
// standard: this is a move, not a rewrite, and whatever is reused keeps the
// reasoning attached to it. Each pair below is an element that travelled and
// the ticket whose comment travelled with it - a move that strips those
// loses the knowledge, not just the prose.
func TestTheMovedBlocksKeepTheReasonsAttachedToThem(t *testing.T) {
	js := servedScript(t)
	block := jsFunc(t, js, "fileBlock")

	// Every element that travelled, still built.
	for _, element := range []string{
		// The metadata disclosure, collapsed by default.
		`class="meta-toggle"`, `class="meta-body"`, `class="specs"`, `class="tracks"`,
		// The reach strip and the swarm chip beside it - two elements
		// sharing one row, which is the whole point of TOR-153's merge.
		`class="reach-strip"`, `class="avail-swarm-dot"`,
		// Regenerate, and Compare beside it.
		`class="file-regen-go"`, `class="file-compare"`, `class="file-regen-count"`,
		// The per-file progress line, the contact-sheet link, and the grid
		// the frames land in.
		`class="file-progress"`, `class="file-links"`, `class="grid"`,
	} {
		if !strings.Contains(block, element) {
			t.Errorf("fileBlock no longer builds %s - it was in the flat detail, so "+
				"losing it in the move is a silent loss of something that worked", element)
		}
	}

	// And the reasoning, in the place it actually sits. A comment sits either
	// beside the element (fileBlock's own template) or on the function that
	// fills it, and this checks whichever of the two it is rather than
	// requiring it to be moved to where a test would find it more easily.
	//
	// SINCE TOR-191 "the place it actually sits" includes WHICH MODULE, and
	// that is the point rather than an inconvenience: this table is the
	// clearest statement in the suite of where each of these decisions ended
	// up, and a piece of reasoning that arrived in the wrong module is a
	// piece of reasoning nobody editing that code will read.
	modules := map[string]string{
		"app.js":    js,
		"state.js":  stateJS(t),
		"events.js": eventsJS(t),
	}
	for _, want := range []struct{ module, where, ticket, what string }{
		{"app.js", "fileBlock", "TOR-153", "the swarm chip beside the reach strip, and why it " +
			"stays a separate shape rather than a fill on the strip's own axis"},
		{"app.js", "fileBlock", "TOR-109", "why Compare sits beside Regenerate"},
		// The plan itself is what a file KNOWS, so it moved to state.js's own
		// file-entry shape - and the reserved cell's reasoning went with the
		// field rather than staying beside the elements it explains.
		{"state.js", "newFileState", "TOR-110", "the plan-shaped grid, laid out at final size " +
			"before any piece is fetched, and the reserved cell that is the reason for it"},
		{"app.js", "fileBlock", "TOR-71", "the metadata accordion, one level inside a file's own"},
		{"app.js", "setMetaExpanded", "TOR-71", "the single function that may open or close a " +
			"file's metadata"},
		// onFileDone split in two: what a finished file KNOWS (events.js) and
		// the link drawn from it (app.js's renderFileLinks). TOR-171 is about
		// what is drawn, so it travelled with the drawing.
		{"app.js", "renderFileLinks", "TOR-171", "why the manifest link is not offered beside " +
			"the contact sheet"},
		{"events.js", "applyFrameProgress", "TOR-167", "which of two signals the row's bar " +
			"reads from"},
		{"app.js", "renderReach", "TOR-179", "where the strip's data comes from, now that it is " +
			"the frames' own byte ranges rather than the last run's claim log"},
		{"state.js", "frameState", "TOR-118", "a point that produced nothing, given a cell of its " +
			"own rather than left looking like one still on its way"},
	} {
		if !strings.Contains(jsFuncWithDoc(t, modules[want.module], want.where), want.ticket) {
			t.Errorf("%s's %s no longer names %s, which is where %s was decided - a move that "+
				"keeps the element and drops the reasoning loses the knowledge, not just "+
				"the prose", want.module, want.where, want.ticket, want.what)
		}
	}

	// The grid's four cell states, still four: the two the disk shape can
	// report and the two only a plan-shaped grid has. gridCells is a
	// derivation over a file's own frames and plan, so it is state.js's.
	cells := jsFunc(t, stateJS(t), "gridCells")
	for _, state := range []string{`"exact"`, `"shifted"`, `"failed"`, `"pending"`} {
		if !strings.Contains(cells, state) {
			t.Errorf("gridCells no longer produces the %s cell state - TOR-110's answer "+
				"is that all four are distinguishable, and a reserved cell that never "+
				"fills must not look like one still on its way", state)
		}
	}

	// And the top-up offer, which is the one block on the ticket's list that
	// is about the RUN rather than about a file: it stays above the list,
	// where it was, and the move must not have swept it in.
	if !strings.Contains(js, `'<section class="run-again" hidden>'`) {
		t.Error("the run detail's template no longer builds .run-again - the top-up " +
			"offer (TOR-152) is a property of the run, not of any one file, and it " +
			"belongs above the list rather than inside a file's own detail")
	}
	if strings.Contains(block, "run-again") {
		t.Error("fileBlock builds the top-up offer into a file's detail. It is one " +
			"offer per run and would be repeated per file, and the figure it states " +
			"is the run's own")
	}
}

// TestTheRowsSummaryStaysOnScreenWhileTheFileIsOpen records a behaviour that
// changed in the move, so that it reads as a decision rather than as
// something that got lost.
//
// The summary - resolution and frame count - used to be hidden while the file
// was expanded, because "the collapsed-only summary line and the specs panel
// say the same thing two different ways". That premise stopped being true
// when TOR-71 put the specs behind their own disclosure, collapsed by
// default: opening a file took both figures off screen and replaced them with
// nothing. And since TOR-182 the summary is a COLUMN of the file list, read
// down twenty-five rows, so a column that empties the row you are looking at
// is one that has to be re-found every time.
func TestTheRowsSummaryStaysOnScreenWhileTheFileIsOpen(t *testing.T) {
	js := servedScript(t)
	fn := jsFunc(t, js, "setFileExpanded")

	if regexp.MustCompile(`fentry\.summary\.hidden\s*=`).MatchString(fn) {
		t.Error("setFileExpanded hides the row's summary again. It is a column of the " +
			"file list now, and the specs panel that used to repeat it is collapsed by " +
			"default (TOR-71) - so hiding it shows nothing in its place")
	}

	// It is on the row itself, and it is still kept current by the one
	// function that ever wrote it.
	render := jsFunc(t, js, "renderFileList")
	if !strings.Contains(render, `summary.className = "picker-summary"`) {
		t.Fatal("renderFileList builds no .picker-summary on a file's row - the file " +
			"card's own title line is gone, so this row is where it goes")
	}
	if !strings.Contains(jsFunc(t, js, "updateFileSummary"), "fentry.summary.textContent") {
		t.Error("updateFileSummary no longer writes the summary - the figures have to " +
			"follow file_started and every frame_ready, open or closed")
	}

	css := stylesheet(t)
	rule := cssRule(t, css, ".picker-summary {")
	if !strings.Contains(rule, "var(--ink-2)") {
		t.Errorf(".picker-summary is %q, want var(--ink-2) - the type .file-summary "+
			"carried before the move", rule)
	}
	if strings.Contains(rule, "var(--warn)") {
		t.Errorf(".picker-summary is %q. --warn is what .picker-cost beside it wears "+
			"for \"this is about to cost you\"; this figure is about what has already "+
			"been spent", rule)
	}
	if strings.Contains(rule, "--ink-3") {
		t.Errorf(".picker-summary reads in --ink-3 (%q). TOR-158 took that level off "+
			"text entirely", rule)
	}
}

// TestTheDisclosureIsReservedOnEveryRowAndDrawnWhereThereIsSomethingToOpen
// is what keeps the list a column. The triangle can only appear once a file
// has a detail behind it, and a control that appears would shift every name
// on the row it appears on - so its box is always there and only its
// visibility changes, the same way .picker-mark reserves the checkbox column
// and .run-detail-cancel[data-idle] reserves Cancel's.
func TestTheDisclosureIsReservedOnEveryRowAndDrawnWhereThereIsSomethingToOpen(t *testing.T) {
	js := servedScript(t)
	css := stylesheet(t)

	render := jsFunc(t, js, "renderFileList")
	if !regexp.MustCompile(`createElement\(video \? "button" : "span"\)`).MatchString(render) {
		t.Error("the disclosure is built as the same element on every row - a <button> " +
			"belongs only where there can ever be something to open, and a file no " +
			"frame can be taken from never can be")
	}
	if !strings.Contains(render, `open.className = "picker-open"`) {
		t.Fatal("renderFileList builds no .picker-open on a file's row")
	}
	if !strings.Contains(render, `open.setAttribute("aria-label", `) {
		t.Error("the disclosure carries no name of its own. The row's <label> text " +
			"belongs to the checkbox, so a reader by ear would get \"button\" with " +
			"nothing after it")
	}

	rule := cssRule(t, css, ".picker-open {")
	if !strings.Contains(rule, "visibility: hidden") {
		t.Errorf(".picker-open is %q and is not reserved-but-hidden. `display: none` "+
			"would take its box out of the row, so every name would shift sideways the "+
			"moment its file started - and the list would stop being a column", rule)
	}
	if strings.Contains(rule, "display: none") {
		t.Errorf(".picker-open is %q and uses display to hide - see above", rule)
	}
	shown := cssRule(t, css, `.picker-item[data-detail="true"] .picker-open {`)
	if !strings.Contains(shown, "visibility: visible") {
		t.Errorf("a row with a detail draws its triangle by %q - the attribute is the "+
			"one signal that there is something behind it", shown)
	}

	// One writer of that attribute, and it is the moment the detail exists.
	block := jsFunc(t, js, "fileBlock")
	if !strings.Contains(block, `listRow.item.dataset.detail = "true"`) {
		t.Error("fileBlock does not mark the row as having a detail, so the triangle " +
			"never appears on a row that does")
	}
	writers := regexp.MustCompile(`dataset\.detail\s*=`).FindAllString(js, -1)
	if len(writers) != 1 {
		t.Errorf("app.js writes dataset.detail in %d places, want 1. It says \"there is "+
			"something behind this triangle\", and a second writer is how that comes to "+
			"be claimed for a row with an empty slot", len(writers))
	}
}

// TestTheFileListNoLongerCapsItsOwnHeight is the loss this ticket accepts,
// written down so it reads as a decision.
//
// .picker-list was `max-height: 18rem; overflow-y: auto`, so a season pack's
// names could not push the rest of the detail off screen. Since the list
// CONTAINS each file's detail, that cap is a cap on the frame grid inside it -
// a contact sheet read through an 18rem window, with two scrollbars fighting
// over the wheel. What replaces it is the level above: a torrent's whole row
// closes in one click.
func TestTheFileListNoLongerCapsItsOwnHeight(t *testing.T) {
	css := stylesheet(t)
	rule := cssRule(t, css, ".picker-list {")

	for _, gone := range []string{"max-height", "overflow-y", "overflow:"} {
		if strings.Contains(rule, gone) {
			t.Errorf(".picker-list still declares %s (%q). It holds each file's own "+
				"detail now, so a cap here is a cap on the frame grid - and a scroller "+
				"here is a scroller around the widest thing on the page", gone, rule)
		}
	}
	// And the detail itself must not have grown one instead.
	if got := cssRule(t, css, ".file-detail {"); strings.Contains(got, "overflow") {
		t.Errorf(".file-detail declares overflow (%q) - putting the scroller one level "+
			"in is the same mistake with a smaller window", got)
	}
}
