package web

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// TOR-191 split app.js into three modules - state.js (what a run and a file
// KNOW), events.js (the one place a message becomes a state change) and app.js
// (the page) - and this file is what makes that split worth having rather than
// merely tidy.
//
// EVERY TEST HERE RUNS THE SHIPPED MODULES FOR REAL. It reads state.js and
// events.js out of the embedded FS - the copy that actually ships - feeds real
// wire messages through the real apply(), and asserts on the STATE that comes
// out. That is only possible because neither module touches the DOM: state.js
// has no `document` and no `fetch`, events.js reaches the page only through a
// view of named hooks, and both facts are themselves checked below
// (TestTheEventLayerNeverTouchesTheDom).
//
// WHY THAT MATTERS MORE THAN ANOTHER TEXT GUARD, in this file's own subject:
// the four traps this ticket is written around are all WRONG-ANSWER traps, not
// missing-text ones. A rebuild that detaches every frame grid still contains
// the word "fileListSig"; a `fetching` set derived from picked-minus-deferred
// still contains "entry.fetching"; a run_state that merged ticks instead of
// assigning them still contains "ev.ticked". columns_test.go's own opening
// note records the measurement that says a substring cannot see the
// difference, and each of the four now has a test here that can.
//
// WHAT THIS FILE STILL CANNOT SEE, said plainly because a test that overclaims
// is worse than one that is narrow: nothing about the DOM. It records which
// redraws were ASKED FOR and in what order - which is exactly the fact TOR-182's
// trap turns on - but not one thing about what any of them actually drew. That
// is app.js's half, it needs a browser, and it is checked in a browser instead
// (this ticket's own pass, recorded at the bottom of this file).

// frontendModule returns one of the shipped ES modules' source, the same way
// appJS(t) does for app.js - out of the embedded FS, because that is the copy
// that ships.
func frontendModule(t *testing.T, name string) string {
	t.Helper()
	b, err := embedded.ReadFile("assets/" + name)
	if err != nil {
		t.Fatalf("reading the embedded %s: %v", name, err)
	}
	return string(b)
}

func stateJS(t *testing.T) string  { t.Helper(); return frontendModule(t, "state.js") }
func eventsJS(t *testing.T) string { t.Helper(); return frontendModule(t, "events.js") }

// jsFileSnapshot is one video file's state as the driver reports it back -
// the fields events.js writes, and nothing about how any of them is drawn.
type jsFileSnapshot struct {
	Frames        []int        `json:"frames"`
	FramesWithURL int          `json:"framesWithURL"`
	FrameURLs     []string     `json:"frameURLs"`
	Plan          []int        `json:"plan"`
	Skipped       []int        `json:"skipped"`
	Media         *jsMedia     `json:"media"`
	Heartbeat     *jsHeartbeat `json:"heartbeat"`
	SheetURL      string       `json:"sheetURL"`
}

type jsMedia struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Codec  string `json:"codec"`
	Audio  int    `json:"audio"`
}

type jsHeartbeat struct {
	FramesDone  int `json:"framesDone"`
	FramesTotal int `json:"framesTotal"`
	Downloaded  int `json:"downloaded"`
	Peers       int `json:"peers"`
}

// jsRunSnapshot is one run entry's state. Sets come back as sorted arrays so a
// failure prints something a person can read.
type jsRunSnapshot struct {
	State         string `json:"state"`
	Disk          bool   `json:"disk"`
	Name          string `json:"name"`
	Error         string `json:"error"`
	Progress      string `json:"progress"`
	Files         int    `json:"files"`
	Complete      int    `json:"complete"`
	Selected      int    `json:"selected"`
	Partial       bool   `json:"partial"`
	FramesDone    int    `json:"framesDone"`
	FramesTotal   int    `json:"framesTotal"`
	Priority      *int   `json:"priority"`
	QueuePosition int    `json:"queuePosition"`
	Arrival       int    `json:"arrival"`
	Picked        []int  `json:"picked"`
	Deferred      []int  `json:"deferred"`
	Fetching      []int  `json:"fetching"`
	Unticked      []int  `json:"unticked"`
	Tickable      bool   `json:"tickable"`
	PassCount     int    `json:"passCount"`
	Videos        int    `json:"videos"`
	FileList      int    `json:"fileList"`
	FileListKnown bool   `json:"fileListKnown"`
	FileListSig   string `json:"fileListSig"`
	SummaryLine   string `json:"summaryLine"`
	TorrentURL    string `json:"torrentURL"`
	Stall         string `json:"stall"`
	Peers         int    `json:"peers"`
	HasLive       bool   `json:"hasLive"`
	// The four TOR-202 texts: what the real cell functions (state.js's
	// peersCellText/seedsCellText/rateCellText) render for this row RIGHT
	// NOW, as opposed to Peers/HasLive above, which read entry.live raw and
	// are deliberately left alone so the pre-TOR-202 assertion at line ~985
	// keeps meaning what it always meant. hasLive above is the same raw
	// !!entry.live TestARunStateClaimingAReopeningRowSwapsItsIdWithoutThrowing
	// and its neighbours already rely on; LiveReading is hasLive() as TOR-202
	// left it - !!entry.live && !FINAL.has(entry.state) - the one a header's
	// cell actually asks.
	LiveReading  bool   `json:"liveReading"`
	PeersText    string `json:"peersText"`
	SeedsText    string `json:"seedsText"`
	DownloadText string `json:"downloadText"`
	UploadText   string `json:"uploadText"`
	// The page's own flags for a row expecting an id it does not hold yet: a
	// disk row being replayed (TOR-55) and a run the server re-armed (TOR-152).
	Reopening bool `json:"reopening"`
	Claiming  bool `json:"claiming"`
	// The two halves of TOR-180's collision, reported as what they ARE rather
	// than as what they hold: `files` has to stay a NUMBER (GET /runs' own
	// per-row count) and `fileList` an array, whatever a message carrying both
	// does to the entry.
	FilesType     string `json:"filesType"`
	FileListArray bool   `json:"fileListArray"`

	FileEntries map[string]jsFileSnapshot `json:"fileEntries"`
}

type jsApplyResult struct {
	Runs map[string]jsRunSnapshot `json:"runs"`
	// Calls is every view hook events.js asked for, in order, tagged with
	// which run or file it was about - so a redraw that should not have
	// happened is countable.
	Calls []string `json:"calls"`
	Logs  []string `json:"logs"`
	// Suspect names any hook that was handed something with a "type" key -
	// i.e. a wire message reaching a renderer, which is the whole thing this
	// ticket set out to stop.
	Suspect []string `json:"suspect"`
}

