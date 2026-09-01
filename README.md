# torpeek

Preview a video torrent without downloading it: frames spread evenly across each
video file, plus audio tracks, subtitles and quality figures — in minutes and
tens of megabytes instead of hours and gigabytes.

It works because BitTorrent lets a client ask for arbitrary pieces of a file,
and video containers carry an index mapping timestamps to byte offsets. torpeek
fetches the index, works out which pieces hold the frames it wants, and asks
only for those.

**Status:** early development. Nothing works yet — see
[REQUIREMENTS.md](REQUIREMENTS.md) for what it is meant to do and
[section 6](REQUIREMENTS.md#6-стек) for why it is written in Go.

## Build

```sh
make build     # ./bin/torpeek
make cross     # dist/{darwin,linux,windows}-*/
make check     # vet + tests
```

Frame decoding shells out to `ffmpeg`/`ffprobe`, which ship next to the binary
in a release archive.
