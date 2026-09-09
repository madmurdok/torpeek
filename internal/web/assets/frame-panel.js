// THE FRAME PANEL, as a custom element (TOR-192).
//
// It is the first element in the batch deliberately, because it is the most
// self-contained thing on the page: a <dialog> showing one frame, with its own
// zoom state and its own key handling. It touches a run only to be GIVEN a
// frame - `panel.open(src, caption)` is its whole API, and before this split
// the same job was done by a module-level openLightbox() reaching into a
// module-level `el` table and a module-level `lb` object. Nothing outside
// reached into the panel's internals even then (there was not one el.lightbox*
// use anywhere else in app.js), which is exactly why it is the cheapest place
// to find out whether `class X extends HTMLElement` is right for this
// codebase.
//
// ---------------------------------------------------------------------------
// NO SHADOW DOM, AND THE MARKUP STAYS IN index.html. Both halves are decisions
// with consequences, so both are stated rather than left to be inferred.
//
// Shadow DOM would isolate this panel's styles, and the isolation is not what
// it costs. Custom properties DO inherit across a shadow boundary, so
// tokens.css's :root would still reach in and --accent, --edge and --scrim
// would all resolve. What would NOT reach in are the RULES that use them:
// .lightbox-view's `overflow: hidden` (rule 4's first half), the bracket
// pseudo-elements, the two scrims TOR-122 requires, every cursor the zoom
// states declare. Getting them back inside a shadow root means one of two
// things, and both are worse than not having the boundary:
//
//   - duplicating framepanel.css into a template literal here, which gives
//     the panel's arithmetic TWO homes. TOR-177's guard exists precisely
//     because that arithmetic drifted once already, and stylesheet_test.go
//     reads the SERVED stylesheet - a second copy in a string is a copy no
//     guard is looking at.
//   - fetching framepanel.css at runtime and adopting it with
//     `new CSSStyleSheet().replace(...)`, which is a build step's work done
//     at load time, in a project whose whole premise is that there is no
//     build step (TOR-190 chose flat <link>s over @import for the same
//     reason).
//
// There is a third cost, smaller but real: showModal() puts a <dialog> in the
// top layer and ::backdrop styles it there. That works in light DOM with no
// thought at all, and every additional boundary is one more thing to reason
// about for a panel that has no encapsulation problem to solve - nothing on
// this page styles .lightbox* except framepanel.css.
//
// The markup stays declarative in index.html for a related reason: the
// comments explaining the chamfer, the scrims and the bracket placement sit
// NEXT TO the elements they explain. Moving that DOM into a template literal
// would move those explanations into a string, where they read as prose about
// code rather than as notes on markup. So the element owns BEHAVIOUR, STATE
// and LIFECYCLE; the page owns structure.
//
// THAT IS THE PATTERN for the elements that follow (TOR-193's compare dialog,
// TOR-194's table, TOR-195's detail tree): wrap the existing markup, find your
// own parts inside yourself in connectedCallback, keep every listener on
// instance-bound handlers so disconnectedCallback can take them off again, and
// expose one method per thing the outside is allowed to ask for. Light DOM
// until something on the page actually collides.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// THE FRAME, FULL SIZE (TOR-172): FIT, 100%, AND A PAN THAT CANNOT LEAK.
//
// The four rules the project owner stated, and where each one lives:
//
//   1. A picture that fits at 100% is drawn at 100%, and NEVER upscaled - a
//      small frame stays small. That is the 1 in layout()'s
//      Math.min(1, ...): the fit factor is capped at 1, so a 320-wide frame
//      in a 1300-wide panel is drawn 320 wide, in a panel that shrinks to
//      meet it rather than a picture blown up to fill the panel.
//   2. A picture that does not fit is scaled down to fit. The same
//      Math.min(1, ...), taking the smaller of the two axes' ratios so the
//      whole of it lands inside the window.
//   3. A click goes to 100%, and then it pans - with the arrow keys, or by
//      moving the mouse. This is the COMMON case, not the rare one: frames
//      come out at the video's own resolution, so 1080 tall and up is the
//      ordinary size of the thing being opened, and the acceptance torrent's
//      own file is 1440x1080.
//   4. It never spills outside the panel. TWO mechanisms, deliberately both:
//      .lightbox-view's `overflow: hidden` (framepanel.css) makes spilling
//      structurally impossible whatever this module computes, and clampPan()
//      below keeps
//      the panel's own ground from showing as a GUTTER at an edge - which is
//      the failure overflow: hidden cannot catch, because a picture panned
//      too far leaks background IN rather than picture out.
//
// THE PANEL'S BOX IS THE PICTURE'S FIT SIZE, in both zoom states. It is set
// once per picture and does not change when the zoom does, because a <dialog>
// is centred by the UA: a box that grows on a click re-centres the whole
// panel and slides the picture out from under the eye, which is the finding
// .compare's own block is built around. So zooming changes what is drawn
// inside the window, never the window.

