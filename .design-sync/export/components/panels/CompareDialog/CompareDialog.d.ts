/**
 * CompareDialog - two result sets of one film, compared by FLIPPING.
 *
 * A modal <dialog> holding both encodes on one rectangle, swapped by
 * visibility so nothing about the picture's size or position changes when the
 * arm does. The behaviour is a custom element, <compare-dialog>, registered by
 * the bundle; this is the React door onto it.
 *
 * WHY FLIPPING RATHER THAN TILING, because it decides every rule below: the
 * same frame, in the same screen position, one keypress apart, makes a
 * difference visible that a side-by-side grid hides - the eye compares against
 * its own afterimage rather than across a gap.
 */
export interface CompareDialogProps {
  /**
   * Whether the dialog is on screen. It is a MODAL dialog: while it is open
   * nothing behind it is clickable, and it sits in the browser's top layer
   * above everything on the page regardless of z-index.
   *
   * Only one modal may be open at a time in torpeek, which the element
   * enforces itself by closing every other open <dialog> - established by
   * measurement rather than read off the spec: two modal dialogs CAN be open
   * together, and this page could reach that state because open() awaits a
   * fetch and a click on a frame during that await opens the frame panel
   * underneath.
   */
  open?: boolean;

  /**
   * The torrent whose file the dialog was opened from. Together with `index`
   * it decides what the dialog opens ON: the two result sets of THAT file come
   * first when there are two - the pairing a person who just regenerated a
   * file is asking for - and otherwise that file against whatever else is on
   * disk. Both are only a default; the two pickers are then free.
   *
   * WITH NO infohash NOTHING OPENS, and that is the truth rather than a guard.
   * The element's only entry point is open(infohash, index), which FETCHES:
   * GET /compare/sets for the pickers, then GET /compare for the pairing. A
   * design with no torpeek server behind it has nothing to show, so it should
   * use the markup in CompareDialog.html, which carries a whole comparison
   * statically.
   */
  infohash?: string;

  /** The torrent index of the file. Defaults to 0. */
  index?: number;

  /**
   * Called when the dialog closes. It closes on Escape, on a click on the
   * backdrop, and on its own close button - all three are the element's, so a
   * design cannot suppress them and should not try. Use this to put the `open`
   * prop back to false.
   */
  onClose?: () => void;
}

export declare function CompareDialog(props: CompareDialogProps): JSX.Element;
export default CompareDialog;

/**
 * The element the bundle registers.
 *
 * Its two injected services must be supplied by compare-dialog.js's
 * setServices({url, log}) before open() is called: url() reads
 * document.baseURI and the page's access token, so it belongs to the page's
 * bootstrap, and log() writes to the activity log element, which is app.js's.
 * With neither injected, open() throws.
 */
export declare class CompareDialogElement extends HTMLElement {
  /**
   * Read what there is to compare, choose the two arms, show the dialog and
   * load the comparison. Async, because it is two GETs.
   */
  open(infohash: string, index: number): Promise<void>;
  /** Show the other arm. The picture does not move; only which arm is visible changes. */
  flip(): void;
  /**
   * Move along the film. It WRAPS rather than stopping at the ends: the
   * flipbook is short - eight positions, twenty at most - and a person walking
   * it with one finger should not have to turn round.
   */
  step(delta: number): void;
  /** The <dialog> itself, for `.open` and `.close()`. */
  dialog: HTMLDialogElement;
}

declare global {
  namespace JSX {
    interface IntrinsicElements {
      /**
       * The custom element. A design should use <CompareDialog> instead - this
       * declaration exists so the wrapper's own JSX type-checks.
       */
      "compare-dialog": React.DetailedHTMLProps<React.HTMLAttributes<HTMLElement>, HTMLElement>;
    }
  }
}
