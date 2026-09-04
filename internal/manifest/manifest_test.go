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

// The tests below are TOR-60's: what a frame path means on disk, and where a
// reader is allowed to look for the file it names. None of them touches the
// filesystem - Resolved is asked what is present through a func, so the rule
// can be stated one case at a time instead of being inferred from a tree.

// present makes a set of paths look like files on disk.
func present(paths ...string) func(string) bool {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	return func(path string) bool { return set[path] }
}

// TestRelativeRecordsFramePathsAgainstTheirManifest is the writer's half of
// TOR-60: what goes on disk must be a path the manifest's own directory can
// resolve, so the tree survives being copied or moved.
func TestRelativeRecordsFramePathsAgainstTheirManifest(t *testing.T) {
	const dir = "/results/e4d3/1f34/00-episode-1"

	actual := int64(2000)
	m := Manifest{Frames: []Frame{
		{Index: 0, ActualMS: &actual, Path: dir + "/frames/000.jpg"},
		{Index: 1, Shift: ShiftFailed},
	}}

	got := m.Relative(dir)
	if got.Frames[0].Path != "frames/000.jpg" {
		t.Errorf("recorded %q, want %q - an absolute path pins the tree to this directory",
			got.Frames[0].Path, "frames/000.jpg")
	}
	if got.Frames[1].Path != "" {
		t.Errorf("a point that took no frame gained a path: %q", got.Frames[1].Path)
	}
	if m.Frames[0].Path != dir+"/frames/000.jpg" {
		t.Errorf("Relative rewrote the caller's own records: %q", m.Frames[0].Path)
	}
	if again := got.Relative(dir); again.Frames[0].Path != "frames/000.jpg" {
		t.Errorf("recording an already-recorded manifest changed it to %q", again.Frames[0].Path)
	}
}

// TestRelativeKeepsAPathItCannotSayRelatively: a record pointing outside the
// manifest's own directory is left exactly as it is. A "../../.." climbing out
// of the results tree would be a worse lie than the absolute path it replaced,
// and core.DeleteFrame refuses such a record on purpose.
func TestRelativeKeepsAPathItCannotSayRelatively(t *testing.T) {
	const dir = "/results/e4d3/1f34/00-episode-1"
	const elsewhere = "/somewhere/else/not-a-frame.jpg"

	got := Manifest{Frames: []Frame{{Path: elsewhere}}}.Relative(dir)
	if got.Frames[0].Path != elsewhere {
		t.Errorf("path = %q, want it left at %q", got.Frames[0].Path, elsewhere)
	}
}

// TestResolvedReadsBothShapes walks every shape a manifest on disk can hold
// against the one rule: look inside the manifest's own directory first, fall
// back to an older record's absolute path only when nothing is there.
func TestResolvedReadsBothShapes(t *testing.T) {
	const dir = "/results/copy/00-episode-1"
	const old = "/results/original/00-episode-1/frames/000.jpg"

	for _, tc := range []struct {
		name     string
		recorded string
		onDisk   func(string) bool
		want     string
	}{{
		name:     "a recorded path resolves against the manifest that names it",
		recorded: "frames/000.jpg",
		onDisk:   present(dir + "/frames/000.jpg"),
		want:     dir + "/frames/000.jpg",
	}, {
		name:     "an older absolute record is re-anchored onto the copy it was found in",
		recorded: old,
		onDisk:   present(dir+"/frames/000.jpg", old),
		want:     dir + "/frames/000.jpg",
	}, {
		name:     "an older absolute record still reads in place",
		recorded: old,
		onDisk:   present(old),
		want:     old,
	}, {
		name:     "a path recorded relative to a working directory is re-anchored too",
		recorded: "torpeek-out/e4d3/1f34/00-episode-1/frames/000.jpg",
		onDisk:   present(dir + "/frames/000.jpg"),
		want:     dir + "/frames/000.jpg",
	}, {
		name:     "a frame that is nowhere reads as the manifest's own missing file",
		recorded: "frames/000.jpg",
		onDisk:   present(),
		want:     dir + "/frames/000.jpg",
	}, {
		name:     "an older record naming a file outside the run is left to be refused",
		recorded: "/somewhere/else/not-a-frame.jpg",
		onDisk:   present("/somewhere/else/not-a-frame.jpg"),
		want:     "/somewhere/else/not-a-frame.jpg",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := Manifest{Frames: []Frame{{Path: tc.recorded}}}.Resolved(dir, tc.onDisk)
			if got.Frames[0].Path != tc.want {
				t.Errorf("resolved %q to %q, want %q", tc.recorded, got.Frames[0].Path, tc.want)
			}
		})
	}
}

// TestResolvedNeverAnswersWithAWorkingDirectoryPath is the one way this could
// still open a file nobody pointed at: "frames/000.jpg" is a valid path
// relative to wherever the process happens to be running, and some unrelated
// directory may well have a frames/000.jpg in it. A relative record is only
// ever read against the manifest's own directory.
func TestResolvedNeverAnswersWithAWorkingDirectoryPath(t *testing.T) {
	const dir = "/results/copy/00-episode-1"
	const recorded = "frames/000.jpg"

	// Everything except the manifest's own copy exists, the bare record
	// included.
	got := Manifest{Frames: []Frame{{Path: recorded}}}.Resolved(dir, present(recorded))
	if got.Frames[0].Path != dir+"/"+recorded {
		t.Errorf("resolved to %q, want %q", got.Frames[0].Path, dir+"/"+recorded)
	}
}

// TestResolvedWithoutAPresenceCheck: a nil present is "nothing is on disk",
// which must still produce the honest reading of each shape rather than a
// panic or an empty path.
func TestResolvedWithoutAPresenceCheck(t *testing.T) {
	const dir = "/results/copy/00-episode-1"

	got := Manifest{Frames: []Frame{
		{Index: 0, Path: "frames/000.jpg"},
		{Index: 1, Path: "/results/original/00-episode-1/frames/001.jpg"},
	}}.Resolved(dir, nil)

	if got.Frames[0].Path != dir+"/frames/000.jpg" {
		t.Errorf("recorded path read as %q", got.Frames[0].Path)
	}
	if got.Frames[1].Path != "/results/original/00-episode-1/frames/001.jpg" {
		t.Errorf("absolute path read as %q", got.Frames[1].Path)
	}
}
