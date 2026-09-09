/* FileDetail - everything one video file of one torrent has to show.
 *
 * A BRIDGE, NOT AN IMPLEMENTATION - the rule FramePanel.jsx states at length
 * and this file does not repeat. The behaviour (the metadata accordion, the
 * contact-sheet link, Regenerate and Compare, the heartbeat line, the reach
 * strip and its swarm chip, and the frame grid with its four cell states)
 * lives in file-detail.js beside this file, which is torpeek's own shipped
 * source, verbatim.
 *
 * THE IMPORT BELOW IS THE WHOLE MECHANISM: the platform COMPILES the bundle
 * from these sources, so the element's code has to arrive through this import
 * to be in it. Importing for the side effect is enough - file-detail.js ends
 * in customElements.define("file-detail", FileDetail).
 *
 * ---------------------------------------------------------------------------
 * THIS ELEMENT TAKES NO CHILDREN, and it has TWO STAGES, which is what makes
 * it different from the two above it:
 *
 *   build()  runs with the ROW and does one thing - puts an empty
 *            `<div class="file-detail" hidden>` inside this element and mints
 *            the id the row's disclosure names in aria-controls. That has to
 *            happen with the row, because a disclosure control has to name
 *            the region it opens and a region minted later is one the control
 *            pointed at nothing for.
 *   mount()  runs the first time the file says anything and fills that slot,
 *            which is when there is anything to fill it with. Before a tick a
 *            file has no metadata, no plan and no frames, and a panel of empty
 *            headings is worse than a row that does not open yet.
 *
 * Both write into this element with innerHTML, so anything passed as a child
 * is destroyed. Only <run-table>, <compare-dialog> and <frame-panel> keep
 * their markup declarative and can take children.
 *
 * IN REAL USE A PARENT MOUNTS THIS. <file-list>'s renderFileList creates one
 * per VIDEO row and appends it as a SIBLING of the row inside the row's <li> -
 * never as its descendant, for the same reason a run's detail is a separate
 * <tr>: a click on a thumbnail or on Regenerate must not close the thing it is
 * being used on.
 *
 * AND IT IS HANDED FOUR ELEMENTS OF THAT ROW - the <li>, the disclosure, the
 * name and the summary span - because since TOR-182 THE ROW IS THIS FILE'S
 * TITLE LINE. So this element cannot be rendered alone and look right: it
 * would be a detail with no name on it. The card beside this file carries the
 * one <li> that owns it, and no more of the list than that.
 * ---------------------------------------------------------------------------
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
import "../../../modules/file-detail.js";

export function FileDetail() {
  // NO EFFECT AND NO PROPS, and that is the honest shape rather than a stub.
  // mount(entry, index, row) is the only way to fill this element, and its
  // third argument is a bundle of live elements from a row <file-list> built -
  // so there is nothing a wrapper can pass it. A design uses the card's
  // markup; a live page lets <file-list> mount it.
  //
  // Nine services (url, post, del, log, showError, began, intake, openFrame,
  // openCompare) must have been injected by file-detail.js's setServices()
  // before mount(): it reads intake().countText for the Regenerate field on
  // the way in.
  return <file-detail></file-detail>;
}

export default FileDetail;
