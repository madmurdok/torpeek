// Package output writes what a run produces: full-resolution frames as they
// become ready, the contact sheet assembled at the end, the JSON manifest, the
// .torrent the run was made from, the result cache keyed by infohash and run
// parameters, and the run state that makes cancel and resume work.
//
// Requirements: sections 2.8, 2.9, 2.10 and 2.11.
package output

import (
	"encoding/json"
	"fmt"
	"os"
	gopath "path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/madmurdok/torpeek/internal/manifest"
)

// SheetName is what the contact sheet is called on disk, next to a file's
// frames and manifest.
const SheetName = "sheet.jpg"

// Layout decides where a run's results live.
//
//	<root>/<infohash>/<params>/
//	├── <infohash>.torrent
//	├── run.json
//	└── <file-slug>/
//	    ├── frames/000.jpg …
//	    ├── sheet.jpg
//	    └── manifest.json
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

// TorrentPath is where the run's own .torrent is kept.
//
// It is named after the infohash rather than given a fixed name the way
// run.json and sheet.jpg are, and the reason is that this is the one artefact
// meant to LEAVE the run directory: it is what a browser downloads and what
// gets dropped into a torrent client's watch directory, where "run.torrent"
// would collide with every other run's and say nothing about which torrent it
// is. The name is redundant with the directory two levels up, deliberately -
// the redundancy is what survives the copy.
//
// It sits in the run directory beside run.json rather than one level up
// beside the sibling parameter sets, even though the file is byte-identical
// in every one of them: a parameter directory is the unit that gets removed,
// and an artefact belonging to a run belongs where the rest of that run's
// output is. Whoever serves it may therefore find the same file under several
// params (see internal/web).
func (l Layout) TorrentPath() string {
	return filepath.Join(l.RunDir(), l.InfoHash+".torrent")
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
	return writeAtomic(dir, name, data)
}

// WriteManifest encodes one file's manifest and writes it beside that file's
// frames, recording every frame path relative to the directory it lands in.
//
// Relativizing here rather than at each caller is what makes a manifest with
// an absolute path in it impossible to write by accident: this package is the
// one that decides where a manifest goes (Layout.FileDir), so it is the only
// one that can say what the paths inside it are relative to, and going
// through it is the only way anything in this project puts a manifest on
// disk. A run writes one at the end of each file and core.DeleteFrame writes
// one back after removing a record; both are the same bytes for the same
// manifest, which is what lets an edited manifest be indistinguishable from a
// captured one.
//
// The encoding is indented and newline-terminated because a manifest is meant
// to be read by a person as readily as by a program (REQUIREMENTS.md 2.8).
func (w *Writer) WriteManifest(fileIndex int, filePath string, m manifest.Manifest) (string, error) {
	data, err := json.MarshalIndent(m.Relative(w.layout.FileDir(fileIndex, filePath)), "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode manifest: %w", err)
	}
	return w.WriteFile(fileIndex, filePath, manifest.Name, append(data, '\n'))
}

// WriteTorrent stores the run's own .torrent in the run directory, next to
// run.json rather than inside any one file's directory.
//
// It is the per-run counterpart of WriteFile, and it exists here rather than
// beside cache.SaveRun for two reasons. cache owns a FORMAT - it marshals the
// run record it defines and writes only that - whereas this package owns
// WHERE a run's output lives (Layout) and how an artefact reaches disk
// safely; a .torrent is an artefact of the run exactly as the contact sheet
// is an artefact of a file, not a record cache can parse back. And doing it
// here means one copy of the temp-write-and-rename discipline covers it: the
// UI hands this file straight to a torrent client, so a reader must never be
// able to see a half-written one, which is the same guarantee every other
// write in this file already makes.
func (w *Writer) WriteTorrent(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("the torrent file for %s is empty", w.layout.InfoHash)
	}
	path := w.layout.TorrentPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create run directory: %w", err)
	}
	return writeAtomic(dir, filepath.Base(path), data)
}

// writeAtomic puts data at dir/name by writing a temporary file beside it and
// renaming it into place.
//
// The rename is what makes it atomic, and it only is because the temporary
// file is created in the destination's own directory: a rename across
// filesystems is a copy, and a copy is exactly the half-written state every
// caller here is avoiding. The permissions are set on the temporary file
// before the rename for the same reason - a file that appears already
// readable rather than one that becomes readable a moment later.
func writeAtomic(dir, name string, data []byte) (string, error) {
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
