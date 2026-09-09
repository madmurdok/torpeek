# Accordion

A thing that opens. torpeek's page has three of them, nested inside each
other, and this is all three.

**It WRAPS its content, and it takes a `level`.** One bridge, all three
depths - the level decides the box's, the header's and the region's tag and
class, and everything else is yours:

```jsx
{/* Level 3, the innermost. */}
<Accordion level={3} label="Metadata"
           open={metaOpen} onToggle={() => setMetaOpen(!metaOpen)}>
  <dl className="specs">…</dl>
</Accordion>
```

At the outer two levels the HEADER is a row, so its other contents come in as
`summary` and the click handler goes on the header rather than on the toggle -
which is what the product does, and is not a detail you can skip (see "It owns
no listener" below):

```jsx
<Accordion level={1} label={run.name}
           summary={<>
             <div className="run-cell run-cell-when">{run.added}</div>
             …the other eight cells…
           </>}
           summaryProps={{ onClick: () => toggleRun(run) }}
           open={run.expanded}>
  <div className="run-detail-cell">…</div>
</Accordion>
```

They nest exactly as the page does: a `level={2}` inside a `level={1}`'s
children, a `level={3}` inside that.

The class itself is still there for markup you built yourself, imported from
the module rather than from the bridge (a second uppercase export would put a
second, unrenderable "Accordion" in the component list):

```jsx
import { Accordion as Disclosure } from "../../../modules/accordion.js";

const acc = new Disclosure({
  level: 2, container: rowItem, summary: rowLabel, toggle: openBtn, region: slot,
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
| the BOX (carries the level) | `.run-row-group` | `li.picker-item` | `section.meta` |
| the HEADER, and what carries `data-expanded` | `.run-row` | `label.picker-file` | `h3.meta-title`, **undressed** |
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
three nested filled grounds read as chrome nobody asked for. **The LEVEL
decides it, not the caller**: level 3's row in the component's own table
carries no rail, so `apply()` writes no `data-expanded` there however it is
called. There is nothing to pass and nothing to forget.

And the two rails are not prose. A Go test parses the rules that dress an open
disclosure out of the served stylesheets and requires the component's table to
match them - widths, the one ground, and that they thin with the depth - so
this table cannot drift away from what the CSS draws.

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

**A CARD WITH SEVEN PANES: all three levels closed and open, plus one more.** A
preview renders without scripts, so every open state is written into the markup
- which is also what makes the card the thing to copy: `aria-expanded="true"`
on the toggle, no `hidden` on the region, `data-expanded="true"` on the header,
`data-accordion-level` on the box. In the product those first three are one
`apply()` call and the fourth is the constructor's.

The seventh pane is the same open level-1 disclosure with `.run-table-wrap` and
`.run-grid` **removed**, and it is there because "needs no particular
container" is the kind of claim worth rendering rather than asserting. Measured
off that card: the two rows resolve byte-identical tracks and identical cell
offsets to the last decimal. What the bare one loses is the table's, not the
disclosure's - the wrap's ground, its horizontal scroll, and its type size (the
row is 14px bare against 13.12px wrapped, since the `.82rem` comes from the
wrap).

**One bridge with a `level` prop, drawing all three.** The version before this
one drew level 3 and refused the prop, on the grounds that levels 1 and 2 meant
"a run-grid ROW or a file-list `<li>` with a checkbox in it, neither of which
is this component's to draw". That was true of the header's CONTENTS and wrong
about the disclosure: the box, the header and the region are the same three
things at every level, and only their tag and class vary. So the bridge draws
those three and the cells or the checkbox come in as `summary`. Nothing was
lost - the class is still exported from the module for markup you built
yourself.

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
stay on `.run-row` and never on the BOX around the row and its detail, or every
click inside an open detail collapses it (a picker checkbox, a thumbnail,
Compare). A component that learned all three would be the page. So the listener
stays with the element it is on, and the component is only ever told the answer.

Worth separating, because it is the one thing about this component that reads
as a contradiction and is not: "the listener must not go on the box" blocks the
box from LISTENING. It never blocked the box from EXISTING. The component owned
no listener before it was a wrapper and owns none now, so `onToggle` is a
convenience for level 3 and the outer two put their handler on the header
through `summaryProps`.

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
| `.run-row-main`, `.run-row[data-expanded="true"]`, `.run-row` and its ten tracks (`--run-tracks`, from **tokens.css**) | **table.css** |
| `.picker-open`, `.picker-file[data-expanded="true"]`, `.file-detail`, `.meta-toggle`, `.meta-body` | **filelist.css** |

The export's split is by **area**, and this one component reaches into three
areas including the page shell. A rendered design receives all of
`styles.css`'s transitive `@import` closure, so nothing is missing - but a mock
that assumed the disclosure lived in one place would lose its triangle.

## Keyboard

All three toggles are real `<button>`s and always were, so this is unchanged by
the component - measured with real key presses rather than synthesised events:

- Eighteen `Tab`s from `BODY` reach all three (level 1 at Tab 10 and 11, level
  2 at 12, level 3 at 15 with a Clear-frames button in the row).
- `Return` and `Space` both activate all three, measured at each level: Return
  closes and Space reopens, and closing a torrent leaves the file inside it
  still `aria-expanded="true"`.
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
