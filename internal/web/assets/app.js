// THE PAGE ITSELF: every element, every listener, every redraw (TOR-191).
//
// ONE FILE BECAME THREE, and the reason is the one this whole batch stands on.
// app.js used to be the UI, the state and the event dispatch at once: twelve
// message types wrote entry fields and text nodes in the same breath, and
// every rendering function reached into a shared state.runs and into the
// entry/fentry objects directly. That coupling is what made a component
// impossible - there was nothing to HAND a component, so it had to go looking.
//
//   state.js   what a run and a file KNOW, and every derivation over it.
//              No DOM, no fetch, no message shapes. Executable on its own.
//   events.js  the socket, and the one place a message becomes a state
//              change. No DOM either: it names redraws through a view this
//              file installs (setView, at the bottom).
//   app.js     this file. The elements, the listeners, the requests, and
//              every function that draws - each of which is now handed STATE
//              and reads only state, never a message.
//
// The imports run one way (app.js -> events.js -> state.js) so that neither
// of the other two can reach the DOM even by accident, which is what lets both
// be run for real in a plain node process rather than only read as text.
//
// It reads the event stream and does nothing else - the run lives in the core,
// and a client that started deciding things would be a second place where the
// run is defined (ARCHITECTURE.md).
//
// Every URL is resolved against document.baseURI rather than written from the
// site root, so the page works unchanged under /torpeek behind a proxy.
//
// NO BUILD STEP: all three are served straight out of the embedded assets and
// loaded natively as ES modules. index.html's own <script> is what says so,
// and it has to say `type="module"` or none of this parses at all.
//
// AND THEN THE ELEMENTS (TOR-192 through TOR-195), each imported below for its
// side effect: frame-panel.js, compare-dialog.js, run-table.js and - since
// TOR-195 - run-detail.js, which brings file-list.js and file-detail.js in
// with it. They are not a fourth layer: they sit beside app.js, in its half of
// the split, and what each took is the DRAWING of one thing.
//
// WHAT IS LEFT IN THIS FILE, and after TOR-195 the list is short enough to be
// exhaustive: the intake line and the drop target, the requests (post, del and
// the four routes only this file addresses), the access token and url(), the
// activity log, the page's own status and error lines, the socket's address,
// the two factories state.js reaches the DOM through, and the wiring at the
// bottom. Not one function that draws a run, a file list or a file remains.
//
// THE THREE NESTED ELEMENTS (TOR-195) are the run's detail, the list of every
// file the torrent holds, and one file's own detail - three genuinely nested
// things that were one 3,500-line stretch of this file. Each one's header says
// where its own boundary falls; run-detail.js's carries the two decisions all
// three share (why their markup is a template rather than markup in
// index.html, and why none of them has a disconnectedCallback).

import {
  PRIORITY_HIGH,
  PRIORITY_LOW,
  claimReopenedRun,
  ensureRun,
  hasPriority,
  newRunState,
  setFileFactory,
  setRunFactory,
  shortId,
  state,
} from "./state.js";
import { connect, setView } from "./events.js";
// Imported for its side effect and nothing else: the module's last statement
// is customElements.define("frame-panel", ...), and running it here is what
// upgrades the <frame-panel> already sitting in the parsed page. A
// type="module" script is deferred, so the markup exists by the time this
// evaluates and the upgrade - and with it connectedCallback's wiring -
// happens before any line of this file runs. Nothing is destructured out of
// it: the element's whole API is the open() method on the instance.
import "./frame-panel.js";
// Imported for the same side effect, and then WIRED: unlike the frame panel,
// the compare dialog needs two things only the page can provide - url(),
// which carries the base path and the token, and log(). setServices is
// called at the bootstrap below rather than here, because url() closes over
// TOKEN and TOKEN is read further down this file.
import { setServices as setCompareServices } from "./compare-dialog.js";
// Imported for the same side effect, and wired the same way (TOR-194). Its
// four services are all things only this file can do - three POSTs and one
// redraw of the detail a row opens onto - and, like the compare dialog's, they
// are handed over at the bootstrap below.
//
// THE SIDE EFFECT MATTERS MORE HERE THAN FOR EITHER DIALOG: the element's
// connectedCallback is what builds the six live columns, so the header row is
// three columns wide until this import has evaluated and nine afterwards.
// Every import runs before the first line of this file, which is why the
// el. lookup below can count on an element whose parts are already found.
import { setServices as setRunTableServices } from "./run-table.js";
// Imported for the same side effect, and wired the same way (TOR-195). ONE
// import for THREE elements: run-detail.js imports file-list.js, which imports
// file-detail.js, each for the side effect of defining the next one down - so
// the definitions exist in the order the mounting needs them, guaranteed by
// the module graph rather than by the order of statements in this file.
//
// Each of the three has its own service set, because each needs a different
// slice of what only this file can do, and each refuses a set missing a name
// at wiring time. They are handed over at the bootstrap below for the reason
// the two dialogs' are: url() closes over TOKEN, which is read from this
// page's own address further down.
import { setServices as setRunDetailServices } from "./run-detail.js";
import { setServices as setFileListServices } from "./file-list.js";
import { setServices as setFileDetailServices } from "./file-detail.js";

