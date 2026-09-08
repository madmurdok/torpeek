/**
 * FramePanel - torpeek's full-size frame viewer.
 *
 * A modal <dialog> showing one captured frame at fit or at 100%, with a pan
 * that cannot leak panel ground into view. The behaviour is a custom element,
 * <frame-panel>, registered by the bundle; this is the React door onto it.
 */
export interface FramePanelProps {
  /**
   * The frame to show. Any URL the page can fetch - torpeek serves its own
   * frames under /files/<id>.
   *
   * The panel never upscales: a picture smaller than the available box is
   * drawn at its own size, in a panel that shrinks to meet it. So a small
   * image is not a broken layout, it is the rule.
   */
  src: string;

  /**
   * The line under the picture. torpeek writes "<file> at <timecode>" here.
   * Empty is legitimate and draws an empty caption bar rather than removing
   * it - the bar is part of the panel's frame, not of the caption.
   */
  caption?: string;

  /**
   * Whether the panel is on screen. The panel is a MODAL dialog: while it is
   * open nothing behind it is clickable, and it sits in the browser's top
   * layer above everything on the page regardless of z-index.
   *
   * Only one modal may be open at a time in torpeek - opening this closes any
   * other open dialog, which the element enforces itself (TOR-193).
   */
  open?: boolean;

  /**
   * Called when the panel closes. It closes on Escape, on a click on the
   * backdrop, and on its own close button - all three are the element's, so
   * a design cannot suppress them and should not try. Use this to put the
   * `open` prop back to false.
   */
  onClose?: () => void;
}

export declare function FramePanel(props: FramePanelProps): JSX.Element;
export default FramePanel;

/**
 * The element the bundle registers. Present so a design can reach the panel's
 * imperative API directly if it needs to - `el.open(src, caption)` is the
 * whole of it - though the React props above are the intended door.
 */
export declare class FramePanelElement extends HTMLElement {
  /** Show a picture. Every call opens at fit, whatever the last one was left at. */
  open(src: string, caption: string): void;
  /** The <dialog> itself, for `.open` and `.close()`. */
  dialog: HTMLDialogElement;
}

declare global {
  namespace JSX {
    interface IntrinsicElements {
      /**
       * The custom element. A design should use <FramePanel> instead - this
       * declaration exists so the wrapper's own JSX type-checks.
       */
      "frame-panel": React.DetailedHTMLProps<React.HTMLAttributes<HTMLElement>, HTMLElement>;
    }
  }
}
