/**
 * RunTable - torpeek's torrent table.
 *
 * Nine columns over every torrent this page knows about, live or read off
 * disk, with client-side sorting, drag-a-border column widths, and an
 * accordion: each entry owns TWO adjacent <tr>s, its own line and the row its
 * detail renders in. The behaviour is a custom element, <run-table>,
 * registered by the bundle; this is the React door onto it.
 *
 * WHAT A DESIGN IS GIVEN, and why it is not an empty shell: the table IS the
 * rows, and the rows are built by JavaScript from run entries. Empty, this
 * component is a header row and one sentence. So the fixed sample lives in
 * RunTable.html - five rows chosen to differ - and this component renders the
 * shell that sample goes into.
 */
export interface RunTableProps {
  /**
   * A ref onto the <run-table> element, and the whole live door: the API is
   * newRow(), bindRow(entry), syncRow(entry) and setRunExpanded(entry,
   * expanded), all of which take torpeek's own run entry.
   *
   * THERE IS NO WAY TO MINT AN ENTRY FROM A DESIGN. It is assembled out of
   * state.js's newRunState, this element's own row parts (newRow's return
   * value) and app.js's detail half, in that order, and the three are one
   * object by design. A wrapper that faked a subset would be a second app.js
   * free to drift from the first, which is why this component translates no
   * props into calls.
   */
  hostRef?: React.Ref<RunTableElement>;

  /**
   * Whether to show #run-list-empty, the one genuinely empty state (no
   * torrents at all). True by default, because a shell with no children is
   * exactly that state. The element hides the note itself the moment newRow()
   * appends a first row.
   */
  empty?: boolean;

  /**
   * Rows, placed inside <tbody id="run-list">. <run-table> keeps its markup
   * declarative and never overwrites its own subtree, so static <tr>s written
   * in JSX survive - which is how a design draws this table. Copy the five in
   * RunTable.html rather than inventing cell values: every one of them is what
   * a state.js derivation writes, and the absences are the point.
   *
   * Do not mix static children with live rows from the element's own newRow():
   * that is React and the element reconciling one node list.
   */
  children?: React.ReactNode;
}

export declare function RunTable(props: RunTableProps): JSX.Element;
export default RunTable;

/**
 * The element the bundle registers.
 *
 * Its four injected services (toggleRun, cancelRun, setPriority, detailShown)
 * must be supplied by run-table.js's setServices() before a row is bound.
 * Each is a POST against a torpeek server, so a design cannot supply them, and
 * setServices throws by name on a missing one rather than failing at the first
 * click on a row.
 */
export declare class RunTableElement extends HTMLElement {
  /**
   * Build one entry's pair of rows, append them, and hand back every element
   * in them - the detail's own <tr> and its colspanned cell included.
   * `detailCell` is the seam: the table owns the row, the page owns what goes
   * in the cell, and this element never learns what went in.
   */
  newRow(): RunRowParts;
  /** Attach the four gestures to a row, once its entry exists. */
  bindRow(entry: unknown): void;
  /** Redraw one torrent's own line from its entry. Ends with a re-sort. */
  syncRow(entry: unknown): void;
  /**
   * Open or close one row. The only function that may put a detail on screen
   * or take it off - and SEVERAL ROWS MAY BE OPEN AT ONCE, deliberately:
   * closing one to open another would destroy work in progress on a live run.
   */
  setRunExpanded(entry: unknown, expanded: boolean): void;
  /**
   * The number of header cells a detail row spans. TEN today: the nine
   * sortable columns plus the unlabelled actions one. Read off
   * `querySelectorAll("thead th").length` after the six live headers are
   * built, never written as a literal.
   */
  columns: number;
}

/** What newRow() hands back. Every field is a row element; none is a detail. */
export interface RunRowParts {
  rowEl: HTMLTableRowElement;
  rowBadge: HTMLElement;
  rowName: HTMLElement;
  rowMeta: HTMLElement;
  rowProgress: HTMLElement;
  rowWhen: HTMLTableCellElement;
  rowCancel: HTMLButtonElement;
  rowToggle: HTMLButtonElement;
  rowPeers: HTMLTableCellElement;
  rowSeeds: HTMLTableCellElement;
  rowDown: HTMLTableCellElement;
  rowUp: HTMLTableCellElement;
  rowAvail: HTMLElement;
  rowAvailMeta: HTMLElement;
  rowAvailCell: HTMLTableCellElement;
  rowQueue: HTMLElement;
  rowQueueMeta: HTMLElement;
  rowQueueCell: HTMLTableCellElement;
  rowRaise: HTMLButtonElement;
  rowLower: HTMLButtonElement;
  detailRowEl: HTMLTableRowElement;
  /** The mount point, and the only reason the page is handed a cell at all. */
  detailCell: HTMLTableCellElement;
}

declare global {
  namespace JSX {
    interface IntrinsicElements {
      /**
       * The custom element. A design should use <RunTable> instead - this
       * declaration exists so the wrapper's own JSX type-checks.
       */
      "run-table": React.DetailedHTMLProps<React.HTMLAttributes<HTMLElement>, HTMLElement>;
    }
  }
}