// How far one arrow key moves the picture. A fixed number of pixels rather
// than a fraction of the overflow, so a press means the same thing on a
// picture half again the size of the window as on one ten times its size.
const PAN_STEP = 64;

// ---------------------------------------------------------------------------
// "NOT YET" IS NOT "NEVER" (TOR-205). Full reasoning here; compare-dialog.js
// and run-table.js carry the same mechanism and point back rather than
// repeat it.
//
// THE BUG. TOR-192 established the pattern point 3 of docs/front-end.md
// records: find every part with this.querySelector in connectedCallback,
// throw by name on the first one missing. That is right about a wrapper
// deleted from index.html - a wiring error, and a page that shipped one
// deserves to fail loudly and immediately. It is wrong about a consumer
// whose HTML STREAMS: TOR-204's own probe connected this element after
// .lightbox existed but before #lightbox-img did, hit the throw, and never
// ran connectedCallback again - the element stayed dead for the life of the
// page with nothing on screen saying so. A subtree still arriving is not a
// wiring error, it is a MOMENT IN TIME, and the fix is to tell the two
// apart rather than to trade the throw away.
//
// THE MECHANISM. wire() below finds its parts the same way connectedCallback
// used to. If any are missing it no longer throws - it bails QUIETLY and
// calls awaitParts(), which creates a MutationObserver on this element's own
// childList (and subtree, since a missing part can be nested under a part
// that did arrive) and re-runs wire() on every batch. The observer is
// created ONLY on that first miss and is disconnected the instant wiring
// succeeds - so on torpeek's OWN page, where index.html is fully parsed
// before app.js's deferred `type="module"` script ever calls
// customElements.define, every upgrade here happens against a complete
// subtree and wire() finds everything on the first try: the observer is
// never created at all. Its cost - one short-lived MutationObserver per
// element instance - is paid only by a consumer that connects this element
// early.
//
// WHAT "NEVER" MEANS, AND WHY IT IS A CLOCK RATHER THAN A COUNT OF
// MUTATIONS OR document.readyState. An observer alone cannot distinguish
// "still arriving" from "never coming" - a subtree that stops changing
// fires no mutation this element is watching for, so a bail with nothing
// bounding it would not correct TOR-192's guard, it would delete it: a
// genuinely missing part (the exact typo the throw existed to catch) would
// now fail silently forever instead of loudly on the first connect.
// document.readyState "complete" was the other candidate and is wrong for
// this bug specifically: the whole scenario is a page that may already be
// fully loaded, streaming ONE element's markup in on its own schedule
// unrelated to the page's load event - readyState says nothing about
// whether THIS element's stream has finished. So the deadline below,
// SETTLE_TIMEOUT_MS, is measured from this element's own first connect, not
// from the document's.
//
// WHAT THE DEADLINE COSTS, STATED RATHER THAN HIDDEN. It is a guess. Set it
// too short and a legitimately slow stream (a large template over a bad
// connection) throws while genuinely still arriving - "not yet" misread as
// "never", which is a false failure on a page that was going to finish
// wiring itself correctly. Set it too long and a truly missing part - the
// case the throw exists for - takes that long to say so, instead of failing
// on the very next line the way it used to; a person testing a page by hand
// waits out the whole deadline before the console says anything. 10 seconds
// is chosen generously against anything this project ships (its own page
// parses whole in well under a second, and torpeek's own upgrade never
// waits at all) while still being short enough that a person driving a page
// by hand notices the console error before they have given up and moved on.
// It is not measured against any specific third-party consumer's stream,
// because none is known - a consumer with a slower one would need a larger
// number, which is why this is one named constant and not a rule baked into
// the mechanism.
//
// AND ONCE THE DEADLINE FIRES: partsNeverArrived() console.error()s the
// still-missing part names BEFORE throwing, because the throw itself does
// not behave the way the original one did. connectedCallback's throw ran
// synchronously inside a DOM reaction and could in principle be seen by
// whatever triggered the upgrade; a throw from a setTimeout callback reaches
// no caller at all - it becomes an uncaught error the browser reports on its
// own (visible in devtools, and to any global error handler or reporting
// tool a host page has wired up). The console.error is what still names the
// missing part in a build with no such handler watching.
const SETTLE_TIMEOUT_MS = 10000;
// ---------------------------------------------------------------------------

