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
  results are shown; the CLI and the web UI are two clients of it, and the TUI
  will be a third.
- **Be one folder.** No install, no daemon, no service: a binary and the two
  ffmpeg executables next to it. True on Linux and Windows; macOS still has to
  be built from source — see below.

## Status

Early development, but the main path works: a magnet link or `.torrent` turns
into frames on disk in one command, over one command-line interface.

Working today: torrent session and metadata over BEP 9, piece window fetching
with priorities and readahead, a loopback HTTP bridge that gives ffmpeg a
seekable file, `ffprobe` media inspection and keyframe lookup, capture point
planning, frame decode at source resolution, atomic writes, a run budget for
time and traffic, parallel work across a torrent's video files, contact sheet
assembly, a JSON manifest per file, a result cache that serves a repeat run
from disk without touching the network, the CLI with NDJSON output, and a web
UI with its real screens, sitting on the same `core.Engine` library the CLI
drives.

The UI also queues a second torrent instead of refusing it — the client can
only run one torrent's networking at a time (one BitTorrent port, one
client-wide DHT switch), so the second waits for the first's slot — and lists
every torrent it knows about, live ones from memory and finished ones found on
disk, so the list survives a restart. Opening a finished one from that list
replays it from disk: no queue slot, no network request.

Release archives now assemble for Linux and Windows: `make archives` builds
one folder per platform holding torpeek, ffmpeg, ffprobe and their licence
material. The Linux one has been run end to end on a clean Debian 12 with no
ffmpeg installed and nothing on `PATH`; the Windows one is assembled and
checksummed but has not yet been run on Windows. macOS is deliberately not
among them — there is no prebuilt LGPL ffmpeg for it worth shipping, and
torpeek will not quietly ship a GPL one;
[docs/licensing.md](docs/licensing.md) sets out the choice that is left.
Nothing has been published anywhere yet.

Not built yet: the live TUI, and the macOS archives. The `min-time` and
`min-traffic` profiles already differ in readahead and window size, but the
strategies on top of them are still open. See
[REQUIREMENTS.md](REQUIREMENTS.md) for what this is meant to become and
[ARCHITECTURE.md](ARCHITECTURE.md) for how it is put together.

## Requirements

**No archive has been published yet**, so today every route starts by
compiling. Once one is: a Linux or Windows archive needs nothing installed —
unpack it and run `./torpeek`, with ffmpeg and ffprobe already in the folder
(`make archives` builds those two; see
[docs/licensing.md](docs/licensing.md) for what the bundled binaries are and
why macOS is not among them).

Compiling needs two things on the machine:

- **Go 1.27 or newer** — the version in `go.mod`. `go version` to check;
  [go.dev/dl](https://go.dev/dl/) to install.
- **ffmpeg and ffprobe** — frame decoding and container inspection shell out
  to them. `brew install ffmpeg` on macOS, `apt install ffmpeg` on Debian or
  Ubuntu. Developed against ffmpeg 8.1; the release archives bundle 9.0.1. The
  test suite additionally needs a GPL-capable ffmpeg, because it renders its
  own fixtures with libx264 — which the bundled LGPL build cannot do, and does
  not need to: torpeek only ever decodes H.264.

torpeek looks for the two executables next to its own binary first — that is
where the release archive puts them — then in `third_party/ffmpeg/` beside it,
and only then falls back to `PATH`.

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
make cross     # dist/{darwin,linux,windows}-*/ - the four binaries, CGO-free
make ffmpeg    # third_party/ffmpeg/<platform>/ - the bundled LGPL ffmpeg, checksums verified
make archives  # dist/torpeek-<version>-<platform>{,.tar.gz,.zip} - the release archives
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
| `-torrent-port` | an OS-assigned port | BitTorrent listen port - also pins DHT and uTP, which share it; set this on a host with a fixed allocated range |
| `-bridge-port` | an OS-assigned port | loopback port for the internal HTTP bridge; set this on a host with a fixed allocated range |
| `-web` | `false` | serve the web UI and open it in a browser instead of running on the command line |
| `-web-host` | `127.0.0.1` | bind address for the web UI - loopback behind a reverse proxy is the documented setup |
| `-web-port` | `8765` | port for the web UI |
| `-base-path` | the site root | path the UI is mounted under behind a reverse proxy, e.g. `/torpeek` |
| `-web-token` | none on localhost | access token required to use the UI/API; auto-generated and required once reachable beyond localhost (a non-loopback `-web-host` or a `-base-path`) - set this to pin one across restarts, e.g. under systemd |
| `-watch-dir` | none | directory a torrent client on this host watches for `.torrent` files; with `-web`, a run then offers a button that drops its `.torrent` there. Without it the button is absent |
| `-headless` | `false` | do not try to open a browser; only serve (for a seedbox with no desktop) |
| `-peer` | — | comma-separated peers to contact directly |
| `-upload` | `true` | serve pieces back to the swarm while running |
| `-dht` | `true` | use DHT and PEX (never for a private torrent) |
| `-sequential` | `false` | when a container has no usable index, degrade to sequential capture from the start instead of failing |
| `-json` | `false` | emit NDJSON events instead of human output |
| `-version` | `false` | print version and exit |

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

### Web UI

```sh
./bin/torpeek -web                    # opens http://127.0.0.1:8765/
./bin/torpeek -web movie.torrent      # and starts on that torrent straight away
```

One binary is both the engine and the UI: the frontend is compiled into it, so
the folder holds the executable and ffmpeg and nothing else. Paste a magnet
link or drop a `.torrent` onto the page: it receives the same events `-json`
writes, over a WebSocket, and fills a live grid of frames as they land, next
to a summary panel of tracks and quality.

The client can only run one torrent's networking at a time — a pinned
BitTorrent port can't be bound twice, and DHT is a client-wide switch, so a
public and a private torrent can't share it either. A second torrent handed
to the UI while the first is still going is not refused: it queues, and starts
as soon as the slot is free. A panel lists every torrent the server knows
about — the live one, queued or running, and every finished run found by
walking the output directory — so the list survives a restart. Opening a
finished run from that panel replays it from its saved frames and manifest:
no queue slot, no network request.

The page's cancel button stops whatever is in the slot, or drops a queued run,
and keeps what was produced. If no browser can be opened — a headless
machine, a seedbox — the address is printed and the server keeps serving.

Once a run is over it offers its `.torrent`, saved beside its frames. **Save
.torrent** downloads it to wherever the browser is; that is not the same thing
as starting it, because the browser is usually not the seedbox. **Send to my
client** is: with `-watch-dir` pointed at a directory a torrent client on the
same host already watches, the button drops a copy there and the client picks
it up. No credentials, no client API, and no button at all when the flag is
not set.

### Output layout

```
<out>/<infohash>/<run-params>/<infohash>.torrent
<out>/<infohash>/<run-params>/run.json
<out>/<infohash>/<run-params>/<NN-file-name>/frames/000.jpg
```

The infohash and the run parameters are part of the path so two runs over the
same torrent with different settings do not overwrite each other.

Every run keeps the `.torrent` it was made from, magnets included — the
metadata a magnet fetches is exactly what a `.torrent` file carries. The info
dictionary is stored byte for byte as it arrived, so the saved file's infohash
is the torrent's; everything around it is generated when the file is written,
because a magnet never fetches it. The creation date is that moment and the
comment and created-by name the BitTorrent library, not whoever published the
torrent. The trackers are the real ones, so the file finds its swarm. It is a
faithful torrent and not a byte-identical copy of the original file.

### For scripts

`-json` writes one event per line, each with an explicit `type`, covering the
whole run: `metadata_ready`, `file_started`, `frame_ready`, `frame_skipped`,
`progress`, `budget_warning`, `file_done`, `done`, `failed`. `done` carries
`torrent_path`, the run's own saved `.torrent`, the way `file_done` carries
`sheet_path` and `manifest_path`.

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

## Licence

torpeek is MIT — see [LICENSE](LICENSE).

The release archives are a separate question, because they carry a
third-party ffmpeg build with its own terms: what travels inside them, why it
is an LGPL build rather than a GPL one, and what that obliges is set out in
[docs/licensing.md](docs/licensing.md).
