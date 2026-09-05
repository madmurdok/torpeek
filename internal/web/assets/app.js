// The whole UI: submit a magnet or drop a .torrent, watch the events arrive,
// watch the frames land. It reads the event stream and does nothing else -
// the run lives in the core, and a client that started deciding things would
// be a second place where the run is defined (ARCHITECTURE.md).
//
// Every URL is resolved against document.baseURI rather than written from the
// site root, so the page works unchanged under /torpeek behind a proxy.
//
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
  runsPanel: document.getElementById("runs-panel"),
  resizer: document.getElementById("resizer"),
  runList: document.getElementById("run-list"),
  runListEmpty: document.getElementById("run-list-empty"),
  sortHeaders: document.querySelectorAll("#run-table thead [data-sort]"),
  detail: document.getElementById("detail"),
  detailEmpty: document.getElementById("detail-empty"),
  log: document.getElementById("log"),
  lightbox: document.getElementById("lightbox"),
  lightboxImg: document.getElementById("lightbox-img"),
  lightboxCaption: document.getElementById("lightbox-caption"),
  lightboxClose: document.getElementById("lightbox-close"),
  compare: document.getElementById("compare"),
  compareClose: document.getElementById("compare-close"),
  compareA: document.getElementById("compare-a"),
  compareB: document.getElementById("compare-b"),
  compareStage: document.getElementById("compare-stage"),
  compareShotA: document.getElementById("compare-shot-a"),
  compareShotB: document.getElementById("compare-shot-b"),
  compareGap: document.getElementById("compare-gap"),
  compareGapCode: document.getElementById("compare-gap-code"),
  comparePrev: document.getElementById("compare-prev"),
  compareNext: document.getElementById("compare-next"),
  compareFlip: document.getElementById("compare-flip"),
  comparePlace: document.getElementById("compare-place"),
  compareTimes: document.getElementById("compare-times"),
  compareNote: document.getElementById("compare-note"),
};

// Every torrent this page knows about lives here, keyed by run id - or, for a
// run known only from disk (GET /runs found it, but this process never
// minted an id for it), by a synthetic "disk:<infohash>:<params>" key until
// it is reopened. Nothing is ever destroyed wholesale any more: a second
// torrent must not erase the first, and a finished one must stay clickable
// for as long as the page remembers it. state.selected is the one entry
// shown on the right.
// sort is the table's current order: key names the column (a <th data-sort>
// value), dir is "asc" or "desc". The default - date, newest first - is what
// the panel already showed before it became a table (TOR-62).
// watch says whether this server was started with a watch directory
// (-watch-dir), which GET /defaults answers. It gates one button and nothing
// else: without a watch directory the button is absent rather than present
// and failing, so the page has to be told before it draws one. False until
// loadDefaults answers, which is the safe way round - a button that appears a
// moment late is better than one that is there and cannot work.
const state = { runs: new Map(), selected: null, watch: false, sort: { key: "when", dir: "desc" } };

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

function shortId(id) {
  return (id || "").replace(/^disk:/, "").slice(0, 8);
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

function seconds(ms) {
  if (!ms && ms !== 0) return "";
  return (ms / 1000).toFixed(1) + "s";
}

// timecode is where a frame came from, as a person reads a video position:
// mm:ss, growing an hours field only when the film is that long. It is what
// a frame's caption says now - the frame INDEX cannot label a merged grid,
// since every result set numbers its own frames from zero, so two different
// moments would both read "#3" (TOR-69).
function timecode(ms) {
  if (!ms && ms !== 0) return "";
  const total = Math.round(ms / 1000);
  const s = String(total % 60).padStart(2, "0");
  const m = Math.floor(total / 60) % 60;
  const h = Math.floor(total / 3600);
  return h > 0 ? h + ":" + String(m).padStart(2, "0") + ":" + s : String(m).padStart(2, "0") + ":" + s;
}

function bitrateLabel(bps) {
  if (!bps) return "";
  if (bps >= 1e6) return (bps / 1e6).toFixed(1) + " Mbps";
  if (bps >= 1e3) return Math.round(bps / 1e3) + " kbps";
  return bps + " bps";
}

function bytesLabel(n) {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = n, i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i++;
  }
  return (i === 0 ? value : value.toFixed(1)) + " " + units[i];
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

// A run can still be cancelled while it is queued, running, or parked
// waiting for a file selection; every other state is final (runs.go's
// RunState.final), and "replaying" is a cache hit already well underway by
// the time this page can react to it.
//
// needs-action belongs here precisely because nothing will ever end it on
// its own: a torrent waiting for someone to tick boxes waits forever, so
// cancelling is the only way out other than deciding.
function cancellable(state) {
  return state === "queued" || state === "running" || state === "needs-action";
}

// entry.partial is GET /runs' own verdict on whether some of what was asked
// for has frames and some does not (RunSummary.Partial in listing.go),
// carried on the wire as row.partial and copied onto the entry by loadRuns -
// never recomputed here. TOR-72 first wrote this comparison twice, once in
// Go and once in a JS isPartial() that alone painted the badge; the two were
// free to drift because nothing ever called the Go copy outside its own
// tests. TOR-80 deleted the second copy: this page reads the answer, it does
// not derive it, so a change to the one surviving rule (listing.go's
// Partial()) changes what every badge shows, rather than leaving a Go test
// green while the page keeps its own opinion.
//
// A live run's badge can turn partial while the page is watching it happen,
// as of TOR-87: the run_state that announces running -> done also carries
// files/complete/selected/partial, read straight off the record this run
// just wrote to disk (server.go's pump), and apply() copies partial from it
// the same way loadRuns copies it from row.partial - never recomputing it.
// Every earlier run_state for this run (queued, running, ...) carries none
// of those fields, so entry.partial keeps whatever loadRuns' initial fetch
// gave it (false, since a run mid-flight has nothing merged in) until this
// one arrives.

// badgeState is what the badge is coloured by, which is not always the run's
// own state: a finished run whose selected files did not all come out whole
// reads "partial", and colouring that with done's green would say the
// opposite of the word inside it. Partial is not a state (cache.Run does not
// record how a run ended, and TOR-72 derives this from counts alone) - it is
// only ever a way of showing one.
function badgeState(entry) {
  if (entry.disk) return "disk";
  if (entry.state === "done" && entry.partial) return "partial";
  return entry.state;
}

function badgeLabel(entry) {
  if (entry.disk) return entry.partial ? "on disk (partial)" : "on disk";
  switch (entry.state) {
    case "queued": return "queued";
    case "running": return "running";
    case "replaying": return "reopening…";
    case "needs-action": return "choose files";
    // cache.Run does not record how a run ended, only what it covered - so
    // "done" from the registry says the run itself finished, not that every
    // selected file came out whole. entry.partial is what tells the two
    // apart, exactly as it does for a disk row above (TOR-72, wired to
    // GET /runs' own "partial" field by TOR-80).
    case "done": return entry.partial ? "partial" : "done";
    case "failed": return "failed";
    case "cancelled": return "cancelled";
    default: return entry.state || "…";
  }
}

// What the row says under its badge. The panel is the one genuinely tight
// surface in the layout, so this is deliberately terse: a finished run's
// counts are a bare ratio rather than a sentence, because "1 / 1 file(s)
// complete" was long enough to push the whole table wider than the panel and
// collapse the torrent's name to an ellipsis (TOR-121). The ratio still says
// what the badge cannot - PARTIAL tells you a run is incomplete, 3/6 tells
// you how incomplete - and metaTitle below keeps the full sentence for the
// tooltip, so nothing is actually lost.
function metaLabel(entry) {
  if (entry.progress) return entry.progress;
  if (entry.disk) return entry.complete + "/" + entry.selected;
  if (entry.error) return entry.error;
  return "";
}

// The long form, on hover, for the row whose label was shortened.
function metaTitle(entry) {
  if (entry.disk) return entry.complete + " of " + entry.selected + " file(s) complete";
  return metaLabel(entry);
}

// displayName is what a row's name cell shows, and whether that answer is
// confirmed or merely offered (TOR-117).
//
// entry.name is never invented - GET /runs and the run's own events only
// ever set it from what the torrent's own metadata, or a finished run's own
// disk record, actually said. entry.provisionalName is the one exception: a
// magnet's own dn= parameter, which anybody can put anything into, so a row
// showing it must stay visibly distinct from one showing a confirmed name
// (syncEntry marks it) rather than let a person mistake a guess for a
// verified answer. Falling all the way through to the raw source (a magnet
// URI, unreadable as it is) or the id is the same last resort this always
// had - now reached only when nothing has offered even a dn=, which after
// this ticket is the one case truly left with nothing to say.
function displayName(entry) {
  if (entry.name) return { text: entry.name, provisional: false };
  if (entry.provisionalName) return { text: entry.provisionalName, provisional: true };
  return { text: entry.source || shortId(entry.id), provisional: false };
}

// noteConfirmedName logs the moment a provisional name (a magnet's own dn=)
// is superseded by the torrent's own confirmed metadata - only when the two
// actually disagree, and only the first time a confirmed name arrives for
// this run (the entry.name guard). A dn= is never authoritative: anyone can
// put anything after it, and the metadata a session actually fetches can
// disagree with it. So the transition off a provisional name must not be a
// silent swap - a person who has been reading "Sintel" deserves to see the
// record say so explicitly if the torrent's own metadata turns out to name
// it something else, rather than watch the row's text change with nothing
// to explain why.
function noteConfirmedName(entry, confirmed) {
  if (entry.name || !entry.provisionalName || entry.provisionalName === confirmed) return;
  logFor(entry, "name confirmed as \"" + confirmed + "\" (the magnet link said \"" + entry.provisionalName + "\")");
}

function whenLabel(ms) {
  if (!ms) return "";
  const d = new Date(ms);
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) + " " +
    d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

// ---------------------------------------------------------------------------
// Table sorting. All client-side, over what state.runs already holds - GET
// /runs is small enough that a server-side sort parameter would only add a
// second place order is decided (TOR-62).

function sortValue(entry, key) {
  switch (key) {
    case "name": return displayName(entry).text.toLowerCase();
    case "status": return badgeLabel(entry).toLowerCase();
    case "when":
    default: return entry.when || 0;
  }
}

function compareEntries(a, b) {
  const { key, dir } = state.sort;
  const va = sortValue(a, key);
  const vb = sortValue(b, key);
  let cmp = typeof va === "number" ? va - vb : String(va).localeCompare(String(vb));
  if (dir === "desc") cmp = -cmp;
  return cmp;
}

// reorderRuns moves every row's existing element into sorted order without
// rebuilding anything - appendChild on a node already in the table just
// relocates it, so a row that has not moved costs a no-op reflow, never a
// rebuild. Called whenever a row is added or anything a sort key reads
// (name, status, when) changes, so the table always reflects the active
// sort - including under the default "when" sort, where a status change
// never touches when, so re-running this never moves that row.
function reorderRuns() {
  const rows = Array.from(state.runs.values()).sort(compareEntries);
  for (const entry of rows) el.runList.append(entry.rowEl);
}

function updateSortIndicators() {
  for (const th of el.sortHeaders) {
    if (th.dataset.sort === state.sort.key) {
      th.setAttribute("aria-sort", state.sort.dir === "asc" ? "ascending" : "descending");
    } else {
      th.setAttribute("aria-sort", "none");
    }
  }
}

// setSort is what clicking (or activating with the keyboard) a column header
// does: the same column reverses direction, a different one is sorted
// ascending - except "when", which starts descending (newest first), the
// same default the table opens with, since that is the more useful way to
// first look at dates.
function setSort(key) {
  if (state.sort.key === key) {
    state.sort.dir = state.sort.dir === "asc" ? "desc" : "asc";
  } else {
    state.sort.key = key;
    state.sort.dir = key === "when" ? "desc" : "asc";
  }
  updateSortIndicators();
  reorderRuns();
}

for (const th of el.sortHeaders) {
  th.addEventListener("click", () => setSort(th.dataset.sort));
  th.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    setSort(th.dataset.sort);
  });
}

