// ONE FILE'S OWN DETAIL, as a custom element (TOR-195), and the innermost of
// the three this ticket nests.
//
// It follows the pattern the elements before it settled: light DOM, parts
// found with this.querySelector after the markup exists with a loud throw
// naming any that is missing, listeners added once on nodes this element
// created inside itself, and one method per thing the outside is allowed to
// ask for. run-detail.js's header carries the two things all three share and
// none repeats: why the markup is a template here rather than declarative in
// index.html, and WHY THERE IS NO disconnectedCallback (the table's re-sort
// moves a run's rows, so everything in them is disconnected and reconnected on
// every redraw - a teardown here would kill Regenerate, Compare and every
// thumbnail on the first event).
//
// WHAT IT IS: everything one video file of one torrent has to show. Its
// metadata behind its own disclosure (TOR-71), the contact-sheet link
// (TOR-171), Regenerate and Compare (TOR-109), its heartbeat line, the reach
// strip and its swarm chip (TOR-111, TOR-142, TOR-153), and the frame grid
// with its four cell states (TOR-110, TOR-118). One instance per video row of
// one torrent, mounted as a sibling of that row inside the row's own <li>.
//
// ---------------------------------------------------------------------------
// TWO STAGES, WHICH IS WHAT MAKES IT DIFFERENT FROM THE TWO ABOVE IT.
//
// build() runs when the row is built and does one thing: it puts an empty
// .file-detail slot inside this element and mints the id the row's disclosure
// names in aria-controls. That has to happen with the ROW, because a
// disclosure control has to name the region it opens and a region minted later
// is one the control pointed at nothing for.
//
// mount() runs the first time the file says anything and fills that slot -
// which is when there is anything to fill it with. Before a tick, a file has
// no metadata, no plan and no frames, and a panel of empty headings is worse
// than a row that does not open yet (file-list.js's toggleFileDetail is the
// no-op that says so).
//
// So the loud throw comes in two parts, one per stage, rather than pretending
// the sixteen parts mount() finds exist before it has built them.
//
// WHAT IT IS HANDED, and it is the one thing that crosses the boundary in the
// other direction: five elements of the row above it (its <li>, the <label>
// that is the row proper, its disclosure, its name and its summary span). The
// <label> is TOR-222's addition and it is the level-2 disclosure's SUMMARY -
// the element the open rail is painted on, which until that ticket was reached
// through its parent by a CSS combinator instead. Since TOR-182 THE ROW IS THIS
// FILE'S TITLE LINE - the `<h2 class="file-title">` that used to hold a
// toggle, the name and the summary is gone, because the row already carries a
// disclosure, the name (as a base name with the whole path on its title, which
// is the better of the two conventions) and now the summary too. Keeping a
// second title line inside the thing the first one opens would have been the
// same line twice. They arrive in one bundle at mount rather than being
// reached for.
// ---------------------------------------------------------------------------

// The disclosure (TOR-212). Two of the page's three levels are this file's -
// a video file's own block, and the Metadata block inside it - and both write
// through the same component the torrent's line one level up uses.
import { Accordion } from "./accordion.js";
import {
  ABSENT,
  availabilityCellTitle,
  availabilityReading,
  basename,
  bytesLabel,
  capturedCells,
  gridCells,
  newFileState,
  nextDetailId,
  seconds,
  timecode,
} from "./state.js";

// THE NINE SERVICES THE PAGE INJECTS, in the shape events.js's setView and the
// other elements' setServices already use, and for the same reason: each is
// something only app.js's bootstrap can do, and a missing name has to fail
// while the wiring is being written rather than at the first press of
// Regenerate.
//
//   url          resolves a path against document.baseURI and attaches the
//                access token. Every frame src, every artefact link and both
//                of this element's own requests go through it.
//   post         the one place a POST is sent, with its error shape.
//   del          the one place a DELETE is sent. It takes a whole URL, because
//                deleting one frame carries the result set as a query
//                parameter (see app.js's del).
//   log          one activity-log line, tagged with which torrent it is about.
//   showError    the page's own error line, above the table.
//   began        what every successful start has in common: the new run gets a
//                row straight away and that row opens (app.js's began).
//                Regenerate starts an ORDINARY run, so it ends where the
//                intake line's own does.
//   intake       what the intake line currently says - { mode, count,
//                countText } - because Regenerate repeats this file at the
//                profile and count a person can actually SEE while pressing
//                the button. A finished run's own profile is not on the wire.
//   openFrame    the frame panel's open(src, caption). The panel is one
//                element on the page, not one per file (frame-panel.js).
//   openCompare  the compare dialog's open(infohash, index), for the same
//                reason.
//
// Undefined until setServices runs, which is checked there rather than at each
// call site.
let url = null;
let post = null;
let del = null;
let log = null;
let showError = null;
let began = null;
let intake = null;
let openFrame = null;
let openCompare = null;

const SERVICES = ["url", "post", "del", "log", "showError", "began", "intake",
  "openFrame", "openCompare"];

function setServices(services) {
  for (const name of SERVICES) {
    if (typeof services[name] !== "function") {
      throw new Error("file-detail: setServices needs a " + name + "() function");
    }
  }
  ({ url, post, del, log, showError, began, intake, openFrame, openCompare } = services);
}

// BODY is what fills the slot, and it is a template here for the reason
// run-detail.js's header gives: there is one per video file of every torrent,
// and index.html has never held it.
const BODY =
  '<section class="meta">' +
    '<h3 class="meta-title">' +
      '<button type="button" class="meta-toggle" aria-expanded="false">' +
        '<span class="meta-toggle-icon" aria-hidden="true"></span>' +
        '<span class="meta-toggle-label">Metadata</span>' +
      "</button>" +
    "</h3>" +
    '<div class="meta-body">' +
      '<dl class="specs"></dl>' +
      '<div class="tracks"></div>' +
    "</div>" +
  "</section>" +
  '<p class="file-links" hidden></p>' +
  '<p class="file-regen">' +
    '<label class="file-regen-label">Frames' +
      '<input class="file-regen-count" type="number" min="1" step="1" inputmode="numeric"' +
      ' aria-label="Frames for this file">' +
    "</label>" +
    '<button type="button" class="file-regen-go">Regenerate</button>' +
    // Compare sits beside Regenerate because it is the same thought one
    // step later: Regenerate is how a second result set comes to exist,
    // and this is what a second set is FOR (TOR-109).
    '<button type="button" class="file-compare">Compare…</button>' +
  "</p>" +
  '<p class="file-progress" hidden></p>' +
  // The picture of the product (TOR-111): how little of the file the run
  // actually ordered. Above the grid because the grid is what those
  // pieces bought. The swarm chip beside the strip is TOR-142's one
  // surviving idea (TOR-153 merged its separate track into this row,
  // once the two turned out to say "41 of 197" twice) - see renderReach's
  // own doc for why the chip stays a separate shape rather than a fill
  // sharing the strip's own axis.
  '<figure class="reach" hidden>' +
    '<div class="reach-body">' +
      '<div class="reach-strip" role="img"></div>' +
      '<div class="avail-swarm"><span class="avail-swarm-dot"></span>' +
        '<span class="avail-swarm-text"></span></div>' +
    "</div>" +
    '<figcaption class="reach-note"></figcaption>' +
  "</figure>" +
  '<div class="grid"></div>';

function bitrateLabel(bps) {
  if (!bps) return "";
  if (bps >= 1e6) return (bps / 1e6).toFixed(1) + " Mbps";
  if (bps >= 1e3) return Math.round(bps / 1e3) + " kbps";
  return bps + " bps";
}

