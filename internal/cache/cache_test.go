package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/manifest"
)

// populated is a run record with every field set, Source and Plan included.
func populated() Run {
	return Run{
		Version:   Version,
		Tool:      "0.5.0",
		CreatedAt: time.Date(2026, 9, 2, 3, 4, 5, 0, time.UTC),
		Source:    "magnet:?xt=urn:btih:e4d37e62d14ba96d29b9e760148803b458aee5b6&dn=Sintel",
		InfoHash:  "e4d37e62d14ba96d29b9e760148803b458aee5b6",
		Name:      "Sintel",
		Private:   false,
		Plan: Plan{
			Count: 20, Start: 0.05, End: 0.95,
			Profile: "min-time", Format: "jpeg", Sequential: false,
		},
		Videos: []File{
			{Index: 0, Path: "Sintel/sintel.mp4", Bytes: 282738688, Offset: 0},
		},
		Selected: []int{0},
		Complete: []int{0},
	}
}

// TestSaveRunRoundTripsSourceAndPlan is the acceptance test for TOR-52: a run
// written now must carry the source it came from and its plan in a form a
// directory name cannot be turned back into.
func TestSaveRunRoundTripsSourceAndPlan(t *testing.T) {
	dir := t.TempDir()
	want := populated()

	if err := SaveRun(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok := LoadRun(dir)
	if !ok {
		t.Fatal("a record just written was not a hit")
	}
	if got.Source != want.Source {
		t.Errorf("source = %q, want %q", got.Source, want.Source)
	}
	if got.Plan != want.Plan {
		t.Errorf("plan = %+v, want %+v", got.Plan, want.Plan)
	}
}

// TestRunRecordsSourceAndPlanUnderTheirJSONNames pins the on-disk field names,
// the same way manifest_test.go pins the manifest's - a rename here would
// silently stop a run from being nameable or repeatable.
func TestRunRecordsSourceAndPlanUnderTheirJSONNames(t *testing.T) {
	raw, err := json.Marshal(populated())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := doc["source"]; !ok {
		t.Error("record has no \"source\" - a run found on disk cannot be re-run without it")
	}
	plan, ok := doc["plan"].(map[string]any)
	if !ok {
		t.Fatal("record has no \"plan\" object")
	}
	for _, field := range []string{"count", "start", "end", "profile", "format", "sequential"} {
		if _, ok := plan[field]; !ok {
			t.Errorf("plan has no %q", field)
		}
	}
}

// TestSaveRunRoundTripsSelected is the acceptance test for TOR-65's first
// change: a record written now must say which files the run took on, not
// only which torrent files exist (Videos) and which came out whole
// (Complete).
func TestSaveRunRoundTripsSelected(t *testing.T) {
	dir := t.TempDir()
	want := populated()

	if err := SaveRun(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok := LoadRun(dir)
	if !ok {
		t.Fatal("a record just written was not a hit")
	}
	if len(got.Selected) != len(want.Selected) || got.Selected[0] != want.Selected[0] {
		t.Errorf("selected = %v, want %v", got.Selected, want.Selected)
	}
}

// TestRunRecordsSelectedUnderItsJSONName pins the on-disk field name the same
// way TestRunRecordsSourceAndPlanUnderTheirJSONNames pins source and plan - a
// rename here would silently stop a rerun from telling "never selected" apart
// from "selected and failed".
func TestRunRecordsSelectedUnderItsJSONName(t *testing.T) {
	raw, err := json.Marshal(populated())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := doc["selected"]; !ok {
		t.Error("record has no \"selected\" - a rerun cannot tell never-selected from selected-and-failed without it")
	}
}

// run04Shape is what saveRunRecord wrote before this task: no source, no
// plan. It is typed by hand rather than built from Run{} with fields zeroed
// out, so a future field added to Run cannot silently widen what this test
// claims to be the 0.4.0 shape.
const run04Shape = `{
  "version": 1,
  "tool": "0.4.0",
  "created_at": "2026-08-01T12:00:00Z",
  "infohash": "e4d37e62d14ba96d29b9e760148803b458aee5b6",
  "name": "Sintel",
  "private": false,
  "videos": [
    {"index": 0, "path": "Sintel/sintel.mp4", "bytes": 282738688, "offset": 0}
  ],
  "complete": [0]
}`

// TestLoadRunAcceptsA04ShapedRecord is the regression this task exists to
// prevent: adding Source and Plan without bumping Version, so a record a
// 0.4.0 build wrote is still a cache hit rather than being invalidated and
// refetched from the swarm. A field absent from the older JSON must read
// back as its zero value, not turn the whole record into a miss.
func TestLoadRunAcceptsA04ShapedRecord(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Name), []byte(run04Shape), 0o600); err != nil {
		t.Fatalf("write 0.4.0-shaped record: %v", err)
	}

	got, ok := LoadRun(dir)
	if !ok {
		t.Fatal("a 0.4.0-shaped run.json was a miss; it must still be a hit")
	}
	if got.Source != "" {
		t.Errorf("source = %q, want \"\" - it was never on disk", got.Source)
	}
	if got.Plan != (Plan{}) {
		t.Errorf("plan = %+v, want the zero value - it was never on disk", got.Plan)
	}
	if got.InfoHash != "e4d37e62d14ba96d29b9e760148803b458aee5b6" || got.Name != "Sintel" {
		t.Errorf("fields the 0.4.0 record did carry were lost: %+v", got)
	}
	if len(got.Videos) != 1 || got.Videos[0].Path != "Sintel/sintel.mp4" {
		t.Errorf("videos lost: %+v", got.Videos)
	}
	if len(got.Complete) != 1 || got.Complete[0] != 0 {
		t.Errorf("complete lost: %+v", got.Complete)
	}
	if got.Selected != nil {
		t.Errorf("selected = %v, want nil - a 0.4.0 record never wrote it", got.Selected)
	}
}

