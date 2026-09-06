package cli

import (
	"strings"
	"testing"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/web"
)

// TestRunConfigCarriesTheWebSelection guards the inbound half of TOR-66: a
// selection a browser sends through web.RunRequest.Files must reach
// core.Config.Files, the same field swarm.Select and Engine.run read - not
// be dropped at the web boundary the way it used to be.
func TestRunConfigCarriesTheWebSelection(t *testing.T) {
	base := core.Config{Source: "should be replaced"}
	req := web.RunRequest{Source: "magnet:?xt=urn:btih:abc", Files: []string{"0", "2"}}

	cfg, err := runConfig(base, req)
	if err != nil {
		t.Fatalf("runConfig: %v", err)
	}
	if cfg.Source != req.Source {
		t.Errorf("Source = %q, want %q", cfg.Source, req.Source)
	}
	if len(cfg.Files) != 2 || cfg.Files[0] != "0" || cfg.Files[1] != "2" {
		t.Errorf("Files = %v, want [0 2]", cfg.Files)
	}
}

// TestRunConfigKeepsTheDashFileFlagWhenTheWebSendsNoSelection is the
// backward-compat half: -file already sets cfg.Files before serveWeb ever
// runs (see Options.config), and a request with no picker attached - the
// server's own startup run, or a plain "start" from a page with no file
// list yet - must not silently clear it.
func TestRunConfigKeepsTheDashFileFlagWhenTheWebSendsNoSelection(t *testing.T) {
	base := core.Config{Files: []string{"episode-2"}}
	req := web.RunRequest{Source: "magnet:?xt=urn:btih:abc"}

	cfg, err := runConfig(base, req)
	if err != nil {
		t.Fatalf("runConfig: %v", err)
	}
	if len(cfg.Files) != 1 || cfg.Files[0] != "episode-2" {
		t.Errorf("Files = %v, want [episode-2] preserved from -file", cfg.Files)
	}
}

// TestRunConfigWebSelectionReplacesTheDashFileFlag: a person who ticked
// boxes in the UI is choosing the whole run's selection, not adding to
// whatever the process happened to be started with.
func TestRunConfigWebSelectionReplacesTheDashFileFlag(t *testing.T) {
	base := core.Config{Files: []string{"episode-2"}}
	req := web.RunRequest{Source: "magnet:?xt=urn:btih:abc", Files: []string{"7"}}

	cfg, err := runConfig(base, req)
	if err != nil {
		t.Fatalf("runConfig: %v", err)
	}
	if len(cfg.Files) != 1 || cfg.Files[0] != "7" {
		t.Errorf("Files = %v, want [7], the web selection alone", cfg.Files)
	}
}

// TestRunConfigRejectsAnUnknownMode keeps the profile lookup's existing
// error behaviour intact after the extraction out of serveWeb's closure.
func TestRunConfigRejectsAnUnknownMode(t *testing.T) {
	_, err := runConfig(core.Config{}, web.RunRequest{Source: "x", Mode: "not-a-real-mode"})
	if err == nil {
		t.Fatal("runConfig: want an error for an unknown mode, got nil")
	}
}

// TestRunConfigModeSelectsProfile is the existing mode-mapping behaviour,
// preserved by the extraction.
func TestRunConfigModeSelectsProfile(t *testing.T) {
	cfg, err := runConfig(core.Config{}, web.RunRequest{Source: "x", Mode: swarm.MinTraffic.Name})
	if err != nil {
		t.Fatalf("runConfig: %v", err)
	}
	if cfg.Profile.Name != swarm.MinTraffic.Name {
		t.Errorf("Profile = %v, want %v", cfg.Profile.Name, swarm.MinTraffic.Name)
	}
}

// TestRunConfigCarriesTheFrameCount is the inbound half of TOR-68: the
// number typed beside the mode select has to reach core.Config.Plan.Count,
// which is what ParamsKey hashes and Plan.Points spreads - so a count is
// also what makes a regeneration a separate result set rather than an
// edit of the first one.
func TestRunConfigCarriesTheFrameCount(t *testing.T) {
	base := core.Config{Plan: frames.DefaultPlan()}
	req := web.RunRequest{Source: "magnet:?xt=urn:btih:abc", Count: 6}

	cfg, err := runConfig(base, req)
	if err != nil {
		t.Fatalf("runConfig: %v", err)
	}
	if cfg.Plan.Count != 6 {
		t.Errorf("Plan.Count = %d, want 6", cfg.Plan.Count)
	}
	if cfg.Plan.Start != base.Plan.Start || cfg.Plan.End != base.Plan.End {
		t.Errorf("the count replaced the window too: %+v, want the base window %+v", cfg.Plan, base.Plan)
	}
}

