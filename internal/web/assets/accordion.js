// THE DISCLOSURE (TOR-212, reworked into a WRAPPER by TOR-222), and it is the
// page's most characteristic interaction: three places open and close, at
// three depths, nested inside each other, and before TOR-212 they were three
// copies of the same three DOM writes.
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
//   summary.dataset.expanded = String(expanded)     (the outer two only)
//
// That triple is what a disclosure IS on this page, and apply() below is now
// the only place it is written.
//
// ---------------------------------------------------------------------------
// IT IS A WRAPPER, and TOR-222 is the ticket that made it one. Worth stating
// what changed, because the change is smaller than the ticket expected and
// the reason is the interesting part.
//
// TOR-212 shipped a class handed four LOOSE ELEMENTS - a toggle, a region, a
// mark and a thing to dress - collected from three different ancestors. That
// is a set of attribute writes, not a component: there was no box, so nothing
// a design could compose and nothing a stylesheet could key off. The owner's
// objection was exactly that.
//
// THE BOX EXISTED AT ALL THREE LEVELS ALREADY. That is the finding, and it was
// checked in the markup rather than assumed - the ticket expected to have to
// rearrange file-detail.js and index.html and there was nothing to rearrange:
//
//   1  run   .run-row-group   holds .run-row        and .run-detail-row
//   2  file  li.picker-item   holds label.picker-file and <file-detail>
//   3  meta  section.meta     holds h3.meta-title  and .meta-body
//
// So `container` is a real part now, REQUIRED at every level, and the
// constructor CHECKS that it holds the summary, the toggle and the region
// rather than trusting the caller (see the containment block below). "It wraps
// its content" stops being a sentence in a comment and becomes a throw.
//
// WHAT THE BOX BUYS, stated plainly so nobody has to guess:
//
//   - The level is written ONCE, on the box (data-accordion, and
//     data-accordion-level), instead of on the toggle and the region
//     separately. A disclosure's depth is now a property of the disclosure
//     rather than of two elements inside it, and anything starting from a
//     control finds it with one closest().
//   - A design can compose one. The export's bridge renders the box and puts
//     a summary and a region in it, which is what a `level` prop can mean at
//     all three levels rather than at one (.design-sync's Accordion).
//   - It is provably container-independent. docs/spikes/TOR-222-anywhere/
//     builds three of these on a page with none of torpeek's markup and none
//     of its stylesheets, moves the boxes between parents, and builds one in a
//     DocumentFragment that is never in the document at all.
//
// WHAT IT DOES NOT BUY, and this is the honest half: no rule in any stylesheet
// keys off data-accordion-level today. The rail and the ground are still the
// two rules they were, in the two area files they were in, and they had to be
// - see THE NESTING LEVEL below for why collapsing them would have been
// inventing a scale rather than reading one.
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
// NO LISTENER, and that is not tidiness either - it is what makes the wrapper
// SAFE rather than what would have made it impossible.
//
// The distinction TOR-222 had to draw: "the run level's listener must stay on
// .run-row" blocks the wrapper from OWNING a listener. It never blocked the
// wrapper from EXISTING. Those are different claims and TOR-212's own design
// already had the component own no listener at all, so the wrapper costs
// nothing here.
//
// Each of the three levels activates from a different element, with a
// different exception:
//
//   - the RUN level listens on .run-row, NOT on .run-row-group, because the
//     detail is the row's SIBLING inside the container: on the container,
//     every click inside an open detail - a picker checkbox, a thumbnail,
//     Compare - would collapse it. That is the exact trap the two-<tr>
//     arrangement existed to avoid and TOR-215 wrote down when it built the
//     wrapper. It also stopPropagation()s Cancel and the two priority buttons.
//   - the FILE level listens on the row and excludes events whose target IS
//     the checkbox, because activating a <label> dispatches a second
//     synthetic click that would toggle the detail straight back closed.
//   - the META level listens on the toggle itself and has no exception.
//
// A component that owned the listener would have to learn all three, and the
// first thing it would get wrong is the one that matters: given a container
// and a region, the obvious place to listen is the container. So the listener
// stays with the element it is on, at each level, and this class is only ever
// told the answer.
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
//   1  run   a torrent's line       summary: .run-row      accent ground + a 3px rail
//   2  file  a video file's block   summary: .picker-file            a 2px rail
//   3  meta  the Metadata block     summary: h3.meta-title           nothing
//
// So the level is the depth, and the depth is how loudly the open state is
// dressed: the rail thins 3px -> 2px -> none and the filled ground appears
// once, at the top. The two rails and the one ground are in the `rail` and
// `ground` fields below, and they are not decoration: TestTheLevelsLooksAreThe
// StylesheetsOwn parses the rules that dress a disclosure out of the served
// CSS and requires this table to match them, so the table cannot drift into
// prose the way a comment would.
//
// THE SUMMARY IS WHAT GETS DRESSED, at both levels that dress anything, and
// that is TOR-222's one real change to the markup contract. Level 1 always
// dressed its summary (.run-row); level 2 dressed the CONTAINER (li.picker-
// item) and had the rail painted on its child through
// `.picker-item[data-expanded="true"] > .picker-file` - a rule that names a
// parent to reach a child, which is the coupling this ticket is about. The
// attribute moved onto .picker-file, the selector lost its combinator, and the
// computed box-shadow is the same (measured; nothing else sets box-shadow on
// .picker-file). Two levels, one rule shape: the summary carries the state.
//
// WHY THE TWO RAIL RULES ARE STILL TWO RULES. Collapsing them the way
// .disclosure-mark collapsed the mark was tried on paper and refused. The mark
// consolidated because its three copies were BYTE-IDENTICAL. These two are
// not: level 1 adds a filled ground and a transparent bottom border, level 2
// adds neither, and one shared rule would need three per-level custom
// properties written from JS - more machinery than the two rules it replaced,
// and a scale invented rather than read. The level table pins them instead.
//
// THE MARK DOES NOT VARY WITH THE LEVEL, and that is the other half of the
// finding, preserved from TOR-212 rather than re-derived. All three levels
// drew their triangle with the same three declarations and the same two
// ::before rules, three times over - and each copy's own comment said it was
// deliberately the same object as its neighbour's, because "opening a torrent
// and opening one of its files are the same gesture, so they should not be two
// different marks". One rule now (.disclosure-mark, base.css) and this
// constructor is what puts it on. There is no `mark` FIELD, deliberately: the
// class is applied once at construction and never read back, so keeping a
// reference would be a field that remembers something - exactly what the
// no-state scan below polices.
//
// ---------------------------------------------------------------------------
// STILL NOT A CUSTOM ELEMENT, unlike the six of docs/front-end.md's element
// pattern, and there is now exactly ONE reason left rather than three:
//
//   - THE LAYOUT REASON IS GONE. .run-row-group used to carry
//     `grid-template-columns: subgrid`, which only works on a DIRECT grid item
//     of .run-grid, so an element wrapped around it would have broken the
//     column alignment TOR-214 measured. TOR-221 removed that and measured the
//     removal: per-row grids held every cell to 0.0000px of its header across
//     24 rows with an element inserted between container and rows, where the
//     shared grid drifted 922.3906px.
//   - THE FILE-LEVEL REASON IS GONE TOO, and it was wrong rather than stale:
//     "nothing holds both the toggle and the region" was said of the toggle
//     being inside the row's <label> and the region being the row's sibling.
//     li.picker-item holds both, and always did.
//   - WHAT IS LEFT is that the box must not be a custom element with a
//     lifecycle of its own, because the box is a <li> in a list, a section in
//     a panel and a pair-of-rows wrapper in a grid: three elements the page
//     needs for their own reasons, which this ADOPTS. Making it an element
//     would mean the page could no longer choose the box - and choosing the
//     box is the whole of "usable where we want".
//
// So this is a plain class over a box the page provides, and the element
// pattern's points that still apply are applied: parts are checked by name
// with a loud throw (point 3), and one method for the one thing the outside
// may ask for (point 5). Points 1, 2, 4 and 6 are about elements and have
// nothing to say here.

