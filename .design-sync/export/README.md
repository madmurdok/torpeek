# torpeek ui

torpeek's front end, as a design system. One component ships today and five
follow it, in the shape this one established.

## Components

- FramePanel - full-size frame viewer (`<frame-panel>`)

Five more follow the same shape: `<run-table>`, `<run-detail>`, `<file-list>`,
`<file-detail>`, `<compare-dialog>`.

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

That is why the other five follow rather than wait.

## The stylesheets' order is load-bearing

`styles.css` `@import`s three files in the order `index.html` links them, and
at equal specificity the later FILE wins. torpeek has four rule pairs that
depend on it. Reordering those imports is a design change, not a formatting
one.

## The ground is dark, and there is no other

torpeek follows no host preference: there is no light theme and no
`prefers-color-scheme` in any of its stylesheets. A light mock is not a
variant.
