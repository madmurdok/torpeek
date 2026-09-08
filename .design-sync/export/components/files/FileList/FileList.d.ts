/**
 * FileList - every file the torrent holds, video or not, one row each.
 *
 * A tick starts that file on the spot; the row states what it will spend
 * before it is spent; and an un-tick means one of five different things
 * depending on what the file is doing. One instance per torrent, mounted last
 * inside that torrent's own <run-detail>. The behaviour is a custom element,
 * <file-list>, registered by the bundle; this is the React door onto it.
 *
 * WHAT A DESIGN IS GIVEN: a fixed sample, in FileList.html, and it is FOUR
 * lists rather than one - see the card, and this file's `entry` note, for why
 * a single list cannot carry all five verdicts.
 */
export interface FileListProps {
  /**
   * torpeek's own run entry - the record this list draws from. When it is
   * given, this component calls `bind(entry)` and `rebuild()`; when it is
   * not, the element stays the empty shell it builds, with `.picker` hidden -
   * which is exactly what a torrent whose metadata has not arrived looks
   * like, and a legitimate state rather than a broken one.
   *
   * A DESIGN CANNOT SUPPLY ONE. Beyond newRunState's own fields, the list
   * reads FOUR SETS that only the server can fill, and they are what decide
   * every box's state:
   *
   *   entry.picked      what this row has asked for (cumulative)
   *   entry.deferred    ticked while the row was ALREADY fetching -> "drop"
   *   entry.fetching    the pass the engine is holding now      -> "stop"
   *   entry.narrowable  the pass chosen but not yet handed over -> "narrow"
   *
   * plus entry.unticked, the only tick state on the page the server does not
   * own, which is what reveals Clear frames. Three of those four are mutually
   * exclusive by ROW STATE - fetching is running-only, narrowable is
   * queued-or-parked-only - which is why one list cannot show every verdict.
   */
  entry?: unknown;
}

export declare function FileList(props: FileListProps): JSX.Element;
export default FileList;

/**
 * The element the bundle registers.
 *
 * Its seven injected services (url, post, del, log, showError, count,
 * cancelRun) must be supplied by file-list.js's setServices() before any
 * method is called. `count` is the OTHER input every price on this list
 * follows, beside the ticks: it is what the intake line currently asks for,
 * and a person may well be adjusting it while reading the list.
 *
 * THERE IS NO disconnectedCallback, deliberately: <run-table>'s re-sort moves
 * a run's rows on every redraw, so everything in them is disconnected and
 * reconnected dozens of times a second on a live run. A teardown here would
 * kill every tick box on the first event. build() is idempotent for the same
 * reason - a second `innerHTML` would throw away every frame grid and every
 * open disclosure inside the list.
 */
export declare class FileListElement extends HTMLElement {
  /** Put this element's markup inside it and wire the two controls in its head. Idempotent. */
  build(): void;
  /** Hand this element the record it draws from. */
  bind(entry: unknown): void;
  /**
   * Build a row per file, from entry.fileList and entry.videos. Called ONLY
   * when the list actually changed shape (entry.fileListSig) - which since
   * TOR-182 is a correctness rule, not a courtesy about scroll positions: a
   * rebuild detaches every frame grid and every open disclosure on the row.
   */
  rebuild(): void;
  /** Decide what the row's state earns: whether the list shows at all, and which controls do. */
  syncFileList(): void;
  /** Re-state every figure on a list that is on screen, and nothing else. */
  reprice(): void;
  /** Take the whole list off screen - the DOM half of a run_state "reset". */
  reset(): void;
  /** Redraw every open file's swarm chip from a fresh, torrent-wide reading. */
  refreshSwarm(): void;
  /** Create (or find) one video file's <file-detail> and return that file's entry. */
  mountFileDetail(index: number): unknown | null;
  /** Open or close one file's detail, if it has one yet. */
  toggleFileDetail(index: number): void;
}

declare global {
  namespace JSX {
    interface IntrinsicElements {
      /**
       * The custom element. A design should use <FileList> instead - this
       * declaration exists so the wrapper's own JSX type-checks.
       */
      "file-list": React.DetailedHTMLProps<React.HTMLAttributes<HTMLElement>, HTMLElement>;
    }
  }
}
