# Accordion

A thing that opens. torpeek's page has three of them, nested inside each
other, and this is all three.

```jsx
{/* Level 3, the innermost - the one shape the wrapper draws. */}
<Accordion label="Metadata" open={metaOpen} onToggle={() => setMetaOpen(!metaOpen)}>
  <dl className="specs">…</dl>
</Accordion>
```

For levels 1 and 2, render your own row - the card carries both, closed and
open - and drive it with the class, imported from the module rather than from
the bridge (a second uppercase export would put a second, unrenderable
"Accordion" in the component list):

```jsx
import { Accordion as Disclosure } from "../../../modules/accordion.js";

const acc = new Disclosure({
  level: 2, toggle: openBtn, region: slot, dressed: rowItem,
});
acc.apply(true);
```

## The three levels are real, and they are what the level parameter means

    1  a torrent's line     opens onto its detail row
    2  a video file's row   opens onto its block, inside that detail
    3  the Metadata header  opens onto the specs, inside that block

They genuinely nest: a file's block only exists inside an open run's detail,
and Metadata only inside the file's block. So `level` is not a size or an
emphasis, it is the depth - and **the depth is how loudly the open state is
dressed.**

| | 1 - run | 2 - file | 3 - meta |
|---|---|---|---|
| what carries `data-expanded` | `.run-row` | `.picker-item` | **nothing** |
| open ground | `--accent-dim` | none | none |
| open rail | `inset 3px 0 0 var(--accent)` | `inset 2px 0 0 var(--accent)` | none |
| toggle type | 13.1px, opacity 1 | 14px, opacity 1 | 12.8px, opacity **.75** |
| toggle chrome | none | none | `.35rem` radius, `--hover` on hover |
| region box | none of its own | `1px solid var(--rule)` on the left, 16.8px + 9.6px indent | 4.8px/5.6px padding |

Every figure there was measured in Chrome on the running page, with all three
open at once. **Nothing about the look was designed for this component**; it is
what the existing rules already produced, which is why the table reads as a
scale rather than as three arbitrary treatments: the rail thins 3px -> 2px ->
none and the one filled ground appears once, at the top.

The reason level 3 dresses nothing is worth keeping when composing: its rail
would sit inside the file's rail, which is inside the torrent's ground, and
three nested filled grounds read as chrome nobody asked for. The component
**refuses** a `dressed` element at level 3 rather than ignoring one - ignoring
it is how a level stops meaning anything.

## The triangle does NOT vary with the level

This was the finding. All three levels drew their mark with the same
declarations and the same two `::before` rules - three copies, in two
stylesheets, each with a comment saying it was deliberately the same object as
its neighbour's, because *opening a torrent and opening one of its files are
the same gesture and should not be two different marks.*

It is one rule now, `.disclosure-mark` in **base.css**: `flex: 0 0 auto`,
`width: .7em`, `opacity: .6`, `"▸"` closed and `"▾"` open. Measured on the
page: exactly six elements carry it, they are exactly the six that carry the
three old level classes, and each measures 0.6995-0.6998em - `.7em` with the
browser's own rounding, which is why the pixel widths differ (9.18 / 9.80 /
8.95) while the rule does not.

**A design must keep the fixed width.** It is what makes the label start at the
same x whichever way the triangle points, so a column of twenty-five file names
does not shuffle sideways when one of them opens. At level 2 the box is
reserved on **every** row - a non-video row's `<span>` included - with
`visibility: hidden` rather than `display: none`, for exactly that reason.

## What a design consumer is given, and why

**A CARD WITH SIX PANES: all three levels, closed and open.** A preview renders
without scripts, so every open state is written into the markup - which is also
what makes the card the thing to copy: `aria-expanded="true"` on the toggle, no
`hidden` on the region, `data-expanded="true"` on the row. In the product those
three are one `apply()` call.