// dx/dy are which way the PICTURE moves, not which way the eye travels:
// ArrowRight means "look further right", which slides the picture left -
// hence the subtraction in panBySteps rather than an addition.
const ARROWS = {
  ArrowLeft: { dx: -1, dy: 0 },
  ArrowRight: { dx: 1, dy: 0 },
  ArrowUp: { dx: 0, dy: -1 },
  ArrowDown: { dx: 0, dy: 1 },
};

class FramePanel extends HTMLElement {
  constructor() {
    super();

    // natW/natH are the picture's own pixels; fit the scale that makes it fit;
    // boxW/boxH the window onto it (the fit size); drawW/drawH the size it is
    // actually drawn at (fit, or 1:1); x/y the pan offset, which is never
    // positive. Instance fields rather than one module-level object, which is
    // the one substantive change the element brings: two panels on a page
    // would have shared that object.
    this.natW = 0;
    this.natH = 0;
    this.fit = 1;
    this.zoomed = false;
    this.boxW = 0;
    this.boxH = 0;
    this.drawW = 0;
    this.drawH = 0;
    this.x = 0;
    this.y = 0;

    // Bound once and kept, so disconnectedCallback removes the same function
    // objects addEventListener was given. An arrow function written inline at
    // each addEventListener call would be unremovable, and the resize
    // listener is on `window` - it outlives the element unless taken off.
    this.onImgLoad = () => this.layout();
    this.onClose = () => this.reset();
    this.onImgClick = (event) => this.toggleZoom(event);
    this.onPointerMove = (event) => this.panFromPointer(event);
    this.onViewKeydown = (event) => this.viewKeydown(event);
    this.onCloseClick = () => this.dialog.close();
    this.onBackdropClick = (event) => this.backdropClick(event);
    this.onDialogKeydown = (event) => this.dialogKeydown(event);
    this.onResize = () => {
      // The fit depends on the window, so it has to be recomputed when the
      // window changes - and the pan re-clamped with it, which layout() does
      // last: widening the window shrinks the overflow, and an offset legal a
      // moment ago would now be showing a gutter.
      if (this.dialog && this.dialog.open) this.layout();
    };

    // wired is true once wire() has actually found every part and attached
    // every listener - see wire()'s own comment for why this has to be
    // checked rather than assumed the first time connectedCallback runs.
    this.wired = false;
    // The MutationObserver awaitParts() creates on a miss, and the deadline
    // timer that goes with it - both null until there is something to wait
    // for, and both cleared the moment wiring succeeds or the deadline fires.
    this.partsObserver = null;
    this.partsDeadline = null;
  }

  connectedCallback() {
    this.wire();
  }

