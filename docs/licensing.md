# Licensing the release archives

REQUIREMENTS.md section 4 says the release is one folder per platform with
ffmpeg and ffprobe next to the binary, and flags the consequence in the same
breath: *"появляется вопрос лицензий (сборки ffmpeg под GPL) при публичном
распространении — учесть при первом релизе."* This is that reckoning. It is
the "licensing position is documented" half of TOR-26's acceptance criterion.

Nothing here is legal advice. It is a record of a decision, of what the
decision obliges, and of which of those obligations the packaging machinery
already discharges.

## The decision

**An LGPL ffmpeg build travels inside the archive, with its licence text and a
written source offer beside the binaries.**

Two alternatives were on the table and both were rejected:

- **A GPL ffmpeg build.** Every convenient prebuilt ffmpeg is
  `--enable-gpl`, so this is the path of least resistance. It would put the
  archive's contents under the GPL as a distributed whole and force a decision
  about torpeek's own licence that nobody has made. Rejected.
- **Fetch ffmpeg on first run.** The archive stays small and carries no
  third-party licence at all, because it distributes nothing. It also turns
  section 4's "one folder, one entry point" into "one folder and a script you
  have to run first", on a tool whose whole promise is that there is nothing to
  install. Rejected.

LGPL keeps the archive self-contained and leaves torpeek's own licence an open
question rather than a forced answer.

## Why the LGPL side is enough for what torpeek does

An LGPL ffmpeg is not a smaller ffmpeg in the ways that would matter to a
video *encoder*. It omits FFmpeg's GPL-only components, and every one of them
is either an encoder torpeek does not use or a filter it does not call:

- **libx264 and libx265 are encoders.** torpeek only ever *decodes* H.264 and
  HEVC; libavcodec's own `h264` and `hevc` decoders are native and LGPL.
- **The GPL filters** (`vf_blackframe`, `vf_cropdetect`, `vf_hqdn3d` and the
  rest listed in `licenses/ffmpeg/LICENSE.md`) are unreachable from torpeek,
  which passes no `-vf`, no `-filter_complex` and no `-lavfi` at all. Blank
  frame detection is done in Go, in `internal/frames/blank.go`, not by an
  ffmpeg filter.
- **The only encoders torpeek asks for are `mjpeg` and `png`** (see
  `internal/frames/extract.go`), both native and LGPL.

That was checked by running the LGPL `ffmpeg` and `ffprobe` out of the
assembled linux-amd64 archive, against real files, rather than by reading
configure. (The Windows binaries are the same build from the same release,
configured the same way, but nobody has run them on Windows yet - see the
caveat at the end of this section.)

| what torpeek needs | how it was confirmed |
|---|---|
| decoders `h264`, `hevc`, `vp9`, `theora`, `mpeg4` | one clip of each decoded to both a jpeg and a png frame, with the bundled `ffmpeg` and `ffprobe`, in an empty environment |
| encoders `mjpeg`, `png` | those same frames - `mjpeg` and `png` are what wrote them |
| the GPL encoders being gone | `-c:v libx264` and `-c:v libx265` both fail with `Unknown encoder`, where the same command with `-c:v mjpeg` succeeds |
| demuxers `mov,mp4`, `matroska,webm`, `avi`, `ogg` | each of those five clips is in a different one of them |
| muxers `image2`, `image2pipe` | `image2pipe` is what torpeek writes frames through, and did |
| input protocols `file`, `http`, `tcp`, `pipe` | listed by `-protocols`; `http` is load-bearing, since the loopback bridge in `internal/bridge` is how ffmpeg gets a seekable file, and a full torpeek run over it produced frames |
| `ffprobe -show_streams`, `-show_entries packet=...`, `-read_intervals` | exercised by that same run - they are how capture points are planned |

`theora` and `mpeg4` are on that list because they are not hypothetical: the
acceptance corpus is the Internet Archive Sintel torrent, whose files include
`sintel-2048-stereo.ogv` (Theora) and `Sintel_Documentary_by_Ali_Boubred.avi`
(MPEG-4 part 2) alongside the H.264 `.mp4`s. See `docs/results/`. The Theora
clip used for the check above is the first 4 MB of that same
`sintel-2048-stereo.ogv`, not a substitute for it.

