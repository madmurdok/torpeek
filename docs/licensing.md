# Licensing the release archives

REQUIREMENTS.md section 4 says the release is one folder per platform with
ffmpeg and ffprobe next to the binary, and flags the consequence in the same
breath: *"появляется вопрос лицензий (сборки ffmpeg под GPL) при публичном
распространении — учесть при первом релизе."* This is that reckoning. It began
as the "licensing position is documented" half of TOR-26's acceptance
criterion and TOR-93 extended it, which is why it reads as a record of two
decisions rather than one.

Nothing here is legal advice. It is a record of decisions, of what they
oblige, and of which of those obligations the packaging machinery already
discharges.

There are two decisions, taken months apart, and the second did not replace
the first:

- **Linux and Windows bundle an LGPL ffmpeg** - TOR-26, below.
- **macOS bundles a GPL one, and the macOS archive travels under the GPL** -
  TOR-93, in "macOS: a GPL build, deliberately". Nothing about the other two
  platforms changed, and that asymmetry is the decision, not an oversight.

## The decision (Linux and Windows)

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

The first bullet's objection has since expired, and only on macOS. It rested
entirely on the licence question being open, and TOR-92 closed it: torpeek is
MIT. That did **not** move Linux and Windows, because the objection was only
ever half the argument - the other half is that an LGPL build is available for
them and gives their recipients more, so there is nothing to buy by changing
it. On macOS no such build exists, and the alternative was shipping no archive
at all. See "macOS: a GPL build, deliberately".

## Why the LGPL side is enough for what torpeek does

This is the argument for Linux and Windows. It is also the reason the macOS
GPL build changes nothing about what torpeek *does*: a GPL ffmpeg is this same
ffmpeg with the GPL-only components added, and torpeek still never asks for
any of them. What the GPL build changes is the terms the macOS archive is
handed over under, not its behaviour.

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

The macOS archive's ffmpeg *can* encode H.264, since it is a GPL build - which
is how TOR-93 proved it really is one rather than a mislabelled LGPL build; see
"Run, on darwin/amd64". That capability is incidental. torpeek asks for `mjpeg`
and `png` on every platform and nothing else.

**What has been run, and where.** All four archives, each on a clean machine
of its own operating system, by `.github/workflows/archives.yml` - the same
two arms as above, and the same conclusion drawn the same way (TOR-101). Until
that workflow existed the windows-amd64 and darwin-arm64 archives had never
been executed at all, because this project is developed on an Intel Mac, which
can run neither a PE binary nor an arm64 Mach-O; everything anybody could say
about those two was structural.

The two things worth an actual test on Windows both now have one. Unpacking
the `.zip` with `Expand-Archive` - the same ZIP implementation Explorer's
"Extract All" uses, though not the GUI itself - leaves torpeek.exe, ffmpeg.exe
and ffprobe.exe in one folder; and `ffmpeg.Locate` does find `ffmpeg.exe`
rather than a bare `ffmpeg`, which the control arm states outright by failing
with `searched: ...\torpeek-1.0.0-windows-amd64-no-ffmpeg\ffmpeg.exe`. That
branch of `executableNameFor` was pinned by
`TestExecutableNameForEveryTarget` in `internal/ffmpeg` for want of anything
better; it has now also executed on Windows.

On darwin-arm64 the open question was whether an arm64 Mach-O nobody signed
with a Developer ID would start at all - the kernel refuses an arm64 binary
with no valid signature outright, rather than merely warning. Three of them
start: the bundled ffmpeg and ffprobe, signed by their builder, and torpeek's
own binary, which carries only the ad-hoc signature the Go linker applies when
cross-linking for darwin/arm64 from a Linux host.

**What still has not been run in CI:** the Gatekeeper path the macOS README.txt
warns about. CI never downloads an archive through a browser, so
`com.apple.quarantine` is never set on the files it unpacks, and nothing in
`archives.yml` exercises it. It has since been run by hand on darwin/amd64,
with the attribute written directly - see "Gatekeeper" below (TOR-97), which is
also what replaced that advice with `first-run.command` in the archive. On
darwin/arm64 it is still untested, and from an Intel Mac untestable.

