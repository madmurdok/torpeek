package web

import (
	"strings"
	"testing"
)

// TOR-174: .run-detail sits in a cell that spans every column, so it is by
// construction as wide as the TABLE (every column's summed width) rather than
// as wide as the PANE (.run-table-wrap's own visible width) - the bare grid
// track lengths TOR-215 put in place of TOR-157's table-layout: fixed are
// what let those two diverge, and at a pane narrower than the table the
// surplus used to sit behind the table's own horizontal scroll, taking the
// top-up offer and its figure with it (measured: 122 elements past the
// pane's right edge at a 592px pane, the top-up block among them).
//
// The fix has two halves and either one missing silently reopens the
// ticket, which is exactly why this checks both rather than just "sticky is
// present": position: sticky pins the OFFSET, never the SIZE, so a sticky
// .run-detail with no width tied to the pane is still exactly as wide as
// the spanning cell it sits in - sticky alone was the trap the ticket's own
// report named and nearly cost an hour. This is the CSS half; TestSyncRunDetail
// WidthIsWiredToEveryPlaceThePaneCanChange (below) is the JS half that
// keeps --run-detail-w current.
//
// This reads the stylesheet as served rather than rendering it - the same
// boundary stylesheet_test.go's own comment on TestLightboxChromeIs... draws
// for its own CSS/JS pair: it can catch the width/sticky plumbing deleted or
// renamed, and it cannot catch a wrong pixel in a real layout. A real
// browser is what TOR-174's own report used for that, at 592px and 432px
// panes and after a real column drag.
func TestExpandedDetailIsStickyAndTracksThePaneWidth(t *testing.T) {
	css := stylesheet(t)
	rule := block(t, css, ".run-detail {")

	if got := rule["position"]; got != "sticky" {
		t.Errorf(".run-detail position is %q, want \"sticky\" - without it the detail "+
			"scrolls away with the table instead of staying in view (TOR-174)", got)
	}
	if got := rule["left"]; got != "0" {
		t.Errorf(".run-detail left is %q, want \"0\" - sticky with no offset pins nothing", got)
	}
	if got, ok := rule["width"]; !ok || !strings.HasPrefix(got, "var(--run-detail-w") {
		t.Errorf(".run-detail width is %q (present: %v), want a var(--run-detail-w, ...) - "+
			"THE TRAP: sticky constrains offset, never size, so without this the element "+
			"stays exactly as wide as its colspanned <td> (the table), not the pane. "+
			"run-table.js's syncRunDetailWidth() is what keeps the token current", got, ok)
	}
	// Without border-box, --run-detail-w's px value would be the CONTENT box
	// and .run-detail's own horizontal padding would add on top of it,
	// pushing the element's outer edge past the pane by exactly that padding
	// - a narrower, but still real, reopening of the same bug.
	if got := rule["box-sizing"]; got != "border-box" {
		t.Errorf(".run-detail box-sizing is %q, want \"border-box\" - otherwise the "+
			"element's own padding pushes its right edge past --run-detail-w, and past "+
			"the pane with it", got)
	}
}

