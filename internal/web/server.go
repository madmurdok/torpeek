package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/madmurdok/torpeek/internal/core"
	"github.com/madmurdok/torpeek/internal/wire"
)

// DefaultHost and DefaultPort make up DefaultAddr - split out so a caller
// overriding only one of them (the CLI's -web-host / -web-port) does not have
// to parse the other back out of a string.
const (
	DefaultHost = "127.0.0.1"
	DefaultPort = 8765

	// DefaultAddr is where the UI listens when nothing says otherwise.
	//
	// A fixed default rather than a free port: a managed host allocates a
	// range and forbids anything outside it (REQUIREMENTS.md section 4.1), so
	// the port has to be something a person can be told and a proxy can be
	// pointed at. There it must be overridden, which is what Config.Addr is
	// for. The host stays loopback even there - a reverse proxy on the same
	// machine is the documented deployment (section 3.3) - but it is still
	// part of Config.Addr rather than hardcoded, for the rarer setup that
	// needs something else.
	DefaultAddr = "127.0.0.1:8765"
)

// Config configures the UI server.
type Config struct {
	// Addr is the listen address. Both the host and the port are part of it:
	// loopback is right on a desktop, while a seedbox behind a proxy binds
	// somewhere else on a port out of its allocated range.
	Addr string

	// BasePath is where the UI is mounted, such as "/torpeek" for a seedbox
	// behind nginx (REQUIREMENTS.md section 4.1). Empty mounts at the site
	// root. Whatever is given is normalized (normalizeBasePath) before use,
	// so "torpeek", "/torpeek" and "/torpeek/" all mean the same thing.
	BasePath string

	// ShutdownTimeout bounds how long Close waits for in-flight requests.
	ShutdownTimeout time.Duration

	// Token gates every request to the API and the event stream (not the
	// static shell - see Handler and authGuard) with a shared secret carried
	// as a "token" query parameter. Empty does not necessarily mean
	// "unprotected": Start decides whether one is required (needsToken) from
	// the resolved Addr and BasePath, and fills in a random value here when
	// it is and the caller left this blank.
	//
	// Setting Token explicitly - what -web-token does - works on any bind
	// address, including loopback, and is how an operator pins a token
	// across restarts: an auto-generated one changes every time the process
	// starts, which is fine for an interactive run but wrong for a systemd
	// unit that restarts on its own with nobody watching the log for the new
	// value (REQUIREMENTS.md section 3.3, section 4.1).
	Token string

	// OutputRoot is where GET /runs looks for runs this process did not
	// start - output.Layout's root, the same directory a run's own cache hit
	// (core.serveFromCache) reads. This package never writes there and never
	// decides a run's parameters from what it finds - it only lists what
	// cache.LoadRun can read back, per run.json (see listRuns). Empty means
	// nothing on disk is listed, only the registry: a caller building the
	// server directly (most tests) has no output directory to speak of, and
	// leaving it empty rather than requiring one keeps that working.
	OutputRoot string
}

// DefaultConfig serves the desktop case: loopback, fixed port.
func DefaultConfig() Config {
	return Config{Addr: DefaultAddr, ShutdownTimeout: 5 * time.Second}
}

// RunRequest is what the UI asks for when someone presses start.
//
// A dropped .torrent's bytes arrive at a separate endpoint, POST
// /runs/upload (multipart/form-data), rather than as a field here: the JSON
// path would need base64 for a binary payload, while multipart is what a
// browser's FormData already produces from a dropped File with no encoding
// step on either side. That handler (handleUploadTorrent) stages the upload
// as a temp file and builds exactly this struct with the temp path as
// Source - a .torrent path is already a valid Source (swarm.ParseSource
// accepts one, same as a path typed on the command line) - then calls
// StartRun. So a drag-and-drop and a pasted magnet converge on this one
// method; there is only ever one way a run begins.
type RunRequest struct {
	Source string `json:"source"`
	// Mode is the capture profile by name, empty meaning the server's default.
	Mode string `json:"mode,omitempty"`
	// Label overrides what run_state reports as the source, for a request
	// whose Source is a server-side temp path nobody typed (an uploaded
	// .torrent). Never set from JSON: it only exists on requests the server
	// itself builds.
	Label string `json:"-"`
}