## What LGPL distribution obliges, and where each obligation lives

This section is about the Linux and Windows archives. The macOS one is GPL and
has its own table further down.

The build those two ship is under **LGPL v3** (BtbN configures with
`--enable-version3`; the `LICENSE.txt` inside their `-lgpl` archive is
byte-identical to FFmpeg's own `COPYING.LGPLv3` at the shipped commit). So:

| obligation | discharged by |
|---|---|
| ship the licence text | `licenses/ffmpeg/COPYING.LGPLv3` in the Linux and Windows archives - in fact in every archive, plus `COPYING.GPLv3`, which LGPL v3 incorporates by reference |
| say prominently that the work is used and under which licence | `README.txt` and `THIRD-PARTY-NOTICES.md` in every archive |
| make the corresponding source available | the written offer in `THIRD-PARTY-NOTICES.md`, naming the exact FFmpeg commit and the exact build definition that produced the binary |
| keep the binaries unmodified, or say what changed | they are redistributed byte for byte; `scripts/fetch-ffmpeg.sh` verifies each one's sha256 against `third_party/ffmpeg.lock` |

torpeek itself is unaffected: it never links FFmpeg. It runs `ffmpeg` and
`ffprobe` as child processes over their command line and stdout
(`internal/ffmpeg`), which is why the Go side is CGO-free in the first place.
Shipping the three executables in one folder is aggregation, not combination,
so the LGPL reaches the two ffmpeg binaries and stops there.

## Provenance, and why the licence is checked twice over

`third_party/ffmpeg.lock` records, per platform: the upstream builder and its
release tag, the build-scripts commit, the FFmpeg commit, the archive URL, the
archive's sha256, and the sha256 of `ffmpeg`, of `ffprobe` and of the licence
text inside it. `scripts/fetch-ffmpeg.sh` verifies all of them at fetch time
and refuses rather than warns.

The archive hash alone would not be enough. The rest exist for reasons worth
stating:

- **Per-binary hashes** mean the thing that lands in `dist/` is pinned, not
  merely the container it arrived in.
- **The licence text's hash** is the guard against the one failure that would
  otherwise be silent: an upstream `-lgpl` asset repointed at a GPL build. The
  binaries would change, so their hashes would fail too - but this one says
  *why*, in the error message, which is the difference between a release
  engineer bumping a hash and a release engineer stopping to read.
- **`configure_requires` / `configure_forbids`**, added by TOR-93, are the
  same guard applied to the artefact instead of to a file beside it. FFmpeg
  bakes its whole configure line into every executable - it is what
  `ffmpeg -version` prints - so grepping the bytes answers "is this the build
  the lock claims" without running anything. Each stanza names the flags that
  must be there and the flags that must not: `--enable-version3` everywhere,
  `--enable-gpl` required on macOS and forbidden on Linux and Windows,
  `--enable-nonfree` forbidden on all four, because a nonfree build may not be
  redistributed by anybody at all.

The third guard exists because the first two cannot cover macOS and because
neither covers the likeliest human failure. martin-riedl's zips hold one
executable and nothing else - there is no licence text inside to checksum - so
without it the macOS stanzas would have no licence check at all. And the hash
guards only fire while the hashes are stale: an engineer who sees a mismatch,
concludes "upstream rebuilt", and bumps the three numbers has just disarmed
them. Measured, with the hashes bumped to match a GPL binary standing in for
the Linux `-lgpl` one:

```
fetch-ffmpeg: linux-amd64: ffmpeg was configured with --enable-gpl
The lock says this is a LGPL-3.0-or-later build and forbids: --enable-gpl --enable-nonfree
```

That check was shown able to tell the two apart before it was trusted:
`--enable-gpl` is present in the macOS builds' bytes and absent from BtbN's
`-lgpl` ones, on both Linux and Windows. No `--enable-nonfree` build was on
hand to demonstrate that arm firing, so that flag rests on the same string
search rather than on its own observation.

The checksums published by the builder are useful for catching a corrupted
download, but they are published by the same party as the binaries, so they
are not an independent attestation. The trust anchor is that the build is
produced from a public build definition, and that the hash of the artefact we
reviewed is now pinned in our own repository - a later substitution upstream
cannot pass unnoticed.

The lock also carries `license` and `license_file` per platform, and
`scripts/package.sh` reads both rather than assuming an answer: every sentence
the generated `README.txt` and `THIRD-PARTY-NOTICES.md` say about the licence
is branched on them, and a `license` value the script has no wording for stops
the release instead of falling back to the older LGPL text. A notices file
that misnames what it ships is worse than no notices file.

### Chosen source: BtbN/FFmpeg-Builds

For Linux and Windows the bundled binaries come from
[BtbN/FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds), which is the
source the FFmpeg project's own download page points at for Windows and Linux.
It builds on public GitHub Actions runners from a public, per-library build
definition, and - unusually among ffmpeg builders - it publishes an explicit
**`-lgpl`** variant next to the GPL one rather than only the GPL one. Both are
static, so an archive needs exactly two extra files and no shared-library
search path.

## The macOS gap, and the search that found nothing LGPL

**There is no prebuilt LGPL ffmpeg/ffprobe for macOS from a source worth
trusting.** That was TOR-26's finding and it still stands - TOR-93 did not
find one either; it took the third option below instead, and the next section
records that. What follows is the survey, kept because the answer to "why not
just use an LGPL macOS build like the other two platforms" is that there is
none, and that has to stay readable.

When TOR-26 closed, both macOS stanzas in `third_party/ffmpeg.lock` were
`status = blocked`, `make archives` built the other two platforms and exited
non-zero naming the ones it skipped, and no macOS archive was produced at all.
Shipping GPL on macOS only, or building ffmpeg from source, were left as
decisions for a person. TOR-93 is that person's answer.

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

So the choice was between three things, none of which was TOR-26's to make.
**None of the three was taken.** What was taken is the fourth possibility this
section's opening paragraph named and this list did not - shipping GPL on
macOS only - and the next section says why. The three are kept because each
remains a live alternative if the GPL decision is ever reversed:

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

Whichever is chosen, macOS needs signing thought that the other two platforms
do not: an unsigned, unnotarised Mach-O from a downloaded archive is
quarantined by Gatekeeper. REQUIREMENTS section 5 puts signing and notarisation
out of scope for v1, which is a decision about torpeek's own binary that will
apply to the bundled ffmpeg too. TOR-93 measured what that actually costs -
see "Gatekeeper" below; the short version is that the bundled ffmpeg turned
out to be signed and torpeek's own binary is the one that gets stopped.

## macOS: a GPL build, deliberately

**The macOS archives bundle a GPL ffmpeg, and the macOS archive as a whole is
distributed under the GNU GPL v3 or later. Linux and Windows keep their LGPL
build and are not under the GPL.** (TOR-93.)

### What changed, and what did not

The objection that made a GPL build unattractive was never about the GPL. It
was that bundling one "would force a decision about torpeek's own licence that
nobody has made" - see "The decision (Linux and Windows)" above. **TOR-92 made
that decision: torpeek is MIT.** MIT is GPL-compatible in the direction that
matters, so MIT-licensed source may travel inside a GPL-licensed whole without
its own repository changing terms. The objection is spent, and with it the
reason macOS had no archive at all.

What did *not* change is the LGPL choice for Linux and Windows, and that is
also a decision rather than laziness. Mixed terms across one release's
archives is deliberate:

- **There is nothing to buy by downgrading them.** Moving Linux and Windows to
  GPL would take away rights their recipients have today - the LGPL's
  permission to combine those binaries into a differently licensed whole -
  and would gain torpeek nothing at all, because nothing here needs it.
- **The notices are already generated per platform.** `scripts/package.sh`
  reads `license` and `license_file` out of the lock, so one uniform sentence
  was never the thing keeping the machinery simple.
- **macOS is the only platform where the alternative was "no archive".** The
  asymmetry exists because the availability of builds is asymmetric, not
  because a preference is.

### The source: ffmpeg.martin-riedl.de

The build is Martin Riedl's, from the survey table above, and it was chosen
over the other GPL macOS options for reasons that are all verifiable rather
than reputational:

- **Both architectures.** `macos/amd64` and `macos/arm64` from one builder,
  which was otherwise the hard part - evermeet.cx, `imageio-ffmpeg` and the
  repackagers of them are Intel-only.
- **`--enable-gpl`, and emphatically not `--enable-nonfree`.** Read off the
  shipped binaries' own configure line, not off the website:

  ```
  configuration: --prefix=/Volumes/ffmpeg_amd64/out --pkg-config-flags=--static
  --extra-version='https://www.martin-riedl.de' --enable-gray --enable-libxml2
  --enable-version3 --enable-gpl --enable-openssl ... --enable-libx264
  --enable-libx265 --enable-libmp3lame --enable-libopus --enable-libvorbis
  --enable-libtheora
  ```

  `--enable-version3` with `--enable-gpl` is what makes it **GPL v3** rather
  than v2, and therefore `COPYING.GPLv3` rather than `COPYING.GPLv2` that has
  to travel. The arm64 build's line is identical but for `--prefix` and the
  clang build number; it was read out of the Mach-O's bytes, since an Intel
  machine cannot run it.

  **evermeet.cx stays rejected whatever else is decided, and not as a licence
  preference:** its builds are `--enable-nonfree`, and a nonfree build may not
  be redistributed at all, by anyone, ever. That is a distribution
  prohibition, not a copyleft obligation, and no decision about torpeek's own
  terms can make it go away.
- **Both tools.** `ffprobe` is published beside `ffmpeg`, which torpeek needs
  and `imageio-ffmpeg`, for instance, does not provide. It arrives in its own
  zip, which is why the lock grew `ffprobe_url` and
  `ffprobe_archive_sha256`.
- **Immutable URLs exist behind the moving ones.** The site advertises
  `/redirect/latest/macos/<arch>/release/ffmpeg.zip` for scripts, and a
  "latest" redirect pins nothing. Behind it is a dated, immutable build
  directory - `/download/macos/amd64/1787081194_9.0.1/ffmpeg.zip` - and that
  is what the lock records, the way the BtbN entries pin
  `autobuild-2026-08-31-13-27`. The two arches are separate builds made hours
  apart, so their directory ids differ and only `ffmpeg_commit` is shared.
- **Self-contained.** `otool -L` on all four shipped executables lists only
  macOS system frameworks and `/usr/lib` system dylibs -
  `libSystem.B.dylib`, `libbz2.1.0.dylib`, `libiconv.2.dylib`,
  `libc++.1.dylib`, `libobjc.A.dylib`, plus Foundation, AVFoundation,
  VideoToolbox and friends. Nothing third-party, no `@rpath`, no closure to
  ship. That is the property conda-forge's genuinely-LGPL build could not
  offer at any acceptable size, and it is what keeps section 4's "one folder"
  true.
- **Signed by a Developer ID.** `Developer ID Application: Martin Riedl
  (KU3N25YGLU)`, hardened runtime, timestamped. Upstream notarises the `.pkg`
  installers rather than the zips, but the signature alone turns out to be
  enough for a quarantined executable to start - see "Gatekeeper".
- **A public build definition, and no patches.** The build script is at
  `git.martin-riedl.de/ffmpeg/build-script`; the lock pins commit
  `f63b8aab8f5ce1a067da86ba69e34a36a7e217e5`, whose message is
  "chore: ffmpeg update (version 9.0.1)" and which was `main`'s tip on
  2026-08-17, the day before both 9.0.1 macOS builds were produced, with
  nothing landing on `main` in between. `script/build-ffmpeg.sh` downloads
  `https://ffmpeg.org/releases/ffmpeg-9.0.1.tar.bz2`, unpacks it and runs
  `./configure` - **no patch is applied to FFmpeg anywhere in that
  repository** (the only `patch` calls are against pkg-config, a build-time
  tool, on Windows). So the corresponding source is FFmpeg 9.0.1 unmodified.

The FFmpeg commit was verified rather than assumed. `n9.0.1` is an annotated
tag; dereferenced it is commit `bf1b838f2ab88b4f8fd83443325c782ea0e0f7fa`, and
the release tarball the build script downloads was unpacked next to the GitHub
archive of that commit and compared file by file: **10 396 files present in
both, and not one of them differs**. The tarball holds 10 397 files and the
git archive 10 422; the entire discrepancy is bookkeeping - 26 `.gitignore`
and `.gitattributes` files only git ships, and one `VERSION` file only the
release tarball ships, containing `9.0.1`.

The licence text was verified the same way TOR-26's was, from FFmpeg rather
than from the builder - the martin-riedl zips carry no licence file to take a
word from. `COPYING.GPLv3` at commit `bf1b838f2a` hashes to
`8ceb4b9e...b65b903`, which is byte-for-byte the `packaging/licenses/ffmpeg/
COPYING.GPLv3` already committed for TOR-26; `COPYING.LGPLv3` and `LICENSE.md`
match too, and all three are also identical at the Linux/Windows commit
`e47273f4d9`. **So `packaging/licenses/` needed nothing added.** All three
texts continue to travel in every archive: an LGPL v3 archive needs the GPL v3
because LGPL v3 incorporates it by reference, and a GPL v3 archive needs the
LGPL v3 because the parts of FFmpeg that are not GPL-only stay LGPL and their
recipients keep LGPL rights in them. Which one governs the archive as a whole
is stated in the notices, not implied by which files are present.

### What GPL distribution obliges, on macOS only

| obligation | discharged by |
|---|---|
| convey the whole archive under the GPL, and say so | `README.txt` and `THIRD-PARTY-NOTICES.md`, both generated from the lock's `license`; the macOS wording says the archive as a whole is GPL v3-or-later and what the holder may do with it |
| ship the licence text | `licenses/ffmpeg/COPYING.GPLv3`, plus `COPYING.LGPLv3` and FFmpeg's `LICENSE.md` |
| make the complete corresponding source available | the written offer in `THIRD-PARTY-NOTICES.md`, naming the FFmpeg commit, the build definition commit, and - added for the GPL platforms - any patches the builder applies and the scripts controlling compilation and installation |
| keep the binaries unmodified, or say what changed | redistributed byte for byte; `scripts/fetch-ffmpeg.sh` verifies each one's sha256 and its configure line against `third_party/ffmpeg.lock` |
| not misrepresent the terms | a `license` value `package.sh` has no wording for is a hard failure, so the LGPL paragraphs cannot be emitted onto a GPL archive by omission |

torpeek's own position is unchanged and worth stating precisely, because two
true things sit next to each other. torpeek never links FFmpeg: it runs
`ffmpeg` and `ffprobe` as child processes over their command line and stdout
(`internal/ffmpeg`), so **torpeek's source stays MIT and its repository is
unaffected**. Bundling the executables in one folder is aggregation. And
separately, **torpeek chooses to convey the macOS archive as a whole under the
GPL** - MIT permits that, and it means nobody holding the folder has to work
out for themselves where the aggregate ends. Redistribute the folder under the
GPL; take the source from the repository under MIT.

### Run, on darwin/amd64

The machine this was built on is an Intel Mac (i7-1068NG7, x86_64, macOS
26.6.2), so the amd64 archive could be exercised the way the Linux one was.
The `torpeek-0.9.0-darwin-amd64.tar.gz` that `make archives` produced was
unpacked into an empty directory and run against a loopback seeder with an
empty directory as its entire `PATH`:

| what | result |
|---|---|
| a two-file torrent (H.264 `.mkv`, Theora `.ogv`), `-n 4` | 8 frames, 2 contact sheets, 2 manifests, in 6.1 s |
| what the manifests named | `h264` and `theora`, at 640x360 and 704x300 |
| the same run with `ffmpeg` and `ffprobe` deleted from the folder | fails in 29 ms with `ffmpeg: executable not found`, having searched the folder, `third_party/ffmpeg` and `PATH` |
| decoders `h264`, `hevc`, `vp9`, `mpeg4`, `theora` | one clip of each decoded to both a jpeg and a png, with the shipped `ffmpeg` and `ffprobe` |
| encoders `mjpeg`, `png` | those same frames - `mjpeg` and `png` are what wrote them |
| **the GPL encoders being present** | `-c:v libx264` and `-c:v libx265` both **succeed**, where the same commands against a `--disable-gpl` build of the same FFmpeg 9.0.1 on the same machine fail with `Unknown encoder`, and `-c:v mjpeg` succeeds on both |

That last row is the point of it. It is the LGPL check from the Linux section
run backwards: a build that merely *claimed* to be GPL would fail it, so it is
positive evidence that what is bundled is the GPL build the lock pins and not
something mislabelled. The LGPL arm is conda-forge's `osx-64`
`ffmpeg-*-lgpl_*`, the one genuinely-LGPL macOS build the survey found -
`--disable-gpl --enable-version3`, same FFmpeg 9.0.1, same architecture, so the
two arms differ in the flag under test and little else.

**darwin/arm64 was not run, and cannot be from here.** An Intel Mac cannot
execute an arm64 Mach-O; Rosetta translates the other direction. Everything
claimed about that archive is structural or read from the binary's bytes: the
archive assembles, the hashes match, the configure line carries
`--enable-gpl --enable-version3` and no `--enable-nonfree`, and `otool -L`
shows the same system-only dylib list. Whether it *runs* is untested.

### Gatekeeper

REQUIREMENTS section 5 puts signing and notarisation out of scope for v1, and
TOR-26 flagged the consequence without measuring it. Measured now, on macOS
26.6.2:

- A browser marks a downloaded `.tar.gz` with `com.apple.quarantine`, and
  **`tar -xzf` propagates that attribute onto every extracted file** - so
  unpacking in Terminal does not shed it.
- The bundled `ffmpeg` and `ffprobe` **start anyway**, quarantined and on
  their first execution. The Developer ID signature is enough; notarising only
  the `.pkg` did not matter here.
- **torpeek's own binary does not.** It carries no Developer ID (the amd64
  build is unsigned; the arm64 one is ad-hoc linker-signed, which is not the
  same thing), so its first execution under quarantine is stopped: the process
  sits at 0% CPU with an 8 KB resident set and prints nothing.
