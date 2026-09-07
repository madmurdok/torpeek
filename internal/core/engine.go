package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/madmurdok/torpeek/internal/bridge"
	"github.com/madmurdok/torpeek/internal/cache"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/manifest"
	"github.com/madmurdok/torpeek/internal/output"
	"github.com/madmurdok/torpeek/internal/probe"
	"github.com/madmurdok/torpeek/internal/sheet"
	"github.com/madmurdok/torpeek/internal/swarm"
	"github.com/madmurdok/torpeek/internal/version"
)

// Config is everything one run needs.
type Config struct {
	// Source is a magnet URI or a path to a .torrent file.
	Source string
	// OutputRoot is where results are written.
	OutputRoot string

	// CacheCeiling is the size ceiling on the whole tree under OutputRoot
	// (REQUIREMENTS.md 2.9). Zero - the default, never set by a flag - means
	// no ceiling: cache.Evict is not even called, and the tree grows without
	// limit until a person clears it themselves. A positive value is bytes,
	// checked once per run (see saveRunRecord's caller in run()), since the
	// tree only ever grows from a run finishing.
	CacheCeiling int64

	Plan    frames.Plan
	Profile swarm.Profile
	Budget  Budget

	// Roof is the ceiling over the whole CLIENT this run's torrent comes out
	// of - Torrents below, or the pool this run builds for itself - rather
	// than over this run. Zero MaxBytes, the default, means unlimited; see
	// Roof and DefaultRoof.
	//
	// A value rather than a pointer, unlike Torrents, and the difference is
	// worth reading. Torrents has to be shared because it holds state; a roof
	// holds none - its figure lives on the client's own counter, which every
	// run over that client reads - so copies of this number all enforce the
	// same ceiling against the same total. What that does NOT excuse is
	// setting it per run: it describes the client, so the one place that
	// builds the pool is the one place that should set it (cli/web.go), and
	// two runs given different numbers would be two opinions about one
	// client, of which the larger silently wins.
	Roof Roof

	// Sequential opts into degrading to sequential reading from the start
	// when a container states no duration to plan capture points across
	// (REQUIREMENTS.md 2.7). Off by default: a container with no index gets
	// a typed error naming why instead, since spending traffic on a guess is
	// a decision the caller makes, never one taken silently.
	Sequential bool

	// Parallelism is how many video files are worked on at once. On a shared
	// seedbox this belongs at 1-2 rather than the desktop default (section 4.1).
	Parallelism int

	// Format is the image encoding for frames.
	Format frames.Format

	// Files narrows the run to some of the torrent's video files, by torrent
	// index or by path pattern (see swarm.Select). Empty means all of them.
	Files []string

	Swarm  swarm.Config
	Bridge bridge.Config

	// Torrents is where this run gets its torrent from: a pool that outlives
	// it, holding one long-lived client for every public torrent.
	//
	// A run no longer configures a client - it attaches to a torrent and
	// detaches from it, and the pool decides whether that torrent shares the
	// public client or needs one of its own (swarm.Pool). Swarm above is
	// still what a pool is built FROM: the data directory, the DHT and upload
	// switches, the port set, the known peers.
	//
	// Nil means this run owns the whole arrangement: it builds a pool over
	// Swarm for itself and closes it when the run ends, which is exactly what
	// a one-shot CLI run wants and exactly what every run did before pools
	// existed. A process that stays up - the web server - builds one pool and
	// puts it here, so the client is the server's to close and never a run's.
	// A pointer is what survives cfg being copied per run, the same way
	// Swarm.Ports is.
	Torrents *swarm.Pool
}

// DefaultConfig fills in the defaults from REQUIREMENTS.md section 7.
func DefaultConfig(source, outputRoot, dataDir string) Config {
	return Config{
		Source:      source,
		OutputRoot:  outputRoot,
		Plan:        frames.DefaultPlan(),
		Profile:     swarm.MinTime,
		Budget:      DefaultBudget(1),
		Roof:        DefaultRoof(),
		Parallelism: 4,
		Format:      frames.JPEG,
		Swarm:       swarm.DefaultConfig(dataDir),
		Bridge:      bridge.DefaultConfig(),
	}
}

// poolFor settles where this run's torrent comes from, and reports whether
// the run has to close what it was given.
//
// The nil case builds a pool for this run alone rather than falling back to
// some other way of opening a torrent, so there is exactly one path through
// swarm however a run was configured. It costs nothing: a pool starts no
// client until a torrent is attached, and closing one that never started is
// a no-op.
func poolFor(cfg Config) (pool *swarm.Pool, owned bool) {
	if cfg.Torrents != nil {
		return cfg.Torrents, false
	}
	return swarm.NewPool(cfg.Swarm), true
}

// DefaultParallelism is how many files a desktop run works on at once.
const DefaultParallelism = 4

// Engine runs one job and reports it as events. It prints nothing.
type Engine struct {
	tools ffmpeg.Tools

	// liveMu guards live below - runs share one Engine (TOR-128 put more
	// than one Engine.Run in flight over a shared swarm.Pool) and finish on
	// their own goroutines, so marking and reading this map races without
	// it.
	liveMu sync.Mutex
	// live is every RunDir a run on this Engine is currently writing to,
	// refcounted rather than a plain set: two runs can legitimately resolve
	// to the same RunDir (same infohash and params - see ParamsKey) and
	// overlap, and unmarking must not clear a directory the other one is
	// still writing. cache.Evict reads a snapshot of this to know which
	// directories no run's own CreatedAt-based ordering can be trusted to
	// protect (see its own doc for why Aged alone stopped being enough).
	live map[string]int
}

// NewEngine returns an engine using the given external tools.
func NewEngine(tools ffmpeg.Tools) *Engine {
	return &Engine{tools: tools, live: make(map[string]int)}
}

// markLive records dir as a run's own directory for as long as that run is
// still writing to it, and unmarkLive is called once - always paired, always
// deferred - when that run is done with it.
func (e *Engine) markLive(dir string) {
	e.liveMu.Lock()
	e.live[dir]++
	e.liveMu.Unlock()
}

func (e *Engine) unmarkLive(dir string) {
	e.liveMu.Lock()
	if e.live[dir] <= 1 {
		delete(e.live, dir)
	} else {
		e.live[dir]--
	}
	e.liveMu.Unlock()
}

// liveDirs snapshots every directory currently marked live, for a run's own
// cache.Evict call to exclude - not just its own directory, since any other
// run on this Engine may be mid-write (or mid-resume - see cache.Evict) at
// the same moment.
func (e *Engine) liveDirs() []string {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	dirs := make([]string, 0, len(e.live))
	for dir := range e.live {
		dirs = append(dirs, dir)
	}
	return dirs
}

// Run starts a job and returns the events it produces. The channel closes when
// the run is over; the last event is always Done or Failed.
//
// Cancelling ctx stops the run and keeps whatever frames already reached disk,
// which is what makes cancel-and-resume possible.
func (e *Engine) Run(ctx context.Context, cfg Config) (<-chan Event, error) {
	if cfg.Parallelism < 1 {
		cfg.Parallelism = DefaultParallelism
	}
	if cfg.Format == "" {
		cfg.Format = frames.JPEG
	}
	if err := cfg.Plan.Validate(); err != nil {
		return nil, Fail(CodeInternal, err)
	}

	src, err := swarm.ParseSource(cfg.Source)
	if err != nil {
		return nil, Fail(CodeInternal, err)
	}

	bus := NewBus(DefaultBuffer)
	events, _ := bus.Subscribe()

	go func() {
		defer bus.Close()
		e.run(ctx, cfg, src, bus)
	}()

	return events, nil
}

