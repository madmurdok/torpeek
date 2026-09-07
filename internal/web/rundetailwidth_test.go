package web

import (
	"strings"
	"testing"
)

// TOR-174: a colspanned .run-detail is, by construction, as wide as the
// TABLE (every column's summed width) rather than as wide as the PANE
// (.run-table-wrap's own visible width) - table-layout: fixed (TOR-157) is
// what lets those two diverge, and at a pane narrower than the table the
// surplus used to sit behind the table's own horizontal scroll, taking the
// top-up offer and its figure with it (measured: 122 elements past the
// pane's right edge at a 592px pane, the top-up block among them).
//
// The fix has two halves and either one missing silently reopens the
// ticket, which is exactly why this checks both rather than just "sticky is
// present": position: sticky pins the OFFSET, never the SIZE, so a sticky
// .run-detail with no width tied to the pane is still exactly as wide as
// its containing <td> - sticky alone was the trap the ticket's own report
// named and nearly cost an hour. This is the CSS half; TestSyncRunDetail
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
			"app.js's syncRunDetailWidth() is what keeps the token current", got, ok)
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
func TestSyncRunDetailWidthIsWiredToEveryPlaceThePaneCanChange(t *testing.T) {
	b, err := embedded.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("reading the embedded app.js: %v", err)
	}
	js := string(b)

	fn := jsFunc(t, js, "syncRunDetailWidth")
	if !strings.Contains(fn, "runTableWrap") || !strings.Contains(fn, "clientWidth") {
		t.Error("syncRunDetailWidth does not read el.runTableWrap's clientWidth - " +
			"that is the pane's own visible width, and the whole point of this function")
	}
	if !strings.Contains(fn, "--run-detail-w") {
		t.Error("syncRunDetailWidth never sets --run-detail-w - app.css's .run-detail " +
			"reads exactly that token for its width")
	}

	if !strings.Contains(js, `window.addEventListener("resize", syncRunDetailWidth)`) {
		t.Error("app.js does not call syncRunDetailWidth on window resize - the pane " +
			"narrows and widens with the window, and the detail's width must follow it")
	}

	expanded := jsFunc(t, js, "setRunExpanded")
	if !strings.Contains(expanded, "syncRunDetailWidth()") {
		t.Error("setRunExpanded never calls syncRunDetailWidth - opening or closing a " +
			"row can add or remove the page's own scrollbar, changing the pane under an " +
			"already-open row")
	}

	// endColumnDrag is declared INSIDE the per-header for loop (a closure
	// over that iteration's th/handle), not at column zero, so jsFunc's
	// "closing brace at column zero" convention does not bound it - this
	// slices instead, from its own declaration to the addEventListener call
	// that registers it a few lines later, which is a real anchor rather
	// than a guessed offset.
	dragStart := strings.Index(js, "function endColumnDrag(event) {")
	if dragStart < 0 {
		t.Fatal("app.js has no endColumnDrag(event) function")
	}
	dragEnd := strings.Index(js[dragStart:], `handle.addEventListener("pointerup", endColumnDrag);`)
	if dragEnd < 0 {
		t.Fatal("endColumnDrag is never registered on pointerup")
	}
	drag := js[dragStart : dragStart+dragEnd]
	if !strings.Contains(drag, "syncRunDetailWidth()") {
		t.Error("endColumnDrag never calls syncRunDetailWidth - TOR-157's column drag " +
			"changes the table's width, and the ticket's report asks for a recheck here too")
	}
}
