// THE COMPARE DIALOG, as a custom element (TOR-193).
//
// The second element, and it follows TOR-192's frame panel rather than
// inventing the pattern again: light DOM, the markup left declarative in
// index.html and wrapped, parts found with this.querySelector in
// connectedCallback, every listener an instance-bound field so
// disconnectedCallback can take it off, and one method - open(infohash,
// index) - as the whole API the page is allowed to call.
//
// WHAT IS DIFFERENT FROM THE PANEL, and it is the only reason this was not a
// mechanical repeat: the panel needed nothing from app.js, and this needs
// five things. Three of them are pure - shortId, basename, timecode - and now
// live in state.js, which is what that file is for; timecode and basename
// moved there under this ticket, since two modules read them and neither
// touches the DOM. The other two cannot move and are INJECTED instead:
//
//   - url() reads document.baseURI and the page's TOKEN, so it belongs to the
//     page's bootstrap, not here. Every request this element makes goes
//     through it, and the reason is REQUIREMENTS.md section 3.3: the UI has to
//     work under an arbitrary base path, so a path built any other way breaks
//     behind a reverse proxy.
//   - log() writes to the activity log element, which is app.js's.
//
// setServices({url, log}) is the same shape events.js's setView already uses,
// and it throws at wiring time on a missing name rather than at the first
// request - a page that forgot to wire it should fail while it is being
// written, not when someone presses Compare.
//
// NO SHADOW DOM, for the reason frame-panel.js sets out at length: custom
// properties would still inherit in, but compare.css's rules would not, and
// getting them back means either a second copy of the stylesheet in a string
// or fetching it at load time. Neither is worth a boundary this page has no
// collision to justify.

import { basename, shortId, timecode } from "./state.js";

// "NOT YET" IS NOT "NEVER" (TOR-205). frame-panel.js's own block carries the
// full reasoning for the mechanism below (wire/awaitParts/partsNeverArrived)
// and for this deadline's value, not repeated here.
const SETTLE_TIMEOUT_MS = 10000;

// The two services the page injects. Undefined until setServices runs, which
// is checked there rather than here.
let url = null;
let log = null;

const SERVICES = ["url", "log"];

function setServices(services) {
  for (const name of SERVICES) {
    if (typeof services[name] !== "function") {
      throw new Error("compare-dialog: setServices needs a " + name + "() function");
    }
  }
  ({ url, log } = services);
}

// ---------------------------------------------------------------------------
// COMPARING TWO RUNS BY FLIPPING (TOR-109).
//
// FLIPPING BEATS TILING, and that is the whole design rather than a
// preference: the same frame, in the same screen position, one keypress
// apart, makes a difference visible that a side-by-side grid hides, because
// the eye compares against its own afterimage rather than across a gap. So
// this is not two grids next to each other, and everything below exists to
// keep one promise - THE PICTURE DOES NOT MOVE.
//
// What that costs, concretely:
//   - the stage's shape is decided from the arms' own resolution before
//     either frame loads (compare.css's --compare-aspect), never from whichever
//     image happened to arrive first;
//   - both arms' frames are given a src at the same moment, so a flip is a
//     visibility toggle over already-decoded pixels rather than a fetch;
//   - a src is only ever re-assigned when it actually changes, so flipping
//     back and forth never re-decodes anything;
//   - nothing above the picture, and nothing sized, changes on a flip. The
//     dialog is centred by the browser, so a single wrapped line below the
//     stage would re-centre the dialog and slide the picture.
//
// The server does the pairing (internal/web/compare.go). That is not
// plumbing: "the same frame" across two encodes of one film means the nearest
// capture point BY FRACTION OF DURATION, the durations live in the manifests,
// and a page deriving that itself would be a second place the rule is
// written.

