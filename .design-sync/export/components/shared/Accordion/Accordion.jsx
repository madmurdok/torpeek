/* Accordion - torpeek's disclosure, at any of the three depths the page has.
 *
 * A BRIDGE, NOT AN IMPLEMENTATION. torpeek's front end is custom elements and
 * ES modules with no build step (docs/front-end.md). The disclosure's
 * behaviour - the three DOM writes that ARE a disclosure on this page, the
 * level's meaning, the shared triangle - lives in accordion.js beside this
 * file, which is torpeek's own shipped source, verbatim. This file only gives
 * that class a React-shaped door, because a design is written in JSX.
 *
 * IT IS A WRAPPER, AND IT TAKES A `level` (TOR-222). The previous version of
 * this bridge drew level 3 and refused the prop, on the grounds that the
 * element carrying the rail at levels 1 and 2 was "a run-grid ROW or a
 * file-list <li> with a checkbox in it, neither of which is this component's
 * to draw". That was true of the SUMMARY'S CONTENTS and wrong about the
 * disclosure: the box, the header and the region are the same three things at
 * every level, and what varies is their tag and their class. So this component
 * draws those three, and the row's cells or the file's checkbox are passed in
 * as `summary` - the caller's, exactly as they are in the product, where
 * run-table.js and file-list.js build them.
 *
 * WHAT THE LEVEL DECIDES here, and it is the same list accordion.js's own
 * level table decides: which tag and class the box, the summary and the region
 * wear, whether the toggle IS the mark or holds one, and - the part the class
 * owns rather than this file - whether the summary is dressed at all. Level 3
 * dresses nothing, so nothing you pass can make it.
 *
 * THE THREE LEVELS, AND WHAT NESTS IN WHAT. They are genuinely nested in the
 * product and compose the same way here:
 *
 *     <Accordion level={1} …>            a torrent's line -> its detail
 *       <Accordion level={2} …>          a video file's row -> its block
 *         <Accordion level={3} …>        the Metadata header -> the specs
 *
 * IT HOLDS NO STATE, AND NEITHER DOES THIS WRAPPER. `open` is the source of
 * truth and it comes from outside. That is not a preference: at the run level
 * the record is what the page reads to decide what a click means, so a copy
 * would be a second answer; at the two inner levels the disclosure is thrown
 * away and rebuilt whenever the file list is, so anything it remembered would
 * come back closed. And the DOM under it moves either way - the run table
 * re-sorts on every redraw and relocates a row's whole box, taking the subtree
 * out of the document and putting it back (measured: isConnected goes false
 * and true on the box and on both custom elements inside it, and all three
 * levels come back open).
 *
 * NO LISTENER IN THE COMPONENT EITHER, and this is the one thing to know
 * before composing at level 1 or 2. `onToggle` below is wired to the toggle
 * BUTTON, which is right at level 3 and is what the product does there. At the
 * RUN level the product listens on the whole ROW instead - a click anywhere in
 * a torrent's line opens it - and that listener must NOT go on the box, or
 * every click inside an open detail would collapse it. At the FILE level it is
 * on the row too, excluding clicks whose target is the checkbox. Neither of
 * those is this component's to install: pass your own handler on the element
 * you want it on, via `summaryProps`, and leave `onToggle` off.
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

/* The three levels' MARKUP, which is what this file adds to the class's own
 * level table. Not exported, for the reason the import note above gives: an
 * uppercase-initial export would be indexed as a second component.
 *
 * Every tag and class here is torpeek's own, lifted from run-table.js's
 * newRow, file-list.js's renderFileList and file-detail.js's BODY. `mark:
 * null` means the toggle IS the mark, which is true at level 2 only -
 * .picker-open is a triangle-sized button rather than a header with a triangle
 * in it. `label: null` goes with it: that toggle carries an aria-label instead
 * of text, because the visible name belongs to the checkbox's <label>.
 */
