/**
 * Accordion - torpeek's disclosure, at any of the three depths the page has.
 *
 * The page's most characteristic interaction, and there are exactly three of
 * them, nested inside each other:
 *
 *   1  a torrent's line     opens onto its detail row
 *   2  a video file's row   opens onto its block, inside that detail
 *   3  the Metadata header  opens onto the specs, inside that block
 *
 * All three are one component. What it does is the three DOM writes that ARE a
 * disclosure on this page - `aria-expanded` on the toggle, `hidden` on the
 * region, `data-expanded` on the header being dressed - and it is the only
 * thing on the page that writes any of them.
 *
 * IT WRAPS ITS CONTENT (TOR-222). A disclosure is a BOX holding a header and a
 * region, and the box is a real part: it is required at every level and the
 * constructor checks that it actually holds the header, the toggle and the
 * region, and that the region is not inside the header. So a shape wired
 * wrongly throws where it can be fixed rather than looking almost right - and
 * a design can compose one, which is what `<Accordion level={…}>` below is.
 *
 * The boxes are torpeek's own elements, adopted rather than invented:
 *
 *   1  .run-row-group   holds .run-row          and .run-detail-row
 *   2  li.picker-item   holds label.picker-file and <file-detail>
 *   3  section.meta     holds h3.meta-title     and .meta-body
 *
 * NOT A CUSTOM ELEMENT, unlike torpeek's other six components, and there is
 * exactly one reason left: the box is a <li> in a list, a <section> in a panel
 * and a row-pair wrapper in a grid - three elements the page needs for their
 * own reasons, which this ADOPTS. Making it an element would mean the page
 * could no longer choose the box, and choosing the box is the whole of "usable
 * where we want". (The two older reasons are gone: `subgrid` was removed from
 * the row in TOR-221 and measured out at 0.0000px of column drift, and "no box
 * holds both" was simply wrong about the <li>.)
 */
/**
 * <Accordion> DRAWS ALL THREE LEVELS AND TAKES A `level` PROP.
 *
 * The version before TOR-222 drew level 3 and refused the prop, on the grounds
 * that the element carrying the rail at levels 1 and 2 was "a run-grid ROW or
 * a file-list <li> with a checkbox in it, neither of which is this component's
 * to draw". That was true of the header's CONTENTS and wrong about the
 * disclosure: the box, the header and the region are the same three things at
 * every level, and only their tag and class vary. This component draws those
 * three; the row's ten cells and the file's checkbox are passed in as
 * `summary`, the caller's, exactly as they are in the product.
 *
 * What the level decides, read off the current stylesheets rather than
 * designed (every figure measured in Chrome on the running page, and the two
 * rails pinned to the served CSS by a Go test so the table cannot drift):
 *
 * | | 1 - run | 2 - file | 3 - meta |
 * |---|---|---|---|
 * | box | `.run-row-group` | `li.picker-item` | `section.meta` |
 * | header (dressed) | `.run-row` | `label.picker-file` | `h3.meta-title` |
 * | open ground | `--accent-dim` rgb(9,58,64) | none | none |
 * | open rail | 3px inset `--accent` | 2px inset `--accent` | none |
 * | toggle type | 13.1px, opacity 1 | 14px, opacity 1 | 12.8px, opacity .75 |
 * | region | no box of its own | 1px `--rule` on the left, indented | padded, no border |
 *
 * So the depth is how loudly the open state is dressed: the rail thins
 * 3px -> 2px -> none and the one filled ground appears once, at the top. At
 * level 3 nothing is dressed, and the LEVEL decides that rather than the
 * caller - no prop can make it dress anything.
 *
 * The TRIANGLE is identical at all three levels - it was three copies of the
 * same rule before this component existed, each with a comment saying it was
 * deliberately the same mark as its neighbour's. One rule now
 * (`.disclosure-mark`, base.css), and the constructor is what puts the class
 * on. Its measured widths differ (9.18 / 9.80 / 8.95px) only because `.7em`
 * resolves against each level's own font size.
 */
export interface AccordionProps {
  /**
   * Which of the three depths this is: 1 a torrent's line, 2 a video file's
   * block, 3 the Metadata block inside it. It decides the box's, the header's
   * and the region's tag and class, whether the toggle IS the mark (level 2
   * only), and whether the header is dressed at all.
   *
   * There are three and no fourth. A level is a real depth in this UI, not a
   * scale to extend on spec - a fourth would mean the page grew a fourth
   * nested disclosure, and the class refuses anything else.
   *
   * @default 3
   */
  level?: 1 | 2 | 3;

