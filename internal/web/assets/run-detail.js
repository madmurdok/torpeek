// THE RUN'S DETAIL, as a custom element (TOR-195), and the outermost of the
// three this ticket nests.
//
// It follows the three elements before it rather than inventing anything:
// light DOM, parts found with this.querySelector after the markup exists with
// a loud throw naming any that is missing, and one method per thing the
// outside is allowed to ask for. frame-panel.js's header carries the full
// reasoning for no shadow DOM; compare-dialog.js's carries the setServices
// shape this file also needs; run-table.js's says where the table/detail line
// falls, which is the line this element sits on the far side of.
//
// WHAT IT IS: everything a torrent's expanded row says about THE RUN. The
// header (badge, name, Save .torrent, Cancel), the error line, the confirmed
// name under it, sending the .torrent to a watch directory (TOR-73, TOR-169),
// and the top-up/retry offer with the price the server computed (TOR-152).
// One instance per torrent, mounted in the cell the table hands out.
//
// ---------------------------------------------------------------------------
// THE MARKUP IS A TEMPLATE HERE, NOT DECLARATIVE IN index.html, and that is
// the one place these three elements depart from the three before them - so
// it is stated rather than left to be noticed.
//
// The panel, the compare dialog and the table are each ONE thing on the page,
// so their structure could stay in index.html and be WRAPPED, which is what
// keeps each comment next to the markup it explains. A run's detail is not
// one thing: there is one per torrent, built when the torrent appears and
// destroyed with it, and index.html has never held it - app.js built it from
// exactly this string, with exactly these comments, before this ticket. So
// the choice here was not "declarative markup or a template literal"; it was
// "the template literal it already is, or a <template> in index.html cloned
// per row". The literal wins on the same ground the wrapping won on: the
// comments explaining the header's fixed order, why .torrent-actions did not
// follow Save up into the header, and why the top-up offer sits above the
// list all stay exactly where they are, next to the markup they are about.
// Moving them into index.html would move them AWAY from the module that owns
// the behaviour they constrain.
//
// NO CUSTOM ELEMENT INSIDE THE TEMPLATE. The file list is created with
// document.createElement and appended below, not written into the string. Two
// reasons: a custom element written inside a template string is a boundary a
// reader cannot see, and an element parsed out of innerHTML is upgraded by a
// REACTION rather than synchronously, so its methods are not there to be
// called in the same breath. createElement constructs it on the spot.
// ---------------------------------------------------------------------------
//
// WHY THERE IS NO disconnectedCallback, and this is the trap this ticket paid
// for. run-table.js's syncRow ENDS WITH A RE-SORT, and reorderRuns moves both
// of a run's <tr>s with `this.list.append(...)` - which for a node already in
// the table is a REMOVE followed by an INSERT. So every element inside the
// detail row, this one included, is disconnected and reconnected on every
// redraw of every row: dozens of times a second on a live run.
//
// A disconnectedCallback that took this element's listeners off would
// therefore take them off on the first event after the page loads, and
// connectedCallback would have to put every one of them back on every event
// after that. Neither is needed, and the first is a page whose Cancel, Top up
// and Retry stop working silently. run-table.js's own rule is what settles
// it: a listener on a node THIS ELEMENT CREATED INSIDE ITSELF goes when the
// DOM goes, and only a listener on something that outlives the element -
// `window`, `document`, a timer - has to be a field and be removed. This
// element has none of those. So the listeners are added once, at build time,
// and nothing is torn down.
//
// That is also why build() is idempotent rather than trusting
// connectedCallback to run once. It runs every time the table re-sorts, and a
// second `this.innerHTML = ...` would throw away the file list, every frame
// grid and every open disclosure inside it.

import {
  FINAL,
  badgeLabel,
  badgeState,
  bytesLabel,
  cancellable,
  claimReopenedRun,
  displayName,
  nextDetailId,
  state,
} from "./state.js";
// Imported for its side effect - the module's last statement defines
// <file-list> - and for nothing else: the list's whole API is the methods on
// the instance this element creates. Importing it here rather than from
// app.js is what guarantees the definition exists before the first
// createElement("file-list") below, since a module's imports evaluate before
// its own body does.
import "./file-list.js";