// Runner starts a run and returns its event stream.
//
// The server does not build a run configuration itself. Turning a request
// into one is the job of whoever owns the command line defaults, and doing it
// in two places is how the UI and the CLI would come to run different things
// from the same input. The web package stays a consumer of events.
type Runner func(ctx context.Context, req RunRequest) (<-chan core.Event, error)

// Replayer replays a finished run from disk and returns its event stream,
// the same shape Runner returns for a live one - a channel that closes when
// the replay is over, its last event always Done or Failed (core.Engine's
// Replay method is the one real implementation; see its doc for why a miss
// is reported as a Failed event rather than a Go error here).
//
// It takes no context: unlike a live run, nothing here is worth cancelling -
// it is a handful of local file reads, never a network wait - and it takes
// no RunRequest, because reopening addresses a run by where it lives
// (infoHash, params - the pair a disk-only GET /runs row carries, TOR-54)
// rather than by what a fresh run would be asked to do.
type Replayer func(infoHash, params string) <-chan core.Event

// ErrNoSuchRun is returned when a request names a run this server does not
// hold - never did, or held and has since forgotten (see keepFinishedRuns).
var ErrNoSuchRun = errors.New("no such run")

// errClosed is what a request gets once Close has run.
var errClosed = errors.New("web: server is closed")

// errReplayUnavailable is what ReopenRun returns when the server was built
// with no Replayer - a test driving newServer directly, most often, since the
// real entry point (cli/web.go) always supplies one alongside its Runner.
var errReplayUnavailable = errors.New("web: reopening a run from disk is not available")

// keepFinishedRuns bounds how many finished runs stay in memory.
//
// A finished run has to outlive its own completion: the panel shows runs that
// are over, and a page that reloads must still find them. But this process is
// meant to stay up for hours, and each finished run holds its whole replay -
// every frame_ready of every file - so an unbounded map is a leak with a
// person's attention span as its only bound.
//
// Ten is not a capacity so much as a horizon: it is more runs than the panel
// shows at once, and what falls off it is not lost - every run leaves a
// record on disk (cache.Run, TOR-52) and TOR-54 lists from there, so memory
// only has to hold the recent tail that disk cannot describe as well. Queued
// and running entries are never trimmed, whatever the count.
const keepFinishedRuns = 10

// Server serves the embedded UI and the event streams of the runs it holds.
//
// It holds a registry of runs and exactly one slot to run them in. One at a
// time is the same rule as before, and for the same reason: two runs must not
// quietly compete for the same output directory and traffic budget, and a
// torrent client binds one pinned port and one client-wide DHT switch, so a
// second concurrent run could not even start. What changed is what happens to
// the second request - it waits its turn instead of being refused.
type Server struct {
	cfg      Config
	runner   Runner
	replayer Replayer
	baseCtx  context.Context

	server *http.Server
	url    string

	files *fileSet
	hub   *hub

	mu sync.Mutex
	// runs is every run this server still knows about, live or finished.
	runs map[string]*runEntry
	// order is their ids, oldest first: what a listing walks and what trim
	// evicts from the front of.
	order []string
	// waiting is the queue, in arrival order. It holds runs that have been
	// accepted and not yet started.
	waiting []*runEntry
	// running is the single slot. Its being one pointer rather than a
	// collection is the concurrency limit: there is no number to raise.
	running *runEntry
	stopped bool
}

