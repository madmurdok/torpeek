package cli

import (
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
