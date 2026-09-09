package web

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE ACCORDION AS ITS OWN COMPONENT (TOR-212).
//
// The page has THREE disclosures, not one, and they nest: a torrent's line
// opens onto its detail, a video file's row opens onto its block inside that
// detail, and the Metadata header opens onto the specs inside the block. All
// three did the same three DOM writes, in three places, in three orders.
//
// accordion.js is those three writes and nothing else. What these tests are
// for is the "nothing else", because that is the part a shared component makes
// easy to get wrong:
//
//   - it must not hold the open state (the entry does, and the table MOVES
//     rows on every re-sort);
//   - it must not swallow what opening MEANS at the run level -
//     syncRunDetailWidth (TOR-174) and detailShown (TOR-152), the second of
//     which had NO guard on its call site at all before this ticket;
//   - it must not impose the FILE level's "one open at a time" (TOR-182) on
//     the RUN level, where the opposite was decided for three written reasons.
//
// TestTheDisclosureFiresTheRunLevelsSideEffects below RUNS the shipped
// run-table.js under node rather than reading it, which is what the ticket
// asks for - and which turned out to matter more than the ticket knew: with
// detailShown never called at all, every other test in this package still
// passes. That test's own heading carries the three mutations.

// TestOneComponentServesAllThreeDisclosures is criterion 1, and it is a claim
// about the three call sites rather than about the class: three functions that
// each wrote the same attributes now each make one call, and no other module
// writes those attributes at all.
func TestOneComponentServesAllThreeDisclosures(t *testing.T) {
	acc := liveJS(t, accordionJS(t))
	table := liveJS(t, runTableJS(t))
	file := liveJS(t, fileDetailJS(t))

	// The three call sites, each applying through its own accordion. The
	// LEVEL is checked with them, because a component serving three levels
	// through one level number would be one call site wearing three hats.
	for _, site := range []struct {
		module, method, call, level, what string
		src                               string
	}{
		{"run-table.js", "setRunExpanded", "entry.rowAccordion.apply(expanded);", "level: 1,",
			"a torrent's line -> its detail row", table},
		{"file-detail.js", "setFileExpanded", "this.fileAccordion.apply(expanded);", "level: 2,",
			"a video file's block", file},
		{"file-detail.js", "setMetaExpanded", "this.metaAccordion.apply(expanded);", "level: 3,",
			"the Metadata sub-block inside it", file},
	} {
		body := jsMethod(t, site.src, site.method)
		if !strings.Contains(body, site.call) {
			t.Errorf("%s's %s (%s) does not apply through the shared disclosure - it wants "+
				"%q. Three call sites collapsing into one is this ticket's whole claim",
				site.module, site.method, site.what, site.call)
		}
		if !strings.Contains(site.src, site.level) {
			t.Errorf("%s never builds a disclosure at %q, so %s is not the level the UI "+
				"actually has there", site.module, site.level, site.what)
		}
	}

	// AND NOBODY ELSE WRITES THE THREE ATTRIBUTES. This is the half that makes
	// "one component" true rather than merely available: a fourth place
	// writing aria-expanded on a disclosure, or hiding a region by hand, would
	// be a second implementation nothing keeps in step.
	//
	// Every served module is read, derived rather than listed (servedModules),
	// so a module added later cannot quietly become the fourth writer.
	//
	// The two exceptions are stated rather than pattern-matched. newRow's
	// `main.setAttribute("aria-expanded", "false")` starts a fresh row closed
	// before any entry exists to apply against, and file-list.js's
	// `open.setAttribute("aria-expanded", "false")` does the same one level
	// down; both are initial values on an element being created, not a
	// disclosure changing state.
	initial := map[string]int{"run-table.js": 1, "file-list.js": 1}
	writer := regexp.MustCompile(`setAttribute\("aria-expanded"`)
	for _, name := range servedModules(t) {
		if name == "accordion.js" {
			continue
		}
		src, err := embedded.ReadFile("assets/" + name)
		if err != nil {
			t.Fatalf("reading the embedded %s: %v", name, err)
		}
		got := len(writer.FindAllString(liveJS(t, string(src)), -1))
		if got != initial[name] {
			t.Errorf("%s writes aria-expanded %d time(s), want %d. Since TOR-212 the only "+
				"writer is accordion.js's apply; the allowance here is for starting a "+
				"newly created control closed, before there is any state to apply",
				name, got, initial[name])
		}
	}
	if n := len(writer.FindAllString(liveJS(t, accordionJS(t)), -1)); n != 1 {
		t.Errorf("accordion.js writes aria-expanded %d times, want exactly 1 - apply() is "+
			"the one writer, and two of them inside the component would be the same "+
			"duplication one level in", n)
	}

	// The three levels, and no fourth. A level is a real depth in the UI, not
	// a scale to extend on spec: adding one would mean the page grew a fourth
	// nested disclosure, and that is a design decision rather than an
	// argument value.
	levels := regexp.MustCompile(`\[(\d+), \{ name: "(\w+)"`).FindAllStringSubmatch(acc, -1)
	if len(levels) != 3 {
		t.Fatalf("accordion.js declares %d levels, want 3 - the UI has exactly three "+
			"nested disclosures (a torrent's line, a file's block, its Metadata block) "+
			"and the parameter's values are those", len(levels))
	}
	for i, want := range []struct{ n, name string }{{"1", "run"}, {"2", "file"}, {"3", "meta"}} {
		if levels[i][1] != want.n || levels[i][2] != want.name {
			t.Errorf("level %d is %s/%q, want %s/%q", i+1, levels[i][1], levels[i][2],
				want.n, want.name)
		}
	}
}