func (e *Engine) run(ctx context.Context, cfg Config, src swarm.Source, bus *Bus) {
	started := time.Now()

	// Before anything else, and before any session exists: an identical rerun
	// is required to make no network request at all, and the only way to be
	// certain of that is not to connect.
	if e.serveFromCache(cfg, src, bus, started) {
		return
	}

	pool, ownPool := poolFor(cfg)
	if ownPool {
		// Registered before the attachment's own defer, so it runs after it:
		// the torrent is detached first, and only then is the client this run
		// brought into being for itself taken down. When the pool came from
		// the caller there is nothing here to close - a run must never take
		// the pool down, which is the whole point of Config.Torrents.
		defer pool.Close()
	}

	// Before the attach, not after: a full roof means this client has already
	// received everything it was allowed to, and going online to find that
	// out would spend more of it. A refusal rather than a Done, for the same
	// reason ErrTorrentBusy is one - the run never happened, and there are no
	// frames to keep. A run that is stopped BY the roof after it has begun is
	// the other case, and ends on Done{Reason: StopRoof} below.
	//
	// And after serveFromCache above, deliberately: a run served from disk
	// goes nowhere and receives nothing, so a full roof is no reason to
	// refuse it. The roof bounds traffic, not work.
	if cfg.Roof.Reached(pool.Downloaded()) {
		err := fmt.Errorf("the client-wide traffic roof of %d bytes is used up; "+
			"raise -max-client-bytes or restart", cfg.Roof.MaxBytes)
		bus.Publish(Failed{File: -1, Code: CodeTrafficRoof, Err: err})
		return
	}

	attachment, err := pool.Attach(ctx, src, cfg.Swarm.Peers...)
	if err != nil {
		bus.Publish(Failed{File: -1, Code: CodeOf(err), Err: err})
		return
	}
	torrent := attachment.Torrent()

	// Every exit from here down goes through this defer - completed,
	// budget-stopped, cancelled or failed alike - and lets this run's torrent
	// go every time, discarding its pieces with it. Detaching is not closing:
	// the shared public client carries on holding whatever else is attached
	// to it, and a run that fails takes nothing down with it.
	//
	// A run that was cut short still gets to keep what matters: resume
	// (serveFromCache and reusableFrames, both in this package) reads only
	// the output directory's manifests and frames, never the swarm's piece
	// cache, so a stopped run loses nothing a later run could have reused by
	// leaving pieces in place. This is what makes a long-lived web session,
	// not just a one-shot CLI run, actually drop pieces after every torrent
	// instead of piling them up for however long the process stays up.
	defer attachment.Detach()

	videos := torrent.Videos()

	// Resolved before the event so it can say what is being worked on, but
	// reported after it either way: a caller whose selection matched nothing
	// still wants to see what the torrent held.
	selected, selErr := swarm.Select(videos, cfg.Files)

	bus.Publish(MetadataReady{
		Name:     torrent.Name(),
		InfoHash: torrent.InfoHash(),
		Private:  torrent.Private(),
		Videos:   videos,
		Selected: indicesOf(selected),
		BlindDHT: attachment.WentOnlineBlind(),
	})

	if len(videos) == 0 {
		err := fmt.Errorf("torrent %q holds no video files", torrent.Name())
		bus.Publish(Failed{File: -1, Code: CodeNoVideo, Err: err})
		return
	}
	if selErr != nil {
		bus.Publish(Failed{File: -1, Code: CodeOf(selErr), Err: selErr})
		return
	}

	// The budget covers the whole run, so every file shares one tracker.
	//
	// budgetFor reads len(selected) right here, before the budget's clock
	// starts (tracker.Context below). Whoever adds another way to narrow a
	// run - the web UI (TOR-66/67/68) included - has to apply that
	// selection earlier, into cfg.Files before swarm.Select runs above;
	// narrowing the file list after this point would leave the budget sized
	// for files no longer being fetched.
	//
	// The pool is handed in as the roof's meter, not the torrent: the roof is
	// the client's ceiling and is read off the client's own counter, which
	// keeps the traffic of torrents this pool has already let go
	// (swarm.Pool.Downloaded). Summing what the live runs report would forget
	// exactly the arrivals section 2.6 insists on counting.
	tracker := NewBudgetTracker(budgetFor(cfg, len(selected)), torrent, cfg.Roof, pool)

	runCtx, cancel := tracker.Context(ctx)
	defer cancel()

	writer, err := output.NewWriter(output.Layout{
		Root:     cfg.OutputRoot,
		InfoHash: torrent.InfoHash(),
		Params:   ParamsKey(cfg),
	})
	if err != nil {
		bus.Publish(Failed{File: -1, Code: CodeStorage, Err: err})
		return
	}

	// Marked live for the rest of this function, resume included: a resumed
	// run reuses a directory its own earlier, incomplete attempt already put
	// a run.json in (Aged is already true, on an old CreatedAt), so without
	// this a concurrent run finishing elsewhere could pick it as the
	// oldest-first eviction candidate it looks like. See cache.Evict's doc.
	e.markLive(writer.Layout().RunDir())
	defer e.unmarkLive(writer.Layout().RunDir())

	// The .torrent is written before a single frame is fetched, not at the end
	// beside the run record. It can only be reconstructed while this session
	// is open - the info dictionary is what the swarm just handed over - and a
	// run that is cancelled or stopped at its budget after two frames should
	// still leave behind the one artefact that lets someone hand the torrent
	// to a real client (TOR-73).
	//
	// A failure here is reported the way saveRunRecord's is, and for the same
	// reason: it is a storage problem worth a person seeing, but it is not a
	// reason to abandon frames that are otherwise fetchable, so the run
	// carries on and simply has no .torrent to announce.
	var warnings []string
	torrentPath, err := saveTorrentFile(writer, torrent)
	if err != nil {
		// Not a Failed: this run's frames are unaffected, and a run-scoped
		// Failed is how an unopenable source is reported, so borrowing it
		// here made a healthy run read as a broken one. It travels on Done
		// instead, which is where the path itself travels (TOR-79).
		warnings = append(warnings, err.Error())
	}

	srv, err := bridge.Start(cfg.Bridge)
	if err != nil {
		bus.Publish(Failed{File: -1, Code: CodeInternal, Err: err})
		return
	}
	defer srv.Close()

	prober := probe.New(e.tools)
	extractor := frames.NewExtractor(e.tools)
	extractor.Format = cfg.Format

	var (
		mu      sync.Mutex
		made    int
		done    int
		stopped StopReason = StopCompleted
		// finished are the files whose frame set came out whole, which is what
		// a later run may be served from disk.
		finished []int
	)

	sem := make(chan struct{}, cfg.Parallelism)
	var wg sync.WaitGroup

	// One tracker for the whole run, shared by every file's goroutine below
	// - see runSpeed's own doc for why that has to be one instance rather
	// than one per file.
	speed := &runSpeed{}

	for _, video := range selected {
		if reason, halt := haltReason(runCtx, tracker); halt {
			mu.Lock()
			stopped = reason
			mu.Unlock()
			break
		}

		wg.Add(1)
		go func(file swarm.FileInfo) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			produced, complete, err := e.processFile(runCtx, cfg, fileDeps{
				torrent:   torrent,
				bridge:    srv,
				prober:    prober,
				extractor: extractor,
				writer:    writer,
				tracker:   tracker,
				speed:     speed,
				bus:       bus,
			}, file)

			mu.Lock()
			defer mu.Unlock()
			made += produced
			if err == nil {
				done++
				if complete {
					finished = append(finished, file.Index)
				}
				return
			}
			if reason, halt := haltReason(runCtx, tracker); halt {
				stopped = reason
				return
			}
			bus.Publish(Failed{File: file.Index, Code: CodeOf(err), Err: err})
		}(video)
	}

	wg.Wait()

	// Ask once more after the workers have stopped. A file that broke out of
	// its capture loop because the budget ran out or the run was cancelled
	// returns no error - it did what it could - so without this the run would
	// report itself completed while having skipped most of its work.
	if reason, halt := haltReason(runCtx, tracker); halt {
		mu.Lock()
		stopped = reason
		mu.Unlock()
	}

	// Written even for a stopped run: what it did finish is still worth
	// serving from disk next time, and a partial record is what resume will
	// read to know where to pick up.
	if err := saveRunRecord(cfg, writer.Layout(), torrent, videos, selected, finished,
		recordedSource(cfg.Source, src, torrentPath, torrent.Magnet())); err != nil {
		bus.Publish(Failed{File: -1, Code: CodeStorage, Err: err})
	} else if cfg.CacheCeiling > 0 {
		// Only once this run's own record is safely written, and only when a
		// ceiling was actually asked for (REQUIREMENTS.md 2.9's default is no
		// eviction at all - skipping the call entirely also skips the scan
		// cost of Evict finding that out for itself on every run).
		//
		// e.liveDirs() names every directory any run on this Engine is
		// currently writing - this run's own included, since unmarkLive is
		// still deferred - not just this one: with more than one run able to
		// be live at once (TOR-128's pool), each finishing run's own Evict
		// call has to protect every sibling still going, not only itself. A
		// failure here must not cost a person the frames this run just
		// finished producing - so, like a .torrent that could not be written
		// (TOR-79), it becomes a warning on Done rather than a run-scoped
		// Failed.
		if _, err := cache.Evict(cfg.OutputRoot, cfg.CacheCeiling, e.liveDirs()); err != nil {
			warnings = append(warnings, fmt.Sprintf("cache eviction: %v", err))
		}
	}

	spent, _ := tracker.Spent()
	// Read off the torrent rather than through the budget tracker: the budget
	// is enforced on what arrived (REQUIREMENTS.md 2.6) and has no business
	// knowing about claims, while the torrent is the thing that placed them.
	claimedPieces, claimedByte := torrent.Claimed()
	bus.Publish(Done{
		Reason:         stopped,
		Files:          done,
		Frames:         made,
		DownloadedByte: spent,
		ClaimedByte:    claimedByte,
		ClaimedPieces:  claimedPieces,
		ClaimedRanges:  torrent.ClaimedRanges(),
		Elapsed:        time.Since(started),
		TorrentPath:    torrentPath,
		Warnings:       warnings,
	})
}

