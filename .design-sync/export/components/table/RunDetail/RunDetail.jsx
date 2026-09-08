/* RunDetail - everything a torrent's expanded row says about THE RUN.
 *
 * A BRIDGE, NOT AN IMPLEMENTATION - the rule FramePanel.jsx states at length
 * and this file does not repeat. The behaviour (the header, the error line,
 * the confirmed name, sending the .torrent to a watch directory, and the
 * top-up/retry offer with the price the server computed) lives in
 * run-detail.js beside this file, which is torpeek's own shipped source,
 * verbatim.
 *
 * THE IMPORT BELOW IS THE WHOLE MECHANISM: the platform COMPILES the bundle
 * from these sources, so the element's code has to arrive through this import
 * to be in it. Importing for the side effect is enough - run-detail.js ends
 * in customElements.define("run-detail", RunDetail). That import also brings
 * <file-list> and, through it, <file-detail>: this element creates a
 * <file-list> in its own build() and appends it, so the whole three-deep tree
 * arrives with this one line.
 *
 * ---------------------------------------------------------------------------
 * THIS ELEMENT TAKES NO CHILDREN, and that is the difference between the two
 * halves of this export rather than an omission here.
 *
 * <run-table>, <compare-dialog> and <frame-panel> keep their markup
 * declarative in index.html, so a design may compose children in JSX and they
 * survive. This element does not: build() runs
 *
 *     this.innerHTML = '<div class="run-detail">' + DETAIL + "</div>";
 *
 * on connect, because there is one detail per torrent and index.html has
 * never held it. Anything passed as a child is destroyed by that line. So
 * this component renders the element EMPTY and lets it fill itself, and the
 * fixed sample of what it fills itself with is RunDetail.html beside this
 * file - which is also the markup a design should copy.
 *
 * IN REAL USE A PARENT MOUNTS THIS. <run-table>'s newRow() hands out a
 * colspanned `detailCell` and app.js mounts one of these in it; nothing on
 * the page puts a <run-detail> anywhere else. Rendered on its own it is a
 * detail with no row above it and no table around it - which also costs it
 * its own ground and accent rail, since those come from
 * `.run-table .run-detail-cell`, a selector that requires the table's class.
 * The card carries that ancestry for exactly this reason.
 * ---------------------------------------------------------------------------
 */
import "./run-detail.js";
import { useEffect, useRef } from "react";

export function RunDetail({ entry }) {
  const host = useRef(null);

  // The one door there is, and it needs torpeek's own run entry: bind(entry)
  // hands the record to this element and to the file list inside it, and
  // syncDetail() draws every line of it from state. An entry is assembled out
  // of state.js's newRunState, <run-table>'s row parts and app.js's detail
  // half, so a design cannot mint one - leave `entry` undefined and the
  // element stays the empty shell it builds, which is what it looks like
  // before its torrent has said anything.
  //
  // Six services (url, post, log, showError, redraw, cancelRun) must have
  // been injected by run-detail.js's setServices() before this runs;
  // syncDetail() ends in refreshAgain(), which is a GET against a torpeek
  // server.
  useEffect(() => {
    const el = host.current;
    if (!el || !entry) return;
    el.bind(entry);
    el.syncDetail();
  }, [entry]);

  return <run-detail ref={host}></run-detail>;
}

export default RunDetail;
