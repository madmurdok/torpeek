// THE ONE PLACE A MESSAGE BECOMES A STATE CHANGE (TOR-191).
//
// WHY THIS FILE EXISTS. The socket carries twelve kinds of message, and before
// this split each of them was a block inside one 340-line apply() that wrote
// entry fields AND text nodes in the same breath - so "what does frame_ready
// change" and "what does the grid draw" were one answer, in one scope, and a
// rendering function could only ever be reached by the handler that happened
// to be holding the message. That is what made a component impossible: there
// was nothing to hand it.
//
// SO THE MESSAGE STOPS HERE. Every handler below reads `ev`, writes state.js's
// entry or file-entry fields, and then names a redraw - and the redraw is
// handed nothing but state. There is no `document` in this file and no `ev`
// past this file: those two sentences together are the whole of the boundary,
// and each is checked rather than promised (eventstate_test.go).
//
// THE TWELVE TYPES, against what the server can actually publish, because the
// count is easy to lose in a move and the whole batch depends on it:
//
//   run_state, needs_action          - server.go (the web layer's own two)
//   metadata_ready, file_started,
//   frame_ready, frame_skipped,
//   progress, budget_warning,
//   file_done, done, failed          - wire.go, from core's event stream
//   unknown                          - wire.go's fallback for a core event it
//                                      does not recognise, and the one type
//                                      with no case below ON PURPOSE: there is
//                                      nothing in it to apply. It falls through
//                                      the switch, which is what it should do.
//
// A run being CANCELLED is not a type of its own, and neither is availability -
// see apply()'s own note at the bottom for both, since both are easy to expect
// here and looking for them is how a real handler gets dropped.
//
// NO BUILD STEP: served straight out of the embedded assets, loaded natively as
// an ES module.

import {
  cancellable,
  fileEntryFor,
  gridCells,
  resetRunState,
  resolveIncomingRun,
  seconds,
  state,
} from "./state.js";

// ---------------------------------------------------------------------------
// THE VIEW: everything this module is allowed to ask the page for, and the
// entire extent of it.
//
// The import graph runs app.js -> events.js -> state.js and no other way, so
// the renderer cannot be imported from here without making a cycle - and a
// cycle is not the reason to do it this way. The reason is that a hook LIST is
// checkable: setView refuses a view missing one of these, at wiring time, with
// the name in the message. The five extraction tickets after this one each add
// a component that something below has to redraw, and the failure they would
// otherwise buy - a handler quietly calling undefined at 3am on a live run -
// is exactly the one worth spending a loop on.
//
// EVERY HOOK TAKES STATE. Not one of them takes `ev`. That is the criterion
// this whole ticket is written for, expressed as a signature rather than as a
// rule somebody has to remember.
const VIEW_HOOKS = [
  // The activity log: one line tagged with which torrent it is about, and one
  // untagged for the socket's own trouble.
  "log",
  "note",
  // The connection indicator, and the address to open. Both are the page's:
  // the address needs document.baseURI and the access token (app.js's url()),
  // and the indicator is a text node.
  "status",
  "socketURL",
  // A whole row: its cells, its badge, its detail header, its file list's
  // state. The one hook nearly every handler ends on.
  "syncEntry",
  // What a reset takes off screen, beside what resetRunState takes out of the
  // entry.
  "resetRunView",
  // The file list's ROWS, rebuilt - called only when the list actually changed
  // shape, which is the whole of TOR-182's trap (see applyFileList).
  "rebuildFileList",
  // One file's own block, one redraw per thing that can change about it.
  "renderFileMeta",
  "renderFrames",
  "renderFileProgress",
  "renderFileDone",
  // The swarm chip on every open file of one torrent.
  "renderSwarm",
  // The run's own .torrent: the save link, and the send button where there is
  // somewhere to send it. Its own hook rather than part of syncEntry, because
  // it also clears the note a send left behind - and syncEntry runs on every
  // event, which would wipe "sent to /path/to/watch" a moment after it
  // appeared.
  "renderTorrent",
];