function channelsLabel(n) {
  switch (n) {
    case 1: return "mono";
    case 2: return "stereo";
    case 6: return "5.1";
    case 8: return "7.1";
    default: return n ? n + " ch" : "";
  }
}

function langLabel(code) {
  return code && code !== "und" ? code : "unknown language";
}

function addSpec(dl, label, value) {
  const dt = document.createElement("dt");
  dt.textContent = label;
  const dd = document.createElement("dd");
  dd.textContent = value || "unknown";
  dl.append(dt, dd);
}

function audioLine(t) {
  const parts = [langLabel(t.language), t.codec, channelsLabel(t.channels), bitrateLabel(t.bitrate)];
  if (t.title) parts.push('"' + t.title + '"');
  if (t.default) parts.push("default");
  return parts.filter(Boolean).join(" · ");
}

function subtitleLine(t) {
  const parts = [langLabel(t.language), t.codec];
  if (t.title) parts.push('"' + t.title + '"');
  if (t.forced) parts.push("forced");
  if (t.default) parts.push("default");
  return parts.filter(Boolean).join(" · ");
}

function trackGroup(label, tracks, formatter) {
  const section = document.createElement("div");
  section.className = "track-group";
  const h3 = document.createElement("h3");
  h3.textContent = tracks.length ? label : label + " — none";
  section.append(h3);
  if (tracks.length) {
    const ul = document.createElement("ul");
    for (const t of tracks) {
      const li = document.createElement("li");
      li.textContent = formatter(t);
      ul.append(li);
    }
    section.append(ul);
  }
  return section;
}

function link(href, text) {
  const a = document.createElement("a");
  a.href = url(href);
  a.textContent = text;
  a.target = "_blank";
  a.rel = "noopener";
  return a;
}

// MAX_BLOCKS is where drawing one block per piece stops being readable and
// aggregating starts. At the pane's usual width this leaves each block a few
// pixels across; a 10,000-piece remux would otherwise ask for a block a
// fortieth of a pixel wide, which is the "lie about individual blocks" TOR-111
// names.
const MAX_BLOCKS = 96;

// REACH_FLOOR is how much of a block is painted when any of its pieces were
// ordered, before density is added on top. Enough to be seen at the strip's
// height; small enough that a full block still reads as clearly fuller.
const REACH_FLOOR = 0.22;

// What a shift and a failure code mean in words, for the cell's own tooltip.
// The manifest's vocabulary is short enough to be cryptic - "shifted",
// "stepped", "read_stalled" - and a person deciding whether to run again
// needs the difference, not the token.
const SHIFT_REASON = {
  shifted: "the exact point was not held by any peer, so a nearby one was taken",
  stepped: "the frame there was blank, so the neighbouring keyframe was taken",
};
const FAILURE_REASON = {
  unavailable: "no peer offered these pieces - running again will not help unless the swarm changes",
  read_stalled: "the pieces were being fetched and the read timed out - running again may well get through",
};

// cellTitle is what the cell says on inspection: the state in words, and for
// anything other than an exact frame, why.
function cellTitle(frame) {
  const planned = Number.isFinite(frame.plannedMs) ? timecode(frame.plannedMs) : "";
  switch (frame.state) {
    case "pending":
      return "planned for " + timecode(frame.timeMs) + " - not captured yet";
    case "failed": {
      const why = FAILURE_REASON[frame.error];
      return "no frame at " + timecode(frame.timeMs) + (frame.error ? " - " + frame.error : "") +
        (why ? ": " + why : "") + (frame.reason ? " (" + frame.reason + ")" : "");
    }
    case "shifted": {
      const why = SHIFT_REASON[frame.shift];
      const asked = planned && planned !== timecode(frame.timeMs) ? "asked for " + planned + ", " : "";
      return asked + "taken at " + timecode(frame.timeMs) + (why ? " - " + why : "");
    }
    default:
      return "frame at " + timecode(frame.timeMs);
  }
}

// detailFrame is one frame of a file-detail response in the shape the grid
// keeps them in. Both readers of that response - the merge on opening a file
// and the replacement after a delete - go through here, so a frame is never
// half-addressable in one of them and whole in the other.
function detailFrame(f) {
  return {
    // f.url is empty for a failed point (TOR-118), and null rather than ""
    // is what gridCells and capturedCells read as "nothing was captured
    // here" - url("") would also have resolved to this very page.
    //
    // THE PATH, NOT A RESOLVED URL, since TOR-191: frameFigure resolves it
    // when it sets the src, so a frame off the socket and a frame off disk
    // are one shape. That is this doc's own promise - "a frame is never
    // half-addressable in one of them and whole in the other" - and it used
    // to hold only for `params`.
    url: f.url || null, timeMs: f.time_ms, shift: f.shift || "",
    params: f.params || "", index: f.index, error: f.error || "",
  };
}

class FileDetail extends HTMLElement {
  constructor() {
    super();

    // The file this detail belongs to, set by mount() the first time the file
    // says anything. Null until then, which is the whole of the two-stage
    // shape this module's header describes - a row exists long before its file
    // has anything to show.
    this.fentry = null;
  }

  connectedCallback() {
    this.build();
  }

  // build puts the empty slot inside this element and mints the id the row's
  // disclosure names - ONCE, however many times this element is connected. See
  // run-detail.js's header for why that guard is load-bearing rather than
  // defensive, and why there is no teardown to match.
  //
  // Public because file-list.js calls it before appending the element:
  // createElement constructs a custom element synchronously, but
  // connectedCallback is a reaction, and regionId is read in the same turn.
  build() {
    if (this.body) return;

    // The slot: built empty here, filled by mount(). No `display` of its own
    // in filelist.css, deliberately, so the `hidden` attribute below is the
    // whole of collapse and a class rule can never beat the UA's own
    // [hidden] - the trap that file has now paid for six times.
    this.innerHTML = '<div class="file-detail" hidden></div>';
    this.body = this.querySelector(".file-detail");
    if (!this.body) throw new Error("file-detail: no body inside the element");
    // Minted from the one counter all three detail levels share (state.js's
    // nextDetailId), so it is unique across the document rather than only
    // within this prefix.
    this.body.id = nextDetailId("file-detail-");
  }

  // regionId is the id the row's own disclosure has to name in aria-controls,
  // and the only thing about this element the LIST needs before the file has
  // said anything. The list sets aria-controls and this element sets
  // aria-expanded (setFileExpanded, because it travels with the expanded
  // state) - one attribute each, on the same button, the same division
  // run-table.js makes with the run row's own toggle.
  get regionId() {
    this.build();
    return this.body.id;
  }

