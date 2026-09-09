package web

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TOR-139 added six sortable columns to the run table - peers, seeds, both
// rates, availability and queue position - and the two rules the ticket
// itself is about: ABSENT IS NOT ZERO (a queued row's missing reading must
// never render or sort as though it were a measured zero), and availability
// is copies per piece, not a percentage.
//
// Every test in this file but one reads one of the shipped modules as served
// text (embedded FS, same as TestServesEmbeddedFrontend does for "WebSocket"
// and ".grid") and asserts on substrings of it - this file's own precedent
// since TOR-139, and
// the right call while the risk was a deletion or a rename: delete the
// absent-sinking branch from compareEntries and
// TestAbsentValuesSortToTheEndRegardlessOfDirection reddens, because the
// guarded text goes with it. It stops being enough the moment the risk is a
// WRONG ANSWER instead: a rewrite that keeps every guarded substring in this
// file and silently changes what compareEntries or sortValue actually
// computes passes all of them (TOR-148 measured this - see
// TestCompareEntriesAndSortValueExecuteForReal's own doc for the exact
// rewrite and which guards below stay green through it).
//
// WHICH MODULE EACH TEST READS, since TOR-191 split app.js into three. The
// derivations these tests are mostly about - sortValue, compareEntries,
// hasLive, arrivalOrdinal and the cell helpers - are state.js's now, and the
// two extractors below take whichever source they are handed rather than
// reaching for app.js themselves. The exports are transparent to both: each
// module lists its surface in one `export { ... };` block at the bottom
// instead of welding `export` onto every declaration, precisely so that
// `function sortValue(entry, key) {` is still the exact text a test can lift
// and a node process can run.
//
// A test that straddles two modules now says which half it expects where -
// TestLiveColumnsAreWiredIntoBothHeadersAndSorting is the clearest case, and
// the split makes its subject MORE worth guarding rather than less: the
// header list and the sort switch can now drift apart in two files instead of
// one.
//
// TOR-148's DECISION, recorded here because seven front-end extraction
// tickets after it build on the answer: yes, this suite may depend on a JS
// runtime, and where node is absent it FAILS rather than skips - see
// requireNode's doc for why a skip was rejected and exactly what running
// `go test ./...` (== `make check`) does on a machine with none. Everything
// else in this file stays a text guard on purpose: LIVE_COLUMNS is a static
// list no runtime input ever reaches, and badgeLabel/displayName/the rest of
// this file's own subjects are already covered honestly by matching their
// exact source. Only compareEntries and sortValue - the two functions whose
// job is a right ANSWER rather than a right SHAPE, and the ones
// TestAbsentValuesSortToTheEndRegardlessOfDirection could only ever confirm
// by reading, never by running - are executed for real, against real entry
// objects, absent cases included. What every text-guard test here still does
// NOT and cannot cover remains documented on each one.

// appJS returns the embedded app.js source, the same way stylesheet(t) in
// theme_test.go reads app.css - from the embedded FS, because that is the
// copy that ships.
func appJS(t *testing.T) string {
	t.Helper()
	b, err := embedded.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("reading the embedded app.js: %v", err)
	}
	return string(b)
}

// runTableJS returns the embedded run-table.js source. TOR-194 moved the
// table's behaviour out of app.js into its own custom element, so most of this
// file's own subjects - LIVE_COLUMNS, the header build, the column widths and
// every row cell - now live there. Read from the embedded FS for the reason
// appJS is: that is the copy that ships.
//
// WHICH MODULE EACH TEST READS is now a SIX-way question, and the answer
// follows the split rather than the ticket: a DERIVATION over an entry is
// state.js's (sortValue, compareEntries, the cell helpers), the ROW it is
// drawn into is run-table.js's, and since TOR-195 the three depths a row opens
// onto are three more modules - run-detail.js (what the row says about the
// RUN: its header, its error, its .torrent, its top-up offer), file-list.js
// (every file the torrent holds, the ticks, the prices, Select all, the
// un-tick verdicts, the clear) and file-detail.js (one file's metadata,
// progress, reach strip and frame grid). What is left in app.js is the intake,
// the requests, the log and the wiring.
func runTableJS(t *testing.T) string {
	t.Helper()
	b, err := embedded.ReadFile("assets/run-table.js")
	if err != nil {
		t.Fatalf("reading the embedded run-table.js: %v", err)
	}
	return string(b)
}

// The three TOR-195 modules, read the same way and for the same reason: that
// is the copy that ships. Named after the elements they define, so a test's
// own reader says which of the three depths its subject lives at.
func runDetailJS(t *testing.T) string {
	t.Helper()
	b, err := embedded.ReadFile("assets/run-detail.js")
	if err != nil {
		t.Fatalf("reading the embedded run-detail.js: %v", err)
	}
	return string(b)
}

func fileListJS(t *testing.T) string {
	t.Helper()
	b, err := embedded.ReadFile("assets/file-list.js")
	if err != nil {
		t.Fatalf("reading the embedded file-list.js: %v", err)
	}
	return string(b)
}

func fileDetailJS(t *testing.T) string {
	t.Helper()
	b, err := embedded.ReadFile("assets/file-detail.js")
	if err != nil {
		t.Fatalf("reading the embedded file-detail.js: %v", err)
	}
	return string(b)
}

// extractJSFunction pulls one top-level function's EXACT source out of one of
// the shipped modules, by name - the same "match the closing brace at the start
// of a line" shape TestAbsentValuesSortToTheEndRegardlessOfDirection already
// uses for compareEntries alone, generalised so
// TestCompareEntriesAndSortValueExecuteForReal can assemble a node program
// out of app.js's OWN text rather than a hand-copied duplicate that could
// drift from what actually ships. Anchored on a column-0 "\n}", which is
// exactly what makes this safe against a function whose body contains its
// own nested { } blocks (compareEntries and sortValue both do): every nested
// close sits indented, so the first bare "}" the regex can reach is the
// function's own.
//
// It also steps over a module's `export` keyword for free, since the match
// starts at `function NAME(` - which is what lets the harness below run text
// lifted out of an ES module in a plain node script.
func extractJSFunction(t *testing.T, js, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)function ` + regexp.QuoteMeta(name) + `\([^)]*\) \{.*?\n\}`)
	m := re.FindString(js)
	if m == "" {
		t.Fatalf("the module handed to this test has no function %s(...) to extract - a rename, a signature "+
			"change, or a move to another of the three modules would strand this test on a function that no "+
			"longer exists under this name here", name)
	}
	return m
}

// extractJSConst pulls one top-level single-line `const NAME = ...;`
// declaration's exact source. Two shapes are extracted: an object literal
// (state, in practice, since compareEntries reads state.sort and this test
// has to set it the same way the real page does - a click handler assigning
// state.sort, not a parameter compareEntries takes) and a `new Set([...])`
// call (FINAL, TOR-202 - hasLive() reads it now, so a harness that lifts
// hasLive out of state.js by itself has a free variable unless this comes
// with it).
func extractJSConst(t *testing.T, js, name string) string {
	t.Helper()
	re := regexp.MustCompile(`const ` + regexp.QuoteMeta(name) + ` = (\{[^\n]*\}|new Set\([^\n]*\));`)
	m := re.FindString(js)
	if m == "" {
		t.Fatalf("the module handed to this test has no const %s = {...}; or const %s = new Set([...]); to "+
			"extract - it has to stay one physical line for this to lift it", name, name)
	}
	return m
}

// TestLiveColumnsAreWiredIntoBothHeadersAndSorting guards against the two
// halves of TOR-139 drifting apart: LIVE_COLUMNS is what builds each <th> (so
// a column with no header cell cannot be clicked to sort at all), and the
// matching case in sortValue's switch is what makes clicking it actually
// reorder anything (a header with no case falls through to sortValue's
// "when" default, which would silently sort every new column exactly like
// the date column instead of by its own figure). A column present in one and
// not the other is a column that looks wired but is not.
//
// THE TWO HALVES SIT IN TWO FILES: the header list is the table element's
// (run-table.js, TOR-194 - it was app.js's when TOR-191 first split this test
// in two) and the sort switch is a derivation over an entry (state.js). That
// makes this test's own subject more likely rather than less - a column can
// now be added to one file by somebody who never opens the other - so it
// reads both and says which half is missing.
func TestLiveColumnsAreWiredIntoBothHeadersAndSorting(t *testing.T) {
	headers := runTableJS(t)
	sorting := stateJS(t)

	for _, key := range []string{"peers", "seeds", "download_bps", "upload_bps", "availability", "priority"} {
		if !strings.Contains(headers, `key: "`+key+`"`) {
			t.Errorf("run-table.js's LIVE_COLUMNS declares no %q column - it would have no header cell, so nothing to click", key)
		}
		if !strings.Contains(sorting, `case "`+key+`":`) {
			t.Errorf("state.js's sortValue() has no case for %q - its header would exist but clicking it would fall "+
				"through to the default (When) ordering instead of sorting by its own figure", key)
		}
	}
}

// TestAvailabilityHeaderNamesItsUnitOnThePage is the acceptance criterion
// written directly into the ticket: "Availability's unit is legible from the
// header." Copies per piece commonly exceeds 1.0 (a healthy swarm might read
// 3.2), which reads as nonsense to anyone who assumes a percentage - so the
// unit has to be on the page itself, not only in a title attribute nobody
// hovers over lookng for it.
func TestAvailabilityHeaderNamesItsUnitOnThePage(t *testing.T) {
	js := runTableJS(t)

	// unit: "..." is what buildLiveColumnHeaders() (run-table.js) turns into a
	// second, visible line under the "Avail" label - see LIVE_COLUMNS and
	// .run-th-unit in app.css. A tooltip-only mention would not satisfy this:
	// the whole point is that a person never has to open one.
	if !regexp.MustCompile(`key:\s*"availability"[^}]*unit:\s*"copies/piece"`).MatchString(js) {
		t.Error(`run-table.js's availability column declares no unit: "copies/piece" - the header would show only ` +
			`"Avail" with nothing to say the figure is not a percentage`)
	}
}

// TestAbsentLiveFiguresAreNullNeverZero is ABSENT IS NOT ZERO, read directly
// off sortValue's own switch: each of peers/seeds/download_bps/upload_bps/
// availability must fall back to null, literally, when entry.live (or the
// specific reading within it) is missing - never a bare 0, which sortValue
// would then treat as a real, comparable measurement instead of excluding it
// (see compareEntries' own absence handling, checked separately below).
//
// This is a whole-line match against the exact source rather than a looser
// substring, on purpose: "return hasLive(entry) ? entry.live.peers : 0"
// contains every word "... : null" does except the one that matters, so a
// substring check for "hasLive(entry)" alone would stay green through
// exactly the regression this test exists to catch.
func TestAbsentLiveFiguresAreNullNeverZero(t *testing.T) {
	js := stateJS(t)

	for _, want := range []string{
		`case "peers": return hasLive(entry) ? entry.live.peers : null;`,
		`case "seeds": return hasLive(entry) ? entry.live.seeds : null;`,
		`case "download_bps": return hasLive(entry) && entry.live.download_bps != null ? entry.live.download_bps : null;`,
		`case "upload_bps": return hasLive(entry) && entry.live.upload_bps != null ? entry.live.upload_bps : null;`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("state.js's sortValue() does not contain %q - an absent reading may be falling back to 0 "+
				"(a real, comparable value) instead of null (excluded from comparison, see compareEntries)", want)
		}
	}

	// availability's case is a small block rather than one line (it reads
	// entry.live.swarm, which can be absent even when entry.live itself is
	// present - a torrent that has not been asked for bytes yet), so this
	// checks the block returns null on both of the ways it can be absent.
	m := regexp.MustCompile(`case "availability": \{([^}]*)\}`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal(`state.js's sortValue() has no case "availability": { ... } block`)
	}
	if !strings.Contains(m[1], "return s ? s.copies_per_piece : null;") {
		t.Errorf("state.js's availability sortValue case does not return null when the swarm reading is absent: %q", m[1])
	}
}

// TestAbsentValuesSortToTheEndRegardlessOfDirection is the sorting trap the
// ticket names directly: a column where most rows are absent has to decide
// where those rows go, and treating absence as the lowest value would bury a
// running torrent with a real zero reading among the queued rows that have
// none - backwards from what a person sorting "fewest peers first" wants.
// TOR-139's decision is that an absent row sinks to the end on EVERY sort,
// ascending or descending alike.
//
// This checks the decision is actually unconditional: compareEntries must
// decide aAbsent/bAbsent and return before it ever reaches the
// `dir === "desc"` flip - a later refactor that moved the absence check
// after that flip would make an absent row's position depend on sort
// direction, which is exactly the bug this test exists to catch, and a
// regex for "aAbsent" alone would not notice the reordering.
func TestAbsentValuesSortToTheEndRegardlessOfDirection(t *testing.T) {
	js := stateJS(t)

	m := regexp.MustCompile(`(?s)function compareEntries\(a, b\) \{.*?\n\}`).FindString(js)
	if m == "" {
		t.Fatal("state.js has no compareEntries(a, b) function to check")
	}

	absentIdx := strings.Index(m, "return aAbsent ? 1 : -1;")
	if absentIdx < 0 {
		t.Fatal(`compareEntries does not contain "return aAbsent ? 1 : -1;" - the absent-goes-last decision`)
	}
	dirIdx := strings.Index(m, `dir === "desc"`)
	if dirIdx < 0 {
		t.Fatal(`compareEntries does not reverse for a descending sort at all (no "dir === "desc"")`)
	}
	if absentIdx > dirIdx {
		t.Error("compareEntries checks dir before it decides whether either value is absent - an absent row's " +
			"position would then depend on sort direction instead of always sinking to the end")
	}

	if !strings.Contains(m, "if (aAbsent && bAbsent) return 0") {
		t.Error("compareEntries does not special-case two absent rows against each other - without it they " +
			"would be ordered by whichever of aAbsent/bAbsent the ternary checks first, which is not a decision, " +
			"it's an accident of comparator order")
	}
}

