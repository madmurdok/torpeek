/* FileList - every file the torrent holds, and what a tick spends.
 *
 * A BRIDGE, NOT AN IMPLEMENTATION - the rule FramePanel.jsx states at length
 * and this file does not repeat. The behaviour (a tick that starts that file
 * on the spot, a price per row before it is spent, Select all with its arming
 * step, the five verdicts an un-tick can carry, and Clear frames) lives in
 * file-list.js beside this file, which is torpeek's own shipped source,
 * verbatim.
 *
 * THE IMPORT BELOW IS THE WHOLE MECHANISM: the platform COMPILES the bundle
 * from these sources, so the element's code has to arrive through this import
 * to be in it. Importing for the side effect is enough - file-list.js ends in
 * customElements.define("file-list", FileList) - and it brings <file-detail>
 * with it, since this element creates one per video row.
 *
 * ---------------------------------------------------------------------------
 * THIS ELEMENT TAKES NO CHILDREN. build() runs `this.innerHTML = LIST` on
 * connect, and renderFileList() then replaces the whole <ul>, so anything
 * passed as a child is destroyed. Only <run-table>, <compare-dialog> and
 * <frame-panel> keep their markup declarative and can take children.
 *
 * IN REAL USE A PARENT MOUNTS THIS. <run-detail>'s build() creates one with
 * document.createElement, calls build() on it and appends it as the LAST
 * block in the run's own detail - because since TOR-182 each file's own
 * detail hangs off that file's row inside this list, so there is nothing left
 * to put after it. Nothing on the page puts a <file-list> anywhere else.
 *
 * The fixed sample is FileList.html beside this file, and it is FOUR lists
 * rather than one: the five un-tick verdicts are partitioned by the row's own
 * state (fetching is running-only, narrowable is queued/parked-only, the
 * clear offer is settled-only), so no single list can carry them all without
 * faking a state. That card is the markup a design should copy.
 * ---------------------------------------------------------------------------
 */
import "./file-list.js";
import { useEffect, useRef } from "react";

export function FileList({ entry }) {
  const host = useRef(null);

  // The one door there is, and it needs torpeek's own run entry: bind(entry)
  // hands the record over, rebuild() builds a row per file from
  // entry.fileList and entry.videos, and syncFileList() decides what the
  // row's state earns. An entry is assembled out of state.js's newRunState,
  // <run-table>'s row parts and app.js's detail half, so a design cannot mint
  // one - leave `entry` undefined and the element stays the empty shell it
  // builds, with .picker hidden, which is exactly what a torrent whose
  // metadata has not arrived looks like.
  //
  // Seven services (url, post, del, log, showError, count, cancelRun) must
  // have been injected by file-list.js's setServices() before this runs:
  // every price on the list follows count(), which is what the intake line
  // currently asks for.
  useEffect(() => {
    const el = host.current;
    if (!el || !entry) return;
    el.bind(entry);
    el.rebuild();
  }, [entry]);

  return <file-list ref={host}></file-list>;
}

export default FileList;
