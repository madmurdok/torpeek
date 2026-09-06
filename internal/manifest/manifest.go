// Package manifest is the machine-readable record of one video file's run:
// what was asked for, what was taken, what it cost and what the file turned
// out to contain.
//
// It holds data and nothing else - no imports beyond time and path/filepath -
// so that the core can build one without anything having to import the core.
// Every field here is required by REQUIREMENTS.md section 2.8; the JSON names
// are the contract a caller reads, so they are written out rather than
// derived. filepath is here for one reason: Frame.Path's meaning is part of
// the format, so the two conversions that give it that meaning (Relative and
// Resolved) belong next to the field rather than in whichever package happens
// to touch a manifest.
package manifest

import (
	"path/filepath"
	"time"
)

// Version is the manifest format. A consumer should refuse a version it does
// not know rather than guess at missing fields.
const Version = 1

// Manifest is one video file's result.
type Manifest struct {
	Version   int       `json:"version"`
	Tool      string    `json:"tool"`
	CreatedAt time.Time `json:"created_at"`

	Torrent   Torrent    `json:"torrent"`
	File      File       `json:"file"`
	Video     Video      `json:"video"`
	Audio     []Audio    `json:"audio"`
	Subtitles []Subtitle `json:"subtitles"`
	Frames    []Frame    `json:"frames"`
	Cost      Cost       `json:"cost"`
}

// Torrent is what the file was taken from.
type Torrent struct {
	InfoHash    string `json:"infohash"`
	Name        string `json:"name"`
	PieceLength int64  `json:"piece_length"`
	Private     bool   `json:"private"`
	Peers       int    `json:"peers"`
	Seeds       int    `json:"seeds"`
	// Availability is how many peers held each stretch of this file, in equal
	// buckets across its length. Each bucket carries its scarcest piece: a
	// stretch is only as reachable as the least held piece in it.
	Availability []int `json:"availability"`
}

// File is which file of the torrent this is.
type File struct {
	Index      int    `json:"index"`
	Path       string `json:"path"`
	Bytes      int64  `json:"bytes"`
	DurationMS int64  `json:"duration_ms"`
	Container  string `json:"container"`
}

// Video is the quality summary - enough to tell an over-compressed rip or an
// upscale from the real thing without watching it.
type Video struct {
	Codec        string  `json:"codec"`
	Profile      string  `json:"profile"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	FPS          float64 `json:"fps"`
	BitRate      int64   `json:"bit_rate"`
	BitsPerPixel float64 `json:"bits_per_pixel"`

	// DynamicRange names what the source declares itself to be - "hdr10",
	// "hlg", "dolby-vision-p5" - and is absent for an ordinary SDR file.
	//
	// It earns a place next to the quality figures because of what a frame
	// cannot say for itself. An HDR source rendered without conversion comes
	// out flat and grey, and the reading available to whoever is looking is
	// that torpeek is broken rather than that the file is HDR (TOR-108). This
	// field is where the run says which it was.
	DynamicRange string `json:"dynamic_range,omitempty"`
	// ToneMappedTo is what the frames were converted to on the way out,
	// "bt709" when they were and absent when they were not. Present with
	// DynamicRange absent never happens; DynamicRange present with this
	// absent does, and means the source is high dynamic range and the frames
	// were left as the decoder produced them - the Dolby Vision profiles
	// whose base layer nothing in the release can render.
	ToneMappedTo string `json:"tone_mapped_to,omitempty"`

	// ColorTransfer, ColorPrimaries and ColorSpace are the stream's own
	// colour tags, verbatim from ffprobe, so a surprising DynamicRange can be
	// traced back to what the container actually said.
	ColorTransfer  string `json:"color_transfer,omitempty"`
	ColorPrimaries string `json:"color_primaries,omitempty"`
	ColorSpace     string `json:"color_space,omitempty"`
	// DolbyVisionProfile is the profile from the stream's Dolby Vision
	// configuration record, absent when it carries none.
	DolbyVisionProfile int `json:"dolby_vision_profile,omitempty"`
}

// Audio is one audio track - the answer to "is this the dub I wanted".
type Audio struct {
	Index    int    `json:"index"`
	Language string `json:"language"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels"`
	Title    string `json:"title"`
	Default  bool   `json:"default"`
}