// TOKEN is this page's proof of authorization when the server was started
// with one (TOR-30): the URL printed at startup carries it as a query
// parameter, since that is the one representation that reaches every kind of
// request this page makes - a WebSocket handshake included, where the
// browser gives page script no way to set a custom header. url() below
// attaches it to everything built through it, so the fetch calls, the
// WebSocket and every frame image src stay authorized without each call site
// having to remember to. It is not stripped from the address bar afterwards:
// doing so would break a plain page reload, and the trade-off - the token
// sitting in the visible URL and in browser/proxy history for the life of
// the tab - is accepted rather than worked around (see server.go's Config.Token).
const TOKEN = new URLSearchParams(location.search).get("token") || "";

const el = {
  status: document.getElementById("status"),
  form: document.getElementById("start"),
  source: document.getElementById("source"),
  count: document.getElementById("count"),
  mode: document.getElementById("mode"),
  go: document.getElementById("go"),
  error: document.getElementById("error"),
  dropzone: document.getElementById("dropzone"),
  fileInput: document.getElementById("file-input"),
  dropOverlay: document.getElementById("drop-overlay"),
  log: document.getElementById("log"),
  // ONE entry where there were six (TOR-192). The panel's parts belong to the
  // panel now: it finds them inside itself, and this file is handed the
  // element rather than its insides. Queried by TAG, not by id, because the
  // element IS the thing being asked for - a <frame-panel> anywhere on the
  // page is one, and nothing here needs to know it happens to contain a
  // <dialog id="lightbox">.
  framePanel: document.querySelector("frame-panel"),
  // ONE entry where there were fifteen (TOR-193), the same collapse TOR-192
  // made for the frame panel: the dialog's parts belong to the dialog, which
  // finds them inside itself. Queried by TAG - the element IS what is being
  // asked for, and nothing here needs to know it contains a
  // <dialog id="compare">.
  compareDialog: document.querySelector("compare-dialog"),
  // ONE entry where there were four (TOR-194): the row list, the empty-state
  // paragraph, the nine sortable headers and the scroll wrap all belong to the
  // table now, which finds them inside itself. Queried by TAG for the same
  // reason the two above are - the element IS what is being asked for, and
  // nothing here needs to know it contains a grid of ten tracks (TOR-215;
  // until then, a <table id="run-table">).
  //
  // Nothing in this file writes a row cell any more. What it asks the table
  // for is three things (newRow, bindRow, syncRow) plus the accordion's own
  // setRunExpanded; see run-table.js's header for where the line between the
  // two halves falls and why.
  runTable: document.querySelector("run-table"),
};

function url(path) {
  const u = new URL(path, document.baseURI);
  if (TOKEN) u.searchParams.set("token", TOKEN);
  return u;
}

function setStatus(text, kind) {
  el.status.textContent = text;
  el.status.dataset.state = kind;
}

function showError(message) {
  el.error.textContent = message || "";
  el.error.hidden = !message;
}

function log(line) {
  el.log.textContent += line + "\n";
  el.log.scrollTop = el.log.scrollHeight;
}

// logFor tags every line with which torrent it is about - the log now spans
// every run on the page, not just one, and an untagged line would be
// unreadable the moment a second torrent is added.
function logFor(entry, line) {
  log("[" + shortId(entry.id) + "] " + line);
}

// intake is what the intake line currently says, as one reading rather than
// three services (file-detail.js's Regenerate is the only caller).
//
//   mode       the profile select, which is the one mode a person can
//              actually SEE while pressing Regenerate - a finished run's own
//              profile is not on the wire.
//   count      the frame count as something a request can carry, so an empty
//              or nonsense field means "I did not choose" (countValue).
//   countText  the field's raw text, for prefilling the per-file count box.
//              Not String(count): a field holding "0" prefills "0" today, and
//              changing that is a decision this ticket is not making.
function intake() {
  return { mode: el.mode.value, count: countValue(), countText: el.count.value };
}