The whole Linux archive was then run on a clean Debian 12 container with no
ffmpeg installed, no network, and an empty `PATH` for the process: a two-file
torrent of an H.264 `.mkv` and that Theora `.ogv`, served by a loopback
seeder, produced 8 frames, 2 contact sheets and 2 manifests naming `h264` and
`theora`. Deleting the two bundled binaries and repeating it fails with
`ffmpeg: executable not found`, which is what makes the first run evidence
about the bundled copies rather than about whatever the machine had.

The LGPL build cannot encode H.264 - `libx264` is absent, as it should be.
Nothing in torpeek asks it to. Note that the project's own *test fixtures* are
generated with `libx264` (`internal/probe`, `internal/frames`,
`internal/cli`), so `make check` still needs a GPL-capable ffmpeg on the
developer's PATH. That is a build-time dependency of the test suite, not
something the release archive distributes.

**What has not been run:** the windows-amd64 archive, on Windows. It is
assembled and checksummed, and its ffmpeg comes from the same BtbN release and
the same `-lgpl` configuration as the Linux one, but the only claim anybody
can make about it today is structural. Two things about it are worth an
actual test before it is published: that Explorer's unzip leaves the three
executables in one folder, and that `ffmpeg.Locate` finds `ffmpeg.exe` rather
than a bare `ffmpeg` - the second is what
`TestExecutableNameForEveryTarget` in `internal/ffmpeg` now pins, because the
older test could only ever exercise the branch for the machine running it.
Neither macOS archive exists to test.

## What LGPL distribution obliges, and where each obligation lives