// THE SIX SERVICES THE PAGE INJECTS, in the shape events.js's setView,
// compare-dialog.js's and run-table.js's setServices already use, and for the
// same reason: each is something only app.js's bootstrap can do, and a missing
// name has to fail while the wiring is being written rather than at the first
// press of Top up.
//
//   url        resolves a path against document.baseURI and attaches the
//              access token (REQUIREMENTS.md 3.3 - the UI has to work under
//              an arbitrary base path). Every request this element makes
//              goes through it.
//   post       the one place a POST is sent, with its error shape.
//   log        one activity-log line, tagged with which torrent it is about
//              (app.js's logFor).
//   showError  the page's own error line, above the table.
//   redraw     one whole row redrawn, row half and detail half (app.js's
//              syncEntry). refreshAgain needs it: the answer to its request
//              lands asynchronously, and what it changes is read by the row
//              as well as by this element.
//   cancelRun  the ✕ this element's header carries. A POST, and deliberately
//              the SAME function the table's own ✕ and an un-tick mid-fetch
//              reach, so there is one place a cancel is sent from.
//
// Undefined until setServices runs, which is checked there rather than at each
// call site.
let url = null;
let post = null;
let log = null;
let showError = null;
let redraw = null;
let cancelRun = null;

const SERVICES = ["url", "post", "log", "showError", "redraw", "cancelRun"];

function setServices(services) {
  for (const name of SERVICES) {
    if (typeof services[name] !== "function") {
      throw new Error("run-detail: setServices needs a " + name + "() function");
    }
  }
  ({ url, post, log, showError, redraw, cancelRun } = services);
}

// DETAIL is the run's own half of an expanded row, and every comment in it is
// about the markup on the line beside it (see this module's header for why it
// is a string rather than markup in index.html).
//
// The file list is NOT in here: it is a nested element, created and appended
// by build() below, which is where the note about it being the last block in
// this pane lives.
const DETAIL =
  '<header class="run-detail-header">' +
    '<span class="run-badge"></span>' +
    '<h2 class="run-detail-title"></h2>' +
    // The run's own .torrent, offered once the run has announced one
    // (TOR-73), pinned level with the name together with Cancel (TOR-169).
    // It used to sit two blocks down, under the summary, on the strength
    // of being a property of the torrent rather than of any one video
    // file - a property of the torrent belongs, if anything, even more
    // plainly on the torrent's own header than under its summary, so that
    // reasoning is what moved it here rather than what it argued against.
    // Cancel already lived in this header; .run-detail-header-actions
    // (detail.css) is where the two now sit in a fixed order so neither
    // moves when the other appears or disappears - see that rule's own
    // comment for why a top-up can put both on screen at once.
    '<span class="run-detail-header-actions">' +
      '<a class="torrent-save" download ' +
        'title="The info dictionary is the one the swarm sent, so this file\'s infohash is the torrent\'s. ' +
        'The wrapper around it is generated: the creation date is when the file was written, and the comment ' +
        'and created-by name the BitTorrent library, not whoever published the torrent.">Save .torrent</a>' +
      '<button class="run-detail-cancel" type="button" data-idle>Cancel</button>' +
    '</span>' +
  "</header>" +
  '<p class="run-detail-error" hidden></p>' +
  // The torrent's own confirmed name, filled in and unhidden by
  // metadata_ready/needs_action (events.js). TOR-178: it used to also state
  // how many of the torrent's video files this run had picked ("N of M ...
  // selected") - that half is gone, so this paragraph carries the name
  // alone now. The torrent's own file count (M) did not go with it; see
  // the "Torrent files" spec renderFileMeta adds to each file's own
  // Metadata disclosure (file-detail.js).
  '<p class="torrent-summary" hidden></p>' +
  // What Save .torrent (now in the header above) left behind: sending the
  // same file to a watch directory on this host, and the note reporting
  // what a send did (TOR-73). This half stays here rather than following
  // Save up into the header (TOR-169) for two reasons - it is a
  // secondary, less-used action (most deployments have no watch
  // directory, state.watch, to send to at all), and torrent-note carries
  // a sentence ("sent to /path/to/watch", or an error), which reads fine
  // as a line of prose under the summary and would only compete with the
  // name for room in the header.
  '<p class="torrent-actions" hidden>' +
    '<button type="button" class="torrent-send" hidden>Send to my client</button>' +
    '<span class="torrent-note"></span>' +
  '</p>' +
  // RUNNING THIS ROW AGAIN (TOR-152), in the detail rather than in the
  // row's own actions cell. Two reasons, and the first is the honest one:
  // .run-cell-actions is 3.8rem wide and nowrap, sized for ✕ and the two
  // queue arrows, so a fourth control there widens the column and squeezes
  // the torrent's name - the same measurement .run-cell-queue's own rule
  // records. The second is that this is where the run's other verbs
  // already are (Save .torrent above, Regenerate and Compare per file
  // below), and where there is room for the sentence that must be read
  // before the button is pressed.
  //
  // It sits above the picker and the files for the same reason
  // .torrent-actions does: it is about the RUN, not about any one video
  // file in it. Since TOR-195 that is structural rather than a matter of
  // order: the files are in a nested <file-list>, and this offer is
  // outside it - which is what the acceptance criterion asks to be
  // checkable rather than eyeballed.
  '<section class="run-again" hidden>' +
    '<p class="run-again-line"></p>' +
    '<p class="run-again-foot">' +
      '<button type="button" class="run-again-go" hidden>Top up</button>' +
      '<button type="button" class="run-again-retry" hidden>Retry</button>' +
      '<span class="run-again-cost"></span>' +
    '</p>' +
    '<p class="run-again-note"></p>' +
  '</section>';