// TestTheDisclosureHoldsNoOpenState is criterion 2's static half: the class
// has no field the state could hide in, and its one method takes the state as
// an argument.
//
// WHY IT MATTERS MORE HERE THAN IT WOULD ELSEWHERE, and the reason is not the
// same at the three levels - which is worth stating because the obvious
// version of it is wrong about the outer one:
//
//   - AT THE RUN LEVEL the instance survives. newRow() builds it once and
//     reorderRuns()'s move (which relocates a row's whole element on every
//     redraw) leaves the object alone. What makes a copy of the flag wrong
//     here is that entry.expanded is what the PAGE reads - app.js's toggleRun
//     computes `!entry.expanded` to decide what a click means - so a copy
//     would be a second answer to one question.
//   - AT THE FILE AND METADATA LEVELS the instance IS destroyed and rebuilt:
//     file-list.js's renderFileList replaces every row with replaceChildren
//     and mints a fresh <file-detail>, whose mount() builds fresh disclosures
//     that never see any flag. Anything remembered there comes back closed
//     under a person who had opened it.
//
// state.js's own note says the shallower version: the singleton detail pane
// went because "the accordion has no single slot to be the one thing in".
//
// The executed half is in TestTheDisclosureSurvivesTheMoveARe_sortMakes below,
// and the browser half in docs/front-end.md's TOR-212 section.
func TestTheDisclosureHoldsNoOpenState(t *testing.T) {
	acc := liveJS(t, accordionJS(t))

	// Every field the class assigns, enumerated rather than spot-checked -
	// the property is a negative one, so a check for a particular forbidden
	// name would miss the next way of spelling it.
	fields := map[string]bool{}
	for _, m := range regexp.MustCompile(`this\.(\w+) = `).FindAllStringSubmatch(acc, -1) {
		fields[m[1]] = true
	}
	if len(fields) == 0 {
		t.Fatal("accordion.js assigns no fields at all, so this scan verified nothing")
	}
	// The five parts it is handed. Anything else is a field somebody added,
	// and the question to answer is whether it remembers something.
	for _, allowed := range []string{"level", "toggle", "region", "mark", "dressed"} {
		delete(fields, allowed)
	}
	for name := range fields {
		t.Errorf("accordion.js keeps a field this.%s beyond the five parts it is handed. "+
			"If it holds the open state, two things break: entry.expanded stops being "+
			"the one answer the page reads (app.js's toggleRun computes !entry.expanded), "+
			"and a file-list rebuild throws the inner two disclosures away and builds "+
			"fresh ones that never saw the flag. The owner holds the state "+
			"(entry.expanded, fentry.expanded, fentry.metaExpanded); this writes it out",
			name)
	}

	// apply() takes it as an argument and reads no field for it. `expanded`
	// must be the parameter, not a lookup - `apply()` reading this.expanded
	// would pass every check above by never assigning it.
	apply := jsMethod(t, acc, "apply")
	if !strings.HasPrefix(strings.TrimSpace(apply), "apply(expanded) {") {
		t.Errorf("accordion.js's apply does not take the state as its argument: %q",
			strings.SplitN(strings.TrimSpace(apply), "\n", 2)[0])
	}
	for _, forbidden := range []string{"this.expanded", "this.open", "this.state",
		`getAttribute("aria-expanded")`, "this.region.hidden ===", "!this.region.hidden"} {
		if strings.Contains(acc, forbidden) {
			t.Errorf("accordion.js names %q - reading the state back, from a field or from "+
				"the DOM, is the same mistake as keeping it: the record on the entry is "+
				"the answer and this component is told it", forbidden)
		}
	}

	// And the three owners still hold it. Each of the three call sites writes
	// its own record, and that write is what the poll reads back.
	for _, owner := range []struct{ module, want, src string }{
		{"run-table.js", "entry.expanded = expanded;", liveJS(t, runTableJS(t))},
		{"file-detail.js", "fentry.expanded = expanded;", liveJS(t, fileDetailJS(t))},
		{"file-detail.js", "this.fentry.metaExpanded = expanded;", liveJS(t, fileDetailJS(t))},
	} {
		if !strings.Contains(owner.src, owner.want) {
			t.Errorf("%s no longer records %q. The open state has to live on the record, "+
				"because the DOM the component writes is moved and re-created under it",
				owner.module, owner.want)
		}
	}
}

