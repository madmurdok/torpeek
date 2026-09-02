package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/madmurdok/torpeek/internal/bridge"
	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/frames"
	"github.com/madmurdok/torpeek/internal/output"
	"github.com/madmurdok/torpeek/internal/probe"
	"github.com/madmurdok/torpeek/internal/swarm"
)

// Config is everything one run needs.
type Config struct {
	// Source is a magnet URI or a path to a .torrent file.
	Source string
	// OutputRoot is where results are written.
	OutputRoot string

	Plan    frames.Plan
	Profile swarm.Profile
	Budget  Budget

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
}

// DefaultConfig fills in the defaults from REQUIREMENTS.md section 7.
func DefaultConfig(source, outputRoot, dataDir string) Config {
	return Config{
		Source:      source,
		OutputRoot:  outputRoot,
		Plan:        frames.DefaultPlan(),
		Profile:     swarm.MinTime,
		Budget:      DefaultBudget(1),
		Parallelism: 4,
		Format:      frames.JPEG,
		Swarm:       swarm.DefaultConfig(dataDir),
		Bridge:      bridge.DefaultConfig(),
	}
}

// DefaultParallelism is how many files a desktop run works on at once.
const DefaultParallelism = 4

// Engine runs one job and reports it as events. It prints nothing.
type Engine struct {
	tools ffmpeg.Tools
}

// NewEngine returns an engine using the given external tools.
func NewEngine(tools ffmpeg.Tools) *Engine {
	return &Engine{tools: tools}
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

	session, torrent, err := swarm.Open(ctx, cfg.Swarm, src)
	if err != nil {
		bus.Publish(Failed{File: -1, Code: CodeOf(err), Err: err})
		return
	}
	defer session.Close()

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
		BlindDHT: session.WentOnlineBlind(),
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
	tracker := NewBudgetTracker(budgetFor(cfg, len(selected)), torrent)

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
	)

	sem := make(chan struct{}, cfg.Parallelism)
	var wg sync.WaitGroup

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

			produced, err := e.processFile(runCtx, cfg, fileDeps{
				torrent:   torrent,
				bridge:    srv,
				prober:    prober,
				extractor: extractor,
				writer:    writer,
				tracker:   tracker,
				bus:       bus,
			}, file)

			mu.Lock()
			defer mu.Unlock()
			made += produced
			if err == nil {
				done++
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

	spent, _ := tracker.Spent()
	bus.Publish(Done{
		Reason:         stopped,
		Files:          done,
		Frames:         made,
		DownloadedByte: spent,
		Elapsed:        time.Since(started),
	})
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
	bus       *Bus
}

func (e *Engine) processFile(ctx context.Context, cfg Config, deps fileDeps, file swarm.FileInfo) (int, error) {
	url, withdraw, err := deps.bridge.Publish(bridge.FromTorrent(deps.torrent, cfg.Profile), file.Index)
	if err != nil {
		return 0, err
	}
	defer withdraw()

	info, err := deps.prober.Inspect(ctx, url)
	if err != nil {
		return 0, err
	}

	points, err := cfg.Plan.Points(info.Duration)
	if err != nil {
		return 0, err
	}

	deps.bus.Publish(FileStarted{
		File:  file.Index,
		Path:  file.Path,
		Media: info,
		Plan:  points,
	})

	produced := 0
	skipped := 0
	tolerance := seekTolerance(points)

	for i, at := range points {
		if _, halt := haltReason(ctx, deps.tracker); halt {
			// Stopping between capture points, never mid-write: the frame in
			// flight is finished and kept.
			break
		}
		if warning := deps.tracker.Warning(); warning != nil {
			deps.bus.Publish(*warning)
		}

		shot, err := e.captureOne(ctx, cfg, deps, url, file, info.Duration, at, tolerance/4)
		if err != nil {
			skipped++
			deps.bus.Publish(FrameSkipped{
				File:      file.Index,
				Index:     i,
				Requested: at,
				Code:      CodeOf(err),
				Reason:    err.Error(),
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
			deps.bus.Publish(FrameSkipped{
				File:      file.Index,
				Index:     i,
				Requested: at,
				Code:      CodeSeekFailed,
				Reason: fmt.Sprintf("decoded at %s, %s away from the requested %s",
					shot.actual.Round(time.Second), (shot.asked - shot.actual).Abs().Round(time.Second),
					shot.asked.Round(time.Second)),
			})
			continue
		}

		path, err := deps.writer.WriteFrame(file.Index, file.Path, i, shot.frame.Data, extensionFor(cfg.Format))
		if err != nil {
			return produced, Fail(CodeStorage, err)
		}
		produced++

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
		deps.bus.Publish(Progress{
			File:           file.Index,
			FramesDone:     produced,
			FramesTotal:    len(points),
			DownloadedByte: spent,
			Elapsed:        elapsed,
			Peers:          connected,
			Seeds:          seeds,
		})
	}

	deps.bus.Publish(FileDone{
		File:    file.Index,
		Path:    file.Path,
		Frames:  produced,
		Skipped: skipped,
	})

	return produced, nil
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
		span = profile.Window
	}

	offset := int64(float64(file.Length) * (float64(at) / float64(duration)))
	return avail.OverFileRange(file, offset, span) > 0
}

// haltReason reports whether the run should stop and why.
func haltReason(ctx context.Context, tracker *BudgetTracker) (StopReason, bool) {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// The only deadline on this context is the time budget.
			return StopBudget, true
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
	raw := fmt.Sprintf("n=%d;start=%.4f;end=%.4f;profile=%s;format=%s",
		cfg.Plan.Count, cfg.Plan.Start, cfg.Plan.End, cfg.Profile.Name, cfg.Format)

	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}
