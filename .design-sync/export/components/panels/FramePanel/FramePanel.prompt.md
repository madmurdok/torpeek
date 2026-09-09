# FramePanel

torpeek's full-size frame viewer: a modal dialog showing one captured frame,
at fit or at 100%, with a pan that cannot leak.

```jsx
const [shown, setShown] = useState(null);

<FramePanel
  src={shown?.url}
  caption={shown ? `${shown.file} at ${shown.timecode}` : ""}
  open={Boolean(shown)}
  onClose={() => setShown(null)}
/>
```

## What it is for

One frame, examined. It opens from a thumbnail in a grid and it is the only
place in torpeek where a picture is shown at its own resolution. It is not a
lightbox carousel: there is no next/previous, because the grid behind it is
the way through the frames.

## Four rules the panel obeys, and they are not negotiable

These are measured behaviour, not preferences, and a design cannot opt out of
them because they live in the element:

1. **A picture that fits is drawn at 100% and NEVER upscaled.** A small frame
   stays small and the panel shrinks to meet it. If a mock shows a 320-wide
   frame filling a 1300-wide panel, the mock is wrong.
2. **A picture that does not fit is scaled down to fit**, by whichever axis
   runs out first.
3. **A click goes to 100%, and then it pans** - with the arrow keys or by
   moving the mouse, no button held. This is the common case, not the rare
   one: frames come out at the video's own resolution.
4. **It never spills outside the panel**, and panel ground never shows as a
   gutter at an edge.

**The panel's box is the picture's fit size in BOTH zoom states.** It is set
once per picture and does not change when the zoom does. This is worth knowing
before designing around it: a dialog is centred by the browser, so a box that
grew on a click would re-centre the whole panel and slide the picture out from
under the eye. Zooming changes what is drawn inside the window, never the
window.

## Layout

    ┌─────────────────────────────────┐
    │ ›› FRAME  [fit]                 │  label strip - the state chip is the
    ├─────────────────────────────────┤  one thing wearing the accent
    │                            (×)  │
    │          the picture            │  the window: clipped, and the close
    │                                 │  button and caption sit OVER it
    │  caption at 00:12:34            │
    ├─────────────────────────────────┤
    │ click zooms · ← ↑ → ↓ pans      │  key hints, in the accent
    └─────────────────────────────────┘

Corner brackets in the accent mark three corners; the fourth is a chamfer.
Both are drawn by the stylesheet, not by markup, so they cannot be moved from
a design.

## What a design must not do with it

- **Do not restyle the picture's size.** Width and height are written inline
  by the element from its own measurement. A rule that sets them fights the
  four rules above and loses in a way that only shows on some pictures.
- **Do not put two on one page.** The markup carries ids, so two instances
  produce duplicate ids. The element still finds its own parts (it searches
  within itself), but the HTML is invalid and anything else querying those
  ids gets the first one. One panel per design.
- **Do not add a close affordance of your own.** It closes on Escape, on the
  backdrop, and on its own button. A fourth way is a fourth thing to keep
  consistent.
- **Do not expect a "no picture" state.** There is none: the panel is opened
  with a picture or it is not opened. An empty `src` leaves it closed.

## Colour and contrast

Everything comes from `tokens.css`. Two facts worth carrying into a design:

- The close button and the caption each sit on their own **scrim**, at .66 and
  .82 alpha. That is not decoration - a control in a single colour over
  arbitrary picture content is illegible over some frames, and the scrim is
  what was measured to fix it. A design that removes them has removed the fix.
- `--accent` on this panel means *this is the state* - the zoom chip, the
  brackets, the key hints. It is not an emphasis colour to spend elsewhere.

## The dark ground is the only ground

torpeek is drawn dark on every machine and follows no host preference: there
is no light theme and no `prefers-color-scheme` anywhere in its stylesheets.
A light mock of this panel is not a variant, it is a different product.
