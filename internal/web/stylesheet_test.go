package web

import (
	"regexp"
	"strings"
	"testing"
)

// TOR-145: a dangling comment silently ate .run-row[data-expanded="true"] and
// every Go test stayed green, because theme_test.go and font_test.go all read
// the stylesheet with regexes that assume it is well formed and never check.
// The two tests below are that check, in two levels, because one level is not
// enough:
//
//   - TestTheStylesheetIsWellFormed catches a file that is actually broken:
//     an unterminated comment, a stray terminator, unbalanced braces.
//   - TestTheLoadBearingSelectorsSurvive catches TOR-145's own defect, which
//     the first test structurally cannot: a comment whose open and close both
//     exist and belong to each other, but which spans further than its author
//     meant, is syntactically perfect - one /*, one matching */, and every
//     brace inside it still balances because the swallowed rule is itself
//     valid CSS. It fails by ABSENCE, not by syntax, so only a test that looks
//     for specific rules and doesn't find them can catch it.
//
// Neither reaches for a CSS parser: the failures here are a comment and a
// brace, and a hand-written scanner that names the line it gave up on is more
// use than a dependency, for a project that ships no third-party Go code it
// doesn't need.

// TestTheStylesheetIsWellFormed is a hand-written balance check. It walks the
// raw stylesheet text once, tracking whether it is inside a /* */ comment or
// a quoted string (so a stray brace or comment marker inside "..." or '...'
// - none exist in app.css today, but nothing here should break the day one
// does - is not mistaken for real structure), and fails on the first place
// the file stops being balanced.
func TestTheStylesheetIsWellFormed(t *testing.T) {
	css := stylesheet(t)

	line := 1
	depth := 0
	var openBraceLines []int
	inComment := false
	commentOpenedAt := 0
	var quote byte

	for i := 0; i < len(css); i++ {
		c := css[i]
		if c == '\n' {
			line++
		}

		if quote != 0 {
			switch c {
			case '\\':
				i++ // an escaped character inside a string can't end it
			case quote:
				quote = 0
			}
			continue
		}
		if inComment {
			if c == '*' && i+1 < len(css) && css[i+1] == '/' {
				inComment = false
				i++
			}
			continue
		}

		switch c {
		case '"', '\'':
			quote = c
		case '/':
			if i+1 < len(css) && css[i+1] == '*' {
				inComment = true
				commentOpenedAt = line
				i++
			}
		case '*':
			if i+1 < len(css) && css[i+1] == '/' {
				t.Fatalf("app.css:%d: stray */ with no /* open to close - a "+
					"comment terminator with nothing before it to terminate", line)
			}
		case '{':
			depth++
			openBraceLines = append(openBraceLines, line)
		case '}':
			if depth == 0 {
				t.Fatalf("app.css:%d: unmatched } - a closing brace with no "+
					"{ open to close it", line)
			}
			depth--
			openBraceLines = openBraceLines[:len(openBraceLines)-1]
		}
	}

	if inComment {
		t.Fatalf("app.css:%d: /* opened here is never closed - everything "+
			"after it, to the end of the file, is silently commented out", commentOpenedAt)
	}
	if depth != 0 {
		t.Fatalf("app.css:%d: { opened here is never closed (%d brace(s) "+
			"still open at end of file)", openBraceLines[0], depth)
	}
}

// TestTheLoadBearingSelectorsSurvive is the check that would have caught
// TOR-145's own defect. It strips comments the same way TestEveryColourComesFromAToken
// does, then asserts a handful of selectors are still there in what's left.
//
// The selectors are picked to spread across the file rather than cluster in
// one rule, each guarded by its own earlier ticket, so that a comment
// swallowing anything from here on has a good chance of eating one of them
// before it reaches the */ that stops it:
//
//   - .intake                              the pinned upload bar (TOR-137)
//   - .run-progress, .run-progress-seg     the run's progress bar (TOR-123)
//   - .reach-block                         the piece-strip blocks (TOR-111)
//   - .reach-strip, .avail-swarm-dot       the piece strip's own extent and
//     the swarm chip TOR-142 added and
//     TOR-153 moved onto this row - two
//     selectors because the whole point
//     of the merge is that the strip and
//     the chip stay separate elements
//     even sharing one row
//   - .run-again                           the top-up / retry block (TOR-152),
//     added to this list deliberately: it
//     occurs exactly once as a bare
//     selector (.run-again-line and its
//     siblings fail the identifier-boundary
//     check below), so the check is exact
//     for it, and it is the one block whose
//     absence would silently remove a
//     control rather than a decoration
//   - .thumb-pending, .thumb-failed        the thumbnail cell states (TOR-110)
//   - .run-detail::before                  the detail panel's corner brackets
//
// This is a presence check, not a rule-by-rule one: .thumb-pending,
// .thumb-failed and .run-detail::before each appear in more than one
// selector list in app.css, so this only notices if every one of a class's
// occurrences is gone, not a single swallowed rule that leaves the name
// intact elsewhere. .intake, .run-progress, .run-progress-seg and
// .reach-block each occur exactly once, so for those the check is exact -
// which is why .intake, extending the comment already sitting right above it
// over the whole rule, is the shape used below to verify this test can fail
// at all: TOR-145's original defect, reproduced without touching balance.
func TestTheLoadBearingSelectorsSurvive(t *testing.T) {
	css := stylesheet(t)
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")

	for _, sel := range []string{
		".intake",
		".run-progress",
		".run-progress-seg",
		".reach-block",
		".reach-strip",
		".avail-swarm-dot",
		".run-again",
		".thumb-pending",
		".thumb-failed",
		".run-detail::before",
	} {
		if !hasSelector(live, sel) {
			t.Errorf("app.css has no %q rule outside a comment - it was "+
				"either deleted, or a comment now swallows it whole", sel)
		}
	}
}

