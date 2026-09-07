package web

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
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
//   - .lightbox-view                        the full-size panel's clipped
//     window (TOR-172), added here because
//     it is the one selector in that block
//     whose absence is not cosmetic: it is
//     what clips the picture to the panel
//     (rule 4) and what app.js measures to
//     decide the fit, so losing it loses
//     both halves at once
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
		".lightbox-view",
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

// TestDropzoneFrameIsHardCorneredAndUsesEdge is TOR-173's guard. .dropzone
// used to be `border: 1px dashed var(--rule)` (1.36 against --s1, no edge at
// all by the 3.0 floor --edge's own comment sets) with rounded corners - the
// opposite of both decisions that ticket makes: the dash implied a narrower
// drop target than the truth (the handler is on `document`, with a
// page-wide #drop-overlay doing that job instead), and the reference sheet's
// corners are hard, with brackets set just inside them, reusing
// .run-detail::before's own corner-bracket technique rather than a
// rectangle. Neither may spend --accent: app.css reserves that for "this is
// live or this is where you are", and a permanently accented resting frame
// would be exactly the furniture-spending that rule exists to prevent.
func TestDropzoneFrameIsHardCorneredAndUsesEdge(t *testing.T) {
	css := stylesheet(t)

	dz := block(t, css, ".dropzone {")
	if v, ok := dz["border-radius"]; ok && v != "0" {
		t.Errorf(".dropzone declares border-radius: %q - TOR-173 wants hard corners, "+
			"matching the reference sheet's vocabulary, not the old rounded box", v)
	}
	if v, ok := dz["border"]; ok {
		t.Errorf(".dropzone still declares its own border (%q) - TOR-173 replaces the "+
			"dashed --rule box with corner brackets drawn by ::before/::after, not a "+
			"rectangle border", v)
	}

	// Both selectors are searched with a leading "\n": .dropzone::after {" is
	// also, verbatim, the tail of the combined ".dropzone::before,
	// .dropzone::after {" rule just above (the one that only sets content/
	// position/width/height), so a bare substring search finds that one
	// first and silently reads the wrong block. Anchoring on the newline
	// that starts the dedicated single-selector rule's own line is what
	// finds the one that actually declares the border colours.
	for _, rawSel := range []string{"\n.dropzone::before {", "\n.dropzone::after {"} {
		sel := strings.TrimSpace(rawSel)
		b := block(t, css, rawSel)
		sawEdge := false
		for prop, val := range b {
			if !strings.Contains(prop, "border") {
				continue
			}
			if strings.Contains(val, "var(--edge)") {
				sawEdge = true
			}
			if strings.Contains(val, "var(--accent") {
				t.Errorf("%s %s is %q - a resting frame must not spend --accent; focus "+
					"is where TOR-159 already put it", sel, prop, val)
			}
		}
		if !sawEdge {
			t.Errorf("%s draws no var(--edge) border - TOR-173 takes --edge, the token "+
				"TOR-159 measured for exactly this job", sel)
		}
		if _, ok := b["filter"]; ok {
			t.Errorf("%s declares filter (a glow) - that glow is accent-tinted "+
				"(--stroke-glow) and this is a resting frame, not a live one", sel)
		}
		if _, ok := b["box-shadow"]; ok {
			t.Errorf("%s declares box-shadow - box-shadow paints a glowing rectangle "+
				"over a stroke-drawn shape (see .run-detail::before's own comment); this "+
				"frame is stroke-drawn corners, not a box", sel)
		}
	}

	// The contrast claim itself, computed rather than trusted: .dropzone has
	// no background of its own, so its real ground is --bg, painted by the
	// sticky .intake it sits inside.
	root := block(t, css, ":root {")
	edge := hexToken(t, root, "--edge")
	bg := hexToken(t, root, "--bg")
	if ratio := contrastRatio(edge, bg); ratio < 3.0 {
		t.Errorf("--edge (%s) against --bg (%s), .dropzone's own ground, is %.2f:1, "+
			"want >= 3.0 (the floor for a UI part to be distinguishable at all)",
			edge, bg, ratio)
	}
}