// requireNode is TOR-148's decision made concrete, not just stated: this
// suite MAY require a JS runtime, and the one test in this file that does
// (TestCompareEntriesAndSortValueExecuteForReal) is not allowed to skip past
// its absence.
//
// The tempting precedent runs the other way - internal/frames/extract_test.go's
// locateTools() calls t.Skipf when ffmpeg.LocateIn() fails, and that has been
// this project's answer to a missing external tool up to now. It is not
// reused here on purpose. `go test ./...` (what `make check` runs) is never
// invoked by CI in this repository today - only .github/workflows/archives.yml
// exists, and it runs a scoped `go test -tags archivecheck ./archivecheck/`,
// never the general suite - so `make check` is entirely a human-or-agent-run
// gate (RELEASING.md step 3). A Skip here would let that gate go green on a
// machine that never actually ran the one test in this file able to catch a
// wrong ANSWER rather than a deleted or renamed one, with nothing printed to
// say so - exactly the failure mode the ticket names: "a test that skips when
// node is missing is a test that silently does not run in the one place it
// matters." Fatal instead: on a machine with no node on PATH, `go test ./...`
// and therefore `make check` FAIL, loudly, with the line below naming why.
//
// This does not strain the CGO_ENABLED=0 promise (README.md, Makefile): that
// promise is about what scripts/package.sh SHIPS - a static torpeek binary -
// and node is no more compiled into that binary than the GPL ffmpeg
// extract_test.go looks for is. Both are dev-time preconditions for running
// part of the test suite, never a runtime dependency of the archive a user
// unpacks.
func requireNode(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node not found on PATH (%v) - TOR-148 decided this suite may depend on a JS runtime rather "+
			"than silently skip the one test that executes app.js's own compareEntries/sortValue for real; "+
			"install Node.js (any recent LTS) to run `make check` in full", err)
	}
	return path
}

// jsSwarmReading mirrors the "swarm" object listing.go's Live carries on the
// wire, as far as sortValue's availability case reads it.
type jsSwarmReading struct {
	CopiesPerPiece float64 `json:"copies_per_piece"`
	Unavailable    int     `json:"unavailable"`
	Pieces         int     `json:"pieces"`
}

// jsLive mirrors GET /runs' "live" object - present only once a real client
// has spoken (hasLive's own subject) - closely enough for compareEntries and
// sortValue to read it exactly as the served page does. No omitempty on
// Swarm: a present-but-not-yet-reported swarm (entry.live present,
// entry.live.swarm absent) is a real, distinct case sortValue has to answer
// null for, and Go's zero value for a pointer already marshals to JSON null,
// which is exactly the shape that case needs.
type jsLive struct {
	Peers       int             `json:"peers"`
	Seeds       int             `json:"seeds"`
	DownloadBps *int64          `json:"download_bps"`
	UploadBps   *int64          `json:"upload_bps"`
	Swarm       *jsSwarmReading `json:"swarm"`
}

// jsEntry is the slice of a run-table "entry" (app.js's own state.runs
// value) that compareEntries and sortValue actually read across the six
// columns and name/status/priority - never the whole object app.js builds,
// because nothing here needs the rest of it. Live has no omitempty: a
// missing client (hasLive's false case) has to marshal as a literal JSON
// null, not an absent key indistinguishable from one Go forgot to set.
type jsEntry struct {
	ID      string  `json:"id"`
	Name    string  `json:"name,omitempty"`
	State   string  `json:"state,omitempty"`
	Disk    bool    `json:"disk,omitempty"`
	Partial bool    `json:"partial,omitempty"`
	Arrival int     `json:"arrival,omitempty"`
	Live    *jsLive `json:"live"`
}

type jsSort struct {
	Key string `json:"key"`
	Dir string `json:"dir"`
}

// jsSortCase is one call to Array.prototype.sort(compareEntries): the
// state.sort the real page would have set from a header click, and the rows
// it would be sorting.
type jsSortCase struct {
	Sort    jsSort    `json:"sort"`
	Entries []jsEntry `json:"entries"`
}

// compareEntriesHarness assembles a standalone node program out of state.js's
// OWN shipped source - the exact functions compareEntries' call graph
// reaches, extracted by name rather than retyped, so a real change to any of
// them changes what this test runs. Nothing DOM-shaped is pulled in: state,
// hasLive, availabilityReading, arrivalOrdinal, badgeLabel, displayName and
// shortId (displayName's own last-resort fallback) are the whole of what
// compareEntries and sortValue call, and every one of them is pure over an
// `entry` object - see state.js's own "Table sorting" block comment, just
// above hasLive, for why that block was written to stay that way.
//
// SINCE TOR-191 THE WHOLE FILE IS PURE IN THAT SENSE, which is why
// eventstate_test.go can import state.js outright rather than lifting eight
// functions out of it. This harness still lifts them: it CONCATENATES them
// into one flat script, with no imports and no module wrapper, so what it
// proves is that each of these declarations is self-contained on its own
// text - the property TOR-148 bought and this ticket had to not break.
//
// FINAL is pulled in alongside state as of TOR-202: hasLive() reads it
// (!!entry.live && !FINAL.has(entry.state)) so a finished run's stale live
// reading sinks the same way a never-had-a-client row's does, and a harness
// that lifted hasLive without it would hand node a ReferenceError instead of
// running the real function.
func compareEntriesHarness(t *testing.T, js string) string {
	t.Helper()

	var b strings.Builder
	b.WriteString(extractJSConst(t, js, "state"))
	b.WriteString("\n")
	b.WriteString(extractJSConst(t, js, "FINAL"))
	b.WriteString("\n")
	for _, name := range []string{
		"hasLive", "availabilityReading", "arrivalOrdinal", "badgeLabel", "displayName", "shortId",
		"sortValue", "compareEntries",
	} {
		b.WriteString(extractJSFunction(t, js, name))
		b.WriteString("\n")
	}

	// The driver: one process, one JSON round trip for every case, rather
	// than a node invocation per case - `go test` already pays node's
	// startup cost once per TEST, and this keeps it to once per RUN. Reads
	// {"cases":[{"sort":{...},"entries":[...]}]} off stdin (fd 0, no temp
	// file needed) and writes back each case's resulting id order.
	// Array.prototype.sort has been stable since V8 7.0 / Node 11 - load
	// bearing here, because a stable sort is what lets the "two absent rows
	// keep their ORIGINAL relative order" case below be checked at all
	// (compareEntries ties them at 0; an unstable sort would leave that tie
	// broken arbitrarily instead).
	b.WriteString(`
const input = JSON.parse(require("fs").readFileSync(0, "utf8"));
const out = input.cases.map((c) => {
  state.sort = c.sort;
  return c.entries.slice().sort(compareEntries).map((e) => e.id);
});
process.stdout.write(JSON.stringify(out));
`)
	return b.String()
}

// runCompareEntriesCases runs compareEntriesHarness's node program for real
// and returns each case's resulting id order. Any node failure - a bad
// extraction, a thrown exception, output that isn't the JSON the driver
// promises - is t.Fatalf'd with the FULL stdout and stderr, never a tail of
// either: this harness is exactly the kind of thing that fails in a way a
// truncated diagnostic hides (see this project's own standard on that).
func runCompareEntriesCases(t *testing.T, js string, cases []jsSortCase) [][]string {
	t.Helper()
	node := requireNode(t)
	script := compareEntriesHarness(t, js)

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "compare_entries_real.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("writing the node harness to a temp file: %v", err)
	}

	input, err := json.Marshal(struct {
		Cases []jsSortCase `json:"cases"`
	}{cases})
	if err != nil {
		t.Fatalf("marshalling this test's own sort cases to JSON: %v", err)
	}

	cmd := exec.Command(node, scriptPath)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node exited with an error running compareEntries/sortValue for real: %v\n--- stderr ---\n%s\n"+
			"--- stdout ---\n%s\n--- script ---\n%s", err, stderr.String(), stdout.String(), script)
	}

	var results [][]string
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("node's stdout was not the JSON array of id-order arrays this harness expects: %v\n"+
			"--- stdout ---\n%s\n--- stderr ---\n%s", err, stdout.String(), stderr.String())
	}
	return results
}

// TestCompareEntriesAndSortValueExecuteForReal is TOR-148's acceptance
// criterion: at least compareEntries and sortValue, executed against real
// inputs rather than matched as text, absent cases included - and the new
// test shown able to fail on a behaviour change that keeps every substring
// TestAbsentValuesSortToTheEndRegardlessOfDirection and
// TestAbsentLiveFiguresAreNullNeverZero guard intact.
//
// That rewrite, run and confirmed by hand rather than only asserted here
// (see this test's git history / the task's own report for the transcript):
// change compareEntries' absence DETECTION -
//
//	const aAbsent = va === null || va === undefined;
//	const bAbsent = vb === null || vb === undefined;
//
// - to check only `=== undefined`, dropping the `null` half:
//
//	const aAbsent = va === undefined;
//	const bAbsent = vb === undefined;
//
// sortValue's absent cases all return `null`, never `undefined` (that is
// what TestAbsentLiveFiguresAreNullNeverZero itself guards), so after this
// rewrite an absent row's va/vb is null, aAbsent/bAbsent both stay false, and
// compareEntries falls all the way through to
// `typeof va === "number" ? va - vb : String(va).localeCompare(String(vb))`
// - null is typeof "object", so a present numeric row now gets
// String(number).localeCompare("null") against an absent one instead of
// being sunk to the end. Every substring
// TestAbsentValuesSortToTheEndRegardlessOfDirection checks
// ("return aAbsent ? 1 : -1;", "if (aAbsent && bAbsent) return 0",
// "dir === "desc"" position) is still there, verbatim, so that test - and
// TestAbsentLiveFiguresAreNullNeverZero, which never looks at this line at
// all - both stay green through the rewrite. This test does not: the peers
// and availability cases below stop landing their absent rows at the end,
// because they no longer are absent by this comparator's own (mutated)
// definition, and reflect.DeepEqual/slices.Equal against the fixed
// expectation fails.
func TestCompareEntriesAndSortValueExecuteForReal(t *testing.T) {
	js := stateJS(t)

	live := func(peers int) *jsLive { return &jsLive{Peers: peers} }
	swarm := func(copies float64) *jsLive { return &jsLive{Swarm: &jsSwarmReading{CopiesPerPiece: copies}} }

	cases := []jsSortCase{
		// peers, ascending: two present rows in numeric order, two absent
		// (no live client at all) sunk to the end IN THEIR ORIGINAL RELATIVE
		// ORDER - compareEntries ties two absent rows at 0, and a stable
		// sort leaves a 0-comparison pair exactly where it found them.
		{
			Sort: jsSort{Key: "peers", Dir: "asc"},
			Entries: []jsEntry{
				{ID: "p1", Live: live(5)},
				{ID: "p2", Live: nil},
				{ID: "p3", Live: live(1)},
				{ID: "p4", Live: nil},
			},
		},
		// Same four rows, descending: the two absent rows stay LAST either
		// way - the ticket's own trap. Treating absence as "the lowest
		// value" would move it to the FRONT on a descending sort, burying a
		// running-but-friendless torrent's real, low peer count behind rows
		// that never had a client at all.
		{
			Sort: jsSort{Key: "peers", Dir: "desc"},
			Entries: []jsEntry{
				{ID: "p1", Live: live(5)},
				{ID: "p2", Live: nil},
				{ID: "p3", Live: live(1)},
				{ID: "p4", Live: nil},
			},
		},
		// availability reads a NESTED entry.live.swarm, absent two different
		// ways - no live client at all (a3), and a live client that has not
		// yet reported what the swarm holds (a2, live present, swarm null) -
		// and both have to sink exactly like a bare missing peers count
		// does.
		{
			Sort: jsSort{Key: "availability", Dir: "asc"},
			Entries: []jsEntry{
				{ID: "a1", Live: swarm(0.5)},
				{ID: "a2", Live: &jsLive{}},
				{ID: "a3", Live: nil},
				{ID: "a4", Live: swarm(2.0)},
			},
		},
		// priority sorts by arrivalOrdinal, whose absent case is a
		// different field and a different guard (arrival <= 0: a disk row
		// this session never numbered) than hasLive() answers for the other
		// five columns - included so the absence path is checked through
		// both of app.js's two absent-detection routes, not only one.
		{
			Sort: jsSort{Key: "priority", Dir: "desc"},
			Entries: []jsEntry{
				{ID: "q1", Arrival: 3},
				{ID: "q2", Arrival: 0},
				{ID: "q3", Arrival: 1},
				{ID: "q4", Arrival: 5},
			},
		},
		// name and status never produce an absent sortValue - included so
		// this test also exercises compareEntries' OTHER branch (the string
		// compare) and badgeLabel/displayName's real logic, not only the
		// absence path the rest of this test is about.
		{
			Sort: jsSort{Key: "name", Dir: "asc"},
			Entries: []jsEntry{
				{ID: "n1", Name: "Charlie"},
				{ID: "n2", Name: "alpha"},
				{ID: "n3", Name: "Bravo"},
			},
		},
		{
			Sort: jsSort{Key: "status", Dir: "asc"},
			Entries: []jsEntry{
				{ID: "s1", State: "queued"},
				{ID: "s2", State: "running"},
				{ID: "s3", State: "done", Partial: true}, // badgeLabel's own "partial" branch
				{ID: "s4", State: "failed"},
			},
		},
	}

	want := [][]string{
		{"p3", "p1", "p2", "p4"},
		{"p1", "p3", "p2", "p4"},
		{"a1", "a4", "a2", "a3"},
		{"q4", "q1", "q3", "q2"},
		{"n2", "n3", "n1"},
		{"s4", "s3", "s1", "s2"},
	}

	got := runCompareEntriesCases(t, js, cases)
	if len(got) != len(want) {
		t.Fatalf("node returned %d case results, want %d - got %v", len(got), len(want), got)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("case %d (sort %s %s): compareEntries sorted the entries to %v, want %v",
				i, cases[i].Sort.Key, cases[i].Sort.Dir, got[i], want[i])
		}
	}
}

