# torpeek ui

torpeek's front end, as a design system. All six of its custom elements ship
here, in the shape the frame panel established - and since TOR-212 one thing
that is not an element: the accordion.

## Components

Grouped by area, which is also how the stylesheets are split - though the two
splits do not line up, and each component's own `.prompt.md` says where they
part company.

**Table**

- RunTable - every torrent, live or on disk, as nine columns that sort, resize
  and open (`<run-table>`)
- RunDetail - what an expanded row says about the RUN, and the price of
  finishing it (`<run-detail>`)

**Files**

- FileList - every file the torrent holds, what a tick spends, and what an
  un-tick would do (`<file-list>`)
- FileDetail - one file's metadata, where its frames came from, and the frame
  grid (`<file-detail>`)

**Panels**

- FramePanel - full-size frame viewer (`<frame-panel>`)
- CompareDialog - two encodes of one film, on one rectangle, one keypress
  apart (`<compare-dialog>`)

**Shared**

- Accordion - a thing that opens, at any of the three depths the page has
  (**no tag - not a custom element**, see below)

### Accordion is the one component that is not an element

Every other entry here wraps a custom element. The accordion cannot be one,
and the reason is structural rather than stylistic: a custom element has to BE
somewhere in the tree, and at each of the three levels the only place to put a
wrapper is between a grid container and its items.

- `.run-row-group` carries `grid-template-columns: subgrid`, which only works
  on a **direct** grid item of `.run-grid`. An element around it breaks the
  column alignment TOR-214 measured to 0.00px - so the wrapper TOR-212 waited
  for is precisely the thing the component must not become.
- At the file level the toggle is inside the row's `<label>` and the region is
  that row's sibling inside the `<li>`. There is no box that holds both and
  only both.

So it ships as a plain class over elements the page already has. Its `.jsx`
draws level 3 in full and the `.d.ts` exports the class itself for the other
two, where the toggle and the region belong to a row the component does not
own.

It is also the only entry whose module exports **one** name, which is the
export rule at its cleanest: the class, and nothing else.

### `setServices` is not a component

Five of the six ELEMENT modules export a second name, `setServices`, and the
checker indexes a module's named exports as components - so five entries in its
list are that function rather than anything renderable. They are not a bug and
they cannot be dropped: `app.js` imports each one BY NAME to inject the
requests, the log and the page's own error line, and the injected-services
setter is point 7 of the element pattern in `docs/front-end.md`. Removing one
to tidy this index would break the page.

Every other dead export WAS dropped (TOR-209's rule, applied across all six):
twenty-odd entries for six components became eleven - the six classes, and
five setters. **TOR-212 adds one entry and no residue**: `accordion.js` injects
nothing, so it has no setter, and its whole export block is the class. It is
the one module here that costs the index exactly what it is worth.

### What a design is given, per component

Two shapes, and which one applies is decided by where the element's markup
lives rather than by preference:

| | markup | children in JSX | what the card is |
|---|---|---|---|
| RunTable, CompareDialog, FramePanel | declarative in `index.html`, wrapped | **survive** - compose freely | the finished state, statically |
| RunDetail, FileList, FileDetail | a template the element writes into itself | **destroyed** by its own `innerHTML` | the only sample there is |
| Accordion | none - it is handed elements the page already has | **survive** - they are what it opens | six panes: three levels, closed and open |

For all seven, the card is the markup to copy. None of the six elements can be
driven from props alone: five of them are built from a **run entry**, which is
assembled out of `state.js`'s `newRunState`, `<run-table>`'s own row parts and
`app.js`'s detail half, and nothing outside a running torpeek can mint one.

**Accordion is the exception, and the only one here that CAN be driven from
props.** It needs no entry: `level`, `open` and a callback are the whole of it,
because the one thing it must not have is state of its own. So it is the one
component a design can compose against directly - which is what makes it worth
exporting first among equals, since "a thing that opens" is the page's most
characteristic interaction and the six elements offered no way to say it.

## What this is, and what it is not

torpeek's UI is **custom elements and ES modules with no build step**. Nothing
in the repository is compiled, bundled, transpiled or minified: the file the
browser runs is the file in the repository.

`_ds_bundle.js` is the one thing here that torpeek does not write. It is the
**platform's own output**, compiled from the `.jsx` sources, and anything
uploaded under that name is replaced. The element's real module travels beside
the wrapper and the wrapper imports it for its side effect:

    import "./frame-panel.js";

which is enough, because that file ends in
`customElements.define("frame-panel", FramePanel)`. The bundle's namespace is
generated as well, so nothing here writes to a global of its own choosing.

So the `.jsx` in each component directory is a **bridge, not an
implementation**. The behaviour lives in the element; the wrapper gives it a
React-shaped door, because a design is written in JSX and a web component is
not. There is no logic in a wrapper to get wrong.

## A web component composes here, and that is now measured

Whether a design agent could render and compose a web component from this
layout was the open question this upload was built to answer. **It can.** A
working screen was built against `<frame-panel>`: the element, the markup and
`panel.open()` composed without special handling, and mounting a custom element
turned out to be a path the agent already had.

That is why the other five follow rather than wait, and they now have.

## The stylesheets' order is load-bearing

`styles.css` `@import`s all eight files in the order `index.html` links them,
and at equal specificity the later FILE wins. torpeek has four rule pairs that
depend on it. Reordering those imports is a design change, not a formatting
one.

**And the split is by AREA, not by component.** The six elements do not map
onto the eight stylesheets, in three places worth naming here because a shorter
closure would silently break one of them:

- **intake.css is not the intake's.** It carries `select, button`,
  `input[type="number"]`, `button:hover:not(:disabled)`, `button:disabled` and
  `select:focus-visible` as BARE ELEMENT rules, so every control in every one
  of the six is drawn by it - the run detail's Cancel/Top up/Retry, the file
  list's Select all and Clear frames, a file's Regenerate/Compare and its
  frame-count field, and both of the compare dialog's pickers. It has to come
  BEFORE the area files, which override it per control.
- **framepanel.css is not only the panel's.** `.grid`, `.thumb*`, `.reach*`
  and the `.avail-swarm` chip live there and are built by `<file-detail>`.
- **detail.css holds `.run-detail-cell`** - a rule about a cell `<run-table>`
  builds. It read `.run-table .run-detail-cell` when this was written, and
  `<run-detail>`'s card carries a wrapping table for that reason; TOR-215
  dropped the ancestor when the table became a grid (the `.run-table td` rule
  it was outranking went with it), so the selector is bare now and the cell no
  longer needs a table around it. Corrected here rather than left standing
  because it is a claim a design consumer would act on; the two cards that
  still show the old table markup are **TOR-218**'s, not this file's.

So "one stylesheet per component" is not available today. The export ships the
whole closure and each `.prompt.md` names what its component needs.

## The ground is dark, and there is no other

torpeek follows no host preference: there is no light theme and no
`prefers-color-scheme` in any of its stylesheets. A light mock is not a
variant.
