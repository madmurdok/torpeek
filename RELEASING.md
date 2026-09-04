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
   Read criterion 2's traffic against the previous releases' figures, not
   only against its ceiling - it has been climbing.

5. **`release_it` in the tracker.** It refuses unless every task in the
   release is done or cancelled, runs a test command through the same
   verifier task completion uses, and generates the notes from the tasks'
   own summaries.

6. **Merge into `main` - after asking.** Then `make tag`, then push the
   branch, `main` and the tag together. The tag goes on the release merge:
   that is the commit that produced the release, and an archive built later
   has something to be built from.

7. **Build the archives with `make archives`.**
   It runs `make cross` first, so the binaries in them are the CGO-free ones
   step 3 tested, then fetches the bundled ffmpeg named in
   `third_party/ffmpeg.lock` and verifies every checksum in it - the archive's,
   each binary's, and the licence text's - before assembling anything. It
   writes `dist/torpeek-<v>-<platform>{,.tar.gz,.zip}` and
   `dist/torpeek-<v>-SHA256SUMS.txt`.

   **It exits non-zero today, and that is correct**: both macOS platforms are
   `status = blocked` for want of an LGPL ffmpeg, so two archives out of four
   are not produced. Read the skip lines and
   [docs/licensing.md](docs/licensing.md) rather than working around them - a
   three-of-four release must not be able to pass for a whole one.

8. **Publish, if this release is for anybody else.** A GitHub release from
   the tag, with the per-platform archives and their checksums, and notes a
   person who has never read the tracker can act on - not a paste of the
   generated ones.

   Two things go up alongside the archives, both because of what they bundle:
   `dist/torpeek-<v>-SHA256SUMS.txt`, and the FFmpeg source tarball for the
   commit pinned in `third_party/ffmpeg.lock`. The second is the cheap way to
   honour the written source offer that each archive carries - GPL v3 section
   6(d) is satisfied by serving the source from the same place as the binary,
   and an offer that points only at somebody else's repository depends on that
   repository still being there.

## Things that bite

- **`git branch --merged main | grep 'Merge release'` is not a tag list.**
  A merge of a release branch *into a task branch* reads almost the same
  ("Merge release-0.7.0: ...") and is not a release. Read the messages.
- **The version in code is the source of truth for the tag.** `make tag`
  derives it rather than taking an argument, so the two cannot disagree.
- **`release_it` is one-shot.** It refuses a release already released, so
  the tracker will not let step 5 happen twice.
