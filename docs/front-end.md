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

## The accordion is its own component now (TOR-212)

The section above ends by saying TOR-212 gets the wrapper it needs. This is
what was done with it, and the first thing to record is that **the wrapper
turned out not to be where the component goes.**

### There were three disclosures, and now there is one component

    setRunExpanded(entry, expanded)   run-table.js    a torrent's line -> its detail row
    setFileExpanded(expanded)         file-detail.js  a video file's block
    setMetaExpanded(expanded)         file-detail.js  the Metadata sub-block inside it

All three did the **same three DOM writes** and nothing else, in three
different orders:

    toggle.setAttribute("aria-expanded", String(expanded))
    region.hidden = !expanded
    dressed.dataset.expanded = String(expanded)        (the outer two only)

`accordion.js` is that triple, as `class Accordion`, and `apply(expanded)` is
now the only place on the page any of those three attributes is written. It
serves **all three** - the honest answer turned out to be three rather than
two, because the third differs from the other two only by having nothing to
dress, which is a value of the level parameter rather than a different shape.

What stayed at the three call sites is what opening *means* at each of them:
the run level's width recheck and top-up read (below), the file level's "one
file's detail at a time" sibling sweep (TOR-182) and its first-open read off
disk. A component that closed siblings would impose the file level's answer on
the run level, where the opposite was decided for three written reasons.

### It is NOT a custom element, and that is structural

Every other component in this file is one. This one cannot be, and the reason
is worth stating because it is the first place the element pattern does not
reach:

- `.run-row-group` carries `grid-template-columns: subgrid`, which only works
  on a **direct** grid item of `.run-grid`. An element wrapped around it would
  break the column alignment TOR-214 measured to 0.00px - so the wrapper this
  ticket waited for is the thing the component must not become.
- At the file level the toggle is inside the row's `<label>` and the region is
  the row's sibling inside the `<li>`. There is no box that holds both and
  only both.
- And the run level's click listener **must stay on `.run-row`**. On
  `.run-row-group` every click inside an open detail would collapse it, which
  is the trap the two-`<tr>` arrangement existed to avoid and TOR-215 wrote
  down when it built the wrapper.

### And the honest consequence: this task did not need the grid

Worth writing down because the whole TOR-211..216 line was justified by it, and
the justification was WRONG.

The premise was: the accordion cannot be a component because a run's line and
its detail are two adjacent `tr`s in one `tbody`, and HTML hoists any wrapper
out from between them. True about a wrapper - TOR-214 measured the hoist - but
it assumed the component would BE that wrapper. It is not, for the three
structural reasons above, and a plain class needs no wrapper at all.

Checked rather than reasoned: the three writes `apply()` owns existed verbatim
in the pre-grid module. At `e01091b`, `run-table.js`'s `setRunExpanded` ended

    entry.detailRowEl.hidden = !expanded;
    entry.rowEl.dataset.expanded = String(expanded);
    entry.rowToggle.setAttribute("aria-expanded", String(expanded));

which is `apply()`'s triple, against `tr` elements. `accordion.js` mentions the
grid only in comments - none in its live code - so it would have worked
unchanged on the table.

**What that does and does not invalidate.** TOR-215 keeps its own value, which
was measured independently of the accordion: the columns are now exactly their
tokens instead of having `width: 100%`'s slack smeared over all ten, the row
has a real box for its hover ground and accent rail, and TOR-214 filled a
silence this project had carried since 1.2.0 about why a table at all. Those
stand. What does not stand is the sentence that started the line - that TOR-212
was blocked on it. It was not.

The lesson for the next such line: "X cannot be a component until Y" is a claim
about the SHAPE the component would take, and the shape is worth settling
before the prerequisite is built. One prototype of the component itself, of the
kind TOR-214 built for the layout, would have found this at the start.

So the component owns no listener at all: three levels activate from three
different elements with three different exceptions (the checkbox-target
exclusion at the file level, `stopPropagation` on Cancel and the two priority
buttons at the run level, none at the Metadata level), and a component that
learned all three would be the page.

### The nesting level: three values, read off the current CSS

Measured in Chrome on the running page with all three open at once, at a
1440x900 window. Nothing here was designed; every figure is what the existing
rules already produced.

| | level 1 - run | level 2 - file | level 3 - meta |
|---|---|---|---|
| `dressed` element | `.run-row` | `.picker-item` | **none** |
| open ground | `rgb(9, 58, 64)` (`--accent-dim`) | none | none |
| open rail | `inset 3px 0 0 rgb(34,224,232)` | `inset 2px 0 0`, same colour | none |
| toggle type | 13.12px, opacity 1 | 14px, opacity 1 | 12.8px, opacity .75 |
| toggle chrome | no ground, no radius | no ground, no radius | `.35rem` radius, `--hover` on hover |
| region box | no padding, no border | `1px solid --rule` on the left, 16.8px margin-left + 9.6px padding-left | 4.8px 5.6px 0 padding, no border |
| region height, open | 972.9px | 648.6px | 194.8px |

