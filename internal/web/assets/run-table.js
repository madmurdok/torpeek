// THE RUN TABLE, as a custom element (TOR-194).
//
// The third element, and it follows the two before it rather than inventing
// anything: light DOM, the markup left declarative in index.html and WRAPPED,
// parts found with this.querySelector in connectedCallback with a loud throw
// naming any that is missing, and one method per thing the outside is allowed
// to ask for. frame-panel.js's header carries the full reasoning for no shadow
// DOM and for leaving the structure in the page; compare-dialog.js's carries
// the setServices shape this file also needs. Neither is repeated here.
//
// WHAT IT IS, in one line: everything that makes the nine-column table a
// table - which since TOR-215 is a CSS grid rather than a <table> element,
// and the behaviour below did not have to change for it (table.css's own
// banner says why, and docs/front-end.md carries the measurements). The row
// building is div-per-cell now, wireColumnResizers is untouched, and the one
// thing that got SMALLER is the column count: the detail spans its container
// rather than a counted number of cells, so there is nothing to count. The
// six live columns (TOR-139), client-side
// sorting over what state.runs already holds, the queue column and the one
// place its figure comes from (TOR-140, TOR-156), the
// drag-a-border-and-remember-it column widths (TOR-157), the accordion's own
// mechanism (TOR-138) and the detail's width token (TOR-174).
//
// ---------------------------------------------------------------------------
// WHERE THE BOUNDARY IS, because the detail's own row belongs to the table
// structurally and to the detail logically, and the ticket asks for the line
// to be drawn rather than left to be inferred.
//
// THE TABLE OWNS THE ROW. All three elements per entry (TOR-215: the wrapper,
// the torrent's line and the detail's row inside it): the detail's row is a
// row, how far it spans is a fact about the grid, and hiding it is what an
// accordion at this level DOES. So newRow() builds the group and hands back
// every element in it, detailCell among them, and setRunExpanded() is the
// only function that may put one on screen or take it off.
//
// THE PAGE OWNS WHAT THE CELL IS FILLED WITH. Not one thing built into
// detailCell is in this file - not the .run-detail div, not its header, not
// the summary, the top-up block or the file list, and not one of the functions
// that draw them. TOR-195 is the ticket for those, and detailCell is its whole
// seam: it takes the cell it is handed, mounts its own element in it, and this
// file never learns what went in.
//
// TWO THINGS SIT EXACTLY ON THE LINE, and each is split rather than fudged:
//
//   - THE ACCORDION. Its mechanism is three attributes on row elements
//     (detailRowEl.hidden, rowEl.dataset.expanded, rowToggle's aria-expanded)
//     and a width recompute, so it is here. Its POLICY is not: what a click
//     on a row means - open it, or ask the server to replay a disk row first -
//     is app.js's toggleRun, because it is a request. bindRow hands the click
//     out and decides nothing.
//   - THE TOGGLE'S ARIA. aria-expanded is the row's own state and is set
//     here; aria-controls names the detail's id, which only the detail can
//     mint, and is set by the page. One attribute each, on the same button,
//     for the same reason the line falls where it does.
//
// WHAT THAT LEAVES app.js HOLDING is one el. entry queried by tag and four
// methods on it: newRow and bindRow when a run appears, syncRow on every
// redraw, and setRunExpanded from its own toggleRun and began(). And what this
// element may NOT do is reach for a detail: every entry field it writes is a
// row element, which is checkable by reading syncRow rather than by trusting
// this paragraph.
// ---------------------------------------------------------------------------
//
// LISTENERS AND WHAT ACTUALLY HAS TO BE TAKEN OFF AGAIN. The pattern says
// every listener is an instance-bound field so disconnectedCallback can remove
// the same function object, and frame-panel.js's own note says why: a resize
// handler on `window` outlives the element and keeps measuring it. That is the
// reason, and it is what decides which listeners are fields here rather than
// how many there are. Two per header, six per drag handle and four per row is
// a tally that grows with the torrent list, every one of them on a node THIS
// ELEMENT CREATED INSIDE ITSELF - they go when the DOM goes, and holding a
// field for each would be bookkeeping that buys nothing. The two that outlive
// the element ARE fields: the window resize, and the one-second stall ticker -
// which is worse than a leak, because it walks state.runs and writes into
// cells nobody can see.

import {
  PRIORITY_HIGH,
  PRIORITY_LOW,
  arrivalOrdinal,
  availabilityCellText,
  availabilityCellTitle,
  availabilityMetaText,
  availabilityReading,
  badgeLabel,
  badgeState,
  cancellable,
  compareEntries,
  displayName,
  hasLive,
  hasPriority,
  metaLabel,
  metaTitle,
  peersCellText,
  peersCellTitle,
  priorityLabel,
  queueCellMetaText,
  queueCellText,
  queueCellTitle,
  rateCellText,
  rateCellTitle,
  seedsCellText,
  seedsCellTitle,
  state,
  waitingForMetadata,
  whenLabel,
} from "./state.js";
// The disclosure itself (TOR-212), one level up from every other one on the
// page: the three DOM writes that open a row are its, and everything opening
// a row MEANS - the width recheck and the top-up read below - stays here.
import { Accordion } from "./accordion.js";

// THE FOUR SERVICES THE PAGE INJECTS, in the shape events.js's setView and
// compare-dialog.js's setServices already use, and for the same reason: each
// one is something only app.js's bootstrap can do, and a missing name has to
// fail while the wiring is being written rather than at the first click on a
// row.
//
//   toggleRun    what a click on a row means. A live row opens and closes; a
//                disk-only row is asked to be replayed first, which is a POST.
//   cancelRun    the ✕ in the actions cell. A POST.
//   setPriority  the ▲/▼ beside it. A POST, and the level is computed against
//                what the page was last told.
//   detailShown  what the page does when a detail comes on screen (today
//                TOR-152's refreshAgain, reading a row's top-up standing off
//                disk). Named for the event rather than for the function,
//                because the function is the detail's and the detail is
//                TOR-195's - the name here should not have to change when it
//                moves again.
//
// Undefined until setServices runs, which is checked there rather than at each
// call site.
let toggleRun = null;
let cancelRun = null;
let setPriority = null;
let detailShown = null;

const SERVICES = ["toggleRun", "cancelRun", "setPriority", "detailShown"];

function setServices(services) {
  for (const name of SERVICES) {
    if (typeof services[name] !== "function") {
      throw new Error("run-table: setServices needs a " + name + "() function");
    }
  }
  ({ toggleRun, cancelRun, setPriority, detailShown } = services);
}

// ---------------------------------------------------------------------------
// THE SIX LIVE COLUMNS (TOR-139): peers, seeds, both rates, availability and
// queue position, added to the table TOR-137 moved into the right pane and
// TOR-138 made expandable.
//
// LIVE_COLUMNS builds both the header cells (below) and, by the same keys,
// state.js's sortValue() switch - one list rather than two, so a column
// added here cannot forget to be wired into sorting or the reverse. The two
// halves now sit in two files, which is why each has a test naming the other
// (TestLiveColumnsAreWiredIntoBothHeadersAndSorting).
// unit, when present, is shown on its own line under the label: the
// availability column needs it (its figure is copies per piece, not a
// percentage, and commonly exceeds 1.0) to be legible without a tooltip
// nobody opens, per this ticket's own acceptance criterion.
const LIVE_COLUMNS = [
  { key: "peers", label: "Peers",
    title: "Connected peers. A dash means this torrent has no client (queued, needs-action, or a row read off disk) - not zero peers." },
  { key: "seeds", label: "Seeds",
    title: "Connected seeds. A dash means no client, not zero seeds." },
  { key: "download_bps", label: "Down",
    title: "Download speed. A dash means no reading yet - before a run's second heartbeat a rate cannot be computed - never 0 B/s." },
  { key: "upload_bps", label: "Up",
    title: "Upload speed. A dash means no reading yet, never 0 B/s." },
  { key: "availability", label: "Avail", unit: "copies/piece",
    title: "Swarm availability, in copies per piece - not a percentage. Below 1.0 means pieces are missing from the swarm; above 1.0 (commonly) means it is healthy. A dash means this torrent has not been asked for bytes yet, so nothing has reported what the swarm holds." },
  { key: "priority", label: "Queue",
    title: "The order torrents were added to this server: 1 is the first one you added, and the number never changes - not when a torrent finishes, not when you reprioritise it, not when you pick its files. Under it, while a row is still waiting, is its place in the queue as the server actually holds it (\"#2\" means one torrent is ahead of it), which is the number that moves. Use ▲ and ▼ at the end of a waiting row to change that; priority orders the torrents that are WAITING and never interrupts one that is already downloading. A dash means this row was added by an earlier run of the server, so this session never gave it a number - the Added column is what dates it." },
];