// TestTheRunLevelsSideEffectsDidNotMigrate is criterion 3's static half, and
// it guards the direction the refactor made easy: a generic disclosure is
// exactly where a run-specific side effect would look tidy.
func TestTheRunLevelsSideEffectsDidNotMigrate(t *testing.T) {
	acc := liveJS(t, accordionJS(t))
	for _, forbidden := range []string{"syncRunDetailWidth", "detailShown", "run-detail-w",
		"clientWidth", "refreshAgain", "loadFileDetail"} {
		if strings.Contains(acc, forbidden) {
			t.Errorf("accordion.js names %q in code. Opening a RUN's detail rechecks the "+
				"pane's width (TOR-174) and reads that run's top-up standing off disk "+
				"(TOR-152); opening a FILE's block reads its other result sets. None of "+
				"those is what opening means in general, and a component that did them "+
				"would do them for the Metadata block too", forbidden)
		}
	}

	// ONE DOOR, so "every path that opens a run's detail" is a claim about one
	// function rather than about however many callers it has. Counted over
	// every served module: app.js reaches this three times (toggleRun's two
	// branches and began()), and none of them may write the state itself.
	//
	// A LEFT BOUNDARY ON `entry.expanded`, and it is not pedantry: without it
	// the pattern matches inside `fEntry.expanded`, which is the FILE level's
	// own record - so the check failed on file-detail.js doing exactly what it
	// is supposed to, and the tempting fix is to drop file-detail.js from the
	// sweep, which would drop the one module most able to grow a second door.
	door := regexp.MustCompile(`(?:^|[^A-Za-z0-9_.])(entry\.expanded\s*=|rowAccordion\.apply\()`)
	for _, name := range servedModules(t) {
		if name == "run-table.js" {
			continue
		}
		src, err := embedded.ReadFile("assets/" + name)
		if err != nil {
			t.Fatalf("reading the embedded %s: %v", name, err)
		}
		for _, m := range door.FindAllStringSubmatch(liveJS(t, string(src)), -1) {
			t.Errorf("%s contains %q - a second door onto a run's detail is a path "+
				"that opens one WITHOUT the width recheck and the top-up read, which "+
				"is the bug TOR-152's guard exists for", name, m[1])
		}
	}
	// And the pattern still finds the one real door, or the sweep above is
	// checking a regex that stopped matching anything.
	if !door.MatchString(liveJS(t, runTableJS(t))) {
		t.Error("the one-door pattern no longer matches run-table.js itself, so the sweep " +
			"over every other module verified nothing")
	}
	table := liveJS(t, runTableJS(t))
	if n := strings.Count(table, "detailShown("); n != 1 {
		t.Errorf("run-table.js calls detailShown %d times, want 1 - it belongs in the one "+
			"function that may put a detail on screen, and nowhere else", n)
	}
}