// Start listens and begins serving. The returned server must be closed.
//
// It does not open a browser: on a seedbox there is nothing to open, and a
// test must never depend on one. The caller decides, with OpenBrowser.
//
// replayer may be nil - ReopenRun then answers errReplayUnavailable rather
// than refusing to start, the same tolerance Config.OutputRoot's own doc
// describes for a caller with no disk-backed run to speak of. The real
// entry point (cli/web.go) always supplies one, paired with runner.
func Start(ctx context.Context, cfg Config, runner Runner, replayer Replayer) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 5 * time.Second
	}
	if runner == nil {
		return nil, errors.New("web: a runner is required")
	}

	// A token the caller did not pin is filled in here, not in newServer:
	// newServer is also what a test drives directly through httptest, and a
	// test that wants no-auth behaviour (most of them) must not have one
	// sprung on it. Only the real entry point auto-provides.
	if cfg.Token == "" && needsToken(cfg.Addr, cfg.BasePath) {
		token, err := generateToken()
		if err != nil {
			return nil, fmt.Errorf("web: generate access token: %w", err)
		}
		cfg.Token = token
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("web listen on %s: %w", cfg.Addr, err)
	}

	s := newServer(ctx, cfg, runner, replayer)
	s.url = "http://" + ln.Addr().String() + s.cfg.BasePath + "/"
	if s.cfg.Token != "" {
		// The token is base64.RawURLEncoding output: a fixed alphabet with
		// no character that needs percent-escaping in a query string, so
		// this is safe to concatenate directly.
		s.url += "?token=" + s.cfg.Token
	}
	s.server = &http.Server{Handler: s.mountedHandler()}

	go func() {
		// http.ErrServerClosed is the normal shutdown path.
		_ = s.server.Serve(ln)
	}()

	return s, nil
}

// newServer builds a server without listening, which is what a test wants
// when it drives the handler through httptest.
func newServer(ctx context.Context, cfg Config, runner Runner, replayer Replayer) *Server {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg.BasePath = normalizeBasePath(cfg.BasePath)
	return &Server{
		cfg:      cfg,
		runner:   runner,
		replayer: replayer,
		baseCtx:  ctx,
		files:    newFileSet(),
		hub:      newHub(connectRecord()),
		runs:     make(map[string]*runEntry),
	}
}

// URL is where the UI can be reached.
func (s *Server) URL() string { return s.url }

// Handler is the whole UI as one http.Handler, unprefixed.
//
// Every route below is relative to wherever this handler is mounted, and
// every URL it hands out is relative too, so mounting it under a base path is
// an http.StripPrefix around this and nothing else (section 3.3). Start does
// exactly that itself, driven by Config.BasePath (see mountedHandler); call
// Handler directly only when embedding the server under a mux of your own.
func (s *Server) Handler() http.Handler {
	assets, err := fs.Sub(embedded, "assets")
	if err != nil {
		// The embed directive is checked at build time; a failure here would
		// mean the binary was assembled without its own frontend.
		panic("web: embedded assets are missing: " + err.Error())
	}

	// Every route that serves the API or the event stream goes through
	// authGuard; GET / (the embedded shell) deliberately does not - see
	// authGuard's doc comment for why gating it too would be self-defeating.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", s.authGuard(s.handleEvents))
	mux.HandleFunc("GET /runs", s.authGuard(s.handleListRuns))
	mux.HandleFunc("POST /runs", s.authGuard(s.handleStartRun))
	mux.HandleFunc("POST /runs/upload", s.authGuard(s.handleUploadTorrent))
	mux.HandleFunc("POST /runs/reopen", s.authGuard(s.handleReopenRun))
	mux.HandleFunc("POST /runs/cancel", s.authGuard(s.handleCancelRun))
	mux.HandleFunc("GET /files/{id}", s.authGuard(s.handleFile))
	mux.Handle("GET /", http.FileServerFS(assets))

	return mountRoot(mux)
}

// mountedHandler is what Start actually serves: Handler with cfg.BasePath
// applied. Handler itself stays unprefixed - a caller mounting the server
// under a reverse proxy's own mux (or a test proving the seam, as
// TestWorksUnderABasePath does) applies http.StripPrefix directly - so this
// exists only to give -base-path an effect when torpeek serves itself.
func (s *Server) mountedHandler() http.Handler {
	return mountBasePath(s.cfg.BasePath, s.Handler())
}

// mountBasePath wraps handler so it answers under base instead of the site
// root, the same way TestWorksUnderABasePath mounts it by hand: both the
// bare prefix and its trailing-slash form point at one http.StripPrefix, so
// a request landing on the prefix itself still reaches mountRoot's redirect
// rather than a 404. base must already be normalized (normalizeBasePath);
// "" mounts handler unchanged.
func mountBasePath(base string, handler http.Handler) http.Handler {
	if base == "" {
		return handler
	}

	stripped := http.StripPrefix(base, handler)
	mux := http.NewServeMux()
	mux.Handle(base, stripped)
	mux.Handle(base+"/", stripped)
	return mux
}