// TestQueueColumnRendersTheServersOwnPositionAndNeverDerivesOne is TOR-139's
// TestQueueColumnDerivesFromReportedFieldsNotAnInventedPriority, INVERTED on
// purpose, because TOR-140 reversed the fact it guarded.
//
// The old test forbade reading row.priority or ev.priority and required a
// queueRank() that ranked queued rows by their own reported queued time. That
// was right while it stood: GET /runs reported no priority, the queue was
// strict FIFO, and a page reading a field the server did not send would have
// been inventing one. TOR-140 makes the position a real, reorderable
// server-side fact, so the field now exists and deriving one locally is the
// defect - the derivation would draw one order while the server dispatched
// another, with nothing on screen to say which was real.
//
// The assertion is therefore rewritten rather than deleted: same subject
// (there is exactly ONE notion of queue position, and the page renders it
// rather than deciding it), opposite direction.
func TestQueueColumnRendersTheServersOwnPositionAndNeverDerivesOne(t *testing.T) {
	page := appJS(t)
	events := eventsJS(t)
	derive := stateJS(t)
	table := runTableJS(t)
	// The retirement below has to hold across the WHOLE front end, not just
	// wherever the derivation used to live - a split is a fine place to
	// smuggle one back in, and TOR-194 made a fourth file for one to hide in.
	all := page + "\n" + events + "\n" + derive + "\n" + table

	// The derived version has to be GONE, not merely unused: two notions of
	// queue position is exactly what a reorder makes disagree, which is the
	// whole reason this ticket had to retire one of them. A mention inside a
	// comment explaining the retirement is not a second notion, so this looks
	// for the call/definition shape rather than the bare word.
	for _, gone := range []string{"function queueRank(", "queueRank(entry)", "refreshQueuePositions("} {
		if strings.Contains(all, gone) {
			t.Errorf("the front end still contains %q - TOR-139's client-side queue ranking must be retired now "+
				"that the server reports a reorderable position, not kept alongside it", gone)
		}
	}

	// And the server's own two fields have to be the ones that are read. Since
	// TOR-191 they are read in two places for two reasons, and each belongs
	// where it is: the LISTING's fields at page load (app.js's loadRuns, which
	// is the only thing that reads GET /runs), and run_state's own on every
	// message afterwards (events.js, which is the only thing that reads the
	// socket).
	for _, want := range []string{"row.queue_position", "row.priority"} {
		if !strings.Contains(page, want) {
			t.Errorf("app.js's loadRuns never reads %q - the queue column would not be showing what the "+
				"server actually holds at page load", want)
		}
	}
	for _, want := range []string{"ev.queue_position", "ev.priority"} {
		if !strings.Contains(events, want) {
			t.Errorf("events.js never reads %q - a reorder, a cancel or a start would move a row's place in "+
				"the queue with nothing on this page following it", want)
		}
	}

	// queuePosition() is what answers both the cell's text and the "priority"
	// sort key, and its absent case must be null rather than 0: the queue is
	// 1-based, so zero means "not waiting at all" and sorting it as a real
	// value would file a finished run among the waiting ones (see
	// compareEntries' own absence handling).
	if !strings.Contains(derive, "return entry.queuePosition > 0 ? entry.queuePosition : null;") {
		t.Error("state.js's queuePosition() does not answer null for a row with no position - a 0 would sort " +
			"and render as a real place in a 1-based queue")
	}
	// The Queue column must sort by the figure it DISPLAYS. That subject is
	// unchanged since TOR-140; which figure it is changed in TOR-156, when
	// the cell's own number became the arrival ordinal (queuePosition moved
	// to the labelled second line under it, and queuePosition() itself is
	// still checked just above, because that line still needs it). A header
	// left sorting by the position would now reorder the table by a number
	// most rows do not have while printing one they all do.
	if !strings.Contains(derive, `case "priority": return arrivalOrdinal(entry);`) {
		t.Error(`state.js's sortValue() does not answer the "priority" column from arrivalOrdinal() - the Queue ` +
			`header would sort by something other than the figure it displays`)
	}
	// And the ordinal's own absent case, the same null-not-zero shape
	// queuePosition() is held to above: only a row read off disk has none,
	// and a 0 would sort it as though it had been added before everything.
	if !strings.Contains(derive, "return entry.arrival > 0 ? entry.arrival : null;") {
		t.Error("state.js's arrivalOrdinal() does not answer null for a row with no ordinal - a 0 would sort " +
			"and render as a real place in a 1-based count")
	}
}

// TestTheQueueCanBeReorderedFromTheRow is TOR-140's acceptance criterion as
// far as served text can carry it: a waiting row has to offer a way to change
// its place without cancelling it, and that way has to be the absolute-level
// POST the server exposes.
//
// There is no JS runner here (see this file's own opening note), so what this
// proves is that the wiring exists in the shipped source - the buttons, their
// handlers, and the request they make. What it cannot prove is that a click
// reaches them; that was checked in a browser instead, and is recorded at the
// bottom of this file.
func TestTheQueueCanBeReorderedFromTheRow(t *testing.T) {
	// TWO MODULES SINCE TOR-194, and this test is the clearest case of the
	// line the ticket drew: the two BUTTONS are row elements and the table
	// builds and binds them; what a press DOES is a POST and stays in app.js,
	// which hands setPriority to the element as a service. Both halves are
	// read, and each assertion says which file it expects its half in.
	table := runTableJS(t)
	page := appJS(t)

	for _, want := range []string{
		`raise.className = "run-priority run-priority-up";`,
		`lower.className = "run-priority run-priority-down";`,
		"setPriority(entry, entry.priority + 1)",
		"setPriority(entry, entry.priority - 1)",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("run-table.js does not contain %q - a waiting row would have no way to change its own "+
				"place in the queue, which is the whole of this ticket", want)
		}
	}
	if !strings.Contains(page, `await post("runs/priority", { id: entry.id, priority: want });`) {
		t.Error(`app.js's setPriority() no longer POSTs runs/priority - the buttons would be wired to ` +
			"something that asks the server for nothing")
	}
	// And the two are actually joined: the element refuses an incomplete set
	// of services, so a rename on either side fails at wiring time - but only
	// if the wiring names it at all.
	// TOR-195 turned detailShown from an alias into a one-line call (the
	// detail is an element now, so what it does when it comes on screen is
	// its own method), which is why this reads the names it is about rather
	// than the whole call verbatim - a set that grows a fifth service should
	// not fail a test about setPriority.
	for _, want := range []string{"setRunTableServices({", "toggleRun,", "cancelRun,", "setPriority,"} {
		if !strings.Contains(page, want) {
			t.Errorf("app.js's setRunTableServices call does not contain %q - the ▲/▼ buttons would call "+
				"an injected service that was never injected, and setServices' own check is what turns "+
				"that into a failure at load rather than at the first press", want)
		}
	}

	// The level sent must be ABSOLUTE, clamped to the band, never a step: two
	// clicks against a stale row would otherwise walk a torrent somewhere
	// nobody asked for, and a retried request would apply the step twice.
	if !strings.Contains(page, "const want = Math.max(PRIORITY_LOW, Math.min(PRIORITY_HIGH, priority));") {
		t.Error("app.js's setPriority() does not clamp to an absolute level in the band - it may be sending a " +
			"step, which is not idempotent against a stale view")
	}

	// The reorder buttons must not also toggle the row open, the same way
	// Cancel must not: a person moving three torrents around would otherwise
	// leave three details expanded behind them.
	block := regexp.MustCompile(`(?s)entry\.rowRaise\.addEventListener\("click".*?\}\);`).FindString(table)
	if !strings.Contains(block, "event.stopPropagation();") {
		t.Errorf("the raise button's click handler does not stop propagation, so reordering a row would also "+
			"expand or collapse it: %q", block)
	}
}

// TestExactlyOneHeaderCellPerColumnIsEverBuilt guards TOR-157's own
// precondition before its own tests get to it, and TOR-215 made the stakes
// higher rather than lower. It used to be about a count: the detail row's
// colspan was read off "#run-table thead th", so a stray extra <th> - a
// resize handle that turned out to be a header cell of its own, say, rather
// than a plain <span> living inside one - inflated that number silently.
// There is no count any more (the detail spans 1 / -1), but a stray extra
// header cell is now a stray GRID ITEM in the header's own row: it takes a
// track, and every column after it renders one column to the left of its own
// data. TestLiveColumnsAreWiredIntoBothHeadersAndSorting would not notice
// either way, since both halves still agree on nine sortable columns.
//
// The class is what the check is anchored on rather than the tag, because the
// tag is a plain div now and there are dozens of those: exactly one line in
// run-table.js may put a cell in the header band, and it is
// buildLiveColumnHeaders'.
func TestExactlyOneHeaderCellPerColumnIsEverBuilt(t *testing.T) {
	js := runTableJS(t)

	if n := strings.Count(js, `className = "run-grid-head`); n != 1 {
		t.Errorf(`run-table.js assigns a "run-grid-head" class %d times, want exactly 1 (inside `+
			"buildLiveColumnHeaders) - a second one would put an extra item in the grid's header "+
			"row, taking a track and shifting every column after it off its own data", n)
	}
	// And nothing OUTSIDE the element may build one either, which is TOR-194's
	// half: the header band is the table's own part, so a header cell minted
	// anywhere else would be a column that reached neither LIVE_COLUMNS, nor
	// sorting, nor the width tokens, nor the grid's track list.
	if strings.Contains(appJS(t), "run-grid-head") {
		t.Error(`app.js builds a header cell of its own - since TOR-194 the header band belongs to ` +
			"the table element, and a column added from outside it would be invisible to every " +
			"mechanism that reads the headers")
	}
	// The <table> is gone and must not come back a piece at a time: a <th> or
	// a <td> built here would be a cell no grid track sizes (TOR-215).
	for _, gone := range []string{`createElement("th")`, `createElement("td")`, `createElement("tr")`} {
		if strings.Contains(js, gone) {
			t.Errorf("run-table.js still calls %s - the run table is a CSS grid, and a table cell "+
				"inside it belongs to no track at all", gone)
		}
	}
}

// TestColumnWidthsKeyOffElSortHeaders is TOR-157's own version of the
// LIVE_COLUMNS "one list rather than two" guard: this.sortHeaders (already
// exactly the nine resizable headers - the actions column carries no
// data-sort) is what decides which columns get a width to persist. A second,
// hand-written list of column keys here could drift from LIVE_COLUMNS the
// header set actually came from, the same way a hard-coded colspan drifted
// from the header count before this.columns was read off the header row.
func TestColumnWidthsKeyOffElSortHeaders(t *testing.T) {
	js := runTableJS(t)

	if !strings.Contains(js, `Array.from(this.sortHeaders, (head) => head.dataset.sort)`) {
		t.Error("run-table.js's resizableColumnKeys() does not derive its column list from this.sortHeaders - " +
			"a hand-written list here could silently drift from the headers buildLiveColumnHeaders() actually built")
	}
	// AND NOTHING SETS A WIDTH ON A HEADER ANY MORE (TOR-215). Under
	// table-layout: fixed the column's width WAS an inline width on its own
	// header, set in wireColumnResizers as
	// `head.style.width = "var(--col-w-" + key + ")"`. A grid track reads the
	// token itself (TestColumnWidthTokensMatchThePanelWidthFamily checks that
	// end), so the line said nothing - and, a grid item being content-box
	// where a table cell's width included its padding, it made every header
	// 9.6px wider than its own track (TOR-214 measured 329.6px in a 320px
	// track). Putting a width back on a header is the specific mistake this
	// guards against.
	//
	// Read through liveJS, which strips comments: the deleted line is quoted
	// verbatim in wireColumnResizers' own note (that is how a reader learns
	// what went and why), and a guard that could not tell prose from code
	// would fail on the explanation instead of on the mistake.
	if strings.Contains(liveJS(t, js), "style.width") {
		t.Error("run-table.js sets an inline width on a header again - the column's width is its GRID " +
			"TRACK's since TOR-215 (--run-tracks reads the same --col-w-* token, and since TOR-221 " +
			"every row's grid reads that one list), and an " +
			"inline width on a content-box grid item overflows that track by its own padding")
	}
}