**The wrapper draws level 3 only, and takes no `level` prop at all.** That is a
refusal rather than laziness: at levels 1 and 2 the toggle and the region belong
to a row this component does not own - the run grid's ten cells, or the file
list's `<li>` with its checkbox and its label - so a `level` prop would build a
disclosure over the wrong elements and dress the Metadata section as though it
were a torrent's line. A wrapper that drew those rows would be a second
RunTable and a second FileList. The `.d.ts` exports the class itself
(`AccordionElement`) for that case: construct it over your own markup and call
`apply`.

## Three things it deliberately does not do

**It does not remember.** `open` comes from outside, every time, and the flag
lives on the run's own record. Two reasons, one per end of the nesting: at the
run level that record is what the page reads to decide what a click means, so a
copy in the component would be a second answer to one question; at the two
inner levels the disclosure is genuinely thrown away and rebuilt whenever the
file list is, so anything it remembered would come back closed under whoever
had opened it.

Either way the DOM under it moves. torpeek's run table re-sorts on every redraw
and relocates a row's whole element - measured, the subtree leaves the document
and comes back, `isConnected` false then true on the row group and on both
custom elements inside it - and the row comes back open, because the answer was
never in the DOM to begin with.

**It owns no listener.** Three levels, three activating elements, three
exceptions - and one of them is load-bearing: a torrent's click listener must
stay on `.run-row` and never on the wrapper around the row and its detail, or
every click inside an open detail collapses it (a picker checkbox, a thumbnail,
Compare). A component that learned all three would be the page. So the listener
stays with the element it is on, and the component is only ever told the answer.

**It does not know what opening MEANS.** Opening a torrent's row also rechecks
the detail pane's width (a detail arriving can add or remove the document's
scrollbar) and reads that run's top-up standing off disk. Opening a file's
block closes its siblings and reads its other result sets. None of that is what
opening means in general, and a component that did any of it would do it for
the Metadata block too.

## Collapse is the `hidden` attribute, and only that

No rule in any of torpeek's stylesheets sets `display` on `.run-detail-row`,
`.file-detail` or `.meta-body`. That is deliberate: a class selector declaring
`display` beats the UA's own `[hidden] { display: none }` at equal specificity,
and the region would then never go off screen while every state flag said it
had. This codebase has paid for that trap six times in one file.

So: hide a region with `hidden`, never with a class, and do not give a region a
`display` of its own.

## The stylesheets it needs, and this is the sharpest case of the split

| what | where |
|---|---|
| `.disclosure-mark` - the triangle, all three levels | **base.css** |
| `.run-row-main`, `.run-row[data-expanded="true"]`, `.run-row-group`, `.run-detail-row` | **table.css** |
| `.picker-open`, `.picker-item[data-expanded="true"] > .picker-file`, `.file-detail`, `.meta-toggle`, `.meta-body` | **filelist.css** |

The export's split is by **area**, and this one component reaches into three
areas including the page shell. A rendered design receives all of
`styles.css`'s transitive `@import` closure, so nothing is missing - but a mock
that assumed the disclosure lived in one place would lose its triangle.

## Keyboard

All three toggles are real `<button>`s and always were, so this is unchanged by
the component - measured with real key presses rather than synthesised events:

- Eighteen `Tab`s from `BODY` reach all three (level 1 at Tab 10 and 11, level
  2 at 12, level 3 at 14).
- `Return` and `Space` both activate all three.
- Levels 2 and 3 draw a `2px var(--accent)` focus ring (offset 1px and -2px
  respectively). **Level 1 wears the browser's default ring**, because
  `.run-row-main` has no `:focus-visible` rule of its own - a known gap, filed
  as its own ticket rather than fixed on spec, since adding a ring would be
  adding an appearance that does not exist today.

Screen-reader behaviour is **out of scope** for this project and no baseline
exists. Nothing here reads the browser's accessibility tree as a substitute for
one.

## The ground is dark, and there is no other

torpeek follows no host preference: there is no light theme and no
`prefers-color-scheme` in any of its stylesheets. A light mock is not a
variant.