// stallSince reports the bridge read that ran out of time since the count
// taken before an operation, or nil if none did.
//
// It is the answer to a question the prober cannot answer for itself. The
// timeout belongs to the bridge's own per-request context, not to the run
// context ffprobe was launched under, and ffprobe is a subprocess besides: it
// sees a truncated body, decides the file holds no streams or no keyframe with
// a position, and exits successfully. Every context the Go side is holding at
// that moment is still alive, so there is nothing there to check - the only
// layer that knows the read never finished is the one that gave up on it.
func stallSince(b *bridge.Bridge, url string, before int) error {
	n, last := b.Stalls(url)
	if n <= before || last == nil {
		return nil
	}
	return last
}

// runSpeed is one run's download and upload rate tracker, shared across
// every file's goroutine. Progress heartbeats fire from more than one file
// at once (cfg.Parallelism), but they read off the SAME cumulative
// counters - the budget tracker's Spent and torrent.Uploaded are both
// run-wide, not per-file (NewBudgetTracker's own doc: "the budget covers
// the whole run, so every file shares one tracker") - so the previous
// reading a rate is a delta against has to be one shared point too, not one
// per goroutine, and reading or advancing it from two files at once needs a
// lock rather than each guessing at the other's last sample.
//
// The arithmetic itself is rateSample (events.go); this only adds the
// concurrency this run's parallel files need around it.
type runSpeed struct {
	mu   sync.Mutex
	down rateSample
	up   rateSample
}

// sample folds in this heartbeat's cumulative download and upload bytes,
// both read against the one shared elapsed reading, and returns the two
// rates - see rateSample.next and Progress.DownloadRate for what nil means.
func (s *runSpeed) sample(downloadedByte, uploadedByte int64, elapsed time.Duration) (downloadRate, uploadRate *float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.down.next(downloadedByte, elapsed), s.up.next(uploadedByte, elapsed)
}

// fileStallClock pairs a stallClock (events.go) with the mutex its two
// callers both need: this file's own capture loop, which has definitive
// evidence every time a point finishes, and this file's own heartbeat
// ticker (startFileHeartbeat), which has none while a single point is still
// in flight and only peeks the clock meanwhile. One instance per FILE, not
// per run - Progress.Stall's own doc explains why a shared, run-wide clock
// would be wrong here even though Peers/Seeds/DownloadRate are run-wide:
// two files worked on in parallel (cfg.Parallelism) can be stalled for two
// different reasons at once, and a single clock would report whichever was
// observed most recently as if it explained both.
type fileStallClock struct {
	mu    sync.Mutex
	clock stallClock
}

func (f *fileStallClock) observe(now time.Time, code ErrorCode) *Stall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock.observe(now, code)
}

func (f *fileStallClock) peek(now time.Time) *Stall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock.peek(now)
}

// stallHeartbeatInterval is how often startFileHeartbeat reports peers,
// seeds, the swarm's own availability reading and the current stall verdict
// while a file is being worked on - independent of whether any capture
// point has succeeded, failed, or even finished being attempted yet.
//
// Five seconds, chosen against the thing it has to beat: bridge.RequestTimeout
// defaults to sixty seconds, and BOTH probe.Inspect (before FileStarted even
// fires) and a single capture point's KeyframeAt/Frame read can block for the
// whole of it without producing any other event - a torrent nobody seeds
// fails every such read that way, so without this heartbeat a row goes
// silent for up to a minute at a time, repeatedly, which is this ticket's own
// complaint restated. Five seconds is frequent enough that "no peers" or "no
// seeds" is recognisable well inside one metadata timeout (sixty seconds,
// the OLD worst case this replaces) and infrequent enough not to flood the
// bus with a heartbeat nobody asked to see that often.
const stallHeartbeatInterval = 5 * time.Second

