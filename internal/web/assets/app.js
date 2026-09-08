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

import {
  ABSENT,
  FINAL,
  PRIORITY_HIGH,
  PRIORITY_LOW,
  arrivalOrdinal,
  availabilityCellText,
  availabilityCellTitle,
  availabilityMetaText,
  availabilityReading,
  badgeLabel,
  badgeState,
  bytesLabel,
  cancellable,
  capturedCells,
  claimReopenedRun,
  compareEntries,
  displayName,
  ensureRun,
  framesOnDisk,
  gridCells,
  hasLive,
  hasPriority,
  metaLabel,
  metaTitle,
  newFileState,
  newRunState,
  passCount,
  peersCellText,
  peersCellTitle,
  priorityLabel,
  queueCellMetaText,
  queueCellText,
  queueCellTitle,
  rateCellText,
  rateCellTitle,
  seconds,
  seedsCellText,
  seedsCellTitle,
  setFileFactory,
  setRunFactory,
  shortId,
  state,
  untickedVideos,
  waitingForMetadata,
  whenLabel,
} from "./state.js";
import { connect, setView } from "./events.js";

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

// buildLiveColumnHeaders inserts the six <th>s into the existing thead row,
// before the (headerless) actions column, so RUN_TABLE_COLUMNS below and
// el.sortHeaders' own querySelectorAll both see them without index.html ever
// naming them by hand - this file owns every column past the three the page
// shipped with (name, added, status).
function buildLiveColumnHeaders() {
  const headRow = document.querySelector("#run-table thead tr");
  const actionsHeader = document.querySelector("#run-table thead th.run-actions-header");
  if (!headRow || !actionsHeader) return;
  const frag = document.createDocumentFragment();
  for (const col of LIVE_COLUMNS) {
    const th = document.createElement("th");
    th.scope = "col";
    th.tabIndex = 0;
    th.setAttribute("role", "button");
    th.setAttribute("aria-sort", "none");
    th.dataset.sort = col.key;
    th.className = "run-cell-metric-header" +
      (col.key === "availability" ? " run-cell-availability-header" : "") +
      (col.key === "priority" ? " run-cell-queue-header" : "");
    th.title = col.title;
    const label = document.createElement("span");
    label.className = "run-th-label";
    label.textContent = col.label;
    th.append(label);
    if (col.unit) {
      const unit = document.createElement("span");
      unit.className = "run-th-unit";
      unit.textContent = col.unit;
      th.append(unit);
    }
    frag.append(th);
  }
  headRow.insertBefore(frag, actionsHeader);
}
buildLiveColumnHeaders();

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
  runList: document.getElementById("run-list"),
  runListEmpty: document.getElementById("run-list-empty"),
  sortHeaders: document.querySelectorAll("#run-table thead [data-sort]"),
  log: document.getElementById("log"),
  lightbox: document.getElementById("lightbox"),
  lightboxView: document.getElementById("lightbox-view"),
  lightboxImg: document.getElementById("lightbox-img"),
  lightboxCaption: document.getElementById("lightbox-caption"),
  lightboxClose: document.getElementById("lightbox-close"),
  lightboxZoom: document.getElementById("lightbox-zoom"),
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
  // No id on this one in index.html - it is the scroll wrapper, not a
  // control, and TOR-174 is the first thing that needs to read it from JS
  // (syncRunDetailWidth, below).
  runTableWrap: document.querySelector(".run-table-wrap"),
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
function renderRunProgress(entry) {
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

// MAX_PROGRESS_SEGMENTS is where one segment per frame stops fitting in a
// panel row. A plan of twenty is the common case and draws one each; a plan of
// two hundred would ask for segments a third of a pixel wide.
const MAX_PROGRESS_SEGMENTS = 24;

// reorderRuns moves every row's existing element into sorted order without
// rebuilding anything - appendChild on a node already in the table just
// relocates it, so a row that has not moved costs a no-op reflow, never a
// rebuild. Called whenever a row is added or anything a sort key reads
// (name, status, when) changes, so the table always reflects the active
// sort - including under the default "when" sort, where a status change
// never touches when, so re-running this never moves that row.
//
// TWO rows per entry since TOR-138: the torrent's own line and, directly
// under it, the row its detail renders in. They move together, in that order,
// which is the whole of what keeps a detail attached to the torrent it
// belongs to under every sort - append() takes both at once, so there is no
// window in which a re-sort has moved one and not the other.
function reorderRuns() {
  const rows = Array.from(state.runs.values()).sort(compareEntries);
  for (const entry of rows) el.runList.append(entry.rowEl, entry.detailRowEl);
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
// Run entries: one per torrent, live or on disk. Each owns TWO adjacent rows
// in the torrent table - its own line, and the row its detail renders in
// directly beneath it (TOR-138) - both built once and updated in place.
// Opening or closing a torrent never rebuilds anything; it only shows and
// hides what is already there, exactly as showing one of several detail panes
// used to.
//
// WHY A SECOND <tr> RATHER THAN SOMETHING INSIDE THE FIRST. A detail nested in
// a data cell would inherit that cell's own click target, and a click anywhere
// in the detail - a picker checkbox, a thumbnail - would bubble to the row's
// handler and collapse the thing being used. A sibling row cannot: the row's
// listener is on the row, and the detail is not in it. It also keeps the
// table's own column widths the only thing deciding the columns, and it stays
// valid markup, which a <div> between two <tr>s would not be.

// detailSeq only exists to give each detail container a unique id, which the
// row's toggle needs for aria-controls: a disclosure control has to name the
// region it opens, and there are now as many regions as there are torrents -
// and, since TOR-182, as many again as there are video files inside them
// (renderFileList mints "file-detail-N" from this same counter). One counter
// for both depths on purpose: what it has to guarantee is uniqueness across
// the document, which two counters would each only guarantee within their
// own prefix.
let detailSeq = 0;

// RUN_TABLE_COLUMNS is how far the detail row has to span, read off the
// header rather than written as a literal - TOR-139 added six more columns to
// the three the page shipped with, via buildLiveColumnHeaders() above, and a
// hard-coded count here would have gone wrong silently the moment it did: a
// short colspan leaves an empty cell at the end of the detail row and narrows
// the detail by a column. Reading it after that function has already run
// (both are top-level statements, in source order) is what keeps this correct
// without the two having to be kept in sync by hand.
const RUN_TABLE_COLUMNS = document.querySelectorAll("#run-table thead th").length || 1;

function newRunEntry(id) {
  const row = document.createElement("tr");
  row.className = "run-row";

  const nameCell = document.createElement("td");
  nameCell.className = "run-cell-name";
  const main = document.createElement("button");
  main.type = "button";
  main.className = "run-row-main";
  // The same disclosure triangle a video file's own row wears one level down
  // (.picker-open, since TOR-182), for the same reason: an accordion that
  // gives no sign it opens is a table.
  const icon = document.createElement("span");
  icon.className = "run-toggle-icon";
  icon.setAttribute("aria-hidden", "true");
  const name = document.createElement("span");
  name.className = "run-name";
  main.append(icon, name);
  nameCell.append(main);

  const whenCell = document.createElement("td");
  whenCell.className = "run-cell-when";

  const statusCell = document.createElement("td");
  statusCell.className = "run-cell-status";
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
  const peersCell = document.createElement("td");
  peersCell.className = "run-cell-metric run-cell-peers";
  const seedsCell = document.createElement("td");
  seedsCell.className = "run-cell-metric run-cell-seeds";
  const downCell = document.createElement("td");
  downCell.className = "run-cell-metric run-cell-down";
  const upCell = document.createElement("td");
  upCell.className = "run-cell-metric run-cell-up";
  const availCell = document.createElement("td");
  availCell.className = "run-cell-metric run-cell-availability";
  const availValue = document.createElement("span");
  availValue.className = "run-cell-availability-value";
  const availMeta = document.createElement("span");
  availMeta.className = "run-meta run-cell-availability-meta";
  availCell.append(availValue, availMeta);
  // The queue cell is two lines, the same shape the availability cell uses:
  // the position on top, the priority level under it when it is not the
  // default (TOR-140). It stays a pure FIGURE column - the two buttons that
  // change the level live in the actions cell below, beside Cancel, because
  // .run-cell-metric's own rule in app.css is "narrow, monospace,
  // tabular-nums, right-aligned, figures meant to be compared straight down
  // a column", and putting controls in one would break that for every cell
  // in the row.
  const queueCell = document.createElement("td");
  queueCell.className = "run-cell-metric run-cell-queue";
  const queueValue = document.createElement("span");
  queueValue.className = "run-cell-queue-value";
  const queueMeta = document.createElement("span");
  queueMeta.className = "run-meta run-cell-queue-meta";
  queueCell.append(queueValue, queueMeta);

  const actionsCell = document.createElement("td");
  actionsCell.className = "run-cell-actions";
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
  // already follow - no rule in app.css sets `display` on .run-detail-row, so
  // the UA's own [hidden] rule is never beaten by a class selector at equal
  // specificity. That trap has already cost this codebase twice (see
  // .drop-overlay[hidden] and the corner-bracket gate in app.css), and the
  // gate itself is gone now: there is no .detail-empty to gate on any more,
  // because a torrent that is not open simply has no detail on screen.
  const detailRow = document.createElement("tr");
  detailRow.className = "run-detail-row";
  detailRow.hidden = true;
  const detailCell = document.createElement("td");
  detailCell.className = "run-detail-cell";
  detailCell.colSpan = RUN_TABLE_COLUMNS;
  detailRow.append(detailCell);

  el.runList.append(row, detailRow);
  el.runListEmpty.hidden = true;

  const detailEl = document.createElement("div");
  detailEl.className = "run-detail";
  detailEl.id = "run-detail-" + (++detailSeq);
  main.setAttribute("aria-expanded", "false");
  main.setAttribute("aria-controls", detailEl.id);
  detailEl.innerHTML =
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
      // (app.css) is where the two now sit in a fixed order so neither
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
    // metadata_ready/needs_action (below). TOR-178: it used to also state how
    // many of the torrent's video files this run had picked ("N of M ...
    // selected") - that half is gone, so this paragraph carries the name
    // alone now. The torrent's own file count (M) did not go with it; see
    // the "Torrent files" spec renderFileMeta adds to each file's own
    // Metadata disclosure.
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
    // file in it.
    '<section class="run-again" hidden>' +
      '<p class="run-again-line"></p>' +
      '<p class="run-again-foot">' +
        '<button type="button" class="run-again-go" hidden>Top up</button>' +
        '<button type="button" class="run-again-retry" hidden>Retry</button>' +
        '<span class="run-again-cost"></span>' +
      '</p>' +
      '<p class="run-again-note"></p>' +
    '</section>' +
    // WHAT THE TORRENT HOLDS (TOR-180), and since that ticket the row's
    // ordinary content rather than a state one run happens to be in: this
    // list is on screen in every state that knows a file list, not only for
    // a torrent parked waiting to be picked from.
    //
    // AND SINCE TOR-182 IT IS THE LAST BLOCK IN THIS PANE, because each
    // file's own detail - its metadata, its progress, its frames and its
    // buttons - now hangs off that file's own row inside this list rather
    // than in a `<section class="files">` below it. There is nothing left to
    // put after the list, so the detail's structure is: what the run is
    // (header, summary, top-up), then what the torrent holds, and inside
    // that, one file at a time. See renderFileList for the row and its
    // nested slot, and fileBlock for what fills the slot.
    //
    // The .picker-* class names are kept on purpose even though this is no
    // longer a picker at all: since TOR-181 a tick IS the decision, so
    // nothing here stages one. Two of the names TOR-180 kept for this
    // ticket's sake are gone with the thing they named (.picker-go and the
    // foot's own .picker-cost), and .picker-cost has been re-used for the
    // figure that moved onto each file's row. Renaming the rest mid-epic
    // would make TOR-182..184 describe selectors that no longer exist.
    //
    // NO BUTTON UNDER THE LIST, and that is the whole of this step: a tick
    // starts that file's frames on the spot (tickFile), so a "Take frames"
    // under the list would be a second click for something already done -
    // and the cost line that sat beside it, which is TOR-50's whole warning,
    // was a price shown where the decision no longer is. It is now on each
    // file's own row, beside the box that spends it.
    //
    // Select none is gone too, and for a sharper reason than tidiness: it
    // used to clear a selection nobody had committed. A tick is now
    // irreversible - the fetch has started - so a control that unticks boxes
    // would say it can stop something it cannot. TOR-184 is the ticket that
    // gives un-ticking a real meaning (cancel these), and it can bring the
    // control back with it.
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
  detailCell.append(detailEl);

  const entry = {
    // The data half, which state.js owns (newRunState) - every field a row
    // KNOWS, with nothing in it that a browser has to exist for. What follows
    // is this file's half: the elements it is drawn in.
    //
    // ONE OBJECT, TWO LITERALS, so a key declared below still shadows the
    // same key in there, silently, exactly as two keys in one literal always
    // did (TOR-180). TestNoRunEntryFieldIsDeclaredTwice scans the union of
    // both rather than either alone.
    ...newRunState(id),
    // pickerRows maps a torrent index to the elements of its row, so
    // syncFileList can re-state every box's checked/disabled state and every
    // figure without rebuilding the list - the list is rebuilt only when the
    // file list itself changes, and rebuilding it on each event would drop
    // the scroll position of a season pack mid-tick.
    //
    // Since TOR-182 a row also carries the SLOT its file's whole detail is
    // built into (`detail`), which turns "rebuilding it on each event would
    // drop the scroll position" from a courtesy into a correctness rule: a
    // rebuild now throws away every frame grid, every open disclosure and
    // every element fileBlock is holding a reference to. entry.fileListSig
    // (state.js) is what makes that impossible rather than merely unlikely,
    // and events.js's applyFileList is what reads it.
    //
    // ON THIS SIDE OF THE SPLIT, not in newRunState, because what it holds is
    // elements: it is a map of DOM, built by renderFileList and read by
    // updateFileCosts, and the only thing state.js would be able to say about
    // it is that it exists.
    pickerRows: new Map(),
    rowEl: row, rowBadge: badge, rowName: name, rowMeta: meta, rowProgress: bar,
    rowWhen: whenCell, rowCancel: cancel, rowToggle: main,
    rowPeers: peersCell, rowSeeds: seedsCell, rowDown: downCell, rowUp: upCell,
    rowAvail: availValue, rowAvailMeta: availMeta, rowAvailCell: availCell,
    rowQueue: queueValue, rowQueueMeta: queueMeta, rowQueueCell: queueCell,
    rowRaise: raise, rowLower: lower,
    detailRowEl: detailRow,
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
    againEl: detailEl.querySelector(".run-again"),
    againLine: detailEl.querySelector(".run-again-line"),
    againGo: detailEl.querySelector(".run-again-go"),
    againRetry: detailEl.querySelector(".run-again-retry"),
    againCost: detailEl.querySelector(".run-again-cost"),
    againNote: detailEl.querySelector(".run-again-note"),
    pickerEl: detailEl.querySelector(".picker"),
    pickerTitle: detailEl.querySelector(".picker-title"),
    pickerList: detailEl.querySelector(".picker-list"),
    pickerAll: detailEl.querySelector(".picker-all"),
    pickerArmed: detailEl.querySelector(".picker-armed"),
  };

  // One listener on the row, not the name button alone: a click anywhere in
  // the row toggles it (a table row is a natural click target), and a
  // keyboard activation of the name button still reaches it too, since a
  // button's click event bubbles the same way a mouse click does. The cancel
  // button stops its own click from bubbling here, so a cancel never also
  // opens the row it sits in.
  //
  // The detail's row carries no listener at all, which is the reason it is a
  // separate <tr>: everything inside a detail - a picker checkbox, a
  // thumbnail, Compare - is outside this row, so using the detail cannot
  // close it.
  row.addEventListener("click", () => toggleRun(entry));
  cancel.addEventListener("click", (event) => {
    event.stopPropagation();
    cancelRun(entry.id);
  });
  // Same stopPropagation the cancel button needs, for the same reason:
  // reordering the queue must not also open or close the row it was done
  // from - a person moving three torrents around would otherwise leave three
  // details expanded behind them.
  raise.addEventListener("click", (event) => {
    event.stopPropagation();
    setPriority(entry, entry.priority + 1);
  });
  lower.addEventListener("click", (event) => {
    event.stopPropagation();
    setPriority(entry, entry.priority - 1);
  });
  entry.detailCancel.addEventListener("click", () => cancelRun(entry.id));

  entry.pickerAll.addEventListener("click", () => armSelectAll(entry));
  // Escape disarms, and it is bound on the SECTION rather than the document:
  // the lightbox and the compare dialog both own Escape while they are open
  // (see their own handlers), and a key that reached past a modal to disarm
  // a button behind it would be the page acting on a screen nobody is
  // looking at. A blur disarms for the same reason a menu closes when you
  // click elsewhere - an armed control left standing is one somebody comes
  // back to and presses without re-reading.
  entry.pickerEl.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && entry.armed) {
      event.stopPropagation();
      disarmSelectAll(entry);
    }
  });
  entry.pickerAll.addEventListener("blur", () => disarmSelectAll(entry));
  entry.torrentSend.addEventListener("click", () => sendTorrent(entry));
  entry.againGo.addEventListener("click", () => topUpRun(entry));
  entry.againRetry.addEventListener("click", () => retryRun(entry));

  return entry;
}