// normalizeBasePath turns whatever -base-path was given into the form the
// rest of the package expects: a single leading slash and no trailing one.
// "" and "/" both mean "no base path" - there is nothing for StripPrefix to
// strip either way - so both collapse to "", which mountBasePath treats as
// "mount at the root".
func normalizeBasePath(base string) string {
	base = strings.TrimSpace(base)
	if base == "" || base == "/" {
		return ""
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	return strings.TrimRight(base, "/")
}

// mountRoot handles being addressed by the mount point itself.
//
// Stripping a prefix from a request for exactly that prefix leaves an empty
// path, and serving the page there would break every relative asset in it -
// the browser would resolve them one level too high. The answer is the same
// one a directory listing gives: move to the trailing-slash form first.
func mountRoot(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "" {
			next.ServeHTTP(w, r)
			return
		}

		target, query, _ := strings.Cut(r.RequestURI, "?")
		target += "/"
		if query != "" {
			target += "?" + query
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

// StartRun accepts a run and returns what it was given: an id, and whether it
// took the slot or is waiting for it.
//
// It never refuses because another run is going. Only one runs at a time -
// two runs must not compete for the same output directory and traffic budget,
// and a pinned BitTorrent port cannot be bound twice - but that is now kept
// by making the second request wait rather than by turning it away. The error
// it can still return is about the request or the server, not about traffic:
// a blank source, or a server that has closed.
//
// A failure of the run itself - a source that does not parse, a torrent that
// cannot be opened - is not returned here. The runner is only ever called
// from the slot, which for a queued run is minutes after this returns, so the
// failure has to reach the client the same way for every run: as a failed
// event and a run_state of "failed" on that run's own stream.
func (s *Server) StartRun(req RunRequest) (RunInfo, error) {
	return s.startRun(req, nil)
}

// startRun is StartRun plus an optional cleanup, run once the run this
// request begins is over - win, lose, or never started. handleUploadTorrent
// is the only caller that passes one, to remove its staged temp file only
// once nothing can still be reading it.
func (s *Server) startRun(req RunRequest, cleanup func()) (RunInfo, error) {
	refuse := func(err error) (RunInfo, error) {
		if cleanup != nil {
			cleanup()
		}
		return RunInfo{}, err
	}

	req.Source = strings.TrimSpace(req.Source)
	if req.Source == "" {
		return refuse(errors.New("give a magnet link or a .torrent file"))
	}

	display := req.Source
	if req.Label != "" {
		display = req.Label
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return refuse(errClosed)
	}

	entry := &runEntry{
		id: newRunID(), source: display, req: req,
		state: RunQueued, cleanup: cleanup, queuedAt: time.Now(),
	}
	s.runs[entry.id] = entry
	s.order = append(s.order, entry.id)
	s.waiting = append(s.waiting, entry)
	rec := s.runStateRecordLocked(entry, true)
	s.mu.Unlock()

	// The run opens its own history the moment it is accepted, queued or not:
	// a page must be able to show a run it asked for before anything has
	// happened in it. reset marks the record that opens it.
	s.hub.begin(entry.id, rec)

	// Taking the slot here, in the caller's own goroutine, is what makes the
	// answer to "was I queued?" true rather than a guess: by the time this
	// returns, this run has either started or is behind one that has.
	s.dispatch()

	s.mu.Lock()
	info := entry.info()
	s.mu.Unlock()
	return info, nil
}

// dispatch gives the slot to the next waiting run, if it is free.
//
// It loops because a run can fail before it produces any events at all - a
// source that does not parse - and that must not leave the slot empty with
// runs still waiting behind it.
func (s *Server) dispatch() {
	for {
		s.mu.Lock()
		if s.stopped || s.running != nil || len(s.waiting) == 0 {
			s.mu.Unlock()
			return
		}
		entry := s.waiting[0]
		s.waiting = s.waiting[1:]

		ctx, cancel := context.WithCancel(s.baseCtx)
		events, err := s.runner(ctx, entry.req)
		if err != nil {
			cancel()
			entry.state, entry.err, entry.endedAt = RunFailed, err, time.Now()
			failure := s.record(entry, core.Failed{File: -1, Code: core.CodeOf(err), Err: err})
			state := s.runStateRecordLocked(entry, false)
			s.mu.Unlock()

			// The client learns why on the run's own stream: this is the one
			// place a start can fail after the request that asked for it has
			// already been answered.
			s.hub.publish(entry.id, failure)
			s.hub.publish(entry.id, state)
			// Nothing ever read this run's source, so nothing can still be
			// reading it.
			if entry.cleanup != nil {
				entry.cleanup()
			}
			s.trim()
			continue
		}

		entry.cancel, entry.state, entry.startedAt = cancel, RunRunning, time.Now()
		s.running = entry
		rec := s.runStateRecordLocked(entry, false)
		s.mu.Unlock()

		s.hub.publish(entry.id, rec)
		go s.pump(entry, events)
		return
	}
}

// ReopenRun replays a finished run from disk under a fresh registry entry, so
// its frames reach a client the same way a live run's do: by id, over the
// event stream, each file carrying a URL (Server.record does that for every
// run alike, live or replayed).
//
// infoHash and params address the run the way GET /runs already describes
// one for a disk-only row (TOR-54) - never an id, because the run being
// reopened may never have had one in this process: it may be a previous
// process's run entirely, or one this process itself ran and later trimmed
// from keepFinishedRuns.
//
// It never touches s.waiting or s.running: a cache hit costs no network and
// competes for neither the traffic budget nor the pinned port the queue
// exists to protect, so making it wait behind a live download would defend
// against a conflict that cannot happen. Unlike startRun, this runs
// pump synchronously rather than handing it to a goroutine - a replay is a
// handful of local file reads, not a wait on the network, so there is
// nothing to gain by returning before it is done, and the caller gets back
// the run's actual outcome (done or failed) instead of having to watch the
// stream to learn it.
func (s *Server) ReopenRun(infoHash, params string) (RunInfo, error) {
	infoHash = strings.TrimSpace(infoHash)
	params = strings.TrimSpace(params)
	if infoHash == "" || params == "" {
		return RunInfo{}, errors.New("give the infohash and params of a run on disk")
	}
	if s.replayer == nil {
		return RunInfo{}, errReplayUnavailable
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return RunInfo{}, errClosed
	}

	entry := &runEntry{
		id: newRunID(), source: "reopened", state: RunReplaying,
		infoHash: infoHash, cancel: func() {},
		queuedAt: time.Now(), startedAt: time.Now(),
	}
	s.runs[entry.id] = entry
	s.order = append(s.order, entry.id)
	rec := s.runStateRecordLocked(entry, true)
	s.mu.Unlock()

	// Opens this run's history before anything is published against it - the
	// same reason startRun calls begin before dispatch: hub.publish on a run
	// with no history yet still reaches a client connected right now, but
	// forgets the message rather than keeping it to replay to one that
	// connects a moment later, which would defeat the entire point of
	// reopening a run for a page that is not even open yet.
	s.hub.begin(entry.id, rec)

	events := s.replayer(infoHash, params)
	s.pump(entry, events)

	s.mu.Lock()
	info := entry.info()
	s.mu.Unlock()
	return info, nil
}

// CancelRun stops one run: the one in the slot, or one still waiting for it.
// Frames already written stay on disk, which is the whole point of cancelling
// rather than killing (section 2.10).
//
// An empty id means the run in the slot, which is what a page showing one run
// asks for.
//
// A queued run has no context to cancel - it never got one - so cancelling it
// is taking it out of the queue, which is why this cannot be left to the run
// itself to notice.
func (s *Server) CancelRun(id string) (RunInfo, error) {
	s.mu.Lock()

	entry := s.running
	if id != "" {
		entry = s.runs[id]
		if entry == nil {
			s.mu.Unlock()
			return RunInfo{}, ErrNoSuchRun
		}
	}
	if entry == nil {
		s.mu.Unlock()
		return RunInfo{}, errors.New("no run is in progress")
	}

	switch entry.state {
	case RunRunning:
		entry.cancelled = true
		cancel := entry.cancel
		info := entry.info()
		s.mu.Unlock()

		// The run ends on its own terms: it stops, writes what it has, and
		// closes its event stream, which is where pump records the outcome.
		cancel()
		return info, nil

	case RunQueued:
		entry.cancelled = true
		entry.state, entry.endedAt = RunCancelled, time.Now()
		s.dropWaitingLocked(entry)
		rec := s.runStateRecordLocked(entry, false)
		info := entry.info()
		s.mu.Unlock()

		// It will never start, so nothing will ever be reading its source.
		if entry.cleanup != nil {
			entry.cleanup()
		}
		s.hub.publish(entry.id, rec)
		s.trim()
		return info, nil

	default:
		info := entry.info()
		s.mu.Unlock()
		return info, fmt.Errorf("run %s is already %s", entry.id, info.State)
	}
}

// dropWaitingLocked takes one entry out of the queue.
func (s *Server) dropWaitingLocked(entry *runEntry) {
	for i, waiting := range s.waiting {
		if waiting == entry {
			s.waiting = append(s.waiting[:i], s.waiting[i+1:]...)
			return
		}
	}
}

// snapshot lists the registry, oldest first.
func (s *Server) snapshot() []RunInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]RunInfo, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.runs[id].info())
	}
	return out
}