// startFileHeartbeat runs a ticker for as long as done is open, publishing a
// Progress reading of this file's current peers/seeds/availability/rates and
// stall verdict on every tick. FramesDone and FramesTotal are left at zero,
// which a client must read as "this heartbeat has nothing to say about the
// capture plan" rather than as a real 0-of-0 (frames.Plan.Validate rejects
// an empty plan, so a real per-point heartbeat never reports that) - see
// wire.go's progress rendering and app.js's own handling of frames_total.
//
// Reads only local, already-computed state (torrent.Peers/Availability,
// the shared rate tracker, the budget tracker's own counters) - never a new
// network request - so it costs nothing to run alongside a slow read rather
// than instead of one, which is the whole point: it keeps reporting while
// something else is still blocked.
func (e *Engine) startFileHeartbeat(deps fileDeps, file int, stall *fileStallClock, done <-chan struct{}) {
	ticker := time.NewTicker(stallHeartbeatInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				connected, seeds := deps.torrent.Peers()
				swarm := newSwarmAvailability(deps.torrent.Availability())
				spent, elapsed := deps.tracker.Spent()
				uploaded := deps.torrent.Uploaded()
				downloadRate, uploadRate := deps.speed.sample(spent, uploaded, elapsed)

				var reading *Stall
				if code := classifyLiveStall(connected, swarm); code != "" {
					reading = stall.observe(now, code)
				} else {
					reading = stall.peek(now)
				}

				deps.bus.Publish(Progress{
					File: file, DownloadedByte: spent, UploadedByte: uploaded,
					Elapsed: elapsed, Peers: connected, Seeds: seeds,
					DownloadRate: downloadRate, UploadRate: uploadRate,
					Swarm: swarm, Stall: reading,
				})
			}
		}
	}()
}

// fileDeps groups what processing one file needs, so the signature does not
// grow a parameter per collaborator.
type fileDeps struct {
	torrent   *swarm.Torrent
	bridge    *bridge.Bridge
	prober    *probe.Prober
	extractor *frames.Extractor
	writer    *output.Writer
	tracker   *BudgetTracker
	// speed is this run's shared rate tracker (see runSpeed's own doc for
	// why one instance, not one per file).
	speed *runSpeed
	bus   *Bus

	// toneMap is the colour conversion decided for this one file, once its
	// stream has been inspected. It lives here rather than on the extractor
	// because two callers need it: the extractor, which applies it, and the
	// manifest, which reports it.
	toneMap frames.ToneMap
}

// ToneMapOf decides what to do about a video stream's colour. The one place
// that translates ffprobe's vocabulary into the extractor's, so the CLI's
// summary line and the manifest cannot disagree with the frames on what was
// done to them.
func ToneMapOf(v probe.VideoStream) frames.ToneMap {
	return frames.ToneMapFor(frames.ColorTags{
		Transfer:           v.ColorTransfer,
		Primaries:          v.ColorPrimaries,
		Matrix:             v.ColorSpace,
		Range:              v.ColorRange,
		DolbyVisionProfile: v.DolbyVisionProfile,
	})
}

