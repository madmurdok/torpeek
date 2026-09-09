package web

import (
	"regexp"
	"strings"
	"testing"
)

// TOR-195 turned the three depths of a row's detail into three nested custom
// elements, and these are the guards that did not exist before it - the same
// finding TOR-193 and TOR-194 each recorded one level up: the mechanisms this
// file checks were spread through 3,500 lines of app.js with nothing watching
// most of them, so a move could have lost any of them silently.
//
// WHAT IS ALREADY GUARDED ELSEWHERE and deliberately not repeated here: the
// tick and its price (tick_test.go), the four un-tick verdicts and their order
// (tick_test.go, untick_test.go), the clear (clear_test.go), the file's own
// detail and its accordion (perfiledetail_test.go), the top-up offer and its
// figure (again_test.go), the reach strip (avail_test.go), the run entry's
// field collisions and the [hidden] trap (filelist_test.go). All of those were
// RETARGETED onto the new modules by this ticket rather than rewritten - the
// subjects did not change, only the file each lives in.
//
// WHAT THIS FILE ADDS is the three elements themselves: are they registered,
// do they find their parts and fail loudly on a missing one, do they refuse an
// incomplete service set, do they survive the table's re-sort (which is the
// trap this ticket paid for), and do the three boundaries hold in both
// directions.
//
// Text guards, like the rest of this package's front-end tests (see
// columns_test.go's opening note): they catch a mechanism deleted, renamed or
// regated, and they cannot see a pointer move. Two things here are stronger
// than a text check, and both are recorded in TOR-195's report rather than
// living in this repository: the whole detail tree was driven headlessly in
// jsdom through the real event layer (sixty assertions, which is what found
// the field/method collision TestNoInstanceFieldShadowsAMethod now guards),
// and the browser pass the acceptance criteria name.
//
// EVERY ONE OF THEM READS THE MODULE WITH ITS COMMENTS STRIPPED where the
// subject could be named in prose, for the reason runtable_test.go's liveJS
// records: `// customElements.define("file-list", FileList);` still CONTAINS
// the string a Contains check looks for, so commenting out a registration
// would leave such a guard green while the element was not registered at all.

// detailModules is the three, with the tag each defines. Every table-driven
// check below walks it, so a fourth depth added later is one line.
func detailModules(t *testing.T) []struct {
	name, tag, src string
} {
	t.Helper()
	return []struct{ name, tag, src string }{
		{"run-detail.js", "run-detail", runDetailJS(t)},
		{"file-list.js", "file-list", fileListJS(t)},
		{"file-detail.js", "file-detail", fileDetailJS(t)},
	}
}

