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

// ---------------------------------------------------------------------------
// THE SIX LIVE COLUMNS (TOR-139): peers, seeds, both rates, availability and
// queue position, added to the table TOR-137 moved into the right pane and
// TOR-138 made expandable.
//
// ABSENT IS NOT ZERO, everywhere in this block. GET /runs' "live" object
// (listing.go's Live) is present only for a row with an actual client that
// has spoken at least once, and even then download_bps/upload_bps/swarm can
// still be individually absent (before the run's second heartbeat, or before
// the swarm has answered what it holds). A queued row and a running row with
// nobody connected are opposite situations, and rendering both as "0" would
// make them read the same - so every cell below reads ABSENT, never a
// literal 0, whenever the reading itself is missing rather than measured.
const ABSENT = "—"; // em dash

// LIVE_COLUMNS builds both the header cells (below) and, by the same keys,
// the sortValue() switch further down - one list rather than two, so a
// column added here cannot forget to be wired into sorting or the reverse.
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
};

// Every torrent this page knows about lives here, keyed by run id - or, for a
// run known only from disk (GET /runs found it, but this process never
// minted an id for it), by a synthetic "disk:<infohash>:<params>" key until
// it is reopened. Nothing is ever destroyed wholesale any more: a second
// torrent must not erase the first, and a finished one must stay clickable
// for as long as the page remembers it.
//
// There is no state.selected any more (TOR-138). Selection was the singleton
// detail pane's own idea - one entry shown on the right, everything else
// hidden - and the accordion has no single slot to be the one thing in. Which
// rows are open is now a per-entry flag (entry.expanded, changed only by
// setRunExpanded), exactly as a file's own accordion has carried its state on
// its fentry since TOR-63. Keeping it on the object rather than in a set of
// ids also removes a whole class of bug for free: claimReopenedRun swaps an
// entry's KEY when a disk row is reopened under a fresh run id, and the old
// code had to remember to move state.selected across with it.
//
// sort is the table's current order: key names the column (a <th data-sort>
// value), dir is "asc" or "desc". The default - date, newest first - is what
// the panel already showed before it became a table (TOR-62).
// watch says whether this server was started with a watch directory
// (-watch-dir), which GET /defaults answers. It gates one button and nothing
// else: without a watch directory the button is absent rather than present
// and failing, so the page has to be told before it draws one. False until
// loadDefaults answers, which is the safe way round - a button that appears a
// moment late is better than one that is there and cannot work.
const state = { runs: new Map(), watch: false, sort: { key: "when", dir: "desc" } };

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

// stallDuration is "how long" for a stall reading - the load-bearing part of
// TOR-141's own acceptance criterion. Distinct from seconds() above: that one
// prints a single measurement to a tenth of a second ("21.3s"), useful for a
// run's own total elapsed time; this is meant to be read at a glance while it
// keeps climbing, so it drops the fraction and grows a minutes field rather
// than ever showing something like "812.4s".
function stallDuration(ms) {
  const total = Math.max(0, Math.floor(ms / 1000));
  if (total < 60) return total + "s";
  const m = Math.floor(total / 60);
  const s = total % 60;
  return m + "m " + String(s).padStart(2, "0") + "s";
}

// STALL_REASON gives each of the causes core.ErrorCode can name on a stall
// reading (core.Progress.Stall, wire.go's "stall" key) a short phrase in a
// person's own words, rather than the bare wire code - the same vocabulary
// errors.go's own doc comments use for each one, kept short enough to sit
// under a badge. An unrecognised code (there should never be one; core's own
// classifyPointFailure/classifyLiveStall only ever produce these four) falls
// back to the bare code rather than hiding the reading entirely.
const STALL_REASON = {
  no_peers: "no peers connected",
  unavailable: "peers connected, but nobody holds this yet",
  no_metadata: "no metadata yet",
  read_stalled: "a read timed out",
};

// stallPhrase renders one stall reading as the row's own status line reads
// it: which cause, for how long. now is the caller's current clock (Date.now()
// by default) rather than always "right now" internally, so the same
// function drives both a fresh render and the ticking refresh below
// (refreshStallDurations) off one consistent instant per pass, rather than
// each row computing its own now() microseconds apart.
function stallPhrase(stall, now) {
  now = now == null ? Date.now() : now;
  // observedAt is stamped by apply()'s "progress" case the moment this
  // reading arrived (Date.now() at receipt, never the server's own clock,
  // which this page has no synchronised way to compare against) - since_ms
  // is only ever as fresh as that last heartbeat, and adding the wall time
  // since then is what keeps the displayed duration ticking up smoothly
  // between heartbeats (at most stallHeartbeatInterval, 5s, apart) instead
  // of visibly standing still and then jumping.
  const liveMS = stall.since_ms + Math.max(0, now - stall.observedAt);
  return (STALL_REASON[stall.code] || stall.code) + " for " + stallDuration(liveMS);
}