// ---------------------------------------------------------------------------
// RUNNING A ROW AGAIN (TOR-152). Two jobs that look like one button and are
// not: TOP UP finishes a run that produced something and stopped at a
// ceiling, RETRY runs one that produced nothing again. Which of them a row
// offers is decided by what the row actually is, never by asking the person
// to know the difference.

// LIMIT_LEVER is the sentence for each ceiling a run can stop at, and it is
// the whole of TOR-152's second question: which lever is the right one.
//
// A per-run traffic raise only ever helps the first of these. The other two
// are recorded distinctly all the way from core (StopBudget, StopTime and
// StopRoof are three separate reasons since TOR-161 - see TopUp.Limit), so
// the page can say which one it was instead of offering more traffic to a
// run that never ran out of any.
const LIMIT_LEVER = {
  traffic: "stopped at this run's own traffic ceiling",
  time: "stopped at this run's own time limit",
  roof: "stopped at the client-wide traffic roof",
};

const LIMIT_NOTE = {
  time: "This run ran out of TIME, not traffic - a bigger traffic allowance would " +
      "change nothing. The run's own clock is -max-time.",
  roof: "The CLIENT-WIDE traffic roof stopped this, not this run's own ceiling, so " +
      "raising this run's allowance would change nothing: the roof is shared with " +
      "every other run and is set with -max-client-bytes. This runs again at the " +
      "ordinary ceiling, which is worth doing if whatever filled the roof has since gone.",
};

class RunDetail extends HTMLElement {
  constructor() {
    super();

    // The record this detail belongs to, set by bind() once the entry exists.
    // Null until then, which is a window of exactly two statements in app.js's
    // newRunEntry - the DOM has to exist before the entry that names it can.
    this.entry = null;
    // The nested file list, created by build(). Null until then.
    this.files = null;
  }

  connectedCallback() {
    this.build();
  }