const SHAPES = {
  1: {
    box: "div", boxClass: "run-row-group",
    summary: "div", summaryClass: "run-row",
    toggleClass: "run-row-main", mark: "run-toggle-icon", label: "run-name",
    region: "div", regionClass: "run-detail-row",
  },
  2: {
    box: "li", boxClass: "picker-item",
    summary: "span", summaryClass: "picker-file",
    toggleClass: "picker-open", mark: null, label: null,
    region: "div", regionClass: "file-detail",
  },
  3: {
    box: "section", boxClass: "meta",
    summary: "h3", summaryClass: "meta-title",
    toggleClass: "meta-toggle", mark: "meta-toggle-icon", label: "meta-toggle-label",
    region: "div", regionClass: "meta-body",
  },
};

/* level        1 (a torrent's line), 2 (a video file's block) or 3 (Metadata).
 * label        the toggle's own text. Ignored at level 2, whose toggle is a
 *              bare triangle - pass `toggleLabel` there for its aria-label.
 * summary      whatever else belongs in the header beside the toggle: the ten
 *              run cells at level 1, the checkbox and the file's name and size
 *              at level 2, nothing at level 3.
 * summaryProps extra props for the header element - notably the click handler
 *              at levels 1 and 2, where the whole row is the target.
 * open         the source of truth, from outside. See the header.
 * onToggle     a handler for the toggle BUTTON. Right at level 3; at the outer
 *              two put yours on the row through summaryProps instead.
 * children     what the disclosure opens onto.
 */
export function Accordion({
  level = 3,
  label = "Metadata",
  toggleLabel,
  summary,
  summaryProps = {},
  open = false,
  onToggle,
  children,
}) {
  const shape = SHAPES[level];
  if (!shape) {
    throw new Error("Accordion: level must be 1, 2 or 3, not " + JSON.stringify(level));
  }

  const box = useRef(null);
  const head = useRef(null);
  const toggle = useRef(null);
  const region = useRef(null);
  const mark = useRef(null);
  const disclosure = useRef(null);

  // Built once, over the elements below, and then only ever told the answer.
  // The class checks that the box really holds the header, the toggle and the
  // region, and that the region is NOT inside the header - so a shape wired
  // wrongly throws here rather than looking almost right.
  useEffect(() => {
    if (!box.current || !head.current || !toggle.current || !region.current) return;
    disclosure.current = new AccordionElement({
      level,
      container: box.current,
      summary: head.current,
      toggle: toggle.current,
      region: region.current,
      mark: mark.current || undefined,
    });
  }, [level]);

  // The declarative half: `open` is the source of truth and the elements are
  // told to catch up. apply() writes aria-expanded on the toggle, `hidden` on
  // the region and - at the levels with a rail - data-expanded on the header.
  // Those three writes are the ones nothing else on the page may make.
  useEffect(() => {
    if (disclosure.current) disclosure.current.apply(open);
  }, [open, level]);

  const Box = shape.box;
  const Head = shape.summary;
  const Region = shape.region;

  return (
    <Box className={shape.boxClass} ref={box} data-accordion-level={level}>
      <Head className={shape.summaryClass} ref={head} {...summaryProps}>
        <button type="button" className={
                  shape.mark ? shape.toggleClass : shape.toggleClass + " disclosure-mark"}
                ref={toggle}
                aria-expanded={open}
                aria-label={shape.label ? undefined : toggleLabel}
                onClick={onToggle}>
          {shape.mark && (
            <span className={shape.mark + " disclosure-mark"} aria-hidden="true"
                  ref={mark}></span>
          )}
          {shape.label && <span className={shape.label}>{label}</span>}
        </button>
        {summary}
      </Head>
      {/* THE REGION IS THE HEADER'S SIBLING INSIDE THE BOX, never its child,
           and that is load-bearing at every level rather than tidy: a region
           inside the thing that toggles it closes on every click within it.
           accordion.js refuses the other arrangement outright.

           `hidden` is the WHOLE of collapse. No rule anywhere sets `display`
           on any of the three regions, deliberately, so the UA's own [hidden]
           is never beaten by a class selector at equal specificity - a trap
           this codebase has paid for six times in one file. Do not hide a
           region any other way. */}
      <Region className={shape.regionClass} ref={region} hidden={!open}>
        {children}
      </Region>
    </Box>
  );
}

export default Accordion;