// MAX_PROGRESS_SEGMENTS is where one segment per frame stops fitting in a
// panel row. A plan of twenty is the common case and draws one each; a plan of
// two hundred would ask for segments a third of a pixel wide.
const MAX_PROGRESS_SEGMENTS = 24;

// ---------------------------------------------------------------------------
// Column widths: a drag-a-border-and-remember-it pattern, once per column -
// one localStorage entry holding a { key: px, ... } map rather than nine
// separate ones, since a stale-column check (below) needs to see the whole
// set at once to decide what to drop.
//
// Every key here is a column's own data-sort value - this.sortHeaders already
// is exactly the nine resizable headers (name/when/status plus the six
// LIVE_COLUMNS keys; the unlabelled actions column has no data-sort and
// keeps the plain 3.8rem table.css always gave it, see .run-actions-header) -
// so this reuses it rather than keeping a second list of column names that
// could drift from the first, the same reason LIVE_COLUMNS itself is one
// list rather than two.
//
// localStorage is read through a try/catch on purpose, the same way the left
// panel's own width was before TOR-168 removed the panel: it throws in a
// private window or with site data blocked, and a stored value can also
// simply be malformed JSON from a build that wrote it differently. Either way
// the page must still render at the defaults tokens.css declares (--col-w-name
// and its siblings) rather than break.
const COLUMN_WIDTHS_KEY = "torpeek.columnWidths";
const COLUMN_MIN_WIDTH = 44;
const COLUMN_MAX_WIDTH = 640;

// "NOT YET" IS NOT "NEVER" (TOR-205). frame-panel.js's own block carries the
// full reasoning for the mechanism below (wire/awaitParts/partsNeverArrived)
// and for this deadline's value, not repeated here.
const SETTLE_TIMEOUT_MS = 10000;

function clampColumnWidth(px) {
  return Math.min(COLUMN_MAX_WIDTH, Math.max(COLUMN_MIN_WIDTH, px));
}

class RunTable extends HTMLElement {
  constructor() {
    super();

    // The two listeners that outlive the element, bound once and kept so
    // disconnectedCallback removes the same function objects addEventListener
    // was given (see this module's own header for why these two and not the
    // hundreds inside).
    //
    // The resize handler is why syncRunDetailWidth exists at all: the fit
    // between the pane and the open detail is recomputed from three named
    // causes, and the window changing is the obvious one.
    this.onResize = () => this.syncRunDetailWidth();
    this.onStallTick = () => this.refreshStallDurations();
    // The interval's handle, so it can be stopped. null when it is not running,
    // which is the same null-until-started shape the drag state uses.
    this.stallTimer = null;
    // Filled in by wire(): the parts and the widths a drag has produced.
    this.columnWidths = {};

    // wired, partsObserver and partsDeadline are wire()'s own bookkeeping
    // (TOR-205) - see frame-panel.js for the full reasoning, and its wire()
    // for the shape this one repeats.
    this.wired = false;
    this.partsObserver = null;
    this.partsDeadline = null;
  }

  connectedCallback() {
    this.wire();
  }

  // wire finds this element's parts and, if every one exists, wires it -
  // what connectedCallback used to do inline, until a part missing meant a
  // quiet bail into awaitParts() instead of a throw (TOR-205; frame-panel.js
  // carries the reasoning). Idempotent: awaitParts()'s observer calls this
  // again on every mutation until it succeeds, and it must do nothing after
  // that.
  wire() {
    if (this.wired) return;

    // Found inside THIS element rather than by document id, so a second table
    // could not steal the first one's parts. The ids stay on the markup for
    // the Go tests, for #run-list-empty's own rule and for the aria wiring.
    //
    // THE HEADER BAND IS AN ELEMENT AGAIN SINCE TOR-221, and this is the only
    // shape change the ticket cost this file. TOR-215 had made the whole table
    // one grid, so the ten header cells were the grid's own first ten items
    // and there was no header row element to find; now the columns are a grid
    // PER ROW (table.css's .run-grid-head-row and .run-row, both reading
    // tokens.css's one --run-tracks token), so the cells sit inside the band
    // that lays them out. this.grid stays the outer box - it is what
    // syncRunDetailWidth measures the pane against - and this.headRow is what
    // buildLiveColumnHeaders inserts into.
    this.grid = this.querySelector("#run-table");
    this.headRow = this.querySelector(".run-grid-head-row");
    this.actionsHeader = this.querySelector(".run-grid-head-row > .run-actions-header");
    this.list = this.querySelector("#run-list");
    this.emptyNote = this.querySelector("#run-list-empty");
    // No id on this one in index.html - it is the scroll wrapper, not a
    // control, and TOR-174's syncRunDetailWidth is the only thing that reads
    // it.
    this.wrap = this.querySelector(".run-table-wrap");

    // buildLiveColumnHeaders used to `return` on a missing header row, which
    // meant a page that had lost its headers rendered a three-column table
    // and said nothing. Every part below is still checked for absence - but a
    // part missing here is no longer necessarily THAT bug (TOR-205): it may
    // be a subtree still arriving, so a miss bails quietly into awaitParts()
    // rather than throwing on the spot.
    const missing = Object.entries({
      grid: this.grid,
      headRow: this.headRow,
      actionsHeader: this.actionsHeader,
      list: this.list,
      emptyNote: this.emptyNote,
      wrap: this.wrap,
    }).filter(([, node]) => !node).map(([name]) => name);

    if (missing.length) {
      this.awaitParts(missing);
      return;
    }
    this.stopAwaitingParts();
    this.wired = true;

    // IN THIS ORDER, and the order is the whole of what keeps the three counts
    // below agreeing: the six live columns are built first, and only then is
    // anything counted or collected. sortHeaders is a static NodeList, so one
    // taken before the build would hold the three headers index.html ships
    // with and every column past them would be unsortable and unresizable.
    this.buildLiveColumnHeaders();
    // A DIRECT-CHILD SELECTOR, on the band rather than on #run-table since
    // TOR-221: the header cells are the band's own children now. Direct on
    // purpose - a descendant selector here would also collect any [data-sort]
    // a row's cell ever grew, and this NodeList is what gets a sort listener
    // and a drag handle each.
    this.sortHeaders = this.querySelectorAll(".run-grid-head-row > [data-sort]");
    // NO COLUMN COUNT IS KEPT ANY MORE, and that is TOR-215's doing rather
    // than an omission. Until the grid, this line read the header count off
    // the DOM (`querySelectorAll("#run-table thead th").length`) so the detail
    // row's colSpan could be set from it - a literal would have gone wrong
    // silently the moment TOR-139 added six columns, leaving an empty cell at
    // the end of the detail row and the detail a column narrow. Neither shape
    // since needs a count: under TOR-215's one grid the detail spanned
    // `1 / -1`, and since TOR-221 the row's grid ends at the row, so the
    // detail is an ordinary block as wide as its container (table.css). The
    // thing the count existed to keep correct is no longer a thing that can
    // be wrong.

    this.wireSorting();
    // columnWidths holds only the entries a drag (or a valid stored value) has
    // actually produced - the null-until-touched shape the left panel's own
    // width had before TOR-168 removed it, kept as a map here instead of a
    // single value because saving has to write back the whole set, not just
    // the one column that just moved.
    this.columnWidths = this.loadColumnWidths();
    for (const [key, px] of Object.entries(this.columnWidths)) this.applyColumnWidth(key, px);
    this.wireColumnResizers();
    // The default sort ("when", newest first - state.js's own state.sort) said
    // out loud on the header that holds it. index.html ships that one header
    // with aria-sort="descending" so the page is correct before this runs; this
    // is what keeps it correct after the first click on another column.
    this.updateSortIndicators();

    window.addEventListener("resize", this.onResize);
    // TOR-141's ticker, started here rather than at module scope so it cannot
    // outlive the table it redraws.
    this.stallTimer = setInterval(this.onStallTick, 1000);
  }