func (e *Engine) processFile(ctx context.Context, cfg Config, deps fileDeps, file swarm.FileInfo) (produced int, complete bool, err error) {
	url, withdraw, err := deps.bridge.Publish(bridge.FromTorrent(deps.torrent, cfg.Profile), file.Index)
	if err != nil {
		return 0, false, err
	}
	defer withdraw()

	// TOR-141: a heartbeat for the whole of this file's own processing, not
	// gated on anything below succeeding - see startFileHeartbeat's own doc
	// for why that has to run concurrently with, rather than only between,
	// the blocking calls beneath it (Inspect included: a torrent nobody
	// seeds can stall THAT before FileStarted has even fired). Stopped by
	// closing done, deferred immediately so it cannot outlive this file's
	// own torrent/bridge, which stay open only until this function returns.
	//
	// Named fileStall rather than stall: this function already shadows that
	// name locally, below, for stallSince's own per-attempt bridge reading -
	// a different, narrower question (did THIS ONE read time out) than the
	// clock's own (how long has the SAME cause been true).
	fileStall := &fileStallClock{}
	heartbeatDone := make(chan struct{})
	e.startFileHeartbeat(deps, file.Index, fileStall, heartbeatDone)
	defer close(heartbeatDone)

	stalls, _ := deps.bridge.Stalls(url)
	info, err := deps.prober.Inspect(ctx, url)
	var points []time.Duration
	sequential := false
	if err != nil {
		// Asked before anything is read into the container's character: a
		// read that never finished makes ffprobe describe a file it only
		// partly saw, and every verdict below would be about that.
		if stall := stallSince(deps.bridge, url, stalls); stall != nil {
			return 0, false, fmt.Errorf("inspect: %w; ffprobe then reported: %v", stall, err)
		}

		// A container with no duration is the one case the sequential
		// fallback exists for (REQUIREMENTS.md 2.7); anything else - no
		// video stream at all, or the file not opening as media - stays a
		// hard failure regardless of the flag, since there is no frame to
		// read sequentially either way.
		var niErr *probe.NoIndexError
		if !cfg.Sequential || !errors.As(err, &niErr) || niErr.Reason != probe.ReasonNoDuration {
			return 0, false, err
		}

		keyframes, seqErr := deps.prober.SequentialKeyframes(ctx, url, cfg.Plan.Count)
		if seqErr != nil {
			// The fallback found nothing to work with either; the original
			// error is still the honest one to report.
			return 0, false, err
		}

		points = make([]time.Duration, len(keyframes))
		for i, kf := range keyframes {
			points[i] = kf.PTS
		}
		// Duration becomes the last point actually found, so everything
		// downstream - shift candidates, the manifest's file duration, the
		// availability ratio - sees a file that ends where the sequential
		// read stopped rather than a nonexistent length.
		info.Duration = points[len(points)-1]
		sequential = true
	} else {
		points, err = cfg.Plan.Points(info.Duration)
		if err != nil {
			return 0, false, err
		}
	}

	// Decided once per file, from the stream ffprobe just described, and held
	// on this call's own copy of deps: one extractor serves every file of a
	// run in parallel, so the conversion travels with the file rather than
	// being set on the shared one (TOR-108).
	deps.toneMap = ToneMapOf(info.Video)
	deps.extractor = deps.extractor.WithToneMap(deps.toneMap)

	deps.bus.Publish(FileStarted{
		File:  file.Index,
		Path:  file.Path,
		Media: info,
		Plan:  points,
	})

	skipped := 0
	tolerance := seekTolerance(points)
	records := make([]manifest.Frame, 0, len(points))

	// What an earlier run already produced for this file. A stopped run keeps
	// its frames, so finishing the job means taking the points it never got
	// to - not paying for the ones it did.
	prior, _ := cache.LoadManifest(deps.writer.Layout().FileDir(file.Index, file.Path))
	done := reusableFrames(prior, points)

	for i, at := range points {
		if earlier, ok := done[i]; ok {
			produced++
			records = append(records, earlier)

			actual := time.Duration(0)
			if earlier.ActualMS != nil {
				actual = time.Duration(*earlier.ActualMS) * time.Millisecond
			}
			deps.bus.Publish(FrameReady{
				File:      file.Index,
				Index:     i,
				Requested: at,
				Actual:    actual,
				Shift:     ShiftReason(earlier.Shift),
				Path:      earlier.Path,
				Width:     earlier.Width,
				Height:    earlier.Height,
			})
			continue
		}

		if _, halt := haltReason(ctx, deps.tracker); halt {
			// Stopping between capture points, never mid-write: the frame in
			// flight is finished and kept.
			break
		}
		if warning := deps.tracker.Warning(); warning != nil {
			deps.bus.Publish(*warning)
		}

		stalls, _ := deps.bridge.Stalls(url)
		shot, err := e.captureOne(ctx, cfg, deps, url, file, info.Duration, at, tolerance/4)
		if err != nil {
			// Same reasoning as at the inspect above, and this is where it was
			// actually caught: a keyframe query whose read timed out comes
			// back as "no keyframe with a byte position", which is a statement
			// about the container that nothing here is entitled to make.
			if stall := stallSince(deps.bridge, url, stalls); stall != nil {
				err = fmt.Errorf("capture point at %s: %w; ffprobe then reported: %v",
					at.Round(time.Second), stall, err)
			}
			skipped++
			pointCode := CodeOf(err)
			records = append(records, manifest.Frame{
				Index:       i,
				RequestedMS: at.Milliseconds(),
				Shift:       manifest.ShiftFailed,
				Error:       string(pointCode),
			})
			deps.bus.Publish(FrameSkipped{
				File:      file.Index,
				Index:     i,
				Requested: at,
				Code:      pointCode,
				Reason:    err.Error(),
			})
			// TOR-141: this point's own failure, reclassified against whether
			// ANY peer is connected (classifyPointFailure's own doc) and
			// folded into this file's stall clock - the row's answer to
			// "why, and for how long" for exactly the two causes a failed
			// point can mean (CodeUnavailable/CodeReadStalled, or CodeNoPeers
			// when neither presumption those two make actually holds).
			// FrameSkipped.Code above is untouched: TOR-45's own acceptance
			// (stall_test.go) is about that field staying CodeReadStalled,
			// and this is a second, additive reading alongside it, not a
			// replacement.
			now := time.Now()
			connected, seeds := deps.torrent.Peers()
			swarmNow := newSwarmAvailability(deps.torrent.Availability())
			spentNow, elapsedNow := deps.tracker.Spent()
			uploadedNow := deps.torrent.Uploaded()
			downloadRate, uploadRate := deps.speed.sample(spentNow, uploadedNow, elapsedNow)
			deps.bus.Publish(Progress{
				File: file.Index, FramesDone: produced, FramesTotal: len(points),
				DownloadedByte: spentNow, UploadedByte: uploadedNow, Elapsed: elapsedNow,
				Peers: connected, Seeds: seeds,
				DownloadRate: downloadRate, UploadRate: uploadRate, Swarm: swarmNow,
				Stall: fileStall.observe(now, classifyPointFailure(pointCode, connected)),
			})
			continue
		}

		// A keyframe legitimately sits before its capture point - the decoder
		// starts there and runs forward. One sitting closer to a neighbouring
		// point than to its own is a different thing: the seek did not land,
		// and the frame answers a question nobody asked. Writing it would put
		// a plausible-looking wrong answer on disk and report it as a success,
		// which is exactly how a run once wrote the same opening frame twenty
		// times over.
		// Compared against what was actually asked for, not the original
		// point: a deliberate shift is not a failed seek, and confusing the
		// two would throw away the frame the shift went to find.
		if !landedNear(shot.asked, shot.actual, tolerance) {
			skipped++
			records = append(records, manifest.Frame{
				Index:       i,
				RequestedMS: at.Milliseconds(),
				Shift:       manifest.ShiftFailed,
				Error:       string(CodeSeekFailed),
			})
			deps.bus.Publish(FrameSkipped{
				File:      file.Index,
				Index:     i,
				Requested: at,
				Code:      CodeSeekFailed,
				Reason: fmt.Sprintf("decoded at %s, %s away from the requested %s",
					shot.actual.Round(time.Second), (shot.asked - shot.actual).Abs().Round(time.Second),
					shot.asked.Round(time.Second)),
			})
			// TOR-141: a seek landing wide of its mark still means a read
			// went through and a container got decoded - the swarm is
			// plainly not the problem here, so this clears the stall clock
			// (classifyPointFailure's own "every other outcome" rule)
			// rather than leaving a stale cause reading behind it. The next
			// heartbeat (startFileHeartbeat, at most stallHeartbeatInterval
			// away) carries the cleared reading to the row.
			fileStall.observe(time.Now(), "")
			continue
		}

		// Blank rejection happens here, after the seek is confirmed real and
		// before anything reaches disk - the only point where a wasted decode
		// is free to retry. It is judged against the picture, not the swarm,
		// so it runs after decoding rather than alongside the availability
		// shift above: that one moves before any traffic is spent, this one
		// can only be judged once a frame exists to look at.
		shot = e.rejectBlank(ctx, deps, url, tolerance/4, shot)

		path, err := deps.writer.WriteFrame(file.Index, file.Path, i, shot.frame.Data, extensionFor(cfg.Format))
		if err != nil {
			return produced, false, Fail(CodeStorage, err)
		}
		produced++

		actual := shot.actual.Milliseconds()
		records = append(records, manifest.Frame{
			Index:       i,
			RequestedMS: at.Milliseconds(),
			ActualMS:    &actual,
			Path:        path,
			// The two vocabularies are deliberately the same strings, so a
			// marker never changes meaning on its way to disk.
			Shift:  manifest.Shift(shot.shift),
			Width:  shot.frame.Width,
			Height: shot.frame.Height,
		})

		// Actual almost never equals Requested: decoding starts at the
		// keyframe before the wanted moment, which is ordinary behaviour and
		// not a shift. Shift says only whether the capture point itself was
		// moved, which is a decision, not a side effect of how keyframes fall.
		deps.bus.Publish(FrameReady{
			File:      file.Index,
			Index:     i,
			Requested: at,
			Actual:    shot.actual,
			Shift:     shot.shift,
			Path:      path,
			Width:     shot.frame.Width,
			Height:    shot.frame.Height,
		})

		spent, elapsed := deps.tracker.Spent()
		connected, seeds := deps.torrent.Peers()
		uploaded := deps.torrent.Uploaded()
		// Both rates are taken against this one shared elapsed reading, so a
		// download stall and an upload burst arriving on the very same
		// heartbeat are each divided by the same real interval rather than
		// two different notions of "since last time" (runSpeed, rateSample).
		downloadRate, uploadRate := deps.speed.sample(spent, uploaded, elapsed)
		deps.bus.Publish(Progress{
			File:           file.Index,
			FramesDone:     produced,
			FramesTotal:    len(points),
			DownloadedByte: spent,
			UploadedByte:   uploaded,
			Elapsed:        elapsed,
			Peers:          connected,
			Seeds:          seeds,
			DownloadRate:   downloadRate,
			UploadRate:     uploadRate,
			// What the SWARM holds, read fresh on every heartbeat, and nil
			// when the client has not learned it yet - which is a real state
			// and not zero copies (newSwarmAvailability, Progress.Swarm).
			Swarm: newSwarmAvailability(deps.torrent.Availability()),
			// TOR-141: a frame just landed, which is the clearest possible
			// evidence this file is NOT a case of nothing happening - so this
			// clears the stall clock (classifyPointFailure's own "" rule)
			// rather than leaving whatever an earlier failed point set.
			Stall: fileStall.observe(time.Now(), ""),
		})
	}

	manifestPath, err := e.writeManifest(deps, file, info, records, sequential)
	if err != nil {
		return produced, false, Fail(CodeStorage, err)
	}

	// The sheet is assembled last, from whatever frames landed on disk during
	// the loop above - never accumulated as they arrived - so a run stopped
	// partway still gets a sheet from what it actually has (section 2.11).
	sheetPath, err := e.writeSheet(deps, file, info, points, records)
	if err != nil {
		return produced, false, Fail(CodeStorage, err)
	}

	deps.bus.Publish(FileDone{
		File:         file.Index,
		Path:         file.Path,
		Frames:       produced,
		Skipped:      skipped,
		ManifestPath: manifestPath,
		SheetPath:    sheetPath,
	})

	// Complete means every point planned for this file produced a frame. A
	// file with a gap is not a cacheable result: a later run should try the
	// missing points again rather than be served the gap as an answer.
	return produced, produced == len(points), nil
}

