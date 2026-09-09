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

## What the run table's markup carries, and what it is for (TOR-213)

Written while asking whether the table could become a grid (TOR-214), because
the one thing a `<table>` gives that a grid does not is semantics for free, and
nobody had ever looked at what this table actually carries. It carries more
than expected:

    <th scope="col" data-sort="name" tabindex="0" role="button" aria-sort="none">

  - **`scope="col"` on every header**, the three in `index.html` and the six
    `buildLiveColumnHeaders` mints.
  - **`aria-sort` is maintained, not just set**: `none`, `ascending` or
    `descending`, rewritten on the sorted column and cleared on the others every
    time the order changes (`run-table.js`, the sort-state block).
  - **`tabindex="0"` and `role="button"`** on every sortable header, which is
    what makes sorting reachable without a mouse.
  - The row's toggle carries **`aria-expanded`**, and the page sets its
    **`aria-controls`** to the detail's own id.
  - The progress bar is a full **`role="progressbar"`** with min, max, now and a
    label.

**Two things in that list work against each other**, and it is worth knowing
before any of it is carried across to another element:

  - `role="button"` OVERRIDES a `th`'s implicit `columnheader` role. `aria-sort`
    is defined on `columnheader`/`rowheader`, so on an element declared a button
    it is probably inert - the attribute is maintained on every sort and may
    reach nothing. Unverified: measuring it needs a screen reader, and the owner
    has scoped screen-reader support out (see below).
  - The tenth column's header carries **`aria-hidden="true"`** while the column
    itself still exists in every row.

**Measured without a screen reader, and complete on its own:** the column
resize handles carry `aria-hidden="true"` and no `tabindex`. Resizing a column
is mouse-only - it is not reachable from the keyboard at all. That is a
KEYBOARD gap, not only an announcement one, and it is the one finding here that
does not depend on assistive technology to matter.

### Scope, recorded so the silence is not mistaken for an oversight

The owner has decided not to invest in screen-reader support. So this section
describes what the markup IS, not a standard it is held to, and nothing here is
filed as a defect.

What that decision changes, concretely: it removes the only argument for
keeping a `<table>` over a grid, since the column behaviour a grid must
reproduce is layout, not semantics. Keyboard operability is a separate question
and was NOT scoped out - sorting by keyboard is `tabindex` and `role="button"`
doing their other job, and whatever replaces the table has to keep it.

## The answer to that question: yes, move to a grid (TOR-214)

The section above ends by saying the only argument for keeping a `<table>` had
just been scoped away. This is the measurement that follows it, and it is not
close. A CSS grid does every one of the six things the table is kept for, and
does the one thing the table cannot do at all.