  // wire finds this element's parts and, if every one of them exists, wires
  // it - exactly what connectedCallback used to do inline. What is different
  // (TOR-205, see this module's own block above) is what happens when a part
  // is missing: not a throw, a quiet bail into awaitParts() so this runs
  // again once the subtree changes.
  //
  // Idempotent for the same reason run-detail.js's build() has to be:
  // awaitParts()'s observer calls this again on every mutation batch until it
  // succeeds, and once it has, this must do nothing on any further call
  // (that same observer's own late-arriving callback included).
  wire() {
    if (this.wired) return;

    // Found inside THIS element rather than by document id, so the panel works
    // wherever it is placed and a second one would not steal the first one's
    // parts. The ids stay on the markup for the tests and the aria wiring.
    this.dialog = this.querySelector(".lightbox");
    this.view = this.querySelector(".lightbox-view");
    this.img = this.querySelector("#lightbox-img");
    this.caption = this.querySelector(".lightbox-caption");
    this.closeButton = this.querySelector(".lightbox-close");
    this.zoomReadout = this.querySelector(".lightbox-zoom");

    const missing = Object.entries({
      dialog: this.dialog,
      view: this.view,
      img: this.img,
      caption: this.caption,
      closeButton: this.closeButton,
      zoomReadout: this.zoomReadout,
    }).filter(([, node]) => !node).map(([name]) => name);

    // NOT YET, not never (TOR-205): the subtree this element is looking
    // inside may simply not have finished arriving. Bail quietly and wait -
    // awaitParts() is what decides how long "quietly" lasts.
    if (missing.length) {
      this.awaitParts(missing);
      return;
    }

    this.stopAwaitingParts();
    this.wired = true;

    this.img.addEventListener("load", this.onImgLoad);
    this.dialog.addEventListener("close", this.onClose);
    // The zoom click is on the PICTURE, not on the window: the close button
    // and the caption bar both sit over it and have to (see .lightbox-close's
    // own comment, TOR-122), and a handler on the window would turn a click
    // aimed at either of them into a zoom.
    this.img.addEventListener("click", this.onImgClick);
    this.view.addEventListener("pointermove", this.onPointerMove);
    this.view.addEventListener("keydown", this.onViewKeydown);
    this.closeButton.addEventListener("click", this.onCloseClick);
    this.dialog.addEventListener("click", this.onBackdropClick);
    this.dialog.addEventListener("keydown", this.onDialogKeydown);
    window.addEventListener("resize", this.onResize);
  }

  // awaitParts is "not yet", made concrete (TOR-205, full reasoning above
  // this class). Called only from wire() when it has just found a part
  // missing, and it does nothing on the second and later call while still
  // waiting - the observer it already created will call wire() again on its
  // own the moment something changes.
  awaitParts(missing) {
    if (this.partsObserver) return;
    console.error("frame-panel: waiting for " + missing.join(", ") +
      " to appear inside the element - connected before its subtree finished arriving");
    this.partsObserver = new MutationObserver(() => this.wire());
    this.partsObserver.observe(this, { childList: true, subtree: true });
    this.partsDeadline = setTimeout(() => this.partsNeverArrived(), SETTLE_TIMEOUT_MS);
  }

  // stopAwaitingParts tears down whatever awaitParts set up. Called the
  // instant wire() succeeds - so a consumer whose stream finishes normally
  // never keeps an observer or a timer running - and from partsNeverArrived
  // before it throws, so a thrown deadline cannot fire twice.
  stopAwaitingParts() {
    if (this.partsObserver) {
      this.partsObserver.disconnect();
      this.partsObserver = null;
    }
    if (this.partsDeadline != null) {
      clearTimeout(this.partsDeadline);
      this.partsDeadline = null;
    }
  }