- Clearing the flag **before** the first attempt works (`xattr -c torpeek`, or
  clearing it on the tarball before extracting), and a fresh copy of the same
  bytes runs. Clearing it **afterwards** is the case that cannot be relied on:
  TOR-93 found the refusal remembered per file, TOR-97 did not - see below.

So the Gatekeeper problem the macOS archives now have is torpeek's own signing
gap, not the bundled ffmpeg's, and it is the same gap the Linux and Windows
archives are spared only because their platforms have no equivalent. It is not
a licensing question; TOR-93 recorded it here because "macOS archives exist
now" is what made it reachable, and TOR-97 then did what can be done about it
without buying a Developer ID.

**Where the stop happens, and what that rules out (TOR-97).** The question that
decides the remedy is whether any of torpeek's own code runs before the stop,
because if none does, no message the program contains can ever be read.
Measured on the same machine, darwin/amd64, with
`xattr -w com.apple.quarantine "0081;00000000;Safari;"` standing in for the
download - a fresh copy of identical bytes in every arm, since the decision is
remembered per file:

| arm | what came out |
|---|---|
| clean copy, `./torpeek -version` | `1.0.0`, exit 0, after 1.6 s |
| quarantined copy, same command | nothing at all; SIGKILL after 1.6 s |
| quarantined copy of a build whose **first statement in `main`** writes `PROBE: reached Go main` to stderr | nothing at all; SIGKILL after 8.5 s |
| clean copy of that same probe build | `PROBE: reached Go main`, then `1.0.0` |
| quarantined 2 MB hello-world Go binary | nothing at all; SIGKILL after 3.8 s |