// accordionDriver runs the SHIPPED front end under node with a DOM small
// enough to fit in this file, and reports what actually happened rather than
// what the source says.
//
// WHAT IS REAL HERE: state.js, accordion.js and run-table.js are the embedded
// files, unmodified. newRow() builds a row through the real code path,
// setRunExpanded is the real method, and the width recheck is observed at its
// FAR END - the custom property it writes on :root - rather than by wrapping
// the method, so a syncRunDetailWidth() that ran and did nothing would not
// pass. detailShown is the real injected service, counted by the page's side
// of the seam.
//
// WHAT IS NOT REAL: the DOM. These fake nodes hold attributes and re-parent on
// append, which is enough for the three writes and for the relocation
// reorderRuns performs - and not enough to show what a browser shows about
// custom elements being disconnected and reconnected by that move. That is why
// docs/front-end.md's TOR-212 section carries the browser measurement too.
// This harness's job is that the calls happen, on every direction, every time.
const accordionDriver = `
// Before any import: run-table.js declares ` + "`class RunTable extends HTMLElement`" + ` at
// module scope, so the base class has to exist when the module is evaluated.
globalThis.HTMLElement = class {};
globalThis.customElements = { define() {} };

// Every write of --run-detail-w, which is the whole observable effect of
// syncRunDetailWidth (detail.css's .run-detail reads exactly that token).
const widths = [];
globalThis.document = {
  createElement: (tag) => node(tag),
  documentElement: { style: { setProperty(name, value) {
    if (name === "--run-detail-w") widths.push(value);
  } } },
};
globalThis.window = { addEventListener() {}, removeEventListener() {} };
globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };

function node(tag) {
  const classes = new Set();
  const listeners = new Map();
  const el = {
    tag,
    children: [],
    parent: null,
    attrs: {},
    dataset: {},
    style: {},
    hidden: false,
    textContent: "",
    title: "",
    type: "",
    get className() { return [...classes].join(" "); },
    set className(v) {
      classes.clear();
      for (const c of String(v).split(/\s+/)) if (c) classes.add(c);
    },
    classList: {
      add(...cs) { for (const c of cs) classes.add(c); },
      contains(c) { return classes.has(c); },
    },
    setAttribute(name, value) { el.attrs[name] = String(value); },
    getAttribute(name) { return name in el.attrs ? el.attrs[name] : null; },
    // Relocating, exactly as the real append does: this is what reorderRuns
    // relies on ("appendChild on a node already in the table just relocates
    // it"), and a shim that duplicated instead would make the move test lie.
    append(...kids) {
      for (const k of kids) {
        if (k.parent) k.parent.children = k.parent.children.filter((x) => x !== k);
        k.parent = el;
        el.children.push(k);
      }
    },
    addEventListener(type, fn) {
      if (!listeners.has(type)) listeners.set(type, []);
      listeners.get(type).push(fn);
    },
    fire(type, event) {
      for (const fn of listeners.get(type) || []) fn(event || {});
    },
    querySelector() { return null; },
  };
  return el;
}

const { RunTable, setServices } = await import("./run-table.js");
const { newRunState } = await import("./state.js");
const { Accordion } = await import("./accordion.js");

// The page's side of the seam, counted. detailShown is app.js's
// entry.detail.refreshAgain() in the product; what matters here is that the
// table calls it, with the entry, on the paths it is supposed to.
const shown = [];
setServices({
  toggleRun: () => {},
  cancelRun: () => {},
  setPriority: () => {},
  detailShown: (entry) => shown.push(entry.id),
});

// wire() is not called - it needs index.html's markup - so the three parts
// setRunExpanded and newRow reach for are set directly. this.wrap's
// clientWidth is TOR-168's usual 1168px; a zero would make
// syncRunDetailWidth bail before writing, which is a real branch and not the
// one under test.
const table = Object.create(RunTable.prototype);
table.wrap = { clientWidth: 1168 };
table.list = node("div");
table.emptyNote = node("p");

const parts = table.newRow();
const entry = { ...newRunState("r1"), ...parts };

const look = () => ({
  ariaExpanded: entry.rowToggle.getAttribute("aria-expanded"),
  detailHidden: entry.detailRowEl.hidden,
  rowDressed: entry.rowEl.dataset.expanded,
  entryExpanded: entry.expanded,
  // Where the row's element sits in the list, so a move is visible.
  index: table.list.children.indexOf(entry.rowGroupEl),
});

const steps = [];
const step = (name) => steps.push({
  name, widths: widths.length, shown: shown.length, ...look(),
});

step("built");
table.setRunExpanded(entry, true);
step("opened");
table.setRunExpanded(entry, false);
step("closed");
table.setRunExpanded(entry, true);
step("reopened");

// THE MOVE A RE-SORT MAKES. reorderRuns' whole body is
// ` + "`for (const entry of rows) this.list.append(entry.rowGroupEl);`" + `, so this is
// that, for a list of one - and a second row is appended first so the
// relocation is a real change of position rather than a no-op.
const other = table.newRow();
table.list.append(entry.rowGroupEl);
step("moved");

// A SECOND ACCORDION OVER THE SAME ELEMENTS, which is what a component that
// remembered would be rebuilt as. It must find the row exactly as it was:
// nothing in the constructor may reset or re-derive the open state.
const rebuilt = new Accordion({
  level: 1,
  toggle: entry.rowToggle,
  region: entry.detailRowEl,
  mark: entry.rowToggle.children[0],
  dressed: entry.rowEl,
});
step("reconstructed");

// What the level parameter puts in the DOM, and what it refuses.
const refusals = {};
const refuse = (name, build) => {
  try { build(); refusals[name] = ""; }
  catch (err) { refusals[name] = String(err.message); }
};
refuse("level0", () => new Accordion({ level: 0, toggle: node("button"), region: node("div") }));
refuse("level4", () => new Accordion({ level: 4, toggle: node("button"), region: node("div") }));
refuse("noRegion", () => new Accordion({ level: 3, toggle: node("button") }));
refuse("runUndressed", () => new Accordion({ level: 1, toggle: node("button"), region: node("div") }));
refuse("metaDressed", () => new Accordion({
  level: 3, toggle: node("button"), region: node("div"), dressed: node("li"),
}));
refuse("metaPlain", () => new Accordion({ level: 3, toggle: node("button"), region: node("div") }));

process.stdout.write(JSON.stringify({
  steps,
  refusals,
  levels: {
    toggle: entry.rowToggle.dataset.accordionLevel,
    region: entry.detailRowEl.dataset.accordionLevel,
  },
  markClassed: entry.rowToggle.children.some((c) => c.classList.contains("disclosure-mark")),
  // The five own properties, read off a live instance rather than off the
  // source: a field assigned conditionally would be invisible to a text scan.
  ownFields: Object.keys(entry.rowAccordion).sort(),
  // Both rows exist, so the move above was a real reorder.
  rows: table.list.children.length,
  otherIsRow: other.rowGroupEl === table.list.children[0],
}));
`

