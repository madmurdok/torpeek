# CompareDialog

Two result sets of one film, compared by **flipping** between them on one
rectangle.

```jsx
const [comparing, setComparing] = useState(null);

<CompareDialog
  infohash={comparing?.infohash}
  index={comparing?.index}
  open={Boolean(comparing)}
  onClose={() => setComparing(null)}
/>
```

## What a design consumer is given, and why

**A FIXED SAMPLE OF TWO FRAMES, with `open` on the dialog** - in
CompareDialog.html.

Not an empty shell, for the reason the frame panel's first card taught this
export: a `<dialog>` without `open` is `display: none` in every UA stylesheet,
so a card of the closed component renders as nothing but the body's ground.

And not example data driven through the props, because the element's only entry
point is `open(infohash, index)` and that is two GETs against a torpeek server:
the pickers are filled from `/compare/sets` and the pairing comes from
`/compare`. There is no offline comparison to hand it. The `.jsx` still takes
`open`/`infohash` - that is the live door, and it is honest about doing nothing
without an `infohash`.

**The card's two arms are two real encodes of one frame**, one at source
quality and one heavily compressed. That is what this component is *for*, and
it is the only way a still picture of a flipbook can show it.

## FLIPPING BEATS TILING, and every rule follows from it

The same frame, in the same screen position, one keypress apart, makes a
difference visible that a side-by-side grid hides: the eye compares against its
own afterimage rather than across a gap. **So the picture does not move.** What
that costs, concretely - and none of it is negotiable, because it lives in the
element and the stylesheet:

1. **The stage's shape is decided before either frame loads**, from the arms'
   own resolution (`--compare-aspect`, a bare number so a `calc()` can multiply
   it), and set **once per comparison** - never per position. A stage that
   re-shaped itself as pictures arrived would move the picture.
2. **Both arms get a `src` at the same moment**, so a flip is a visibility
   toggle over already-decoded pixels rather than a fetch. A `src` is only
   re-assigned when it actually changes, so flipping back and forth never
   re-decodes anything.
3. **The hidden arm is `visibility: hidden`, never `display: none`.** Display
   none takes it out of layout and lets the browser drop its decoded pixels,
   and the whole feature is one keypress being instant.
4. **Nothing above the picture, and nothing sized, changes on a flip.** The
   dialog's width is pinned rather than shrink-to-fit, and every line below the
   stage is one line, `nowrap`, whatever it says - a dialog is centred by the
   browser, so a single wrapped line would re-centre the whole thing and slide
   the picture out from under the eye.
5. **The server does the pairing.** "The same frame" across two encodes means
   the nearest capture point *by fraction of duration*; the durations live in
   the manifests, and a page deriving that itself would be a second place the
   rule is written.

## The accent marks which arm is live, and nothing else does

Not a border, and not a semantic colour. Nothing has gone wrong when you flip,
so `--bad` or `--warn` would be a lie; and a border here would be a marking
drawn *on the picture's edge*, changing under the eye at the very moment the
eye is trying to hold an afterimage. It appears in exactly two places: the
numbered key chip above, and the live arm's timecode in the bar below.

## What the pairing left out is said out loud

A set of 8 flipped against a set of 20 has twelve points that are simply not in
the flipbook. Pairing them with whatever happened to be nearest would put a
difference on screen that the *film* caused rather than the encode, and a
person who counted twenty frames in the grid needs to be told where they went.
So `.compare-note` carries it, and three cases have their own sentences:

- no shared point near enough to pair at all;
- N of set 1's M and N of set 2's M capture points with no partner;
- paired by timecode rather than by fraction of duration, because one file
  never said how long it is.

## A position where the live arm captured nothing

`data-gap="true"` on the stage draws a dashed `--bad` rule over `--bad-dim`
with the engine's own code inside it - deliberately **the same idiom as the
frame grid's failed cell**, because it means the same thing there and here. It
fills the same rectangle the picture would have, so flipping onto a hole holds
position exactly as flipping onto a frame does.

**The shift is shown only for a frame that EXISTS.** A failed point's own shift
serialises to the word "unavailable", which is also one of the two failure
codes, so printing both would read `01:23 unavailable — no frame` and invite
exactly the wrong conclusion - that the shift is the reason. On a point with no
frame, the error is what says what lost it.

## What the stylesheets give it

- **compare.css** draws this component, and it is the closest thing in the
  export to one stylesheet for one component.
- **framepanel.css** lends it one class: `.lightbox-close`, which the close
  cross reuses rather than declaring a second one.
- **intake.css** carries the bare `select, button` rules the two set pickers
  and the four bar controls inherit.

## What a design must not do with it

- **Do not tile the two sets.** That is the design this component replaced.
- **Do not add anything that changes size on a flip.** Not a caption that grows
  a line, not a badge that appears on one arm only.
- **Do not label a set by its file name alone.** `setLabel()` puts the params
  key in full and *last*, because two sets of the same file of the same torrent
  differ in nothing a person can see except their frame counts - and two runs
  at the same count do not differ in that either.
- **Do not open it beside another modal.** The element closes every other open
  `<dialog>` itself, on purpose; a design that works around that is recreating
  a confusing picture.
- **Do not add a close affordance of your own.** It closes on Escape, on the
  backdrop, and on its own button.

## `setServices` is not a component

The checker lists it, because it is one of this module's two named exports and
the checker indexes named exports as components. It injects `url` and `log`.
There is nothing to render.

## The dark ground is the only ground

There is no light theme and no `prefers-color-scheme` in any of torpeek's
stylesheets. A light mock is not a variant.