  // awaitParts/stopAwaitingParts/partsNeverArrived: the same three as
  // frame-panel.js's, for the same reason - see its own copy for the
  // reasoning behind each.
  awaitParts(missing) {
    if (this.partsObserver) return;
    console.error("run-table: waiting for " + missing.join(", ") +
      " to appear inside the element - connected before its subtree finished arriving");
    this.partsObserver = new MutationObserver(() => this.wire());
    this.partsObserver.observe(this, { childList: true, subtree: true });
    this.partsDeadline = setTimeout(() => this.partsNeverArrived(), SETTLE_TIMEOUT_MS);
  }

  stopAwaitingParts() {
    if (this.partsObserver) {
      this.partsObserver.disconnect();
      this.partsObserver = null;
    }
    if (this.partsDeadline != null) {
      clearTimeout(this.partsDeadline);
      this.partsDeadline = null;
    }
  }

  partsNeverArrived() {
    this.partsDeadline = null;
    this.wire();
    if (this.wired) return;

    const missing = Object.entries({
      grid: this.querySelector("#run-table"),
      headRow: this.querySelector(".run-grid-head-row"),
      actionsHeader: this.querySelector(".run-grid-head-row > .run-actions-header"),
      list: this.querySelector("#run-list"),
      emptyNote: this.querySelector("#run-list-empty"),
      wrap: this.querySelector(".run-table-wrap"),
    }).filter(([, node]) => !node).map(([name]) => name);
    this.stopAwaitingParts();
    const message = "run-table: no " + missing.join(", ") + " inside the element, " +
      (SETTLE_TIMEOUT_MS / 1000) + "s after connecting - giving up rather than waiting forever";
    console.error(message);
    throw new Error(message);
  }

  disconnectedCallback() {
    window.removeEventListener("resize", this.onResize);
    clearInterval(this.stallTimer);
    this.stallTimer = null;
    // A table removed from the page while still waiting for its subtree has
    // nothing left to wire - and an observer left running on a detached
    // element would keep firing for nothing.
    this.stopAwaitingParts();
  }

  // buildLiveColumnHeaders inserts the six header cells straight into the
  // grid, before the (headerless) actions column, so this.sortHeaders' own
  // querySelectorAll sees them without index.html ever naming them by hand -
  // this file owns every column past the three the page shipped with (name,
  // added, status).
  //
  // A DIV PER HEADER SINCE TOR-215, not a <th>, and every attribute the <th>
  // carried is carried here unchanged. Three of them do real work on any
  // element: tabIndex and role="button" are what make sorting reachable
  // without a mouse, and aria-sort is rewritten on every sort. `scope` does
  // not - it has no meaning outside a table and no ARIA mapping of its own -
  // and is set anyway, deliberately: TOR-213 recorded the attribute set this
  // markup has, front-end.md says the screen-reader question is to be
  // REOPENED rather than quietly discarded, and dropping the attribute here
  // would be the first half of a redesign nobody has measured. index.html
  // says the same next to the three headers it ships.
  buildLiveColumnHeaders() {
    const frag = document.createDocumentFragment();
    for (const col of LIVE_COLUMNS) {
      const head = document.createElement("div");
      head.setAttribute("scope", "col");
      head.tabIndex = 0;
      head.setAttribute("role", "button");
      head.setAttribute("aria-sort", "none");
      head.dataset.sort = col.key;
      head.className = "run-grid-head run-cell-metric-header" +
        (col.key === "availability" ? " run-cell-availability-header" : "") +
        (col.key === "priority" ? " run-cell-queue-header" : "");
      head.title = col.title;
      const label = document.createElement("span");
      label.className = "run-th-label";
      label.textContent = col.label;
      head.append(label);
      if (col.unit) {
        const unit = document.createElement("span");
        unit.className = "run-th-unit";
        unit.textContent = col.unit;
        head.append(unit);
      }
      frag.append(head);
    }
    this.headRow.insertBefore(frag, this.actionsHeader);
  }

  // wireSorting makes every header a sort control - a click, and Enter or
  // space for anyone reaching it by keyboard, which is why each header cell
  // carries tabindex and role=button (buildLiveColumnHeaders above, and
  // index.html for the three it ships). One pass over this.sortHeaders, so a
  // header that exists is sortable by construction.
  wireSorting() {
    for (const head of this.sortHeaders) {
      head.addEventListener("click", () => this.setSort(head.dataset.sort));
      head.addEventListener("keydown", (event) => {
        if (event.key !== "Enter" && event.key !== " ") return;
        event.preventDefault();
        this.setSort(head.dataset.sort);
      });
    }
  }

  // reorderRuns moves every row's existing element into sorted order without
  // rebuilding anything - appendChild on a node already in the table just
  // relocates it, so a row that has not moved costs a no-op reflow, never a
  // rebuild. Called whenever a row is added or anything a sort key reads
  // (name, status, when) changes, so the table always reflects the active
  // sort - including under the default "when" sort, where a status change
  // never touches when, so re-running this never moves that row.
  //
  // ONE ELEMENT PER ENTRY TO MOVE, since TOR-215. TOR-138 gave an entry two
  // adjacent <tr>s - its own line and the row its detail renders in - and
  // this loop had to append both at once, in that order, because nothing
  // owned the pair: a re-sort that moved one and not the other would have
  // detached a detail from its torrent. Now rowGroupEl IS the pair, so the
  // ordering is not something this function has to get right; there is no
  // arrangement of one element that can separate them.
  reorderRuns() {
    const rows = Array.from(state.runs.values()).sort(compareEntries);
    for (const entry of rows) this.list.append(entry.rowGroupEl);
  }

  updateSortIndicators() {
    for (const head of this.sortHeaders) {
      if (head.dataset.sort === state.sort.key) {
        head.setAttribute("aria-sort", state.sort.dir === "asc" ? "ascending" : "descending");
      } else {
        head.setAttribute("aria-sort", "none");
      }
    }
  }

  // setSort is what clicking (or activating with the keyboard) a column header
  // does: the same column reverses direction, a different one is sorted
  // ascending - except "when", which starts descending (newest first), the
  // same default the table opens with, since that is the more useful way to
  // first look at dates.
  setSort(key) {
    if (state.sort.key === key) {
      state.sort.dir = state.sort.dir === "asc" ? "desc" : "asc";
    } else {
      state.sort.key = key;
      state.sort.dir = key === "when" ? "desc" : "asc";
    }
    this.updateSortIndicators();
    this.reorderRuns();
  }