// ---- TOR-172: the frame's full-size panel, its chrome, and its clamp. ----
//
// Everything below belongs to one ticket and would sit better in a
// lightbox_test.go of its own; it is here because this file is the only test
// file that ticket owns, and splitting a ticket's guards across a file it was
// not given would be worse than the misfiled name.
//
// What these can and cannot do. The CSS half is real: it reads the rules that
// ship and computes the contrast claims rather than restating them. The
// app.js half reads the SERVED SCRIPT AS TEXT, because this repository has no
// JS runner (see columns_test.go's own note on that precedent) - so it can
// catch a rule deleted, a function renamed and an accent spent where it was
// forbidden, and it cannot catch a wrong answer. Nothing here proves the
// clamp holds at an edge; only a browser can, and TOR-172's own report says
// what a browser showed.

// jsFunc returns the source of the named top-level function, up to its
// closing brace at column zero - the same shape block() uses for a CSS rule,
// and it relies on the same convention: nothing in app.js is indented at
// column zero inside a function body.
func jsFunc(t *testing.T, js, name string) string {
	t.Helper()
	head := "function " + name + "("
	i := strings.Index(js, head)
	if i < 0 {
		t.Fatalf("app.js has no %s function", head+")")
	}
	end := strings.Index(js[i:], "\n}\n")
	if end < 0 {
		t.Fatalf("app.js's %s is never closed", name)
	}
	return js[i : i+end]
}

// jsListener returns the source of the first listener registered with the
// given prefix, up to the "});" that closes the call.
func jsListener(t *testing.T, js, prefix string) string {
	t.Helper()
	i := strings.Index(js, prefix)
	if i < 0 {
		t.Fatalf("app.js registers no listener matching %q", prefix)
	}
	end := strings.Index(js[i:], "\n});")
	if end < 0 {
		t.Fatalf("app.js's %q listener is never closed", prefix)
	}
	return js[i : i+end]
}

// rgbaOverWhite composites an rgba() token over pure white and returns the
// result as a hex literal contrastRatio can score. White is the worst ground
// a video frame can hand a control: TOR-122's near-invisible close button was
// on the bright top edge of a snow shot.
func rgbaOverWhite(t *testing.T, root map[string]string, name string) string {
	t.Helper()
	v, ok := root[name]
	if !ok {
		t.Fatalf(":root declares no %s", name)
	}
	m := regexp.MustCompile(`^rgba\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*,\s*(\.?\d+(?:\.\d+)?)\s*\)$`).
		FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		t.Fatalf("%s is %q, want an rgba(r, g, b, a) literal so the composite can be computed", name, v)
	}
	alpha, err := strconv.ParseFloat("0"+strings.TrimPrefix(m[4], "0"), 64)
	if err != nil {
		t.Fatalf("%s has an alpha this cannot read (%q): %v", name, m[4], err)
	}
	out := "#"
	for _, ch := range m[1:4] {
		c, _ := strconv.ParseFloat(ch, 64)
		over := alpha*c + (1-alpha)*255
		out += fmt.Sprintf("%02X", int(math.Round(over)))
	}
	return out
}