// saveRunRecord writes what this run covered, so a later one can tell a whole
// result from a partial one without opening a session to find out - and, with
// Source and Plan, so it can be named and repeated without a session open to
// ask what it was.
//
// What it records as the source is recordedSource's decision, not cfg.Source
// outright - see there for why a .torrent run names a magnet reconstructed
// from its infohash rather than any path.
//
// Selected and Complete are merged with whatever record already sits in
// layout.RunDir(), never replaced outright. ParamsKey deliberately excludes
// the file selection (see its own doc comment), so a run over three files and
// an earlier run over six share the very same directory. Rebuilding either
// list from only this run's own files would make the narrower run erase the
// wider one's results - the six files' frames would stay on disk while the
// record forgot them, and a later request for one would needlessly go back
// to the swarm. Merging means every file any run here has ever asked for, or
// completed, stays recorded regardless of what any other run touched. A
// missing or unreadable prior record (LoadRun reports ok=false) merges as
// empty, so the first run into a directory behaves exactly as before.
func saveRunRecord(cfg Config, layout output.Layout, torrent *swarm.Torrent, videos []swarm.FileInfo,
	selected []swarm.FileInfo, finished []int, source string) error {

	prior, _ := cache.LoadRun(layout.RunDir())

	record := cache.Run{
		Version:   cache.Version,
		Tool:      version.Version,
		CreatedAt: time.Now().UTC(),
		Source:    source,
		InfoHash:  torrent.InfoHash(),
		Name:      torrent.Name(),
		Private:   torrent.Private(),
		Plan: cache.Plan{
			Count:      cfg.Plan.Count,
			Start:      cfg.Plan.Start,
			End:        cfg.Plan.End,
			Profile:    cfg.Profile.Name,
			Format:     string(cfg.Format),
			Sequential: cfg.Sequential,
		},
		Videos:   make([]cache.File, 0, len(videos)),
		Selected: mergeIndices(prior.Selected, indicesOf(selected)),
		Complete: mergeIndices(prior.Complete, finished),
		// Not merged with prior, unlike the two above: see the field's own
		// doc. This is one run's traversal, and a union across reruns would
		// describe a spread no single run achieved.
		Claimed: claimedPairs(torrent.ClaimedRanges()),
	}
	for _, v := range videos {
		record.Videos = append(record.Videos, cache.File{
			Index: v.Index, Path: v.Path, Bytes: v.Length, Offset: v.Offset,
		})
	}
	return cache.SaveRun(layout.RunDir(), record)
}

// claimedPairs flattens swarm's named ranges into the record's pair array.
// Two shapes for one thing, on purpose: the named fields are what code reads,
// the pairs are what a few hundred bytes of JSON can afford (cache.Run.Claimed
// says why). Nil in stays nil out, so a run that claimed nothing writes no
// field at all rather than an empty array.
func claimedPairs(ranges []swarm.PieceRange) [][2]int {
	if len(ranges) == 0 {
		return nil
	}
	out := make([][2]int, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, [2]int{r.Begin, r.End})
	}
	return out
}

// saveTorrentFile puts the torrent this run is working on into the run
// directory, as a .torrent a client can load.
//
// Two packages meet here and neither is allowed to know the other: swarm
// renders the metainfo (only it may touch anacrolix) and output decides where
// a run's artefacts go and how they reach disk safely. This is the seam, and
// it is three lines long precisely because both halves already exist.
func saveTorrentFile(writer *output.Writer, torrent *swarm.Torrent) (string, error) {
	data, err := torrent.TorrentFile()
	if err != nil {
		return "", fmt.Errorf("render the torrent file: %w", err)
	}
	return writer.WriteTorrent(data)
}

// recordedSource decides what run.json says this run was given.
//
// A magnet is recorded exactly as typed. It is the whole torrent in one line,
// it costs nothing to keep, and pasting it back is literally how the run is
// repeated - which is what cache.Run.Source promises.
//
// A .torrent source is recorded as the magnet its own infohash resolves to
// (swarm.Torrent.Magnet), not a path at all. TOR-73 first tried the copy this
// run had just saved in its own directory (output.Layout.TorrentPath),
// because the path the source was actually read from is not reliably still
// there - the web UI stages a dropped .torrent in a temp directory and
// removes it the moment the run's event stream ends (handleUploadTorrent's
// cleanup, fired from pump before the client is even told the run finished),
// so recording that path described a file that provably did not exist by the
// time anyone could read the record. But TOR-73 left the saved copy's path
// absolute, which is only true while the results tree sits exactly where it
// was captured - and cache.Run.Source's own doc calls it "the string a
// person could paste back in", a promise a moved tree breaks the same way
// the temp file did (TOR-86). A magnet has no directory to depend on: it
// reopens the same torrent by infohash, over DHT and trackers, from wherever
// the tree - or the .torrent inside it - ends up.
//
// It is not what anyone typed, which is the one honest cost of this choice
// over recordedSource's other candidate, a path relative to the run
// directory: app.js's regenerate POSTs this string straight back to
// swarm.ParseSource, which os.Stats it as a bare filesystem path with nothing
// to join it against, so a relative path would only ever resolve by accident
// of whatever directory the server happened to be running in - it is
// portable to LOOK at, not portable to USE. The magnet is both.
//
// With no saved copy - the .torrent write failed - the original path is
// still the most honest thing left to say, exactly as before TOR-86: this
// path only replaces the case TOR-73 already covered.
func recordedSource(source string, src swarm.Source, torrentPath, magnet string) string {
	if torrentPath == "" || src.IsMagnet() {
		return source
	}
	return magnet
}