// The default view is silent and draws nothing, which is not a placeholder for
// a real one: it is what makes this module executable on its own, against real
// messages, in a plain node process (eventstate_test.go drives the whole event
// layer through a recording view and asserts on the STATE that comes out).
// app.js installs the drawing one at the bottom of its own file, before
// connect() can be reached.
let view = Object.fromEntries(VIEW_HOOKS.map((name) => [name, () => {}]));

function setView(next) {
  const missing = VIEW_HOOKS.filter((name) => typeof next[name] !== "function");
  if (missing.length) {
    throw new Error("events.js: the view is missing " + missing.join(", "));
  }
  view = next;
}

// ---------------------------------------------------------------------------

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
  view.log(entry, "name confirmed as \"" + confirmed + "\" (the magnet link said \"" +
    entry.provisionalName + "\")");
}

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
// captured, and the grid is built from exactly that stream (applyFrame).
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

// applyRunState is the message that says what a torrent IS, and the only one
// that can arrive for a row nobody on this page has touched: reordering, a
// cancel, or a run simply starting moves everybody behind it, and the server
// publishes one of these to each of them (server.go's queueRecordsLocked).
//
// THREE OF THE FOUR TRAPS THIS TICKET NAMES ARE IN THIS FUNCTION, which is why
// it is the longest one here and why each has its note at the line that
// carries it: entry.picked is ASSIGNED from the server's answer (TOR-183, and
// the reason un-ticking is recorded in entry.unticked instead), entry.fetching
// is READ rather than derived (TOR-184), and a reset clears both rather than
// letting a reconnect inherit them.
function applyRunState(ev) {
  // The one message with no "run" key is the connection marker: it opens
  // the whole replay a fresh (or reconnected) socket is about to send, but
  // it is not itself about any torrent, so there is nothing on the page to
  // update for it - the per-run reset that follows for each run already
  // rebuilds that run's own content from scratch.
  if (!ev.run) return;

  const entry = resolveIncomingRun(ev.run, ev.infohash);
  if (ev.reset) {
    resetRunState(entry);
    view.resetRunView(entry);
  }
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
  // TOR-181: what this row has asked to capture, and whether it can be
  // asked for more. All four ride on run_state rather than on a message of
  // their own for the reason the queue's two fields do (server.go's
  // runStateFieldsLocked): the hub replays a run's LAST run_state to a
  // reconnecting page, so a field overwritten by the run's own next state
  // can never be replayed stale.
  //
  // OVERWRITTEN, NEVER MERGED. entry.picked is the server's answer, not
  // this page's intention: a tick that was refused, a second tab ticking
  // the same torrent, or a pass ending and taking the deferred files into
  // itself all arrive here, and keeping whatever the page thought would be
  // the one way the boxes could come to disagree with what is fetching.
  // Absent means empty here, which is safe for exactly this message - the
  // server always knows the answer for a live entry, and nothing replays a
  // run_state off disk for an older shape's silence to be mistaken for a
  // fact.
  //
  // AND IT IS WHY entry.unticked IS NOT TOUCHED HERE (TOR-183). An un-tick of
  // a FINISHED file asks nothing of the server and spends nothing, so there
  // is nothing on the wire for it to ride on - and a local un-tick recorded
  // in entry.picked would be undone by the very next one of these.
  entry.picked = new Set(ev.ticked || []);
  entry.deferred = new Set(ev.deferred || []);
  // TOR-184, and the direction an absent key falls is the safe one here
  // too: the server sends this only while the row is running, so an empty
  // set means "the engine is holding nothing", which draws no offer to stop
  // anything. Guessing the other way would put a run-stopping gesture on a
  // row that has already finished.
  //
  // READ, NEVER DERIVED, and "picked minus deferred" is the derivation that
  // looks right and is wrong in exactly one direction - see the field's own
  // doc in state.js for the row-with-two-passes-behind-it case it gets wrong.
  entry.fetching = new Set(ev.fetching || []);
  // TOR-197, and the absent key falls the safe way here as well: the server
  // sends this only while the row is queueable, so an empty set means
  // "nothing here can be taken back out", which leaves the box disabled.
  // Guessing the other way would offer a narrowing the server would refuse.
  entry.narrowable = new Set(ev.narrowable || []);
  // false when the key is missing, which cannot happen for a run this
  // server holds (it is sent on every run_state, true or false) and is the
  // safe direction if it ever does: a live checkbox the server would
  // refuse is worse than a disabled one it would have taken.
  entry.tickable = !!ev.tickable;
  entry.tickRefusal = ev.tick_refusal || "";
  // 0 is "the server's own -n", which the intake box already displays, so
  // passCount falls back to that rather than quoting a zero.
  entry.passCount = ev.count || 0;
  // A stall reading belongs to a run that is actively going nowhere; a
  // run that just left a cancellable state (done, failed, cancelled) is
  // not stalled any more, it is over - cleared the same moment and by the
  // same test entry.progress already is.
  if (!cancellable(entry.state)) {
    entry.progress = "";
    entry.stall = null;
  }
  view.syncEntry(entry);
}