// TestTheThreeDetailElementsAreRegisteredAndNested is the move itself: three
// elements, defined natively, each created by the one above it rather than
// written into a template string.
//
// THE NESTING IS CREATED, NOT PARSED, and that is a decision with a reason
// rather than a style: a custom element written inside an innerHTML string is
// upgraded by a REACTION rather than synchronously, so its methods are not
// there to be called in the same breath - and the id the row's disclosure has
// to name is read in exactly that breath. document.createElement constructs a
// defined element on the spot.
func TestTheThreeDetailElementsAreRegisteredAndNested(t *testing.T) {
	for _, mod := range detailModules(t) {
		live := liveJS(t, mod.src)
		want := `customElements.define("` + mod.tag + `",`
		if !strings.Contains(live, want) {
			t.Errorf("%s does not contain %q in code - the element would never be defined, so "+
				"the tag would stay an unknown inline element with none of its behaviour",
				mod.name, want)
		}
	}

	// Each level creates the next with createElement, and none of them writes
	// a custom element into its own template.
	page := liveJS(t, appJS(t))
	if !strings.Contains(page, `document.createElement("run-detail")`) {
		t.Error("app.js does not create a <run-detail> - the cell the table hands out (TOR-194's " +
			"seam) would be left empty")
	}
	// AND INTO THE CELL THE TABLE HANDED OVER, which is the first of the
	// three boundaries and the one TOR-194 cut in advance: detailCell is the
	// whole seam, and mounting anywhere else would leave the table's own
	// colspanned row empty while the detail rendered somewhere it does not
	// belong.
	if !strings.Contains(page, "rowParts.detailCell.append(detail);") {
		t.Error("app.js does not mount the run's detail in the cell the table handed back - " +
			"detailCell is TOR-194's seam and the only place a detail may go")
	}
	detail := liveJS(t, runDetailJS(t))
	if !strings.Contains(detail, `document.createElement("file-list")`) {
		t.Error("run-detail.js does not create a <file-list> - a row would open onto a detail " +
			"with nothing in it about what the torrent holds")
	}
	list := liveJS(t, fileListJS(t))
	if !strings.Contains(list, `document.createElement("file-detail")`) {
		t.Error("file-list.js does not create a <file-detail> per video row - a file's row would " +
			"open onto nothing")
	}

	// AND NOT IN A TEMPLATE, which is the half a reader cannot see. Checked
	// against each module's own markup constant rather than the whole file, so
	// the createElement call above cannot satisfy it - AND WITH COMMENTS
	// STRIPPED, which the first draft of this guard was not: run-detail.js's
	// DETAIL carries a note saying the files sit in a nested <file-list>,
	// which is prose explaining the rule, and a raw-text check read it as the
	// violation. That is liveJS's lesson in its mirror image.
	for _, tmpl := range []struct{ where, name, src string }{
		{"run-detail.js", "DETAIL", jsConst(t, liveJS(t, runDetailJS(t)), "DETAIL")},
		{"file-list.js", "LIST", jsConst(t, liveJS(t, fileListJS(t)), "LIST")},
		{"file-detail.js", "BODY", jsConst(t, liveJS(t, fileDetailJS(t)), "BODY")},
	} {
		for _, tag := range []string{"<run-detail", "<file-list", "<file-detail"} {
			if strings.Contains(tmpl.src, tag) {
				t.Errorf("%s's %s template writes %q into markup. An element parsed out of innerHTML "+
					"is upgraded by a reaction rather than synchronously, so its methods are not "+
					"there to be called in the same turn - and the id the disclosure names is read "+
					"in that turn", tmpl.where, tmpl.name, tag)
			}
		}
	}
}

// jsConst returns the source of one top-level `const NAME =` up to the line
// that ends it - the same shape jsFunc uses for a function, for a multi-line
// string concatenation instead of a body. It ends at the first line ending in
// `;` at column zero's own statement level, which is what every one of these
// three templates ends with.
func jsConst(t *testing.T, js, name string) string {
	t.Helper()
	head := "const " + name + " =\n"
	i := strings.Index(js, head)
	if i < 0 {
		t.Fatalf("no top-level `const %s =` in the module under test", name)
	}
	rest := js[i:]
	end := strings.Index(rest, ";\n")
	if end < 0 {
		t.Fatalf("the %s constant is never closed", name)
	}
	return rest[:end]
}

// TestEachDetailElementFindsItsPartsAndFailsLoudly is the pattern's own
// discipline, which these three needed a second stage of: a run's detail and
// its file list find their parts as soon as their markup exists, and a file's
// detail finds ONE part then (the slot, because the row's disclosure has to
// name its id from the start) and the rest at mount, when the file has
// anything to show.
//
// A missing part is a wiring error rather than a state to degrade into, and
// this is the check that keeps it one: a class renamed in a template and not
// in the query list is otherwise silent, and two of these draws used to open
// with `if (!el) return`, which made a missing element a permanent no-op.
func TestEachDetailElementFindsItsPartsAndFailsLoudly(t *testing.T) {
	for _, mod := range detailModules(t) {
		live := liveJS(t, mod.src)
		if !strings.Contains(live, "this.querySelector(") {
			t.Errorf("%s finds no part with this.querySelector - a part found by document id would "+
				"let one torrent's detail pick up another's, which is the whole reason the page "+
				"can hold several open at once", mod.name)
		}
		if strings.Contains(live, "document.getElementById") ||
			strings.Contains(live, "document.querySelector(") {
			t.Errorf("%s reaches for a part through the document. One instance per torrent (and per "+
				"file) means the first one on the page would answer for all of them", mod.name)
		}
		want := `throw new Error("` + mod.tag + `: no " + name + " inside the element");`
		if !strings.Contains(live, want) {
			t.Errorf("%s does not throw by name on a missing part (%q) - every method below "+
				"dereferences them, so the alternative is \"cannot read property of null\" out of "+
				"whichever handler fires first, or a silent no-op forever", mod.name, want)
		}
	}

	// The file's detail has one part before mount and the rest after, so its
	// first stage throws on its own.
	if !strings.Contains(liveJS(t, fileDetailJS(t)),
		`if (!this.body) throw new Error("file-detail: no body inside the element");`) {
		t.Error("file-detail.js's build does not fail on a missing slot. It is the one part that " +
			"has to exist before the file has said anything, because the row's disclosure names " +
			"its id in aria-controls")
	}
}