// ---------------------------------------------------------------------------
// A RUN ENTRY, and what is left of building one after TOR-195 (see
// run-detail.js's header for the boundary, and run-table.js's for the one
// above it).
//
// THREE ELEMENTS AND THREE LINES. The table builds both <tr>s and hands back
// every element in them, the colspanned detailCell included; this function
// mounts a <run-detail> in that cell, and the detail mounts a <file-list>
// inside itself, and the list mounts a <file-detail> per video row. Not one
// thing any of them builds is in this file any more - not the detail's header,
// not the summary, not the top-up block, not the file list, not a file's own
// grid.
//
// SO THE ENTRY IS ONE OBJECT WITH ONE NEW FIELD. It used to gain twenty-two
// DOM fields here; it gains `detail`, and every one of the twenty-two is now a
// field of the element that owns it. The only reader outside the detail was
// loadDefaults, reaching past the boundary to hide a button - and that is a
// method call now (renderTorrent).
function newRunEntry(id) {
  const rowParts = el.runTable.newRow();

  // Created rather than parsed, and appended to the cell the table handed
  // over. createElement constructs a defined custom element synchronously, so
  // its build() and regionId are there to be used on the next line;
  // connectedCallback is a reaction and would not be.
  const detail = document.createElement("run-detail");
  rowParts.detailCell.append(detail);
  // aria-expanded is the row's own state and is set by the table; this is the
  // other half of the same button's wiring, and it is here because the id it
  // names is one only the detail can mint (run-table.js's own note on where
  // that line falls).
  rowParts.rowToggle.setAttribute("aria-controls", detail.regionId);

  const entry = {
    // The data half, which state.js owns (newRunState) - every field a row
    // KNOWS, with nothing in it that a browser has to exist for.
    //
    // ONE OBJECT, THREE LITERALS SINCE TOR-194, so a key declared below still
    // shadows the same key in either of the others, silently, exactly as two
    // keys in one literal always did (TOR-180).
    // TestNoRunEntryFieldIsDeclaredTwice scans the union of all three rather
    // than any one alone - state.js's newRunState, run-table.js's newRow, and
    // this.
    ...newRunState(id),
    // The row's own elements, built by the table (run-table.js's newRow) and
    // spread in here rather than reached for later, so an entry is still ONE
    // object: the rows are the table's to create and to write, and every
    // field they contribute is a row element. detailCell is the one the lines
    // above spent - the cell this run's detail is mounted in.
    ...rowParts,
    // And the detail itself, which is the whole of this file's half now: one
    // element, holding its own parts and every method that draws them. Written
    // as shorthand, which is also why it cannot collide silently - shorthand
    // names a variable that has to exist.
    detail,
  };

  // The row's four controls, bound now that there is an entry for them to
  // close over - which is why the table splits building a row from binding it
  // (run-table.js's bindRow). Nothing about them is decided here: the table
  // says which element carries which gesture, and the services this file wired
  // say what each gesture does.
  el.runTable.bindRow(entry);
  // And the detail's own, which is one assignment rather than four closures:
  // its listeners were added when its DOM was and read this entry when they
  // fire (run-detail.js's bind).
  detail.bind(entry);

  return entry;
}

// fileBlock is state.js's registered FILE factory, and since TOR-195 it is one
// hop: the detail a file's own row opens onto is mounted by the list that owns
// the row, because the row is where the mount point is (file-list.js's
// mountFileDetail, which is also where the "no row for this index" log lives).
//
// It stays a function of this file rather than being registered as a method,
// because setFileFactory takes (entry, index) and which torrent it is about is
// exactly what has to be resolved to reach the right list.
function fileBlock(entry, index) {
  return entry.detail.files.mountFileDetail(index);
}

// resetRunView takes off screen everything resetRunState (state.js) took out
// of the entry - what a run_state "reset" means for the DOM. Both halves of
// that are the detail's now (run-detail.js's resetView carries the reasoning
// for why they are two functions at all); this is the hook events.js names.
function resetRunView(entry) {
  entry.detail.resetView();
}