// pump moves one run's events onto its own history and announces the end of
// the run, then hands the slot to whoever is next.
//
// dispatch is its usual caller, in its own goroutine, for the entry that
// currently holds the slot; ReopenRun also calls it, synchronously and for
// an entry that never held the slot at all. Both are safe: the "hands the
// slot to whoever is next" step only fires `if s.running == entry`, which is
// never true for a replay, so it is simply a no-op dispatch() call there
// rather than a special case this function has to know about.
func (s *Server) pump(entry *runEntry, events <-chan core.Event) {
	outcome, failure := RunDone, error(nil)

	for ev := range events {
		s.hub.publish(entry.id, s.record(entry, ev))

		switch e := ev.(type) {
		case core.MetadataReady:
			// The one thing that ties this run to the record it will leave on
			// disk, which is keyed by infohash rather than by run id.
			s.mu.Lock()
			entry.infoHash = e.InfoHash
			s.mu.Unlock()
		case core.Failed:
			// A run-scoped failure (File < 0) ends the run; a file-scoped one
			// is about one video and the run carries on.
			if e.File < 0 {
				outcome, failure = RunFailed, e.Err
			}
		case core.Done:
			if e.Reason == core.StopCancelled && outcome != RunFailed {
				outcome = RunCancelled
			}
		}
	}

	// After the stream, never before: swarm.Open re-reads a file Source from
	// inside the run's own goroutine long after the handler that staged it
	// replied, so the file can only go once the run is over (TOR-25).
	if entry.cleanup != nil {
		entry.cleanup()
	}

	s.mu.Lock()
	if outcome == RunDone && entry.cancelled {
		// A cancelled run whose stream ended without saying so - a runner
		// that simply closes its channel - is still a cancelled run.
		outcome = RunCancelled
	}
	entry.state, entry.err, entry.endedAt = outcome, failure, time.Now()
	if s.running == entry {
		s.running = nil
	}
	rec := s.runStateRecordLocked(entry, false)
	s.mu.Unlock()

	// The context outlives the channel only to be released here; the run is
	// over either way.
	entry.cancel()
	s.hub.publish(entry.id, rec)

	s.trim()
	s.dispatch()
}

