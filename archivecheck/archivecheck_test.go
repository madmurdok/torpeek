//go:build archivecheck

// Package archivecheck runs an unpacked release archive on the operating
// system that archive was built for, the way TOR-26's acceptance criterion
// asks: out of the folder a person unpacked, against a seeder on loopback,
// with PATH pointing at an empty directory - so the ffmpeg and ffprobe inside
// the archive are the only ones anything could possibly have used.
//
// Why this exists at all. torpeek 0.9.0 published four archives and two of
// them - windows-amd64 and darwin-arm64 - had never been executed by anybody,
// and the release notes said so in those words, because the machine this
// project is developed on is an Intel Mac: it can run neither a PE binary nor
// an arm64 Mach-O. A CI job that merely assembled the archives would have
// proved exactly what was already known. So nothing here asserts that
// something compiled; it asserts that frames came out on that OS, and that
// they came out of the decoder the archive carries (TOR-101).
//
// Two arms, both in this file on purpose. A green run that would also be
// green with the bundled binaries missing proves nothing, so
// TestArchiveWithoutItsOwnFFmpegFails deletes them from a copy of the same
// unpacked folder and requires the identical run to fail with
// ffmpeg.ErrNotFound. That pair is the cheap observation showing this check
// can tell the two states apart; running only the first arm is not a weaker
// version of this check, it is not this check.
//
// It sits behind a build tag for the reason acceptance/ does: it needs a
// ~100 MB unpacked archive handed to it and takes minutes, so it has no
// business in `go test ./...`.
//
// Run with:
//
//	make archive-check ARCHIVE=dist/torpeek-1.0.0-darwin-amd64 MEDIA=clip.mkv
//
// or, as .github/workflows/archives.yml does it, `go test` directly - Windows
// runners have no make.
package archivecheck

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
	"github.com/madmurdok/torpeek/internal/torrenttest"
	"github.com/madmurdok/torpeek/internal/version"
)

var (
	archive = flag.String("archive", "",
		"the unpacked release archive to run: the folder holding torpeek, ffmpeg and ffprobe")
	count = flag.Int("n", 4, "frames per video file to ask the archive for")
	limit = flag.Duration("run-timeout", 10*time.Minute, "ceiling for one torpeek run")
	media mediaFiles
)

// mediaFiles is a repeatable -media flag. More than one file is not padding:
// a per-file manifest and a per-file contact sheet are only really asserted
// by a torrent that holds two files, and -parallel's default is 4.
type mediaFiles []string

func (m *mediaFiles) String() string     { return strings.Join(*m, ", ") }
func (m *mediaFiles) Set(v string) error { *m = append(*m, v); return nil }

func init() {
	flag.Var(&media, "media", "a media file to put in the seeded torrent (repeatable)")
}

