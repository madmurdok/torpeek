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

## THE SYNTAX, MEASURED: a trailing comment, on the declaration's own line

The marker's placement was documented nowhere - `@kind` reached this repo as
the design agent relaying the checker - so `tokens.css` shipped it in two
placements at once, one per token, and one manifest read named the winner:

    --sans: "…", sans-serif;   /* @kind font */    -> kind FONT,  annotation "font"
    /* @kind font */
    --mono: "…", monospace;                        -> kind OTHER, NO annotation field

So: **trailing, on the same line as the declaration.** A comment on the line
above is not seen at all - and the manifest says so precisely, by omitting the
`annotation` field rather than by reporting a wrong kind. That field is the
thing to check in future: it tells you whether the marker was PARSED,
independently of whether the kind it asked for is the kind you got.

The second, independent observable moved with the first arm and not the second,
which is what makes the result a measurement rather than a coincidence:

    brandFonts  Torpeek Sans  status "unreferenced" tokens []  ->  "ok"  ["--sans"]
                Torpeek Mono  status "unreferenced" tokens []  ->  unchanged

Confirmed after normalising --mono to the trailing form and re-reading: it is
now `kind: "font"` with `annotation: "font"`, and BOTH faces report

    brandFonts  Torpeek Sans  status "ok"  tokens ["--sans"]
                Torpeek Mono  status "ok"  tokens ["--mono"]

with `--compare-aspect` still `other` throughout - the control never moved.

So the annotation does not merely relabel a token: it is what links a font FACE
to the token that names it. Both faces load and are fully indexed either way,
but an un-annotated family is reported as referenced by nothing.

`--compare-aspect` keeps its marker as a control worth leaving in: it comes back
`kind: "other"` WITH `annotation: "other"`, an unchanged kind beside a parsed
annotation, which is the evidence that annotating a token the checker already
classifies correctly does no harm.

One value-derived case for contrast: `--text: 14px` is `kind: "font"` with no
annotation at all. Annotate only what the checker gets wrong.


---

# What the checker calls a component: NAMED EXPORTS (TOR-209)

Established by changing one variable and re-reading the manifest, not inferred.
The three candidate rules all fitted the original evidence equally, because in
`frame-panel.js` the top-level declarations, the exports and the
capitalised-looking names were the SAME three names - so the first reading
could not tell them apart at all.

    before:  export { FramePanel, PAN_STEP, ARROWS };
             components: FramePanel, PAN_STEP, ARROWS      (three)

    after:   export { FramePanel };
             components: FramePanel                        (one)

`PAN_STEP` and `ARROWS` are still declared at module top level and still read
inside the element (`panBySteps`, the keydown handler). Only the export list
changed. So **"every top-level declaration" is falsified** and the rule is the
module's named exports.

Kind does not matter: `PAN_STEP` is a plain number and it indexed. Do not
expect the checker to keep classes and skip values.

## The consequence for the other five, and it does not fully go away

Every element module lists its surface in one trailing `export { ... }` block,
and `app.js` imports exactly ONE name from each of the five - `setServices`.
Nothing imports any of the classes, and `frame-panel.js` is taken with a bare
`import "./frame-panel.js"` for the side effect. So the current lists are:

    run-table.js       RunTable, LIVE_COLUMNS, MAX_PROGRESS_SEGMENTS, setServices
    run-detail.js      RunDetail, DETAIL, LIMIT_LEVER, LIMIT_NOTE, setServices
    file-list.js       FileList, LIST, WHY_NOT_VIDEO, framesLabel, setServices
    file-detail.js     (a multi-line list)
    compare-dialog.js  CompareDialog, setServices

Shipped as they are, that is around twenty entries for five components. Narrowed
to what each one actually needs - the class, so the component appears, plus
`setServices`, which `app.js` genuinely imports - it becomes TWO entries each.

**So there is an irreducible residue of one phantom entry per element:
`setServices` itself.** It cannot simply be dropped: the injected-services
setter is point 7 of the element pattern (docs/front-end.md) and app.js calls
it by name. Removing it from the export would break the page to tidy a
consumer's index, which is the wrong way round. Five phantom entries instead of
twenty is the realistic target; getting to zero would mean changing how
services are injected, which is a decision for TOR-208 to take deliberately or
to accept, not something to smuggle in as a cleanup.

