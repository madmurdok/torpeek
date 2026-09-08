# torpeek ui

torpeek's front end, as a design system. One component so far, and on purpose:
this upload is a probe.

## What this is, and what it is not

torpeek's UI is **custom elements and ES modules with no build step**. Nothing
here is compiled, bundled, transpiled or minified - `_ds_bundle.js` is the
repository's own `frame-panel.js` with exactly one change, its trailing
`export` turned into an assignment onto `window.TorpeekUI`, because a bundle
is loaded as a classic script rather than imported. Every other line is the
shipped file, and that is checkable rather than claimed.

So the `.jsx` in each component directory is a **bridge, not an
implementation**. The behaviour lives in the element; the wrapper gives it a
React-shaped door, because a design is written in JSX and a web component is
not. There is no logic in a wrapper to get wrong.

## The one open question

Whether a design agent can render and compose a web component from this
layout. It is not confirmed - the layout's component file is `.jsx` and the
agent builds from React code. This upload is one element shaped by hand to
find out cheaply, rather than six shaped on a guess.

If the answer is yes, the other five follow the same shape:
`<run-table>`, `<run-detail>`, `<file-list>`, `<file-detail>`,
`<compare-dialog>`.

## The stylesheets' order is load-bearing

`styles.css` `@import`s three files in the order `index.html` links them, and
at equal specificity the later FILE wins. torpeek has four rule pairs that
depend on it. Reordering those imports is a design change, not a formatting
one.

## The ground is dark, and there is no other

torpeek follows no host preference: there is no light theme and no
`prefers-color-scheme` in any of its stylesheets. A light mock is not a
variant.
