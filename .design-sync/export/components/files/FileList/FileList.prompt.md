# FileList

Every file the torrent holds, video or not. A tick starts that file's frames
on the spot; the row says what that will cost first; and an un-tick means one
of five different things.

```jsx
{/* In real use <run-detail> creates one of these and appends it last. */}
<FileList entry={entry} />
```

## What a design consumer is given, and why

**A FIXED SAMPLE, in the card - and FOUR lists rather than one.**

The element writes its own markup (`this.innerHTML = LIST`, then
`renderFileList()` replaces the whole `<ul>`), so an empty shell shows a hidden
`.picker` and nothing else, and children written in JSX are destroyed. The
sample has to be markup.

**Four lists, because the five verdicts are partitioned by the row's state**,
and that is a fact about the entry rather than a presentational choice:

| set on the entry | non-empty only while | verdict |
|---|---|---|
| `entry.deferred` | the row is **running** (ticked mid-pass) | `drop` |
| `entry.fetching` | the row is **running** | `stop` |
| `entry.narrowable` | the row is **queued or parked** | `narrow` |
| `FINAL.has(state)` + frames on disk | the row has **settled** | `clear` |
| none of the above | the row is **running** | `""` |

A row cannot be running and queued at once, so one list would have had to fake
a state. Select all is parked-only for its own reason, which is a fourth list.

## THE FIVE VERDICTS, and why they are mostly invisible

This is the difficult part of the component and the reason to read the card's
titles rather than only look at it. Four of the six row states are the **same
ticked, live checkbox**; what differs is the row's own `title`, and that
sentence *is* the mechanism - TOR-184's acceptance is that the two gestures are
distinguishable **before** they are made.

- **`drop`** - ticked while the row was already fetching. Nothing has been
  asked of the swarm for it, so un-ticking stops that one file and nothing
  else. The only per-file stop that honestly exists. Marked on screen, because
  it is the one exception a reader cannot see: `in the next pass`.
- **`narrow`** - the pass is chosen and the engine has not been handed it, so
  the file comes out. If it is the last one the row goes back to needs-action.
  The sentence says *both* outcomes, in that order.
- **`stop`** - the engine is fetching it now, and `core.Engine` has no
  per-file stop: one context for the whole run, one budget sized before it
  started. **So this stops the whole run**, and the sentence says so, plus
  that nothing on disk is lost. It also appears once for the row, on screen,
  in the list's own heading - a hover title is not readable on a touch screen.
- **`clear`** - a settled file with frames on disk. The un-tick **changes
  nothing by itself**: it only reveals Clear frames, and re-ticking hides it
  again. Note the exact opposite wording to `stop`'s, which acts on the press.
- **`""`** - an earlier pass on this row captured it while the pass in flight
  fetches something else. Nothing to stop, nothing yet to clear, so the box is
  the page's **one disabled ticked box** with the reason in full.

**A box is live only where the gesture can actually be carried out.** That is
the rule underneath all five, and it is why Select none no longer exists: a box
that moves and then does nothing is a control that lied.

## Prices, and the one place a cost is shown in advance

`-n` is frames **per video file**, so a torrent of six quality variants costs
six times the intake's number. This is the only screen in the whole program
that states a cost *before* it is spent, and it follows both inputs live: the
ticks, and the count on the intake line.

- Each tickable row carries its own price where the box that spends it is.
- **Select all is the most expensive click on the page.** It arms on the first
  press and states the multiplication - `2 file(s) × 8 = 16 frames` - and only
  the second press spends. Three carriers say it is armed and only one is a
  colour: the label changes, a sentence appears beside it, and the button
  wears `--warn`. **Do not replace this with a `confirm()` or a dialog**: this
  page deliberately has none anywhere, an unstyleable modal cannot state a
  figure in the page's own type, and a blocking dialog cannot be seen in a
  screenshot of what the page said.
- **Clear frames carries its figure in the button** for the same reason -
  `Clear 5 frames`, read from the same count the summary beside it shows.

## Non-video rows say so without colour

They cannot be ticked - the server validates a tick against exactly the video
list, and offering one it would refuse is a control that lies. Three carriers,
only one of them a colour: they read in `--ink-2`, **the checkbox is absent**
and an em dash sits where it would have been, and the row's title gives the
reason in words. The wording names **both** reasons a file can be missing from
the video list (an extension torpeek does not open, *or* a sample beside a much
larger video) because the page cannot tell them apart and must not guess.

## What the stylesheets give it

- **filelist.css** draws this component: `.picker` and its `--warn` parked
  frame, the rows, the disclosure triangle, the price/state/summary columns,
  Clear frames and the note beside it.
- **intake.css** carries the bare `select, button`, `button:hover:not(:disabled)`
  and `button:disabled` rules that Select all and Clear frames inherit.
- **filelist.css also draws most of `<file-detail>`** - its slot, the metadata
  accordion, the specs, the links and the Regenerate row - while that
  element's grid, reach strip and swarm chip are in **framepanel.css**. So
  this file is not one component's, and the file's detail is not one file's.
  See FileDetail.

The two `file-list { display: block }` and `file-list[hidden]` rules are worth
knowing about: an unknown element is `display: inline` by default, which would
put a block-level `<section>` inside an inline box. The `[hidden]` companion is
redundant today and kept on purpose - a type selector declaring `display` would
beat the UA's own `[hidden] { display: none }`, and that trap has cost this file
six times.

## What a design must not do with it

- **Do not bring back a "Take frames" button under the list.** Since TOR-181 a
  tick *is* the decision; a button would be a second click for something
  already done.
- **Do not bring back Select none.** It used to clear a selection nobody had
  committed. Un-ticking now means one of five real acts.
- **Do not hide the list because of the row's state.** It is hidden only when
  there is nothing to list. A queued row whose metadata has not arrived and a
  disk row whose replay failed are both "not known yet", which an empty framed
  box would state as "holds nothing".
- **Do not put the whole sentence inside the `<label>`.** The row is a label,
  and a paragraph in it would join the checkbox's accessible name - which is
  why `.picker-note` is a sibling of the row, not part of it.
- **Do not let a `<label>` pick its control by DOM order.** It names it by id
  (`for`). A labelable element appearing first in the row silently became the
  label's control once and cost the whole of TOR-181's tick.

## `setServices` is not a component

The checker lists it, because it is one of this module's two named exports and
the checker indexes named exports as components. It injects `url`, `post`,
`del`, `log`, `showError`, `count` and `cancelRun`. There is nothing to render.

## The dark ground is the only ground

There is no light theme and no `prefers-color-scheme` in any of torpeek's
stylesheets. A light mock is not a variant.
