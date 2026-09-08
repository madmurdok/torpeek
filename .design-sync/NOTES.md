# Notes for /design-sync in this repo

Read this before running the skill again. Everything here was established by
checking, not assumed, and it is the state of things as of release 1.4.0.

## The automatic converter cannot run here, and that is not a gap to fix

`/design-sync`'s converter expects a JavaScript design-system repo: a
`package.json`, a lockfile, a compiled `dist/` of components, optionally a
Storybook. torpeek has **none** of them:

    package.json       absent
    package-lock.json  absent
    node_modules       absent
    .storybook         absent
    tsconfig.json      absent

That is a decision, recorded in `docs/front-end.md` under TOR-196: the front
end is custom elements and ES modules, served verbatim out of `//go:embed
assets`, and there is no JavaScript toolchain anywhere in the build. Adding one
to satisfy the converter would change the release story for four platforms at
once, which is precisely what the decision protects.

So `shape` in `config.json` is neither `storybook` nor `package`. It is
`off-script`, and the skill covers that case explicitly: the upload FORMAT is
the contract and the converter is only the deterministic path to it, so a repo
outside its envelope may produce the layout by whatever means it allows - with
the gates unmoved.

## THE TRAP: `dist/` exists and is not what the converter wants

    dist/darwin-amd64/torpeek      Mach-O 64-bit executable x86_64
    dist/darwin-arm64/torpeek      Mach-O 64-bit executable arm64
    dist/linux-amd64/torpeek       ELF 64-bit LSB executable, statically linked
    dist/windows-amd64/torpeek.exe PE32+ executable (console) x86-64
    dist/torpeek-1.2.0-*.tar.gz    release archives
    dist/torpeek-1.2.0-SHA256SUMS.txt

This is `make cross`'s output. It is gitignored (`.gitignore:3`) and has zero
tracked files. A converter that discovers a `dist/` here and tries to bundle it
finds platform binaries, not components. Do not point anything at it.

## Where the design system actually is

    internal/web/assets/tokens.css     the design system proper - palette, type
                                       scale, spacing rhythm. TOR-189 split it
                                       out and made it independently linkable
                                       for exactly this kind of consumer.
    internal/web/assets/*.css          seven area stylesheets, in a LOAD ORDER
                                       that is load-bearing: at equal
                                       specificity the later FILE wins, and
                                       four rule pairs depend on it. index.html
                                       is the source of truth for that order.
    internal/web/assets/*.js           nine ES modules; six of them register a
                                       custom element.

The six elements: `<run-table>`, `<run-detail>`, `<file-list>`,
`<file-detail>`, `<frame-panel>`, `<compare-dialog>`.

`frame-panel.js` and `state.js` are the import graph's two leaves. The panel
imports nothing at all, which is why it is the probe.

## The open question this repo exists to answer cheaply

Claude Design's agent builds designs from **React** code, and the skill's own
layout names `.jsx`. A web-components design system is contemplated in its
conventions guidance but is not confirmed to work. 1.4.0 was cut so that this
could be answered by one attempt against real components rather than by
rewriting 8300 lines on a guess - see the release's own description.

So: probe with one element first. If the shape is rejected, the fallback is
tokens plus static mockups, which needs none of the component machinery and
which `tokens.css` is already shaped for.

## Rules that survive whatever the answer is

- **Never add a JavaScript toolchain to this repository** to make an export
  easier. The export is a projection of the front end; it is not a change to
  it. If a bundle is needed, build it in the scratchpad or a throwaway
  worktree.
- The stylesheets' load order must be preserved in whatever `styles.css` the
  upload carries, and rendered designs receive only `styles.css`'s transitive
  `@import` closure - so every area file has to be reachable from it.
- The markup that goes with an element lives in `index.html`, wrapped, not in a
  template literal (TOR-192's pattern). An export that needs standalone markup
  has to lift it from there rather than invent it.

---

# What the probe answered (TOR-204)

**Yes.** A design agent can render and compose torpeek's web component from
this layout. Its own words, after building a working screen against it:
`<frame-panel>` plus the markup plus `panel.open()` composed fine, and mounting
a custom element is a path it already has. The `.jsx`-versus-web-component
question the batch was cut to answer is settled, and the other five elements
follow the same shape.

It took three round trips to learn three things about the format, none of which
should have to be learned again.

## 1. `_ds_bundle.js` is the platform's OUTPUT, not an input

Do not hand-write it. The app compiles it from the `.jsx` sources - the
generated header names them in `sourceHashes` - and it overwrites anything
uploaded under that name. The first attempt shipped a hand-built bundle
containing torpeek's own `frame-panel.js` verbatim; the platform replaced it
with a twelve-line stub carrying `components: []`, because the JSX had not
compiled.

So **the element's own module travels beside the `.jsx` and is imported by
it**, and that import is the whole mechanism:

    import "./frame-panel.js";

Importing for the side effect is enough - the file ends in
`customElements.define("frame-panel", FramePanel)`.

The namespace is generated too (`TorpeekUi_2928d4`), so writing to
`window.<SomeName>` is guesswork. Nothing needs to.

## 2. HTML comments are a syntax error in JSX

Markup lifted from `index.html` needs `class` → `className` **and** every
`<!-- … -->` turned into `{/* … */}`. Doing the first and not the second is
what compiled the first upload to zero components, and the failure is silent
from the uploader's side: the manifest simply says `components: []`.

## 3. A preview card does not run scripts

The card's HTML is rendered without JavaScript. The first card loaded the
bundle and called `panel.open()`, which worked in a plain browser and rendered
**nothing** here - a `<dialog>` without `open` is `display: none` in every UA
stylesheet, so all that showed was the body painted `#03070B`.

A card is a PICTURE of the component. It has to carry the finished state
statically: `open` on the dialog, the `src`, and the values `layout()` would
have written (`--lb-view-w/h` on the view, inline width/height on the image,
`data-zoom`, the readout's text).

## What the platform got right without help

Worth knowing so it is not re-solved: **forty tokens** were extracted with
their kinds and their defining file, including the four `.lightbox`-scoped
locals; **nine font faces** with their unicode ranges and files; all four
stylesheets recognised; and the card indexed from its `@dsCard` first line.

Two remaining checker complaints are TOR-206's, and one is a false positive -
the `.lightbox`-scoped variables are local layout vars, not a theme.

## Where the export's sources live, and what is derived

`.design-sync/export/` holds only what is **hand-authored** and would
otherwise be lost with a scratch directory:

    export/styles.css                         the @import entry, in index.html's order
    export/README.md                          the project's own README
    export/components/panels/FramePanel/*     .jsx, .d.ts, .prompt.md, .html

Everything else the upload needs is a **copy of `internal/web/assets/`** -
`frame-panel.js`, `tokens.css`, `base.css`, `framepanel.css`, `fonts/` - and is
deliberately NOT stored here. The upload derives them at sync time, so there is
no second version to drift. That is the same reason the bundle is not stored:
it is generated.

Keeping the bridge beside the element it bridges is the point. A change to
`frame-panel.js`'s API that the `.jsx` or the `.d.ts` does not follow shows up
as a diff in one commit rather than as a broken design weeks later.

One thing to know before editing the card: it embeds its sample frame as a
`data:` URI, which is why that one file is ~23 KB. A card must render without
scripts and without a network, so an embedded image is the only kind it can be
sure of.