// run06Shape is what saveRunRecord wrote before this task added Selected:
// Source and Plan (TOR-52) are present, but no "selected" key. Modeled on a
// real record this build's own predecessor wrote (see queue53/out in this
// task's scratchpad) rather than invented, so it reflects an actual disk
// shape rather than the test author's guess at one.
const run06Shape = `{
  "version": 1,
  "tool": "0.5.0",
  "created_at": "2026-09-02T19:17:59.577415Z",
  "source": "magnet:?xt=urn:btih:e4d37e62d14ba96d29b9e760148803b458aee5b6&dn=Sintel",
  "infohash": "e4d37e62d14ba96d29b9e760148803b458aee5b6",
  "name": "Sintel",
  "private": false,
  "plan": {
    "count": 1,
    "start": 0.05,
    "end": 0.95,
    "profile": "min-traffic",
    "format": "jpeg",
    "sequential": false
  },
  "videos": [
    {"index": 2, "path": "Sintel/Sintel_Documentary_by_Ali_Boubred.avi", "bytes": 961242162, "offset": 463671},
    {"index": 6, "path": "Sintel/sintel-2048-stereo.mp4", "bytes": 282690045, "offset": 962032655}
  ],
  "complete": [6, 2]
}`

// TestLoadRunAcceptsA06ShapedRecord is the acceptance criterion for TOR-65's
// trap: adding Selected without bumping Version, so a record written before
// Selected existed is still a cache hit rather than a miss that sends
// everything back to the swarm. A field absent from the older JSON must read
// back as its zero value (nil), not turn the whole record into a miss.
func TestLoadRunAcceptsA06ShapedRecord(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Name), []byte(run06Shape), 0o600); err != nil {
		t.Fatalf("write pre-Selected record: %v", err)
	}

	got, ok := LoadRun(dir)
	if !ok {
		t.Fatal("a pre-Selected run.json was a miss; it must still be a hit")
	}
	if got.Selected != nil {
		t.Errorf("selected = %v, want nil - it was never on disk", got.Selected)
	}
	if got.Source == "" || got.Plan == (Plan{}) {
		t.Errorf("fields this record did carry were lost: source=%q plan=%+v", got.Source, got.Plan)
	}
	if len(got.Complete) != 2 {
		t.Errorf("complete lost: %+v", got.Complete)
	}
}

