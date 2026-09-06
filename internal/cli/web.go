package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
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

	// One pool for the whole server, and the server is what closes it.
	//
	// This is the ownership the pool exists to state: every public torrent
	// shares one long-lived client, so that client cannot belong to a run -
	// a run that fails, or is cancelled, would take every other run's client
	// down with it. It belongs here, next to the server whose lifetime it
	// actually matches, and every run below is handed it through
	// core.Config.Torrents rather than configuring a client of its own.
	//
	// Built from the same base every run is built from, so the pool, the
	// listing and the run agree about the data directory, the DHT switch, the
	// port set and the known peers without any of them deciding it
	// separately. Deferred before the server's own Close so it runs after it:
	// the server cancels what is running, and only then does the client go
	// down.
	//
	// base.Roof is the ceiling over this one client, and it arrives already
	// set, from Options.config: it is a flag, like every other default a run
	// starts from. What matters here is that it must not be set anywhere
	// ELSE. runConfig below copies base per request, and a request that
	// varied the roof would be a second opinion about one client, of which
	// the larger silently wins - so the roof travels with the pool, decided
	// once, by whoever decided there is one client (core.Config.Roof).
	pool := swarm.NewPool(base.Swarm)
	defer pool.Close()
	base.Torrents = pool

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

	// Listing a torrent's files before any of them is captured (TOR-67) is
	// injected for the same reason again, and starts from the same base: the
	// metadata pass and the run that follows it must share one pool - the
	// same client, the same DHT switch, the same known peers - or they would
	// reach the same torrent by two different routes, and the second would be
	// the first to find out. Only the source varies, which
	// is why this takes one rather than a whole web.RunRequest: no other
	// field of a request can change what a torrent contains.
	//
	// It takes a context because it is a wait on the swarm, not a file read
	// (see web.Lister), and core.List is what bounds it: metadata only, the
	// session closed and its pieces discarded before it returns.
	lister := func(ctx context.Context, source string) (core.Contents, error) {
		cfg := base
		cfg.Source = source
		return engine.List(ctx, cfg)
	}

	// Checked before anything is served rather than when the button is
	// pressed: a mistyped -watch-dir is a command line to fix, and finding
	// out about it as a failed drop - minutes later, from a browser, on a
	// machine nobody is sitting at - is the wrong place to learn it. A
	// missing directory is not created here either: this flag names a
	// directory some torrent client is already watching, and inventing one
	// nothing watches would look like it worked.
	watchDir, err := resolveWatchDir(opts.WatchDir)
	if err != nil {
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		return ExitUsage
	}

	cfg := web.DefaultConfig()
	if addr := webAddr(opts.WebHost, opts.WebPort); addr != "" {
		cfg.Addr = addr
	}
	// Empty leaves the UI without the "send to my client" button entirely -
	// see web.Config.WatchDir.
	cfg.WatchDir = watchDir
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
	// Same reason again, for the one number a top-up has to state before it
	// is spent: the client-wide roof bounds what any run may receive
	// (core.Roof), so an offer of extra traffic must never be a bigger
	// figure than the roof would allow. web only ever REPORTS it - the roof
	// is enforced in the engine, against the pool's own counter, whatever
	// the UI says (web.Config.RoofBytes).
	cfg.RoofBytes = base.Roof.MaxBytes
	// -max-active-torrents is the queue's width (TOR-130); see
	// web.Config.MaxActiveTorrents for what it governs and why raising it
	// past 1 is checked below rather than left to newServer's own default.
	cfg.MaxActiveTorrents = opts.MaxActiveTorrents

	// A width past 1 that is not TOR-130's own choice to sit safely inside
	// section 4.1's guidance would defeat the point of a value in the CLI at
	// all - anyone could type a bigger one - so the value itself is not
	// bounded above. What IS enforced is the promise both web.Server's own
	// doc and core.DefaultRoof make: a queue wider than one slot arrives
	// with a client-wide traffic roof configured, or N runs going at once
	// multiply one run's own ceiling by N, exactly what TOR-131's roof
	// exists to prevent. Refused here, before a port is even opened, rather
	// than left as a log line nobody is watching on an unattended systemd
	// unit (section 4.1) - the fix is one more flag, and typing it is
	// cheaper than a traffic bill nobody meant to run up.
	if err := queueWidthError(cfg.MaxActiveTorrents); err != nil {
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		return ExitUsage
	}

	server, err := web.Start(ctx, cfg, runner, replayer, deleter, lister)
	if err != nil {
		fmt.Fprintf(stderr, "torpeek: %v\n", err)
		return ExitFailed
	}
	defer server.Close()

	fmt.Fprintf(stdout, "torpeek: serving the UI at %s\n", server.URL())

	// Said out loud at startup rather than left to be discovered. A server
	// that stays up for hours can spend a great deal of somebody's
	// allowance, the default is no roof at all (core.DefaultRoof), and a
	// lever nobody knows about is not a lever. Received bytes only - nothing
	// caps upload (swarm.Torrent.Uploaded).
	if base.Roof.MaxBytes > 0 {
		fmt.Fprintf(stdout, "torpeek: client-wide traffic roof: %d bytes received, across every run together\n",
			base.Roof.MaxBytes)
	} else {
		fmt.Fprintln(stdout, "torpeek: client-wide traffic roof: none; -max-client-bytes sets one")
	}
	// The queue's width, and - when it is wider than one slot - the traffic
	// arithmetic that follows from it. Widening used to be refused without a
	// roof (queueWidthError); this line is what replaced the refusal, and it
	// exists so that N times a run's ceiling is a number somebody read once
	// rather than something they worked out afterwards from a bandwidth
	// bill. Printed even when a roof IS set, because the roof bounds the
	// total while this bounds what the runs would ask for.
	fmt.Fprintf(stdout, "torpeek: queue width: %d torrent(s) at once\n", cfg.MaxActiveTorrents)
	if notice := queueWidthNotice(cfg.MaxActiveTorrents, base.Budget.MaxBytes); notice != "" {
		fmt.Fprintf(stdout, "torpeek: %s\n", notice)
	}

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

	// TOR-152. A top-up is a run that has to land in the SAME result
	// directory as the set it is finishing, or it reuses none of its frames
	// and pays full price for a second set nobody asked for. core.ParamsKey
	// hashes the count, the window, the profile, the format and the
	// sequential switch; the first two of those already have their own
	// request fields above, and this is the rest of them, read off the run
	// record web is finishing rather than taken from whatever flags this
	// process happens to be running with today.
	//
	// Nil for every ordinary run, which leaves -start/-end/-format and the
	// sequential opt-in exactly as they were.
	if w := req.Window; w != nil {
		cfg.Plan.Start, cfg.Plan.End = w.Start, w.End
		cfg.Sequential = w.Sequential
		if w.Format != "" {
			cfg.Format = frames.Format(w.Format)
		}
	}

	// The raised traffic ceiling, and the ONE thing on a request that can
	// spend more of somebody's allowance than the flags allowed for. It is
	// never set from JSON (web.RunRequest.MaxBytes) - only by the server
	// itself, for a partial set it read off disk and priced - and zero, the
	// value every other run carries, leaves core.budgetFor to scale the
	// ceiling to the file count exactly as it always has.
	//
	// What is NOT touched here is the roof. base.Roof arrives from the pool
	// this closure built once, and a request that varied it would be a
	// second opinion about one client, of which the larger silently wins
	// (see the pool comment above, and core.Config.Roof). So a raised
	// per-run ceiling is still held under the client-wide one, which is what
	// stops a top-up from becoming a way around it.
	if req.MaxBytes > 0 {
		cfg.Budget.MaxBytes = req.MaxBytes
	}

	return cfg, nil
}