// TestRunConfigKeepsTheDashNFlagWhenTheWebSendsNoCount is the other half of
// treating zero as "the request said nothing": -n is set before serveWeb
// ever runs, and a request from a page that never touched the field - or
// the server's own startup run - must not turn the count into zero, which
// frames.Plan.Validate would then refuse outright.
func TestRunConfigKeepsTheDashNFlagWhenTheWebSendsNoCount(t *testing.T) {
	base := core.Config{Plan: frames.Plan{Count: 6, Start: frames.DefaultStart, End: frames.DefaultEnd}}
	req := web.RunRequest{Source: "magnet:?xt=urn:btih:abc"}

	cfg, err := runConfig(base, req)
	if err != nil {
		t.Fatalf("runConfig: %v", err)
	}
	if cfg.Plan.Count != 6 {
		t.Errorf("Plan.Count = %d, want 6 preserved from -n", cfg.Plan.Count)
	}
}

// TestQueueWidthErrorAcceptsTheShippedDefault: the default is now a WIDENED
// queue (web.DefaultMaxActiveTorrents is 5) and no roof is configured out of
// the box, so the case that used to be refused is now the ordinary one. If
// this ever fails, torpeek will not start with its own defaults.
func TestQueueWidthErrorAcceptsTheShippedDefault(t *testing.T) {
	if err := queueWidthError(web.DefaultMaxActiveTorrents); err != nil {
		t.Errorf("queueWidthError(%d) = %v, want nil - that is the shipped default",
			web.DefaultMaxActiveTorrents, err)
	}
}

// TestQueueWidthErrorWideningNeedsNoRoof is the decision TOR-149 carried out,
// kept as a test rather than only as a comment: widening past one slot used
// to be a usage error unless -max-client-bytes was set, and it deliberately
// is not any more. The multiplication it guarded against is now stated at
// startup instead, which TestQueueWidthNoticeStatesTheWorstCase covers.
func TestQueueWidthErrorWideningNeedsNoRoof(t *testing.T) {
	for _, n := range []int{2, 3, 5, 20} {
		if err := queueWidthError(n); err != nil {
			t.Errorf("queueWidthError(%d) = %v, want nil with no roof configured", n, err)
		}
	}
}

// TestQueueWidthErrorRejectsNonPositive: a width under 1 is not a narrower
// queue, it is a broken one (see web.Server.SetMaxActiveTorrents), and is
// the one thing still refused here.
func TestQueueWidthErrorRejectsNonPositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		if err := queueWidthError(n); err == nil {
			t.Errorf("queueWidthError(%d) = nil, want an error", n)
		}
	}
}

// TestWorstCaseBytesMultipliesThePerRunCeiling is the arithmetic the startup
// line prints, checked here rather than through a running server. The
// zero-budget case is the one that matters: zero means "decide once the file
// count is known", and the honest ceiling for such a run is the cap
// core.DefaultBudget applies - not zero, which would print a worst case of
// nothing at all.
func TestWorstCaseBytesMultipliesThePerRunCeiling(t *testing.T) {
	if got, want := worstCaseBytes(5, 0), int64(5)*core.MaxRunBytes; got != want {
		t.Errorf("worstCaseBytes(5, 0) = %d, want %d - five times the default per-run cap", got, want)
	}
	if got, want := worstCaseBytes(3, 100<<20), int64(3)*(100<<20); got != want {
		t.Errorf("worstCaseBytes(3, 100 MiB) = %d, want %d", got, want)
	}
	if got, want := worstCaseBytes(1, 0), int64(core.MaxRunBytes); got != want {
		t.Errorf("worstCaseBytes(1, 0) = %d, want one run's own ceiling %d", got, want)
	}
}

// TestQueueWidthNoticeStatesTheWorstCase is the other half of dropping the
// refusal. The whole argument for removing it was that the multiplication
// gets STATED instead of enforced, so if this line ever stops naming the
// figure, the decision has quietly become plain silence.
func TestQueueWidthNoticeStatesTheWorstCase(t *testing.T) {
	notice := queueWidthNotice(5, 0)
	if notice == "" {
		t.Fatal("queueWidthNotice(5, 0) = \"\", want a line - five at once is the shipped default")
	}
	for _, want := range []string{"5", humanBytes(core.MaxRunBytes), humanBytes(5 * core.MaxRunBytes)} {
		if !strings.Contains(notice, want) {
			t.Errorf("the notice %q does not contain %q", notice, want)
		}
	}
}

// TestQueueWidthNoticeIsSilentAtOneSlot: a width of one multiplies nothing,
// so there is nothing to disclose and the line must not appear - a warning
// that shows up when it does not apply teaches people to ignore it.
func TestQueueWidthNoticeIsSilentAtOneSlot(t *testing.T) {
	for _, n := range []int{0, 1} {
		if notice := queueWidthNotice(n, 0); notice != "" {
			t.Errorf("queueWidthNotice(%d, 0) = %q, want empty", n, notice)
		}
	}
}
