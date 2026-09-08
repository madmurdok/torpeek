# The front end: components, and still no build step

Written at the end of release 1.4.0, which turned one 5971-line `app.js` into
nine ES modules and six custom elements **without acquiring a JavaScript
toolchain**. That was a choice, and this file exists so the next person
proposing a bundler is arguing against a recorded decision rather than filling
a silence.

## What the batch actually did

| | before 1.4.0 | after |
|---|---|---|
| `app.js` | 5971 lines | 828 |
| `app.css` | 2560 lines | gone: `tokens.css` plus seven area files |
| modules | one script | nine, one import graph |
| elements | none | six |

The nine modules and their imports, which is the whole architecture:

    app.js          -> state.js, events.js, and the six element modules
    events.js       -> state.js
    run-table.js    -> state.js
    run-detail.js   -> state.js
    file-list.js    -> state.js
    file-detail.js  -> state.js
    compare-dialog.js -> state.js
    frame-panel.js  -> (nothing)
    state.js        -> (nothing)

`state.js` and `frame-panel.js` are the graph's two leaves and are meant to
stay that way. The direction matters more than the count: **nothing imports
app.js**, so no element can reach the page's own wiring, which is what lets
`state.js` and `events.js` be executed for real in a plain node process
(`internal/web/eventstate_test.go`) rather than only read as text.

The six elements: `<run-table>`, `<run-detail>`, `<file-list>`,
`<file-detail>`, `<frame-panel>`, `<compare-dialog>`.

## There is no build step, and here is what that preserves

Custom elements and ES modules are native browser API. Nothing is compiled,
bundled, transpiled or minified, so:

- **`//go:embed assets` still ships the files as they are written.** The file
  the browser runs is the file in the repository - no source map, no build
  output to keep in sync with it.
- **`make cross` still cross-compiles four platforms with plain `go build`**
  (darwin/amd64, darwin/arm64, linux/amd64, windows/amd64) and there is no
  `node`, `npm` or bundler anywhere in the `Makefile` or in `scripts/`.
  Checked, not assumed: a grep for every such tool across both finds nothing.
- **`CGO_ENABLED := 0`** is exported at the top of the `Makefile`, so the
  shipped build is a single static binary, and `make check` tests exactly that
  build.
- **`scripts/package.sh` is untouched** by any of this.

## What a bundler would have cost

The release story for every platform at once. Today a release is `make cross`
on one machine: four `go build` invocations and nothing else. A bundler puts a
second toolchain in front of that - a node version, a lockfile, a
`node_modules` tree, and a build artefact that has to be produced before the
Go build can embed it. Concretely, it would mean:

- **A cross-compile that needs node.** `make cross` currently needs only a Go
  toolchain, which is why one machine can produce all four platforms. A
  bundled front end has to be built once and then embedded, so either the
  build order grows a step that must not be skipped, or the artefact is
  committed and can silently disagree with its source.
- **A tested build that is not the shipped build.** `make check` runs
  `go test ./...` against the embedded assets. With a build step, the tests
  either read the source (not what ships) or the bundle (which must then be
  rebuilt before every test run).
- **Two answers to "where does this rule live".** The stylesheets are split
  across eight files whose LOAD ORDER is load-bearing (see below); a bundler
  concatenates them, and the order becomes a config file's business rather
  than something visible in `index.html`.
- **A dependency surface where there is none.** The front end has zero
  third-party runtime dependencies today.

What it would have bought is smaller: fewer requests on first load, and
syntax the browsers already support natively. Neither is worth the above for a
UI served from localhost or from a reverse proxy on the same host.

## The element pattern

Established by `<frame-panel>` (TOR-192) on the most self-contained piece
deliberately, so a wrong pattern would be found at the smallest blast radius.
Its module header carries the long form; this is the shape every element after
it follows:

1. **Light DOM.** See the next section.
2. **The markup stays declarative in `index.html`, wrapped rather than
   rebuilt.** An element does not build its own structure from a template
   literal. This is not sentiment: the comments explaining the chamfer, the
   scrims and the bracket placement sit *next to* the elements they explain,
   and inside a string they would read as prose about code.