// mergeIndices unions two file-index lists into one, deduplicated and sorted
// so the result is stable regardless of which run contributed which index.
// It always returns a non-nil slice, even from two nil inputs, so a fresh
// record's Selected and Complete serialize as "[]" rather than "null".
func mergeIndices(existing, next []int) []int {
	set := make(map[int]bool, len(existing)+len(next))
	for _, i := range existing {
		set[i] = true
	}
	for _, i := range next {
		set[i] = true
	}
	out := make([]int, 0, len(set))
	for i := range set {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

// reusableFrames picks out the capture points an earlier run already took,
// keyed by their position in the plan.
//
// A point is only reused when its recorded request matches the one planned
// now. The parameters that decide where points fall are part of the result's
// key, so they cannot have changed - but an index is a weak thing to trust a
// frame to, and comparing the timestamp costs nothing.
//
// Points that failed last time are deliberately not reused: a gap is what a
// second run exists to fill.
func reusableFrames(prior manifest.Manifest, points []time.Duration) map[int]manifest.Frame {
	if len(prior.Frames) == 0 {
		return nil
	}

	out := make(map[int]manifest.Frame, len(prior.Frames))
	for _, f := range prior.Frames {
		if f.Index < 0 || f.Index >= len(points) {
			continue
		}
		if f.Shift == manifest.ShiftFailed || f.ActualMS == nil || f.Path == "" {
			continue
		}
		if f.RequestedMS != points[f.Index].Milliseconds() {
			continue
		}
		if info, err := os.Stat(f.Path); err != nil || info.Size() == 0 {
			continue
		}
		out[f.Index] = f
	}
	return out
}

// availabilityBuckets is how finely the swarm map is recorded. Enough for a UI
// to draw a bar and for a person to see where the gaps are, without pretending
// to a precision that changes minute by minute anyway.
const availabilityBuckets = 64

// toneMappedTo names what the frames were converted to, for the manifest.
// Empty when nothing was converted, which covers both an SDR source and a
// high dynamic range one nothing in the release can render faithfully.
func toneMappedTo(t frames.ToneMap) string {
	if !t.Applies() {
		return ""
	}
	return "bt709"
}

// writeManifest records what happened to one file, next to its frames.
//
// The cost it carries is the run's, not the file's: the budget is shared
// across files, and a per-file share of it would be a number nothing enforces.
func (e *Engine) writeManifest(deps fileDeps, file swarm.FileInfo,
	info probe.MediaInfo, records []manifest.Frame, sequential bool) (string, error) {

	spent, elapsed := deps.tracker.Spent()
	limitBytes, limitTime := deps.tracker.Limits()
	connected, seeds := deps.torrent.Peers()

	limitHit := ""
	if exhausted, reason := deps.tracker.Exhausted(); exhausted {
		limitHit = string(reason)
	}

	m := manifest.Manifest{
		Version:   manifest.Version,
		Tool:      version.Version,
		CreatedAt: time.Now().UTC(),
		Torrent: manifest.Torrent{
			InfoHash:     deps.torrent.InfoHash(),
			Name:         deps.torrent.Name(),
			PieceLength:  deps.torrent.PieceLength(),
			Private:      deps.torrent.Private(),
			Peers:        connected,
			Seeds:        seeds,
			Availability: deps.torrent.Availability().Coarse(file, availabilityBuckets),
		},
		File: manifest.File{
			Index:      file.Index,
			Path:       file.Path,
			Bytes:      file.Length,
			DurationMS: info.Duration.Milliseconds(),
			Container:  info.FormatName,
		},
		Video: manifest.Video{
			Codec:        info.Video.Codec,
			Profile:      info.Video.Profile,
			Width:        info.Video.Width,
			Height:       info.Video.Height,
			FPS:          info.Video.FPS,
			BitRate:      info.Video.BitRate,
			BitsPerPixel: info.Video.BitsPerPixel(),

			DynamicRange:       deps.toneMap.Source,
			ToneMappedTo:       toneMappedTo(deps.toneMap),
			ColorTransfer:      info.Video.ColorTransfer,
			ColorPrimaries:     info.Video.ColorPrimaries,
			ColorSpace:         info.Video.ColorSpace,
			DolbyVisionProfile: info.Video.DolbyVisionProfile,
		},
		Audio:     make([]manifest.Audio, 0, len(info.Audio)),
		Subtitles: make([]manifest.Subtitle, 0, len(info.Subtitles)),
		Frames:    records,
		Cost: manifest.Cost{
			DownloadedBytes: spent,
			ElapsedMS:       elapsed.Milliseconds(),
			LimitBytes:      limitBytes,
			LimitMS:         limitTime.Milliseconds(),
			LimitHit:        limitHit,
			Sequential:      sequential,
		},
	}

	for _, a := range info.Audio {
		m.Audio = append(m.Audio, manifest.Audio{
			Index: a.Index, Language: a.Language, Codec: a.Codec,
			Channels: a.Channels, Title: a.Title, Default: a.Default,
		})
	}
	for _, sub := range info.Subtitles {
		m.Subtitles = append(m.Subtitles, manifest.Subtitle{
			Index: sub.Index, Language: sub.Language, Format: sub.Codec,
			Title: sub.Title, Forced: sub.Forced, Default: sub.Default,
		})
	}

	// The records carry the absolute paths WriteFrame returned; WriteManifest
	// is what turns them into the relative form that goes on disk, so this
	// func never has to know that the two shapes differ (manifest.Frame.Path).
	return deps.writer.WriteManifest(file.Index, file.Path, m)
}

// writeSheet composes the contact sheet from whatever frames this file's
// records point to and writes it beside the manifest. It reads tiles from
// disk rather than from anything held in memory as frames arrived, per
// section 2.11 - and tolerates records shorter than points (a run stopped
// between capture points) and individual records with no frame (an
// unavailable or rejected point), per section 2.3.
func (e *Engine) writeSheet(deps fileDeps, file swarm.FileInfo,
	info probe.MediaInfo, points []time.Duration, records []manifest.Frame) (string, error) {

	data, err := sheet.Build(points, records, info.Video.Width, info.Video.Height)
	if err != nil {
		return "", fmt.Errorf("compose sheet: %w", err)
	}
	return deps.writer.WriteFile(file.Index, file.Path, output.SheetName, data)
}

// minSeekTolerance is the smallest gap worth calling a miss. Keyframes are
// commonly seconds apart, so below this nothing can be judged: a plan denser
// than the file's own keyframes produces near-duplicate frames by design, not
// by failure.
const minSeekTolerance = 10 * time.Second

// seekTolerance is how far before its capture point a frame may land. The
// spacing between points is the natural bound - past it, a frame belongs to
// the neighbouring point rather than its own.
func seekTolerance(points []time.Duration) time.Duration {
	spacing := time.Duration(0)
	if len(points) >= 2 {
		spacing = points[1] - points[0]
	}
	if spacing < minSeekTolerance {
		return minSeekTolerance
	}
	return spacing
}

// landedNear reports whether a decoded frame answers the point it was asked
// for. Forward slack is small and only absorbs rounding: a keyframe is
// expected at or before the request.
func landedNear(requested, actual, tolerance time.Duration) bool {
	if actual > requested+time.Second {
		return false
	}
	return requested-actual <= tolerance
}

// capture is one frame and the story of where it came from.
type capture struct {
	frame frames.Frame
	// asked is the timestamp finally requested, which is the capture point
	// unless it had to be moved; actual is where the decoder landed.
	asked  time.Duration
	actual time.Duration
	shift  ShiftReason
}

// maxShift is how far a capture point may be moved, as a multiple of step.
// Two steps is a quarter of the way to the neighbouring point on either side:
// far enough to escape a hole, near enough that the frame still represents the
// moment it was planned for.
const maxShift = 2

// maxBlankSteps is how many neighbouring keyframes a blank frame may be
// stepped past before it is kept as it is. Unlike a shift, each attempt here
// costs a real decode - it is judged after the frame is already in hand - so
// it is bounded the same way and for the same reason: escape a hole in the
// picture without hunting forever.
const maxBlankSteps = 2

// rejectBlank detects a black or near-monotone frame (REQUIREMENTS.md 2.3)
// and steps forward through neighbouring keyframes to escape it, up to
// maxBlankSteps attempts. When every attempt is still blank, the last frame
// reached is kept rather than the point being failed - ARCHITECTURE.md's
// Stepping state is explicit about this: "attempts exhausted, take it as
// is." The marker only ever says ShiftBlank when a step actually happened;
// a frame that was never blank, or that failed every retry, leaves shot's
// own shift untouched.
//
// Stepping only moves forward. KeyframeAt seeks backwards to the keyframe at
// or before its argument, so a request strictly after the timestamp already
// held is how the next keyframe is reached - there is no "next keyframe"
// query. When a request lands on the same pts already held, nothing moved,
// and the next attempt tries a larger step instead of repeating the same
// request; a genuine probe or decode failure stops retrying rather than
// spending the remaining attempts on requests unlikely to do better.
func (e *Engine) rejectBlank(ctx context.Context, deps fileDeps, url string, step time.Duration, shot capture) capture {
	blank, err := frames.IsBlank(shot.frame.Data)
	if err != nil || !blank {
		return shot
	}
	if step <= 0 {
		step = minSeekTolerance / 4
	}

	best := shot
	advance := step
	for attempt := 1; attempt <= maxBlankSteps; attempt++ {
		candidate := best.actual + advance

		keyframe, err := deps.prober.KeyframeAt(ctx, url, candidate)
		if err != nil {
			break
		}
		if keyframe.PTS <= best.actual {
			// Did not reach a new keyframe; try further out next time,
			// without spending a decode on a frame already held.
			advance *= 2
			continue
		}
		advance = step

		frame, err := deps.extractor.Frame(ctx, url, keyframe.PTS)
		if err != nil {
			break
		}

		best = capture{frame: frame, asked: shot.asked, actual: keyframe.PTS, shift: ShiftBlank}

		blank, err = frames.IsBlank(frame.Data)
		if err != nil || !blank {
			break
		}
	}

	return best
}

// captureOne takes the frame for one capture point, moving it if the swarm
// cannot serve the pieces there.
//
// Reachability is judged from an offset estimated by time, before anything is
// read. Asking ffprobe where the keyframe is would be more accurate and would
// defeat the purpose: that question is itself a read, and on a region no peer
// holds it stalls until the deadline - which is exactly the wait the shift
// exists to avoid.
func (e *Engine) captureOne(ctx context.Context, cfg Config, deps fileDeps, url string,
	file swarm.FileInfo, duration, at, step time.Duration) (capture, error) {

	avail := deps.torrent.Availability()

	for _, candidate := range shiftCandidates(at, step, duration) {
		if !reachable(avail, cfg.Profile, file, duration, candidate) {
			continue
		}

		keyframe, err := deps.prober.KeyframeAt(ctx, url, candidate)
		if err != nil {
			return capture{}, err
		}
		frame, err := deps.extractor.Frame(ctx, url, keyframe.PTS)
		if err != nil {
			return capture{}, err
		}

		shift := ShiftNone
		if candidate != at {
			shift = ShiftUnavailable
		}
		return capture{frame: frame, asked: candidate, actual: keyframe.PTS, shift: shift}, nil
	}

	return capture{}, Fail(CodeUnavailable, fmt.Errorf(
		"no peer holds the pieces at %s, nor within %s of it",
		at.Round(time.Second), (step*maxShift).Round(time.Second)))
}

// availabilityMap is the part of swarm.Availability this decision needs. Named
// here rather than taken concretely so the rule can be tested without standing
// up a swarm - the case that matters most is the one where there is no swarm
// to stand up yet.
type availabilityMap interface {
	Known() bool
	PieceLength() int64
	OverFileRange(f swarm.FileInfo, off, length int64) int
}

// shiftCandidates lists where to try, nearest first and alternating sides, so
// a frame moves the least it can and does not drift consistently one way.
func shiftCandidates(at, step, duration time.Duration) []time.Duration {
	out := []time.Duration{at}
	if step <= 0 {
		return out
	}

	for n := 1; n <= maxShift; n++ {
		for _, candidate := range []time.Duration{at + time.Duration(n)*step, at - time.Duration(n)*step} {
			if candidate > 0 && (duration <= 0 || candidate < duration) {
				out = append(out, candidate)
			}
		}
	}
	return out
}

// reachStart is how much of a capture point's region must be held for it to be
// worth trying, in pieces.
//
// Two, and deliberately not the profile's fetch window. The window is claim
// geometry - how much to ask for at once so the decoder does not come back for
// more - while this asks something else: does this part of the file exist in
// the swarm at all. Measured on a 2.9 MiB fixture, judging by the 1 MiB window
// meant every capture point overlapped a hole in the middle, because the
// window was a third of the file; two pieces scale with the torrent's own
// geometry instead of a policy constant, and are the smallest span that holds
// the keyframe and survives a piece boundary.
const reachStart = 2

// reachable reports whether the swarm can serve the region a capture point
// would start from.
//
// Until the swarm has said anything the honest answer is "unknown", and it is
// given as yes. Measured, not guessed: judging on a map that was merely empty
// skipped the first frame of a healthy run, because a peer is connected for a
// while before its bitfield arrives and an empty map is indistinguishable from
// a swarm that holds nothing.
func reachable(avail availabilityMap, profile swarm.Profile,
	file swarm.FileInfo, duration, at time.Duration) bool {

	if !avail.Known() || duration <= 0 || file.Length <= 0 {
		return true
	}

	span := avail.PieceLength() * reachStart
	if span <= 0 {
		span = profile.WindowSize(0)
	}

	offset := int64(float64(file.Length) * (float64(at) / float64(duration)))
	return avail.OverFileRange(file, offset, span) > 0
}

// haltReason reports whether the run should stop and why.
func haltReason(ctx context.Context, tracker *BudgetTracker) (StopReason, bool) {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// The only deadline on this context is the time budget
			// (BudgetTracker.Context), so a context that expired on its own
			// - rather than being cancelled by a caller - can only mean the
			// clock. StopTime, never StopBudget: this is the run's own wall
			// clock, told apart from its own traffic ceiling since TOR-161.
			return StopTime, true
		}
		return StopCancelled, true
	}
	if exhausted, reason := tracker.Exhausted(); exhausted {
		return reason, true
	}
	return StopCompleted, false
}