type accordionStep struct {
	Name          string `json:"name"`
	Widths        int    `json:"widths"`
	Shown         int    `json:"shown"`
	AriaExpanded  string `json:"ariaExpanded"`
	DetailHidden  bool   `json:"detailHidden"`
	RowDressed    string `json:"rowDressed"`
	EntryExpanded bool   `json:"entryExpanded"`
	Index         int    `json:"index"`
}

type accordionResult struct {
	Steps    []accordionStep   `json:"steps"`
	Refusals map[string]string `json:"refusals"`
	Levels   struct {
		Toggle string `json:"toggle"`
		Region string `json:"region"`
	} `json:"levels"`
	MarkClassed bool     `json:"markClassed"`
	OwnFields   []string `json:"ownFields"`
	Rows        int      `json:"rows"`
	OtherIsRow  bool     `json:"otherIsRow"`
}

// runAccordionDriver copies the three shipped modules into a temp directory
// beside a package.json declaring {"type":"module"} - node reads a bare .js as
// CommonJS otherwise, and the shipped files must not be renamed or edited to
// be testable - and returns what the driver saw.
//
// Any node failure is reported with the FULL stdout and stderr, never a tail
// of either: a truncated diagnostic hides exactly the harness failures this
// shape produces.
func runAccordionDriver(t *testing.T) accordionResult {
	t.Helper()
	node := requireNode(t)

	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s into the harness directory: %v", name, err)
		}
	}
	write("package.json", `{"type":"module"}`)
	write("state.js", stateJS(t))
	write("accordion.js", accordionJS(t))
	write("run-table.js", runTableJS(t))
	write("driver.js", accordionDriver)

	cmd := exec.Command(node, filepath.Join(dir, "driver.js"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node exited with an error running the shipped run table: %v\n"+
			"--- stderr ---\n%s\n--- stdout ---\n%s", err, stderr.String(), stdout.String())
	}

	var out accordionResult
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("node's stdout was not the JSON this harness expects: %v\n"+
			"--- stdout ---\n%s\n--- stderr ---\n%s", err, stdout.String(), stderr.String())
	}
	return out
}

