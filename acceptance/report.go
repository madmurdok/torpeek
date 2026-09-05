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

// Figure names one of the two traffic numbers a run has. They are not
// interchangeable and a report that showed only one has been read as if it
// showed the other.
type Figure int

const (
	// OnDownloaded: bytes that arrived from the swarm, whoever asked for
	// them. The zero value, so a criterion that says nothing is judged the
	// way every criterion was judged before TOR-94.
	OnDownloaded Figure = iota
	// OnClaimed: bytes the run itself ordered.
	OnClaimed
)

// Traffic is what one run cost, in both figures, with the ceiling attached to
// whichever of them the verdict is actually taken on.
//
// The two exist separately because they are not the same quantity and only one
// of them is torpeek's. Claimed is the total size of the distinct pieces the
// run ordered: on the acceptance torrent min-traffic orders exactly 44 of them,
// to the byte, every rep. Downloaded is what arrived, and on byte-identical
// code that has been measured anywhere from 45.5 to 118.6 MiB - reader
// prefetch, and chunks that keep coming from several peers after the claim was
// released, 2.3 to 12.3 MiB of it while no request was open at all
// (docs/tor-88-min-traffic-spread.md).
//
// So criterion 2 is judged on Claimed (TOR-94): a verdict taken on arrivals is
// a verdict somebody else's prefetch casts, and it missed the 60 MiB ceiling
// 21 times in 22 runs during one forty-minute afternoon in which nothing in
// this repository had changed. Criterion 1 stays on Downloaded - its ceiling
// is 150 MB against a figure that has sat inside a 2.7 MiB band for seven
// releases, so nothing there needs protecting from the swarm's mood, and
// re-basing a criterion that is not failing would cost the release-to-release
// comparison for no gain.
//
// Nothing is hidden either way: all three numbers are reported for both
// criteria, and the gap is the one worth watching.
type Traffic struct {
	// ClaimedByte and ClaimedPieces are what the run ordered.
	ClaimedByte   int64
	ClaimedPieces int
	// DownloadedByte is what arrived.
	DownloadedByte int64
	// Ceiling is the criterion's traffic ceiling; zero means it names none.
	Ceiling int64
	// JudgedOn says which figure Ceiling applies to.
	JudgedOn Figure
}

// Judged is the figure this criterion's verdict is taken on, and so the one to
// compare against Ceiling.
func (t Traffic) Judged() int64 {
	if t.JudgedOn == OnClaimed {
		return t.ClaimedByte
	}
	return t.DownloadedByte
}

// Measurements renders all three numbers for the report, saying on the face of
// each row whether it decided anything.
//
// Every row is labelled rather than only the judged one, because the failure
// this shape exists to prevent is a reader taking the first traffic number in
// the table as the one that matters - which is exactly how a 60 MiB ceiling
// came to be read as protecting a 44 MiB intent.
func (t Traffic) Measurements() []Measurement {
	gap := t.DownloadedByte - t.ClaimedByte

	var unclaimed string
	switch {
	case gap < 0:
		unclaimed = fmt.Sprintf("-%s — less arrived than was ordered: pieces already on disk, "+
			"or a claim the run ended before it landed", mib(-gap))
	case t.ClaimedByte > 0:
		// One decimal, because the small end of this is meaningful: min-time
		// measured 0.4 MiB over a 104 MiB order, and "0%" would have read as
		// nothing to see where 0.4% is the finding that its readahead sits
		// inside its own window.
		unclaimed = fmt.Sprintf("+%s (%.1f%% on top of the claim) — piece data no claim asked for "+
			"and no read consumed", mib(gap), 100*float64(gap)/float64(t.ClaimedByte))
	default:
		unclaimed = fmt.Sprintf("+%s — piece data no claim asked for and no read consumed", mib(gap))
	}

	return []Measurement{
		{Name: "claimed", Value: fmt.Sprintf("%s in %d pieces%s",
			mib(t.ClaimedByte), t.ClaimedPieces, t.verdict(OnClaimed))},
		{Name: "downloaded", Value: mib(t.DownloadedByte) + t.verdict(OnDownloaded)},
		{Name: "unclaimed arrivals", Value: unclaimed},
	}
}

// verdict says what this figure did, and against what.
func (t Traffic) verdict(f Figure) string {
	if t.JudgedOn != f {
		return " — reported, not judged"
	}
	if t.Ceiling <= 0 {
		return " — the verdict"
	}
	return fmt.Sprintf(" — the verdict, against a ceiling of %s", mib(t.Ceiling))
}

// mib and secs render the units the criteria are stated in. They moved here
// from beside the tests because Traffic above renders itself, which is what
// lets `make check` cover the report's shape without a swarm - and they stay
// unexported, because how a number is spelled is nobody else's business.
func mib(n int64) string          { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }
func secs(d time.Duration) string { return fmt.Sprintf("%.1fs", d.Seconds()) }

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