**The depth is how loudly the open state is dressed**: the rail thins 3px ->
2px -> none, and the one filled ground appears once, at the top. That is why
`dressed` is *required* at levels 1 and 2 and *refused* at level 3 - the
component checks the table above rather than describing it, and refusing is
what keeps the level from quietly meaning nothing.

**The MARK does not vary with the level at all**, which was the surprise. All
three drew their triangle with the same declarations and the same two
`::before` rules, three times over, in two files - and each copy's comment
said it was deliberately the same object as its neighbour's. It is one rule
now, `.disclosure-mark` in base.css, and the component is what puts the class
on.

Measured neutrality, in the page: exactly **six** elements carry
`.disclosure-mark`, and they are exactly the same six that carry the three old
level classes. Every one measures `flex: 0 0 auto`, `opacity: .6` and a width
of **0.6995-0.6998em** - `.7em` with the browser's own sub-pixel rounding,
which is why the pixel widths differ (9.18 / 9.80 / 8.95) while the rule does
not: `.7em` resolves against each level's own font size. The two `<span
class="picker-open">` on non-video rows keep their reserved 9.797px box at
`visibility: hidden`, which is the column alignment the reservation exists
for - and it is why that one class is added in `file-list.js` at row creation
rather than by its accordion, which does not exist until the file has
something to say.

Shown able to fail: removing `.disclosure-mark` from all six collapses every
one to `0px`, `opacity: 1`, `content: none`, and putting it back reproduces the
before values byte-for-byte. So the numbers above come from that rule and from
nowhere else.

### Criterion 2: the open state is not in the component, demonstrated by the move

`reorderRuns()` re-sorts on every redraw and MOVES a row's element. Driven
three ways on the live page, on a row that was open with its first file open
inside it:

1. **A real sort.** Clicking the Name header twice took the row from index 1
   to index 0 in `#run-list`. Still `aria-expanded="true"`,
   `data-expanded="true"`, detail 778.1px.
2. **The exact call `reorderRuns` makes**, `list.append(group)` on a node
   already in the list. Unchanged.
3. **A genuine detach and re-attach.** `group.remove()` put the whole subtree
   out of the document - `isConnected` false on the group, on its
   `<run-detail>` and on its `<file-detail>`, height 0, index -1 - and
   `list.append(group)` brought it back at 778.1px with the run open AND the
   file's own block still open (`aria-expanded="true"` on `.picker-open`).

The Go side executes the same move under node against the shipped
`run-table.js` (`TestTheDisclosureSurvivesTheMoveARe_sortMakes`), and also
reads the five own properties off a **live** instance rather than off the
source - `dressed, level, mark, region, toggle` and nothing else, so a field
assigned conditionally cannot hide from it.

**A measurement that failed, recorded so nobody repeats it.** A spy patched
onto `customElements.get("file-detail").prototype.connectedCallback` counted
**zero** callbacks - across the sort, the append and a genuine remove +
re-attach. That is not evidence about the move: `customElements.define`
**snapshots** the reaction callbacks into the definition, so patching the
prototype afterwards is invisible to the reaction queue. The spy was checked
against a case it should have caught, found not to fire, and discarded.
`isConnected` is what the claim above rests on instead.

### Criterion 3: the run level's two side effects, by test rather than by reading

`setRunExpanded` still calls `syncRunDetailWidth` (TOR-174) and `detailShown`
(TOR-152), and neither went into the component - `accordion.js` names neither,
which a Go test enforces in both directions.

**And one of the two was not guarded at all, which is not what the ticket
assumed.** Four mutations, each run against the whole package:

| mutation | the pre-existing suite | the new executed test |
|---|---|---|
| delete `this.syncRunDetailWidth()` from `setRunExpanded` | **fails** - `rundetailwidth_test.go` catches it | fails |
| rename the property it writes to `--run-detail-width` | **PASSES** - that guard's check is `Contains(fn, "--run-detail-w")`, which the longer name satisfies | **fails**: 0 writes where 1, 2, 3 were expected |
| gate the call as `if (expanded && entry.infohash) detailShown(entry)` | **PASSES** | **fails**: 0 calls where 1 was expected |
| never call `detailShown` at all (`if (false) …`) | **PASSES - the whole package, green** | **fails**: 0 calls where 1 was expected |

The last row is the finding. `runtable_test.go` checks that `detailShown` is
declared in `SERVICES` and injected by `app.js` - a different claim, since a
service can be wired perfectly and never called. So the call TOR-152's ticket
describes as the one that "was once forgotten" was in fact **the least
guarded thing in that file**, and consolidating three disclosures into one
component is exactly the refactor that would have dropped it silently a second
time.