// syncEntry is one redraw of one torrent, and since TOR-194 it is two halves
// called in order: the table redraws the row (every cell, the six live
// figures, the queue column, the progress bar, and the re-sort that may move
// it), then the detail redraws what the row opens onto - and, inside that, its
// file list.
//
// THE TWO HALVES DO NOT SHARE A COMPUTATION, deliberately, even though both
// need displayName(entry): each reads it from state itself rather than one
// handing the other its answer. That is the same rule the whole split rests
// on - a renderer reads state, never another renderer's leftovers - and the
// price is one call to a pure derivation.
//
// events.js names this hook and nothing below it, so the order lives here
// rather than in the event layer.
function syncEntry(entry) {
  el.runTable.syncRow(entry);
  entry.detail.syncDetail();
}

// toggleRun is what clicking a row does. A live or already-live-again entry
// just opens and closes. A disk-only entry has nothing to show until it is
// replayed (TOR-55): opening it asks the server to read it back from disk
// under a fresh id, and that id's own run_state/file/frame events - arriving
// over the socket this page already holds open - fill the same container in,
// moments later.
//
// Closing a row mid-reopen does not cancel the reopen, and should not: the
// server is already replaying, the events will land in this entry's detail
// either way, and re-opening the row shows what arrived meanwhile. That is
// exactly the property a collapsed file block already has.
function toggleRun(entry) {
  if (!entry.disk) {
    el.runTable.setRunExpanded(entry, !entry.expanded);
    return;
  }
  if (entry.expanded) {
    el.runTable.setRunExpanded(entry, false);
    return;
  }
  el.runTable.setRunExpanded(entry, true);
  if (entry.reopening) return;
  reopenRun(entry);
}

function reopenRun(entry) {
  entry.reopening = true;
  entry.state = "replaying";
  syncEntry(entry);

  post("runs/reopen", { infohash: entry.infohash, params: entry.params })
    .then((info) => {
      // No re-open of the row afterwards, unlike the code this replaced: the
      // expansion is a flag on the entry, and claimReopenedRun keeps the
      // entry, so swapping its key cannot lose it.
      claimReopenedRun(entry, info.id);
      // The redraw is the caller's since TOR-203: claimReopenedRun cannot see
      // syncEntry, and calling it from there threw on every id swap - quietly,
      // because the re-key had already happened, so the success path was never
      // reached and a reopen that worked arrived as a failure.
      syncEntry(entry);
    })
    .catch((err) => {
      entry.reopening = false;
      entry.state = "failed";
      // A results tree that was moved or copied since it was captured cannot
      // be replayed - its manifest still points at the old, absolute paths
      // (TOR-60) - and this is where that surfaces: a reopen that fails.
      entry.error = String(err.message || err);
      syncEntry(entry);
      logFor(entry, "reopen failed: " + entry.error);
    });
}

async function cancelRun(id) {
  try {
    await post("runs/cancel", { id });
  } catch (err) {
    showError(String(err.message || err));
  }
}

// setPriority moves one waiting torrent up or down the queue (TOR-140) - the
// whole of what the ▲/▼ buttons do, and the answer to the pain this release
// is written from: changing a priority no longer means cancelling a download
// and adding it again at the back.
//
// An ABSOLUTE level is sent, never a step, even though the buttons are steps:
// the arithmetic happens here, against the level this page was last told, and
// the server is asked for that exact value. Sending "one higher" would let
// two clicks on a stale row walk a torrent past where anybody asked for, and
// a retried request would do it a second time.
//
// NOTHING HERE WRITES entry.priority OR entry.queuePosition. The response
// carries both, and they are still ignored: the server publishes a run_state
// to this row and to every other row the move displaced, and applying only
// those keeps one path into those two fields for every browser tab watching -
// this one has no standing to know sooner than the others.
async function setPriority(entry, priority) {
  const want = Math.max(PRIORITY_LOW, Math.min(PRIORITY_HIGH, priority));
  if (!hasPriority(entry) || want === entry.priority) return;
  try {
    await post("runs/priority", { id: entry.id, priority: want });
  } catch (err) {
    showError(String(err.message || err));
    logFor(entry, "could not change the queue priority: " + (err.message || err));
  }
}

// socketAddress is the events socket's own URL, and the reason events.js asks
// the page for one instead of building it: url() resolves against
// document.baseURI and attaches the access token, and both of those are facts
// about THIS PAGE rather than about the event stream (see TOKEN's own note at
// the top of this file, and why a WebSocket handshake is exactly the request
// that cannot carry a header). The ws/wss swap rides along because it is the
// same one decision - which address to open.
function socketAddress() {
  const address = url("events");
  address.protocol = address.protocol === "https:" ? "wss:" : "ws:";
  return address;
}

