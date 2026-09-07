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

	// And the gating that DID survive must be about the controls, not the
	// list: a decision can only be taken while the torrent is parked.
	if !strings.Contains(fn, `entry.state === "needs-action"`) {
		t.Error("syncFileList never asks whether the row is parked - the controls that " +
			"stage and send a decision (Select all/none, Take frames) only mean " +
			"anything there, and the server refuses a decision in any other state")
	}
	for _, control := range []string{"pickerAll", "pickerNone", "pickerFoot"} {
		if !regexp.MustCompile(`entry\.` + control + `\.hidden = !parked`).MatchString(fn) {
			t.Errorf("syncFileList does not hide entry.%s outside the parked state - a "+
				"\"Take frames\" beside a run that is already fetching is a control that "+
				"either does nothing or is refused", control)
		}
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

// TestTheFootIsHiddenByARuleThatBeatsTheUAsOwn is a trap this file has
// already paid for twice (see the note above .drop-overlay[hidden]) and which
// this ticket walked straight into a third time: .picker-foot sets display,
// and a class selector setting display beats the UA's rule for [hidden] at
// equal specificity - so hiding the foot from JS alone leaves "Take frames"
// on screen beside a run that is already fetching.
func TestTheFootIsHiddenByARuleThatBeatsTheUAsOwn(t *testing.T) {
	css := stylesheet(t)

	if !strings.Contains(cssRule(t, css, ".picker-foot {"), "display:") {
		t.Skip("the foot no longer sets display, so the UA's [hidden] rule wins " +
			"uncontested and this guard has nothing to protect")
	}
	got := cssRule(t, css, ".picker-foot[hidden] {")
	if !strings.Contains(got, "display: none") {
		t.Errorf(".picker-foot[hidden] is %q, want display: none - without it the "+
			"foot's own display beats the UA rule and app.js hiding it does nothing", got)
	}
}