// waitingForMetadata is true for the one stall-shaped state the engine
// cannot yet report a Stall reading for at all (TOR-141's own scope note):
// the run is running, but no client has spoken - not even once, per hasLive
// - and no name has been confirmed either. There is no torrent object to
// read peers from during this phase (pool.Attach is still resolving it), so
// this is computed here, client-side, from what the row already carries
// rather than invented on the wire - and it clears itself the instant either
// a name or a live reading arrives, which is the same run_state/progress
// traffic that already flows regardless.
function waitingForMetadata(entry) {
  return entry.state === "running" && !entry.name && !hasLive(entry) && !entry.stall;
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
// TOR-141: a stall reading (or, before there is even a torrent to read
// peers from, waitingForMetadata) takes priority over the plain frame
// ratio. Distinguishable from a run that is merely slow is this ticket's own
// acceptance criterion, and showing "4/20 frames" unchanged while a run sits
// stalled is exactly the failure it names - a slow run's ratio keeps
// climbing, a stalled one's would not, but nothing about the TEXT says so.
// now, when passed, is the caller's current clock (refreshStallDurations'
// own ticking redraw); omitted, both branches fall back to Date.now().
function metaLabel(entry, now) {
  if (entry.stall) return stallPhrase(entry.stall, now);
  if (waitingForMetadata(entry)) {
    return "waiting for metadata, " + stallDuration((now == null ? Date.now() : now) - entry.runningSince);
  }
  if (entry.progress) return entry.progress;
  if (entry.disk) return entry.complete + "/" + entry.selected;
  if (entry.error) return entry.error;
  return "";
}

// The long form, on hover, for the row whose label was shortened.
function metaTitle(entry, now) {
  if (entry.stall) {
    const base = "stalled: " + (STALL_REASON[entry.stall.code] || entry.stall.code);
    return entry.progress ? base + " (" + entry.progress + " so far)" : base;
  }
  if (waitingForMetadata(entry)) {
    return "still waiting for the torrent's own metadata - no peer has answered yet, " +
        "or none has offered the file list";
  }
  if (entry.disk) return entry.complete + " of " + entry.selected + " file(s) complete";
  return metaLabel(entry, now);
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

// applyFrameProgress sets entry.framesDone/entry.framesTotal - and the
// "N/M frames" line (entry.progress) that sits above the bar and must never
// disagree with it - from whichever file (ev.file) just reported something,
// on file_started, frame_ready, frame_skipped and progress alike (TOR-167).
//
// WHY NOT JUST READ core.Progress's OWN frames_done, which is what this used
// to do: that heartbeat counts only frames THIS RUN captured fresh. A top-up
// mostly REPLAYS frames an earlier run already wrote to disk (TOR-166's own
// pricing depends on that reuse being free), and engine.go's processFile
// publishes a frame_ready for a reused point but no Progress heartbeat for
// it - only real captures get one. So a top-up whose two still-missing
// points fall early in the plan can report "1 of 20" from its very first
// heartbeat and never correct itself: every point after that is a replay,
// which never heartbeats again, while the file quietly finishes at 20 of 20
// on disk. The grid beside the bar does not have this gap, because
// frame_ready fires for a landed point whether it was replayed or freshly
// captured, and the grid is built from exactly that stream (addFrame).
//
// So this reads the same stream the grid already reads - the count of cells
// with a frame, gridCells' own "cell.url" test, identical to what
// updateFileSummary already shows under the grid - rather than the sparser
// heartbeat. That is not a new measurement: it is still "frames landed out
// of frames planned" (TOR-123's own definition), taken from the signal that
// never misses a landed frame instead of the one that only speaks when this
// run did the work itself. wireDone, core.Progress's own frames_done when
// this call came from a progress event, is folded in with Math.max rather
// than trusted alone or discarded - it can never be fewer than what the grid
// has already proven landed, but nothing stops it being the fresher of the
// two on a heartbeat that arrives between two frame_ready events.
function applyFrameProgress(entry, file, wireDone) {
  const fentry = entry.fileEntries.get(file);
  if (!fentry || !fentry.plan.length) return;

  const landed = gridCells(fentry).filter((cell) => cell.url).length;
  entry.framesTotal = fentry.plan.length;
  entry.framesDone = Math.max(landed, wireDone || 0);
  if (cancellable(entry.state)) {
    entry.progress = entry.framesDone + "/" + entry.framesTotal + " frames";
  }
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

// hasLive is the one question every one of the six live columns asks first:
// does this row have a client that has spoken at all. entry.live is null
// until it does (loadRuns copies GET /runs' own "live" object, the
// "progress" case in apply() below keeps it current), never a zeroed
// stand-in - see this block's own opening comment.
function hasLive(entry) {
  return !!entry.live;
}

// absentReason is the title text for a peers/seeds/rate/availability cell
// that has nothing to show, so a person who does wonder why gets an answer
// that matches what the cell actually knows rather than a bare dash.
function absentReason(entry) {
  if (entry.disk) return "no reading - this row was read off disk, never a live client";
  if (!hasLive(entry)) return "no reading yet - this torrent has no client (queued, needs-action, or not yet started)";
  return "";
}

function peersCellText(entry) {
  return hasLive(entry) ? String(entry.live.peers) : ABSENT;
}
function peersCellTitle(entry) {
  return hasLive(entry) ? entry.live.peers + " connected peer(s)" : absentReason(entry);
}

function seedsCellText(entry) {
  return hasLive(entry) ? String(entry.live.seeds) : ABSENT;
}
function seedsCellTitle(entry) {
  return hasLive(entry) ? entry.live.seeds + " connected seed(s)" : absentReason(entry);
}

// rateCellText/rateCellTitle serve both the download and upload columns -
// bps is entry.live.download_bps or entry.live.upload_bps, already null
// (never 0) when this heartbeat has nothing to report, per Live's own doc.
function rateCellText(bps) {
  return bps == null ? ABSENT : bytesLabel(bps) + "/s";
}
function rateCellTitle(entry, bps, label) {
  if (bps != null) return label + ": " + bytesLabel(bps) + "/s";
  if (!hasLive(entry)) return absentReason(entry);
  return "no reading yet - a rate needs an interval between two heartbeats, so it is absent until this run's second one";
}

function availabilityReading(entry) {
  return hasLive(entry) ? entry.live.swarm : null;
}
function availabilityCellText(entry) {
  const s = availabilityReading(entry);
  return s ? s.copies_per_piece.toFixed(2) + "×" : ABSENT;
}
// availabilityMetaText is deliberately shorter than the title
// (availabilityCellTitle) that carries the full "N of M pieces" sentence:
// measured in a real browser at this column's width, "N of M unavailable"
// wraps to three lines and makes the row taller than every other one, which
// is worse than the information it was trying to fit. "N missing" is what
// actually stays legible on a single line - the total is one hover away.
function availabilityMetaText(entry) {
  const s = availabilityReading(entry);
  return s ? s.unavailable + " missing" : "";
}
function availabilityCellTitle(entry) {
  const s = availabilityReading(entry);
  if (s) {
    return s.copies_per_piece.toFixed(2) + " copies per piece, on average, across the swarm - not a " +
      "percentage. " + s.unavailable + " of " + s.pieces + " pieces are held by no connected peer.";
  }
  if (!hasLive(entry)) return absentReason(entry);
  return "no reading yet - this torrent has not been asked for bytes, so nothing has reported what the swarm holds";
}

// ---------------------------------------------------------------------------
// THE QUEUE COLUMN, AND THE ONE PLACE ITS POSITION COMES FROM (TOR-140).
//
// TOR-139 had to DERIVE this. GET /runs reported no priority and no position,
// the queue was strict FIFO over arrival, and so ranking every queued row by
// its own reported queued time recovered the server's order exactly - a
// queueRank() function that lived here and answered from state.runs.
//
// It is gone, and deliberately not kept alongside the server's answer. The
// moment a queue can be REORDERED, arrival time stops predicting position,
// and a page holding its own derivation would draw one order while the server
// dispatched another - with nothing on screen to say which was real. So the
// position is now a fact the server reports (listing.go's
// RunSummary.QueuePosition, and the same field on every run_state that
// concerns a waiting row) and this file renders it. There is exactly one
// notion of queue position in the codebase, and it is not this one.
//
// Absent (0 or missing) means this row is not waiting for a slot at all -
// running, parked for a file selection, finished, or read off disk - and
// answers null, the same ABSENT IS NOT ZERO rule the five live figures
// follow: position 0 is not a place in a 1-based queue.
function queuePosition(entry) {
  return entry.queuePosition > 0 ? entry.queuePosition : null;
}

// ---------------------------------------------------------------------------
// TWO FACTS IN ONE COLUMN, AND WHICH OF THEM IS THE FIGURE (TOR-156).
//
// The owner asked for "в каком порядке торренты добавлены" - the order
// torrents were added - after watching 1.2.0 with the queue width at 5
// (TOR-149), where almost nothing ever queues and so the column read as a
// feature that does not work. TOR-140 had built the column honestly for what
// it meant: a queue POSITION, set only on the rows the queue still has
// something to say about, an em dash everywhere else.
//
// The trap this ticket is written around is that those are TWO facts which
// look like one number:
//
//   - ARRIVAL ORDINAL: "the third torrent you added". True for the life of
//     the row, unmoved by priority, still meaningful on a row that finished
//     an hour ago. This is the one the owner named.
//   - QUEUE POSITION: "one ahead of you". Exists only while waiting, and
//     moves whenever anything ahead finishes or a priority changes.
//
// TOR-153 had just been bitten by the mirror image of this - two bars saying
// the same thing - so shipping these two as two numbers in two columns would
// have repeated it from the other side. THE DECISION, and the reason it is
// written here rather than only in the ticket:
//
//   - ONE COLUMN carries both, in the two-line shape TOR-140 already gave
//     this very cell (a figure, and a muted second line under it). Nothing
//     grows a column, and nothing has to be sorted twice.
//   - THE ORDINAL IS THE FIGURE. It is the fact that is never absent, so it
//     is the one that fixes the empty column; it is the fact the owner asked
//     for; and it is the fact that means something on the finished rows,
//     which are most of the table.
//   - THE POSITION IS THE SECOND LINE, marked ("#2"), present only while it
//     applies. Marked because two bare numbers stacked in one narrow cell is
//     precisely the confusion this ticket exists to avoid - the figure needs
//     nothing under a header that says Queue, the one that is only sometimes
//     there does. See queueCellMetaText for why it is "#2" and not the
//     spelled-out "queue 2" it started as.
//   - THE ADDED COLUMN STAYS A TIMESTAMP. It is not redundant with the
//     ordinal and the ordinal is not redundant with it: a date answers
//     arrival order only RELATIONALLY, by comparing rows, and only under the
//     default sort - sorted by peers, the timestamps scatter and nothing on a
//     row says it was the third. A cardinal number reads the same whatever
//     order the rows are in, which is TOR-140's own argument for a priority
//     LEVEL over a dragged position, applied to the same table one column
//     over.
//
// What was considered and rejected: giving the ordinal to the Added column
// (two numbers in two places again, and it would make a date column carry
// something that is not a date), and dropping the position (it is TOR-140's
// own acceptance criterion - the person who reorders a queue has to be able
// to see the order - and no other cell answers "how long until mine starts").
//
// Absent means one thing only: a row this session never gave a number,
// i.e. one read off disk from an earlier run of the server (see
// RunSummary.Arrival for why the counter cannot honestly outlive its
// process). Never "not waiting" - that is what the second line says by
// staying empty.
function arrivalOrdinal(entry) {
  return entry.arrival > 0 ? entry.arrival : null;
}

// The three levels runs.go's Priority declares, mirrored here because the
// two buttons have to know what the ends of the band are to disable
// themselves there. Widening the band is a change in both places, which is
// why the names match exactly.
const PRIORITY_LOW = -1;
const PRIORITY_NORMAL = 0;
const PRIORITY_HIGH = 1;

// hasPriority is "does the queue still have anything to say about this row",
// which is the server's own question (RunState.queueable) answered by the
// presence of the field rather than re-derived from entry.state here. A
// running or finished row carries no priority at all - not a zero - so this
// is a null check, and it is what gates whether the row gets controls.
function hasPriority(entry) {
  return entry.priority === PRIORITY_LOW || entry.priority === PRIORITY_NORMAL || entry.priority === PRIORITY_HIGH;
}

function priorityLabel(priority) {
  if (priority > PRIORITY_NORMAL) return "high";
  if (priority < PRIORITY_NORMAL) return "low";
  return "normal";
}

function queueCellText(entry) {
  const arrival = arrivalOrdinal(entry);
  return arrival === null ? ABSENT : String(arrival);
}

// The second line, in the same muted subtitle the availability cell uses.
//
// It carries the WAITING POSITION while there is one, and otherwise the
// priority level when that is not the default - which is TOR-140's own rule
// for this line, kept for the one row it still applies to: a parked torrent
// holds a level and no position (it rejoins the queue when someone picks its
// files, server.go's DecideRun).
//
// "#2" RATHER THAN "queue 2", AND THE LEVEL LEFT OFF, both for one measured
// reason: this line does not wrap, it ELLIPSISES. .run-cell-queue-meta is
// white-space: nowrap with text-overflow: ellipsis (app.css, TOR-140's own
// rule, so that a long level could not make the row taller than every other
// one), and at --col-w-priority (3.4rem) the line holds about seven
// characters. Measured in a real browser: "queue 2" fits exactly, at 54px of
// 54px; "queue 12" needs 62px and comes out as "queue 1…".
//
// A truncated position is not a cosmetic problem, which is what makes this
// the deciding argument rather than a preference. Every other clipped label
// on this page loses letters a person can guess at; this one would lose a
// DIGIT and leave behind another position that is entirely plausible - a row
// waiting twelfth reading as first, with an ellipsis as the only sign, and
// "first" is the one value that also means "this starts next". So the format
// is the one that cannot run out of room: "#" and the number, four characters
// at three digits.
//
// The level goes the same way. "#2 high" is already eight, and the ▲/▼ pair
// beside the cell says it without a word - the button at the end of the band
// is disabled, so high and low are both visible at a glance - with the full
// sentence in this cell's own title. TOR-140 made the opposite trade for the
// availability cell's "N of M unavailable" and recorded the same measurement:
// the fact that MOVES is worth the line, the one a control already shows is
// not.
function queueCellMetaText(entry) {
  const position = queuePosition(entry);
  if (position !== null) return "#" + position;
  if (!hasPriority(entry) || entry.priority === PRIORITY_NORMAL) return "";
  return priorityLabel(entry.priority);
}

function queueCellTitle(entry) {
  const arrival = arrivalOrdinal(entry);
  const position = queuePosition(entry);

  // Both facts, always named apart, and the ordinal first because it is the
  // figure on the row. A tooltip is where the distinction the whole ticket
  // rests on can be spelled out at length, which two digits in a 3.4rem cell
  // cannot do for themselves.
  const parts = [];
  if (arrival !== null) {
    parts.push("added " + ordinalWord(arrival) + " to this server - a number that never changes");
  } else {
    parts.push("no arrival number: this row was read off disk, from a run of the server before this one");
  }
  if (position !== null) {
    parts.push("waiting at position " + position + " of the queue the server actually holds, at " +
      priorityLabel(entry.priority) + " priority" +
      (position === 1 ? " - this is the next torrent to start" : ""));
  } else if (hasPriority(entry)) {
    // A parked torrent: it has a level, and it will re-enter the queue with
    // it the moment someone picks files (server.go's DecideRun).
    parts.push("not in the queue while it waits for a file selection - it will rejoin at " +
      priorityLabel(entry.priority) + " priority");
  } else if (!entry.disk) {
    parts.push("not waiting for a slot");
  }
  return parts.join(". ");
}

// ordinalWord turns 3 into "3rd", for the one place a sentence reads better
// than a bare figure (the cell itself stays a bare figure - .run-cell-metric
// is a column of numbers meant to be compared straight down, and "3rd" in it
// would break the tabular alignment every other metric cell keeps).
function ordinalWord(n) {
  const tens = n % 100;
  if (tens >= 11 && tens <= 13) return n + "th";
  switch (n % 10) {
    case 1: return n + "st";
    case 2: return n + "nd";
    case 3: return n + "rd";
    default: return n + "th";
  }
}

function sortValue(entry, key) {
  switch (key) {
    case "name": return displayName(entry).text.toLowerCase();
    case "status": return badgeLabel(entry).toLowerCase();
    // The five live figures and the queue position are null, never 0, the
    // moment their reading is absent (hasLive, queuePosition) - see compareEntries
    // for what that null is FOR: an absent row is not "the lowest value", it
    // is excluded from the comparison entirely and sinks to the end.
    case "peers": return hasLive(entry) ? entry.live.peers : null;
    case "seeds": return hasLive(entry) ? entry.live.seeds : null;
    case "download_bps": return hasLive(entry) && entry.live.download_bps != null ? entry.live.download_bps : null;
    case "upload_bps": return hasLive(entry) && entry.live.upload_bps != null ? entry.live.upload_bps : null;
    case "availability": {
      const s = availabilityReading(entry);
      return s ? s.copies_per_piece : null;
    }
    // Sorting the Queue column sorts by the ARRIVAL ORDINAL, because that is
    // the figure the column shows (TOR-156) - a header that sorted by the
    // second line would reorder the table by a number most rows do not have.
    // Ascending is therefore the order the torrents were added, which is what
    // the owner asked this column for; the rows still waiting keep their
    // relative order within it unless somebody has reprioritised them, and
    // the second line is where that shows.
    //
    // Until TOR-156 this returned queuePosition(entry), which was right while
    // the position was what the cell displayed. The two must not disagree:
    // the column that sorts by one number and prints another is unreadable in
    // exactly the way a person only discovers after trusting it.
    //
    // Absent - a disk row, which this session never numbered - sinks to the
    // end like every other absent reading (see compareEntries).
    case "priority": return arrivalOrdinal(entry);
    case "when":
    default: return entry.when || 0;
  }
}

// compareEntries' first job, before it compares anything: decide where a row
// with nothing to say goes. An absent value is not zero and must not sort as
// though it were - the trap the ticket names directly, that a column where
// most rows are absent would otherwise bury a running torrent with a real
// zero reading among the queued rows that have none at all, which is exactly
// backwards. The decision made here is that absent rows sink to the END of
// EVERY sort, ascending or descending alike - unconditionally, before dir is
// ever consulted, so a person clicking a header to sort by "most peers" and
// then again for "fewest peers" finds the running-but-friendless torrents at
// one end both times, and the never-had-a-client rows at the other,
// consistently. name/status/when never produce a null/undefined sortValue,
// so this is a no-op for the three columns that existed before TOR-139.
function compareEntries(a, b) {
  const { key, dir } = state.sort;
  const va = sortValue(a, key);
  const vb = sortValue(b, key);

  const aAbsent = va === null || va === undefined;
  const bAbsent = vb === null || vb === undefined;
  if (aAbsent || bAbsent) {
    if (aAbsent && bAbsent) return 0;
    return aAbsent ? 1 : -1;
  }

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
// region it opens, and there are now as many regions as there are torrents.
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
  // The same disclosure triangle a file block wears one level down, for the
  // same reason: an accordion that gives no sign it opens is a table.
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
  detailCell.append(detailEl);

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
    // live is GET /runs' own "live" object (listing.go's Live) for this row -
    // null, not a zeroed struct, until the run actually has a client that has
    // spoken at least once (loadRuns copies row.live; the "progress" case in
    // apply() below keeps it current for the rest of the page's life). Every
    // one of the six live columns (TOR-139) reads through hasLive()/this
    // field rather than defaulting any piece of it to 0 - see that block's
    // own opening comment for why.
    live: null,
    // stall is TOR-141's own reading (core.Progress.Stall, wire.go's "stall"
    // key on the progress event) - which distinct cause currently explains
    // no progress, and how long, plus observedAt, THIS page's own wall clock
    // reading stamped the moment the reading arrived (never the server's
    // clock, which this page cannot compare against). Null, not a stale
    // object, whenever the run is progressing or has never run at all - the
    // same "absent, not zero" shape live above already follows, and cleared
    // at exactly the same moments entry.progress is (see the run_state and
    // terminal-event handling in apply()).
    stall: null,
    // runningSince is stamped in local wall-clock time the moment this row
    // is FIRST seen in the "running" state (the run_state handler below) -
    // used only to compute waitingForMetadata's own duration, since there is
    // no torrent yet during that phase for a Stall reading to ride on.
    runningSince: 0,
    // priority is the queue level the server reports for this row (runs.go's
    // Priority, TOR-140) and queuePosition its 1-based place in the queue.
    // null and 0 mean the same thing they mean on the wire: this row is not
    // one the queue has anything to say about - never "normal" and never
    // "position zero", which is why the first is null rather than 0 (see
    // hasPriority, and RunSummary.Priority's own doc for why one field is a
    // pointer server-side and the other is not).
    priority: null,
    queuePosition: 0,
    // arrival is "this was the Nth torrent added to this server" (TOR-156) -
    // the figure the Queue column shows. 0 means this row has none, which
    // happens for exactly one kind of row: one read off disk, from a run of
    // the server before this one. Unlike the two fields above it never
    // changes and never goes away once set, which is why the two readers
    // below keep the value they have rather than clearing it when a message
    // arrives without one.
    arrival: 0,
    // when is this row's sort key for the default (date, newest-first) sort.
    // Set once, here, at creation - never touched again by a status update -
    // which is what keeps a live run from jumping position as events arrive.
    // loadRuns() overwrites it once with the authoritative value GET /runs
    // reports, for a row it already knows about at page load.
    when: Date.now(),
    reopening: false,
    // TOR-152. topup is GET /runs/{infohash}/topup's own answer for this
    // row - what finishing this result set would be allowed to spend, and
    // which ceiling stopped it - or null for a row that has not been asked
    // about (nothing has finished yet) or has nothing to finish. Null rather
    // than a zeroed object, the same "absent, not zero" rule live and stall
    // already follow: an offer of 0 bytes is a real answer (no raise would
    // help) and must not read the same as "not asked".
    topup: null,
    // againKey is the (torrent, set, state, completeness) this row last
    // asked about, so syncEntry can call refreshAgain on every event
    // without the page re-fetching the same answer per frame.
    againKey: "",
    // claiming is reopening's counterpart for a run started FROM this row -
    // a top-up or a retry. Both mint or re-arm a run whose id the socket may
    // announce before the POST settles, and resolveIncomingRun folds that id
    // into this row rather than spawning a second one for the same torrent
    // (TOR-140: one torrent, one row).
    claiming: false,
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
    // Whether this torrent's detail is on screen (TOR-138). Changed only
    // inside setRunExpanded, which is the same discipline setFileExpanded and
    // setMetaExpanded keep one and two levels down: no event that arrives for
    // this run may open or close it, so a person's click cannot be undone
    // from under them by a frame landing.
    expanded: false,
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
    pickerNone: detailEl.querySelector(".picker-none"),
    pickerGo: detailEl.querySelector(".picker-go"),
    pickerCost: detailEl.querySelector(".picker-cost"),
    filesEl: detailEl.querySelector(".files"),
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

  entry.pickerAll.addEventListener("click", () => setAllPicked(entry, true));
  entry.pickerNone.addEventListener("click", () => setAllPicked(entry, false));
  entry.pickerGo.addEventListener("click", () => decide(entry));
  entry.torrentSend.addEventListener("click", () => sendTorrent(entry));
  entry.againGo.addEventListener("click", () => topUpRun(entry));
  entry.againRetry.addEventListener("click", () => retryRun(entry));

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
  // A reconnecting page is about to replay this run's history from the
  // start, the same as every other field cleared here - a stall reading
  // from before the reset would otherwise sit stale on screen until the
  // replay's own next progress event happens to overwrite it.
  entry.stall = null;
  entry.runningSince = 0;
  // Same reasoning as the stall reading just above: entry.fileEntries is
  // gone (cleared two lines up), so applyFrameProgress has nothing to read
  // until the replay's own file_started rebuilds it - these three would
  // otherwise sit at whatever the ended run last reported and let
  // renderRunProgress draw a bar for a file the page no longer has anything
  // about, however briefly, before that happens (TOR-167).
  entry.progress = "";
  entry.framesDone = 0;
  entry.framesTotal = 0;
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
  // defined beside sortValue) for what decides which.
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
  // One rule for whether the picker is on screen, and it is the run's own
  // state: the needs_action record arrives just before the run_state that
  // announces the parking, and the run_state that ends the parking (queued,
  // or cancelled) is what takes it away again.
  entry.pickerEl.hidden = !(entry.state === "needs-action" && entry.videos.length > 0);
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

// FINAL is the three states a run never leaves - runs.go's RunState.final,
// read here rather than re-derived, because "can this be run again" has to
// mean the same thing on both sides of the wire.
const FINAL = new Set(["done", "failed", "cancelled"]);

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

  entry.againRetry.hidden = !canRetry;
  entry.againGo.hidden = !canTopUp;
  entry.againEl.hidden = !canRetry && !canTopUp && !(settled && t && t.refused);

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

  if (t && t.refused) {
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
//      whether or not it was on screen - addFrame and renderFrames never
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
    state.runs.delete(entry.id);
    entry.id = newId;
    entry.disk = false;
    entry.reopening = false;
    entry.claiming = false;
    state.runs.set(entry.id, entry);
    syncEntry(entry);
    return;
  }
  // The id it already had. A top-up or retry the server RE-ARMED answers
  // with the same id this row is keyed by (Server.again), so there is
  // nothing to swap - but the flag still has to come down, or this row would
  // go on claiming every other run's id that shares its infohash.
  entry.claiming = false;
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
      // reopening OR claiming: a disk row being replayed (TOR-55) and a row
      // whose own run is being topped up or retried (TOR-152) are the same
      // situation for this function - a row that is expecting an id it does
      // not have yet - and folding the new id into the row that asked for it
      // is what keeps one torrent on one row (TOR-140).
      if ((candidate.reopening || candidate.claiming) && candidate.infohash === infohash) {
        claimReopenedRun(candidate, id);
        return candidate;
      }
    }
  }

  return ensureRun(id);
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
    availSwarm: article.querySelector(".avail-swarm"),
    availSwarmText: article.querySelector(".avail-swarm-text"),
    reach: article.querySelector(".reach"),
    reachStrip: article.querySelector(".reach-strip"),
    reachNote: article.querySelector(".reach-note"),
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
// with a fresh swarm reading (the "progress" case in apply(), below). The
// reading is torrent-wide, not per-file (renderAvail's own doc), so every
// file card shares the identical figure and all of them redraw together
// rather than only the one file this particular heartbeat happened to name.
function refreshAvailForEntry(entry) {
  for (const fentry of entry.fileEntries.values()) renderAvail(fentry);
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

function onFileDone(entry, ev) {
  const fentry = fileBlock(entry, ev.file);
  fentry.progress.hidden = true;

  const links = [];
  if (ev.sheet_url) links.push(link(ev.sheet_url, "contact sheet"));
  // TOR-171: no manifest link here on purpose. ev.manifest_url still rides
  // the NDJSON stream (server.go's record() keeps publishing it) - the page
  // itself just has no use for the JSON manifest a person would open, only
  // the contact sheet does. Something other than the page reading the
  // stream might still want it, which is the whole reason the field is
  // still there to read.
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

// showTorrent reveals - or takes away - the run's own .torrent, in the
// header (the save link) and, if this server has somewhere to send it, the
// group below it (TOR-169).
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
function showTorrent(entry, href) {
  entry.torrentURL = href || "";
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
    // TOR-141: stamped the FIRST time this row is seen running, in this
    // page's own wall clock - waitingForMetadata's only source of "how
    // long", since there is no torrent yet at that point for a Stall
    // reading (core.Progress.Stall) to ride on. Guarded on the transition
    // rather than stamped unconditionally, so a run_state that merely
    // repeats "running" (a reordering elsewhere, TOR-140's own note two
    // paragraphs down) does not reset a clock already ticking.
    if (ev.state === "running" && entry.state !== "running") {
      entry.runningSince = Date.now();
    }
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
    // TOR-140: run_state carries the queue's two fields only while this run
    // is still one the queue has something to say about (server.go's
    // runStateFieldsLocked, gated on RunState.queueable). Their ABSENCE is
    // information, so both are CLEARED rather than left at their last value:
    // the run_state announcing queued -> running is exactly the message that
    // must stop the row showing the position it held a moment ago.
    //
    // This message also arrives for rows nobody touched. Reordering, a
    // cancel, or a run simply starting moves everybody behind it, and the
    // server publishes a run_state to each of them - so a row's position
    // stays right without this page recomputing anything.
    entry.priority = ev.priority === undefined ? null : ev.priority;
    entry.queuePosition = ev.queue_position || 0;
    // TOR-156: on every run_state (server.go's runStateFieldsLocked), not
    // only a queueable one, and kept rather than cleared when it is missing -
    // see the same two lines in loadRuns for why this field's absence is not
    // the information the two above it carry.
    entry.arrival = ev.arrival || entry.arrival;
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
    // A stall reading belongs to a run that is actively going nowhere; a
    // run that just left a cancellable state (done, failed, cancelled) is
    // not stalled any more, it is over - cleared the same moment and by the
    // same test entry.progress already is.
    if (!cancellable(entry.state)) {
      entry.progress = "";
      entry.stall = null;
    }
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
      // The row's bar/line pick up this file's plan the moment it is known
      // (TOR-167) - not on the first progress heartbeat, which for a top-up
      // may be seconds away and may already be behind what a moment's worth
      // of replayed frame_ready events is about to show.
      applyFrameProgress(entry, ev.file);
      syncEntry(entry);
      break;

    case "frame_ready":
      addFrame(entry, ev);
      // Every landed frame keeps the row's bar/line current (TOR-167),
      // whether this point was just captured or replayed off disk - see
      // applyFrameProgress's own doc for why the heartbeat alone is not
      // enough for a top-up.
      applyFrameProgress(entry, ev.file);
      syncEntry(entry);
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
      // TOR-141: frames_total is 0 on a stall heartbeat (engine.go's
      // startFileHeartbeat) - a reading of peers/seeds/rates/swarm/stall
      // taken whether or not any capture point has even been attempted,
      // never a real 0-of-0 plan (frames.Plan.Validate rejects an empty
      // one, so a genuine per-point heartbeat never reports that). Applying
      // it to the per-file card would blank out real progress between two
      // genuine capture-point heartbeats - "3 / 12 frames" flashing to
      // "0 / 0 frames" and back every five seconds - so it is skipped here;
      // the six live columns and the stall reading below still update every
      // time regardless, which is the whole point of that heartbeat.
      if (ev.frames_total > 0) {
        const fentry = fileBlock(entry, ev.file);
        fentry.progress.hidden = false;
        fentry.progress.textContent =
          ev.frames_done + " / " + ev.frames_total + " frames · " +
          bytesLabel(ev.downloaded) + " downloaded · " + ev.peers + " peer(s)";
        // The row's own line and bar, which read from the fuller of two
        // signals rather than this heartbeat alone - see applyFrameProgress's
        // own doc for why a top-up needs that (TOR-167). ev.frames_done still
        // goes in, as the floor it always was; it just no longer wins on its
        // own when the grid has already proven more landed than this one
        // heartbeat knows about.
        applyFrameProgress(entry, ev.file, ev.frames_done);
      }
      // TOR-139: the same heartbeat carries this run's current swarm reading
      // - peers, seeds, both rates and the availability figure - under the
      // identical keys GET /runs' own "live" object uses (wire.go's
      // core.Progress case, listing.go's Live), so the six live columns keep
      // updating after page load rather than only once at loadRuns(). Built
      // fresh every time rather than merged onto the previous reading,
      // because that is what the registry itself does before this ever
      // reaches the wire (runEntry.applyProgress's own doc: "a whole new
      // *Live replaces the old one rather than being edited field by
      // field") - merging here instead could keep a stale rate or swarm
      // reading on screen past the heartbeat that actually dropped it.
      // download_bps/upload_bps/swarm are checked with "in" rather than
      // ev.download_bps (etc.) being truthy, because a real reading of 0
      // must stay 0, not fall through to absent.
      entry.live = {
        peers: ev.peers, seeds: ev.seeds,
        download_bps: "download_bps" in ev ? ev.download_bps : null,
        upload_bps: "upload_bps" in ev ? ev.upload_bps : null,
        swarm: ev.swarm || null,
      };
      // TOR-141: which distinct cause currently explains no progress for
      // THIS file, and how long - core.Progress.Stall's own doc has the
      // rule that keeps the duration from restarting on every heartbeat
      // that merely repeats the same finding. observedAt is stamped here,
      // in this page's own clock, at the moment the reading arrived -
      // stallPhrase adds the wall time since then, which is what keeps the
      // displayed duration ticking between heartbeats rather than only
      // updating once every stallHeartbeatInterval. Absent (ev.stall is
      // undefined) means progressing, which clears whatever this row showed
      // a moment ago rather than leaving it stuck on an old cause.
      entry.stall = ev.stall ? { ...ev.stall, observedAt: Date.now() } : null;
      // TOR-142: the third strip's own swarm chip, on every file card already
      // open - see refreshAvailForEntry's own doc for why it is every file
      // rather than only ev.file.
      refreshAvailForEntry(entry);
      syncEntry(entry);
      logFor(entry, "progress: " + ev.frames_done + "/" + ev.frames_total +
          ", " + ev.downloaded + " bytes, " + ev.peers + " peers");
      break;
    }

    case "budget_warning":
      // Two sentences rather than one with the numbers swapped in. At scope
      // "client" the figures are every run's together and this run may have
      // spent almost none of them, so the run-scoped wording would read as
      // an accusation of the wrong run (core.LimitScope).
      if (ev.scope === "client") {
        logFor(entry, "warning: " + ev.spent + " of the client-wide traffic roof of " +
            ev.limit + " bytes used, by every run together; all runs stop when it is reached");
      } else {
        logFor(entry, "warning: " + ev.spent + " of " + ev.limit + " bytes used");
      }
      break;

    case "file_done":
      onFileDone(entry, ev);
      break;

    case "done":
      entry.progress = "";
      // Whatever this run's own clock last said stopped mattering the
      // moment the run itself did - a finished run cannot still be
      // "stalled", it is simply over.
      entry.stall = null;
      // The run's own .torrent rides on this event because there is one per
      // run: a live run announces the file it just wrote, and a run reopened
      // from disk announces the same one, so the link does not depend on
      // which process captured it.
      showTorrent(entry, ev.torrent_url);
      syncEntry(entry);
      logFor(entry, "done: " + ev.reason + ", " + ev.frames + " frames from " + ev.files +
          " file(s), " + ev.downloaded + " bytes in " + seconds(ev.elapsed_ms));
      // The reason spelled out, for the three that are not self-explanatory
      // from a word. A run that hit the client-wide roof must not read like
      // a run that hit its own ceiling, and a run that hit its own TIME
      // ceiling must not read like one that hit its own TRAFFIC ceiling: the
      // first is about traffic this run may not have caused and cannot
      // narrow its way out of, the second two are about this run's own
      // spending but call for different responses - more traffic helps one
      // and does nothing for the other (core.StopRoof, core.StopBudget and
      // core.StopTime are three separate reasons since TOR-161).
      if (ev.reason === "traffic_roof") {
        logFor(entry, "stopped at the client-wide traffic roof, not at this run's own limit; " +
            "what was produced is kept");
      } else if (ev.reason === "budget") {
        logFor(entry, "stopped at this run's own traffic limit; what was produced is kept");
      } else if (ev.reason === "time") {
        logFor(entry, "stopped at this run's own time limit; what was produced is kept");
      }
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
      // TOR-141: a run-scoped failure IS the final answer to "why" - the
      // engine already names it (ev.code) below the badge via entry.error,
      // so a stall reading from a moment ago would only repeat, in fainter
      // words, what the row is about to say plainly.
      entry.stall = null;
      syncEntry(entry);
      logFor(entry, "failed: " + ev.code + " " + ev.error);
      break;
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
    // loadRuns sets it; from here on the "progress" case in apply() keeps it
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
    // anyway, the same shape apply()'s "progress" case gives it, so the
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

updateSortIndicators();
loadDefaults();
loadRuns();
connect();