  // mount fills the slot and returns this file's whole entry - the data half
  // state.js owns (newFileState) plus the one field that names this element.
  // It is state.js's registered file factory, one hop away (app.js's fileBlock
  // hands it the run, file-list.js's mountFileDetail finds the row).
  //
  // Each video file gets its own metadata panel and its own frame grid, since
  // a multi-file torrent should not mix their frames or their tracks in one
  // place - and one torrent's files must never mix with another's now that the
  // page can hold several at once.
  //
  // Everything in the detail - specs, tracks, links, progress, the frame
  // grid - is built and filled in whether or not the file is expanded; only
  // the slot's `hidden` attribute decides what is on screen. A frame_ready for
  // a collapsed file still appends its figure to .grid (events.js's applyFrame
  // never checks expanded state), so expanding it later shows everything that
  // arrived while it was closed - nothing is built lazily past this point,
  // there is nothing to replay.
  //
  // Only the first file mounted for a run is auto-expanded (entry.autoExpanded
  // latches on the first call and never resets except on a full
  // resetRunState). Every file after that starts collapsed, and a file's
  // expanded state changes from then on only in response to its own row being
  // clicked - never from a later file_started/frame_ready/progress event - so
  // a person's click can neither be collapsed out from under them nor have
  // the expansion stolen back to file zero.
  //
  // One level inside that, the summary - specs and tracks - has its own
  // accordion (TOR-71), collapsed by default with no auto-expand exception:
  // the frames are what the file was opened for, the metadata is a detail on
  // top of that. Same shape as the file-level toggle - a header button plus a
  // body whose `hidden` attribute is the only thing collapse touches - and the
  // same rule: fentry.metaExpanded changes only inside setMetaExpanded, called
  // from exactly two places (the toggle's click handler, and once here at
  // creation), so a later file_started re-filling .specs/.tracks in place can
  // never reopen or reclose it.
  mount(entry, index, row) {
    if (this.fentry) return this.fentry;
    this.build();

    this.body.innerHTML = BODY;

    // The five row elements this detail is handed rather than builds (see this
    // module's header): the row's <li>, the <label> that is the row proper,
    // its disclosure, its name and its summary span.
    //
    // fileRow is the <label class="picker-file"> and it is named rather than
    // reached for through this.item, because it is level 2's disclosure
    // SUMMARY (TOR-222) - the element that carries data-expanded and wears the
    // open rail. `summary` was already taken by the .picker-summary span, so
    // the name here is the markup's rather than the component's.
    this.item = row.item;
    this.fileRow = row.row;
    this.rowToggle = row.open;
    this.name = row.name;
    this.summary = row.summary;

    // The Metadata section and its header, both parts of level 3's disclosure
    // (TOR-222): the <section> is its container and the <h3> is its summary.
    // Named here with everything else rather than queried inline at the
    // accordion, so a BODY template that lost either fails by name.
    this.meta = this.querySelector(".meta");
    this.metaTitle = this.querySelector(".meta-title");
    this.metaToggle = this.querySelector(".meta-toggle");
    this.metaBody = this.querySelector(".meta-body");
    this.specs = this.querySelector(".specs");
    this.tracks = this.querySelector(".tracks");
    this.links = this.querySelector(".file-links");
    this.regenCount = this.querySelector(".file-regen-count");
    this.regenGo = this.querySelector(".file-regen-go");
    this.compareGo = this.querySelector(".file-compare");
    this.progress = this.querySelector(".file-progress");
    this.availSwarm = this.querySelector(".avail-swarm");
    this.availSwarmText = this.querySelector(".avail-swarm-text");
    this.reach = this.querySelector(".reach");
    this.reachStrip = this.querySelector(".reach-strip");
    this.reachNote = this.querySelector(".reach-note");
    this.grid = this.querySelector(".grid");

    // A missing part is a wiring error, not a state to degrade into. Two of
    // the draws below used to open with `if (!el) return` for exactly this
    // case, which made a missing element a permanent silent no-op; naming it
    // here once is both louder and cheaper.
    for (const [name, node] of Object.entries({
      item: this.item,
      fileRow: this.fileRow,
      rowToggle: this.rowToggle,
      name: this.name,
      summary: this.summary,
      meta: this.meta,
      metaTitle: this.metaTitle,
      metaToggle: this.metaToggle,
      metaBody: this.metaBody,
      specs: this.specs,
      tracks: this.tracks,
      links: this.links,
      regenCount: this.regenCount,
      regenGo: this.regenGo,
      compareGo: this.compareGo,
      progress: this.progress,
      availSwarm: this.availSwarm,
      availSwarmText: this.availSwarmText,
      reach: this.reach,
      reachStrip: this.reachStrip,
      reachNote: this.reachNote,
      grid: this.grid,
    })) {
      if (!node) throw new Error("file-detail: no " + name + " inside the element");
    }

    // THE TWO INNER LEVELS OF THE PAGE'S THREE DISCLOSURES (TOR-212), built
    // here because this is the moment both their toggles and both their
    // regions exist: the row's own triangle was handed over above, and BODY's
    // Metadata header was written into the slot a few lines earlier.
    //
    // BOTH ARE WRAPPERS AROUND A BOX THE MARKUP ALREADY HAD (TOR-222), and
    // that is this ticket's finding at these two levels rather than at the
    // outer one: nothing had to be rearranged.
    //
    //   level 2  container li.picker-item   summary label.picker-file
    //   level 3  container section.meta     summary h3.meta-title
    //
    // The <li> holds the row and the slot; the <section> holds its header and
    // its body. accordion.js checks both boxes hold all three of their parts,
    // so "the toggle is inside the row's <label> and the region is that row's
    // sibling" - which docs/front-end.md gave as a reason no box existed - is
    // exactly what a box containing both looks like.
    //
    // Level 2 dresses the row's <label>, NOT its <li>: an open file is a state
    // of the ROW, the same way an open torrent is a state of its line one
    // level up, and the rail was always painted on .picker-file. Until TOR-222
    // the attribute sat on the <li> and filelist.css reached the label through
    // it (`.picker-item[data-expanded="true"] > .picker-file`) - a rule that
    // has to name a parent to find a child, which is the coupling the ticket
    // is about. Its mark IS the toggle, because .picker-open is a
    // triangle-sized button rather than a label with a triangle in it.
    //
    // Level 3 dresses NOTHING, and that is the level's own definition rather
    // than an omission: this rail would already be inside the file's rail,
    // which is inside the torrent's accented ground, and three nested grounds
    // read as chrome (filelist.css says so where the file's own rail is
    // quieter than the torrent's for the same reason). The LEVEL decides that
    // now rather than the caller - level 3's rail is null in accordion.js's
    // own table, so apply() writes no data-expanded here however this is
    // called.
    this.fileAccordion = new Accordion({
      level: 2,
      container: this.item,
      summary: this.fileRow,
      toggle: this.rowToggle,
      region: this.body,
    });
    this.metaAccordion = new Accordion({
      level: 3,
      container: this.meta,
      summary: this.metaTitle,
      toggle: this.metaToggle,
      region: this.metaBody,
      mark: this.querySelector(".meta-toggle-icon"),
    });

    // The row's own item carries the open/closed state, so filelist.css can
    // dress the whole row - not just the slot under it - the way .run-row
    // [data-expanded="true"] already dresses an open torrent one level up.
    // data-detail is what brings the row's disclosure triangle on screen: this
    // is the moment there is something behind it, and this is the only writer
    // of that attribute.
    this.item.dataset.detail = "true";

    this.fentry = {
      // The data half, which state.js owns (newFileState): what this file
      // KNOWS - its frames, its plan, its media, its last heartbeat. Below it,
      // the one field this element adds. Same union-scan rule as the run entry
      // (TestNoRunEntryFieldIsDeclaredTwice): a key named here still shadows
      // the same key in newFileState, silently.
      ...newFileState(index, entry),
      // block is this element, and since TOR-195 it is the WHOLE of the file
      // entry's DOM half: the elements themselves are this element's own
      // fields, and everything that draws them is a method on it. So the
      // events layer and the list above hand a redraw to fentry.block rather
      // than reaching into a bag of nodes.
      block: this,
    };

    this.regenCount.value = intake().countText;
    // Arrow functions, every one of them: a `function` declaration used as a
    // listener is handed the LISTENING ELEMENT as its `this`, so `this.fentry`
    // inside one would be the button's own missing field - which is TOR-194's
    // shipped regression in the one shape a text check cannot see.
    this.regenGo.addEventListener("click", () => this.regenerate());
    this.compareGo.addEventListener("click", () => openCompare(entry.infohash, index));

    // No toggle listener here: the row that opens this detail is built once by
    // file-list.js's renderFileList and owns the click, so the listener lives
    // with the element it is on rather than being attached to it from inside
    // the thing it opens. this.rowToggle is still the row's own disclosure
    // button, because aria-expanded has to travel with the expanded state and
    // setFileExpanded is where that is written.
    this.setFileExpanded(!entry.autoExpanded);
    entry.autoExpanded = true;
    this.updateFileSummary();

    this.metaToggle.addEventListener("click", () => {
      this.setMetaExpanded(!this.fentry.metaExpanded);
    });
    this.setMetaExpanded(false);

    // Drawn once immediately, from whatever this entry already knows (a swarm
    // reading from GET /runs' own "live" object may already be sitting on
    // entry.live before this card exists) rather than waiting for the next
    // heartbeat or the file to finish - the same reasoning updateFileSummary
    // above is called for on creation.
    this.renderAvail();

    return this.fentry;
  }