  // partsNeverArrived is TOR-192's guard, corrected rather than deleted: a
  // part still missing SETTLE_TIMEOUT_MS after this element first connected
  // is not "not yet", it is "never" - a genuinely missing part, the exact
  // case the original throw existed for. One last wire() first, because the
  // observer's own callback and this timer can both be queued for the same
  // tick, and a wiring that in fact just succeeded must not be reported as a
  // failure.
  partsNeverArrived() {
    this.partsDeadline = null;
    this.wire();
    if (this.wired) return;

    const missing = Object.entries({
      dialog: this.querySelector(".lightbox"),
      view: this.querySelector(".lightbox-view"),
      img: this.querySelector("#lightbox-img"),
      caption: this.querySelector(".lightbox-caption"),
      closeButton: this.querySelector(".lightbox-close"),
      zoomReadout: this.querySelector(".lightbox-zoom"),
    }).filter(([, node]) => !node).map(([name]) => name);
    this.stopAwaitingParts();
    const message = "frame-panel: no " + missing.join(", ") + " inside the element, " +
      (SETTLE_TIMEOUT_MS / 1000) + "s after connecting - giving up rather than waiting forever";
    console.error(message);
    throw new Error(message);
  }

  disconnectedCallback() {
    // The window listener is the one that matters - the rest go with the DOM
    // when it does, but a resize handler holding a reference to a removed
    // element keeps it alive and keeps measuring it.
    window.removeEventListener("resize", this.onResize);
    // A panel removed from the page while still waiting for its subtree has
    // nothing left to wire - and an observer left running on a detached
    // element would keep firing for nothing.
    this.stopAwaitingParts();
    if (!this.dialog) return;
    this.img.removeEventListener("load", this.onImgLoad);
    this.dialog.removeEventListener("close", this.onClose);
    this.img.removeEventListener("click", this.onImgClick);
    this.view.removeEventListener("pointermove", this.onPointerMove);
    this.view.removeEventListener("keydown", this.onViewKeydown);
    this.closeButton.removeEventListener("click", this.onCloseClick);
    this.dialog.removeEventListener("click", this.onBackdropClick);
    this.dialog.removeEventListener("keydown", this.onDialogKeydown);
  }

  // open is the panel's whole API, and the reason the element earns its keep:
  // a caller hands it a picture, and knows nothing about zoom, pan or layout.
  //
  // Every picture opens at fit, whatever the last one was left at, and with no
  // inline size or transform carried over from it - a stale transform on a
  // fresh picture shows it already panned for the frame or two before the
  // first layout runs.
  open(src, caption) {
    // ONE MODAL AT A TIME (TOR-193 established this; compare-dialog.js's
    // open() carries the full reasoning). Two modal <dialog>s can be open
    // together - the platform allows it and this page could reach it - and
    // the result is one painted over the other. Their keys do not fight, so
    // this is a line rather than a mechanism.
    for (const other of document.querySelectorAll("dialog[open]")) {
      if (other !== this.dialog) other.close();
    }
    this.zoomed = false;
    this.fit = 1;
    this.x = 0;
    this.y = 0;
    this.img.removeAttribute("style");
    this.dialog.dataset.zoom = "fit";
    this.zoomReadout.textContent = "fit";
    this.img.src = src;
    this.caption.textContent = caption;
    this.dialog.showModal();
    // Twice on purpose: the load event is what fires for a picture arriving
    // over the wire, and this call is what covers one already decoded in the
    // cache - which is the usual case here, since the thumbnail just showed
    // it. layout is written to be safe to call twice.
    this.layout();
  }

  reset() {
    this.zoomed = false;
    this.x = 0;
    this.y = 0;
    this.img.removeAttribute("style");
  }

  // availableBox is what the STYLESHEET leaves for the picture, in px, read
  // off the element rather than recomputed here. .lightbox-view's max-width
  // and max-height subtract the panel's ring, its two label strips and their
  // gaps from 92vw/92vh; that arithmetic has exactly one home, in
  // framepanel.css, and this probe asks the browser what it came to: set an
  // absurd size, read what it was clamped to. A later change to the panel's
  // padding then needs no matching edit in this file - which is the whole
  // reason not to write `window.innerWidth * 0.92 - 38` here and let the two
  // drift apart.
  availableBox() {
    const view = this.view;
    view.style.setProperty("--lb-view-w", "100000px");
    view.style.setProperty("--lb-view-h", "100000px");
    const rect = view.getBoundingClientRect();
    return { w: rect.width, h: rect.height };
  }

