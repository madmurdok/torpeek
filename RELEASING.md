# Releasing

Eight releases went out before this file existed and not one of them was
tagged, because the ritual lived only in whoever was doing it. It lives here
now. The order is not arbitrary - each step's position is the lesson from a
release that got it wrong.

## The ritual

1. **Cut the branch, bump the version in the same breath.**
   `git checkout -b release-<v> main`, then the first commit on it sets
   `internal/version.Version` to `<v>`. The manifest naming the version has
   to name the release being built, or a binary, a result file and a bug
   report all claim a version nobody shipped. (g-mesh-bench sat at `0.1.0`
   through nineteen releases for want of this.)

2. **One branch per task, off the release branch.**
   `<type>/<PREFIX>-<number>-<short-title>`, e.g.
   `fix/TOR-78-a-delete-that-happened`. Merged back into the release branch
   as each task finishes - that merge needs nobody's permission. The merge
   into `main` does.

3. **`make check` before anything is called done.**
   It exports `CGO_ENABLED=0`, and that is not a cross-compilation
   convenience: with cgo off the piece-completion store is bbolt rather than
   sqlite, and those behave differently under contention. The shipped build
   is the cgo-off one, so it is the one to test. A bare `go test` on a
   developer machine is a different program in that respect (TOR-59).

4. **`make acceptance` BEFORE the next release branch bumps the version.**
   The harness reads the version out of the binary and stamps it into both
   the report's filename and its `Tool` field, so this is the only moment it
   can honestly say `<v>`. 0.7.0 shipped without a report and the run had to
   be done from a branch off `main` a release later to avoid signing 0.7.0's
   measurements with 0.8.0's number (TOR-76).
   Criterion 2's verdict is taken on what the run ORDERED, not on what
   arrived (REQUIREMENTS.md 8.2, TOR-94), and the report carries three
   numbers: ordered, arrived, and the gap. The first is deterministic and is
   the one worth comparing release to release. The second samples this
   swarm's mood at this moment - it has read 45.5 to 118.6 MiB on unchanged
   code - so a high figure there is a thing to look into, not a release to
   block. A gap that grows release over release is the interesting signal.
   Give the machine a quiet half-hour before starting: the arrival figure
   doubled after about 25 back-to-back runs and needed well over 20 minutes
   of idle to come back, so a release run should be a cold run (TOR-88).

5. **Merge into `main` - after asking.** Then `make tag`, then push the
   branch, `main` and the tag together. The tag goes on the release merge:
   that is the commit that produced the release, and an archive built later
   has something to be built from.

6. **Build the archives with `make archives`.**
   It runs `make cross` first, so the binaries in them are the CGO-free ones
   step 3 tested, then fetches the bundled ffmpeg named in
   `third_party/ffmpeg.lock` and verifies every checksum in it - the archive's,
   each binary's, and, where upstream ships one, the licence text's - as well
   as the configure line each binary carries, against the flags its stanza
   requires and forbids. All of that before assembling anything. It writes
   `dist/torpeek-<v>-<platform>{,.tar.gz,.zip}` and
   `dist/torpeek-<v>-SHA256SUMS.txt`.

   **It should exit zero and write four archives.** Since TOR-93 no platform
   in the lock is blocked - Linux and Windows carry an LGPL ffmpeg, macOS a
   GPL one - so there is nothing left to skip. The gate is still there: if it
   exits non-zero, read the `SKIP` lines and
   [docs/licensing.md](docs/licensing.md) rather than working around them, a
   three-of-four release must not be able to pass for a whole one.

7. **Run the archives on the operating systems they are for.**
   `gh workflow run archives.yml --ref release-<v>`, then
   `gh run watch <id>`. Four jobs have to go green, one per archive: each
   downloads the archive the same workflow built, unpacks it the way a person
   on that OS would, and runs torpeek out of the unpacked folder against a
   seeder on loopback with `PATH` pointing at an empty directory - then does
   it again with the bundled ffmpeg deleted and requires that pass to fail.

   This step is here because 0.9.0 published two archives nobody had ever
   run - windows-amd64 and darwin-arm64 - and said so in its notes. It was
   not carelessness: this machine is an Intel Mac and can execute neither a
   PE binary nor an arm64 Mach-O, so "build it and check the checksum" was
   the whole of what step 6 could honestly claim (TOR-101).

   The workflow also fires by itself on a push that touches the packaging
   scripts, `third_party/ffmpeg.lock`, `internal/ffmpeg`, `archivecheck/` or
   `internal/version` - which is to say on step 1's version bump - so the
   dispatch here is usually a re-confirmation rather than the first run. Read
   the four logs rather than the four ticks: each one prints the version
   line of the ffmpeg it actually executed and the count of frames, contact
   sheets and manifests that came out. `make archive-check` is the same pair
   of arms by hand, against an archive already unpacked.

