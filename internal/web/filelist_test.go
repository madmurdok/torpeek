package web

import (
	"regexp"
	"strings"
	"testing"
)

// TOR-180: a row expands onto the LIST OF FILES the torrent holds - every
// file, not only the videos - and the non-video ones are shown but cannot be
// ticked.
//
// These read the script and the stylesheet as served, which is the same
// boundary rundetailwidth_test.go's own comment draws for its CSS/JS pair:
// this repository ships no JS runner (see columns_test.go's note), so a text
// check can catch the plumbing deleted, renamed or silently regated by a
// later refactor, and it cannot catch a wrong pixel or a wrong row in a real
// layout. A real browser is what TOR-180's own report used for that, on a
// torrent holding one video, one sample and three non-video files.

// cssRule returns one rule's declarations, from its selector to the next
// closing brace. block() in theme_test.go cannot be used here: it bounds a
// rule at a brace in COLUMN ZERO, which only exists for a multi-line rule,
// and several of the rules below are deliberately one-liners.
func cssRule(t *testing.T, css, sel string) string {
	t.Helper()
	i := strings.Index(css, sel)
	if i < 0 {
		t.Fatalf("app.css has no %q rule", sel)
	}
	rest := css[i+len(sel):]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("the %q rule is never closed", sel)
	}
	return rest[:end]
}

func servedScript(t *testing.T) string {
	t.Helper()
	b, err := embedded.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("reading the embedded app.js: %v", err)
	}
	return string(b)
}

// TestTheFileListIsTheRowsContentNotAState is the shape of the whole ticket.
// Before it, one line in syncEntry read `state === "needs-action"` and that
// alone decided whether the list was on screen - so a running, finished or
// reopened torrent showed no file list at all, and the list was a state a run
// happened to be in rather than what the row contains.
//
// What replaced it must be keyed off HAVING a list, not off being in a state:
// a queued row (no metadata fetched yet) and a disk row whose replay has not
// arrived genuinely have nothing to list, and that is the only reason the
// section may be hidden.
func TestTheFileListIsTheRowsContentNotAState(t *testing.T) {
	js := servedScript(t)
	fn := jsFunc(t, js, "syncFileList")

	// The one statement that decides whether the section is on screen.
	hidden := regexp.MustCompile(`entry\.pickerEl\.hidden = ([^;]+);`).FindStringSubmatch(fn)
	if hidden == nil {
		t.Fatal("syncFileList never sets entry.pickerEl.hidden - it is the only function " +
			"that may put the file list on screen or take it off")
	}
	if strings.Contains(hidden[1], "state") {
		t.Errorf("the file list is still gated on the run's state: %q. TOR-180's whole "+
			"point is that the list is the row's content in EVERY state, so the only "+
			"thing that may hide it is having nothing to list", hidden[1])
	}
	if !strings.Contains(hidden[1], "fileList") {
		t.Errorf("the file list's visibility is decided by %q, which does not read the "+
			"file list itself - having nothing to list is the one honest reason to "+
			"hide it", hidden[1])
	}

	// And the gating that DID survive must be about the frame, not the list:
	// a --warn border says "nothing happens until you act", which is only
	// true of a parked torrent.
	if !strings.Contains(fn, `entry.state === "needs-action"`) {
		t.Error("syncFileList never asks whether the row is parked - the amber frame " +
			"belongs to the one state where nothing at all happens until somebody acts")
	}
	// TOR-181 rewrote what this used to check. Select none and the foot's
	// "Take frames" are gone with the staged selection they served (see
	// TestTheButtonUnderTheFileListIsGone), and whether a TICK works is no
	// longer read off the state here at all - it is the server's own verdict,
	// carried on run_state (entry.tickable, runEntry.refuseTick). What is
	// still parked-only is Select all, and it says so in its own function.
	all := jsFunc(t, js, "syncSelectAll")
	if !strings.Contains(all, `entry.state === "needs-action"`) ||
		!strings.Contains(all, "entry.pickerAll.hidden") {
		t.Error("syncSelectAll does not gate Select all on the parked state - \"all of " +
			"it\" answers a torrent whose whole list is undecided, and on a row already " +
			"fetching it would undo, in one press, the choice not to take the rest")
	}
	if strings.Contains(fn, "entry.tickable = ") {
		t.Error("syncFileList decides tickability for itself - it is the server's answer " +
			"(refuseTick, run_state's \"tickable\"), and a second copy of that rule is " +
			"how the page comes to offer a live box the server then refuses")
	}
}

