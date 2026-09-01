package swarm

import (
	"path"
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