// TestStoredColumnWidthsDegradeGracefully is this ticket's own acceptance
// criterion: a stored value must fall back to the default widths when it is
// empty, unreadable, or - the one this ticket calls out by name - naming a
// column that no longer exists (an older column set than the page currently
// renders). Read as served text because there is no JS runner here (see this
// file's opening note); what a real browser did with an actually-corrupt
// value is recorded in the summary at the bottom of this file instead.
func TestStoredColumnWidthsDegradeGracefully(t *testing.T) {
	js := runTableJS(t)

	// A METHOD SINCE TOR-194, so it closes at two spaces rather than at column
	// zero - jsMethod is the helper for that shape (stylesheet_test.go), and
	// using it rather than a looser regex is what keeps this anchored on the
	// whole function instead of on whatever the next `\n}` happens to be.
	m := jsMethod(t, js, "loadColumnWidths")

	for _, want := range []string{
		// Unreadable localStorage (private window, site data blocked) and a
		// missing key both fall back to the same empty map.
		"try {",
		`if (!raw) return {};`,
		// Malformed JSON throws inside the try, caught below.
		"} catch (err) {",
		"return {};",
		// Not an object at all (a bare number or string JSON-decodes fine
		// but isn't a map of columns).
		`if (!parsed || typeof parsed !== "object") return {};`,
		// THE ticket's own named case: a key from an older column set.
		"const known = new Set(this.resizableColumnKeys());",
		"if (!known.has(key)) continue;",
		// A width that doesn't parse as a finite number (corrupted, or not
		// a number at all) is dropped rather than applied as NaN.
		"if (Number.isFinite(width)) widths[key] = clampColumnWidth(width);",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("run-table.js's loadColumnWidths() does not contain %q - a stored value could render a broken "+
				"table instead of degrading to the default widths: %q", want, m)
		}
	}

	// The read itself, and the whole function, must be inside the try - not
	// just the JSON.parse - since localStorage.getItem itself is what throws
	// in a private window.
	if !strings.Contains(m, "localStorage.getItem(COLUMN_WIDTHS_KEY)") {
		t.Error("run-table.js's loadColumnWidths() does not read COLUMN_WIDTHS_KEY from localStorage at all")
	}

	// The same try/catch on the way back out: a write that throws (private
	// window, quota) must not crash the drag it was trying to persist.
	if !strings.Contains(js, "  saveColumnWidths(widths) {") ||
		!strings.Contains(js, "localStorage.setItem(COLUMN_WIDTHS_KEY, JSON.stringify(widths));") {
		t.Error("run-table.js's saveColumnWidths() does not write the whole widths map back to " +
			"COLUMN_WIDTHS_KEY as JSON")
	}

	// AND THE MAP IS LOADED AT ALL. loadColumnWidths degrading correctly is
	// worth nothing if nothing calls it: TOR-194 moved the call from a
	// top-level `const columnWidths = loadColumnWidths()` into
	// connectedCallback, which is a place a later edit can drop it from
	// without touching one line this test would otherwise read.
	if !strings.Contains(js, "this.columnWidths = this.loadColumnWidths();") {
		t.Error("run-table.js's connectedCallback does not load the stored column widths - every stored " +
			"width would be discarded on load and the table would open at the defaults every time")
	}
	if !strings.Contains(js,
		"for (const [key, px] of Object.entries(this.columnWidths)) this.applyColumnWidth(key, px);") {
		t.Error("run-table.js loads the stored widths and never applies them - the map would be in memory " +
			"with no --col-w-* token written, so the table would still render at the defaults")
	}
}

// TestColumnDragNeverTriggersSort is the trap this ticket names directly:
// every header is a sort control (TOR-139), so the resize handle run-table.js
// appends inside each one sits inside a click target that reorders the
// table. TOR-140's raise/lower buttons hit the identical problem for the
// accordion and fixed it with stopPropagation; this checks the same fix
// landed on every one of the handle's own events, not just the drag start -
// a plain click (pointerdown+pointerup with no movement) still bubbles a
// separate click event that pointerdown's own stopPropagation does not
// touch.
func TestColumnDragNeverTriggersSort(t *testing.T) {
	js := runTableJS(t)

	// SINCE TOR-194 the two loops over this.sortHeaders are two named methods
	// (wireSorting and wireColumnResizers), so the block this test is about
	// can be asked for by name instead of being told apart from its twin by a
	// line inside it. jsMethod bounds it at the two-space close.
	block := jsMethod(t, js, "wireColumnResizers")

	if !strings.Contains(block, `handle.className = "col-resizer";`) {
		t.Fatal("run-table.js's wireColumnResizers does not create a .col-resizer handle - nothing below " +
			"would be testing what this test thinks it is")
	}
	// The other half of what made this loop the right subject: the handles go
	// inside the very headers wireSorting made into sort controls.
	if !strings.Contains(block, "for (const head of this.sortHeaders) {") {
		t.Fatal("run-table.js's wireColumnResizers no longer walks this.sortHeaders - the handles would not " +
			"be sitting inside a sort control at all, which is the collision this test exists for")
	}

	for _, want := range []string{
		// pointerdown: stops the drag itself from reaching the header.
		`handle.addEventListener("pointerdown", (event) => {`,
		// pointermove: stopped too, since a pointer captured on the handle
		// still dispatches move events the header never asked for.
		`handle.addEventListener("pointermove", (event) => {`,
		// A plain click - the case pointerdown's own stopPropagation cannot
		// reach, since click is a separate event fired after pointerup.
		`handle.addEventListener("click", (event) => event.stopPropagation());`,
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the resize-handle wiring does not contain %q", want)
		}
	}

	// One call each in pointerdown, pointermove and endColumnDrag (shared by
	// both pointerup and pointercancel) - the click listener's own call is
	// already checked above by its exact line instead, since as a one-line
	// arrow body ("(event) => event.stopPropagation())") it reads
	// "stopPropagation());", not "stopPropagation();", and would not count
	// here.
	if n := strings.Count(block, "event.stopPropagation();"); n < 3 {
		t.Errorf("the resize-handle wiring calls event.stopPropagation() only %d times across pointerdown, "+
			"pointermove and endColumnDrag - expected at least 3, so a drag cannot also reorder the table", n)
	}
}

// TestColumnDragEndsWhenPointerCaptureIsLost is TOR-165: a drag whose
// pointerup/pointercancel never reaches the handle - because the capture was
// lost some other way, e.g. the handle leaving the document (per spec, the
// baseline trigger for lostpointercapture) - must not leave the drag "stuck"
// active forever. Before this ticket the handle's "dragging" class was the
// only state a drag left behind, and nothing here ever cleared it back off.
// buildLiveColumnHeaders() only builds this table's header once today, so a
// header rebuild is not a live way to hit this in the current app - the
// browser repro below instead reproduced a real capture loss straight from
// this repo's own browser-automation tooling, no header rebuild involved.
// Reuses TestColumnDragNeverTriggersSort's own block extraction, since both
// tests are about the same wiring.
//
// Text assertions cannot see a pointer actually move or a capture actually
// get lost - that is the browser pass recorded right below, not this test.
func TestColumnDragEndsWhenPointerCaptureIsLost(t *testing.T) {
	js := runTableJS(t)

	block := jsMethod(t, js, "wireColumnResizers")

	// Half one: lostpointercapture is the event that fires when the capture
	// set in pointerdown ends WITHOUT a pointerup/pointercancel reaching the
	// handle. Nothing listened for it before this fix.
	if !strings.Contains(block, `handle.addEventListener("lostpointercapture", endColumnDrag);`) {
		t.Error(`run-table.js's resize-handle wiring does not end the drag on "lostpointercapture" - a capture lost ` +
			"any way other than pointerup/pointercancel on the handle itself would leave the handle lit and " +
			"dragging forever")
	}

	// Half two, and the one that matters more per the ticket: pointermove
	// must require the primary button still be held (event.buttons bit 0)
	// and end the drag itself when it is not - so a plain hover with no
	// button down is never computed as a continued drag, which is what
	// makes ANY future way of losing the pointer harmless instead of sticky,
	// not just the lostpointercapture case above.
	if !strings.Contains(block, "if (!(event.buttons & 1)) {") {
		t.Error("run-table.js's pointermove handler does not check event.buttons for the primary button - a hover " +
			"with no button held would be computed as though it were a continued drag")
	}
	// Anchored on the GUARD rather than on the two statements' own indentation,
	// which TOR-194 changed by two spaces when this became a method - a check
	// that reddens on a re-indent is a check nobody trusts the next time. The
	// regex is also the stronger claim: it says the end-and-return belongs to
	// the no-button branch, not merely that both appear somewhere.
	if !regexp.MustCompile(`if \(!\(event\.buttons & 1\)\) \{\s*endColumnDrag\(event\);\s*return;`).
		MatchString(block) {
		t.Error("run-table.js's pointermove handler does not end the drag when event.buttons shows no button held - " +
			"it would keep reading a stale start point instead of stopping")
	}
}

// TOR-165's browser pass (its own acceptance criterion: "Verified in a
// browser by interrupting a drag, not only by asserting the served text"),
// against a real binary built from this branch, serving on 127.0.0.1:8811:
//
//   - reading the wiring first: buildLiveColumnHeaders() builds the header
//     row exactly once, at page load, and nothing else in app.js ever
//     touches a .col-resizer node afterward - so "this table re-renders its
//     header row on events," the mechanism the ticket named, is not a live
//     path in the CURRENT code. The rest of the ticket's read was accurate:
//     colDragStartX/colDragStartWidth were the sole state (this task turned
//     them into a null-until-dragging `drag` object instead, per-handle by
//     closure either way), pointermove trusted the "dragging" class alone,
//     and nothing listened for lostpointercapture.
//   - a real capture loss was still found without forcing one: on the
//     UNFIXED binary, a single plain left-click on the NAME/ADDED resize
//     handle (browser automation via CDP, no drag distance at all) left the
//     handle's classList as "col-resizer dragging" - pointerup's own event
//     target had already become the <th> rather than the handle, meaning
//     capture was already gone before pointerup fired, exactly the gap the
//     ticket describes, from an ordinary click rather than any header
//     rebuild. A subsequent hover (mousemove, event.buttons: 0, no click)
//     at a point 27px to the right then widened --col-w-name from
//     356.734375px to 383.734375px - the column visibly grew under a
//     hover with no button held, matching "afterwards merely moving the
//     cursor over it dragged the column further right" from the ticket
//     verbatim, confirmed by screenshot (the divider sat lit and the NAME
//     column had visibly widened).
//   - the exact same sequence (fresh page load, one plain click on the
//     handle, then a hover 27px right of it) against the FIXED binary left
//     handle.className as plain "col-resizer" after the click - the same
//     capture loss happened, but lostpointercapture (or the event.buttons
//     guard - either half independently ends it) cleared it - and the
//     following hover left --col-w-name unchanged at 20rem, confirmed by
//     screenshot (divider unlit, NAME column back at its original width).
//
// No console errors during the pass. Server started and stopped cleanly on
// port 8811; `uptime`'s load average at the time (13-25, a shared, heavily
// loaded machine) is why timings are not reported - nothing here depended on
// wall-clock speed.