// resetRunView takes off screen everything resetRunState (state.js) took out
// of the entry - what a run_state "reset" means for the DOM: this run's own
// history is starting over (a fresh start, or a reconnecting page about to
// replay it from the beginning), not "every torrent on the page is gone".
//
// TWO FUNCTIONS RATHER THAN ONE (TOR-191), and not for tidiness. Every field
// the state half clears is something the replayed history is about to state
// again; a renderer keeping its own copy of one of them would be a second
// place the reset had to be got right, which is exactly how a reconnect comes
// to leave a stale set behind (TOR-184). So nothing here DECIDES anything - it
// draws the emptiness the state half already established, which is why every
// line below reads a field or calls the one function that may draw it.
//
// events.js calls the two together, in that order, and nowhere else does.
function resetRunView(entry) {
  // Since TOR-182 the file blocks live INSIDE the file list's own rows, so
  // there is no separate container to empty here - pickerList's own
  // replaceChildren below is what takes the blocks off screen with the rows
  // that hold them. entry.fileEntries, the only handle on those blocks, was
  // already cleared by resetRunState before this ran.
  entry.pickerRows.clear();
  entry.pickerList.replaceChildren();
  entry.pickerEl.hidden = true;
  // Select all's own two states, redrawn through the one function that may
  // draw that button. entry.armed itself was cleared by resetRunState - what
  // is left here is the label and the sentence beside it, and leaving those
  // standing would put a whole torrent's bill under one stray press on a row
  // that has just been emptied.
  syncSelectAll(entry);
  // The summary paragraph and the run's own .torrent, both of which read a
  // field the state half emptied rather than being reached into.
  renderSummaryLine(entry);
  renderTorrent(entry);
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
  // Styling hook for app.css - a stalled or still-waiting-on-metadata row
  // reads in the same amber the queue and "needs a decision" already use
  // (--warn), so it does not look like the same plain, quiet text a normal
  // "4/20 frames" or "queued" line does. See TestEveryColourComesFromAToken:
  // the colour itself lives in app.css's :root, never here.
  entry.rowMeta.dataset.stall = String(!!entry.stall || waitingForMetadata(entry));
  renderRunProgress(entry);
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

  reorderRuns();

  entry.detailBadge.textContent = badgeLabel(entry);
  entry.detailBadge.dataset.state = badgeState(entry);
  entry.detailTitle.textContent = shown.text;
  entry.detailTitle.classList.toggle("run-name-provisional", shown.provisional);
  // The sentence under the header, from entry.summaryLine - which
  // metadata_ready and needs_action each write their own version of
  // (events.js). Drawn here, on every event, rather than by those two
  // handlers reaching in: that is the whole of "rendering reads from state
  // rather than from whatever the last handler left in scope".
  renderSummaryLine(entry);
  // data-idle, not .hidden: Save .torrent sits right next to Cancel in
  // .run-detail-header-actions (TOR-169), and [hidden]'s display: none would
  // let Save slide over to fill the gap the instant Cancel is not
  // cancellable - the same "control that jumps sideways" .run-priority-up/
  // -down are disabled rather than hidden at the ends of their band to
  // avoid, a few lines up. data-idle keeps Cancel's box in the flow (see
  // its own rule in app.css) so Save's position never depends on it.
  entry.detailCancel.toggleAttribute("data-idle", entry.disk || !cancellable(entry.state));
  entry.detailError.hidden = !entry.error;
  entry.detailError.textContent = entry.error || "";
  // The file list, and which of its controls this row's state earns
  // (TOR-180). This used to be one rule reading `state === "needs-action"`,
  // which is what made the list a state rather than the row's content.
  syncFileList(entry);
  // TOR-152, in this order on purpose: draw whatever answer this row already
  // has, then ask for a newer one if the row has moved on. Drawing first is
  // what keeps the section from flickering empty on every event between two
  // answers.
  renderAgain(entry);
  refreshAgain(entry);
}