  // layout decides the panel's box and the picture's drawn size from the
  // picture's own resolution and the room the stylesheet leaves. Safe to call
  // more than once and at any time: it gives up quietly on a picture whose
  // size is not known yet (the load event calls back), and it re-clamps the
  // existing pan every time, which is what makes a window resize safe.
  layout() {
    if (!this.dialog.open) return;
    const img = this.img;
    if (!img.naturalWidth || !img.naturalHeight) return;
    this.natW = img.naturalWidth;
    this.natH = img.naturalHeight;

    const avail = this.availableBox();
    // RULES 1 AND 2, IN ONE EXPRESSION. The two ratios are rule 2 - scale down
    // to fit, by whichever axis runs out first. The 1 is rule 1, and it is the
    // whole of "never upscale": without it a 320-wide frame would be drawn
    // 1300 wide and read as a blurry mistake.
    this.fit = Math.min(1, avail.w / this.natW, avail.h / this.natH);
    // A panel measured mid-open can hand back a zero, and 0/0 is NaN, which
    // would propagate into the box and the clamp and pin the picture at a size
    // no arithmetic recovers from.
    if (!(this.fit > 0)) this.fit = 1;

    this.boxW = Math.max(1, Math.round(this.natW * this.fit));
    this.boxH = Math.max(1, Math.round(this.natH * this.fit));

    // A picture that already fits has nothing to zoom TO - rule 1 forbids
    // upscaling it - so the gesture is not offered at all, and the cursor says
    // so ("none"). It is also why this state does not wear the accent: nothing
    // is live when the whole picture is already in front of you.
    const zoomable = this.fit < 1;
    if (!zoomable) this.zoomed = false;

    const scale = this.zoomed ? 1 : this.fit;
    this.drawW = Math.max(1, Math.round(this.natW * scale));
    this.drawH = Math.max(1, Math.round(this.natH * scale));

    const view = this.view;
    view.style.setProperty("--lb-view-w", this.boxW + "px");
    view.style.setProperty("--lb-view-h", this.boxH + "px");
    // Whole pixels on both, from the SAME rounded expression in the fit case
    // (scale === this.fit there), so the picture lands exactly on the window's
    // edges and no half-pixel seam of ground can show along one of them.
    img.style.width = this.drawW + "px";
    img.style.height = this.drawH + "px";

    this.dialog.dataset.zoom = !zoomable ? "none" : this.zoomed ? "full" : "fit";
    this.zoomReadout.textContent = zoomable && !this.zoomed ? "fit" : "100%";

    this.clampPan();
    this.applyPan();
  }

  // clampPan IS rule 4's second half, and the edges are where this gets got
  // wrong. The picture's top-left sits at (x, y) inside the window, so the two
  // bounds are: x <= 0, or a gutter of panel ground opens along the LEFT edge;
  // and x >= boxW - drawW, or one opens along the RIGHT. Same for y, top and
  // bottom. Nothing else may write this.x/this.y without coming through here.
  clampPan() {
    // Math.min(0, ...) on the far bound is what makes an axis with nothing to
    // pan collapse to one legal offset rather than invert its range: a picture
    // drawn NARROWER than the window has boxW - drawW > 0, and using that as a
    // lower bound would licence a positive x - a gutter down the left edge, at
    // the one size where the picture cannot cover it.
    const minX = Math.min(0, this.boxW - this.drawW);
    const minY = Math.min(0, this.boxH - this.drawH);
    this.x = Math.min(0, Math.max(minX, this.x));
    this.y = Math.min(0, Math.max(minY, this.y));
  }

  applyPan() {
    this.img.style.transform = "translate(" + this.x + "px, " + this.y + "px)";
  }

