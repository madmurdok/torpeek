# FileDetail

One video file's own detail: what it is, where its frames came from, and the
frames themselves.

```jsx
{/* In real use <file-list> creates one per video row and appends it as a
    SIBLING of the row inside that row's <li>. It has no props. */}
<FileDetail />
```

## What a design consumer is given, and why

**A FIXED SAMPLE, in the card - plus the row above it.**

Two reasons an empty shell is not available:

- **The element is two-stage.** `build()` puts an empty
  `<div class="file-detail" hidden>` inside it and mints the id the row's
  disclosure names; `mount()` fills that slot the first time the file says
  anything. Built-but-unmounted, this component is a hidden empty div - which
  is the *right* state for a file nobody has ticked (a panel of empty headings
  is worse than a row that does not open yet), and a card of it would render as
  nothing at all.
- **`mount(entry, index, row)` cannot be called from a design.** Its third
  argument is a bundle of live elements `<file-list>` built.

**And the row is part of the picture.** Since TOR-182 **the row IS this file's
title line**: the name, the size and the summary this element keeps current
(resolution, frame count) are a *column* of the file list, not a line inside
the slot - four row elements are handed over at mount. Rendered alone this is a
detail with no name on it. The card carries the one `<li>` that owns it and no
more of the list than that; a mock should do the same.

**The sample's state is a file that has FINISHED on a row that has not.** That
is a real combination - an earlier pass captured this file while the pass in
flight fetches a different one - and it is the only one that puts every part on
screen at once: the contact sheet exists, the reach strip exists, and the swarm
chip has a live reading because the *run* still has a client. The heartbeat line
is absent, because `file_done` set that reading to null.

## The two stylesheets, and this is the mismatch the ticket names

| what | where |
|---|---|
| the slot, `file-detail { display: block }`, the metadata accordion, `.specs`, `.tracks`, `.file-links`, `.file-progress`, the Regenerate row | **filelist.css** |
| `.grid`, `.thumb`, `.thumb-box`, `.thumb-code`, `.thumb-delete`, the four cell states, `.reach`, `.reach-strip`, `.reach-block`, `.reach-note`, `.avail-swarm*` | **framepanel.css** |
| the bare `select, button` and `input[type="number"]` rules Regenerate, Compare and the frame-count field inherit | **intake.css** |

**"One stylesheet per component" is not available here, and this is the
sharpest case.** The split is by AREA: filelist.css is "picking which files to
capture from", framepanel.css is "seeing the frames a run captured", and this
one element does both. A rendered design receives only `styles.css`'s
transitive `@import` closure, and all three files are in it - but a mock that
assumed framepanel.css belongs to the frame panel would lose this component's
grid, its strip and its chip.

## The grid's four states, spent unevenly on purpose

- **exact** gets *no marking at all*. Most cells are exact, the picture is the
  content, and a grid that marks every cell marks none of them.
- **shifted** keeps the plain thumbnail and gains exactly one signal: a warn
  rule under the image, with the word already in its caption and the reason on
  inspection. The frame is real and worth looking at; it just came from a
  neighbour.
- **failed** is the only state with a **border**, which is what makes a border
  in this grid mean *nothing was captured here* and nothing else. It is also
  the only one with text inside the cell, because the reason is the whole
  content when the picture is missing.
- **pending** is the quietest of the four, deliberately: it is the normal state
  of a run that is still working, and it must not read as an error while it
  waits. No animation - the progress line already says the run is alive.

**A pending cell cannot appear beside a reach strip**, and the card shows the
two shapes separately for that reason: pending cells exist only while the
file's plan is set, and `loadFileDetail` clears the plan at the same moment it
puts the strip on screen.

**Captions are TIMECODES, never frame indices.** Every result set numbers its
own frames from zero, so two different moments would both read `#3` in a merged
grid. A pending or failed cell shows where the point *was planned*, because
nowhere else is true for it.

**The cross is only on a cell that has a frame and names its result set.** A
failed point has no file to delete, and offering the cross there would offer an
action that cannot succeed. There is no confirmation step: Regenerate is the
way back, and a dialog on every thumbnail would make clearing a handful of bad
frames worse than the frames are.

## The reach strip and the swarm chip say different things

- **The strip** is where in the file *this set's* frames came from: one block
  per piece until there are more pieces than blocks worth drawing, then one
  block per several. Each block is a vertical **fill**, not a flag - at one
  piece per block it is a map, above that a density, and the caption says
  which ("each block is 13 pieces"). With a floor, because 4 claimed pieces of
  a 13-piece block would paint about a pixel. A block with no claimed piece
  stays empty. The accent carries it: pieces the run ordered are not a warning,
  they are the thing the tool did.
- **The chip** is what the swarm currently holds, **torrent-wide** - one mean
  copies-per-piece figure. So it is its own box in a **semantic** colour
  (`ok` / `warn` / `bad` / `unknown`), never the accent and never a fill on the
  strip's own axis: painting it along this file's span would claim a
  per-position resolution it does not have.
- **Absent is not zero here either.** A set whose manifest predates the field
  cannot say where its frames came from, so the strip **hatches**
  (`data-known="false"`) rather than rendering full or empty - and the chip
  stays beside it, because the swarm reading does not depend on this file's own
  claims.
- **Two ticks would be one fact drawn twice.** TOR-142 shipped a second row of
  capture-point marks positioned from these same ranges; TOR-153 removed it. A
  filled block already *is* a capture point's footprint. Do not add it back.

## What a design must not do with it

- **Do not open two files of one torrent at once.** Opening one closes its
  siblings, on purpose. Compare is the answer for two result sets of one file;
  for two different files, the level above allows two torrents open side by
  side.
- **Do not add a title line inside the slot.** The row is the title line; a
  second one would be the same line twice.
- **Do not expect the metadata open.** It starts closed with no auto-expand
  exception - the frames are what the file was opened for. The card opens it
  only so the specs are visible in a picture.
- **Do not give the slot a `display` of its own.** `hidden` is the whole of
  collapse, and a class rule declaring `display` would beat the UA's own
  `[hidden] { display: none }` - a trap this file has paid for six times.
- **Do not put a percentage on the reach strip alone.** Under half a percent is
  reported as `<1%`, because `(0%)` beside "44 of 10000 pieces" would be a
  rounding that contradicts the number next to it.

## `setServices` is not a component

The checker lists it, because the checker indexes named exports as components.
It injects `url`, `post`, `del`, `log`, `showError`, `began`, `intake`,
`openFrame` and `openCompare` - the last two being the frame panel's and the
compare dialog's own doors, since there is one of each on the page rather than
one per file. There is nothing to render.

## The dark ground is the only ground

There is no light theme and no `prefers-color-scheme` in any of torpeek's
stylesheets. A light mock is not a variant.