3. **Parts are found with `this.querySelector`, in `wire()`** (called once
   from `connectedCallback`, and again by the mechanism below): if every part
   exists, the element wires itself exactly as it always did; if one is
   missing, it fails **loudly by name, unless the element's own markup could
   still be arriving, in which case it bails quietly and waits.**

   **Corrected by TOR-205, and worth stating what was right and what was
   wrong about the original rule.** `connectedCallback` throwing on the first
   missing part was right for the case it was written for: a wrapper deleted
   from `index.html`. That is a wiring error, and `docs/front-end.md`
   originally called it one without qualification, because every one of the
   six elements' markup was fully parsed before `app.js`'s deferred
   `type="module"` script could possibly connect it - so "the part is
   missing" and "the part is a mistake" were the same fact on this page. They
   are not the same fact for a consumer whose HTML **streams**: TOR-204's own
   probe connected `<frame-panel>` after `.lightbox` existed but before
   `#lightbox-img` did, hit the throw, and never got a second chance -
   `connectedCallback` does not run again on its own, so the element stayed
   permanently dead with nothing on screen saying so. A subtree still
   arriving is not a wiring error; it is a moment in time, and treating it as
   the former traded a real bug for a worse one.

   **The two cases still have to be told apart, or TOR-192's guard is traded
   away rather than corrected** - so only the three elements that can
   actually be connected against an incomplete subtree (`<run-table>`,
   `<frame-panel>`, `<compare-dialog>` - the three whose markup is parsed,
   declarative HTML per point 2) grew the mechanism: a `MutationObserver` on
   the element's own `childList` and `subtree`, created **only** the first
   time `wire()` finds a part missing, and disconnected the instant wiring
   succeeds. Its callback re-runs `wire()`; once every part exists, wiring
   proceeds exactly as `connectedCallback` always did. A part still missing
   `SETTLE_TIMEOUT_MS` (10s) after the element's first connection is deemed
   **never**, not **not yet**: `partsNeverArrived()` `console.error()`s the
   still-missing names (a throw from a timer callback reaches no caller the
   way the original synchronous throw did, so this is what still says which
   part is absent) and then throws - TOR-192's guard, delayed but intact.
   That bound is a stated guess, not a measurement: too short and a
   legitimately slow stream throws while genuinely still arriving; too long
   and a truly missing part takes that long to say so out loud, instead of
   failing on the very next line the way it used to.

   **What this costs, and where it is paid.** One short-lived
   `MutationObserver` per element instance that connects early - and on
   torpeek's own page it is never created at all, because `index.html` is
   fully parsed before `customElements.define` ever runs, so every upgrade
   here finds a complete subtree on the first try. The cost lands entirely on
   a consumer that streams, which is the only place the bug existed.

   The other three elements - `<run-detail>`, `<file-list>`, `<file-detail>` -
   are the exception point 2 above does not cover: since there is one instance
   per torrent (or per video file) rather than one on the whole page, their
   markup is a template literal assigned with `this.innerHTML = TEMPLATE`
   inside `build()`, not declarative HTML in `index.html` (each module's own
   header carries the reasoning). That assignment is synchronous, so the
   instant it returns every part it wrote exists in the same turn - there is
   no window in which one of these three can be connected with part of its
   subtree missing. For them the original reasoning was never wrong, and
   their `throw` is unchanged: a missing part there is exactly what it always
   was, a wiring error, with no "not yet" to distinguish it from.
4. **Every listener is an instance-bound field**, so `disconnectedCallback`
   removes the same function object `addEventListener` was given. Handlers on
   `window` are the ones that genuinely leak.
5. **One method per thing the outside may ask for.** `panel.open(src,
   caption)` is the frame panel's whole API.
6. **`customElements.define` at the end of the module**, and `app.js` imports
   the module for that side effect.
7. **Pure helpers belong in `state.js`; impure ones are injected.** A module
   that needs something only the page can provide takes it through a
   `setServices({...})` export in the shape `events.js`'s `setView` already
   uses, and that setter throws at wiring time on a missing name rather than
   at the first request. `url()` and `log()` are the two that cannot move -
   the first closes over the page's token and `document.baseURI`, the second
   writes to the activity log element.

### Three traps this pattern has already paid for