// trim drops the oldest finished runs past keepFinishedRuns, with their
// histories. A queued or running run is never dropped, however old.
func (s *Server) trim() {
	s.mu.Lock()

	finished := 0
	var dropped []string
	for i := len(s.order) - 1; i >= 0; i-- {
		entry := s.runs[s.order[i]]
		if !entry.state.final() {
			continue
		}
		finished++
		if finished > keepFinishedRuns {
			dropped = append(dropped, entry.id)
		}
	}
	if len(dropped) == 0 {
		s.mu.Unlock()
		return
	}

	gone := make(map[string]bool, len(dropped))
	for _, id := range dropped {
		gone[id] = true
		delete(s.runs, id)
	}
	kept := s.order[:0]
	for _, id := range s.order {
		if !gone[id] {
			kept = append(kept, id)
		}
	}
	s.order = kept
	s.mu.Unlock()

	for _, id := range dropped {
		s.hub.drop(id)
	}
}

// record encodes one event for the wire.
//
// The object is exactly the one the CLI writes as NDJSON, so both clients
// describe the same run the same way, plus a URL for each file the event
// names - a browser cannot do anything with an absolute path on the server's
// disk, and the path stays alongside for a person reading the stream.
func (s *Server) record(entry *runEntry, ev core.Event) record {
	m := wire.Event(entry.id, ev)

	switch e := ev.(type) {
	case core.FrameReady:
		m["url"] = s.files.publish(e.Path)
	case core.FileDone:
		if e.SheetPath != "" {
			m["sheet_url"] = s.files.publish(e.SheetPath)
		}
		if e.ManifestPath != "" {
			m["manifest_url"] = s.files.publish(e.ManifestPath)
		}
	}

	rec := record{data: encode(m)}
	if p, ok := ev.(core.Progress); ok {
		rec.progress, rec.file = true, p.File
	}
	return rec
}

