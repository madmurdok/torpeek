# torpeek

Preview a video torrent without downloading it: frames spread evenly across each
video file, plus audio tracks, subtitles and quality figures — in minutes and
tens of megabytes instead of hours and gigabytes.

It works because BitTorrent lets a client ask for arbitrary pieces of a file,
and video containers carry an index mapping timestamps to byte offsets. torpeek
fetches the index, works out which pieces hold the frames it wants, and asks
only for those.

```
$ torpeek -n 6 'magnet:?xt=urn:btih:...'
Some.Movie.2019.1080p
  1 video file(s)

Some.Movie.2019.1080p/movie.mkv
  1h52m30s, 1920x1080, h264, 0.098 bits/pixel
  audio: eng "Original", eac3, 6 ch
  audio: rus "Dub", ac3, 6 ch
  subtitles: eng, subrip
  frame 00  5m37s
  frame 01  22m30s
  ...
  6 frames

6 frames from 1 file(s) in 1m14s, 71.4 MiB downloaded
```

## What it is for

- **Answer one question cheaply:** is this the right release — the right cut,
  the right dub, the right quality — before spending hours and gigabytes
  finding out. Twenty frames and a track list are usually the whole answer.
- **Work over a swarm nobody controls.** Pieces arrive out of order, peers
  hold different parts, some regions are unavailable at all. The tool budgets
  its time and traffic and reports what it could not get, instead of hanging.
- **Stay a library.** The core prints nothing and knows nothing about how
  results are shown; the CLI is one client of it, and the TUI and web UI will
  be others.
- **Be one folder eventually.** No install, no daemon, no service: a binary
  and the two ffmpeg executables next to it. That is not true yet — see below.

## Status

Early development, but the main path works: a magnet link or `.torrent` turns
into frames on disk in one command, over one command-line interface.

Working today: torrent session and metadata over BEP 9, piece window fetching
with priorities and readahead, a loopback HTTP bridge that gives ffmpeg a
seekable file, `ffprobe` media inspection and keyframe lookup, capture point
planning, frame decode at source resolution, atomic writes, a run budget for
time and traffic, parallel work across a torrent's video files, contact sheet
assembly, and the CLI with NDJSON output.

Not built yet: the JSON manifest, the result cache and
resume, the live TUI, the web UI, and release archives with bundled ffmpeg. The
`min-time` and `min-traffic` profiles already differ in readahead and window
size, but the strategies on top of them are still open. See
[REQUIREMENTS.md](REQUIREMENTS.md) for what this is meant to become and
[ARCHITECTURE.md](ARCHITECTURE.md) for how it is put together.

## Requirements

**There are no prebuilt binaries yet.** Release archives with ffmpeg bundled
alongside the executable are planned but unbuilt, so the only way to run
torpeek today is to compile it. That means two things have to be on the
machine:

- **Go 1.27 or newer** — the version in `go.mod`. `go version` to check;
  [go.dev/dl](https://go.dev/dl/) to install.
- **ffmpeg and ffprobe** — frame decoding and container inspection shell out
  to them. `brew install ffmpeg` on macOS, `apt install ffmpeg` on Debian or
  Ubuntu. Developed against ffmpeg 8.1.

torpeek looks for the two executables next to its own binary first — that is
where a release archive will one day put them — and falls back to `PATH`.

## Build from source

```sh
git clone https://github.com/madmurdok/torpeek.git
cd torpeek
make build          # ./bin/torpeek
```

A first run on something legal and well seeded, which is also how the project
tests itself:

```sh
curl -LO https://archive.org/download/BigBuckBunny_124/BigBuckBunny_124_archive.torrent
./bin/torpeek -n 6 -out ./frames BigBuckBunny_124_archive.torrent
```

That writes six frames from each of the torrent's three video files — 18 in
`./frames` — and takes about 130 MB of its 421 MB to do it, in ten seconds on a
warm swarm. `-mode min-traffic` trades time for a fraction of that. Point it at
a magnet link the same way.

Other targets:

```sh
make check     # vet + tests (the suite drives real torrents through a local seeder, so it is slow)
make cross     # dist/{darwin,linux,windows}-*/
make fmt
```

## Usage

```sh
./bin/torpeek [flags] <magnet-uri | file.torrent>
```

| flag | default | meaning |
| --- | --- | --- |
| `-out` | `torpeek-out` | directory for frames |
| `-data` | a temporary one | directory for fetched pieces |
| `-n` | `20` | frames per video file |
| `-start` / `-end` | `0.05` / `0.95` | capture window, as a fraction of the duration |
| `-mode` | `min-time` | `min-time` or `min-traffic` |
| `-format` | `jpeg` | `jpeg` or `png` |
| `-max-bytes` | 150 MiB per file, capped at 2 GiB | traffic ceiling for the run |
| `-max-time` | `10m` | time ceiling for the run |
| `-file` | all of them | which video files to process: torrent index or path pattern, comma-separated |
| `-list` | `false` | list the torrent's video files and exit, without taking frames |
| `-parallel` | `4` | video files to work on at once |
| `-torrent-port` | any free port | BitTorrent listen port, for a fixed port range |
| `-bridge-port` | any free port | loopback port for the internal HTTP bridge |
| `-peer` | — | comma-separated peers to contact directly |
| `-upload` | `true` | serve pieces back to the swarm while running |
| `-dht` | `true` | use DHT and PEX (never for a private torrent) |
| `-sequential` | `false` | when a container has no usable index, degrade to sequential capture from the start instead of failing |
| `-json` | `false` | emit NDJSON events instead of human output |

Ctrl+C cancels the run rather than killing it: frames already written stay, and
the summary still prints.

A pack does not need a frame set from every episode to answer whether it is the
right rip. `-list` shows what is inside without fetching any of it, and `-file`
narrows the run — by the index `-list` prints, or by a pattern:

```sh
./bin/torpeek -list season-1.torrent
./bin/torpeek -file S01E03 season-1.torrent      # or -file 11, or -file '*.mkv'
```

The traffic ceiling follows the selection, so one episode gets one episode's
worth of budget rather than a share of the pack's.

### Output layout

```
<out>/<infohash>/<run-params>/<NN-file-name>/frames/000.jpg
```

The infohash and the run parameters are part of the path so two runs over the
same torrent with different settings do not overwrite each other.

### For scripts

`-json` writes one event per line, each with an explicit `type`, covering the
whole run: `metadata_ready`, `file_started`, `frame_ready`, `frame_skipped`,
`progress`, `budget_warning`, `file_done`, `done`, `failed`.

```sh
torpeek -json -n 6 movie.torrent | jq -r 'select(.type == "frame_ready") | .path'
```

Exit codes distinguish the three outcomes a caller has to tell apart:

| code | meaning |
| --- | --- |
| `0` | the run finished |
| `1` | the run produced nothing |
| `2` | bad command line — nothing was attempted |
| `3` | stopped at a budget or cancelled; what was produced is kept |