  // build puts this element's own markup inside it, finds its parts and wires
  // its controls - ONCE, however many times this element is connected.
  //
  // The guard is not defensive tidiness: see this module's header. A run's two
  // rows are MOVED by the table's re-sort on every redraw, which disconnects
  // and reconnects everything inside them, and a second innerHTML would throw
  // away the file list, every frame grid and every open disclosure in it.
  //
  // Public rather than private because the element that creates one calls it
  // before appending it: createElement constructs a custom element
  // synchronously, but connectedCallback is a reaction, and the id the row's
  // toggle has to name (regionId) is read in the same turn.
  build() {
    if (this.detailEl) return;

    this.innerHTML = '<div class="run-detail">' + DETAIL + "</div>";

    // Found inside THIS element rather than by document id, so one torrent's
    // detail cannot pick up another's parts - which is the whole reason the
    // page can hold several open at once.
    this.detailEl = this.querySelector(".run-detail");
    // The id the row's disclosure names in aria-controls. Minted from the one
    // counter all three detail levels share (state.js's nextDetailId), so it
    // is unique across the document rather than only within this prefix.
    this.detailEl.id = nextDetailId("run-detail-");

    this.detailBadge = this.querySelector(".run-detail-header .run-badge");
    this.detailTitle = this.querySelector(".run-detail-title");
    this.detailCancel = this.querySelector(".run-detail-cancel");
    this.detailError = this.querySelector(".run-detail-error");
    this.torrentSummary = this.querySelector(".torrent-summary");
    this.torrentActions = this.querySelector(".torrent-actions");
    this.torrentSave = this.querySelector(".torrent-save");
    this.torrentSend = this.querySelector(".torrent-send");
    this.torrentNote = this.querySelector(".torrent-note");
    this.againEl = this.querySelector(".run-again");
    this.againLine = this.querySelector(".run-again-line");
    this.againGo = this.querySelector(".run-again-go");
    this.againRetry = this.querySelector(".run-again-retry");
    this.againCost = this.querySelector(".run-again-cost");
    this.againNote = this.querySelector(".run-again-note");

    // A missing part is a wiring error, not a state to degrade into: every
    // method below dereferences these, so failing here names the part that is
    // absent instead of throwing "cannot read property of null" out of
    // whichever handler happens to fire first. It is worth having even though
    // this element builds its own markup - a class renamed in DETAIL above and
    // not here is exactly the typo it catches, and it is silent otherwise.
    for (const [name, node] of Object.entries({
      detailEl: this.detailEl,
      detailBadge: this.detailBadge,
      detailTitle: this.detailTitle,
      detailCancel: this.detailCancel,
      detailError: this.detailError,
      torrentSummary: this.torrentSummary,
      torrentActions: this.torrentActions,
      torrentSave: this.torrentSave,
      torrentSend: this.torrentSend,
      torrentNote: this.torrentNote,
      againEl: this.againEl,
      againLine: this.againLine,
      againGo: this.againGo,
      againRetry: this.againRetry,
      againCost: this.againCost,
      againNote: this.againNote,
    })) {
      if (!node) throw new Error("run-detail: no " + name + " inside the element");
    }

    // WHAT THE TORRENT HOLDS (TOR-180), and since that ticket the row's
    // ordinary content rather than a state one run happens to be in: this
    // list is on screen in every state that knows a file list, not only for
    // a torrent parked waiting to be picked from.
    //
    // AND SINCE TOR-182 IT IS THE LAST BLOCK IN THIS PANE, because each
    // file's own detail - its metadata, its progress, its frames and its
    // buttons - hangs off that file's own row inside this list rather than in
    // a `<section class="files">` below it. There is nothing left to put after
    // the list, so the detail's structure is: what the run is (header,
    // summary, top-up), then what the torrent holds, and inside that, one file
    // at a time. Since TOR-195 those are three elements rather than three
    // stretches of one file, and this append is the second of the three
    // boundaries: everything above is about the run, everything inside the
    // list is about the files, and this element never learns what went in.
    //
    // Built before it is appended rather than left to its own
    // connectedCallback: createElement constructs it synchronously, but
    // connectedCallback is a reaction, and its own build has to have happened
    // before anything reaches its methods. build() is idempotent, so calling
    // it here and again on connect costs one field test.
    this.files = document.createElement("file-list");
    this.files.build();
    this.detailEl.append(this.files);

    // Every listener is an arrow function, deliberately: a `function`
    // declaration used as a listener is given the LISTENING ELEMENT as its
    // `this`, so `this.entry` inside one would be the button's own missing
    // field rather than this element's record - which is TOR-194's shipped
    // regression (endColumnDrag) in the one shape a text check cannot see.
    //
    // They read this.entry when they fire, which is why binding an entry is
    // one assignment rather than four closures: the DOM has to exist before
    // the entry that names it can, and a handler that reads the field at click
    // time does not care which came first.
    this.detailCancel.addEventListener("click", () => cancelRun(this.entry.id));
    this.torrentSend.addEventListener("click", () => this.sendTorrent());
    this.againGo.addEventListener("click", () => this.topUpRun());
    this.againRetry.addEventListener("click", () => this.retryRun());
  }

  // regionId is the id the row's own disclosure button has to name in
  // aria-controls, and the one thing about this element the TABLE's half of
  // the row needs. run-table.js sets aria-expanded (the row's own state) and
  // the page sets aria-controls (the id only the detail can mint) - one
  // attribute each, on the same button, which is the line TOR-194 drew.
  get regionId() {
    this.build();
    return this.detailEl.id;
  }