**The process never reaches `main`.** The kernel says so itself - one
`(AppleSystemPolicy) ASP: Security policy would not allow process: <pid>,
<path>` in the unified log per stopped launch - and `ps` shows the process
parked at 0% CPU with an 8 KB resident set for the whole of its short life,
which is a blocked `execve` rather than a program that started and stalled.
That same signature also appears *transiently* on a clean unsigned binary's
first run (1.6 s, against 0.5 s on its second, while XProtect looks at the new
file), so the timing is not the evidence. The printing is: identical bytes,
one copy marked and one not, and only the unmarked one speaks.

That rules out the tidy remedy. A startup check inside torpeek that read
`com.apple.quarantine` on its own executable could only ever be read by
somebody whose run was **not** stopped, so `cmd/torpeek/main.go` carries a note
saying why the check is absent instead of carrying the check. What can run is
something that runs *before* torpeek, and that is what the macOS archives now
ship: `packaging/macos/first-run.command` clears the flag from the folder,
checks that it is really gone, restores the execute bit and only then launches
torpeek - one action, in the one order that works. The generated `README.txt`
leads with it, in a section above the commands it has to precede rather than a
note below them.

Two things came out differently from TOR-93, and are recorded as measured
rather than reconciled. First, **the stop did not wait**: the held process was
killed by the system after 1.6-11.7 s in all seven quarantined launches, so in
a terminal the run ends rather than hanging. Dialogs did appear - the person
logged in at this machine watched them stack up and asked what was trying to
open - but nothing in the terminal showed one, and the kill did not wait for
an answer. Second, clearing the flag **after** a stopped attempt did let that
same file run, where TOR-93 found the refusal remembered per file. Both
probably turn on what the launching context is allowed to prompt: TOR-93's runs
came from an interactive Terminal, TOR-97's from an agent session under the
same Aqua login. The archive's wording is therefore that clearing afterwards
*may* not help and that unpacking again does, which is true under both
measurements, and the script prints that same advice itself if torpeek dies of
SIGKILL after a clean clear.

