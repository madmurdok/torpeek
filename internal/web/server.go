package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	// (core.serveFromCache) reads. This package writes nothing there itself
	// and never decides a run's parameters from what it finds - it only
	// lists what cache.LoadRun can read back, per run.json (see listRuns).
	// Deleting one frame (TOR-70) is the single change that starts here, and
	// even that is core's to make: this package validates the address and
	// calls the Deleter it was handed, exactly as it calls a Runner rather
	// than building a run configuration itself.
	//
	// Empty means nothing on disk is listed, only the registry: a caller
	// building the server directly (most tests) has no output directory to
	// speak of, and leaving it empty rather than requiring one keeps that
	// working. A delete needs one and refuses without it.
	OutputRoot string

	// WatchDir is a directory a torrent client on this host watches for new
	// .torrent files. When it is set, the UI offers a button that copies a
	// run's saved .torrent into it; when it is empty the button is absent
	// altogether rather than shown and failing, and the one route that would
	// use it answers 503 (SendToWatchDir).
	//
	// It exists because downloading and queueing are two different things on
	// the deployment this tool targets (REQUIREMENTS.md section 4.1): the UI
	// runs on the seedbox and the browser runs on a laptop, so the download
	// link puts the .torrent on the laptop. A watch directory is the one way
	// to say "start this where I just checked it" that needs no credentials
	// and no client API - every client on a managed host already has one.
	//
	// This is the only directory outside the output root this package ever
	// writes to, and it never comes from a request: it is a flag, and a
	// request can only name which already-announced .torrent to copy into it.
	WatchDir string

	// DefaultCount is how many frames per video file a run takes when the
	// request does not say - the -n flag, which runConfig starts from. The
	// server does not decide it and does not use it: GET /defaults only
	// reports it, so the page can show the number that is actually in force
	// instead of a copy of the default written into the HTML, which would
	// quietly disagree with a server started as -n 6. Zero means "not
	// stated" (a caller building the server directly, most tests), and the
	// page then leaves its field empty - which sends no count at all, and
	// so still gets the server's default.
	DefaultCount int
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
	// Files narrows the run to some of the torrent's video files - the same
	// specs cfg.Files and swarm.Select already understand, so a tick-box UI
	// need only send the indices metadata_ready just told it about. Empty
	// means every video file, the same as leaving -file off on the command
	// line.
	Files []string `json:"files,omitempty"`
	// Count is how many frames per video file the run should take. Zero
	// means the server's own default - the -n flag runConfig starts from -
	// which is why it is an int and not a pointer: a page that has not
	// chosen a number sends nothing, and "nothing" and "the default" have
	// to be the same request. Negative is refused outright (startRun), so
	// the only value that reaches frames.Plan.Validate from here is one
	// somebody typed.
	//
	// Beware what the number means: it is frames PER video file, and a
	// torrent bundling six quality variants multiplies it (TOR-50).
	Count int `json:"count,omitempty"`
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

// Deleter removes one frame of one result set from disk - the record in the
// file's manifest and the frame file both - and reports whether it could.
//
// It is injected for the same reason Runner and Replayer are: this package
// does not decide what a run is and does not write under the output root.
// core.DeleteFrame is the one real implementation, and everything that makes
// a delete safe (the manifest written before the file is unlinked, survivors
// never renumbered, a file whose last frame is gone taken out of run.json's
// Complete) lives there with the cache rules it has to keep. Here there is
// only an address to validate.
//
// It takes no context: like a replay, this is a handful of local file
// operations with nothing worth cancelling.
type Deleter func(infoHash, params string, fileIndex, frameIndex int) error

// Lister reports what a torrent holds - its name, its infohash and its video
// files - without capturing anything from it.
//
// It is injected for the same reason Runner is: this package does not build
// a run configuration, and a listing has to start from the very same one a
// run does, or the two would disagree about the pinned port, the DHT switch
// and the known peers while looking at the same torrent (cli/web.go's
// serveWeb builds both from one base config). It takes only a source,
// because nothing else about a request can change what a torrent contains.
//
// Unlike Replayer and Deleter it takes a context, and that difference is the
// point: those are a handful of local file reads, while this is a wait on
// the swarm for metadata bounded only by swarm.Config.MetadataTimeout. It is
// the one injected call worth cancelling - cancelling a torrent that is
// still fetching its file list cancels exactly this.
//
// Nil is allowed and means no parking at all: every run goes straight to the
// runner, which is what this server did before TOR-67 and what every test
// that is not about the picker still does.
type Lister func(ctx context.Context, source string) (core.Contents, error)