// applyDriver is the node program that runs the real event layer: it imports
// the two shipped modules unmodified, installs a view that records rather than
// draws, and replays a scripted list of messages through apply().
//
// NOTHING IS STUBBED except the view. state.js's own default factories build
// the data-only entry and file shapes, which is exactly what a run without a
// browser needs - so the store, the entry shape, the file shape, the
// signature, the sets and every derivation below them are the shipped code,
// not a copy of it.
const applyDriver = `
import * as S from "./state.js";
import * as E from "./events.js";
import { readFileSync } from "node:fs";

const calls = [];
const logs = [];
const suspect = [];

function label(x) {
  if (x && typeof x === "object" && x.id !== undefined) return "run " + x.id;
  if (x && typeof x === "object" && x.index !== undefined) return "file " + x.index;
  return String(x);
}

const view = {};
for (const hook of E.VIEW_HOOKS) {
  view[hook] = (...args) => {
    calls.push(hook + " " + label(args[0]));
    for (const a of args) {
      // A wire message is the one thing no hook may ever be handed.
      if (a && typeof a === "object" && !(a instanceof Map) && !(a instanceof Set) &&
          Object.prototype.hasOwnProperty.call(a, "type")) {
        suspect.push(hook);
      }
    }
    if (hook === "log") logs.push(label(args[0]) + " | " + args[1]);
    if (hook === "note") logs.push("| " + args[0]);
    if (hook === "socketURL") return "ws://example.invalid/events";
    return undefined;
  };
}
E.setView(view);

const sorted = (s) => [...s].sort((a, b) => a - b);

function snapshot() {
  const out = {};
  for (const [id, e] of S.state.runs) {
    out[id] = {
      state: e.state, disk: e.disk, name: e.name, error: e.error, progress: e.progress,
      files: typeof e.files === "number" ? e.files : -1,
      complete: e.complete, selected: e.selected, partial: e.partial,
      framesDone: e.framesDone, framesTotal: e.framesTotal,
      priority: e.priority, queuePosition: e.queuePosition, arrival: e.arrival,
        // Neither flag is ever on the wire; both are the page's own record of a
        // row waiting for an id, and both must come DOWN on the id swap
        // (TOR-203), so a test cannot check the swap without seeing them.
        reopening: !!e.reopening, claiming: !!e.claiming,
      picked: sorted(e.picked), deferred: sorted(e.deferred),
      fetching: sorted(e.fetching), unticked: sorted(e.unticked),
      tickable: e.tickable, passCount: e.passCount,
      videos: e.videos.length, fileList: e.fileList.length,
      fileListKnown: e.fileListKnown, fileListSig: e.fileListSig,
      summaryLine: e.summaryLine, torrentURL: e.torrentURL,
      stall: e.stall ? e.stall.code : "",
      peers: e.live ? e.live.peers : -1,
      hasLive: !!e.live,
      // TOR-202: the real reader-side answers, run through the shipped
      // functions rather than re-derived here - what a header cell would
      // actually show this instant, live reading or none. downBps/upBps
      // mirror run-table.js's own syncRow exactly (gated on hasLive(e), not
      // on the raw e.live) - rateCellText takes a bps, not an entry, so the
      // absence decision has to be made at the call site the same way the
      // real renderer makes it, or this driver would be checking a call
      // nothing on the page actually performs.
      liveReading: S.hasLive(e),
      peersText: S.peersCellText(e),
      seedsText: S.seedsCellText(e),
      downloadText: S.rateCellText(S.hasLive(e) ? e.live.download_bps : null),
      uploadText: S.rateCellText(S.hasLive(e) ? e.live.upload_bps : null),
      filesType: typeof e.files,
      fileListArray: Array.isArray(e.fileList),
      fileEntries: Object.fromEntries([...e.fileEntries].map(([i, f]) => [String(i), {
        frames: [...f.frames.keys()].sort((a, b) => a - b),
        framesWithURL: [...f.frames.values()].filter((fr) => fr.url).length,
        frameURLs: [...f.frames.values()].map((fr) => String(fr.url)),
        plan: f.plan,
        skipped: [...f.skipped.keys()].sort((a, b) => a - b),
        media: f.media
          ? { width: f.media.width, height: f.media.height, codec: f.media.codec,
              audio: f.media.audio.length }
          : null,
        heartbeat: f.heartbeat,
        sheetURL: f.sheetURL,
      }])),
    };
  }
  return out;
}

const input = JSON.parse(readFileSync(0, "utf8"));
for (const step of input.steps) {
  if (step.mark) calls.push("--- " + step.mark);
  if (step.event) E.apply(step.event);
  // The one thing on this page the server does not own (TOR-183): somebody
  // un-ticking a finished file, which tickFile records here and nothing on
  // the wire ever carries.
  if (step.untick) S.state.runs.get(step.untick.run).unticked.add(step.untick.file);
  // The other thing the wire never carries: a row WAITING for an id it does
  // not have yet - a disk row being replayed (reopening, TOR-55) or a run the
  // server re-armed (claiming, TOR-152). The page raises the flag itself when
  // it posts, and the id arrives in a later run_state, so this step is the
  // only way to script resolveIncomingRun's id-swap branch (TOR-203).
  if (step.reopen) {
    const waiting = S.state.runs.get(step.reopen.run);
    if (step.reopen.claiming) waiting.claiming = true;
    else waiting.reopening = true;
  }
}
process.stdout.write(JSON.stringify({ runs: snapshot(), calls, logs, suspect }));
`

// runApply feeds a scripted list of steps through the real event layer and
// returns what the state looks like afterwards.
//
// The modules are copied into a temp directory beside a package.json declaring
// {"type":"module"}, because node reads a bare .js as CommonJS otherwise and
// the shipped files must not be renamed or edited to be testable - that is the
// whole point of reading them out of the embedded FS. Any node failure is
// reported with the FULL stdout and stderr, never a tail of either (this
// project's own standard: a truncated diagnostic hides exactly the harness
// failures this shape produces).
func runApply(t *testing.T, steps []map[string]any) jsApplyResult {
	t.Helper()
	node := requireNode(t)

	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s into the harness directory: %v", name, err)
		}
	}
	write("package.json", `{"type":"module"}`)
	write("state.js", stateJS(t))
	write("events.js", eventsJS(t))
	write("driver.js", applyDriver)

	input, err := json.Marshal(struct {
		Steps []map[string]any `json:"steps"`
	}{steps})
	if err != nil {
		t.Fatalf("marshalling this test's own steps to JSON: %v", err)
	}

	cmd := exec.Command(node, filepath.Join(dir, "driver.js"))
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node exited with an error running the shipped event layer: %v\n"+
			"--- stderr ---\n%s\n--- stdout ---\n%s\n--- steps ---\n%s",
			err, stderr.String(), stdout.String(), input)
	}

	var out jsApplyResult
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("node's stdout was not the JSON snapshot this harness expects: %v\n"+
			"--- stdout ---\n%s\n--- stderr ---\n%s", err, stdout.String(), stderr.String())
	}
	return out
}

