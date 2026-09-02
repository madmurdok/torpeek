package swarm

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// FileInfo describes one file inside a torrent.
type FileInfo struct {
	Index  int
	Path   string // path inside the torrent
	Length int64
	Offset int64 // absolute offset of the file within the torrent's byte stream
}

// Name is the file's base name.
func (f FileInfo) Name() string { return path.Base(f.Path) }

// videoExtensions are the containers worth trying. The list stays deliberately
// broad: ffprobe decides what is actually readable, this only avoids handing it
// obvious non-video files.
var videoExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
	".mov": true, ".ts": true, ".m2ts": true, ".mts": true,
	".wmv": true, ".flv": true, ".webm": true, ".mpg": true,
	".mpeg": true, ".vob": true, ".ogv": true, ".divx": true,
	".rmvb": true, ".asf": true, ".3gp": true,
}

// sampleFraction is the size below which a file named like a sample is treated
// as one: real episodes in a season pack are within an order of magnitude of
// each other, samples are a tiny fraction of the largest file.
const sampleFraction = 0.10

// SelectVideos returns the video files worth processing, in torrent order.
//
// Selection is by extension only. Confirming a file really is video costs a
// read of its header, which is what the probe step does anyway once the bridge
// can serve bytes - doing it twice would mean paying for those pieces twice.
func SelectVideos(files []FileInfo) []FileInfo {
	var candidates []FileInfo
	var largest int64

	for _, f := range files {
		if !videoExtensions[strings.ToLower(path.Ext(f.Path))] {
			continue
		}
		candidates = append(candidates, f)
		if f.Length > largest {
			largest = f.Length
		}
	}

	out := candidates[:0:0]
	for _, f := range candidates {
		if looksLikeSample(f, largest) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// looksLikeSample keeps trailers and samples out of the result. Both halves are
// required: a file merely named "sample" but as big as the feature is the
// feature, and a small file with an ordinary name may be a short episode.
func looksLikeSample(f FileInfo, largest int64) bool {
	name := strings.ToLower(f.Path)
	named := strings.Contains(name, "sample") || strings.Contains(name, "trailer")
	return named && float64(f.Length) < float64(largest)*sampleFraction
}

// ErrNoFileMatch means a selection named something the torrent does not hold.
var ErrNoFileMatch = errors.New("no file matches the selection")

// Select narrows a list of files to those named by specs, keeping torrent
// order and dropping duplicates. With no specs the list is returned unchanged.
//
// A spec is either an index as the torrent numbers its files - the same number
// that appears in events and in output directory names - or a pattern matched
// against the path, case-insensitively: a glob if it contains one of * ? [,
// otherwise a substring. Nobody reads a torrent to learn that episode 3 is
// index 11, so the pattern form is the one people will use.
//
// A spec matching nothing is an error rather than an empty result: silently
// producing no frames looks identical to a run that found nothing worth
// taking, and the caller cannot tell which happened.
func Select(files []FileInfo, specs []string) ([]FileInfo, error) {
	if len(specs) == 0 {
		return files, nil
	}

	keep := make(map[int]bool, len(files))
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}

		matched := false
		for _, f := range files {
			if matches(f, spec) {
				keep[f.Index] = true
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("%w: %q does not name any of %s",
				ErrNoFileMatch, spec, describe(files))
		}
	}

	out := files[:0:0]
	for _, f := range files {
		if keep[f.Index] {
			out = append(out, f)
		}
	}
	return out, nil
}

func matches(f FileInfo, spec string) bool {
	if n, err := strconv.Atoi(spec); err == nil {
		return f.Index == n
	}

	spec = strings.ToLower(spec)
	full := strings.ToLower(f.Path)

	if strings.ContainsAny(spec, "*?[") {
		// A glob never crosses a separator, so a pattern like *.mkv has to be
		// offered the base name as well as the whole path to behave the way
		// someone typing it expects.
		if ok, err := path.Match(spec, full); err == nil && ok {
			return true
		}
		ok, err := path.Match(spec, strings.ToLower(f.Name()))
		return err == nil && ok
	}

	return strings.Contains(full, spec)
}

// describe lists what was on offer, so a failed selection says what could have
// been named instead of only what could not.
func describe(files []FileInfo) string {
	if len(files) == 0 {
		return "(no video files)"
	}
	parts := make([]string, 0, len(files))
	for _, f := range files {
		parts = append(parts, fmt.Sprintf("%d:%s", f.Index, f.Name()))
	}
	return strings.Join(parts, ", ")
}