// run080Shape is a REAL run.json written by the actual 0.8.0 build - checked
// out at tag v0.8.0 in a separate worktree and run once against a local
// swarm with a .torrent source, not typed out from this task's own idea of
// what TOR-73 wrote - the same discipline manifest07Shape documents for the
// manifest side. "source" is TOR-73's shape: the absolute path of the
// .torrent copy the run saved in its own directory
// (output.Layout.TorrentPath). It names a temp directory that this build
// deleted the moment the capture test ended, which is the point: this is
// exactly what TOR-86 calls a path to nothing, preserved here so a reader can
// be tested against it rather than a paraphrase of it.
const run080Shape = `{
  "version": 1,
  "tool": "0.8.0",
  "created_at": "2026-09-04T09:22:59.13528Z",
  "source": "/var/folders/ps/r478n82d1zb82phf7k1dsy6w0000gp/T/TestCaptureRunFixtureTOR862853530860/003/d7ed5220021b0170ae969e532c268ca884b357f5/1f34641acf26018c/d7ed5220021b0170ae969e532c268ca884b357f5.torrent",
  "infohash": "d7ed5220021b0170ae969e532c268ca884b357f5",
  "name": "001",
  "private": false,
  "plan": {
    "count": 3,
    "start": 0.1,
    "end": 0.9,
    "profile": "min-traffic",
    "format": "jpeg",
    "sequential": false
  },
  "videos": [
    {
      "index": 0,
      "path": "001/episode-1.mkv",
      "bytes": 160471,
      "offset": 0
    }
  ],
  "selected": [
    0
  ],
  "complete": [
    0
  ]
}`

// TestLoadRunAcceptsAn080ShapedRecord is TOR-86's acceptance criterion for the
// format: cache.Run's shape did not change (Source is still a plain string,
// under the same JSON key), only what a NEW record puts in that string, so a
// record written by 0.8.0 must keep reading exactly as it always did - Version
// unchanged, every field intact, Source included even though it is now a
// stale absolute path rather than the magnet a build made after TOR-86 would
// have written. Nothing about LoadRun tells the two shapes of Source apart,
// because nothing needs to: this field is only ever displayed and pasted (see
// its own doc comment), never parsed or joined by this package, so there is
// no shape for a reader to detect - the same trap TOR-52 documented for this
// very struct.
func TestLoadRunAcceptsAn080ShapedRecord(t *testing.T) {
	if !strings.Contains(run080Shape, `"tool": "0.8.0"`) {
		t.Fatal("the fixture is no longer an 0.8.0 record - it must stay the captured one")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Name), []byte(run080Shape), 0o600); err != nil {
		t.Fatalf("write the 0.8.0 record: %v", err)
	}

	got, ok := LoadRun(dir)
	if !ok {
		t.Fatal("an 0.8.0-shaped run.json was a miss; it must still be a hit")
	}
	const wantSource = "/var/folders/ps/r478n82d1zb82phf7k1dsy6w0000gp/T/TestCaptureRunFixtureTOR862853530860/" +
		"003/d7ed5220021b0170ae969e532c268ca884b357f5/1f34641acf26018c/d7ed5220021b0170ae969e532c268ca884b357f5.torrent"
	if got.Source != wantSource {
		t.Errorf("source = %q, want the recorded path unchanged: %q", got.Source, wantSource)
	}
	if got.InfoHash != "d7ed5220021b0170ae969e532c268ca884b357f5" || got.Name != "001" {
		t.Errorf("fields the 0.8.0 record did carry were lost: %+v", got)
	}
	if got.Plan.Count != 3 || got.Plan.Profile != "min-traffic" {
		t.Errorf("plan lost: %+v", got.Plan)
	}
	if len(got.Videos) != 1 || got.Videos[0].Path != "001/episode-1.mkv" {
		t.Errorf("videos lost: %+v", got.Videos)
	}
	if len(got.Selected) != 1 || len(got.Complete) != 1 {
		t.Errorf("selected/complete lost: selected=%v complete=%v", got.Selected, got.Complete)
	}
}

// The tests below are TOR-60's at the seam that reads a manifest: LoadManifest
// is the only place in the project that parses one, so it is the only place
// that can turn a recorded frame path back into a file, and the only place
// that can get it wrong for everybody at once.

