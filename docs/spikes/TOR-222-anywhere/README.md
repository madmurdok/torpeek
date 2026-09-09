# TOR-222: the accordion used somewhere that is not torpeek

Criterion 1's evidence. The ticket's requirement is that the component be
"usable where we want", and that is only provable by an **unrelated** use:
inside torpeek's own table the wrapper would be indistinguishable from the
class TOR-212 shipped, because the table is what it was written against.

So `anywhere.html` is a plain FAQ about tomatoes. No run table, no file list,
no `<li>` with a checkbox in it, and **not one line of torpeek's CSS** - no
`tokens.css`, no `base.css`. Serif type on paper-white, which is about as far
from a dark monospace torrent table as a page gets, so nothing it reports can
be passing because it inherited something.

**This is not application code and cannot become application code.**
`internal/web/assets.go` embeds `assets` and nothing else, so nothing under
`docs/` reaches a built binary.

## It imports the SHIPPED module, and that is the one thing that matters

    import { Accordion } from "../../../internal/web/assets/accordion.js";

TOR-214's and TOR-221's spikes each freeze a **copy** of `tokens.css`, on
purpose, because they measure a layout against values that must not move under
them. This spike is the opposite case: the claim is about the module that
ships, so a copy would prove nothing about it, and drift is exactly what this
page is for. `TestTheAnywhereSpikeUsesTheShippedModule` fails if it ever
becomes a copy.

Which is why the server goes at the **repository root**, not in this directory:

    python3 -m http.server 8875 --bind 127.0.0.1
    # then open http://127.0.0.1:8875/docs/spikes/TOR-222-anywhere/anywhere.html

A `file://` open will not work - ES module imports are blocked by CORS there.

## Four arms, and the fourth is what makes the other three a measurement

    1  three declared disclosures     levels 1, 2 and 3, nested, in markup
    2  a box built in a FRAGMENT      constructed and applied OPEN while it had
                                      no parent, no document and no stylesheet
                                      in reach; inserted afterwards
    3  one box, two parents           opened, then relocated - the move the run
                                      table makes on every redraw
    4  five refusals (the CONTROL)    wirings the component MUST throw on

Arm 4 is the arm the ticket's verification discipline asks for. A page whose
every reading is "fine" proves nothing unless the same page can be shown
reporting otherwise, so it wires five disclosures wrongly and records what each
one is refused with:

    region somewhere else        the box does not hold the region
    box is the summary           the box is also its summary
    region inside the summary    a region inside its own toggle closes on
                                 every click within it
    toggle outside the summary   the rail would be painted on a header that
                                 does not hold the control
    a fourth level               there are three

Any of those accepted reads as `ACCEPTED - the check is not there` rather than
as silence.

## What `probe()` returns

`window.probe()` returns, and the page writes the same object into itself
(a spike whose result only exists in a console is one nobody re-runs):

    declared[1..3]    per level: the box's tag, its data-accordion-level and
                      data-accordion, whether the box carries the STATE (it
                      must not - the summary does), aria-expanded, the region's
                      hidden, the summary's data-expanded, whether the toggle
                      or the region carries a level (both must be null since
                      TOR-222 - the box carries it, once), the mark's computed
                      width, the summary's computed box-shadow, the region's
                      height, and the two structural facts: the region is the
                      box's child and not the summary's, and the box holds all
                      three parts
    ownState          this page's own open flags - the state the component
                      does not hold
    fields            the five own fields of a live instance: container,
                      level, region, summary, toggle. No state, and no `mark`
    fragment          what arm 2 read while the box was still detached,
                      isConnected included
    move              arm 3's before and after, with the two parents named
    refusals          arm 4

## What it does not cover

Chrome only, like both earlier spikes. It says nothing about torpeek's own
appearance - the per-level rails here are violet on paper and are this page's
own, deliberately; the shipped values are pinned to the served stylesheets by
`TestTheLevelsLooksAreTheStylesheetsOwn` and measured in the app in
`docs/front-end.md`. And it drives clicks rather than keys; the keyboard pass
is on the real page, where the tab order matters.