// TestEachDetailElementRefusesAnIncompleteServiceSet is compare-dialog.js's
// shape, three more times: everything only app.js's bootstrap can do is
// injected, declared in one list, and checked by walking that list - so a
// rename fails while the wiring is being written rather than at the first
// press of a button three levels in.
func TestEachDetailElementRefusesAnIncompleteServiceSet(t *testing.T) {
	for _, mod := range detailModules(t) {
		live := liveJS(t, mod.src)
		if !strings.Contains(live, "const SERVICES = [") {
			t.Errorf("%s does not declare its services in one list - the check can only be as "+
				"complete as the list it walks", mod.name)
		}
		if !strings.Contains(live, "for (const name of SERVICES) {") {
			t.Errorf("%s's setServices does not walk SERVICES - a service added to the list would "+
				"then not be checked, which is the drift the one-list shape exists to prevent",
				mod.name)
		}
		want := `throw new Error("` + mod.tag + `: setServices needs a " + name + "() function");`
		if !strings.Contains(live, want) {
			t.Errorf("%s's setServices does not throw on a missing service (%q) - the first press "+
				"of the control that needs it would call undefined, mid-run, with nothing said at "+
				"wiring time", mod.name, want)
		}

		// AND THE LIST NAMES EVERY SLOT, which the first draft of this guard
		// did not check and a mutation run found: dropping cancelRun from
		// run-detail.js's SERVICES left everything green, because a shorter
		// list is only a shorter loop. Each injected service is a module-level
		// `let x = null;` - that is what setServices' destructuring fills - so
		// the two have to agree in BOTH directions, or the element stops
		// demanding something it still calls.
		slots := map[string]bool{}
		for _, m := range regexp.MustCompile(`(?m)^let (\w+) = null;`).FindAllStringSubmatch(live, -1) {
			slots[m[1]] = true
		}
		if len(slots) == 0 {
			t.Errorf("%s declares no injected service slot (`let x = null;`), so the checks below "+
				"verified nothing", mod.name)
		}
		declared := map[string]bool{}
		if names := regexp.MustCompile(`const SERVICES = \[([^\]]*)\]`).FindStringSubmatch(live); names != nil {
			for _, m := range regexp.MustCompile(`"(\w+)"`).FindAllStringSubmatch(names[1], -1) {
				declared[m[1]] = true
			}
		}
		for slot := range slots {
			if !declared[slot] {
				t.Errorf("%s injects %s but does not name it in SERVICES - setServices would accept "+
					"a wiring with no %s, and the element would call undefined at the first press",
					mod.name, slot, slot)
			}
		}
		for name := range declared {
			if !slots[name] {
				t.Errorf("%s demands a %q service it has no slot for - either the slot was renamed "+
					"and the list not, or the service is no longer used and the demand should go",
					mod.name, name)
			}
		}
	}

	// And the page hands all three sets over. The names are read out of each
	// module's own SERVICES list rather than repeated here, so this cannot
	// drift from what the elements actually demand.
	page := liveJS(t, appJS(t))
	for _, wiring := range []struct{ mod, call, src string }{
		{"run-detail.js", "setRunDetailServices({", runDetailJS(t)},
		{"file-list.js", "setFileListServices({", fileListJS(t)},
		{"file-detail.js", "setFileDetailServices({", fileDetailJS(t)},
	} {
		if !strings.Contains(page, wiring.call) {
			t.Errorf("app.js never calls %s - %s would throw at the first thing it was asked to "+
				"do", wiring.call, wiring.mod)
			continue
		}
		names := regexp.MustCompile(`const SERVICES = \[([^\]]*)\]`).
			FindStringSubmatch(liveJS(t, wiring.src))
		if names == nil {
			t.Errorf("cannot read %s's SERVICES list", wiring.mod)
			continue
		}
		found := regexp.MustCompile(`"(\w+)"`).FindAllStringSubmatch(names[1], -1)
		if len(found) == 0 {
			t.Errorf("%s's SERVICES list is empty, so the loop below checks nothing", wiring.mod)
		}
		for _, m := range found {
			if !strings.Contains(page, m[1]+":") && !strings.Contains(page, "\n\t"+m[1]) &&
				!strings.Contains(page, " "+m[1]+",") && !strings.Contains(page, "  "+m[1]+",") {
				t.Errorf("app.js's wiring does not hand %s its %q service - setServices would throw "+
					"at load, which is the point, but only after somebody notices", wiring.mod, m[1])
			}
		}
	}
}