// runStateRecordLocked says where one run is. It is not a core event - the
// core has no opinion about a server holding runs - so it is named apart from
// the event vocabulary rather than mixed into it, but it carries the same
// "run" key every event does, because with more than one run a state message
// that did not say whose it is would be unreadable.
//
// state is the whole answer; active is the same answer for a client that only
// asks "is this one going", kept because it is what run_state has always
// meant - true for running and replaying alike, since both mean more events
// are still coming for this run, and false the moment either reaches a final
// state. reset marks the message that opens a run's history: a page clears
// what it shows for that run when it sees one. It is what tells a
// reconnecting page that what follows is the whole run, rather than more of
// what it already has.
//
// The caller must hold s.mu: every field read here is written under it.
func (s *Server) runStateRecordLocked(entry *runEntry, reset bool) record {
	m := map[string]any{
		"type": "run_state", "run": entry.id, "state": string(entry.state),
		"active": entry.state == RunRunning || entry.state == RunReplaying,
		"source": entry.source, "reset": reset,
	}
	if entry.infoHash != "" {
		m["infohash"] = entry.infoHash
	}
	if entry.err != nil {
		m["error"] = entry.err.Error()
	}
	return record{data: encode(m)}
}

// connectRecord is the first thing any client is sent: an idle run_state
// belonging to no run, marking the start of the history that follows.
//
// It has no "run" key because it is about the connection rather than about a
// run - a client that wants to know what the server holds reads the records
// after it, which are the runs. reset is what makes a reconnecting page throw
// away the stale copy it was holding before replaying.
func connectRecord() record {
	return record{data: encode(map[string]any{
		"type": "run_state", "active": false, "source": "", "reset": true,
	})}
}

func encode(m map[string]any) []byte {
	data, err := json.Marshal(m)
	if err != nil {
		// The maps here hold only values encoding/json accepts; a failure
		// would be a programming error, and swallowing it would show as a
		// silently missing event.
		return []byte(`{"type":"failed","file":-1,"code":"internal","error":"encode event: ` + err.Error() + `"}`)
	}
	return data
}

// Close stops serving, cancels the run in the slot and drops the queue.
//
// A queued run is cancelled rather than left behind: nothing will ever start
// it once the server is stopped, so nothing would ever run its cleanup, and
// an uploaded .torrent staged for a run that never happens would outlive the
// process that staged it. Marking them cancelled also means a Close during a
// queue leaves no run stuck in "queued" for whatever reads the registry next.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	running := s.running
	waiting := s.waiting
	s.waiting = nil
	ended := time.Now()
	for _, entry := range waiting {
		entry.cancelled = true
		entry.state, entry.endedAt = RunCancelled, ended
	}
	s.mu.Unlock()

	if running != nil {
		running.cancel()
	}
	for _, entry := range waiting {
		if entry.cleanup != nil {
			entry.cleanup()
		}
	}
	s.hub.close()

	if s.server == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	if err := s.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