class CompareDialog extends HTMLElement {
  constructor() {
    super();

    // sets is every result set on disk, from GET /compare/sets - the picker's
    // options, refreshed each time the dialog opens so a run that finished in
    // the meantime is offered.
    this.sets = [];
    // a and b are the two arms' addresses ("infohash:params:index"), built by
    // the server and never spelled here.
    this.a = "";
    this.b = "";
    // data is the comparison itself; at is which position is on screen, and
    // live which arm. Those last two are the entire flipbook's state.
    this.data = null;
    this.at = 0;
    this.live = "a";

    // Bound once and kept, so disconnectedCallback removes the same function
    // objects addEventListener was given.
    this.onPickA = () => { this.a = this.pickerA.value; this.loadComparison(); };
    this.onPickB = () => { this.b = this.pickerB.value; this.loadComparison(); };
    this.onStageClick = () => this.flip();
    this.onFlipClick = () => this.flip();
    this.onPrevClick = () => this.step(-1);
    this.onNextClick = () => this.step(1);
    this.onCloseClick = () => this.dialog.close();
    this.onBackdropClick = (event) => {
      // A click on the dialog element itself, rather than anything inside it,
      // is a click on the backdrop - the same rule the frame panel uses.
      if (event.target === this.dialog) this.dialog.close();
    };
    this.onKeydown = (event) => this.dialogKeydown(event);

    // wired, partsObserver and partsDeadline are wire()'s own bookkeeping
    // (TOR-205) - see frame-panel.js for the full reasoning, and its wire()
    // for the shape this one repeats.
    this.wired = false;
    this.partsObserver = null;
    this.partsDeadline = null;
  }

  connectedCallback() {
    this.wire();
  }

  // wire finds this element's parts and, if every one exists, wires it -
  // what connectedCallback used to do inline, until a part missing meant a
  // quiet bail into awaitParts() instead of a throw (TOR-205; frame-panel.js
  // carries the reasoning). Idempotent: awaitParts()'s observer calls this
  // again on every mutation until it succeeds, and it must do nothing after
  // that.
  wire() {
    if (this.wired) return;

    this.dialog = this.querySelector(".compare");
    this.stage = this.querySelector(".compare-stage");
    this.pickerA = this.querySelector("#compare-a");
    this.pickerB = this.querySelector("#compare-b");
    this.shotA = this.querySelector("#compare-shot-a");
    this.shotB = this.querySelector("#compare-shot-b");
    this.gapCode = this.querySelector(".compare-gap-code");
    this.prevButton = this.querySelector("#compare-prev");
    this.nextButton = this.querySelector("#compare-next");
    this.flipButton = this.querySelector(".compare-flip");
    this.closeButton = this.querySelector("#compare-close");
    this.place = this.querySelector(".compare-place");
    this.times = this.querySelector(".compare-times");
    this.note = this.querySelector(".compare-note");

    const missing = Object.entries({
      dialog: this.dialog, stage: this.stage, pickerA: this.pickerA, pickerB: this.pickerB,
      shotA: this.shotA, shotB: this.shotB, gapCode: this.gapCode, prevButton: this.prevButton,
      nextButton: this.nextButton, flipButton: this.flipButton, closeButton: this.closeButton,
      place: this.place, times: this.times, note: this.note,
    }).filter(([, node]) => !node).map(([name]) => name);

    if (missing.length) {
      this.awaitParts(missing);
      return;
    }

    this.stopAwaitingParts();
    this.wired = true;

    this.pickerA.addEventListener("change", this.onPickA);
    this.pickerB.addEventListener("change", this.onPickB);
    this.stage.addEventListener("click", this.onStageClick);
    this.flipButton.addEventListener("click", this.onFlipClick);
    this.prevButton.addEventListener("click", this.onPrevClick);
    this.nextButton.addEventListener("click", this.onNextClick);
    this.closeButton.addEventListener("click", this.onCloseClick);
    this.dialog.addEventListener("click", this.onBackdropClick);
    this.dialog.addEventListener("keydown", this.onKeydown);
  }

  // awaitParts/stopAwaitingParts/partsNeverArrived: the same three as
  // frame-panel.js's, for the same reason - see its own copy for the
  // reasoning behind each.
  awaitParts(missing) {
    if (this.partsObserver) return;
    console.error("compare-dialog: waiting for " + missing.join(", ") +
      " to appear inside the element - connected before its subtree finished arriving");
    this.partsObserver = new MutationObserver(() => this.wire());
    this.partsObserver.observe(this, { childList: true, subtree: true });
    this.partsDeadline = setTimeout(() => this.partsNeverArrived(), SETTLE_TIMEOUT_MS);
  }

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