// TestTheDetailElementsSurviveTheTablesResort is THE trap this ticket paid
// for, and the one a text check can still state precisely.
//
// run-table.js's syncRow ENDS WITH A RE-SORT, and reorderRuns moves a run's
// whole row group with `this.list.append(...)` - which for a node already in
// the grid is a REMOVE followed by an INSERT. Since TOR-215 that is ONE
// element per entry rather than two adjacent <tr>s, and the detail is inside
// it, so every element inside the detail is still disconnected and reconnected
// on every redraw of every row: dozens of times a second on a live run.
//
// Two things follow, and both are asserted here because either alone is a
// silent catastrophe. The markup must be built ONCE, or the second
// connectedCallback throws away the file list, every frame grid and every open
// disclosure. And nothing may be torn down on disconnect, or the first event
// after the page loads takes every listener off - which is TOR-194's shipped
// `this` regression in its other form: everything reads correctly and nothing
// works.
func TestTheDetailElementsSurviveTheTablesResort(t *testing.T) {
	// The premise, read from the table rather than assumed - if the re-sort
	// ever stops moving the rows, the reasoning below has to be re-made rather
	// than left standing.
	table := liveJS(t, runTableJS(t))
	if !strings.Contains(table, "this.list.append(entry.rowGroupEl);") {
		t.Fatal("run-table.js's reorderRuns no longer moves the row group with append. That move is " +
			"what disconnects and reconnects every element in the detail, and it is the whole " +
			"reason the three elements below build once and tear down nothing")
	}
	if !strings.Contains(jsMethod(t, table, "syncRow"), "this.reorderRuns();") {
		t.Fatal("syncRow no longer ends with a re-sort; the same re-check applies")
	}

	for _, mod := range detailModules(t) {
		live := liveJS(t, mod.src)

		if !strings.Contains(jsMethod(t, live, "connectedCallback"), "this.build();") {
			t.Errorf("%s's connectedCallback does not go through build() - the guard that makes it "+
				"safe to run again lives there", mod.name)
		}
		build := jsMethod(t, live, "build")
		if !regexp.MustCompile(`^\s*build\(\) \{\n\s*if \(this\.\w+\) return;`).MatchString(build) {
			t.Errorf("%s's build does not return early when it has already built - the table's "+
				"re-sort reconnects this element on every redraw, and a second innerHTML would "+
				"throw away everything inside it: %q", mod.name, firstLines(build, 3))
		}
		if strings.Contains(live, "disconnectedCallback") {
			t.Errorf("%s has a disconnectedCallback. Every listener it registers is on a node it "+
				"created inside itself, so they go when the DOM goes - and the table's re-sort "+
				"disconnects this element on every redraw, so a teardown would take every "+
				"control off on the first event and never put it back", mod.name)
		}
		// The reason there is nothing to tear down, stated as a check rather
		// than as prose: none of the three listens to anything that outlives
		// it. If one ever does, this is what says the paragraph above has to
		// be re-made.
		for _, outlives := range []string{
			"window.addEventListener", "document.addEventListener", "setInterval(", "setTimeout(",
		} {
			if strings.Contains(live, outlives) {
				t.Errorf("%s uses %q, which outlives the element - it needs an instance-bound field "+
					"and a disconnectedCallback to remove it (frame-panel.js's resize handler is "+
					"the worked example), and the no-teardown reasoning above no longer holds",
					mod.name, outlives)
			}
		}
	}
}

