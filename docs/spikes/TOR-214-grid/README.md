# TOR-214: the run table as a CSS grid (throwaway spike)

Evidence for the decision recorded in `docs/front-end.md`, kept because a
decision whose evidence has evaporated is a decision nobody can re-check.

**This is not application code and cannot become application code.**
`internal/web/assets.go` embeds `assets` and nothing else, so nothing under
`docs/` reaches a built binary; every Go test that reads a stylesheet reads it
through that embedded FS. `tokens.css` here is a verbatim copy of
`internal/web/assets/tokens.css` taken at 750693e, so the widths measured are
torpeek's own and not invented ones. It is a copy on purpose - the spike must
not be able to affect the app - and it will drift. Do not sync it; re-copy it
if the spike is ever run again.

Run it:

    cd docs/spikes/TOR-214-grid && python3 -m http.server 8873 --bind 127.0.0.1
    # then open http://127.0.0.1:8873/grid.html and call window.probe()

`window.probe()` returns every number quoted in the doc. Measured at a 1440x900
window, which gives the same 1168px pane the app has since TOR-168.