- **`this` in a function used as a listener.** A `function` declaration
  registered with `addEventListener` gets the LISTENING ELEMENT as its `this`,
  so `this.anyMethod()` inside it throws. This shipped once, in TOR-194's
  `endColumnDrag`: the lines before the failing call had already run, so a
  column drag *looked* finished while no width was ever saved. Thirty-nine
  mutation tests passed on the broken file, because the call reads exactly as
  it should. Use an arrow function.
- **An instance field shadowing a method.** `this.toggle = row.open` hides a
  `toggle()` method from the moment it is set. This was caught in TOR-195 by
  executing the code, not by reading it. `TestNoInstanceFieldShadowsAMethod`
  walks every class in every element module because of it.
- **A loud throw that assumed "missing" always means "wrong".** TOR-192's
  guard (point 3, above) was right about a wrapper deleted from `index.html`
  and wrong about a consumer whose HTML streams: TOR-204's own probe
  connected `<frame-panel>` between two parts of its subtree arriving, hit the
  throw, and the element stayed dead for the rest of the page's life because
  `connectedCallback` never runs a second time on its own. TOR-205 is the
  fix - wait and retry via a `MutationObserver`, bounded by a deadline so a
  genuinely missing part still fails loudly rather than silently forever -
  and the trap worth naming here is the one still open in this shape of
  reasoning: a rule proven right for the failure you were looking at can be
  wrong about a failure that had not happened yet.

## Light DOM, and the token consequence

**No element uses shadow DOM, and the isolation is not what it would cost.**

Custom properties DO inherit across a shadow boundary, so `tokens.css`'s
`:root` would still reach inside one and `--accent`, `--edge` and `--scrim`
would all resolve. What would NOT reach inside are the RULES that use them:
`.lightbox-view`'s `overflow: hidden`, the bracket pseudo-elements, both of
TOR-122's scrims, every cursor the zoom states declare. Getting them back
inside a shadow root means one of two things, and both are worse than not
having the boundary:

- **duplicating the stylesheet into a template literal**, which gives the
  panel's arithmetic two homes. `stylesheet_test.go` reads the SERVED
  stylesheet, so a second copy in a string is a copy no guard is looking at -
  and that arithmetic has drifted once already (TOR-177).
- **fetching the stylesheet at load time** and adopting it with
  `new CSSStyleSheet().replace(...)`, which is a build step's work done at
  runtime, in a project whose whole premise is that there is no build step.

There is a third cost, smaller but real: `showModal()` puts a `<dialog>` in the
top layer and `::backdrop` styles it there, which works in light DOM with no
thought at all. And there is no encapsulation problem to solve - nothing on
this page styles another area's classes.

So: light DOM until something on the page actually collides.

## The stylesheets' load order is load-bearing

`index.html` links eight stylesheets in a flat list -
`tokens`, `base`, `intake`, `table`, `detail`, `filelist`, `framepanel`,
`compare` - rather than `@import`ing them from one entry sheet, so the browser
fetches them in parallel and the order is visible in the page instead of
hidden in CSS.

Splitting one stylesheet into eight moved the cascade's second axis out of the
stylesheet and into that list: at equal specificity, the later FILE wins. Four
rule pairs depend on it, each a grouped rule plus a later standalone override,
and each is kept inside ONE file so no link order can un-decide it
(`TestEveryPositionDecidedPairStaysInOneFile`).
`TestTheConcatenationOrderIsThePagesOwn` holds the test helper's own idea of
that order to what `index.html` actually says.

## REQUIREMENTS.md needs nothing

Examined rather than left unexamined, which the ticket asked for either way.

§3.3 is 334 lines and every one of them is about what the UI must **do**: the
stall reading and its duration, the single entry point, working under an
arbitrary base path, headless mode, the access token, the queue's width, the
frame grid, the summary panel. None of it is about how the front end is
assembled, and adding the no-build-step decision there would blur the
distinction the document itself keeps.

One line of §3.3 is build-adjacent - "фронтенд вшит в бинарник", the front end
is embedded in the binary - and it is still exactly true. It is also the
requirement this decision protects: a bundler would not break it, but it would
put an artefact between the source and the thing embedded.