// TestTheDisclosureFiresTheRunLevelsSideEffects is criterion 3, BY TEST rather
// than by reading - and what the "rather than" is worth was measured rather
// than assumed. Three mutations, each run against the whole package:
//
//	delete this.syncRunDetailWidth() from setRunExpanded
//	  -> rundetailwidth_test.go catches it. A real guard.
//	rename the property it writes to --run-detail-width
//	  -> rundetailwidth_test.go PASSES. Its check is
//	     Contains(fn, "--run-detail-w"), which the longer name satisfies.
//	     This test fails: 0 writes where 1, 2, 3 were expected.
//	never call detailShown at all (`if (false) detailShown(entry)`)
//	  -> THE ENTIRE PRE-EXISTING SUITE PASSES. Nothing checked the call
//	     site. runtable_test.go checks that the service is DECLARED in
//	     SERVICES and INJECTED by app.js, which is a different claim - a
//	     service can be wired perfectly and never called.
//
// So TOR-152's call was the least guarded thing in this file, not the
// best-guarded, and "the guard exists because it was once forgotten" was true
// of the wiring rather than of the call. Consolidating three disclosures into
// one component is exactly the refactor that would have dropped it silently.
//
// Both effects are observed at their far end. The width is the custom property
// on :root, so a syncRunDetailWidth() that ran and wrote nothing fails here;
// detailShown is the injected service itself, so the table has to reach the
// page's own seam and hand it the entry.
func TestTheDisclosureFiresTheRunLevelsSideEffects(t *testing.T) {
	got := runAccordionDriver(t)

	steps := map[string]accordionStep{}
	for _, s := range got.Steps {
		steps[s.Name] = s
	}
	for _, name := range []string{"built", "opened", "closed", "reopened"} {
		if _, ok := steps[name]; !ok {
			t.Fatalf("the driver reported no %q step; it recorded %v", name, got.Steps)
		}
	}

	// The row starts closed and neither effect has fired: without this the
	// counts below could be measuring the row being BUILT.
	if b := steps["built"]; b.Widths != 0 || b.Shown != 0 {
		t.Errorf("building a row already wrote the width %d time(s) and showed a detail "+
			"%d time(s); both should be 0, and every count below is relative to this",
			b.Widths, b.Shown)
	}

	// TOR-174: EITHER DIRECTION. A detail coming on OR off screen changes the
	// page's height, which can add or remove the document's scrollbar and with
	// it a few pixels of the pane's width - so a close has to recheck too, and
	// that is the half a "does it fire on open" test would miss.
	for _, want := range []struct {
		step  string
		width int
	}{{"opened", 1}, {"closed", 2}, {"reopened", 3}} {
		if got := steps[want.step].Widths; got != want.width {
			t.Errorf("after %s, --run-detail-w had been written %d time(s), want %d. "+
				"TOR-174: opening AND closing a row can add or remove the document's own "+
				"scrollbar, and with it a few pixels of the width every open detail is "+
				"sized from", want.step, got, want.width)
		}
	}

	// TOR-152: ON OPENING ONLY, on every opening, and never on a close. The
	// standing being read is "how much would finishing this run cost", and it
	// is worth a disk read the moment a row comes on screen - a row going off
	// screen asks nothing.
	for _, want := range []struct {
		step  string
		shown int
	}{{"opened", 1}, {"closed", 1}, {"reopened", 2}} {
		if got := steps[want.step].Shown; got != want.shown {
			t.Errorf("after %s, detailShown had been called %d time(s), want %d. TOR-152: "+
				"a row opening is when its top-up standing is worth reading off disk, and "+
				"that guard exists because the call was once forgotten", want.step, got,
				want.shown)
		}
	}

	// And the disclosure itself did its three writes, both ways, so the two
	// effects above are not being counted for a row that never opened.
	for _, want := range []struct {
		step, aria, dressed string
		hidden, expanded    bool
	}{
		{"built", "false", "", true, false},
		{"opened", "true", "true", false, true},
		{"closed", "false", "false", true, false},
		{"reopened", "true", "true", false, true},
	} {
		s := steps[want.step]
		if s.AriaExpanded != want.aria || s.DetailHidden != want.hidden ||
			s.RowDressed != want.dressed || s.EntryExpanded != want.expanded {
			t.Errorf("after %s the row is aria-expanded=%q hidden=%v data-expanded=%q "+
				"entry.expanded=%v; want %q/%v/%q/%v", want.step, s.AriaExpanded,
				s.DetailHidden, s.RowDressed, s.EntryExpanded, want.aria, want.hidden,
				want.dressed, want.expanded)
		}
	}
}

