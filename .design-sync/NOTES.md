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

---

# The checker's report is a FILE, and reading it settles arguments (TOR-206)

`_ds_manifest.json` in the project is the checker's own output, and
`DesignSync(get_file)` reads it without a permission prompt. Read it before
arguing with the checker from memory: it carries every token with the `kind` it
assigned and the file it came from, the fonts with their unicode ranges, the
cards, the templates, and the generated namespace. Two round trips of guesswork
in this batch would have been one read.

## What it said, and where the ticket's premise was wrong

**Two tokens were misclassified, not three.**

    --sans            kind: other     wrong, should be font
    --mono            kind: other     wrong, should be font
    --compare-aspect  kind: other     ALREADY CORRECT

`--compare-aspect` needed nothing. The kind the ticket asked for is the kind it
already had, and `other` is genuinely the checker's own value rather than a
category forced to fit - `--text: 14px` comes back `kind: font` from the same
run, so the vocabulary is real and `font` is in it.

**The consequence of the two wrong ones is visible in the same file**, which is
what makes them worth fixing rather than tidying:

    "brandFonts": [{"family":"Torpeek Sans","status":"unreferenced","tokens":[]},
                   {"family":"Torpeek Mono","status":"unreferenced","tokens":[]}]

Both families load, both are indexed with all nine faces - and neither is
linked to the token that names it, because a token classified `other` is not a
font token. So a design agent reading this manifest is told torpeek's two
faces are referenced by nothing.

## Do not confuse the skill's classifier with the checker's

`/design-sync`'s own `lib/emit.mjs` classifies tokens for the README it
generates, **by NAME regex**, into `color / spacing / typography / radius /
shadow / other`. It has no `font` family and no annotation support of any kind
(the skill's whole marker vocabulary is `@dsCard` and `@ds-*`). All three
tokens above land in `other` there and always will.

That is a different program from the one that writes `_ds_manifest.json`, which
classifies by VALUE and does have `font`. Reading the skill's source to predict
the checker's behaviour is therefore a wrong turn - it looks authoritative and
answers a different question.

## The `.lightbox` "theme scope" is a FALSE POSITIVE, and the manifest agrees

The relayed complaint was that the `.lightbox`-scoped properties register as a
theme scope. The manifest does not say that:

    "themes": []
    {"name":"--lb-pad","kind":"spacing","definedIn":"framepanel.css","scope":".lightbox"}

`scope` is a field ON the token, exactly right - these are the panel's local
layout variables. There are four and the manifest names them `--lb-pad`,
`--lb-bar`, `--lb-keys` and `--lb-gap`, confirming the ticket's own correction:
the `--lb-view-w/h` the agent reported are written by `layout()` at runtime and
appear in no stylesheet. Nothing to fix, and nothing to chase next sync.

## Found while reading, and NOT yet ticketed: two phantom components

    "components":[{"name":"FramePanel", ...},
                  {"name":"PAN_STEP",   ...},
                  {"name":"ARROWS",     ...}]

`PAN_STEP` and `ARROWS` are module-level constants in `frame-panel.js`, not
components. The checker takes top-level declarations in a component's source as
components, so shipping the remaining five elements will multiply this by
whatever constants each module happens to declare. Worth a ticket before
TOR-208 rather than after it.

## An upload does NOT refresh the manifest

Measured, and it is the thing to know before trying to verify anything through
it. `write_files` put the annotated `tokens.css` in the project - re-read, it
carries them verbatim - and the manifest came back BYTE-IDENTICAL, `--sans` and
`--mono` still `other`, `brandFonts` still unreferenced.

That is not the checker rejecting the annotation. The manifest is stamped
`"source":"design-sync-cli"` and is regenerated by the app's own self-check,
which the skill says runs the next time the project is OPENED - and this
manifest already lists the `templates/frame-opener` the design agent built
after the first upload, so it does get regenerated, just not by an upload from
here.

**So a null result read straight after an upload means "nothing was measured",
not "no effect".** Write the `_ds_needs_recompile` sentinel (it arms the
refresh), then have the project opened, then read the manifest.

## The pending measurement, deliberately left in place

Because the annotation SYNTAX is not documented anywhere readable - `@kind`
came from the design agent relaying the checker, and no skill source carries it
- `tokens.css` currently ships the marker in TWO placements on purpose, one per
token, so that one manifest read answers which one the checker reads:

    --sans: "…", sans-serif;   /* @kind font */      <- trailing, same line
    /* @kind font */
    --mono: "…", monospace;                          <- own line, above

Whichever comes back as `kind: font` names the placement; if neither does, the
marker itself is not read and the kinds have to be got some other way. Normalise
both to the winner once it is known - shipping two placements is a measurement,
not a style.