// ErrNoSuchRun is returned when a request names a run this server does not
// hold - never did, or held and has since forgotten (see keepFinishedRuns).
var ErrNoSuchRun = errors.New("no such run")

// errClosed is what a request gets once Close has run.
var errClosed = errors.New("web: server is closed")

// errReplayUnavailable is what ReopenRun returns when the server was built
// with no Replayer - a test driving newServer directly, most often, since the
// real entry point (cli/web.go) always supplies one alongside its Runner.
var errReplayUnavailable = errors.New("web: reopening a run from disk is not available")

// errBadRequest marks a request this server can read but not act on - a
// malformed result set name, today - so a handler can answer 400 without
// matching on message text.
var errBadRequest = errors.New("web: malformed request")

// errDeleteUnavailable is DELETE's counterpart to errReplayUnavailable: a
// server built with no Deleter, or with no output root to delete from,
// refuses rather than pretending it removed something.
var errDeleteUnavailable = errors.New("web: deleting a frame is not available")

// errWatchUnavailable is what SendToWatchDir returns on a server started
// without -watch-dir. The page does not show the button at all in that case
// (GET /defaults reports whether it should), so reaching this is a client
// asking for something it was told is not there - answered as 503, the same
// way the other "this server was not built for that" refusals are.
var errWatchUnavailable = errors.New("web: no watch directory is configured")