async function post(path, body) {
  const response = await fetch(url(path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body || {}),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || response.statusText);
  return data;
}

// del is post's counterpart for the one request that removes something.
//
// It takes a whole URL rather than a path, because unlike every POST here the
// delete carries a query parameter (the result set) and url() is what already
// knows how to build one with the access token on it - so the caller sets its
// parameter on that object instead of hand-encoding a string. There is no
// body: the address is the request.
async function del(target) {
  const response = await fetch(target, { method: "DELETE" });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || response.statusText);
  return data;
}

// countValue reads the intake's frame count as something a request can carry.
//
// An empty field returns undefined rather than a number, and JSON.stringify
// drops an undefined field entirely - so "I did not choose" and "no count on
// the wire" are the same thing by construction, and the server's own -n
// stays in force for a page that never touched the field. Anything that is
// not a positive integer is treated the same way: the field's min="1" and
// the server's own check are the two places a bad number is refused, not
// here.
function countValue() {
  const n = parseInt(el.count.value, 10);
  return Number.isInteger(n) && n > 0 ? n : undefined;
}

// loadDefaults fills the count field with the number a run would actually
// use - GET /defaults reports the server's own -n, since the page is served
// straight out of the embed with no templating step that could substitute it
// into the HTML. A failure is not fatal: the field stays empty, which sends
// no count and therefore gets that same default anyway.
async function loadDefaults() {
  try {
    const response = await fetch(url("defaults"));
    if (!response.ok) throw new Error(response.statusText);
    const data = await response.json();
    if (Number.isInteger(data.count) && data.count > 0) el.count.value = data.count;
    state.watch = data.watch === true;
    // This can land after a reconnecting socket has already replayed a
    // finished run, so anything already showing a .torrent is asked again
    // whether it may offer the button - and .torrent-actions (TOR-169)
    // along with it, since the whole group is only worth showing when the
    // button in it is.
    //
    // ASKED, NOT REACHED INTO, since TOR-195: this used to set two `hidden`
    // flags on elements two levels inside the detail, which was the one place
    // in this file that read past the boundary. renderTorrent decides exactly
    // those two flags from exactly these two facts, so the answer is
    // identical - and it is safe to use the whole method here even though it
    // also clears the note a send left behind, because state.watch is what
    // puts the send button on screen at all and this is the only line that
    // ever sets it. No send can have happened yet.
    for (const entry of state.runs.values()) entry.detail.renderTorrent();
  } catch (err) {
    log("could not read the defaults: " + (err.message || err));
  }
}

