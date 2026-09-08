// THE LIST OF EVERY FILE THE TORRENT HOLDS, as a custom element (TOR-195),
// and the middle of the three this ticket nests.
//
// It follows the pattern the three elements before it settled: light DOM,
// parts found with this.querySelector after the markup exists with a loud
// throw naming any that is missing, listeners added once on nodes this element
// created inside itself, and one method per thing the outside is allowed to
// ask for. run-detail.js's header carries the two things this file shares with
// it and does not repeat: why the markup is a template here rather than
// declarative in index.html, and WHY THERE IS NO disconnectedCallback (the
// table's re-sort moves a run's rows, so everything in them is disconnected
// and reconnected on every redraw - a teardown here would kill every tick box
// on the first event and build() would have to rebuild the world on the next).
//
// WHAT IT IS: every file the torrent carries, video or not (TOR-180), each row
// a tick that starts that file on the spot (TOR-181), a price per row before
// it is spent (TOR-50), Select all with its arming step, the four verdicts an
// un-tick can carry (TOR-184), and Clear frames (TOR-183). One instance per
// torrent, mounted last inside that torrent's own <run-detail>.
//
// ---------------------------------------------------------------------------
// THE THIRD BOUNDARY, and the one this element is on both sides of.
//
// ABOVE IT: the run's detail owns everything about the RUN and never learns
// what is in this list. It creates one of these, appends it, and calls four
// methods on it (syncFileList on every redraw, reset on a run_state reset,
// rebuild when the list changes shape, refreshSwarm on a heartbeat).
//
// BELOW IT: THE LIST OWNS THE ROW, THE FILE'S DETAIL OWNS WHAT THE ROW OPENS
// ONTO - which is exactly the line run-table.js drew one level up, repeated
// here because the shape repeats. This element builds every <li>: the
// disclosure, the checkbox, the name, the size, the price, the "in the next
// pass" note, the summary, Clear frames and the note under it. It creates one
// <file-detail> per VIDEO row, appends it as a SIBLING of the row inside the
// <li>, and never learns what goes in it - the same seam detailCell is one
// level up.
//
// TWO THINGS SIT EXACTLY ON THAT LINE, and each is split rather than fudged:
//
//   - THE DISCLOSURE'S ARIA. aria-expanded is the row's own state and is
//     written by the detail (setFileExpanded, because it travels with the
//     expanded state); aria-controls names the slot's id, which only the
//     detail can mint, and is set HERE from the element's regionId. One
//     attribute each, on the same button - the same division run-table.js
//     makes with the run row's own toggle.
//   - THE CLICK. Which row was clicked, and whether there is a detail to open
//     at all, is this element's (toggleFileDetail). What OPENING means - the
//     accordion that closes every sibling, and reading the other result sets
//     off disk the first time - is the detail's (its toggle()).
//
// AND FOUR ROW ELEMENTS CROSS IT, deliberately: a file's detail is handed its
// row's <li>, its disclosure, its name and its summary span at mount, because
// since TOR-182 THE ROW IS THE FILE'S TITLE LINE (see file-detail.js's own
// heading) - the resolution and frame count it keeps current are a COLUMN of
// this list, not a line inside the slot. They are handed over once, in one
// bundle, rather than reached for.
// ---------------------------------------------------------------------------

import {
  FINAL,
  basename,
  bytesLabel,
  cancellable,
  framesOnDisk,
  nextDetailId,
  passCount,
  untickedVideos,
} from "./state.js";
// Imported for its side effect - the module's last statement defines
// <file-detail> - and for nothing else. Importing it here rather than from
// app.js is what guarantees the definition exists before the first
// createElement("file-detail") in renderFileList, since a module's imports
// evaluate before its own body does.
import "./file-detail.js";

// THE SEVEN SERVICES THE PAGE INJECTS, in the shape events.js's setView and
// the other elements' setServices already use, and for the same reason: each
// is something only app.js's bootstrap can do, and a missing name has to fail
// while the wiring is being written rather than at the first tick.
//
//   url        resolves a path against document.baseURI and attaches the
//              access token. The one request this element addresses itself
//              (the clear's DELETE) goes through it.
//   post       the one place a POST is sent, with its error shape.
//   del        the one place a DELETE is sent. It takes a whole URL rather
//              than a path, because a clear carries no query parameter today
//              but the shape is url()'s (see app.js's del).
//   log        one activity-log line, tagged with which torrent it is about.
//   showError  the page's own error line, above the table.
//   count      what the intake line currently asks for, as something a
//              request can carry (app.js's countValue). This is the OTHER
//              input every price on this list follows, beside the ticks, and
//              a person may well be adjusting it while reading the list.
//   cancelRun  what un-ticking a file the engine is fetching means, because
//              there is no per-file stop (see stopFetch). The SAME function
//              the row's own ✕ and the detail's Cancel reach, so there is one
//              place a cancel is sent from.
//
// Undefined until setServices runs, which is checked there rather than at each
// call site.
let url = null;
let post = null;
let del = null;
let log = null;
let showError = null;
let count = null;
let cancelRun = null;

const SERVICES = ["url", "post", "del", "log", "showError", "count", "cancelRun"];

function setServices(services) {
  for (const name of SERVICES) {
    if (typeof services[name] !== "function") {
      throw new Error("file-list: setServices needs a " + name + "() function");
    }
  }
  ({ url, post, del, log, showError, count, cancelRun } = services);
}

// LIST is the section this element wraps, and it is a template here for the
// reason run-detail.js's header gives: there is one per torrent and index.html
// has never held it.
//
// The .picker-* class names are kept on purpose even though this is no longer
// a picker at all: since TOR-181 a tick IS the decision, so nothing here
// stages one. Two of the names TOR-180 kept for TOR-181's sake are gone with
// the thing they named (.picker-go and the foot's own .picker-cost), and
// .picker-cost has been re-used for the figure that moved onto each file's
// row. Renaming the rest mid-epic would make TOR-182..184 describe selectors
// that no longer exist.
//
// NO BUTTON UNDER THE LIST, and that is the whole of TOR-181: a tick starts
// that file's frames on the spot (tickFile), so a "Take frames" under the list
// would be a second click for something already done - and the cost line that
// sat beside it, which is TOR-50's whole warning, was a price shown where the
// decision no longer is. It is now on each file's own row, beside the box that
// spends it.
//
// Select none is gone too, and for a sharper reason than tidiness: it used to
// clear a selection nobody had committed. A tick is now irreversible - the
// fetch has started - so a control that unticks boxes would say it can stop
// something it cannot. TOR-184 is the ticket that gives un-ticking a real
// meaning (cancel these), and it did not bring the control back with it.
const LIST =
  '<section class="picker" hidden>' +
    '<p class="picker-head">' +
      '<span class="picker-title"></span>' +
      '<button type="button" class="picker-all" hidden>Select all</button>' +
      // The armed note: what Select all is about to spend, and the ask for
      // a second click. role=status rather than aria-live on the button,
      // because the sentence is the new information and the button's own
      // label is only its short form (armSelectAll).
      '<span class="picker-armed" role="status"></span>' +
    '</p>' +
    '<ul class="picker-list"></ul>' +
  '</section>';