// ---------------------------------------------------------------------------
// Run entries: one per torrent, live or on disk. Each owns its own row in the
// left panel and its own container on the right, built once and updated in
// place - selecting a different torrent never rebuilds anything, it only
// shows and hides what is already there.

function newRunEntry(id) {
  const row = document.createElement("tr");
  row.className = "run-row";

  const nameCell = document.createElement("td");
  nameCell.className = "run-cell-name";
  const main = document.createElement("button");
  main.type = "button";
  main.className = "run-row-main";
  const name = document.createElement("span");
  name.className = "run-name";
  main.append(name);
  nameCell.append(main);

  const whenCell = document.createElement("td");
  whenCell.className = "run-cell-when";

  const statusCell = document.createElement("td");
  statusCell.className = "run-cell-status";
  const badge = document.createElement("span");
  badge.className = "run-badge";
  const meta = document.createElement("span");
  meta.className = "run-meta";
  statusCell.append(badge, meta);

  const actionsCell = document.createElement("td");
  actionsCell.className = "run-cell-actions";
  const cancel = document.createElement("button");
  cancel.type = "button";
  cancel.className = "run-cancel";
  cancel.title = "Cancel";
  cancel.textContent = "✕";
  cancel.hidden = true;
  actionsCell.append(cancel);

  row.append(nameCell, whenCell, statusCell, actionsCell);
  el.runList.append(row);
  el.runListEmpty.hidden = true;

  const detailEl = document.createElement("div");
  detailEl.className = "run-detail";
  detailEl.hidden = true;
  detailEl.innerHTML =
    '<header class="run-detail-header">' +
      '<span class="run-badge"></span>' +
      '<h2 class="run-detail-title"></h2>' +
      '<button class="run-detail-cancel" type="button" hidden>Cancel</button>' +
    "</header>" +
    '<p class="run-detail-error" hidden></p>' +
    '<p class="torrent-summary" hidden></p>' +
    // The run's own .torrent, offered once the run has announced one (TOR-73).
    // It sits under the summary line - a property of the torrent, like the
    // summary itself - and deliberately not inside a file block: there is one
    // .torrent per run, not one per video file.
    '<p class="torrent-actions" hidden>' +
      '<a class="torrent-save" download ' +
        'title="The info dictionary is the one the swarm sent, so this file\'s infohash is the torrent\'s. ' +
        'The wrapper around it is generated: the creation date is when the file was written, and the comment ' +
        'and created-by name the BitTorrent library, not whoever published the torrent.">Save .torrent</a>' +
      '<button type="button" class="torrent-send" hidden>Send to my client</button>' +
      '<span class="torrent-note"></span>' +
    '</p>' +
    // The picker sits between the torrent's own summary line and its files:
    // the one gap in this pane, and both of its neighbours are already
    // scoped to this entry, so a second torrent's picker cannot land in it.
    '<section class="picker" hidden>' +
      '<p class="picker-head">' +
        '<span class="picker-title"></span>' +
        '<button type="button" class="picker-all">Select all</button>' +
        '<button type="button" class="picker-none">Select none</button>' +
      '</p>' +
      '<ul class="picker-list"></ul>' +
      '<p class="picker-foot">' +
        '<button type="button" class="picker-go">Take frames</button>' +
        '<span class="picker-cost"></span>' +
      '</p>' +
    '</section>' +
    '<section class="files"></section>';
  el.detail.append(detailEl);

  const entry = {
    id, disk: false, infohash: "", params: "",
    state: "", source: "", name: "",
    // provisionalName is a magnet's own dn=, offered only while name is
    // still empty (TOR-117) - see displayName for how the two are chosen
    // between, and noteConfirmedName for how the switch off this one is
    // announced rather than left silent.
    provisionalName: "",
    error: "", progress: "",
    files: 0, complete: 0, selected: 0,
    // partial is GET /runs' own "partial" field (RunSummary.Partial in
    // listing.go, TOR-80) - false here for the same reason files/complete/
    // selected start at zero: nothing has merged a disk record into this
    // entry yet, and loadRuns is the only place that changes.
    partial: false,
    // when is this row's sort key for the default (date, newest-first) sort.
    // Set once, here, at creation - never touched again by a status update -
    // which is what keeps a live run from jumping position as events arrive.
    // loadRuns() overwrites it once with the authoritative value GET /runs
    // reports, for a row it already knows about at page load.
    when: Date.now(),
    reopening: false,
    fileEntries: new Map(),
    // videos is the file list a needs_action record brought, and picked the
    // indices ticked in it. Both are empty for every torrent that never
    // parked - a single-file one, or any run started with a selection
    // already in it (Regenerate).
    videos: [],
    picked: new Set(),
    // Set once this run's first file block is built, so every file after it
    // defaults to collapsed - only the first one earns the auto-expand.
    autoExpanded: false,
    // torrentURL is the files/{id} handle the run's own done event announced
    // for its saved .torrent, empty for a run that has none to offer.
    torrentURL: "",
    rowEl: row, rowBadge: badge, rowName: name, rowMeta: meta,
    rowWhen: whenCell, rowCancel: cancel,
    detailEl,
    detailBadge: detailEl.querySelector(".run-detail-header .run-badge"),
    detailTitle: detailEl.querySelector(".run-detail-title"),
    detailCancel: detailEl.querySelector(".run-detail-cancel"),
    detailError: detailEl.querySelector(".run-detail-error"),
    torrentSummary: detailEl.querySelector(".torrent-summary"),
    torrentActions: detailEl.querySelector(".torrent-actions"),
    torrentSave: detailEl.querySelector(".torrent-save"),
    torrentSend: detailEl.querySelector(".torrent-send"),
    torrentNote: detailEl.querySelector(".torrent-note"),
    pickerEl: detailEl.querySelector(".picker"),
    pickerTitle: detailEl.querySelector(".picker-title"),
    pickerList: detailEl.querySelector(".picker-list"),
    pickerAll: detailEl.querySelector(".picker-all"),
    pickerNone: detailEl.querySelector(".picker-none"),
    pickerGo: detailEl.querySelector(".picker-go"),
    pickerCost: detailEl.querySelector(".picker-cost"),
    filesEl: detailEl.querySelector(".files"),
  };

  // One listener on the row, not the name button alone: a click anywhere in
  // the row selects it (a table row is a natural click target), and a
  // keyboard activation of the name button still reaches it too, since a
  // button's click event bubbles the same way a mouse click does. The cancel
  // button stops its own click from bubbling here, so a cancel never also
  // selects the row it sits in.
  row.addEventListener("click", () => selectOrReopen(entry));
  cancel.addEventListener("click", (event) => {
    event.stopPropagation();
    cancelRun(entry.id);
  });
  entry.detailCancel.addEventListener("click", () => cancelRun(entry.id));

  entry.pickerAll.addEventListener("click", () => setAllPicked(entry, true));
  entry.pickerNone.addEventListener("click", () => setAllPicked(entry, false));
  entry.pickerGo.addEventListener("click", () => decide(entry));
  entry.torrentSend.addEventListener("click", () => sendTorrent(entry));

  return entry;
}