// firstLines is for an error message that has to show where a guard looked.
func firstLines(s string, n int) string {
	lines := strings.SplitN(strings.TrimLeft(s, "\n"), "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// TestNoInstanceFieldShadowsAMethod is the bug the headless drive found and
// no reading did, and it is exactly the class of mistake this batch keeps
// paying for: `this.toggle = row.open` in file-detail.js's mount shadowed the
// element's own `toggle()` method, so `fentry.block.toggle()` threw
// "toggle is not a function" the first time anybody opened a second file - and
// the code reads perfectly at both sites.
//
// A field is assigned on an instance and a method lives on the prototype, so
// the field wins and the method becomes unreachable, silently, from the moment
// the field is set. The fix was renaming the field (this.rowToggle, which also
// says what it is - the ROW's disclosure, the same thing entry.rowToggle is
// one level up); this is what makes the next one fail in CI instead of in a
// browser.
//
// It walks every class in every front-end module rather than only the three:
// the same mistake is available in all of them, and the scan costs nothing.
func TestNoInstanceFieldShadowsAMethod(t *testing.T) {
	for _, mod := range []struct{ name, src string }{
		{"run-detail.js", runDetailJS(t)},
		{"file-list.js", fileListJS(t)},
		{"file-detail.js", fileDetailJS(t)},
		{"run-table.js", runTableJS(t)},
		{"frame-panel.js", framePanelJS(t)},
		{"compare-dialog.js", compareDialogJS(t)},
	} {
		live := liveJS(t, mod.src)

		// A method sits at exactly two spaces of indent, which is the same
		// convention jsMethod relies on. Getters count: they are prototype
		// properties too, and a field of the same name shadows one just as
		// completely.
		methods := map[string]bool{}
		for _, m := range regexp.MustCompile(`(?m)^  (?:async |get |set )?(\w+)\(`).
			FindAllStringSubmatch(live, -1) {
			methods[m[1]] = true
		}
		if len(methods) == 0 {
			t.Errorf("%s: no methods were found, so this scan verified nothing", mod.name)
			continue
		}

		// `this.x = ` only: `this.x.y = ` writes THROUGH a field and does not
		// create one, so it cannot shadow anything.
		for _, m := range regexp.MustCompile(`this\.(\w+) = `).FindAllStringSubmatch(live, -1) {
			if methods[m[1]] {
				t.Errorf("%s assigns this.%s, and %s() is also a method on the same class. The "+
					"instance field wins over the prototype from the moment it is set, so the "+
					"method becomes unreachable - silently, and at both call sites the code reads "+
					"exactly as intended", mod.name, m[1], m[1])
			}
		}
	}
}

// TestTheThreeBoundariesHoldInBothDirections is what the ticket asked to be
// stated rather than left to be inferred, as a test rather than as a
// paragraph. Each of the three is checked from both sides, because either one
// alone leaves the seam able to rot.
//
//  1. THE TABLE OWNS THE ROW, THE RUN'S DETAIL OWNS THE CELL'S CONTENT.
//     TOR-194 drew this one; runtable_test.go checks the table's side, and
//     this checks the detail's.
//  2. THE RUN'S DETAIL OWNS WHAT THE ROW SAYS ABOUT THE RUN, THE LIST OWNS
//     WHAT THE TORRENT HOLDS. The offer to run again is the run's; every row,
//     tick, price and verdict is the list's.
//  3. THE LIST OWNS THE ROW, THE FILE'S DETAIL OWNS WHAT THE ROW OPENS ONTO.
//     The same shape as 1, one level down - and with the same single
//     exception, in the same place: the disclosure's aria-controls names an
//     id only the detail can mint.
func TestTheThreeBoundariesHoldInBothDirections(t *testing.T) {
	page := liveJS(t, appJS(t))
	detail := liveJS(t, runDetailJS(t))
	list := liveJS(t, fileListJS(t))
	file := liveJS(t, fileDetailJS(t))

	// 1. app.js holds no detail DOM at all any more. The entry gained one
	// field and lost twenty-two, and the one reader that used to reach past
	// this line (loadDefaults, hiding a button two levels in) asks instead.
	// The class names are looked for QUOTED, because app.js legitimately names
	// two of the modules in its own import statements ("./file-detail.js"),
	// and a bare substring would read an import as a renderer.
	for _, forbidden := range []string{
		"detailBadge", "detailTitle", "detailCancel", "detailError",
		"torrentSummary", "torrentActions", "torrentSave", "torrentSend", "torrentNote",
		"againEl", "againLine", "againGo", "againRetry", "againCost", "againNote",
		"pickerEl", "pickerTitle", "pickerList", "pickerAll", "pickerArmed", "pickerRows",
		`"picker-item"`, `"picker-file"`, `".file-detail"`, "run-detail-header", ".run-detail",
	} {
		if strings.Contains(page, forbidden) {
			t.Errorf("app.js names %q, which belongs to one of the three detail elements. What is "+
				"left in this file is the intake, the requests, the log and the wiring - a "+
				"renderer here would be a second place the detail is drawn", forbidden)
		}
	}
	if !strings.Contains(page, "entry.detail.syncDetail();") {
		t.Error("app.js's syncEntry does not ask the detail to redraw itself - the two halves of " +
			"one redraw are the row's and the detail's, in that order, and the order lives here")
	}

	// 1, the other way: the run's detail writes no row element. Every field
	// the table contributes to the entry is a row element, so naming one here
	// would mean two files writing one cell.
	for _, forbidden := range []string{
		"rowEl", "rowBadge", "rowName", "rowMeta", "rowProgress", "rowWhen", "rowCancel",
		"rowPeers", "rowSeeds", "rowDown", "rowUp", "rowAvail", "rowQueue", "rowRaise",
		"rowLower", "detailRowEl", "detailCell", "setRunExpanded", "reorderRuns",
	} {
		if strings.Contains(detail, forbidden) {
			t.Errorf("run-detail.js names %q, which is the TABLE's. The detail is handed a cell and "+
				"knows nothing about the row it hangs under - not even whether it is on screen, "+
				"which is what lets several be open at once", forbidden)
		}
	}

	// 2. The run's detail draws nothing about a file. It creates the list,
	// calls four methods on it, and never learns what went in.
	for _, forbidden := range []string{
		"picker-item", "picker-file", "picker-cost", "picker-clear", "fileEntries",
		"tickFile", "updateFileCosts", "framesOnDisk", "untickedVideos",
	} {
		if strings.Contains(detail, forbidden) {
			t.Errorf("run-detail.js names %q, which is the LIST's. Everything about which files a "+
				"torrent holds, what a tick spends and what an un-tick would do is one level in",
				forbidden)
		}
	}
	// 2, the other way: the list draws nothing about the run's own verbs.
	for _, forbidden := range []string{
		"torrent-save", "torrent-send", "run-again", "run-detail-cancel", "topUpRun",
		"retryRun", "refreshAgain", "renderTorrent", "summaryLine",
	} {
		if strings.Contains(list, forbidden) {
			t.Errorf("file-list.js names %q, which is the RUN DETAIL's. The offer to run again is "+
				"one per run and the .torrent is a property of the torrent; a list that drew "+
				"either would draw it once per file", forbidden)
		}
	}

	// 3. The list builds every row and hands the file's detail exactly four
	// row elements - the ones that are that file's title line since TOR-182 -
	// and nothing else about a file.
	for _, forbidden := range []string{
		"meta-toggle", "meta-body", `"specs"`, `"tracks"`, "file-regen", "file-compare",
		"reach-strip", "avail-swarm", `"grid"`, "detailFrame", "frameFigure", "loadFileDetail",
	} {
		if strings.Contains(list, forbidden) {
			t.Errorf("file-list.js names %q, which is one FILE's detail. The list owns the row; "+
				"what the row opens onto is the element it mounts there", forbidden)
		}
	}
	// 3, the other way: the file's detail writes exactly the four row
	// elements it was handed, and nothing else on the row.
	handed := map[string]bool{"item": true, "rowToggle": true, "name": true, "summary": true}
	for _, m := range regexp.MustCompile(`this\.(\w+) = row\.(\w+);`).FindAllStringSubmatch(file, -1) {
		if !handed[m[1]] {
			t.Errorf("file-detail.js takes row.%s as this.%s - four row elements cross this "+
				"boundary (the <li>, the disclosure, the name and the summary, because the row IS "+
				"the file's title line since TOR-182) and a fifth needs the reasoning re-made, "+
				"not just the assignment added", m[2], m[1])
		}
	}
	if n := len(regexp.MustCompile(`this\.\w+ = row\.\w+;`).FindAllString(file, -1)); n != 4 {
		t.Errorf("file-detail.js takes %d row elements, want 4 - if one genuinely went, the "+
			"boundary paragraph in its own header should go with it", n)
	}
	for _, forbidden := range []string{
		"picker-cost", "picker-state", "picker-clear", "picker-note", "picker-mark",
		"this.rows", "pickerList", "tickFile", "clearFrames", "startFiles",
	} {
		if strings.Contains(file, forbidden) {
			t.Errorf("file-detail.js names %q, which is the ROW's. A file's detail is handed the "+
				"four elements of its row it must keep current and reaches for nothing else - "+
				"otherwise two elements write one row", forbidden)
		}
	}

	// AND THE ONE EXCEPTION, twice, in the same place at both levels: the
	// control that opens a region sets aria-expanded (its own state) and the
	// side that MINTED the region's id sets aria-controls.
	if !strings.Contains(page, `rowParts.rowToggle.setAttribute("aria-controls", detail.regionId);`) {
		t.Error("app.js does not name the run detail's region on the row's toggle - a disclosure " +
			"that names nothing leaves the region it opens unannounced")
	}
	if !strings.Contains(list, `open.setAttribute("aria-controls", block.regionId)`) {
		t.Error("file-list.js does not name the file detail's region on the row's disclosure - " +
			"same rule, one level down")
	}
	if !strings.Contains(file, `this.rowToggle.setAttribute("aria-expanded", String(expanded));`) {
		t.Error("file-detail.js does not write the row disclosure's aria-expanded - it travels " +
			"with the expanded state, which is this element's, and the id travels with the " +
			"region, which is the list's to name")
	}
}

// TestTheDetailIdsComeFromOneCounter is the invariant three prefixes in three
// modules would otherwise each guarantee only for themselves.
//
// There are as many detail regions as there are torrents, as many again as
// there are video files inside them, and one id per tick box on top - and all
// of them have to be unique ACROSS THE DOCUMENT, because a disclosure names
// its region and a <label> names its control by id. app.js held one counter
// for all three prefixes before this ticket; the counter is state.js's now,
// which is the only place all three modules can reach without importing one
// another.
func TestTheDetailIdsComeFromOneCounter(t *testing.T) {
	derive := liveJS(t, stateJS(t))
	if n := strings.Count(derive, "let detailSeq = 0;"); n != 1 {
		t.Fatalf("state.js declares the id counter %d times, want exactly 1 - two counters each "+
			"guarantee uniqueness only within their own prefix", n)
	}
	if !strings.Contains(derive, "return prefix + ++detailSeq;") {
		t.Error("state.js's nextDetailId does not increment the one counter - an id handed out " +
			"twice is a label pointing at another row's checkbox")
	}

	for _, want := range []struct{ mod, src, call string }{
		{"run-detail.js", runDetailJS(t), `nextDetailId("run-detail-")`},
		{"file-list.js", fileListJS(t), `nextDetailId("file-tick-")`},
		{"file-detail.js", fileDetailJS(t), `nextDetailId("file-detail-")`},
	} {
		live := liveJS(t, want.src)
		if !strings.Contains(live, want.call) {
			t.Errorf("%s does not mint its ids through %s - the shared counter is what makes "+
				"three prefixes in three files safe", want.mod, want.call)
		}
		// And it keeps no counter of its own, which is the way this could
		// silently come apart: a local `++seq` reads identically at the call
		// site and collides across modules the moment two prefixes match.
		if regexp.MustCompile(`(?m)^let \w*[Ss]eq\b`).MatchString(live) {
			t.Errorf("%s declares an id counter of its own. Three counters cannot see each "+
				"other, and the first two prefixes to be spelled the same way would hand out "+
				"the same id", want.mod)
		}
	}
}

// TestTheThreeWrappersAreBlockAndDoNotBeatTheHiddenAttribute is the CSS half
// of the move, and the reason it needs a guard is that both halves of it fail
// SILENTLY in opposite directions.
//
// An unknown element is display: inline by default, so wrapping a block-level
// <section> or <div> in one puts a block inside an inline box - which is what
// table.css's `run-table { display: block; }` already exists to prevent one
// level up (see that rule's own comment for why display: contents was not
// used). Without these three lines the whole detail tree lays out wrong, in a
// way no text check on the JS could see.
//
// The other direction is this project's most expensive recurring trap, paid
// for six times in filelist.css alone: a TYPE selector declaring display beats
// the UA's own `[hidden] { display: none }`, so the moment somebody moves the
// `hidden` attribute from the wrapped element onto the wrapper - which is the
// obvious simplification to try - the list can no longer be hidden and a
// file's detail can no longer close. The [hidden] companions make that
// impossible rather than unlikely, which is exactly why they are kept while
// being redundant today.
func TestTheThreeWrappersAreBlockAndDoNotBeatTheHiddenAttribute(t *testing.T) {
	css := stylesheet(t)
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")

	for _, want := range []struct {
		tag       string
		companion bool
		wraps     string
	}{
		// The run's detail wraps a .run-detail div that is never hidden -
		// collapse at that level is the `hidden` attribute on the
		// .run-detail-row <tr> above it, a different element entirely - so it
		// needs no companion, exactly as <run-table> does not.
		{"run-detail", false, "a position: sticky .run-detail whose width resolves against it"},
		// These two wrap elements that ARE hidden by attribute.
		{"file-list", true, ".picker, which syncFileList hides when there is nothing to list"},
		{"file-detail", true, ".file-detail, whose `hidden` is the whole of a file's collapse"},
	} {
		// ANCHORED AT THE START OF A LINE, which the first draft of this
		// guard was not and a mutation run found: `.file-detail[hidden] {
		// display: none; }` - the CLASS rule, which has always been there -
		// contains the type rule's whole text as a substring, so deleting the
		// type rule left this green. A TYPE selector and a class selector
		// differing by one leading character is exactly the pair where a
		// substring check answers the wrong question.
		block := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(want.tag) + ` \{ display: block; \}`)
		if !block.MatchString(live) {
			t.Errorf("no `%s { display: block; }` rule ships (as a TYPE selector, at the start of a "+
				"line). An unknown element is display: inline, so this wrapper would put %s inside "+
				"an inline box - and nothing about the JS would look wrong", want.tag, want.wraps)
		}
		none := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(want.tag) + `\[hidden\] \{ display: none; \}`)
		got := none.MatchString(live)
		if want.companion && !got {
			t.Errorf("`%s` declares display with no [hidden] companion. A type selector beats the "+
				"UA's own [hidden] rule, so the first ticket to move `hidden` onto the wrapper "+
				"instead of %s gets something that cannot be hidden at all - the trap "+
				"filelist.css has now paid for six times", want.tag, want.wraps)
		}
		if !want.companion && got {
			t.Errorf("`%s[hidden]` has a companion rule but nothing ever hides that wrapper. If "+
				"something now does, the reasoning about which element carries collapse at this "+
				"level has to be re-made rather than the rule quietly added", want.tag)
		}
	}
}
