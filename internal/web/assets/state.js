// THE STATE EVERY OTHER MODULE READS AND EXACTLY ONE MODULE WRITES (TOR-191).
//
// WHY THIS FILE EXISTS. app.js used to be one file in which every rendering
// function reached into a shared state.runs and into the entry and file-entry
// objects directly, and each of the twelve message types the socket can carry
// wrote its own fields on the way past. That coupling is what made a component
// impossible: there was nothing to HAND a component, so it had to go looking.
// This module is what there now is to hand it.
//
// WHAT IT OWNS: state.runs; the run-entry shape (newRunState) and the
// file-entry shape (newFileState); the four operations on the store
// (ensureRun, resolveIncomingRun, claimReopenedRun, resetRunState); and every
// DERIVATION over an entry - what a row's badge says, what each of its cells
// reads, how two rows compare, what one file has on disk. Every one of those
// is a pure function of the state it is handed.
//
// WHAT IT MUST NOT DO, and this is the whole discipline: touch the DOM, make a
// request, or know a message shape. There is no `document` in this file, no
// `fetch`, and no `ev`. That is what lets it be executed against real inputs
// in a plain node process with no browser at all - which is how columns_test.go
// already runs compareEntries and sortValue for real, and how eventstate_test.go
// now runs the whole event layer on top of it.
//
// THE IMPORT GRAPH IS ONE-DIRECTIONAL, on purpose: app.js -> events.js ->
// state.js, and this file imports nothing. A state module that imported the
// renderer would be a cycle, and it would also stop being executable on its
// own. The one thing the renderer genuinely has to be reached FOR from here -
// building the DOM half of an entry, which only app.js can do - is an injected
// factory instead (setRunFactory / setFileFactory below).
//
// NO BUILD STEP: this is served straight out of the embedded assets and loaded
// natively as an ES module. `import` and `<script type="module">` are the whole
// mechanism (REQUIREMENTS.md section 3.3 - a folder holding the binary and
// ffmpeg, and nothing else).

// ---------------------------------------------------------------------------
// ABSENT IS NOT ZERO, everywhere in this block. GET /runs' "live" object
// (listing.go's Live) is present only for a row with an actual client that
// has spoken at least once, and even then download_bps/upload_bps/swarm can
// still be individually absent (before the run's second heartbeat, or before
// the swarm has answered what it holds). A queued row and a running row with
// nobody connected are opposite situations, and rendering both as "0" would
// make them read the same - so every cell below reads ABSENT, never a
// literal 0, whenever the reading itself is missing rather than measured.
const ABSENT = "—"; // em dash

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

function shortId(id) {
  return (id || "").replace(/^disk:/, "").slice(0, 8);
}