// applyFileList takes the torrent's own file list off whichever of the two
// messages carries one - a metadata_ready or a needs_action - and DECIDES
// WHETHER THE ROWS HAVE TO BE REBUILT.
//
// THAT DECISION IS THE WHOLE OF TOR-182'S TRAP, and it belongs here rather
// than inside the renderer, which is where it used to sit. More than one
// message carries a file list and only the first is a fresh start: a top-up
// and a retry (TOR-152) each mint a run whose events land on THIS entry
// (claimReopenedRun), and each publishes its own metadata_ready - so this runs
// a second time, mid-life, on a row whose files are open with frames on
// screen. Handing the renderer a rebuild there would detach every frame grid,
// every open disclosure and every element fileBlock is holding a reference to,
// and the page would look as though the run had lost its work.
//
// The signature is the list ITSELF (each file's index and path) plus whether
// the whole of it was known, because those are exactly the inputs the rows are
// built from - not a message counter or a length, either of which would call
// two different lists the same. A genuine fresh start arrives through
// resetRunState, which clears the signature with the list, so this can never
// suppress the rebuild that follows a reset.
//
// The renderer is now told to rebuild or not told anything at all, which is
// what makes the trap checkable without a browser: eventstate_test.go counts
// the rebuilds a second identical metadata_ready asks for, and the answer has
// to be none.
function applyFileList(entry, ev) {
  entry.videos = ev.videos || [];
  // ABSENT IS NOT EMPTY. A message with no "files" key cannot say what the
  // torrent holds - a replay of a run recorded before cache.Run.Files
  // existed is exactly that - so the video list stands in rather than the
  // page claiming an empty torrent. Every row is then tickable, which is
  // true of that list by construction.
  entry.fileListKnown = !!ev.files;
  entry.fileList = ev.files || entry.videos;
  // ADDED TO, NEVER ASSIGNED, and this is a bug the browser found rather
  // than the tests: ev.selected is the pass IN FLIGHT, which for a SECOND
  // pass names only the files that pass was re-armed with (TOR-181), so
  // assigning it here cleared the tick beside a file the first pass had
  // already captured - which reads as the capture having been undone.
  //
  // The cumulative answer is the server's (runEntry.asked, run_state's
  // "ticked"); all this has to do is not throw it away. It is still exactly
  // right for a parked torrent, whose own record carries no "selected" at
  // all (server.go's needsActionRecordLocked deliberately omits it) and
  // whose set is empty because resetRunState emptied it when the run's
  // history opened.
  for (const index of ev.selected || []) entry.picked.add(index);

  const sig = entry.fileList.length
    ? (entry.fileListKnown ? "files:" : "videos:") +
        entry.fileList.map((file) => file.index + " " + file.path).join("\n")
    : "";
  // THE SAME LIST IS NOT REDRAWN. Everything about a row that is not its
  // SHAPE - the boxes, the prices, the frame and the title - is restated by
  // the syncEntry every caller below ends on, which is all a repeat message
  // can legitimately have changed about a list it has already drawn.
  if (sig !== "" && sig === entry.fileListSig) return;
  entry.fileListSig = sig;
  // AND THE FILE ENTRIES GO WITH THE ROWS THAT HELD THEM. Every fentry points
  // at elements inside a row the rebuild is about to detach, so keeping the
  // map would leave fileBlock returning a block whose DOM is no longer on the
  // page - and that file's detail would then never appear again, silently,
  // for the rest of the row's life. Cleared here rather than in
  // resetRunState alone, because this is the OTHER way the rows can go, and
  // BEFORE the rebuild rather than after, because this map is the only handle
  // on them.
  //
  // Reaching this line at all means the list genuinely changed shape (the
  // signature above is what settles that), which for one torrent on one row
  // should never happen - so this is the guard for a case that ought to be
  // impossible rather than a path with a known caller.
  entry.fileEntries.clear();
  entry.autoExpanded = false;
  view.rebuildFileList(entry);
}

