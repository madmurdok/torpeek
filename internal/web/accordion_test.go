package web

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	// The five parts it keeps. Anything else is a field somebody added, and
	// the question to answer is whether it remembers something.
	//
	// `mark` is NOT among them and that is deliberate (TOR-222): the class is
	// added to it once in the constructor and never read back, so a field for
	// it would be a reference kept for nothing - which is the shape the scan
	// below is looking for. `container` joined them in the same ticket,
	// because a wrapper that does not keep its box cannot be said to wrap.
	for _, allowed := range []string{"level", "container", "summary", "toggle", "region"} {
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
    // The DOM's own containment, walking the same parent links append
    // maintains - and TRANSITIVE, like Node.contains, because the accordion's
    // box holds a subtree rather than two direct children (at level 2 the
    // region is inside a <file-detail> inside the <li>). A shim that only
    // compared direct parents would make every containment refusal below
    // pass for the wrong reason.
    contains(other) {
      for (let n = other; n; n = n.parent) if (n === el) return true;
      return false;
    },
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
  container: entry.rowGroupEl,
  summary: entry.rowEl,
  toggle: entry.rowToggle,
  region: entry.detailRowEl,
  mark: entry.rowToggle.children[0],
});
step("reconstructed");

// ---------------------------------------------------------------------------
// A DISCLOSURE OVER NOTHING BUT PLAIN NODES (TOR-222), which is criterion 1's
// and criterion 2's executed half in this harness: a box, a header, a button
// in the header, a region beside it. No run table, no file list, no <li>, no
// class from any stylesheet - and NO PARENT AT ALL, because the box is never
// appended anywhere.
const box = (level, tweak) => {
  const container = node("div");
  const summary = node("div");
  const toggle = node("button");
  const region = node("div");
  summary.append(toggle);
  container.append(summary, region);
  const parts = { level, container, summary, toggle, region };
  return tweak ? tweak(parts) : parts;
};

// One per level, applied both ways, and every write read back. What this
// proves is the negative the ticket asks for: no code path needs a
// particular parent, because there is no parent.
const anywhere = {};
for (const level of [1, 2, 3]) {
  const parts = box(level);
  const acc = new Accordion(parts);
  const read = () => ({
    aria: parts.toggle.getAttribute("aria-expanded"),
    hidden: parts.region.hidden,
    dressed: parts.summary.dataset.expanded === undefined
      ? null : parts.summary.dataset.expanded,
    boxLevel: parts.container.dataset.accordionLevel,
    boxName: parts.container.dataset.accordion,
    // The box carries the level and the parts inside it do NOT (TOR-222): one
    // write, on the disclosure itself, rather than one on each of two
    // elements inside it.
    toggleLevel: parts.toggle.dataset.accordionLevel === undefined
      ? null : parts.toggle.dataset.accordionLevel,
    regionLevel: parts.region.dataset.accordionLevel === undefined
      ? null : parts.region.dataset.accordionLevel,
    marked: parts.toggle.classList.contains("disclosure-mark"),
    detached: parts.container.parent === null,
  });
  acc.apply(true);
  const opened = read();
  acc.apply(false);
  const closed = read();
  // AND THE BOX MOVES. Put it inside something else entirely and apply
  // again: a component that had learned anything about where it was would
  // stop working here, and the run table moves a row's box on every redraw.
  const elsewhere = node("main");
  elsewhere.append(parts.container);
  acc.apply(true);
  anywhere[level] = { opened, closed, moved: read(), reparented: !read().detached };
}

// What the constructor refuses, and every one of these is a fact about the
// component rather than defensive coding. The accepted cases are the three
// plain disclosures above - without them every refusal here could be a
// constructor that refuses everything.
const refusals = {};
const refuse = (name, build) => {
  try { build(); refusals[name] = ""; }
  catch (err) { refusals[name] = String(err.message); }
};
refuse("level0", () => new Accordion(box(0)));
refuse("level4", () => new Accordion(box(4)));
refuse("noRegion", () => new Accordion(box(3, (p) => ({ ...p, region: null }))));
refuse("noContainer", () => new Accordion(box(3, (p) => ({ ...p, container: null }))));
refuse("noSummary", () => new Accordion(box(3, (p) => ({ ...p, summary: null }))));
// The box is the summary, so it wraps nothing but the region.
refuse("containerIsSummary", () => new Accordion(box(2, (p) => ({ ...p, container: p.summary }))));
// The box is the region, which is the same mistake pointing the other way.
refuse("containerIsRegion", () => new Accordion(box(2, (p) => ({ ...p, container: p.region }))));
// A region that is somewhere else on the page. Every attribute write still
// lands, which is exactly why this has to be refused rather than noticed
// later: it is the loose-elements component wearing a container argument.
refuse("regionOutside", () => new Accordion(box(1, (p) => ({ ...p, region: node("div") }))));
refuse("summaryOutside", () => new Accordion(box(1, (p) => ({ ...p, summary: node("div") }))));
// A toggle in the box but not in the summary: the rail would be painted on a
// header that does not hold the control.
refuse("toggleOutsideSummary", () => new Accordion(box(1, (p) => {
  const stray = node("button");
  p.container.append(stray);
  return { ...p, toggle: stray };
})));
// A region INSIDE the summary, which is the trap the listener note describes:
// it would close on every click within it.
refuse("regionInsideSummary", () => new Accordion(box(1, (p) => {
  const inner = node("div");
  p.summary.append(inner);
  return { ...p, region: inner };
})));