Measured in Chrome at a 1440x900 window, which gives the pane its usual 1168px
(TOR-168's own number), against a throwaway page in
`docs/spikes/TOR-214-grid/` - a `<div class="run-grid">` of ten tracks read
from a verbatim copy of `tokens.css`, with `wireColumnResizers` pasted in from
`run-table.js`. Nothing in `internal/web/assets` was touched. Every figure
below comes from that page's own `window.probe()`.

### The ten tracks

    grid-template-columns:
      var(--col-w-name) var(--col-w-when) var(--col-w-status)
      var(--col-w-peers) var(--col-w-seeds)
      var(--col-w-download_bps) var(--col-w-upload_bps)
      var(--col-w-availability) var(--col-w-priority)
      minmax(3.8rem, 1fr);

Nine tokens and the unlabelled actions column - **ten**, the same count
`this.columns` reads off the header and writes into the detail's `colSpan`.
Measured at rest: `320 104 140 54.4 54.4 54.4 54.4 86.4 54.4 245.6`, summing to
1168, the pane exactly.

### The six behaviours, each measured

**1. A column narrower than its own content.** The name column was dragged to
its `COLUMN_MIN_WIDTH` floor: the track measured **44px** while the magnet name
in it - `MAGNETDN_THE_ABSOLUTELY_UNBREAKABLE_..._0123456789`, 96 characters with
no space and no hyphen, so not one soft wrap opportunity - measured **828.17px**
natural. The column is 18.8x narrower than its content and the row height did
not change (68.6px before and after).

This is the behaviour `table-layout: fixed` exists for (`table.css`, the
`.run-table` rule), so it was worth proving the demonstration could fail. Same
string, same 44px asked for, four ways:

| sizing | asked | rendered |
| --- | --- | --- |
| `table-layout: auto` cell | 44px | **822.43px** - content wins |
| `table-layout: fixed` cell | 44px | 53.59px |
| grid, bare `44px` track | 44px | **44px** |
| grid, `minmax(min-content, 44px)` | 44px | **822.43px** - content wins |

So a grid track is *not* automatically able to do this. It can because every
sortable track above is a bare length and never `auto`, `min-content` or
`max-content`. That is the one line of the conversion that must not be
loosened later, and it is the exact counterpart of `table-layout: fixed`.

**2. The name ellipsises.** At the 44px track: `.run-name` `scrollWidth` 828 vs
`clientWidth` 20, `text-overflow: ellipsis`, `overflow: hidden`,
`white-space: nowrap`, and the cell measured 44px - inside its track, not
shoving it. Renders as `M…`. The rule is `.run-name`'s, copied unchanged; grid
needed nothing added to it.

**3. Fills the pane at rest, outgrows it on a wide drag.** `min-width: 100%`
with `width: max-content` is the shape the ticket predicted, and it holds:

  - at rest: grid `offsetWidth` 1168 = wrap `clientWidth` 1168, no horizontal
    scroll anywhere;
  - after a real pointer drag widening the name column to 640 (its
    `COLUMN_MAX_WIDTH`): tracks sum **1303.19**, wrap `scrollWidth` 1303 >
    `clientWidth` 1168, so `.run-table-wrap` scrolls - and
    `documentElement.scrollWidth == clientWidth`, so **the page does not**.

The slack at rest has to go somewhere, and this is the one place the grid is
not identical to the table. `width: 100%` under fixed layout spreads slack over
every column; the grid parks it in the actions track's `1fr`, which at rest
measures 245.6px instead of 3.8rem/60.8px. The obvious alternative - wrapping
every token as `minmax(var(--col-w-KEY), 1fr)` so the slack spreads - was tried
and is **wrong**: under `width: max-content` every `1fr` track resolved to
851.74px, all ten identical, an 8517px grid, and dragging a token then changed
nothing at all. Park the slack in one track.

**4. Sticky header.** Each header cell is its own sticky item; there is no
`<thead>` box and none is needed. Proved with a control, because the honest
finding is subtler than "it sticks": with the wrap given a height, scrolling it
250px left the header pinned at offset **0** from the wrap's top while the first
row moved to **-223.7**; setting that same header to `position: static` moved it
to **-250**. So sticky is doing real work.

What it does *not* do is stick against the page - and neither does the table's
today. `.run-table-wrap`'s `overflow-x: auto` computes `overflow-y` to `auto`
as well, making the wrap the scrollport; the wrap has no height of its own
(`clientHeight` 986 == `scrollHeight` 986), so the header travels with the page
and tucks under the pinned intake. That is exactly what `.run-table thead th`'s
own comment in `table.css` says happens today. The grid reproduces the
behaviour and the reason for it, unchanged.

**5. The detail spans every column.** `grid-column: 1 / -1` measured 1168px
against a row of 1168px and a track sum of 1168px, visible at 1168x37.89. No
count is written down anywhere: `-1` is the last line of whatever the grid has,
so the thing `this.columns` exists to keep correct (`colSpan = 10`, and the
silent off-by-one a hard-coded count would have caused when TOR-139 added six
columns) simply stops being a thing that can go wrong.

**6. A real wrapper around a row AND its detail - the point of the exercise.**
`<run-row>` is a registered custom element containing all ten cells plus the
detail (`wrapsRowAndDetail: true`), and every cell's left edge matched its
header's to **0.00px** across all ten columns.

The control, in the same browser, is decisive. Parsing
`<tbody><run-row><tr>…</tr><tr>…</tr></run-row></tbody>` yields:

    <run-row id="w"></run-row><table><tbody><tr id="r1">…

The wrapper is hoisted out in front of the whole table and left empty; the rows
end up under `<tbody>` as before. There is no arrangement of a `<table>` in
which TOR-212 gets this element. That is the whole case, and it is not a
preference.

`subgrid`, not `display: contents`, for the wrapper. Both keep the cells in the
outer grid's tracks, but `display: contents` deletes the wrapper's box - and
the box is what the wrapper is wanted for: `.run-row:hover`'s ground, the open
row's `inset 3px 0 0` accent rail and the bottom rule all belong to the row as
a whole. Measured: the `subgrid` wrapper is a real box, 1168x68.6.

### Keyboard operability, which was not scoped out

Driven with real key presses, not synthesised events. From `BODY`, one `Tab`
put focus on the name header (`role="button"`, `tabIndex` 0, focus ring
`2px rgb(34, 224, 232)` = `--accent`); one `Return` sorted, and `aria-sort` went
to `ascending` on that header and `none` on the other eight. All nine sortable
headers carry `tabindex="0"` and `role="button"`, exactly as the `<th>`s do.

Unchanged, and deliberately not fixed here: the resize handles still measure
`tabIndex: -1` and `aria-hidden="true"`, so resizing stays mouse-only. That is
the pre-existing gap the section above records, carried across as-is. The grid
neither causes it nor makes it worse, and fixing it is its own ticket.

### Does the column drag survive? Yes - unchanged (criterion 3)

`wireColumnResizers` was pasted in from `run-table.js` with only its `this.`
receivers rebound, and driven with a real pointer drag. Both directions worked
on the first attempt: `--col-w-name` went 320px -> 640px, then 640px -> 44px,
clamped at `COLUMN_MAX_WIDTH` and `COLUMN_MIN_WIDTH` respectively, with the
grid re-laying out on each move. `stopPropagation` still keeps a drag from
reaching the header's sort listener - the sort count was 1 before the two drags
and 1 after.

Why it survives: the drag never touched the table. It writes `--col-w-KEY` on
`:root` and reads `th.getBoundingClientRect().width`, and both are still true
of a grid - `grid-template-columns` reads the same tokens, and a header cell
stretches to its track, so the rect it measures *is* the column width. So
**TOR-215 is a markup and stylesheet change, not a behaviour change**: the drag
code, the localStorage round-trip and the clamp all carry over untouched.

One line becomes vestigial rather than wrong:
`th.style.width = "var(--col-w-" + key + ")"`. Under `table-layout: fixed` that
inline width is what sizes the column; under grid the track sizes it and the
header just fills the track. Harmless, and worth deleting with a comment
saying why rather than leaving a reader to wonder what sizes what.

### The one porting detail that is not optional

A table cell's `width` in the border-collapse model already includes its
padding, so `--col-w-name: 20rem` means 320px *including* the `.4rem .3rem` on
every cell. A grid item is content-box by default, and `base.css` has no global
`box-sizing` reset - so a header given `width: var(--col-w-name)` measured
329.6px and overflowed its own 320px track until the grid's cells were given
`box-sizing: border-box`. Found by measuring; it would not have been noticed by
reading, and every column would have been 9.6px wrong.

### What could not be reproduced

**The accessibility criterion was dropped, not passed.** TOR-214 asked for the
screen-reader baseline from TOR-213 to be reproduced point by point. There is
no such baseline: screen-reader support was scoped out while TOR-213 was in
flight, so it closed with a source audit and no transcript. Comparing against
nothing cannot produce a result, and reading the browser's accessibility tree
instead would not be the same measurement - it would be a different, easier one
wearing the answer's clothes. So this is recorded as **not done**, and the gate
was decided without it.

That is defensible only because of what the section above established: with
screen-reader support out of scope, semantics were the sole thing the `<table>`
gave that a grid does not, and everything remaining is layout. If that scope
decision is ever reversed, this is the question to reopen - and a hand-built
`role="table"`/`row`/`columnheader` set, which is what the grid would then
need, is worse than no markup at all when it is wrong. Do not add one on spec;
measure it with the reader first.

Two smaller things, stated rather than glossed: the spike ran in Chrome only,
and `subgrid` is what carries the wrapper (Chrome 117+, Firefox 71+, Safari
16+) - fine for this project, but it is the one feature the conversion cannot
degrade out of. And the spike reproduced the six behaviours, not the table's
every cosmetic rule; `.run-cell-when` clipping under the grid's
`overflow: hidden` where a `<td>` would have spilled is the kind of small
difference TOR-215 will meet cell by cell.

### The decision

Move. TOR-215 and TOR-216 proceed, and TOR-212 gets the wrapper element it
needs, which is the only reason any of this was asked.
