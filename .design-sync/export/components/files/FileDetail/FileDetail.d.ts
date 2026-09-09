/**
 * FileDetail - everything one video file of one torrent has to show.
 *
 * Its metadata behind its own disclosure, the contact-sheet link, Regenerate
 * and Compare, its heartbeat line, the reach strip and its swarm chip, and the
 * frame grid with its four cell states. One instance per video row of one
 * torrent, mounted as a SIBLING of that row inside the row's own <li>. The
 * behaviour is a custom element, <file-detail>, registered by the bundle; this
 * is the React door onto it.
 *
 * WHAT A DESIGN IS GIVEN: a fixed sample, in FileDetail.html - and the row
 * above it, because since TOR-182 the row IS this file's title line.
 */
export interface FileDetailProps {
  // Deliberately empty. mount(entry, index, row) is the only way to fill this
  // element and its third argument is a bundle of live elements from a row
  // <file-list> built, so there is no prop a wrapper could translate into it.
}

export declare function FileDetail(props?: FileDetailProps): JSX.Element;
export default FileDetail;

/**
 * The element the bundle registers.
 *
 * Its nine injected services (url, post, del, log, showError, began, intake,
 * openFrame, openCompare) must be supplied by file-detail.js's setServices()
 * before mount(), which reads intake().countText on the way in. openFrame and
 * openCompare are the other two elements' own doors: the frame panel and the
 * compare dialog are ONE each on the page, not one per file.
 *
 * THERE IS NO disconnectedCallback, deliberately: <run-table>'s re-sort moves
 * a run's rows on every redraw, so everything in them is disconnected and
 * reconnected dozens of times a second on a live run. A teardown here would
 * kill Regenerate, Compare and every thumbnail on the first event.
 */
export declare class FileDetailElement extends HTMLElement {
  /**
   * Stage one: put the empty, `hidden` slot inside this element and mint the
   * id the row's disclosure names. Idempotent, and public because
   * <file-list> calls it before appending the element - createElement
   * constructs a custom element synchronously, but connectedCallback is a
   * reaction, and regionId is read in the same turn.
   */
  build(): void;
  /** The id the ROW's disclosure names in aria-controls. */
  readonly regionId: string;
  /**
   * Stage two: fill the slot, and hand back this file's whole entry - the data
   * half state.js owns (newFileState) plus the one field that names this
   * element. `row` is the bundle <file-list> built: its <li>, its disclosure,
   * its name and its summary span.
   *
   * Everything inside the detail is built and filled whether or not the file
   * is expanded; only the slot's `hidden` attribute decides what is on screen.
   * A frame_ready for a collapsed file still appends its figure, so expanding
   * later shows everything that arrived meanwhile - nothing is lazy past this
   * point and there is nothing to replay.
   */
  mount(entry: unknown, index: number, row: unknown): unknown;
  /**
   * Open or close this file's detail. ONE FILE'S DETAIL AT A TIME: opening one
   * closes every sibling. That bound is on THIS level and not on the row above
   * it, where several torrents may be open at once - a season pack holds
   * twenty-five video files, and two expanded torrents each allowing
   * twenty-five open frame grids would put fifty grids on one page.
   */
  toggle(): void;
  /** Write whether this file is expanded - the row's <li>, its disclosure's aria-expanded, and the slot. */
  setFileExpanded(expanded: boolean): void;
  /** The metadata accordion one level down. Starts closed, with no auto-expand exception. */
  setMetaExpanded(expanded: boolean): void;
  /** Keep the ROW's summary current: the resolution, and frames against what was planned. */
  updateFileSummary(): void;
  /** Draw the specs and the two track groups from fentry.media. */
  renderFileMeta(): void;
  /** Draw the heartbeat line - frames, bytes, peers - or take it off screen when there is none. */
  renderFileProgress(): void;
  /** Rebuild the grid AND restate the row's count of it. The single hook events.js names. */
  renderFileGrid(): void;
  /** Draw where in the file this set's frames were taken from, plus the swarm chip. */
  renderReach(sets: unknown[]): void;
  /** Update the swarm chip alone, for a heartbeat that moved no file's claims. */
  renderAvail(): void;
  /** What a finished file redraws: the heartbeat goes, the sheet appears, the other sets are read off disk. */
  renderFileDone(): void;
  /** Replace this file's whole detail from what the server read back after a Clear frames. */
  afterClear(cleared: unknown): void;
}

declare global {
  namespace JSX {
    interface IntrinsicElements {
      /**
       * The custom element. A design should use <FileDetail> instead - this
       * declaration exists so the wrapper's own JSX type-checks.
       */
      "file-detail": React.DetailedHTMLProps<React.HTMLAttributes<HTMLElement>, HTMLElement>;
    }
  }
}
