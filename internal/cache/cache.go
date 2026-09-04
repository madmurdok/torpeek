// Package cache serves a finished run from disk instead of the swarm.
//
// The cache is not a separate store: it is the output directory, keyed by
// infohash and run parameters exactly as results are already laid out. That
// keying is what makes a repeat run free - and it is also why the parameters
// that change what a frame looks like belong in the key while the file
// selection does not (see core.ParamsKey).
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/madmurdok/torpeek/internal/manifest"
)

// Version is the run record's format.
const Version = 1

// Name is the run record's file name, in the parameters directory alongside
// the per-file result directories.
const Name = "run.json"

// Run records what a run covered, so a later one can tell a complete result
// from a partial one without opening a session to find out.
type Run struct {
	Version   int       `json:"version"`
	Tool      string    `json:"tool"`
	CreatedAt time.Time `json:"created_at"`

	// Source is the string a person could paste back in to run this again: the
	// magnet URI exactly as typed, or - for a .torrent - the copy the run kept
	// in its own directory (output.Layout.TorrentPath) rather than wherever
	// the file was read from. The distinction is not cosmetic: a .torrent
	// dropped onto the web UI is staged in a temp directory that is deleted as
	// the run ends, so recording that path put a name for a file that provably
	// no longer existed in the one field promising to be pasteable
	// (core.recordedSource). Absent from a record written before this field
	// existed, in which case it reads back as "": a run found on disk that
	// cannot say what it was asked for.
	Source string `json:"source"`

	InfoHash string `json:"infohash"`
	Name     string `json:"name"`
	Private  bool   `json:"private"`

	// Plan is what was asked for, in the form a person described it - not
	// ParamsKey's sha256 prefix, which names the directory but cannot be
	// turned back into a description. Absent from an older record, in which
	// case it reads back as the zero value.
	Plan Plan `json:"plan"`

	// Videos is every video file the torrent holds, not only the ones this
	// run worked on. Without it a rerun could not tell "all files" from "the
	// files that happened to be asked for last time" while staying offline.
	Videos []File `json:"videos"`
	// Selected lists every file index any run recorded in this directory has
	// ever asked for - not only this run's own selection. Without it, a file
	// index absent from Complete is ambiguous: never picked, or picked and
	// failed. Cross-referenced with Complete, it tells the two apart. Absent
	// from a record written before this field existed, in which case it
	// reads back as nil: a pre-existing record cannot say what was asked for,
	// only what came out whole.
	Selected []int `json:"selected"`
	// Complete lists the file indices whose frame set came out whole, across
	// every run this directory has ever recorded - not only this run's own.
	// A file with a failed capture point is deliberately absent: a rerun
	// should try it again rather than serve a gap as a result.
	Complete []int `json:"complete"`
}

// SelectedCount is how many files this directory's runs have ever asked for -
// the denominator a listing judges Complete against to call a result Partial
// rather than Done (TOR-72). It is not simply len(Selected): the engine
// already supported capturing a subset of a torrent's files before TOR-65
// taught it to record which subset, so a nil Selected does not mean "nothing
// was asked for" - it means this record predates the field and cannot say.
// The honest fallback for that case is Videos, the same file count a listing
// compared Complete against before Selected existed, so a pre-TOR-65 record
// keeps reading exactly as it always did instead of gaining a Partial
// verdict nothing about it actually changed.
func (r Run) SelectedCount() int {
	if r.Selected == nil {
		return len(r.Videos)
	}
	return len(r.Selected)
}

// Plan is a run's parameters in the form they were asked for, since
// ParamsKey only ever stores their sha256 prefix in the directory name and a
// directory cannot be turned back into a description of what was requested.
type Plan struct {
	Count      int     `json:"count"`
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Profile    string  `json:"profile"`
	Format     string  `json:"format"`
	Sequential bool    `json:"sequential"`
}

// File is one video file as the torrent numbers it.
type File struct {
	Index  int    `json:"index"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Offset int64  `json:"offset"`
}

// SaveRun writes the record for a finished run.
func SaveRun(paramsDir string, run Run) error {
	if err := os.MkdirAll(paramsDir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Written the same way frames are: a reader must never find half a record.
	tmp, err := os.CreateTemp(paramsDir, "."+Name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(paramsDir, Name))
}

// LoadRun reads the record, reporting whether there is a usable one.
func LoadRun(paramsDir string) (Run, bool) {
	data, err := os.ReadFile(filepath.Join(paramsDir, Name))
	if err != nil {
		return Run{}, false
	}

	var run Run
	if err := json.Unmarshal(data, &run); err != nil || run.Version != Version {
		// A record from a format this build does not know is not a hit. Going
		// to the swarm is slow; serving something misread is wrong.
		return Run{}, false
	}
	return run, true
}

// LoadManifest reads one file's manifest from its result directory, with
// every frame path resolved against that directory.
//
// This is the only place in the project that parses a manifest.json, which is
// what makes it the seam: a frame path is recorded relative to the manifest
// (manifest.Frame.Path) and only this func knows which directory the record
// came out of, so it is the one place that can turn the record back into
// something openable. Everything downstream - cache.Usable, core's replay and
// delete, the web listing, sheet.Build - therefore keeps seeing a path it can
// hand to os.Open, exactly as it did when the path on disk was absolute, and
// none of them has to learn where a manifest lives.
//
// The counterpart on the way out is output.Writer.WriteManifest, which is the
// only place one is written.
func LoadManifest(fileDir string) (manifest.Manifest, bool) {
	data, err := os.ReadFile(filepath.Join(fileDir, manifest.Name))
	if err != nil {
		return manifest.Manifest{}, false
	}

	var m manifest.Manifest
	if err := json.Unmarshal(data, &m); err != nil || m.Version != manifest.Version {
		return manifest.Manifest{}, false
	}
	return m.Resolved(fileDir, onDisk), true
}

// onDisk is what manifest.Resolved asks about a candidate location. Existence
// alone, deliberately: whether a frame is whole enough to serve is Usable's
// judgement to make, on one path, and duplicating a weaker version of it here
// would give two answers to one question.
func onDisk(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Usable reports whether a manifest still describes something on disk.
//
// Frames outlive nothing: a directory can be cleaned out from under a manifest
// at any time, and serving paths to files that are gone would be worse than
// admitting a miss and fetching again.
//
// It judges the manifest as LoadManifest handed it over - paths already
// resolved against the directory the record was read from - so it is asking
// whether the frames of THIS results tree are there, not whether the tree the
// run originally wrote still exists somewhere.
func Usable(m manifest.Manifest) bool {
	if len(m.Frames) == 0 {
		return false
	}
	for _, f := range m.Frames {
		if f.Shift == manifest.ShiftFailed {
			return false
		}
		if f.Path == "" {
			return false
		}
		if info, err := os.Stat(f.Path); err != nil || info.Size() == 0 {
			return false
		}
	}
	return true
}