// TestTheDisclosureSurvivesTheMoveARe_sortMakes is criterion 2's executed
// half. The table re-sorts on every redraw and MOVES a row's element; the
// component must not notice.
//
// WHAT THIS SHOWS AND WHAT IT DOES NOT. The move is the real one - reorderRuns'
// body is `this.list.append(entry.rowGroupEl)` and the harness's append
// relocates the same way the DOM's does - and the second half, constructing a
// FRESH accordion over the same elements, is the shape a component rebuilt on
// reconnection would take. What a fake DOM cannot show is a browser
// disconnecting and reconnecting custom elements inside the moved subtree;
// docs/front-end.md's TOR-212 section carries that measurement, taken in
// Chrome against the running page.
func TestTheDisclosureSurvivesTheMoveARe_sortMakes(t *testing.T) {
	got := runAccordionDriver(t)

	steps := map[string]accordionStep{}
	for _, s := range got.Steps {
		steps[s.Name] = s
	}

	// The move has to have moved something, or the assertion after it passes
	// by measuring nothing. Two rows, and the one under test is no longer
	// first.
	if got.Rows != 2 || !got.OtherIsRow {
		t.Fatalf("the driver ended with %d row(s) in the list and the second row first: "+
			"%v - the relocation was not a real change of position", got.Rows, got.OtherIsRow)
	}
	if before, after := steps["reopened"].Index, steps["moved"].Index; before == after {
		t.Fatalf("the row sat at index %d before the move and %d after, so nothing moved "+
			"and this test verified nothing", before, after)
	}

	for _, name := range []string{"moved", "reconstructed"} {
		s := steps[name]
		if !s.EntryExpanded || s.AriaExpanded != "true" || s.DetailHidden || s.RowDressed != "true" {
			t.Errorf("after %s the row reads aria-expanded=%q hidden=%v data-expanded=%q "+
				"entry.expanded=%v - it was open before, and the open state lives on the "+
				"ENTRY precisely so a re-sort cannot take it away", name, s.AriaExpanded,
				s.DetailHidden, s.RowDressed, s.EntryExpanded)
		}
	}

	// The instance holds the five parts it was handed and nothing else, read
	// off a LIVE object rather than off the source - a field assigned
	// conditionally, or on first use, is invisible to a text scan.
	want := []string{"dressed", "level", "mark", "region", "toggle"}
	if strings.Join(got.OwnFields, ",") != strings.Join(want, ",") {
		t.Errorf("a live disclosure's own fields are %v, want %v. Anything else is a "+
			"place the open state could be remembered, and the table's re-sort is what "+
			"would find it", got.OwnFields, want)
	}
}