// writeManifestAt puts a manifest on disk with the frame paths given exactly
// as written - no writer, no relativizing - because these tests are about what
// a reader does with a shape it finds, including shapes only an older release
// wrote.
func writeManifestAt(t *testing.T, dir string, paths ...string) {
	t.Helper()

	m := manifest.Manifest{
		Version: manifest.Version,
		Tool:    "test",
		File:    manifest.File{Index: 0, Path: "season/episode-1.mkv", Bytes: 1 << 20},
		Video:   manifest.Video{Codec: "h264", Width: 640, Height: 360},
	}
	for i, p := range paths {
		ms := int64(i * 1000)
		m.Frames = append(m.Frames, manifest.Frame{
			Index: i, RequestedMS: ms, ActualMS: &ms, Path: p, Width: 640, Height: 360,
		})
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create file directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifest.Name), data, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// writeFrames puts frame files under dir/frames, each carrying content so two
// copies of one tree can be told apart by what was actually read.
func writeFrames(t *testing.T, dir, content string, count int) []string {
	t.Helper()

	frames := filepath.Join(dir, "frames")
	if err := os.MkdirAll(frames, 0o755); err != nil {
		t.Fatalf("create frames directory: %v", err)
	}
	var paths []string
	for i := 0; i < count; i++ {
		path := filepath.Join(frames, fmt.Sprintf("%03d.jpg", i))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
		paths = append(paths, path)
	}
	return paths
}

// TestLoadManifestResolvesAgainstTheDirectoryItCameFrom is the reader's half
// of TOR-60. The manifest is written once and read from two different
// directories - the second one built by moving the first, the way a backup or
// a sync to another machine restores a results tree - and both must answer
// with frames that are actually there.
func TestLoadManifestResolvesAgainstTheDirectoryItCameFrom(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first", "00-episode-1")
	writeManifestAt(t, first, "frames/000.jpg", "frames/001.jpg")
	writeFrames(t, first, "jpeg-bytes", 2)

	m, ok := LoadManifest(first)
	if !ok {
		t.Fatal("a manifest just written was not readable")
	}
	for _, f := range m.Frames {
		if !filepath.IsAbs(f.Path) || !strings.HasPrefix(f.Path, first) {
			t.Errorf("frame %d resolved to %q, want a path under %q", f.Index, f.Path, first)
		}
	}
	if !Usable(m) {
		t.Fatal("a manifest whose frames are all on disk was called unusable")
	}

	second := filepath.Join(root, "second", "00-episode-1")
	if err := os.MkdirAll(filepath.Dir(second), 0o755); err != nil {
		t.Fatalf("create the destination: %v", err)
	}
	if err := os.Rename(first, second); err != nil {
		t.Fatalf("move the results: %v", err)
	}

	moved, ok := LoadManifest(second)
	if !ok {
		t.Fatal("a manifest that had been moved was not readable")
	}
	for _, f := range moved.Frames {
		if !strings.HasPrefix(f.Path, second) {
			t.Errorf("frame %d of the moved tree resolved to %q, want a path under %q",
				f.Index, f.Path, second)
		}
	}
	if !Usable(moved) {
		t.Fatal("a moved results tree was called unusable - it is all there")
	}
}

// TestLoadManifestPrefersTheCopyToTheOriginalItWasCopiedFrom is the failure
// the ticket called worse than the loud one, at the level it is decided: a
// manifest written before 0.8.0 holds the absolute path the frame had when it
// was taken, so a copy of that tree, read while the original is still sitting
// there, used to answer with the ORIGINAL's files. Nothing said so; the person
// was simply looking at files they had not pointed the tool at.
func TestLoadManifestPrefersTheCopyToTheOriginalItWasCopiedFrom(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original", "00-episode-1")
	recorded := writeFrames(t, original, "the original", 2)
	// The absolute shape, exactly as every release up to 0.7.0 wrote it.
	writeManifestAt(t, original, recorded...)

	copied := filepath.Join(root, "copy", "00-episode-1")
	copyTree(t, original, copied)
	// Distinguishable content, so which tree answered is a fact rather than
	// an inference from a path.
	writeFrames(t, copied, "the copy", 2)

	m, ok := LoadManifest(copied)
	if !ok {
		t.Fatal("the copy's manifest was not readable")
	}
	for _, f := range m.Frames {
		if strings.HasPrefix(f.Path, original) {
			t.Fatalf("frame %d of the copy served %q - the original is still there and was read instead",
				f.Index, f.Path)
		}
		data, err := os.ReadFile(f.Path)
		if err != nil {
			t.Fatalf("read frame %d at %s: %v", f.Index, f.Path, err)
		}
		if string(data) != "the copy" {
			t.Errorf("frame %d read %q, want the copy's own bytes", f.Index, data)
		}
	}

	// And with the original gone the copy still stands on its own, which is
	// the loud half of the same bug.
	if err := os.RemoveAll(filepath.Join(root, "original")); err != nil {
		t.Fatalf("remove the original: %v", err)
	}
	if alone, ok := LoadManifest(copied); !ok || !Usable(alone) {
		t.Fatalf("the copy stopped being usable when the original was deleted (ok=%v)", ok)
	}
}

// copyTree copies a directory recursively, the way somebody would copy a
// results directory to another disk.
func copyTree(t *testing.T, from, to string) {
	t.Helper()

	if err := filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	}); err != nil {
		t.Fatalf("copy %s to %s: %v", from, to, err)
	}
}

