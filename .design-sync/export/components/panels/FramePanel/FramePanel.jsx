/* FramePanel - torpeek's full-size frame viewer.
 *
 * A BRIDGE, NOT AN IMPLEMENTATION. torpeek's front end is custom elements and
 * ES modules with no build step (docs/front-end.md). The panel's behaviour -
 * the fit/100% zoom, the pan that cannot leak, the key handling - lives in
 * frame-panel.js beside this file, which is torpeek's own shipped source,
 * verbatim. This file only gives that element a React-shaped door, because a
 * design is written in JSX and a web component is not.
 *
 * THE IMPORT BELOW IS THE WHOLE MECHANISM, and getting it wrong is what made
 * the first upload of this component inert. The bundle is not something to
 * hand-write and upload: the platform COMPILES it from these sources, so the
 * element's code has to arrive through this import to be in it. Importing for
 * the side effect is enough - frame-panel.js ends in
 * customElements.define("frame-panel", FramePanel), so <frame-panel> is
 * registered by the time anything renders.
 *
 * The HTML comments the markup below carries are written as JSX comments.
 * Leaving them in HTML form is a syntax error, and it is what made the first
 * upload compile to zero components - the class -> className conversion was
 * done and the comments were not.
 */
import "./frame-panel.js";
import { useEffect, useRef } from "react";

export function FramePanel({ src, caption = "", open = false, onClose }) {
  const host = useRef(null);

  // The element's whole API is open(src, caption), and it is imperative
  // because showModal() is. This effect is what makes it declarative from the
  // outside: the `open` prop is the source of truth and the element is told
  // to catch up.
  useEffect(() => {
    const el = host.current;
    if (!el || typeof el.open !== "function") return;
    if (open && src) {
      el.open(src, caption);
    } else if (!open && el.dialog && el.dialog.open) {
      el.dialog.close();
    }
  }, [src, caption, open]);

  // The dialog closes itself on Escape, on a backdrop click and on its own
  // button - all three are the element's, not this wrapper's - so the only way
  // a consumer hears about it is the dialog's own close event.
  useEffect(() => {
    const el = host.current;
    if (!el || !onClose) return;
    const dialog = el.querySelector(".lightbox");
    if (!dialog) return;
    dialog.addEventListener("close", onClose);
    return () => dialog.removeEventListener("close", onClose);
  }, [onClose]);

  return (
    <frame-panel ref={host}>
      <dialog id="lightbox" className="lightbox">
        <p className="lightbox-bar">
          <span className="lightbox-tab">
            <span className="lightbox-chev" aria-hidden="true">&rsaquo;&rsaquo;</span>
            frame
            {/* The zoom readout, and the one thing in this panel wearing --accent:
                 it is the state, not decoration. aria-live so a change announces
                 itself to anyone who cannot see the pill fill. */}
            <span id="lightbox-zoom" className="lightbox-zoom" aria-live="polite">fit</span>
          </span>
          <span className="lightbox-ticks" aria-hidden="true"></span>
        </p>
        {/* tabIndex and the self-closing <img/> are JSX's spellings, not
            HTML's, and TOR-208 corrected them here rather than leaving the
            question open. `tabindex` reaches the DOM either way (React passes
            an unknown attribute through with a warning), but an UNCLOSED void
            element is a JSX parse error in every strict parser - and a parse
            error compiles the whole bundle to zero components, silently, which
            is the exact failure the HTML-comment conversion already cost this
            file one round trip. Whether the platform's own compiler happens to
            tolerate it is not worth another: valid JSX is accepted by a
            lenient parser too. */}
        <div id="lightbox-view" className="lightbox-view" tabIndex={0} role="button"
             aria-label="The frame - press to zoom to 100%, then the arrow keys pan it">
          <img id="lightbox-img" alt="" />
          <button id="lightbox-close" className="lightbox-close" type="button" aria-label="Close">&times;</button>
          <p id="lightbox-caption" className="lightbox-caption"></p>
        </div>
        <p className="lightbox-keys">click zooms to 100% &middot; &larr; &uarr; &rarr; &darr; or the mouse pans &middot; esc closes</p>
        {/* The two corners the dialog's own ::before/::after cannot reach: a
             bottom-left bracket and the stroke along the chamfer. A direct child of
             the dialog, NOT of the view - every bracket sits in one of the panel's
             label strips, off the footage, because a hairline in a single colour
             over arbitrary picture content is TOR-122's problem with no scrim
             available to solve it. */}
        <span className="lightbox-marks" aria-hidden="true"></span>
      </dialog>
    </frame-panel>
  );
}

export default FramePanel;
