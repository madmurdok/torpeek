# RunTable

Every torrent this page knows about, live or read off disk, as nine columns
that sort, resize, and open.

```jsx
<RunTable empty={false}>
  {/* Copy the rows from RunTable.html. They are what syncRow() writes. */}
</RunTable>
```

## What a design consumer is given, and why

**A FIXED SAMPLE OF ROWS, in the card - not an empty shell.**

The table *is* the rows, and the rows do not exist in `index.html`: `newRow()`
builds both `<tr>`s per torrent and `syncRow()` fills ten cells from state.js
derivations. An empty shell would be a header row and the sentence "Nothing yet
- paste a magnet link to start", which is a real state of the page and teaches
nothing about the component.

Example *data* was the other candidate and is not available: an entry is
assembled out of `newRunState`, this element's own row parts and app.js's
detail half, and those three are one object by design. Nothing outside a
running torpeek can mint one. So the sample is markup, and it is the markup the
element itself writes.

**The five rows are chosen to look different from each other**, because this
project's most-repeated rule is only visible in a mix:

| row | badge | what it shows |
|---|---|---|
| running | `running` | a real reading in every live cell, plus the segmented progress bar |
| queued | `queued` | a PROVISIONAL name from the magnet's `dn=`, every live figure absent, `#2` in the queue, ▲▼ |
| finished | `partial` | done-plus-partial, and every live figure absent because the run is over (TOR-202) |
| failed | `failed` | the error as the row's own second line, no Cancel |
| on disk | `on disk` | no arrival number either, and `6/6` for its complete/selected ratio |

## ABSENT IS NOT ZERO, and it is the rule to carry into any mock

Four of the five rows show `—` in the live columns, each for a different
reason, and each cell says which one in its own `title`:

- `no reading yet - this torrent has no client (queued, needs-action, or not yet started)`
- `no reading - this run has finished, and no client is connected any more`
- `no reading - this row was read off disk, never a live client`

A queued row drawn as `0 peers · 0 B/s` is indistinguishable from a running
torrent that found nobody, and those are opposite situations. **Never draw a
zero for a reading that is missing.** The em dash is this page's mark for it,
everywhere, and `data-absent="true"` on the cell is what dims it.

## The Added column is the viewer's locale, not English

`whenLabel()` is `toLocaleDateString({month:'short',day:'numeric'})` plus
`toLocaleTimeString({hour:'2-digit',minute:'2-digit'})`, so the same row reads
`Sep 8 21:41` on one machine and `9 сент. 21:41` on another - measured in a
browser, not assumed. The card writes the English form because a card has to
write something; **a mock must not hard-code a date format**, and the column's
width has to survive a longer one.

## Two columns are two facts each, and both matter

- **Avail** is copies per piece across the swarm, *not a percentage* - it
  commonly exceeds 1.0. That is why its header carries a second, muted line
  reading `copies/piece`. Under the figure, `N missing` counts the pieces no
  connected peer holds.
- **Queue** is the ARRIVAL ORDINAL as its figure ("the third torrent you
  added", true for the life of the row) and the QUEUE POSITION as a muted
  second line, marked `#2`, present only while the row is waiting. Do not
  merge them and do not draw the position as a bare number: a truncated `#12`
  would read as `#1`, which also means "starts next".

## What the stylesheets give it

- **table.css** - the table, the sticky header, the nine columns, the badges,
  the drag handles and the segmented bar.
- **detail.css** - `.run-table .run-detail-cell`, the ground and accent rail
  of the cell the detail mounts in. The selector *requires* `.run-table`, so
  this rule is about a cell this element builds even though it lives in the
  detail's file. **This is one of the three places the area split does not
  match the element split**; see the export's `styles.css` for the other two.
- **intake.css** - the bare `select, button`, `button:hover:not(:disabled)`
  and `button:disabled` rules, which the two queue arrows and the cancel cross
  inherit. Named for the intake section; reached by every control here.
- **tokens.css** - and specifically `--col-w-name`, `--col-w-when`,
  `--col-w-status`, `--col-w-peers`, `--col-w-seeds`, `--col-w-download_bps`,
  `--col-w-upload_bps`, `--col-w-availability`, `--col-w-priority`: the nine
  default column widths, which `table-layout: fixed` reads off each `<th>`'s
  own inline `width: var(--col-w-KEY)`.

A rendered design receives only `styles.css`'s transitive `@import` closure,
and all four are in it.

## Rules the table obeys, and a design cannot opt out of

1. **Ten `<th>`, and only four of them are in the markup.** Nine sortable and
   resizable columns plus the unlabelled actions one; the six live columns are
   built by the element from `LIVE_COLUMNS`, before the actions header. A
   design that writes them into a live `<run-table>` gets sixteen. **Ten is
   also the detail row's `colSpan`** - the element reads it off
   `querySelectorAll("thead th").length` rather than writing a literal,
   because a short colspan leaves an empty cell at the end of the detail row
   and narrows the detail by a column. Measured in a browser, not inferred:
   the card's first draft said nine.
2. **Several rows may be open at once.** Closing one to open another would
   destroy work in progress on a live run; a file's own accordion one level in
   makes the opposite choice, deliberately.
3. **A detail is a second `<tr>`, never something inside the first.** A click
   on a checkbox or a thumbnail inside a detail must not bubble to the row's
   own handler and collapse the thing being used.
4. **The progress bar is not on a finished, failed or disk row.** A full bar on
   a done run tells nobody anything, and an empty one on a failed run reads
   like a second failure.
5. **A provisional name must never read the same as a confirmed one.** `dn=`
   is attacker-controlled text.

## What a design must not do with it

- **Do not put two live `<run-table>`s on one page.** The markup carries ids.
- **Do not restyle the `<th>` widths away.** They are the column widths, and
  `table-layout: fixed` has no other source for them.
- **Do not use a semantic colour for "which row is open".** The accent means
  *this is live, or this is where you are*; an open row wears it, and nothing
  has gone wrong.

## `setServices` is not a component

The checker lists it, because it is one of this module's two named exports and
the checker indexes named exports as components. It is the function app.js's
bootstrap calls to inject `toggleRun`, `cancelRun`, `setPriority` and
`detailShown` - four POSTs against a torpeek server. There is nothing to
render.

## The dark ground is the only ground

torpeek follows no host preference: there is no light theme and no
`prefers-color-scheme` in any of its stylesheets. A light mock of this table is
not a variant.