**darwin/arm64 remains untested and cannot be tested from an Intel Mac.** Its
binary is ad-hoc linker-signed rather than unsigned, and the kernel's rule
there is stricter - it refuses an arm64 Mach-O with no valid signature outright
- so a stop is at least as likely; whether it looks the same is unknown.
`first-run.command` travels in both macOS archives because the remedy does not
depend on the answer.

## Size

Section 4 estimates ~80 MB per platform. The measured figure for the chosen
static builds is **two to three times that**, and the reason is worth
recording rather than discovering again later.

Measured on the 0.9.0 archives this machinery actually produced:

| platform | ffmpeg | ffprobe | torpeek | unpacked | compressed |
|---|---|---|---|---|---|
| linux-amd64 | 110.66 MiB | 110.46 MiB | 30.69 MiB | 251.88 MiB | 107.75 MiB (`.tar.gz`) |
| windows-amd64 | 109.10 MiB | 108.91 MiB | 31.07 MiB | 249.13 MiB | 106.61 MiB (`.zip`) |
| darwin-amd64 | 90.29 MiB | 90.10 MiB | 31.42 MiB | 211.86 MiB | 80.53 MiB (`.tar.gz`) |
| darwin-arm64 | 63.26 MiB | 63.09 MiB | 29.48 MiB | 155.89 MiB | 69.09 MiB (`.tar.gz`) |

