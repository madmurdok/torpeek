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

	InfoHash string `json:"infohash"`
	Name     string `json:"name"`
	Private  bool   `json:"private"`

	// Videos is every video file the torrent holds, not only the ones this
	// run worked on. Without it a rerun could not tell "all files" from "the
	// files that happened to be asked for last time" while staying offline.
	Videos []File `json:"videos"`
	// Complete lists the file indices whose frame set came out whole. A file
	// with a failed capture point is deliberately absent: a rerun should try
	// it again rather than serve a gap as a result.
	Complete []int `json:"complete"`
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

// LoadManifest reads one file's manifest from its result directory.
func LoadManifest(fileDir string) (manifest.Manifest, bool) {
	data, err := os.ReadFile(filepath.Join(fileDir, manifest.Name))
	if err != nil {
		return manifest.Manifest{}, false
	}

	var m manifest.Manifest
	if err := json.Unmarshal(data, &m); err != nil || m.Version != manifest.Version {
		return manifest.Manifest{}, false
	}
	return m, true
}

// Usable reports whether a manifest still describes something on disk.
//
// Frames outlive nothing: a directory can be cleaned out from under a manifest
// at any time, and serving paths to files that are gone would be worse than
// admitting a miss and fetching again.
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