function applyMetadataReady(entry, ev) {
  noteConfirmedName(entry, ev.name);
  entry.name = ev.name;
  // TOR-178: entry.videos used to be set only by needs_action (the
  // undecided path). metadata_ready is the OTHER path - a torrent whose
  // selection was already made - and its own file lists are stored the
  // same way, so a file's Metadata disclosure (renderFileMeta's "Torrent
  // files" spec) has an answer on both paths, not only the one that
  // happens to park first.
  //
  // TOR-180 renders the list from here too, which is what makes it the
  // row's ordinary content: this event is the one every state that has
  // a file list goes through - a live run, and the replay a disk row's
  // reopen produces.
  applyFileList(entry, ev);
  // TOR-178: this used to also state how many of the torrent's video
  // files this run had picked ("N of M ... selected"). The owner asked
  // for that gone - the row's own badge (DONE/PARTIAL) already carries
  // whether this run's selection came out whole. This paragraph's other
  // job, carrying the torrent's name, is untouched - see
  // .torrent-summary's own doc in the detail template.
  entry.summaryLine = ev.name;
  view.syncEntry(entry);
  view.log(entry, "metadata: " + ev.name + " (" + ev.infohash + ")");
}

function applyNeedsAction(entry, ev) {
  // Not a core event and deliberately not a metadata_ready: no run is
  // running. The torrent's name and file lists arrive here, and the
  // run_state that follows this record is what turns the list's own
  // controls on (syncFileList).
  noteConfirmedName(entry, ev.name);
  entry.name = ev.name;
  if (ev.infohash) entry.infohash = ev.infohash;
  entry.summaryLine =
    ev.name + " — " + (ev.videos || []).length + " video file(s), none captured yet";
  applyFileList(entry, ev);
  view.syncEntry(entry);
  view.log(entry, "waiting for a file selection: " + (ev.videos || []).length + " video file(s)");
}