  // bind hands this element the record it draws from. One assignment, and it
  // is separate from build() for the reason run-table.js's newRow and bindRow
  // are separate: the entry does not exist when the DOM is built, because it
  // is assembled OUT OF the DOM (state.js's newRunState, the table's row parts
  // and this element).
  bind(entry) {
    this.entry = entry;
    this.files.bind(entry);
  }

  // syncDetail is the detail half of one redraw of one torrent - the half
  // app.js's syncEntry calls after the table has redrawn the row (which is
  // where the order lives, not here).
  //
  // THE TWO HALVES DO NOT SHARE A COMPUTATION, deliberately, even though both
  // need displayName(entry): each reads it from state itself rather than one
  // handing the other its answer. That is the same rule the whole split rests
  // on - a renderer reads state, never another renderer's leftovers - and the
  // price is one call to a pure derivation.
  syncDetail() {
    const entry = this.entry;

    const shown = displayName(entry);
    this.detailBadge.textContent = badgeLabel(entry);
    this.detailBadge.dataset.state = badgeState(entry);
    this.detailTitle.textContent = shown.text;
    this.detailTitle.classList.toggle("run-name-provisional", shown.provisional);
    // The sentence under the header, from entry.summaryLine - which
    // metadata_ready and needs_action each write their own version of
    // (events.js). Drawn here, on every event, rather than by those two
    // handlers reaching in: that is the whole of "rendering reads from state
    // rather than from whatever the last handler left in scope".
    this.renderSummaryLine();
    // data-idle, not .hidden: Save .torrent sits right next to Cancel in
    // .run-detail-header-actions (TOR-169), and [hidden]'s display: none would
    // let Save slide over to fill the gap the instant Cancel is not
    // cancellable - the same "control that jumps sideways" .run-priority-up/
    // -down are disabled rather than hidden at the ends of their band to
    // avoid. data-idle keeps Cancel's box in the flow (see its own rule in
    // detail.css) so Save's position never depends on it.
    this.detailCancel.toggleAttribute("data-idle", entry.disk || !cancellable(entry.state));
    this.detailError.hidden = !entry.error;
    this.detailError.textContent = entry.error || "";
    // The file list, and which of its controls this row's state earns
    // (TOR-180). This used to be one rule reading `state === "needs-action"`,
    // which is what made the list a state rather than the row's content.
    this.files.syncFileList();
    // TOR-152, in this order on purpose: draw whatever answer this row already
    // has, then ask for a newer one if the row has moved on. Drawing first is
    // what keeps the section from flickering empty on every event between two
    // answers.
    this.renderAgain();
    this.refreshAgain();
  }

  // resetView takes off screen everything resetRunState (state.js) took out of
  // the entry - what a run_state "reset" means for the DOM: this run's own
  // history is starting over (a fresh start, or a reconnecting page about to
  // replay it from the beginning), not "every torrent on the page is gone".
  //
  // TWO FUNCTIONS RATHER THAN ONE (TOR-191), and not for tidiness. Every field
  // the state half clears is something the replayed history is about to state
  // again; a renderer keeping its own copy of one of them would be a second
  // place the reset had to be got right, which is exactly how a reconnect comes
  // to leave a stale set behind (TOR-184). So nothing here DECIDES anything -
  // it draws the emptiness the state half already established, which is why
  // every line below calls the one function that may draw the thing it names.
  //
  // events.js calls the two together, in that order, and nowhere else does.
  resetView() {
    // The list's own emptying, which is the file list's to do: since TOR-182
    // the file blocks live INSIDE its rows, so there is no separate container
    // for this element to empty. entry.fileEntries, the only handle on those
    // blocks, was already cleared by resetRunState before this ran.
    this.files.reset();
    // The summary paragraph and the run's own .torrent, both of which read a
    // field the state half emptied rather than being reached into.
    this.renderSummaryLine();
    this.renderTorrent();
  }

  // renderSummaryLine draws the sentence under the header from
  // entry.summaryLine - the torrent's own confirmed name, or for a parked one
  // the name plus what it holds and that nothing is captured yet.
  //
  // ONE FUNCTION, so the paragraph can never be left visible and empty or
  // filled and hidden: an empty string IS the absence, exactly the rule
  // file-list.js's setFileNote follows for a file's own note one level in. The
  // two sentences themselves are written by the two messages that carry a name
  // (events.js's applyMetadataReady and applyNeedsAction); nothing here
  // chooses between them, which is why a parked row's sentence does not change
  // the instant its first tick moves it out of needs-action.
  renderSummaryLine() {
    const entry = this.entry;
    this.torrentSummary.textContent = entry.summaryLine;
    this.torrentSummary.hidden = !entry.summaryLine;
  }