// TestArchiveRunsWithNothingButItsOwnFFmpeg is the criterion: an unpacked
// archive, a torrent on loopback, an empty PATH, and frames on disk at the
// end of it.
func TestArchiveRunsWithNothingButItsOwnFFmpeg(t *testing.T) {
	dir, exe := requireArchive(t)

	// Before asking torpeek to use them, establish that the two bundled
	// executables run on this machine at all. On darwin-arm64 that is the
	// open question and not a formality: an arm64 Mach-O with no valid code
	// signature is killed by the kernel rather than refused by the loader,
	// and the copies in the macOS archives are somebody else's build. The
	// version line also lands in the log, which is how a run says which
	// decoder produced its frames.
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		path := filepath.Join(dir, exeName(tool))
		out, err := exec.Command(path, "-hide_banner", "-version").CombinedOutput()
		if err != nil {
			t.Fatalf("the bundled %s does not run on %s/%s: %v\n%s",
				exeName(tool), runtime.GOOS, runtime.GOARCH, err, out)
		}
		t.Logf("bundled %s: %s", exeName(tool), firstLine(string(out)))
	}

	got := runArchive(t, exe)

	if got.exit != 0 {
		t.Fatalf("torpeek exited %d; it was supposed to produce frames", got.exit)
	}

	wantFrames := *count * len(media)
	if got.frames != wantFrames {
		t.Errorf("frames on disk = %d, want %d", got.frames, wantFrames)
	}
	if got.sheets != len(media) {
		t.Errorf("contact sheets = %d, want one per video file (%d)", got.sheets, len(media))
	}
	if got.manifests != len(media) {
		t.Errorf("manifests = %d, want one per video file (%d)", got.manifests, len(media))
	}

	// A manifest is read rather than counted, because "a file called
	// manifest.json exists" is a weaker claim than the one being made here:
	// the codec field is filled in by ffprobe and the frame records by
	// ffmpeg, so a manifest naming h264 and *count frames is the bundled
	// pair's own signature. Its Tool field is what ties the artefact to a
	// commit - an archive from some older dist/ directory would say so here.
	for _, m := range got.read {
		if m.Tool != version.Version {
			t.Errorf("manifest says tool %q, but this tree is %q - is -archive an old build?",
				m.Tool, version.Version)
		}
		if m.Video.Codec != "h264" {
			t.Errorf("manifest says codec %q, want h264 (the fixture is libx264)", m.Video.Codec)
		}
		if len(m.Frames) != *count {
			t.Errorf("manifest for %s records %d frames, want %d", m.File.Path, len(m.Frames), *count)
		}
		t.Logf("manifest %s: %s %dx%d, %d frames, %d bytes downloaded",
			m.File.Path, m.Video.Codec, m.Video.Width, m.Video.Height,
			len(m.Frames), m.Cost.DownloadedBytes)
	}

	t.Logf("RESULT: %d frames, %d contact sheets, %d manifests out of %s in %s",
		got.frames, got.sheets, got.manifests, filepath.Base(dir), got.elapsed.Round(time.Millisecond))
}

// TestArchiveWithoutItsOwnFFmpegFails is the control arm, and the reason the
// arm above is worth anything. It is the identical run - same fixture, same
// seeder, same empty PATH - against a copy of the archive with ffmpeg and
// ffprobe deleted, and it requires that run to fail with ffmpeg.ErrNotFound.
//
// It works on a copy rather than moving the real binaries aside so that the
// two arms are independent of each other and of their order: a control that
// has to put something back is a control that can leave the archive broken
// for whatever runs next.
func TestArchiveWithoutItsOwnFFmpegFails(t *testing.T) {
	dir, _ := requireArchive(t)

	stripped := copyWithoutFFmpeg(t, dir)
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := os.Stat(filepath.Join(stripped, exeName(tool))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s is still in the stripped copy (stat: %v); this arm would prove nothing",
				exeName(tool), err)
		}
	}

	got := runArchive(t, filepath.Join(stripped, exeName("torpeek")))

	if got.exit == 0 {
		t.Fatalf("torpeek succeeded with no ffmpeg anywhere - then the other arm's frames "+
			"did not come from the bundled copies, and neither arm means anything.\n"+
			"stdout:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, ffmpeg.ErrNotFound.Error()) {
		t.Fatalf("torpeek failed, but not with %q; it must fail for the reason this arm removed, "+
			"not for some other one", ffmpeg.ErrNotFound.Error())
	}
	if got.frames != 0 {
		t.Errorf("frames on disk = %d, want none", got.frames)
	}
	t.Logf("RESULT: failed as it had to, exit %d - the bundled ffmpeg is what the other arm used",
		got.exit)
}

// ---- the run -------------------------------------------------------------

type result struct {
	exit      int
	stdout    string
	stderr    string
	elapsed   time.Duration
	frames    int
	sheets    int
	manifests int
	read      []manifest.Manifest
}

