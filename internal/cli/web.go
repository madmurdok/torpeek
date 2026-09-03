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
// same events the CLI prints, and opens a browser at itself - unless
// -headless says there is nothing to open it on.
//
// The web package is handed a runner rather than a configuration, so building
// a run out of a request stays here, next to the flag defaults it has to
// agree with. Otherwise the UI and the command line would grow two answers to
// what a run is (REQUIREMENTS.md section 3.1: every client sits on the same
// core as an equal).
func serveWeb(ctx context.Context, opts Options, base core.Config, tools ffmpeg.Tools, stdout, stderr io.Writer) int {
	engine := core.NewEngine(tools)

	runner := func(ctx context.Context, req web.RunRequest) (<-chan core.Event, error) {
		cfg, err := runConfig(base, req)
		if err != nil {
			return nil, err
		}
		return engine.Run(ctx, cfg)
	}

	// Reopening a finished run replays it from disk (TOR-55) through the same
	// engine the runner above uses, so a cache hit and a live run agree about
	// what a run looks like without web deciding that itself - it only ever
	// asks for base.OutputRoot, the one output directory this whole closure
	// already agrees with GET /runs about (see cfg.OutputRoot below).
	replayer := func(infoHash, params string) <-chan core.Event {
		return engine.Replay(base.OutputRoot, infoHash, params)
	}

	// Deleting one frame (TOR-70) is injected the same way and for the same
	// reason: core owns everything under base.OutputRoot - it is the only
	// package that writes there - and the rules a delete has to keep are the
	// cache's, not a UI's. web validates the address and calls this; what a
	// safe delete is stays in core.DeleteFrame.
	deleter := func(infoHash, params string, fileIndex, frameIndex int) error {
		return core.DeleteFrame(base.OutputRoot, infoHash, params, fileIndex, frameIndex)
	}

	cfg := web.DefaultConfig()
	if addr := webAddr(opts.WebHost, opts.WebPort); addr != "" {
		cfg.Addr = addr
	}
	cfg.BasePath = opts.BasePath
	cfg.Token = opts.Token
	// GET /runs lists what is already on disk (TOR-54) from the same root
	// the engine writes to and reads cache hits from - base is the one
	// shared core.Config this closure already reads OutputRoot from for
	// every run, so the listing and a run agree on where results live
	// without web deciding that itself.
	cfg.OutputRoot = base.OutputRoot
	// GET /defaults reports this so the intake field can show the number a
	// run would actually use. Same reason as OutputRoot above: base is the
	// one core.Config this closure already builds every run from, so the
	// page and a run agree without web deciding anything.
	cfg.DefaultCount = base.Plan.Count

	server, err := web.Start(ctx, cfg, runner, replayer, deleter)
	if err != nil {
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		return ExitFailed
	}
	defer server.Close()

	fmt.Fprintf(stdout, "torpeek: serving the UI at %s\n", server.URL())

	// A source on the command line starts straight away; without one the page
	// waits for someone to paste a link.
	//
	// The run failing to start is no longer an error StartRun returns - a
	// queued run reaches the engine long after the call that asked for it -
	// so a source that cannot be opened comes back as a failed run instead.
	// Reported here anyway: a person who typed the source on the command line
	// is looking at this terminal, and -headless has no page to read it on.
	if opts.Source != "" {
		run, err := server.StartRun(web.RunRequest{Source: opts.Source, Mode: opts.Profile})
		if err != nil {
			fmt.Fprintf(stderr, "torpeek: %v\n", err)
		} else if run.State == web.RunFailed {
			fmt.Fprintf(stderr, "torpeek: %s\n", run.Err)
		}
	}

	// -headless is the explicit switch section 3.3 asks for, rather than a
	// guess from $DISPLAY or $SSH_CONNECTION: either can be set on a machine
	// that still has a real browser to open (X11 forwarding, a remote
	// desktop session), and OpenBrowser's failure is already advisory - a
	// wrong guess would silently skip a browser that was actually available,
	// where a wrong flag is just a line typed once. On a machine with
	// nothing to open at all, the failure this skips is harmless anyway: it
	// is reported and the server keeps serving either way.
	if !opts.Headless {
		if err := web.OpenBrowser(server.URL()); err != nil {
			fmt.Fprintf(stderr, "torpeek: %v; open it yourself\n", err)
		}
	}

	<-ctx.Done()
	fmt.Fprintln(stdout, "torpeek: stopping")
	return ExitOK
}

// runConfig turns one web request into the config its run executes with.
// Extracted out of serveWeb's runner closure so the mapping - profile
// lookup, file selection - can be tested without a running server or a real
// torrent.
//
// req.Files, when the browser sent a selection, replaces base's own -file
// flag rather than being merged with it: a person who ticked boxes in the
// UI is choosing the whole selection, not adding to whatever the process
// happened to be started with. An empty selection leaves base.Files alone,
// which is what keeps -file working exactly as before for the source the
// command line itself starts (opts.Source below) and for any request that
// simply does not offer a picker.
func runConfig(base core.Config, req web.RunRequest) (core.Config, error) {
	cfg := base
	cfg.Source = req.Source

	if req.Mode != "" {
		profile, err := swarm.ProfileByName(req.Mode)
		if err != nil {
			return core.Config{}, err
		}
		cfg.Profile = profile
	}

	if len(req.Files) > 0 {
		cfg.Files = req.Files
	}

	// Zero means the request said nothing, so -n stands. Only the lower
	// bound frames.Plan.Validate already enforces applies beyond that -
	// there is no cap here, and n is per video file, so a torrent of six
	// quality variants costs six times this number (TOR-50).
	if req.Count > 0 {
		cfg.Plan.Count = req.Count
	}

	return cfg, nil
}

// webAddr turns -web-host/-web-port into a listen address, leaving the
// decision to web.DefaultConfig() (loopback, a fixed documented port) when
// neither flag was given. A host without a port keeps the documented default
// port; a port without a host keeps loopback - either flag alone still does
// something useful rather than requiring both.
func webAddr(host string, port int) string {
	if host == "" && port <= 0 {
		return ""
	}
	if host == "" {
		host = web.DefaultHost
	}
	if port <= 0 {
		port = web.DefaultPort
	}
	return fmt.Sprintf("%s:%d", host, port)
}