// TOR-119's record: where the run reached, not only how much it took.

func TestSaveRunRoundTripsClaimed(t *testing.T) {
	dir := t.TempDir()
	want := [][2]int{{0, 2}, {17, 19}, {265, 270}}
	if err := SaveRun(dir, Run{Version: Version, Claimed: want}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	got, ok := LoadRun(dir)
	if !ok {
		t.Fatal("LoadRun missed a record it had just written")
	}
	if len(got.Claimed) != len(want) {
		t.Fatalf("claimed = %v, want %v", got.Claimed, want)
	}
	for i := range want {
		if got.Claimed[i] != want[i] {
			t.Fatalf("claimed = %v, want %v", got.Claimed, want)
		}
	}
}

// TestRunRecordsClaimedAsPairsNotObjects pins the ON-DISK shape, because the
// shape is the decision: a pair array is a quarter of the bytes of a struct
// per range, on a record that weighs a few hundred. A future refactor to named
// fields would be a silent multiplication of every run record.
func TestRunRecordsClaimedAsPairsNotObjects(t *testing.T) {
	dir := t.TempDir()
	if err := SaveRun(dir, Run{Version: Version, Claimed: [][2]int{{0, 2}, {17, 19}}}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, Name))
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	text := string(data)

	if !strings.Contains(text, `"claimed"`) {
		t.Errorf("the record does not carry a claimed field:\n%s", text)
	}
	// The exact indentation is the marshaller's business; what matters is the
	// numbers sitting as bare numbers under the key. Checked on the claimed
	// VALUE and not on the whole file: Plan has its own "end" key, so a
	// file-wide search for object syntax reports the wrong field.
	compact := strings.Join(strings.Fields(text), "")
	if !strings.Contains(compact, `"claimed":[[0,2],[17,19]]`) {
		t.Errorf("claimed is not the compact pair array, which is a quarter of the "+
			"bytes of a struct per range; got, whitespace removed:\n%s", compact)
	}
}

// TestAClaimlessRunWritesNoClaimedFieldAtAll keeps a run that ordered nothing
// from asserting that it ordered zero pieces. Absent and empty are different
// answers, and only one of them is honest for a run served from cache.
func TestAClaimlessRunWritesNoClaimedFieldAtAll(t *testing.T) {
	dir := t.TempDir()
	if err := SaveRun(dir, Run{Version: Version}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, Name))
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	if strings.Contains(string(data), "claimed") {
		t.Errorf("a run with no claims still wrote a claimed field:\n%s", data)
	}

	got, _ := LoadRun(dir)
	if got.Claimed != nil {
		t.Errorf("claimed read back as %v, want nil", got.Claimed)
	}
}