  // ---------------------------------------------------------------------------
  // Run entries: one per torrent, live or on disk. Each owns TWO adjacent rows
  // in the torrent table - its own line, and the row its detail renders in
  // directly beneath it (TOR-138) - both built once and updated in place.
  // Opening or closing a torrent never rebuilds anything; it only shows and
  // hides what is already there, exactly as showing one of several detail panes
  // used to.
  //
  // THREE ELEMENTS PER ENTRY SINCE TOR-215, and the middle one is the whole
  // reason that ticket exists:
  //
  //   .run-row-group  the wrapper, a plain block around both
  //     .run-row        the torrent's own line: ten cells, and its OWN grid
  //     .run-detail-row the detail, a block as wide as the row above it
  //
  // ALL THREE ARE PLAIN BLOCKS EXCEPT THE ROW SINCE TOR-221. Under TOR-215
  // the wrapper and the row were both subgrids of one table-wide grid, which
  // meant the wrapper had to be a DIRECT grid item of it and nothing could be
  // inserted between them; now the row carries the ten tracks itself (from
  // tokens.css's --run-tracks) and the other two need no layout rule at all.
  //
  // A <table> could not have the wrapper. An unknown element written between
  // <tbody> and <tr> is hoisted out in front of the whole table and left
  // empty - TOR-214 measured it - so the row and its detail were two sibling
  // <tr>s with nothing owning both, which is what blocked TOR-212.
  //
  // WHY THE DETAIL IS STILL NOT INSIDE .run-row. Exactly the reason it was
  // not inside the first <tr>: a detail under the row's own click listener
  // would collapse when anything in it is used - a picker checkbox, a
  // thumbnail - because the click bubbles to the row's handler. The listener
  // is on .run-row (bindRow), and the detail is its SIBLING inside the
  // wrapper, so using a detail still cannot close it. Two sibling <tr>s gave
  // that separation for free; inside one wrapper it has to be an element, and
  // that is what .run-row is now.

  newRow() {
    const group = document.createElement("div");
    group.className = "run-row-group";

    const row = document.createElement("div");
    row.className = "run-row";

    const nameCell = document.createElement("div");
    nameCell.className = "run-cell run-cell-name";
    const main = document.createElement("button");
    main.type = "button";
    main.className = "run-row-main";
    // The same disclosure triangle a video file's own row wears one level down
    // (.picker-open, since TOR-182), for the same reason: an accordion that
    // gives no sign it opens is a table. Since TOR-212 it is literally the
    // same object rather than a matching one: the class stays as this level's
    // own name, and .disclosure-mark - added by the Accordion below - is the
    // one rule that draws all three.
    const icon = document.createElement("span");
    icon.className = "run-toggle-icon";
    icon.setAttribute("aria-hidden", "true");
    const name = document.createElement("span");
    name.className = "run-name";
    main.append(icon, name);
    nameCell.append(main);
    // Closed to begin with. Its aria-controls is set by the page instead,
    // because the id it has to name belongs to the detail this row opens
    // onto - the one attribute of this row that TOR-195's half decides.
    main.setAttribute("aria-expanded", "false");

    const whenCell = document.createElement("div");
    whenCell.className = "run-cell run-cell-when";

    const statusCell = document.createElement("div");
    statusCell.className = "run-cell run-cell-status";
    const badge = document.createElement("span");
    badge.className = "run-badge";
    const meta = document.createElement("span");
    meta.className = "run-meta";
    // Progress as a graphic (TOR-123), directly under the line that says what
    // it is counting - the bar is the glance and the text is the number.
    const bar = document.createElement("span");
    bar.className = "run-progress";
    bar.hidden = true;
    statusCell.append(badge, meta, bar);

    // The six live columns (TOR-139), in the same order as LIVE_COLUMNS'
    // headers above. peers/seeds/down/up are one text node each; availability
    // carries a second, muted line for "N unavailable" the same way the status
    // cell's own badge carries .run-meta under it.
    const peersCell = document.createElement("div");
    peersCell.className = "run-cell run-cell-metric run-cell-peers";
    const seedsCell = document.createElement("div");
    seedsCell.className = "run-cell run-cell-metric run-cell-seeds";
    const downCell = document.createElement("div");
    downCell.className = "run-cell run-cell-metric run-cell-down";
    const upCell = document.createElement("div");
    upCell.className = "run-cell run-cell-metric run-cell-up";
    const availCell = document.createElement("div");
    availCell.className = "run-cell run-cell-metric run-cell-availability";
    const availValue = document.createElement("span");
    availValue.className = "run-cell-availability-value";
    const availMeta = document.createElement("span");
    availMeta.className = "run-meta run-cell-availability-meta";
    availCell.append(availValue, availMeta);
    // The queue cell is two lines, the same shape the availability cell uses:
    // the position on top, the priority level under it when it is not the
    // default (TOR-140). It stays a pure FIGURE column - the two buttons that
    // change the level live in the actions cell below, beside Cancel, because
    // .run-cell-metric's own rule in table.css is "narrow, monospace,
    // tabular-nums, right-aligned, figures meant to be compared straight down
    // a column", and putting controls in one would break that for every cell
    // in the row.
    const queueCell = document.createElement("div");
    queueCell.className = "run-cell run-cell-metric run-cell-queue";
    const queueValue = document.createElement("span");
    queueValue.className = "run-cell-queue-value";
    const queueMeta = document.createElement("span");
    queueMeta.className = "run-meta run-cell-queue-meta";
    queueCell.append(queueValue, queueMeta);

    const actionsCell = document.createElement("div");
    actionsCell.className = "run-cell run-cell-actions";
    // The two verbs a WAITING torrent has, in the column the row's verbs
    // already live in: move it up the queue, move it down. Absent - not
    // disabled - for every row the queue has nothing to say about, the same
    // way Cancel is absent on a finished one: a control that cannot do
    // anything is worse than no control, because it invites the click.
    const raise = document.createElement("button");
    raise.type = "button";
    raise.className = "run-priority run-priority-up";
    raise.textContent = "▲";
    raise.hidden = true;
    const lower = document.createElement("button");
    lower.type = "button";
    lower.className = "run-priority run-priority-down";
    lower.textContent = "▼";
    lower.hidden = true;
    const cancel = document.createElement("button");
    cancel.type = "button";
    cancel.className = "run-cancel";
    cancel.title = "Cancel";
    cancel.textContent = "✕";
    cancel.hidden = true;
    actionsCell.append(raise, lower, cancel);

    row.append(nameCell, whenCell, statusCell, peersCell, seedsCell, downCell, upCell, availCell, queueCell, actionsCell);

    // The detail's own row, and the ONE thing collapse touches: its `hidden`
    // attribute, nothing else. Same rule the file and metadata accordions
    // already follow - no rule in detail.css or table.css sets `display` on
    // .run-detail-row (table.css gives it grid-column and nothing else), so
    // the UA's own [hidden] rule is never beaten by a class selector at equal
    // specificity. That trap has already cost this codebase twice (see
    // .drop-overlay[hidden] in base.css, and the corner-bracket gate
    // detail.css once carried), and the gate itself is gone now: there is no
    // .detail-empty to gate on any more, because a torrent that is not open
    // simply has no detail on screen.
    //
    // NO colSpan SINCE TOR-215, and since TOR-221 not even a grid-column:
    // the row's grid ends at the row, so this is an ordinary block and is as
    // wide as the wrapper it sits in - which is as wide as every row. There
    // is no count to write, keep or get wrong, and nothing to keep it in step
    // with the number of columns.
    const detailRow = document.createElement("div");
    detailRow.className = "run-detail-row";
    detailRow.hidden = true;
    const detailCell = document.createElement("div");
    detailCell.className = "run-detail-cell";
    detailRow.append(detailCell);

    group.append(row, detailRow);
    this.list.append(group);
    this.emptyNote.hidden = true;

    // THE DISCLOSURE, at the outermost of the page's three levels (TOR-212).
    // Built here, with the row, because every part it needs was just created -
    // and it is what setRunExpanded writes THROUGH from now on, rather than
    // repeating three attribute writes that also exist twice in
    // file-detail.js.
    //
    // The mark is the icon span rather than the button, because at this level
    // the triangle is a fixed-width box INSIDE the label so the name starts at
    // the same x whichever way it points; at the file level the button is the
    // mark. `dressed` is the row and not the wrapper: an open torrent is a
    // state of its LINE (table.css's .run-row[data-expanded="true"] paints the
    // ground and the 3px rail), and dressing the wrapper would paint the
    // detail too.
    const accordion = new Accordion({
      level: 1,
      toggle: main,
      region: detailRow,
      mark: icon,
      dressed: row,
    });

    // WHAT THE PAGE IS HANDED BACK, and the boundary this ticket had to draw:
    // every element of a row - the wrapper, the detail's own row and the
    // spanning cell inside it INCLUDED - and not one thing built into that
    // cell. The table owns the row; TOR-195 owns what the row opens onto, and
    // detailCell is the seam between them (see this module's own header).
    //
    // Returned rather than kept in a map of the element's own, because an entry
    // IS the shared record: state.js's newRunState, this literal and app.js's
    // detail half are one object by design (TestNoRunEntryFieldIsDeclaredTwice
    // scans all three), and a private map would have to be re-keyed every time
    // claimReopenedRun swaps a run's id.
    return {
      rowGroupEl: group,
      rowEl: row, rowBadge: badge, rowName: name, rowMeta: meta, rowProgress: bar,
      rowWhen: whenCell, rowCancel: cancel, rowToggle: main,
      rowPeers: peersCell, rowSeeds: seedsCell, rowDown: downCell, rowUp: upCell,
      rowAvail: availValue, rowAvailMeta: availMeta, rowAvailCell: availCell,
      rowQueue: queueValue, rowQueueMeta: queueMeta, rowQueueCell: queueCell,
      rowRaise: raise, rowLower: lower,
      detailRowEl: detailRow,
      // The row's disclosure (TOR-212), on the entry for the same reason every
      // other row part is: the table re-sorts and MOVES rows on every redraw,
      // and a private map keyed by id would have to be re-keyed every time
      // claimReopenedRun swaps a run's. It holds no open state - entry.expanded
      // is still the only record of that (accordion.js's own header).
      rowAccordion: accordion,
      // The mount point, and the only reason the page is handed a cell at all.
      detailCell,
    };
  }

