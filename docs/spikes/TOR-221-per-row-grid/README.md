# TOR-221: one grid per row, instead of one grid per table (throwaway spike)

Evidence for the decision recorded in `docs/front-end.md` ("One grid per row,
so a row stops depending on its container"), kept for the reason TOR-214's own
spike is kept: a decision whose evidence has evaporated is a decision nobody
can re-check.

**This is not application code and cannot become application code.**
`internal/web/assets.go` embeds `assets` and nothing else, so nothing under
`docs/` reaches a built binary; every Go test that reads a stylesheet reads it
through that embedded FS.

`tokens.css` here is a copy of `internal/web/assets/tokens.css` taken at
`1d7ff4d` - before this ticket's own change to that file - so the widths
measured are torpeek's own and not invented ones. It is a copy on purpose (the
spike must not be able to affect the app) and it **will** drift. Do not sync
it; re-copy it if the spike is ever run again. The one thing the spike adds is
the `--run-tracks` declaration this ticket proposes, and it is in the page's
own `<style>` rather than in that copy - a `:root` declaration either way, and
it keeps the copy verbatim.

Run it:

    cd docs/spikes/TOR-221-per-row-grid && python3 -m http.server 8874 --bind 127.0.0.1
    # then open http://127.0.0.1:8874/per-row.html

## Four arms, and why the third one is the point

    A  shared grid + subgrid rows            TOR-215's shape, the baseline
    B  one grid per row, one track list      the proposal
    C  arm A with <run-accordion> inserted   MUST break
    D  arm B with the same element inserted  MUST hold

Arms A and B were expected to agree, and did - both 0.0000px. That agreement
is only worth something because of C: a probe that reported zero for every arm
would be measuring nothing, and C is the arm on which it is demonstrably able
to report otherwise (922.3906px). D is the decoupling itself - the element
TOR-212 wanted, wrapped around a row and its detail, changing nothing.

## What each function returns

- `window.probe()` - the whole picture: per arm, the max deviation of any
  cell's left edge from its own column header's (over every row and every
  column, 240 comparisons per arm), the number of distinct computed track
  lists, distinct row widths and distinct tenth-track widths, and whether the
  wrap or the page scrolls.
- `window.probeNarrow()` - a column narrower than its content: the string's
  natural width against the rendered cell, per arm.
- `window.probeDetail([rows])` - opens details and measures each against its
  row and against the summed tracks.
- `window.closeDetails()` - puts them back.

`wireColumnResizers` is pasted in from `run-table.js` with only its `this.`
receivers rebound, so a drag here is the app's drag; it writes `--col-w-KEY`
on `:root`, which means one drag re-lays out all four arms at once and
alignment can be read mid-drag.

Measured at a 1440x900 window, which gives the same 1168px pane the app has
since TOR-168. Every figure quoted in the doc comes from this page, except the
ones the doc explicitly attributes to the running application.