// TestLightboxChromeIsHudWithAccentBracketsChipAndHint is TOR-176's palette
// decision, made checkable, replacing TOR-172's original (TestLightbox
// ChromeIsHudAndKeepsTheAccentForTheZoomState, before this ticket): the owner
// looked at the shipped panel next to the HUD reference they gave and said it
// was "вообще не похож на референс" - not restrained, wrong. Three
// treatments were built side by side on a real frame with the live tokens;
// VARIANT B, WITH VARIANT A'S HINT COLOUR, is what this test guards.
//
// The accent moves from one place (the zoom state alone) to three: the three
// corner brackets, now permanently loud (34px at 2px, glowing) rather than
// gated on the zoom state; the state pill, unchanged from TOR-172; and the
// key-hint line, which is variant A's own contribution and the owner's
// specific request. The chamfer stroke, the FRAME label tab and the tick run
// are deliberately left off that list - the panel does not become
// accent-coloured throughout.
func TestLightboxChromeIsHudWithAccentBracketsChipAndHint(t *testing.T) {
	css := stylesheet(t)

	panel := block(t, css, ".lightbox {")
	if v, ok := panel["border-radius"]; !ok || v != "0" {
		t.Errorf(".lightbox border-radius is %q (present: %v) - the reference sheet has no radii "+
			"anywhere, and this panel used to be border-radius: .5rem", v, ok)
	}
	if v, ok := panel["clip-path"]; !ok || !strings.Contains(v, "polygon(") {
		t.Errorf(".lightbox clip-path is %q (present: %v) - the notched corner is one of the "+
			"motifs the acceptance criteria names, and a polygon() is what cuts it", v, ok)
	}

	// The three brackets: loud now, not restrained. 34px at 2px, in --accent,
	// with a permanent stroke glow - unconditional, no longer keyed on the
	// zoom state the way TOR-172 had it.
	dims := block(t, css, ".lightbox::before, .lightbox::after, .lightbox-marks::before {")
	for _, dim := range []string{"width", "height"} {
		if v := dims[dim]; v != "34px" {
			t.Errorf(".lightbox::before/::after/.lightbox-marks::before %s is %q, want 34px - "+
				"variant B's brackets are a quarter of the panel's own edge, not TOR-172's "+
				"restrained 12px", dim, v)
		}
	}
	for _, rawSel := range []string{
		"\n.lightbox::before {",
		"\n.lightbox::after {",
		"\n.lightbox-marks::before {",
	} {
		sel := strings.TrimSpace(rawSel)
		b := block(t, css, rawSel)
		sawAccent := false
		for prop, val := range b {
			if !strings.Contains(prop, "border") {
				continue
			}
			if strings.Contains(val, "var(--edge)") {
				t.Errorf("%s %s is %q - TOR-176 moves the brackets off --edge onto --accent; "+
					"the resting chrome is loud now, not the restrained TOR-172 original", sel, prop, val)
			}
			if strings.Contains(val, "var(--accent)") {
				sawAccent = true
			}
			if !strings.HasPrefix(val, "2px") {
				t.Errorf("%s %s is %q, want a 2px border - variant B doubles TOR-172's 1px "+
					"stroke, part of what makes the chrome loud rather than a hairline", sel, prop, val)
			}
		}
		if !sawAccent {
			t.Errorf("%s draws no border in var(--accent) - variant B's brackets are accent, "+
				"not --edge, and not gated on the zoom state any more", sel)
		}
		if v, ok := b["box-shadow"]; ok {
			t.Errorf("%s declares box-shadow: %q - box-shadow follows an element's BOX and "+
				"paints a glowing rectangle over a shape drawn from strokes; filter: "+
				"var(--stroke-glow) is the one that follows the painted pixels "+
				"(.run-detail::before's own comment)", sel, v)
		}
		if v, ok := b["filter"]; !ok || !strings.Contains(v, "var(--stroke-glow)") {
			t.Errorf("%s filter is %q (present: %v), want var(--stroke-glow) - the brackets glow "+
				"permanently under variant B, not only at 100%%", sel, v, ok)
		}
	}

	// The chamfer stroke stays off the accent list on purpose - TOR-176
	// spends --accent on exactly three things (the brackets, the chip, the
	// hint line) and the chamfer is not one of them, same as the label and
	// the ticks.
	chamfer := block(t, css, "\n.lightbox-marks::after {")
	if v, ok := chamfer["background"]; !ok || !strings.Contains(v, "var(--edge)") {
		t.Errorf(".lightbox-marks::after background is %q (present: %v), want var(--edge) - "+
			"the chamfer was left off variant B's accent list on purpose", v, ok)
	}
	if v, ok := chamfer["background"]; ok && strings.Contains(v, "var(--accent") {
		t.Errorf(".lightbox-marks::after background is %q - the panel does not become "+
			"accent-coloured throughout; only the brackets, the chip and the hint line do", v)
	}

	// The zoom-state chip: unchanged from TOR-172, and now the only chrome
	// still keyed on data-zoom - the brackets used to share this rule and no
	// longer do.
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
	lit := regexp.MustCompile(`\.lightbox\[data-zoom="full"\][^{]*\{[^}]*\}`)
	states := lit.FindAllString(live, -1)
	if len(states) == 0 {
		t.Fatal(`app.css has no .lightbox[data-zoom="full"] rule - the state pill has nowhere ` +
			"to spend its accent, and the panel cannot look different at 100% than at fit")
	}
	all := strings.Join(states, "\n")
	for _, want := range []string{"var(--accent)", "var(--glow)"} {
		if !strings.Contains(all, want) {
			t.Errorf(`no .lightbox[data-zoom="full"] rule reaches for %s - the state pill is a `+
				"filled box, so --glow is the one it wants", want)
		}
	}
	if strings.Contains(all, "::before") || strings.Contains(all, "::after") {
		t.Error(`a .lightbox[data-zoom="full"] rule still targets a bracket pseudo-element - ` +
			"variant B makes the brackets unconditionally accent, so nothing should key their " +
			"colour off the zoom state any more")
	}

	// The key-hint line: variant A's own contribution, at full strength.
	// --accent on --s1 measures 11.60:1, so nothing needs weakening, and
	// opacity multiplying against a token is exactly what TOR-158 removed
	// from this file (TestLightboxControlsOverThePictureCarryTheirOwnGround
	// guards the same trap on .lightbox-close/.lightbox-caption).
	keys := block(t, css, ".lightbox-keys {")
	if v := keys["color"]; v != "var(--accent)" {
		t.Errorf(".lightbox-keys color is %q, want var(--accent) - the owner's specific "+
			"request was variant A's hint colour, carried into variant B", v)
	}
	if v, ok := keys["opacity"]; ok {
		t.Errorf(".lightbox-keys declares opacity: %q - full-strength accent needs no "+
			"weakening, and this is the exact opacity-over-a-token shape TOR-158 removed "+
			"from this file", v)
	}
	root := block(t, css, ":root {")
	accent := hexToken(t, root, "--accent")
	s1 := hexToken(t, root, "--s1")
	if ratio := contrastRatio(accent, s1); ratio < 4.5 {
		t.Errorf("--accent (%s) over --s1 (%s), .lightbox-keys' own ground, is %.2f:1, "+
			"want >= 4.5 - the hint line is text, so it answers to the 4.5 floor, not the "+
			"3.0 one a UI part like a border gets", accent, s1, ratio)
	}

	// The cursor is the zoom state's other half, and all three states need
	// one: zoom-in at fit, zoom-out at 100%, and neither on a picture that
	// already fits, where a click has nothing to do. Untouched by this
	// ticket, still required.
	for _, want := range []string{`[data-zoom="fit"]`, `[data-zoom="full"]`, `[data-zoom="none"]`} {
		re := regexp.MustCompile(`\.lightbox` + regexp.QuoteMeta(want) + ` \.lightbox-view \{[^}]*cursor:`)
		if !re.MatchString(live) {
			t.Errorf("no cursor declared for .lightbox%s .lightbox-view - the zoom state has to "+
				"be legible from the cursor, in every state it can be in", want)
		}
	}
}