// errNoSuchTorrent is what SendToWatchDir returns when the id it was given is
// not one this server minted for a run's .torrent - an id it never published,
// or one that names some other artefact (a frame, a sheet, a manifest). Both
// collapse into one 404 on purpose: the request named nothing this route can
// act on, and which of the two it was is a detail for the message.
var errNoSuchTorrent = errors.New("web: no such saved torrent")

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
	deleter  Deleter
	lister   Lister
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
// replayer, deleter and lister may all be nil - the routes that need the
// first two then answer 503 rather than the server refusing to start, the
// same tolerance Config.OutputRoot's own doc describes for a caller with no
// disk-backed run to speak of, and a nil lister simply means no torrent ever
// parks for a file selection (see Lister). The real entry point (cli/web.go)
// always supplies all three, paired with runner.
func Start(ctx context.Context, cfg Config, runner Runner, replayer Replayer, deleter Deleter, lister Lister) (*Server, error) {
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

	s := newServer(ctx, cfg, runner, replayer, deleter, lister)
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
func newServer(ctx context.Context, cfg Config, runner Runner, replayer Replayer, deleter Deleter, lister Lister) *Server {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg.BasePath = normalizeBasePath(cfg.BasePath)
	return &Server{
		cfg:      cfg,
		runner:   runner,
		replayer: replayer,
		deleter:  deleter,
		lister:   lister,
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
	mux.HandleFunc("GET /defaults", s.authGuard(s.handleDefaults))
	mux.HandleFunc("GET /runs", s.authGuard(s.handleListRuns))
	mux.HandleFunc("GET /runs/{infohash}/files/{index}", s.authGuard(s.handleFileDetail))
	mux.HandleFunc("POST /runs", s.authGuard(s.handleStartRun))
	mux.HandleFunc("POST /runs/upload", s.authGuard(s.handleUploadTorrent))
	mux.HandleFunc("POST /runs/reopen", s.authGuard(s.handleReopenRun))
	mux.HandleFunc("POST /runs/cancel", s.authGuard(s.handleCancelRun))
	mux.HandleFunc("POST /runs/decide", s.authGuard(s.handleDecideRun))
	mux.HandleFunc("DELETE /runs/{infohash}/files/{index}/frames/{frame}", s.authGuard(s.handleDeleteFrame))
	mux.HandleFunc("GET /files/{id}", s.authGuard(s.handleFile))
	mux.HandleFunc("POST /files/{id}/watch", s.authGuard(s.handleWatchTorrent))
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

	// Refused here rather than left to frames.Plan.Validate inside the
	// engine: that failure would arrive minutes later as a failed run on a
	// 202-accepted request, where a number the client can see is wrong
	// before anything is queued is a 400 about the request itself. Zero is
	// not a rejection - it is how a page says "whatever the server was
	// started with".
	if req.Count < 0 {
		return refuse(fmt.Errorf("frames per file cannot be negative, got %d", req.Count))
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
//
// A run that still has to be told which files to capture takes the slot for
// its metadata pass first (needsListingLocked, listThenRun). That pass costs
// the same pinned BitTorrent port a run costs, which is why it happens here
// rather than beside the queue: RunReplaying's exemption does not transfer -
// a replay reads local files, a listing opens a swarm session.
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

		if s.needsListingLocked(entry) {
			// The slot is taken before a single byte is asked for, and the
			// state is "running" because that is what this is: the run's own
			// first phase, on the run's own cancellable context, holding the
			// one thing a second run could not share.
			entry.cancel, entry.state, entry.startedAt = cancel, RunRunning, time.Now()
			s.running = entry
			rec := s.runStateRecordLocked(entry, false)
			s.mu.Unlock()

			s.hub.publish(entry.id, rec)
			// Its own goroutine: this waits on the swarm for metadata, up to
			// swarm.Config.MetadataTimeout, and dispatch is called from the
			// goroutine that is answering an HTTP request.
			go s.listThenRun(ctx, cancel, entry)
			return
		}

		if s.beginRun(ctx, cancel, entry) {
			return
		}
		// The runner refused this one before it produced any events; the slot
		// is still free, so the next waiter gets its turn immediately.
	}
}

// needsListingLocked reports whether this entry has to be told what the
// torrent holds before anything can be captured from it.
//
// A request that already names files has been decided - by the picker, or by
// Regenerate, which asks for the one file a person is already looking at -
// so paying for a metadata pass to offer a choice nobody is waiting to make
// would only delay it. An entry that has already listed once (a parked
// torrent coming back through the queue with its selection) never lists
// again. And a server built without a Lister keeps the behaviour it had
// before TOR-67: straight into the run.
//
// The caller must hold s.mu.
func (s *Server) needsListingLocked(entry *runEntry) bool {
	return s.lister != nil && !entry.listed && len(entry.req.Files) == 0
}

// beginRun hands one entry to the runner and puts its events on the wire. It
// reports whether the run actually began: false means the runner refused it,
// the entry is already recorded as failed on its own stream, and the slot is
// free for whoever is next.
//
// It is shared by the two places a run can start - dispatch, for a request
// that needed no listing, and listThenRun, for one whose metadata pass found
// a single video file. The second must not release the slot between the two
// phases, or a torrent queued behind it could jump in front of a request
// that has already paid for its metadata; that is why taking the slot is
// this function's own step rather than something dispatch did beforehand.
//
// The caller must hold s.mu, and beginRun releases it before it returns: the
// hub is never written to under the registry lock, and neither is cleanup
// called under it.
func (s *Server) beginRun(ctx context.Context, cancel context.CancelFunc, entry *runEntry) bool {
	events, err := s.runner(ctx, entry.req)
	if err != nil {
		cancel()
		entry.state, entry.err, entry.endedAt = RunFailed, err, time.Now()
		failure := s.record(entry, core.Failed{File: -1, Code: core.CodeOf(err), Err: err})
		state := s.runStateRecordLocked(entry, false)
		s.releaseSlotLocked(entry)
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
		return false
	}

	entry.cancel, entry.state, entry.startedAt = cancel, RunRunning, time.Now()
	s.running = entry
	rec := s.runStateRecordLocked(entry, false)
	s.mu.Unlock()

	s.hub.publish(entry.id, rec)
	go s.pump(entry, events)
	return true
}

// listThenRun is a run's first phase: find out what the torrent holds, then
// either capture it or stop and ask.
//
// It runs inside the slot, because a listing opens a full swarm session on
// the same pinned port a run uses, and it deliberately does not go through
// pump - pump reads a stream that ends as a run that finished, which is the
// one thing a metadata pass must not be mistaken for.
//
// Three ways out, and only one of them keeps the slot:
//   - the listing failed, or a cancel or a Close overtook it: the entry ends
//     here, the slot goes back;
//   - more than one video file: the file list goes out as a needs_action
//     record, the entry parks in RunNeedsAction, and the slot goes back so
//     the next torrent runs while this one waits for a person;
//   - one video file, or none: the run starts immediately, still holding the
//     slot, because there is nothing to ask about.
func (s *Server) listThenRun(ctx context.Context, cancel context.CancelFunc, entry *runEntry) {
	contents, err := s.lister(ctx, entry.req.Source)

	s.mu.Lock()
	entry.listed = true
	if err == nil {
		entry.contents = &contents
		// The same tie metadata_ready makes for a live run: the infohash is
		// what joins this entry to the record its run will leave on disk.
		entry.infoHash = contents.InfoHash
	}

	// Which of the three happens, and the state the entry moves to, are
	// decided in one locked step. CancelRun reads that state to choose what
	// cancelling this entry means, so a decision taken here and written a
	// moment later would let a cancel land in the gap and be lost - a run
	// answered as "cancelling" that then parks itself and waits for a person
	// who has already walked away.
	switch {
	case entry.cancelled || s.stopped:
		// A cancel, or a Close, that landed while the metadata was still in
		// flight. Usually it is what ended the listing (the error above is
		// its own context being cancelled), but it can also arrive just
		// after a listing that succeeded - which is exactly why this is
		// checked before the outcome and not after.
		entry.state, entry.endedAt = RunCancelled, time.Now()
	case err != nil:
		// The listing is the run's first phase, so a listing that fails is a
		// run that failed, reported on the run's own stream exactly the way
		// beginRun reports a runner that refused to start.
		entry.state, entry.err, entry.endedAt = RunFailed, err, time.Now()
	case len(contents.Videos) > 1:
		// Nothing more happens until a person picks (RunNeedsAction).
		entry.state = RunNeedsAction
	default:
		// One video file - or none, which is the engine's own failure to
		// report rather than a choice worth offering - so there is nothing
		// to ask about and the run starts here, still holding the slot.
		if !s.beginRun(ctx, cancel, entry) {
			s.dispatch()
		}
		return
	}

	// Everything that reaches here is done with the slot. A parked entry
	// holds no context either: its listing is over, and the wait that
	// follows can last hours, so the context is released rather than left
	// hanging off s.baseCtx for the life of the process.
	entry.cancel = nil
	s.releaseSlotLocked(entry)
	var failure, notice record
	switch entry.state {
	case RunFailed:
		failure = s.record(entry, core.Failed{File: -1, Code: core.CodeOf(err), Err: err})
	case RunNeedsAction:
		notice = s.needsActionRecordLocked(entry)
	}
	state := s.runStateRecordLocked(entry, false)
	parked := entry.state == RunNeedsAction
	s.mu.Unlock()

	cancel()

	if failure.data != nil {
		s.hub.publish(entry.id, failure)
	}
	if notice.data != nil {
		s.hub.publish(entry.id, notice)
	}
	s.hub.publish(entry.id, state)

	// A parked entry keeps its source: the run it is waiting for has not
	// happened yet, and an uploaded .torrent staged for it is exactly what
	// the run will be started from once someone ticks a box. Its cleanup
	// happens when the run ends, or when the cancel does (CancelRun, Close).
	if !parked {
		if entry.cleanup != nil {
			entry.cleanup()
		}
		s.trim()
	}
	s.dispatch()
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

// DeleteFrame removes one frame of one result set and answers with what that
// file has left, gathered from disk exactly as GET
// /runs/{infohash}/files/{index} gathers it - so a page re-renders from what
// is actually there rather than from its own guess at what a delete did.
//
// Both strings reach the filesystem, so both are checked to be what they
// claim: an infohash is a 40-character hex digest and a params key is
// core.ParamsKey's 16. Everywhere else this package serves only paths its own
// event stream named (see files.go), which is why this is one of the two
// places a guard is needed at all - the other being fileDetail, whose
// validInfoHash this shares. (ReopenRun, which also takes a params from a
// request, only trims it: it addresses a directory to read, not one to
// remove from, and tightening it is not this change's to make.)
//
// A file whose frames are now all gone answers an empty detail rather than
// the 404 fileDetail reports for it: the delete did happen, and "there is
// nothing left" is the answer to the request that emptied it.
func (s *Server) DeleteFrame(infoHash, params string, fileIndex, frameIndex int) (FileDetail, error) {
	if s.deleter == nil || s.cfg.OutputRoot == "" {
		return FileDetail{}, errDeleteUnavailable
	}
	if !validInfoHash(infoHash) || fileIndex < 0 || frameIndex < 0 {
		return FileDetail{}, fmt.Errorf("%w: no frame %d of file %d under %s",
			core.ErrNoSuchFrame, frameIndex, fileIndex, infoHash)
	}
	if !validParams(params) {
		return FileDetail{}, fmt.Errorf("%w: %q is not a result set", errBadRequest, params)
	}

	// A stale contact sheet is not a failed delete. The frame and its record
	// are already gone by the time rebuilding the sheet is even attempted
	// (core.DeleteFrame explains why in that order), so the error is carried
	// back alongside the truth about the file rather than instead of it - the
	// handler answers with both, and a person is told what happened rather
	// than being told the delete failed and left to discover otherwise on a
	// reload (TOR-78).
	deleteErr := s.deleter(infoHash, params, fileIndex, frameIndex)
	if deleteErr != nil && !errors.Is(deleteErr, core.ErrSheetStale) {
		return FileDetail{}, deleteErr
	}

	detail, ok := s.fileDetail(infoHash, fileIndex)
	if !ok {
		// Explicitly empty rather than left nil: the page replaces its grid
		// with this list, and a JSON null would read as "no answer" where
		// what is meant is "no frames".
		return FileDetail{Index: fileIndex, Sets: []FrameSet{}, Frames: []FrameRef{}}, deleteErr
	}
	return detail, deleteErr
}

// SendToWatchDir copies one run's saved .torrent into the watch directory, so
// a torrent client already running on this host picks it up, and answers with
// where it put it.
//
// This is deliberately a second feature rather than the download link's
// server-side half. A download hands the file to the BROWSER, which on the
// deployment this tool targets is a laptop somewhere else; a watch directory
// hands it to a client on the machine the UI is running on. Both are useful
// and they are not substitutes, which is why the page offers both when it can
// and only the download when it cannot.
//
// It is addressed by the same files/{id} handle the download uses rather than
// by infohash and params, and that is the whole reason no new guard appears
// here: an id is not a path, it is a key into the registry of paths this
// server's own event stream published (fileSet), so nothing a request carries
// can name a file that was never announced. The extension check on top of that
// is not path safety - it is scope: every frame and sheet of every run is
// published under the same registry, and "send this to my torrent client"
// must mean a torrent, not whatever handle a page had lying around.
//
// The copy is written to a temporary file in the watch directory and renamed
// into place. A watch directory is, by definition, being watched: a client
// that sees the file appear opens it immediately, and a partially written one
// is a corrupt torrent it will refuse - permanently, for the clients that
// remember having rejected it. The temporary name starts with a dot and does
// not end in .torrent, so it does not match what a client is looking for even
// during the moment it exists.
func (s *Server) SendToWatchDir(id string) (string, error) {
	if s.cfg.WatchDir == "" {
		return "", errWatchUnavailable
	}

	path, ok := s.files.lookup(id)
	if !ok || !isTorrentPath(path) {
		return "", fmt.Errorf("%w: %q does not name a run's .torrent", errNoSuchTorrent, id)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the saved torrent: %w", err)
	}

	tmp, err := os.CreateTemp(s.cfg.WatchDir, ".torpeek-*.part")
	if err != nil {
		return "", fmt.Errorf("write into the watch directory: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("write into the watch directory: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("write into the watch directory: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("write into the watch directory: %w", err)
	}

	dest := filepath.Join(s.cfg.WatchDir, filepath.Base(path))
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("place the torrent in the watch directory: %w", err)
	}
	return dest, nil
}

// isTorrentPath reports whether a published path is a run's own .torrent
// rather than one of the frames, sheets and manifests published beside it.
//
// The extension is the whole test, and it is enough because these paths are
// not user input: every one of them was written by output.Writer, which is
// the only thing that ever names a file .torrent under the output root
// (output.Layout.TorrentPath).
func isTorrentPath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".torrent")
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
// itself to notice. A torrent parked for a file selection is the same case
// for a different reason: it had a context and gave it back when it released
// the slot, and nothing is watching it that could notice anything.
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

	case RunNeedsAction:
		// A parked torrent is cancelled the way a queued one is, and for the
		// same reason: there is nothing running to ask to stop. It is a
		// registry edit - mark it, tell the page, let trim have it - plus
		// the cleanup of a staged upload the run it was waiting for will now
		// never read.
		//
		// entry.cancel must not be called: listThenRun set it to nil when it
		// gave the slot back, which is the honest record of what a parked
		// entry holds (RunNeedsAction). ReopenRun avoids the same nil the
		// other way, by handing its entry a no-op cancel, and that is right
		// there because it shares pump with live runs and pump calls
		// entry.cancel unconditionally at the end of every stream. Nothing
		// runs for a parked entry at all, so there is no shared path to
		// satisfy here and a placeholder closure would only claim there is
		// something to cancel.
		//
		// It is never in s.waiting either, so dropWaitingLocked would be a
		// no-op walk; leaving it out says so, instead of implying it might
		// be in the queue.
		entry.cancelled = true
		entry.state, entry.endedAt = RunCancelled, time.Now()
		rec := s.runStateRecordLocked(entry, false)
		info := entry.info()
		s.mu.Unlock()

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

// DecideRun answers the question a parked torrent asked: these are the files
// to capture. The entry goes back into the queue - the same entry, so one
// torrent stays one row and one history, needs-action to queued to running -
// and returns to the runner with the selection in its request.
//
// Only an entry in RunNeedsAction can be decided. Anything else is either a
// run this server does not hold (ErrNoSuchRun, a 404) or one that is not
// waiting to be told anything (a 409, matching how CancelRun answers a run
// that has already ended).
//
// files is checked against the video files the listing actually found rather
// than passed through to the run. Otherwise a page left open across two
// different torrents would send an index this one does not have, and
// swarm.Select's ErrNoFileMatch would surface minutes later as a failed run
// instead of immediately as a 400 about the request that was wrong. An empty
// selection is refused for a different reason: it means "every file" to
// cfg.Files, and a person looking at a picker who wants everything can tick
// everything - reading a blank answer as "all of it" is how a torrent gets
// captured six times over (TOR-50).
//
// count carries the intake's frames-per-file when the page sends one, so the
// number chosen while looking at the picker is the number the run uses. Zero
// leaves whatever the request already had, which is the server's own -n.
func (s *Server) DecideRun(id string, files []string, count int) (RunInfo, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return RunInfo{}, ErrNoSuchRun
	}
	if len(files) == 0 {
		return RunInfo{}, fmt.Errorf("%w: pick at least one file to capture", errBadRequest)
	}
	if count < 0 {
		return RunInfo{}, fmt.Errorf("%w: frames per file cannot be negative, got %d", errBadRequest, count)
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return RunInfo{}, errClosed
	}

	entry := s.runs[id]
	if entry == nil {
		s.mu.Unlock()
		return RunInfo{}, ErrNoSuchRun
	}
	if entry.state != RunNeedsAction || entry.contents == nil {
		info := entry.info()
		s.mu.Unlock()
		return info, fmt.Errorf("run %s is not waiting for a file selection, it is %s", entry.id, info.State)
	}

	chosen := make([]string, 0, len(files))
	for _, spec := range files {
		spec = strings.TrimSpace(spec)
		if !entry.holdsFile(spec) {
			s.mu.Unlock()
			return RunInfo{}, fmt.Errorf("%w: %q is not a video file of this torrent", errBadRequest, spec)
		}
		chosen = append(chosen, spec)
	}

	entry.req.Files = chosen
	if count > 0 {
		entry.req.Count = count
	}
	entry.state = RunQueued
	s.waiting = append(s.waiting, entry)
	rec := s.runStateRecordLocked(entry, false)
	s.mu.Unlock()

	s.hub.publish(entry.id, rec)
	// The same call StartRun makes, in the caller's own goroutine and for the
	// same reason: by the time this returns, this torrent has either started
	// or is behind one that has - so the state answered here is the truth
	// rather than a guess, exactly as it is for a fresh run.
	s.dispatch()

	s.mu.Lock()
	info := entry.info()
	s.mu.Unlock()
	return info, nil
}

// holdsFile reports whether spec names one of the video files this entry's
// listing found.
//
// Only a torrent index, never the path patterns swarm.Select also accepts. A
// picker sends back the indices it was handed in the needs_action record, so
// anything else arriving here is a page guessing rather than a person
// choosing - and a pattern would have to be matched twice, loosely here and
// for real in swarm.Select, which is how the two would come to disagree
// about what was picked.
//
// The caller must hold the server's lock: contents is written under it.
func (e *runEntry) holdsFile(spec string) bool {
	index, err := strconv.Atoi(spec)
	if err != nil {
		return false
	}
	for _, video := range e.contents.Videos {
		if video.Index == index {
			return true
		}
	}
	return false
}

// releaseSlotLocked gives the single slot back, if this entry is what holds
// it.
//
// The one place s.running is ever cleared. Three paths reach it - a run
// whose event stream ended (pump), a run the runner refused before it
// produced a stream (beginRun), and a torrent parked for someone to choose
// files (listThenRun) - and a second assignment written by hand in any of
// them would be free to drift from the others.
//
// The guard is not a formality. pump also runs for a replay, which never
// took the slot at all (ReopenRun), and clearing it there would take the
// slot away from whoever legitimately holds it; the same guard makes
// beginRun's failure path safe whether or not its caller had already taken
// the slot for a listing.
//
// The caller must hold s.mu.
func (s *Server) releaseSlotLocked(entry *runEntry) {
	if s.running == entry {
		s.running = nil
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
	s.releaseSlotLocked(entry)
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
	case core.Done:
		// The run's own .torrent, published the same way every other artefact
		// is: the page can only ask for a path this server's event stream
		// named (files.go), and announcing it here is also what makes the
		// link appear exactly when the file is really on disk - a run that
		// could not write one, or a cached run captured before torpeek kept
		// one, announces no path and so gets no link, with nothing having to
		// guess. It is why this needed no new route and no new path guard.
		if e.TorrentPath != "" {
			m["torrent_url"] = s.files.publish(e.TorrentPath)
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
// state. False for needs-action too, and that is not a special case: nothing
// more is coming for a parked torrent until a person acts, which is exactly
// what active has always meant. reset marks the message that opens a run's history: a page clears
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

// needsActionRecordLocked is the file list a parked torrent is waiting on.
//
// Like run_state it is not a core event - the core has no opinion about a
// server that stops and asks a person something - so it is built here rather
// than in the event vocabulary, and carries the same "run" key everything
// else on a run's stream carries.
//
// Deliberately not a metadata_ready. That event means a capture has begun
// and its "selected" list says what is already being worked on; sending it
// here would tell the page traffic is being spent when the whole point of
// this record is that none is. A type of its own also lands in the run's own
// backlog, so a page opened or reconnected an hour later replays it and
// rebuilds the picker without asking the server for anything.
//
// The per-file shape is wire.VideoFiles, the same one metadata_ready uses,
// so the page has exactly one notion of what a video file is.
//
// The caller must hold s.mu: every field read here is written under it.
func (s *Server) needsActionRecordLocked(entry *runEntry) record {
	contents := entry.contents
	return record{data: encode(map[string]any{
		"type": "needs_action", "run": entry.id,
		"name": contents.Name, "infohash": contents.InfoHash,
		"private": contents.Private, "videos": wire.VideoFiles(contents.Videos),
	})}
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
//
// A torrent parked for a file selection needs the same treatment for the
// same reasons, and cannot be reached the same way: it is neither in the
// queue nor in the slot - that is what parking means - so it has to be found
// in the registry itself. Left alone it would sit in "needs-action" on a
// stopped server, waiting for a decision no route is left to accept.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	running := s.running
	ended := time.Now()

	// Everything that was going to start and now never will: the queue, plus
	// every torrent parked for a decision.
	stranded := s.waiting
	s.waiting = nil
	for _, id := range s.order {
		if entry := s.runs[id]; entry.state == RunNeedsAction {
			stranded = append(stranded, entry)
		}
	}
	for _, entry := range stranded {
		entry.cancelled = true
		entry.state, entry.endedAt = RunCancelled, ended
	}
	s.mu.Unlock()

	if running != nil {
		running.cancel()
	}
	for _, entry := range stranded {
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