  // bindRow is the second half of one act, and it is separate for a reason a
  // reader will otherwise look for: these four listeners close over the ENTRY,
  // and the entry does not exist when newRow() runs - the page builds it out of
  // state.js's newRunState, this element's row parts and its own detail half,
  // in that order. So the row is created first and bound to its record second.
  //
  // Three of the four are handed straight back out to the page (toggleRun,
  // cancelRun, setPriority): what a click MEANS for a run is a request or a
  // reopen, and both belong to the bootstrap that holds the token. What stays
  // here is which element carries which gesture.
  bindRow(entry) {
    // One listener on the row, not the name button alone: a click anywhere in
    // the row toggles it (a table row is a natural click target), and a
    // keyboard activation of the name button still reaches it too, since a
    // button's click event bubbles the same way a mouse click does. The cancel
    // button stops its own click from bubbling here, so a cancel never also
    // opens the row it sits in.
    //
    // The detail's row carries no listener at all, and the listener going on
    // .run-row rather than on .run-row-group is what keeps that worth
    // something: the detail is the row's SIBLING inside the wrapper, not its
    // descendant, so everything inside a detail - a picker checkbox, a
    // thumbnail, Compare - is outside this listener and using the detail
    // cannot close it. Put this on the wrapper instead and every click in an
    // open detail would collapse it (see newRow's own note).
    entry.rowEl.addEventListener("click", () => toggleRun(entry));
    entry.rowCancel.addEventListener("click", (event) => {
      event.stopPropagation();
      cancelRun(entry.id);
    });
    // Same stopPropagation the cancel button needs, for the same reason:
    // reordering the queue must not also open or close the row it was done
    // from - a person moving three torrents around would otherwise leave three
    // details expanded behind them.
    entry.rowRaise.addEventListener("click", (event) => {
      event.stopPropagation();
      setPriority(entry, entry.priority + 1);
    });
    entry.rowLower.addEventListener("click", (event) => {
      event.stopPropagation();
      setPriority(entry, entry.priority - 1);
    });
  }

