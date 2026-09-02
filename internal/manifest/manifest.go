// Package manifest is the machine-readable record of one video file's run:
// what was asked for, what was taken, what it cost and what the file turned
// out to contain.
//
// It holds data and nothing else - no imports beyond time - so that the core
// can build one without anything having to import the core. Every field here
// is required by REQUIREMENTS.md section 2.8; the JSON names are the contract
// a caller reads, so they are written out rather than derived.
package manifest

import "time"

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
	Path     string `json:"path"`
	Shift    Shift  `json:"shift"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
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
