package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file carries no acceptance build tag on purpose: it goes nowhere near
// a swarm, and the thing it guards - what lands in docs/results - is worth
// checking on every `make check` rather than only on the rare afternoon
// somebody runs the criteria for real.

// TestAPartialRunWritesNoReleaseReport is TOR-96's acceptance criterion, and
// it asserts on what is on disk rather than on what WriteFor returned: the
// defect was a file existing, not a value.
func TestAPartialRunWritesNoReleaseReport(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "9.9.9-acceptance.md")

	var r Report
	r.Tool = "9.9.9"
	r.Add(Result{Number: 2, Title: "min-traffic", Verdict: Met})

	path, err := r.WriteFor("", release)
	if err == nil {
		t.Fatalf("a 1-of-%d run was allowed to write %s", Criteria, path)
	}
	if path != "" {
		t.Errorf("refused but still named a path: %q", path)
	}
	if _, statErr := os.Stat(release); !os.IsNotExist(statErr) {
		t.Errorf("%s exists after a refused write (stat err %v) - "+
			"the whole defect was a file that reads as the release's own evidence", release, statErr)
	}
	// The message has to say how to get the subset written, or the refusal
	// just looks like a bug to whoever hits it.
	if !strings.Contains(err.Error(), "-report") {
		t.Errorf("refusal does not mention -report, so it does not say what to do instead: %v", err)
	}
}

// TestAnEmptyRunWritesNoReleaseReport is the case that actually happened: a
// -run filter matching nothing still produced a file with a header and zero
// rows.
func TestAnEmptyRunWritesNoReleaseReport(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "9.9.9-acceptance.md")

	var r Report
	if _, err := r.WriteFor("", release); err == nil {
		t.Fatal("a run with no results at all was allowed to write the release report")
	}
	if _, err := os.Stat(release); !os.IsNotExist(err) {
		t.Errorf("%s exists after a run that executed nothing", release)
	}
}

// TestACompleteRunWritesTheReleaseReport is the other arm: the guard has to
// let a real run through, or it would have been noticed differently.
func TestACompleteRunWritesTheReleaseReport(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "9.9.9-acceptance.md")

	var r Report
	r.Tool = "9.9.9"
	for i := 1; i <= Criteria; i++ {
		r.Add(Result{Number: i, Title: "criterion", Verdict: Met,
			Measured: []Measurement{Measure("downloaded", "%.1f MiB", 46.0)}})
	}

	path, err := r.WriteFor("", release)
	if err != nil {
		t.Fatalf("a complete run was refused: %v", err)
	}
	if path != release {
		t.Errorf("wrote %q, want %q", path, release)
	}
	body, err := os.ReadFile(release)
	if err != nil {
		t.Fatalf("reading what was written: %v", err)
	}
	if n := strings.Count(string(body), "## "); n != Criteria {
		t.Errorf("report has %d criterion sections, want %d:\n%s", n, Criteria, body)
	}
}

// TestAnExplicitPathIsHonouredForASubset keeps the escape hatch working: this
// is how a spread of reps gets measured without touching docs/results
// (TOR-88 took forty).
func TestAnExplicitPathIsHonouredForASubset(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "scratch", "one-rep.md")
	release := filepath.Join(dir, "9.9.9-acceptance.md")

	var r Report
	r.Tool = "9.9.9"
	r.Add(Result{Number: 2, Title: "min-traffic", Verdict: Met})

	path, err := r.WriteFor(explicit, release)
	if err != nil {
		t.Fatalf("an explicitly named path was refused: %v", err)
	}
	if path != explicit {
		t.Errorf("wrote %q, want the explicit %q", path, explicit)
	}
	if _, err := os.Stat(explicit); err != nil {
		t.Errorf("nothing at the explicit path: %v", err)
	}
	if _, err := os.Stat(release); !os.IsNotExist(err) {
		t.Errorf("%s was written too, which is the file this must never touch", release)
	}
}