// ---------------------------------------------------------------------------
// RUNNING A ROW AGAIN (TOR-152). Two jobs that look like one button and are
// not: TOP UP finishes a run that produced something and stopped at a
// ceiling, RETRY runs one that produced nothing again. Which of them a row
// offers is decided by what the row actually is, never by asking the person
// to know the difference.

// refreshAgain asks the server what finishing this row would cost, once per
// answer worth having.
//
// ONLY FOR AN OPEN ROW, and that is the same rule loadFileDetail follows one
// level down: the endpoint reads every selected file's manifest off disk -
// which is exactly what GET /runs refuses to do for fifty rows at once (see
// walkRuns) - so it is paid for the row a person is actually looking at, and
// not before.
//
// againKey is what makes calling this from syncEntry safe: syncEntry runs on
// every event, and without the key a run streaming twenty frames would ask
// twenty times for an answer that cannot change until it finishes. The key
// carries the completeness counts as well as the state, so the one moment
// the answer DOES change - a run ending, having filled some gaps - asks
// again.
function refreshAgain(entry) {
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
      syncEntry(entry);
    })
    .catch(() => {});
}

// LIMIT_LEVER is the sentence for each ceiling a run can stop at, and it is
// the whole of this ticket's second question: which lever is the right one.
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

// renderAgain draws whichever of the two verbs this row has, and - for a top
// up - the traffic it will be allowed BEFORE it is spent.
//
// That last part is the requirement, not a nicety. TOR-50's trap is that n is
// per video file, so the ceiling a person meets is the per-file figure times
// the files selected rather than the number in the flag's help; this is the
// second place that trap can bite, so the line states the multiplication
// (frames × files) and the figure states bytes, both from the server's own
// arithmetic rather than this page's.
function renderAgain(entry) {
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

  entry.againRetry.hidden = !canRetry;
  entry.againGo.hidden = !canTopUp;
  entry.againEl.hidden = !canRetry && !canTopUp && !showRefusal;

  if (canTopUp) {
    const short = t.files.filter((f) => f.captured < f.planned).length;
    const asked = t.count * t.files.length;
    const stopped = LIMIT_LEVER[t.limit];
    entry.againLine.textContent =
      t.captured + " of " + asked + " frames (" + t.count + " × " + t.files.length +
      " file(s)) — " + t.remaining + " point(s) still missing across " + short + " file(s)" +
      (stopped ? ", " + stopped +
        (t.limit === "traffic" && t.ceiling_bytes ? " of " + bytesLabel(t.ceiling_bytes) : "") : "");
    // The figure, in the accent the picker's own cost line wears, because it
    // is the same promise: this is what pressing the button spends.
    entry.againCost.textContent = t.raise_helps
      ? "up to " + bytesLabel(t.offer_bytes) + " more traffic" +
          (t.roof_capped ? " (all the client-wide roof allows)" : "") +
          " — a ceiling sized to finish in one press, not an estimate of what it will cost"
      : "at the ordinary ceiling — no raise would help";
    entry.againCost.title = t.files
      .map((f) => f.path + ": " + f.captured + "/" + f.planned)
      .join("\n");
    // Said rather than left to be discovered. Pieces are discarded after
    // every run (REQUIREMENTS.md 2.9), so this is not a download resuming
    // where it stopped: the frames come back for free off disk, and the
    // piece data behind the points that are still missing is fetched again.
    // That is what makes a top-up cheap despite the discard, and a person
    // expecting a byte-for-byte continuation would be surprised twice - once
    // by the cost, once by the wait.
    entry.againNote.textContent =
      (LIMIT_NOTE[t.limit] ? LIMIT_NOTE[t.limit] + " " : "") +
      "The " + t.captured + " frame(s) already taken are reused off disk. The piece data " +
      "for the " + t.remaining + " point(s) still missing is fetched again - pieces are " +
      "discarded after every run, so only the frames survive, never the download.";
    return;
  }

  if (showRefusal) {
    entry.againLine.textContent = t.refused;
    entry.againCost.textContent = "";
    entry.againCost.title = "";
    entry.againNote.textContent = "";
    return;
  }

  if (canRetry) {
    entry.againLine.textContent = entry.state === "failed"
      ? "This run produced nothing, so there is nothing to top up — retry asks for the " +
          "same thing again: the same source, the same files, the same ceiling."
      : "This run was cancelled — retry asks for the same thing again: the same source, " +
          "the same files, the same ceiling.";
    entry.againCost.textContent = "";
    entry.againCost.title = "";
    entry.againNote.textContent = "";
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
async function topUpRun(entry) {
  showError("");
  entry.againGo.disabled = true;
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
    logFor(entry, "topping up: " + (entry.topup && entry.topup.remaining) + " point(s), up to " +
        bytesLabel((entry.topup && entry.topup.offer_bytes) || 0) + " more traffic");
    claimReopenedRun(entry, info.id);
  } catch (err) {
    entry.claiming = false;
    showError(String(err.message || err));
    logFor(entry, "could not top up: " + (err.message || err));
  } finally {
    entry.againGo.disabled = false;
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
async function retryRun(entry) {
  showError("");
  entry.againRetry.disabled = true;
  entry.claiming = true;
  try {
    const info = await post("runs/retry", { id: entry.id });
    logFor(entry, "retrying");
    claimReopenedRun(entry, info.id);
  } catch (err) {
    entry.claiming = false;
    showError(String(err.message || err));
    logFor(entry, "could not retry: " + (err.message || err));
  } finally {
    entry.againRetry.disabled = false;
  }
}

// ---------------------------------------------------------------------------
// THE DETAIL'S OWN WIDTH (TOR-174). app.css's .run-detail explains WHAT this
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
function syncRunDetailWidth() {
  if (!el.runTableWrap) return;
  const width = el.runTableWrap.clientWidth;
  // 0 while the wrap is display:none or not yet laid out - leave the
  // previous value (or app.css's own 100% fallback) rather than pin every
  // open detail to zero.
  if (width > 0) {
    document.documentElement.style.setProperty("--run-detail-w", width + "px");
  }
}
window.addEventListener("resize", syncRunDetailWidth);

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
function setRunExpanded(entry, expanded) {
  entry.expanded = expanded;
  // TOR-174: either direction can change the page's own height (a detail
  // coming on or off screen), which can add or remove the document's
  // vertical scrollbar and with it a few pixels of .run-table-wrap's own
  // width - see syncRunDetailWidth's own comment.
  syncRunDetailWidth();
  // TOR-152: opening a row is when its top-up standing is worth reading off
  // disk - here rather than in toggleRun, because this is the one function
  // that may put a detail on screen (see this block's own heading) and
  // several paths reach it: a click, and began() for a run just started.
  // refreshAgain is a no-op for a row that is not settled, has no infohash,
  // or was already asked this question, so calling it on every expansion
  // costs a comparison.
  if (expanded) refreshAgain(entry);
  // The row's `hidden` attribute and nothing else - no rule in app.css sets
  // display on .run-detail-row, so the UA rule wins uncontested.
  entry.detailRowEl.hidden = !expanded;
  entry.rowEl.dataset.expanded = String(expanded);
  entry.rowToggle.setAttribute("aria-expanded", String(expanded));
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
    setRunExpanded(entry, !entry.expanded);
    return;
  }
  if (entry.expanded) {
    setRunExpanded(entry, false);
    return;
  }
  setRunExpanded(entry, true);
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

function basename(path) {
  const parts = String(path || "").split("/");
  return parts[parts.length - 1] || path || "";
}

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

// renderFileList BUILDS THE ROWS of the torrent's whole file list, from
// entry.fileList and entry.videos - never from a message.
//
// events.js is what puts those two on the entry, off whichever of
// metadata_ready or needs_action carried them, and it is also what decides
// that this function should run AT ALL: the same list is not rebuilt, which
// since TOR-182 is a correctness rule rather than a courtesy about scroll
// positions (see applyFileList for the signature that settles it, and point 1
// below for what a needless rebuild destroys). Reaching this function means
// the list genuinely changed shape.
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
// frames and its buttons - is built into a slot inside that file's own <li>
// (fileBlock fills it), which is the third depth this page now has. Three
// things follow from that, and each has its own note below where it lands:
//
//   1. THE ROW BUILD IS NOW LOAD-BEARING, not a redraw. It used to be free
//      to rebuild the list on every message that carried one; now a rebuild
//      detaches every frame grid on the row. entry.fileListSig - kept in
//      state.js, read in events.js - is what makes a second metadata_ready,
//      which a top-up and a retry both publish onto this same entry, never
//      reach this function at all.
//   2. THE ROW OWNS THE TOGGLE, not the block inside it. The listener is on
//      .picker-file and the detail is its SIBLING inside the <li>, never its
//      descendant - the same shape, and for the same reason, as the run
//      row's own detail being a separate <tr> (see newRunEntry's heading): a
//      click on a thumbnail or on Regenerate must not close the thing it is
//      being used on.
//   3. THE TICK AND THE DISCLOSURE ARE ONE ROW WITH TWO MEANINGS, and they
//      cannot collide, because a file gets a detail exactly when it has been
//      asked for - and updateFileCosts disables the box of every asked-for
//      file. So a row either spends traffic (no detail yet) or opens what
//      that traffic bought (box disabled), never both.
function renderFileList(entry) {
  // A FRESH MAP, not a cleared one: every bundle in the old map points at a
  // row this rebuild is about to detach, and events.js has already dropped
  // the file entries that pointed into them (applyFileList) - which is the
  // only handle on the blocks, and had to go first.
  entry.pickerRows = new Map();

  const tickable = new Set(entry.videos.map((video) => video.index));

  entry.pickerList.replaceChildren(...entry.fileList.map((file) => {
    const item = document.createElement("li");
    item.className = "picker-item";
    const video = tickable.has(file.index);
    // The styling hook, and the one thing app.css keys the grey off - the
    // colour itself lives in :root, never here (TestEveryColourComesFrom
    // AToken).
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
    // app.css keys the triangle's visibility off the item's data-detail,
    // which fileBlock is the only writer of, so there is one source of truth
    // for "this file has a detail" rather than a disabled flag to keep in
    // step with it.
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
      box.id = "file-tick-" + (++detailSeq);
      row.htmlFor = box.id;
      box.addEventListener("change", () => tickFile(entry, file.index, box));
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
    // The row first, so the detail slot below can be appended after it and
    // read as what it is: the thing this row opens onto.
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
      // on IS this row now.
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
      // control by id (row.htmlFor below), so a labelable element appearing
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
        clearFrames(entry, file.index);
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
      // A SIBLING OF THE ROW, not part of it, for the reason .file-detail
      // below is one: the row is a <label>, and a whole sentence inside it
      // would join the checkbox's accessible name.
      const note = document.createElement("p");
      note.className = "picker-note";
      note.hidden = true;
      item.append(note);

      // THE SLOT ITSELF: built empty with the row, filled by fileBlock the
      // first time this file says anything, and a SIBLING of the row rather
      // than part of it (see this function's own heading, point 2). It is
      // built here rather than in fileBlock so that the id the toggle names
      // in aria-controls exists from the start - a disclosure control has to
      // name the region it opens, and a region minted later is one the
      // control pointed at nothing for.
      const detail = document.createElement("div");
      detail.className = "file-detail";
      detail.hidden = true;
      detail.id = "file-detail-" + (++detailSeq);
      open.setAttribute("aria-controls", detail.id);
      item.append(detail);

      entry.pickerRows.set(file.index,
        { item, row, box, name, cost, state: asked, summary, open, detail, clear, note });

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
        toggleFileDetail(entry, file.index);
      });
    }

    return item;
  }));

  syncFileList(entry);
}

