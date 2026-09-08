/* RunTable - torpeek's torrent table, and the accordion each row opens.
 *
 * A BRIDGE, NOT AN IMPLEMENTATION - the same rule FramePanel.jsx states at
 * length and this file does not repeat. The behaviour (nine columns,
 * client-side sorting, drag-a-border column widths, the one-second stall
 * ticker, the accordion) lives in run-table.js beside this file, which is
 * torpeek's own shipped source, verbatim.
 *
 * THE IMPORT BELOW IS THE WHOLE MECHANISM: the platform COMPILES the bundle
 * from these sources, so the element's code has to arrive through this import
 * to be in it. Importing for the side effect is enough - run-table.js ends in
 * customElements.define("run-table", RunTable).
 *
 * THERE IS NO EFFECT IN THIS WRAPPER, and that is a decision rather than an
 * omission. The element's API is entry-shaped - newRow(), bindRow(entry),
 * syncRow(entry), setRunExpanded(entry, expanded) - and an entry is torpeek's
 * own run record, assembled out of state.js's newRunState, this element's own
 * row parts and app.js's detail half, in that order. A wrapper cannot mint
 * one, and one that faked a subset would be a second app.js free to drift
 * from the first. So `hostRef` is the whole door for a live page, and a design
 * that wants the picture uses the markup in the card beside this file.
 *
 * The four services (toggleRun, cancelRun, setPriority, detailShown) must be
 * injected by setServices() before a row is bound; each is a POST against a
 * torpeek server, so nothing here can supply them.
 *
 * ---------------------------------------------------------------------------
 * WHY THIS SHELL HAS FOUR COLUMN HEADERS AND THE COMPONENT HAS TEN, and it is
 * the one thing to get right in this file.
 *
 * index.html ships THREE labelled headers plus the unlabelled actions one.
 * The other six - Peers, Seeds, Down, Up, Avail, Queue - are built by the
 * element itself, in connectedCallback, from run-table.js's LIVE_COLUMNS
 * (buildLiveColumnHeaders inserts them before the actions header). Nine of
 * the ten are sortable and resizable; the tenth is the actions column, which
 * carries no data-sort and never drags.
 *
 * So this markup must NOT write the six out: a shell that carried all of them
 * would give the page sixteen columns, and the colSpan the element computes
 * from `querySelectorAll("thead th").length` would be wrong with it.
 *
 * The CARD beside this file does carry all ten, and for the opposite reason -
 * a preview card runs no scripts, so nothing builds them there. The two files
 * disagree on purpose, and this is the note that says so.
 * ---------------------------------------------------------------------------
 *
 * CHILDREN ARE THE ROWS, and this element is one of the three that can take
 * them: <run-table>, <compare-dialog> and <frame-panel> keep their markup
 * declarative in index.html and never overwrite their own subtree, so a
 * design may compose static <tr>s in JSX and they survive. The other three
 * elements of this export (<run-detail>, <file-list>, <file-detail>) build
 * their own markup with innerHTML and would destroy anything passed in - see
 * their own bridges.
 *
 * Do not mix: static children AND live rows from the element's own newRow()
 * in one tbody is React and the element reconciling the same node list.
 */
import "./run-table.js";

export function RunTable({ hostRef, empty = true, children }) {
  return (
    <run-table ref={hostRef}>
      <section id="runs" className="runs">
        <h2 className="runs-title">Torrents</h2>
        <div className="run-table-wrap">
          <table id="run-table" className="run-table">
            <thead>
              <tr>
                <th scope="col" data-sort="name" tabIndex={0} role="button" aria-sort="none">Name</th>
                <th scope="col" data-sort="when" tabIndex={0} role="button" aria-sort="descending">Added</th>
                <th scope="col" data-sort="status" tabIndex={0} role="button" aria-sort="none">Status</th>
                {/* The six live columns are inserted between these two by
                    buildLiveColumnHeaders() - see this file's own heading. */}
                <th scope="col" className="run-actions-header" aria-hidden="true"></th>
              </tr>
            </thead>
            <tbody id="run-list">{children}</tbody>
          </table>
        </div>
        {/* The one genuinely empty state: no torrents at all. The element
            hides it the moment newRow() appends a first row. */}
        <p id="run-list-empty" className="run-list-empty" hidden={!empty}>Nothing yet - paste a magnet link to start.</p>
      </section>
    </run-table>
  );
}

export default RunTable;