`TestTheDisclosureFiresTheRunLevelsSideEffects` runs the shipped `state.js`,
`accordion.js` and `run-table.js` under node against a DOM small enough to sit
in the test file, and observes both effects at their **far end** rather than by
wrapping the methods: the width is the custom property on `:root`, so a
`syncRunDetailWidth()` that runs and writes nothing fails; `detailShown` is the
injected service itself, so the table has to reach the page's own seam and hand
it the entry. Counts, in order: open 1 width / 1 shown, close 2 / 1, reopen
3 / 2 - **either direction rechecks the width, only an opening reads the
standing.**

Confirmed again on the live page with the two effects counted at their real far
ends (`CSSStyleDeclaration.prototype.setProperty` for the token,
`RunDetail.prototype.refreshAgain` for what `detailShown` calls): two keyboard
toggles of one row wrote the token twice more and called `refreshAgain` exactly
**once**, on the opening.

One detail worth keeping: counting the `GET runs/<id>/again` **request**
instead measured 0, because `refreshAgain` returns early for a row that "is not
settled, has no infohash, or was already asked this question". The call is the
thing to count; the request is downstream of a documented no-op.

### Criterion 5: keyboard, in full - and the screen-reader half is not attempted

**Screen readers were scoped out** while TOR-213 was in flight, and TOR-214 and
TOR-215 both recorded their versions of that criterion as *dropped* rather than
passed. Nothing here reads the browser's accessibility tree as a substitute:
that would be a different, easier measurement wearing the answer's clothes.

Keyboard operability was **not** scoped out, and every figure below is a real
key press delivered to the page, verified by `document.hasFocus()` and by a
`focusin` trail - the first attempt's key presses reached nothing at all while
the tool still reported "Pressed 1 key", so the trail is what makes this a
measurement rather than a hope.

Eighteen `Tab`s from `BODY`, with the ring each stop draws:

| Tab | element | level | focus ring |
|---|---|---|---|
| 1-9 | the nine sortable `.run-grid-head` | - | `2px rgb(34,224,232)`, offset -2px |
| 10, 11 | `button.run-row-main`, both rows | **1** | `rgb(153,200,255) auto 1px` - **the UA default** |
| 12 | `button.picker-open` | **2** | `2px rgb(34,224,232)`, offset 1px |
| 13 | the file's tick box | - | UA default |
| 14 | `button.meta-toggle` | **3** | `2px rgb(34,224,232)`, offset -2px |
| 15-18 | contact sheet, frame count, Regenerate, Compare | - | mixed |

Activation, `Return` and `Space`, on each of the three:

- **Level 3.** `Return` opened it (`aria-expanded` false -> true, glyph ▸ ->
  ▾, `.meta-body` 0 -> 194.8px); `Space` closed it again.
- **Level 2.** `Return` closed the file's block (0px, `data-expanded` false,
  and the run's detail shrank 778.1 -> 313.1px); `Space` reopened it to
  453.8px.
- **Level 1.** `Return` closed the row (detail 0px) and `Space` reopened it to
  778.1px - and the file inside stayed `aria-expanded="true"` through both,
  which is `TestCollapsingATorrentLeavesItsOpenFileOpen`'s property, live.

So all three levels are reachable and operable, and each is at least what it
was: nothing about the tab order, the keys or the rings changed, because the
toggles are the same three `<button>`s they always were.

**One measured gap, pre-existing and NOT fixed here.** Level 1's toggle
(`.run-row-main`) has no `:focus-visible` rule of its own, so it wears the UA's
default ring where levels 2 and 3 wear the project's accent one. It is
reachable and operable - a consistency gap, not an operability one - and giving
it a ring would be adding an appearance that does not exist today, which
criterion 4 asks not to do. Filed as **TOR-220** rather than decided here, with
the one thing that makes it a decision rather than a one-liner: the row already
carries a 3px accent rail when it is open, so the offset has to be chosen
against an open row as well as a closed one.

### What the design export got, and what it did not

Authored under `.design-sync/export/components/shared/Accordion/`: the card,
the bridge, the `.d.ts` and the `.prompt.md`, in the shape the other six
follow. **Not uploaded, and the checker not read back** - TOR-208 established
that the implementing agent has no DesignSync tool and that the manifest only
refreshes when the project is reopened, so criterion 6 is authored and handed
over rather than met.

The card carries **six** panes: all three levels, closed and open, statically -
a preview renders without scripts, so the open state has to be written into the
markup. And the export keeps to one uppercase-initial named export, because the
checker indexes those as components: `Accordion` and nothing else.

One shape decision worth recording, because it is the first bridge here that
refuses a prop: **the `.jsx` draws level 3 and takes no `level` prop.** At
levels 1 and 2 the element carrying the rail is a run-grid row or a file-list
`<li>` with a checkbox in it, so a `level` prop on a wrapper that draws neither
would construct a disclosure over the wrong elements and dress the Metadata
section as a torrent's line. The `.d.ts` exports the class for those two
instead, and the card carries their markup in both states.