// TestLightboxControlsOverThePictureCarryTheirOwnGround computes TOR-122's
// finding rather than restating it. That ticket measured that no single
// colour is legible against every frame - the close button sat near-invisible
// on the bright top edge of a snow shot - and the answer was a scrim under
// the control. TOR-172 keeps both controls over the picture (the close at its
// corner, the caption naming what you are looking at) and makes it harder,
// because the picture can now pan beneath them. So the scrim has to hold
// against the worst ground a frame can be: pure white.
func TestLightboxControlsOverThePictureCarryTheirOwnGround(t *testing.T) {
	css := stylesheet(t)
	root := block(t, css, ":root {")
	ink := hexToken(t, root, "--ink")

	for _, c := range []struct {
		sel, scrim string
	}{
		{".lightbox-close {", "--scrim"},
		{".lightbox-caption {", "--scrim-strong"},
	} {
		b := block(t, css, c.sel)
		got := b["background"]
		if !strings.Contains(got, "var("+c.scrim+")") {
			t.Errorf("%s background is %q, want var(%s) - it is laid over arbitrary picture "+
				"content, and TOR-122 measured that no flat colour is legible on every frame",
				strings.TrimSuffix(c.sel, " {"), got, c.scrim)
		}
		if v, ok := b["opacity"]; ok {
			t.Errorf("%s declares opacity: %q on top of its own colour - opacity multiplies "+
				"against whatever the token already measures, and the pair reads dimmer than "+
				"either (.compare-keys' own comment, TOR-158). This rule used to carry "+
				"opacity: .8", strings.TrimSuffix(c.sel, " {"), v)
		}
		ground := rgbaOverWhite(t, root, c.scrim)
		if ratio := contrastRatio(ink, ground); ratio < 4.5 {
			t.Errorf("--ink (%s) over %s composited on a blown-out white frame (%s) is %.2f:1, "+
				"want >= 4.5 - which is the whole reason the control carries a ground at all",
				ink, c.scrim, ground, ratio)
		}
	}
}