  partsNeverArrived() {
    this.partsDeadline = null;
    this.wire();
    if (this.wired) return;

    const missing = Object.entries({
      dialog: this.querySelector(".compare"),
      stage: this.querySelector(".compare-stage"),
      pickerA: this.querySelector("#compare-a"),
      pickerB: this.querySelector("#compare-b"),
      shotA: this.querySelector("#compare-shot-a"),
      shotB: this.querySelector("#compare-shot-b"),
      gapCode: this.querySelector(".compare-gap-code"),
      prevButton: this.querySelector("#compare-prev"),
      nextButton: this.querySelector("#compare-next"),
      flipButton: this.querySelector(".compare-flip"),
      closeButton: this.querySelector("#compare-close"),
      place: this.querySelector(".compare-place"),
      times: this.querySelector(".compare-times"),
      note: this.querySelector(".compare-note"),
    }).filter(([, node]) => !node).map(([name]) => name);
    this.stopAwaitingParts();
    const message = "compare-dialog: no " + missing.join(", ") + " inside the element, " +
      (SETTLE_TIMEOUT_MS / 1000) + "s after connecting - giving up rather than waiting forever";
    console.error(message);
    throw new Error(message);
  }

  disconnectedCallback() {
    this.stopAwaitingParts();
    if (!this.dialog) return;
    this.pickerA.removeEventListener("change", this.onPickA);
    this.pickerB.removeEventListener("change", this.onPickB);
    this.stage.removeEventListener("click", this.onStageClick);
    this.flipButton.removeEventListener("click", this.onFlipClick);
    this.prevButton.removeEventListener("click", this.onPrevClick);
    this.nextButton.removeEventListener("click", this.onNextClick);
    this.closeButton.removeEventListener("click", this.onCloseClick);
    this.dialog.removeEventListener("click", this.onBackdropClick);
    this.dialog.removeEventListener("keydown", this.onKeydown);
  }


  // setLabel is how one result set reads in the picker. The params key
  // is included in full and last: two sets of the same file of the same torrent
  // differ in nothing a person can see except their frame counts, and two runs
  // at the SAME count (a different profile, say) do not differ in that either -
  // the key is the only thing that always tells them apart.
  setLabel(set) {
    const parts = [set.name || shortId(set.infohash), basename(set.path)];
    parts.push(set.frames === set.points
      ? set.points + " frames"
      : set.frames + " of " + set.points + " frames");
    if (set.duration_ms) parts.push(timecode(set.duration_ms));
    parts.push(set.params);
    return parts.join(" · ");
  }

  fillPickers() {
    for (const picker of [this.pickerA, this.pickerB]) {
      picker.replaceChildren(...this.sets.map((set) => {
        const option = document.createElement("option");
        option.value = set.addr;
        option.textContent = this.setLabel(set);
        return option;
      }));
      picker.disabled = this.sets.length === 0;
    }
  }

  // chooseArms picks what the dialog opens on, given the file it was opened
  // from. The two result sets of THAT file come first when there are two -
  // which is the pairing a person pressing Compare on a file they just
  // regenerated is asking for - and otherwise it falls back to that file
  // against whatever else is on disk, which is the cross-torrent case. Both are
  // only a default: the two pickers are then free.
  chooseArms(infohash, index) {
    const mine = this.sets.filter((set) => set.infohash === infohash && set.index === index);
    const first = mine[0] || this.sets[0];
    if (!first) return ["", ""];
    const second = mine[1] || this.sets.find((set) => set.addr !== first.addr);
    return [first.addr, second ? second.addr : ""];
  }

  async open(infohash, index) {
    // ONE MODAL AT A TIME, enforced rather than assumed (TOR-193).
    //
    // Established by measurement, not by reading the spec: two modal
    // <dialog>s CAN be open together - a bare pair with none of this code
    // attached proves the platform allows it - and this page can reach that
    // state, because open() below awaits a fetch before showModal() and a
    // click on a frame during that await opens the frame panel underneath.
    // The result is a dialog painted over another one, and the one beneath
    // revealed again when the top one closes.
    //
    // Their KEYS do not fight, which was the worry: each handler sits on its
    // own dialog, they are siblings rather than nested, and a keydown reaches
    // only the one containing the focused element - measured with both open.
    // So this is about a confusing picture, not a broken one, which is why it
    // is one line here rather than a mechanism.
    //
    // Written against document rather than against the other element on
    // purpose: neither of these two needs to know the other exists, and a new
    // full-screen dialog gets the same treatment for free.
    for (const other of document.querySelectorAll("dialog[open]")) {
      if (other !== this.dialog) other.close();
    }
    try {
      const response = await fetch(url("compare/sets"));
      if (!response.ok) throw new Error(response.statusText);
      this.sets = (await response.json()).sets || [];
    } catch (err) {
      this.sets = [];
      log("could not read what there is to compare: " + (err.message || err));
    }

    this.fillPickers();
    const [a, b] = this.chooseArms(infohash, index);
    this.a = a;
    this.b = b;
    this.pickerA.value = a;
    this.pickerB.value = b;

    if (!this.dialog.open) this.dialog.showModal();
    // Focused straight away, so the keys work without a person having to find
    // something to click first - the feature is one keypress.
    this.stage.focus();
    await this.loadComparison();
  }