// applyFileStarted is the moment a file's media is known - before a single
// frame exists - and since TOR-191 it puts that media ON THE FILE ENTRY
// (fentry.media) rather than straight into the specs list.
//
// That is not a rearrangement. Written into the DOM, the codecs and the tracks
// existed nowhere else, so nothing but this handler could ever draw them: a
// re-render for any other reason would have had to wait for another
// file_started, which for a finished file never comes. Now file_started is the
// only writer and renderFileMeta the only drawer, and either can happen
// without the other.
function applyFileStarted(entry, ev) {
  const fentry = fileEntryFor(entry, ev.file);
  if (fentry) {
    fentry.media = {
      // The engine's own path. The LIST's convention - a base name with the
      // whole path on its title - is renderFileMeta's to apply; what is kept
      // here is the path itself, unshortened, since the block's title and the
      // row's name are drawn from it and a base name cannot be un-shortened.
      path: ev.path,
      width: ev.width,
      height: ev.height,
      codec: ev.codec,
      profile: ev.profile,
      fps: ev.fps,
      bitrate: ev.bitrate,
      videoBitrate: ev.video_bitrate,
      durationMs: ev.duration_ms,
      // Defaulted to empty lists rather than left undefined: "no audio
      // tracks" is a real answer this file's Metadata has to be able to
      // state, and trackGroup already draws "Audio — none" for it.
      audio: ev.audio || [],
      subtitles: ev.subtitles || [],
    };
    // The whole grid, at final size, before the first piece is fetched.
    fentry.plan = Array.isArray(ev.plan) ? ev.plan : [];
    fentry.skipped.clear();
    view.renderFileMeta(fentry);
    view.renderFrames(fentry);
  }
  view.log(entry, "file " + ev.file + ": " + ev.path + " — " + seconds(ev.duration_ms) +
      ", " + ev.width + "x" + ev.height + " " + ev.codec + ", " + ev.planned + " points");
}

// applyFrame records one frame the event stream just announced. It records
// rather than appends: two result sets of the same file share timecodes, and
// the grid is one list ordered by time (renderFrames rebuilds from the map).
function applyFrame(entry, ev) {
  const fentry = fileEntryFor(entry, ev.file);
  // Nowhere to put it (see fileEntryFor): the frame is on disk either way, and
  // it is reachable again the moment a row for this file exists.
  if (!fentry) return;
  const at = ev.actual_ms;

  // THE PATH, NOT A RESOLVED URL, since TOR-191 - and detailFrame stores the
  // same shape, so a frame is one kind of record however it arrived. Resolving
  // it needs document.baseURI and the access token (app.js's url()), which is
  // exactly the sort of thing this module must not know; frameFigure resolves
  // it at the moment it sets the src instead. A frame that is half-addressable
  // in one reader and whole in the other is the trap detailFrame's own doc
  // names, and one shape for both is what keeps it shut.
  //
  // No params: a frame straight off the event stream has no result set to
  // address until its manifest exists, so it carries no cross yet. The
  // file_done that follows re-reads every frame from disk moments later
  // (loadFileDetail) and replaces this one with an addressable version.
  fentry.frames.set(at, { url: ev.url, timeMs: at, shift: ev.shift || "", params: "", index: ev.index });
  view.renderFrames(fentry);

  view.log(entry, "frame " + ev.index + " at " + seconds(at) + (ev.shift ? " (" + ev.shift + ")" : ""));
}

// applyFrameSkipped marks the cell this point had reserved, rather than only
// saying so in the log: a reserved cell that never fills is indistinguishable
// from one still waiting, and a person watching cannot tell a slow read from a
// dead one (TOR-110).
function applyFrameSkipped(entry, ev) {
  const fentry = fileEntryFor(entry, ev.file);
  // The log line still goes out for a file with no row of its own: the
  // skip is what a person is owed here, and the cell it would have
  // marked does not exist to mark.
  if (fentry) {
    fentry.skipped.set(ev.index, { code: ev.code || "", reason: ev.reason || "" });
    view.renderFrames(fentry);
  }
  view.log(entry, "frame " + ev.index + " skipped: " + ev.code + " " + ev.reason);
}

function applyProgress(entry, ev) {
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
    const fentry = fileEntryFor(entry, ev.file);
    if (fentry) {
      fentry.heartbeat = {
        framesDone: ev.frames_done,
        framesTotal: ev.frames_total,
        downloaded: ev.downloaded,
        peers: ev.peers,
      };
      view.renderFileProgress(fentry);
    }
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
  // TOR-142: the reach strip's own swarm chip, on every file card already
  // open - see refreshAvailForEntry's own doc for why it is every file
  // rather than only ev.file.
  view.renderSwarm(entry);
  view.syncEntry(entry);
  view.log(entry, "progress: " + ev.frames_done + "/" + ev.frames_total +
      ", " + ev.downloaded + " bytes, " + ev.peers + " peers");
}