  // refreshAgain asks the server what finishing this row would cost, once per
  // answer worth having.
  //
  // ONLY FOR AN OPEN ROW, and that is the same rule loadFileDetail follows one
  // level down: the endpoint reads every selected file's manifest off disk -
  // which is exactly what GET /runs refuses to do for fifty rows at once (see
  // walkRuns) - so it is paid for the row a person is actually looking at, and
  // not before.
  //
  // againKey is what makes calling this from syncDetail safe: syncDetail runs
  // on every event, and without the key a run streaming twenty frames would
  // ask twenty times for an answer that cannot change until it finishes. The
  // key carries the completeness counts as well as the state, so the one
  // moment the answer DOES change - a run ending, having filled some gaps -
  // asks again.
  refreshAgain() {
    const entry = this.entry;
    if (!entry.expanded || !entry.infohash) return;
    if (!entry.disk && !FINAL.has(entry.state)) return;

    const key = [entry.infohash, entry.params, entry.state, entry.complete, entry.selected].join(":");
    if (entry.againKey === key) return;
    entry.againKey = key;

    const target = url("runs/" + entry.infohash + "/topup");
    // The set, when this row knows which one it is. A live row that finished
    // while the page watched does not - run_state carries no params - and the
    // server resolves it from the infohash instead (Server.TopUp), by the very
    // rule that decides whether this row could have merged with a disk record
    // at all.
    if (entry.params) target.searchParams.set("params", entry.params);

    fetch(target)
      .then((response) => (response.ok ? response.json() : null))
      .then((data) => {
        // A 404 is a row with no record on disk - a failed run, most often,
        // which is precisely the row that gets Retry and no top-up. Null
        // rather than a zeroed answer, so renderAgain can tell "nothing to
        // finish" from "nothing was asked".
        entry.topup = (data && data.topup) || null;
        if (entry.topup && entry.topup.params) entry.params = entry.topup.params;
        redraw(entry);
      })
      .catch(() => {});
  }

