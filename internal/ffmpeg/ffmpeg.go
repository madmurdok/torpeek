// Package ffmpeg locates and runs the bundled ffmpeg and ffprobe binaries.
//
// torpeek never links a decoder: it ships the two executables next to itself
// and shells out. That is what keeps the Go side free of CGO and the release a
// single folder per platform (REQUIREMENTS.md section 4).
//
// Both the probe and the frame extractor go through here, so there is exactly
// one place that knows how an external tool is found, invoked and blamed.
package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrNotFound means a required executable could not be located. Callers turn
// this into a stable error code rather than a stack trace: on a user's machine
// it is the single most likely thing to go wrong.
var ErrNotFound = errors.New("executable not found")

// NotFoundError names which tool is missing and where it was looked for.
type NotFoundError struct {
	Tool     string
	Searched []string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s: %v (searched: %s)", e.Tool, ErrNotFound, strings.Join(e.Searched, ", "))
}

func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// ExitError carries a failed run's exit code and the tail of its stderr.
// ffmpeg explains itself on stderr, so discarding it turns every failure into
// "exit status 1" and a guessing game.
type ExitError struct {
	Tool   string
	Args   []string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("%s exited with code %d", e.Tool, e.Code)
	}
	return fmt.Sprintf("%s exited with code %d: %s", e.Tool, e.Code, e.Stderr)
}

// Tools holds resolved paths to the two executables.
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// executableName adds the platform's extension. Windows will not run a bare
// "ffmpeg", which is the whole reason this is not a plain string concat.
func executableName(base string) string {
	return executableNameFor(runtime.GOOS, base)
}

// executableNameFor is executableName with the target named rather than
// inherited, so the Windows case can be tested from anywhere.
//
// That split is not decoration. The Windows release archive holds ffmpeg.exe
// and ffprobe.exe, and a lookup that asked for a bare "ffmpeg" would find
// neither - yet on a mac or a Linux runner the windows branch of a
// runtime.GOOS switch is unreachable, so nothing that runs in this project's
// tests or CI would ever notice it break (TOR-26).
func executableNameFor(goos, base string) string {
	if goos == "windows" {
		return base + ".exe"
	}
	return base
}

// Locate finds ffmpeg and ffprobe, preferring the copies shipped alongside the
// running binary over whatever the machine happens to have on PATH. A user who
// downloaded one folder gets the versions that were tested with this build.
func Locate() (Tools, error) {
	return LocateIn(bundledDirs()...)
}

// LocateIn searches the given directories first, then PATH.
func LocateIn(dirs ...string) (Tools, error) {
	ffmpegPath, err := find("ffmpeg", dirs)
	if err != nil {
		return Tools{}, err
	}
	ffprobePath, err := find("ffprobe", dirs)
	if err != nil {
		return Tools{}, err
	}
	return Tools{FFmpeg: ffmpegPath, FFprobe: ffprobePath}, nil
}

// bundledDirs are the places a release archive may keep the executables: right
// next to the binary, or in a subdirectory if the archive keeps them tidy.
func bundledDirs() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	return []string{dir, filepath.Join(dir, "third_party", "ffmpeg")}
}

func find(tool string, dirs []string) (string, error) {
	name := executableName(tool)
	searched := make([]string, 0, len(dirs)+1)

	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		searched = append(searched, candidate)
		if isExecutable(candidate) {
			return candidate, nil
		}
	}

	searched = append(searched, "PATH")
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}

	return "", &NotFoundError{Tool: tool, Searched: searched}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	// On Windows the permission bits carry no meaning; existence plus the .exe
	// extension is as much as can be checked.
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

// Run executes one of the tools and returns its stdout.
//
// stderr is captured and attached to the error, never streamed to the terminal:
// the core stays silent so its clients decide what a person sees.
func (t Tools) Run(ctx context.Context, tool string, args ...string) ([]byte, error) {
	path, err := t.pathFor(tool)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), &ExitError{
			Tool:   tool,
			Args:   args,
			Code:   exitErr.ExitCode(),
			Stderr: lastLines(stderr.String(), 5),
		}
	}
	// Context cancellation and "could not start" land here.
	return nil, fmt.Errorf("run %s: %w", tool, err)
}

func (t Tools) pathFor(tool string) (string, error) {
	switch tool {
	case "ffmpeg":
		if t.FFmpeg == "" {
			return "", &NotFoundError{Tool: tool, Searched: []string{"(unset)"}}
		}
		return t.FFmpeg, nil
	case "ffprobe":
		if t.FFprobe == "" {
			return "", &NotFoundError{Tool: tool, Searched: []string{"(unset)"}}
		}
		return t.FFprobe, nil
	default:
		return "", fmt.Errorf("unknown tool %q", tool)
	}
}

// Versions reports what both tools call themselves, for the run manifest: a
// measurement is only comparable if it says which decoder produced it.
func (t Tools) Versions(ctx context.Context) (ffmpegVersion, ffprobeVersion string, err error) {
	out, err := t.Run(ctx, "ffmpeg", "-version")
	if err != nil {
		return "", "", err
	}
	ffmpegVersion = firstLine(out)

	out, err = t.Run(ctx, "ffprobe", "-version")
	if err != nil {
		return "", "", err
	}
	return ffmpegVersion, firstLine(out), nil
}

func firstLine(b []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(line)
}

// lastLines keeps the tail of a tool's stderr, which is where the actual
// complaint sits after pages of build configuration.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "; "))
}