  // toggle is the second half of what clicking a file's row does: the list
  // decides which row was clicked and whether it has a detail at all
  // (file-list.js's toggleFileDetail), and this decides what opening means.
  //
  // Opening a file is when it is worth reading the other result sets off disk -
  // not on every run_state, and not for a file nobody looked at. Once is
  // enough: a set only gains frames by a run finishing, which asks again
  // itself (renderFileDone, on the file's own file_done).
  toggle() {
    const fentry = this.fentry;
    const expanded = !fentry.expanded;
    this.setFileExpanded(expanded);
    if (expanded && !fentry.detailLoaded) this.loadFileDetail();
  }

  // ---------------------------------------------------------------------------
  // ONE FILE'S DETAIL AT A TIME (TOR-182), and this is the level where that
  // decision is made rather than the one above it.
  //
  // The row level deliberately allows SEVERAL torrents open at once, for three
  // written reasons (see run-table.js's setRunExpanded) - the first of which is
  // that closing one to open another destroys work in progress. None of those
  // reasons is repealed here; what changes is what the worst case costs. A
  // season pack holds twenty-five video files, and two expanded torrents that
  // each allowed twenty-five open frame grids would put fifty grids on one
  // page: not a page anybody can read, and not one any of the three reasons
  // above asks for.
  //
  // So the bound goes on the INNER level, which is the one place it costs
  // nothing that matters:
  //
  //   - The outer level keeps what it is for. Two torrents side by side, one
  //     of them mid-run, is exactly the comparison setRunExpanded exists to
  //     allow, and it still works.
  //   - The inner level has nothing to lose by closing. A file's detail is
  //     built once and updated whether or not it is on screen (mount's own
  //     heading): a collapsed file's grid still fills, its progress line still
  //     moves, and re-opening it shows everything that arrived meanwhile. This
  //     is the same property that makes closing a torrent's row harmless, and
  //     it is why "closing one destroys work" - true of a pane that has to be
  //     rebuilt - is not true of either accordion in this page.
  //   - And a person comparing two files of one torrent has the level above:
  //     nothing stops the same torrent's row from being open beside another's.
  //
  // What it costs, said plainly: two files of ONE torrent cannot be read side
  // by side. Compare (TOR-109) is the answer for two result sets of the same
  // file, and there is no equivalent for two different files - if one is ever
  // wanted, this method is where it would be relaxed, and the reasoning above
  // is what would have to be answered.
  setFileExpanded(expanded) {
    const fentry = this.fentry;
    // Every sibling first, and only when opening: this is the accordion, and
    // it reads the entry's own map rather than a "which one is open" field so
    // that nothing can be left claiming to be open after a reset emptied the
    // map from under it.
    if (expanded) {
      for (const other of fentry.entry.fileEntries.values()) {
        if (other !== fentry && other.expanded) other.block.setFileExpanded(false);
      }
    }

    fentry.expanded = expanded;
    // THE DISCLOSURE, at level 2 of three (TOR-212), and one call rather than
    // the three attribute writes that used to stand here and again in
    // setMetaExpanded below and again in run-table.js's setRunExpanded.
    //
    // What did NOT move is the sibling sweep above it: "one file's detail at a
    // time" is a decision about a SET of disclosures, the level above made the
    // opposite decision for the three written reasons in this method's own
    // heading, and a component that closed siblings would impose one of those
    // answers on all three levels.
    //
    // The row's <label> is what carries data-expanded, not the <li> and not
    // the slot: an open file is a state of the ROW, the same way
    // .run-row[data-expanded="true"] is a state of the torrent's row rather
    // than of its detail. That is the `summary` the accordion was given at
    // mount - and since TOR-222 it is the element the rail rule keys off
    // directly, with no parent named to reach it.
    this.fileAccordion.apply(expanded);
    // THE SUMMARY STAYS, and that is a change of behaviour TOR-182 owes a
    // reason for. It used to be hidden while the file was expanded, because
    // "the collapsed-only summary line and the specs panel say the same thing
    // two different ways" - but that premise stopped being true when TOR-71
    // put the specs behind their own disclosure, collapsed by default: opening
    // a file now takes the resolution and the frame count off screen and
    // replaces them with nothing.
    //
    // The move is what makes keeping it the better answer rather than merely
    // the harmless one. The summary is a COLUMN of the file list now, read
    // down twenty-five rows, and a column that empties the row you are looking
    // at is a column that has to be re-found every time you open something.
  }

  // setMetaExpanded is setFileExpanded's counterpart one level down (TOR-71):
  // the single method that may change whether a file's metadata (.specs and
  // .tracks) is on screen, called from exactly two places - the metadata
  // toggle's own click handler, and once at mount to start every file's
  // metadata collapsed, with no auto-expand exception. No event that rebuilds
  // .specs/.tracks in place (renderFileMeta) touches fentry.metaExpanded, so a
  // person who opens the metadata keeps it open through every event that
  // follows.
  setMetaExpanded(expanded) {
    this.fentry.metaExpanded = expanded;
    // The innermost of the page's three disclosures (TOR-212), through the
    // same component as the two outside it - and the one level that dresses
    // nothing, which is what level 3 MEANS here rather than something it
    // happens to lack: there is no rail and no ground at this depth, because
    // both would be inside the file's rail, which is inside the torrent's
    // ground. accordion.js's level table is where that is decided.
    this.metaAccordion.apply(expanded);
  }

