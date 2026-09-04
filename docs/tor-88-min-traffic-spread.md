# TOR-88: what criterion 2's traffic figure actually measures

Measured on 2026-09-04 against `fix/TOR-88-traffic-creep` (torpeek 0.9.0, off
`release-0.9.0`), macOS 26.6.2, Go 1.27.0, ffmpeg 8.1.2, 8 CPUs, on the same
live torrent and the same file every release has used
(`archive.org/download/Sintel/Sintel_archive.torrent`, `sintel-2048-stereo.mp4`).
Forty runs of criterion 2 on the shipped build, plus twelve on 0.7.0's torrent
library, plus twenty-one on the local seeder. Every rep is listed with the
machine state it was taken on; a mean on its own is the one thing this ticket
did not need.

## Verdict, in short

1. **The spread on the current build is 45.5–54.6 MiB, median 46.9, mean 48.8,
   sd 3.35** (n = 18, in the swarm's normal state). Six of the eight
   historical figures fall inside it, including 0.8.0's 52.9. **There is no
   three-release climb to explain.**
2. **It is not the torrent library.** Two interleaved A/Bs — on a local seeder
   and on the live torrent — show no shift, on arms first shown to differ by a
   wire version string, an API symbol, and the stall TOR-47 fixed.
3. **It is not anything else of ours either**, because nothing changed: every
   file in the read path is byte-identical from `v0.7.0` to HEAD.
4. **But the figure has a second, transient regime in which criterion 2 simply
   fails.** For about forty minutes this afternoon, on an unchanged binary,
   the same run cost **60.7–118.6 MiB** and missed the 60 MiB ceiling **21
   times in 22 runs**. It began after ~25 back-to-back runs, survived a
   20-minute pause, and was gone after a longer idle. The cause is outside
   this repository; the exposure is not.
5. **The intended cost is a deterministic 44 MiB.** Everything above it is
   prefetch nobody claimed and nobody read, and it ranged from 1.5 to 74.5 MiB
   on the same code. A 60 MiB ceiling over a 44 MiB intent is 16 MiB of room
   for a quantity measured at 74.5 MiB.

So: **within the historical range, and the historical range was never the
thing to watch.** The ticket was right that criterion 2 breaks first and right
to file before it broke. The number to watch is the gap between the 44 MiB a
run claims and the ceiling, not the release-to-release figure.

## The 0.9.0 release run, against this document's own prediction

Written after the fact, because a spread is only worth measuring if the next
point is checked against it. `make acceptance` for 0.9.0 ran on 2026-09-04 at
19:20, ~2h after the reps below, on a quiet machine (load 4.02):

| | criterion 2 | criterion 1 |
| --- | --- | --- |
| predicted here (n = 18, normal regime) | 45.5-54.6 MiB, median 46.9 | - |
| **0.9.0, measured** | **46.0 MiB in 33.5s** | 101.6 MiB in 61.1s |
| 0.8.0 | 52.9 | 104.3 |
| 0.7.0 | 47.5 | 103.7 |
| 0.6.0 | 45.9 | 102.1 |

46.0 lands just under the predicted median and below all three figures the
ticket read as a climb, which is what an oscillating series does. Criterion 1
sits at 101.6, and read out of the reports rather than from memory it has been
102.1, 102.1, 102.1, 102.1, 103.7, 104.3, 101.6 from 0.3.0 to 0.9.0 - a 2.7
MiB band over seven releases, against a 150 MiB ceiling. (0.2.0's 172.0 was
the one miss, before seeking worked.) Whatever moves criterion 2 does not
move criterion 1, which is the strongest single reason not to look for the
cause in our own read path.

What this run does NOT establish, and the reason it is recorded here rather
than only in the report: one passing run is a sample from the normal regime,
not evidence the elevated one is gone. The elevated regime produced 60.7-118.6
MiB on this same code earlier the same day. A release whose acceptance run
happens to land in it will fail criterion 2 with nothing wrong, and the fix
for that is the ceiling question, not a re-run.

## 1. The history, read out of the reports rather than the ticket

The ticket names three figures. There are eight, and they do not climb.

| release | criterion 2 | elapsed | criterion 1 (min-time) |
| --- | --- | --- | --- |
| 0.1.0 | 44.8 MiB (per-file, run B) | — | 80.0 / 67.9 MiB where seeking worked |
| 0.2.0 | 45.4 MiB | 42.1s | 172.0 MiB (**missed**) |
| 0.3.0 | 46.8 MiB | 39.1s | 102.1 MiB |
| 0.4.0 | 47.5 MiB | 41.5s | 102.1 MiB |
| 0.5.0 | 45.8 MiB | 28.1s | 102.1 MiB |
| 0.6.0 | 45.9 MiB | 25.5s | 102.1 MiB |
| 0.7.0 | 47.5 MiB | 43.1s | 103.7 MiB |
| 0.8.0 | 52.9 MiB | 28.3s | 104.3 MiB |

0.4.0 already stood at 47.5 and 0.5.0 came back down to 45.8, so the series is
45.4, 46.8, 47.5, 45.8, 45.9, 47.5, 52.9 — six values inside a 2.1 MiB band and
then one at 52.9. Reading the last three as a three-release climb is picking a
monotone subsequence out of a series that oscillates.

Criterion 1 is the useful column. It is the *same* code path with a wider
window, measured in the same runs against the same swarm, and it sat at 102.1
MiB four releases running to the tenth of a MiB. Whatever moves criterion 2
does not move criterion 1.

## 2. How this was measured

`make acceptance` runs all seven criteria, so reaching criterion 2 costs a
min-time run (~104 MiB) first. Criterion 2 is now its own subtest:

    go test -tags acceptance -run 'TestCriteria1And2MinTimeAndMinTraffic/criterion2' ./acceptance/

That is the only change to shipped code on this branch. The two criteria
already took a fresh output and data directory each, so the subtest wrapper
carries no state and measures exactly what the loop measured before; a plain
`make acceptance` still runs both, in order. Each rep also logs one
machine-readable line so a spread can be read out of `go test -v` instead of
diffing report files.

Reps ran from precompiled test binaries against a `.torrent` fetched once
(`sha256 7027600f…d72c54`) and passed with `-torrent`, so the metainfo was
identical across every rep, with `-report` pointed at a scratch directory so
no rep overwrote `docs/results/`. Every rep produced 20/20 frames, 0 shifted
points and `reason=completed`, so all forty are complete, comparable frame
sets — none of the variation below is a partial run.

## 3. The spread: forty runs of the shipped build

`real` runs 4–7× `user` in every rep, so these processes were waiting on the
network throughout, as a download should. The one-minute load average is the
one that matters here and it stayed between 2.7 and 13.9; where the 5- and
15-minute figures are high it is because `make check` had just run, and those
reps are not the extreme ones.

### 3a. Normal regime, 14:05–14:33 (n = 14)

| # | when | MiB | MiB/frame | elapsed | `real`/`user`/`sys` | load 1-min before → after |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 14:05 | 45.6 | 2.28 | 54.0s | 54.55 / 7.33 / 4.18 | 13.36 → 8.03 |
| 2 | 14:06 | 51.5 | 2.57 | 46.3s | 46.38 / 6.52 / 3.67 | 6.84 → 4.66 |
| 3 | 14:07 | 46.8 | 2.34 | 42.1s | 42.41 / 7.21 / 4.43 | 4.66 → 4.61 |
| 4 | 14:08 | 46.7 | 2.34 | 32.1s | 32.20 / 7.14 / 4.31 | 4.61 → 3.66 |
| 5 | 14:09 | 46.5 | 2.32 | 30.6s | 30.67 / 7.42 / 4.51 | 3.66 → 3.43 |
| 6 | 14:09 | 48.2 | 2.41 | 44.9s | 44.99 / 8.32 / 5.05 | 3.19 → 7.71 |
| 7 | 14:10 | 54.1 | 2.70 | 49.2s | 49.83 / 7.34 / 4.40 | 7.71 → 6.73 |
| 8 | 14:11 | 52.5 | 2.62 | 41.3s | 41.37 / 7.01 / 4.27 | 6.73 → 6.52 |
| 9 | 14:12 | 52.2 | 2.61 | 37.2s | 37.25 / 7.16 / 4.25 | 6.52 → 4.47 |
| 10 | 14:12 | 46.1 | 2.31 | 39.2s | 39.23 / 6.67 / 3.70 | 4.47 → 3.78 |
| 11 | 14:29 | 54.6 | 2.73 | 41.7s | 41.74 / 7.16 / 3.79 | 5.04 → 18.33 |
| 12 | 14:31 | 46.1 | 2.30 | 28.6s | 28.72 / 6.87 / 3.87 | 10.85 → 8.58 |
| 13 | 14:32 | 50.0 | 2.50 | 32.5s | 32.55 / 7.20 / 4.22 | 5.85 → 6.83 |
| 14 | 14:33 | 45.9 | 2.30 | 28.9s | 29.00 / 6.76 / 3.52 | 6.23 → 9.01 |

Reps 11–14 are the `arm-master` arm of the library A/B in section 5; that arm
is `git archive HEAD` with the same subtest patch and an untouched `go.mod`,
so it is the same build and the batches pool.

Ten reps alone: min 45.6, max 54.1, **median 47.5**, mean 49.0, sd 3.18.
Fourteen: 45.6–54.6, median 47.5, mean 49.1, sd 3.30, **none over the
ceiling**. Traffic and elapsed are essentially uncorrelated (Pearson r = 0.22
over reps 1–10): rep 1 was the slowest *and* the cheapest, rep 5 among the
fastest and also cheap. That is the first thing that argues against the
ticket's "faster, greedier peer set" reading.

### 3b. Elevated regime, 14:34–15:13 (n = 22)

Same binaries, same torrent, same file, same afternoon:

| when | MiB | build | note |
| --- | --- | --- | --- |
| 14:34 | 90.6 | untraced | **missed** |
| 14:35 | 60.7 | untraced | **missed** |
| 14:38 | 118.6 | traced | **missed** |
| 14:39 | 100.9 | traced | **missed** |
| 14:39 | 89.9 | traced | **missed** |
| 14:40 | 93.0 | traced | **missed** |
| 14:40 | 102.5 | traced | **missed** |
| 14:41 | 77.1 | traced | **missed** |
| 14:42 | 89.8 | traced | **missed** |
| 14:42 | 60.8 | traced | **missed** |
| 14:43 | 100.4 | **untraced** | **missed** |
| 14:44 | 89.6 | traced | **missed** |
| 14:45 | 89.8 | **untraced** | **missed** |
| 14:46 | 89.8 | traced | **missed** |
| 14:46 | 62.2 | **untraced** | **missed** |
| 14:47 | 101.8 | traced | **missed** |
| 15:09 | 80.7 | untraced, after a 20-min pause | **missed** |
| 15:10 | 90.7 | untraced | **missed** |
| 15:11 | 60.9 | untraced | **missed** |
| 15:11 | 96.7 | untraced | **missed** |
| 15:12 | 52.2 | untraced | met |
| 15:13 | 95.8 | traced | **missed** |

n = 22, min 52.2, max 118.6, median 89.9, mean 86.1, sd 17.1, **21 of 22 over
the ceiling**. Per frame, 2.61–5.93 MiB — the worst run is above TOR-44's 4.5
MiB/frame ceiling for min-traffic, which the acceptance run does not enforce.

Two things this regime is **not**:

- **Not the diagnostic patch.** The obvious suspicion is the traced build of
  section 7. The untraced binary, interleaved with the traced one in the same
  minutes, read 100.4, 89.8 and 62.2 MiB — and the same untraced binary read
  45.9 MiB at 14:33.
- **Not the machine.** One-minute load was 3.0–8.7 through that window, lower
  than during several of the cheap reps, and `real` still ran 5–6× `user`, so
  those runs were waiting on the network exactly as the cheap ones were.

### 3c. Normal regime again, 16:48–16:52 (n = 4)

After the machine sat idle for about ninety-five minutes (this is the control
arm of the fix A/B in section 8):

| when | MiB | elapsed | `real`/`user`/`sys` | load 1-min before |
| --- | --- | --- | --- | --- |
| 16:48 | 45.5 | 46.6s | 47.19 / 7.18 / 4.33 | 25.88 |
| 16:49 | 53.9 | 42.8s | 43.12 / 7.71 / 5.38 | 18.81 |
| 16:51 | 47.0 | 41.1s | 41.14 / 7.45 / 4.57 | 6.08 |
| 16:52 | 45.8 | 46.0s | 46.31 / 7.64 / 5.17 | 5.16 |

Back to 45.5–53.9, none over the ceiling — at *higher* machine load than the
elevated regime ran at, which is the cleanest available evidence that the
machine was never the variable. So the elevated regime is transient: a
20-minute pause did not clear it (15:09–15:12 above), a much longer idle did.

### 3d. Pooled normal regime (n = 18)

**45.5–54.6 MiB, median 46.9, mean 48.8, sd 3.35; 2.27–2.73 MiB/frame against
TOR-44's 4.5 ceiling.** 0.8.0's 52.9 MiB sits inside it, as do 0.3.0 through
0.7.0. Only 0.1.0's 44.8 and 0.2.0's 45.4 fall below the floor, by 0.1 and 0.7
MiB — a fifth of one piece, not worth chasing and not a trend either.

### 3e. The min-time control, taken the same hour

| # | when | MiB | elapsed | `real`/`user`/`sys` | load 1-min before → after |
| --- | --- | --- | --- | --- | --- |
| 1 | 14:14 | 102.1 | 41.3s | 41.58 / 7.81 / 5.02 | 3.73 → 2.93 |
| 2 | 14:15 | 102.8 | 41.8s | 41.88 / 7.83 / 4.96 | 2.93 → 4.18 |
| 3 | 14:16 | 102.1 | 37.7s | 37.81 / 7.28 / 4.42 | 4.18 → 4.23 |

Same machine, same torrent, same swarm, same hour, same library, same
harness: min-time varies by **0.7%** where min-traffic varies by 17% in its
normal regime and by a factor of 2.6 across both. Whatever is loose is loose in
min-traffic specifically, not in the swarm's throughput and not in the
measurement. **Caveat, and it is the biggest gap in this work: min-time was
not measured while the elevated regime was active**, so whether that regime
doubles min-time too is unknown. Section 10 says how to find out.

## 4. The same profiles on a local seeder, with no swarm at all

`internal/core/profiles_test.go` (TOR-44) measures both profiles against a
seeder in-process. Six reps on this branch, at load average 3.3–4.0:

| rep | min-time | min-traffic |
| --- | --- | --- |
| 1 | 5.94 MiB/frame | 3.45 MiB/frame |
| 2 | 5.94 | 3.52 |
| 3 | 5.94 | 3.31 |
| 4 | 5.94 | 3.32 |
| 5 | 5.94 | 3.61 |
| 6 | 5.94 | 3.45 |

min-traffic lands at 3.31–3.61 MiB/frame against the 3.44–3.61 TOR-44 wrote
down when it chose the 4.5 ceiling: the thrifty profile costs today exactly
what it cost when the ceiling was set. min-time is pinned at 5.94 across all
six, a shade above TOR-44's recorded 5.76 and well under its 7.0 ceiling.

Two things follow. **No per-frame ceiling is under pressure from anything in
the code** — the harness that guards them measures what it always did. And
min-traffic is the noisier profile even with one local seeder and no swarm
(3.31–3.61 while min-time is bit-stable at 5.94), so its variance is a
property of how it reads. It is also load-sensitive in a way min-time is not:
the same three reps at load 30–52 read 3.78 / 3.63 / 3.48 while min-time stayed
at 5.94.

## 5. The library A/B, and how the arms were shown to differ first

TOR-47 moved `anacrolix/torrent` from `v1.61.0` to a pseudo-version off
master. Two arms were built as plain `git archive HEAD` trees in a scratch
directory — never by editing the branch this work commits from — differing
only in the module pins:

    diff -rq arm-master arm-v1610   ->   go.mod and go.sum only

`arm-v1610`'s resolved anacrolix versions (`dht/v2 v2.23.0`,
`missinggo/v2 v2.10.0`, `chansync v0.7.0`, `generics v0.1.1-…`,
`go-libutp v1.3.2`, `roaring v1.2.3`) are exactly the set v0.7.0 shipped, so
this arm is HEAD's code on 0.7.0's library stack.