  async loadComparison() {
    this.data = null;
    this.at = 0;
    this.live = "a";

    if (!this.a || !this.b) {
      this.renderComparison("There is only one result set on disk so far - " +
        "regenerate a file at a different frame count, or capture another torrent, " +
        "and there will be something to flip against.");
      return;
    }
    if (this.a === this.b) {
      this.renderComparison("Both pickers name the same result set; pick a different one for the second.");
      return;
    }

    const target = url("compare");
    target.searchParams.set("a", this.a);
    target.searchParams.set("b", this.b);
    try {
      const response = await fetch(target);
      const body = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(body.error || response.statusText);
      this.data = body.comparison || null;
    } catch (err) {
      this.renderComparison(String(err.message || err));
      return;
    }
    this.renderComparison("");
  }

  // comparisonNote is what the pairing left out, said out loud rather than
  // silently dropped. A set of 8 flipped against a set of 20 has twelve points
  // that are simply not in the flipbook - pairing them with whatever happened
  // to be nearest would put a difference on screen that the film caused rather
  // than the encode - and a person who counted twenty frames in the grid needs
  // to be told that, not left to wonder where they went.
  comparisonNote(data) {
    if (!data.positions || data.positions.length === 0) {
      return "These two sets share no capture point near enough to pair, so there is " +
        "nothing here that is the same moment of the same film.";
    }
    const parts = [];
    const orphans = [];
    if (data.a.unpaired) orphans.push(data.a.unpaired + " of set 1's " + data.a.points);
    if (data.b.unpaired) orphans.push(data.b.unpaired + " of set 2's " + data.b.points);
    if (orphans.length) {
      parts.push(orphans.join(" and ") + " capture points have no partner in the other set, " +
        "so they are not positions here");
    }
    if (data.basis === "time") {
      parts.push("paired by timecode rather than by fraction of duration: one of these files " +
        "never said how long it is");
    }
    return parts.join(" · ");
  }

  // aspectOf is the stage's shape, taken from an arm's own resolution. It is
  // set once per comparison and never per position: every frame of one encode
  // has the same resolution, and a stage that re-shaped itself as the pictures
  // arrived would move the picture, which is the one thing this must not do.
  aspectOf(arm) {
    // A bare number, not "w / h": app.css also multiplies this inside a calc()
    // to bound the stage's height without breaking its shape, and only a number
    // can be multiplied. aspect-ratio takes either form.
    return arm && arm.width > 0 && arm.height > 0 ? String(arm.width / arm.height) : "";
  }

  renderComparison(message) {
    const data = this.data;
    this.note.textContent = message || (data ? this.comparisonNote(data) : "");
    this.note.title = this.note.textContent;

    const positions = (data && data.positions) || [];
    this.stage.style.setProperty("--compare-aspect",
      (data && (this.aspectOf(data.a) || this.aspectOf(data.b))) || "1.7778");

    if (positions.length === 0) {
      this.shotA.removeAttribute("src");
      this.shotB.removeAttribute("src");
      this.stage.dataset.live = "a";
      this.stage.dataset.gap = "false";
      this.place.textContent = "0 / 0";
      this.times.replaceChildren();
      this.prevButton.disabled = true;
      this.nextButton.disabled = true;
      this.flipButton.disabled = true;
      this.markLiveArm();
      return;
    }

    this.prevButton.disabled = false;
    this.nextButton.disabled = false;
    this.flipButton.disabled = false;
    this.renderPosition();
  }