// TestSyncRunDetailWidthIsWiredToEveryPlaceThePaneCanChange is the JS half.
// One shared --run-detail-w custom property (the CSS half above reads it)
// has to be recomputed from .run-table-wrap's own clientWidth at every place
// the ticket's report identifies the pane's width as able to change: a
// window resize, a row's own expand/collapse (opening or closing a row
// changes the PAGE's height, which can add or remove the document's
// vertical scrollbar and with it a few pixels of the pane), and the end of a
// column drag (TOR-157 - changes the TABLE's width, not the pane's, but the
// report says to hook it anyway for the rare case a scrollbar appears or
// disappears with it).
//
// This is a text check on the served script (this repository ships no JS
// runner - see columns_test.go's own note), so it catches the wiring
// deleted or a hook silently dropped during a later refactor; it does not
// prove the number syncRunDetailWidth computes is correct in a real layout -
// TOR-174's own report is the real-browser check for that.
//
// SINCE TOR-194 IT READS run-table.js, and all three causes moved there
// together, which is the point rather than an inconvenience: the pane whose
// width this measures is one of the table element's own parts, and so is
// every one of the three things that can change it. Nothing in app.js sets
// --run-detail-w any more, which the last check here is about.
func TestSyncRunDetailWidthIsWiredToEveryPlaceThePaneCanChange(t *testing.T) {
	b, err := embedded.ReadFile("assets/run-table.js")
	if err != nil {
		t.Fatalf("reading the embedded run-table.js: %v", err)
	}
	js := string(b)

	// A METHOD NOW, so it closes at two spaces rather than at column zero.
	fn := jsMethod(t, js, "syncRunDetailWidth")
	if !strings.Contains(fn, "this.wrap") || !strings.Contains(fn, "clientWidth") {
		t.Error("syncRunDetailWidth does not read this.wrap's clientWidth - that is the " +
			"pane's own visible width (.run-table-wrap, found in connectedCallback), and " +
			"the whole point of this function")
	}
	if !strings.Contains(fn, "--run-detail-w") {
		t.Error("syncRunDetailWidth never sets --run-detail-w - detail.css's .run-detail " +
			"reads exactly that token for its width")
	}

	// CAUSE ONE: the window. The listener has to be the bound instance field,
	// not an inline arrow - disconnectedCallback removes the same function
	// object, and a resize handler left on `window` keeps measuring a table
	// that is no longer on the page.
	for _, want := range []string{
		"this.onResize = () => this.syncRunDetailWidth();",
		`window.addEventListener("resize", this.onResize);`,
		`window.removeEventListener("resize", this.onResize);`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("run-table.js does not contain %q - the pane narrows and widens with the "+
				"window, the detail's width must follow it, and the listener has to come off "+
				"again with the element", want)
		}
	}

	// CAUSE TWO: a row opening or closing.
	expanded := jsMethod(t, js, "setRunExpanded")
	if !strings.Contains(expanded, "this.syncRunDetailWidth()") {
		t.Error("setRunExpanded never calls syncRunDetailWidth - opening or closing a " +
			"row can add or remove the page's own scrollbar, changing the pane under an " +
			"already-open row")
	}

	// CAUSE THREE: the end of a column drag. endColumnDrag is declared INSIDE
	// wireColumnResizers' per-header loop (a closure over that iteration's
	// th/handle), so neither jsFunc's column-zero convention nor jsMethod's
	// two-space one bounds it - this slices instead, from its own declaration
	// to the addEventListener call that registers it a few lines later, which
	// is a real anchor rather than a guessed offset.
	//
	// AN ARROW, and this test is how the shape change announced itself: it was
	// a `function` declaration until a browser pass found that `this` inside
	// it was then the <span> handle, so the this.syncRunDetailWidth() call
	// below - the very thing this block checks for - threw TypeError on every
	// drag instead of running. TestTheDragEndKeepsTheElementAsIts_this pins
	// the arrow form; this anchor has to agree with it, and failing loudly
	// when it does not is the behaviour that caught the change.
	dragStart := strings.Index(js, "const endColumnDrag = (event) => {")
	if dragStart < 0 {
		t.Fatal("run-table.js has no `const endColumnDrag = (event) =>` - it must be an " +
			"arrow function so `this` is the element rather than the handle it is " +
			"registered on (see TestTheDragEndKeepsTheElementAsIts_this)")
	}
	dragEnd := strings.Index(js[dragStart:], `handle.addEventListener("pointerup", endColumnDrag);`)
	if dragEnd < 0 {
		t.Fatal("endColumnDrag is never registered on pointerup")
	}
	drag := js[dragStart : dragStart+dragEnd]
	if !strings.Contains(drag, "this.syncRunDetailWidth()") {
		t.Error("endColumnDrag never calls syncRunDetailWidth - TOR-157's column drag " +
			"changes the table's width, and the ticket's report asks for a recheck here too")
	}

	// AND ONE PLACE IT MUST NOT BE. Two modules writing --run-detail-w would
	// be two answers to "how wide is the pane", and the one that lost the race
	// would be the one on screen.
	page, err := embedded.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("reading the embedded app.js: %v", err)
	}
	if strings.Contains(string(page), "--run-detail-w") {
		t.Error("app.js writes --run-detail-w - since TOR-194 the pane belongs to the table " +
			"element, which is the only thing that may measure it; a second writer is a " +
			"second answer to the same question")
	}
}