The macOS rows are smaller for the same reason the archive is GPL: a different
builder. Counted off the two configure lines, martin-riedl passes 31
`--enable-*` flags where BtbN passes 63, so the duplicated payload is smaller
to begin with - and note that the *GPL* build is the smaller one here, which is
a fact about these two builders and not about the licences.

Two causes, in order of size. First, a static `ffmpeg` and a static `ffprobe`
each embed their own copy of the same library code - ~110 MB of it on Linux and
Windows, ~90 MB on macOS Intel, ~63 MB on Apple Silicon - so half of every
archive is a duplicate. Second, BtbN configures in everything they can
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

## Settled since

- **torpeek is MIT** (`LICENSE`, TOR-92). This document had it listed as open,
  and it was the one blocker that had nothing to do with ffmpeg: a release of
  an unlicensed program gives its recipients no rights at all, whatever is
  bundled with it. Every module in the CGO-free build is MIT/BSD/ISC/Apache-2.0/
  MPL-2.0, so nothing underneath constrained the choice.

  It does **not** decide what the archive as a whole travels under. That is a
  separate axis, and the LGPL decision above is what keeps it separate: an
  LGPL ffmpeg invoked as its own process leaves torpeek's own terms to
  torpeek. Were a GPL build bundled instead, the archive would have to be
  distributed under the GPL - which MIT permits, since MIT is GPL-compatible
  in that direction, but which is a decision about the archive, not about the
  source.