// TestLightboxViewIsTheClippedWindowBoundedByThePanelsOwnTokens is rule 4's
// structural half and the one place the panel's arithmetic lives.
//
// overflow: hidden is what makes "the picture must never spill outside the
// popup" true whatever app.js computes - it cannot be got wrong by a bad
// clamp, only by deleting this declaration. And the maximum the picture may
// be drawn at is stated here, in terms of the panel's own padding, strips and
// gaps, because app.js reads the resolved number back off the element: a
// maximum written as a bare 88vw (which is what .lightbox img carried before
// this ticket) would drift out of step with the panel around it the first
// time the padding changed.
func TestLightboxViewIsTheClippedWindowBoundedByThePanelsOwnTokens(t *testing.T) {
	css := stylesheet(t)

	view := block(t, css, ".lightbox-view {")
	if got := view["overflow"]; got != "hidden" {
		t.Errorf(".lightbox-view overflow is %q, want hidden - it is what makes rule 4 "+
			"structural rather than a promise about app.js's arithmetic", got)
	}
	panel := block(t, css, ".lightbox {")
	for _, axis := range []struct{ prop, viewport string }{
		{"max-width", "92vw"},
		{"max-height", "92vh"},
	} {
		got, ok := view[axis.prop]
		if !ok {
			t.Errorf(".lightbox-view declares no %s - app.js measures this to decide the fit, "+
				"so with no ceiling the panel would size itself to the picture's own pixels",
				axis.prop)
			continue
		}
		if !strings.Contains(got, axis.viewport) {
			t.Errorf(".lightbox-view %s is %q, want it bounded by %s like the panel itself",
				axis.prop, got, axis.viewport)
		}
		// Every token the panel declares for its own chrome on this axis has
		// to be subtracted here, or the panel overflows its own max and
		// drops a label strip onto the backdrop.
		for _, tok := range []string{"--lb-pad", "--lb-bar", "--lb-keys", "--lb-gap"} {
			if _, declared := panel[tok]; !declared {
				t.Errorf(".lightbox declares no %s - .lightbox-view's %s subtracts it, and "+
					"app.js reads the result off the element", tok, axis.prop)
				continue
			}
			if axis.prop == "max-width" && tok != "--lb-pad" {
				continue // only the ring costs width; the strips are full-width rows
			}
			if !strings.Contains(got, "var("+tok+")") {
				t.Errorf(".lightbox-view %s is %q and never subtracts var(%s) - the panel's "+
					"own arithmetic and this ceiling have to be the same arithmetic",
					axis.prop, got, tok)
			}
		}
	}
	// The old ceiling, gone: it capped the picture at 88vw/80vh, which at
	// 100% would silently crop a 2048-wide frame down to something else.
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
	if regexp.MustCompile(`\.lightbox img \{[^}]*max-width:\s*88vw`).MatchString(live) {
		t.Error(".lightbox img still carries max-width: 88vw - the picture's drawn size is " +
			"computed now (app.js), and a leftover viewport cap would quietly override the " +
			"100% state")
	}
}