// TestTheNestingLevelIsAParameterWithThreeValues is criterion 4's mechanical
// half: the level is a real argument, it reaches the DOM, and it decides what
// the level MEANS structurally - whether there is anything to dress.
//
// What each level LOOKS like is the other half, and it is not a thing a Go
// test can measure: it is table.css's accent ground and 3px rail, filelist.css's
// 2px rail, and nothing at all one level in. docs/front-end.md's TOR-212
// section carries the per-level diff, read off the current stylesheets rather
// than designed.
func TestTheNestingLevelIsAParameterWithThreeValues(t *testing.T) {
	got := runAccordionDriver(t)

	if got.Levels.Toggle != "1" || got.Levels.Region != "1" {
		t.Errorf("a run row's disclosure wrote data-accordion-level %q on its toggle and "+
			"%q on its region, want \"1\" on both - the depth is written into the DOM so "+
			"it can be read from the page rather than inferred",
			got.Levels.Toggle, got.Levels.Region)
	}
	if !got.MarkClassed {
		t.Error("no child of the run row's toggle carries .disclosure-mark - that rule " +
			"(base.css) is the one thing all three levels draw identically, and the " +
			"component is what puts it on")
	}

	// THE REFUSALS ARE THE PARAMETER, and each one is a fact about the UI
	// rather than defensive coding:
	//
	//   - there are three levels, so 0 and 4 are not levels;
	//   - levels 1 and 2 dress a ROW (the accent ground and rail live on
	//     .run-row and .picker-item), so a disclosure there without one would
	//     open silently;
	//   - level 3 dresses NOTHING, because its rail would sit inside the
	//     file's rail inside the torrent's ground, and three nested grounds
	//     read as chrome. So a dressed element at level 3 is refused rather
	//     than ignored - ignoring it is how a level stops meaning anything.
	for _, want := range []struct{ name, needle string }{
		{"level0", "level must be 1"},
		{"level4", "level must be 1"},
		{"noRegion", "has no region"},
		{"runUndressed", "needs a dressed element"},
		{"metaDressed", "takes no dressed element"},
	} {
		msg, ok := got.Refusals[want.name]
		if !ok {
			t.Errorf("the driver did not try %s at all", want.name)
			continue
		}
		if !strings.Contains(msg, want.needle) {
			t.Errorf("%s was accepted or refused with %q, want a refusal naming %q",
				want.name, msg, want.needle)
		}
	}
	// And the one that must be ACCEPTED, or every refusal above could be the
	// constructor refusing everything.
	if msg := got.Refusals["metaPlain"]; msg != "" {
		t.Errorf("a level-3 disclosure with no dressed element was refused with %q - that "+
			"is the Metadata block, the innermost one the page actually has", msg)
	}
}