  // syncRow redraws one torrent's own line, and it is the row half of what
  // used to be one syncEntry: app.js calls this, then draws the detail the row
  // opens onto. Every value it writes is read off the entry through a state.js
  // derivation - not one of them comes from a message - which is the rule the
  // whole three-file split rests on and the reason this method takes a state
  // object and nothing else.
  //
  // IT ENDS WITH A RE-SORT, in the same place the single function put it: a
  // name, a status or a date changing can move this row, and reorderRuns is a
  // relocation of nodes already in the table rather than a rebuild, so paying
  // it on every event costs a no-op reflow for a row that has not moved.
  //
  // ABSENT IS NOT ZERO runs through the whole of the live half below. Every
  // figure's TEXT comes from the state.js helper that knows how to say "no
  // reading", and every one of the six cells carries its own data-absent
  // decided by a real absence test - because a queued row showing 0 peers at
  // 0 KB/s is indistinguishable from a running torrent that found nobody, and
  // those are opposite situations (internal/web/listing.go keeps the tally).
  syncRow(entry) {
    entry.rowEl.dataset.state = badgeState(entry);
    entry.rowBadge.textContent = badgeLabel(entry);
    entry.rowBadge.dataset.state = badgeState(entry);
    // The cell truncates, so the whole name has to be reachable some other way
    // than by widening the panel - a tooltip costs nothing and answers "which
    // Sintel is this" without moving the divider.
    const shown = displayName(entry);
    entry.rowName.textContent = shown.text;
    entry.rowName.title = shown.provisional
      ? shown.text + " — from the magnet link, not yet confirmed by the torrent's own metadata"
      : shown.text;
    // run-name-provisional is the visible marker TOR-117 requires: a dn=-
    // derived name must never read the same as a confirmed one. The rule this
    // toggles styles by lives beside the row cells it is scoped next to in
    // table.css, not duplicated here.
    entry.rowName.classList.toggle("run-name-provisional", shown.provisional);
    entry.rowMeta.textContent = metaLabel(entry);
    entry.rowMeta.title = metaTitle(entry);
    // Styling hook for table.css - a stalled or still-waiting-on-metadata row
    // reads in the same amber the queue and "needs a decision" already use
    // (--warn), so it does not look like the same plain, quiet text a normal
    // "4/20 frames" or "queued" line does. See TestEveryColourComesFromAToken:
    // the colour itself lives in tokens.css's :root, never here.
    entry.rowMeta.dataset.stall = String(!!entry.stall || waitingForMetadata(entry));
    this.renderRunProgress(entry);
    entry.rowWhen.textContent = whenLabel(entry.when);
    entry.rowWhen.title = entry.when ? new Date(entry.when).toString() : "";
    entry.rowCancel.hidden = entry.disk || !cancellable(entry.state);

    // The six live columns (TOR-139). Each pair of lines below is a text and a
    // title, and every one of them can legitimately be ABSENT rather than a
    // number - see this block's own helpers (peersCellText and friends,
    // state.js, beside sortValue) for what decides which.
    entry.rowPeers.textContent = peersCellText(entry);
    entry.rowPeers.title = peersCellTitle(entry);
    entry.rowPeers.dataset.absent = String(!hasLive(entry));
    entry.rowSeeds.textContent = seedsCellText(entry);
    entry.rowSeeds.title = seedsCellTitle(entry);
    entry.rowSeeds.dataset.absent = String(!hasLive(entry));
    const downBps = hasLive(entry) ? entry.live.download_bps : null;
    entry.rowDown.textContent = rateCellText(downBps);
    entry.rowDown.title = rateCellTitle(entry, downBps, "Download speed");
    entry.rowDown.dataset.absent = String(downBps == null);
    const upBps = hasLive(entry) ? entry.live.upload_bps : null;
    entry.rowUp.textContent = rateCellText(upBps);
    entry.rowUp.title = rateCellTitle(entry, upBps, "Upload speed");
    entry.rowUp.dataset.absent = String(upBps == null);
    entry.rowAvail.textContent = availabilityCellText(entry);
    entry.rowAvailMeta.textContent = availabilityMetaText(entry);
    entry.rowAvailCell.title = availabilityCellTitle(entry);
    entry.rowAvailCell.dataset.absent = String(!availabilityReading(entry));
    // The queue column, and the two controls that change it (TOR-140). Note
    // what is NOT here any more: a pass over every other queued row to repaint
    // its rank. TOR-139 needed one, because dequeuing #1 silently made #2 into
    // #1 and only this page knew it; now the server publishes a run_state to
    // every row whose position moved (server.go's queueRecordsLocked), so each
    // row's own sync is the whole of it and no row's cell depends on another
    // row's last update being right.
    entry.rowQueue.textContent = queueCellText(entry);
    entry.rowQueueMeta.textContent = queueCellMetaText(entry);
    entry.rowQueueCell.title = queueCellTitle(entry);
    // absent tracks the FIGURE, which since TOR-156 is the arrival ordinal -
    // so the cell is only dimmed for a row this session never numbered, not
    // for every row that happens not to be waiting. That was the visible half
    // of the complaint: at queue width 5 almost nothing waits, so almost every
    // cell was dimmed and the column read as broken.
    entry.rowQueueCell.dataset.absent = String(arrivalOrdinal(entry) === null);
    const canReorder = !entry.disk && hasPriority(entry);
    entry.rowRaise.hidden = !canReorder;
    entry.rowLower.hidden = !canReorder;
    if (canReorder) {
      // Disabled at the ends of the band rather than hidden there: a button
      // that vanishes when you reach the top makes the pair jump sideways
      // under the cursor, and the row is the one place a person is aiming.
      entry.rowRaise.disabled = entry.priority >= PRIORITY_HIGH;
      entry.rowLower.disabled = entry.priority <= PRIORITY_LOW;
      entry.rowRaise.title = entry.rowRaise.disabled
        ? "already at high priority - the front of the queue"
        : "move up the queue (to " + priorityLabel(entry.priority + 1) + " priority)";
      entry.rowLower.title = entry.rowLower.disabled
        ? "already at low priority - behind everything else waiting"
        : "move down the queue (to " + priorityLabel(entry.priority - 1) + " priority)";
      entry.rowRaise.setAttribute("aria-label", "Move this torrent up the queue");
      entry.rowLower.setAttribute("aria-label", "Move this torrent down the queue");
    }

    this.reorderRuns();
  }

  // renderRunProgress draws a run's progress as a segmented bar in its row
  // (TOR-123). Segments with gaps rather than a smooth fill, because torpeek
  // deals in discrete captures and a percentage would be a shape borrowed from
  // software that deals in bytes.
  //
  // WHAT IT MEASURES: frames landed out of frames planned, which is the only
  // denominator known from the first moment and the only one that answers "how
  // much is left". Pieces would answer "is it moving" better - a run can sit at
  // 4 of 20 frames while steadily pulling data - but the claimed-piece figures
  // do not travel live: they reach core.Done and the run record, not the
  // progress heartbeat. Choosing them would have meant inventing a measurement
  // to draw, and the bar sits directly beneath the line reading "4/20 frames",
  // which is what keeps a stalled bar legible as a slow frame rather than as a
  // bar measuring the wrong thing. The bytes and peers on the file block's own
  // progress line are what say the run is alive meanwhile.
  //
  // WHERE IT IS: the panel row only, not the detail pane. In the detail the grid
  // is already this graphic - since TOR-110 every planned point has a cell from
  // the first moment and they fill in place - so a bar above it would measure
  // the same thing twice, and the two would disagree for a second at every
  // frame. One graphic per fact.
  //
  // NOT ON A FINISHED RUN, which the criterion asks for: a full bar on a done
  // run tells nobody anything, and an empty one on a failed run reads like a
  // second failure.
  //
  // WHERE entry.framesDone/entry.framesTotal COME FROM is applyFrameProgress,
  // not this function - by the time a redraw is asked for, both are already
  // the most complete reading available (TOR-167). This function only ever
  // turns them into segments; it never reads the wire directly.
  renderRunProgress(entry) {
    const el = entry.rowProgress;
    if (!el) return;

    const total = entry.framesTotal || 0;
    if (!cancellable(entry.state) || entry.disk || total <= 0) {
      el.hidden = true;
      el.replaceChildren();
      return;
    }

    const done = Math.max(0, Math.min(total, entry.framesDone || 0));
    const segments = Math.min(total, MAX_PROGRESS_SEGMENTS);
    const per = total / segments;

    const frag = document.createDocumentFragment();
    for (let i = 0; i < segments; i++) {
      // How much of THIS segment's share of the plan is done. At one frame per
      // segment it is 0 or 1; above the cap a segment stands for several and
      // fills proportionally, the same way the piece strip aggregates - and the
      // exact count is in the line above, so nothing is lost by grouping.
      const from = i * per;
      const filled = Math.max(0, Math.min(1, (done - from) / per));
      const seg = document.createElement("span");
      seg.className = "run-progress-seg";
      seg.style.setProperty("--fill", filled.toFixed(3));
      frag.append(seg);
    }
    el.replaceChildren(frag);
    el.hidden = false;
    el.setAttribute("role", "progressbar");
    el.setAttribute("aria-valuemin", "0");
    el.setAttribute("aria-valuemax", String(total));
    el.setAttribute("aria-valuenow", String(done));
    el.setAttribute("aria-label", done + " of " + total + " frames captured");
  }

  // TOR-141: a stall's (or a metadata wait's) own "how long" would otherwise
  // only refresh when a new heartbeat happens to redraw this row - every
  // stallHeartbeatInterval (5s) at best, or not at all while metadata is still
  // being waited for, since nothing else touches this row in the meantime.
  // That reads as broken rather than as "nothing new to report" - a duration
  // standing visibly still is indistinguishable from one that stopped being
  // tracked. This recomputes just the one line every second, from numbers
  // already on the entry (stallPhrase's own since_ms + observedAt, or
  // runningSince) - it never invents a reading a heartbeat has not itself
  // reported, only keeps the display of one honest between heartbeats.
  refreshStallDurations() {
    const now = Date.now();
    for (const entry of state.runs.values()) {
      if (!entry.stall && !waitingForMetadata(entry)) continue;
      entry.rowMeta.textContent = metaLabel(entry, now);
      entry.rowMeta.title = metaTitle(entry, now);
    }
  }

