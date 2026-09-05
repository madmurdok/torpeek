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

// TestTrafficReportsBothFiguresAndTheGap is the shape TOR-94 exists to
// produce: three rows, with the ceiling attached to the one that decided the
// verdict and the other explicitly marked as not having decided anything.
func TestTrafficReportsBothFiguresAndTheGap(t *testing.T) {
	// The figures TOR-88 measured on rep 8: a deterministic 44-piece order,
	// 60.8 MiB arriving, 16.8 MiB of it unasked.
	traffic := Traffic{
		ClaimedByte:    44 << 20,
		ClaimedPieces:  44,
		DownloadedByte: 60*(1<<20) + 838861,
		Ceiling:        60 << 20,
		JudgedOn:       OnClaimed,
	}

	if got, want := traffic.Judged(), int64(44<<20); got != want {
		t.Errorf("Judged() = %d, want the claimed %d - the verdict must not be taken on arrivals", got, want)
	}

	rows := traffic.Measurements()
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want claimed, downloaded and the gap: %+v", len(rows), rows)
	}
	byName := map[string]string{}
	for _, m := range rows {
		byName[m.Name] = m.Value
	}

	claimed := byName["claimed"]
	switch {
	case !strings.Contains(claimed, "44.0 MiB"):
		t.Errorf("claimed row does not say what was ordered: %q", claimed)
	case !strings.Contains(claimed, "44 pieces"):
		t.Errorf("claimed row drops the piece count, which is the deterministic part: %q", claimed)
	case !strings.Contains(claimed, "ceiling of 60.0 MiB"):
		t.Errorf("the ceiling is not on the row it applies to: %q", claimed)
	}

	downloaded := byName["downloaded"]
	switch {
	case !strings.Contains(downloaded, "60.8 MiB"):
		t.Errorf("downloaded row does not say what arrived: %q", downloaded)
	case !strings.Contains(downloaded, "not judged"):
		t.Errorf("downloaded row does not say it decided nothing, which is how a reader "+
			"comes to think the ceiling protects it: %q", downloaded)
	case strings.Contains(downloaded, "ceiling"):
		t.Errorf("the ceiling is repeated on a row it does not apply to: %q", downloaded)
	}

	gap := byName["unclaimed arrivals"]
	if !strings.Contains(gap, "+16.8 MiB") {
		t.Errorf("gap row = %q, want the +16.8 MiB between the two figures", gap)
	}
	if !strings.Contains(gap, "38.2%") {
		t.Errorf("gap row does not scale the gap against the claim: %q", gap)
	}
}

// TestTrafficJudgedOnArrivalsKeepsCriterion1AsItWas: the other arm. Without
// this, "judged on the claim" could be hard-coded and nothing would notice.
func TestTrafficJudgedOnArrivalsKeepsCriterion1AsItWas(t *testing.T) {
	traffic := Traffic{
		ClaimedByte:    84 << 20,
		ClaimedPieces:  84,
		DownloadedByte: 101*(1<<20) + 629146,
		Ceiling:        150 << 20,
		JudgedOn:       OnDownloaded,
	}

	if got, want := traffic.Judged(), traffic.DownloadedByte; got != want {
		t.Errorf("Judged() = %d, want the downloaded %d", got, want)
	}

	byName := map[string]string{}
	for _, m := range traffic.Measurements() {
		byName[m.Name] = m.Value
	}
	if got := byName["downloaded"]; !strings.Contains(got, "ceiling of 150.0 MiB") {
		t.Errorf("downloaded row does not carry the ceiling it is judged against: %q", got)
	}
	if got := byName["claimed"]; !strings.Contains(got, "not judged") {
		t.Errorf("claimed row does not say it decided nothing here: %q", got)
	}
}

// TestTrafficGapGoesBothWays: fewer bytes can arrive than were ordered - a
// piece already on disk, or a run that ended before its last order landed -
// and a report that rendered that as "+-2.0 MiB" or as a positive number
// would be lying about the direction.
func TestTrafficGapGoesBothWays(t *testing.T) {
	traffic := Traffic{ClaimedByte: 15 << 20, ClaimedPieces: 15, DownloadedByte: 13 << 20}

	var gap string
	for _, m := range traffic.Measurements() {
		if m.Name == "unclaimed arrivals" {
			gap = m.Value
		}
	}
	if !strings.HasPrefix(gap, "-2.0 MiB") {
		t.Errorf("gap row = %q, want it to lead with -2.0 MiB", gap)
	}
}

// TestMeasurementsSurviveTheReport keeps the rows and the renderer honest
// together: the three figures have to reach the file, not just the struct.
func TestMeasurementsSurviveTheReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "one-rep.md")

	var r Report
	r.Tool = "9.9.9"
	r.Add(Result{Number: 2, Title: "min-traffic", Verdict: Met,
		Measured: Traffic{
			ClaimedByte: 44 << 20, ClaimedPieces: 44,
			DownloadedByte: 60 << 20, Ceiling: 60 << 20, JudgedOn: OnClaimed,
		}.Measurements()})

	if _, err := r.WriteFor(path, filepath.Join(dir, "9.9.9-acceptance.md")); err != nil {
		t.Fatalf("write: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, want := range []string{"claimed", "44 pieces", "downloaded", "unclaimed arrivals", "not judged"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the written report does not contain %q:\n%s", want, body)
		}
	}
}
