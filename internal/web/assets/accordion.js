// THE DISCLOSURE (TOR-212), and it is the page's most characteristic
// interaction: three places open and close, at three depths, nested inside
// each other, and until this file they were three copies of the same three
// DOM writes.
//
// WHAT WAS ALREADY THERE, so this is a consolidation rather than an invention:
//
//   setRunExpanded(entry, expanded)   run-table.js    a torrent's line -> its detail
//   setFileExpanded(expanded)         file-detail.js  a video file's block
//   setMetaExpanded(expanded)         file-detail.js  the Metadata sub-block inside it
//
// All three did the SAME THREE WRITES and nothing else, in three different
// orders:
//
//   toggle.setAttribute("aria-expanded", String(expanded))
//   region.hidden = !expanded
//   dressed.dataset.expanded = String(expanded)     (the outer two only)
//
// That triple is what a disclosure IS on this page, and apply() below is now
// the only place it is written. Everything else at those three call sites -
// what opening MEANS - stayed where it was, which is the whole of the
// boundary this file draws:
//
//   - The RUN level keeps syncRunDetailWidth (a detail arriving can add or
//     remove the document's own scrollbar and with it a few pixels of the
//     pane's width, TOR-174) and detailShown (a row opening is when its
//     top-up standing is worth reading off disk, TOR-152). Both are the
//     TABLE's and the PAGE's business and neither belongs in a generic
//     disclosure. The second of them had NO test on its call site before
//     TOR-212 - measured, by never calling it and watching the whole package
//     stay green - so a component that swallowed it would have swallowed it
//     silently. accordion_test.go now runs both.
//   - The FILE level keeps "one file's detail at a time" (TOR-182): opening
//     one closes its siblings. That is a decision about a SET of disclosures
//     and it is deliberately NOT made here, because the level above it made
//     the opposite decision for written reasons (setRunExpanded's own
//     heading) and a component that closed siblings would impose one of them
//     on all three.
//   - The FILE level also keeps loadFileDetail, the first-open read off disk.
//
// ---------------------------------------------------------------------------
// IT DOES NOT REMEMBER, and this is the one thing about it that is not a
// preference.
//
// state.js says the shallow reason: the singleton detail pane was replaced
// because "the accordion has no single slot to be the one thing in", so the
// open flag lives on the record - entry.expanded, fentry.expanded,
// fentry.metaExpanded - and has since TOR-63.
//
// There are two harder reasons, and they are different at the outer level and
// the inner two. Worth separating, because the obvious version of the story is
// wrong about one of them:
//
//   - AT THE RUN LEVEL the instance is NOT destroyed. newRow() builds it once
//     and hands it out on the entry, and reorderRuns()'s move - which relocates
//     a row's whole element on every redraw - leaves the object alone. What
//     makes a second copy of the flag wrong here is that entry.expanded is
//     what the PAGE reads: app.js's toggleRun computes `!entry.expanded` to
//     decide what a click means. A copy in here would be a second answer to
//     one question, and the loser would be the one on screen.
//   - AT THE FILE AND METADATA LEVELS the instance genuinely IS destroyed and
//     rebuilt. file-list.js's renderFileList replaces every row with
//     `replaceChildren`, minting a fresh <file-detail> per video file, and
//     mount() then builds fresh disclosures whose constructors never see any
//     flag. Anything remembered here would be lost every time the file list
//     is rebuilt - and would come back closed under a person who had opened
//     it.
//
// So apply() takes the state as an ARGUMENT on every call and this class has
// no field for it - not a private one, not a cached copy, not the DOM read
// back. The owner holds it; this writes it out.
//
// ---------------------------------------------------------------------------
// NO LISTENER, and that is not tidiness either.
//
// Each of the three levels activates from a different element, with a
// different exception:
//
//   - the RUN level listens on .run-row, NOT on .run-row-group, because the
//     detail is the row's SIBLING inside the wrapper: on the wrapper, every
//     click inside an open detail - a picker checkbox, a thumbnail, Compare -
//     would collapse it. That is the exact trap the two-<tr> arrangement
//     existed to avoid and TOR-215 wrote down when it built the wrapper. It
//     also stopPropagation()s Cancel and the two priority buttons.
//   - the FILE level listens on the row and excludes events whose target IS
//     the checkbox, because activating a <label> dispatches a second
//     synthetic click that would toggle the detail straight back closed.
//   - the META level listens on the toggle itself and has no exception.
//
// A component that owned the listener would have to learn all three, and the
// first thing it would get wrong is the one that matters: given a wrapper and
// a region, the obvious place to listen is the wrapper. So the listener stays
// with the element it is on, at each level, and this class is only ever told
// the answer.
//
// ---------------------------------------------------------------------------
// THE NESTING LEVEL, which is a parameter because the UI has three of them and
// they are genuinely nested: a file's block sits inside an open run's detail,
// and Metadata inside the file's block.
//
// Its values are those three and no others, and what each one looks like was
// read off the current stylesheets rather than designed (docs/front-end.md
// carries the measured diff). What the level decides here:
//
//   1  run   a torrent's line       dressed: .run-row     accent ground + a 3px rail
//   2  file  a video file's block   dressed: .picker-item          a 2px rail
//   3  meta  the Metadata block     dressed: none                  nothing
//
// So the level is the depth, and the depth is how loudly the open state is
// dressed: the rail thins 3px -> 2px -> none and the filled ground appears
// once, at the top. `dressed` is REQUIRED at 1 and 2 and REFUSED at 3, which
// is that table turned into a check rather than a comment - level 3 has
// nothing to dress because two filled grounds nested already read as a third
// level of chrome nobody asked for (filelist.css says so at the level above).
//
// THE MARK DOES NOT VARY WITH THE LEVEL, and that is the other half of the
// finding. All three levels drew their triangle with the same three
// declarations and the same two ::before rules, three times over - and each
// copy's own comment said it was deliberately the same object as its
// neighbour's, because "opening a torrent and opening one of its files are the
// same gesture, so they should not be two different marks". One rule now
// (.disclosure-mark, base.css) and this constructor is what puts it on.
//
// ---------------------------------------------------------------------------
// NOT A CUSTOM ELEMENT, unlike the six of docs/front-end.md's element pattern,
// and the reason is structural rather than stylistic. A custom element has to
// BE somewhere in the tree, and at every one of the three levels the only
// place to put it is between a grid container and its items:
//
//   - .run-row-group carries `grid-template-columns: subgrid`, which only
//     works on a DIRECT grid item of .run-grid; an element wrapped around it
//     would break the column alignment TOR-214 measured to 0.00px.
//   - at the file level the toggle is inside the row's <label> and the region
//     is the row's sibling inside the <li>; there is no box that holds both
//     and only both.
//
// So this is a plain class over elements the page already has, and the
// element pattern's points that still apply are applied: parts are checked by
// name with a loud throw (point 3), and one method for the one thing the
// outside may ask for (point 5). Points 1, 2, 4 and 6 are about elements and
// have nothing to say here.