  // renderAgain draws whichever of the two verbs this row has, and - for a top
  // up - the traffic it will be allowed BEFORE it is spent.
  //
  // That last part is the requirement, not a nicety. TOR-50's trap is that n is
  // per video file, so the ceiling a person meets is the per-file figure times
  // the files selected rather than the number in the flag's help; this is the
  // second place that trap can bite, so the line states the multiplication
  // (frames × files) and the figure states bytes, both from the server's own
  // arithmetic rather than this page's.
  renderAgain() {
    const entry = this.entry;
    const t = entry.topup;
    // Retry is offered for a run that ENDED WITHOUT A RESULT, which is what
    // failed means (a run-scoped core.Failed) and what cancelled can mean. A
    // disk row is never one: it exists because a run wrote a record, and it
    // has no live entry to run again anyway.
    const canRetry = !entry.disk && (entry.state === "failed" || entry.state === "cancelled");
    // Only on a row that has SETTLED. A row mid-run is not a row with an offer
    // on it: the figure this block states was priced off a manifest the run in
    // flight is rewriting, and the button would ask the server to start a run
    // that is already going (refused, correctly, by Server.again). The whole
    // block goes rather than the buttons alone - what it says stops being true
    // for as long as the run lasts, and refreshAgain brings it back the moment
    // the run reaches a final state.
    const settled = entry.disk || FINAL.has(entry.state);
    const canTopUp = settled && !!t && !t.refused && t.remaining > 0;
    // t.complete is the one refusal that says nothing a person does not
    // already have: the row's own DONE/PARTIAL badge already carries "every
    // frame this run asked for is already on disk" (TOR-178), so that refusal
    // draws no line here at all - the block collapses rather than repeating
    // it. Every other refusal reason is a genuine fact about the record (a
    // stale one, or files missing from disk) and still gets drawn below.
    const showRefusal = settled && !!t && !!t.refused && !t.complete;

    this.againRetry.hidden = !canRetry;
    this.againGo.hidden = !canTopUp;
    this.againEl.hidden = !canRetry && !canTopUp && !showRefusal;

    if (canTopUp) {
      const short = t.files.filter((f) => f.captured < f.planned).length;
      const asked = t.count * t.files.length;
      const stopped = LIMIT_LEVER[t.limit];
      this.againLine.textContent =
        t.captured + " of " + asked + " frames (" + t.count + " × " + t.files.length +
        " file(s)) — " + t.remaining + " point(s) still missing across " + short + " file(s)" +
        (stopped ? ", " + stopped +
          (t.limit === "traffic" && t.ceiling_bytes ? " of " + bytesLabel(t.ceiling_bytes) : "") : "");
      // The figure, in the accent each file row's own cost line wears, because
      // it is the same promise: this is what pressing the button spends.
      this.againCost.textContent = t.raise_helps
        ? "up to " + bytesLabel(t.offer_bytes) + " more traffic" +
            (t.roof_capped ? " (all the client-wide roof allows)" : "") +
            " — a ceiling sized to finish in one press, not an estimate of what it will cost"
        : "at the ordinary ceiling — no raise would help";
      this.againCost.title = t.files
        .map((f) => f.path + ": " + f.captured + "/" + f.planned)
        .join("\n");
      // Said rather than left to be discovered. Pieces are discarded after
      // every run (REQUIREMENTS.md 2.9), so this is not a download resuming
      // where it stopped: the frames come back for free off disk, and the
      // piece data behind the points that are still missing is fetched again.
      // That is what makes a top-up cheap despite the discard, and a person
      // expecting a byte-for-byte continuation would be surprised twice - once
      // by the cost, once by the wait.
      this.againNote.textContent =
        (LIMIT_NOTE[t.limit] ? LIMIT_NOTE[t.limit] + " " : "") +
        "The " + t.captured + " frame(s) already taken are reused off disk. The piece data " +
        "for the " + t.remaining + " point(s) still missing is fetched again - pieces are " +
        "discarded after every run, so only the frames survive, never the download.";
      return;
    }

    if (showRefusal) {
      this.againLine.textContent = t.refused;
      this.againCost.textContent = "";
      this.againCost.title = "";
      this.againNote.textContent = "";
      return;
    }

    if (canRetry) {
      this.againLine.textContent = entry.state === "failed"
        ? "This run produced nothing, so there is nothing to top up — retry asks for the " +
            "same thing again: the same source, the same files, the same ceiling."
        : "This run was cancelled — retry asks for the same thing again: the same source, " +
            "the same files, the same ceiling.";
      this.againCost.textContent = "";
      this.againCost.title = "";
      this.againNote.textContent = "";
    }
  }

  // topUpRun asks for the gaps in this row's own result set to be filled.
  //
  // It sends the SET, never a number: the extra traffic was computed by the
  // server, shown on this row, and is recomputed by the server when this lands
  // (Server.TopUpRun). A page that could name its own ceiling would be a page
  // that could spend somebody's allowance by asking.
  //
  // claiming is set before the request, not after it, because the socket races
  // the response: the run this starts can publish its first run_state before
  // the fetch settles, and resolveIncomingRun needs the flag already up to
  // fold that id into this row instead of opening a second one.
  async topUpRun() {
    const entry = this.entry;
    showError("");
    this.againGo.disabled = true;
    entry.claiming = true;
    try {
      const info = await post("runs/topup", {
        infohash: entry.infohash,
        params: entry.params || undefined,
        // The row's own live id, when it has one, so the server can re-arm
        // this very entry rather than mint a second one for one torrent. A
        // disk row sends none - nothing ever minted one for it.
        id: entry.disk ? undefined : entry.id,
      });
      log(entry, "topping up: " + (entry.topup && entry.topup.remaining) + " point(s), up to " +
          bytesLabel((entry.topup && entry.topup.offer_bytes) || 0) + " more traffic");
      claimReopenedRun(entry, info.id);
      // TOR-203: the redraw belongs to the caller, and this one used to be done
      // for it by a call state.js could not make. Without it a re-armed run
      // keeps its old row until the socket's next message.
      redraw(entry);
    } catch (err) {
      entry.claiming = false;
      showError(String(err.message || err));
      log(entry, "could not top up: " + (err.message || err));
    } finally {
      this.againGo.disabled = false;
    }
  }