// TestLightboxScalingAndPanAreWiredInTheServedScript reads app.js as served
// text - there is no JS runner here (columns_test.go's note explains the
// precedent) - so what it guards is that each of the four rules still has
// code answering for it, and that the two things easiest to lose in an edit
// are still there: the clamp's far bound, and the arrow keys' preventDefault.
//
// It cannot tell whether the clamp is CORRECT. Only a browser can, by panning
// to each edge and looking for a gutter.
func TestLightboxScalingAndPanAreWiredInTheServedScript(t *testing.T) {
	js := appJS(t)
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(js, "")
	live = regexp.MustCompile(`(?m)//[^\n]*`).ReplaceAllString(live, "")

	// RULES 1 AND 2: one expression, and the 1 is the whole of "never
	// upscale". Without it a 320-wide frame is blown up to fill the panel.
	layout := jsFunc(t, live, "layoutLightbox")
	if !strings.Contains(layout, "Math.min(1, avail.w / lb.natW, avail.h / lb.natH)") {
		t.Error("layoutLightbox no longer caps the fit factor at 1 alongside the two axis " +
			"ratios - the cap is rule 1 (a picture that fits is drawn at 100%, and a small " +
			"frame is never upscaled) and the ratios are rule 2")
	}
	if !strings.Contains(layout, "clampPan();") {
		t.Error("layoutLightbox does not re-clamp the pan - a window resize changes the " +
			"window's size, and an offset that was legal at the old size shows a gutter at " +
			"the new one")
	}

	// RULE 4's second half. Both bounds, on both axes, or an edge leaks.
	clamp := jsFunc(t, live, "clampPan")
	for _, want := range []string{
		"Math.min(0, lb.boxW - lb.drawW)",
		"Math.min(0, lb.boxH - lb.drawH)",
		"lb.x = Math.min(0, Math.max(minX, lb.x));",
		"lb.y = Math.min(0, Math.max(minY, lb.y));",
	} {
		if !strings.Contains(clamp, want) {
			t.Errorf("clampPan does not contain %q - the pan offset has to be held to "+
				"[box - drawn, 0] on BOTH axes, and the Math.min(0, ...) on the far bound is "+
				"what stops a picture drawn narrower than the window from panning positive "+
				"and opening a gutter down the left edge", want)
		}
	}

	// Nothing may move the picture without coming through the clamp.
	for _, fn := range []string{"panFromPointer", "panBySteps", "toggleLightboxZoom"} {
		body := jsFunc(t, live, fn)
		if !strings.Contains(body, "clampPan()") && !strings.Contains(body, "layoutLightbox()") {
			t.Errorf("%s writes the pan without going through clampPan() - rule 4 has one "+
				"enforcement point on purpose", fn)
		}
	}

	// RULE 3, both halves: the mouse's position maps to the offset, and the
	// arrow keys step it.
	if !strings.Contains(live, `el.lightboxView.addEventListener("pointermove", panFromPointer)`) {
		t.Error(`app.js does not map pointermove to the pan - "moving the mouse" pans the ` +
			"picture, with no button held, which is what the rules asked for")
	}
	if !strings.Contains(live, `el.lightboxImg.addEventListener("click", toggleLightboxZoom)`) {
		t.Error("app.js does not zoom on a click on the picture - and it has to be the " +
			"picture, not the panel: the close button and the caption sit over it, and a " +
			"handler on the panel would turn a click aimed at either into a zoom")
	}
	keys := jsListener(t, live, `el.lightbox.addEventListener("keydown"`)
	if !strings.Contains(keys, "LIGHTBOX_ARROWS[event.key]") {
		t.Error("the lightbox's keydown handler no longer reads LIGHTBOX_ARROWS - the arrow " +
			"keys are how this pans without a mouse")
	}
	if !strings.Contains(keys, "event.preventDefault();") {
		t.Error("the lightbox's keydown handler does not preventDefault - a modal <dialog> " +
			"does NOT stop the document behind it from scrolling, so an arrow key it " +
			"declines scrolls the page under the backdrop")
	}
	if strings.Contains(keys, "Escape") {
		t.Error("the lightbox's keydown handler mentions Escape - the dialog closes itself " +
			"on Escape for free, and a handler that touches it is how that gets lost")
	}
	if !strings.Contains(live, `el.lightboxZoom.textContent`) {
		t.Error("app.js never writes el.lightboxZoom.textContent - the readout is where the " +
			"zoom state is said in words, next to the cursor that says it in shape")
	}
	if !strings.Contains(live, "dataset.zoom") {
		t.Error("app.js never sets the panel's dataset.zoom - every chrome rule that changes " +
			"with the zoom is keyed on that attribute")
	}

	// The panel's arithmetic has ONE home, and it is app.css: availableBox
	// asks the browser what the stylesheet's own maximum came to rather than
	// keeping a second copy of the numbers here.
	avail := jsFunc(t, live, "availableBox")
	if !strings.Contains(avail, "getBoundingClientRect()") {
		t.Error("availableBox no longer measures the element - it exists so the panel's " +
			"padding and strips are stated once, in app.css, instead of twice")
	}
	if strings.Contains(live, "innerWidth") || strings.Contains(live, "innerHeight") {
		t.Error("app.js computes the viewport itself - that is the copy of app.css's " +
			"arithmetic availableBox exists to avoid, and the two drift apart silently")
	}
}