  /**
   * The toggle's own text. At level 1 it is the torrent's name (rendered into
   * `.run-name`), at level 3 it is "Metadata" (into `.meta-toggle-label`).
   *
   * IGNORED AT LEVEL 2, whose toggle is a bare triangle button with no text:
   * the visible name there belongs to the checkbox's `<label>`, and a control
   * announced as "button" with nothing after it is one nobody reading by ear
   * can act on - so pass `toggleLabel` for its aria-label instead.
   *
   * @default "Metadata"
   */
  label?: string;

  /**
   * The toggle's accessible name at level 2, where it has no visible text.
   * The product uses "Frames and metadata for <file name>".
   */
  toggleLabel?: string;

  /**
   * Whatever else belongs in the HEADER beside the toggle: the ten run cells
   * at level 1, the checkbox and the file's name, size and summary at level 2,
   * nothing at level 3.
   *
   * It goes in the header rather than the region because the header IS the row
   * at the outer two levels - since TOR-182 a file's row is that file's title
   * line, and the resolution and frame count it carries are a COLUMN of the
   * list rather than a line inside the thing it opens.
   */
  summary?: React.ReactNode;

  /**
   * Extra props for the header element - notably the click handler at levels 1
   * and 2, where the whole row is the target. See `onToggle`.
   */
  summaryProps?: Record<string, unknown>;

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
   * redraw and relocates a row's whole box, which takes the subtree out of the
   * document and puts it back - measured, `isConnected` goes false then true
   * on the box and on both custom elements inside it - and all three levels
   * come back open, because the answer was never in the DOM to begin with.
   *
   * @default false
   */
  open?: boolean;

  /**
   * Called when the TOGGLE BUTTON is activated. Put the `open` prop where this
   * asks.
   *
   * RIGHT AT LEVEL 3, AND NOT WHAT THE OUTER TWO DO. The other two levels
   * listen on the ROW - a click anywhere in a torrent's line opens it - each
   * with an exception of its own, and one of them is load-bearing: that
   * listener must stay on `.run-row` and never go on the BOX, or every click
   * inside an open detail (a checkbox, a thumbnail, Compare) collapses the
   * thing being used. That is why the component owns no listener at all, and
   * why the box existing is not the same question as the box listening.
   *
   * Reproducing level 1 or 2: put your handler on the header through
   * `summaryProps` and leave this prop off.
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
 * The class itself, for a design driving a disclosure over markup it built
 * rather than over this bridge's.
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
 * flag changes. It reads nothing back: not a field, not the DOM. And it asks
 * the DOM exactly one question - whether the box contains the parts - so it
 * works in a DocumentFragment that is never in the document, and keeps working
 * when the box is moved to a different parent (both measured;
 * docs/spikes/TOR-222-anywhere/ is the runnable case, on a page with none of
 * torpeek's markup or CSS).
 *
 * Declared under a different name here only so this file can describe both
 * the React door and the class without one shadowing the other.
 */
declare class AccordionElement {
  constructor(parts: {
    /** 1, 2 or 3. See AccordionProps["level"]. */
    level: 1 | 2 | 3;
    /**
     * THE BOX: the element that holds the header and the region and is neither
     * of them. Required at every level, and checked - it must contain the
     * summary, the toggle and the region, transitively. A box that does not is
     * refused, because every attribute write would still land and the mistake
     * would be invisible.
     */
    container: HTMLElement;
    /**
     * The header the toggle lives in, and the element this level's rail is
     * painted on. Required at every level; only the levels WITH a rail get
     * `data-expanded` written on it. It may BE the toggle where the header is
     * nothing but the control.
     *
     * The region must be OUTSIDE it - a region inside the thing that toggles
     * it closes on every click within it, and that is refused too.
     */
    summary: HTMLElement;
    /**
     * The control carrying `aria-expanded`, inside (or equal to) the header. A
     * real `<button>` at all three levels in the product, which is where the
     * keyboard operability comes from: Return and Space both activate it,
     * measured with real key presses at all three levels.
     */
    toggle: HTMLElement;
    /** The element the toggle opens. Its `hidden` attribute is all of collapse. */
    region: HTMLElement;
    /**
     * The element wearing the triangle; defaults to the toggle. That default
     * is right at level 2, where `.picker-open` IS a triangle-sized button,
     * and wrong at 1 and 3, where the mark is a fixed-width span inside the
     * button so the label starts at the same x whichever way it points.
     *
     * The class adds `.disclosure-mark` to it once, idempotently, and keeps no
     * reference: a preview card that carries the class statically is unchanged.
     */
    mark?: HTMLElement;
  });

  /**
   * Write the open state out. The one writer of those three attributes, and
   * the state arrives as an argument every time - there is no field to keep it
   * in. Whether the header is dressed comes from the LEVEL, so level 3 writes
   * two attributes and the outer two write three.
   */
  apply(expanded: boolean): void;
}