  // setShot gives one arm its picture. Both arms are set for every
  // position, whichever is live, so the flip that follows is a visibility
  // toggle over pixels the browser has already decoded.
  setShot(img, frame, label) {
    if (!frame.url) {
      // Nothing was captured here. An empty src would ask the browser to fetch
      // this very page, so the attribute goes away entirely and .compare-gap
      // says what happened instead.
      img.removeAttribute("src");
      img.alt = "";
      return;
    }
    const href = String(url(frame.url));
    if (img.getAttribute("src") !== href) img.src = href;
    img.alt = label + " at " + timecode(frame.time_ms);
  }

  // armTime is one arm's entry in the bar: which set, where its frame came
  // from, and - for a point that produced nothing - the engine's own reason.
  //
  // The shift is shown only for a frame that EXISTS, and that is TOR-118's trap
  // avoided rather than a tidy-up. manifest.ShiftFailed serialises to the word
  // "unavailable", which is also one of the two failure CODES, so a failed
  // point rendered with both reads "01:23 unavailable — no frame" and invites
  // exactly the wrong conclusion: that the shift is the reason. On a point with
  // no frame the shift says only THAT it was lost; frame.error is what says
  // what lost it, and it is the one worth the space.
  armTime(label, frame, live) {
    const span = document.createElement("span");
    if (live) span.className = "compare-live";
    span.textContent = label + " " + timecode(frame.time_ms) + (frame.url
      ? (frame.shift ? " " + frame.shift : "")
      : " — " + (frame.error || "no frame"));
    return span;
  }

  markLiveArm() {
    for (const label of this.dialog.querySelectorAll(".compare-arm")) {
      label.dataset.live = String(label.dataset.arm === this.live);
    }
  }

  renderPosition() {
    const data = this.data;
    const position = data.positions[this.at];

    this.setShot(this.shotA, position.a, "set 1");
    this.setShot(this.shotB, position.b, "set 2");

    const shown = this.live === "a" ? position.a : position.b;
    this.stage.dataset.live = this.live;
    this.stage.dataset.gap = shown.url ? "false" : "true";
    this.gapCode.textContent = shown.error || "no frame";
    this.stage.title = shown.url
      ? "set " + (this.live === "a" ? "1" : "2") + " at " + timecode(shown.time_ms) +
        " - press to flip to the other set"
      : "set " + (this.live === "a" ? "1" : "2") + " captured nothing at " +
        timecode(shown.time_ms) + (shown.error ? " (" + shown.error + ")" : "");

    this.place.textContent = (this.at + 1) + " / " + data.positions.length;
    this.times.replaceChildren(
      this.armTime("1", position.a, this.live === "a"),
      document.createTextNode("  "),
      this.armTime("2", position.b, this.live === "b"),
    );
    this.markLiveArm();
  }

  flip() {
    this.showArm(this.live === "a" ? "b" : "a");
  }

  showArm(arm) {
    if (!this.data || !this.data.positions || this.data.positions.length === 0) return;
    this.live = arm;
    this.renderPosition();
  }

  // step moves along the film. It wraps rather than stopping at the
  // ends: the flipbook is short - eight positions, twenty at most - and a
  // person walking it with one finger should not have to turn round.
  step(delta) {
    const positions = (this.data && this.data.positions) || [];
    if (positions.length === 0) return;
    this.at = (this.at + delta + positions.length) % positions.length;
    this.renderPosition();
  }

  dialogKeydown(event) {
    const tag = (event.target.tagName || "").toLowerCase();
    // Someone using the pickers is choosing a set, not steering the flipbook -
    // arrow keys belong to the select then.
    if (tag === "select" || tag === "input" || tag === "textarea") return;
    // Space on a focused button is that button's own activation; intercepting
    // it here would flip twice for one press.
    if (tag === "button" && event.key === " ") return;

    switch (event.key) {
      case "ArrowLeft": this.step(-1); break;
      case "ArrowRight": this.step(1); break;
      case "ArrowUp":
      case "ArrowDown":
      case " ":
      case "f":
      case "F": this.flip(); break;
      case "1": this.showArm("a"); break;
      case "2": this.showArm("b"); break;
      // Escape is the dialog's own, and everything else belongs to the page.
      default: return;
    }
    event.preventDefault();
  }
}

// Native, no bundler.
customElements.define("compare-dialog", CompareDialog);

export { CompareDialog, setServices };