  // updateFileSummary keeps a row worth choosing by without opening it: the
  // file name and size are already on the row (file-list.js's renderFileList),
  // and this adds whatever of resolution and frame count are already known -
  // both update live (resolution the moment file_started arrives, the frame
  // count on every frame_ready) whether or not the file happens to be expanded
  // right now. Since TOR-182 it writes into that same row (.picker-summary)
  // rather than onto a title line inside the detail, and it is on screen open
  // or closed - see setFileExpanded for why that stopped being a repetition.
  //
  // It says how many frames the file has, and against what it was trying for
  // when the two differ.
  //
  // It counts frames rather than CELLS, which is a distinction the grid only
  // acquired once a point that produced nothing started being listed at all
  // (TOR-118): fentry.frames holds an entry for every planned point a finished
  // run recorded, failures included, so its plain size would report a holed run
  // of five frames as twelve. A count that is really a plan pretending to be a
  // result is the kind of quiet lie this project keeps finding.
  //
  // That count is capturedCells, shared with the row's Clear frames button
  // since TOR-183 rather than spelled twice: the button states how many frames
  // it will delete right beside this figure, and two independent counts of one
  // thing is two chances to be wrong about it.
  updateFileSummary() {
    const fentry = this.fentry;
    const parts = [];
    // fentry.media, not two fields of its own: the picture's size is one of the
    // things file_started said about this file, and it lives with the rest of
    // them (state.js's newFileState). Absent until the file has started, which
    // is why this is a guard and not a truthiness test on two numbers.
    const media = fentry.media;
    if (media && media.width && media.height) parts.push(media.width + "×" + media.height);

    const cells = gridCells(fentry);
    const captured = capturedCells(fentry);
    const planned = fentry.plan.length || cells.length;

    if (planned > captured) {
      parts.push(captured + " of " + planned + " frames");
    } else {
      parts.push(captured === 1 ? "1 frame" : captured + " frames");
    }
    this.summary.textContent = parts.join(" · ");
  }

  // renderFileMeta draws everything a file's own metadata says: its name on the
  // row, and inside its Metadata disclosure the specs and the two track groups.
  // All of it from fentry.media, which is where file_started's message now
  // lands (events.js's applyFileStarted).
  //
  // IT IS HANDED STATE, NOT A MESSAGE, and that is TOR-191 rather than a
  // preference. Written straight out of `ev` the way this used to be, the
  // codecs, the tracks and the duration existed nowhere but in these text
  // nodes - so nothing except another file_started could ever draw them again,
  // and for a finished file another one never comes. Now file_started is the
  // only writer of the fact and this is the only drawer of it, and either can
  // happen without the other.
  //
  // A file with no media yet draws nothing rather than a row of zeroes: absent
  // is not zero one level in either (state.js's newFileState).
  //
  // It never touches fentry.metaExpanded, which is what lets a person who
  // opened the metadata keep it open through every event that follows
  // (setMetaExpanded's own rule, unchanged by the move).
  renderFileMeta() {
    const fentry = this.fentry;
    const media = fentry.media;
    if (!media) return;

    // The engine's own path, restated on the row in the LIST's convention
    // rather than the block's: a base name with the whole path on its title
    // (file-list.js's renderFileList). The two used to differ - the block's
    // title line showed the full path, which in a season pack is the same
    // forty characters of directory twenty-five times over - and since
    // TOR-182 there is one line to show it on, so one of the two conventions
    // had to win. This one did because the list is read as a column.
    this.name.textContent = basename(media.path);
    this.name.title = media.path;

    this.specs.replaceChildren();
    // TOR-178: how many video files the TORRENT holds (M), not this one file's
    // own specs - the fact "N of M ... selected" used to state on the summary
    // line above, before the owner asked for that line gone. There is no
    // torrent-scoped disclosure anywhere in this detail to move it to - the
    // Metadata accordion this spec lands in is the only one that exists, and
    // it is otherwise scoped to one file - so this reuses it rather than
    // inventing new chrome for one fact. It is repeated once per file for the
    // same reason: cheaper than building a place that says it exactly once,
    // and every file's own Metadata is where a person already goes looking
    // for facts about the file's container.
    //
    // Read off fentry.entry rather than taken as a parameter, which is the
    // small version of TOR-191: a file entry knows which torrent it belongs
    // to, so whoever asks for a redraw does not have to carry one along.
    addSpec(this.specs, "Torrent files",
      fentry.entry.videos.length ? String(fentry.entry.videos.length) : "");
    addSpec(this.specs, "Resolution",
      media.width && media.height ? media.width + "×" + media.height : "");
    addSpec(this.specs, "Video",
      [media.codec, media.profile, media.fps ? media.fps.toFixed(2) + " fps" : "",
        bitrateLabel(media.videoBitrate)].filter(Boolean).join(" · "));
    addSpec(this.specs, "Overall bitrate", bitrateLabel(media.bitrate));
    addSpec(this.specs, "Duration", seconds(media.durationMs));

    this.tracks.replaceChildren(
      trackGroup("Audio", media.audio, audioLine),
      trackGroup("Subtitles", media.subtitles, subtitleLine),
    );
  }

  // renderFileProgress draws one file's own heartbeat line - frames, bytes,
  // peers - from fentry.heartbeat, and takes it off screen when there is none.
  //
  // TWO ROWS LEGITIMATELY HAVE NONE and they are opposite situations: a file
  // that has not started, and a file that has FINISHED (file_done sets the
  // field back to null, so the line goes because the reading went, not because
  // something remembered to hide an element still holding a stale
  // "11 / 12 frames").
  //
  // The stall heartbeat never reaches here - events.js drops it before this
  // field, since frames_total 0 on it means "no capture point has been
  // attempted" rather than a real 0-of-0 plan, and applying it would blink a
  // genuine 3/12 to 0/0 and back every five seconds.
  renderFileProgress() {
    const beat = this.fentry.heartbeat;
    this.progress.hidden = !beat;
    this.progress.textContent = beat
      ? beat.framesDone + " / " + beat.framesTotal + " frames · " +
          bytesLabel(beat.downloaded) + " downloaded · " + beat.peers + " peer(s)"
      : "";
  }

  // renderFileGrid is the pair the grid and the row's own count of it always
  // move as: renderFrames rebuilds the cells from fentry.frames/plan/skipped,
  // updateFileSummary restates what they add up to. Every event that changes
  // the grid changes that count, so this is the single hook events.js names
  // rather than the two of them separately.
  //
  // frame_skipped now restates the count as well, which it did not before
  // TOR-191. Nothing on screen moves: a skipped point already had a planned
  // cell, so capturedCells and the planned total both come out the same - what
  // it buys is one fewer way for the grid and its own caption to disagree.
  renderFileGrid() {
    this.renderFrames();
    this.updateFileSummary();
  }

  // renderFrames rebuilds the grid from what the file is known to have.
  //
  // Rebuilding the whole grid rather than inserting into it keeps one rule -
  // the DOM is the map - instead of two: a live run appends in plan order and
  // would look sorted either way, while a set fetched from disk arrives all at
  // once and interleaves with what is already shown.
  renderFrames() {
    this.grid.replaceChildren(...gridCells(this.fentry).map((cell) => this.frameFigure(cell)));
  }