process.stdout.write(JSON.stringify({
  steps,
  refusals,
  anywhere,
  levels: {
    container: entry.rowGroupEl.dataset.accordionLevel,
    name: entry.rowGroupEl.dataset.accordion,
    toggle: entry.rowToggle.dataset.accordionLevel === undefined
      ? "" : entry.rowToggle.dataset.accordionLevel,
    region: entry.detailRowEl.dataset.accordionLevel === undefined
      ? "" : entry.detailRowEl.dataset.accordionLevel,
  },
  // The run level's box really does hold all three of its parts, read off the
  // shipped newRow() rather than off its comment.
  wraps: {
    summary: entry.rowGroupEl.contains(entry.rowEl),
    toggle: entry.rowGroupEl.contains(entry.rowToggle),
    region: entry.rowGroupEl.contains(entry.detailRowEl),
    regionNotInSummary: entry.rowEl.contains(entry.detailRowEl),
  },
  markClassed: entry.rowToggle.children.some((c) => c.classList.contains("disclosure-mark")),
  // The five own properties, read off a live instance rather than off the
  // source: a field assigned conditionally would be invisible to a text scan.
  ownFields: Object.keys(entry.rowAccordion).sort(),
  // Both rows exist, so the move above was a real reorder.
  rows: table.list.children.length,
  otherIsRow: other.rowGroupEl === table.list.children[0],
  // The class of the element level 1 actually dresses, off the live row - so
  // the stylesheet rule the level's rail is read from can be tied to the
  // level by execution rather than by a text search for a class name.
  summaryClass: entry.rowEl.className,
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

// accordionLook is one reading of a disclosure built over plain nodes: every
// attribute the component may write, plus whether the box has a parent at all.
type accordionLook struct {
	Aria        string  `json:"aria"`
	Hidden      bool    `json:"hidden"`
	Dressed     *string `json:"dressed"`
	BoxLevel    string  `json:"boxLevel"`
	BoxName     string  `json:"boxName"`
	ToggleLevel *string `json:"toggleLevel"`
	RegionLevel *string `json:"regionLevel"`
	Marked      bool    `json:"marked"`
	Detached    bool    `json:"detached"`
}

type accordionAnywhere struct {
	Opened     accordionLook `json:"opened"`
	Closed     accordionLook `json:"closed"`
	Moved      accordionLook `json:"moved"`
	Reparented bool          `json:"reparented"`
}

type accordionResult struct {
	Steps    []accordionStep              `json:"steps"`
	Refusals map[string]string            `json:"refusals"`
	Anywhere map[string]accordionAnywhere `json:"anywhere"`
	Levels   struct {
		Container string `json:"container"`
		Name      string `json:"name"`
		Toggle    string `json:"toggle"`
		Region    string `json:"region"`
	} `json:"levels"`
	Wraps struct {
		Summary            bool `json:"summary"`
		Toggle             bool `json:"toggle"`
		Region             bool `json:"region"`
		RegionNotInSummary bool `json:"regionNotInSummary"`
	} `json:"wraps"`
	MarkClassed  bool     `json:"markClassed"`
	OwnFields    []string `json:"ownFields"`
	Rows         int      `json:"rows"`
	OtherIsRow   bool     `json:"otherIsRow"`
	SummaryClass string   `json:"summaryClass"`
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
	want := []string{"container", "level", "region", "summary", "toggle"}
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

	// THE LEVEL IS WRITTEN ON THE BOX, ONCE (TOR-222). It used to be written
	// on the toggle and on the region separately, which is the same value in
	// two places and neither of them the disclosure - anything holding a
	// control reaches the box with one closest("[data-accordion-level]").
	if got.Levels.Container != "1" || got.Levels.Name != "run" {
		t.Errorf("a run row's box carries data-accordion-level=%q data-accordion=%q, "+
			"want \"1\"/\"run\" - the depth is written into the DOM so it can be read "+
			"from the page rather than inferred", got.Levels.Container, got.Levels.Name)
	}
	if got.Levels.Toggle != "" || got.Levels.Region != "" {
		t.Errorf("the level is still written on the toggle (%q) and/or the region (%q). "+
			"Since TOR-222 the box carries it and the parts inside it do not: two copies "+
			"of one value is what this ticket took out of the component's own writes",
			got.Levels.Toggle, got.Levels.Region)
	}
	if !got.MarkClassed {
		t.Error("no child of the run row's toggle carries .disclosure-mark - that rule " +
			"(base.css) is the one thing all three levels draw identically, and the " +
			"component is what puts it on")
	}

	// WHAT THE LEVEL DECIDES, EXECUTED: whether the summary is dressed. It is
	// the level's own table that decides now rather than the caller passing a
	// `dressed` element, which is the difference between a parameter and an
	// argument the caller could get wrong: levels 1 and 2 have a rail in the
	// stylesheets and level 3 has none, so however level 3 is called it
	// writes no data-expanded.
	for _, want := range []struct {
		level, open, closed string
		dresses             bool
	}{
		{"1", "true", "false", true},
		{"2", "true", "false", true},
		{"3", "", "", false},
	} {
		a, ok := got.Anywhere[want.level]
		if !ok {
			t.Errorf("the driver built no plain disclosure at level %s", want.level)
			continue
		}
		if want.dresses {
			if a.Opened.Dressed == nil || *a.Opened.Dressed != want.open ||
				a.Closed.Dressed == nil || *a.Closed.Dressed != want.closed {
				t.Errorf("level %s dressed its summary %v open and %v closed, want %q/%q - "+
					"the rail lives on the summary at every level that has one",
					want.level, a.Opened.Dressed, a.Closed.Dressed, want.open, want.closed)
			}
		} else if a.Opened.Dressed != nil {
			t.Errorf("level %s wrote data-expanded=%q on its summary. Level 3 dresses "+
				"NOTHING - its rail would sit inside the file's rail inside the torrent's "+
				"ground, and three nested grounds read as chrome - so the level's own "+
				"table is what refuses it rather than the caller remembering to",
				want.level, *a.Opened.Dressed)
		}
		// And the two writes every level makes, at every level.
		if a.Opened.Aria != "true" || a.Opened.Hidden || a.Closed.Aria != "false" ||
			!a.Closed.Hidden {
			t.Errorf("level %s read aria=%q hidden=%v open and aria=%q hidden=%v closed; "+
				"want true/false and false/true", want.level, a.Opened.Aria, a.Opened.Hidden,
				a.Closed.Aria, a.Closed.Hidden)
		}
		if a.Opened.BoxLevel != want.level {
			t.Errorf("level %s wrote data-accordion-level=%q on its box",
				want.level, a.Opened.BoxLevel)
		}
	}

	// THE REFUSALS ARE THE PARAMETER AND THE WRAPPING, and each one is a fact
	// about the component rather than defensive coding:
	//
	//   - there are three levels, so 0 and 4 are not levels;
	//   - a wrapper has to have a box, a header and a region, so a missing
	//     one is a wiring error at a call site;
	//   - the box has to be a THIRD element - one that is also the summary or
	//     the region wraps nothing;
	//   - the box has to actually HOLD its parts, because every attribute
	//     write still lands if it does not, so the mistake is invisible;
	//   - the toggle has to be in the summary, or the rail is painted on a
	//     header that does not hold the control;
	//   - and the region has to be OUTSIDE the summary, which is the one
	//     structural rule that is load-bearing: a region inside the thing
	//     that toggles it closes on every click within it.
	for _, want := range []struct{ name, needle string }{
		{"level0", "level must be 1"},
		{"level4", "level must be 1"},
		{"noRegion", "has no region"},
		{"noContainer", "has no container"},
		{"noSummary", "has no summary"},
		{"containerIsSummary", "is also its summary"},
		{"containerIsRegion", "is also its region"},
		{"regionOutside", "does not hold its region"},
		{"summaryOutside", "does not hold its summary"},
		{"toggleOutsideSummary", "toggle is not in its summary"},
		{"regionInsideSummary", "region is inside its summary"},
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
	// And the accepted cases are the `anywhere` block above - three levels
	// built and driven - without which every refusal here could be a
	// constructor that refuses everything.
	if len(got.Anywhere) != 3 {
		t.Errorf("the driver built %d plain disclosures, want 3 - without an accepted "+
			"case the refusals above prove only that the constructor throws",
			len(got.Anywhere))
	}
}

// TestTheDisclosureWrapsItsContentAtTheRunLevel is criterion 1's executed half
// inside the app, and it reads the SHIPPED newRow() rather than its comment:
// .run-row-group holds the line, its toggle and the detail row, and the detail
// row is not inside the line.
//
// That last one is the load-bearing half and it is why the wrapper is safe.
// The region is the summary's SIBLING inside the box, never its descendant -
// which is what lets the run level keep its click listener on .run-row while
// an element sits around the pair. A region inside the summary would collapse
// on every click in it, and that is the trap the two-<tr> arrangement existed
// to avoid.
//
// The unrelated use - a box, a header and a region with none of torpeek's
// markup or CSS anywhere near them - is `anywhere` in the test above, plus
// docs/spikes/TOR-222-anywhere/ in a browser.
func TestTheDisclosureWrapsItsContentAtTheRunLevel(t *testing.T) {
	got := runAccordionDriver(t)

	if !got.Wraps.Summary || !got.Wraps.Toggle || !got.Wraps.Region {
		t.Errorf(".run-row-group holds its summary=%v toggle=%v region=%v; a box that does "+
			"not contain all three is not the box, and the constructor's own check is "+
			"what would have thrown before this test read anything",
			got.Wraps.Summary, got.Wraps.Toggle, got.Wraps.Region)
	}
	if got.Wraps.RegionNotInSummary {
		t.Error("the detail row is INSIDE .run-row. It has to be its sibling in the box: " +
			"the row's click listener is what opens and closes a torrent, so a detail " +
			"inside it would collapse on every click within it - a picker checkbox, a " +
			"thumbnail, Compare. That is the trap the two-<tr> arrangement existed to " +
			"avoid, and it is the reason the listener stays on .run-row rather than a " +
			"reason the box cannot exist")
	}

	// AND THE SAME COMPONENT, MOVED. Criterion 2's own case, executed on the
	// plain-node disclosures: the box is built with no parent at all, driven
	// both ways, then put inside something else and driven again.
	for _, level := range []string{"1", "2", "3"} {
		a, ok := got.Anywhere[level]
		if !ok {
			continue
		}
		if !a.Opened.Detached {
			t.Errorf("level %s's plain disclosure had a parent when it was driven, so it "+
				"did not show that a box needs none", level)
		}
		if !a.Reparented {
			t.Errorf("level %s's box was not actually moved, so the reading after the "+
				"move proves nothing", level)
		}
		if a.Moved.Aria != "true" || a.Moved.Hidden {
			t.Errorf("after being moved into a different parent, level %s read aria=%q "+
				"hidden=%v; want true/false. No code path may need a particular parent",
				level, a.Moved.Aria, a.Moved.Hidden)
		}
	}
}

// disclosureParts are the elements the page's three disclosures are built
// from - the box, the summary and the region at each level (run-table.js's
// newRow, file-list.js's renderFileList, file-detail.js's BODY). Every one of
// them is an element the component either adopts or writes on, and none of
// them may be styled in a way that only works inside one particular parent.
//
// The three row parts are TOR-221's own list, kept in step by
// TestTheDisclosurePartsIncludeEveryRowPart below rather than by hope.
var disclosureParts = []string{
	".run-row-group", ".run-row", ".run-detail-row", // level 1
	".picker-item", ".picker-file", ".file-detail", // level 2
	".meta", ".meta-title", ".meta-body", // level 3
}

// disclosureState are the ways a rule can be ABOUT a disclosure's state or
// depth rather than about the element's own appearance. Each is written by
// accordion.js and by nothing else.
var disclosureState = []string{`[data-expanded`, `[data-accordion`}

// TestNoDisclosureDependsOnItsContainer is criterion 2's guard, and it is
// TOR-221's TestNoRowDependsOnItsContainerToFindItsColumns pointed at the
// disclosure instead of at the columns - same idea, same reason for the shape:
// one grep for one spelling is weak, because a reintroduction arrives as
// whichever spelling looked natural to whoever wrote it. So each arm names a
// DIFFERENT way to write "this disclosure only works in that box":
//
//	the open state qualified      `.picker-item[data-expanded="true"] >
//	by an ancestor                 .picker-file { ... }` - the exact rule this
//	                               ticket removed. The attribute is written on
//	                               the element being dressed, so a rule that
//	                               names an ancestor to reach it is asserting a
//	                               parent it does not need.
//	the LEVEL qualified            the same mistake with the newer attribute:
//	by an ancestor                 nothing keys off data-accordion-level today,
//	                               and a first rule that did should key off the
//	                               box itself.
//	the MARK reached through       `.picker-item .disclosure-mark { ... }`. The
//	a container                    mark's one legitimate ancestor is the TOGGLE
//	                               (base.css draws the open triangle as
//	                               `[aria-expanded="true"] > .disclosure-mark`),
//	                               because the component guarantees that
//	                               relationship - the mark is the toggle or
//	                               inside it. A class or an id as the ancestor
//	                               is a container.
//	a grid ITEM's property on      `grid-column`, `grid-row`, `grid-area`,
//	a disclosure part              `display: contents`, `subgrid` - properties
//	                               that mean nothing except inside a particular
//	                               parent, so declaring one asserts one.
//	the component reaching out     accordion.js naming closest, parentElement,
//	of its own parts               querySelector, document or window: the
//	                               "just look the box up" version of the same
//	                               coupling, in JS instead of CSS.
//	a second writer of the         data-expanded or data-accordion written by
//	state                          any other module - a hand-written level is a
//	                               copy nothing keeps in step, and it would be
//	                               written where the element happens to be.
//
// The EXECUTED half is elsewhere and is what actually proves the negative:
// TestTheNestingLevelIsAParameterWithThreeValues builds three disclosures over
// plain nodes with no parent at all, drives them, moves the box into a
// different element and drives them again, and refuses eleven malformed
// wirings. This file's arms are what stop the property being written back out
// in CSS, where no driver would see it.
func TestNoDisclosureDependsOnItsContainer(t *testing.T) {
	// Comments stripped, for the reason TOR-221's own guard records: three
	// files and this test's own subject EXPLAIN the removed selector by name,
	// and a guard that could not tell prose from code would fail on the
	// explanation instead of on the mistake.
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(stylesheet(t), "")

	stateRules, markRules := 0, 0
	for _, rule := range splitRules(live) {
		for _, sel := range strings.Split(rule.selector, ",") {
			sel = strings.TrimSpace(sel)
			if sel == "" {
				continue
			}
			key := keyCompound(sel)

			// ARM 1 and 2: the open state and the level name no ancestor. The
			// attribute is on the element the rule is about, always, so the
			// selector needs nothing before it.
			for _, attr := range disclosureState {
				if !strings.Contains(sel, attr) {
					continue
				}
				stateRules++
				if key != sel {
					t.Errorf("%q qualifies %s by an ancestor. accordion.js writes that "+
						"attribute on the element the rule dresses - its summary, or its "+
						"box for the level - so naming a container to reach it is an "+
						"assertion about a parent the disclosure does not need. That is "+
						"the rule TOR-222 removed: `.picker-item[data-expanded=\"true\"] > "+
						".picker-file` became `.picker-file[data-expanded=\"true\"]`",
						sel, attr)
				}
			}

			// ARM 3: the mark's only legitimate ancestor is the toggle.
			if strings.Contains(key, ".disclosure-mark") {
				markRules++
				if key != sel {
					ancestor := strings.TrimSpace(strings.TrimSuffix(sel, key))
					ancestor = strings.TrimRight(ancestor, " >+~")
					if !strings.Contains(ancestor, "[aria-expanded") &&
						!strings.Contains(ancestor, "[data-expanded") &&
						!strings.Contains(ancestor, "[data-accordion") {
						t.Errorf("%q reaches the disclosure mark through %q. The mark's one "+
							"ancestor is the TOGGLE, and base.css says so as "+
							"`[aria-expanded=\"true\"] > .disclosure-mark` - a relationship "+
							"the component guarantees, because the mark is the toggle or "+
							"inside it. A class or an id there is a container, and the mark "+
							"is the one thing that does NOT vary with the level", sel, ancestor)
					}
				}
			}

			// ARM 4: a disclosure part may not be given a property that only
			// means something to a child of one particular parent.
			part := ""
			for _, p := range disclosureParts {
				if strings.Contains(key, p) {
					part = p
				}
			}
			if part == "" {
				continue
			}
			for _, prop := range []string{"grid-column", "grid-row", "grid-area"} {
				if regexp.MustCompile(`(^|[;{\s])` + prop + `\s*:`).MatchString(rule.body) {
					t.Errorf("%q declares %s on the disclosure part %s. That property only "+
						"means anything to a grid ITEM, so it is an assertion about this "+
						"element's PARENT - and a disclosure that only lays out inside one "+
						"container is the thing TOR-221 removed and TOR-222 depends on "+
						"staying removed", sel, prop, part)
				}
			}
			for _, value := range []string{"display: contents", "subgrid"} {
				if strings.Contains(rule.body, value) {
					t.Errorf("%q declares %q on the disclosure part %s. Both make this "+
						"element's layout its parent's business: `display: contents` hands "+
						"its children to the grandparent's grid, and `subgrid` resolves "+
						"only on a direct grid item", sel, value, part)
				}
			}
		}
	}

	// Both selector arms have to have matched something, or they swept a
	// stylesheet that stopped spelling the thing they look for and reported
	// nothing. Two state rules today (the two levels with a rail) and three
	// mark rules (the class plus its two ::before forms).
	if stateRules == 0 {
		t.Error("no rule in the served stylesheet mentions data-expanded or " +
			"data-accordion, so arms 1 and 2 checked nothing. Either the two rails are " +
			"gone or they are spelled some way this guard cannot see")
	}
	if markRules == 0 {
		t.Error("no rule keys off .disclosure-mark, so arm 3 checked nothing - that rule " +
			"is the one thing all three levels draw identically")
	}

	// ARM 5: the component reaches for nothing. Every name here is a way to
	// find an element it was not handed, and `contains` is deliberately NOT
	// among them - it is the containment ASSERTION the constructor makes, the
	// positive half of this arm, and its refusals are executed in
	// TestTheNestingLevelIsAParameterWithThreeValues.
	acc := liveJS(t, accordionJS(t))
	for _, forbidden := range []string{
		"parentElement", "parentNode", "closest(", "querySelector", "getElementById",
		"getElementsBy", "matches(", "ownerDocument", "document.", "window.",
		"isConnected", "getRootNode", "nextElementSibling", "previousElementSibling",
		"firstElementChild", "lastElementChild", "appendChild", "insertBefore",
		"replaceChildren",
	} {
		if strings.Contains(acc, forbidden) {
			t.Errorf("accordion.js names %q. Every one of those finds an element the "+
				"component was not handed, which is how a disclosure comes to depend on "+
				"where it is: the box is an ARGUMENT and the parts are arguments, and the "+
				"only thing the class asks the DOM is whether the box contains them",
				forbidden)
		}
	}
	// And the containment check is really there, in code rather than in the
	// header: without it the box argument is decoration.
	if !strings.Contains(acc, ".contains(") {
		t.Error("accordion.js never calls contains(). The box argument is then a claim " +
			"nobody checks - every attribute write still lands on a wrapper that wraps " +
			"nothing, so the mistake is invisible. See the constructor's own note")
	}

	// ARM 6: one writer of the state and of the level, across every served
	// module, derived rather than listed so a module added later cannot
	// quietly become the second.
	for _, name := range servedModules(t) {
		if name == "accordion.js" {
			continue
		}
		src, err := embedded.ReadFile("assets/" + name)
		if err != nil {
			t.Fatalf("reading the embedded %s: %v", name, err)
		}
		for _, forbidden := range []string{
			"dataset.expanded", "dataset.accordion", `"data-expanded"`, `"data-accordion`,
		} {
			if strings.Contains(liveJS(t, string(src)), forbidden) {
				t.Errorf("%s writes %s. accordion.js's apply and constructor are the only "+
					"writers of a disclosure's open state and depth; a second one is a copy "+
					"nothing keeps in step, and it would be written wherever the element "+
					"happened to be rather than on the summary and the box", name, forbidden)
			}
		}
	}
}

// TestTheDisclosurePartsIncludeEveryRowPart keeps this file's part list in
// step with TOR-221's. The row's three elements ARE level 1's box, summary and
// region, so a part added there and not here would be swept by one guard and
// not the other - and the one that would miss it is the newer one.
func TestTheDisclosurePartsIncludeEveryRowPart(t *testing.T) {
	have := map[string]bool{}
	for _, p := range disclosureParts {
		have[p] = true
	}
	for _, p := range rowParts {
		if !have[p] {
			t.Errorf("%s is one of a run row's parts (columns_test.go's rowParts) but not "+
				"one of the disclosure's. Level 1's box, summary and region are exactly "+
				"those three elements", p)
		}
	}
}

// TestTheLevelsLooksAreTheStylesheetsOwn is criterion 5, and its point is that
// the level table in accordion.js cannot drift into prose.
//
// TOR-212 read the three levels' appearance off the running page and wrote it
// into a comment. A comment is not checkable, and the finding it carried is
// the kind that rots first: what varies with the depth is the RAIL and the
// GROUND, and both live in stylesheets a later ticket edits without opening
// accordion.js. So the table has `rail` and `ground` fields now, and this test
// parses the rules that actually dress a disclosure out of the served CSS and
// requires the table to match them.
//
// WHAT IT DERIVES RATHER THAN LOOKS UP. It does not know which class belongs
// to which level - it finds every rule keyed on `[data-expanded="true"]`,
// which is exactly the set of levels that dress anything, and then uses the
// one fact the design states: the depth is how LOUDLY the state is dressed, so
// the rails thin monotonically outside-in and the one filled ground is the
// outermost. Level 1 is then tied to its class by execution (the driver reads
// the className off a live row) rather than by a text search.
//
// The MEASURED values, in Chrome on the running page and unchanged by TOR-222:
// level 1 rgb(9,58,64) ground with `inset 3px 0 0 rgb(34,224,232)`; level 2 the
// same colour rail at 2px and no ground; level 3 nothing at all. That the
// component puts .disclosure-mark on all three and that the mark does not vary
// is TestTheNestingLevelIsAParameterWithThreeValues' and base.css's own guard.
func TestTheLevelsLooksAreTheStylesheetsOwn(t *testing.T) {
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(stylesheet(t), "")

	// What the stylesheets say: one entry per rule that dresses an open
	// disclosure, in the order the cascade sees them.
	type dressing struct {
		class  string
		rail   string
		ground bool
	}
	var dressings []dressing
	inset := regexp.MustCompile(`box-shadow:\s*inset\s+(\d+px)\s`)
	for _, rule := range splitRules(live) {
		for _, sel := range strings.Split(rule.selector, ",") {
			sel = strings.TrimSpace(sel)
			if !strings.Contains(sel, `[data-expanded="true"]`) {
				continue
			}
			m := inset.FindStringSubmatch(rule.body)
			if m == nil {
				t.Errorf("%q dresses an open disclosure but declares no inset box-shadow. "+
					"The rail IS the level's look; a rule here without one means the level "+
					"table below is describing something that is no longer drawn", sel)
				continue
			}
			dressings = append(dressings, dressing{
				class:  strings.TrimSuffix(sel, `[data-expanded="true"]`),
				rail:   m[1],
				ground: regexp.MustCompile(`(^|[;{\s])background\s*:`).MatchString(rule.body),
			})
		}
	}
	if len(dressings) != 2 {
		t.Fatalf("%d rules dress an open disclosure, want 2: %+v. The page has three "+
			"nested levels and the innermost dresses nothing, deliberately - its rail "+
			"would sit inside the file's rail inside the torrent's ground", len(dressings),
			dressings)
	}

	// What the component says. Parsed out of the level table rather than
	// hardcoded, so this compares two independent statements of one fact.
	levels := regexp.MustCompile(
		`\[(\d+), \{ name: "(\w+)", rail: (null|"\d+px"), ground: (true|false) \}\]`).
		FindAllStringSubmatch(liveJS(t, accordionJS(t)), -1)
	if len(levels) != 3 {
		t.Fatalf("accordion.js's level table did not parse as three {name, rail, ground} "+
			"entries - got %d. The rail and the ground are the level's whole meaning and "+
			"they have to be readable, or this test is comparing the CSS with nothing",
			len(levels))
	}

	// The rails, outermost first. Two levels have one and the third is null;
	// the widths must thin as the nesting deepens, which is the design's own
	// statement of what the level MEANS.
	type railedLevel struct {
		level, rail string
		ground      bool
	}
	var railed []railedLevel
	for _, m := range levels {
		if m[3] == "null" {
			if m[4] != "false" {
				t.Errorf("level %s has no rail but claims a ground. A level that dresses "+
					"nothing dresses nothing", m[1])
			}
			continue
		}
		railed = append(railed, railedLevel{m[1], strings.Trim(m[3], `"`), m[4] == "true"})
	}
	if len(railed) != len(dressings) {
		t.Fatalf("accordion.js's table has %d levels with a rail and the stylesheets have "+
			"%d rules that draw one. The table is meant to be the CSS's own values, so a "+
			"mismatch is either a level that draws nothing or a rail nothing declares",
			len(railed), len(dressings))
	}

	// Paired by depth against the rules sorted widest-rail first, because the
	// depth IS how loudly the state is dressed.
	sort.Slice(dressings, func(i, j int) bool {
		return railWidth(t, dressings[i].rail) > railWidth(t, dressings[j].rail)
	})
	for i, want := range railed {
		got := dressings[i]
		if got.rail != want.rail {
			t.Errorf("level %s claims a %s rail; the %s-widest rule that dresses an open "+
				"disclosure (%s) draws %s. accordion.js's table is meant to BE the "+
				"stylesheets' values - this is how it stops being a comment",
				want.level, want.rail, ordinal(i), got.class, got.rail)
		}
		if got.ground != want.ground {
			t.Errorf("level %s claims ground=%v; %s declares a background=%v. The one "+
				"filled ground appears once, at the top - two nested grounds read as a "+
				"third level of chrome nobody asked for (filelist.css says so where the "+
				"file's rail is quieter than the torrent's)",
				want.level, want.ground, got.class, got.ground)
		}
		// And it thins going in.
		if i > 0 && railWidth(t, dressings[i].rail) >= railWidth(t, dressings[i-1].rail) {
			t.Errorf("the rails do not thin with the depth: %s draws %s and %s draws %s. "+
				"The depth is how loudly the open state is dressed, and a level that "+
				"shouts louder than the one outside it is not a depth",
				dressings[i-1].class, dressings[i-1].rail, dressings[i].class, dressings[i].rail)
		}
	}

	// AND LEVEL 1'S CLASS, BY EXECUTION. The driver reads className off the
	// element the shipped newRow() actually hands over as the summary, so the
	// widest rail is tied to the outermost level by what the code does rather
	// than by this test knowing a class name.
	if got := runAccordionDriver(t).SummaryClass; got != strings.TrimPrefix(dressings[0].class, ".") {
		t.Errorf("the run level dresses an element whose class is %q, but the widest rail "+
			"is drawn on %q. The outermost level is the one with the ground and the 3px "+
			"rail, and that has to be the same element the table's own rows carry",
			got, dressings[0].class)
	}
	// Level 2's is the other one, and its class is checked against the two
	// modules that hand it over - the row's <label>, which file-list.js builds
	// and file-detail.js dresses.
	if got := dressings[1].class; got != ".picker-file" {
		t.Errorf("the 2px rail is drawn on %q, want .picker-file - the <label> that IS a "+
			"file's row. filelist.css used to reach it through its <li> and TOR-222 moved "+
			"the attribute onto it; if the element changed, file-detail.js's `summary` and "+
			"file-list.js's bundle have to change with it", got)
	}
	if !strings.Contains(liveJS(t, fileDetailJS(t)), "summary: this.fileRow,") {
		t.Error("file-detail.js's level-2 disclosure no longer dresses this.fileRow - the " +
			"rail rule keys off .picker-file[data-expanded=\"true\"], so the summary the " +
			"accordion is given has to be that <label>")
	}
	if !strings.Contains(liveJS(t, fileListJS(t)), `row.className = "picker-file";`) {
		t.Error("file-list.js no longer builds the row as .picker-file, so the class the " +
			"rail rule keys off is drawn on nothing")
	}
}

// railWidth turns "3px" into 3. A rail spelled any other way is a failure
// rather than a zero: the test above sorts on this, and a silent zero would
// reorder the levels instead of reporting the problem.
func railWidth(t *testing.T, rail string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSuffix(rail, "px"))
	if err != nil {
		t.Fatalf("a disclosure's rail is %q, which is not a whole number of pixels: %v",
			rail, err)
	}
	return n
}

// ordinal names which of the sorted dressing rules a message is about, so a
// failure reads as a sentence rather than as an index.
func ordinal(i int) string {
	if i == 0 {
		return "first"
	}
	return "next"
}

// TestTheAnywhereSpikeUsesTheShippedModule keeps criterion 1's evidence from
// rotting into a mock-up.
//
// docs/spikes/TOR-222-anywhere/ is the unrelated use the ticket asks for - a
// FAQ page with none of torpeek's markup and none of its stylesheets - and its
// whole value rests on ONE line: it imports internal/web/assets/accordion.js
// by relative path rather than carrying a copy. A copy would keep passing
// forever while the shipped class changed under it, which is the failure mode
// the two earlier spikes deliberately accept for tokens.css (they measure a
// layout against frozen values) and this one must not.
//
// So: the import is there, no class of its own is declared, and no torpeek
// stylesheet is linked. Not a skip if the file is missing - the file IS the
// evidence, and a criterion whose evidence quietly stopped existing should
// fail loudly.
func TestTheAnywhereSpikeUsesTheShippedModule(t *testing.T) {
	const path = "../../docs/spikes/TOR-222-anywhere/anywhere.html"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v. That page is criterion 1's whole evidence - the "+
			"component shown working somewhere that is not torpeek's table - and it is "+
			"referenced from docs/front-end.md's TOR-222 section", path, err)
	}
	page := string(b)
	// COMMENTS STRIPPED for the stylesheet arm below, and this file's own
	// liveJS records why in the general case: the page EXPLAINS at length that
	// it links none of torpeek's stylesheets, naming two of them, so a raw-text
	// check reads the explanation as the violation. HTML and CSS comments are
	// both block-delimited, so this is a safe strip - a `//` line strip on an
	// HTML file would not be.
	live := regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(page, "")
	live = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(live, "")

	const wantImport = `from "../../../internal/web/assets/accordion.js"`
	if !strings.Contains(live, wantImport) {
		t.Errorf("the spike does not import the shipped module (%s). Its claim is about "+
			"the class that SHIPS; a page carrying its own copy would keep proving that "+
			"copy works long after accordion.js had changed", wantImport)
	}
	// A copy would arrive as a class declaration, or as a second import from
	// somewhere inside the spike's own directory.
	for _, forbidden := range []string{"class Accordion", `from "./accordion.js"`,
		`from "accordion.js"`} {
		if strings.Contains(live, forbidden) {
			t.Errorf("the spike contains %q - a copy of the component, or an import of "+
				"one beside the page. There must be exactly one Accordion and it must be "+
				"the served one", forbidden)
		}
	}
	// And none of torpeek's stylesheets, because "it works without them" is
	// half of what the page is for. Checked by filename against the served
	// list rather than by naming two of them.
	for _, sheet := range stylesheetFiles {
		if strings.Contains(live, sheet) {
			t.Errorf("the spike references %s. It is meant to prove the component needs "+
				"none of torpeek's CSS - every rule on that page is its own, and a link "+
				"to one of ours makes the whole reading uninterpretable", sheet)
		}
	}
	// The control arm has to be in it, or the page is a probe that can only
	// say yes. Its own README lists the five refusals it wires wrongly.
	if !strings.Contains(live, "ACCEPTED - the check is not there") {
		t.Error("the spike has no control arm. Every reading it takes is 'fine', and a " +
			"probe that cannot be shown reporting otherwise measured nothing - so it " +
			"wires five disclosures WRONGLY and records what each is refused with")
	}
}
