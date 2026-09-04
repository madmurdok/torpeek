// Package acceptance runs the seven acceptance criteria from REQUIREMENTS.md
// section 8 and writes down what they measured.
//
// It exists as its own package, behind a build tag, because these are not unit
// tests: five of the seven go to a live public swarm, take minutes and cost
// real traffic. `make check` must stay something anyone can run.
package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Criteria is how many acceptance criteria REQUIREMENTS.md section 8 states.
//
// It is a constant rather than something counted at runtime because it is the
// number a report has to be judged against: a run that produced six results
// cannot be told apart from a complete one by looking at the six. Adding a
// criterion to section 8 and forgetting to register it should break something
// loudly, and this is that something.
const Criteria = 7

// Verdict is how a criterion came out.
type Verdict string

const (
	// Met: the criterion held, with numbers to show it.
	Met Verdict = "met"
	// Missed: the criterion was tested and did not hold.
	Missed Verdict = "missed"
	// Local: tested, but against a fixture rather than the live swarm, because
	// the condition cannot be ordered from a public torrent.
	Local Verdict = "local"
	// Skipped: not run.
	Skipped Verdict = "skipped"
)

// Result is one criterion's outcome, with the numbers behind it.
type Result struct {
	Number  int
	Title   string
	Verdict Verdict
	// Measured are the numbers that decided it, in the order they were taken.
	Measured []Measurement
	// Note explains a verdict that needs it - why a criterion was tested
	// locally, or what exactly missed.
	Note string
}

// Measurement is one number and what it is.
type Measurement struct {
	Name  string
	Value string
}

// Report collects the run.
type Report struct {
	Tool     string
	Started  time.Time
	Machine  string
	Torrents []string
	Results  []Result
}

// Add records a criterion's outcome.
func (r *Report) Add(res Result) { r.Results = append(r.Results, res) }

// Measure is a convenience for building a measurement list.
func Measure(name, format string, args ...any) Measurement {
	return Measurement{Name: name, Value: fmt.Sprintf(format, args...)}
}

// WriteFor writes the report and answers where it went.
//
// explicit is the -report flag's value. Given, it is honoured as-is and
// nothing is checked: a person who names a path has said what they are doing,
// which is how TOR-88 took forty reps without touching docs/results.
//
// Empty means the release-named path - docs/results/<version>-acceptance.md -
// and THAT one is refused unless every criterion ran. The refusal exists
// because the alternative was observed: a `-run` filter that executed nothing
// at all still wrote that file, with a header, a machine-state line, a table
// header and zero rows, named exactly as the release's own evidence and
// indistinguishable from a real one at a glance (TOR-96). In the main
// checkout it would have overwritten the report a real run produced, and
// TOR-76 had already cost this project one report that had to be regenerated
// a release later.
//
// Refusing rather than stamping the file "partial": a partial report at the
// canonical name is still the thing somebody diffs against last release.
func (r *Report) WriteFor(explicit, releasePath string) (string, error) {
	if explicit != "" {
		return explicit, r.Write(explicit)
	}
	if len(r.Results) != Criteria {
		return "", fmt.Errorf("%d of %d criteria ran, so this is not %s: "+
			"a report named for the release must cover the release. "+
			"Run every criterion, or pass -report <path> to write this subset somewhere else",
			len(r.Results), Criteria, filepath.Base(releasePath))
	}
	return releasePath, r.Write(releasePath)
}

// Write renders the report as markdown at path.
//
// Markdown rather than JSON because the audience is a person deciding whether
// a release is worth shipping, and because the previous baseline
// (docs/results/0.1.0-acceptance.md) is markdown and the two have to be
// readable side by side.
func (r *Report) Write(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	sort.SliceStable(r.Results, func(i, j int) bool { return r.Results[i].Number < r.Results[j].Number })

	var b strings.Builder
	fmt.Fprintf(&b, "# Acceptance run, torpeek %s\n\n", r.Tool)
	fmt.Fprintf(&b, "Run %s.\n\n", r.Started.Format(time.RFC3339))
	if r.Machine != "" {
		fmt.Fprintf(&b, "Machine while measuring: `%s`. Elapsed times are only\n"+
			"comparable against another run on a similarly loaded machine; traffic is\n"+
			"comparable regardless.\n\n", r.Machine)
	}
	if len(r.Torrents) > 0 {
		b.WriteString("Torrents:\n\n")
		for _, t := range r.Torrents {
			fmt.Fprintf(&b, "- %s\n", t)
		}
		b.WriteString("\n")
	}

	b.WriteString("| # | criterion | verdict |\n| --- | --- | --- |\n")
	for _, res := range r.Results {
		fmt.Fprintf(&b, "| %d | %s | **%s** |\n", res.Number, res.Title, res.Verdict)
	}
	b.WriteString("\n")

	for _, res := range r.Results {
		fmt.Fprintf(&b, "## %d. %s\n\n**%s**", res.Number, res.Title, res.Verdict)
		if res.Note != "" {
			fmt.Fprintf(&b, " — %s", res.Note)
		}
		b.WriteString("\n\n")
		if len(res.Measured) == 0 {
			b.WriteString("No numbers taken.\n\n")
			continue
		}
		b.WriteString("| measurement | value |\n| --- | --- |\n")
		for _, m := range res.Measured {
			fmt.Fprintf(&b, "| %s | %s |\n", m.Name, m.Value)
		}
		b.WriteString("\n")
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}
