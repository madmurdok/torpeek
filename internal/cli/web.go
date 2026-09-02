package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/web"
)

// serveWeb runs the UI: one binary that serves its own frontend, streams the
// same events the CLI prints, and opens a browser at itself.
//
// The web package is handed a runner rather than a configuration, so building
// a run out of a request stays here, next to the flag defaults it has to
// agree with. Otherwise the UI and the command line would grow two answers to
// what a run is (REQUIREMENTS.md section 3.1: every client sits on the same
// core as an equal).
func serveWeb(ctx context.Context, opts Options, base core.Config, tools ffmpeg.Tools, stdout, stderr io.Writer) int {
	engine := core.NewEngine(tools)

	runner := func(ctx context.Context, req web.RunRequest) (<-chan core.Event, error) {
		cfg := base
		cfg.Source = req.Source

		if req.Mode != "" {
			profile, err := swarm.ProfileByName(req.Mode)
			if err != nil {
				return nil, err
			}
			cfg.Profile = profile
		}

		return engine.Run(ctx, cfg)
	}

	cfg := web.DefaultConfig()
	if opts.WebPort > 0 {
		cfg.Addr = fmt.Sprintf("127.0.0.1:%d", opts.WebPort)
	}

	server, err := web.Start(ctx, cfg, runner)
	if err != nil {
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		return ExitFailed
	}
	defer server.Close()

	fmt.Fprintf(stdout, "torpeek: serving the UI at %s\n", server.URL())

	// A source on the command line starts straight away; without one the page
	// waits for someone to paste a link.
	if opts.Source != "" {
		if err := server.StartRun(web.RunRequest{Source: opts.Source, Mode: opts.Profile}); err != nil {
			fmt.Fprintf(stderr, "torpeek: %v\n", err)
		}
	}

	// A machine with no desktop has nothing to open, and that must not stop it
	// serving. Section 3.3's explicit headless switch is still to come; until
	// then the failure is reported and ignored.
	if err := web.OpenBrowser(server.URL()); err != nil {
		fmt.Fprintf(stderr, "torpeek: %v; open it yourself\n", err)
	}

	<-ctx.Done()
	fmt.Fprintln(stdout, "torpeek: stopping")
	return ExitOK
}