  // renderReach draws where in the file this set's frames were actually taken
  // from (TOR-111): a strip of blocks along the file with those stretches
  // marked, plus the swarm chip TOR-142 added and TOR-153 moved onto this row.
  // 44 of 270 pieces is the argument of the whole product and we could only
  // state it as a sentence.
  //
  // WHERE THE DATA COMES FROM, which TOR-179 changed under this function
  // without changing its shape. It used to be the last run's own claim log
  // (cache.Run.Claimed): one traversal, overwritten by whichever run wrote
  // last. A top-up (TOR-152) works only the points an earlier run missed, so
  // that log said "a couple of blocks at the end" while twenty frames on disk
  // covered the whole file - the strip and the frame grid beside it disagreed
  // by construction, which is the bug the owner reported. Each frame now
  // records its own byte ranges in the manifest (manifest.Frame.ByteRanges),
  // and reach.claimed is the union of them: cumulative because the frames are,
  // durable because the manifest is.
  //
  // WHICH SET. The ranges belong to the frames of one set, so the strip is one
  // set's, not the merged grid's - and the set drawn is the one with the most
  // capture points, which is the set the grid is mostly showing. Stated in the
  // caption rather than left for the reader to wonder about.
  //
  // WHAT IS NOT DRAWN, and this is a deliberate refusal, twice over now.
  // TOR-111 already refused to mark capture points as TIMES on this BYTES
  // axis - that needs an assumption of constant bitrate no container owes us,
  // a tick that would be a guess drawn to look like a measurement. TOR-142
  // then drew a second, separate row of ticks instead, positioned from these
  // same claimed piece ranges rather than from a timecode - honest on its own,
  // but a second drawing of a fact this strip's blocks already carry. TOR-153
  // removes that row rather than reconciling it: a filled block already IS a
  // capture point's footprint, at the same resolution the ordering density is
  // shown at, so nothing further is added here for "where captures came from".
  //
  // THE SWARM CHIP is the one part of TOR-142 that answered a genuinely
  // different question - not "where did this run reach" but "what does the
  // swarm currently hold, torrent-wide" (core.Progress.Swarm's own doc) - so it
  // stays its own box in a SEMANTIC colour (ok/warn/bad/unknown), never the
  // accent and never a fill on the strip's own axis: entry.live.swarm is one
  // mean copies-per-piece figure for the WHOLE TORRENT, not a per-position
  // reading, so painting it along this file's span would claim a resolution it
  // does not have. Its title carries the exact figure, in the identical
  // sentence the six live columns already show (availabilityCellTitle) - not a
  // second, independently-worded copy of it.
  renderReach(sets) {
    const fentry = this.fentry;

    // ABSENT IS NOT ZERO. A set whose manifest was written before the frames
    // recorded where they came from (TOR-179 added the field without bumping
    // manifest.Version, so those results stay readable and simply cannot say)
    // has nothing to say about WHERE its frames came from - so the strip
    // hatches instead of rendering full or empty, neither of which would be
    // true, and the row stays visible rather than hiding outright (as it did
    // before TOR-153) because the swarm chip beside it doesn't depend on this
    // file's own frames and has its own reading to show regardless. The server
    // omits reach entirely for that case rather than sending an empty one
    // (web.reachOf), which is what this filter reads.
    const withReach = (sets || []).filter((s) => s.reach && s.reach.pieces > 0);
    const known = withReach.length > 0;
    this.reachStrip.dataset.known = String(known);
    if (!known) {
      this.reachStrip.replaceChildren();
      this.reachStrip.setAttribute("aria-label",
        "Where this file's frames came from is not recorded - the set was captured before that was kept");
      this.reachNote.textContent = "where the frames came from was not recorded for this set";
      this.reach.hidden = false;
      this.renderAvail();
      return;
    }
    withReach.sort((a, b) => (b.count || 0) - (a.count || 0));
    const set = withReach[0];
    const reach = set.reach;

    // How many blocks the strip holds: one per piece until there are more
    // pieces than blocks worth drawing, then one block per several pieces.
    //
    // Derived from the piece count alone, deliberately, not from the measured
    // width. A width-derived count would have to be redrawn on every resize and
    // every drag of a column border, and worse, the caption's "each block is N
    // pieces" would be true only until the window moved. Blocks stretch instead,
    // so the aggregation is a fact about the torrent rather than about the
    // viewport.
    const blocks = Math.min(reach.pieces, MAX_BLOCKS);
    const per = reach.pieces / blocks;

    // Claimed pieces as a flat lookup, so each block can ask how many of its
    // own were ordered without walking every range.
    const claimed = new Uint8Array(reach.pieces);
    for (const [begin, end] of reach.claimed || []) {
      for (let i = Math.max(0, begin); i < Math.min(reach.pieces, end); i++) claimed[i] = 1;
    }

    const frag = document.createDocumentFragment();
    for (let b = 0; b < blocks; b++) {
      const from = Math.floor(b * per);
      const to = Math.max(from + 1, Math.floor((b + 1) * per));
      let hit = 0;
      for (let i = from; i < to && i < reach.pieces; i++) hit += claimed[i];
      const span = Math.min(to, reach.pieces) - from;

      const block = document.createElement("span");
      block.className = "reach-block";
      // A fraction rather than a flag, which is what makes aggregation honest:
      // at one piece per block it is 0 or 1 and the strip is a map, and above
      // that it is a density and the caption says so.
      //
      // With a floor, because the honest fraction can be unreadable: 4 claimed
      // pieces of a 104-piece block is 0.038, which paints about one pixel and
      // leaves a reader unable to see WHERE the run reached - the one thing the
      // strip exists to show. So presence is drawn legibly and density is
      // carried by the height above that floor. Nothing is invented: a block
      // with no claimed piece stays empty, and the caption states the block
      // size so a reader knows a mark means "some of these 104".
      const fraction = span > 0 ? hit / span : 0;
      const fill = fraction === 0 ? 0 : REACH_FLOOR + (1 - REACH_FLOOR) * fraction;
      block.style.setProperty("--fill", fill.toFixed(3));
      frag.append(block);
    }
    this.reachStrip.replaceChildren(frag);

    // "(0%)" for a run that ordered forty-four pieces of ten thousand would be
    // a rounding that contradicts the number beside it. Under half a percent is
    // reported as under one, which is both true and the product's whole point.
    const pct = (100 * reach.claimed_pieces) / reach.pieces;
    const pctText = reach.claimed_pieces === 0 ? "0%"
      : pct < 0.5 ? "<1%"
      : pct < 10 ? pct.toFixed(1) + "%"
      : pct.toFixed(0) + "%";
    const parts = [
      reach.claimed_pieces + " of " + reach.pieces + " pieces the frames came from (" + pctText + ")",
      bytesLabel(reach.claimed_pieces * reach.piece_bytes) + " of " +
        bytesLabel(reach.pieces * reach.piece_bytes),
    ];
    // The aggregation is stated, never silently faked: a block standing for
    // several pieces is shaded by how many of them the frames came from.
    if (per > 1) parts.push("each block is " + Math.round(per) + " pieces");
    // PARTIAL KNOWLEDGE IS SAID OUT LOUD, and this is the one case where the
    // strip is a floor rather than an answer: a set topped up onto one captured
    // before TOR-179 knows where its newer points came from and nothing about
    // its older ones, so the union under-reports and no drawing can tell. The
    // whole-set case says nothing extra, because there is nothing missing.
    if (reach.located < reach.captured) {
      parts.push(reach.located + " of " + reach.captured + " frames say where they came from");
    }
    if (withReach.length > 1) parts.push("set " + set.params.slice(0, 8));

    this.reachNote.textContent = parts.join(" · ");
    this.reachStrip.setAttribute("aria-label",
      "Pieces of this file the frames came from: " + reach.claimed_pieces + " of " + reach.pieces);
    this.reach.hidden = false;
    this.renderAvail();
  }