function applyBudgetWarning(entry, ev) {
  // Two sentences rather than one with the numbers swapped in. At scope
  // "client" the figures are every run's together and this run may have
  // spent almost none of them, so the run-scoped wording would read as
  // an accusation of the wrong run (core.LimitScope).
  if (ev.scope === "client") {
    view.log(entry, "warning: " + ev.spent + " of the client-wide traffic roof of " +
        ev.limit + " bytes used, by every run together; all runs stop when it is reached");
  } else {
    view.log(entry, "warning: " + ev.spent + " of " + ev.limit + " bytes used");
  }
}

function applyFileDone(entry, ev) {
  const fentry = fileEntryFor(entry, ev.file);
  if (!fentry) return;
  // The heartbeat's own line goes with the run it was reporting: null is what
  // takes it off screen (renderFileProgress reads exactly this), rather than a
  // hidden flag left standing beside a stale "11 / 12 frames".
  fentry.heartbeat = null;
  // TOR-171: the contact sheet, and only the contact sheet. ev.manifest_url
  // still rides the NDJSON stream (server.go's record() keeps publishing it)
  // and is deliberately not kept here - the page has no use for the raw JSON
  // manifest, and something other than the page reading the stream might,
  // which is the whole reason the field is still there to read.
  fentry.sheetURL = ev.sheet_url || "";
  view.log(entry, "file " + ev.file + " done: " + ev.frames + " frames, " + ev.skipped + " skipped");
  // The manifest exists from now on, so this is the first moment the other
  // result sets of this file can be read off disk - and the moment this
  // run's own frames become part of what a later open would find.
  // renderFileDone is what goes and gets them (loadFileDetail).
  view.renderFileDone(fentry);
}

function applyDone(entry, ev) {
  entry.progress = "";
  // Whatever this run's own clock last said stopped mattering the
  // moment the run itself did - a finished run cannot still be
  // "stalled", it is simply over.
  entry.stall = null;
  // The run's own .torrent rides on this event because there is one per
  // run: a live run announces the file it just wrote, and a run reopened
  // from disk announces the same one, so the link does not depend on
  // which process captured it.
  entry.torrentURL = ev.torrent_url || "";
  view.renderTorrent(entry);
  view.syncEntry(entry);
  view.log(entry, "done: " + ev.reason + ", " + ev.frames + " frames from " + ev.files +
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
    view.log(entry, "stopped at the client-wide traffic roof, not at this run's own limit; " +
        "what was produced is kept");
  } else if (ev.reason === "budget") {
    view.log(entry, "stopped at this run's own traffic limit; what was produced is kept");
  } else if (ev.reason === "time") {
    view.log(entry, "stopped at this run's own time limit; what was produced is kept");
  }
  // A run that finished and still left something out says so here rather
  // than by reading as failed, which is what it used to do when its
  // .torrent could not be written (TOR-79). The badge stays "done"
  // because the run is: the warning is about an artefact, not the frames.
  for (const warning of ev.warnings || []) {
    view.log(entry, "warning: " + warning);
  }
}

function applyFailed(entry, ev) {
  entry.progress = "";
  // TOR-141: a run-scoped failure IS the final answer to "why" - the
  // engine already names it (ev.code) below the badge via entry.error,
  // so a stall reading from a moment ago would only repeat, in fainter
  // words, what the row is about to say plainly.
  entry.stall = null;
  view.syncEntry(entry);
  view.log(entry, "failed: " + ev.code + " " + ev.error);
}

