# RunDetail

What a torrent's expanded row says about **the run**: its badge and name, its
`.torrent`, and - once it has finished - what finishing it properly would cost.

```jsx
{/* In real use <run-table> hands out the cell and app.js mounts one of these
    in it. Standalone, it renders empty and fills itself from an entry: */}
<RunDetail entry={entry} />
```

## What a design consumer is given, and why

**A FIXED SAMPLE, in the card - and there is no alternative.**

This element writes its own markup. `build()` runs
`this.innerHTML = '<div class="run-detail">' + DETAIL + '</div>'` on connect,
because there is one detail per torrent and `index.html` has never held it. Two
consequences a design has to know:

- **An empty shell shows nothing.** Every line of it is filled from a run
  entry by `syncDetail()`; with nothing bound, it is a blank block.
- **Children written in JSX are destroyed.** That `innerHTML` is the first
  thing the element does. Only `<run-table>`, `<compare-dialog>` and
  `<frame-panel>` keep their markup declarative and can take children.

**A PARENT MOUNTS IT IN REAL USE.** `<run-table>`'s `newRow()` hands out a
colspanned `detailCell` and app.js mounts one of these there; nothing puts a
`<run-detail>` anywhere else. That matters for more than tidiness - see the
next section.

**The sample is a SETTLED run**, so the top-up offer is on screen. That is the
substantial half of this component and it exists only on a row that has
finished. On a live row it is the other way round: no `.run-again` at all, and
Cancel visible instead of `data-idle`.

## The stylesheets, and where the area split does not match

- **detail.css** draws this component: the corner brackets, the header's fixed
  action order, the error line, the top-up block.
- **table.css** draws two things inside it. `.run-badge` and all nine of its
  `[data-state]` colours are the TABLE's file, and this header wears the same
  badge; and `.run-table` itself is what the detail is mounted in.
- **detail.css's own `.run-table .run-detail-cell`** is the reverse case: a
  rule about a cell `<run-table>` builds, living in the detail's file, with the
  table's class *required* in the selector (`.run-table td` sets the shared
  padding at (0,1,1), so a bare `.run-detail-cell` would lose to it).

**So this component cannot be drawn standalone and look right.** Its ground -
one rung up from the page - and the accent rail continuing from the open row
above it both come from that cell rule, so a `<run-detail>` with no
`.run-table` ancestor floats on the page ground with no rail. The card carries
a one-row `<table class="run-table">` for exactly this reason, and a mock
should too.

- **filelist.css** draws the nested `<file-list>` (see FileList).
- **intake.css** carries the bare `select, button` and `button:disabled` rules
  that Cancel, Send to my client, Top up and Retry all inherit.

## Top up and Retry are two jobs that look like one button

Which one a row offers is decided by what the row **is**, never by asking the
person to know the difference:

- **Top up** finishes a run that produced something and stopped at a ceiling.
  Offered only on a settled row with a priced answer and points still missing.
- **Retry** runs one that produced nothing again - `failed`, or `cancelled`.
  Same source, same files, same ceiling, and deliberately no raise.

**The figures are the server's arithmetic**, priced off the manifest on disk.
The line states the multiplication (`8 × 3 file(s)`) because that is the trap:
`-n` is frames *per video file*, so the ceiling a person meets is the per-file
figure times the files selected. And the traffic figure is a **ceiling sized to
finish in one press, not an estimate of what it will cost** - the sentence says
so, and a design must not shorten it into a price.

Three different ceilings can stop a run and the offer says which, because only
one of them is helped by more traffic:

| limit | the sentence | does a raise help |
|---|---|---|
| `traffic` | stopped at this run's own traffic ceiling | yes |
| `time` | stopped at this run's own time limit | no |
| `roof` | stopped at the client-wide traffic roof | no |

## Cancel is `visibility: hidden`, never `display: none`

`data-idle` keeps Cancel's box in the flow. `[hidden]`'s `display: none` would
let Save .torrent slide sideways into the gap the instant the run stops being
cancellable - the same "control that jumps under the cursor" the queue arrows
are disabled rather than hidden to avoid. **Do not replace `data-idle` with
`hidden`.**

## What a design must not do with it

- **Do not draw a second title line inside it.** The row above already carries
  the name; TOR-182 removed the file-level one for exactly this reason and the
  same argument applies here.
- **Do not put the top-up offer inside the file list.** It is about the run,
  and since TOR-195 that is structural: the files are in a nested
  `<file-list>` and this offer is outside it.
- **Do not add a second confirmation to Top up.** The figure is read before the
  press; this page has no confirmation dialogs anywhere, deliberately.
- **Do not show `Send to my client` unconditionally.** It needs a watch
  directory on the host (`-watch-dir`); without one the whole paragraph is
  hidden rather than offering a button that cannot work.

## `setServices` is not a component

The checker lists it, because it is one of this module's two named exports and
the checker indexes named exports as components. It is the function app.js's
bootstrap calls to inject `url`, `post`, `log`, `showError`, `redraw` and
`cancelRun`. There is nothing to render.

## The dark ground is the only ground

There is no light theme and no `prefers-color-scheme` in any of torpeek's
stylesheets. A light mock is not a variant.
