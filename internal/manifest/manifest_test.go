package manifest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// populated is a manifest with every field set, so a missing JSON name shows up
// as an absent key rather than as a zero value that happens to marshal.
func populated() Manifest {
	actual := int64(43700)
	return Manifest{
		Version:   Version,
		Tool:      "0.2.0",
		CreatedAt: time.Date(2026, 9, 2, 3, 4, 5, 0, time.UTC),
		Torrent: Torrent{
			InfoHash: "e4d37e62d14ba96d29b9e760148803b458aee5b6", Name: "Sintel",
			PieceLength: 1 << 20, Private: false, Peers: 12, Seeds: 4,
			Availability: []int{3, 3, 0, 2},
		},
		File: File{Index: 6, Path: "Sintel/sintel-2048-stereo.mp4",
			Bytes: 282738688, DurationMS: 888000, Container: "mov,mp4,m4a"},
		Video: Video{Codec: "h264", Profile: "High", Width: 2048, Height: 872,
			FPS: 24, BitRate: 2545000, BitsPerPixel: 0.0594},
		Audio: []Audio{{Index: 1, Language: "eng", Codec: "aac",
			Channels: 6, Title: "Original", Default: true}},
		Subtitles: []Subtitle{{Index: 2, Language: "eng", Format: "subrip",
			Title: "Full", Forced: false, Default: true}},
		Frames: []Frame{
			{Index: 0, RequestedMS: 44400, ActualMS: &actual,
				Path: "frames/000.jpg", Shift: ShiftNone, Width: 2048, Height: 872},
			{Index: 1, RequestedMS: 86500, Shift: ShiftFailed, Error: "unavailable"},
		},
		Cost: Cost{DownloadedBytes: 49806540, ElapsedMS: 102900,
			LimitBytes: 157286400, LimitMS: 600000, LimitHit: "budget"},
	}
}

// TestManifestCarriesEveryRequiredField walks the encoded form for each field
// REQUIREMENTS.md section 2.8 asks for. Checking the JSON rather than the Go
// struct is the point: the JSON names are the contract, and renaming one is
// exactly the change that would otherwise pass unnoticed.
func TestManifestCarriesEveryRequiredField(t *testing.T) {
	raw, err := json.Marshal(populated())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, path := range []string{
		// per frame: requested and actual timecode, path, shift marker
		"frames.0.requested_ms", "frames.0.actual_ms", "frames.0.path", "frames.0.shift",
		// the run's cost and the ceilings it was held to
		"cost.downloaded_bytes", "cost.elapsed_ms",
		"cost.limit_bytes", "cost.limit_ms", "cost.limit_hit",
		// audio: language, codec, channels, title
		"audio.0.language", "audio.0.codec", "audio.0.channels", "audio.0.title",
		// subtitles: language, format, forced/default
		"subtitles.0.language", "subtitles.0.format",
		"subtitles.0.forced", "subtitles.0.default",
		// quality: bitrate, fps, codec/profile, resolution, bits per pixel
		"video.bit_rate", "video.fps", "video.codec", "video.profile",
		"video.width", "video.height", "video.bits_per_pixel",
		// torrent technicals: infohash, piece size, seeds/peers, availability
		"torrent.infohash", "torrent.piece_length", "torrent.peers",
		"torrent.seeds", "torrent.availability",
		// identity of the record itself
		"version", "tool", "created_at", "file.path", "file.index", "file.duration_ms",
	} {
		if _, ok := lookup(doc, path); !ok {
			t.Errorf("manifest has no %q - REQUIREMENTS.md section 2.8 asks for it", path)
		}
	}
}

// TestManifestDecodesIntoItsOwnType: the declared type is the schema, so an
// encoded manifest must round-trip through it with nothing left over.
func TestManifestDecodesIntoItsOwnType(t *testing.T) {
	raw, err := json.Marshal(populated())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var back Manifest
	if err := dec.Decode(&back); err != nil {
		t.Fatalf("a manifest does not fit its own type: %v", err)
	}
	if back.Version != Version || back.Torrent.InfoHash != populated().Torrent.InfoHash {
		t.Errorf("round trip lost data: %+v", back)
	}
	if back.Frames[1].ActualMS != nil {
		t.Error("a point that was never taken came back with a timestamp")
	}
	if back.Frames[0].ActualMS == nil || *back.Frames[0].ActualMS != 43700 {
		t.Error("a taken frame lost its actual timestamp")
	}
}

// TestShiftMarkersMatchTheRequirements pins the three names section 2.8 states.
// They are also the strings the core uses, so a rename here would silently
// change what a marker means on disk.
func TestShiftMarkersMatchTheRequirements(t *testing.T) {
	for marker, want := range map[Shift]string{
		ShiftNone: "", ShiftUnavailable: "shifted",
		ShiftBlank: "stepped", ShiftFailed: "unavailable",
	} {
		if string(marker) != want {
			t.Errorf("marker is %q, want %q", marker, want)
		}
	}
}

// lookup walks a dotted path through decoded JSON, where a numeric element
// indexes an array.
func lookup(doc any, path string) (any, bool) {
	current := doc
	for _, part := range strings.Split(path, ".") {
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[part]
			if !ok {
				return nil, false
			}
			current = value
		case []any:
			var idx int
			if _, err := fmtSscan(part, &idx); err != nil || idx >= len(node) {
				return nil, false
			}
			current = node[idx]
		default:
			return nil, false
		}
	}
	return current, true
}

func fmtSscan(s string, out *int) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errNotAnIndex
		}
		n = n*10 + int(r-'0')
	}
	*out = n
	return 1, nil
}

var errNotAnIndex = errNotIndex{}

type errNotIndex struct{}

func (errNotIndex) Error() string { return "not an index" }