// apply is the dispatch, and the whole of it: one message in, state changed,
// redraws named.
//
// run_state comes first and returns, because it is the only type that may
// CREATE a row (resolveIncomingRun) - every other type is about a torrent this
// page has already been told about, which is what the single `state.runs.get`
// below is asserting rather than merely assuming. A message for a run this
// page has never heard of is dropped: the socket replays a run's history from
// the start, so the run_state that opens it always arrives first.
function apply(ev) {
  if (ev.type === "run_state") {
    applyRunState(ev);
    return;
  }

  const entry = ev.run ? state.runs.get(ev.run) : null;
  if (!entry) return;

  switch (ev.type) {
    case "metadata_ready": applyMetadataReady(entry, ev); break;
    case "needs_action": applyNeedsAction(entry, ev); break;
    case "file_started":
      applyFileStarted(entry, ev);
      // The row's bar/line pick up this file's plan the moment it is known
      // (TOR-167) - not on the first progress heartbeat, which for a top-up
      // may be seconds away and may already be behind what a moment's worth
      // of replayed frame_ready events is about to show.
      applyFrameProgress(entry, ev.file);
      view.syncEntry(entry);
      break;
    case "frame_ready":
      applyFrame(entry, ev);
      // Every landed frame keeps the row's bar/line current (TOR-167),
      // whether this point was just captured or replayed off disk - see
      // applyFrameProgress's own doc for why the heartbeat alone is not
      // enough for a top-up.
      applyFrameProgress(entry, ev.file);
      view.syncEntry(entry);
      break;
    case "frame_skipped": applyFrameSkipped(entry, ev); break;
    case "progress": applyProgress(entry, ev); break;
    case "budget_warning": applyBudgetWarning(entry, ev); break;
    case "file_done": applyFileDone(entry, ev); break;
    case "done": applyDone(entry, ev); break;
    case "failed": applyFailed(entry, ev); break;
    // AND THE TWO THAT LOOK LIKE THEY BELONG HERE AND DO NOT, written down
    // because looking for them is how a real handler gets dropped in a move
    // like this one:
    //
    //   CANCELLED is not a message type. It is a RunState (runs.go's
    //   RunCancelled), a StopReason on the done event (core.StopCancelled)
    //   and an ErrorCode on the failed one (core.CodeCancelled) - so a
    //   cancel reaches this page as a run_state whose state is "cancelled",
    //   which applyRunState handles like any other state, and cancellable()
    //   and FINAL in state.js are where the word is read.
    //
    //   AVAILABILITY is not a message type either. It is the swarm figure's
    //   own column key (LIVE_COLUMNS and sortValue) and a manifest field; the
    //   reading itself rides the progress heartbeat as ev.swarm.
    //
    // The genuine twelfth type is wire.go's "unknown", the fallback for a core
    // event it does not recognise, and it has no case on purpose: there is
    // nothing in it to apply.
  }
}

// The socket carries events only. Reconnecting replays every run the server
// still holds from the start, so a dropped connection costs nothing but a
// redraw of what it covers - a torrent this page never heard of before
// reconnecting (or one whose history the server has since trimmed,
// keepFinishedRuns) is unaffected either way.
//
// THE REPLAY IS WHY resetRunState EXISTS, and the reason TOR-184's trap is a
// reconnect trap: each run's history opens with a run_state carrying reset,
// and a page that kept entry.fetching across it would offer to stop a run
// whose history it is in the middle of re-reading.
//
// The address and the indicator are the page's (view.socketURL, view.status):
// the first needs document.baseURI and the access token, the second is a text
// node. Everything else about the connection is this module's.
function connect() {
  const socket = new WebSocket(view.socketURL());

  socket.onopen = () => view.status("live", "live");
  socket.onclose = () => {
    view.status("reconnecting", "lost");
    setTimeout(connect, 1000);
  };
  socket.onmessage = (message) => {
    try {
      apply(JSON.parse(message.data));
    } catch (err) {
      view.note("unreadable event: " + err);
    }
  };
}

export {
  VIEW_HOOKS,
  setView,
  apply,
  applyFrameProgress,
  applyFileList,
  applyRunState,
  connect,
};