// syncFileList decides what this row's state earns: whether the list is on
// screen at all, and which of its controls are.
//
// Called from syncEntry, so it re-runs on every event - the list has to
// survive a row moving from parked to queued to running to done, changing
// only what it offers, and the one thing that must NOT be re-decided here is
// whether the row is expanded (setRunExpanded owns that; see its own doc).
function syncFileList(entry) {
  // Hidden only when there is nothing to list, never because of the state.
  // Two rows legitimately have nothing: a queued one, whose metadata has not
  // been fetched (choosing what to fetch cannot be offered before the
  // torrent has said what it holds), and a disk row whose replay has not
  // arrived or failed. Both are "not known yet", which an empty framed box
  // would state as "holds nothing".
  entry.pickerEl.hidden = entry.fileList.length === 0;

  // PARKED still earns the frame, and only the frame: .picker's --warn
  // border says "this is blocking, nothing happens until you act", which is
  // true of a torrent waiting to be picked from and a false alarm on a run
  // already going. What it no longer decides is whether a tick works -
  // TOR-181 made that the server's own answer, carried on every run_state
  // (entry.tickable), because the rule has a case this page cannot see.
  const parked = entry.state === "needs-action";
  entry.pickerEl.dataset.parked = String(parked);
  entry.pickerTitle.textContent = fileListTitle(entry, parked);

  // Every box's state, every price, and Select all's own label and total.
  // One function, and it has to run here as well as on each tick: a row that
  // has just reached the parked state has boxes that have never been priced.
  updateFileCosts(entry);
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
function fileListTitle(entry, parked) {
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

// framesLabel is one file's price, in the unit TOR-50's trap is measured in.
//
// Frames rather than bytes, and that is deliberate: the frame count is the
// thing that multiplies per file and the thing this page can state exactly.
// What a frame costs in traffic is the server's arithmetic, priced off a
// record on disk and shown where the server has computed it (.run-again-cost
// for a top-up); a byte figure invented here would be a guess wearing the
// authority of a measurement.
function framesLabel(n) {
  return n ? n + " frames" : "server default";
}

// updateFileCosts re-states every row: whether its box may be clicked, what
// clicking it would spend, and - for a file already asked for - which run it
// is in.
//
// This is the one place in the whole program where a cost can be shown IN
// ADVANCE. Every other screen only ever reports traffic once it is gone. It
// follows both inputs live: the ticks (through run_state), and the count on
// the intake line, which a person may well adjust while reading this list.
function updateFileCosts(entry) {
  const n = passCount(entry, countValue());

  for (const [index, row] of entry.pickerRows) {
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
    const untick = !asked ? ""
      : entry.deferred.has(index) ? "drop"
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
    // and carries a [hidden] companion anyway; see app.css).
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

  syncSelectAll(entry);
}

// ---------------------------------------------------------------------------
// SELECT ALL IS THE MOST EXPENSIVE CLICK ON THE PAGE (TOR-181).
//
// It used to stage a selection somebody then confirmed with a button under
// the list; the button is gone, so pressing it now spends traffic on every
// video file the torrent holds at once. A one-click bill is exactly what
// this ticket refuses to leave it as, and dropping it is not the answer
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

function armSelectAll(entry) {
  // The second press. Everything it will start is read again HERE rather
  // than remembered from the first press: a tick or a run_state may have
  // landed in between, and starting a list that is no longer true would
  // spend on a file somebody else's tick already started.
  if (entry.armed) {
    const files = untickedVideos(entry);
    disarmSelectAll(entry);
    startFiles(entry, files);
    return;
  }
  entry.armed = true;
  syncSelectAll(entry);
}

function disarmSelectAll(entry) {
  entry.armed = false;
  syncSelectAll(entry);
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
function syncSelectAll(entry) {
  const parked = entry.state === "needs-action";
  const remaining = untickedVideos(entry);
  const offer = parked && entry.tickable && remaining.length > 0;

  entry.pickerAll.hidden = !offer;
  if (!offer) {
    entry.armed = false;
    entry.pickerAll.removeAttribute("data-armed");
    entry.pickerAll.textContent = "Select all";
    entry.pickerAll.title = "";
    entry.pickerArmed.textContent = "";
    return;
  }

  const n = passCount(entry, countValue());
  const total = n ? remaining.length * n : 0;
  const sum = total
    ? remaining.length + " file(s) × " + n + " = " + total + " frames"
    : remaining.length + " file(s) at the server's own frame count";

  if (!entry.armed) {
    entry.pickerAll.removeAttribute("data-armed");
    entry.pickerAll.textContent = "Select all";
    entry.pickerAll.title = "start every video file in this torrent at once — " + sum;
    entry.pickerArmed.textContent = "";
    return;
  }

  entry.pickerAll.dataset.armed = "true";
  entry.pickerAll.textContent = total
    ? "Start all " + remaining.length + " — " + total + " frames"
    : "Start all " + remaining.length;
  // The sentence, beside the button, because a label that changed is a hint
  // and this needs an instruction: nothing has been spent yet, and a person
  // has to be told that the next press is the one that spends it.
  entry.pickerArmed.textContent = "this starts " + sum +
    " — press again to confirm, Esc to cancel";
  entry.pickerAll.title = entry.pickerArmed.textContent;
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
function tickFile(entry, index, box) {
  // Somebody who ticks a single file has answered Select all's question by
  // doing something else; leaving it armed behind them would put a
  // twenty-file bill under a second stray press.
  disarmSelectAll(entry);

  if (!box.checked) {
    // TOR-184's two stops, in updateFileCosts' own order and read from the
    // same sets, so the box that was drawn live is the box that acts. Both
    // are guarded on the file being one this row asked for at all: a box
    // moved on a row whose run_state has since re-armed it must not send a
    // stop for a file the server no longer holds.
    if (entry.picked.has(index) && entry.deferred.has(index)) {
      dropFile(entry, index);
      return;
    }
    if (entry.picked.has(index) && entry.fetching.has(index)) {
      stopFetch(entry, index, box);
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
      setFileNote(entry, index, "");
      updateFileCosts(entry);
      logFor(entry, "file " + index + " un-ticked - its frames can be cleared from disk, " +
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
    setFileNote(entry, index, "");
    updateFileCosts(entry);
    return;
  }

  startFiles(entry, [index]);
}

// setFileNote writes - or takes away - the sentence beside one file's row.
//
// One function so the note can never be left visible and empty, or filled and
// hidden: it is taken off screen by the `hidden` attribute AND emptied, since
// a stale sentence sitting in a hidden element is one an unrelated later
// change would put back on screen.
function setFileNote(entry, index, text) {
  const row = entry.pickerRows.get(index);
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
async function dropFile(entry, index) {
  showError("");

  // Optimistic, for the same frame startFiles guesses through and rolled back
  // the same way: the boxes are the only feedback there is between the click
  // and the socket. run_state overwrites both sets moments later.
  entry.picked.delete(index);
  entry.deferred.delete(index);
  updateFileCosts(entry);

  try {
    await post("runs/untick", { id: entry.id, file: String(index) });
    logFor(entry, "file " + index + " dropped before it started - it was waiting for the " +
      "next pass, so nothing was fetched for it and nothing was deleted");
  } catch (err) {
    // A refusal is a real answer: the pass in flight may have ended in the
    // meantime and taken this file into itself (Server.pendingPass), and it
    // is being fetched by the time the POST lands. Put back, so the box does
    // not say a file is not wanted while the engine is holding it.
    entry.picked.add(index);
    entry.deferred.add(index);
    updateFileCosts(entry);
    showError(String(err.message || err));
    logFor(entry, "could not drop file " + index + ": " + (err.message || err));
  }
}

// stopFetch is what un-ticking a file the engine is already fetching means:
// STOP THE RUN THAT FILE BELONGS TO (TOR-184).
//
// It is the whole of this ticket's decision, and it is a decision about the
// ENGINE rather than about the page. core.Engine.Run hands back an event
// channel and nothing else; its only handle is the context it was started
// with, every file's goroutine is given that same context, and the run's
// traffic budget is sized once from the file list before the clock starts. So
// there is no per-file stop to call here - the choice was between saying the
// gesture stops the run and building per-file cancellation into the engine,
// and this release says the first and says it out loud.
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
function stopFetch(entry, index, box) {
  // The box goes straight back, and that is the truth rather than a
  // rollback: a cancel does not un-ask for the file. The run asked for it,
  // whatever frames it took stay on disk, and run_state redraws this row
  // moments later with the box ticked and a Clear frames beside it - so a box
  // left cleared would be the one thing on screen claiming otherwise.
  box.checked = true;
  logFor(entry, "un-ticked file " + index + " while it was being fetched - stopping this " +
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
// The grid is REPLACED from what the server read back off disk, never edited
// here: a page that removed its own tiles and hoped the two agreed is exactly
// how a frame comes to be on screen that is not on disk.
async function clearFrames(entry, index) {
  showError("");
  if (!entry.infohash) return;
  const row = entry.pickerRows.get(index);
  if (!row) return;

  setFileNote(entry, index, "");
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
    const detail = answer.file;

    const fentry = entry.fileEntries.get(index);
    if (fentry) {
      fentry.frames = new Map();
      for (const f of (detail && detail.frames) || []) {
        fentry.frames.set(f.time_ms, detailFrame(f));
      }
      // The plan and the live skips go with the frames, for the reason
      // loadFileDetail drops them: they describe one run's attempt, and what
      // is on screen now is every set this file has left on disk. Keeping the
      // plan would lay the grid out from points nothing can fill.
      fentry.plan = [];
      fentry.skipped.clear();
      renderFrames(fentry);
      updateFileSummary(fentry);
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
      // is a 404. It is exactly the untruth this ticket is about, one element
      // over from the frames. Since TOR-191 the link is drawn from
      // fentry.sheetURL, so removing it is the same act as putting it there
      // rather than a second, opposite piece of DOM handling.
      if (capturedCells(fentry) === 0) {
        fentry.reach.hidden = true;
        fentry.sheetURL = "";
        renderFileLinks(fentry);
      } else {
        renderReach(fentry, (detail && detail.sets) || []);
      }
    }

    // THE WARNING GOES BESIDE THE FILE (TOR-78's shape): the clear happened -
    // the record and the manifest say the file is not part of this result any
    // more - and something of it is still on disk. Refusing to re-render
    // would leave frames on screen that nothing accounts for, and reporting
    // it as a failure would tell a person nothing was deleted.
    if (answer.warning) setFileNote(entry, index, answer.warning);

    // The offer is over only if the file is actually clean. A clear that
    // partly failed leaves frames, so it leaves the button too - with the
    // warning beside it saying what is in the way, and a second press worth
    // making.
    if (framesOnDisk(entry, index) === 0) entry.unticked.delete(index);
    updateFileCosts(entry);
    logFor(entry, "cleared file " + index + "'s frames from disk" +
      (answer.warning ? " - " + answer.warning : ""));
  } catch (err) {
    // Beside the file, like the warning, and for the same reason: this is a
    // report on one row's button. Nothing on screen changed, because nothing
    // on disk did.
    setFileNote(entry, index, String(err.message || err));
    logFor(entry, "could not clear file " + index + ": " + (err.message || err));
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
async function startFiles(entry, indices) {
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
  updateFileCosts(entry);

  try {
    await post("runs/decide", {
      id: entry.id,
      files: files.map(String),
      count: passCount(entry, countValue()),
    });
    logFor(entry, (later ? "queued for the next pass: " : "capturing: ") +
      files.length + " file(s) of " + entry.videos.length + ", " +
      framesLabel(passCount(entry, countValue())) + " each");
  } catch (err) {
    // Rolled all the way back. A refusal is a real answer here - a run that
    // has settled, or a torrent dropped as a file whose staged copy is gone
    // (refuseTick) - and a box left ticked after one would say a file is
    // being captured when nothing is.
    for (const index of files) {
      entry.picked.delete(index);
      entry.deferred.delete(index);
    }
    updateFileCosts(entry);
    showError(String(err.message || err));
    logFor(entry, "could not start " + files.length + " file(s): " + (err.message || err));
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

// fileBlock returns the detail for one file of one torrent, building it the
// first time it is needed. Each video file gets its own summary panel and
// its own frame grid, since a multi-file torrent should not mix their frames
// or their tracks in one place - and one torrent's files must never mix with
// another's now that the page can hold several at once.
//
// SINCE TOR-182 IT BUILDS INTO THE FILE'S OWN ROW, one level in, rather than
// into a `.files` section under the list. Nothing about what it builds
// changed in that move - the metadata disclosure, the progress line, the
// reach strip, the grid and the buttons are the same elements with the same
// comments - and two things about WHERE it builds did:
//
//   1. THE TITLE LINE IS GONE, because the row is the title line. The
//      `<h2 class="file-title">` held a toggle, the file's name and its
//      summary; the row already carries a disclosure, the name (as a base
//      name with the whole path on its title, which is the better of the two
//      conventions) and now the summary too. Keeping a second one inside the
//      thing the first one opens would have been the same line twice.
//   2. THE MOUNT POINT COMES FROM THE LIST, and if the list has no row for
//      this index there is nowhere to put a detail. That cannot happen for
//      any sequence the server produces - MetadataReady is published before
//      the first FileStarted on both paths that publish one at all
//      (core.Engine.run, and cacheHit.publish for a replay), and its file
//      list holds every video the run can then start - so this returns null
//      rather than inventing a home, and the events that call it treat that
//      the way they already treat any absent reading.
//
// Everything in the detail - specs, tracks, links, progress, the frame
// grid - is built and filled in exactly as before, whether or not the file
// is expanded; only the slot's `hidden` attribute decides what is on
// screen. A frame_ready for a collapsed file still appends its figure to
// .grid (events.js's applyFrame never checks expanded state), so expanding
// it later shows
// everything that arrived while it was closed - nothing is built lazily,
// there is nothing to replay.
//
// Only the first file built for a run is auto-expanded (entry.autoExpanded
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
function fileBlock(entry, index) {
  let fentry = entry.fileEntries.get(index);
  if (fentry) return fentry;

  const listRow = entry.pickerRows.get(index);
  if (!listRow) {
    // SAID OUT LOUD RATHER THAN SWALLOWED. Every caller below already copes
    // with a null the way this page copes with any absent reading, so a file
    // whose row never arrived costs its frames rather than a broken page -
    // but it would cost them in total silence, and the invariant this
    // depends on lives in another package. The log is where a person can see
    // it happened at all.
    logFor(entry, "file " + index + " has no row in the torrent's file list, so there is " +
      "nowhere to show it - the metadata that names the list must arrive first");
    return null;
  }

  const body = listRow.detail;
  body.innerHTML =
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
  // The row's own item carries the open/closed state, so app.css can dress
  // the whole row - not just the slot under it - the way .run-row
  // [data-expanded="true"] already dresses an open torrent one level up.
  // data-detail is what brings the row's disclosure triangle on screen: this
  // is the moment there is something behind it, and this is the only writer
  // of that attribute.
  listRow.item.dataset.detail = "true";

  fentry = {
    // The data half, which state.js owns (newFileState): what this file
    // KNOWS - its frames, its plan, its media, its last heartbeat. Below it,
    // this file's half: the elements. Same union-scan rule as the run entry.
    ...newFileState(index, entry),
    // item is the row's <li>, body the slot inside it. The two are what the
    // old .file article and its .file-body were, one level in - see this
    // function's own heading for what happened to the title line between
    // them.
    item: listRow.item,
    toggle: listRow.open,
    name: listRow.name,
    summary: listRow.summary,
    body,
    metaToggle: body.querySelector(".meta-toggle"),
    metaBody: body.querySelector(".meta-body"),
    specs: body.querySelector(".specs"),
    tracks: body.querySelector(".tracks"),
    links: body.querySelector(".file-links"),
    regenCount: body.querySelector(".file-regen-count"),
    regenGo: body.querySelector(".file-regen-go"),
    compareGo: body.querySelector(".file-compare"),
    progress: body.querySelector(".file-progress"),
    availSwarm: body.querySelector(".avail-swarm"),
    availSwarmText: body.querySelector(".avail-swarm-text"),
    reach: body.querySelector(".reach"),
    reachStrip: body.querySelector(".reach-strip"),
    reachNote: body.querySelector(".reach-note"),
    grid: body.querySelector(".grid"),
  };
  entry.fileEntries.set(index, fentry);

  fentry.regenCount.value = el.count.value;
  fentry.regenGo.addEventListener("click", () => regenerate(entry, index, fentry));
  fentry.compareGo.addEventListener("click", () => openCompare(entry.infohash, index));

  // No toggle listener here any more: the row that opens this detail is
  // built once by renderFileList and owns the click (toggleFileDetail), so
  // the listener lives with the element it is on rather than being attached
  // to it from inside the thing it opens. fentry.toggle is still the row's
  // own disclosure button, because aria-expanded has to travel with the
  // expanded state and setFileExpanded is where that is written.
  setFileExpanded(fentry, !entry.autoExpanded);
  entry.autoExpanded = true;
  updateFileSummary(fentry);

  fentry.metaToggle.addEventListener("click", () => {
    setMetaExpanded(fentry, !fentry.metaExpanded);
  });
  setMetaExpanded(fentry, false);

  // Drawn once immediately, from whatever this entry already knows (a swarm
  // reading from GET /runs' own "live" object may already be sitting on
  // entry.live before this card exists) rather than waiting for the next
  // heartbeat or the file to finish - the same reasoning updateFileSummary
  // above is called for on creation.
  renderAvail(fentry);

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

// toggleFileDetail is what clicking a file's row does (renderFileList binds
// it), and the only path a person's click takes to setFileExpanded.
//
// A row whose file has said nothing yet has no detail to open - the ticks are
// what start a file, and until one has, there is no metadata, no plan and no
// frames to show - so this is a no-op there rather than an empty panel.
function toggleFileDetail(entry, index) {
  const fentry = entry.fileEntries.get(index);
  if (!fentry) return;

  const expanded = !fentry.expanded;
  setFileExpanded(fentry, expanded);
  // Opening a file is when it is worth reading the other result sets off
  // disk - not on every run_state, and not for a file nobody looked at.
  // Once is enough: a set only gains frames by a run finishing, which
  // asks again itself (renderFileDone, on the file's own file_done).
  if (expanded && !fentry.detailLoaded) loadFileDetail(entry, fentry, index);
}

// ---------------------------------------------------------------------------
// ONE FILE'S DETAIL AT A TIME (TOR-182), and this is the level where that
// decision is made rather than the one above it.
//
// The row level deliberately allows SEVERAL torrents open at once, for three
// written reasons (see setRunExpanded's own heading) - the first of which is
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
//     built once and updated whether or not it is on screen (fileBlock's own
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
// wanted, this function is where it would be relaxed, and the reasoning
// above is what would have to be answered.
function setFileExpanded(fentry, expanded) {
  // Every sibling first, and only when opening: this is the accordion, and
  // it reads the entry's own map rather than a "which one is open" field so
  // that nothing can be left claiming to be open after a reset emptied the
  // map from under it.
  if (expanded) {
    for (const other of fentry.entry.fileEntries.values()) {
      if (other !== fentry && other.expanded) setFileExpanded(other, false);
    }
  }

  fentry.expanded = expanded;
  // On the row's own <li>, not on the slot: an open file is a state of the
  // row, the same way .run-row[data-expanded="true"] is a state of the
  // torrent's row rather than of its detail.
  fentry.item.dataset.expanded = String(expanded);
  fentry.toggle.setAttribute("aria-expanded", String(expanded));
  fentry.body.hidden = !expanded;
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

// setMetaExpanded is the file-level setFileExpanded's counterpart one level
// down (TOR-71): the single function that may change whether a file's
// metadata (.specs and .tracks) is on screen, called from exactly two
// places - the metadata toggle's own click handler, and once at creation to
// start every file's metadata collapsed, with no auto-expand exception. No
// event that rebuilds .specs/.tracks in place (renderFileMeta) touches
// fentry.metaExpanded, so a person who opens the metadata keeps it open
// through every event that follows.
function setMetaExpanded(fentry, expanded) {
  fentry.metaExpanded = expanded;
  fentry.metaToggle.setAttribute("aria-expanded", String(expanded));
  fentry.metaBody.hidden = !expanded;
}

// updateFileSummary keeps a row worth choosing by without opening it: the
// file name and size are already on the row (renderFileList), and this adds
// whatever of resolution and frame count are already known - both update
// live (resolution the moment file_started arrives, the frame count on
// every frame_ready) whether or not the file happens to be expanded right
// now. Since TOR-182 it writes into that same row (.picker-summary) rather
// than onto a title line inside the detail, and it is on screen open or
// closed - see setFileExpanded for why that stopped being a repetition.
// updateFileSummary says how many frames the file has, and against what it
// was trying for when the two differ.
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
function updateFileSummary(fentry) {
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
  fentry.summary.textContent = parts.join(" · ");
}

// renderSummaryLine draws the sentence under a run's detail header from
// entry.summaryLine - the torrent's own confirmed name, or for a parked one
// the name plus what it holds and that nothing is captured yet.
//
// ONE FUNCTION, so the paragraph can never be left visible and empty or
// filled and hidden: an empty string IS the absence, exactly the rule
// setFileNote already follows for a file's own note one level in. The two
// sentences themselves are written by the two messages that carry a name
// (events.js's applyMetadataReady and applyNeedsAction); nothing here
// chooses between them, which is why a parked row's sentence does not change
// the instant its first tick moves it out of needs-action.
function renderSummaryLine(entry) {
  entry.torrentSummary.textContent = entry.summaryLine;
  entry.torrentSummary.hidden = !entry.summaryLine;
}

// renderFileMeta draws everything a file's own metadata says: its name on the
// row, and inside its Metadata disclosure the specs and the two track groups.
// All of it from fentry.media, which is where file_started's message now
// lands (events.js's applyFileStarted).
//
// IT IS HANDED STATE, NOT A MESSAGE, and that is the ticket rather than a
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
function renderFileMeta(fentry) {
  const media = fentry.media;
  if (!media) return;

  // The engine's own path, restated on the row in the LIST's convention
  // rather than the block's: a base name with the whole path on its title
  // (renderFileList). The two used to differ - the block's title line showed
  // the full path, which in a season pack is the same forty characters of
  // directory twenty-five times over - and since TOR-182 there is one line
  // to show it on, so one of the two conventions had to win. This one did
  // because the list is read as a column.
  fentry.name.textContent = basename(media.path);
  fentry.name.title = media.path;

  fentry.specs.replaceChildren();
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
  // small version of this whole ticket: a file entry knows which torrent it
  // belongs to, so whoever asks for a redraw does not have to carry one
  // along.
  addSpec(fentry.specs, "Torrent files",
    fentry.entry.videos.length ? String(fentry.entry.videos.length) : "");
  addSpec(fentry.specs, "Resolution",
    media.width && media.height ? media.width + "×" + media.height : "");
  addSpec(fentry.specs, "Video",
    [media.codec, media.profile, media.fps ? media.fps.toFixed(2) + " fps" : "",
      bitrateLabel(media.videoBitrate)].filter(Boolean).join(" · "));
  addSpec(fentry.specs, "Overall bitrate", bitrateLabel(media.bitrate));
  addSpec(fentry.specs, "Duration", seconds(media.durationMs));

  fentry.tracks.replaceChildren(
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
function renderFileProgress(fentry) {
  const beat = fentry.heartbeat;
  fentry.progress.hidden = !beat;
  fentry.progress.textContent = beat
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
// frame_skipped now restates the count as well, which it did not before the
// split. Nothing on screen moves: a skipped point already had a planned cell,
// so capturedCells and the planned total both come out the same - what it
// buys is one fewer way for the grid and its own caption to disagree.
function renderFileGrid(fentry) {
  renderFrames(fentry);
  updateFileSummary(fentry);
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
function renderReach(fentry, sets) {
  const el = fentry.reach;
  if (!el) return;

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
  fentry.reachStrip.dataset.known = String(known);
  if (!known) {
    fentry.reachStrip.replaceChildren();
    fentry.reachStrip.setAttribute("aria-label",
      "Where this file's frames came from is not recorded - the set was captured before that was kept");
    fentry.reachNote.textContent = "where the frames came from was not recorded for this set";
    el.hidden = false;
    renderAvail(fentry);
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
  fentry.reachStrip.replaceChildren(frag);

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

  fentry.reachNote.textContent = parts.join(" · ");
  fentry.reachStrip.setAttribute("aria-label",
    "Pieces of this file the frames came from: " + reach.claimed_pieces + " of " + reach.pieces);
  el.hidden = false;
  renderAvail(fentry);
}

// MAX_BLOCKS is where drawing one block per piece stops being readable and
// aggregating starts. At the pane's usual width this leaves each block a few
// pixels across; a 10,000-piece remux would otherwise ask for a block a
// fortieth of a pixel wide, which is the "lie about individual blocks" the
// ticket names.
const MAX_BLOCKS = 96;

// REACH_FLOOR is how much of a block is painted when any of its pieces were
// ordered, before density is added on top. Enough to be seen at the strip's
// height; small enough that a full block still reads as clearly fuller.
const REACH_FLOOR = 0.22;

// renderAvail updates the swarm chip beside the reach strip (TOR-142's one
// surviving idea, moved onto that row by TOR-153 - see renderReach's own doc
// for the shapes-must-stay-different reasoning and for why nothing here
// draws capture-point marks any more). It is a plain function of
// entry.live.swarm, independent of this file's own reach: renderReach calls
// it after every redraw of the strip, known or not, and refreshAvailForEntry
// below calls it on its own for a heartbeat that only moved the swarm
// reading and touched no file's claims at all.
//
// ok/warn/bad is the same three-way health judgement a reader already has to
// make from the number itself (SwarmAvailability's own doc: below 1.0 means
// pieces are missing, unavailable means some are held by nobody at all) -
// drawn here so it can be read at a glance, with the exact figure a hover
// away (availabilityCellTitle, the identical sentence the six live columns
// show) rather than lost by being reduced to a colour.
function renderAvail(fentry) {
  const el = fentry.availSwarm;
  if (!el) return;
  const entry = fentry.entry;
  const swarm = availabilityReading(entry);

  const health = !swarm ? "unknown" : swarm.unavailable > 0 ? "bad" : swarm.copies_per_piece < 1 ? "warn" : "ok";
  el.dataset.health = health;
  el.title = availabilityCellTitle(entry);
  fentry.availSwarmText.textContent = swarm ? swarm.copies_per_piece.toFixed(2) + "×" : ABSENT;
}

// refreshAvailForEntry re-draws every one of this entry's open file chips
// with a fresh swarm reading (events.js's "progress" case, through the view's
// renderSwarm hook). The
// reading is torrent-wide, not per-file (renderAvail's own doc), so every
// file card shares the identical figure and all of them redraw together
// rather than only the one file this particular heartbeat happened to name.
function refreshAvailForEntry(entry) {
  for (const fentry of entry.fileEntries.values()) renderAvail(fentry);
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
    renderReach(fentry, detail.sets);
  } catch (err) {
    logFor(entry, "could not read file " + index + "'s frames: " + (err.message || err));
  }
}

// ---------------------------------------------------------------------------
// THE FRAME, FULL SIZE (TOR-172): FIT, 100%, AND A PAN THAT CANNOT LEAK.
//
// The four rules the project owner stated, and where each one lives:
//
//   1. A picture that fits at 100% is drawn at 100%, and NEVER upscaled - a
//      small frame stays small. That is the 1 in layoutLightbox's
//      Math.min(1, ...): the fit factor is capped at 1, so a 320-wide frame
//      in a 1300-wide panel is drawn 320 wide, in a panel that shrinks to
//      meet it rather than a picture blown up to fill the panel.
//   2. A picture that does not fit is scaled down to fit. The same
//      Math.min(1, ...), taking the smaller of the two axes' ratios so the
//      whole of it lands inside the window.
//   3. A click goes to 100%, and then it pans - with the arrow keys, or by
//      moving the mouse. This is the COMMON case, not the rare one: frames
//      come out at the video's own resolution, so 1080 tall and up is the
//      ordinary size of the thing being opened, and the acceptance torrent's
//      own file is 1440x1080.
//   4. It never spills outside the panel. TWO mechanisms, deliberately both:
//      .lightbox-view's `overflow: hidden` makes spilling structurally
//      impossible whatever this script computes, and clampPan() below keeps
//      the panel's own ground from showing as a GUTTER at an edge - which is
//      the failure overflow: hidden cannot catch, because a picture panned
//      too far leaks background IN rather than picture out.
//
// THE PANEL'S BOX IS THE PICTURE'S FIT SIZE, in both zoom states. It is set
// once per picture and does not change when the zoom does, because a <dialog>
// is centred by the UA: a box that grows on a click re-centres the whole
// panel and slides the picture out from under the eye, which is the finding
// .compare's own block is built around. So zooming changes what is drawn
// inside the window, never the window.

// Everything the view knows. natW/natH are the picture's own pixels; boxW/
// boxH the window onto it (the fit size); drawW/drawH the size it is actually
// drawn at (fit, or 1:1); x/y the pan offset, which is never positive.
const lb = {
  natW: 0, natH: 0,
  fit: 1,
  zoomed: false,
  boxW: 0, boxH: 0,
  drawW: 0, drawH: 0,
  x: 0, y: 0,
};

// How far one arrow key moves the picture. A fixed number of pixels rather
// than a fraction of the overflow, so a press means the same thing on a
// picture half again the size of the window as on one ten times its size.
const LIGHTBOX_PAN_STEP = 64;

// Which way each arrow moves the WINDOW. Pressing Right looks further right
// into the picture, which slides the picture left - hence the subtraction in
// panBySteps rather than an addition.
const LIGHTBOX_ARROWS = {
  ArrowLeft: { dx: -1, dy: 0 },
  ArrowRight: { dx: 1, dy: 0 },
  ArrowUp: { dx: 0, dy: -1 },
  ArrowDown: { dx: 0, dy: 1 },
};

// availableBox is what the STYLESHEET leaves for the picture, in px, read off
// the element rather than recomputed here. .lightbox-view's max-width and
// max-height subtract the panel's ring, its two label strips and their gaps
// from 92vw/92vh; that arithmetic has exactly one home, in app.css, and this
// probe asks the browser what it came to: set an absurd size, read what it
// was clamped to. A later change to the panel's padding then needs no
// matching edit in this file - which is the whole reason not to write
// `window.innerWidth * 0.92 - 38` here and let the two drift apart.
function availableBox() {
  const view = el.lightboxView;
  view.style.setProperty("--lb-view-w", "100000px");
  view.style.setProperty("--lb-view-h", "100000px");
  const rect = view.getBoundingClientRect();
  return { w: rect.width, h: rect.height };
}

// layoutLightbox decides the panel's box and the picture's drawn size from
// the picture's own resolution and the room the stylesheet leaves. Safe to
// call more than once and at any time: it gives up quietly on a picture whose
// size is not known yet (the load event calls back), and it re-clamps the
// existing pan every time, which is what makes a window resize safe.
function layoutLightbox() {
  if (!el.lightbox.open) return;
  const img = el.lightboxImg;
  if (!img.naturalWidth || !img.naturalHeight) return;
  lb.natW = img.naturalWidth;
  lb.natH = img.naturalHeight;

  const avail = availableBox();
  // RULES 1 AND 2, IN ONE EXPRESSION. The two ratios are rule 2 - scale down
  // to fit, by whichever axis runs out first. The 1 is rule 1, and it is the
  // whole of "never upscale": without it a 320-wide frame would be drawn
  // 1300 wide and read as a blurry mistake.
  lb.fit = Math.min(1, avail.w / lb.natW, avail.h / lb.natH);
  // A panel measured mid-open can hand back a zero, and 0/0 is NaN, which
  // would propagate into the box and the clamp and pin the picture at a size
  // no arithmetic recovers from.
  if (!(lb.fit > 0)) lb.fit = 1;

  lb.boxW = Math.max(1, Math.round(lb.natW * lb.fit));
  lb.boxH = Math.max(1, Math.round(lb.natH * lb.fit));

  // A picture that already fits has nothing to zoom TO - rule 1 forbids
  // upscaling it - so the gesture is not offered at all, and the cursor says
  // so ("none"). It is also why this state does not wear the accent: nothing
  // is live when the whole picture is already in front of you.
  const zoomable = lb.fit < 1;
  if (!zoomable) lb.zoomed = false;

  const scale = lb.zoomed ? 1 : lb.fit;
  lb.drawW = Math.max(1, Math.round(lb.natW * scale));
  lb.drawH = Math.max(1, Math.round(lb.natH * scale));

  const view = el.lightboxView;
  view.style.setProperty("--lb-view-w", lb.boxW + "px");
  view.style.setProperty("--lb-view-h", lb.boxH + "px");
  // Whole pixels on both, from the SAME rounded expression in the fit case
  // (scale === lb.fit there), so the picture lands exactly on the window's
  // edges and no half-pixel seam of ground can show along one of them.
  img.style.width = lb.drawW + "px";
  img.style.height = lb.drawH + "px";

  el.lightbox.dataset.zoom = !zoomable ? "none" : lb.zoomed ? "full" : "fit";
  el.lightboxZoom.textContent = zoomable && !lb.zoomed ? "fit" : "100%";

  clampPan();
  applyPan();
}

// clampPan IS rule 4's second half, and the edges are where this gets got
// wrong. The picture's top-left sits at (x, y) inside the window, so the two
// bounds are: x <= 0, or a gutter of panel ground opens along the LEFT edge;
// and x >= boxW - drawW, or one opens along the RIGHT. Same for y, top and
// bottom. Nothing else may write lb.x/lb.y without coming through here.
function clampPan() {
  // Math.min(0, ...) on the far bound is what makes an axis with nothing to
  // pan collapse to one legal offset rather than invert its range: a picture
  // drawn NARROWER than the window has boxW - drawW > 0, and using that as a
  // lower bound would licence a positive x - a gutter down the left edge, at
  // the one size where the picture cannot cover it.
  const minX = Math.min(0, lb.boxW - lb.drawW);
  const minY = Math.min(0, lb.boxH - lb.drawH);
  lb.x = Math.min(0, Math.max(minX, lb.x));
  lb.y = Math.min(0, Math.max(minY, lb.y));
}

function applyPan() {
  el.lightboxImg.style.transform = "translate(" + lb.x + "px, " + lb.y + "px)";
}

// panFromPointer is "moving the mouse" read literally, which is what was
// asked for and what the panel is built around: the pointer's position inside
// the window MAPS to the pan offset, with no button held - the left edge of
// the window shows the picture's left edge, the right edge its right. It is a
// magnifier, not a drag. Press-and-drag is the alternative if this turns out
// unusable in the hand; it is not what the rules say, so it is not what is
// here.
function panFromPointer(event) {
  if (!lb.zoomed) return;
  const rect = el.lightboxView.getBoundingClientRect();
  const fx = rect.width > 0 ? (event.clientX - rect.left) / rect.width : 0;
  const fy = rect.height > 0 ? (event.clientY - rect.top) / rect.height : 0;
  // A fraction held to 0..1 times a span that is never positive lands inside
  // the legal range by construction - and it still goes through clampPan,
  // because rule 4 having ONE enforcement point is worth more than saving
  // two comparisons.
  lb.x = Math.round((lb.boxW - lb.drawW) * Math.min(1, Math.max(0, fx)));
  lb.y = Math.round((lb.boxH - lb.drawH) * Math.min(1, Math.max(0, fy)));
  clampPan();
  applyPan();
}

function panBySteps(dx, dy) {
  if (!lb.zoomed) return;
  lb.x -= dx * LIGHTBOX_PAN_STEP;
  lb.y -= dy * LIGHTBOX_PAN_STEP;
  clampPan();
  applyPan();
}

// A SECOND CLICK IS WHAT RETURNS IT TO FIT. The rules left that open; this is
// the answer, because the gesture that got you here is the one hand already
// on the mouse, and the cursor says which way it will go (zoom-in at fit,
// zoom-out at 100% - see app.css). Zooming in from a click also pans to where
// that click landed, so the region under the pointer is the region you get,
// rather than the picture's top-left corner.
function toggleLightboxZoom(event) {
  if (lb.fit >= 1) return;
  lb.zoomed = !lb.zoomed;
  if (!lb.zoomed) {
    lb.x = 0;
    lb.y = 0;
  }
  layoutLightbox();
  if (lb.zoomed && event && typeof event.clientX === "number") panFromPointer(event);
}

function openLightbox(src, caption) {
  // Every picture opens at fit, whatever the last one was left at, and with
  // no inline size or transform carried over from it - a stale transform on a
  // fresh picture shows it already panned for the frame or two before the
  // first layout runs.
  lb.zoomed = false;
  lb.fit = 1;
  lb.x = 0;
  lb.y = 0;
  el.lightboxImg.removeAttribute("style");
  el.lightbox.dataset.zoom = "fit";
  el.lightboxZoom.textContent = "fit";
  el.lightboxImg.src = src;
  el.lightboxCaption.textContent = caption;
  el.lightbox.showModal();
  // Twice on purpose: the load event is what fires for a picture arriving
  // over the wire, and this call is what covers one already decoded in the
  // cache - which is the usual case here, since the thumbnail just showed it.
  // layoutLightbox is written to be safe to call twice.
  layoutLightbox();
}

el.lightboxImg.addEventListener("load", layoutLightbox);
el.lightbox.addEventListener("close", () => {
  lb.zoomed = false;
  lb.x = 0;
  lb.y = 0;
  el.lightboxImg.removeAttribute("style");
});

// The zoom click is on the PICTURE, not on the window: the close button and
// the caption bar both sit over it and have to (see .lightbox-close's own
// comment, TOR-122), and a handler on the window would turn a click aimed at
// either of them into a zoom.
el.lightboxImg.addEventListener("click", toggleLightboxZoom);
el.lightboxView.addEventListener("pointermove", panFromPointer);
el.lightboxView.addEventListener("keydown", (event) => {
  // Only the window's own keys. Enter on the close button inside it fires
  // that button and keeps bubbling to here, which would zoom a panel on its
  // way shut.
  if (event.target !== el.lightboxView) return;
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    toggleLightboxZoom(null);
  }
});
el.lightboxClose.addEventListener("click", () => el.lightbox.close());
el.lightbox.addEventListener("click", (event) => {
  // A click that lands on the dialog element itself, rather than anything
  // inside it, is a click on the backdrop.
  if (event.target === el.lightbox) el.lightbox.close();
});
el.lightbox.addEventListener("keydown", (event) => {
  const step = LIGHTBOX_ARROWS[event.key];
  // Escape is deliberately not in that table: the dialog closes itself on it
  // and this handler must never take that away. Everything else belongs to
  // the page.
  if (!step) return;
  // preventDefault whether or not there is anything to pan. A modal <dialog>
  // does NOT stop the document behind it from scrolling, so an arrow key this
  // handler declines scrolls the page under the backdrop - and then closing
  // the panel leaves the reader somewhere they never asked to be.
  event.preventDefault();
  panBySteps(step.dx, step.dy);
});
// The fit depends on the window, so it has to be recomputed when the window
// changes - and the pan re-clamped with it, which layoutLightbox does last:
// widening the window shrinks the overflow, and an offset legal a moment ago
// would now be showing a gutter.
window.addEventListener("resize", () => {
  if (el.lightbox.open) layoutLightbox();
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
function renderFileLinks(fentry) {
  const links = fentry.sheetURL ? [link(fentry.sheetURL, "contact sheet")] : [];
  fentry.links.replaceChildren(...links);
  fentry.links.hidden = links.length === 0;
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
function renderFileDone(fentry) {
  renderFileProgress(fentry);
  renderFileLinks(fentry);
  loadFileDetail(fentry.entry, fentry, fentry.index);
}

function link(href, text) {
  const a = document.createElement("a");
  a.href = url(href);
  a.textContent = text;
  a.target = "_blank";
  a.rel = "noopener";
  return a;
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
// condition - see the block's own comment in the template above.
//
// IT IS NOT CALLED FROM syncEntry, deliberately, even though it reads nothing
// but state: it clears entry.torrentNote, and syncEntry runs on every event -
// so "sent to /path/to/watch" would be wiped a moment after it appeared. Its
// two callers are the two moments the link itself can change: the run's own
// done event, and a reset (both events.js).
function renderTorrent(entry) {
  entry.torrentSave.hidden = !entry.torrentURL;
  entry.torrentNote.textContent = "";
  if (!entry.torrentURL) {
    entry.torrentSend.hidden = true;
    entry.torrentActions.hidden = true;
    return;
  }
  entry.torrentSave.href = url(entry.torrentURL);
  entry.torrentSend.hidden = !state.watch;
  entry.torrentActions.hidden = !state.watch;
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
function refreshStallDurations() {
  const now = Date.now();
  for (const entry of state.runs.values()) {
    if (!entry.stall && !waitingForMetadata(entry)) continue;
    entry.rowMeta.textContent = metaLabel(entry, now);
    entry.rowMeta.title = metaTitle(entry, now);
  }
}
setInterval(refreshStallDurations, 1000);

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
    for (const entry of state.runs.values()) {
      entry.torrentSend.hidden = !state.watch || !entry.torrentURL;
      entry.torrentActions.hidden = entry.torrentSend.hidden;
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
  setRunExpanded(entry, true);
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
el.count.addEventListener("input", () => {
  for (const entry of state.runs.values()) {
    if (!entry.pickerEl.hidden) updateFileCosts(entry);
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
// Column widths: a drag-a-border-and-remember-it pattern, once per column -
// one localStorage entry holding a { key: px, ... } map rather than nine
// separate ones, since a stale-column check (below) needs to see the whole
// set at once to decide what to drop.
//
// Every key here is a column's own data-sort value - el.sortHeaders already
// is exactly the nine resizable headers (name/when/status plus the six
// LIVE_COLUMNS keys; the unlabelled actions column has no data-sort and
// keeps the plain 3.8rem app.css always gave it, see .run-actions-header) -
// so this reuses it rather than keeping a second list of column names that
// could drift from the first, the same reason LIVE_COLUMNS itself is one
// list rather than two.
//
// localStorage is read through a try/catch on purpose, same as the panel
// width above: it throws in a private window or with site data blocked, and
// a stored value can also simply be malformed JSON from a build that wrote
// it differently. Either way the page must still render at the defaults
// app.css declares (--col-w-name and its siblings) rather than break.
const COLUMN_WIDTHS_KEY = "torpeek.columnWidths";
const COLUMN_MIN_WIDTH = 44;
const COLUMN_MAX_WIDTH = 640;

function clampColumnWidth(px) {
  return Math.min(COLUMN_MAX_WIDTH, Math.max(COLUMN_MIN_WIDTH, px));
}

function resizableColumnKeys() {
  return Array.from(el.sortHeaders, (th) => th.dataset.sort);
}

// A column that no longer exists - the table shipped fewer or differently-
// named columns when the value was stored - is dropped rather than kept:
// nothing on the current page would ever read it, and rendering the OTHER
// columns from a partly-stale map is still exactly the graceful fallback the
// acceptance criterion asks for. Malformed JSON, a non-object, or a width
// that doesn't parse as a finite number all fall back to the same empty map,
// which is indistinguishable from "nothing was ever stored" - the table then
// simply renders at app.css's own defaults for every column.
function loadColumnWidths() {
  try {
    const raw = localStorage.getItem(COLUMN_WIDTHS_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return {};
    const known = new Set(resizableColumnKeys());
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

function saveColumnWidths(widths) {
  try {
    localStorage.setItem(COLUMN_WIDTHS_KEY, JSON.stringify(widths));
  } catch (err) {
    // Best-effort only - the default widths still work.
  }
}

// Mirrors applyPanelWidth: one custom property per column, on :root, so the
// header's own inline width (set once below, as `var(--col-w-KEY)`) picks
// up every later drag without needing to be touched again itself.
function applyColumnWidth(key, px) {
  document.documentElement.style.setProperty("--col-w-" + key, px + "px");
}

// columnWidths holds only the entries a drag (or a valid stored value) has
// actually produced - same shape loadPanelWidth's null-until-touched state
// has, kept as a map here instead of a single value because saving has to
// write back the whole set, not just the one column that just moved.
const columnWidths = loadColumnWidths();
for (const [key, px] of Object.entries(columnWidths)) applyColumnWidth(key, px);

// Every sortable header gets its width from the matching --col-w-* token
// (app.css declares the defaults; the loop above already overrode any that
// were stored) and a drag handle at its own right edge - the border between
// it and the next column. The actions header is deliberately excluded: it
// is not in el.sortHeaders (no data-sort), so its width stays the plain
// 3.8rem app.css gives .run-actions-header and it grows no handle of its
// own, since there is no column past it for a border to belong to.
for (const th of el.sortHeaders) {
  const key = th.dataset.sort;
  th.style.width = "var(--col-w-" + key + ")";

  const handle = document.createElement("span");
  handle.className = "col-resizer";
  handle.setAttribute("aria-hidden", "true");
  th.append(handle);

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
  // pointerdown: the handle sits inside a <th> that is itself a sort
  // control (el.sortHeaders' own click listener, wired above), and without
  // this a drag - or even a plain click that lands on the handle - would
  // bubble up and also reorder the table, the same trap TOR-140's ▲/▼
  // buttons stopPropagation against so a reorder did not also toggle the
  // accordion.
  handle.addEventListener("pointerdown", (event) => {
    if (event.button !== undefined && event.button !== 0) return;
    event.stopPropagation();
    event.preventDefault();
    drag = { startX: event.clientX, startWidth: th.getBoundingClientRect().width };
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
    columnWidths[key] = width;
    applyColumnWidth(key, width);
  });

  function endColumnDrag(event) {
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
    saveColumnWidths(columnWidths);
    // TOR-174: see syncRunDetailWidth's own comment for why a column drag,
    // which changes the TABLE's width rather than the pane's, still gets a
    // recheck here.
    syncRunDetailWidth();
  }
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

// ---------------------------------------------------------------------------
// THE WIRING (TOR-191), and it sits here rather than at the top of the file
// for a reason that can be checked rather than trusted: nothing above it
// reaches the store or the socket. The header build, the element lookups, the
// sort and drag listeners, the intake handlers, the drop handlers and the
// column widths are every top-level statement in this file, and not one of
// them calls ensureRun or connect - so registering at the last possible
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
setView({
  log: logFor,
  note: log,
  status: setStatus,
  socketURL: socketAddress,
  syncEntry,
  resetRunView,
  rebuildFileList: renderFileList,
  renderFileMeta,
  renderFrames: renderFileGrid,
  renderFileProgress,
  renderFileDone,
  renderSwarm: refreshAvailForEntry,
  renderTorrent,
});

updateSortIndicators();
loadDefaults();
loadRuns();
connect();

