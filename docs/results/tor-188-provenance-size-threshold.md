# TOR-188: at what file size does a capture point record where it went?

Measured on 2026-09-07 against `test/TOR-188-provenance-on-a-film-sized-file`
(torpeek 1.3.0 in progress, off `release-1.3.0` at `d64d5ee`), macOS 26.6.2,
Go 1.27.0, ffmpeg 8.1.2, on a locally generated clip served by a loopback
seeder — no network, no real torrent. Load average 6.74; the run itself was
`real 17.81 user 48.67 sys 5.19`, i.e. CPU-bound across cores rather than
waiting on anything.

## Verdict, in short

**The assertion TOR-179 rests on holds, and the threshold is three orders of
magnitude below any film.** Above roughly 3 MB every capture point records its
own distinct byte range; below roughly 0.7 MB they share the file's head and
cannot be told apart.

## Why the question existed

TOR-179 put where a frame came from on the frame, so the piece strip could be
drawn from the frames instead of from the last run's claim log. Its own test
fixture then measured something nobody had asked about: on a 669 KB clip,
several of four capture points recorded only `[[0, 32768)]` — the file's head —
and never showed where they actually went.

That is not an attribution bug. A range is claimed only where ffmpeg *issues a
request*; the bridge deliberately claims the **head** of each requested range
(TOR-88); and on a small file ffmpeg opens `bytes=0-` and reads forward, so
everything past the head arrives as reader readahead, which every claim figure
in this project excludes by design.

But TOR-179's whole purpose is the strip for a film. If a film-sized file also
collapsed to the head, TOR-179 would ship a mechanism that is correct, tested,
and still draws a strip lit only at the front — the exact complaint it was
written to fix. Nobody had measured it.

## The measurement

Four capture points (`Plan{Count: 4, Start: 0.1, End: 0.9}`), `min-traffic`,
readahead and window both set to one piece, on three fixtures. **Piece size
travels with file size deliberately**: a 400 MB torrent does not use 16 KiB
pieces, and the claim's granularity *is* the piece, so growing the file while
holding piece size fixed would have measured a fixture nobody ships.

| file | pieces | distinct range sets across 4 points | recorded the head alone |
| --- | --- | --- | --- |
| 669,316 B | 16 KiB | 3 | 2 of 4 |
| 3,194,475 B | 256 KiB | 4 | 0 |
| 6,432,689 B | 512 KiB | 4 | 0 |

So the threshold sits **between 0.67 MB and 3.2 MB**.

Sizes are what the encoder produced, not what was asked for: `-b:v` is a
request, and `testsrc` compresses so well that x264 landed at 3.2 MB and 6.4 MB
against targets of 12 MB and 60 MB. The first version of this table carried the
targets as labels, which made it disagree with its own numbers.

## What a located point actually records

Above the threshold, one point's set looks like this (the 6.4 MB fixture):

    point 1 -> [[0, 1048576], [2097152, 3145728], [6291456, 6432689]]

Three parts, all honest:

- **the head** — the container's own header, which every point's ffprobe
  re-reads;
- **its own seek** — the bytes around the timestamp this point went to, which
  is the part the strip needs;
- **the tail** — the cues/index at the end of the mkv, likewise re-read per
  point.

The head and the tail repeating across points is therefore correct rather than
noise. What distinguishes the points is the middle.

## Repeatability, and why the guard asserts a direction rather than a count

The sub-threshold arm was observed at **2 of 4** head-only in one run and
**3 of 4** in the next, on the same fixture settings — the encode is not
bit-identical between runs. The guard in
`internal/core/provenance_scale_test.go` therefore keeps that arm as its own
control and asserts only that it *still collapses*, never how many points do.
Asserting the number would have been flaky by the second run.

Keeping the control arm in the table is also what makes the guard above it
non-vacuous: every run demonstrates, in the same run, that the assertion can
fail. Confirmed anyway by pointing the guarded arm at the sub-threshold
geometry, which fails with "3 of the captured points recorded only the file's
head" and "2 distinct range sets across 4 capture points".

## Consequence

No follow-on is needed. TOR-179's report named a fallback — record what the
**bridge served** rather than what was claimed, reaching into `internal/bridge`
— in case the claim turned out too coarse. It is not required: the claim
already lands where each point went on anything a person would preview. The
option stays available if provenance is ever wanted to cover readahead-served
bytes as well, but nothing depends on it.

Not demonstrated, and still open: the owner's own Lupin set. There is no
results tree on this machine (`torpeek-out` exists nowhere under the home
directory), so the case that originally exposed the disagreement has not been
re-checked against the fix. This measurement answers the mechanism's question,
not that one.