## And a rule for adding a name to any element's export block

An extra name in that block is not free any more: it becomes a component a
design agent is offered and can do nothing with. Export what is imported, plus
the class. `frame-panel.js`'s own export line now carries this warning beside
it, where somebody about to add a name will read it.

---

# The other five elements (TOR-208)

All six components are now authored under `.design-sync/export/`. **Nothing has
been uploaded and the manifest has not been read** - see "What is still open"
at the end of this section, which is the first thing to read before continuing.

## WHAT EACH ELEMENT IS GIVEN, and the answer turned out to be structural

The ticket asks for a decision per element - example data, an empty shell, or a
fixed sample - and the honest answer is not five separate judgements. It falls
out of ONE fact about each element, which is checkable in its source rather
than argued about: **where its markup lives.**

    <run-table>       markup declarative in index.html, WRAPPED
    <compare-dialog>  markup declarative in index.html, WRAPPED
    <frame-panel>     markup declarative in index.html, WRAPPED
      -> the element never overwrites its own subtree, so children written in
         JSX SURVIVE. The .jsx renders the markup; a design may compose rows or
         frames into it; the card is the finished state statically.

    <run-detail>      build(): this.innerHTML = '<div class="run-detail">' + DETAIL + '</div>'
    <file-list>       build(): this.innerHTML = LIST, then renderFileList replaces the <ul>
    <file-detail>     build(): this.innerHTML = '<div class="file-detail" hidden>'
                      mount(): this.body.innerHTML = BODY
      -> anything passed as a child is DESTROYED on connect. The .jsx renders
         the element EMPTY and the card is the only sample there is.

**Example data is not available for any of the five.** Every one of them is
drawn from a run ENTRY, and an entry is assembled out of state.js's
newRunState, `<run-table>`'s own row parts (newRow's return value) and app.js's
detail half - three literals, one object, scanned by
TestNoRunEntryFieldIsDeclaredTwice. Nothing outside a running torpeek can mint
one, and a wrapper that faked a subset would be a second app.js free to drift
from the first. So the bridges translate no props into calls except the one
real door each has (`entry` for run-detail and file-list, `open`/`infohash` for
compare-dialog, a ref for run-table, nothing at all for file-detail, whose
mount() takes a bundle of live row elements).

**An empty shell shows nothing for four of the five.** run-detail, file-list
and file-detail render blank or hidden until something is bound; compare-dialog
is a `<dialog>` with no `open`, which is `display: none` in every UA
stylesheet - the lesson the panel's first card already paid for. `<run-table>`
is the one exception, and its empty shell is a real state of the page (a header
row and "Nothing yet - paste a magnet link to start") that teaches nothing
about the component, because the table IS the rows.

**Two elements cannot be drawn standalone at all**, and that is worth knowing
before designing with them:

  - `<run-detail>`'s ground and accent rail come from
    `.run-table .run-detail-cell`, whose selector REQUIRES the table's class.
    Its card carries a one-row `<table class="run-table">` for that reason.
  - `<file-detail>` has no title line of its own: since TOR-182 THE ROW is the
    file's title line, and four row elements are handed to it at mount. Its
    card carries the one `<li>` that owns it.

## THE FIVE UN-TICK VERDICTS CANNOT BE ON ONE LIST

Established from the three sets' own docs in state.js, not from taste:

    entry.fetching    "only ever non-empty while the row is running"          -> "stop"
    entry.deferred    ticked while the row was ALREADY fetching               -> "drop"
    entry.narrowable  "only ever non-empty while the row is queued or parked" -> "narrow"
    FINAL + frames on disk                                                    -> "clear"
    none of those, on a running row                                           -> ""

A row cannot be running and queued at once, so `stop`/`drop` and `narrow` are
on different rows by construction and `clear` is on a third. Select all is
parked-only for its own reason. So FileList.html is FOUR lists, one per row
state, each labelled - one list would have had to fake a state.

And four of the six row states look IDENTICAL on screen (a ticked, live
checkbox). The difference is the row's own `title`, which is TOR-184's
legibility requirement, so the card carries all of them verbatim.

## CARD-CRITICAL FACTS, each of which would have rendered a wrong card

Beyond "a card runs no scripts", which was already known:

