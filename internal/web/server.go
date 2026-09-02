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

// DefaultAddr is where the UI listens when nothing says otherwise.
//
// A fixed default rather than a free port: a managed host allocates a range
// and forbids anything outside it (REQUIREMENTS.md section 4.1), so the port
// has to be something a person can be told and a proxy can be pointed at.
// There it must be overridden, which is what Config.Addr is for.
const DefaultAddr = "127.0.0.1:8765"

// Config configures the UI server.
type Config struct {
	// Addr is the listen address. Both the host and the port are part of it:
	// loopback is right on a desktop, while a seedbox behind a proxy binds
	// somewhere else on a port out of its allocated range.
	Addr string

	// ShutdownTimeout bounds how long Close waits for in-flight requests.
	ShutdownTimeout time.Duration
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

// ErrRunInProgress is returned when a run is asked for while one is going.
var ErrRunInProgress = errors.New("a run is already in progress")

// Server serves the embedded UI and one run's event stream.
type Server struct {
	cfg     Config
	runner  Runner
	baseCtx context.Context

	server *http.Server
	url    string

	files *fileSet
	hub   *hub

	mu      sync.Mutex
	run     *runState
	stopped bool
}

type runState struct {
	source string
	cancel context.CancelFunc
	active bool
	// cleanup runs once, after the run's event stream ends. It exists for
	// handleUploadTorrent's staged temp file: swarm.Open re-reads a file
	// Source from disk (AddTorrentFromFile) from inside the run's own
	// goroutine, not during the synchronous ParseSource call StartRun already
	// waited on - so the file has to outlive StartRun's return and can only
	// be removed once the run that might still be reading it is over.
	cleanup func()
}

// Start listens and begins serving. The returned server must be closed.
//
// It does not open a browser: on a seedbox there is nothing to open, and a
// test must never depend on one. The caller decides, with OpenBrowser.
func Start(ctx context.Context, cfg Config, runner Runner) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 5 * time.Second
	}
	if runner == nil {
		return nil, errors.New("web: a runner is required")
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("web listen on %s: %w", cfg.Addr, err)
	}

	s := newServer(ctx, cfg, runner)
	s.url = "http://" + ln.Addr().String() + "/"
	s.server = &http.Server{Handler: s.Handler()}

	go func() {
		// http.ErrServerClosed is the normal shutdown path.
		_ = s.server.Serve(ln)
	}()

	return s, nil
}

// newServer builds a server without listening, which is what a test wants
// when it drives the handler through httptest.
func newServer(ctx context.Context, cfg Config, runner Runner) *Server {
	if ctx == nil {
		ctx = context.Background()
	}
	s := &Server{
		cfg:     cfg,
		runner:  runner,
		baseCtx: ctx,
		files:   newFileSet(),
		hub:     newHub(),
	}
	s.hub.reset(s.stateRecord(true))
	return s
}

// URL is where the UI can be reached.
func (s *Server) URL() string { return s.url }

// Handler is the whole UI as one http.Handler.
//
// Every route below is relative to wherever this handler is mounted, and
// every URL it hands out is relative too, so mounting it under a base path is
// an http.StripPrefix around this and nothing else (section 3.3).
func (s *Server) Handler() http.Handler {
	assets, err := fs.Sub(embedded, "assets")
	if err != nil {
		// The embed directive is checked at build time; a failure here would
		// mean the binary was assembled without its own frontend.
		panic("web: embedded assets are missing: " + err.Error())
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("POST /runs", s.handleStartRun)
	mux.HandleFunc("POST /runs/upload", s.handleUploadTorrent)
	mux.HandleFunc("POST /runs/cancel", s.handleCancelRun)
	mux.HandleFunc("GET /files/{id}", s.handleFile)
	mux.Handle("GET /", http.FileServerFS(assets))

	return mountRoot(mux)
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

// StartRun begins a run, replacing nothing: only one runs at a time, so that
// two tabs cannot quietly compete for the same output directory and traffic
// budget. The caller cancels the current one first.
func (s *Server) StartRun(req RunRequest) error {
	return s.startRun(req, nil)
}

// startRun is StartRun plus an optional cleanup, run once the run this
// request begins is over - win, lose, or never started. handleUploadTorrent
// is the only caller that passes one, to remove its staged temp file only
// once nothing can still be reading it.
func (s *Server) startRun(req RunRequest, cleanup func()) error {
	done := func(err error) error {
		if cleanup != nil {
			cleanup()
		}
		return err
	}

	req.Source = strings.TrimSpace(req.Source)
	if req.Source == "" {
		return done(errors.New("give a magnet link or a .torrent file"))
	}

	s.mu.Lock()

	if s.stopped {
		s.mu.Unlock()
		return done(errors.New("web: server is closed"))
	}
	if s.run != nil && s.run.active {
		s.mu.Unlock()
		return done(ErrRunInProgress)
	}

	ctx, cancel := context.WithCancel(s.baseCtx)
	events, err := s.runner(ctx, req)
	if err != nil {
		cancel()
		s.mu.Unlock()
		return done(err)
	}

	display := req.Source
	if req.Label != "" {
		display = req.Label
	}
	state := &runState{source: display, cancel: cancel, active: true, cleanup: cleanup}
	s.run = state
	rec := s.stateRecordLocked(true)
	s.mu.Unlock()

	// A new run starts the history over: what the page shows is this run.
	s.hub.reset(rec)
	go s.pump(state, events)

	return nil
}

// CancelRun stops the current run. Frames already written stay on disk, which
// is the whole point of cancelling rather than killing (section 2.10).
func (s *Server) CancelRun() error {
	s.mu.Lock()
	state := s.run
	s.mu.Unlock()

	if state == nil || !state.active {
		return errors.New("no run is in progress")
	}
	state.cancel()
	return nil
}

// pump moves the run's events onto the hub and announces the end of the run.
func (s *Server) pump(state *runState, events <-chan core.Event) {
	for ev := range events {
		s.hub.publish(s.record(ev))
	}

	if state.cleanup != nil {
		state.cleanup()
	}

	s.mu.Lock()
	state.active = false
	rec := s.stateRecordLocked(false)
	s.mu.Unlock()

	// The context outlives the channel only to be released here; the run is
	// over either way.
	state.cancel()
	s.hub.publish(rec)
}

// record encodes one event for the wire.
//
// The object is exactly the one the CLI writes as NDJSON, so both clients
// describe the same run the same way, plus a URL for each file the event
// names - a browser cannot do anything with an absolute path on the server's
// disk, and the path stays alongside for a person reading the stream.
func (s *Server) record(ev core.Event) record {
	m := wire.Event(ev)

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

// stateRecord says whether a run is going. It is not a core event - the core
// has no opinion about a server holding a run - so it is named apart from the
// event vocabulary rather than mixed into it.
//
// reset marks the message that opens a history: the page clears what it shows
// when it sees one. It is what tells a reconnecting page that the replay
// which follows is the whole run, rather than more of what it already has.
func (s *Server) stateRecord(reset bool) record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateRecordLocked(reset)
}

func (s *Server) stateRecordLocked(reset bool) record {
	m := map[string]any{"type": "run_state", "active": false, "source": "", "reset": reset}
	if s.run != nil {
		m["active"] = s.run.active
		m["source"] = s.run.source
	}
	return record{data: encode(m)}
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

// Close stops serving and cancels any run still going.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	state := s.run
	s.mu.Unlock()

	if state != nil {
		state.cancel()
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