// Subtitle is one subtitle track.
type Subtitle struct {
	Index    int    `json:"index"`
	Language string `json:"language"`
	Format   string `json:"format"`
	Title    string `json:"title"`
	Forced   bool   `json:"forced"`
	Default  bool   `json:"default"`
}

// Shift says why a frame did not come from where it was asked for.
type Shift string

const (
	// ShiftNone: taken where it was planned.
	ShiftNone Shift = ""
	// ShiftUnavailable: no peer held the pieces there, so a nearby point was
	// used instead.
	ShiftUnavailable Shift = "shifted"
	// ShiftBlank: the frame there was blank, so a neighbouring keyframe was
	// used instead.
	ShiftBlank Shift = "stepped"
	// ShiftFailed: the point could not be taken at all. Path is empty and
	// Error says why.
	ShiftFailed Shift = "unavailable"
)

// Frame is one capture point and what became of it.
type Frame struct {
	Index       int   `json:"index"`
	RequestedMS int64 `json:"requested_ms"`
	// ActualMS is where the frame came from, absent when none was taken.
	ActualMS *int64 `json:"actual_ms"`
	// Path is where the frame is, written relative to the directory holding
	// the manifest that names it ("frames/000.jpg"), slash-separated whatever
	// the host OS is - so a results tree can be copied, moved, backed up or
	// synced from a seedbox to a laptop and still describe itself (TOR-60).
	// Empty when the point produced no frame.
	//
	// In memory it is the opposite: always a path that can be opened, because
	// every reader goes through Resolved (cache.LoadManifest) and every writer
	// through Relative (output.Writer.WriteManifest). That asymmetry is the
	// whole design - one seam converts on the way in, one on the way out, and
	// nothing in between has to know which shape it is holding.
	//
	// A manifest written before 0.8.0 holds the absolute path the frame had
	// when it was taken. Nothing distinguishes the two shapes but the string
	// itself, and that is deliberate: manifest.Version is checked the way
	// cache.Version is, so bumping it would turn every manifest already on
	// disk into a miss rather than an older shape to read (the TOR-52 trap).
	// Resolved reads both.
	Path   string `json:"path"`
	Shift  Shift  `json:"shift"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// Error is the typed code when the point failed, empty otherwise. The
	// shift marker says a point was lost; this says what lost it.
	Error string `json:"error"`
}

// Cost is what the run had spent by the time this file was finished. It is the
// run's cost, not the file's: the budget is shared, and a per-file share of it
// would be a number nothing enforces.
type Cost struct {
	DownloadedBytes int64 `json:"downloaded_bytes"`
	ElapsedMS       int64 `json:"elapsed_ms"`
	LimitBytes      int64 `json:"limit_bytes"`
	LimitMS         int64 `json:"limit_ms"`
	// LimitHit names the ceiling that stopped the run, empty if none did.
	//
	// "budget" is one of the run's own two, above; "traffic_roof" is the
	// client-wide roof over every run at once, which is NOT bounded by
	// LimitBytes here and may have been filled by other runs entirely
	// (core.StopRoof). Recording them under one name would leave a reader of
	// this file comparing DownloadedBytes against LimitBytes and finding the
	// run stopped nowhere near its ceiling, with nothing here to say why.
	LimitHit string `json:"limit_hit"`
	// Sequential marks a run that degraded to sequential reading from the
	// start because the container carried no duration (REQUIREMENTS.md
	// 2.7). Its frames sit clustered near the front of the file rather than
	// spread across it - this is what tells a reader of the manifest alone,
	// without re-deriving it from the timestamps, not to expect otherwise.
	Sequential bool `json:"sequential"`
}

// Name is what a manifest is called on disk, next to the frames it describes.
const Name = "manifest.json"

// Relative returns m with every frame path recorded the way it must sit on
// disk: relative to dir, the directory the manifest itself is written into.
//
// A path that cannot be said relative to dir - one pointing outside the run
// altogether, which no run of ours writes - is left exactly as it is. A
// manifest may only ever record where a frame is, and a relative path that
// climbed out of its own directory would be a worse lie than the absolute one
// it replaced.
//
// The result is a copy: callers hold the resolved manifest afterwards (delete
// rebuilds the contact sheet from it, and sheet.Build opens each frame by
// path), so relativizing in place would quietly break the caller that asked
// for the recordable form.
//
// It is idempotent, so calling it on an already-recorded manifest is safe:
// filepath.Rel of a directory and a path already relative to it either fails
// or escapes, and both leave the path untouched.
func (m Manifest) Relative(dir string) Manifest {
	m.Frames = mapFramePaths(m.Frames, func(path string) string {
		rel, err := filepath.Rel(dir, path)
		if err != nil || !filepath.IsLocal(rel) {
			return path
		}
		return filepath.ToSlash(rel)
	})
	return m
}

// Resolved returns m with every frame path turned into somewhere openable,
// given the manifest was read from dir. present reports whether a candidate is
// actually on disk; a nil present means none of them is.
//
// The rule, and the reason the ticket that asked for this called the quiet
// failure worse than the loud one: a frame is looked for INSIDE the manifest's
// own directory first, and only then where an older manifest's absolute path
// literally points. A results tree copied somewhere else therefore serves its
// own frames, never the original's - which is exactly what a person copying a
// directory and pointing the tool at the copy has already asked for. Serving
// the original would be indistinguishable from working.
//
// Two places inside dir are tried, in this order:
//
//   - the path as recorded, joined onto dir. This is the whole answer for a
//     manifest written by 0.8.0 or later, and costs one stat.
//   - the frame's own name one directory down (dir/frames/000.jpg), which is
//     where every version of this tool has put frames (output.Layout). It is
//     what re-anchors an OLDER manifest onto the copy it was found in, and it
//     is also what saves a manifest written with a relative -out whose process
//     no longer runs from the same working directory.
//
// Only when neither is there is an older record's own absolute path tried, and
// then kept as the answer whatever the outcome - precisely what every reader
// did before this existed. So a tree that has not moved reads exactly as it
// always did, and a record pointing outside its own run still arrives at
// core.DeleteFrame's within() to be refused rather than being quietly
// rewritten into something acceptable. A relative record never gets that third
// try; frameLocations says why.
//
// Like Relative, the result is a copy.
func (m Manifest) Resolved(dir string, present func(path string) bool) Manifest {
	m.Frames = mapFramePaths(m.Frames, func(path string) string {
		fallback, candidates := frameLocations(dir, path)
		for _, candidate := range candidates {
			if present != nil && present(candidate) {
				return candidate
			}
		}
		return fallback
	})
	return m
}

// frameLocations is Resolved's rule as data: where to look, and what to say
// when the frame is nowhere. The fallback is never a path relative to the
// process's working directory, whatever the record holds - a bare
// "frames/000.jpg" resolved against a cwd nobody chose could open some
// unrelated file that happens to sit there, which is the one way this could
// serve a frame from somewhere the caller did not point at.
func frameLocations(dir, path string) (fallback string, candidates []string) {
	if filepath.IsLocal(path) {
		fallback = filepath.Join(dir, filepath.FromSlash(path))
		candidates = append(candidates, fallback)
	} else {
		// Absolute, or a record that climbs out of its own directory:
		// nothing here may invent a place for it, so it falls back to
		// itself - what every reader did before this existed.
		fallback = path
	}

	// The frame's own name one directory down, which is where output.Layout
	// has always put frames. For a manifest written by 0.8.0 this is the
	// candidate above again; for an older one it is the whole point.
	if tail := filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path)); filepath.IsLocal(tail) {
		if neighbour := filepath.Join(dir, tail); !contains(candidates, neighbour) {
			candidates = append(candidates, neighbour)
		}
	}

	// Last, and only when the record is absolute. A relative record is never
	// consulted as written: resolved against a working directory nobody
	// chose it could open some unrelated file that happens to sit there,
	// which is the one remaining way this could serve a frame from somewhere
	// the caller never pointed at.
	if filepath.IsAbs(path) {
		candidates = append(candidates, path)
	}
	return fallback, candidates
}

func contains(list []string, want string) bool {
	for _, have := range list {
		if have == want {
			return true
		}
	}
	return false
}

// mapFramePaths rewrites every non-empty frame path through convert, on a copy
// of the records. A point that produced no frame has nothing to convert and
// keeps its empty path, which is what says so.
func mapFramePaths(frames []Frame, convert func(string) string) []Frame {
	if len(frames) == 0 {
		return frames
	}
	out := make([]Frame, len(frames))
	copy(out, frames)
	for i, f := range out {
		if f.Path == "" {
			continue
		}
		out[i].Path = convert(f.Path)
	}
	return out
}