// ensureRun finds a torrent by key, creating it - at the top of the list,
// since a key that does not exist yet is always something just starting -
// the first time it is needed.
function ensureRun(id) {
  let entry = state.runs.get(id);
  if (!entry) {
    entry = newRunEntry(id);
    state.runs.set(id, entry);
  }
  return entry;
}

// resetRunContent clears one run's files and frames without touching its row
// or detail container - what a run_state "reset" means now: this run's own
// history is starting over (a fresh start, or a reconnecting page about to
// replay it from the beginning), not "every torrent on the page is gone".
function resetRunContent(entry) {
  entry.filesEl.replaceChildren();
  entry.fileEntries.clear();
  entry.autoExpanded = false;
  entry.torrentSummary.hidden = true;
  entry.torrentSummary.textContent = "";
  entry.error = "";
  // The picker goes with everything else this run has shown. It comes back
  // from the replayed needs_action that follows in the same history, so a
  // reconnecting page rebuilds it rather than keeping a stale copy of a list
  // the server may since have moved past.
  entry.videos = [];
  entry.picked.clear();
  entry.pickerList.replaceChildren();
  entry.pickerEl.hidden = true;
  // The .torrent link goes with the rest of what this run has shown. It comes
  // back from the done event the replayed history ends on, so a reconnecting
  // page rebuilds it rather than keeping a handle the server may no longer
  // resolve.
  showTorrent(entry, "");
}

function syncEntry(entry) {
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
  // app.css, not duplicated here.
  entry.rowName.classList.toggle("run-name-provisional", shown.provisional);
  entry.rowMeta.textContent = metaLabel(entry);
  entry.rowMeta.title = metaTitle(entry);
  entry.rowWhen.textContent = whenLabel(entry.when);
  entry.rowWhen.title = entry.when ? new Date(entry.when).toString() : "";
  entry.rowCancel.hidden = entry.disk || !cancellable(entry.state);
  reorderRuns();

  entry.detailBadge.textContent = badgeLabel(entry);
  entry.detailBadge.dataset.state = badgeState(entry);
  entry.detailTitle.textContent = shown.text;
  entry.detailTitle.classList.toggle("run-name-provisional", shown.provisional);
  entry.detailCancel.hidden = entry.disk || !cancellable(entry.state);
  entry.detailError.hidden = !entry.error;
  entry.detailError.textContent = entry.error || "";
  // One rule for whether the picker is on screen, and it is the run's own
  // state: the needs_action record arrives just before the run_state that
  // announces the parking, and the run_state that ends the parking (queued,
  // or cancelled) is what takes it away again.
  entry.pickerEl.hidden = !(entry.state === "needs-action" && entry.videos.length > 0);
}

function selectRun(id) {
  if (state.selected === id) return;
  const previous = state.runs.get(state.selected);
  if (previous) previous.rowEl.classList.remove("selected");
  state.selected = id;
  const entry = state.runs.get(id);
  el.detailEmpty.hidden = !!entry;
  for (const e of state.runs.values()) e.detailEl.hidden = e !== entry;
  if (entry) entry.rowEl.classList.add("selected");
}

// claimReopenedRun trades a disk entry's synthetic key for the real run id
// the server minted for it, keeping its DOM - its row, its detail container,
// its place in the list - exactly as it was, so the run_state and events
// that follow for that id land on this same entry instead of spawning a
// duplicate row.
//
// It is idempotent by design: the reopen response and the socket message
// that announces the same new run race each other (server.go's ReopenRun
// runs the whole replay, publishing every event, before the HTTP handler
// even writes its response - see resolveIncomingRun), and either one can
// arrive first. Whichever gets here first does the swap; the other finds
// entry.id already equal to newId and does nothing.
function claimReopenedRun(entry, newId) {
  if (entry.id !== newId) {
    const oldKey = entry.id;
    state.runs.delete(oldKey);
    entry.id = newId;
    entry.disk = false;
    entry.reopening = false;
    state.runs.set(entry.id, entry);
    if (state.selected === oldKey) state.selected = entry.id;
    syncEntry(entry);
  }
}

// resolveIncomingRun is ensureRun's counterpart for a run_state message: a
// brand new id might not be a brand new torrent. If some disk entry is
// mid-reopen and shares this message's infohash, this id is what that
// reopen minted, and claimReopenedRun folds the message into that entry
// instead of ensureRun spawning a second row for the same torrent. Matching
// on infohash alone cannot tell apart two different plans of the very same
// torrent (TOR-54) both being reopened at once - a rare case this picks the
// first match for, rather than handling.
function resolveIncomingRun(id, infohash) {
  const existing = state.runs.get(id);
  if (existing) return existing;

  if (infohash) {
    for (const candidate of state.runs.values()) {
      if (candidate.reopening && candidate.infohash === infohash) {
        claimReopenedRun(candidate, id);
        return candidate;
      }
    }
  }

  return ensureRun(id);
}