// TestLightboxMarkupHoldsTheWindowAndItsChrome guards the shape app.css and
// app.js both assume: the clipped window exists, the picture and both
// controls laid over it are INSIDE it (so the controls stay put while the
// picture pans beneath them, and the clip catches everything), and the label
// tab with its zoom readout is above it in the panel's own strip.
func TestLightboxMarkupHoldsTheWindowAndItsChrome(t *testing.T) {
	html, err := embedded.ReadFile("assets/index.html")
	if err != nil {
		t.Fatalf("reading the embedded index.html: %v", err)
	}
	page := string(html)

	for _, needle := range []string{
		`id="lightbox-view"`,
		`class="lightbox-view"`,
		`id="lightbox-zoom"`,
		`class="lightbox-tab"`,
		`class="lightbox-marks"`,
		`class="lightbox-keys"`,
	} {
		if !strings.Contains(page, needle) {
			t.Errorf("index.html has no %s - app.css styles it and app.js reaches for it", needle)
		}
	}

	view := strings.Index(page, `id="lightbox-view"`)
	if view < 0 {
		t.Fatal("index.html has no #lightbox-view to check the order of")
	}
	end := strings.Index(page[view:], "</div>")
	if end < 0 {
		t.Fatal("#lightbox-view is never closed")
	}
	inside := page[view : view+end]
	for _, needle := range []string{`id="lightbox-img"`, `id="lightbox-close"`, `id="lightbox-caption"`} {
		if !strings.Contains(inside, needle) {
			t.Errorf("%s is not inside #lightbox-view - the window is what clips the picture "+
				"and what the controls laid over it are positioned against; outside it they "+
				"drift as the panel resizes", needle)
		}
	}
	// The tab rides the panel's top strip, above the window - on the panel's
	// own ground, where its contrast is a number app.css can be held to,
	// rather than over footage where nothing can be.
	if tab := strings.Index(page, `class="lightbox-tab"`); tab < 0 || tab > view {
		t.Errorf("the label tab is not above #lightbox-view in the markup (tab=%d view=%d) - "+
			"it belongs in the panel's top strip, off the picture", tab, view)
	}
}

// ---- TOR-169: Save .torrent moves up to sit level with the name. ----