// runArchive seeds the fixture on loopback and runs exe against it with
// nothing on PATH.
func runArchive(t *testing.T, exe string) result {
	t.Helper()

	// The torrent's name is its payload directory's own name, and the
	// seeder's DataDir is that directory's parent, so the payload gets a
	// parent of its own rather than sharing one with the run's scratch.
	payload := filepath.Join(t.TempDir(), "torpeek-archivecheck")
	if err := os.MkdirAll(payload, 0o755); err != nil {
		t.Fatalf("create payload directory: %v", err)
	}
	for _, m := range media {
		dst := filepath.Join(payload, filepath.Base(m))
		copyFile(t, m, dst)
		info, err := os.Stat(dst)
		if err != nil {
			t.Fatalf("stat %s: %v", dst, err)
		}
		t.Logf("payload: %s (%d bytes)", filepath.Base(m), info.Size())
	}

	fixture := torrenttest.BuildDir(t, payload, 256<<10)
	peer := fixture.StartSeeder(t)
	t.Logf("seeder: %s", peer)

	work := t.TempDir()
	out := filepath.Join(work, "out")
	pieces := filepath.Join(work, "pieces")
	emptyPath := filepath.Join(work, "empty-path")
	for _, d := range []string{out, pieces, emptyPath} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("create %s: %v", d, err)
		}
	}
	assertNothingFindable(t, emptyPath)

	args := []string{
		"-n", strconv.Itoa(*count),
		"-mode", "min-traffic",
		"-dht=false",
		"-peer", peer,
		"-out", out,
		"-data", pieces,
		fixture.TorrentPath,
	}
	t.Logf("run: %s %s", exe, strings.Join(args, " "))

	ctx, cancel := context.WithTimeout(context.Background(), *limit)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = emptyPathEnv(emptyPath, work)
	// A directory holding no executable, because Windows' own lookup rules
	// consult the working directory: Go turns such a hit into exec.ErrDot
	// rather than a path, which internal/ffmpeg reads as "not found" - but a
	// run whose PATH argument leans on that is one Go release away from
	// being wrong.
	cmd.Dir = work
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	got := result{
		exit:    exitCode(t, err),
		stdout:  stdout.String(),
		stderr:  stderr.String(),
		elapsed: time.Since(start),
	}

	// Whole, never a tail: the tail of a failed ffmpeg lookup is the part
	// that says which directories were searched, and the tail of a stalled
	// run is the part that says nothing.
	t.Logf("--- torpeek stdout ---\n%s", got.stdout)
	t.Logf("--- torpeek stderr ---\n%s", got.stderr)
	t.Logf("--- exit %d after %s ---", got.exit, got.elapsed.Round(time.Millisecond))

	got.frames, got.sheets, got.manifests, got.read = countOutput(t, out)
	return got
}

// emptyPathEnv is the environment the child gets: PATH is one empty directory
// and nothing else is inherited.
//
// Except on Windows, where a process with a blank environment fails for
// reasons that have nothing to do with ffmpeg - the net package needs
// SystemRoot to resolve anything and os.TempDir falls back to the Windows
// directory without TEMP - so the machine's own bookkeeping is passed through
// and PATH alone is emptied. That does not weaken what the empty PATH proves:
// Go's exec.LookPath reads PATH and PATHEXT and no other variable, so none of
// the names below can make an ffmpeg findable.
func emptyPathEnv(emptyPath, home string) []string {
	if runtime.GOOS != "windows" {
		return []string{"PATH=" + emptyPath, "HOME=" + home}
	}

	env := []string{"PATH=" + emptyPath}
	for _, name := range []string{
		"SystemRoot", "SystemDrive", "windir", "ComSpec", "PATHEXT",
		"TEMP", "TMP", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "ProgramData",
		"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
	} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// assertNothingFindable proves the negative instead of assuming it, with the
// same lookup internal/ffmpeg's own PATH fallback does and against the PATH
// the child is about to be given. The .exe spellings are in the list because
// on Windows they are the names that exist; asking only for "ffmpeg" there
// would be a check that cannot fail.
func assertNothingFindable(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s is meant to be empty, holds %d entries", dir, len(entries))
	}

	saved, had := os.LookupEnv("PATH")
	if err := os.Setenv("PATH", dir); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	defer func() {
		if had {
			os.Setenv("PATH", saved)
		} else {
			os.Unsetenv("PATH")
		}
	}()

	for _, tool := range []string{"ffmpeg", "ffprobe", "ffmpeg.exe", "ffprobe.exe"} {
		// A hit in the working directory comes back as exec.ErrDot rather
		// than a usable path, which is a miss for this purpose and a miss
		// for internal/ffmpeg too - both read a non-nil error as not found.
		if path, err := exec.LookPath(tool); err == nil {
			t.Fatalf("%s is reachable at %s with PATH=%s", tool, path, dir)
		}
	}
	t.Logf("PATH: %s (empty; neither ffmpeg nor ffprobe is findable on it)", dir)
}