// ---------------------------------------------------------------------------
// WHAT THE TORRENT HOLDS, AND WHAT EACH FILE COSTS TO TAKE.
//
// Since TOR-180 this list is a row's ordinary content: every file the
// torrent carries, video or not, in every state that knows a file list
// rather than only while a multi-file torrent is parked waiting to be picked
// from (TOR-67).
//
// SINCE TOR-181 A TICK IS THE DECISION. There is no button under the list
// any more: ticking a box starts that file's frames on this row's own run
// (tickFile), which is why the price moved onto each file's row. A cost
// shown where a button used to be is a cost nobody sees, and the price is
// the whole point of showing one - -n is frames PER video file, so a torrent
// of six quality variants costs six times the intake's number (TOR-50), and
// the moment that multiplication becomes a decision is the checkbox.
//
// A TICK GROWS ONE RUN. It never opens a second row or a second run beside
// the first: a parked torrent's tick lets it out of needs-action and into
// the queue, a tick on a torrent still queued joins the pass it is already
// waiting to make, and a tick on one that is already fetching is held by the
// server until that pass ends and then starts on the same entry
// (Server.DecideRun, runEntry.pending). The row says which of the three
// happened, per file: "in this run" or "in the next pass".
//
// The non-video files are here because they answer the question a count
// never could - "what did I actually download" - and because they are how a
// person finds out that the film they were after is a .nfo, some artwork and
// a sample. They cannot be ticked: swarm.SelectVideos decides what frames
// can be taken from, the server validates a tick against exactly that list
// (runEntry.holdsFile), and offering a tick the server would refuse would be
// a control that lies.
//
// SAYING SO WITHOUT COLOUR. They read in --ink-2 (never --ink-3, which
// TOR-158 took off text for failing AA on every surface), and grey alone
// must not be what carries "you cannot tick this" - somebody who cannot
// separate the two greys still has to know. Two more carriers do it, and
// neither is a colour: the checkbox is ABSENT, and where it would have sat
// there is an em dash instead, which is this page's own established mark for
// a reading that is not there (every absent metric cell in the run table
// draws one). The row's title says why in prose for a pointer or a screen
// reader that reads one.

// WHY_NOT_VIDEO is the prose on a row that cannot be ticked. It names both
// reasons a file can be missing from the video list, because the page cannot
// tell them apart and must not guess: the extension is not one swarm tries,
// OR it is (a .mkv, say) and the file is a sample - small beside the largest
// video in the same torrent, which swarm.looksLikeSample drops on purpose.
// Wording that said only "not a video" would be a plain lie on the second
// case, which is the one this list exists to reveal.
const WHY_NOT_VIDEO =
  "torpeek takes no frames from this file — either its extension is not one " +
  "it opens, or it is a sample or trailer beside a much larger video";

// framesLabel is one file's price, in the unit TOR-50's trap is measured in.
//
// Frames rather than bytes, and that is deliberate: the frame count is the
// thing that multiplies per file and the thing this page can state exactly.
// What a frame costs in traffic is the server's arithmetic, priced off a
// record on disk and shown where the server has computed it
// (.run-again-cost for a top-up); a byte figure invented here would be a
// guess wearing the authority of a measurement.
function framesLabel(n) {
  return n ? n + " frames" : "server default";
}

class FileList extends HTMLElement {
  constructor() {
    super();

    // The record this list belongs to, set by bind() once the entry exists.
    this.entry = null;
    // rows maps a torrent index to the elements of its row, so
    // updateFileCosts can re-state every box's checked/disabled state and
    // every figure without rebuilding the list - the list is rebuilt only when
    // the file list itself changes, and rebuilding it on each event would drop
    // the scroll position of a season pack mid-tick.
    //
    // Since TOR-182 a row also carries the <file-detail> its file's whole
    // detail is built into (`block`), which turns "rebuilding it on each event
    // would drop the scroll position" from a courtesy into a correctness rule:
    // a rebuild now throws away every frame grid, every open disclosure and
    // every element the detail is holding a reference to. entry.fileListSig
    // (state.js) is what makes that impossible rather than merely unlikely,
    // and events.js's applyFileList is what reads it.
    //
    // ON THIS SIDE OF THE SPLIT, not in newRunState, because what it holds is
    // elements - and since TOR-195 not on the entry either: it is this
    // element's own DOM, and nothing outside this file reads it.
    this.rows = new Map();
  }

  connectedCallback() {
    this.build();
  }