// The three levels the page has, and the shape of each. Not exported: Claude
// Design's checker indexes a module's UPPERCASE-INITIAL named exports as
// COMPONENTS (TOR-208 found five constants offered to designers as things to
// render, PRIORITY_HIGH the number 1 among them), so this module exports the
// class and nothing else.
const LEVELS = new Map([
  [1, { name: "run", dressed: true }],
  [2, { name: "file", dressed: true }],
  [3, { name: "meta", dressed: false }],
]);

export class Accordion {
  // level    1, 2 or 3 - see the block above.
  // toggle   the control carrying aria-expanded. Always a real <button> at all
  //          three levels, which is where the keyboard operability comes from:
  //          Return and Space activate it and its click bubbles to whatever
  //          listener the level put on the row.
  // region   the element the toggle opens. Its `hidden` attribute is the WHOLE
  //          of collapse - no rule in any stylesheet sets `display` on any of
  //          the three regions, deliberately, so the UA's own [hidden] rule is
  //          never beaten by a class selector at equal specificity. That trap
  //          has cost this codebase six fixes in one file.
  // mark     the element wearing the triangle. Defaults to the toggle, which
  //          is right at level 2 (.picker-open IS the mark) and wrong at 1 and
  //          3, where the triangle is a span inside the button so the label
  //          starts at the same x whichever way it points.
  // dressed  the element carrying data-expanded, for the level's own rail.
  //          Required at levels 1 and 2, refused at level 3.
  constructor({ level, toggle, region, mark, dressed = null }) {
    const shape = LEVELS.get(level);
    if (!shape) {
      throw new Error("accordion: level must be 1 (run), 2 (file) or 3 (meta), not " +
        JSON.stringify(level));
    }
    // Loudly by name, the element pattern's point 3: a missing part here is a
    // wiring error at every one of the three call sites, because all three
    // build or are handed their parts synchronously and there is no "not yet"
    // to tell it apart from (docs/front-end.md's own note on which elements
    // have to wait and which do not).
    for (const [name, node] of Object.entries({ toggle, region })) {
      if (!node) throw new Error("accordion: level " + shape.name + " has no " + name);
    }
    if (shape.dressed && !dressed) {
      throw new Error("accordion: level " + shape.name + " needs a dressed element - its " +
        "open state is a state of the ROW, not of what the row opens onto");
    }
    if (!shape.dressed && dressed) {
      throw new Error("accordion: level " + shape.name + " takes no dressed element - " +
        "nothing in the stylesheets dresses an open Metadata block, and a rail here would " +
        "be a third nested ground");
    }

    this.level = level;
    this.toggle = toggle;
    this.region = region;
    this.mark = mark || toggle;
    this.dressed = dressed;

    // The level, written into the DOM rather than kept to itself, so the
    // depth of a disclosure is readable from the page - by a Go test, by a
    // browser drive, and by a design consumer composing one.
    this.toggle.dataset.accordionLevel = String(level);
    this.region.dataset.accordionLevel = String(level);
    // The one thing every level draws identically (base.css). classList.add
    // is idempotent, so an element whose markup already carries the class -
    // the export's own preview card, which renders without scripts and has to
    // carry it statically - is unchanged.
    this.mark.classList.add("disclosure-mark");
  }

  // apply writes the open state out. THE ONLY WRITER of these three
  // attributes on this page, and the state arrives as an argument every time -
  // see this module's header for why there is no field to keep it in.
  //
  // The three writes are independent: nothing observes any of them (no
  // MutationObserver on this page watches attributes, and the two that exist
  // watch childList until their element is wired and then disconnect), so
  // this order is stable rather than load-bearing.
  apply(expanded) {
    this.toggle.setAttribute("aria-expanded", String(expanded));
    this.region.hidden = !expanded;
    if (this.dressed) this.dressed.dataset.expanded = String(expanded);
  }
}
