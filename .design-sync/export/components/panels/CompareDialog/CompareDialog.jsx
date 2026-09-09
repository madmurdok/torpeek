/* CompareDialog - two result sets of one film, compared by FLIPPING.
 *
 * A BRIDGE, NOT AN IMPLEMENTATION - the rule FramePanel.jsx states at length
 * and this file does not repeat. The behaviour (the pairing, the flip, the
 * walk along the film, the pickers, the keys) lives in compare-dialog.js
 * beside this file, which is torpeek's own shipped source, verbatim.
 *
 * THE IMPORT BELOW IS THE WHOLE MECHANISM: the platform COMPILES the bundle
 * from these sources, so the element's code has to arrive through this import
 * to be in it. Importing for the side effect is enough - compare-dialog.js
 * ends in customElements.define("compare-dialog", CompareDialog).
 *
 * The markup below is index.html's, WRAPPED and not re-indented there, with
 * class -> className and every HTML comment turned into a JSX comment. Leaving
 * one in HTML form is a syntax error, and doing the class conversion without
 * the comment conversion is what compiled this export's first upload to zero
 * components.
 *
 * WHAT THIS ELEMENT NEEDS THAT THE FRAME PANEL DID NOT: two injected services.
 * url() reads document.baseURI and the page's access token, so it belongs to
 * the page's bootstrap rather than here; log() writes to the activity log
 * element, which is app.js's. Both must be supplied by
 * compare-dialog.js's setServices({url, log}) BEFORE open() is called -
 * open() is a fetch, and with no url() injected it throws.
 */
// The module is imported from ../../../modules/ rather than from beside this
// file, and that path is load-bearing (TOR-208). Claude Design's checker takes
// every UPPERCASE-initial export of a module it finds under components/ as a
// COMPONENT. state.js has to travel with every element - each one imports
// ./state.js - and its five uppercase exports (ABSENT, FINAL and the three
// PRIORITY_* levels) are constants, not components: PRIORITY_HIGH is the
// number 1. Shipped inside the component folders they became five entries a
// design agent is offered and can do nothing with. Nothing outside
// components/ is scanned - the project's own templates/ carries .js files and
// none of them is indexed - so one shared modules/ folder keeps the index
// honest AND ships one copy of each module instead of state.js five times.
import "../../../modules/compare-dialog.js";
import { useEffect, useRef } from "react";

export function CompareDialog({ open = false, infohash, index = 0, onClose }) {
  const host = useRef(null);

  // The element's whole API is open(infohash, index), and it is imperative
  // because showModal() is - and because it FETCHES: it reads every result set
  // on disk (GET /compare/sets) so a run that finished in the meantime is
  // offered, then pairs the two arms (GET /compare). This effect is what makes
  // it declarative from the outside.
  //
  // WITH NO infohash THERE IS NOTHING TO OPEN, and that is the truth rather
  // than a guard: the dialog's content is a comparison of two sets on a
  // server, and there is no "empty" comparison to show. A design that wants
  // the picture uses the markup in CompareDialog.html, which carries a whole
  // comparison statically.
  useEffect(() => {
    const el = host.current;
    if (!el || typeof el.open !== "function") return;
    if (open && infohash) {
      el.open(infohash, index);
    } else if (!open && el.dialog && el.dialog.open) {
      el.dialog.close();
    }
  }, [open, infohash, index]);

  // The dialog closes itself on Escape, on a backdrop click and on its own
  // button - all three are the element's, not this wrapper's - so the only way
  // a consumer hears about it is the dialog's own close event.
  useEffect(() => {
    const el = host.current;
    if (!el || !onClose) return;
    const dialog = el.querySelector(".compare");
    if (!dialog) return;
    dialog.addEventListener("close", onClose);
    return () => dialog.removeEventListener("close", onClose);
  }, [onClose]);

  return (
    <compare-dialog ref={host}>
      <dialog id="compare" className="compare">
        <button id="compare-close" className="lightbox-close" type="button" aria-label="Close">&times;</button>
        <header className="compare-head">
          <label className="compare-arm" data-arm="a">
            <span className="compare-key" aria-hidden="true">1</span>
            <select id="compare-a" className="compare-pick" aria-label="The first set to compare"></select>
          </label>
          <label className="compare-arm" data-arm="b">
            <span className="compare-key" aria-hidden="true">2</span>
            <select id="compare-b" className="compare-pick" aria-label="The second set to compare"></select>
          </label>
        </header>

        {/* tabindex, so the keys work the moment it opens without a person
            having to find something to focus first; the buttons below do the
            same jobs for anyone who would rather press a control than learn a
            key. */}
        <div id="compare-stage" className="compare-stage" tabIndex={0} role="button"
             aria-label="The frame - press to flip to the other set">
          <img id="compare-shot-a" className="compare-shot" data-arm="a" alt="" />
          <img id="compare-shot-b" className="compare-shot" data-arm="b" alt="" />
          <div id="compare-gap" className="compare-gap"><span id="compare-gap-code" className="compare-gap-code"></span></div>
        </div>

        <p className="compare-bar">
          <button id="compare-prev" className="compare-step" type="button" aria-label="The previous position">&lsaquo;</button>
          <span id="compare-place" className="compare-place"></span>
          <button id="compare-next" className="compare-step" type="button" aria-label="The next position">&rsaquo;</button>
          <button id="compare-flip" className="compare-flip" type="button">Flip</button>
          <span id="compare-times" className="compare-times"></span>
        </p>
        <p id="compare-note" className="compare-note"></p>
        <p className="compare-keys">← → position · space, ↑ ↓ or click flips · 1 2 pick a set · esc closes</p>
      </dialog>
    </compare-dialog>
  );
}

export default CompareDialog;