func extensionFor(format frames.Format) string {
	if format == frames.PNG {
		return "png"
	}
	return "jpg"
}

// ParamsKey identifies the parameters that change what a run produces, so an
// identical rerun can be served from cache while a different plan cannot.
//
// Budgets and parallelism are deliberately excluded: they change how long a
// run takes, not what a finished one contains.
// budgetFor resolves the run's budget, defaulting it to the number of files
// actually being worked on rather than everything the torrent holds: asking
// for one episode out of twenty should get one episode's worth of traffic, not
// a twentieth of the pack's.
func budgetFor(cfg Config, selected int) Budget {
	if cfg.Budget.MaxBytes == 0 && cfg.Budget.MaxTime == 0 {
		return DefaultBudget(selected)
	}
	return cfg.Budget
}

// indicesOf reduces files to the numbers the torrent knows them by.
func indicesOf(files []swarm.FileInfo) []int {
	out := make([]int, 0, len(files))
	for _, f := range files {
		out = append(out, f.Index)
	}
	return out
}

// The file selection is deliberately not part of the key. Which files a run
// asked for does not change what a frame of any one of them looks like, and
// keying on it would scatter the same file's frames across directories - and
// stop a later run from reusing what an earlier, narrower one already fetched.
func ParamsKey(cfg Config) string {
	raw := fmt.Sprintf("n=%d;start=%.4f;end=%.4f;profile=%s;format=%s;sequential=%v",
		cfg.Plan.Count, cfg.Plan.Start, cfg.Plan.End, cfg.Profile.Name, cfg.Format, cfg.Sequential)

	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}