**The arms were shown to be different before anything was measured with
them**, three ways, weakest first:

1. Each arm announces a different library version *on the wire*: the
   extended-handshake `v` string is built from build info at run time, and
   reads `"… (anacrolix/torrent v1.61.1-0.20260831123324-4ad31c517078)"`
   against `"… (anacrolix/torrent v1.61.0)"`; likewise the HTTP user-agent.
2. The API differs: a one-line program referencing
   `version.AnonymousHttpUserAgent` builds and runs on `arm-master` and fails
   to compile on `arm-v1610` (`undefined`). 219 files differ between the two
   module versions — `requesting.go`, `internal/request-strategy/*`,
   `peer.go`, `peerconn.go`, `piece.go`, `reader.go` among them. This is a
   master snapshot, not one backported fix.
3. Best of the three, because it is behaviour: **the old library still stalls
   and the new one does not.** In six local reps `arm-v1610` stalled twice —
   once losing a min-time capture point to a 60s bridge timeout, once losing a
   whole min-traffic run (0 frames, `inspect` timed out after 60.0s) — where
   `arm-master` stalled 0 times in 12. That is TOR-47's documented defect,
   reproduced, and it is what makes a null result on traffic mean something.

### Local A/B (no swarm), 6 reps per arm, interleaved

| rep | arm-master min-traffic | arm-v1610 min-traffic |
| --- | --- | --- |
| 1 | 3.61 MiB/frame | 2.92 (the rep whose min-time stalled) |
| 2 | 3.47 | — (stalled, 0 frames) |
| 3 | 3.45 | 3.45 |
| 4 | 3.46 | 3.45 |
| 5 | 3.46 | 3.45 |
| 6 | 3.46 | 3.45 |

