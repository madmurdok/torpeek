package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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
}