8. **Publish, if this release is for anybody else.** A GitHub release from
   the tag, with the per-platform archives and their checksums, and notes a
   person who has never read the tracker can act on - not a paste of the
   generated ones.

   Two things go up alongside the archives, both because of what they bundle:
   `dist/torpeek-<v>-SHA256SUMS.txt`, and the FFmpeg source tarballs for the
   commits pinned in `third_party/ffmpeg.lock` - **two of them now**, since
   Linux/Windows and macOS are built from different FFmpeg commits. The second
   is the cheap way to honour the written source offer that each archive
   carries - GPL v3 section 6(d) is satisfied by serving the source from the
   same place as the binary, and an offer that points only at somebody else's
   repository depends on that repository still being there. It matters most for
   the macOS archives, which are conveyed under the GPL as a whole rather than
   merely containing an LGPL library, and whose build definition lives on a
   small self-hosted Gitea.

   **Three things for a release with macOS archives in it**, because their
   offer names x264's source separately - it is the one library upstream's
   build definition does not pin, so the revision lives in our lock (TOR-98,
   [docs/licensing.md](docs/licensing.md)). Step 6 has already downloaded and
   verified it: it is `third_party/ffmpeg/.cache/darwin-amd64/x264-<rev>.tar.gz`
   and the identical file under `darwin-arm64`, and `x264_source_sha256` in the
   lock is its checksum. Upload one copy. Of the three, this is the one most
   worth serving ourselves: FFmpeg's tarball can always be re-fetched by tag
   from ffmpeg.org, whereas the name the macOS build actually used - x264's
   `master` - stops denoting this tree the moment x264 commits again.

9. **`release_it` in the tracker, last.** It refuses unless every task in
   the release is done or cancelled, runs a test command through the same
   verifier task completion uses, and generates the notes from the tasks' own
   summaries.

   **It is last because it cannot be earlier.** It was step 5 for one
   release, and 0.9.0 discovered why that cannot work: the publish is itself a
   task in the release, it needs the tag, and the tag goes on the merge - so a
   release_it that demands every task be done waits on a step that waits on it
   (TOR-99). Nothing about the order is a preference.

   What that costs, stated rather than glossed: release_it's test command no
   longer runs before anything irreversible. So it is not what guards the
   publish, and it was never really the guard - **steps 3 and 4 are**, plus
   `make tag` refusing a dirty tree and deriving the version from the code.
   Read those as the gate. release_it is the record: it marks the release
   shipped and writes down what was in it.

10. **Sweep the branches the release left behind.**
    `make prune-worktrees` then `make prune-branches`, once the release is in
    `main` - only then are its task branches merged from `main`'s point of
    view, which is what those targets judge against. Skipped for eight
    releases running, this is how fifty-one dead branches came to hide the
    three that were live (TOR-89). `release-*` is kept: `origin` carries one
    per shipped release, and a release branch's tip is the only independent
    witness a tag could be checked against, which matters because
    `v0.1.0`..`v0.6.0` were backfilled long after the fact.

## Things that bite

- **A release that publishes itself cannot gate on itself.** `release_it`
  refuses a release with an unfinished task, and the publish is a task; the
  publish needs a tag, the tag needs the merge. Whatever else moves, those
  three cannot be ordered so that release_it comes first - see step 9.
- **`git branch --merged main | grep 'Merge release'` is not a tag list.**
  A merge of a release branch *into a task branch* reads almost the same
  ("Merge release-0.7.0: ...") and is not a release. Read the messages.
- **The version in code is the source of truth for the tag.** `make tag`
  derives it rather than taking an argument, so the two cannot disagree.
- **`release_it` is one-shot.** It refuses a release already released, so
  the tracker will not let step 9 happen twice.