// TestColumnWidthTokensMatchThePanelWidthFamily is the ticket's own
// instruction, checked directly: "a column-width equivalent belongs in the
// same family" as --panel-width. Same mechanism as TestStoredColumnWidthsDegradeGracefully's
// sibling tests above, but on the stylesheet side - the CSS half of what
// makes a dragged width actually move a border on screen.
func TestColumnWidthTokensMatchThePanelWidthFamily(t *testing.T) {
	css := stylesheet(t)

	for _, key := range []string{
		"name", "when", "status", "peers", "seeds", "download_bps", "upload_bps", "availability", "priority",
	} {
		token := "--col-w-" + key + ":"
		if !strings.Contains(css, token) {
			t.Errorf("app.css declares no %q token in :root - run-table.js's applyColumnWidth(%q, ...) would be "+
				"setting a custom property nothing in the stylesheet ever reads a default from", token, key)
		}
	}

	// THE TRACK LIST IS WHERE A COLUMN'S WIDTH COMES FROM SINCE TOR-215, and
	// SINCE TOR-221 IT IS ONE VALUE - --run-tracks. It is checked whole - every
	// track, in order - rather than by looking for each token somewhere in it.
	// The list is the one place the nine draggable columns, their order and the
	// tenth flexible one all have to agree, and the shape of a track matters as
	// much as its presence (below).
	//
	// ONE VALUE rather than one rule is TOR-221's whole claim: alignment stops
	// being a dependency on a PARENT (a row had to be a direct grid item of
	// .run-grid carrying grid-template-columns: subgrid) and becomes one on
	// this token, which every grid that lays a run's columns out reads.
	// TestNoRowDependsOnItsContainerToFindItsColumns is the other half - that
	// nothing has quietly gone back to depending on the parent instead.
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
	if n := strings.Count(live, "--run-tracks:"); n != 1 {
		t.Fatalf("the served stylesheet declares --run-tracks %d times, want exactly 1 - two "+
			"declarations are two track lists that can drift apart, which is the one way per-row "+
			"grids can come to misalign at all", n)
	}
	decl := regexp.MustCompile(`(?s)--run-tracks:([^;]*);`).FindStringSubmatch(live)
	if decl == nil {
		t.Fatal("the served stylesheet declares no --run-tracks - since TOR-221 that token IS the ten " +
			"columns, so without it .run-grid-head-row and .run-row would each lay out a single auto " +
			"track and no --col-w-* token would reach the page at all")
	}
	// AND IT IS ON :root, not on a container. On a container it would reach a
	// row only by INHERITANCE, which looks like it works and is the same
	// coupling wearing inheritance's clothes: a row moved out of that box
	// silently loses every column, because an unresolvable var() makes the
	// whole declaration invalid at computed-value time and grid-template-columns
	// falls back to none.
	if rootRule := regexp.MustCompile(`(?s):root \{.*?\n\}`).FindString(live); !strings.Contains(rootRule, "--run-tracks:") {
		t.Error("--run-tracks is declared outside the :root token rule - on a container it reaches a " +
			"row only by inheritance, so a row put anywhere else loses all ten columns; on :root it " +
			"reaches a row wherever the row is (TOR-221)")
	}
	// ", " -> "," first, so minmax(3.8rem, 1fr) is one field rather than two.
	tracks := strings.Fields(strings.ReplaceAll(decl[1], ", ", ","))

	// A BARE LENGTH PER SORTABLE TRACK, and nothing softer - which is why the
	// want list below is written out in full instead of being built from the
	// key list above. This is the exact counterpart of the table-layout: fixed
	// it replaced, and TOR-214 measured that a grid does NOT give it for free:
	// for a column dragged to 44px holding a string 828.17px wide,
	// table-layout: auto rendered 822.43px and minmax(min-content, 44px)
	// rendered 822.43px too - only a bare 44px track rendered 44px. Wrapping
	// the tokens as minmax(token, 1fr) so the slack would spread was tried as
	// well: all ten tracks resolved to 851.74px and dragging went inert. So a
	// track named here in any other shape is a regression a "contains the
	// token" check would have passed.
	//
	// The tenth is the actions column: no data-sort, no --col-w-* token of its
	// own (see resizableColumnKeys()), and the ONE flexible track - which is
	// both where the pane's slack goes and why it keeps a 3.8rem floor.
	want := []string{
		"var(--col-w-name)",
		"var(--col-w-when)",
		"var(--col-w-status)",
		"var(--col-w-peers)",
		"var(--col-w-seeds)",
		"var(--col-w-download_bps)",
		"var(--col-w-upload_bps)",
		"var(--col-w-availability)",
		"var(--col-w-priority)",
		"minmax(3.8rem,1fr)",
	}
	if len(tracks) != len(want) {
		t.Fatalf("--run-tracks declares %d column tracks, want %d - the header band's cells are that "+
			"grid's own items and a row's cells are its own, so a track too few or too many shifts "+
			"every column after it off its own data, in both at once: %q", len(tracks), len(want), tracks)
	}
	for i, w := range want {
		if tracks[i] != w {
			t.Errorf("--run-tracks track %d is %q, want %q - a column has to be a BARE length read "+
				"from its own token (TOR-214 measured minmax/min-content/auto all letting content win at "+
				"822.43px where a bare 44px track gave 44px), and the tenth has to stay minmax(3.8rem, 1fr) "+
				"or the rows stop filling the pane the way the <table>'s width: 100%% did",
				i+1, tracks[i], w)
		}
	}

	// AND BOTH GRIDS READ IT, by var() and not by a copy of the list. Two
	// copies would resolve identically on the day they were written and drift
	// on the day one of them is edited, which is a misalignment nobody sees
	// for a year - the exact failure TOR-221's own measurement was aimed at.
	for _, sel := range []string{".run-grid-head-row", ".run-row"} {
		rule := regexp.MustCompile(`(?s)\` + sel + `\s*\{.*?\n\}`).FindString(live)
		if rule == "" {
			t.Fatalf("app.css has no %s rule - since TOR-221 the ten columns are laid out per row, and "+
				"that rule IS one of the two grids that does it", sel)
		}
		if !strings.Contains(rule, "grid-template-columns: var(--run-tracks);") {
			t.Errorf("%s does not lay out `grid-template-columns: var(--run-tracks)` - a second copy of "+
				"the track list aligns on the day it is written and drifts on the day one copy is "+
				"edited, and a cell an eighth of a pixel out of its column is the failure nobody "+
				"notices (TOR-221)", sel)
		}
		if !strings.Contains(rule, "display: grid;") {
			t.Errorf("%s does not declare display: grid - grid-template-columns on a non-grid box is "+
				"inert, so every cell in it would stack in one column", sel)
		}
	}

	// AND THE ROWS OUTGROW THE PANE RATHER THAN THE PAGE. width: max-content
	// with min-width: 100% is the pair, and it stays on .run-grid - the one
	// job that box still has since TOR-221 took the grid off it: at rest
	// min-width wins and it fills the pane, every row stretching to it and
	// resolving the same 1fr from the same width; the moment a drag makes the
	// tracks sum wider, max-content wins and .run-table-wrap's own overflow-x
	// scrolls. Measured on the running page: 1168px at rest against a 1168px
	// pane, 1303.19px after a drag to the 640px ceiling, with the wrap
	// scrolling at 1303/1168 and documentElement.scrollWidth still equal to
	// its clientWidth.
	grid := regexp.MustCompile(`(?s)\.run-grid \{.*?\n\}`).FindString(live)
	if grid == "" {
		t.Fatal("app.css has no .run-grid rule at all - it is no longer a grid (TOR-221) but it is " +
			"still the box whose width: max-content/min-width: 100% decides whether the wrap " +
			"scrolls or the page does")
	}
	for _, w := range []string{"width: max-content;", "min-width: 100%;"} {
		if !strings.Contains(grid, w) {
			t.Errorf("app.css's .run-grid rule does not set %q - one of the two halves of \"fills the "+
				"pane at rest, outgrows it on a wide drag\" is missing, and a dragged column would "+
				"either be clamped by the pane or push the whole page sideways", w)
		}
	}
}

// rowParts are the three elements a run is built from (run-table.js's newRow):
// the wrapper, the torrent's own line and the detail's row. TOR-221's claim is
// about exactly these - that none of them needs to be anywhere in particular
// to look right.
var rowParts = []string{".run-row-group", ".run-row", ".run-detail-row"}

// TestNoRowDependsOnItsContainerToFindItsColumns is TOR-221's fifth criterion,
// and it is deliberately not the check that criterion could be read as asking
// for. "`subgrid` appears nowhere in the served stylesheets" is one grep and it
// is weak: subgrid is one of at least four ways to write "this row's layout
// comes from its parent", and a reintroduction would almost certainly arrive as
// one of the others - most likely by someone restoring the shared grid because
// two rules with the same track list looked like duplication.
//
// So each arm below names a DIFFERENT way to make a row depend on its
// container, and the wrong implementations they catch are:
//
//	subgrid on a row part          the original coupling, back by its own name
//	display: contents anywhere     the same coupling one level up - a container
//	                               with no box makes its children items of the
//	                               GRANDPARENT's grid, which is how #run-list
//	                               used to work and why it needed that rule
//	display: grid on .run-grid     the shared grid restored, which makes every
//	                               row a grid ITEM again (and, with the row's
//	                               own tracks still in place, a nested grid
//	                               inside one column - measured at 922.3906px
//	                               out in TOR-221's control arm)
//	grid-column/-row/-area on a
//	  row part                     properties that only mean anything to a
//	                               grid item, so declaring one asserts a parent
//	a row part as the key selector
//	  under any combinator         `.run-grid > .run-row-group { ... }` says in
//	                               the selector what subgrid used to say in the
//	                               value: this only works in that box
//
// What it cannot catch is a track list copied instead of read
// (TestColumnWidthTokensMatchThePanelWidthFamily's own var(--run-tracks) arm
// covers that) and anything about what a browser actually renders - the
// 0.0000px across 22 rows is in docs/front-end.md with the control that makes
// it a measurement.
func TestNoRowDependsOnItsContainerToFindItsColumns(t *testing.T) {
	css := stylesheet(t)
	// Comments stripped, and that matters more here than in most of these
	// tests: table.css, tokens.css and accordion.js all EXPLAIN the subgrid
	// that was removed, by name, because a reader who does not know what was
	// there cannot know why the token exists. A guard that could not tell
	// prose from code would fail on the explanation instead of on the mistake.
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")

	if strings.Contains(live, "subgrid") {
		t.Error("`subgrid` is back in the served stylesheet. It only resolves on a DIRECT grid item " +
			"of the grid it borrows from, so whatever carries it cannot have anything inserted " +
			"above it - which is the coupling TOR-221 removed and the first reason front-end.md " +
			"used to give for the accordion not being an element")
	}
	if strings.Contains(live, "display: contents") {
		t.Error("`display: contents` is back in the served stylesheet. It is the same dependency one " +
			"level up: a box that is not there makes its children items of the GRANDPARENT's grid, " +
			"so a row would again only lay out correctly at one exact depth. #run-list carried it " +
			"until TOR-221 and needs nothing now")
	}

	// .run-grid must not be a grid. If it is, every row is a grid item again -
	// and worse than before, because the row still carries its own tracks, so
	// it lays its ten columns out inside whatever single column it landed in.
	if grid := regexp.MustCompile(`(?s)\.run-grid \{.*?\n\}`).FindString(live); grid != "" {
		for _, banned := range []string{"display: grid", "display: inline-grid", "grid-template-columns"} {
			if strings.Contains(grid, banned) {
				t.Errorf(".run-grid declares %q again - the shared grid is what made a row a fragment "+
					"of its container rather than a whole thing (TOR-221). The rows carry their own "+
					"tracks now, so a row would lay its ten columns out inside one column of this one",
					banned)
			}
		}
	}

	// Rule by rule: a row part may not be given a grid ITEM's properties, and
	// may not be reached through a combinator as the thing a rule is about.
	for _, rule := range splitRules(live) {
		for _, sel := range strings.Split(rule.selector, ",") {
			sel = strings.TrimSpace(sel)
			if sel == "" {
				continue
			}
			key := keyCompound(sel)
			part := ""
			for _, p := range rowParts {
				if strings.Contains(key, p) {
					part = p
				}
			}
			if part == "" {
				continue
			}
			for _, prop := range []string{"grid-column", "grid-row", "grid-area"} {
				if regexp.MustCompile(`(^|[;{\s])` + prop + `\s*:`).MatchString(rule.body) {
					t.Errorf("%q declares %s. That property only means anything to a grid ITEM, so it "+
						"is an assertion about this element's PARENT - exactly what TOR-221 took out. "+
						"A row part spans its container because it is a block, not because it was "+
						"placed on somebody else's lines", sel, prop)
				}
			}
			if key != sel {
				t.Errorf("%q reaches %s through a combinator. A rule written that way says in the "+
					"selector what subgrid used to say in the value - this element only lays out "+
					"correctly inside that box - so the accordion could not be wrapped around it "+
					"again (TOR-221). Style the part by its own class", sel, part)
			}
		}
	}
}

// styleRule is one declaration block with the selector list it belongs to.
type styleRule struct {
	selector string
	body     string
}

// splitRules cuts comment-stripped CSS into rules. Flat by design: this
// stylesheet has no @media and no nesting (theme_test.go's own guard is what
// keeps the first true), so a rule is everything between one "}" and the next.
func splitRules(css string) []styleRule {
	var out []styleRule
	for _, chunk := range strings.Split(css, "}") {
		i := strings.Index(chunk, "{")
		if i < 0 {
			continue
		}
		out = append(out, styleRule{selector: strings.TrimSpace(chunk[:i]), body: chunk[i+1:]})
	}
	return out
}

// keyCompound returns the last compound of one selector - the element the rule
// is ABOUT, as opposed to the ancestors it is qualified by. Equal to the whole
// selector exactly when there is no combinator, which is the shape TOR-221
// wants for a row part.
func keyCompound(sel string) string {
	sel = strings.TrimSpace(sel)
	last := 0
	for i, r := range sel {
		if r == ' ' || r == '>' || r == '+' || r == '~' {
			last = i + 1
		}
	}
	return strings.TrimSpace(sel[last:])
}

// WHAT THESE TESTS DO NOT COVER, in one place - the summary the top of this
// file promises, written when TOR-140 added the first tests here that guard a
// CONTROL rather than a rendering rule.
//
// Every test here reads app.js as served text. That catches a rule deleted, a
// field renamed, an absent reading falling back to 0, a derivation left in
// place beside the fact that replaced it - real regressions, all of them, and
// all of them cheap. What it cannot catch is anything that only exists when
// the file RUNS:
//
//   - that buildLiveColumnHeaders() actually inserts its six <th>s before the
//     actions header, in the order LIVE_COLUMNS lists them;
//   - that clicking ▲ on a waiting row reaches setPriority at all, that the
//     row does not also expand, and that the button is disabled at the top of
//     the band rather than merely told to be;
//   - that a run_state arriving for a row NOBODY clicked repaints that row's
//     position (server.go's queueRecordsLocked sends one and has its own Go
//     test, but that the page redraws on it is not checked here);
//   - anything about layout: whether "high" fits the Queue cell on one line,
//     whether the wider actions column pushes the table past the pane.
//
// TOR-140 checked all four by hand in a real browser rather than leaving them
// as known gaps, against the actual binary (four magnets queued behind one
// running torrent, -max-active-torrents at its default 1). What that pass
// showed, since a claim of "driven once in a browser" is worth no more than
// what it names:
//
//   - the header row came out NAME/ADDED/STATUS/PEERS/SEEDS/DOWN/UP/AVAIL/
//     QUEUE plus the unlabelled actions column, in LIVE_COLUMNS' order;
//   - one ▲ click moved a row from position 3 to 1, marked it "high", disabled
//     its own ▲, repainted the two rows it passed to 2 and 3, and did not
//     expand the row it was clicked in; one ▼ on the front row sent it to the
//     back marked "low" with its ▼ then disabled;
//   - a second tab, never clicked in, followed the reorder - including the row
//     nobody had touched changing position - and a full page reload (the
//     GET /runs path rather than the socket) rendered the same order, so the
//     two paths agree;
//   - the running torrent's metadata timed out mid-pass, freeing the slot, and
//     the run that started was the PROMOTED one rather than the row that had
//     been at position 1 - the acceptance criterion end to end, in the real
//     process rather than against a fake runner;
//   - "high" and "low" each fit on one line under the position and left the
//     row height unchanged, and the table still fit the pane.
//
// The console carried no errors or exceptions throughout.

// TestFileDoneNoLongerLinksManifest is TOR-171: the page has no use for the
// raw JSON manifest a finished file's event carries, so nothing may turn
// manifest_url into a link the way it still does for the contact sheet -
// unrelated to this file's own ticket, but placed here because the front
// end's served-text tests mostly live in this one file. manifest_url itself
// stays on the wire (server.go's record() is unchanged) for whatever else
// reads the NDJSON stream - this test is only about what the page renders
// from it.
//
// SINCE TOR-191 THE GUARD IS STRONGER, because the field is now kept or not
// kept rather than linked or not linked: events.js records the contact sheet
// on the file entry (fentry.sheetURL) and deliberately records nothing for
// the manifest, so a manifest link is not merely absent from the renderer -
// there is no state for one to be drawn from. Both halves are checked, since
// either alone could be satisfied while the other brought it back.
func TestFileDoneNoLongerLinksManifest(t *testing.T) {
	// The link is one FILE's, so since TOR-195 it is file-detail.js's - and
	// "there is no field to draw one from" has to be swept over every module
	// that could hold one, which is why the negative half below reads the
	// whole front end rather than one file.
	page := fileDetailJS(t)
	events := eventsJS(t)

	sweep := strings.Join([]string{appJS(t), stateJS(t), events, runTableJS(t),
		runDetailJS(t), fileListJS(t), page}, "\n")
	for _, gone := range []string{"link(ev.manifest_url", "link(fentry.manifestURL", "manifestURL"} {
		if strings.Contains(sweep, gone) {
			t.Errorf("the front end still contains %q - the page should not offer a manifest link at all, "+
				"live or reopened from disk, and should not keep the field to draw one from", gone)
		}
	}

	// The contact sheet link is the one an end user does want, and must
	// survive both this change and the split untouched: kept by the event
	// layer, drawn by the page.
	if !strings.Contains(events, `fentry.sheetURL = ev.sheet_url || "";`) {
		t.Error("events.js's file_done handler no longer records the contact sheet on the file entry - " +
			"there would be nothing for the page to draw a link from")
	}
	if !strings.Contains(page, `link(this.fentry.sheetURL, "contact sheet")`) {
		t.Error("file-detail.js no longer links the file entry's sheetURL as \"contact sheet\" - " +
			"that link should stay")
	}
}

// TOR-171's browser check, same session and binary as TOR-165's pass above:
// no completed file was available to inspect through the real event path
// (the one seeded run in this environment was FAILED, with no frames), so
// the single-link-row rendering ("whatever separates two links must not
// leave a dangling separator" once the manifest link is gone, since a
// finished file usually offers only the contact sheet now) was checked
// directly against app.css's actual rule for it - .file-links is `display:
// flex; gap: 1rem` - by rendering one <a> inside a .file-links element on
// the live page and reading its layout back: one child, and a zoomed
// screenshot showing only "contact sheet" with nothing beside it. flex gap
// only inserts space BETWEEN children, so a single child leaves nothing to
// dangle by construction - confirmed rather than assumed.
//
// What this did not verify: manifest_url actually still arriving over a
// live NDJSON stream end to end (server.go's record() is unchanged, so this
// is read off the source, not observed on the wire, in this pass).

// ---------------------------------------------------------------------------
// TOR-191's BROWSER PASS: the three states the acceptance criterion names -
// a live run, a reconnect and a replay from disk - against a real binary
// built from this branch, serving on 127.0.0.1:8813 out of a seeded results
// tree (one torrent, three files, one of them a video with six frames on
// disk including a "shifted" and a "stepped" one, real JPEGs).
//
// THE ONE THING THAT HAD TO BE PROVED FIRST, because nothing else could be
// true without it: the three modules actually load and resolve each other in
// a browser. The network log for a page load reads
//
//	GET /  200 · /tokens.css 200 · /app.css 200
//	GET /app.js 200 · /state.js 200 · /events.js 200
//	GET /defaults 200 · GET /runs 200
//
// - app.js requested and then its two import specifiers requested, which only
// happens if the page parsed it AS A MODULE and followed them; and /defaults
// and /runs after that, which are app.js's own bootstrap, which sits BELOW
// the setRunFactory/setFileFactory/setView block. A view missing a hook would
// have thrown in setView and neither fetch would have been made. All three
// are served as text/javascript, which a module script requires.
//
// THE CONSOLE CARRIED NO PAGE ERRORS AT ANY POINT in the session - page load,
// the replay, the socket drop, the reconnect, and two live runs. The only
// exceptions logged came from an ad-blocking browser extension's own content
// scripts (chrome-extension://gighmm…/vendor/@eyeo/…, "Cannot read properties
// of undefined (reading 'useCache')" and "Could not establish connection"),
// none of them from this page.
//
// REPLAY FROM DISK. On load the disk row rendered from GET /runs alone:
// "Sintel (browser pass)", badge "on disk", "1 of 1 file(s) complete"
// (metaTitle's disk branch), all five live cells titled "no reading - this
// row was read off disk, never a live client" (absentReason), the queue cell
// titled "no arrival number: this row was read off disk, from a run of the
// server before this one" (queueCellTitle), and the nine sortable headers
// buildLiveColumnHeaders() builds. Reopening it replayed the whole history
// over the socket, and every part of the new state->render seam came out
// right:
//
//   - metadata_ready: the file list rebuilt to "3 file(s), 1 video" with the
//     .nfo and the .jpg carrying WHY_NOT_VIDEO and no checkbox;
//   - file_started: the Metadata disclosure showed Resolution 1280×720, Video
//     "h264 · High · 24.00 fps · 1.1 Mbps", Duration "888.0s" and BOTH track
//     groups ("eng · aac · 5.1 · default", "rus · ac3 · stereo ·
//     \"commentary\"", "eng · subrip · default", "unknown language · subrip ·
//     forced"). That is the whole point of fentry.media: those lines are
//     drawn from state, and nothing but state was left by the time they were;
//   - frame_ready: six cells at 01:00/03:01/05:00/07:00/09:01/11:00, each
//     with a real <img> that LOADED - so a frame storing the server's PATH
//     and frameFigure resolving it with url() produces a fetchable src. The
//     shifted cell read "taken at 03:01 - the exact point was not held by any
//     peer, so a nearby one was taken" and the stepped one "taken at 09:01 -
//     the frame there was blank, so the neighbouring keyframe was taken";
//   - file_done: the contact sheet link appeared, drawn from fentry.sheetURL,
//     resolved to http://127.0.0.1:8813/files/23b6bc33…; and its
//     loadFileDetail read the sets back, which is why the reach strip said
//     "where the frames came from was not recorded for this set" (this
//     fixture's manifest carries no byte ranges - renderReach's absent
//     branch, correctly);
//   - done: Save .torrent appeared in the detail header;
//   - the row's summary sentence "Sintel (browser pass)" under the header,
//     from entry.summaryLine; the row's own "1280×720 · 6 frames" from
//     fentry.media plus capturedCells; and the file's row titled "this row
//     captured this file - un-tick it to be offered a clear…", which is
//     TOR-183's offer path with picked, FINAL and framesOnDisk all true.
//
// A LIVE RUN, twice. A retry re-armed the same run id and failed for real:
// badge "failed" on the row and in the detail, the engine's own message
// "internal: source is neither a magnet URI nor a readable file:" under it
// (entry.error), a progressbar reading "6 of 6 frames captured" - which is
// entry.framesDone/framesTotal, the two fields TOR-191 newly declares in
// newRunState, written by events.js's applyFrameProgress - and renderAgain's
// "This run produced nothing, so there is nothing to top up…". A second live
// run from a magnet showed TOR-117's provisional name ("Sintel — from the
// magnet link, not yet confirmed by the torrent's own metadata"), badge
// "running", and metaTitle's waiting-for-metadata branch ("still waiting for
// the torrent's own metadata - no peer has answered yet, or none has offered
// the file list"), which is refreshStallDurations redrawing from
// entry.runningSince every second. It sorted to the top of the table, which
// is reorderRuns/compareEntries under the default newest-first sort.
//
// THE RECONNECT. The server was stopped with the page open: the status
// indicator went from "live" to "reconnecting" - events.js's socket.onclose
// calling view.status, which is app.js's setStatus - and every row kept
// exactly what it held, frames included. The server was restarted on the same
// port and the page reconnected on its own (connect()'s 1s retry), the
// indicator returned to "live", and the run list was unchanged: the
// reconnected server holds no runs, so the only thing replayed was the
// connection marker (a run_state with no "run" key), which applyRunState
// returns on immediately. Read back afterwards, both rows were identical to
// before the drop.
//
// WHAT THIS PASS DID NOT COVER, and how each is covered instead:
//
//   - A RESET REPLAYED ONTO A ROW THAT ALREADY HELD CONTENT, which is
//     TOR-184's trap proper. Only two things publish reset: true - a run's own
//     first record (server.go's StartRun/ReopenRun) - and neither could be
//     aimed at an existing entry from outside the page here, because the
//     entry has to be `reopening` or `claiming` for resolveIncomingRun to
//     fold the new id into it, and both flags are set by a CLICK. The retry
//     used instead publishes reset: FALSE on purpose (server.go's again,
//     TOR-152), which is why the row kept its list and frames through it -
//     correct, not a defect. The trap itself is checked by running the exact
//     message sequence: TestAReconnectLeavesNoStalePassBehind and
//     TestResetRunStateEmptiesEverythingAReplayWillStateAgain
//     (eventstate_test.go).
//   - CLICKS AND PIXELS. Script injection into the page was unavailable in
//     this environment (the browser tooling's inject/screenshot calls timed
//     out throughout, while its accessibility-tree, console and network
//     readers worked), so the DOM was read as an accessibility tree rather
//     than screenshotted, and every state was driven by real messages from
//     the server side rather than by clicking. What that means precisely: the
//     text and structure above are what the page actually rendered, and no
//     claim here rests on a pointer event, a layout measurement or a colour.
//     The reopen was therefore triggered by POST /runs/reopen rather than by
//     clicking the disk row, which is why the replayed run arrived as a
//     SECOND row beside the disk one - resolveIncomingRun found no entry
//     expecting that id, so ensureRun made one, exactly as documented.
//   - index.html. This branch does not change it (TOR-190 owns that file),
//     and the ES-module split cannot load without
//     `<script type="module" src="app.js"></script>` there. The line was
//     applied for this pass only, and reverted afterwards with a
//     byte-for-byte comparison against a pre-pass snapshot.

// ---------------------------------------------------------------------------
// TOR-203's browser pass: THE ID SWAP, BY CLICK, five times.
//
// This is the gap TOR-191's pass named and could not close. That pass triggered
// the reopen with POST /runs/reopen instead of a click, so resolveIncomingRun
// found no entry expecting the new id and ensureRun opened a SECOND row beside
// the disk one - correct for how it was driven, and the exact reason it never
// executed claimReopenedRun's swap branch. The swap is where the defect lived,
// so it survived that pass untouched.
//
// Driven this time by clicking the disk row, which is what raises `reopening`.
// torpeek was served headless on 8837 with -dht=false over an out dir holding
// the browser fixture's result sets. A reopen replays off disk (Server.ReopenRun
// goes through s.replayer), so no swarm and no seeder are involved in it.
//
// FIVE DISK ROWS REOPENED BY CLICK, and every one arrived as `done`:
// data-state went disk -> replaying -> done, the detail rendered inside the
// row with its frame grid, the row took a queue ordinal, and ONE row existed
// per reopen - the new id folded into the row that asked for it rather than
// spawning a duplicate. On the broken module each of these was `failed` with
// "syncEntry is not defined" as its reason, because the ReferenceError in the
// swap branch sent app.js's own handler into its catch.
//
// THE TOP-UP, on a finished run: the log recorded "topping up: 6 point(s), up
// to 150.0 MB more traffic", the row went done -> running and redrew AT ONCE -
// which is the redraw this ticket moved out to the caller - and NO error line
// appeared, neither the row's own nor the page's, read at 2.5s and again at 9s.
// That page-level line is precisely where the old failure printed itself.
//
// Two failures on the way there were the FIXTURE's, not the page's, and are
// recorded because each names a real constraint on this kind of check:
//   - `unknown profile "fastest", want "min-time" or "min-traffic"`. A plan
//     written by hand carried the label off the page's dropdown instead of the
//     internal profile name. The intake's "fastest" is not a stored value.
//   - `privacy_unresolvable: magnet has no trackers and DHT is disabled`. With
//     -dht=false the privacy routing refuses a trackerless magnet, which is why
//     the TOR-194 recipe's magnets carry a dead loopback tracker. Adding one to
//     the fixture's source is what let the topped-up run reach `running`.
// Both arrived AFTER the POST had succeeded, as named server errors on the run,
// and neither is a ReferenceError - which is what made them separable from the
// thing under test rather than fatal to the pass.
//
// A top-up whose set has no recorded plan offers nothing at all: topUpFor
// refuses with "this run was recorded before torpeek kept the capture plan",
// and the Top up control stays hidden. The stock fixture is such a set, so the
// half-captured set above (plan.count 12 against 6 frames on disk) had to be
// built before this half of the criterion could be exercised at all.
//
// NOT COVERED, deliberately: the swap branch reached from topUpRun rather than
// from the reopen. It needs a row that is still `entry.disk` when Top up is
// pressed, since that is what makes the page send no id and the server mint a
// new one - and clicking a disk row reopens it, so the detail is never open
// with the flag still up. Every other caller re-arms with the id it already
// has (Server.again), which is claimReopenedRun's second branch and never held
// the missing name. The swap itself is covered by execution instead:
// TestARunStateClaimingAReopeningRowSwapsItsIdWithoutThrowing
// (eventstate_test.go) runs it through apply() and fails on the broken module.

// TOR-205's browser pass: A SUBTREE CONNECTED BEFORE IT FINISHES ARRIVING,
// STREAMED IN BY HAND, AND A GENUINELY MISSING PART THAT NEVER DOES.
//
// settle_test.go's own header explains why this is a browser pass and not
// another text guard: wire()/awaitParts()/partsNeverArrived() are not
// DOM-free the way state.js and events.js are (eventstate_test.go's whole
// premise), so running them for real needs an actual DOM, which plain node
// does not have and this repository ships no jsdom for. Criterion 1 asks for
// exactly that - "tested by BUILDING the element and its subtree in an order
// that connects it early, not only by reading the code" - so this is where
// that happens.
//
// torpeek was served headless on 127.0.0.1:8973 with -dht=false over an out
// dir holding the browser fixture's stock result set (no swarm needed - see
// this ticket's own instructions: the bug is about connection order, not
// about anything a torrent does). Every check below ran as real JavaScript
// in the loaded page's own console, against the SHIPPED, REGISTERED classes
// (customElements.define already ran via app.js's normal bootstrap) - not a
// copy, not a mock DOM.
//
// FIRST, THE COST CLAIM ITSELF, CHECKED RATHER THAN ASSUMED: on torpeek's own
// page, `document.querySelector("frame-panel").partsObserver` and the same
// for `compare-dialog` and `run-table` were all `null` - the observer this
// ticket adds was never created at all, because index.html is fully parsed
// before app.js's deferred module ever calls customElements.define. Every
// one of the three was already `.wired === true` at that point.
//
// THE THREE ELEMENTS THAT CAN ACTUALLY STREAM (run-table, frame-panel,
// compare-dialog - see settle_test.go's header for why the other three
// cannot), each driven the same way: `document.createElement(tag)`, appended
// to the page EMPTY (connecting it with no subtree at all - the exact TOR-205
// scenario), then its markup appended back in over two steps with
// `await Promise.resolve()` between them so the MutationObserver's callback
// gets a chance to run without any real clock time passing.
//
//   frame-panel: connected empty -> not wired, an observer created
//     (partsObserver truthy). .lightbox appended alone (TOR-204's own probe's
//     exact shape - the wrapper exists, #lightbox-img does not yet) -> still
//     not wired, 1.3ms in. The rest of the subtree (.lightbox-view,
//     #lightbox-img, .lightbox-caption, .lightbox-close, .lightbox-zoom)
//     appended together -> WIRED, at 2ms total - four orders of magnitude
//     under the 10s deadline - with partsObserver and partsDeadline both back
//     to null. Called .open("/x.jpg", "test caption") on it afterward:
//     dialog.open became true and the caption read "test caption" - not just
//     "wired" but actually working.
//   compare-dialog: same shape, #compare-close held back as the one part
//     still missing after everything else arrived (1.8ms in, not wired) ->
//     WIRED at 2.2ms once it arrived. Proved the listener itself works, not
//     only that the field exists: showModal()'d the dialog, clicked
//     closeButton, and dialog.open read false afterward.
//   run-table: connected empty -> waiting. .run-table-wrap containing
//     table#run-table (thead/tr/th.run-actions-header, tbody#run-list)
//     appended, #run-list-empty held back -> still not wired at 1.3ms.
//     #run-list-empty appended -> WIRED at 3ms, with buildLiveColumnHeaders
//     having actually run: this.columns read 7 (the one actions header plus
//     the six live columns), this.sortHeaders.length read 6, and
//     this.columnWidths was a real object - the element did not just flip a
//     flag, it ran the rest of what connectedCallback always did.
//
// THE OTHER HALF OF CRITERION 2 - "NEVER", NOT "NOT YET" - for the same three,
// each connected with EVERY part except one, which was never added:
//
//   frame-panel, missing #lightbox-img/.lightbox-view/.lightbox-caption/
//     .lightbox-close/.lightbox-zoom entirely (dialog only): console read
//     "frame-panel: no view, img, caption, closeButton, zoomReadout inside
//     the element, 10s after connecting - giving up rather than waiting
//     forever" - caught by a window "error" listener, i.e. a real uncaught
//     error, not a swallowed one. This case doubled as an accidental but
//     genuine demonstration of the deadline racing real wall-clock time
//     across two separate tool calls: the element was created in one call and
//     checked again after enough real latency between calls had passed for
//     the 10s deadline to already be gone - which is exactly the scenario the
//     mechanism has to survive, not only a tight in-page loop.
//   compare-dialog, missing only #compare-close: connected, then the page
//     was left running a real setTimeout(11000) inside one script call.
//     Date.now() before and after read 13432ms elapsed (real wall clock, not
//     simulated) - the console read exactly "compare-dialog: no closeButton
//     inside the element, 10s after connecting - giving up rather than
//     waiting forever" once, naming the ONE part actually missing rather than
//     every part queried.
//   run-table, missing only #run-list-empty: same recipe, 16957ms real
//     elapsed, console read "run-table: no emptyNote inside the element, 10s
//     after connecting - giving up rather than waiting forever".
//
// In every one of the three "never" cases, `.wired` stayed `false` and
// `.partsObserver` read back `null` afterward - the failed deadline still
// tears down its own observer, so a genuinely dead element does not go on
// polling the page forever either.
//
// THE OTHER THREE ELEMENTS - run-detail, file-list, file-detail - build their
// own markup synchronously (settle_test.go's header has the full reasoning
// for why that means no settle mechanism was needed), so "shown able to
// fail" for them is TOR-192's original guard, unchanged, forced with a
// legitimate technique rather than by editing shipped source: one instance's
// own `querySelector` was shadowed (an own-property function on the
// instance, which JavaScript resolves before the prototype's) so ONE
// specific selector returned `null`, `build()` was called, the throw was
// read, and the shadow was removed before creating a second, ordinary
// instance to confirm normal construction still works:
//
//   run-detail: querySelector(".run-detail-cancel") shadowed to null ->
//     build() threw "run-detail: no detailCancel inside the element". A
//     fresh instance built clean: detailEl existed, no error.
//   file-list: querySelector(".picker-armed") shadowed to null -> build()
//     threw "file-list: no pickerArmed inside the element". A fresh instance
//     built clean: pickerEl existed, no error.
//   file-detail: querySelector shadowed to return null unconditionally (its
//     FIRST stage, build(), looks up only the one .file-detail slot) ->
//     build() threw "file-detail: no body inside the element" - the one
//     explicitly two-staged guard detailtree_test.go's own
//     TestEachDetailElementFindsItsPartsAndFailsLoudly names. A fresh
//     instance built clean: body existed, no error.
//
// Cleanup: every synthetic element created above was removed from the
// document before the pass ended, and the page's own six singletons were
// re-checked as the very last step - one of each tag, every one still
// `.wired === true` - so nothing this pass did left the real page any
// different from how it started. The server (pid captured from the
// foreground process, not a `go run` wrapper) was killed and
// `lsof -nP -iTCP:8973 -sTCP:LISTEN` confirmed the port free afterward.

// TOR-207's browser pass: the six checks 1.4.0's release notes named as
// outstanding, closed against a real torpeek server on 127.0.0.1:8827
// (-headless -dht=false -max-active-torrents 1), real loopback-seeded
// torrents (scratchpad/seed.go, the TestZZHarness194 harness's replacement -
// that harness lived in a worktree removed after TOR-194 and was never
// tracked in this repository), and Chrome DevTools Protocol driving the
// shipped page. Machine load at the start of the pass was `uptime`'s
// `4.58 22.72 21.58` on 8 cores (the 1-minute figure - the one that matters
// for what the tooling is about to do - was calm; the 15-minute figure was
// inherited from earlier sibling agents' work and fell across the session).
// One CDP `Runtime.evaluate` call timed out at 45s against load ~8.9; a
// retry immediately after succeeded, matching TOR-194's own note that this
// is a load artifact and not a driving-agent one.
//
// CHECK 1 - A LIVE ROW'S FIGURES AT FULL STRENGTH, the positive case TOR-194
// left unverified. It could not be reached by polling an ordinary loopback
// run: a 200KB-2MB clip transfers over loopback in well under a second, far
// faster than any HTTP round trip this session could poll with, so peers and
// download_bps read back 0 for a run's entire observable "running" window in
// every unthrottled attempt - the download is over before the first poll
// after it starts, and the remaining ~15-30s of "running" is ffmpeg frame
// extraction with the swarm already idle. seed.go gained a `-rate-bps` flag
// (an anacrolix `cfg.UploadRateLimiter`, capping the SEED side's upload) to
// hold a transfer open long enough to read: at -rate-bps 40000 over a
// 1.98MB clip, GET /runs returned
// `"live":{"peers":1,"seeds":1,"download_bps":39323.92,"upload_bps":0,...}`,
// and the page's own DOM showed PEERS 1, SEEDS 1, DOWN ramping 19.2 KB/s ->
// 37.2 KB/s -> 41.6 KB/s as the poll caught it, UP 0 B/s, AVAIL 1.00x/0
// missing - every one of `run-cell-peers/-seeds/-down/-up/-availability` on
// that row read `data-absent="false"`, including the genuinely-zero UP cell,
// which is the case criterion 1 is actually about: a real zero is not the
// same reading as an absent dash, and hasLive() does not conflate them.
//
// CHECK 2 - ABSENT SINKING TO THE END OF A SORT, ON A TABLE HOLDING BOTH. The
// attempt TOR-194 counted was rejected for running on one row, which cannot
// show an order. This one ran on 13: 1 running row with real Peers (the
// check 1 row above) and 12 absent rows (1 queued, 11 done/on-disk). Peers
// was clicked to ascending, then to descending; in BOTH directions the one
// real row (`absent="false"`, text "1") sat at index 0 and all 12 absent
// rows (`absent="true"`, text "-") filled 1-12 - proving the rule is
// "absent always sinks to the end" rather than "small values sort first",
// which a single real row sorting trivially first could not have shown
// either way.
//
// CHECK 3 - TWO ROWS OPEN AT ONCE, AND A FILE'S DETAIL SURVIVING A COLLAPSE
// AND RE-OPEN. tor207-filler2 (running) and tor207-multi (queued) were
// expanded together and both stayed rendered open at once - ordinary
// <run-detail> accordion behaviour, but never asserted in a browser before
// now. Separately, on a finished row with real frames on disk
// (tor207-clip-k), its lone file's own picker-open sub-detail was opened
// (`.picker-item.dataset.expanded` read "true", a `.file-detail` node
// present), the WHOLE TORRENT ROW was collapsed
// (`.run-row-main`'s `aria-expanded` false), then re-expanded
// (`aria-expanded` true again) - and the file's own `dataset.expanded` read
// "true" throughout, with `.file-detail` still present after the reopen:
// the file-level state survived the row-level collapse rather than
// resetting.
//
// CHECK 4 - COLUMN WIDTHS PERSISTING ACROSS A RELOAD, AND A CORRUPT
// localStorage VALUE FALLING BACK TO DEFAULTS. The Name column's resize
// handle was dragged with real `PointerEvent`s (pointerdown/pointermove
// with `buttons: 1`/pointerup on the `.col-resizer`, the same events
// wireColumnResizers listens for and the reason a plain synthetic
// MouseEvent drag did nothing first) from the default 20rem to 456.734375px.
// `localStorage["torpeek.columnWidths"]` read back
// `{"name":456.734375}` and a full page reload (fresh navigation, not a
// soft refresh) still computed `--col-w-name: 456.734375px` - the width
// survived the reload, matching what a screenshot showed as a visibly wider
// Name column. Then `localStorage.setItem("torpeek.columnWidths",
// "{not valid json!!!")` and another reload: the page rendered normally
// (18 rows, table intact), `--col-w-name` read back the plain default
// "20rem", and `read_console_messages` found no errors or exceptions across
// that load - loadColumnWidths' try/catch does exactly what its own comment
// says, falling back to "as if nothing was ever stored" rather than
// breaking.
//
// CHECK 5 - THE STALL TICKER COUNTING UP, THEN GOING QUIET ONCE THE ELEMENT
// IS REMOVED. A torrent was posted whose seed's own peer address was
// deliberately left out of -peer, so it could never connect: GET /runs
// showed `"stall":{"code":"no_peers","since_ms":5000}` and climbing, and the
// row's `.run-meta` text read "no peers connected for 15s", then "...25s",
// then "...50s" as real time passed - the 1s ticker (run-table.js's single
// `stallTimer`) counting up for real. To check it goes quiet: the live
// `<run-table>` element's own `.stallTimer` read back `1` (an active
// interval id), `window.clearInterval` was wrapped to record its argument,
// and `el.remove()` was called directly. Immediately after: `el.stallTimer`
// had been reset to `null` and the wrapped `clearInterval` had been called
// with exactly `[1]` - disconnectedCallback's `clearInterval(this.stallTimer)`
// ran for real, on the real interval id, the moment the element left the
// document, rather than leaving a dangling 1s timer touching removed nodes.
//
// CHECK 6 - TOR-197'S FIVE-VERDICT CHAIN AND ITS TWO SENTENCES, ON A REAL
// QUEUED TORRENT - the one check TOR-197 itself only ever text-checked.
// Getting a queued row that still has an on-screen file picker took an
// ordering discovery worth recording: a QUEUED entry carries no metadata at
// all (files:0, no file list, just a "QUEUED" badge and Cancel - confirmed
// against file-list.js's own syncFileList comment, "a queued one, whose
// metadata has not been fetched"), so a multi-file torrent must be POSTed
// FIRST, while the one active slot is still free, to reach `needs-action`
// ("CHOOSE FILES", files known, picker rendered) - then a SECOND, throttled
// torrent posted after it takes the slot instead, since a needs-action row
// consumes no slot of its own. That left a genuine two-video-file torrent
// (tor207-multi: a-clip.mkv, b-clip.mkv) sitting at `state: "queued"`,
// `"QUEUED"` badge, queue position #1, with a real running row ahead of it.
// Both boxes were ticked (asking for both files while still queued), and
// the resulting `<label class="picker-file">` title on EACH file read
// exactly: "this torrent is waiting to start and has not been handed to
// the engine yet - un-tick to take this file out of the pass it will start
// with. The other 1 stay, and nothing has been fetched or deleted" - the
// untick==="narrow", `entry.narrowable.size > 1` branch of
// updateFileCosts, word for word, including the two-sentence shape
// (what pressing it does, then what happens if it is the one taken) the
// ticket's own comment calls out as the whole of its legibility
// requirement. Before ticking, both boxes showed the `!asked -> ""` case
// (unchecked, a plain frame-count estimate, no verdict title) - so this one
// pass crossed both ends of the chain's first two links on a torrent that
// was never anything but genuinely queued.
//
// No defect was found: all six checks show the shipped code doing exactly
// what its own comments say it does. The one thing worth flagging for
// whoever next drives torpeek's web UI from a script rather than the page:
// `DELETE /runs/{id}` is not a route (405) - cancelling a run is
// `POST /runs/cancel` with a `{"id": "..."}` JSON body
// (Server.handleCancelRun), reached from the row's own X button.

// TOR-216's browser pass: TOR-207's six checks above, re-run against TOR-215's
// grid - `div#run-table.run-grid` / `div#run-list.run-grid-rows`, each entry a
// `div.run-row-group` (`grid-template-columns: subgrid`) holding `.run-row`
// and `.run-detail-row` as siblings, in place of `<table>/<tr>`. Every one of
// the six exercises this markup, so all six were unverified again the moment
// TOR-215 landed - not because anything was expected to be wrong, but because
// nothing had looked. Against a real torpeek server on 127.0.0.1:8916
// (-headless -dht=false -max-active-torrents 1 -n 200), real loopback-seeded
// torrents (scratchpad/seed.go), a real out dir seeded from TOR-207's own
// out207 (its 15 completed runs plus the browserout disk fixture, copied
// forward rather than re-earned), and Chrome DevTools Protocol driving the
// shipped page.
//
// THE MACHINE, RECORDED BECAUSE IT MATTERED HERE MORE THAN USUAL. `uptime` at
// the very start read a calm `6.26 11.62 39.59`. Partway through, an unrelated
// third-party security agent's own `spindump` (plus `softwareupdated`) drove
// the 1-minute figure as high as `541.67`, with `vm_stat`/`vm.swapusage`
// showing the machine nearly out of real memory (6987 of 8192 MB swap used,
// under 25000 4 KB pages free). Every `Runtime.evaluate` timeout in this pass
// landed inside that window and every one succeeded on retry once the 1-minute
// figure came back under ~15 - the same load-artifact pattern TOR-194 and
// TOR-207 both recorded, confirmed a third time. Nothing here that failed
// once and passed on a retry is treated as a finding about the page.
//
// CHECK 1 - A LIVE ROW'S FIGURES AT FULL STRENGTH. A fresh single-file
// torrent (tor216-live2, never seeded before, to avoid the disk-cache skip a
// reused payload hits - see below) was posted by `.torrent` path and throttled
// on the seed side (`-rate-bps 25000`) the same way TOR-207 established is
// required at all. An in-page collector (a `setInterval` pushing distinct
// snapshots onto `window`, per this ticket's own guidance on a row that keeps
// changing underneath a single tool call) caught, and a direct read of the
// row's cells confirmed: peers "1", seeds "1", down "22.4 KB/s", up "0 B/s",
// availability "1.00×0 missing" - every one of
// `.run-cell-peers/-seeds/-down/-up/-availability` (the grid's actual class
// names; the field names in the JSON payload are `download_bps`/`upload_bps`
// but the CSS classes have always been the shorter `-down`/`-up`, unchanged by
// TOR-215) reading `data-absent="false"`, the real-zero UP cell included -
// the same distinction TOR-207's own check 1 called out.
//
// ONE DEDUP TRAP FOUND WHILE SETTING THIS UP, not a defect but worth recording
// so it is not re-discovered at cost: reposting a torrent whose infohash and
// params hash already have a complete result directory on disk (TOR-207's own
// tor207-clip-a, copied forward in out216) reaches `state: "done"` in under
// three seconds, with no observable download window at all - the engine skips
// straight to what is already on disk. Check 1's positive case needs a payload
// that has NEVER been captured under this exact out dir before; every payload
// in this pass past the first attempt used a freshly-named directory for
// exactly this reason.
//
// CHECK 2 - ABSENT SINKING TO THE END OF A SORT, ON A TABLE HOLDING BOTH. Run
// on 23 rows: 1 real (tor216-live3, a second fresh throttled live torrent -
// `.run-cell-peers` `data-absent="false"`, text "1") and 22 absent (the 17
// disk rows out216 started with, plus five more live-then-finished/cancelled
// entries this pass's own setup produced along the way). The Peers header was
// clicked to ascending, then to descending; in BOTH directions the sequence of
// `data-absent` down the 23 rows read exactly `R` at index 0 followed by 22
// `A`s - the one real row first, every absent row after it, in both
// directions, which is what TOR-207's own check 2 established this proves and
// a 1-real/12-absent or 1-real/22-absent split equally cannot show any other
// way.
//
// CHECK 3 - TWO ROWS OPEN AT ONCE, AND A FILE'S DETAIL SURVIVING A COLLAPSE
// AND RE-OPEN. tor216-live3 (running) and tor207-clip-k (done, real frames on
// disk, carried forward from out207) were expanded together by clicking each
// `.run-row-main` button; both `#run-detail-N` elements (siblings of
// `.run-row` inside their own `.run-row-group`, not a following `<tr>`) were
// simultaneously PRESENT AND VISIBLE (`getBoundingClientRect()` non-zero for
// both, not merely `aria-expanded="true"`). Separately, on tor207-clip-k's
// lone file: its `.picker-item` was already `data-expanded="true"` with a
// `.file-detail` node present; the WHOLE ROW was then collapsed
// (`.run-row-main` `aria-expanded` false, detail rect 0×0 - confirmed
// genuinely invisible, not merely un-flagged) and re-expanded (`aria-expanded`
// true again, detail rect 837×6689.8) - and the file's own `data-expanded`
// read "true" throughout, `.file-detail` still present after the reopen: the
// file-level state survived the row-level collapse exactly as it did under
// the `<table>`, now through a `.run-row-group` rather than two `<tr>`s.
//
// CHECK 4 - COLUMN WIDTHS PERSISTING ACROSS A RELOAD, AND A CORRUPT
// localStorage VALUE FALLING BACK TO DEFAULTS. A real pointer drag
// (`pointerdown`/`pointermove`/`pointerup`, `buttons: 1`, dispatched on the
// `.col-resizer` handle itself - dispatching `pointerup` on `document` instead
// of the handle silently did NOT save, because `wireColumnResizers` listens
// for it on the handle after `setPointerCapture`, so a synthetic event has to
// land where a real one's capture would redirect it) moved the Name column
// from the default 20rem (320px) to 620px. `localStorage["torpeek.columnWidths"]`
// read back `{"name":620}`, and a full page reload (fresh navigation) still
// computed `--col-w-name: 620px` - survived the reload, matching TOR-207's own
// finding that this is unaffected by the table-to-grid conversion (the ticket
// TOR-215 itself already argued from source: the drag writes a CSS custom
// property and reads a header's own rect, and both stay true of a grid).
// Then `localStorage.setItem("torpeek.columnWidths", "{not valid json!!!")`
// and another reload: the page rendered normally (23 rows, grid intact),
// `--col-w-name` read back the plain default "20rem", and
// `read_console_messages` found NO messages at all - not even benign ones,
// let alone errors - across that load once console tracking was armed before
// the reload that mattered.
//
// CHECK 5 - THE STALL TICKER COUNTING UP, THEN GOING QUIET ONCE THE ELEMENT IS
// REMOVED. A torrent was seeded and posted whose peer address was deliberately
// left out of -peer, so it could never connect: GET /runs showed
// `"stall":{"code":"no_peers","since_ms":...}` climbing (10000 -> 39999 across
// polls a few seconds apart), and the row's `.run-meta` text read "no peers
// connected for 33s" with title "stalled: no peers connected" and
// `data-stall="true"` - the 1s ticker counting up for real, unchanged by the
// conversion. To check it goes quiet: since TOR-215 the stall ticker lives on
// the `<run-table>` custom element itself (one timer for the whole table, not
// per row) - `document.querySelector("run-table").stallTimer` read back `1`
// (an active interval id). `window.clearInterval` was wrapped to record its
// argument, and the element's own `.remove()` was called directly. Immediately
// after: `.stallTimer` had been reset to `null` and the wrapped `clearInterval`
// had been called with exactly `[1]` - `disconnectedCallback`'s
// `clearInterval(this.stallTimer)` ran for real, on the real interval id, the
// moment the element left the document.
//
// AN INCIDENTAL FINDING WHILE SETTING THIS CHECK UP, ORTHOGONAL TO TOR-215 AND
// FILED SEPARATELY (TOR-219) RATHER THAN FOLDED IN HERE: three independent
// no-peer torrents in this pass - TOR-207's own tor207-stall (reused directory)
// and two freshly-seeded ones (tor216-stall2, tor216-stall3), none of them
// ever reachable by any peer - each transitioned on their own from
// `state: "running"` (stalled, `code: "no_peers"`) to `state: "done"` with
// `complete: 0` after roughly 40-70 seconds of continuous no-peer stall, with
// no frames ever written to disk. One of the three reproductions (tor216-stall3)
// happened on a calm machine (`uptime` 1-minute figure ~6-7 at post time), so
// this does not look like only a load artifact, but this task did not read
// enough of internal/core's stall/budget handling to name a cause - recorded
// as observed, not diagnosed. `internal/core/budget.go`'s `defaultRunTime` (10
// minutes) is far longer than the ~60s observed, so it is very likely not the
// mechanism.
//
// CHECK 6 - TOR-197'S FIVE-VERDICT CHAIN AND ITS TWO SENTENCES, ON A REAL
// QUEUED TORRENT. Reproducing the ordering TOR-207 discovered took one more
// correction on top of it: posting the multi-file torrent WHILE something else
// already holds the active slot skips `needs-action` entirely and lands
// straight in `queued` with no metadata at all (`files: 0`, no picker) - the
// same shape as any other queued row, useless for this check. The multi-file
// torrent (tor216-multi3: a-clip.mkv, b-clip.mkv) had to be posted FIRST, while
// the slot was still free, to reach `needs-action` ("choose files" badge, the
// detail correctly showing "2 video file(s)" even though the top-level
// `GET /runs` listing's own `files` field read 0 for it at that point - a
// display-field lag on this endpoint, not something the row's own detail got
// wrong). A second torrent (tor207-stall, its own peer again deliberately
// withheld so it would occupy the slot without ever finishing) was posted
// second and took the active slot. Both of tor216-multi3's file checkboxes
// were then ticked by a real DOM click on each `input[type="checkbox"]`,
// asking for both files while still parked: the row flipped to a genuine
// `state: "queued"`, badge "queued", queue cell "1#1", and each
// `<label class="picker-file">` title read exactly: "this torrent is waiting
// to start and has not been handed to the engine yet - un-tick to take this
// file out of the pass it will start with. The other 1 stay, and nothing has
// been fetched or deleted" - the untick==="narrow" branch, word for word,
// matching TOR-207's own record and confirming the grid's `.run-row-group`
// carries the same file-picker state and title text the `<table>` row did.
//
// NO DEFECT IN THE CONVERSION ITSELF WAS FOUND: all six checks show the grid
// doing exactly what the `<table>` did, cell for cell, class name for class
// name (the one genuine surprise - `.run-cell-down`/`.run-cell-up` rather than
// a name matching the JSON field - was this task's own wrong guess, not a
// defect). The stall/budget anomaly above is filed as TOR-219, in the backlog
// rather than this release, because it is an engine question unrelated to
// TOR-215's markup change and this task did not diagnose it far enough to say
// it belongs in this release's scope.

// TOR-221 CHANGED THE MARKUP UNDER THE RECORD ABOVE, so what that record still
// stands for is worth being exact about rather than leaving to a reader to
// guess from the ticket numbers.
//
// What moved: `#run-table` is no longer a grid, `#run-list` lost
// `.run-grid-rows` and its `display: contents`, the header cells now sit
// inside a `.run-grid-head-row` band, and `.run-row-group` /
// `.run-detail-row` are plain blocks where they were subgrids. So every
// selector the six checks name still exists and still means the same thing -
// `.run-row`, `.run-row-main`, `.run-cell-*`, `.picker-item`, `.file-detail`,
// `#run-detail-N` are untouched - and the one sentence in check 3 that reads
// as a claim about the wrapper's LAYOUT ("through a `.run-row-group` rather
// than two `<tr>`s") is now true of a block rather than of a subgrid.
//
// What TOR-221's own browser pass re-exercised, against 22 disk rows: the
// column drag in both directions with its clamps and its localStorage
// round-trip (check 4's mechanism), a sort click with `aria-sort` moving
// across all nine headers (check 2's mechanism), three rows open at once with
// their details measured on screen (check 3's first half), and the alignment
// and slack numbers TOR-221 exists for. What it did NOT re-run is anything
// needing a live throttled torrent - checks 1, 5 and 6, and check 2's
// real-vs-absent split - because those exercise the data path and TOR-221
// touches no JS that reads or writes a cell's data: the whole of its JS diff
// is three part lookups and one insertion point. That is a reason, not a
// claim that they are covered.
