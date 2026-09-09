/**
 * Accordion - torpeek's disclosure, at any of the three depths the page has.
 *
 * The page's most characteristic interaction, and there are exactly three of
 * them, nested inside each other:
 *
 *   1  a torrent's line   opens onto its detail row
 *   2  a video file's row opens onto its block, inside that detail
 *   3  the Metadata header opens onto the specs, inside that block
 *
 * All three are one component. What it does is the three DOM writes that ARE a
 * disclosure on this page - `aria-expanded` on the toggle, `hidden` on the
 * region, `data-expanded` on the row being dressed - and it is the only thing
 * on the page that writes any of them.
 *
 * NOT A CUSTOM ELEMENT, unlike torpeek's other six components, and the reason
 * is structural rather than stylistic: at every level the only place a
 * wrapping element could go is between a grid container and its items, and
 * `.run-row-group`'s `grid-template-columns: subgrid` only resolves on a
 * direct grid item. So it is a plain class over elements that already exist,
 * and `<Accordion>` below is a React door onto it.
 */
/**
 * <Accordion> DRAWS LEVEL 3, THE INNERMOST, AND TAKES NO `level` PROP - which
 * is a deliberate refusal rather than a gap.
 *
 * The level IS a real parameter, and it decides which element carries the
 * rail. But at levels 1 and 2 that element is a run-grid ROW or a file-list
 * `<li>` with a checkbox in it, neither of which this component draws - so a
 * `level` prop here would build a disclosure over the wrong elements and dress
 * the Metadata section as though it were a torrent's line.
 *
 * What the level decides, read off the current stylesheets rather than
 * designed (every figure measured in Chrome on the running page):
 *
 * | | 1 - run | 2 - file | 3 - meta |
 * |---|---|---|---|
 * | open ground | `--accent-dim` | none | none |
 * | open rail | 3px inset `--accent` | 2px inset `--accent` | none |
 * | toggle type | 13.1px, opacity 1 | 14px, opacity 1 | 12.8px, opacity .75 |
 * | region | no box of its own | 1px `--rule` on the left, indented | padded, no border |
 *
 * So the depth is how loudly the open state is dressed: the rail thins
 * 3px -> 2px -> none and the one filled ground appears once, at the top. The
 * TRIANGLE is identical at all three - it was three copies of the same rule
 * before this component existed, each with a comment saying it was
 * deliberately the same mark as its neighbour's.
 *
 * For levels 1 and 2: render your own row (the card carries both, closed and
 * open) and drive it with `AccordionElement` below.
 */
export interface AccordionProps {
  /**
   * The toggle's own text. Only level 3 has a label of its own ("Metadata");
   * at levels 1 and 2 the label is the torrent's name or the file's, and the
   * row that carries it is not this component's to draw.
   *
   * @default "Metadata"
   */
  label?: string;

  /**
   * Whether it is open. THE SOURCE OF TRUTH, and it has to come from outside.
   *
   * The component holds no open state and neither does this wrapper. In the
   * product the flag lives on the run's own record (`entry.expanded`,
   * `fentry.expanded`, `fentry.metaExpanded`), for two reasons that differ by
   * level: at the run level the record is what the page reads to decide what a
   * click means, so a copy would be a second answer; at the two inner levels
   * the disclosure is genuinely thrown away and rebuilt whenever the file list
   * is, so anything it remembered would come back closed.
   *
   * Either way the DOM under it moves: torpeek's run table re-sorts on every
   * redraw and relocates a row's whole element, which takes the subtree out of
   * the document and puts it back - measured, `isConnected` goes false then
   * true on the row group and on both custom elements inside it - and the row
   * comes back open because the answer was never in the DOM to begin with.
   *
   * @default false
   */
  open?: boolean;

  /**
   * Called when the toggle is activated. Put the `open` prop where this asks.
   *
   * WIRED TO THE BUTTON HERE, WHICH IS THE LEVEL-3 ARRANGEMENT. The other two
   * levels listen on the ROW instead, each with an exception of its own, and
   * one of them is load-bearing: a torrent's listener must stay on `.run-row`
   * and never on `.run-row-group`, or every click inside an open detail - a
   * checkbox, a thumbnail, Compare - collapses the thing being used. That is
   * why the component owns no listener at all. Reproducing level 1 or 2 means
   * putting your own listener on the row and leaving this prop off.
   */
  onToggle?: () => void;

  /**
   * What the disclosure opens onto. Rendered inside the region, whose `hidden`
   * attribute is the WHOLE of collapse.
   *
   * Do not hide a region any other way. No rule in any of torpeek's
   * stylesheets sets `display` on `.run-detail-row`, `.file-detail` or
   * `.meta-body`, deliberately, so the UA's own `[hidden] { display: none }`
   * is never beaten by a class selector at equal specificity - a trap this
   * codebase has paid for six times in one file.
   */
  children?: React.ReactNode;
}

export declare function Accordion(props: AccordionProps): JSX.Element;
export default Accordion;

/**
 * The class itself, for a design that needs to drive a disclosure over markup
 * it built itself - which is the usual case at levels 1 and 2, where the
 * toggle and the region belong to a row this component does not draw.
 *
 * IT IS NOT RE-EXPORTED FROM THE BRIDGE, on purpose: the checker indexes a
 * module's uppercase-initial named exports as COMPONENTS, and a second name
 * here would put a second "Accordion" in a designer's list with nothing
 * renderable behind it (TOR-208 spent round trips learning that). Import it
 * from the module instead - the same path the bridge uses:
 *
 *     import { Accordion } from "../../../modules/accordion.js";
 *
 * Construct it once over the elements, then call `apply` whenever the owner's
 * flag changes. It reads nothing back: not a field, not the DOM.
 *
 * Declared under a different name here only so this file can describe both
 * the React door and the class without one shadowing the other.
 */
declare class AccordionElement {
  constructor(parts: {
    /** 1, 2 or 3. See AccordionProps["level"]. */
    level: 1 | 2 | 3;
    /**
     * The control carrying `aria-expanded`. A real `<button>` at all three
     * levels in the product, which is where the keyboard operability comes
     * from: Return and Space both activate it, measured with real key presses.
     */
    toggle: HTMLElement;
    /** The element the toggle opens. Its `hidden` attribute is all of collapse. */
    region: HTMLElement;
    /**
     * The element wearing the triangle; defaults to the toggle. That default
     * is right at level 2, where `.picker-open` IS a triangle-sized button,
     * and wrong at 1 and 3, where the mark is a fixed-width span inside the
     * button so the label starts at the same x whichever way it points.
     */
    mark?: HTMLElement;
    /**
     * The element carrying `data-expanded`, for this level's rail.
     * REQUIRED at levels 1 and 2, REFUSED at level 3 - there is no rail and no
     * ground at that depth, because both would sit inside the file's rail
     * inside the torrent's ground, and three nested grounds read as chrome.
     */
    dressed?: HTMLElement | null;
  });

  /**
   * Write the open state out. The one writer of those three attributes, and
   * the state arrives as an argument every time - there is no field to keep it
   * in.
   */
  apply(expanded: boolean): void;
}