function seconds(ms) {
  if (!ms && ms !== 0) return "";
  return (ms / 1000).toFixed(1) + "s";
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

// basename is the last path segment. Here rather than in app.js since
// TOR-193: it labels a result set in the compare picker and a file in the
// list, so two modules read it, and it is a pure string function with no DOM
// and no message shape - which is exactly what this file is for.
function basename(path) {
  const parts = String(path || "").split("/");
  return parts[parts.length - 1] || path || "";
}

// ---------------------------------------------------------------------------
// THE ONE COUNTER THE THREE DETAIL MODULES SHARE (TOR-195).
//
// detailSeq gives each detail container and each checkbox a unique id, which
// the control that opens or labels it needs: a disclosure has to name the
// region it opens (aria-controls) and a <label> has to name its control
// (htmlFor), and there are as many regions as there are torrents, as many
// again as there are video files inside them, and one id per tick box on top.
//
// ONE COUNTER, THREE PREFIXES, and it is here rather than in any one of the
// three modules because all three mint ids and what it has to guarantee is
// uniqueness ACROSS THE DOCUMENT - which three counters would each only
// guarantee within their own prefix. That is the same reasoning app.js's own
// detailSeq carried before this ticket split the detail into three files; the
// only thing that changed is that the counter now has three callers instead
// of two, so it can no longer live in any of them.
//
// It is not a derivation, which is the one thing every other function in this
// file is. It is here on the strength of the two rules this file actually
// keeps: it touches no DOM, makes no request and knows no message shape, and
// it is the only place all three detail modules can reach without importing
// one another.
let detailSeq = 0;

function nextDetailId(prefix) {
  return prefix + ++detailSeq;
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
// function drives both a fresh render and app.js's own ticking refresh
// (refreshStallDurations) off one consistent instant per pass, rather than
// each row computing its own now() microseconds apart.
function stallPhrase(stall, now) {
  now = now == null ? Date.now() : now;
  // observedAt is stamped by events.js's "progress" case the moment this
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
// carried on the wire as row.partial and copied onto the entry by app.js's
// loadRuns - never recomputed here. TOR-72 first wrote this comparison twice, once in
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
// just wrote to disk (server.go's pump), and events.js's run_state case
// copies partial from it exactly as loadRuns copies it from row.partial -
// never recomputing it.
// Every earlier run_state for this run (queued, running, ...) carries none
// of those fields, so entry.partial keeps whatever the initial fetch
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
// now, when passed, is the caller's current clock (app.js's
// refreshStallDurations' own ticking redraw); omitted, both branches fall
// back to Date.now().
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
// until it does (app.js's loadRuns copies GET /runs' own "live" object, and
// events.js's "progress" case keeps it current), never a zeroed stand-in -
// see this block's own opening comment.
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
// concerns a waiting row) and this page renders it - read here
// (queuePosition, queueCellMetaText), drawn by app.js's syncEntry. There is
// exactly one notion of queue position in the codebase, and it is not this
// one.
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

// FINAL is the three states a run never leaves - runs.go's RunState.final,
// read here rather than re-derived, because "can this be run again" has to
// mean the same thing on both sides of the wire.
const FINAL = new Set(["done", "failed", "cancelled"]);

// untickedVideos is every capturable file this row has NOT asked for yet -
// what a tick still has left to reach, and what Select all would start.
function untickedVideos(entry) {
  return entry.videos
    .map((video) => video.index)
    .filter((index) => !entry.picked.has(index));
}

// passCount is the frames-per-file THIS ROW'S next start will actually use.
//
// It is the intake's number until this row has asked for something, and the
// server's locked figure afterwards (run_state's "count", DecideRun). Those
// are different numbers the moment somebody retypes the intake box with a
// pass already forming, and the row has to quote the one its next tick will
// get - a price that changes under a person after they read it is worse than
// no price.
//
// THE FALLBACK IS A PARAMETER, not a read of the intake box, and that is the
// module boundary doing its job rather than an inconvenience: "the server's
// own -n" is whatever #count currently displays, which is a DOM fact, and a
// state module that reached for it would stop being a state module (and stop
// being runnable outside a browser). app.js passes countValue(); everything
// else about the decision is here, where the row's own locked figure is.
function passCount(entry, fallback) {
  if (entry.picked.size > 0 && entry.passCount > 0) return entry.passCount;
  return fallback;
}


// framesOnDisk is how many frames of one file this row has to show, which is
// the same question as "is there anything here to clear" (TOR-183).
//
// It counts the very cells app.js's updateFileSummary states on the row, through the
// same helper, so the button's figure and the summary's can never disagree -
// a "Clear 12 frames" beside "8 frames" would be two answers to one question,
// and only one of them could be right.
//
// Zero for a file with no block at all, which is the honest answer rather
// than "unknown": a block exists from the moment a file says anything, live
// or replayed off disk - core's cacheHit.publish republishes file_started and
// every frame_ready for a reopened run, so a finished file's frames reach
// this page the same way whether the run is happening now or happened last
// week. A file with no block has said nothing, and there is nothing on this
// row for a clear to act on.
function framesOnDisk(entry, index) {
  const fentry = entry.fileEntries.get(index);
  return fentry ? capturedCells(fentry) : 0;
}

// capturedCells counts the cells of one file's grid that actually have a
// picture.
//
// NOT fentry.frames.size, and that distinction is the one the grid acquired
// when a point that produced nothing started being listed at all (TOR-118):
// the map holds an entry for every planned point a finished run recorded,
// failures included, so its plain size reports a holed run of five frames as
// twelve. A count that is really a plan pretending to be a result is the kind
// of quiet lie this project keeps finding.
function capturedCells(fentry) {
  return gridCells(fentry).filter((cell) => cell.url).length;
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

// ---------------------------------------------------------------------------
// THE TWO SHAPES (TOR-191). A run entry and a file entry are each two halves:
// what the thing KNOWS, which is here, and the elements it is drawn in, which
// app.js adds. The knowing half is what events.js writes and what every
// derivation above and below reads, and it is complete without a browser.

// newRunState is the data half of one run's entry - one per torrent, live or
// on disk.
//
// THE DOM HALF IS app.js's (newRunEntry): it spreads this and adds the row,
// the detail container and every element inside them. Two literals, ONE
// object - so a key declared in the second still shadows the same key in the
// first, silently, exactly as two keys in one literal always did. That is
// TOR-180's trap and the split did not remove it, so
// TestNoRunEntryFieldIsDeclaredTwice scans the UNION of the two literals
// rather than either one alone, which is strictly more than it could see
// while there was only one file to look in.
function newRunState(id) {
  return {
    id, disk: false, infohash: "", params: "",
    state: "", source: "", name: "",
    // provisionalName is a magnet's own dn=, offered only while name is
    // still empty (TOR-117) - see displayName for how the two are chosen
    // between, and noteConfirmedName for how the switch off this one is
    // announced rather than left silent.
    provisionalName: "",
    error: "", progress: "",
    // framesDone/framesTotal are the segmented bar's own two numbers, and
    // entry.progress just above is the line printed over it - one reading, so
    // the bar and the text can never disagree (TOR-123, TOR-167). Declared
    // here, which they were not before TOR-191: applyFrameProgress was the
    // only writer and resetRunState the only other reader, so a row that had
    // never had a file report anything carried neither field at all and
    // renderRunProgress read two undefineds through `|| 0`. That worked, and
    // it also meant the shape of an entry could not be read off one place -
    // which is the whole of what this module is for.
    framesDone: 0, framesTotal: 0,
    files: 0, complete: 0, selected: 0,
    // partial is GET /runs' own "partial" field (RunSummary.Partial in
    // listing.go, TOR-80) - false here for the same reason files/complete/
    // selected start at zero: nothing has merged a disk record into this
    // entry yet, and app.js's loadRuns is the only place that changes.
    partial: false,
    // live is GET /runs' own "live" object (listing.go's Live) for this row -
    // null, not a zeroed struct, until the run actually has a client that has
    // spoken at least once (app.js's loadRuns copies row.live; events.js's
    // "progress" case keeps it current for the rest of the page's life). Every
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
    // terminal-event handling in events.js).
    stall: null,
    // runningSince is stamped in local wall-clock time the moment this row
    // is FIRST seen in the "running" state (events.js's run_state handler) -
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
    // changes and never goes away once set, which is why its two readers -
    // events.js's run_state case and app.js's loadRuns - keep the value they
    // have rather than clearing it when a message arrives without one.
    arrival: 0,
    // when is this row's sort key for the default (date, newest-first) sort.
    // Set once, here, at creation - never touched again by a status update -
    // which is what keeps a live run from jumping position as events arrive.
    // app.js's loadRuns() overwrites it once with the authoritative value
    // GET /runs reports, for a row it already knows about at page load.
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
    // videos is the CAPTURABLE files this torrent holds - the only indices a
    // selection may name (the server validates a decision against exactly
    // this list, runEntry.holdsFile) - and fileList is EVERY file it holds,
    // video or not (TOR-180). Both arrive on the same two messages, a live or
    // replayed metadata_ready and a parked torrent's needs_action.
    //
    // fileList IS EMPTY FOR TWO DIFFERENT REASONS and the renderer treats
    // them the way this page treats any absent reading: nothing has arrived
    // yet (a queued row, whose metadata has not been fetched), or the
    // message carried no "files" key at all - a replay of a run recorded
    // before cache.Run.Files existed. Neither means "this torrent holds no
    // files", so the event layer falls back to videos rather than drawing an
    // empty torrent (events.js's applyFileList).
    //
    // NOT `files`, and the name matters: entry.files above is already GET
    // /runs' own per-row COUNT of video files (RunSummary.Files), read by
    // badgeState and metaLabel. Calling this one `files` too put two
    // different things under one key in the same object literal, where the
    // second silently won and took the badge's completeness arithmetic with
    // it - caught by a mutation run, not by reading. The wire key stays
    // "files" (it is one message type's own key, and metadata_ready has no
    // count for it to collide with there); it is this object that cannot
    // afford the overload.
    videos: [],
    fileList: [],
    // fileListKnown tells the two apart: true only when a message actually
    // carried a "files" key, so fileListTitle can say "5 files, 1 video"
    // where the whole truth is known and fall back to the video count alone
    // where it is not, rather than reporting one list's length as the
    // other's - which is a misreport a reader has no way to check.
    fileListKnown: false,
    // picked is what this row HAS ASKED TO CAPTURE, by torrent index, and
    // since TOR-181 that is the server's answer rather than this page's
    // staged intention: run_state's "ticked" is written straight into it on
    // every message, so a tick that was refused, or a second browser tab
    // ticking the same torrent, cannot leave the boxes disagreeing with what
    // is actually being fetched. A tick sets it optimistically for the one
    // frame between the click and the socket, and rolls it back if the POST
    // is refused (tickFile).
    //
    // It is seeded from the run's own selection on metadata_ready and starts
    // empty for a parked torrent - nothing is pre-ticked there on purpose,
    // since the count is per file and pre-ticking six variants would put the
    // most expensive possible run one careless click away (TOR-50).
    picked: new Set(),
    // deferred is the part of picked that is NOT in the pass in flight: a
    // file ticked while this row was already fetching, which the engine
    // cannot be handed mid-plan, so the server holds it and re-arms this
    // very entry when the current pass ends (runEntry.pending). Its own set
    // rather than a flag on picked, because the two say different things on
    // the row - "in this run" against "in the next pass" - and a reader has
    // to be able to tell which of their ticks is already spending.
    deferred: new Set(),
    // fetching is the part of picked the ENGINE IS HOLDING RIGHT NOW: the
    // files of the pass in flight, from run_state's own "fetching"
    // (runEntry.fetchingLocked). Only ever non-empty while the row is
    // running.
    //
    // NOT "picked minus deferred", which is what it looks like and is wrong
    // in exactly one direction: picked is cumulative, so that difference also
    // holds every file an EARLIER pass on this row already captured, and
    // those are files nothing is fetching. The two need opposite answers from
    // TOR-184 - un-ticking a file being fetched stops this torrent's fetch,
    // and un-ticking one a previous pass finished must not, because the run
    // it would stop is fetching something else. So the server sends the set
    // rather than leaving the page to reconstruct it and be wrong on the row
    // with two passes behind it.
    fetching: new Set(),
    // narrowable is the part of picked that can still come back OUT, from
    // run_state's own "narrowable" (runEntry.narrowableLocked): the pass a
    // person has chosen and the engine has not been handed. Only ever
    // non-empty while the row is queued or parked.
    //
    // A THIRD SET RATHER THAN A DERIVATION, for the same reason as fetching
    // and one of its own. "Neither deferred nor fetching nor settled" looks
    // like it would identify this case and does not: it is also true of a
    // file an earlier pass captured while the row runs on, which can neither
    // be narrowed nor cleared. And the rule about WHICH states are narrowable
    // lives in refuseUntick (RunState.queueable); a copy of it here would be
    // free to disagree the moment a state is added, which is exactly what
    // TOR-184's own comment says this page must not do.
    narrowable: new Set(),
    // unticked is what somebody has UN-TICKED on this row to be offered the
    // clear (TOR-183), by torrent index. It is the only piece of tick state
    // on this page the server does not own, and that is the whole design:
    // un-ticking a FINISHED file asks nothing of the server and spends
    // nothing - the run still asked for the file and cache.Run.Selected still
    // says so - it only puts the Clear frames button on the row. So there is
    // nothing for run_state to carry and nothing for it to overwrite, which
    // matters because run_state ASSIGNS entry.picked on every message: a
    // local un-tick recorded there would be undone by the next one.
    //
    // Its own set for the reason deferred above has one: picked answers "has
    // this row asked for the file", which stays true after a clear and stays
    // true while the offer is on screen, and folding two answers into one
    // would leave a reader unable to tell an un-tick from a file that was
    // never wanted.
    //
    // A stale entry is harmless by construction rather than by tidying:
    // app.js's updateFileCosts only ever reads it together with "and this file has
    // frames to clear", so an index left here for a file that no longer has
    // any simply stops being offered and the box goes back to checked. It is
    // still dropped at the two moments the offer genuinely ends - a re-tick,
    // and a clear that left the file clean - because a top-up could otherwise
    // bring the frames back and find the row still offering to delete them.
    unticked: new Set(),
    // tickable is the server's own verdict on whether a tick would be
    // ACCEPTED for this row at all (run_state's "tickable", from
    // runEntry.refuseTick), and tickRefusal is the sentence to put on the
    // box when it would not.
    //
    // Read rather than re-derived from entry.state, deliberately: the rule
    // has a case this page cannot see (a torrent dropped as a file, whose
    // staged copy is gone once its run ends), so a page deciding for itself
    // would offer live checkboxes the server then refuses. false to begin
    // with, which is the one safe default - a row this page has not yet
    // heard a state for must not draw a live box.
    tickable: false,
    tickRefusal: "",
    // passCount is the frames-per-file the pass this row is forming will
    // use (run_state's "count"). DecideRun locks it to the tick that opened
    // that pass, so once anything is ticked the remaining rows have to be
    // priced at THIS number rather than at whatever the intake box now
    // shows - otherwise a person who retyped the count would read a price
    // their next tick will not get. 0 means "the server's own -n", which is
    // exactly what the intake box displays (loadDefaults).
    passCount: 0,
    // armed is Select all's confirmation state: it has been pressed once and
    // is showing what it would cost, waiting for the second press that
    // spends it (armSelectAll). Per row, because two parked torrents can
    // each be waiting on their own answer.
    armed: false,
    // fileListSig is the file list this row's DOM was LAST BUILT FROM -
    // indices, paths and whether the whole list was known - so events.js can
    // tell a message that says something new about the list from one that
    // repeats what it already drew, and only hand app.js a rebuild for the
    // first kind.
    //
    // It exists because more than one message carries a file list and only
    // the first of them is a fresh start. A top-up and a retry (TOR-152) mint
    // a run whose events land on THIS entry (claimReopenedRun), and each of
    // them publishes its own metadata_ready - so before TOR-182 the second
    // one merely redrew an identical list, and since TOR-182 it would take
    // the details nested in that list down with it, mid-run, on a row whose
    // frames are on screen. A genuine reset comes through resetRunState,
    // which clears this along with the list itself.
    fileListSig: "",
    // Set once this run's first file block is built, so every file after it
    // defaults to collapsed - only the first one earns the auto-expand.
    autoExpanded: false,
    // torrentURL is the files/{id} handle the run's own done event announced
    // for its saved .torrent, empty for a run that has none to offer.
    torrentURL: "",
    // summaryLine is the sentence under the detail's header - the torrent's
    // own confirmed name, or for a parked one the name plus what it holds and
    // that nothing is captured yet. Empty means there is nothing to say, which
    // is what takes the paragraph off screen; there is no separate hidden
    // flag, so it cannot be left visible and empty.
    //
    // A FIELD RATHER THAN A DERIVATION (TOR-191), and the distinction is worth
    // the line: the two sentences come from two different MESSAGES
    // (metadata_ready and needs_action), not from two different states, and
    // deriving one from entry.state instead would switch a parked row's
    // sentence to the bare name the instant its first tick moved it out of
    // needs-action - a moment earlier than the page has ever done it.
    summaryLine: "",
    // Whether this torrent's detail is on screen (TOR-138). Changed only
    // inside setRunExpanded, which is the same discipline setFileExpanded and
    // setMetaExpanded keep one and two levels down: no event that arrives for
    // this run may open or close it, so a person's click cannot be undone
    // from under them by a frame landing.
    expanded: false,
  };
}

// newFileState is the data half of one video file's entry, one level in.
//
// THE DOM HALF IS app.js's (fileBlock), which spreads this and adds the row it
// mounts into and the elements of the block itself. Same union-scan rule as
// the run entry above, and for the same reason.
//
// WHAT MOVED ONTO IT IN TOR-191, and why each one had to: media, heartbeat and
// sheetURL. Before this split, file_started wrote the specs list straight out
// of its own message, progress wrote the progress line out of its own, and
// file_done built the contact-sheet link out of its own - so all three could
// only ever be drawn ONCE, by the handler that happened to be holding the
// message. Nothing else on the page could redraw them, because the facts were
// not anywhere: they had gone straight into text nodes. Keeping them here is
// what makes the metadata, the progress line and the link readable from state
// like everything else, and it is what let TOR-183's clear take the link off
// screen by clearing a field rather than by reaching into the DOM.
function newFileState(index, entry) {
  return {
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
    // media is everything file_started said about this file itself: its path,
    // the picture's size, the codecs, the tracks, the duration. NULL until the
    // file has started, which is not the same as a media object full of
    // zeroes - a row whose file has said nothing has no metadata, and drawing
    // "0×0" for it would be this page inventing a reading (the same ABSENT IS
    // NOT ZERO rule the run table's own cells follow).
    media: null,
    // heartbeat is the last progress reading for THIS file - frames done and
    // planned, bytes downloaded, peers - or null when none has arrived, and
    // null again once the file is done (which is what takes the line off
    // screen, rather than a hidden flag beside a stale reading).
    //
    // A stall heartbeat is deliberately NOT one of these: it carries
    // frames_total 0, meaning "a reading taken whether or not any capture
    // point has been attempted", and folding it in here would blank a real
    // 3/12 to 0/0 and back every five seconds. events.js drops it before it
    // ever reaches this field; the run's own row still shows every one of
    // them, which is what that heartbeat is for.
    heartbeat: null,
    // sheetURL is the contact sheet this file's own file_done announced - the
    // one artefact link the page offers (TOR-171 took the manifest's away) -
    // and empty for a file that has not finished or produced none.
    sheetURL: "",
    // detailLoaded says whether the other result sets have been read back off
    // disk for this file yet (loadFileDetail), so opening it a second time
    // does not ask again.
    detailLoaded: false,
    // expanded and metaExpanded are this file's two accordions, changed only
    // inside setFileExpanded and setMetaExpanded (app.js) - never by an event
    // that arrives for this run, so a person's click cannot be undone from
    // under them by a frame landing.
    expanded: false,
    metaExpanded: false,
    // index and entry are how a frame addresses an action on itself: a
    // delete names the torrent (entry.infohash), the file, the result set
    // and the frame's own number in that set's manifest. Everything else on
    // this page is driven by events arriving with a run already in hand;
    // this is the one thing a click on a thumbnail has to look up.
    index,
    entry,
  };
}

// ---------------------------------------------------------------------------
// THE STORE, AND THE TWO FACTORIES THAT REACH THE RENDERER WITHOUT IMPORTING IT.
//
// A whole entry is the data half above plus a DOM half only app.js can build.
// ensureRun and fileEntryFor are called from events.js, which must never touch
// the DOM, so they reach that builder through a factory app.js registers at
// wiring time rather than by importing it - which would make these two modules
// a cycle and would stop this file being executable on its own.
//
// THE DEFAULTS ARE NOT STUBS. A data-only entry is a complete, valid entry for
// everything in this file and everything in events.js; it is exactly what a
// headless test wants, and it is why eventstate_test.go can drive the real
// event layer with no browser and no fakes. app.js overrides both at the
// bottom of its own file, before anything can reach the store.
let makeRunEntry = newRunState;
let makeFileEntry = (entry, index) => {
  let fentry = entry.fileEntries.get(index);
  if (!fentry) {
    fentry = newFileState(index, entry);
    entry.fileEntries.set(index, fentry);
  }
  return fentry;
};

function setRunFactory(make) {
  makeRunEntry = make;
}

function setFileFactory(make) {
  makeFileEntry = make;
}

// ensureRun finds a torrent by key, creating it - at the top of the list,
// since a key that does not exist yet is always something just starting -
// the first time it is needed.
function ensureRun(id) {
  let entry = state.runs.get(id);
  if (!entry) {
    entry = makeRunEntry(id);
    state.runs.set(id, entry);
  }
  return entry;
}

// fileEntryFor is ensureRun one level in: the entry for one video file of one
// torrent, created the first time that file says anything.
//
// IT CAN ANSWER NULL, and every caller has to cope with that the way this page
// copes with any absent reading. The registered factory is app.js's fileBlock,
// which mounts the block into that file's own row in the torrent's file list -
// and if the list has no row for this index there is nowhere to put a detail.
// That cannot happen for any sequence the server produces (metadata_ready is
// published before the first file_started on both paths that publish one at
// all), so it is the guard for a case that ought to be impossible rather than
// a path with a known caller. See fileBlock's own doc for the whole argument.
function fileEntryFor(entry, index) {
  return makeFileEntry(entry, index);
}

// resetRunState clears one run's files and frames without touching its row
// or detail container - what a run_state "reset" means now: this run's own
// history is starting over (a fresh start, or a reconnecting page about to
// replay it from the beginning), not "every torrent on the page is gone".
//
// IT IS THE DATA HALF ONLY (TOR-191). What a reset takes OFF SCREEN - the file
// list's rows, the summary paragraph, the .torrent link, Select all's armed
// note - is app.js's resetRunView, which events.js calls in the same breath.
// The split earns its keep precisely here: every field below is something the
// replayed history is about to state again, and a renderer holding its own
// copy of one of them would be a second place the reset had to be got right.
//
// THIS IS THE FUNCTION THE RECONNECT TRAP LIVES IN (TOR-184). A stale
// entry.fetching would offer to stop a run whose history the page is in the
// middle of re-reading, and stopping is the one act on this page that reaches
// the server without being asked twice.
function resetRunState(entry) {
  // Since TOR-182 the file blocks live INSIDE the file list's own rows, so
  // there is no separate container to empty here - resetRunView's own
  // replaceChildren is what takes the blocks off screen with the rows that
  // hold them. This map still has to be cleared by hand, and before that
  // happens rather than after: it is the only handle on those blocks, and
  // every one of them is about to be detached.
  entry.fileEntries.clear();
  entry.autoExpanded = false;
  entry.error = "";
  // The summary sentence goes with the rest of what this run has shown: it
  // comes back from the replayed metadata_ready or needs_action a moment
  // later, and a name kept here would sit over a detail with nothing in it.
  entry.summaryLine = "";
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
  // The file list goes with everything else this run has shown. It comes back
  // from the replayed metadata_ready or needs_action that follows in the same
  // history, so a reconnecting page rebuilds it rather than keeping a stale
  // copy of a list the server may since have moved past.
  entry.videos = [];
  entry.fileList = [];
  entry.fileListKnown = false;
  entry.picked.clear();
  entry.deferred.clear();
  // And what the engine was holding (TOR-184), for the same reason as the two
  // above and one of its own: a stale "fetching" would make an un-tick offer
  // to stop a run whose history this page is in the middle of re-reading, and
  // stopping is the one act here that reaches the server without being asked
  // twice.
  entry.fetching.clear();
  // And what could still be narrowed (TOR-197): the row's request is about
  // to be re-read, and a stale set here would offer to take a file out of a
  // pass this page has yet to be told about.
  entry.narrowable.clear();
  // And the offers on it (TOR-183). The rows those buttons sat on are about
  // to be detached, and the frames they offered to clear are re-read from
  // disk by the history that follows - so an offer kept here would be an
  // offer about a file this page has yet to be told anything about.
  entry.unticked.clear();
  // The tick's own two readings go with the list: they are the server's
  // answer for a state this row is about to be told again, and a stale
  // "tickable" would draw a live checkbox for one frame on a row whose run
  // has since ended. false is the safe direction (see the field's own doc).
  entry.tickable = false;
  entry.tickRefusal = "";
  entry.passCount = 0;
  // Select all goes back to its unarmed state. The button's own label and the
  // sentence beside it are resetRunView's to redraw; this is the fact they
  // read, and leaving it true would put a whole torrent's bill under one
  // stray press on a row that has just been emptied.
  entry.armed = false;
  // The signature goes with the list it describes. Left behind, it would
  // tell the replayed metadata_ready that follows "you already drew this",
  // and the row would come back from a reset with an empty list (TOR-182).
  entry.fileListSig = "";
  // The .torrent link goes with the rest of what this run has shown. It comes
  // back from the done event the replayed history ends on, so a reconnecting
  // page rebuilds it rather than keeping a handle the server may no longer
  // resolve.
  entry.torrentURL = "";
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
//
// IT DOES NOT REDRAW, AND THAT IS THE CALLER'S JOB (TOR-203). It used to end
// the swap branch with syncEntry(entry) - a function this module cannot see.
// state.js imports nothing and syncEntry lives in app.js, so the call was a
// ReferenceError on every id swap, and a quiet one: the re-key above happens
// BEFORE it, so the swap survived and the caller's catch ran instead of its
// success path. A successful reopen was reported as FAILED with
// "syncEntry is not defined" as its error (TOR-55), and a top-up or retry
// cleared entry.claiming - defeating the very race guard this comment is
// about (TOR-152).
//
// The cause was not a typo but a contract violation, which is why the fix is
// here rather than an import: this file owns what a run and a file KNOW and
// touches no DOM, and that property is exactly what lets it be executed for
// real in node (eventstate_test.go). A redraw sitting in it is the one shape
// it may not have - and its having no imports is precisely why nothing caught
// the name at load time.
//
// So every caller redraws after calling this, in BOTH branches: the flags come
// down either way, so the row has changed either way. resolveIncomingRun's own
// call below is the exception that needs nothing added - events.js's handler
// ends on view.syncEntry for every message it processes.
function claimReopenedRun(entry, newId) {
  if (entry.id !== newId) {
    state.runs.delete(entry.id);
    entry.id = newId;
    entry.disk = false;
    entry.reopening = false;
    entry.claiming = false;
    state.runs.set(entry.id, entry);
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

// ---------------------------------------------------------------------------
// THE MODULE'S SURFACE, in one list rather than an `export` on each
// declaration - deliberately, and for a reason that is not style. The Go
// tests extract functions out of this file's OWN shipped text by name
// (columns_test.go's extractJSFunction anchors on `function NAME(` and a
// column-0 `}`, and its compareEntries harness then RUNS what it extracted in
// a plain node process). An `export` keyword welded to each declaration would
// leave that harness assembling a script whose every line is a module-only
// keyword, for no gain: one list says the same thing and leaves each
// declaration exactly the text a test can lift and execute.
export {
  ABSENT,
  state,
  FINAL,
  PRIORITY_LOW,
  PRIORITY_NORMAL,
  PRIORITY_HIGH,
  shortId,
  seconds,
  bytesLabel,
  timecode,
  basename,
  nextDetailId,
  waitingForMetadata,
  cancellable,
  badgeState,
  badgeLabel,
  metaLabel,
  metaTitle,
  displayName,
  whenLabel,
  hasLive,
  peersCellText,
  peersCellTitle,
  seedsCellText,
  seedsCellTitle,
  rateCellText,
  rateCellTitle,
  availabilityReading,
  availabilityCellText,
  availabilityMetaText,
  availabilityCellTitle,
  queuePosition,
  arrivalOrdinal,
  hasPriority,
  priorityLabel,
  queueCellText,
  queueCellMetaText,
  queueCellTitle,
  sortValue,
  compareEntries,
  newRunState,
  newFileState,
  setRunFactory,
  setFileFactory,
  ensureRun,
  fileEntryFor,
  claimReopenedRun,
  resolveIncomingRun,
  resetRunState,
  untickedVideos,
  passCount,
  framesOnDisk,
  capturedCells,
  gridCells,
};