// TestAnOlderRecordReadsBackWithNoClaim is the TOR-52 trap for this field.
// Claimed was added WITHOUT bumping Version, so every record already on disk
// stays a hit and simply has nothing to say about where its run reached -
// which is the truth. Bumping would have turned all of them into misses.
func TestAnOlderRecordReadsBackWithNoClaim(t *testing.T) {
	if strings.Contains(run080Shape, "claimed") {
		t.Fatal("the 0.8.0 fixture has grown a claimed field; it must stay the captured record")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Name), []byte(run080Shape), 0o600); err != nil {
		t.Fatalf("write the 0.8.0 record: %v", err)
	}

	got, ok := LoadRun(dir)
	if !ok {
		t.Fatal("a record written before Claimed existed became a miss - the field was " +
			"added without bumping Version precisely so this could not happen")
	}
	if got.Claimed != nil {
		t.Errorf("an older record claims to have reached %v; it cannot know", got.Claimed)
	}
	if got.InfoHash == "" || got.Plan.Count == 0 {
		t.Errorf("adding Claimed cost the older record its other fields: %+v", got)
	}
}

// TOR-124: Usable asks about the disk, and only about the disk.

// usableFixture writes n frames into dir and returns a manifest describing
// them, with the indices in failed recorded the way the engine records a point
// that produced nothing - no path, ShiftFailed, a code in Error.
func usableFixture(t *testing.T, dir string, n int, failed ...int) manifest.Manifest {
	t.Helper()

	out := map[int]bool{}
	for _, i := range failed {
		out[i] = true
	}

	frames := make([]manifest.Frame, n)
	for i := range frames {
		ms := int64(i * 1000)
		frames[i] = manifest.Frame{Index: i, RequestedMS: ms, ActualMS: &ms, Width: 640, Height: 360}
		if out[i] {
			frames[i].Shift = manifest.ShiftFailed
			frames[i].Error = "read_stalled"
			continue
		}
		path := filepath.Join(dir, "frame-"+strconv.Itoa(i)+".jpg")
		if err := os.WriteFile(path, []byte{0xFF, 0xD8, byte(i)}, 0o600); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
		frames[i].Path = path
	}
	return manifest.Manifest{Version: manifest.Version, Frames: frames}
}

func TestUsableAcceptsARunWithUnreachableCapturePoints(t *testing.T) {
	dir := t.TempDir()
	m := usableFixture(t, dir, 12, 5, 6, 7, 8, 9, 10, 11)

	if !Usable(m) {
		t.Error("a manifest with five frames on disk and seven points the engine could " +
			"not reach was refused. Those seven have no file to have lost, so they say " +
			"nothing about the disk - and refusing on them is what made a holed run " +
			"impossible to reopen (TOR-124)")
	}
}

func TestUsableStillRefusesAFrameGoneFromDisk(t *testing.T) {
	dir := t.TempDir()
	m := usableFixture(t, dir, 4)

	if !Usable(m) {
		t.Fatal("the fixture is not usable to begin with")
	}
	if err := os.Remove(m.Frames[2].Path); err != nil {
		t.Fatalf("remove a frame: %v", err)
	}
	if Usable(m) {
		t.Error("a manifest promising a frame the disk no longer has was accepted; " +
			"that is the one question this function exists to answer")
	}
}

func TestUsableRefusesAManifestWithNoFrameOnDiskAtAll(t *testing.T) {
	dir := t.TempDir()

	// Every point failed: the run produced nothing, so there is nothing to
	// show - a floor, not a completeness test.
	all := usableFixture(t, dir, 3, 0, 1, 2)
	if Usable(all) {
		t.Error("a manifest whose every point failed was accepted; one frame of twelve " +
			"is a partial result, none of twelve is not a result")
	}

	if Usable(manifest.Manifest{Version: manifest.Version}) {
		t.Error("a manifest with no frames at all was accepted")
	}
}

// TestUsableJudgesTheDiskNotTheRecord states the split in one place: the same
// manifest is usable or not purely on whether its frames are there, and the
// failed points are constant across both arms.
func TestUsableJudgesTheDiskNotTheRecord(t *testing.T) {
	dir := t.TempDir()
	m := usableFixture(t, dir, 6, 4, 5)

	if !Usable(m) {
		t.Fatal("four frames on disk, two unreachable points: want usable")
	}
	for _, f := range m.Frames {
		if f.Path == "" {
			continue
		}
		if err := os.Remove(f.Path); err != nil {
			t.Fatalf("remove %s: %v", f.Path, err)
		}
	}
	if Usable(m) {
		t.Error("the same manifest with its frames deleted is still usable; the " +
			"judgement is not reading the disk")
	}
}