  // ---------------------------------------------------------------------------
  // THE DETAIL'S OWN WIDTH (TOR-174). detail.css's .run-detail explains WHAT this
  // is for (sticky pins the offset, this sets the size) - this is WHERE the
  // number comes from and WHEN it gets recomputed.
  //
  // One shared custom property (--run-detail-w) on :root, the same pattern
  // applyColumnWidth already uses for --col-w-* - every open .run-detail reads
  // the same var(), so one write here keeps all of them current at once
  // instead of walking the open rows by hand.
  //
  // Called from three places, each somewhere the PANE's own width can change:
  //   - window resize, the obvious one;
  //   - setRunExpanded, because opening or closing a row changes the PAGE's
  //     height, which can add or remove the document's own vertical scrollbar
  //     and, with it, a few pixels of the viewport width .run-table-wrap was
  //     counting on;
  //   - endColumnDrag (below), because TOR-157 dragging a column changes the
  //     TABLE's width, not the pane's - .run-table-wrap's clientWidth is not
  //     expected to move from that alone, but a table that crosses the
  //     overflow threshold can gain or lose a horizontal scrollbar, and that
  //     is a real (if rare) way the pane's own box changes. Recomputing here
  //     costs one comparison and closes that gap rather than assume it never
  //     happens.
  // Deliberately NOT hooked to pointermove mid-drag: the pane's width does not
  // track a border being dragged frame by frame, only (rarely) the moment a
  // scrollbar appears or disappears, which the drag's end already covers.
  //
  // KNOWN GAP, and it is the cost of hooking three named causes rather than
  // watching the pane itself. The table's wrap does not scroll vertically - the
  // PAGE does (see .run-table-wrap's own comment) - so the page's scrollbar
  // appearing or vanishing changes this pane's clientWidth. During a live run
  // the frame grid grows as frames land, which can bring that scrollbar in
  // without a resize, an expand or a column drag, and the open detail then sits
  // ~15px wider than the pane until one of the three fires. A ResizeObserver on
  // the pane would catch every cause instead of these three; it was not used
  // because the three above are verified live and cannot oscillate, and a
  // scrollbar-driven feedback path deserves its own browser check rather than
  // being introduced in review. The 15px is a sliver at the right edge, and the
  // one thing that must never be off-screen - the top-up figure - is far from
  // that edge, which is why this is recorded rather than fixed.
  syncRunDetailWidth() {
    // Kept even though connectedCallback throws on a missing wrap: this is the
    // one method reachable from a listener (window's resize), so it is the one
    // that can be entered by an element whose parts were never found.
    if (!this.wrap) return;
    const width = this.wrap.clientWidth;
    // 0 while the wrap is display:none or not yet laid out - leave the
    // previous value (or detail.css's own 100% fallback) rather than pin every
    // open detail to zero.
    if (width > 0) {
      document.documentElement.style.setProperty("--run-detail-w", width + "px");
    }
  }

  // ---------------------------------------------------------------------------
  // THE ACCORDION (TOR-138). A torrent's detail lives in its own row, and this
  // is the only function that may put one on screen or take it off.
  //
  // SEVERAL ROWS MAY BE OPEN AT ONCE, and that is the decision the ticket asks
  // for rather than a side effect of how this is written. Three reasons, in the
  // order they matter:
  //
  //   1. Closing one to open another would DESTROY WORK IN PROGRESS. A live run
  //      streaming frames into its grid is the case this whole release is
  //      about; a person who opens a second torrent to see what it is would
  //      lose sight of the first one mid-run, and get it back scrolled to the
  //      top with its file accordions as they were left only by luck.
  //   2. This codebase has already made this decision twice, one and two levels
  //      down, and written down why: a file's accordion and its metadata
  //      accordion both change state ONLY from their own toggle, precisely so a
  //      person's click "can neither be collapsed out from under them nor have
  //      the expansion stolen back to file zero" (fileBlock). Auto-closing a
  //      sibling here would be the same theft, at the level above.
  //   3. It costs nothing that the old shape was not already paying. Every
  //      entry's detail DOM has always existed and has always been updated
  //      whether or not it was on screen - applyFrame and renderFrames never
  //      checked - so N open details is N grids laid out, not N grids kept up
  //      to date. The thumbnails are loading="lazy", so an open row scrolled
  //      off screen fetches nothing.
  //
  // What it costs, said plainly: two expanded live runs are two grids doing
  // layout on every frame_ready, and a page with every row open is as tall as
  // its contents. Both are the person's own choice, made one click at a time,
  // and reversible with the same click.
  setRunExpanded(entry, expanded) {
    entry.expanded = expanded;
    // TOR-174: either direction can change the page's own height (a detail
    // coming on or off screen), which can add or remove the document's
    // vertical scrollbar and with it a few pixels of .run-table-wrap's own
    // width - see syncRunDetailWidth's own comment.
    this.syncRunDetailWidth();
    // TOR-152: opening a row is when its top-up standing is worth reading off
    // disk - here rather than in toggleRun, because this is the one function
    // that may put a detail on screen (see this block's own heading) and
    // several paths reach it: a click, and began() for a run just started.
    // refreshAgain is a no-op for a row that is not settled, has no infohash,
    // or was already asked this question, so calling it on every expansion
    // costs a comparison.
    if (expanded) detailShown(entry);
    // AND THE DISCLOSURE ITSELF, which since TOR-212 is one call rather than
    // the three attribute writes this method used to end with - the same three
    // that also stood, twice, in file-detail.js. What is left above them is
    // what opening a ROW means as opposed to what opening anything means, and
    // that division is the whole reason the two side effects above did not
    // travel into the component: TOR-152's guard exists because detailShown
    // was once forgotten, and a generic disclosure is exactly where it would
    // be forgotten again.
    //
    // Still nothing but the detail row's `hidden` attribute on screen - no
    // rule in detail.css sets display on .run-detail-row, so the UA rule wins
    // uncontested (accordion.js's apply says the same thing for all three
    // levels at once).
    entry.rowAccordion.apply(expanded);
  }

  resizableColumnKeys() {
    return Array.from(this.sortHeaders, (head) => head.dataset.sort);
  }

  // A column that no longer exists - the table shipped fewer or differently-
  // named columns when the value was stored - is dropped rather than kept:
  // nothing on the current page would ever read it, and rendering the OTHER
  // columns from a partly-stale map is still exactly the graceful fallback the
  // acceptance criterion asks for. Malformed JSON, a non-object, or a width
  // that doesn't parse as a finite number all fall back to the same empty map,
  // which is indistinguishable from "nothing was ever stored" - the table then
  // simply renders at tokens.css's own defaults for every column.
  loadColumnWidths() {
    try {
      const raw = localStorage.getItem(COLUMN_WIDTHS_KEY);
      if (!raw) return {};
      const parsed = JSON.parse(raw);
      if (!parsed || typeof parsed !== "object") return {};
      const known = new Set(this.resizableColumnKeys());
      const widths = {};
      for (const key of Object.keys(parsed)) {
        if (!known.has(key)) continue; // a column this page no longer has
        const width = parseFloat(parsed[key]);
        if (Number.isFinite(width)) widths[key] = clampColumnWidth(width);
      }
      return widths;
    } catch (err) {
      return {};
    }
  }

  saveColumnWidths(widths) {
    try {
      localStorage.setItem(COLUMN_WIDTHS_KEY, JSON.stringify(widths));
    } catch (err) {
      // Best-effort only - the default widths still work.
    }
  }

  // One custom property per column, on :root - the shape the left panel's own
  // width used before TOR-168 removed the panel, and the reason it is worth
  // keeping: nothing has to be told that a column moved. tokens.css's
  // --run-tracks names the same nine tokens, and since TOR-221 both grids
  // that lay a run's columns out read that one list - so writing one token
  // here re-sizes that track in the header band and in every row at once.
  applyColumnWidth(key, px) {
    document.documentElement.style.setProperty("--col-w-" + key, px + "px");
  }

