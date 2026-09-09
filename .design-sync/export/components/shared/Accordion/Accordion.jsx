/* Accordion - torpeek's disclosure, at any of the three depths the page has.
 *
 * A BRIDGE, NOT AN IMPLEMENTATION. torpeek's front end is custom elements and
 * ES modules with no build step (docs/front-end.md). The disclosure's
 * behaviour - the three DOM writes that ARE a disclosure on this page, the
 * level's meaning, the shared triangle - lives in accordion.js beside this
 * file, which is torpeek's own shipped source, verbatim. This file only gives
 * that class a React-shaped door, because a design is written in JSX.
 *
 * THE ONE PLACE THIS BRIDGE IS SHAPED DIFFERENTLY FROM THE OTHER SIX, and it
 * is worth knowing before composing with it: Accordion is NOT a custom
 * element. It cannot be. At every one of the three levels the only place to
 * put a wrapping element is between a grid container and its items:
 * .run-row-group carries `grid-template-columns: subgrid`, which only works on
 * a direct grid item of .run-grid, and at the file level the toggle sits
 * inside the row's <label> while the region is that row's sibling inside the
 * <li>. So it is a plain class over elements the page already has, and this
 * wrapper renders those elements and hands them over on mount.
 *
 * IT HOLDS NO STATE, AND NEITHER DOES THIS WRAPPER. `open` is the source of
 * truth and it comes from outside. That is not a preference: at the run level
 * the record is what the page reads to decide what a click means, so a copy
 * would be a second answer; at the two inner levels the disclosure is thrown
 * away and rebuilt whenever the file list is, so anything it remembered would
 * come back closed. And the DOM under it moves either way - the run table
 * re-sorts on every redraw and relocates a row's whole element, taking the
 * subtree out of the document and putting it back (measured: isConnected goes
 * false and true on the group and on both custom elements inside it).
 *
 * NO LISTENER IN THE COMPONENT EITHER. The three levels activate from three
 * different elements with three different exceptions - and the one that
 * matters is that a torrent's listener must stay on its ROW rather than on the
 * wrapper, or every click inside an open detail collapses it. So `onToggle`
 * below is wired to the toggle BUTTON by this wrapper, which is the level-3
 * arrangement; a design reproducing the run or file level puts its own
 * listener on the row and leaves this one off.
 *
 * THIS WRAPPER DRAWS LEVEL 3 AND ONLY LEVEL 3, and there is no `level` prop -
 * which is a deliberate refusal rather than a gap. The level is a real
 * parameter of the class, and it decides which element carries the rail; but
 * at levels 1 and 2 that element is a run-grid ROW or a file-list <li> with a
 * checkbox in it, neither of which is this component's to draw. A `level` prop
 * here would construct a disclosure over the wrong elements and dress the
 * Metadata section as though it were a torrent's line. So: for levels 1 and 2,
 * render your own row (the card carries both, closed and open) and drive it
 * with the exported class directly - `new AccordionElement({...}).apply(open)`
 * is the whole of it.
 */
// The module is imported from ../../../modules/ rather than from beside this
// file, and that path is load-bearing (TOR-208): Claude Design's checker takes
// every UPPERCASE-initial export of a module it finds under components/ as a
// COMPONENT, and one shared modules/ folder keeps the index honest AND ships
// one copy of each module rather than one per component folder.
//
// accordion.js exports exactly one name, the class, for the same reason - and
// it imports nothing itself, so this is the whole of what travels with it.
import { Accordion as AccordionElement } from "../../../modules/accordion.js";
import { useEffect, useRef } from "react";

/* The Metadata block: level 3 of the three, and the innermost. See the header
   above for why the other two are the class's to drive rather than this
   wrapper's, and the card for their markup in both states. */
export function Accordion({
  label = "Metadata",
  open = false,
  onToggle,
  children,
}) {
  const toggle = useRef(null);
  const region = useRef(null);
  const mark = useRef(null);
  const disclosure = useRef(null);

  // Built once, over the elements below, and then only ever told the answer.
  // No `dressed` element, and that is what level 3 MEANS rather than something
  // it happens to lack: there is no rail and no ground at this depth, because
  // both would sit inside the file's rail inside the torrent's ground. The
  // class REFUSES one here, so the difference is checked rather than
  // remembered.
  useEffect(() => {
    if (!toggle.current || !region.current) return;
    disclosure.current = new AccordionElement({
      level: 3,
      toggle: toggle.current,
      region: region.current,
      mark: mark.current || undefined,
    });
  }, []);

  // The declarative half: `open` is the source of truth and the elements are
  // told to catch up. apply() writes aria-expanded on the toggle, `hidden` on
  // the region and data-expanded on the dressed element - the three writes
  // nothing else on the page may make.
  useEffect(() => {
    if (disclosure.current) disclosure.current.apply(open);
  }, [open]);

  // The region is a SIBLING of the header inside <section class="meta">,
  // never a child of it - which is the shape at all three levels, and at the
  // outer two it is load-bearing rather than tidy: a region inside the thing
  // that toggles it collapses on every click within it.
  return (
    <section className="meta">
      <h3 className="meta-title">
        <button type="button" className="meta-toggle" ref={toggle}
                aria-expanded={open}
                onClick={onToggle}>
          <span className="meta-toggle-icon disclosure-mark" aria-hidden="true"
                ref={mark}></span>
          <span className="meta-toggle-label">{label}</span>
        </button>
      </h3>
      {/* `hidden` is the WHOLE of collapse, at all three levels. No rule
           anywhere sets `display` on any of the three regions, deliberately,
           so the UA's own [hidden] is never beaten by a class selector at
           equal specificity - a trap this codebase has paid for six times in
           one file. Do not hide a region any other way. */}
      <div className="meta-body" ref={region} hidden={!open}>
        {children}
      </div>
    </section>
  );
}

export default Accordion;