  // retryRun runs this row's own request again, unchanged.
  //
  // The question the owner actually asked of a torrent whose metadata never
  // arrived - "как теперь его заново запустить?" - had no answer on this page
  // at all: the row sat there and the only way back was to paste the magnet a
  // second time. This is that answer, and it deliberately raises nothing (see
  // Server.RetryRun): a run that never reached a ceiling is not one a bigger
  // ceiling helps.
  async retryRun() {
    const entry = this.entry;
    showError("");
    this.againRetry.disabled = true;
    entry.claiming = true;
    try {
      const info = await post("runs/retry", { id: entry.id });
      log(entry, "retrying");
      claimReopenedRun(entry, info.id);
      // TOR-203: the redraw belongs to the caller, and this one used to be done
      // for it by a call state.js could not make. Without it a re-armed run
      // keeps its old row until the socket's next message.
      redraw(entry);
    } catch (err) {
      entry.claiming = false;
      showError(String(err.message || err));
      log(entry, "could not retry: " + (err.message || err));
    } finally {
      this.againRetry.disabled = false;
    }
  }

  // renderTorrent draws the run's own .torrent - the save link in the header,
  // and where this server has somewhere to send it, the group below it
  // (TOR-169) - from entry.torrentURL.
  //
  // The one thing that decides whether the save link is there is whether the
  // run announced a URL for it, which the server only does when the file is
  // really on disk (server.go's record). So a run captured before torpeek
  // kept one, and a run whose write failed, simply have no link; nothing here
  // guesses at a path, and there is no broken link to press.
  //
  // The anchor carries a bare download attribute rather than a filename: the
  // server sends a Content-Disposition that already names the file after the
  // torrent itself (TOR-170) - or, absent a name, after its infohash - and
  // that server-sent filename is what a browser uses. Putting a name here too
  // would only be a second name that never takes effect.
  //
  // .torrent-actions (Send to my client, and the note reporting what a send
  // did) is a separate decision from the save link's own: it needs both a
  // .torrent AND a watch directory (state.watch) to have anything to show, so
  // it is gated on both rather than mirroring the save link's single
  // condition - see the block's own comment in DETAIL above.
  //
  // IT IS NOT CALLED FROM syncDetail, deliberately, even though it reads
  // nothing but state: it clears entry.torrentNote, and syncDetail runs on
  // every event - so "sent to /path/to/watch" would be wiped a moment after it
  // appeared. Its callers are the two moments the link itself can change (the
  // run's own done event and a reset, both events.js) and app.js's
  // loadDefaults, which is the one moment state.watch itself can change and is
  // before any send can have happened.
  renderTorrent() {
    const entry = this.entry;
    this.torrentSave.hidden = !entry.torrentURL;
    this.torrentNote.textContent = "";
    if (!entry.torrentURL) {
      this.torrentSend.hidden = true;
      this.torrentActions.hidden = true;
      return;
    }
    this.torrentSave.href = url(entry.torrentURL);
    this.torrentSend.hidden = !state.watch;
    this.torrentActions.hidden = !state.watch;
  }

  // sendTorrent asks the server to drop this run's .torrent into the watch
  // directory a torrent client on that host is already reading (TOR-73).
  //
  // It is a separate action from the link beside it, not a fallback for it: the
  // link saves the file where the BROWSER is, which on a seedbox deployment is
  // somebody's laptop, while this one queues the torrent where the UI itself is
  // running. The answer names the file that landed, which is the only
  // confirmation available - nothing here can watch a torrent client pick it up.
  async sendTorrent() {
    const entry = this.entry;
    if (!entry.torrentURL) return;

    this.torrentSend.disabled = true;
    this.torrentNote.textContent = "sending…";
    try {
      const info = await post(entry.torrentURL + "/watch", {});
      this.torrentNote.textContent = "sent to " + (info.path || "the watch directory");
      log(entry, "torrent sent to " + (info.path || "the watch directory"));
    } catch (err) {
      this.torrentNote.textContent = String(err.message || err);
      log(entry, "sending the torrent failed: " + (err.message || err));
    } finally {
      this.torrentSend.disabled = false;
    }
  }
}

// Native, no bundler: this is the whole registration mechanism, and importing
// this module for its side effect is how app.js gets the element defined
// before its own newRunEntry creates the first one.
customElements.define("run-detail", RunDetail);

export { RunDetail, DETAIL, LIMIT_LEVER, LIMIT_NOTE, setServices };