  // renderAvail updates the swarm chip beside the reach strip (TOR-142's one
  // surviving idea, moved onto that row by TOR-153 - see renderReach's own doc
  // for the shapes-must-stay-different reasoning and for why nothing here
  // draws capture-point marks any more). It is a plain function of
  // entry.live.swarm, independent of this file's own reach: renderReach calls
  // it after every redraw of the strip, known or not, and file-list.js's
  // refreshSwarm calls it on its own for a heartbeat that only moved the swarm
  // reading and touched no file's claims at all.
  //
  // ok/warn/bad is the same three-way health judgement a reader already has to
  // make from the number itself (SwarmAvailability's own doc: below 1.0 means
  // pieces are missing, unavailable means some are held by nobody at all) -
  // drawn here so it can be read at a glance, with the exact figure a hover
  // away (availabilityCellTitle, the identical sentence the six live columns
  // show) rather than lost by being reduced to a colour.
  renderAvail() {
    const entry = this.fentry.entry;
    const swarm = availabilityReading(entry);

    const health = !swarm ? "unknown" : swarm.unavailable > 0 ? "bad" : swarm.copies_per_piece < 1 ? "warn" : "ok";
    this.availSwarm.dataset.health = health;
    this.availSwarm.title = availabilityCellTitle(entry);
    this.availSwarmText.textContent = swarm ? swarm.copies_per_piece.toFixed(2) + "×" : ABSENT;
  }

  // frameFigure draws one cell in the state gridCells gave it.
  //
  // The states are spent unevenly on purpose. An exact frame gets no marking at
  // all, because most cells are exact and a grid that marks every cell marks
  // none of them - the picture is the content. A shifted frame keeps the plain
  // thumbnail and gains exactly one signal, a warn rule under it, with the
  // reason on inspection. A BORDER is spent on one state only, failure, so that
  // a border in this grid means "nothing was captured here" and nothing else.
  // And a pending cell is deliberately the quietest of the four: it is the
  // normal state of a run that is still working, and it must not read as an
  // error while it waits.
  frameFigure(frame) {
    const figure = document.createElement("figure");
    figure.tabIndex = 0;
    figure.className = "thumb thumb-" + frame.state;

    // A pending or failed cell has no url - nothing was captured there, or not
    // yet - so there is no src to give an <img>. <img src=""> would ask the
    // browser to fetch the page itself; a box the same shape as a thumbnail
    // says "nothing here" without doing that.
    let img = null;
    if (frame.url) {
      img = document.createElement("img");
      // RESOLVED HERE, not where the frame was recorded (TOR-191): url() needs
      // document.baseURI and the access token, which is a fact about this page
      // rather than about the frame - so a frame keeps the PATH the server
      // named and both of its sources (events.js's applyFrame off the socket,
      // detailFrame off disk) store one shape.
      img.src = url(frame.url);
      img.alt = "frame at " + timecode(frame.timeMs);
      img.loading = "lazy";
    } else {
      img = document.createElement("div");
      img.className = "thumb-box";
      if (frame.state === "failed") {
        const code = document.createElement("span");
        code.className = "thumb-code";
        code.textContent = frame.error || "no frame";
        img.append(code);
      }
    }

    const caption = document.createElement("figcaption");
    // A pending or failed cell shows where the point WAS PLANNED - nowhere else
    // is true for it - and a captured one shows where the frame came from.
    caption.textContent = timecode(frame.timeMs);
    // Only a cell that actually HAS a frame says why it moved. A failed point's
    // manifest shift is ShiftFailed, whose serialised value is the word
    // "unavailable" - so printing it here put "unavailable" in the caption of a
    // cell whose interior said "read_stalled", two words about one cell and the
    // louder one wrong. Nothing moved: there is no frame. The state carries
    // that, and the code inside the cell carries the reason.
    if (frame.state === "shifted" && frame.shift) {
      caption.append(" ");
      const note = document.createElement("span");
      note.className = "shifted";
      note.textContent = frame.shift;
      caption.append(note);
    }

    // The explanation lives on the figure rather than on the image, so it is
    // reachable on a cell that has no image.
    figure.title = cellTitle(frame);

    figure.append(img, caption);

    // The cross, only for a frame that both exists and names the result set it
    // lives in. The set is the other half of its address on disk (see
    // events.js's applyFrame); the existence is the rest - a failed point has
    // no file to
    // delete, and offering the cross on one would be offering an action that
    // cannot succeed.
    if (frame.params && frame.url) {
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "thumb-delete";
      remove.textContent = "×";
      const what = "Delete the frame at " + timecode(frame.timeMs);
      remove.title = what;
      remove.setAttribute("aria-label", what);
      remove.addEventListener("click", (event) => {
        event.stopPropagation();
        this.deleteFrame(frame);
      });
      figure.append(remove);
    }

    // Nothing to open full-size for a cell with no frame in it - there is only
    // the box standing in for one.
    const open = () => { if (frame.url) openFrame(img.src, caption.textContent); };
    figure.addEventListener("click", open);
    figure.addEventListener("keydown", (event) => {
      // Only the figure's own keys open it. Enter on the delete button inside
      // fires that button's click and then keeps bubbling to here, which would
      // open a lightbox on a frame that is on its way out.
      if (event.target !== figure) return;
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        open();
      }
    });