  // panFromPointer is "moving the mouse" read literally, which is what was
  // asked for and what the panel is built around: the pointer's position
  // inside the window MAPS to the pan offset, with no button held - the left
  // edge of the window shows the picture's left edge, the right edge its
  // right. It is a magnifier, not a drag. Press-and-drag is the alternative if
  // this turns out unusable in the hand; it is not what the rules say, so it
  // is not what is here.
  panFromPointer(event) {
    if (!this.zoomed) return;
    const rect = this.view.getBoundingClientRect();
    const fx = rect.width > 0 ? (event.clientX - rect.left) / rect.width : 0;
    const fy = rect.height > 0 ? (event.clientY - rect.top) / rect.height : 0;
    // A fraction held to 0..1 times a span that is never positive lands inside
    // the legal range by construction - and it still goes through clampPan,
    // because rule 4 having ONE enforcement point is worth more than saving
    // two comparisons.
    this.x = Math.round((this.boxW - this.drawW) * Math.min(1, Math.max(0, fx)));
    this.y = Math.round((this.boxH - this.drawH) * Math.min(1, Math.max(0, fy)));
    this.clampPan();
    this.applyPan();
  }

  panBySteps(dx, dy) {
    if (!this.zoomed) return;
    this.x -= dx * PAN_STEP;
    this.y -= dy * PAN_STEP;
    this.clampPan();
    this.applyPan();
  }

  // A SECOND CLICK IS WHAT RETURNS IT TO FIT. The rules left that open; this
  // is the answer, because the gesture that got you here is the one hand
  // already on the mouse, and the cursor says which way it will go (zoom-in at
  // fit, zoom-out at 100% - see framepanel.css). Zooming in from a click also
  // pans to where that click landed, so the region under the pointer is the
  // region you get, rather than the picture's top-left corner.
  toggleZoom(event) {
    if (this.fit >= 1) return;
    this.zoomed = !this.zoomed;
    if (!this.zoomed) {
      this.x = 0;
      this.y = 0;
    }
    this.layout();
    if (this.zoomed && event && typeof event.clientX === "number") this.panFromPointer(event);
  }

  viewKeydown(event) {
    // Only the window's own keys. Enter on the close button inside it fires
    // that button and keeps bubbling to here, which would zoom a panel on its
    // way shut.
    if (event.target !== this.view) return;
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      this.toggleZoom(null);
    }
  }

  backdropClick(event) {
    // A click that lands on the dialog element itself, rather than anything
    // inside it, is a click on the backdrop.
    if (event.target === this.dialog) this.dialog.close();
  }

  dialogKeydown(event) {
    const step = ARROWS[event.key];
    // Escape is deliberately not in that table: the dialog closes itself on it
    // and this handler must never take that away. Everything else belongs to
    // the page.
    if (!step) return;
    // preventDefault whether or not there is anything to pan. A modal <dialog>
    // does NOT stop the document behind it from scrolling, so an arrow key
    // this handler declines scrolls the page under the backdrop - and then
    // closing the panel leaves the reader somewhere they never asked to be.
    event.preventDefault();
    this.panBySteps(step.dx, step.dy);
  }
}

// Native, no bundler: this is the whole registration mechanism, and importing
// this module for its side effect is how app.js gets the element defined
// before the page's own <frame-panel> is upgraded.
customElements.define("frame-panel", FramePanel);

// THE SURFACE IS THE CLASS, AND NOTHING ELSE (TOR-209). PAN_STEP and ARROWS
// used to be exported beside it and were imported by NOBODY - checked across
// the whole repository: they are read only at panBySteps and the keydown
// handler above, and app.js takes this module with a bare
// `import "./frame-panel.js"` for the side effect on the line before this
// comment's own subject. So this narrows a DEAD export, which is a thing this
// module wants on its own terms; it is not the front end being reshaped to
// suit a consumer, and .design-sync/NOTES.md's rule against that still holds.
//
// It has a consequence for the design export worth knowing before adding a
// name here: Claude Design's checker indexes a component module's named
// exports as COMPONENTS, so every extra name in this block becomes an entry a
// design agent is offered and can do nothing with. PAN_STEP and ARROWS were
// two such entries on the smallest element in the project.
export { FramePanel };
