package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

	Plan    frames.Plan
	Profile swarm.Profile
	Budget  Budget

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

	// Before anything else, and before any session exists: an identical rerun
	// is required to make no network request at all, and the only way to be
	// certain of that is not to connect.
	if e.serveFromCache(cfg, src, bus, started) {
		return
	}

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
		// finished are the files whose frame set came out whole, which is what
		// a later run may be served from disk.
		finished []int
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

			produced, complete, err := e.processFile(runCtx, cfg, fileDeps{
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
	if err := saveRunRecord(writer.Layout(), torrent, videos, finished); err != nil {
		bus.Publish(Failed{File: -1, Code: CodeStorage, Err: err})
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

func (e *Engine) processFile(ctx context.Context, cfg Config, deps fileDeps, file swarm.FileInfo) (produced int, complete bool, err error) {
	url, withdraw, err := deps.bridge.Publish(bridge.FromTorrent(deps.torrent, cfg.Profile), file.Index)
	if err != nil {
		return 0, false, err
	}
	defer withdraw()

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
			records = append(records, manifest.Frame{
				Index:       i,
				RequestedMS: at.Milliseconds(),
				Shift:       manifest.ShiftFailed,
				Error:       string(CodeOf(err)),
			})
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
// result from a partial one without opening a session to find out.
func saveRunRecord(layout output.Layout, torrent *swarm.Torrent, videos []swarm.FileInfo, finished []int) error {
	record := cache.Run{
		Version:   cache.Version,
		Tool:      version.Version,
		CreatedAt: time.Now().UTC(),
		InfoHash:  torrent.InfoHash(),
		Name:      torrent.Name(),
		Private:   torrent.Private(),
		Videos:    make([]cache.File, 0, len(videos)),
		Complete:  finished,
	}
	for _, v := range videos {
		record.Videos = append(record.Videos, cache.File{
			Index: v.Index, Path: v.Path, Bytes: v.Length, Offset: v.Offset,
		})
	}
	if record.Complete == nil {
		record.Complete = []int{}
	}
	return cache.SaveRun(layout.RunDir(), record)
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

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode manifest: %w", err)
	}
	data = append(data, '\n')

	return deps.writer.WriteFile(file.Index, file.Path, manifest.Name, data)
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
	raw := fmt.Sprintf("n=%d;start=%.4f;end=%.4f;profile=%s;format=%s;sequential=%v",
		cfg.Plan.Count, cfg.Plan.Start, cfg.Plan.End, cfg.Profile.Name, cfg.Format, cfg.Sequential)

	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}
