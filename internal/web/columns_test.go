package web

import (
	"regexp"
	"strings"
	"testing"
)

// TOR-139 added six sortable columns to the run table - peers, seeds, both
// rates, availability and queue position - and the two rules the ticket
// itself is about: ABSENT IS NOT ZERO (a queued row's missing reading must
// never render or sort as though it were a measured zero), and availability
// is copies per piece, not a percentage.
//
// There is no JS test runner in this repository (see internal/web's other
// _test.go files, none of which run app.js - they all read it as served
// text, the same way TestServesEmbeddedFrontend does for "WebSocket" and
// ".grid"). These tests follow that precedent: they assert on the exact
// source app.js ships, which is a real - if narrower - guard than executing
// it would be. What they do NOT and cannot cover is documented on each test
// and summarized at the bottom of this file.

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

// TestLiveColumnsAreWiredIntoBothHeadersAndSorting guards against the two
// halves of TOR-139 drifting apart: LIVE_COLUMNS is what builds each <th> (so
// a column with no header cell cannot be clicked to sort at all), and the
// matching case in sortValue's switch is what makes clicking it actually
// reorder anything (a header with no case falls through to sortValue's
// "when" default, which would silently sort every new column exactly like
// the date column instead of by its own figure). A column present in one and
// not the other is a column that looks wired but is not.
func TestLiveColumnsAreWiredIntoBothHeadersAndSorting(t *testing.T) {
	js := appJS(t)

	for _, key := range []string{"peers", "seeds", "download_bps", "upload_bps", "availability", "priority"} {
		if !strings.Contains(js, `key: "`+key+`"`) {
			t.Errorf("app.js's LIVE_COLUMNS declares no %q column - it would have no header cell, so nothing to click", key)
		}
		if !strings.Contains(js, `case "`+key+`":`) {
			t.Errorf("app.js's sortValue() has no case for %q - its header would exist but clicking it would fall "+
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
	js := appJS(t)

	// unit: "..." is what buildLiveColumnHeaders() (app.js) turns into a
	// second, visible line under the "Avail" label - see LIVE_COLUMNS and
	// .run-th-unit in app.css. A tooltip-only mention would not satisfy this:
	// the whole point is that a person never has to open one.
	if !regexp.MustCompile(`key:\s*"availability"[^}]*unit:\s*"copies/piece"`).MatchString(js) {
		t.Error(`app.js's availability column declares no unit: "copies/piece" - the header would show only ` +
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
	js := appJS(t)

	for _, want := range []string{
		`case "peers": return hasLive(entry) ? entry.live.peers : null;`,
		`case "seeds": return hasLive(entry) ? entry.live.seeds : null;`,
		`case "download_bps": return hasLive(entry) && entry.live.download_bps != null ? entry.live.download_bps : null;`,
		`case "upload_bps": return hasLive(entry) && entry.live.upload_bps != null ? entry.live.upload_bps : null;`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js's sortValue() does not contain %q - an absent reading may be falling back to 0 "+
				"(a real, comparable value) instead of null (excluded from comparison, see compareEntries)", want)
		}
	}

	// availability's case is a small block rather than one line (it reads
	// entry.live.swarm, which can be absent even when entry.live itself is
	// present - a torrent that has not been asked for bytes yet), so this
	// checks the block returns null on both of the ways it can be absent.
	m := regexp.MustCompile(`case "availability": \{([^}]*)\}`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal(`app.js's sortValue() has no case "availability": { ... } block`)
	}
	if !strings.Contains(m[1], "return s ? s.copies_per_piece : null;") {
		t.Errorf("app.js's availability sortValue case does not return null when the swarm reading is absent: %q", m[1])
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
	js := appJS(t)

	m := regexp.MustCompile(`(?s)function compareEntries\(a, b\) \{.*?\n\}`).FindString(js)
	if m == "" {
		t.Fatal("app.js has no compareEntries(a, b) function to check")
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

// TestQueueColumnDerivesFromReportedFieldsNotAnInventedPriority guards the
// other explicit instruction in this ticket: the priority/queue column must
// read the run's place in the queue from fields GET /runs already reports
// (state and when/QueuedAt - see queueRank's own doc in app.js), never from
// a "priority" field the server does not send. GET /runs' RunSummary
// (listing.go) has no such field; if a future change starts reading
// row.priority or ev.priority, that is exactly the invented field this
// ticket said not to add.
func TestQueueColumnDerivesFromReportedFieldsNotAnInventedPriority(t *testing.T) {
	js := appJS(t)

	for _, bad := range []string{"row.priority", "ev.priority"} {
		if strings.Contains(js, bad) {
			t.Errorf("app.js contains %q - GET /runs reports no priority field (listing.go's RunSummary), "+
				"so reading one here would be inventing it rather than rendering what the server sent", bad)
		}
	}

	// queueRank (app.js) is what answers the "priority" sort key and the
	// queue cell's text - it must refuse to answer for anything not
	// currently queued, which is the whole of why it needs no server-side
	// priority field: outside "queued" the question has no honest answer yet.
	if !strings.Contains(js, `if (entry.state !== "queued") return null;`) {
		t.Error(`app.js's queueRank() does not guard on entry.state !== "queued" - it would answer a rank for ` +
			`a row that was never in the queue`)
	}
}
