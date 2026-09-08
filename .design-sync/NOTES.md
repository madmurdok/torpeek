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