// runIDs names the keys the store ended up with, sorted, so a failure about a
// row that is missing or duplicated prints where the rows actually went.
func (r jsApplyResult) runIDs() []string {
	ids := make([]string, 0, len(r.Runs))
	for id := range r.Runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func event(fields map[string]any) map[string]any { return map[string]any{"event": fields} }

// runStateFor is the message every other one arrives behind: the socket
// replays a run's history from the start, so nothing else is applied to a run
// this page has not first been told about.
func runStateFor(id, runState string, extra map[string]any) map[string]any {
	ev := map[string]any{"type": "run_state", "run": id, "state": runState}
	for k, v := range extra {
		ev[k] = v
	}
	return event(ev)
}

func countCalls(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// TRAP 1 (TOR-180): the run entry's own shape.

// TestTheRunEntryKeepsBothFilesAndFileListWhenAMessageCarriesEachOfThem is
// TOR-180's collision, checked by ANSWER rather than by counting keys in the
// source (TestNoRunEntryFieldIsDeclaredTwice does that, and is the guard
// against a NEW duplicate; this is the guard against the old one coming back
// in some other form).
//
// `files` is GET /runs' per-row COUNT of video files, read by badgeState and
// metaLabel; `fileList` is what the torrent actually holds. When both were
// spelled `files` in one literal the second silently won, and the badge's
// completeness arithmetic started reading an array's length as a count. So the
// test drives exactly that sequence - a run_state carrying the counts, then a
// metadata_ready carrying the list - and asserts each field is still the KIND
// of thing its reader expects.
func TestTheRunEntryKeepsBothFilesAndFileListWhenAMessageCarriesEachOfThem(t *testing.T) {
	got := runApply(t, []map[string]any{
		// A row nothing has said anything about yet, which is where the
		// collision actually bites and where the falsification run for this
		// test found the arm below insufficient: rename fileList to files in
		// newRunState and the DEFAULTS collide, but the two messages r1 gets
		// then reassign both fields to the right kinds and repair it. A fresh
		// entry has nothing to repair it, so this is the arm that fails.
		runStateFor("bare", "queued", nil),
		runStateFor("r1", "running", nil),
		// The one run_state that carries the completeness counts (server.go's
		// pump, TOR-87).
		runStateFor("r1", "done", map[string]any{
			"files": 5, "complete": 3, "selected": 5, "partial": true,
		}),
		event(map[string]any{
			"type": "metadata_ready", "run": "r1", "name": "Sintel", "infohash": "abc",
			"videos": []map[string]any{{"index": 0, "path": "a.mkv"}},
			"files": []map[string]any{
				{"index": 0, "path": "a.mkv"}, {"index": 1, "path": "a.nfo"},
				{"index": 2, "path": "cover.jpg"},
			},
		}),
	})

	// THE FRESH ROW FIRST. entry.files must be the numeric count GET /runs
	// reports per row and entry.fileList the torrent's own list, from the
	// moment the entry exists - and it is the DEFAULTS that carry that, since
	// a row can sit in the table for a long time before any message says
	// either. (Worth recording while this is being written: entry.files has no
	// reader in the front end today - loadRuns and the run_state handler both
	// write it and nothing reads it - so what this arm really guards is the
	// SHAPE, which is the thing the collision destroyed.)
	if bare := got.Runs["bare"]; bare.FilesType != "number" || !bare.FileListArray {
		t.Errorf("a row nothing has described yet has files as a %s and fileList as an array=%v, "+
			"want number and true - one of them is shadowing the other in the entry's two "+
			"literals, which is TOR-180 exactly", bare.FilesType, bare.FileListArray)
	}

	r := got.Runs["r1"]
	if r.FilesType != "number" {
		t.Errorf("entry.files is a %s, not a number - GET /runs' per-row video COUNT has been "+
			"shadowed by something else under the same key, which is TOR-180 exactly", r.FilesType)
	}
	if r.Files != 5 {
		t.Errorf("entry.files = %d, want 5 - the count the badge's completeness arithmetic reads", r.Files)
	}
	if !r.FileListArray {
		t.Error("entry.fileList is not an array - the torrent's own file list has been shadowed")
	}
	if r.FileList != 3 || r.Videos != 1 {
		t.Errorf("entry.fileList holds %d and entry.videos %d, want 3 and 1 - the whole list and "+
			"the capturable part of it are two different answers", r.FileList, r.Videos)
	}
	if r.Complete != 3 || r.Selected != 5 || !r.Partial {
		t.Errorf("the completeness counts did not survive the file list: complete=%d selected=%d "+
			"partial=%v, want 3, 5, true", r.Complete, r.Selected, r.Partial)
	}
}

// ---------------------------------------------------------------------------
// TRAP 2 (TOR-182): a second file list must not rebuild the rows.

// TestASecondIdenticalFileListRebuildsNothing is the trap in full, and the one
// this ticket's split most changes the shape of: the decision now lives in
// events.js (applyFileList's signature) and the rebuild is a redraw the view
// is either asked for or not - which is what makes it countable here without a
// browser.
//
// A top-up and a retry (TOR-152) each mint a run whose events land on THIS
// entry, and each publishes its own metadata_ready. Before TOR-182 the second
// one merely redrew an identical list; since TOR-182 a rebuild takes every
// frame grid, every open disclosure and every element fileBlock is holding a
// reference to down with it, mid-run, on a row whose frames are on screen.
//
// Four moments, and the last two are what stop this test passing for the wrong
// reason - a signature that suppressed EVERYTHING would satisfy the first two
// and be a worse bug than the one it fixed.
func TestASecondIdenticalFileListRebuildsNothing(t *testing.T) {
	listA := []map[string]any{{"index": 0, "path": "a.mkv"}, {"index": 1, "path": "b.mkv"}}
	listB := []map[string]any{{"index": 0, "path": "a.mkv"}, {"index": 1, "path": "c.mkv"}}
	meta := func(files []map[string]any) map[string]any {
		return event(map[string]any{
			"type": "metadata_ready", "run": "r1", "name": "Pack", "infohash": "abc",
			"videos": files, "files": files,
		})
	}

	got := runApply(t, []map[string]any{
		runStateFor("r1", "running", nil),
		{"mark": "first"},
		meta(listA),
		{"mark": "same again"},
		meta(listA),
		{"mark": "a different list"},
		meta(listB),
		{"mark": "a reset, then the first list again"},
		runStateFor("r1", "running", map[string]any{"reset": true}),
		meta(listA),
	})

	rebuilds := countCalls(got.Calls, "rebuildFileList")
	if rebuilds != 3 {
		t.Errorf("the rows were rebuilt %d times across four file-list messages, want 3 "+
			"(the first list, the changed list, and the first list again after a reset - "+
			"never the identical repeat). The whole call order was:\n%s",
			rebuilds, strings.Join(got.Calls, "\n"))
	}

	// And the identical repeat specifically: between the "same again" mark and
	// the next one there must be no rebuild at all.
	from := slices.Index(got.Calls, "--- same again")
	to := slices.Index(got.Calls, "--- a different list")
	if from < 0 || to < 0 {
		t.Fatalf("the harness lost its own marks: %v", got.Calls)
	}
	for _, c := range got.Calls[from:to] {
		if strings.HasPrefix(c, "rebuildFileList") {
			t.Errorf("a metadata_ready repeating a list the row already holds asked for a rebuild "+
				"(%q) - that detaches every frame grid on the row, mid-run", c)
		}
	}

	// The reset case is the other direction and has its own history of going
	// wrong: resetRunState clears the signature along with the list, so the
	// replayed metadata_ready that follows a reconnect must NOT be told "you
	// already drew this".
	after := slices.Index(got.Calls, "--- a reset, then the first list again")
	if after < 0 {
		t.Fatal("the harness lost the reset mark")
	}
	if countCalls(got.Calls[after:], "rebuildFileList") != 1 {
		t.Errorf("after a reset, the replayed file list did not rebuild the rows - a reconnecting "+
			"page would come back with an empty list (TOR-182). Calls after the reset:\n%s",
			strings.Join(got.Calls[after:], "\n"))
	}
	if got.Runs["r1"].FileListSig == "" {
		t.Error("entry.fileListSig is empty after a list was drawn - nothing would suppress the " +
			"next identical message")
	}
}

// ---------------------------------------------------------------------------
// TRAP 3 (TOR-183): run_state ASSIGNS the ticks, and the local un-tick is not
// on the wire at all.

// TestRunStateAssignsTheTicksAndNeverTouchesTheLocalUnTick checks both halves
// of the reason un-ticking a finished file is page-local.
//
// ASSIGNED, NOT MERGED: entry.picked is the server's answer, so a tick that
// was refused, or a second browser tab ticking the same torrent, arrives here
// and wins. A merge would leave the boxes disagreeing with what is actually
// being fetched, and - the case that makes it a bug rather than a nicety - a
// file the server DROPPED would stay ticked forever.
//
// AND entry.unticked SURVIVES, because run_state carries nothing for it: an
// un-tick of a finished file asks nothing of the server and spends nothing, so
// there is no field for it to ride on. If it had been recorded in
// entry.picked instead, the very next run_state would undo it - which is the
// whole reason the set exists.
func TestRunStateAssignsTheTicksAndNeverTouchesTheLocalUnTick(t *testing.T) {
	got := runApply(t, []map[string]any{
		runStateFor("r1", "running", map[string]any{
			"ticked": []int{0, 1}, "tickable": true, "count": 20,
		}),
		// Somebody un-ticks file 0 on a finished row (tickFile's own
		// TOR-183 branch, which asks nothing of the server).
		runStateFor("r1", "done", map[string]any{"ticked": []int{0, 1}}),
		{"untick": map[string]any{"run": "r1", "file": 0}},
		{"mark": "after the local un-tick"},
		// A later run_state for the same row - a reorder elsewhere, a second
		// tab, anything.
		runStateFor("r1", "done", map[string]any{"ticked": []int{0, 1}}),
		{"mark": "and one where the server dropped file 0"},
		runStateFor("r1", "done", map[string]any{"ticked": []int{1}}),
	})

	r := got.Runs["r1"]
	if !slices.Equal(r.Picked, []int{1}) {
		t.Errorf("entry.picked = %v, want [1] - run_state ASSIGNS what this row has asked for, so "+
			"a file the server no longer holds has to disappear from the set. A merge leaves a "+
			"box ticked for something nothing is fetching", r.Picked)
	}
	if !slices.Equal(r.Unticked, []int{0}) {
		t.Errorf("entry.unticked = %v, want [0] - the local un-tick is the one piece of tick state "+
			"the server does not own, and three run_states in a row must not have touched it "+
			"(TOR-183)", r.Unticked)
	}
}

// TestTheTicksOfAnEarlierPassAreNotClearedByTheNextFileList is the other half
// of the same subject and a bug the browser found rather than the tests: a
// message's "selected" is the pass IN FLIGHT, which for a second pass names
// only the files that pass was re-armed with - so ASSIGNING it would clear the
// tick beside a file the first pass had already captured, which reads as the
// capture having been undone.
func TestTheTicksOfAnEarlierPassAreNotClearedByTheNextFileList(t *testing.T) {
	files := []map[string]any{
		{"index": 0, "path": "a.mkv"}, {"index": 1, "path": "b.mkv"}, {"index": 2, "path": "c.mkv"},
	}
	got := runApply(t, []map[string]any{
		runStateFor("r1", "running", map[string]any{"ticked": []int{0}}),
		event(map[string]any{
			"type": "metadata_ready", "run": "r1", "name": "Pack", "infohash": "abc",
			"videos": files, "files": files, "selected": []int{0},
		}),
		// A second pass on the same row, re-armed with file 2 alone. Its own
		// metadata_ready carries a "selected" naming only that file.
		event(map[string]any{
			"type": "metadata_ready", "run": "r1", "name": "Pack", "infohash": "abc",
			"videos": files, "files": files, "selected": []int{2},
		}),
	})

	if got := got.Runs["r1"].Picked; !slices.Equal(got, []int{0, 2}) {
		t.Errorf("entry.picked = %v, want [0 2] - a file list's \"selected\" is ADDED to what the "+
			"row has asked for, never assigned over it, or the second pass would un-tick the file "+
			"the first one captured (TOR-181)", got)
	}
}

// ---------------------------------------------------------------------------
// TRAP 4 (TOR-184): the pass in flight is READ, and a reconnect must not
// inherit it.

// TestThePassInFlightIsReadFromTheServerAndNotDerived is the trap stated as
// the arithmetic that looks right and is wrong.
//
// "picked minus deferred" is the derivation anybody would reach for, and it is
// wrong in exactly one direction: picked is CUMULATIVE, so the difference also
// holds every file an EARLIER pass on this row already captured - and those
// are files nothing is fetching. The two need opposite answers, because
// un-ticking a file being fetched stops this torrent's fetch and un-ticking
// one a previous pass finished must not.
//
// So the row below is the one that tells the two apart: file 0 was captured by
// an earlier pass, file 1 is being fetched now, file 2 is waiting for the next
// pass. The derivation would answer {0, 1}; the server's own set answers {1}.
func TestThePassInFlightIsReadFromTheServerAndNotDerived(t *testing.T) {
	got := runApply(t, []map[string]any{
		runStateFor("r1", "running", map[string]any{
			"ticked": []int{0, 1, 2}, "deferred": []int{2}, "fetching": []int{1},
			"tickable": true,
		}),
	})

	r := got.Runs["r1"]
	if !slices.Equal(r.Fetching, []int{1}) {
		t.Errorf("entry.fetching = %v, want [1] - it is the server's own set (run_state's "+
			"\"fetching\", runEntry.fetchingLocked), never picked minus deferred, which would "+
			"answer [0 1] and offer to stop this run over a file an earlier pass finished", r.Fetching)
	}
	if !slices.Equal(r.Picked, []int{0, 1, 2}) {
		t.Errorf("entry.picked = %v, want [0 1 2]", r.Picked)
	}
	if !slices.Equal(r.Deferred, []int{2}) {
		t.Errorf("entry.deferred = %v, want [2]", r.Deferred)
	}
}

// TestAReconnectLeavesNoStalePassBehind is TOR-184's own reconnect trap, and
// the one the ticket calls out by name: the socket replays each run's history
// from the start behind a run_state carrying reset, and a page that kept
// entry.fetching across it would offer to STOP a run whose history it is in
// the middle of re-reading - which is the one act on this page that reaches
// the server without being asked twice.
//
// The replayed history is deliberately not complete here: it stops after the
// reset, which is exactly the window the trap lives in (a real reconnect has
// several messages in flight before the run's current state arrives again).
func TestAReconnectLeavesNoStalePassBehind(t *testing.T) {
	files := []map[string]any{{"index": 0, "path": "a.mkv"}, {"index": 1, "path": "b.mkv"}}
	got := runApply(t, []map[string]any{
		// A live row, mid-pass, with everything a reconnect could inherit.
		runStateFor("r1", "running", map[string]any{
			"ticked": []int{0, 1}, "deferred": []int{1}, "fetching": []int{0},
			"tickable": true, "tick_refusal": "", "count": 20,
		}),
		event(map[string]any{
			"type": "metadata_ready", "run": "r1", "name": "Pack", "infohash": "abc",
			"videos": files, "files": files,
		}),
		event(map[string]any{
			"type": "file_started", "run": "r1", "file": 0, "path": "a.mkv",
			"width": 1920, "height": 1080, "codec": "h264", "plan": []int{1000, 2000},
			"planned": 2,
		}),
		event(map[string]any{
			"type": "frame_ready", "run": "r1", "file": 0, "index": 0,
			"actual_ms": 1000, "url": "files/f1",
		}),
		event(map[string]any{
			"type": "done", "run": "r1", "reason": "complete", "torrent_url": "files/t1",
			"frames": 1, "files": 1, "downloaded": 100, "elapsed_ms": 1000,
		}),
		// The socket drops and comes back. The connection marker carries no
		// "run" and is not about any torrent; each run's history then opens
		// with a reset.
		event(map[string]any{"type": "run_state", "active": false, "source": "", "reset": true}),
		{"mark": "reconnected"},
		runStateFor("r1", "queued", map[string]any{"reset": true}),
	})

	r := got.Runs["r1"]
	for _, c := range []struct {
		name string
		got  []int
	}{
		{"fetching", r.Fetching}, {"picked", r.Picked}, {"deferred", r.Deferred},
		{"unticked", r.Unticked},
	} {
		if len(c.got) != 0 {
			t.Errorf("entry.%s = %v after a reconnect's reset, want empty - the replayed history "+
				"is about to state all of it again, and a stale set survives to offer an act "+
				"about a file this page has not yet been told anything about (TOR-184)",
				c.name, c.got)
		}
	}
	if r.Tickable {
		t.Error("entry.tickable survived the reset - a live checkbox the server would refuse is " +
			"worse than a disabled one it would have taken")
	}
	if r.FileListSig != "" || r.FileList != 0 || r.Videos != 0 {
		t.Errorf("the file list survived the reset (sig=%q, %d files, %d videos) - the row would "+
			"come back from a reconnect with a list the server may since have moved past",
			r.FileListSig, r.FileList, r.Videos)
	}
	if r.TorrentURL != "" {
		t.Errorf("entry.torrentURL = %q after the reset - the .torrent handle comes back from the "+
			"done event the replayed history ends on, and the server may no longer resolve this one",
			r.TorrentURL)
	}
	if len(r.FileEntries) != 0 {
		t.Errorf("%d file entries survived the reset - every one of them points at a row the "+
			"rebuild is about to detach", len(r.FileEntries))
	}
	if r.FramesDone != 0 || r.FramesTotal != 0 || r.Progress != "" {
		t.Errorf("the row's own progress survived the reset (%d/%d, %q) - renderRunProgress would "+
			"draw a bar for a file the page no longer has anything about (TOR-167)",
			r.FramesDone, r.FramesTotal, r.Progress)
	}

	// And the reset must have asked the page to clear what it drew, not only
	// the entry: the two halves are called together and nothing else calls
	// either (state.js's resetRunState, app.js's resetRunView).
	after := slices.Index(got.Calls, "--- reconnected")
	if after < 0 {
		t.Fatal("the harness lost the reconnect mark")
	}
	if countCalls(got.Calls[after:], "resetRunView") != 1 {
		t.Errorf("the reset did not ask the page to clear the row it emptied. Calls after the "+
			"reconnect:\n%s", strings.Join(got.Calls[after:], "\n"))
	}
}

// TestResetRunStateEmptiesEverythingAReplayWillStateAgain calls resetRunState
// DIRECTLY, on an entry with something in every field it claims to clear.
//
// THIS TEST EXISTS BECAUSE THE FALSIFICATION RUN FOUND A HOLE. Commenting out
// `entry.fetching.clear()` in resetRunState reddened nothing at all: driven
// through apply(), the run_state that carries a reset goes on to ASSIGN
// entry.fetching from the message a few lines later, so the clear is
// unobservable from outside - and untick_test.go's own text guard was
// satisfied by the commented-out line, because a comment still contains the
// substring.
//
// Both halves are worth fixing rather than shrugging at. The text guard now
// strips comments (untick_test.go). And the clear is not pointless: it is what
// makes resetRunState correct ON ITS OWN, for the next caller, rather than
// correct only because its one caller happens to reassign four of these
// fields afterwards. That is exactly the property a test should pin, and the
// only way to pin it is to call the function rather than the page.
//
// The fields fall into two groups and the difference is the point: some are
// reassigned by the run_state that follows (picked, deferred, fetching,
// tickable, tickRefusal, passCount), and the rest are cleared HERE and
// nowhere else - unticked above all, which no message ever carries (TOR-183).
func TestResetRunStateEmptiesEverythingAReplayWillStateAgain(t *testing.T) {
	node := requireNode(t)
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	write("package.json", `{"type":"module"}`)
	write("state.js", stateJS(t))
	write("driver.js", `
import * as S from "./state.js";

const e = S.ensureRun("r1");
// Something in every field the function claims to clear, so an omitted line
// shows up as a leftover rather than as a value that was empty anyway.
e.error = "the reopen failed";
e.summaryLine = "Sintel - 3 video file(s)";
e.stall = { code: "no_peers", since_ms: 4000, observedAt: Date.now() };
e.runningSince = 1700000000000;
e.progress = "3/9 frames";
e.framesDone = 3;
e.framesTotal = 9;
e.videos = [{ index: 0, path: "a.mkv" }];
e.fileList = [{ index: 0, path: "a.mkv" }, { index: 1, path: "a.nfo" }];
e.fileListKnown = true;
e.fileListSig = "files:0 a.mkv";
e.picked.add(0); e.picked.add(1);
e.deferred.add(1);
e.fetching.add(0);
e.unticked.add(0);
e.tickable = true;
e.tickRefusal = "this run has settled";
e.passCount = 20;
e.armed = true;
e.autoExpanded = true;
e.torrentURL = "files/abc.torrent";
S.fileEntryFor(e, 0);
S.fileEntryFor(e, 1);

const filled = { fileEntries: e.fileEntries.size, picked: e.picked.size };
S.resetRunState(e);

process.stdout.write(JSON.stringify({
  filled,
  error: e.error, summaryLine: e.summaryLine, stall: e.stall,
  runningSince: e.runningSince, progress: e.progress,
  framesDone: e.framesDone, framesTotal: e.framesTotal,
  videos: e.videos.length, fileList: e.fileList.length,
  fileListKnown: e.fileListKnown, fileListSig: e.fileListSig,
  picked: e.picked.size, deferred: e.deferred.size,
  fetching: e.fetching.size, unticked: e.unticked.size,
  tickable: e.tickable, tickRefusal: e.tickRefusal, passCount: e.passCount,
  armed: e.armed, autoExpanded: e.autoExpanded, torrentURL: e.torrentURL,
  fileEntries: e.fileEntries.size,
  // And what a reset must NOT touch: the row's identity and its place in the
  // table. A reset is one run's history starting over, not a new torrent.
  id: e.id, when: typeof e.when,
}));
`)

	cmd := exec.Command(node, filepath.Join(dir, "driver.js"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node exited with an error calling resetRunState: %v\n--- stderr ---\n%s\n"+
			"--- stdout ---\n%s", err, stderr.String(), stdout.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("node's stdout was not JSON: %v\n%s", err, stdout.String())
	}

	// The setup has to have actually filled things, or every assertion below
	// is comparing empty against empty.
	filled, _ := got["filled"].(map[string]any)
	if filled == nil || filled["fileEntries"] != float64(2) || filled["picked"] != float64(2) {
		t.Fatalf("the driver did not populate the entry before resetting it (%v) - every "+
			"assertion below would pass by measuring nothing", got["filled"])
	}

	for _, want := range []struct {
		field string
		value any
		why   string
	}{
		{"error", "", "the replayed history states its own error, or none"},
		{"summaryLine", "", "the name comes back from the replayed metadata_ready"},
		{"stall", nil, "a reading from before the reset would sit stale until the next heartbeat"},
		{"runningSince", float64(0), "waitingForMetadata's clock belongs to the run being replayed"},
		{"progress", "", "renderRunProgress would draw a bar for a file the page has nothing about"},
		{"framesDone", float64(0), "same bar, same reason (TOR-167)"},
		{"framesTotal", float64(0), "same bar, same reason (TOR-167)"},
		{"videos", float64(0), "the list comes back from the replayed metadata_ready or needs_action"},
		{"fileList", float64(0), "as above - a stale copy is a list the server may have moved past"},
		{"fileListKnown", false, "whether the whole list was known is a fact about that message"},
		{"fileListSig", "", "left behind it tells the replay \"you already drew this\" (TOR-182)"},
		{"picked", float64(0), "run_state assigns it, but the reset is what stops a union accumulating"},
		{"deferred", float64(0), "as picked"},
		{"fetching", float64(0), "a stale set offers to STOP a run whose history is being re-read (TOR-184)"},
		{"unticked", float64(0), "nothing on the wire carries it, so this is the ONLY thing that clears it (TOR-183)"},
		{"tickable", false, "false is the safe direction - a live box the server would refuse is worse"},
		{"tickRefusal", "", "the server's sentence for a state this row is about to be told again"},
		{"passCount", float64(0), "0 means the server's own -n, which the intake box displays"},
		{"armed", false, "a whole torrent's bill must not sit under one stray press on an emptied row"},
		{"autoExpanded", false, "the latch is what makes only the FIRST file open itself"},
		{"torrentURL", "", "the handle comes back from the done event, and may no longer resolve"},
		{"fileEntries", float64(0), "every one of them points at a row the rebuild detaches"},
	} {
		if got[want.field] != want.value {
			t.Errorf("resetRunState left entry.%s as %#v, want %#v - %s",
				want.field, got[want.field], want.value, want.why)
		}
	}

	// And the two it must leave alone.
	if got["id"] != "r1" {
		t.Errorf("resetRunState changed the entry's id to %#v - a reset is one run's history "+
			"starting over, not a different torrent", got["id"])
	}
	if got["when"] != "number" {
		t.Errorf("resetRunState left entry.when a %v - it is the default sort's key, and a row "+
			"must not jump position because its history was replayed", got["when"])
	}
}

// ---------------------------------------------------------------------------
// The dispatch itself.

// TestEveryEventTypeTheServerCanPublishIsHandled is the acceptance criterion's
// own demand, run rather than read: one session carrying every type the server
// can put on the wire, and a state at the end that shows each of them arrived
// somewhere.
//
// THE TWELVE, against the two files that publish them:
//
//	server.go: run_state, needs_action
//	wire.go:   metadata_ready, file_started, frame_ready, frame_skipped,
//	           progress, budget_warning, file_done, done, failed, unknown
//
// "unknown" is wire.go's fallback for a core event it does not recognise and
// is the one type with no handler ON PURPOSE - there is nothing in it to
// apply - so it is fed in here to show it passes through harmlessly rather
// than being quietly absent from the test.
//
// Two types a reader expects and will not find, because looking for them is
// how a real handler gets dropped in a move like this one: CANCELLED is a
// RunState and a StopReason, not a message (a cancel arrives as a run_state
// whose state is "cancelled", exercised below), and AVAILABILITY is a column
// key and a manifest field, with the reading itself riding the progress
// heartbeat as ev.swarm.
func TestEveryEventTypeTheServerCanPublishIsHandled(t *testing.T) {
	files := []map[string]any{{"index": 0, "path": "a.mkv"}, {"index": 1, "path": "notes.nfo"}}
	videos := files[:1]

	got := runApply(t, []map[string]any{
		// 1. run_state
		runStateFor("r1", "queued", map[string]any{
			"infohash": "abc", "source": "magnet:?xt=1", "arrival": 3,
			"priority": 0, "queue_position": 2, "tickable": true,
		}),
		// 2. needs_action
		event(map[string]any{
			"type": "needs_action", "run": "r1", "name": "Pack", "infohash": "abc",
			"videos": videos, "files": files,
		}),
		// 3. metadata_ready
		event(map[string]any{
			"type": "metadata_ready", "run": "r1", "name": "Pack", "infohash": "abc",
			"videos": videos, "files": files, "selected": []int{0},
		}),
		// 4. file_started
		event(map[string]any{
			"type": "file_started", "run": "r1", "file": 0, "path": "dir/a.mkv",
			"width": 1920, "height": 1080, "codec": "h264", "profile": "High",
			"fps": 23.976, "bitrate": 8000000, "video_bitrate": 7000000,
			"duration_ms": 600000, "planned": 3, "plan": []int{1000, 2000, 3000},
			"audio":     []map[string]any{{"language": "eng", "codec": "aac", "channels": 6}},
			"subtitles": []map[string]any{{"language": "eng", "codec": "subrip"}},
		}),
		// 5. frame_ready
		event(map[string]any{
			"type": "frame_ready", "run": "r1", "file": 0, "index": 0,
			"actual_ms": 1000, "url": "files/f0", "shift": "",
		}),
		// 6. frame_skipped
		event(map[string]any{
			"type": "frame_skipped", "run": "r1", "file": 0, "index": 1,
			"code": "unavailable", "reason": "no peer offered these pieces",
		}),
		// 7. progress
		event(map[string]any{
			"type": "progress", "run": "r1", "file": 0, "frames_done": 1, "frames_total": 3,
			"downloaded": 12345, "peers": 7, "seeds": 2,
			"download_bps": 500000, "upload_bps": 0,
			"swarm": map[string]any{"copies_per_piece": 2.5, "unavailable": 0, "pieces": 100},
		}),
		// 8. budget_warning
		event(map[string]any{
			"type": "budget_warning", "run": "r1", "scope": "client",
			"spent": 900, "limit": 1000,
		}),
		// 9. file_done
		event(map[string]any{
			"type": "file_done", "run": "r1", "file": 0, "path": "dir/a.mkv",
			"frames": 2, "skipped": 1, "sheet_url": "files/sheet0",
			"manifest_url": "files/manifest0",
		}),
		// 10. done
		event(map[string]any{
			"type": "done", "run": "r1", "reason": "time", "frames": 2, "files": 1,
			"downloaded": 12345, "elapsed_ms": 60000, "torrent_url": "files/t0",
			"warnings": []string{"the .torrent could not be written"},
		}),
		// 11. failed, on a second run so it does not overwrite the first.
		runStateFor("r2", "running", map[string]any{"infohash": "def"}),
		event(map[string]any{
			"type": "failed", "run": "r2", "file": -1, "code": "no_metadata",
			"error": "no peer answered",
		}),
		// 12. unknown - wire.go's fallback, deliberately without a handler.
		event(map[string]any{"type": "unknown", "run": "r1"}),
		// And a cancel, which is a run_state rather than a type of its own.
		runStateFor("r2", "cancelled", nil),
	})

	r1 := got.Runs["r1"]
	// A run's STATE comes from run_state and from nothing else - the done and
	// failed events carry the run's own reckoning of what it produced, not a
	// state - so this row is still "queued" here, which is exactly right and
	// worth asserting rather than assuming: an event that started writing
	// entry.state would be a second place a row's state is decided.
	if r1.State != "queued" {
		t.Errorf("r1 state = %q, want queued - only run_state may set a row's state, and this "+
			"session sent just the one", r1.State)
	}
	if r1.Name != "Pack" || r1.SummaryLine != "Pack" {
		t.Errorf("metadata_ready did not land: name=%q summaryLine=%q", r1.Name, r1.SummaryLine)
	}
	if r1.FileList != 2 || r1.Videos != 1 || !r1.FileListKnown {
		t.Errorf("the file list did not land: %d files, %d videos, known=%v",
			r1.FileList, r1.Videos, r1.FileListKnown)
	}
	if !slices.Equal(r1.Picked, []int{0}) {
		t.Errorf("metadata_ready's own selection did not land: picked=%v", r1.Picked)
	}
	if r1.Arrival != 3 || r1.QueuePosition != 2 || r1.Priority == nil || *r1.Priority != 0 {
		t.Errorf("run_state's queue fields did not land: arrival=%d position=%d priority=%v",
			r1.Arrival, r1.QueuePosition, r1.Priority)
	}
	if !r1.HasLive || r1.Peers != 7 {
		t.Errorf("progress' swarm reading did not land: hasLive=%v peers=%d", r1.HasLive, r1.Peers)
	}
	if r1.TorrentURL != "files/t0" {
		t.Errorf("done's .torrent handle did not land: %q", r1.TorrentURL)
	}
	// And what done TAKES AWAY, which is the other half of its own handler: a
	// finished run cannot still be "stalled", it is simply over, and the
	// progress line it was showing stopped being true at the same instant.
	if r1.Progress != "" || r1.Stall != "" {
		t.Errorf("done left the row's progress line (%q) or stall reading (%q) standing - a "+
			"run that has ended is not going slowly, it is over", r1.Progress, r1.Stall)
	}

	f0, ok := r1.FileEntries["0"]
	if !ok {
		t.Fatalf("no file entry for file 0 at all - file_started never landed. Runs: %+v", r1)
	}
	if f0.Media == nil || f0.Media.Width != 1920 || f0.Media.Codec != "h264" || f0.Media.Audio != 1 {
		t.Errorf("file_started's media did not land on the file entry: %+v", f0.Media)
	}
	if !slices.Equal(f0.Plan, []int{1000, 2000, 3000}) {
		t.Errorf("file_started's plan did not land: %v", f0.Plan)
	}
	if !slices.Equal(f0.Frames, []int{1000}) || f0.FramesWithURL != 1 {
		t.Errorf("frame_ready did not land: frames=%v withURL=%d", f0.Frames, f0.FramesWithURL)
	}
	if !slices.Equal(f0.Skipped, []int{1}) {
		t.Errorf("frame_skipped did not land: %v", f0.Skipped)
	}
	if f0.SheetURL != "files/sheet0" {
		t.Errorf("file_done's contact sheet did not land: %q", f0.SheetURL)
	}
	// file_done takes the heartbeat away rather than hiding the line over it.
	if f0.Heartbeat != nil {
		t.Errorf("the heartbeat survived file_done: %+v - the line would sit over a finished "+
			"file saying how far it had got", f0.Heartbeat)
	}

	if got.Runs["r2"].State != "cancelled" {
		t.Errorf("r2 state = %q, want cancelled - a cancel is a run_state, not a message type of "+
			"its own", got.Runs["r2"].State)
	}
	// failed's own effect is the log line and clearing the stall: the sentence
	// under the badge is entry.error, which run_state carries (and this
	// session's run_state for r2 carried none). Asserted through the log
	// rather than through a field, because that is where it goes.
	if !slices.ContainsFunc(got.Logs, func(l string) bool {
		return strings.Contains(l, "failed: no_metadata no peer answered")
	}) {
		t.Errorf("failed's code and message did not reach the log:\n%s", strings.Join(got.Logs, "\n"))
	}

	// budget_warning's whole effect is a line in the log, and it is the
	// client-scoped wording that has to be the client one.
	if !slices.ContainsFunc(got.Logs, func(l string) bool {
		return strings.Contains(l, "client-wide traffic roof")
	}) {
		t.Errorf("budget_warning at scope \"client\" did not log the client-wide wording:\n%s",
			strings.Join(got.Logs, "\n"))
	}
	// done's reason, spelled out for the three that are not self-explanatory.
	if !slices.ContainsFunc(got.Logs, func(l string) bool {
		return strings.Contains(l, "stopped at this run's own time limit")
	}) {
		t.Errorf("done with reason \"time\" did not spell the reason out:\n%s",
			strings.Join(got.Logs, "\n"))
	}
}

// TestAnUnknownEventChangesNothing is the twelfth type on its own: wire.go
// emits {"type":"unknown"} for a core event it does not recognise, and the
// page must do nothing with it rather than throw - which, since apply() is
// what a socket message goes straight into, would otherwise be an exception
// per message for as long as the run lasted.
func TestAnUnknownEventChangesNothing(t *testing.T) {
	base := runApply(t, []map[string]any{runStateFor("r1", "running", nil)})
	with := runApply(t, []map[string]any{
		runStateFor("r1", "running", nil),
		{"mark": "unknown"},
		event(map[string]any{"type": "unknown", "run": "r1"}),
		event(map[string]any{"type": "unknown"}),
	})

	if len(with.Runs) != len(base.Runs) {
		t.Errorf("an unknown event changed how many runs the page holds: %d, want %d",
			len(with.Runs), len(base.Runs))
	}
	after := slices.Index(with.Calls, "--- unknown")
	if after < 0 {
		t.Fatal("the harness lost its mark")
	}
	if extra := with.Calls[after+1:]; len(extra) != 0 {
		t.Errorf("an unknown event asked for redraws: %v", extra)
	}
}

// TestAMessageForARunThePageHasNeverHeardOfIsDropped is the guard on apply()'s
// single state.runs.get: only run_state may CREATE a row, because the socket
// replays a run's history from the start and that message always opens it. A
// frame arriving for a run with no entry must not mint one, or a reconnect
// racing a trimmed history would put a nameless row on the page.
func TestAMessageForARunThePageHasNeverHeardOfIsDropped(t *testing.T) {
	got := runApply(t, []map[string]any{
		event(map[string]any{
			"type": "frame_ready", "run": "ghost", "file": 0, "index": 0,
			"actual_ms": 1000, "url": "files/f0",
		}),
		event(map[string]any{"type": "done", "run": "ghost", "reason": "complete"}),
	})
	if len(got.Runs) != 0 {
		t.Errorf("the page invented %d row(s) from messages for a run it was never told about: %v",
			len(got.Runs), got.Runs)
	}
}

// TestAFrameIsOneShapeWhereverItCameFrom is a promise detailFrame's own doc
// has always made - "a frame is never half-addressable in one of them and
// whole in the other" - and which TOR-191 is the first ticket to make true of
// the URL as well as of `params`.
//
// A frame arrives two ways: off the socket, and off disk when a file's other
// result sets are read back. Before this ticket the socket handler stored a
// RESOLVED URL (url(ev.url), against document.baseURI and with the access
// token on it) because it happened to be in a file that had url() to hand.
// That is a page fact inside a state record, and it is exactly what a state
// module cannot have: resolving it needs a document.
//
// So both sources now store the PATH the server named, and frameFigure
// resolves it at the moment it sets the src. This checks the claim from both
// ends - the value a real frame_ready leaves behind, and the one place that
// turns it into something a browser can fetch.
func TestAFrameIsOneShapeWhereverItCameFrom(t *testing.T) {
	got := runApply(t, []map[string]any{
		runStateFor("r1", "running", nil),
		event(map[string]any{
			"type": "file_started", "run": "r1", "file": 0, "path": "a.mkv",
			"width": 1920, "height": 1080, "codec": "h264", "plan": []int{1000},
			"planned": 1,
		}),
		event(map[string]any{
			"type": "frame_ready", "run": "r1", "file": 0, "index": 0,
			"actual_ms": 1000, "url": "files/abc123",
		}),
	})

	f0 := got.Runs["r1"].FileEntries["0"]
	if !slices.Equal(f0.FrameURLs, []string{"files/abc123"}) {
		t.Errorf("the frame's url is %v, want [files/abc123] - the PATH the server named, not a "+
			"URL resolved against this page. Resolving it needs document.baseURI and the access "+
			"token, which is a fact about the page and not about the frame", f0.FrameURLs)
	}

	// The other end: the page resolves it exactly once, where the src is set,
	// and the disk-shaped reader stores the same thing the socket one does.
	// Both are one FILE's since TOR-195, so both are file-detail.js's.
	page := fileDetailJS(t)
	if !strings.Contains(page, "img.src = url(frame.url);") {
		t.Error("frameFigure does not resolve the frame's path when it sets the src - a bare " +
			"path would lose the access token, and every thumbnail on an authorized page " +
			"would 401")
	}
	if !strings.Contains(jsFunc(t, page, "detailFrame"), "url: f.url || null,") {
		t.Error("detailFrame no longer stores the frame's path as-is - a frame read back off " +
			"disk and one off the socket would be two shapes in one map again, which is the " +
			"half-addressable trap its own doc names")
	}
}

// ---------------------------------------------------------------------------
// The boundary itself.

// TestNoWireMessageEverReachesARenderer is this ticket's central claim, made
// checkable: every view hook takes STATE, and not one of them takes `ev`.
//
// The recording view flags any argument carrying a "type" key - which is what
// every message on this wire has and what no entry or file entry has - across
// a session touching every handler. A handler that passed its message along
// "just for this one field" is exactly the coupling the split exists to
// remove, and it would be invisible to any amount of reading.
func TestNoWireMessageEverReachesARenderer(t *testing.T) {
	files := []map[string]any{{"index": 0, "path": "a.mkv"}}
	got := runApply(t, []map[string]any{
		runStateFor("r1", "running", map[string]any{"infohash": "abc", "tickable": true}),
		event(map[string]any{
			"type": "needs_action", "run": "r1", "name": "Pack", "infohash": "abc",
			"videos": files, "files": files,
		}),
		event(map[string]any{
			"type": "metadata_ready", "run": "r1", "name": "Pack", "infohash": "abc",
			"videos": files, "files": files,
		}),
		event(map[string]any{
			"type": "file_started", "run": "r1", "file": 0, "path": "a.mkv",
			"width": 1920, "height": 1080, "codec": "h264", "plan": []int{1000},
			"planned": 1,
		}),
		event(map[string]any{
			"type": "frame_ready", "run": "r1", "file": 0, "index": 0,
			"actual_ms": 1000, "url": "files/f0",
		}),
		event(map[string]any{
			"type": "frame_skipped", "run": "r1", "file": 0, "index": 0, "code": "unavailable",
		}),
		event(map[string]any{
			"type": "progress", "run": "r1", "file": 0, "frames_done": 1, "frames_total": 1,
			"downloaded": 1, "peers": 1, "seeds": 1,
		}),
		event(map[string]any{
			"type": "file_done", "run": "r1", "file": 0, "frames": 1, "skipped": 0,
			"sheet_url": "files/sheet0",
		}),
		event(map[string]any{"type": "done", "run": "r1", "reason": "complete"}),
		event(map[string]any{"type": "failed", "run": "r1", "code": "x", "error": "y"}),
		event(map[string]any{"type": "budget_warning", "run": "r1", "scope": "run",
			"spent": 1, "limit": 2}),
	})

	if len(got.Suspect) != 0 {
		t.Errorf("these view hooks were handed a wire message rather than state: %v. Every hook "+
			"takes an entry, a file entry or a string - that is the whole of TOR-191's boundary, "+
			"and it is a signature rather than a rule somebody has to remember",
			got.Suspect)
	}
	// And the session really did exercise the renderers, so an empty Suspect
	// is not the answer to a test that measured nothing.
	if len(got.Calls) < 20 {
		t.Errorf("only %d view calls across every handler - this test would report no message "+
			"leak simply by not having run anything:\n%s",
			len(got.Calls), strings.Join(got.Calls, "\n"))
	}
}

// TestTheEventLayerNeverTouchesTheDom is the other half of the boundary, and
// the reason every test above can run at all: state.js and events.js must
// contain no DOM, no fetch and no message shape in their CODE. Read with
// comments stripped, because both files discuss the DOM at length and should.
func TestTheEventLayerNeverTouchesTheDom(t *testing.T) {
	for _, mod := range []string{"state.js", "events.js"} {
		code := stripJSComments(frontendModule(t, mod))
		for _, banned := range []string{"document.", "window.", "fetch(", "localStorage"} {
			if strings.Contains(code, banned) {
				t.Errorf("%s contains %q in code - it would stop being runnable outside a browser, "+
					"which is what every test in this file depends on", mod, banned)
			}
		}
	}

	// THE RENDERERS' OWN HALF OF IT: no renderer may read a message. One
	// mention survives in a comment (renderFileLinks, naming the manifest
	// field it deliberately does not keep), which is why this reads stripped
	// code - and the match is word-bounded, because `el.comparePrev.disabled`
	// ends in the same three characters and is not a message at all.
	//
	// WIDENED FROM app.js ALONE BY TOR-195, which is when it started mattering
	// most: five modules draw now, and the three the detail split into are
	// exactly the ones a handler would be tempted to hand an `ev` to, because
	// each of them redraws in response to one.
	ev := regexp.MustCompile(`(^|[^A-Za-z0-9_$.])ev\.`)
	for _, mod := range []struct{ name, src string }{
		{"app.js", appJS(t)},
		{"run-table.js", runTableJS(t)},
		{"run-detail.js", runDetailJS(t)},
		{"file-list.js", fileListJS(t)},
		{"file-detail.js", fileDetailJS(t)},
	} {
		if m := ev.FindString(stripJSComments(mod.src)); m != "" {
			t.Errorf("%s reads `ev.` somewhere in code (%q) - a message has reached a renderer "+
				"again, which is the coupling TOR-191 removed and the five extraction tickets "+
				"after it depend on", mod.name, m)
		}
	}
}

// stripJSComments removes // and /* */ comments so a check about CODE is not
// answered by prose. Deliberately crude about strings containing "//" - none
// of the checks above would be fooled by one, and a real parser here would be
// more machinery than the question deserves.
func stripJSComments(src string) string {
	var out strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(strings.TrimSpace(line), "//"); i == 0 {
			continue
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

// TestTheViewRefusesAWiringThatIsMissingAHook is the safety net the five
// extraction tickets after this one inherit: setView checks the view against
// events.js's own VIEW_HOOKS and throws, at wiring time, naming what is
// missing. A handler quietly calling undefined on a live run - the failure it
// forecloses - is the kind that surfaces at the one moment nobody is watching
// the console.
func TestTheViewRefusesAWiringThatIsMissingAHook(t *testing.T) {
	node := requireNode(t)
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	write("package.json", `{"type":"module"}`)
	write("state.js", stateJS(t))
	write("events.js", eventsJS(t))
	write("driver.js", `
import * as E from "./events.js";
const full = Object.fromEntries(E.VIEW_HOOKS.map((h) => [h, () => {}]));
const out = { hooks: E.VIEW_HOOKS, accepted: false, refused: "" };
try { E.setView(full); out.accepted = true; } catch (err) { out.error = String(err.message); }
const short = { ...full };
delete short[E.VIEW_HOOKS[0]];
delete short[E.VIEW_HOOKS[E.VIEW_HOOKS.length - 1]];
try { E.setView(short); } catch (err) { out.refused = String(err.message); }
process.stdout.write(JSON.stringify(out));
`)

	cmd := exec.Command(node, filepath.Join(dir, "driver.js"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node exited with an error: %v\n--- stderr ---\n%s\n--- stdout ---\n%s",
			err, stderr.String(), stdout.String())
	}

	var out struct {
		Hooks    []string `json:"hooks"`
		Accepted bool     `json:"accepted"`
		Error    string   `json:"error"`
		Refused  string   `json:"refused"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("node's stdout was not JSON: %v\n%s", err, stdout.String())
	}

	if !out.Accepted {
		t.Fatalf("setView refused a view holding every one of its own VIEW_HOOKS: %q", out.Error)
	}
	if out.Refused == "" {
		t.Fatal("setView accepted a view with two hooks missing - a handler would call undefined " +
			"on a live run instead, with nothing said at wiring time")
	}
	for _, want := range []string{out.Hooks[0], out.Hooks[len(out.Hooks)-1]} {
		if !strings.Contains(out.Refused, want) {
			t.Errorf("setView's refusal does not name the missing hook %q: %q", want, out.Refused)
		}
	}

	// And app.js has to actually install one, with every hook in it - the
	// check above is worth nothing if nothing calls setView.
	js := appJS(t)
	if !strings.Contains(js, "setView({") {
		t.Fatal("app.js never calls setView - events.js would keep its silent default view and " +
			"the page would apply every message and draw none of it")
	}
	for _, hook := range out.Hooks {
		if !strings.Contains(js, hook+":") && !strings.Contains(js, "\n  "+hook+",") {
			t.Errorf("app.js's setView call does not supply the %q hook", hook)
		}
	}
}

// ---------------------------------------------------------------------------
// TRAP 5 (TOR-203): the id swap must not throw, and only RUNNING it can say so.
//
// claimReopenedRun used to end its swap branch with syncEntry(entry) - a name
// state.js cannot see, since this module imports nothing and syncEntry lives in
// app.js. Every id swap therefore raised a ReferenceError, and a quiet one: the
// re-key happens BEFORE that call, so the swap itself survived and only the
// caller's success path was lost. A reopen that had actually worked arrived on
// the page as FAILED, reason "syncEntry is not defined".
//
// Every test in this file passed on that code, and no text guard could have
// failed: the call reads exactly as a correct redraw does, and the sole thing
// wrong with it is a name absent from one module's scope. Whether a name
// resolves is a property of running the module, so this test runs it - through
// apply(), by the one path that reaches the swap from a wire message
// (resolveIncomingRun), which is also the path a real socket takes whenever the
// reopen's own POST loses the race it is documented to race.
//
// It fails on the broken module by runApply's own node-error fatal, which
// carries the ReferenceError in stderr. Verified by re-adding the call.
func TestARunStateClaimingAReopeningRowSwapsItsIdWithoutThrowing(t *testing.T) {
	const (
		diskID = "disk-7c1f"
		runID  = "run-91b2"
		hash   = "0123456789abcdef0123456789abcdef01234567"
	)

	res := runApply(t, []map[string]any{
		// The disk row, under the synthetic key the listing minted for it.
		runStateFor(diskID, "done", map[string]any{"infohash": hash, "name": "Some.Pack"}),
		// A local un-tick, which nothing on the wire carries and no fresh entry
		// could be born holding. It is this test's proof of IDENTITY: if the
		// set survives under the new key, the same object was re-keyed rather
		// than a second row spawned for the same torrent.
		{"untick": map[string]any{"run": diskID, "file": 2}},
		// Somebody reopens it. The page raises the flag and posts; the server
		// replays the whole run - publishing this very message - before it
		// writes the POST's response, so this message can arrive first.
		{"reopen": map[string]any{"run": diskID}},
		{"mark": "the swap"},
		runStateFor(runID, "running", map[string]any{"infohash": hash}),
	})

	if _, stale := res.Runs[diskID]; stale {
		t.Errorf("the synthetic key %q is still in the store after the swap - the row was not re-keyed", diskID)
	}
	entry, ok := res.Runs[runID]
	if !ok {
		t.Fatalf("no row under the real id %q after the swap; the store holds %v", runID, res.runIDs())
	}
	if len(res.Runs) != 1 {
		t.Errorf("one torrent, %d rows: the reopen spawned a duplicate instead of folding into the row that asked for it", len(res.Runs))
	}

	// Identity, then the flags. The name and the un-tick both predate the swap.
	if entry.Name != "Some.Pack" {
		t.Errorf("the row under the real id has name %q, not the disk row's - this is a fresh entry, not the re-keyed one", entry.Name)
	}
	if got := entry.Unticked; len(got) != 1 || got[0] != 2 {
		t.Errorf("the local un-tick did not survive the swap: %v - only a re-key of the same object keeps it", got)
	}
	if entry.Reopening || entry.Claiming {
		t.Errorf("reopening=%v claiming=%v after the swap: the flags must come down, or this row goes on claiming every other run's id that shares its infohash",
			entry.Reopening, entry.Claiming)
	}
	if entry.Disk {
		t.Error("the row is still marked disk after a run_state claimed it")
	}
}

// TestALiveReadingIsAbsentOnceARunReachesAFinalState is TOR-202's own
// acceptance criteria 1-3 and 5, run through the real event layer rather than
// matched as text: a live "progress" heartbeat is the only thing that ever
// writes entry.live, and nothing on the wire tells this page to blank it back
// to null when a run ends - run_state, the message that announces
// done/failed/cancelled, carries no live figures at all (see applyRunState's
// own doc, which lists exactly what it does and does not touch). So the last
// reading a running torrent had would simply sit on the entry forever unless
// the READER, not the writer, refuses to show it once the row is final.
//
// TWO CASES, DELIBERATELY BOTH PRESENT (criterion 3's own text: the guard
// "must distinguish the two cases in point 2"):
//
//   - a RUNNING torrent that genuinely found nobody (peers=0, seeds=0, rates
//     0) must still read "0" - that is a real reading, and the whole point of
//     ABSENT IS NOT ZERO is that this looks different from having no client
//     at all.
//   - the SAME torrent, once it reaches done/failed/cancelled, must read
//     absent - the reading is not stale, it no longer has a client to have
//     read anything from.
//   - a row that is NOT final and has NEVER had a client (queued, no progress
//     event ever) must ALSO read absent - "not yet" and "over" are different
//     situations that happen to render the same, and this is the third case.
//
// THE THIRD CASE IS NOT DECORATION, and it was added after the first two were
// MEASURED not to rule out what this comment originally claimed they did. The
// degenerate shape to exclude is hasLive() dropping the entry.live check and
// answering !FINAL.has(entry.state) alone. Cases one and two both PASS under
// it - the lonely row is `running` AND holds a live reading, so a fix that
// only looks at the state still renders its real 0, and the finished row is
// still final either way. Only a row that is non-final with entry.live null
// separates them: the degenerate version calls it live and then reads
// entry.live.peers off null. Verified by applying that shape and watching
// this test fail on this case and only this case.
func TestALiveReadingIsAbsentOnceARunReachesAFinalState(t *testing.T) {
	progress := func(run string, peers, seeds int) map[string]any {
		return event(map[string]any{
			"type": "progress", "run": run, "file": 0,
			"frames_done": 1, "frames_total": 1, "downloaded": 0,
			"peers": peers, "seeds": seeds,
			"download_bps": 0, "upload_bps": 0,
		})
	}

	// Case 2: running, genuinely alone. A real zero reading, not absence.
	lonely := runApply(t, []map[string]any{
		runStateFor("r1", "running", nil),
		progress("r1", 0, 0),
	})
	r1, ok := lonely.Runs["r1"]
	if !ok {
		t.Fatalf("no row for r1 after a run_state and a progress event; runs: %v", lonely.runIDs())
	}
	if !r1.LiveReading {
		t.Error("a running torrent with a live reading of 0 peers/0 seeds reads as having no client at all - " +
			"a real zero reading must not be treated as absent")
	}
	if r1.PeersText != "0" || r1.SeedsText != "0" || r1.DownloadText != "0 B/s" || r1.UploadText != "0 B/s" {
		t.Errorf("a running torrent that genuinely found nobody must render 0, not absent: "+
			"peers=%q seeds=%q download=%q upload=%q", r1.PeersText, r1.SeedsText, r1.DownloadText, r1.UploadText)
	}

	// Case 3: never had a client at all. Queued, so no progress event has
	// ever been applied to it and entry.live is null - the "not yet" case,
	// which must read absent for a different reason than the finished row
	// below does, and which is the only one of the three that catches a
	// hasLive() that stopped consulting entry.live (see this test's doc).
	fresh := runApply(t, []map[string]any{runStateFor("r3", "queued", nil)})
	r3, ok := fresh.Runs["r3"]
	if !ok {
		t.Fatalf("no row for r3 after a queued run_state; runs: %v", fresh.runIDs())
	}
	if r3.HasLive {
		t.Fatalf("a queued row already holds an entry.live (peers=%d) - this case's premise is that nothing "+
			"has written one yet, so it cannot be checking what it claims to", r3.Peers)
	}
	if r3.LiveReading {
		t.Error("a queued row that has never had a client reads as having a live reading - hasLive() has " +
			"stopped consulting entry.live and is answering from the state alone")
	}
	if r3.PeersText != "—" || r3.SeedsText != "—" || r3.DownloadText != "—" || r3.UploadText != "—" {
		t.Errorf("a row with no client yet must render absent (—): peers=%q seeds=%q download=%q upload=%q",
			r3.PeersText, r3.SeedsText, r3.DownloadText, r3.UploadText)
	}

	// Cases 1 and 5: the same sequence, but the run then reaches each of the
	// three final states (done, failed, cancelled - runs.go's RunState.final,
	// mirrored by state.js's own FINAL). Every one of them must go absent -
	// this is the criterion that fails without the fix: entry.live still
	// holds peers:5/seeds:3/rates:100 from the progress event below, and
	// nothing on the wire clears it.
	for _, final := range []string{"done", "failed", "cancelled"} {
		t.Run(final, func(t *testing.T) {
			got := runApply(t, []map[string]any{
				runStateFor("r2", "running", nil),
				progress("r2", 5, 3),
				runStateFor("r2", final, nil),
			})
			r2, ok := got.Runs["r2"]
			if !ok {
				t.Fatalf("no row for r2 after reaching %q; runs: %v", final, got.runIDs())
			}
			if r2.State != final {
				t.Fatalf("r2 state = %q, want %q - run_state did not land", r2.State, final)
			}
			// entry.live itself (the raw field) is still non-null here - that
			// is exactly the bug's mechanism, and worth confirming rather than
			// assuming, since a fix that instead CLEARED entry.live on
			// run_state would make this assertion fail for the wrong reason.
			if !r2.HasLive || r2.Peers != 5 {
				t.Fatalf("entry.live was cleared by run_state (hasLive=%v peers=%d) - this test's premise (the "+
					"stale reading survives on the entry) does not hold, so it cannot be checking what it claims to",
					r2.HasLive, r2.Peers)
			}
			if r2.LiveReading {
				t.Errorf("state %q: hasLive() still reads true off a stale live reading - a finished run has no "+
					"client any more, live or not", final)
			}
			if r2.PeersText != "—" || r2.SeedsText != "—" || r2.DownloadText != "—" || r2.UploadText != "—" {
				t.Errorf("state %q: a finished run's peers/seeds/rates must render absent (—), not the stale "+
					"reading: peers=%q seeds=%q download=%q upload=%q",
					final, r2.PeersText, r2.SeedsText, r2.DownloadText, r2.UploadText)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TOR-191's browser pass is recorded at the bottom of columns_test.go, beside
// the earlier ones, since that is where this package keeps them.