// The three levels the page has, and the shape of each. Not exported: Claude
// Design's checker indexes a module's UPPERCASE-INITIAL named exports as
// COMPONENTS (TOR-208 found five constants offered to designers as things to
// render, PRIORITY_HIGH the number 1 among them), so this module exports the
// class and nothing else.
//
// `rail` is the inset width the level's open summary wears, or null for a
// level that wears nothing - and it doubles as "does this level dress its
// summary at all", which is why apply() reads it rather than a second flag.
// `ground` is whether the open summary also gets a filled background; it is
// true at the top level only. Both are the served stylesheets' own values, and
// a Go test parses those rules and requires this table to match.
const LEVELS = new Map([
  [1, { name: "run", rail: "3px", ground: true }],
  [2, { name: "file", rail: "2px", ground: false }],
  [3, { name: "meta", rail: null, ground: false }],
]);

export class Accordion {
  // level      1, 2 or 3 - see the block above.
  // container  the BOX: the element that holds the summary and the region and
  //            is neither of them. Checked, not trusted - it must contain all
  //            of summary, toggle and region. This is what makes this a
  //            wrapper rather than a set of attribute writes, and it is the
  //            element a design composes.
  // summary    the header the toggle lives in, and the element the level's
  //            rail is painted on. Required at every level; only levels with a
  //            rail get data-expanded written on it. It may BE the toggle at a
  //            level whose header is nothing but the control.
  // toggle     the control carrying aria-expanded, inside (or equal to) the
  //            summary. Always a real <button> at all three levels, which is
  //            where the keyboard operability comes from: Return and Space
  //            activate it and its click bubbles to whatever listener the
  //            level put on the row.
  // region     the element the toggle opens. Its `hidden` attribute is the
  //            WHOLE of collapse - no rule in any stylesheet sets `display` on
  //            any of the three regions, deliberately, so the UA's own
  //            [hidden] rule is never beaten by a class selector at equal
  //            specificity. That trap has cost this codebase six fixes in one
  //            file.
  // mark       the element wearing the triangle. Defaults to the toggle, which
  //            is right at level 2 (.picker-open IS the mark) and wrong at 1
  //            and 3, where the triangle is a span inside the button so the
  //            label starts at the same x whichever way it points. Not kept as
  //            a field - see the header.
  constructor({ level, container, summary, toggle, region, mark }) {
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
    for (const [name, node] of Object.entries({ container, summary, toggle, region })) {
      if (!node) throw new Error("accordion: level " + shape.name + " has no " + name);
    }

    // THE CONTAINMENT CHECK, and it is the whole of "this wraps its content".
    // A wrapper that is handed a box which does not actually hold its parts is
    // the same loose-elements component TOR-212 shipped, wearing a container
    // argument - and it fails invisibly, because every attribute write still
    // lands. So the shape is checked once, here, where the call site can be
    // fixed.
    //
    // contains() rather than parentElement, and TRANSITIVELY on purpose: at
    // level 2 the region is `.file-detail` inside a <file-detail> element
    // inside the <li>, and at level 3 the toggle is inside an <h3> inside the
    // <section>. A disclosure wraps a subtree, not two direct children.
    if (container === summary || container === region) {
      throw new Error("accordion: level " + shape.name + "'s container is also its " +
        (container === summary ? "summary" : "region") + " - the box has to be a third " +
        "element, or there is nothing wrapping anything");
    }
    for (const [name, node] of Object.entries({ summary, toggle, region })) {
      if (!container.contains(node)) {
        throw new Error("accordion: level " + shape.name + "'s container does not hold its " +
          name + ". This class wraps a summary and a region; a box that does not contain " +
          "them is not the box");
      }
    }
    if (summary !== toggle && !summary.contains(toggle)) {
      throw new Error("accordion: level " + shape.name + "'s toggle is not in its summary. " +
        "The summary is the header the control lives in - and at the levels with a rail it " +
        "is what the rail is painted on, so a control outside it would dress the wrong row");
    }
    // The region must be OUTSIDE the summary, at every level, and this is the
    // one structural rule that is load-bearing rather than tidy: a region
    // inside the element that toggles it collapses on every click within it,
    // which is the same trap the listener note above describes one level up.
    if (summary.contains(region)) {
      throw new Error("accordion: level " + shape.name + "'s region is inside its summary. " +
        "A region inside the thing that toggles it closes on every click in it - the region " +
        "is the summary's SIBLING inside the container at all three levels");
    }

    this.level = level;
    this.container = container;
    this.summary = summary;
    this.toggle = toggle;
    this.region = region;

    // The level, written into the DOM ONCE and on the BOX (TOR-222), rather
    // than on the toggle and the region separately: the depth is a property of
    // the disclosure, and anything holding a control finds it with one
    // closest("[data-accordion-level]"). Readable from the page by a Go test,
    // by a browser drive, and by a design consumer composing one.
    this.container.dataset.accordion = shape.name;
    this.container.dataset.accordionLevel = String(level);
    // The one thing every level draws identically (base.css). classList.add
    // is idempotent, so an element whose markup already carries the class -
    // the export's own preview card, which renders without scripts and has to
    // carry it statically - is unchanged.
    (mark || toggle).classList.add("disclosure-mark");
  }

  // apply writes the open state out. THE ONLY WRITER of these three
  // attributes on this page, and the state arrives as an argument every time -
  // see this module's header for why there is no field to keep it in.
  //
  // The three writes are independent: nothing observes any of them (no
  // MutationObserver on this page watches attributes, and the two that exist
  // watch childList until their element is wired and then disconnect), so
  // this order is stable rather than load-bearing.
  //
  // The level's own table decides whether the summary is dressed - read here
  // rather than cached in a field, so the level stays the one place the answer
  // comes from and there is nothing to keep in step with it.
  apply(expanded) {
    this.toggle.setAttribute("aria-expanded", String(expanded));
    this.region.hidden = !expanded;
    if (LEVELS.get(this.level).rail) {
      this.summary.dataset.expanded = String(expanded);
    }
  }
}