// loadRuns populates the panel with every torrent GET /runs already knows
// about - what is still live in this process, and everything on disk under
// OutputRoot - so a page that loads after a restart still finds them
// (TOR-54): the socket's own replay only ever covers runs the server still
// holds in memory, never what a previous process left behind.
async function loadRuns() {
  let data;
  try {
    const response = await fetch(url("runs"));
    data = await response.json();
  } catch (err) {
    log("could not load the torrent list: " + err);
    return;
  }

  for (const row of data.runs || []) {
    const disk = !row.id;
    // NOTHING HERE DECIDES WHICH ROWS ARE THE SAME TORRENT, and TOR-162 is
    // what deleted the code that did. TOR-152 had to keep "one torrent, one
    // row" (TOR-140) from this loop, with a liveRowFor() that skipped a disk
    // row whose torrent a live entry on this page was already showing,
    // because listing.go merged a live entry with its record only once the
    // entry was FINAL and a top-up spends its whole life before that. The
    // merge no longer waits for a final state (listing.go's listRuns), so
    // every row this loop is handed is already one torrent's one row - and
    // the rule now lives where a second consumer of GET /runs can see it,
    // which was the point of moving it rather than the point of the code
    // that moved.
    //
    // A disk-only row has no run id to key on - nothing ever minted one for
    // it - so infohash+params, the same pair that addresses it for reopening,
    // stands in. TOR-54 documented that the same torrent captured under two
    // different plans shows up as two rows; keying this way is what keeps
    // them two separate rows here too, rather than one clobbering the other.
    const key = row.id || ("disk:" + row.infohash + ":" + row.params);
    const entry = ensureRun(key);
    entry.disk = disk;
    entry.infohash = row.infohash || entry.infohash;
    entry.params = row.params || entry.params;
    entry.source = row.source || entry.source;
    entry.name = row.name || entry.name;
    // TOR-117: GET /runs carries the two the same way run_state does - never
    // both for one row (see RunSummary.ProvisionalName) - so there is
    // nothing to reconcile here beyond copying whichever arrived.
    entry.provisionalName = row.provisional_name || entry.provisionalName;
    entry.files = row.files || 0;
    entry.complete = row.complete || 0;
    entry.selected = row.selected || 0;
    // row.partial is the server's own verdict (listing.go's MarshalJSON,
    // TOR-80), read as-is rather than recomputed from the counts above -
    // see the comment above badgeState for why nothing here compares
    // complete and selected itself.
    entry.partial = !!row.partial;
    if (!disk) entry.state = row.state || entry.state;
    entry.error = row.error || entry.error;
    // row.live is GET /runs' own Live object (listing.go), present only for
    // a row with an actual client that has spoken at least once - copied
    // through as-is, null rather than defaulted to anything with numbers in
    // it, exactly like every other "absent, not zero" field this page reads
    // (row.partial above, entry.provisionalName). This is the one place
    // loadRuns sets it; from here on events.js's "progress" case keeps it
    // current, the same relationship entry.partial has with run_state's own
    // "partial" field.
    entry.live = row.live || null;
    // TOR-141: row.live.stall is listing.go's own mirror of the same
    // "stall" reading the WebSocket progress event carries - not yet
    // populated by this server (see listing.go's Live.Stall doc for the
    // one-line follow-up that would close that gap), so this is normally
    // null on a fresh load and the page picks the reading up from its next
    // WebSocket heartbeat instead, same as row.live itself does for a run
    // this page has never seen a "progress" for yet. Read defensively
    // anyway, the same shape events.js's "progress" case gives it, so the
    // follow-up needs nothing here once it lands.
    entry.stall = row.live && row.live.stall
      ? { ...row.live.stall, observedAt: Date.now() } : null;
    // TOR-140: the queue's own two fields, present only for a row still
    // waiting to be told to go (listing.go's RunSummary). Read, never
    // derived - see queuePosition's own comment for what was deleted to make
    // that true. `row.priority === undefined` rather than a falsy test: 0 is
    // PriorityNormal, a real answer, and `row.priority || null` would erase
    // exactly the commonest one.
    entry.priority = row.priority === undefined ? null : row.priority;
    entry.queuePosition = row.queue_position || 0;
    // TOR-156: kept rather than cleared when the row carries none, which is
    // the opposite of the two lines above and for the opposite reason. Their
    // absence is INFORMATION - it says the queue has nothing left to say
    // about this row - while an arrival ordinal, once handed out, is true
    // forever and only ever missing because nothing ever handed one out.
    entry.arrival = row.arrival || entry.arrival;
    // row.when is GET /runs's own answer for this row - the newest lifecycle
    // timestamp for a live entry, run.json's created_at for a disk one - and
    // it is the one moment this page overwrites entry.when after creation:
    // this is the initial listing, not a live status update, so setting it
    // here does not conflict with the rule that a status change must never
    // move a row under the default sort.
    const when = row.when ? Date.parse(row.when) : NaN;
    if (!Number.isNaN(when)) entry.when = when;
    // TOR-141: seeds waitingForMetadata's own clock from the server's own
    // timestamp on a page load or reload, rather than leaving it at 0 (which
    // would read as "waiting since the epoch") for a run that was already
    // running before this page ever asked. row.when is StartedAt for a
    // running entry (RunSummary.When's own doc), which is exactly what
    // runningSince means; only set once, the same "never move a row" rule
    // entry.when's own comment gives for why this is safe on the initial
    // listing alone.
    if (entry.state === "running" && !entry.runningSince && !Number.isNaN(when)) {
      entry.runningSince = when;
    }
    syncEntry(entry);
  }
}

// A dropped .torrent is bytes, not a string, so it takes a different request
// than the magnet path - but both end at the same server-side StartRun, and
// from here on the run is indistinguishable from one started by pasting a
// magnet: the same events, the same screens.
async function uploadTorrent(file) {
  showError("");
  const body = new FormData();
  body.append("torrent", file);
  body.append("mode", el.mode.value);
  // A drop reads the same intake field as a pasted magnet; the handler on
  // the other side treats an absent value as the server's default.
  const count = countValue();
  if (count !== undefined) body.append("count", String(count));
  try {
    const response = await fetch(url("runs/upload"), { method: "POST", body });
    const info = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(info.error || response.statusText);
    began(info);
  } catch (err) {
    showError(String(err.message || err));
  }
}