// selectOrReopen is what clicking a row does. A live or already-live-again
// entry just needs showing - its files and frames, if any exist yet, are
// already sitting in its own container. A disk-only entry has nothing to
// show until it is replayed (TOR-55): reopening asks the server to read it
// back from disk under a fresh id, and that id's own run_state/file/frame
// events - arriving over the socket this page already holds open - fill the
// same container in, moments later.
function selectOrReopen(entry) {
  if (!entry.disk) {
    selectRun(entry.id);
    return;
  }
  if (entry.reopening) return;
  entry.reopening = true;
  entry.state = "replaying";
  syncEntry(entry);
  selectRun(entry.id);

  post("runs/reopen", { infohash: entry.infohash, params: entry.params })
    .then((info) => {
      claimReopenedRun(entry, info.id);
      selectRun(entry.id);
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

// ---------------------------------------------------------------------------
// The picker: what a multi-file torrent shows instead of starting (TOR-67).
// The server has fetched the metadata, given the queue slot back and parked
// the torrent; nothing at all happens until someone says which files to
// capture, so this is the whole of the run's UI while it waits.

function basename(path) {
  const parts = String(path || "").split("/");
  return parts[parts.length - 1] || path || "";
}

// renderPicker builds the tick list from a needs_action record.
//
// Nothing is ticked to begin with, and "Take frames" stays disabled until
// something is. A torrent reaches this screen precisely because it holds
// several video files, and pre-ticking them all would put the most expensive
// possible run one careless click away - the count is per file, so six
// variants at 20 frames is 120 captures (TOR-50). Select all is right there
// for the person who does mean all of it.
function renderPicker(entry, ev) {
  entry.videos = ev.videos || [];
  entry.picked = new Set();
  entry.pickerTitle.textContent =
    entry.videos.length + " video file(s) — pick what to capture";

  entry.pickerList.replaceChildren(...entry.videos.map((video) => {
    const item = document.createElement("li");
    const label = document.createElement("label");
    label.className = "picker-file";

    const box = document.createElement("input");
    box.type = "checkbox";
    box.value = String(video.index);
    box.addEventListener("change", () => {
      if (box.checked) entry.picked.add(video.index);
      else entry.picked.delete(video.index);
      updatePickerCost(entry);
    });

    const name = document.createElement("span");
    name.className = "picker-name";
    name.textContent = basename(video.path);
    name.title = video.path;

    const size = document.createElement("span");
    size.className = "picker-size";
    size.textContent = bytesLabel(video.length);

    label.append(box, name, size);
    item.append(label);
    return item;
  }));

  updatePickerCost(entry);
}

function setAllPicked(entry, picked) {
  entry.picked = new Set(picked ? entry.videos.map((video) => video.index) : []);
  for (const box of entry.pickerList.querySelectorAll("input[type=checkbox]")) {
    box.checked = picked;
  }
  updatePickerCost(entry);
}

// updatePickerCost shows what pressing the button would cost, before it is
// spent rather than after.
//
// This is the one place in the whole program where that number can be shown
// in advance: -n is frames PER video file (TOR-50), so a bundle of quality
// variants multiplies it, and every other screen only ever reports the
// traffic once it is gone. It follows both inputs live - the ticks here and
// the count on the intake line, which a person may well adjust while looking
// at this list.
function updatePickerCost(entry) {
  const files = entry.picked.size;
  const n = countValue();
  entry.pickerGo.disabled = files === 0;
  if (files === 0) {
    entry.pickerCost.textContent = "nothing picked yet";
    return;
  }
  entry.pickerCost.textContent = n
    ? files + " file(s) × " + n + " = " + files * n + " frames"
    : files + " file(s)";
}

// decide sends the selection and lets the torrent out of its parked state.
//
// The answer is only an acknowledgement: what actually moves this row from
// "choose files" to "queued" (and takes the picker off the screen) is the
// run_state that arrives on the socket, the same way every other change to a
// run reaches this page.
async function decide(entry) {
  showError("");
  const files = [...entry.picked].sort((a, b) => a - b).map(String);
  if (files.length === 0) return;

  entry.pickerGo.disabled = true;
  try {
    await post("runs/decide", { id: entry.id, files, count: countValue() });
    logFor(entry, "capturing " + files.length + " of " + entry.videos.length + " file(s)");
  } catch (err) {
    showError(String(err.message || err));
    entry.pickerGo.disabled = false;
  }
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

// fileBlock returns the section for one file of one torrent, building it the
// first time it is needed. Each video file gets its own summary panel and
// its own frame grid, since a multi-file torrent should not mix their frames
// or their tracks in one place - and one torrent's files must never mix with
// another's now that the page can hold several at once.
//
// Everything below the title - specs, tracks, links, progress, the frame
// grid - is built and filled in exactly as before, whether or not the file
// is expanded; only file-body's `hidden` attribute decides what is on
// screen. A frame_ready for a collapsed file still appends its figure to
// .grid (addFrame never checks expanded state), so expanding it later shows
// everything that arrived while it was closed - nothing is built lazily,
// there is nothing to replay.
//
// Only the first file built for a run is auto-expanded (entry.autoExpanded
// latches on the first call and never resets except on a full
// resetRunContent). Every file after that starts collapsed, and a file's
// expanded state changes from then on only in response to its own toggle
// button - never from a later file_started/frame_ready/progress event - so
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
function fileBlock(entry, index) {
  let fentry = entry.fileEntries.get(index);
  if (fentry) return fentry;

  const article = document.createElement("article");
  article.className = "file";
  article.innerHTML =
    '<h2 class="file-title">' +
      '<button type="button" class="file-toggle" aria-expanded="false">' +
        '<span class="file-toggle-icon" aria-hidden="true"></span>' +
        '<span class="file-name"></span>' +
        '<span class="file-summary"></span>' +
      "</button>" +
    "</h2>" +
    '<div class="file-body">' +
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
      '<div class="grid"></div>' +
    "</div>";
  entry.filesEl.append(article);

  fentry = {
    article,
    toggle: article.querySelector(".file-toggle"),
    name: article.querySelector(".file-name"),
    summary: article.querySelector(".file-summary"),
    body: article.querySelector(".file-body"),
    metaToggle: article.querySelector(".meta-toggle"),
    metaBody: article.querySelector(".meta-body"),
    specs: article.querySelector(".specs"),
    tracks: article.querySelector(".tracks"),
    links: article.querySelector(".file-links"),
    regenCount: article.querySelector(".file-regen-count"),
    regenGo: article.querySelector(".file-regen-go"),
    compareGo: article.querySelector(".file-compare"),
    progress: article.querySelector(".file-progress"),
    grid: article.querySelector(".grid"),
    expanded: false,
    metaExpanded: false,
    // frames is this file's grid, keyed by timecode in milliseconds. The key
    // is what merges the result sets: frames.Plan pins the first and last
    // point to the window, so every regeneration lands on the first set's
    // edges exactly, and one moment must be one thumbnail however many sets
    // hold it (TOR-69). It is also why the grid is rendered from state
    // rather than appended to: a set fetched after the fact arrives in no
    // particular order relative to what is already there.
    frames: new Map(),
    // plan is the capture points this file's run is going to attempt, in
    // milliseconds, straight off file_started (TOR-110). While it is set the
    // grid is laid out FROM it - one cell per planned point, at final size,
    // before any piece is fetched - so the grid stops shoving itself around
    // for the minute a run spends waiting.
    //
    // It is emptied once the file's frames have been read back from disk,
    // and that hand-off is deliberate rather than tidy-up. A plan describes
    // one run; the disk holds every result set this torrent has for the
    // file, and the sets merge by timecode into cells no single plan
    // accounts for (see frames above). Once nothing is arriving there is
    // also nothing left to reflow, so the truthful view costs nothing.
    plan: [],
    // skipped is what the engine reported as unreachable, by plan index -
    // frame_skipped's code and reason. Without it a point that failed live
    // leaves its reserved cell looking like a frame still on its way, which
    // is the one thing the reserved cell must not do.
    skipped: new Map(),
    detailLoaded: false,
    width: 0,
    height: 0,
    // index and entry are how a frame addresses an action on itself: a
    // delete names the torrent (entry.infohash), the file, the result set
    // and the frame's own number in that set's manifest. Everything else on
    // this page is driven by events arriving with a run already in hand;
    // this is the one thing a click on a thumbnail has to look up.
    index,
    entry,
  };
  entry.fileEntries.set(index, fentry);

  fentry.regenCount.value = el.count.value;
  fentry.regenGo.addEventListener("click", () => regenerate(entry, index, fentry));
  fentry.compareGo.addEventListener("click", () => openCompare(entry.infohash, index));

  fentry.toggle.addEventListener("click", () => {
    const expanded = !fentry.expanded;
    setFileExpanded(fentry, expanded);
    // Opening a file is when it is worth reading the other result sets off
    // disk - not on every run_state, and not for a file nobody looked at.
    // Once is enough: a set only gains frames by a run finishing, which
    // asks again itself (onFileDone).
    if (expanded && !fentry.detailLoaded) loadFileDetail(entry, fentry, index);
  });
  setFileExpanded(fentry, !entry.autoExpanded);
  entry.autoExpanded = true;
  updateFileSummary(fentry);

  fentry.metaToggle.addEventListener("click", () => {
    setMetaExpanded(fentry, !fentry.metaExpanded);
  });
  setMetaExpanded(fentry, false);

  return fentry;
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
async function regenerate(entry, index, fentry) {
  showError("");
  if (!entry.source) {
    showError("this torrent has no source to repeat");
    return;
  }

  const n = parseInt(fentry.regenCount.value, 10);
  const count = Number.isInteger(n) && n > 0 ? n : countValue();

  fentry.regenGo.disabled = true;
  try {
    const info = await post("runs", {
      source: entry.source,
      mode: el.mode.value,
      // swarm.Select already takes a torrent index as a spec, so the index
      // the events arrived under is the whole selection.
      files: [String(index)],
      count,
    });
    began(info);
  } catch (err) {
    showError(String(err.message || err));
  } finally {
    fentry.regenGo.disabled = false;
  }
}

function setFileExpanded(fentry, expanded) {
  fentry.expanded = expanded;
  fentry.article.dataset.expanded = String(expanded);
  fentry.toggle.setAttribute("aria-expanded", String(expanded));
  fentry.body.hidden = !expanded;
  // The collapsed-only summary line and the specs panel say the same thing
  // two different ways; showing both at once would just repeat resolution.
  fentry.summary.hidden = expanded;
}

// setMetaExpanded is the file-level setFileExpanded's counterpart one level
// down (TOR-71): the single function that may change whether a file's
// metadata (.specs and .tracks) is on screen, called from exactly two
// places - the metadata toggle's own click handler, and once at creation to
// start every file's metadata collapsed, with no auto-expand exception. No
// event that rebuilds .specs/.tracks in place (onFileStarted) touches
// fentry.metaExpanded, so a person who opens the metadata keeps it open
// through every event that follows.
function setMetaExpanded(fentry, expanded) {
  fentry.metaExpanded = expanded;
  fentry.metaToggle.setAttribute("aria-expanded", String(expanded));
  fentry.metaBody.hidden = !expanded;
}

// updateFileSummary keeps a collapsed row worth choosing by without opening
// it: the file name is always visible on the toggle itself, and this adds
// whatever of resolution and frame count are already known - both update
// live (resolution the moment file_started arrives, the frame count on
// every frame_ready) whether or not the file happens to be expanded right
// now.
// updateFileSummary says how many frames the file has, and against what it
// was trying for when the two differ.
//
// It counts frames rather than CELLS, which is a distinction the grid only
// acquired once a point that produced nothing started being listed at all
// (TOR-118): fentry.frames holds an entry for every planned point a finished
// run recorded, failures included, so its plain size would report a holed run
// of five frames as twelve. A count that is really a plan pretending to be a
// result is the kind of quiet lie this project keeps finding.
function updateFileSummary(fentry) {
  const parts = [];
  if (fentry.width && fentry.height) parts.push(fentry.width + "×" + fentry.height);

  const cells = gridCells(fentry);
  const captured = cells.filter((cell) => cell.url).length;
  const planned = fentry.plan.length || cells.length;

  if (planned > captured) {
    parts.push(captured + " of " + planned + " frames");
  } else {
    parts.push(captured === 1 ? "1 frame" : captured + " frames");
  }
  fentry.summary.textContent = parts.join(" · ");
}

// The summary panel: audio tracks, subtitles, bitrate, resolution, filled the
// moment the file's media is known - before a single frame exists.
function onFileStarted(entry, ev) {
  const fentry = fileBlock(entry, ev.file);
  fentry.name.textContent = ev.path;
  fentry.width = ev.width;
  fentry.height = ev.height;

  // The whole grid, at final size, before the first piece is fetched.
  fentry.plan = Array.isArray(ev.plan) ? ev.plan : [];
  fentry.skipped.clear();
  renderFrames(fentry);
  updateFileSummary(fentry);

  fentry.specs.replaceChildren();
  addSpec(fentry.specs, "Resolution", ev.width && ev.height ? ev.width + "×" + ev.height : "");
  addSpec(fentry.specs, "Video",
    [ev.codec, ev.profile, ev.fps ? ev.fps.toFixed(2) + " fps" : "", bitrateLabel(ev.video_bitrate)]
      .filter(Boolean).join(" · "));
  addSpec(fentry.specs, "Overall bitrate", bitrateLabel(ev.bitrate));
  addSpec(fentry.specs, "Duration", seconds(ev.duration_ms));

  fentry.tracks.replaceChildren(
    trackGroup("Audio", ev.audio || [], audioLine),
    trackGroup("Subtitles", ev.subtitles || [], subtitleLine),
  );

  logFor(entry, "file " + ev.file + ": " + ev.path + " — " + seconds(ev.duration_ms) +
      ", " + ev.width + "x" + ev.height + " " + ev.codec + ", " + ev.planned + " points");
}

// addFrame records one frame the event stream just announced and re-renders
// the file's grid. It records rather than appends: two result sets of the
// same file share timecodes, and the grid is one list ordered by time.
function addFrame(entry, ev) {
  const fentry = fileBlock(entry, ev.file);
  const at = ev.actual_ms;

  // No params: a frame straight off the event stream has no result set to
  // address until its manifest exists, so it carries no cross yet. The
  // file_done that follows re-reads every frame from disk moments later
  // (loadFileDetail) and replaces this one with an addressable version.
  fentry.frames.set(at, { url: url(ev.url), timeMs: at, shift: ev.shift || "", params: "", index: ev.index });
  renderFrames(fentry);
  updateFileSummary(fentry);

  logFor(entry, "frame " + ev.index + " at " + seconds(at) + (ev.shift ? " (" + ev.shift + ")" : ""));
}

// renderFrames rebuilds the grid from what the file is known to have.
//
// Rebuilding the whole grid rather than inserting into it keeps one rule -
// the DOM is the map - instead of two: a live run appends in plan order and
// would look sorted either way, while a set fetched from disk arrives all at
// once and interleaves with what is already shown.
function renderFrames(fentry) {
  fentry.grid.replaceChildren(...gridCells(fentry).map((cell) => frameFigure(cell, fentry)));
}

// gridCells is the grid as a list of cells, each carrying the state it should
// be drawn in. There are two ways to build it and which one applies is the
// difference between a run in flight and a run that is over.
//
// FROM THE PLAN, while fentry.plan is set. One cell per capture point the
// engine said it would attempt, in plan order, whether or not anything has
// arrived for it yet - which is the point: every cell exists at final size
// before the first piece is fetched, so nothing reflows during the minute a
// run spends waiting. A frame claims its cell by INDEX rather than by
// timecode, because a shifted frame lands somewhere other than where it was
// asked for and still belongs to the point that asked.
//
// FROM THE DISK, once the plan has been handed over. Ordered by time and
// keyed by it, so the result sets merge exactly as fentry.frames does - a
// view no single plan can describe, and one nothing is arriving into, so it
// has no reflow to avoid.
function gridCells(fentry) {
  if (fentry.plan.length) {
    const byIndex = new Map();
    for (const frame of fentry.frames.values()) {
      if (Number.isInteger(frame.index)) byIndex.set(frame.index, frame);
    }
    return fentry.plan.map((plannedMs, index) => {
      const frame = byIndex.get(index);
      if (frame) return { ...frame, plannedMs, state: frame.shift ? "shifted" : "exact" };

      const skip = fentry.skipped.get(index);
      if (skip) {
        return {
          state: "failed", timeMs: plannedMs, plannedMs, index,
          url: null, shift: "", error: skip.code, reason: skip.reason, params: "",
        };
      }
      return {
        state: "pending", timeMs: plannedMs, plannedMs, index,
        url: null, shift: "", error: "", params: "",
      };
    });
  }

  return [...fentry.frames.values()]
    .sort((a, b) => a.timeMs - b.timeMs)
    .map((frame) => ({ ...frame, state: frameState(frame) }));
}

// frameState reads a disk-shaped frame's own fields. A point that produced
// nothing has no URL and carries the engine's reason instead (TOR-118); one
// that produced a frame somewhere other than where it was asked says so in
// shift; everything else is exactly what was planned, and gets no marking at
// all, because most cells are this one and a grid that marks every cell marks
// none of them.
function frameState(frame) {
  if (!frame.url) return "failed";
  return frame.shift ? "shifted" : "exact";
}

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
function frameFigure(frame, fentry) {
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
    img.src = frame.url;
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
  if (frame.shift) {
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
  // addFrame); the existence is the rest - a failed point has no file to
  // delete, and offering the cross on one would be offering an action that
  // cannot succeed.
  if (fentry && frame.params && frame.url) {
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "thumb-delete";
    remove.textContent = "×";
    const what = "Delete the frame at " + timecode(frame.timeMs);
    remove.title = what;
    remove.setAttribute("aria-label", what);
    remove.addEventListener("click", (event) => {
      event.stopPropagation();
      deleteFrame(fentry, frame);
    });
    figure.append(remove);
  }

  // Nothing to open full-size for a cell with no frame in it - there is only
  // the box standing in for one.
  const open = () => { if (frame.url) openLightbox(img.src, caption.textContent); };
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

// deleteFrame removes one frame from disk - its manifest record and its file
// both, which is what keeps the rest of the run openable (core.DeleteFrame).
//
// There is no confirmation step: the ticket asks for a cross, the panel's own
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
async function deleteFrame(fentry, frame) {
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
    renderFrames(fentry);
    updateFileSummary(fentry);
    logFor(fentry.entry, "deleted the frame at " + seconds(frame.timeMs) + " of file " + fentry.index);
  } catch (err) {
    showError(String(err.message || err));
  }
}

// detailFrame is one frame of a file-detail response in the shape the grid
// keeps them in. Both readers of that response - the merge on opening a file
// and the replacement after a delete - go through here, so a frame is never
// half-addressable in one of them and whole in the other.
function detailFrame(f) {
  return {
    // f.url is empty for a failed point (TOR-118) - nothing to resolve into
    // a fetchable URL, and url("") would resolve to this very page.
    url: f.url ? url(f.url) : null, timeMs: f.time_ms, shift: f.shift || "",
    params: f.params || "", index: f.index, error: f.error || "",
  };
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
async function loadFileDetail(entry, fentry, index) {
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
    renderFrames(fentry);
    updateFileSummary(fentry);
  } catch (err) {
    logFor(entry, "could not read file " + index + "'s frames: " + (err.message || err));
  }
}

function openLightbox(src, caption) {
  el.lightboxImg.src = src;
  el.lightboxCaption.textContent = caption;
  el.lightbox.showModal();
}

el.lightboxClose.addEventListener("click", () => el.lightbox.close());
el.lightbox.addEventListener("click", (event) => {
  // A click that lands on the dialog element itself, rather than anything
  // inside it, is a click on the backdrop.
  if (event.target === el.lightbox) el.lightbox.close();
});

// ---------------------------------------------------------------------------
// COMPARING TWO RUNS BY FLIPPING (TOR-109).
//
// FLIPPING BEATS TILING, and that is the whole design rather than a
// preference: the same frame, in the same screen position, one keypress
// apart, makes a difference visible that a side-by-side grid hides, because
// the eye compares against its own afterimage rather than across a gap. So
// this is not two grids next to each other, and everything below exists to
// keep one promise - THE PICTURE DOES NOT MOVE.
//
// What that costs, concretely:
//   - the stage's shape is decided from the arms' own resolution before
//     either frame loads (app.css's --compare-aspect), never from whichever
//     image happened to arrive first;
//   - both arms' frames are given a src at the same moment, so a flip is a
//     visibility toggle over already-decoded pixels rather than a fetch;
//   - a src is only ever re-assigned when it actually changes, so flipping
//     back and forth never re-decodes anything;
//   - nothing above the picture, and nothing sized, changes on a flip. The
//     dialog is centred by the browser, so a single wrapped line below the
//     stage would re-centre the dialog and slide the picture.
//
// The server does the pairing (internal/web/compare.go). That is not
// plumbing: "the same frame" across two encodes of one film means the nearest
// capture point BY FRACTION OF DURATION, the durations live in the manifests,
// and a page deriving that itself would be a second place the rule is
// written.

const compare = {
  // sets is every result set on disk, from GET /compare/sets - the picker's
  // options, refreshed each time the dialog opens so a run that finished in
  // the meantime is offered.
  sets: [],
  // a and b are the two arms' addresses ("infohash:params:index"), built by
  // the server and never spelled here.
  a: "", b: "",
  // data is the comparison itself; at is which position is on screen, and
  // live which arm. Those last two are the entire flipbook's state.
  data: null,
  at: 0,
  live: "a",
};

// compareSetLabel is how one result set reads in the picker. The params key
// is included in full and last: two sets of the same file of the same torrent
// differ in nothing a person can see except their frame counts, and two runs
// at the SAME count (a different profile, say) do not differ in that either -
// the key is the only thing that always tells them apart.
function compareSetLabel(set) {
  const parts = [set.name || shortId(set.infohash), basename(set.path)];
  parts.push(set.frames === set.points
    ? set.points + " frames"
    : set.frames + " of " + set.points + " frames");
  if (set.duration_ms) parts.push(timecode(set.duration_ms));
  parts.push(set.params);
  return parts.join(" · ");
}

function fillComparePickers() {
  for (const picker of [el.compareA, el.compareB]) {
    picker.replaceChildren(...compare.sets.map((set) => {
      const option = document.createElement("option");
      option.value = set.addr;
      option.textContent = compareSetLabel(set);
      return option;
    }));
    picker.disabled = compare.sets.length === 0;
  }
}

// chooseArms picks what the dialog opens on, given the file it was opened
// from. The two result sets of THAT file come first when there are two -
// which is the pairing a person pressing Compare on a file they just
// regenerated is asking for - and otherwise it falls back to that file
// against whatever else is on disk, which is the cross-torrent case. Both are
// only a default: the two pickers are then free.
function chooseArms(infohash, index) {
  const mine = compare.sets.filter((set) => set.infohash === infohash && set.index === index);
  const first = mine[0] || compare.sets[0];
  if (!first) return ["", ""];
  const second = mine[1] || compare.sets.find((set) => set.addr !== first.addr);
  return [first.addr, second ? second.addr : ""];
}

async function openCompare(infohash, index) {
  try {
    const response = await fetch(url("compare/sets"));
    if (!response.ok) throw new Error(response.statusText);
    compare.sets = (await response.json()).sets || [];
  } catch (err) {
    compare.sets = [];
    log("could not read what there is to compare: " + (err.message || err));
  }

  fillComparePickers();
  const [a, b] = chooseArms(infohash, index);
  compare.a = a;
  compare.b = b;
  el.compareA.value = a;
  el.compareB.value = b;

  if (!el.compare.open) el.compare.showModal();
  // Focused straight away, so the keys work without a person having to find
  // something to click first - the feature is one keypress.
  el.compareStage.focus();
  await loadComparison();
}

async function loadComparison() {
  compare.data = null;
  compare.at = 0;
  compare.live = "a";

  if (!compare.a || !compare.b) {
    renderComparison("There is only one result set on disk so far - " +
      "regenerate a file at a different frame count, or capture another torrent, " +
      "and there will be something to flip against.");
    return;
  }
  if (compare.a === compare.b) {
    renderComparison("Both pickers name the same result set; pick a different one for the second.");
    return;
  }

  const target = url("compare");
  target.searchParams.set("a", compare.a);
  target.searchParams.set("b", compare.b);
  try {
    const response = await fetch(target);
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || response.statusText);
    compare.data = body.comparison || null;
  } catch (err) {
    renderComparison(String(err.message || err));
    return;
  }
  renderComparison("");
}

// comparisonNote is what the pairing left out, said out loud rather than
// silently dropped. A set of 8 flipped against a set of 20 has twelve points
// that are simply not in the flipbook - pairing them with whatever happened
// to be nearest would put a difference on screen that the film caused rather
// than the encode - and a person who counted twenty frames in the grid needs
// to be told that, not left to wonder where they went.
function comparisonNote(data) {
  if (!data.positions || data.positions.length === 0) {
    return "These two sets share no capture point near enough to pair, so there is " +
      "nothing here that is the same moment of the same film.";
  }
  const parts = [];
  const orphans = [];
  if (data.a.unpaired) orphans.push(data.a.unpaired + " of set 1's " + data.a.points);
  if (data.b.unpaired) orphans.push(data.b.unpaired + " of set 2's " + data.b.points);
  if (orphans.length) {
    parts.push(orphans.join(" and ") + " capture points have no partner in the other set, " +
      "so they are not positions here");
  }
  if (data.basis === "time") {
    parts.push("paired by timecode rather than by fraction of duration: one of these files " +
      "never said how long it is");
  }
  return parts.join(" · ");
}

// aspectOf is the stage's shape, taken from an arm's own resolution. It is
// set once per comparison and never per position: every frame of one encode
// has the same resolution, and a stage that re-shaped itself as the pictures
// arrived would move the picture, which is the one thing this must not do.
function aspectOf(arm) {
  // A bare number, not "w / h": app.css also multiplies this inside a calc()
  // to bound the stage's height without breaking its shape, and only a number
  // can be multiplied. aspect-ratio takes either form.
  return arm && arm.width > 0 && arm.height > 0 ? String(arm.width / arm.height) : "";
}

function renderComparison(message) {
  const data = compare.data;
  el.compareNote.textContent = message || (data ? comparisonNote(data) : "");
  el.compareNote.title = el.compareNote.textContent;

  const positions = (data && data.positions) || [];
  el.compareStage.style.setProperty("--compare-aspect",
    (data && (aspectOf(data.a) || aspectOf(data.b))) || "1.7778");

  if (positions.length === 0) {
    el.compareShotA.removeAttribute("src");
    el.compareShotB.removeAttribute("src");
    el.compareStage.dataset.live = "a";
    el.compareStage.dataset.gap = "false";
    el.comparePlace.textContent = "0 / 0";
    el.compareTimes.replaceChildren();
    el.comparePrev.disabled = true;
    el.compareNext.disabled = true;
    el.compareFlip.disabled = true;
    markLiveArm();
    return;
  }

  el.comparePrev.disabled = false;
  el.compareNext.disabled = false;
  el.compareFlip.disabled = false;
  renderComparePosition();
}

// setCompareShot gives one arm its picture. Both arms are set for every
// position, whichever is live, so the flip that follows is a visibility
// toggle over pixels the browser has already decoded.
function setCompareShot(img, frame, label) {
  if (!frame.url) {
    // Nothing was captured here. An empty src would ask the browser to fetch
    // this very page, so the attribute goes away entirely and .compare-gap
    // says what happened instead.
    img.removeAttribute("src");
    img.alt = "";
    return;
  }
  const href = String(url(frame.url));
  if (img.getAttribute("src") !== href) img.src = href;
  img.alt = label + " at " + timecode(frame.time_ms);
}

// armTime is one arm's entry in the bar: which set, where its frame came
// from, and - for a point that produced nothing - the engine's own reason.
//
// The shift is shown only for a frame that EXISTS, and that is TOR-118's trap
// avoided rather than a tidy-up. manifest.ShiftFailed serialises to the word
// "unavailable", which is also one of the two failure CODES, so a failed
// point rendered with both reads "01:23 unavailable — no frame" and invites
// exactly the wrong conclusion: that the shift is the reason. On a point with
// no frame the shift says only THAT it was lost; frame.error is what says
// what lost it, and it is the one worth the space.
function armTime(label, frame, live) {
  const span = document.createElement("span");
  if (live) span.className = "compare-live";
  span.textContent = label + " " + timecode(frame.time_ms) + (frame.url
    ? (frame.shift ? " " + frame.shift : "")
    : " — " + (frame.error || "no frame"));
  return span;
}

function markLiveArm() {
  for (const label of el.compare.querySelectorAll(".compare-arm")) {
    label.dataset.live = String(label.dataset.arm === compare.live);
  }
}

function renderComparePosition() {
  const data = compare.data;
  const position = data.positions[compare.at];

  setCompareShot(el.compareShotA, position.a, "set 1");
  setCompareShot(el.compareShotB, position.b, "set 2");

  const shown = compare.live === "a" ? position.a : position.b;
  el.compareStage.dataset.live = compare.live;
  el.compareStage.dataset.gap = shown.url ? "false" : "true";
  el.compareGapCode.textContent = shown.error || "no frame";
  el.compareStage.title = shown.url
    ? "set " + (compare.live === "a" ? "1" : "2") + " at " + timecode(shown.time_ms) +
      " - press to flip to the other set"
    : "set " + (compare.live === "a" ? "1" : "2") + " captured nothing at " +
      timecode(shown.time_ms) + (shown.error ? " (" + shown.error + ")" : "");

  el.comparePlace.textContent = (compare.at + 1) + " / " + data.positions.length;
  el.compareTimes.replaceChildren(
    armTime("1", position.a, compare.live === "a"),
    document.createTextNode("  "),
    armTime("2", position.b, compare.live === "b"),
  );
  markLiveArm();
}

function flipCompare() {
  showCompareArm(compare.live === "a" ? "b" : "a");
}

function showCompareArm(arm) {
  if (!compare.data || !compare.data.positions || compare.data.positions.length === 0) return;
  compare.live = arm;
  renderComparePosition();
}

// stepCompare moves along the film. It wraps rather than stopping at the
// ends: the flipbook is short - eight positions, twenty at most - and a
// person walking it with one finger should not have to turn round.
function stepCompare(delta) {
  const positions = (compare.data && compare.data.positions) || [];
  if (positions.length === 0) return;
  compare.at = (compare.at + delta + positions.length) % positions.length;
  renderComparePosition();
}

el.compareA.addEventListener("change", () => {
  compare.a = el.compareA.value;
  loadComparison();
});
el.compareB.addEventListener("change", () => {
  compare.b = el.compareB.value;
  loadComparison();
});

el.compareStage.addEventListener("click", flipCompare);
el.compareFlip.addEventListener("click", flipCompare);
el.comparePrev.addEventListener("click", () => stepCompare(-1));
el.compareNext.addEventListener("click", () => stepCompare(1));
el.compareClose.addEventListener("click", () => el.compare.close());
el.compare.addEventListener("click", (event) => {
  // A click on the dialog element itself, rather than anything inside it, is
  // a click on the backdrop - the same rule the lightbox uses.
  if (event.target === el.compare) el.compare.close();
});

el.compare.addEventListener("keydown", (event) => {
  const tag = (event.target.tagName || "").toLowerCase();
  // Someone using the pickers is choosing a set, not steering the flipbook -
  // arrow keys belong to the select then.
  if (tag === "select" || tag === "input" || tag === "textarea") return;
  // Space on a focused button is that button's own activation; intercepting
  // it here would flip twice for one press.
  if (tag === "button" && event.key === " ") return;

  switch (event.key) {
    case "ArrowLeft": stepCompare(-1); break;
    case "ArrowRight": stepCompare(1); break;
    case "ArrowUp":
    case "ArrowDown":
    case " ":
    case "f":
    case "F": flipCompare(); break;
    case "1": showCompareArm("a"); break;
    case "2": showCompareArm("b"); break;
    // Escape is the dialog's own, and everything else belongs to the page.
    default: return;
  }
  event.preventDefault();
});

function onFileDone(entry, ev) {
  const fentry = fileBlock(entry, ev.file);
  fentry.progress.hidden = true;

  const links = [];
  if (ev.sheet_url) links.push(link(ev.sheet_url, "contact sheet"));
  if (ev.manifest_url) links.push(link(ev.manifest_url, "manifest"));
  if (links.length) {
    fentry.links.replaceChildren(...links);
    fentry.links.hidden = false;
  }

  logFor(entry, "file " + ev.file + " done: " + ev.frames + " frames, " + ev.skipped + " skipped");

  // The manifest exists from now on, so this is the first moment the other
  // result sets of this file can be read off disk - and the moment this
  // run's own frames become part of what a later open would find.
  loadFileDetail(entry, fentry, ev.file);
}

function link(href, text) {
  const a = document.createElement("a");
  a.href = url(href);
  a.textContent = text;
  a.target = "_blank";
  a.rel = "noopener";
  return a;
}

// showTorrent reveals - or takes away - the run's own .torrent.
//
// The one thing that decides whether the link is there is whether the run
// announced a URL for it, which the server only does when the file is really
// on disk (server.go's record). So a run captured before torpeek kept one,
// and a run whose write failed, simply have no link; nothing here guesses at
// a path, and there is no broken link to press.
//
// The anchor carries a bare download attribute rather than a filename: the
// server sends a Content-Disposition naming the file after its infohash,
// which is what a browser uses, and putting a prettier name here would only
// be a name that never takes effect.
function showTorrent(entry, href) {
  entry.torrentURL = href || "";
  entry.torrentActions.hidden = !entry.torrentURL;
  entry.torrentNote.textContent = "";
  if (!entry.torrentURL) {
    entry.torrentSend.hidden = true;
    return;
  }
  entry.torrentSave.href = url(entry.torrentURL);
  entry.torrentSend.hidden = !state.watch;
}

// sendTorrent asks the server to drop this run's .torrent into the watch
// directory a torrent client on that host is already reading (TOR-73).
//
// It is a separate action from the link beside it, not a fallback for it: the
// link saves the file where the BROWSER is, which on a seedbox deployment is
// somebody's laptop, while this one queues the torrent where the UI itself is
// running. The answer names the file that landed, which is the only
// confirmation available - nothing here can watch a torrent client pick it up.
async function sendTorrent(entry) {
  if (!entry.torrentURL) return;

  entry.torrentSend.disabled = true;
  entry.torrentNote.textContent = "sending…";
  try {
    const info = await post(entry.torrentURL + "/watch", {});
    entry.torrentNote.textContent = "sent to " + (info.path || "the watch directory");
    logFor(entry, "torrent sent to " + (info.path || "the watch directory"));
  } catch (err) {
    entry.torrentNote.textContent = String(err.message || err);
    logFor(entry, "sending the torrent failed: " + (err.message || err));
  } finally {
    entry.torrentSend.disabled = false;
  }
}

function apply(ev) {
  if (ev.type === "run_state") {
    // The one message with no "run" key is the connection marker: it opens
    // the whole replay a fresh (or reconnected) socket is about to send, but
    // it is not itself about any torrent, so there is nothing on the page to
    // update for it - the per-run reset that follows for each run already
    // rebuilds that run's own content from scratch.
    if (!ev.run) return;

    const entry = resolveIncomingRun(ev.run, ev.infohash);
    if (ev.reset) resetRunContent(entry);
    entry.disk = false;
    entry.state = ev.state;
    if (ev.source) entry.source = ev.source;
    if (ev.infohash) entry.infohash = ev.infohash;
    // TOR-117: run_state carries whichever of the two the server has -
    // never both (see runStateFieldsLocked). This is what a page that was
    // already open sees during the exact gap the ticket is about: accepted,
    // nothing confirmed yet, a magnet's dn= is all there is.
    if (ev.name) {
      noteConfirmedName(entry, ev.name);
      entry.name = ev.name;
    } else if (ev.provisional_name) {
      entry.provisionalName = ev.provisional_name;
    }
    entry.error = ev.error || "";
    // ev.partial rides on exactly one run_state a run ever publishes: the one
    // sent after this run's own record was written to disk (server.go's
    // pump, TOR-87) - the only moment the verdict this page is already
    // showing (false, from GET /runs, since a live entry had nothing merged
    // in yet) could turn out to be wrong. Every other run_state - queued,
    // running, needs-action - carries no such field, so entry.partial stays
    // whatever loadRuns or the previous run_state left it at; this still
    // reads it rather than recomputing it from entry.complete/entry.selected
    // for the same reason loadRuns does (see the comment above badgeState).
    if (ev.partial !== undefined) {
      entry.files = ev.files || 0;
      entry.complete = ev.complete || 0;
      entry.selected = ev.selected || 0;
      entry.partial = !!ev.partial;
    }
    if (!cancellable(entry.state)) entry.progress = "";
    syncEntry(entry);
    return;
  }

  const entry = ev.run ? state.runs.get(ev.run) : null;
  if (!entry) return;

  switch (ev.type) {
    case "metadata_ready":
      noteConfirmedName(entry, ev.name);
      entry.name = ev.name;
      entry.torrentSummary.hidden = false;
      // videos is the list itself, not a count: TOR-66 replaced the bare
      // number with {index, path, length} per file so a picker has something
      // to pick from. Only the count is wanted here - what a file is called
      // and how big it is belongs to the picker (TOR-67), not this line.
      entry.torrentSummary.textContent =
        ev.name + " — " + ev.selected.length + " of " + ev.videos.length + " video file(s) selected";
      syncEntry(entry);
      logFor(entry, "metadata: " + ev.name + " (" + ev.infohash + ")");
      break;

    case "needs_action":
      // Not a core event and deliberately not a metadata_ready: no run is
      // running. The torrent's name and file list arrive here, and the
      // run_state that follows this record is what actually shows the
      // picker (syncEntry).
      noteConfirmedName(entry, ev.name);
      entry.name = ev.name;
      if (ev.infohash) entry.infohash = ev.infohash;
      entry.torrentSummary.hidden = false;
      entry.torrentSummary.textContent =
        ev.name + " — " + (ev.videos || []).length + " video file(s), none captured yet";
      renderPicker(entry, ev);
      syncEntry(entry);
      logFor(entry, "waiting for a file selection: " + (ev.videos || []).length + " video file(s)");
      break;

    case "file_started":
      onFileStarted(entry, ev);
      break;

    case "frame_ready":
      addFrame(entry, ev);
      break;

    case "frame_skipped": {
      // Mark the cell this point had reserved, rather than only saying so in
      // the log: a reserved cell that never fills is indistinguishable from
      // one still waiting, and a person watching cannot tell a slow read
      // from a dead one (TOR-110).
      const fentry = fileBlock(entry, ev.file);
      fentry.skipped.set(ev.index, { code: ev.code || "", reason: ev.reason || "" });
      renderFrames(fentry);
      logFor(entry, "frame " + ev.index + " skipped: " + ev.code + " " + ev.reason);
      break;
    }

    case "progress": {
      const fentry = fileBlock(entry, ev.file);
      fentry.progress.hidden = false;
      fentry.progress.textContent =
        ev.frames_done + " / " + ev.frames_total + " frames · " +
        bytesLabel(ev.downloaded) + " downloaded · " + ev.peers + " peer(s)";
      entry.progress = ev.frames_done + "/" + ev.frames_total + " frames";
      syncEntry(entry);
      logFor(entry, "progress: " + ev.frames_done + "/" + ev.frames_total +
          ", " + ev.downloaded + " bytes, " + ev.peers + " peers");
      break;
    }

    case "budget_warning":
      logFor(entry, "warning: " + ev.spent + " of " + ev.limit + " bytes used");
      break;

    case "file_done":
      onFileDone(entry, ev);
      break;

    case "done":
      entry.progress = "";
      // The run's own .torrent rides on this event because there is one per
      // run: a live run announces the file it just wrote, and a run reopened
      // from disk announces the same one, so the link does not depend on
      // which process captured it.
      showTorrent(entry, ev.torrent_url);
      syncEntry(entry);
      logFor(entry, "done: " + ev.reason + ", " + ev.frames + " frames from " + ev.files +
          " file(s), " + ev.downloaded + " bytes in " + seconds(ev.elapsed_ms));
      // A run that finished and still left something out says so here rather
      // than by reading as failed, which is what it used to do when its
      // .torrent could not be written (TOR-79). The badge stays "done"
      // because the run is: the warning is about an artefact, not the frames.
      for (const warning of ev.warnings || []) {
        logFor(entry, "warning: " + warning);
      }
      break;

    case "failed":
      entry.progress = "";
      syncEntry(entry);
      logFor(entry, "failed: " + ev.code + " " + ev.error);
      break;
  }
}

// The socket carries events only. Reconnecting replays every run the server
// still holds from the start, so a dropped connection costs nothing but a
// redraw of what it covers - a torrent this page never heard of before
// reconnecting (or one whose history the server has since trimmed,
// keepFinishedRuns) is unaffected either way.
function connect() {
  const address = url("events");
  address.protocol = address.protocol === "https:" ? "wss:" : "ws:";

  const socket = new WebSocket(address);

  socket.onopen = () => setStatus("live", "live");
  socket.onclose = () => {
    setStatus("reconnecting", "lost");
    setTimeout(connect, 1000);
  };
  socket.onmessage = (message) => {
    try {
      apply(JSON.parse(message.data));
    } catch (err) {
      log("unreadable event: " + err);
    }
  };
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
    // whether it may offer the button.
    for (const entry of state.runs.values()) {
      entry.torrentSend.hidden = !state.watch || !entry.torrentURL;
    }
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
    // row.when is GET /runs's own answer for this row - the newest lifecycle
    // timestamp for a live entry, run.json's created_at for a disk one - and
    // it is the one moment this page overwrites entry.when after creation:
    // this is the initial listing, not a live status update, so setting it
    // here does not conflict with the rule that a status change must never
    // move a row under the default sort.
    const when = row.when ? Date.parse(row.when) : NaN;
    if (!Number.isNaN(when)) entry.when = when;
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
// straight away, from the id the POST already answered with, and it becomes
// the one shown on the right - "Take frames" should show something happening
// immediately, not leave a person staring at whatever was on screen before.
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
  selectRun(info.id);
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

// The cost line beside "Take frames" reads the intake's count, so a picker
// on screen has to follow it while it is being typed in.
el.count.addEventListener("input", () => {
  for (const entry of state.runs.values()) {
    if (!entry.pickerEl.hidden) updatePickerCost(entry);
  }
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
// The panel divider: dragging it resizes the left panel, and the width it is
// left at survives a reload - a long torrent name that got cut off is what
// the drag is for, so losing the width on every visit would defeat it.
// localStorage is read through a try/catch on purpose: it throws in a
// private window or with site data blocked, and a page that cannot remember
// the width must still render at the default from app.css rather than break.
const PANEL_WIDTH_KEY = "torpeek.panelWidth";
const PANEL_MIN_WIDTH = 160;
const PANEL_MAX_WIDTH = 640;
const PANEL_RIGHT_MARGIN = 240; // the right column keeps at least this much room

function clampPanelWidth(px) {
  const roomMax = Math.max(PANEL_MIN_WIDTH, window.innerWidth - PANEL_RIGHT_MARGIN);
  const max = Math.min(PANEL_MAX_WIDTH, roomMax);
  return Math.min(max, Math.max(PANEL_MIN_WIDTH, px));
}

function loadPanelWidth() {
  try {
    const raw = localStorage.getItem(PANEL_WIDTH_KEY);
    const width = raw ? parseFloat(raw) : NaN;
    return Number.isFinite(width) ? width : null;
  } catch (err) {
    return null;
  }
}

function savePanelWidth(px) {
  try {
    localStorage.setItem(PANEL_WIDTH_KEY, String(px));
  } catch (err) {
    // Best-effort only - the default width still works.
  }
}

function applyPanelWidth(px) {
  document.documentElement.style.setProperty("--panel-width", px + "px");
}

// panelWidth stays null until either a stored width was found or the divider
// has been dragged once - only then is there anything to reclamp on resize
// or to persist.
let panelWidth = loadPanelWidth();
if (panelWidth != null) {
  panelWidth = clampPanelWidth(panelWidth);
  applyPanelWidth(panelWidth);
}

let dragStartX = 0;
let dragStartWidth = 0;

el.resizer.addEventListener("pointerdown", (event) => {
  if (event.button !== undefined && event.button !== 0) return;
  dragStartX = event.clientX;
  dragStartWidth = el.runsPanel.getBoundingClientRect().width;
  el.resizer.classList.add("dragging");
  el.resizer.setPointerCapture(event.pointerId);
  event.preventDefault();
});

el.resizer.addEventListener("pointermove", (event) => {
  if (!el.resizer.classList.contains("dragging")) return;
  panelWidth = clampPanelWidth(dragStartWidth + (event.clientX - dragStartX));
  applyPanelWidth(panelWidth);
});

function endPanelDrag(event) {
  if (!el.resizer.classList.contains("dragging")) return;
  el.resizer.classList.remove("dragging");
  try {
    el.resizer.releasePointerCapture(event.pointerId);
  } catch (err) {
    // Already released (e.g. on pointercancel) - nothing more to do.
  }
  savePanelWidth(panelWidth);
}

el.resizer.addEventListener("pointerup", endPanelDrag);
el.resizer.addEventListener("pointercancel", endPanelDrag);

// Arrow keys on the focused divider give keyboard users the same control.
el.resizer.addEventListener("keydown", (event) => {
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
  event.preventDefault();
  const current = el.runsPanel.getBoundingClientRect().width;
  panelWidth = clampPanelWidth(current + (event.key === "ArrowLeft" ? -16 : 16));
  applyPanelWidth(panelWidth);
  savePanelWidth(panelWidth);
});

// A width chosen at one viewport size can stop fitting after the window is
// resized; only reclamp a width that was actually set, never impose one on
// a page that is still using the CSS default.
window.addEventListener("resize", () => {
  if (panelWidth == null) return;
  applyPanelWidth(clampPanelWidth(panelWidth));
});

updateSortIndicators();
loadDefaults();
loadRuns();
connect();