// ---- what came out -------------------------------------------------------

// countOutput walks a run's output directory and counts what a person would
// look for. It reads every manifest it finds, so the caller can assert on
// what the run recorded rather than only on how many files it wrote.
func countOutput(t *testing.T, root string) (frames, sheets, manifests int, read []manifest.Manifest) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		switch name := entry.Name(); {
		case name == output.SheetName:
			sheets++
		case strings.HasSuffix(name, ".jpg"):
			info, err := entry.Info()
			if err != nil {
				return err
			}
			// A zero-byte frame is the failure mode a count cannot see: the
			// atomic writer creates the file before ffmpeg has written a
			// byte into it.
			if info.Size() == 0 {
				t.Errorf("%s is empty", path)
			}
			frames++
		case name == manifest.Name:
			manifests++
			m, err := readManifest(path)
			if err != nil {
				t.Errorf("read %s: %v", path, err)
				return nil
			}
			read = append(read, m)
		}
		return nil
	})
	// A run that produced nothing at all leaves the directory missing, which
	// is a count of zero and not an error to report.
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("walk %s: %v", root, err)
	}
	return frames, sheets, manifests, read
}

func readManifest(path string) (manifest.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest.Manifest{}, err
	}
	var m manifest.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest.Manifest{}, err
	}
	return m, nil
}

// ---- the archive ---------------------------------------------------------

// requireArchive resolves -archive and the torpeek inside it.
//
// It fails rather than skips when it has no archive to run. A skipped
// verification and a passing one are the same green tick in a CI summary, and
// the whole point of this package is that two platforms were once believed
// verified when nothing had run on them.
func requireArchive(t *testing.T) (dir, exe string) {
	t.Helper()

	if *archive == "" || len(media) == 0 {
		t.Fatal("archivecheck needs -archive <unpacked archive> and at least one -media <file>")
	}

	dir, err := filepath.Abs(*archive)
	if err != nil {
		t.Fatalf("resolve -archive: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("-archive %s is not a directory a release was unpacked into (stat: %v)", dir, err)
	}

	exe = filepath.Join(dir, exeName("torpeek"))
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("no %s in %s: %v", exeName("torpeek"), dir, err)
	}
	for _, m := range media {
		if _, err := os.Stat(m); err != nil {
			t.Fatalf("-media %s: %v", m, err)
		}
	}
	return dir, exe
}

// exeName is the .exe question, and on Windows it is half of what this
// package exists to exercise. internal/ffmpeg.executableNameFor has a unit
// test for the windows case precisely because on a mac or a Linux runner that
// branch is unreachable (TOR-26) - so until this ran on Windows the naming
// was pinned structurally and never once executed. Here it is exercised
// twice: by this line finding torpeek.exe, and inside the child process,
// which has to find ffmpeg.exe next to itself or produce no frames at all.
func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// copyWithoutFFmpeg copies the unpacked archive, leaving out the two
// executables the control arm is about, and returns the copy.
func copyWithoutFFmpeg(t *testing.T, dir string) string {
	t.Helper()

	dst := filepath.Join(t.TempDir(), filepath.Base(dir)+"-no-ffmpeg")
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		switch entry.Name() {
		case exeName("ffmpeg"), exeName("ffprobe"):
			return nil
		}
		copyFile(t, path, target)
		return nil
	})
	if err != nil {
		t.Fatalf("copy %s: %v", dir, err)
	}
	t.Logf("stripped copy: %s (no %s, no %s)", dst, exeName("ffmpeg"), exeName("ffprobe"))
	return dst
}

// copyFile copies one file and its mode. The mode matters for exactly one
// file - torpeek itself, which the control arm has to be able to run - and
// getting it wrong there would fail that arm for the wrong reason.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()

	info, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat %s: %v", src, err)
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatalf("copy %s: %v", src, err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close %s: %v", dst, err)
	}
}

func exitCode(t *testing.T, err error) int {
	t.Helper()

	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	// Not an exit status at all: the process could not be started, or the
	// context ran out. Either is a failure of this check and not a result.
	t.Fatalf("torpeek did not run to a verdict: %v", err)
	return -1
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}