// TestSaveTorrentSitsInTheHeaderBesideCancel guards the move itself. Save
// .torrent used to live two blocks below the header, inside .torrent-actions,
// after .torrent-summary. It now belongs in the header's own
// .run-detail-header-actions group, ahead of Cancel - and .torrent-actions
// must no longer contain the save link at all, or the control would render
// twice.
//
// This reads app.js as served text, the same way
// TestLightboxScalingAndPanAreWiredInTheServedScript does: there is no JS
// runner here, so it cannot build the template and inspect the DOM, only
// confirm the markup and its order are what TOR-169 asked for.
func TestSaveTorrentSitsInTheHeaderBesideCancel(t *testing.T) {
	js := appJS(t)
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(js, "")
	live = regexp.MustCompile(`(?m)//[^\n]*`).ReplaceAllString(live, "")

	header := strings.Index(live, `'<header class="run-detail-header">'`)
	if header < 0 {
		t.Fatal("app.js has no .run-detail-header template to check")
	}
	headerEnd := strings.Index(live[header:], `"</header>"`)
	if headerEnd < 0 {
		t.Fatal("the .run-detail-header template is never closed")
	}
	headerBlock := live[header : header+headerEnd]

	actionsIdx := strings.Index(headerBlock, `class="run-detail-header-actions"`)
	saveIdx := strings.Index(headerBlock, `class="torrent-save"`)
	cancelIdx := strings.Index(headerBlock, `class="run-detail-cancel"`)
	if actionsIdx < 0 {
		t.Fatal("the header template has no .run-detail-header-actions group - Save .torrent and " +
			"Cancel each need a fixed slot in it, not their own separate pinning")
	}
	if saveIdx < 0 || cancelIdx < 0 {
		t.Fatalf("the header template is missing torrent-save (index %d) or run-detail-cancel "+
			"(index %d)", saveIdx, cancelIdx)
	}
	if !(actionsIdx < saveIdx && saveIdx < cancelIdx) {
		t.Errorf("the header's controls are not in the order .run-detail-header-actions, "+
			"torrent-save, run-detail-cancel (actions=%d save=%d cancel=%d) - Cancel has to come "+
			"last so it stays flush against the header's own right edge regardless of Save",
			actionsIdx, saveIdx, cancelIdx)
	}
	if !strings.Contains(headerBlock, `class="run-detail-cancel" type="button" data-idle>`) {
		t.Error(`Cancel's template markup does not start with data-idle - it must not start ` +
			`hidden via the [hidden] attribute, or its box leaves the flow and Save slides over ` +
			`to take its place the moment a run is not cancellable`)
	}

	actionsBlockStart := strings.Index(live, `'<p class="torrent-actions" hidden>'`)
	if actionsBlockStart < 0 {
		t.Fatal("app.js has no .torrent-actions template to check")
	}
	actionsBlockEnd := strings.Index(live[actionsBlockStart:], `'</p>'`)
	if actionsBlockEnd < 0 {
		t.Fatal("the .torrent-actions template is never closed")
	}
	actionsBlock := live[actionsBlockStart : actionsBlockStart+actionsBlockEnd]
	if strings.Contains(actionsBlock, "torrent-save") {
		t.Error(".torrent-actions still contains torrent-save - Save .torrent moved into the " +
			"header (TOR-169) and must not also render here, or the same control shows twice")
	}
	if !strings.Contains(actionsBlock, "torrent-send") {
		t.Error(".torrent-actions lost torrent-send along with torrent-save - only the save " +
			"link was meant to move, not the whole group")
	}
}

// TestSaveAndCancelStayPinnedRegardlessOfEachOther is TOR-169's CSS half.
// Cancel is reserved rather than removed when it does not apply, so Save's
// own position beside it never depends on whether a cancellable run is what
// is currently showing (see .run-detail-cancel[data-idle]'s own comment),
// and the whole group tracks the table's visible scrollport rather than the
// row's full width, which .run-table-wrap's overflow-x: auto (TOR-157) can
// make wider than the pane.
func TestSaveAndCancelStayPinnedRegardlessOfEachOther(t *testing.T) {
	css := stylesheet(t)
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")

	actions := block(t, css, ".run-detail-header-actions {")
	if got := actions["margin-left"]; got != "auto" {
		t.Errorf(".run-detail-header-actions margin-left is %q, want auto - it has to sit flush "+
			"against the header's own right edge when there is nothing to scroll", got)
	}
	if got := actions["position"]; got != "sticky" {
		t.Errorf(".run-detail-header-actions position is %q, want sticky - margin-left: auto alone "+
			"pins the group to the ROW's own right edge, which .run-table-wrap's overflow-x: auto "+
			"can put off screen; sticky is what keeps it on the visible pane instead, the same "+
			"primitive .run-table thead th already uses on the vertical axis", got)
	}
	if got := actions["right"]; got != "0" {
		t.Errorf(".run-detail-header-actions right is %q, want 0 - the edge sticky measures the "+
			"scrollport against", got)
	}

	if !regexp.MustCompile(`(?s)\.run-detail-cancel\[data-idle\]\s*\{[^}]*visibility:\s*hidden`).MatchString(live) {
		t.Error("no .run-detail-cancel[data-idle] { visibility: hidden } rule - an idle Cancel " +
			"has to keep its box in the flow (visibility, not [hidden]'s display: none) or Save " +
			"slides sideways into the space it leaves the moment Cancel stops applying")
	}
	if regexp.MustCompile(`(?s)\.run-detail-cancel\[data-idle\]\s*\{[^}]*display:\s*none`).MatchString(live) {
		t.Error(".run-detail-cancel[data-idle] sets display: none - that removes Cancel from the " +
			"flow, which is exactly what reserving its box with visibility was meant to avoid")
	}
}