The build we ship is under **LGPL v3** (BtbN configures with
`--enable-version3`; the `LICENSE.txt` inside their `-lgpl` archive is
byte-identical to FFmpeg's own `COPYING.LGPLv3` at the shipped commit). So:

| obligation | discharged by |
|---|---|
| ship the licence text | `licenses/ffmpeg/COPYING.LGPLv3` in every archive, plus `COPYING.GPLv3`, which LGPL v3 incorporates by reference |
| say prominently that the work is used and under which licence | `README.txt` and `THIRD-PARTY-NOTICES.md` in every archive |
| make the corresponding source available | the written offer in `THIRD-PARTY-NOTICES.md`, naming the exact FFmpeg commit and the exact build definition that produced the binary |
| keep the binaries unmodified, or say what changed | they are redistributed byte for byte; `scripts/fetch-ffmpeg.sh` verifies each one's sha256 against `third_party/ffmpeg.lock` |

torpeek itself is unaffected: it never links FFmpeg. It runs `ffmpeg` and
`ffprobe` as child processes over their command line and stdout
(`internal/ffmpeg`), which is why the Go side is CGO-free in the first place.
Shipping the three executables in one folder is aggregation, not combination,
so the LGPL reaches the two ffmpeg binaries and stops there.

## Provenance, and why it is checksummed twice

`third_party/ffmpeg.lock` records, per platform: the upstream builder and its
release tag, the build-scripts commit, the FFmpeg commit, the archive URL, the
archive's sha256, and the sha256 of `ffmpeg`, of `ffprobe` and of the licence
text inside it. `scripts/fetch-ffmpeg.sh` verifies all of them at fetch time
and refuses rather than warns.

The archive hash alone would not be enough. Two of those hashes exist for
reasons worth stating:

- **Per-binary hashes** mean the thing that lands in `dist/` is pinned, not
  merely the container it arrived in.
- **The licence text's hash** is the guard against the one failure that would
  otherwise be silent: an upstream `-lgpl` asset repointed at a GPL build. The
  binaries would change, so their hashes would fail too - but this one says
  *why*, in the error message, which is the difference between a release
  engineer bumping a hash and a release engineer stopping to read.

The checksums published by the builder are useful for catching a corrupted
download, but they are published by the same party as the binaries, so they
are not an independent attestation. The trust anchor is that the build is
produced by a public GitHub Actions workflow from a public build definition,
and that the hash of the artefact we reviewed is now pinned in our own
repository - a later substitution upstream cannot pass unnoticed.

### Chosen source: BtbN/FFmpeg-Builds

For Linux and Windows the bundled binaries come from
[BtbN/FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds), which is the
source the FFmpeg project's own download page points at for Windows and Linux.
It builds on public GitHub Actions runners from a public, per-library build
definition, and - unusually among ffmpeg builders - it publishes an explicit
**`-lgpl`** variant next to the GPL one rather than only the GPL one. Both are
static, so an archive needs exactly two extra files and no shared-library
search path.

## The macOS gap

**There is no prebuilt LGPL ffmpeg/ffprobe for macOS from a source worth
trusting, and this ticket does not invent one.** Both macOS stanzas in
`third_party/ffmpeg.lock` are `status = blocked`, `make archives` builds the
other two platforms and exits non-zero naming the ones it skipped, and no
macOS archive is produced at all. Shipping GPL on macOS only, or building
ffmpeg from source, are both decisions for a person.

The shape of the problem shows in FFmpeg's own download page: for Windows and
for Linux it points at BtbN, who publish an LGPL variant. For macOS it points
at exactly one builder, evermeet.cx, whose builds are `--enable-gpl` **and**
`--enable-nonfree` and Intel-only. There is no macOS equivalent of the BtbN
`-lgpl` asset anywhere.

What was checked, and what each turned out to be:

| candidate | verdict |
|---|---|
| BtbN/FFmpeg-Builds | no macOS targets at all - Linux and Windows only |
| evermeet.cx | `--enable-gpl` **and** `--enable-nonfree` (x264, x265, faac). Intel only |
| ffmpeg.martin-riedl.de | `--enable-gpl --enable-version3` (x264, x265, libklvanc). Has both Intel and Apple Silicon, which is otherwise the hard part |
| OSXExperts | GPL |
| `descriptinc/ffmpeg-ffprobe-static`, `eugeneware/ffmpeg-static`, ffbinaries | repackagers of evermeet and OSXExperts; GPL, and provenance is a chain of third parties |
| `imageio-ffmpeg` (PyPI) | `--enable-gpl --enable-nonfree`, and ships no `ffprobe` |
| ffmpeg-kit | LGPL v3 variants exist, but they are iOS/macOS XCFrameworks - libraries, no CLI executables - and the project is retired |
| Homebrew, MacPorts, nixpkgs | GPL formulae, and dynamically linked against the package manager's own prefix |
| **conda-forge `ffmpeg-*-lgpl_*`** | **genuinely LGPL**: the binary's own configure line carries `--disable-gpl --enable-version3`, so LGPL v3 like BtbN's. Covers `osx-64` **and** `osx-arm64`, public recipe, sha256 in the channel metadata. But dynamically linked: `ffmpeg` and `ffprobe` are 0.6 MB between them and pull a **69-library, 139 MB** dylib closure, of which 33 MB is `libicudata` |

So the choice is between three things, none of which is this ticket's to make:

1. **conda-forge, bundling the dylib closure.** This one was tried, not just
   costed. The rpath is already relocatable (`@loader_path/../lib/`), so it
   needs no binary patching: the two executables go in `third_party/ffmpeg/`
   inside the archive - which `ffmpeg.Locate` already searches - and the
   libraries in `third_party/lib/`. Assembled that way it is 164 MiB, and a
   full torpeek run against a loopback seeder with an empty `PATH` produced
   frames from both an H.264 and a Theora file on darwin/amd64. What makes it
   a decision rather than a fix is the cost: ~140 MB of libraries on top of a
   0.6 MB pair of executables, and 69 more projects whose licence texts then
   have to travel in the archive too.
2. **Build a minimal LGPL ffmpeg for darwin from source.** torpeek needs a
   decode-only ffmpeg with two image encoders; configured to that, it is a
   ~15 MB build rather than a ~116 MB one, and it would fix the size problem
   on *every* platform (see below). It also makes torpeek the builder, and so
   the party responsible for the corresponding source, the toolchain and the
   codesigning.
3. **No macOS archive in the first release.** Honest, and cheap: macOS users
   build from source, which is what the README documents today.

Whichever is chosen, darwin/arm64 needs signing thought that the other two
platforms do not: an unsigned, unnotarised Mach-O from a downloaded archive is
quarantined by Gatekeeper. REQUIREMENTS section 5 puts signing and notarisation
out of scope for v1, which is a decision about torpeek's own binary that will
apply to the bundled ffmpeg too.

## Size

Section 4 estimates ~80 MB per platform. The measured figure for the chosen
static LGPL builds is **three times that**, and the reason is worth recording
rather than discovering again later.

Measured on the 0.9.0 archives this machinery actually produced:

| platform | ffmpeg | ffprobe | torpeek | unpacked | compressed |
|---|---|---|---|---|---|
| linux-amd64 | 110.66 MiB | 110.46 MiB | 30.68 MiB | 251.86 MiB | 107.74 MiB (`.tar.gz`) |
| windows-amd64 | 109.10 MiB | 108.91 MiB | 31.06 MiB | 249.12 MiB | 106.60 MiB (`.zip`) |

Two causes, in order of size. First, a static `ffmpeg` and a static `ffprobe`
each embed their own copy of the same ~110 MB of library code, so half of
every archive is a duplicate. Second, BtbN configures in everything they can
LGPL-legally reach - libaom, rav1e, SVT-AV1, vvenc, dav1d, libvpx, uavs3d,
kvazaar, JPEG XL, WebP, libass with harfbuzz/freetype/fribidi/fontconfig,
libbluray, libzvbi, aribb24, libopenmpt, libgme, SRT, RIST, ZeroMQ, LV2,
OpenAL, Vulkan with libplacebo, chromaprint, libvmaf, zimg, and the AMF, QSV
and NVENC hardware paths - and torpeek asks for none of it beyond five
decoders and two image encoders.

Two ways down, if the size is judged unacceptable:

- **BtbN's `-lgpl-shared` variant.** The same build, with the libav\*
  libraries shared between the two executables instead of copied into both.
  Measured on the same pinned release: `ffmpeg` and `ffprobe` shrink to
  0.46 MiB and 0.21 MiB, and the seven shared objects come to 137.3 MiB, so
  the payload drops from 221.1 MiB to 138.0 MiB - and the published tarball is
  51.9 MiB against 108.1 MiB. It costs a load path: the binaries resolve their
  libraries relative to their own directory, in upstream's `bin/` and `lib/`
  shape. Verified by running it - `bin/ffmpeg` starts with `lib/` beside its
  parent and dies with `libavdevice.so.63: cannot open shared object file` the
  moment the binary is moved out of that shape. Static was chosen over this
  because two self-contained files cannot be half-copied, cannot find the
  wrong library, and need no per-platform layout.
- **Build our own,** as in option 2 above: a decode-only LGPL ffmpeg is around
  15 MB, which would put a platform's archive under 40 MB - comfortably inside
  section 4's estimate rather than three times over it.

BtbN publish `linuxarm64` and `winarm64` LGPL builds in the same releases, so
adding either target later is a matter of two more stanzas in the lock and two
more lines in `make cross`, not another search for a builder.

## Still open

These are named because leaving them unnamed is how they get missed, not
because TOR-26 was meant to settle them.

- **torpeek has no licence of its own.** There is no `LICENSE` file in the
  repository. The LGPL decision was made specifically so this could stay open,
  but a public release cannot: a GitHub release of an unlicensed program gives
  its recipients no rights at all. This needs deciding before anything is
  published, independently of ffmpeg.
- **Patents are a separate axis from copyright.** H.264, HEVC and AAC are
  patent-encumbered, and no free-software licence - LGPL included - grants
  patent rights in the encoded formats. Distributing a decoder is the same
  posture every open-source distributor takes, and this document does not
  change or evaluate it. Flagged so that "licensing is resolved" is not read
  as covering it.
- **Mirroring the corresponding source at release time.** The written offer in
  each archive is honoured today by pointing at GitHub for the FFmpeg commit
  and the build definition. GPL v3 section 6(d)'s cleaner route is to serve the
  source from the same place as the binaries: when the release assets are
  uploaded, attach the FFmpeg source tarball for the pinned commit alongside
  them. `third_party/ffmpeg.lock` records the URL to fetch it from.
- **A `-lgpl-shared` or self-built ffmpeg** would change the archive layout,
  and `ffmpeg.Locate`'s second search directory (`third_party/ffmpeg` next to
  the binary) already exists for exactly that; nothing in Go needs to change
  to take either option.
