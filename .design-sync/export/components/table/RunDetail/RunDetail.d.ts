/**
 * RunDetail - everything a torrent's expanded row says about THE RUN.
 *
 * The header (badge, name, Save .torrent, Cancel), the error line, the
 * torrent's confirmed name, sending the .torrent to a watch directory on the
 * host, and the top-up/retry offer with the price the server computed. One
 * instance per torrent, mounted in the colspanned cell <run-table> hands out.
 * The behaviour is a custom element, <run-detail>, registered by the bundle;
 * this is the React door onto it.
 *
 * WHAT A DESIGN IS GIVEN: a fixed sample, in RunDetail.html. Not an empty
 * shell, because an empty shell shows nothing at all - the element writes its
 * own markup with innerHTML on connect and fills it from a run entry, so a
 * <run-detail> with nothing bound to it is a blank block. And not example
 * data, because an entry cannot be minted outside a running torpeek.
 */
export interface RunDetailProps {
  /**
   * torpeek's own run entry - the record this detail draws from. When it is
   * given, this component calls `bind(entry)` and `syncDetail()`; when it is
   * not, the element stays the empty shell it builds, which is what it looks
   * like before its torrent has said anything.
   *
   * A DESIGN CANNOT SUPPLY ONE. An entry is assembled out of state.js's
   * newRunState, <run-table>'s own row parts and app.js's detail half, and
   * those three are one object by design (a test scans all three for a field
   * declared twice). syncDetail() also ends in refreshAgain(), which is a GET
   * against a torpeek server. Use the card's markup instead.
   */
  entry?: unknown;
}

export declare function RunDetail(props: RunDetailProps): JSX.Element;
export default RunDetail;

/**
 * The element the bundle registers.
 *
 * Its six injected services (url, post, log, showError, redraw, cancelRun)
 * must be supplied by run-detail.js's setServices() before any method is
 * called. setServices throws by name on a missing one rather than failing at
 * the first press of Top up.
 *
 * THERE IS NO disconnectedCallback, deliberately, and it is worth knowing
 * before designing around this element: <run-table>'s syncRow() ends with a
 * re-sort, and moving a node already in the table is a remove followed by an
 * insert - so this element is disconnected and reconnected on every redraw of
 * every row, dozens of times a second on a live run. A teardown here would
 * take Cancel, Top up and Retry off on the first event after load. build() is
 * idempotent for the same reason.
 */
export declare class RunDetailElement extends HTMLElement {
  /**
   * Put this element's markup inside it, find its parts, wire its controls and
   * create the nested <file-list>. Idempotent, and public because the element
   * that creates one calls it before appending it: createElement constructs a
   * custom element synchronously, but connectedCallback is a reaction, and
   * regionId is read in the same turn.
   */
  build(): void;
  /**
   * The id the ROW's disclosure button names in aria-controls. <run-table>
   * sets aria-expanded (the row's own state) and the page sets aria-controls
   * (the id only the detail can mint) - one attribute each, on the same
   * button.
   */
  readonly regionId: string;
  /** Hand this element the record it draws from, and the file list with it. */
  bind(entry: unknown): void;
  /** Draw every line of the detail from state. Ends by asking what a top-up would cost. */
  syncDetail(): void;
  /** Take off screen everything a run_state "reset" took out of the entry. */
  resetView(): void;
  /** Draw the .torrent link and the watch-directory group from entry.torrentURL. */
  renderTorrent(): void;
  /** The nested file list. Created by build(); never null after it. */
  files: HTMLElement;
}

declare global {
  namespace JSX {
    interface IntrinsicElements {
      /**
       * The custom element. A design should use <RunDetail> instead - this
       * declaration exists so the wrapper's own JSX type-checks.
       */
      "run-detail": React.DetailedHTMLProps<React.HTMLAttributes<HTMLElement>, HTMLElement>;
    }
  }
}