// resolveWatchDir turns -watch-dir into the absolute path the server will
// copy into, refusing anything that is not already a directory.
//
// Absolute, because the answer a run gives back names where the file landed
// and a relative path would name it from a working directory the person
// reading the answer is not in. Empty stays empty: that is the documented way
// to say "no watch directory", and it must not become the current one.
func resolveWatchDir(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve the watch directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("watch directory %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("watch directory %s is not a directory", abs)
	}
	return abs, nil
}

// queueWidthError is TOR-130's own check, extracted out of serveWeb the same
// way runConfig was: a decision worth verifying without a running server, a
// real ffmpeg or an open port.
//
// maxActive is what -max-active-torrents resolved to (web.Config's own
// defaulting has already turned "not stated" into
// web.DefaultMaxActiveTorrents by the time serveWeb calls this, so 0 never
// legitimately reaches here from a flag - it only can from a caller building
// Options by hand); roofMaxBytes is core.Config.Roof.MaxBytes, zero meaning
// unlimited, the documented default (core.DefaultRoof).
//
// A width under 1 is the one way to fail: that is not a narrower queue but a
// broken one (see web.Server.SetMaxActiveTorrents for why zero is not "pause
// the queue").
//
// Widening past 1 used to be refused here unless a client-wide roof was set,
// because N runs each free to spend their own per-run ceiling adds up to N
// times that ceiling with nothing over the whole client to stop it
// (core.Roof). That refusal is gone by decision: torpeek's own default is
// now a widened queue for local use, where the link and the quota belong to
// the person running it. The multiplication has not gone anywhere, so
// serveWeb states it at startup instead - a number said once is the
// alternative to a refusal, not to silence.
func queueWidthError(maxActive int) error {
	if maxActive < 1 {
		return fmt.Errorf("-max-active-torrents must be at least 1, got %d", maxActive)
	}
	return nil
}

// worstCaseBytes is width times the per-run traffic ceiling: what every run
// this server allows at once could receive between them, in the worst case.
//
// It is the figure the removed roof requirement used to protect against, so
// it is the figure worth printing (core.Roof, queueWidthError). budgetBytes
// is core.Config.Budget.MaxBytes, and zero there means "decide once the file
// count is known", which the engine caps at core.MaxRunBytes - so that cap
// is the honest worst case for a run whose ceiling nobody set.
func worstCaseBytes(maxActive int, budgetBytes int64) int64 {
	return int64(maxActive) * perRunCeiling(budgetBytes)
}

// queueWidthNotice is the startup line that replaced the refusal, extracted
// from serveWeb for the same reason queueWidthError and runConfig were: it is
// the sentence a person reads once about what a widened queue can spend, and
// checking that it says the right number should not need a listening server,
// a real ffmpeg or an open port.
//
// Empty at a width of one, which multiplies nothing and so has nothing to
// disclose.
func queueWidthNotice(maxActive int, budgetBytes int64) string {
	if maxActive <= 1 {
		return ""
	}
	return fmt.Sprintf("with %d at once, the per-run traffic ceiling of %s adds up to %s received in the worst case",
		maxActive, humanBytes(perRunCeiling(budgetBytes)),
		humanBytes(worstCaseBytes(maxActive, budgetBytes)))
}

// perRunCeiling is the traffic ceiling one run is held to: what -max-bytes
// said, or the cap the engine applies when it was left to scale with the
// file count (core.DefaultBudget).
func perRunCeiling(budgetBytes int64) int64 {
	if budgetBytes <= 0 {
		return core.MaxRunBytes
	}
	return budgetBytes
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