On the four reps where the old library completed cleanly it costs 3.45
MiB/frame; the new one costs 3.45–3.61. **The bump did not make min-traffic
dearer.**

### Live A/B on the acceptance torrent, 6 reps per arm, interleaved

| rep | arm-master (new) | arm-v1610 (0.7.0's library) |
| --- | --- | --- |
| 1 | 54.6 MiB | **77.5 — missed** |
| 2 | 46.1 | 46.7 |
| 3 | 50.0 | 46.8 |
| 4 | 45.9 | 46.8 |
| 5 | **90.6 — missed** | 45.9 |
| 6 | **60.7 — missed** | 47.5 |

Both arms produce runs in the mid-40s and both produce runs far over the
ceiling; the old library's worst (77.5 MiB) is worse than anything the new one
produced before rep 5. Six reps per arm resolves a 5 MiB shift at 80% power
given sd = 3.35, which is the size of the effect the ticket suspected
(52.9 − 47.5 = 5.4). No such shift is there.

That the library cannot be the cause is also settled without measuring
anything, by reading the diff: **every file in the read path is byte-identical
between `v0.7.0` and 0.9.0 HEAD** — `internal/bridge`, `internal/probe`,
`internal/frames`, `internal/swarm/fetch.go` (profiles, windows, readahead,
`Claim`/`Release`), `internal/core/budget.go` (which is what the figure is
measured with), and the whole `acceptance` package. The only swarm-facing
changes since 0.7.0 are TOR-59's storage close and a `Magnet()` accessor, and
the only `internal/ffmpeg` change renames a helper so the Windows case can be
tested. **Nothing in torpeek that could move this number has moved.**

## 6. Where the bytes go, per HTTP range request

A diagnostic patch in the scratch arm logs, for every range request ffmpeg
makes through the bridge, the range asked for, the pieces claimed, the bytes
the body delivered and the run's cumulative `BytesReadUsefulData`. Eight
traced live reps, and the deterministic parts are strikingly deterministic:

- **163 range requests**, every rep, to the byte.
- **44 distinct pieces claimed**, every rep — pieces 917..1187 of a 269.6 MiB
  file with 1 MiB pieces. **44 MiB is what the run intends to cost.**
- 326 MiB of piece-claims are issued across those 163 requests (2 pieces
  each), covering only those same 44 distinct pieces: one piece is claimed and
  released **82 times** in a single run. Every re-read after the first is free.
- ffmpeg asks for ranges that run to the end of the file (up to 269.6 MiB) and
  consumes 852 KiB of one at the median, 2.08 MiB at the most.
  `bridge.torrentContent.Fetch` is right to claim only the head.

Against that fixed 44 MiB of intent (the totals here are the counter as the
last range request closed, so they sit a tenth of a MiB under the run's own
final figure in section 3b — the counter is still moving when the last body is
released, which is itself part of the point):

| rep | run total | over the 44 MiB claimed | per range request | arriving *between* requests |
| --- | --- | --- | --- | --- |
| 8 | 60.8 MiB | 16.8 MiB | 105 KiB | 2.3 MiB |
| 6 | 77.0 | 33.0 | 208 KiB | 7.7 MiB |
| 3 | 89.8 | 45.8 | 288 KiB | 2.7 MiB |
| 1 | 118.5 | 74.5 | 468 KiB | 12.3 MiB |

Everything above 44 MiB is piece data **no claim asked for and no read
consumed**. In the normal regime the same quantity is 1.5–10.6 MiB. Peer count
was flat at a median of 3 across cheap and expensive reps, so it is not simply
"more peers". Between 2.3 and 12.3 MiB of it arrives while *no* request is
open at all — i.e. after a window was released — and the rest arrives during
open requests, beyond what those requests claimed.

## 7. A mechanism that explains the local overshoot, and does not explain the live one

`MinTraffic.Readahead` is 256 KiB, but `Profile.ReadaheadSize` puts it through
the same `alignUp` as a window, and `alignUp` floors an intent at one whole
piece. So on this torrent every reader prefetches a full 1 MiB piece past its
read position — and because the bridge deliberately claims only the 1 MiB head
of each range, that prefetch reaches outside the claim, where
`Window.Release`'s `CancelPieces` does not go.

On the local seeder that is the whole story, and it is measurable three ways:

| local run (min-traffic session) | distinct claimed | downloaded | overshoot |
| --- | --- | --- | --- |
| shipped profile, rep 1 | 15 MiB | 20.9 MiB | 5.9 MiB |
| shipped profile, rep 2 | 15 | 20.7 | 5.7 |
| shipped profile, rep 3 | 15 | 20.9 | 5.9 |
| readahead raised to 4 MiB | 15 | 41.8 | 26.8 |
| readahead raised to 4 MiB | 15 | 42.0 | 27.0 |
| readahead raised to 4 MiB | 15 | 39.4 | 24.4 |
| readahead not rounded up (candidate fix) | 15 | 15.8 | 0.8 |
| candidate fix | 15 | 15.8 | 0.8 |
| candidate fix | 15 | 15.8 | 0.8 |

min-time, in the same runs, claims 36 pieces and downloads 35.2–35.7 MiB —
**zero overshoot**, because its 2 MiB readahead sits inside its 4 MiB window,
exactly the property `internal/swarm/fetch.go` documents for min-time and
which min-traffic loses once the bridge narrows the claim to the head. Raising
min-traffic's readahead to four pieces takes it to 6.58–7.12 MiB/frame and
**TOR-44's 4.5 ceiling catches it every time** — so that guard demonstrably
works and is a reason not to touch it.

## 8. The candidate fix, and its live null result

The minimal change the section above suggests is one line: a window is a
*claim*, so rounding it up to whole pieces costs nothing that was not already
being paid; a readahead is a *hint* about how far ahead to want data, and
rounding it up manufactures prefetch.

```go
// ReadaheadSize resolves the readahead intent - and, unlike a window, does
// NOT round it up to a whole piece.
func (p Profile) ReadaheadSize(pieceLength int64) int64 { return p.Readahead }
```

Measured in its own scratch arm (its only other change is dropping the
`ReadaheadSize` half of `TestProfileGeometryRoundsToWholePieces`, whose
recorded rationale — a window under one piece measured 62.7s against 3.4s — is
about windows):

- Local min-traffic: **3.48 / 3.45 / 3.49 → 2.63 / 2.65 / 2.63 MiB/frame**, a
  24% cut, and the run total goes from 20.7–20.9 MiB to a bit-stable 15.8.
- **No slowdown**: 900/900/800 ms against 900/800/900 ms, 6/6 frames both
  arms, no skipped points. The round-trip pathology did not appear.
- min-time unchanged; `CGO_ENABLED=0 go vet ./... && go test ./...` all green.

And then, interleaved against the shipped build on the live torrent:

| rep | when | control (shipped) | candidate fix |
| --- | --- | --- | --- |
| 1 | 15:13 / 15:14 | 95.8 MiB (elevated regime) | 89.7 MiB (elevated regime) |
| 2 | 16:48 | 45.5 | 44.5 |
| 3 | 16:49 / 16:50 | 53.9 | 45.1 |
| 4 | 16:51 | 47.0 | 51.4 |
| 5 | 16:52 / 16:53 | 45.8 | 45.8 |

Normal-regime means: control 48.05 (sd 3.95), candidate 46.70 (sd 3.18),
difference **1.35 MiB** — the right sign and far below what four reps an arm
can resolve (a 2 MiB difference needs about 44 reps per arm at sd 3.35). In
the elevated regime it removed 6 MiB of 52.

**So the fix does what it claims and it is not the answer to this ticket.** It
removes the readahead overshoot — proven, deterministically, on the local
harness, where it is the whole 5.9 MiB — and it does **not** remove the live
40–70 MiB tail. Because the fix arm is demonstrably different (20.9 → 15.8 MiB
locally, every rep), that live null is a real null and not "I measured
nothing". The live overshoot comes from something else the client asks for
that our claims do not name, and finding it means instrumenting inside
anacrolix's request strategy rather than in torpeek.

**It was therefore not on this branch** - shipping a fetch-window tuning
change whose measurable benefit is confined to a local fixture, on the
strength of an afternoon in which the live measurement moved by a factor of
two on its own, would have been tuning to a transient.

**It shipped in 1.0.0 as TOR-95**, on a different justification than a live
win: the semantic one. A window is a claim and a readahead is a hint, and
alignUp was turning the hint into a claim - so the change is a correction
rather than a tuning, and its local determinism is the evidence, not a
substitute for a live effect it never claimed. Re-measured then, interleaved,
by two independent runs: 20.3-20.7 -> 15.8-16.7 MiB at load 40-60, and
20.3-21.8 -> 17.1-18.6 MiB at load 435-485, every rep of both runs in the same
direction with no overlap between arms. The second run's higher absolute
numbers under load are themselves worth noting - this profile was already
flagged in section 4 as the load-sensitive one, and it means the 4.5 MiB/frame
ceiling sits closer than a quiet machine suggests. `ReadaheadSize` no longer
rounds, and `TestReadaheadSizeIsNotRoundedUp` fails if anybody makes it.

## 9. What was deliberately not done

- **No ceiling was relaxed.** TOR-44's 4.5 MiB/frame for min-traffic and 7.0
  for min-time are untouched, and the harness that enforces them measures
  3.31–3.61 and 5.94 today, so neither is under pressure. The 60 MiB
  acceptance ceiling is untouched. The raised-readahead arm in section 7
  demonstrates that the 4.5 guard catches a real regression of exactly this
  kind, which is the argument for leaving it exactly where it is.
- **No fetch-window tuning was shipped.** See section 8. One candidate that
  looks free was also checked and is not: bounding the served body to the
  claimed head is *not* behaviour-preserving, because between 1 and 10 of the
  163 requests do read past their own claim, by up to 370 KB, and would come
  back for another range — the round trip `WindowSize`'s own comment says cost
  62.7s against 3.4s.
- **min-time was not measured in the elevated regime.** The single most
  useful missing number in this document; section 10 says how to get it.
- **This is one afternoon's swarm.** Eighteen normal-regime reps sample two
  swarm moments thoroughly and swarm variance across days not at all.

## 10. Recommended follow-ups

1. **Find out whether the elevated regime is profile-specific.** Alternate
   criterion 1 and criterion 2 back-to-back until the regime flips (it took
   about 25 runs today) and record both. If min-time stays at ~102 MiB while
   min-traffic doubles, the tail belongs to the narrow-window profile and the
   fetch path is where to fix it; if both double, it is the swarm's treatment
   of this client and criterion 2 needs a different kind of protection. Costs
   roughly 1.5–2 GB of traffic, which is why it is a ticket and not a
   paragraph here.
2. **Find the live overshoot.** It is 40–70 MiB of piece data that no claim
   named and no read consumed, some of it arriving after `Release`. The
   candidate fix in section 8 rules out reader readahead. The next suspects
   are the claim/cancel churn — one piece claimed and released 82 times per
   run at `PiecePriorityNow` — and chunks that keep arriving from several peers
   after a `CancelPieces`. Instrument anacrolix's request strategy, not
   torpeek.
3. **Have the acceptance report record what a run claimed, not only what it
   spent.** 44 distinct pieces is deterministic; 60.8 MiB is not. A report
   carrying both would have made this ticket a five-minute read.

   **Done in 1.0.0 as TOR-94**, together with item 5 below, which it turned
   out to be the same decision. `swarm.Torrent` now counts the distinct
   pieces any `Claim` covers - `Claim` is the single funnel, and a reader's
   readahead deliberately does not reach it - and carries the figure out on
   `core.Done` as far as the acceptance harness, no further. The report
   prints ordered, arrived and the gap for both criteria. Measured against
   this document's own trace on the local seeder, the counter independently
   reproduces the numbers the scratch patch logged: 36 pieces for min-time
   and 15 for min-traffic, both short by the same 0.3 MiB tail piece.
4. **Do not run the acceptance suite in a tight loop against the public
   torrent, and say how long the machine had been quiet.** The figure is not
   stationary under that load: it doubled after ~25 back-to-back runs and
   needed well over 20 minutes of idle to come back. A release run should be a
   cold run.
5. **Reconsider what criterion 2's ceiling should sit on.** As written it
   measures our fetch plan plus whatever the swarm pushes at us; the intent it
   encodes — "the thrifty profile does not pull the film" — is about the plan,
   which is a deterministic 44 MiB.

   **Settled in 1.0.0 as TOR-94**: the criterion is judged on what the run
   ORDERED, and actual traffic plus the gap are reported beside it rather
   than folded into it. The 60 MiB ceiling did not move - against a 44 MiB
   order it is 16 MiB of room for the fetch plan to grow into, and the plan
   is the part this project controls. Criterion 1 stays on arrivals: its 150
   MB ceiling sits over a figure inside a 2.7 MiB band for seven releases, so
   there is nothing there to protect and re-basing it would have cost the
   release-to-release comparison. REQUIREMENTS.md 8.2 says all of this in the
   requirement itself, and 2.6 says why a run's traffic BUDGET keeps counting
   arrivals: a budget protects a link, and bytes on the wire cost the same
   whoever asked for them.