// began is what every successful start has in common: the torrent gets a row
// straight away, from the id the POST already answered with, and that row
// opens - "Take frames" should show something happening immediately, not
// leave a person staring at whatever was on screen before.
//
// It OPENS the new row rather than making it the only open one (TOR-138).
// Nothing already on screen is taken away: queueing a second torrent behind
// the first is the intake's own advertised trick, and a page that closed the
// running one to show a queued one would undo it. Opening is also the one
// place the accordion moves without a click, and it is the right one - a
// torrent that was just asked for is a torrent somebody wants to watch.
function began(info) {
  // The answer to the POST is this run's FIRST state, never an update to one.
  //
  // The response and the socket race, and the socket can win: a multi-file
  // torrent takes the slot for a metadata pass and can be listed and parked
  // before the fetch settles (TOR-67), so run_state:needs-action may already
  // have arrived. Writing the response's "running" over that showed a
  // torrent as running, with a Cancel button and no picker, for a run that
  // was in fact waiting for a person - until a reload read GET /runs and
  // corrected it.
  //
  // An entry the socket has already created has already applied a state and
  // knows better, and its existence is the whole test. A run whose socket
  // delivered nothing yet - a page reconnecting - still gets its row from
  // here, which is why the response is used at all.
  const known = state.runs.has(info.id);
  const entry = ensureRun(info.id);
  entry.disk = false;
  if (!known) entry.state = info.state;
  syncEntry(entry);
  el.runTable.setRunExpanded(entry, true);
}

el.form.addEventListener("submit", async (event) => {
  event.preventDefault();
  showError("");
  const source = el.source.value;
  el.go.disabled = true;
  try {
    const info = await post("runs", { source, mode: el.mode.value, count: countValue() });
    // Cleared the moment the server has accepted the run, not when it
    // finishes - that is the whole point: a second torrent can be queued up
    // right behind the first without waiting for anything.
    el.source.value = "";
    began(info);
  } catch (err) {
    showError(String(err.message || err));
  } finally {
    el.go.disabled = false;
  }
});

// The price on every file's row reads the intake's count, so a list on
// screen has to follow it while it is being typed in.
//
// Keyed off the SECTION now, not a foot: since TOR-181 the figure is on each
// file's own row rather than beside a button, so every row that HAS a list
// has prices to restate - and updateFileCosts is what knows that a row with
// a pass already forming quotes the server's locked count instead of this
// box (passCount).
//
// The gate moved INTO the list with TOR-195 (file-list.js's reprice): "is
// there a list on screen" is a question about the list, and this file no
// longer holds the element that answers it.
el.count.addEventListener("input", () => {
  for (const entry of state.runs.values()) entry.detail.files.reprice();
});

el.fileInput.addEventListener("change", async () => {
  const file = el.fileInput.files[0];
  el.fileInput.value = ""; // lets the same file be picked again later
  if (file) await uploadTorrent(file);
});

// Drag-and-drop works anywhere on the page, not only over the small input
// row - a stranger dragging a .torrent in from Finder or Explorer should not
// have to find a pixel-precise target first. A depth counter is what makes
// dragenter/dragleave over child elements not flicker the overlay off.
function isFileDrag(event) {
  return event.dataTransfer && Array.from(event.dataTransfer.types || []).includes("Files");
}

let dragDepth = 0;

document.addEventListener("dragenter", (event) => {
  if (!isFileDrag(event)) return;
  dragDepth++;
  el.dropOverlay.hidden = false;
});

document.addEventListener("dragleave", () => {
  dragDepth = Math.max(0, dragDepth - 1);
  if (dragDepth === 0) el.dropOverlay.hidden = true;
});

document.addEventListener("dragover", (event) => {
  if (isFileDrag(event)) event.preventDefault();
});

document.addEventListener("drop", async (event) => {
  if (!isFileDrag(event)) return;
  event.preventDefault();
  dragDepth = 0;
  el.dropOverlay.hidden = true;
  const file = event.dataTransfer.files[0];
  if (file) await uploadTorrent(file);
});

