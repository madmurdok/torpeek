package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/torpeek/torpeek/internal/bridge"
	"github.com/torpeek/torpeek/internal/ffmpeg"
	"github.com/torpeek/torpeek/internal/frames"
	"github.com/torpeek/torpeek/internal/output"
	"github.com/torpeek/torpeek/internal/probe"
	"github.com/torpeek/torpeek/internal/swarm"
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
	bus.Publish(MetadataReady{
		Name:     torrent.Name(),
		InfoHash: torrent.InfoHash(),
		Private:  torrent.Private(),
		Videos:   videos,
		BlindDHT: session.WentOnlineBlind(),
	})

	if len(videos) == 0 {
		err := fmt.Errorf("torrent %q holds no video files", torrent.Name())
		bus.Publish(Failed{File: -1, Code: CodeNoVideo, Err: err})
		return
	}

	// The budget covers the whole run, so every file shares one tracker.
	budget := cfg.Budget
	if budget.MaxBytes == 0 && budget.MaxTime == 0 {
		budget = DefaultBudget(len(videos))
	}
	tracker := NewBudgetTracker(budget, torrent)

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

	for _, video := range videos {
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

	for i, at := range points {
		if _, halt := haltReason(ctx, deps.tracker); halt {
			// Stopping between capture points, never mid-write: the frame in
			// flight is finished and kept.
			break
		}
		if warning := deps.tracker.Warning(); warning != nil {
			deps.bus.Publish(*warning)
		}

		frame, actual, err := e.captureOne(ctx, deps, url, at)
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

		path, err := deps.writer.WriteFrame(file.Index, file.Path, i, frame.Data, extensionFor(cfg.Format))
		if err != nil {
			return produced, Fail(CodeStorage, err)
		}
		produced++

		// Actual almost never equals Requested: decoding starts at the
		// keyframe before the wanted moment, which is ordinary behaviour and
		// not a shift. ShiftUnavailable means something else entirely - that
		// the swarm could not serve the pieces there and another position was
		// chosen instead - and it is set by the availability logic (TOR-13),
		// not inferred from the timestamps differing.
		deps.bus.Publish(FrameReady{
			File:      file.Index,
			Index:     i,
			Requested: at,
			Actual:    actual,
			Shift:     ShiftNone,
			Path:      path,
			Width:     frame.Width,
			Height:    frame.Height,
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

// captureOne locates the keyframe for a timestamp and decodes it. The keyframe
// lookup comes first and costs almost nothing, which is what lets a decision
// about a point be made before spending traffic on its window.
func (e *Engine) captureOne(ctx context.Context, deps fileDeps, url string, at time.Duration) (frames.Frame, time.Duration, error) {
	keyframe, err := deps.prober.KeyframeAt(ctx, url, at)
	if err != nil {
		return frames.Frame{}, 0, err
	}

	frame, err := deps.extractor.Frame(ctx, url, keyframe.PTS)
	if err != nil {
		return frames.Frame{}, 0, err
	}
	return frame, keyframe.PTS, nil
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
func ParamsKey(cfg Config) string {
	raw := fmt.Sprintf("n=%d;start=%.4f;end=%.4f;profile=%s;format=%s",
		cfg.Plan.Count, cfg.Plan.Start, cfg.Plan.End, cfg.Profile.Name, cfg.Format)

	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}
