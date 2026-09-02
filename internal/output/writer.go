// Package output writes what a run produces: full-resolution frames as they
// become ready, the contact sheet assembled at the end, the JSON manifest, the
// result cache keyed by infohash and run parameters, and the run state that
// makes cancel and resume work.
//
// Requirements: sections 2.8, 2.9, 2.10 and 2.11.
package output

import (
	"fmt"
	"os"
	gopath "path"
	"path/filepath"
	"strings"
	"unicode"
)

// SheetName is what the contact sheet is called on disk, next to a file's
// frames and manifest.
const SheetName = "sheet.jpg"

// Layout decides where a run's results live.
//
//	<root>/<infohash>/<params>/
//	├── <file-slug>/
//	│   ├── frames/000.jpg …
//	│   ├── sheet.jpg
//	│   └── manifest.json
//
// Keying by infohash and by a hash of the parameters that change the result is
// what lets an identical rerun be served from disk without going near the
// network (section 2.9).
type Layout struct {
	Root     string
	InfoHash string
	Params   string
}

// RunDir is the directory holding everything this run produces.
func (l Layout) RunDir() string {
	return filepath.Join(l.Root, l.InfoHash, l.Params)
}

// FileDir is where one video file's results go.
func (l Layout) FileDir(fileIndex int, filePath string) string {
	return filepath.Join(l.RunDir(), FileSlug(fileIndex, filePath))
}

// FramesDir is where that file's frames go.
func (l Layout) FramesDir(fileIndex int, filePath string) string {
	return filepath.Join(l.FileDir(fileIndex, filePath), "frames")
}

// Writer puts frames on disk as they are produced.
//
// Frames are written the moment they exist rather than at the end of a run,
// because the UI fills in as it goes and a cancelled run must keep what it
// already had (sections 2.10 and 2.11).
type Writer struct {
	layout Layout
}

// NewWriter prepares the run directory.
func NewWriter(layout Layout) (*Writer, error) {
	if layout.Root == "" {
		return nil, fmt.Errorf("output root must not be empty")
	}
	if err := os.MkdirAll(layout.RunDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create run directory: %w", err)
	}
	return &Writer{layout: layout}, nil
}

// Layout reports where this writer puts things.
func (w *Writer) Layout() Layout { return w.layout }

// WriteFrame stores one frame and returns its path.
//
// The write is atomic: a temporary file is renamed into place, so a reader
// watching the directory - the web UI does exactly that - never sees a
// half-written image, and a run killed mid-write leaves no corrupt frame
// behind to be mistaken for a real one on resume.
func (w *Writer) WriteFrame(fileIndex int, filePath string, index int, data []byte, ext string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("frame %d of file %d is empty", index, fileIndex)
	}

	dir := w.layout.FramesDir(fileIndex, filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create frames directory: %w", err)
	}

	name := fmt.Sprintf("%03d.%s", index, strings.TrimPrefix(ext, "."))
	final := filepath.Join(dir, name)

	tmp, err := os.CreateTemp(dir, ".frame-*")
	if err != nil {
		return "", fmt.Errorf("create temporary frame: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("write frame: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("close frame: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("set frame permissions: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("place frame: %w", err)
	}

	return final, nil
}

// WriteFile stores an arbitrary artefact (a sheet, a manifest) beside a file's
// frames, atomically for the same reasons.
func (w *Writer) WriteFile(fileIndex int, filePath, name string, data []byte) (string, error) {
	dir := w.layout.FileDir(fileIndex, filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create file directory: %w", err)
	}

	final := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("set permissions on %s: %w", name, err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("place %s: %w", name, err)
	}

	return final, nil
}

// FileSlug turns a path inside a torrent into one safe directory name.
//
// Torrent paths carry anything a filesystem may refuse: separators, colons,
// control characters, names that mean something on Windows. The index prefix
// keeps two files that slugify the same from colliding.
//
// Both separators are stripped regardless of the host OS. Torrent paths are
// slash-separated by spec, but plenty are built on Windows and carry
// backslashes, which a Unix filepath.Base would keep - turning a path into
// part of a name, which is how "..\..\evil" would stop being neutered.
func FileSlug(index int, path string) string {
	normalized := strings.ReplaceAll(path, `\`, "/")
	base := gopath.Base(normalized)
	base = strings.TrimSuffix(base, gopath.Ext(base))

	var b strings.Builder
	lastDash := false
	for _, r := range base {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			lastDash = false
		case r == '.' || r == '_' || r == '-' || unicode.IsSpace(r):
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			// Anything else is dropped rather than transliterated: the slug
			// only has to be stable and safe, not readable in every script.
		}
	}

	slug := strings.Trim(b.String(), "-")
	const maxSlug = 60
	if len(slug) > maxSlug {
		slug = strings.Trim(slug[:maxSlug], "-")
	}
	if slug == "" {
		slug = "file"
	}

	return fmt.Sprintf("%02d-%s", index, slug)
}