1. **`table-layout: fixed` reads every column's width off its `<th>` alone**,
   and wireColumnResizers() is what writes `style="width: var(--col-w-KEY)"`
   on the nine sortable headers. A card without those inline widths gets nine
   equal columns and is not the component. The `.col-resizer` handles are
   appended by the same function and are also the card's to carry.
2. **THE COLSPAN IS TEN, NOT NINE.** Measured in a browser
   (`table.columns === 10`), and the first draft of both cards said nine. The
   header row holds the nine SORTABLE columns plus the unlabelled actions one,
   and `this.columns` is `querySelectorAll("thead th").length`. The project's
   own prose says "the nine-column table" and "the nine resizable headers",
   which is true of the sortable ones and is what misled the draft. A short
   colspan leaves an empty cell at the end of the detail row.
3. **The six live headers must be in the CARD and must NOT be in the `.jsx`.**
   The element builds them in connectedCallback; a `.jsx` shell that also
   carried them would give the page sixteen columns.
4. **`.avail-swarm` lives INSIDE `<figure class="reach">`**, so it is hidden
   whenever the reach strip is. A file mid-capture therefore shows no swarm
   chip at all - the chip is not an independently placeable part.
5. **A pending grid cell and the reach strip cannot be on screen together.**
   Pending cells exist only while `fentry.plan` is set, and loadFileDetail
   clears the plan at the same moment it puts the strip up. FileDetail.html
   shows the two shapes as two grids and says so, rather than drawing a state
   that cannot happen.
6. **`whenLabel()` is the viewer's locale.** The design test rendered
   `9 сент. 00:29` where the card writes `Sep 8 21:41`. A mock must not
   hard-code a date format.
7. **The embedded picture is ffmpeg's own test pattern**, downscaled from the
   data: URI FramePanel.html already carried (`sips`, in the scratchpad). So no
   new asset provenance was introduced, and the two cards that needed a picture
   reuse it: FileDetail at 176px, CompareDialog at 640px in two encodes (one
   heavily compressed, so the flip actually shows something).

## FramePanel.jsx carried invalid JSX, and it is now valid

`<img id="lightbox-img" alt="">` - an UNCLOSED void element - is a parse error
in every strict JSX parser, and `tabindex` is HTML's spelling rather than
JSX's. Both were corrected to `<img ... />` and `tabIndex={0}`.

This is not a claim that the platform's compiler rejected the old form: the
manifest's own component list proves that file compiled at least once. It is
that a parse error compiles the whole bundle to ZERO components silently -
which the HTML-comment conversion already cost this export one round trip - and
valid JSX is accepted by a lenient parser too. Not worth a second round trip to
learn which parser it is.

## The checker's phantom entries: from twenty-odd to five

TOR-209's rule ("export what is imported, plus the class") applied to the
remaining five modules. Every name was checked by grep across the whole
repository before removal, and the finding is that the Go tests which NAME
these constants all lift them out of the module's TEXT - `jsConst`, `jsFunc`,
`jsMethod`, `strings.Contains` - so an export is not what they depend on:

    run-table.js       - LIVE_COLUMNS, MAX_PROGRESS_SEGMENTS
    run-detail.js      - DETAIL, LIMIT_LEVER, LIMIT_NOTE
    file-list.js       - LIST, WHY_NOT_VIDEO, framesLabel
    file-detail.js     - BODY, MAX_BLOCKS, REACH_FLOOR, SHIFT_REASON,
                         FAILURE_REASON, cellTitle, detailFrame
    compare-dialog.js  - nothing to remove; already the class plus setServices

`app.js` imports exactly ONE name from each of the five - `setServices` - and
`frame-panel.js` is taken with a bare side-effect import. So the surface is now
eleven names for six components: six classes and five setters. **The five
setters are the irreducible residue** and each `.prompt.md` says so under a
heading of its own ("`setServices` is not a component"), so a design agent
offered one knows what it is instead of guessing.

## The stylesheet closure is now ALL EIGHT, and here is the third mismatch

`styles.css` imports every area file, in index.html's order. The ticket named
one mismatch (framepanel.css holds the reach strip and the swarm chip, which
`<file-detail>` builds). There are three, and the one it did not name is the
sharpest:

**intake.css is not the intake's.** It carries `select, button`,
`input[type="number"]`, `button:hover:not(:disabled)`, `button:disabled` and
`select:focus-visible` as BARE ELEMENT rules, so every control in every one of
the six elements is drawn by it: the run detail's Cancel/Top up/Retry, the file
list's Select all and Clear frames, a file's Regenerate/Compare and its
frame-count field, and both of the compare dialog's pickers. A closure that
skipped it "because no element is in the intake" would leave every button in
the export unstyled, and it has to come BEFORE the area files, which override
it per control.

The third is `detail.css`'s `.run-table .run-detail-cell`, a rule about a cell
`<run-table>` builds whose selector requires the table's class.

So "one stylesheet per component" is not available, it is said rather than
papered over (README.md, styles.css's own header, and each `.prompt.md`), and
the export ships the whole closure.

**A GAP WORTH KNOWING: nothing keeps `export/styles.css` in step with
index.html.** `TestTheConcatenationOrderIsThePagesOwn` holds the Go test
helper's list to the page's own `<link>` order; the export's `@import` list is
a third copy of that order with no test at all. Filed as TOR-210 (backlog, no
release) rather than fixed here.

## WHAT THE UPLOAD HAS TO DERIVE, and it is more than the panel needed

`frame-panel.js` imports nothing, so TOR-204 never met this: **each component
directory needs the element's module PLUS that module's transitive import
closure**, because the `.jsx` imports the element and the element imports its
siblings.

    RunTable/       run-table.js, state.js
    RunDetail/      run-detail.js, file-list.js, file-detail.js, state.js
    FileList/       file-list.js, file-detail.js, state.js
    FileDetail/     file-detail.js, state.js
    CompareDialog/  compare-dialog.js, state.js
    FramePanel/     frame-panel.js                     (imports nothing)

Plus, as before: `tokens.css`, `base.css`, `intake.css`, `table.css`,
`detail.css`, `filelist.css`, `framepanel.css`, `compare.css` and `fonts/` at
the export root. None of it is stored here - it is a copy of
`internal/web/assets/`, derived at sync time so there is no second version to
drift.

## HOW THE CARDS WERE LOOKED AT, and it is repeatable

Criterion 4 asks for a look at every card before upload, and the platform is
not needed for it. Build the upload's own layout in the scratchpad - the
export's `components/`, `styles.css` and `README.md`, plus the eight
stylesheets and `fonts/` copied from `internal/web/assets/` - serve it with
`python3 -m http.server`, and open each `.html`. The relative
`../../../styles.css` each card links resolves exactly as it will after upload.

All six were looked at: five new ones, and FramePanel again, to confirm the
closure growing from three imports to eight did not disturb the one card known
to work. It did not.

## CRITERION 6: a design built against the live elements, and it works

`.design-sync/` has no committed copy of this - it is a scratchpad page, and
the result is what matters. A page that imports the real modules, injects the
services with stubs, and then:

    creates <run-table> and appends it EMPTY, with no subtree at all
    -> asserts it did NOT wire, and that awaitParts() said so on the console
    streams the markup in ~60ms later, with NOTHING removed or re-inserted
    -> asserts the MutationObserver retried and the element wired itself

**TOR-205's fix is confirmed: a streaming consumer no longer needs the
remove-and-reinsert dance the design agent had to write against the panel.**
The element bails quietly, logs, waits, and wires when the subtree lands.

The rest of the page composes the whole tree and checks it DID something rather
than merely rendered: two real entries built the way app.js builds them,
syncRow drawing live figures and absences, a click on a row reaching the
injected toggleRun, `--run-detail-w` written from the pane, the run detail
building itself and creating its nested file list, the list building one row per
file with the "stop" verdict's own title on the fetched one, a file detail
mounted and drawing metadata, a heartbeat line, a 96-block reach strip stating
"each block is 13 pieces", the swarm chip judging `bad`, all four grid cell
states, a click on a thumbnail opening the frame panel as a modal, and the
compare dialog opening and explaining why there is nothing to flip against.

**42 checks, 0 failures.** And two controls, because a green suite proves
nothing until it is shown capable of going red:

    control A: the subtree never arrives   -> 6 of the 9 checks that ran fail,
                                              and the run aborts at newRow()
    control B: the QUEUED row is given a
               zeroed live reading         -> exactly ONE check flips, the
                                              absent-is-not-zero one

One incidental finding from running it: `requestAnimationFrame` never fires in
a background tab, so a check that awaits one hangs forever and reports nothing.
Use `setTimeout`. And the browser extension's `javascript_tool` evaluates in an
isolated world, so a page's `window.__x` is invisible to it - put the result in
a `data-` attribute instead.

## THE UPLOAD IS DONE, and the predictions this section made were WRONG

Written when the authoring agent had no DesignSync tool. It has since been
uploaded and the manifest read after three reopens. What it predicted, against
what the manifest says:

    predicted  eleven components: six classes plus five `setServices`
    measured   eleven components: six classes plus FIVE STATE.JS CONSTANTS

The count was right by coincidence and the content was wrong in both halves.
`setServices` is never indexed - it is lowercase, and case is the whole rule
(see the next section). The five that ARE there come from state.js.

Everything else it predicted holds: six cards with their groups and subtitles,
all eight stylesheets in `globalCssPaths` plus `styles.css`, forty tokens with
their kinds, nine font faces, and both brand fonts `status: "ok"`.

---

# The component rule, REFINED: uppercase-initial exports only (TOR-208)

TOR-209 established that the checker indexes a module's named exports as
components, on three names. Shipping five more elements gave a far stronger
test and narrowed the rule: it indexes an exported name only when its FIRST
LETTER IS UPPERCASE.

Measured on `state.js`, which travels with every element:

    54 exports in one block
     5 uppercase-initial: ABSENT, FINAL, PRIORITY_LOW, PRIORITY_NORMAL, PRIORITY_HIGH
    49 lowercase-initial: state, hasLive, peersCellText, setServices, ...

The manifest indexed **exactly those five** and none of the 49. Forty-nine
negatives against five positives, perfectly separated - which is what makes
this a measurement rather than the degenerate three-name case TOR-209 had,
where declarations, exports and capitalised names were the same three names.

**So the "irreducible residue of one `setServices` per element" TOR-209
predicted does not exist.** `setServices` is lowercase and is never indexed.
That prediction was wrong, and the correction is the useful part: an export's
CASE, not its role, is what decides.

## Why that produced five phantom components, and why they STAY

They were not fixable by narrowing an export list, the way `PAN_STEP` and
`ARROWS` were. All five are live: ABSENT is read in 9 places, FINAL in 8,
PRIORITY_LOW and PRIORITY_HIGH in 4 each, and PRIORITY_NORMAL five times
inside state.js itself. (Note the trap: "no uses outside state.js" does NOT
mean a dead export - it only means no other module imports it.)

The cause was the LAYOUT. A `.jsx` imports its element module, and every
element module imports `./state.js`, so state.js had to sit inside each
component folder - where it is scanned. It was uploaded five times, and
file-detail.js three times, for the same reason.

**A shared `modules/` folder at the project root was tried, and it did NOT
remove them.** Each `.jsx` now imports `../../../modules/<module>.js`, the
modules were deleted from every component folder, and after a reopen the five
were still there - just attributed to `modules/state.js`. So the checker does
NOT confine its scan to `components/`.

The evidence that suggested it would was WRONG, and worth naming so nobody
leans on it again: `templates/frame-opener/` carries `ds-base.js` and
`support.js` and neither is indexed - but that folder is a DECLARED TEMPLATE
in the manifest (`templates[].folder`), which is why it is skipped. It is not
evidence about paths outside `components/` in general.

**So the five stand, and criterion 5 records them rather than claiming a clean
checker.** They are not a false positive either: they are real uppercase-initial
exports and the checker is doing exactly what it does. They are simply not
components, and there is no way to stop them without deleting exports that nine,
eight and four call sites depend on.

## Keep the modules/ layout anyway - it fixed two other things

The move stays, because the phantoms were not its only effect:

  - **Attribution was wrong before it.** `FileList` and `FileDetail` were
    reported at `components/table/RunDetail/file-list.js` and
    `.../file-detail.js` - the copies inside ANOTHER component's folder, because
    the checker takes the first copy it meets. Now each of the six points at its
    own single module.
  - **One copy of each module instead of fourteen.** state.js shipped five
    times and file-detail.js three, and any copy could have drifted from the
    rest.

And it confirmed the compiler resolves a relative path out of a component
folder: all six components still compile and are present.

## The rule to apply when adding an element

Export the class. Everything else in that block must be lowercase-initial, or
it becomes a component. Shared modules can live anywhere - it will not save you
from their uppercase exports.