// hasSelector reports whether sel occurs in css as a whole selector token,
// rather than as a substring of a longer one (".run-progress" must not match
// inside ".run-progress-seg"). It's a plain substring search with an
// identifier-boundary check on both sides, deliberately no more than that:
// every selector this checks is a simple class or pseudo-element, never
// compounded with another class with nothing between them.
func hasSelector(css, sel string) bool {
	for from := 0; ; {
		i := strings.Index(css[from:], sel)
		if i < 0 {
			return false
		}
		pos := from + i
		before, after := byte(0), byte(0)
		if pos > 0 {
			before = css[pos-1]
		}
		if end := pos + len(sel); end < len(css) {
			after = css[end]
		}
		if !isIdentByte(before) && !isIdentByte(after) {
			return true
		}
		from = pos + 1
	}
}

func isIdentByte(b byte) bool {
	return b == '-' || b == '_' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// TestLeftPanelArtifactsAreFullyRemoved is TOR-168's own guard against the
// thing its acceptance criteria explicitly warns about: dead CSS and a dead
// localStorage key left behind after the feature they served is gone. The
// ticket's own description records the panel in enough detail to rebuild it,
// so nothing here needs to guess what "gone" means - it checks each artefact
// the description names by name: the --panel-width token, the .runs-panel
// and .panel-header rules, the .resizer divider (not .col-resizer, the
// unrelated per-column handle TOR-157 added, which must survive), and every
// one of app.js's panel functions and element lookups, plus the
// torpeek.panelWidth localStorage key and the markup that held them.
func TestLeftPanelArtifactsAreFullyRemoved(t *testing.T) {
	css := stylesheet(t)
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")

	for _, sel := range []string{".runs-panel", ".panel-header", ".resizer"} {
		if hasSelector(live, sel) {
			t.Errorf("app.css still has a %q rule - TOR-168 removed the left panel "+
				"and its divider; leaving dead CSS behind is worse than either keeping "+
				"or removing the feature", sel)
		}
	}
	if !hasSelector(live, ".col-resizer") {
		t.Errorf("app.css has lost .col-resizer along the way - that is TOR-157's " +
			"per-column drag handle, unrelated to the left panel's own .resizer, and " +
			"must survive this ticket")
	}
	if strings.Contains(live, "--panel-width") {
		t.Errorf("app.css still declares or reads --panel-width - the panel it sized " +
			"is gone (TOR-168)")
	}

	js, err := embedded.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("reading the embedded app.js: %v", err)
	}
	jsLive := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(js), "")
	jsLive = regexp.MustCompile(`(?m)//[^\n]*`).ReplaceAllString(jsLive, "")

	for _, name := range []string{
		"runsPanel", "PANEL_WIDTH_KEY", "PANEL_MIN_WIDTH", "PANEL_MAX_WIDTH",
		"PANEL_RIGHT_MARGIN", "clampPanelWidth", "loadPanelWidth", "savePanelWidth",
		"applyPanelWidth", "endPanelDrag", "torpeek.panelWidth",
		`"runs-panel"`, `"resizer"`,
	} {
		if strings.Contains(jsLive, name) {
			t.Errorf("app.js still references %q - every panel function and element "+
				"lookup was meant to go with the panel (TOR-168)", name)
		}
	}

	html, err := embedded.ReadFile("assets/index.html")
	if err != nil {
		t.Fatalf("reading the embedded index.html: %v", err)
	}
	page := string(html)
	for _, needle := range []string{
		`id="runs-panel"`, `class="runs-panel"`, `class="panel-header"`, `id="resizer"`,
	} {
		if strings.Contains(page, needle) {
			t.Errorf("index.html still has %q - the left panel and its divider "+
				"should be gone (TOR-168)", needle)
		}
	}

	// What replaced them: the title and the status element move INTO the
	// intake, ahead of the input row (the project owner's call, TOR-168's own
	// handoff note) - #status keeps its id, since app.js still reaches it by
	// id as the page's only live-connection indicator.
	intakeIdx := strings.Index(page, `id="intake"`)
	h1Idx := strings.Index(page, "<h1>torpeek</h1>")
	statusIdx := strings.Index(page, `id="status"`)
	formIdx := strings.Index(page, `id="start"`)
	if intakeIdx < 0 || h1Idx < 0 || statusIdx < 0 || formIdx < 0 {
		t.Fatalf("index.html is missing one of #intake, <h1>, #status or #start "+
			"entirely (intake=%d h1=%d status=%d form=%d)", intakeIdx, h1Idx, statusIdx, formIdx)
	}
	if !(intakeIdx < h1Idx && h1Idx < formIdx) {
		t.Errorf("<h1>torpeek</h1> is not between #intake and the #start form - "+
			"it belongs inside the intake, before the input row (intake=%d h1=%d form=%d)",
			intakeIdx, h1Idx, formIdx)
	}
	if !(intakeIdx < statusIdx && statusIdx < formIdx) {
		t.Errorf("#status is not between #intake and the #start form - it belongs "+
			"inside the intake, before the input row, and still reachable by id "+
			"(intake=%d status=%d form=%d)", intakeIdx, statusIdx, formIdx)
	}
}