  // Every sortable header gets a drag handle at its own right edge - the
  // border between it and the next column. Its WIDTH it gets from its track
  // (table.css's .run-grid-head-row lays the band out over --run-tracks,
  // which reads the matching --col-w-* token; tokens.css declares the
  // defaults and wire()'s own loop has already overridden any that were
  // stored), so nothing about the width is set here.
  //
  // TOR-215 DELETED ONE LINE FROM THIS LOOP, and what it was is worth knowing
  // because the rest of the function is untouched:
  //
  //     th.style.width = "var(--col-w-" + key + ")";
  //
  // Under table-layout: fixed that inline width was what sized the column -
  // fixed layout reads a column's width off its header cell alone. Under the
  // grid the track sizes the column and the header just fills it, so the line
  // said nothing; worse, a grid item is content-box where a table cell's
  // width included its padding, so it made every header 9.6px wider than its
  // own track until table.css's box-sizing caught it (TOR-214 measured
  // 329.6px in a 320px track). The drag itself never touched the table: it
  // writes --col-w-KEY on :root and reads getBoundingClientRect().width off
  // the header, and both are still exactly true of a grid, which is why
  // nothing below this comment had to change. TOR-221 moved the header cells
  // inside a band element and neither half stopped being true: the token is
  // still on :root, and a header cell still stretches to its own track, so
  // the rect it measures is still the column's width. Verified with a real
  // pointer drag rather than assumed, both directions and both clamps.
  //
  // The actions header is deliberately excluded: it is not in
  // this.sortHeaders (no data-sort), so it grows no handle of its own, since
  // there is no column past it for a border to belong to - and its track is
  // the flexible one, the only track that must not be pinned to a width (see
  // table.css's .run-row).
  wireColumnResizers() {
    for (const head of this.sortHeaders) {
      const key = head.dataset.sort;

      const handle = document.createElement("span");
      handle.className = "col-resizer";
      handle.setAttribute("aria-hidden", "true");
      head.append(handle);

      // drag holds this handle's own in-progress drag - { startX, startWidth } -
      // or null when it isn't dragging. Keeping the start point and width in one
      // object that is null between drags (instead of two bare variables that
      // just keep whatever the last drag left in them) means there is one place
      // that says whether THIS handle is dragging, instead of that fact living
      // only in the "dragging" CSS class - which pointermove used to trust
      // blindly. Scoped inside this loop iteration, so each handle already gets
      // its own binding and one handle's drag can never read another's start
      // point.
      let drag = null;

      // stopPropagation on every one of the handle's own events, not just
      // pointerdown: the handle sits inside a header cell that is itself a
      // sort control (this.sortHeaders' own click listener, wired above), and
      // without this a drag - or even a plain click that lands on the handle - would
      // bubble up and also reorder the table, the same trap TOR-140's ▲/▼
      // buttons stopPropagation against so a reorder did not also toggle the
      // accordion.
      handle.addEventListener("pointerdown", (event) => {
        if (event.button !== undefined && event.button !== 0) return;
        event.stopPropagation();
        event.preventDefault();
        drag = { startX: event.clientX, startWidth: head.getBoundingClientRect().width };
        handle.classList.add("dragging");
        handle.setPointerCapture(event.pointerId);
      });

      handle.addEventListener("pointermove", (event) => {
        if (!drag) return;
        // A move with no button held is an ordinary hover, not a drag - the
        // primary button bit (1) must still be set in event.buttons. Without
        // this, any way the drag's pointerup/pointercancel never reaches the
        // handle (lostpointercapture below covers the one this repo can name,
        // but not necessarily every one a future browser or code change adds)
        // leaves the next hover computing against a stale start point, which is
        // exactly the "handle stays lit and dragging creeps on hover" bug.
        if (!(event.buttons & 1)) {
          endColumnDrag(event);
          return;
        }
        event.stopPropagation();
        const width = clampColumnWidth(drag.startWidth + (event.clientX - drag.startX));
        this.columnWidths[key] = width;
        this.applyColumnWidth(key, width);
      });

      // AN ARROW FUNCTION, AND THAT IS THE WHOLE OF IT. Written as
      // `function endColumnDrag(...)` this is registered as a listener on the
      // handle, so `this` inside it is the <span> - and
      // `this.saveColumnWidths(...)` throws
      // `TypeError: this.saveColumnWidths is not a function` on every drag
      // that ends. The lines above it still run, so the drag LOOKS finished:
      // the class comes off and the column keeps its new width on screen.
      // What silently does not happen is the save - so no width ever reaches
      // localStorage and TOR-157's whole point, drag-a-border-and-remember-it,
      // is gone - and syncRunDetailWidth, so TOR-174's recheck after a drag is
      // gone with it.
      //
      // No text guard could see this: the call reads exactly as it should. It
      // took a real drag in a browser and the console, which is why TOR-194's
      // acceptance criterion asked for one.
      const endColumnDrag = (event) => {
        if (!drag) return;
        drag = null;
        event.stopPropagation();
        handle.classList.remove("dragging");
        try {
          handle.releasePointerCapture(event.pointerId);
        } catch (err) {
          // Already released (e.g. on pointercancel, or because capture was
          // already lost - see lostpointercapture below) - nothing more to do.
        }
        this.saveColumnWidths(this.columnWidths);
        // TOR-174: see syncRunDetailWidth's own comment for why a column drag,
        // which changes the TABLE's width rather than the pane's, still gets a
        // recheck here.
        this.syncRunDetailWidth();
      };
      handle.addEventListener("pointerup", endColumnDrag);
      handle.addEventListener("pointercancel", endColumnDrag);
      // lostpointercapture fires whenever the capture set in pointerdown ends
      // some way other than pointerup/pointercancel reaching the handle itself -
      // per spec, at minimum whenever the captured element leaves the document.
      // (buildLiveColumnHeaders() only builds this table's header once today, so
      // that specific trigger is not a live path here yet - but a real drag can
      // still lose capture other ways, e.g. an automated or synthetic pointer
      // sequence, as TOR-165's own browser repro found without any header
      // rebuild involved.) Nothing else here listens for it, so before this the
      // "dragging" class - and the stale start point above - just stayed put.
      handle.addEventListener("lostpointercapture", endColumnDrag);
      // A plain click - no drag, pointerdown and pointerup on the same spot -
      // still bubbles to the header's own click listener unless stopped here
      // too; pointerdown's stopPropagation only stops the pointerdown event
      // itself, not the separate click event the browser dispatches afterwards.
      handle.addEventListener("click", (event) => event.stopPropagation());
    }
  }
}

// Native, no bundler: this is the whole registration mechanism, and importing
// this module for its side effect is how app.js gets the element defined
// before the page's own <run-table> is upgraded. The upgrade runs
// connectedCallback, which is what builds the six live columns - so by the
// time app.js's own body evaluates, the header is nine columns wide and
// el.runTable is an element with its parts already found.
customElements.define("run-table", RunTable);

// THE SURFACE IS THE CLASS PLUS setServices, AND NOTHING ELSE (TOR-209,
// applied to the remaining five by TOR-208). frame-panel.js's own export block
// carries the full reasoning; the two facts that decide this one:
//
//   - LIVE_COLUMNS and MAX_PROGRESS_SEGMENTS were imported by NOBODY, checked
//     across the whole repository. They are read here (buildLiveColumnHeaders,
//     renderRunProgress) and named in columns_test.go, which lifts them out of
//     this module's TEXT and needs no export to do it. So this narrows a DEAD
//     export - something this module wants on its own terms.
//   - setServices STAYS, and is the irreducible residue: app.js imports it by
//     name (setRunTableServices), and the injected-services setter is point 7
//     of the element pattern in docs/front-end.md. Removing it to tidy a
//     consumer's index would break the page, which is the wrong way round.
//
// Before adding a name here: Claude Design's checker indexes a component
// module's named exports as COMPONENTS, so every extra name becomes an entry a
// design agent is offered and can do nothing with.
export { RunTable, setServices };