- **The macOS archive travels under the GPL; Linux and Windows do not**
  (TOR-93). That is the decision the paragraph above said still had to be
  taken, taken - for one platform, because macOS is the only one where the
  alternative was shipping nothing. Both macOS stanzas in the lock are
  `status = ok`, `make archives` produces all four archives and exits zero,
  and the generated notices name a different licence on macOS than on the
  other two. See "macOS: a GPL build, deliberately" for the source, the
  evidence it is GPL and not nonfree, and what GPL obliges.

  What is no longer open, as a consequence: **no platform is blocked**. The
  final gate in `scripts/package.sh` is untouched - it still exits non-zero
  and names what it skipped - it simply has nothing to skip. If a fifth
  platform is ever added without a build, it fails exactly as macOS used to.

## Still open

These are named because leaving them unnamed is how they get missed, not
because TOR-26 or TOR-93 was meant to settle them.

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
  them. `third_party/ffmpeg.lock` records the URL to fetch it from. This
  matters more now than it did: on macOS the archive itself is under the GPL,
  so the offer is not a courtesy attached to a bundled library but the terms
  the whole thing is conveyed under. Two commits have to be mirrored, not one -
  `e47273f4d9` for Linux and Windows, `bf1b838f2a` for macOS - and the build
  definitions sit on two hosts, one of them a small self-hosted Gitea.