// TestNoRunEntryFieldIsDeclaredTwice exists because this ticket walked into
// exactly that and got away with it for a while. The run entry is one large
// object literal, and a duplicate key there is not an error in JavaScript -
// the LAST one silently wins. TOR-180 added a `files` list beside the
// `files: 0` count GET /runs already puts on every row (RunSummary.Files,
// read by badgeState and metaLabel), so the badge's completeness arithmetic
// started reading an array's length as a count, and nothing said a word.
//
// It was caught by a mutation run, not by reading the diff, which is what
// makes it worth a standing check rather than a lesson: the entry gains
// fields in most tickets that touch this file, and every one of them is a
// chance to shadow a field declared two hundred lines away.
func TestNoRunEntryFieldIsDeclaredTwice(t *testing.T) {
	js := servedScript(t)

	start := strings.Index(js, "const entry = {")
	if start < 0 {
		t.Fatal("app.js no longer builds a `const entry = {` literal - this check is " +
			"anchored on it")
	}
	end := strings.Index(js[start:], "\n  };")
	if end < 0 {
		t.Fatal("the run entry literal is never closed")
	}
	// Comments first: several of them name other fields in prose, including
	// this very collision.
	body := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(js[start:start+end], "")

	// A key is a name that opens the literal, follows a comma, or opens a
	// line - which is every shape this literal actually uses, shorthand
	// (`id`, `detailEl`) aside, and shorthand cannot collide silently
	// because it names a variable that has to exist.
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)(?:^|[{,])\s*([A-Za-z_][A-Za-z0-9_]*)\s*:`).FindAllStringSubmatch(body, -1) {
		if seen[m[1]] {
			t.Errorf("the run entry declares %q twice. JavaScript keeps the LAST one "+
				"silently, so whichever reader wanted the first is now reading the "+
				"other one's type", m[1])
		}
		seen[m[1]] = true
	}
	if len(seen) < 20 {
		t.Fatalf("only %d fields were found in the run entry; the scan is broken, not "+
			"the literal", len(seen))
	}
	// The two that actually collided, named so a later rename cannot quietly
	// remove the thing this test is about.
	for _, want := range []string{"files", "fileList"} {
		if !seen[want] {
			t.Errorf("the run entry has no %q field - if it was renamed, rename it here "+
				"too rather than leaving this check pointing at nothing", want)
		}
	}
}

// TestTheFileListRendersEveryFileAndFallsBackWhenItCannotKnow is the client
// half of the wire widening. Two things it must not do: render only the video
// list (the pre-TOR-180 behaviour), and treat a message with no "files" key
// as a torrent holding nothing - a replay of a run recorded before
// cache.Run.Files existed is exactly that message, and the video list is the
// honest stand-in.
func TestTheFileListRendersEveryFileAndFallsBackWhenItCannotKnow(t *testing.T) {
	js := servedScript(t)
	fn := jsFunc(t, js, "renderFileList")

	if !regexp.MustCompile(`entry\.fileList = ev\.files \|\| entry\.videos`).MatchString(fn) {
		t.Error("renderFileList does not fall back from ev.files to the video list - an " +
			"absent \"files\" key means \"cannot say\", never \"holds nothing\", and no " +
			"torrent holds no files")
	}
	if !strings.Contains(fn, "entry.fileListKnown = !!ev.files") {
		t.Error("renderFileList does not record whether the whole list was actually " +
			"known - without it the title reports one list's length as the other's")
	}
	// The rows come from the whole list, and tickability from the video one.
	if !strings.Contains(fn, "entry.fileList.map(") {
		t.Error("renderFileList does not build its rows from entry.fileList - drawing " +
			"the video list is the behaviour this ticket replaced")
	}
	if !regexp.MustCompile(`tickable = new Set\(entry\.videos\.map`).MatchString(fn) {
		t.Error("renderFileList does not decide tickability from the video list - which " +
			"file can be captured is swarm.SelectVideos' judgement (a small .mkv beside " +
			"a large one is a sample), never something a path can be re-read for here")
	}
}

// TestAnUntickableFileIsNotToldByColourAlone is the accessibility half, and
// the constraint the ticket states outright: somebody who cannot separate the
// two greys still has to know they cannot tick a row.
//
// Two carriers that are not colours, and this checks both. The checkbox is
// ABSENT - a non-video row is built as a <span>, so there is no input to
// click and no <label> pretending there is - and .picker-mark draws an em
// dash in the column the checkbox would have occupied, the same mark every
// absent metric cell in the run table already uses.
func TestAnUntickableFileIsNotToldByColourAlone(t *testing.T) {
	js := servedScript(t)
	fn := jsFunc(t, js, "renderFileList")

	if !regexp.MustCompile(`createElement\(video \? "label" : "span"\)`).MatchString(fn) {
		t.Error("a non-video row is not built as a plain element - a <label> with no " +
			"control in it is furniture pretending to be interactive")
	}
	if !regexp.MustCompile(`(?s)if \(video\) \{.*?box\.type = "checkbox"`).MatchString(fn) {
		t.Error("renderFileList does not build the checkbox only for a tickable row - " +
			"the checkbox's ABSENCE is what says \"you cannot tick this\" without colour")
	}
	if !strings.Contains(fn, `"picker-mark"`) || !strings.Contains(fn, `"—"`) {
		t.Error("no .picker-mark em dash on an untickable row - it is the visible, " +
			"non-colour half of the same statement, and the page's own mark for a " +
			"reading that is not there")
	}
	if !strings.Contains(fn, "WHY_NOT_VIDEO") {
		t.Error("an untickable row carries no title saying why - the reason has to be " +
			"reachable in words, not only inferred from a missing control")
	}

	css := stylesheet(t)
	grey := cssRule(t, css, `.picker-item[data-video="false"] .picker-file {`)
	if !strings.Contains(grey, "var(--ink-2)") {
		t.Errorf("an untickable row's colour is %q, want var(--ink-2)", grey)
	}
	if strings.Contains(grey, "--ink-3") {
		t.Errorf("an untickable row reads in --ink-3 (%q). TOR-158 took that level off "+
			"text entirely: it measured 3.14-4.14 against the surfaces it was painted "+
			"on, under AA's 4.5 on every one of them", grey)
	}
}

// TestTheAmberFrameIsOnlyForARowThatIsBlocking is what the list becoming
// permanent costs if nobody notices it. .picker wore a --warn border because
// it WAS the blocking state - nothing happened until somebody ticked a box -
// and a list that is now on screen for every row would wear that alarm
// against a run that is already fetching, or one that finished a month ago.
func TestTheAmberFrameIsOnlyForARowThatIsBlocking(t *testing.T) {
	css := stylesheet(t)

	resting := cssRule(t, css, ".picker {")
	if strings.Contains(resting, "var(--warn)") {
		t.Errorf(".picker wears var(--warn) at rest: %q. Since TOR-180 the list is on "+
			"screen in every state, and an amber frame says \"this is blocking\" - which "+
			"is a false alarm on a run that is already going", resting)
	}
	if !strings.Contains(resting, "var(--rule)") {
		t.Errorf(".picker's resting border is %q, want the quiet var(--rule) - the same "+
			"choice .run-again makes for an offer rather than a demand", resting)
	}

	parked := cssRule(t, css, `.picker[data-parked="true"] {`)
	if !strings.Contains(parked, "var(--warn)") {
		t.Errorf("a parked row's frame is %q, want var(--warn) - nothing at all happens "+
			"there until somebody ticks a box, which is what earns the alarm", parked)
	}
}

// TestEverythingHiddenFromJSCanActuallyBeHidden is a trap this file has
// already paid for three times (see the note above .drop-overlay[hidden]):
// a class selector setting `display` beats the UA's rule for [hidden] at
// equal specificity, so hiding such an element from JS alone does nothing at
// all and the control stays on screen.
//
// It used to check one element, .picker-foot, which TOR-181 removed along
// with the button in it. Rather than delete the guard with its subject, this
// asks the general question: for EVERY element app.js hides by setting
// `.hidden`, if any rule naming that element declares display, there must be
// a [hidden] companion.
//
// TOR-182 WIDENED IT TWICE, and both widenings caught something standing.
//
// The scope was the file list; it is now every element in the run's detail
// that app.js hides, per-file ones included - the file details moved inside
// the list, so "the file list" and "the detail" are no longer two places. The
// two elements that walked into the trap long ago and have been on screen
// ever since are .file-links (an empty <p> plus its paragraph margins under
// every file card since the rule was written) and .torrent-actions (the same,
// under every torrent's summary). Both were invisible to a reader of the diff
// that introduced them, because the CONTROL inside each - the contact-sheet
// link, Send to my client - declares no display of its own and so was hidden
// correctly, leaving only whitespace to notice.
//
// And it reads the rules by SELECTOR LIST rather than by `sel + " {"`, which
// is what makes the answer trustworthy: `.run-cancel,\n.run-priority { ... }`
// is one rule for two hidden elements, and the old form found neither of them
// and silently passed. It happens to declare no display; the point is that
// nothing was checking.
func TestEverythingHiddenFromJSCanActuallyBeHidden(t *testing.T) {
	js := servedScript(t)
	css := stylesheet(t)

	// The detail's own elements app.js takes off screen, as the property
	// assignments themselves - so an element that stops being hidden drops
	// out of this list rather than failing it. Both receivers, because a
	// per-file element is reached through its own fentry.
	hidden := regexp.MustCompile(`(?:entry|fentry)\.([A-Za-z]+)\.hidden = `).FindAllStringSubmatch(js, -1)
	if len(hidden) == 0 {
		t.Fatal("app.js hides nothing in a run's detail - this guard is anchored on it")
	}

	// The element behind each name, since the JS reads a property and the CSS
	// a class. Kept as a table rather than derived, because the mapping is
	// the one thing a rename would silently break. An empty string is an
	// element whose class carries no rule at all in app.css, which is the
	// one honest way to be exempt: nothing can outrank the UA's [hidden].
	selector := map[string]string{
		"pickerEl":       ".picker",
		"pickerAll":      ".picker-all",
		"pickerArmed":    ".picker-armed",
		"detailRowEl":    ".run-detail-row",
		"detailError":    ".run-detail-error",
		"torrentSummary": ".torrent-summary",
		"torrentActions": ".torrent-actions",
		"torrentSave":    ".torrent-save",
		"torrentSend":    ".torrent-send",
		"againEl":        ".run-again",
		"againGo":        ".run-again-go",
		"againRetry":     ".run-again-retry",
		"rowCancel":      ".run-cancel",
		"rowRaise":       ".run-priority-up",
		"rowLower":       ".run-priority-down",
		// Per file, and all three are inside the list's own rows since
		// TOR-182: the detail slot the row opens onto, the metadata one
		// level in, the contact-sheet link and the progress line.
		"body":     ".file-detail",
		"metaBody": ".meta-body",
		"links":    ".file-links",
		"progress": ".file-progress",
	}

	seen := map[string]bool{}
	for _, m := range hidden {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true

		sel, ok := selector[name]
		if !ok {
			t.Errorf("app.js hides %s and this test does not know which class that "+
				"is - add it to the table rather than leaving the display/[hidden] trap "+
				"unguarded for it", name)
			continue
		}

		declares := false
		for _, body := range ruleBodiesNaming(css, sel) {
			if strings.Contains(body, "display:") {
				declares = true
			}
		}
		if !declares {
			continue
		}
		got := cssRule(t, css, sel+"[hidden] {")
		if !strings.Contains(got, "display: none") {
			t.Errorf("%s declares display but %s[hidden] is %q, want display: none - "+
				"without it the class rule beats the UA's and app.js hiding it does "+
				"nothing", sel, sel, got)
		}
	}
	// The scan found the elements it was widened for, rather than quietly
	// matching nothing after a rename.
	for _, want := range []string{"links", "torrentActions", "body"} {
		if !seen[want] {
			t.Errorf("app.js no longer hides %q. If it was renamed, rename it in the "+
				"table above too - this guard was widened to cover exactly this element, "+
				"and it caught a live instance of the trap on it", want)
		}
	}

	// AND THE ONES HIDDEN THROUGH A LOCAL ALIAS, which the scan above cannot
	// see and which are therefore the likeliest place for this trap to sit
	// unnoticed: two functions take a reference first (`const el =
	// entry.rowProgress`) and then hide `el`, so there is no
	// `entry.<name>.hidden` for a receiver-based scan to match.
	//
	// .run-progress is the live instance TOR-182 found here. It declares
	// `display: flex`, so `el.hidden = true` on a finished run never took it
	// off screen - and because renderRunProgress also sets role="progressbar"
	// and the aria-value* attributes when it shows the bar, and removes none
	// of them when it hides it, a done row kept announcing a progress bar
	// stuck at its last reading. Nothing was VISIBLE (the bar is .28rem tall
	// with no ground of its own and its segments are emptied), which is
	// exactly why it lasted: the trap's usual symptom, a control still on
	// screen, was missing.
	//
	// Kept as a small table of its own rather than folded above, because the
	// two halves are found differently and a reader has to know which is
	// which.
	for fn, sel := range map[string]string{
		"renderRunProgress": ".run-progress",
		"renderReach":       ".reach",
	} {
		body := jsFunc(t, js, fn)
		if !strings.Contains(body, "el.hidden") {
			t.Errorf("%s no longer hides anything through a local alias. If the alias is "+
				"gone the scan above covers it and this row should go; if the function "+
				"is gone, so should the row", fn)
			continue
		}
		declares := false
		for _, rule := range ruleBodiesNaming(css, sel) {
			if strings.Contains(rule, "display:") {
				declares = true
			}
		}
		if !declares {
			continue
		}
		if got := cssRule(t, css, sel+"[hidden] {"); !strings.Contains(got, "display: none") {
			t.Errorf("%s hides %s, which declares display, but %s[hidden] is %q, want "+
				"display: none", fn, sel, sel, got)
		}
	}
}

// ruleBodiesNaming returns the declaration block of every rule whose selector
// list names sel on a line of its own - `.x {` and the `.x,` of a grouped
// selector both count. Comments are stripped first, so a brace inside one
// cannot be mistaken for a rule's.
//
// It exists because `strings.Contains(css, sel + " {")` cannot see a grouped
// rule at all, and a grouped rule declares just as much display as a lone
// one. Deliberately no more than this: every selector it is asked about is a
// plain class, and a rule reached only through a compound or a descendant
// selector is not what the [hidden] question is about.
func ruleBodiesNaming(css, sel string) []string {
	live := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(sel) + `\s*(?:,|\{)`)

	var out []string
	for _, loc := range re.FindAllStringIndex(live, -1) {
		rest := live[loc[0]:]
		open := strings.Index(rest, "{")
		end := strings.Index(rest, "}")
		if open < 0 || end < open {
			continue
		}
		out = append(out, rest[open+1:end])
	}
	return out
}