  // build puts this element's own markup inside it, finds its parts and wires
  // the two controls in its head - ONCE, however many times this element is
  // connected. See run-detail.js's header for why that guard is load-bearing
  // rather than defensive, and why there is no teardown to match.
  build() {
    if (this.pickerEl) return;

    this.innerHTML = LIST;

    // Found inside THIS element rather than by document id, so one torrent's
    // list cannot pick up another's parts - which is the whole reason the page
    // can hold several open at once.
    this.pickerEl = this.querySelector(".picker");
    this.pickerTitle = this.querySelector(".picker-title");
    this.pickerList = this.querySelector(".picker-list");
    this.pickerAll = this.querySelector(".picker-all");
    this.pickerArmed = this.querySelector(".picker-armed");

    // A missing part is a wiring error, not a state to degrade into: every
    // method below dereferences these, so failing here names the part that is
    // absent instead of throwing "cannot read property of null" out of
    // whichever handler happens to fire first. A class renamed in LIST above
    // and not here is exactly the typo it catches.
    for (const [name, node] of Object.entries({
      pickerEl: this.pickerEl,
      pickerTitle: this.pickerTitle,
      pickerList: this.pickerList,
      pickerAll: this.pickerAll,
      pickerArmed: this.pickerArmed,
    })) {
      if (!node) throw new Error("file-list: no " + name + " inside the element");
    }

    // Arrow functions, every one of them: a `function` declaration used as a
    // listener is handed the LISTENING ELEMENT as its `this`, so `this.entry`
    // inside one would be the button's own missing field - which is TOR-194's
    // shipped regression in the one shape a text check cannot see.
    this.pickerAll.addEventListener("click", () => this.armSelectAll());
    // Escape disarms, and it is bound on the SECTION rather than the document:
    // the lightbox and the compare dialog both own Escape while they are open
    // (see their own handlers), and a key that reached past a modal to disarm
    // a button behind it would be the page acting on a screen nobody is
    // looking at. A blur disarms for the same reason a menu closes when you
    // click elsewhere - an armed control left standing is one somebody comes
    // back to and presses without re-reading.
    this.pickerEl.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && this.entry.armed) {
        event.stopPropagation();
        this.disarmSelectAll();
      }
    });
    this.pickerAll.addEventListener("blur", () => this.disarmSelectAll());
  }

  // bind hands this element the record it draws from. One assignment, for the
  // reason run-detail.js's own bind gives: the entry is assembled OUT OF the
  // DOM, so it cannot exist when the DOM is built, and every listener above
  // reads this.entry at the moment it fires rather than closing over it.
  bind(entry) {
    this.entry = entry;
  }

  // reset takes the whole list off screen - the DOM half of what
  // resetRunState (state.js) took out of the entry, called through
  // <run-detail>'s resetView.
  //
  // Since TOR-182 the file blocks live INSIDE these rows, so there is no
  // separate container to empty: replaceChildren below is what takes the
  // blocks off screen with the rows that hold them. entry.fileEntries, the
  // only handle on those blocks, was already cleared by resetRunState.
  reset() {
    this.rows.clear();
    this.pickerList.replaceChildren();
    this.pickerEl.hidden = true;
    // Select all's own two states, redrawn through the one function that may
    // draw that button. entry.armed itself was cleared by resetRunState - what
    // is left here is the label and the sentence beside it, and leaving those
    // standing would put a whole torrent's bill under one stray press on a row
    // that has just been emptied.
    this.syncSelectAll();
  }

  // reprice re-states every figure on a list that is on screen, and nothing
  // else. app.js's intake listener calls it for every torrent while somebody
  // is typing in the frame count, so the gate is here rather than in the
  // listener: a row with no list on screen has no price to restate.
  reprice() {
    if (this.pickerEl.hidden) return;
    this.updateFileCosts();
  }

  // rebuild is renderFileList under the name events.js knows it by. The list
  // is rebuilt only when it actually changed shape (applyFileList's
  // signature), which since TOR-182 is a correctness rule rather than a
  // courtesy about scroll positions - see renderFileList's own heading.
  rebuild() {
    this.renderFileList();
  }

  // mountFileDetail is the file factory's own half of state.js's
  // fileEntryFor: the entry for one video file of one torrent, created the
  // first time that file says anything.
  //
  // THE MOUNT POINT COMES FROM THIS LIST, and if there is no row for this
  // index there is nowhere to put a detail. That cannot happen for any
  // sequence the server produces - MetadataReady is published before the first
  // FileStarted on both paths that publish one at all (core.Engine.run, and
  // cacheHit.publish for a replay), and its file list holds every video the
  // run can then start - so this answers null rather than inventing a home,
  // and the events that reach it treat that the way they already treat any
  // absent reading (events.js guards every one of the hooks it would feed).
  mountFileDetail(index) {
    const entry = this.entry;
    const existing = entry.fileEntries.get(index);
    if (existing) return existing;

    const row = this.rows.get(index);
    if (!row) {
      // SAID OUT LOUD RATHER THAN SWALLOWED. Every caller copes with a null
      // the way this page copes with any absent reading, so a file whose row
      // never arrived costs its frames rather than a broken page - but it
      // would cost them in total silence, and the invariant this depends on
      // lives in another package. The log is where a person can see it
      // happened at all.
      log(entry, "file " + index + " has no row in the torrent's file list, so there is " +
        "nowhere to show it - the metadata that names the list must arrive first");
      return null;
    }

    const fentry = row.block.mount(entry, index, row);
    entry.fileEntries.set(index, fentry);
    return fentry;
  }

  // toggleFileDetail is what clicking a file's row does (renderFileList binds
  // it), and this element's half of that act: which row was clicked, and
  // whether it has a detail at all.
  //
  // A row whose file has said nothing yet has no detail to open - the ticks
  // are what start a file, and until one has, there is no metadata, no plan
  // and no frames to show - so this is a no-op there rather than an empty
  // panel. What OPENING means is the detail's own (its toggle).
  toggleFileDetail(index) {
    const fentry = this.entry.fileEntries.get(index);
    if (!fentry) return;
    fentry.block.toggle();
  }

  // refreshSwarm re-draws every one of this torrent's open file chips with a
  // fresh swarm reading (events.js's "progress" case, through the view's
  // renderSwarm hook). The reading is torrent-wide, not per-file
  // (file-detail.js's renderAvail), so every file shares the identical figure
  // and all of them redraw together rather than only the one file this
  // particular heartbeat happened to name.
  refreshSwarm() {
    for (const fentry of this.entry.fileEntries.values()) fentry.block.renderAvail();
  }

  // renderFileList BUILDS THE ROWS of the torrent's whole file list, from
  // entry.fileList and entry.videos - never from a message.
  //
  // events.js is what puts those two on the entry, off whichever of
  // metadata_ready or needs_action carried them, and it is also what decides
  // that this function should run AT ALL: the same list is not rebuilt, which
  // since TOR-182 is a correctness rule rather than a courtesy about scroll
  // positions (see applyFileList for the signature that settles it, and point
  // 1 below for what a needless rebuild destroys). Reaching this function
  // means the list genuinely changed shape.
  //
  // TICKS COME FROM THE RUN'S OWN SELECTION, not from nothing: a row whose
  // files are already decided shows which of them this run is working on (the
  // message's own "selected", folded into entry.picked by events.js), which is
  // real information the page had and threw away before. A parked torrent is
  // the exception and starts with NOTHING ticked -
  // it reaches that state precisely because it holds several video files, and
  // pre-ticking them all would put the most expensive possible run one
  // careless click away, since the count is per file and six variants at 20
  // frames is 120 captures (TOR-50). Select all is right there for the person
  // who does mean all of it, and since TOR-181 it states that total and asks
  // before spending it (armSelectAll).
  //
  // IT BUILDS THE ROWS AND NOTHING ELSE decides state: every checked,
  // disabled, priced or explained thing about a row is written by
  // updateFileCosts, which re-runs on every event without rebuilding the list.
  // A season pack is twenty rows in a scrolling box, and rebuilding it under
  // somebody mid-tick would throw their scroll position away each heartbeat.
  //
  // AND SINCE TOR-182 A ROW IS A DISCLOSURE, not just a line with a checkbox
  // on it: each video file's own detail - its metadata, its progress bar, its
  // frames and its buttons - is built into a <file-detail> inside that file's
  // own <li> (the element fills itself), which is the third depth this page
  // now has. Three things follow from that, and each has its own note below
  // where it lands:
  //
  //   1. THE ROW BUILD IS NOW LOAD-BEARING, not a redraw. It used to be free
  //      to rebuild the list on every message that carried one; now a rebuild
  //      detaches every frame grid on the row. entry.fileListSig - kept in
  //      state.js, read in events.js - is what makes a second metadata_ready,
  //      which a top-up and a retry both publish onto this same entry, never
  //      reach this function at all.
  //   2. THE ROW OWNS THE TOGGLE, not the detail inside it. The listener is on
  //      .picker-file and the detail is its SIBLING inside the <li>, never its
  //      descendant - the same shape, and for the same reason, as the run
  //      row's own detail being a separate <tr> (see run-table.js): a click on
  //      a thumbnail or on Regenerate must not close the thing it is being
  //      used on.
  //   3. THE TICK AND THE DISCLOSURE ARE ONE ROW WITH TWO MEANINGS, and they
  //      cannot collide, because a file gets a detail exactly when it has been
  //      asked for - and updateFileCosts disables the box of every asked-for
  //      file. So a row either spends traffic (no detail yet) or opens what
  //      that traffic bought (box disabled), never both.
  renderFileList() {
    const entry = this.entry;
    // A FRESH MAP, not a cleared one: every bundle in the old map points at a
    // row this rebuild is about to detach, and events.js has already dropped
    // the file entries that pointed into them (applyFileList) - which is the
    // only handle on the blocks, and had to go first.
    this.rows = new Map();

    const tickable = new Set(entry.videos.map((video) => video.index));

    this.pickerList.replaceChildren(...entry.fileList.map((file) => {
      const item = document.createElement("li");
      item.className = "picker-item";
      const video = tickable.has(file.index);
      // The styling hook, and the one thing filelist.css keys the grey off -
      // the colour itself lives in :root, never here
      // (TestEveryColourComesFromAToken).
      item.dataset.video = String(video);

      // A <label> only for a row that has a control to label; a label wrapping
      // no input is furniture pretending to be interactive.
      const row = document.createElement(video ? "label" : "span");
      row.className = "picker-file";

      // THE DISCLOSURE (TOR-182), first in the row and the same mark the
      // torrent's own row wears one level up (.run-toggle-icon): opening a
      // torrent and opening one of its files should not be two different
      // gestures.
      //
      // A <button> only where there can ever be something to open, and a
      // <span> in the same class otherwise, so the column is RESERVED on every
      // row and the names below still line up - the reasoning
      // .run-detail-cancel[data-idle] already follows one block up, and the
      // reason .picker-mark draws an em dash where a checkbox would be.
      // filelist.css keys the triangle's visibility off the item's
      // data-detail, which the file's own detail is the only writer of, so
      // there is one source of truth for "this file has a detail" rather than
      // a disabled flag to keep in step with it.
      //
      // WHY IT MAY SIT INSIDE THE <label>, and the trap that is: HTML's
      // activation behaviour for a label does nothing for events targeted at
      // interactive content descendants, so clicking the triangle cannot also
      // toggle the checkbox beside it. That half held. What did not is which
      // control the label belongs to.
      //
      // A <label> with no `for` labels its FIRST LABELABLE DESCENDANT, and
      // `button` is labelable. So this button, sitting first in the row,
      // silently became the label's control: clicking the file's NAME
      // activated the button, which dispatched a second click on this same
      // row, which toggled the detail straight back closed - one click, two
      // toggles, a row that looked dead. Worse and quieter, the checkbox
      // stopped being the label's control at all, so clicking the name of a
      // row that CAN be ticked would no longer tick it, which is the whole of
      // TOR-181 undone by DOM order.
      //
      // Found in a browser, not by the text checks, which is what the browser
      // check is for. The fix is `for` on the label (below): naming the
      // control explicitly is the only form of this that does not depend on
      // which element happens to come first in the row.
      const open = document.createElement(video ? "button" : "span");
      open.className = "picker-open";
      if (video) {
        open.type = "button";
        open.setAttribute("aria-expanded", "false");
        open.title = "this file's frames, metadata and buttons";
        // Its own name, because the label's text belongs to the checkbox: a
        // control announced as "button" with nothing after it is one nobody
        // reading by ear can act on.
        open.setAttribute("aria-label", "Frames and metadata for " + basename(file.path));
      } else {
        open.setAttribute("aria-hidden", "true");
      }

      let box = null;
      if (video) {
        box = document.createElement("input");
        box.type = "checkbox";
        box.value = String(file.index);
        // THE LABEL NAMES ITS CONTROL, rather than letting the DOM's order
        // decide (see the disclosure's own note just above for what that cost).
        // The id comes from the same counter the detail regions use, for the
        // same reason: it only has to be unique across the document.
        box.id = nextDetailId("file-tick-");
        row.htmlFor = box.id;
        box.addEventListener("change", () => this.tickFile(file.index, box));
      } else {
        // The em dash, in the column the checkbox would have occupied: the
        // non-colour half of "you cannot tick this", and aria-hidden because
        // a screen reader reading "em dash" learns nothing - the row's title
        // below is what carries the reason in words.
        box = document.createElement("span");
        box.className = "picker-mark";
        box.textContent = "—";
        box.setAttribute("aria-hidden", "true");
        row.title = WHY_NOT_VIDEO;
      }

      const name = document.createElement("span");
      name.className = "picker-name";
      name.textContent = basename(file.path);
      // The whole path, since the list shows base names and a season pack's
      // files can share one. Not overwritten for a non-video row: that row's
      // title is on .picker-file, one level up, so both answers survive.
      name.title = file.path;

      const size = document.createElement("span");
      size.className = "picker-size";
      size.textContent = bytesLabel(file.length);

      row.append(open, box, name, size);
      // The row first, so the detail below can be appended after it and read
      // as what it is: the thing this row opens onto.
      item.append(row);

      // TOR-181's two spans, and only on a row a tick can reach: what this
      // file would cost, and - once it has been asked for - which run it is
      // in. Never both at once (updateFileCosts), and an untickable row has
      // neither: there is no price for something that cannot be bought.
      if (video) {
        const cost = document.createElement("span");
        cost.className = "picker-cost";
        const asked = document.createElement("span");
        asked.className = "picker-state";
        row.append(cost, asked);
        // WHAT THE FILE ALREADY HAS, on the same row (TOR-182). This is the
        // .file-summary that used to sit on each file block's own title line -
        // resolution and frame count, kept current by updateFileSummary -
        // moved here with the rest of the block, because the title line it sat
        // on IS this row now. It is written by the file's own detail, which is
        // handed this span at mount: one of the four row elements that cross
        // the boundary (see this module's header).
        //
        // Last rather than beside the name, so it takes the place the price
        // vacates: the two are mutually exclusive by construction (a file with
        // frames has been asked for, and updateFileCosts clears the price of
        // an asked-for file), so the column reads as one column of "what this
        // row costs or what it got" rather than two half-empty ones.
        const summary = document.createElement("span");
        summary.className = "picker-summary";
        row.append(summary);

        // CLEAR FRAMES (TOR-183), last in the row and hidden until somebody
        // un-ticks a finished file - see updateFileCosts for the three things
        // that have to be true at once, and tickFile for why the un-tick is
        // the gate.
        //
        // A BUTTON INSIDE THE <label>, which the disclosure at the head of this
        // row has already paid for the right to be: HTML's activation behaviour
        // for a label does nothing for events targeted at interactive content
        // descendants, so pressing this cannot also toggle the checkbox. What
        // that note ALSO records is what made it safe - the label names its
        // control by id (row.htmlFor above), so a labelable element appearing
        // in the row can no longer steal it by being first, which is exactly
        // how TOR-182 lost the whole of TOR-181's tick.
        //
        // stopPropagation, unlike the disclosure: the row's own click listener
        // opens the file's detail, and the triangle wants that (it IS the
        // control for it) while a destructive button must never also be a
        // gesture that opens something. The same discipline .run-cancel and the
        // two priority buttons keep one level up.
        const clear = document.createElement("button");
        clear.type = "button";
        clear.className = "picker-clear";
        clear.hidden = true;
        clear.addEventListener("click", (event) => {
          event.stopPropagation();
          this.clearFrames(file.index);
        });
        row.append(clear);

        // WHAT A CLEAR THAT ONLY PARTLY HAPPENED LEFT BEHIND (TOR-78's shape at
        // a file's size): reported here, beside the file, rather than in the
        // page's own error line - which is where the per-frame delete puts its
        // warning, and can afford to, because a person clicking a thumbnail is
        // looking at the one grid it belongs to. A season pack has twenty-five
        // rows, and a sentence at the top of the page saying some frames could
        // not be removed would not say WHICH file's.
        //
        // A SIBLING OF THE ROW, not part of it, for the reason the detail
        // below is one: the row is a <label>, and a whole sentence inside it
        // would join the checkbox's accessible name.
        const note = document.createElement("p");
        note.className = "picker-note";
        note.hidden = true;
        item.append(note);

        // THE FILE'S OWN DETAIL, the third of this ticket's three elements and
        // the innermost. Built empty with the row and filled the first time
        // this file says anything (its mount), and a SIBLING of the row rather
        // than part of it (see this function's own heading, point 2).
        //
        // Created here rather than written into LIST above for the reason
        // run-detail.js's header gives: a custom element parsed out of an
        // innerHTML string is upgraded by a reaction rather than
        // synchronously, and the id the toggle names in aria-controls is read
        // in this same turn. createElement constructs it on the spot, and
        // build() gives it the slot whose id that is - a disclosure control
        // has to name the region it opens, and a region minted later is one
        // the control pointed at nothing for.
        const block = document.createElement("file-detail");
        block.build();
        open.setAttribute("aria-controls", block.regionId);
        item.append(block);

        this.rows.set(file.index,
          { item, row, box, name, cost, state: asked, summary, open, block, clear, note });

        // ONE LISTENER FOR THE WHOLE ROW, on the row and not on the triangle:
        // the ticket's own words are that clicking a video FILE opens its
        // detail, and a 0.7em triangle is not the file. The triangle is inside
        // this row, so its clicks arrive here too and there is nothing to
        // stop bubbling from.
        //
        // THE CHECKBOX IS EXCLUDED BY TARGET, and it has to be by target
        // rather than by state: activating a <label> dispatches a second,
        // synthetic click on the control it labels, which bubbles here as
        // well - so a click on the file's name would arrive twice and toggle
        // the detail back closed. Excluding events whose target IS the box
        // leaves exactly one toggle per click, and leaves a click on the box
        // itself meaning only what it has meant since TOR-181: start this
        // file.
        row.addEventListener("click", (event) => {
          if (event.target === box) return;
          this.toggleFileDetail(file.index);
        });
      }

      return item;
    }));

    this.syncFileList();
  }

  // syncFileList decides what this row's state earns: whether the list is on
  // screen at all, and which of its controls are.
  //
  // Called from <run-detail>'s syncDetail, so it re-runs on every event - the
  // list has to survive a row moving from parked to queued to running to done,
  // changing only what it offers, and the one thing that must NOT be
  // re-decided here is whether the row is expanded (run-table.js's
  // setRunExpanded owns that; see its own doc).
  syncFileList() {
    const entry = this.entry;
    // Hidden only when there is nothing to list, never because of the state.
    // Two rows legitimately have nothing: a queued one, whose metadata has not
    // been fetched (choosing what to fetch cannot be offered before the
    // torrent has said what it holds), and a disk row whose replay has not
    // arrived or failed. Both are "not known yet", which an empty framed box
    // would state as "holds nothing".
    this.pickerEl.hidden = entry.fileList.length === 0;

    // PARKED still earns the frame, and only the frame: .picker's --warn
    // border says "this is blocking, nothing happens until you act", which is
    // true of a torrent waiting to be picked from and a false alarm on a run
    // already going. What it no longer decides is whether a tick works -
    // TOR-181 made that the server's own answer, carried on every run_state
    // (entry.tickable), because the rule has a case this page cannot see.
    const parked = entry.state === "needs-action";
    this.pickerEl.dataset.parked = String(parked);
    this.pickerTitle.textContent = this.fileListTitle(parked);

    // Every box's state, every price, and Select all's own label and total.
    // One function, and it has to run here as well as on each tick: a row that
    // has just reached the parked state has boxes that have never been priced.
    this.updateFileCosts();
  }

  // fileListTitle says what the list is, and it must not report the video
  // count as the torrent's own: a torrent of one film, a sample, a .nfo and
  // two images holds five files and offers one, and "1 file(s)" was the shape
  // of that misreport before TOR-180.
  //
  // The fallback wording is deliberately different rather than a count of a
  // list that stood in: a message with no "files" key cannot say how many
  // files the torrent holds, and claiming the video count as the total would
  // be the same misreport in a place a reader could not check.
  fileListTitle(parked) {
    const entry = this.entry;
    // "tick a file to start it", not "pick what to capture": since TOR-181
    // there is nothing to pick and then confirm, and a sentence that says
    // otherwise would have people ticking a box and hunting for the button
    // that used to follow it.
    //
    // AND WHAT AN UN-TICK MEANS WHILE THE ROW IS FETCHING (TOR-184), which is
    // said HERE rather than on each file for two reasons. It is a fact about
    // the row's state, not about any one file - the scope of the stop is the
    // run - and a season pack fetching twenty files would otherwise carry
    // twenty copies of one sentence down the list, which is the same column of
    // repeated words updateFileCosts refuses to draw beside every ticked box.
    //
    // ON SCREEN, not in a title, and that is the requirement rather than a
    // preference: TOR-184's acceptance is that the two gestures are
    // distinguishable BEFORE they are made, and a hover title is not readable
    // on a touch screen or by somebody who never hovers. The per-file title
    // still says it precisely for the box under the pointer; this is what a
    // person reads without doing anything at all.
    //
    // Mutually exclusive with the parked sentence by construction: a parked row
    // has fetched nothing, and a fetching one is long past being parked.
    const suffix = parked
      ? " — tick a file to start it"
      : entry.fetching.size > 0
        ? " — un-ticking a file it is fetching stops this torrent's fetch; frames already taken stay"
        : "";
    if (!entry.fileListKnown) {
      return entry.videos.length + " video file(s)" + suffix;
    }
    return entry.fileList.length + " file(s), " + entry.videos.length + " video" + suffix;
  }

  // updateFileCosts re-states every row: whether its box may be clicked, what
  // clicking it would spend, and - for a file already asked for - which run it
  // is in.
  //
  // This is the one place in the whole program where a cost can be shown IN
  // ADVANCE. Every other screen only ever reports traffic once it is gone. It
  // follows both inputs live: the ticks (through run_state), and the count on
  // the intake line, which a person may well adjust while reading this list.
  updateFileCosts() {
    const entry = this.entry;
    const n = passCount(entry, count());

    for (const [index, row] of this.rows) {
      const asked = entry.picked.has(index);
      const deferred = entry.deferred.has(index);
      // WHETHER THIS FILE CAN BE CLEARED (TOR-183), which is three things at
      // once and none of them is "was it selected":
      //
      //   - it was asked for, so it is one of this row's files at all;
      //   - the row has SETTLED, so nothing is fetching this file - un-ticking
      //     one that is still going is a cancel and a different act entirely,
      //     which is TOR-184's, and offering a clear there would put a
      //     destructive button on a file mid-capture. The row's own state is
      //     the coarsest honest answer available: file_done says a file
      //     finished, but a row can start a SECOND pass afterwards (TOR-181),
      //     so a per-file flag would go stale where this cannot;
      //   - and it HAS FRAMES ON DISK. This is the thing the ticket insists on
      //     - the page must know a file has frames, not merely that it was
      //     selected - and the page already does: framesOnDisk counts exactly
      //     the cells updateFileSummary states beside them, whether they
      //     arrived live or off disk on a replay.
      const clearable = asked && FINAL.has(entry.state) && framesOnDisk(entry, index) > 0;
      // And whether somebody has actually asked for it. Read together with
      // clearable and never on its own, which is what makes a stale entry in
      // the set harmless: a file whose frames have gone stops being offered
      // and its box goes straight back to checked.
      const offering = clearable && entry.unticked.has(index);

      // WHAT UN-TICKING THIS BOX WOULD DO, AS ONE OF FOUR ANSWERS (TOR-184),
      // decided here once and read by everything below - the box's own live or
      // dead state, the sentence on the row, and tickFile's routing, which
      // re-asks these same three questions rather than trusting the DOM.
      //
      // The order is the priority and the three cases are mutually exclusive by
      // construction, so it states the reading rather than resolving a clash: a
      // file waiting for the next pass is never in the pass in flight
      // (Server.UntickFile's own disjointness note), and neither can be true on
      // a row that has settled, which is all "clear" is ever offered on.
      //
      //   "drop"  - ticked while this row was already fetching, so nothing has
      //             been asked of the swarm for it yet. Taking it out stops
      //             that one file and nothing else, spends nothing, deletes
      //             nothing. The one honestly per-file stop there is.
      //   "stop"  - the engine is fetching it now, and core.Engine has no
      //             per-file stop to offer: one context for the whole run, one
      //             budget sized before it started. So this stops THE RUN, and
      //             the row says so before it is pressed (below, and once for
      //             the row in fileListTitle).
      //   "clear" - TOR-183's: a finished file with frames on disk, where the
      //             un-tick only puts the Clear frames button on the row and
      //             changes nothing by itself.
      //   ""      - nothing this can reach. A file an earlier pass captured
      //             while this row runs on is the case that matters: nothing is
      //             fetching it, so stopping the run would stop the wrong
      //             thing, and it is not settled, so there is nothing to clear
      //             yet either.
      //   "narrow" - TOR-197's: the pass has been chosen and the engine has
      //             not been handed it, so the file simply comes out. If it
      //             is the LAST one the row goes back to needs-action, which
      //             is what an empty selection already means there - so the
      //             gesture never depends on how many boxes are left, and
      //             there is no last-box case a person cannot predict.
      //             Placed after "drop" and before "stop" because it is the
      //             other case where nothing has been asked of the swarm
      //             yet; the two are mutually exclusive anyway, since
      //             "deferred" only ever holds files on a RUNNING row.
      const untick = !asked ? ""
        : entry.deferred.has(index) ? "drop"
        : entry.narrowable.has(index) ? "narrow"
        : entry.fetching.has(index) ? "stop"
        : clearable ? "clear"
        : "";

      // ASKED FOR IS DISABLED UNLESS AN UN-TICK MEANS SOMETHING, which since
      // TOR-184 is three cases rather than TOR-183's one. What has not changed
      // is the rule underneath: a box is live only where the gesture can
      // actually be carried out, because a box that moves and then does nothing
      // is the control that lied - and that is still the whole reason Select
      // none is gone.
      row.box.disabled = asked ? untick === "" : !entry.tickable;
      row.box.checked = asked && !offering;

      // The button goes when there is nothing to clear, which is the other
      // half of its stated lifecycle - and it goes by the `hidden` attribute
      // alone, never a class (.picker-clear declares no display of its own,
      // and carries a [hidden] companion anyway; see filelist.css).
      row.clear.hidden = !offering;
      if (offering) {
        // THE FIGURE IS IN THE BUTTON, the way Select all's total is: this is
        // the most destructive press on the row, and a control labelled
        // "Clear" alone would not say how much. Read from the same count the
        // summary beside it shows, so the two cannot disagree.
        const frames = framesOnDisk(entry, index);
        row.clear.textContent = "Clear " + (frames === 1 ? "1 frame" : frames + " frames");
        row.clear.title = "delete this file's frames, its contact sheet and its manifest from " +
          "disk, and drop it from what this result counts as complete — the file stays " +
          "listed as asked for, so a top-up can take it again";
      }

      if (asked) {
        row.item.dataset.tick = "asked";
        row.cost.textContent = "";
        // ONLY THE EXCEPTION IS SPOKEN. The ticked box already says this file
        // has been asked for; a span repeating "in this run" beside every one
        // of twenty rows would be a column of the same three words. What a
        // reader cannot see is the one case where a tick did NOT join the run
        // in flight, so that is the only case with words on it.
        row.state.textContent = deferred ? "in the next pass" : "";
        // WHICH ACT THE BOX IS ABOUT TO DO, BEFORE IT IS DONE, and since
        // TOR-184 this is an acceptance criterion rather than a courtesy: an
        // asked-for box now performs one of THREE acts, or none, and it is the
        // same box in all four states - so nothing but the row can say which.
        //
        // One sentence per verdict, in the verdict's own order. The clear's
        // reads "changes nothing by itself", because that un-tick only reveals
        // a button; the stop's has to read the opposite, because that one acts
        // on the press - and the difference between those two sentences is the
        // whole of this ticket's legibility requirement. The heading above the
        // list says the stop's scope once more, on screen, for a reader who
        // never hovers anything (fileListTitle).
        if (untick === "drop") {
          row.row.title = "ticked while this torrent was already fetching - the engine works " +
            "from a plan it was handed, so this file starts the moment that pass ends, on " +
            "this same row. Un-tick to drop it before it starts: nothing has been asked of " +
            "the swarm for it, so that stops this file alone and costs nothing";
        } else if (untick === "stop") {
          // THE ONE DESTRUCTIVE-BY-SCOPE PRESS ON THIS ROW, and the whole of
          // TOR-184's legibility requirement is that this sentence exists
          // before it rather than after. It says three things a person cannot
          // see: that the box acts at once (unlike the clear's, which only
          // reveals a button), that it reaches the WHOLE run and not just this
          // file, and that nothing on disk is lost - which is what makes the
          // scope survivable and is exactly what a reader will fear.
          //
          // The count is read from the same set the verdict came from, so it
          // cannot claim a scope the gesture does not have, and a one-file pass
          // says nothing about siblings rather than "and 0 others".
          row.row.title = "this file is being fetched now — un-ticking it STOPS THIS TORRENT'S FETCH" +
            (entry.fetching.size > 1
              ? ", all " + entry.fetching.size + " files of this pass with it"
              : "") +
            ". The engine works from the plan it was handed," +
            " so one file of it cannot be stopped on its own." +
            " Nothing is deleted: the frames already written stay on disk," +
            " and a later run reuses them";
        } else if (untick === "narrow") {
          // TOR-197's, and the sentence has to say which of two things the
          // press will be, because the person cannot see the difference: with
          // another box still ticked it takes this file out and the torrent
          // goes on waiting; with this one the last, the row goes back to
          // asking which files to take. Both are said, in that order, so the
          // one that happens is the one that was read.
          //
          // The count comes from the same set the verdict did, so it cannot
          // claim a scope the gesture does not have.
          row.row.title = entry.narrowable.size > 1
            ? "this torrent is waiting to start and has not been handed to the engine yet - " +
              "un-tick to take this file out of the pass it will start with. The other " +
              (entry.narrowable.size - 1) + " stay, and nothing has been fetched or deleted"
            : "this is the only file this torrent is waiting to take - un-ticking it puts " +
              "the row back to asking which files you want, which is where it was before " +
              "any box was pressed. Nothing has been fetched, so nothing is lost";
        } else if (untick === "clear") {
          row.row.title = "this row captured this file - un-tick it to be offered a clear, " +
            "which deletes its frames from disk; un-ticking by itself changes nothing";
        } else if (cancellable(entry.state)) {
          // The row is still live and this file is in none of the three: an
          // earlier pass on this row captured it and the pass in flight is
          // fetching something else. Said in full because it is the case whose
          // dead box looks arbitrary beside the live ones above it.
          row.row.title = "this row has asked for this file and nothing is fetching it now - " +
            "an earlier pass took it, so stopping this run would stop the wrong file, and " +
            "its frames can be cleared once the row has finished";
        } else {
          row.row.title = "this row has asked for this file - whatever it has taken is in " +
            "the file's own block below";
        }
        continue;
      }

      row.state.textContent = "";

      if (!entry.tickable) {
        // The server's own sentence, not this page's guess at it: refuseTick
        // is the single rule, and run_state carries its words so a disabled
        // box says exactly what the POST would have answered.
        row.item.dataset.tick = "closed";
        row.cost.textContent = "";
        row.row.title = entry.tickRefusal ||
          "this row cannot be asked for another file right now";
        continue;
      }

      row.item.dataset.tick = "open";
      row.cost.textContent = framesLabel(n);
      row.row.title = "tick to start this file's frames now — " + framesLabel(n) +
        " for this one file, and the count is per file";
    }

    this.syncSelectAll();
  }

  // ---------------------------------------------------------------------------
  // SELECT ALL IS THE MOST EXPENSIVE CLICK ON THE PAGE (TOR-181).
  //
  // It used to stage a selection somebody then confirmed with a button under
  // the list; the button is gone, so pressing it now spends traffic on every
  // video file the torrent holds at once. A one-click bill is exactly what
  // that ticket refuses to leave it as, and dropping it is not the answer
  // either - a person who does mean all twenty episodes should not have to
  // click twenty times.
  //
  // So it keeps its job and grows a confirmation, in the button itself rather
  // than in a dialog: the first press ARMS it and it says what it is about to
  // cost ("Start all 6 — 120 frames"), the second press starts them. The
  // wording is not decoration - it is the multiplication TOR-50 exists to warn
  // about, stated before the money is spent rather than after.
  //
  // A native confirm() was the other candidate and was rejected: this page
  // deliberately has no confirmation dialogs (see deleteFrame's own note on
  // why a dialog per thumbnail would be worse than the frames), an unstyleable
  // modal cannot state a figure in the page's own type, and a blocking dialog
  // cannot be seen in a screenshot of what the page said. Three carriers say
  // the armed state and only one of them is colour: the label changes, a
  // sentence appears beside it, and the button wears --warn.

  armSelectAll() {
    const entry = this.entry;
    // The second press. Everything it will start is read again HERE rather
    // than remembered from the first press: a tick or a run_state may have
    // landed in between, and starting a list that is no longer true would
    // spend on a file somebody else's tick already started.
    if (entry.armed) {
      const files = untickedVideos(entry);
      this.disarmSelectAll();
      this.startFiles(files);
      return;
    }
    entry.armed = true;
    this.syncSelectAll();
  }

  disarmSelectAll() {
    this.entry.armed = false;
    this.syncSelectAll();
  }

  // syncSelectAll draws the button in whichever of its two states it is in,
  // and takes it off screen when it has nothing left to offer.
  //
  // PARKED ONLY, and that is a scope decision rather than an oversight: "all
  // of it" is the answer to a torrent whose whole list is undecided, which is
  // what the parked state is. On a row already fetching, the files left are
  // the ones somebody chose not to take, and a button that starts them all
  // would undo that choice in one press. Ticking them individually still
  // works there.
  syncSelectAll() {
    const entry = this.entry;
    const parked = entry.state === "needs-action";
    const remaining = untickedVideos(entry);
    const offer = parked && entry.tickable && remaining.length > 0;

    this.pickerAll.hidden = !offer;
    if (!offer) {
      entry.armed = false;
      this.pickerAll.removeAttribute("data-armed");
      this.pickerAll.textContent = "Select all";
      this.pickerAll.title = "";
      this.pickerArmed.textContent = "";
      return;
    }

    const n = passCount(entry, count());
    const total = n ? remaining.length * n : 0;
    const sum = total
      ? remaining.length + " file(s) × " + n + " = " + total + " frames"
      : remaining.length + " file(s) at the server's own frame count";

    if (!entry.armed) {
      this.pickerAll.removeAttribute("data-armed");
      this.pickerAll.textContent = "Select all";
      this.pickerAll.title = "start every video file in this torrent at once — " + sum;
      this.pickerArmed.textContent = "";
      return;
    }

    this.pickerAll.dataset.armed = "true";
    this.pickerAll.textContent = total
      ? "Start all " + remaining.length + " — " + total + " frames"
      : "Start all " + remaining.length;
    // The sentence, beside the button, because a label that changed is a hint
    // and this needs an instruction: nothing has been spent yet, and a person
    // has to be told that the next press is the one that spends it.
    this.pickerArmed.textContent = "this starts " + sum +
      " — press again to confirm, Esc to cancel";
    this.pickerAll.title = this.pickerArmed.textContent;
  }

  // tickFile is what a checkbox means, and since TOR-184 that is FOUR different
  // acts depending on what the file is doing - which is the whole difficulty of
  // this control and the reason each of them is named in one place.
  //
  // The box's own state is already changed by the time this runs (it is a
  // change listener). A TICK captures the file, optimistically - startFiles
  // rolls the box back if the server refuses. An UN-TICK is one of these, in
  // this order, and it re-asks updateFileCosts' own three questions rather than
  // trusting the box being clickable: a run_state, a pass ending or a delete
  // may have landed between the render and the click.
  //
  //   - WAITING FOR THE NEXT PASS: dropped, at the server (dropFile). Nothing
  //     has been asked of the swarm for it, so this stops that one file and
  //     nothing else. The only per-file stop that honestly exists.
  //   - BEING FETCHED NOW: stops the run (stopFetch). core.Engine has no
  //     per-file stop - one context for the whole run, one budget sized from
  //     the file list before the clock started - so this is the only stop
  //     there is, and the row has said so before the press.
  //   - A FINISHED file with frames on disk: it offers to clear them (TOR-183).
  //     It asks nothing of the server, spends nothing and deletes nothing: the
  //     run still asked for this file and cache.Run.Selected still says so. It
  //     puts the button on the row, and ticking the box again takes it away.
  //     Nothing about this is irreversible until that button is pressed.
  //   - ANYTHING ELSE: refused, with the box put back. Since TOR-184 that is
  //     one case rather than the old three - a file an earlier pass captured
  //     while this row runs on - and updateFileCosts leaves its box an honest
  //     `disabled` control, so this is the guard for a stale DOM rather than a
  //     path a person can normally take.
  //
  // THE FIRST TWO ARE ONE GESTURE WITH TWO SCOPES, which is what makes this
  // control dangerous and what the row's own sentences exist to answer: one
  // stops a file, the other stops a torrent, and only the row can say which
  // before it happens (updateFileCosts' titles, fileListTitle's suffix).
  tickFile(index, box) {
    const entry = this.entry;
    // Somebody who ticks a single file has answered Select all's question by
    // doing something else; leaving it armed behind them would put a
    // twenty-file bill under a second stray press.
    this.disarmSelectAll();

    if (!box.checked) {
      // TOR-184's two stops, in updateFileCosts' own order and read from the
      // same sets, so the box that was drawn live is the box that acts. Both
      // are guarded on the file being one this row asked for at all: a box
      // moved on a row whose run_state has since re-armed it must not send a
      // stop for a file the server no longer holds.
      if (entry.picked.has(index) && entry.deferred.has(index)) {
        this.dropFile(index);
        return;
      }
      if (entry.picked.has(index) && entry.narrowable.has(index)) {
        this.narrowPass(index);
        return;
      }
      if (entry.picked.has(index) && entry.fetching.has(index)) {
        this.stopFetch(index, box);
        return;
      }
      // The same three conditions updateFileCosts drew the live box from, read
      // again here rather than trusted: a run_state or a delete may have landed
      // between the render and the click, and the box being clickable is not by
      // itself evidence that there is anything left to clear.
      if (entry.picked.has(index) && FINAL.has(entry.state) && framesOnDisk(entry, index) > 0) {
        entry.unticked.add(index);
        // Any warning a previous clear on this file left goes with the new
        // offer: it described a state the button is about to be pressed on
        // again, and keeping it would read as a report on this press.
        this.setFileNote(index, "");
        this.updateFileCosts();
        log(entry, "file " + index + " un-ticked - its frames can be cleared from disk, " +
          "and nothing has been deleted");
        return;
      }
      // THE ONE CASE LEFT, and it is narrow now: a file an earlier pass on this
      // row already captured, while the pass in flight is fetching something
      // else. There is nothing to stop - nothing is fetching this file - and
      // nothing to clear yet, because the row has not settled and TOR-183's
      // offer is deliberately gated on that. The sentence says both, because
      // "cannot" with no reason is a refusal a person cannot act on.
      box.checked = true;
      showError("nothing is fetching this file — an earlier pass on this row took it, so " +
        "there is nothing here to stop. Its frames can be cleared once the row has " +
        "finished, or use Cancel on the row to stop the pass that is going now");
      return;
    }

    // TICKING AN OFFER BACK CLOSED, and it must be handled before startFiles:
    // the file is already in entry.picked, so startFiles would filter it out
    // and return without redrawing, leaving the button standing beside a box
    // that is ticked again.
    if (entry.unticked.delete(index)) {
      this.setFileNote(index, "");
      this.updateFileCosts();
      return;
    }

    this.startFiles([index]);
  }

  // setFileNote writes - or takes away - the sentence beside one file's row.
  //
  // One function so the note can never be left visible and empty, or filled and
  // hidden: it is taken off screen by the `hidden` attribute AND emptied, since
  // a stale sentence sitting in a hidden element is one an unrelated later
  // change would put back on screen.
  setFileNote(index, text) {
    const row = this.rows.get(index);
    if (!row) return;
    row.note.textContent = text;
    row.note.hidden = !text;
  }

  // dropFile takes one file back out of what this row will fetch, before
  // anything has been asked of the swarm for it (TOR-184).
  //
  // THE PER-FILE STOP, and the only one that exists. It reaches exactly the
  // file a tick could not put in the plan the engine was already working from,
  // which the server holds on the entry for the next pass (runEntry.pending):
  // no traffic has been spent on it, no budget was sized for it, and dropping
  // it stops that file and touches nothing else on the row.
  //
  // NO CONFIRMATION, and this is the easy half of that argument: the act spends
  // nothing, deletes nothing and can be undone by ticking the box again at no
  // cost. See stopFetch for the harder half, where the scope is the whole run
  // and the answer is still no.
  async dropFile(index) {
    const entry = this.entry;
    showError("");

    // Optimistic, for the same frame startFiles guesses through and rolled back
    // the same way: the boxes are the only feedback there is between the click
    // and the socket. run_state overwrites both sets moments later.
    entry.picked.delete(index);
    entry.deferred.delete(index);
    this.updateFileCosts();

    try {
      await post("runs/untick", { id: entry.id, file: String(index) });
      log(entry, "file " + index + " dropped before it started - it was waiting for the " +
        "next pass, so nothing was fetched for it and nothing was deleted");
    } catch (err) {
      // A refusal is a real answer: the pass in flight may have ended in the
      // meantime and taken this file into itself (Server.pendingPass), and it
      // is being fetched by the time the POST lands. Put back, so the box does
      // not say a file is not wanted while the engine is holding it.
      entry.picked.add(index);
      entry.deferred.add(index);
      this.updateFileCosts();
      showError(String(err.message || err));
      log(entry, "could not drop file " + index + ": " + (err.message || err));
    }
  }

  // narrowPass takes one file out of a pass this torrent is WAITING to start
  // (TOR-197), which TOR-184 had to refuse over a trap rather than an
  // objection.
  //
  // THE TRAP, AND WHY THIS IS SAFE NOW: an empty RunRequest.Files means every
  // video file the torrent holds, so taking the LAST file out of a queued
  // pass would have widened the run from one file to twenty rather than
  // emptying it. The server answers that by putting the row back in
  // needs-action, where an empty selection already means the opposite -
  // nothing decided yet - so there is no last-box case, and nothing here has
  // to count boxes to know which of the two happened.
  //
  // Same route as dropFile, and deliberately not the same method: the act is
  // different enough to need its own sentence in the log. dropFile takes a
  // file out from BEHIND a pass that is running; this takes one out of a pass
  // that has not started, and on the last file the row changes state.
  //
  // NO CONFIRMATION, on the same argument dropFile makes and more strongly:
  // this row has been handed to nothing, so the act spends nothing, deletes
  // nothing, and ticking the box again puts it straight back.
  async narrowPass(index) {
    const entry = this.entry;
    showError("");

    const last = entry.narrowable.size <= 1;
    // Optimistic, the same frame dropFile guesses through and rolled back the
    // same way - run_state overwrites all three sets moments later.
    entry.picked.delete(index);
    entry.narrowable.delete(index);
    this.updateFileCosts();

    try {
      await post("runs/untick", { id: entry.id, file: String(index) });
      log(entry, last
        ? "file " + index + " was the last this torrent was waiting to take, so the row is " +
          "back to asking which files you want - nothing was fetched and nothing deleted"
        : "file " + index + " taken out of the pass this torrent will start with - it had " +
          "not been handed to the engine, so nothing was fetched for it");
    } catch (err) {
      // A refusal is a real answer here too: the slot may have freed and the
      // pass started between the click and the POST, at which point the file
      // is being fetched and the server says so.
      entry.picked.add(index);
      entry.narrowable.add(index);
      this.updateFileCosts();
      showError(String(err.message || err));
      log(entry, "could not take file " + index + " out of the waiting pass: " +
        (err.message || err));
    }
  }

  // stopFetch is what un-ticking a file the engine is already fetching means:
  // STOP THE RUN THAT FILE BELONGS TO (TOR-184).
  //
  // It is the whole of that ticket's decision, and it is a decision about the
  // ENGINE rather than about the page. core.Engine.Run hands back an event
  // channel and nothing else; its only handle is the context it was started
  // with, every file's goroutine is given that same context, and the run's
  // traffic budget is sized once from the file list before the clock starts. So
  // there is no per-file stop to call here - the choice was between saying the
  // gesture stops the run and building per-file cancellation into the engine,
  // and that release says the first and says it out loud.
  //
  // NO CONFIRMATION, and it is the deliberate opposite of Select all one
  // section up. That button ARMS on its first press and states what it is about
  // to spend, because it SPENDS - and this page keeps its one confirmation
  // gesture for spending. A stop spends nothing and destroys nothing: the
  // traffic already sent is not recoverable whatever anybody clicks next, and
  // the frames already written stay on disk (section 2.10, and the Clear button
  // that appears on this very row afterwards is the proof of it). A speed bump
  // in front of a harmless act only trains people to click through the one in
  // front of the harmful one.
  //
  // WHAT CARRIES THE WEIGHT INSTEAD is that the row said so first: the box's
  // own title states the scope and that nothing is deleted, and the list's
  // heading says it once for the row without needing to be hovered
  // (updateFileCosts, fileListTitle).
  stopFetch(index, box) {
    const entry = this.entry;
    // The box goes straight back, and that is the truth rather than a
    // rollback: a cancel does not un-ask for the file. The run asked for it,
    // whatever frames it took stay on disk, and run_state redraws this row
    // moments later with the box ticked and a Clear frames beside it - so a box
    // left cleared would be the one thing on screen claiming otherwise.
    box.checked = true;
    log(entry, "un-ticked file " + index + " while it was being fetched - stopping this " +
      "run; the frames already written stay on disk and a later run reuses them");
    cancelRun(entry.id);
  }

  // clearFrames deletes everything one file has on disk - its frames, its
  // contact sheet, its manifest - and drops it from what each result set counts
  // as complete (core.ClearFile, reached through DELETE on the file's frames).
  //
  // NO CONFIRMATION STEP, and unlike the per-frame cross this one has two
  // gestures rather than none: the box has to be un-ticked before the button
  // exists at all, and the button says how many frames it will delete. So the
  // figure is read before the press, which is what Select all's arming exists
  // to achieve for the most expensive click on the page - and there is no
  // dialog here for the same three reasons stated there (this page has none
  // anywhere, an unstyleable modal cannot state a figure in the page's own
  // type, and a blocking dialog cannot be seen in a screenshot of what the page
  // said).
  //
  // THE WAY BACK IS REAL, which is what makes one press acceptable: the file
  // stays in cache.Run.Selected, so a top-up (TOR-152) sees a file that was
  // asked for with nothing captured and offers to take it again, at the same
  // plan, into the same result set. That is stated on the button's own title
  // rather than left for somebody to discover.
  //
  // WHAT IT DOES NOT DO IS REDRAW THE GRID. The frames, the reach strip and the
  // contact-sheet link belong to that file's own detail, which is handed the
  // server's answer and replaces them from it (file-detail.js's afterClear) -
  // the one place a page that removed its own tiles and hoped the two agreed
  // would have been. This function owns the request, the button, the note
  // beside the row and the offer's own lifecycle.
  async clearFrames(index) {
    const entry = this.entry;
    showError("");
    if (!entry.infohash) return;
    const row = this.rows.get(index);
    if (!row) return;

    this.setFileNote(index, "");
    // The button is the only feedback there is between the press and the
    // answer, and this is a request that can take a moment (it walks every
    // result set on disk). A second press would send a second DELETE for a file
    // the first one is already clearing.
    row.clear.disabled = true;
    try {
      // No params: every result set this torrent holds frames of this file in,
      // which is what the grid under this row is showing (Server.ClearFile
      // argues why the button cannot honestly mean one of them). Assembled from
      // segments for the reason loadFileDetail gives - an inline path would
      // read as a site-root path and break under a base path.
      const path = ["runs", entry.infohash, "files", index, "frames"].join("/");
      const answer = await del(url(path));

      const fentry = entry.fileEntries.get(index);
      if (fentry) fentry.block.afterClear(answer.file);

      // THE WARNING GOES BESIDE THE FILE (TOR-78's shape): the clear happened -
      // the record and the manifest say the file is not part of this result any
      // more - and something of it is still on disk. Refusing to re-render
      // would leave frames on screen that nothing accounts for, and reporting
      // it as a failure would tell a person nothing was deleted.
      if (answer.warning) this.setFileNote(index, answer.warning);

      // The offer is over only if the file is actually clean. A clear that
      // partly failed leaves frames, so it leaves the button too - with the
      // warning beside it saying what is in the way, and a second press worth
      // making.
      if (framesOnDisk(entry, index) === 0) entry.unticked.delete(index);
      this.updateFileCosts();
      log(entry, "cleared file " + index + "'s frames from disk" +
        (answer.warning ? " - " + answer.warning : ""));
    } catch (err) {
      // Beside the file, like the warning, and for the same reason: this is a
      // report on one row's button. Nothing on screen changed, because nothing
      // on disk did.
      this.setFileNote(index, String(err.message || err));
      log(entry, "could not clear file " + index + ": " + (err.message || err));
    } finally {
      row.clear.disabled = false;
    }
  }

  // startFiles asks the server to add these files to this row's run, and is
  // the one path both a tick and Select all go through.
  //
  // It sends a SET rather than one index, which is why Select all is a single
  // call and a single confirmation: the route has always taken a list
  // (decideRequest), and the server adds it to whatever the row already has
  // rather than replacing it, so two calls racing cannot lose a file.
  //
  // The count it sends is the number this row DISPLAYED (passCount), never the
  // intake box read afresh - the promise a price makes is that pressing the
  // thing beside it costs that. The server ignores it for a pass already
  // forming, having locked the figure to the tick that opened it, and answers
  // with that figure on run_state so the row keeps quoting the truth.
  async startFiles(indices) {
    const entry = this.entry;
    showError("");

    const files = indices.filter((index) => !entry.picked.has(index));
    if (files.length === 0) return;

    // Optimistic, for the frame between the click and the socket: the boxes
    // are the only feedback there is, and leaving them empty until a websocket
    // message lands reads as a click that did nothing. run_state overwrites
    // all of it moments later (the "ticked" and "deferred" keys), which is
    // what makes guessing safe here - including the guess below about which
    // pass these land in, since a row already fetching cannot take them into
    // the plan the engine is working from.
    const later = entry.state === "running";
    for (const index of files) {
      entry.picked.add(index);
      if (later) entry.deferred.add(index);
    }
    this.updateFileCosts();

    try {
      await post("runs/decide", {
        id: entry.id,
        files: files.map(String),
        count: passCount(entry, count()),
      });
      log(entry, (later ? "queued for the next pass: " : "capturing: ") +
        files.length + " file(s) of " + entry.videos.length + ", " +
        framesLabel(passCount(entry, count())) + " each");
    } catch (err) {
      // Rolled all the way back. A refusal is a real answer here - a run that
      // has settled, or a torrent dropped as a file whose staged copy is gone
      // (refuseTick) - and a box left ticked after one would say a file is
      // being captured when nothing is.
      for (const index of files) {
        entry.picked.delete(index);
        entry.deferred.delete(index);
      }
      this.updateFileCosts();
      showError(String(err.message || err));
      log(entry, "could not start " + files.length + " file(s): " + (err.message || err));
    }
  }
}

// Native, no bundler: this is the whole registration mechanism, and importing
// this module for its side effect is how run-detail.js gets the element
// defined before it creates the first one.
customElements.define("file-list", FileList);

export { FileList, LIST, WHY_NOT_VIDEO, framesLabel, setServices };