- **One library in the macOS build is not pinned by its build definition.**
  martin-riedl's build script pins every dependency by a file under
  `version/` - x265 4.2, aom 3.14.1, dav1d 1.5.4 and so on - except **x264**,
  which `script/build-x264.sh` fetches from
  `code.videolan.org/videolan/x264/-/archive/master/x264-master.tar.gz`. The
  published `versions.txt` records what that produced (`x264 0.165.x`), but
  "master, on 2026-08-18" is not a revision anybody can fetch back. torpeek
  never encodes H.264, yet the binary it redistributes contains x264 object
  code, so x264's source is part of the corresponding source the offer covers.
  Mirroring at release time does **not** fix this retroactively - the exact
  tree that build compiled is not addressable any more, and fetching
  `x264-master.tar.gz` today would get something else. Closing it properly
  means one of: asking upstream to pin x264 the way it pins x265, taking the
  Linux/Windows route of a builder who pins everything, or building our own.
  Flagged rather than solved because it is upstream's build definition, not
  ours - and named here so that "the written offer is honoured" is not read as
  covering more than it does.
- **A `-lgpl-shared` or self-built ffmpeg** would change the archive layout,
  and `ffmpeg.Locate`'s second search directory (`third_party/ffmpeg` next to
  the binary) already exists for exactly that; nothing in Go needs to change
  to take either option.