// ---------------------------------------------------------------------------
// THE WIRING (TOR-191), and it sits here rather than at the top of the file
// for a reason that can be checked rather than trusted: nothing above it
// reaches the store or the socket. The element lookups, the intake handlers
// and the drop handlers are every top-level statement left in this file -
// TOR-194 took the header build, the sort listeners, the column widths and
// the stall ticker into the table element's own connectedCallback - and not
// one of them calls ensureRun or connect, so registering at the last possible
// moment cannot be too late, and beside the bootstrap it enables is where a
// reader goes looking for it.
//
// THE TWO FACTORIES are how state.js reaches a builder only this file has: an
// entry's DOM half (newRunEntry) and a file's whole block (fileBlock).
// Without them the store still works and still hands back valid entries -
// data-only ones, which is exactly what a headless test wants and exactly
// what a browser must not get.
//
// THE VIEW is every redraw events.js is allowed to name, and setView REFUSES
// one that is missing a hook (events.js's VIEW_HOOKS). That check is the
// point: five more extraction tickets each add a component something in
// events.js has to redraw, and the failure it forecloses - a handler quietly
// calling undefined, on a live run, at the one moment nobody is watching the
// console - is worth a loop over an array.
//
// Every hook below is handed STATE. Not one of them takes a message.
setRunFactory(newRunEntry);
setFileFactory(fileBlock);
// THE COMPARE DIALOG'S TWO SERVICES (TOR-193), wired here for the same
// reason the factories are: they are things only this file can provide.
// url() closes over TOKEN, which is read from this page's own address at the
// top of this file, and log() writes to the activity log element. Neither
// could move into the element without taking the page's bootstrap with it.
// setServices refuses a missing one, so a future rename fails here rather
// than at the first press of Compare.
setCompareServices({ url, log });
// THE RUN TABLE'S FOUR SERVICES (TOR-194), same shape and same reason. Three
// of them are requests this file owns (a cancel, a priority change, and the
// reopen a click on a disk row triggers), and the fourth is the redraw of the
// detail a row has just opened onto - which is a detail concern, so the table
// is handed it rather than naming it.
//
// detailShown was named for the EVENT rather than for the function it stood
// for, and TOR-195 is the ticket that cashed that in: what a detail does when
// it comes on screen is now the detail's own method, and this line changed
// from an alias to a one-line call without the table's contract moving at all.
setRunTableServices({
  toggleRun,
  cancelRun,
  setPriority,
  detailShown: (entry) => entry.detail.refreshAgain(),
});
// THE THREE DETAIL ELEMENTS' SERVICES (TOR-195), same shape and same reason,
// one set each because each needs a different slice of what only this file can
// do. Every one of them is either a REQUEST (post, del, and the url() that
// carries the base path and the token), a PAGE-WIDE surface (the activity log,
// the error line, the intake's own reading, what a new run does), or ANOTHER
// element this file holds the one instance of (the frame panel, the compare
// dialog).
//
// url() closes over TOKEN, which is read from this page's own address at the
// top of this file, so none of these could move into an element without taking
// the page's bootstrap with it. Each setServices refuses a set missing a name,
// so a future rename fails here rather than at the first press of a button
// three levels in.
setRunDetailServices({ url, post, log: logFor, showError, redraw: syncEntry, cancelRun });
setFileListServices({ url, post, del, log: logFor, showError, count: countValue, cancelRun });
setFileDetailServices({
  url,
  post,
  del,
  log: logFor,
  showError,
  began,
  intake,
  // Two elements this file owns the only instance of, handed over as the one
  // thing each is asked for. A file's grid opens a frame in the page's panel
  // and a file's Compare opens the page's dialog; neither is one per file.
  openFrame: (src, caption) => el.framePanel.open(src, caption),
  openCompare: (infohash, index) => el.compareDialog.open(infohash, index),
});
// EVERY HOOK IS NOW A CALL ON THE ELEMENT THAT OWNS THE THING BEING REDRAWN,
// which is what the five extraction tickets were for: the run's own hooks go
// to entry.detail, the file list's to entry.detail.files, and a file's to
// fentry.block. The hook NAMES have not changed - events.js still asks for the
// same thirteen redraws in the same order, and eventstate_test.go still
// records exactly those asks - so what moved is only who answers.
setView({
  log: logFor,
  note: log,
  status: setStatus,
  socketURL: socketAddress,
  syncEntry,
  resetRunView,
  rebuildFileList: (entry) => entry.detail.files.rebuild(),
  renderFileMeta: (fentry) => fentry.block.renderFileMeta(),
  renderFrames: (fentry) => fentry.block.renderFileGrid(),
  renderFileProgress: (fentry) => fentry.block.renderFileProgress(),
  renderFileDone: (fentry) => fentry.block.renderFileDone(),
  renderSwarm: (entry) => entry.detail.files.refreshSwarm(),
  renderTorrent: (entry) => entry.detail.renderTorrent(),
});

loadDefaults();
loadRuns();
connect();