    return figure;
  }

  // deleteFrame removes one frame from disk - its manifest record and its file
  // both, which is what keeps the rest of the run openable (core.DeleteFrame).
  //
  // There is no confirmation step: TOR-118 asks for a cross, this panel's own
  // Regenerate is the way back, and a dialog on every thumbnail would make
  // clearing a handful of bad frames worse than the frames are. What the server
  // answers with is the file's remaining frames, read back off disk, so the
  // grid is replaced from that rather than by removing a tile here and hoping
  // the two agree. A failure changes nothing on screen.
  //
  // Replaced, not merged: a frame the grid still shows for a file whose
  // manifest is gone has to go too. The one thing that costs is a regeneration
  // in flight for this same file - its live frames are not on disk yet, so they
  // drop out of the grid until its own file_done puts them back moments later.
  async deleteFrame(frame) {
    const fentry = this.fentry;
    showError("");
    if (!fentry.entry.infohash || !frame.params) return;

    const path = ["runs", fentry.entry.infohash, "files", fentry.index, "frames", frame.index].join("/");
    const target = url(path);
    target.searchParams.set("params", frame.params);

    try {
      const answer = await del(target);
      const detail = answer.file;
      // The delete happened even when something derived from it did not - a
      // contact sheet that could not be rebuilt, today. Saying so beside the
      // grid is the honest report; refusing to re-render would leave a frame
      // on screen that is no longer on disk (TOR-78).
      if (answer.warning) showError(answer.warning);
      fentry.frames = new Map();
      for (const f of (detail && detail.frames) || []) {
        fentry.frames.set(f.time_ms, detailFrame(f));
      }
      this.renderFrames();
      this.updateFileSummary();
      log(fentry.entry, "deleted the frame at " + seconds(frame.timeMs) + " of file " + fentry.index);
    } catch (err) {
      showError(String(err.message || err));
    }
  }

  // afterClear replaces this file's whole detail from what the server read back
  // off disk after a Clear frames (TOR-183). file-list.js owns the request, the
  // button and the note beside the row; the frames, the strip that described
  // them and the artefact link are this element's, and this is where they are
  // replaced from the answer.
  //
  // REPLACED FROM WHAT THE SERVER READ BACK, never edited here: a page that
  // removed its own tiles and hoped the two agreed is exactly how a frame
  // comes to be on screen that is not on disk.
  afterClear(cleared) {
    const fentry = this.fentry;
    fentry.frames = new Map();
    for (const f of (cleared && cleared.frames) || []) {
      fentry.frames.set(f.time_ms, detailFrame(f));
    }
    // The plan and the live skips go with the frames, for the reason
    // loadFileDetail drops them: they describe one run's attempt, and what
    // is on screen now is every set this file has left on disk. Keeping the
    // plan would lay the grid out from points nothing can fill.
    fentry.plan = [];
    fentry.skipped.clear();
    this.renderFrames();
    this.updateFileSummary();
    // AND THE STRIP THAT DESCRIBED THEM. renderReach draws where in the
    // file the frames came from; with no frames left there is nothing for
    // it to be about, and it has no "nothing" state - handed an empty list
    // it says "where the frames came from was not recorded for this set",
    // which is a sentence about a set that no longer exists. So it is taken
    // off screen rather than redrawn.
    //
    // AND THE CONTACT SHEET LINK, which the browser found rather than any
    // test here: it used to be put on screen by the file's own file_done and
    // nothing ever took it away, so after a clear it stood there offering a
    // picture that had just been deleted - a link whose only possible answer
    // is a 404. It is exactly the untruth TOR-183 is about, one element
    // over from the frames. Since TOR-191 the link is drawn from
    // fentry.sheetURL, so removing it is the same act as putting it there
    // rather than a second, opposite piece of DOM handling.
    if (capturedCells(fentry) === 0) {
      this.reach.hidden = true;
      fentry.sheetURL = "";
      this.renderFileLinks();
    } else {
      this.renderReach((cleared && cleared.sets) || []);
    }
  }

  // loadFileDetail asks the server for every frame this torrent has on disk
  // for one file, across every result set, and merges them into the grid.
  //
  // This is the only way the other sets can be reached at all: a frame URL is
  // a handle the server minted for a path its own event stream named, and a
  // sibling set was never announced to this page - a replay is addressed by
  // one params directory and no event carries a params name. So the frames of
  // a regeneration are not "somewhere in the event history"; they have to be
  // asked for.
  //
  // A 404 is the normal answer while a run is still going: the manifest this
  // reads is written when a file finishes, so there is nothing on disk to
  // merge yet, and the live grid is already correct. Anything else is logged
  // and changes nothing.
  async loadFileDetail() {
    const fentry = this.fentry;
    const entry = fentry.entry;
    const index = fentry.index;
    if (!entry.infohash) return;

    try {
      // Assembled from segments rather than written as one string: a path
      // segment spelled inline would read as a site-root path and trip
      // TestTheFrontendUsesNoAbsolutePaths, which guards against exactly the
      // absolute paths that break under a base path. url() then resolves the
      // relative path against document.baseURI.
      const path = ["runs", entry.infohash, "files", index].join("/");
      const response = await fetch(url(path));
      if (response.status === 404) return;
      if (!response.ok) throw new Error(response.statusText);

      const detail = (await response.json()).file;
      if (!detail || !detail.frames) return;

      for (const frame of detail.frames) {
        fentry.frames.set(frame.time_ms, detailFrame(frame));
      }
      fentry.detailLoaded = true;
      // The plan has done its job and now stands in the way of the truth: it
      // describes one run, while this response spans every result set the
      // torrent holds for the file, merged by timecode into cells no single
      // plan accounts for (TOR-110). Nothing is arriving any more either, so
      // there is no reflow left for the reserved cells to prevent.
      fentry.plan = [];
      fentry.skipped.clear();
      this.renderFrames();
      this.updateFileSummary();
      this.renderReach(detail.sets);
    } catch (err) {
      log(entry, "could not read file " + index + "'s frames: " + (err.message || err));
    }
  }

  // renderFileLinks draws the one artefact link a finished file offers, from
  // fentry.sheetURL.
  //
  // TOR-171: the contact sheet and nothing else. ev.manifest_url still rides
  // the NDJSON stream (server.go's record() keeps publishing it) and is
  // deliberately never kept on the file entry, so there is nothing here that
  // COULD turn the raw JSON manifest into a link - the page has no use for one,
  // and something else reading the stream might, which is the whole reason the
  // field is still there to read.
  //
  // AND IT TAKES THE LINK AWAY, which is the half a browser found rather than
  // any test (TOR-183). While the link was built by the file_done handler and
  // nothing ever removed it, a clear left it standing there offering a picture
  // that had just been deleted - a link whose only possible answer is a 404.
  // Drawn from a field, taking it away is the same act as putting it there.
  renderFileLinks() {
    const links = this.fentry.sheetURL ? [link(this.fentry.sheetURL, "contact sheet")] : [];
    this.links.replaceChildren(...links);
    this.links.hidden = links.length === 0;
  }

  // renderFileDone is what a finished file redraws: the heartbeat line goes (the
  // reading is null by now), the contact sheet appears, and the other result
  // sets of this file are read back off disk.
  //
  // That last one is a request rather than a redraw, and it belongs to this
  // moment rather than to any other: the manifest loadFileDetail reads is
  // written when a file finishes, so this is the first instant the sibling sets
  // can be reached at all - and the instant this run's own frames become part
  // of what a later open would find.
  renderFileDone() {
    this.renderFileProgress();
    this.renderFileLinks();
    this.loadFileDetail();
  }

  // regenerate asks for this one file again at a different frame count.
  //
  // It is an ordinary run, started exactly the way the intake line starts one:
  // the count is part of core.ParamsKey and frames.Plan spreads its points
  // evenly across the window, so asking for 21 moves every timestamp rather
  // than adding one to the 20 already taken. That makes it a SEPARATE result
  // set on disk, and nothing of the old one is touched or deleted - TOR-69 is
  // what later shows both sets as one grid.
  //
  // The profile is whatever the intake select currently shows: a finished
  // run's own profile is not on the wire (run_state does not carry it, and the
  // listing only has the opaque params hash), so this is the one mode a person
  // can actually see while pressing the button.
  //
  // A run started from a dropped .torrent cannot be repeated this way: its
  // source is a staged temp file, gone once the run ended, so the new run
  // fails the way any unopenable source does - with a failed row and the
  // server's own message. Keeping the .torrent is TOR-73.
  async regenerate() {
    const fentry = this.fentry;
    const entry = fentry.entry;
    showError("");
    if (!entry.source) {
      showError("this torrent has no source to repeat");
      return;
    }

    const asked = intake();
    const n = parseInt(this.regenCount.value, 10);
    const count = Number.isInteger(n) && n > 0 ? n : asked.count;

    this.regenGo.disabled = true;
    try {
      const info = await post("runs", {
        source: entry.source,
        mode: asked.mode,
        // swarm.Select already takes a torrent index as a spec, so the index
        // the events arrived under is the whole selection.
        files: [String(fentry.index)],
        count,
      });
      began(info);
    } catch (err) {
      showError(String(err.message || err));
    } finally {
      this.regenGo.disabled = false;
    }
  }
}

// Native, no bundler: this is the whole registration mechanism, and importing
// this module for its side effect is how file-list.js gets the element defined
// before it creates the first one.
customElements.define("file-detail", FileDetail);

// THE SURFACE IS THE CLASS PLUS setServices (TOR-209's rule, applied by
// TOR-208), and this module is where it saved the most: seven names went.
// BODY, MAX_BLOCKS, REACH_FLOOR, SHIFT_REASON, FAILURE_REASON, cellTitle and
// detailFrame were imported by NOBODY - checked across the whole repository -
// and every Go test that names one of them (perfiledetail_test.go's BODY,
// eventstate_test.go's detailFrame) lifts it out of this module's TEXT, which
// needs no export. setServices stays because app.js imports it by name and it
// is point 7 of the element pattern. See frame-panel.js's own export block for
// the full reasoning, and for why an extra name here is not free any more.
export { FileDetail, setServices };
