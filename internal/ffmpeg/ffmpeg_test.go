package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeTool writes a stub executable that prints and exits as instructed, so
// the runner can be tested without depending on a real ffmpeg.
func fakeTool(t *testing.T, dir, name, script string) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("stub executables are written as shell scripts")
	}

	path := filepath.Join(dir, name)
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
	return path
}

func TestLocateInPrefersBundledOverPath(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "ffmpeg", "echo bundled-ffmpeg")
	fakeTool(t, dir, "ffprobe", "echo bundled-ffprobe")

	tools, err := LocateIn(dir)
	if err != nil {
		t.Fatalf("LocateIn: %v", err)
	}

	if got, want := tools.FFmpeg, filepath.Join(dir, "ffmpeg"); got != want {
		t.Errorf("FFmpeg = %q, want the bundled copy at %q", got, want)
	}
	if got, want := tools.FFprobe, filepath.Join(dir, "ffprobe"); got != want {
		t.Errorf("FFprobe = %q, want the bundled copy at %q", got, want)
	}
}

func TestLocateInFallsBackToPath(t *testing.T) {
	// An empty directory forces the PATH lookup, which finds the real tools.
	tools, err := LocateIn(t.TempDir())
	if err != nil {
		t.Skipf("no ffmpeg on PATH to fall back to: %v", err)
	}
	if tools.FFmpeg == "" || tools.FFprobe == "" {
		t.Fatal("LocateIn returned empty paths without an error")
	}
}

// TestMissingToolIsTypedError covers the acceptance criterion: a missing
// binary must surface as an error a caller can match on, not a panic.
func TestMissingToolIsTypedError(t *testing.T) {
	// Emptying PATH removes the fallback, leaving nothing to find.
	t.Setenv("PATH", t.TempDir())

	_, err := LocateIn(t.TempDir())
	if err == nil {
		t.Fatal("LocateIn found tools that do not exist")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error %v does not match ErrNotFound", err)
	}

	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error %v is not a *NotFoundError", err)
	}
	if notFound.Tool != "ffmpeg" {
		t.Errorf("Tool = %q, want %q", notFound.Tool, "ffmpeg")
	}
	if len(notFound.Searched) == 0 {
		t.Error("NotFoundError does not say where it looked")
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("error %q should mention that PATH was searched too", err.Error())
	}
}

func TestRunReturnsStdout(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "ffmpeg", "echo hello-stdout")
	fakeTool(t, dir, "ffprobe", "echo hello-stdout")

	tools, err := LocateIn(dir)
	if err != nil {
		t.Fatalf("LocateIn: %v", err)
	}

	out, err := tools.Run(context.Background(), "ffmpeg")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "hello-stdout" {
		t.Errorf("stdout = %q, want %q", got, "hello-stdout")
	}
}

// TestRunFailureCarriesStderr matters because ffmpeg explains itself on stderr;
// dropping it turns every failure into "exit status 1".
func TestRunFailureCarriesStderr(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "ffmpeg", "echo 'moov atom not found' >&2\nexit 3")
	fakeTool(t, dir, "ffprobe", "exit 0")

	tools, err := LocateIn(dir)
	if err != nil {
		t.Fatalf("LocateIn: %v", err)
	}

	_, err = tools.Run(context.Background(), "ffmpeg", "-i", "nonexistent")
	if err == nil {
		t.Fatal("Run reported success for a failing tool")
	}

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error %v is not an *ExitError", err)
	}
	if exitErr.Code != 3 {
		t.Errorf("Code = %d, want 3", exitErr.Code)
	}
	if !strings.Contains(exitErr.Stderr, "moov atom not found") {
		t.Errorf("Stderr = %q, want it to carry the tool's complaint", exitErr.Stderr)
	}
}

func TestRunRespectsContext(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "ffmpeg", "sleep 30")
	fakeTool(t, dir, "ffprobe", "exit 0")

	tools, err := LocateIn(dir)
	if err != nil {
		t.Fatalf("LocateIn: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := tools.Run(ctx, "ffmpeg"); err == nil {
		t.Fatal("Run returned success for a cancelled context")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Run took %s to honour a 200ms deadline", elapsed)
	}
}

func TestUnknownToolIsRejected(t *testing.T) {
	tools := Tools{FFmpeg: "/bin/true", FFprobe: "/bin/true"}
	if _, err := tools.Run(context.Background(), "ffplay"); err == nil {
		t.Error("Run accepted a tool it does not manage")
	}
}

func TestExecutableName(t *testing.T) {
	got := executableName("ffmpeg")
	want := "ffmpeg"
	if runtime.GOOS == "windows" {
		want = "ffmpeg.exe"
	}
	if got != want {
		t.Errorf("executableName = %q, want %q on %s", got, want, runtime.GOOS)
	}
}

// TestExecutableNameForEveryTarget names each target instead of inheriting it.
// The release archive for Windows holds ffmpeg.exe and ffprobe.exe, and the
// test above can only ever exercise the branch for the machine it runs on -
// so on a mac or a Linux runner the case the Windows archive depends on would
// go unchecked (TOR-26).
func TestExecutableNameForEveryTarget(t *testing.T) {
	cases := []struct {
		goos, base, want string
	}{
		{"windows", "ffmpeg", "ffmpeg.exe"},
		{"windows", "ffprobe", "ffprobe.exe"},
		{"linux", "ffmpeg", "ffmpeg"},
		{"linux", "ffprobe", "ffprobe"},
		{"darwin", "ffmpeg", "ffmpeg"},
		{"darwin", "ffprobe", "ffprobe"},
	}
	for _, c := range cases {
		if got := executableNameFor(c.goos, c.base); got != c.want {
			t.Errorf("executableNameFor(%q, %q) = %q, want %q", c.goos, c.base, got, c.want)
		}
	}
}

// TestVersionsAgainstRealTools is a smoke test against whatever ffmpeg the
// machine has; it is skipped where there is none.
func TestVersionsAgainstRealTools(t *testing.T) {
	tools, err := LocateIn()
	if err != nil {
		t.Skipf("no ffmpeg available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ffmpegVersion, ffprobeVersion, err := tools.Versions(ctx)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	t.Logf("ffmpeg: %s", ffmpegVersion)
	t.Logf("ffprobe: %s", ffprobeVersion)

	if !strings.HasPrefix(ffmpegVersion, "ffmpeg version") {
		t.Errorf("ffmpeg version line = %q", ffmpegVersion)
	}
	if !strings.HasPrefix(ffprobeVersion, "ffprobe version") {
		t.Errorf("ffprobe version line = %q", ffprobeVersion)
	}
}
