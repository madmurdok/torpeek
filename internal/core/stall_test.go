package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/bridge"
	"github.com/madmurdok/torpeek/internal/probe"
)

// stallingContent serves a file normally until it is frozen, after which
// every read blocks until the bridge gives up on it.
//
// Frozen is the shape the real defect takes: mid-run, against a seeder holding
// every piece, the torrent client stops making progress and every read in
// flight waits out the bridge's timeout. Reproducing it here without a swarm
// is what makes it a test rather than a wait for a one-in-ten run.
type stallingContent struct {
	data []byte

	mu               sync.Mutex
	holeFrom, holeTo int64
}

func (c *stallingContent) Length(int) (int64, error) { return int64(len(c.data)), nil }
func (c *stallingContent) Name(int) string           { return "movie.mkv" }

// stall makes every read that reaches into [from, to) hand over whatever sits
// before it and then wait, which is what a read running into pieces the swarm
// has stopped delivering does - not a clean EOF, and not an error either.
func (c *stallingContent) stall(from, to int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.holeFrom, c.holeTo = from, to
}

func (c *stallingContent) hole() (int64, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.holeFrom, c.holeTo
}

func (c *stallingContent) Fetch(ctx context.Context, _ int, off, length int64) (io.ReadCloser, error) {
	end := off + length
	if end > int64(len(c.data)) {
		end = int64(len(c.data))
	}

	from, to := c.hole()
	if to <= from || end <= from || off >= to {
		return io.NopCloser(bytes.NewReader(c.data[off:end])), nil
	}
	if off >= from {
		// The read starts inside the hole: nothing to hand over at all.
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &stallingReader{r: bytes.NewReader(c.data[off:from]), ctx: ctx}, nil
}

// stallingReader hands over the bytes it has and then blocks.
type stallingReader struct {
	r   *bytes.Reader
	ctx context.Context
}

func (s *stallingReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err == io.EOF {
		<-s.ctx.Done()
		return n, s.ctx.Err()
	}
	return n, err
}

func (s *stallingReader) Close() error { return nil }

// TestStalledReadIsNotBlamedOnTheContainer is TOR-45's acceptance: a read that
// times out must surface as its own error code, not as "this container has no
// usable index".
//
// The two are indistinguishable from where ffprobe stands. It is a subprocess
// reading over HTTP; a request that runs out of time gives it headers and then
// a body that stops early, so it reports no keyframe with a byte position and
// exits successfully. That verdict is about a file it never finished reading,
// and it is the same verdict a genuinely broken container earns - which is why
// the answer has to come from the bridge, the only layer that knows the read
// died.
//
// The test also pins the reason the obvious fix does not work: at the moment
// the prober gives its wrong answer, every context on the Go side is still
// alive. There is nothing there to check.
func TestStalledReadIsNotBlamedOnTheContainer(t *testing.T) {
	tools := locateTools(t)

	renderCtx, cancelRender := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancelRender()

	// Raw video, and big, for the same reason the profiles fixture is: every
	// packet is a keyframe, so this is the file least capable of having no
	// usable index, and a keyframe eight seconds in sits far enough past the
	// head that reaching it is a real read rather than a rounding error.
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if _, err := tools.Run(renderCtx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=10",
		"-c:v", "rawvideo", "-pix_fmt", "yuv420p",
		path,
	); err != nil {
		t.Fatalf("render sample: %v", err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}

	content := &stallingContent{data: payload}

	cfg := bridge.DefaultConfig()
	// Short on purpose: the defect needs a timeout to expire, not a long one.
	cfg.RequestTimeout = 2 * time.Second
	b, err := bridge.Start(cfg)
	if err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	defer b.Close()

	url, withdraw, err := b.Publish(content, 0)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	defer withdraw()

	// Generous, and deliberately so: this context must still be healthy when
	// the prober reports its wrong answer.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	prober := probe.New(tools)

	// First, on a bridge that is answering: the file is sound, and this is the
	// evidence for it. Without this half the test would prove only that a
	// broken file reports as broken.
	info, err := prober.Inspect(ctx, url)
	if err != nil {
		t.Fatalf("inspect before the stall: %v", err)
	}
	t.Logf("healthy: %s %s, %s, %dx%d, %d bytes - every packet a keyframe",
		info.FormatName, info.Video.Codec, info.Duration,
		info.Video.Width, info.Video.Height, len(payload))

	before, _ := b.Stalls(url)

	// A hole across the middle, with the head and the tail still served: the
	// file opens, its index is readable, and only the read for the capture
	// point itself runs into pieces that never arrive.
	size := int64(len(payload))
	content.stall(size/8, size-size/8)

	if _, err = prober.KeyframeAt(ctx, url, 8*time.Second); err == nil {
		t.Fatal("a keyframe came back from a bridge that answers nothing")
	}
	t.Logf("keyframe failed with: %v", err)

	if ctx.Err() != nil {
		t.Fatalf("the prober's context ended (%v); the stall has to be caught "+
			"while it is still healthy or the test proves nothing", ctx.Err())
	}

	// What the prober alone is worth: a verdict on the container, and one that
	// a client would act on. This is the mislabelling, asserted rather than
	// described, so the test fails if the raw path ever starts answering
	// correctly on its own and this scaffolding becomes pointless.
	raw := CodeOf(err)
	if raw == CodeReadStalled {
		t.Fatalf("the prober reported the stall by itself (%s) - the "+
			"reclassification below is no longer what makes the difference", raw)
	}
	var noIndex *probe.NoIndexError
	if errors.As(err, &noIndex) {
		t.Logf("unaided verdict: %s, probe reason %q - a statement about the container", raw, noIndex.Reason)
	} else {
		t.Logf("unaided verdict: %s", raw)
	}

	// What the bridge knows, and nothing else does.
	stall := stallSince(b, url, before)
	if stall == nil {
		t.Fatalf("the bridge recorded no stall, though the read it served never finished")
	}
	if !errors.Is(stall, bridge.ErrStalled) {
		t.Fatalf("recorded %v, which is not a bridge stall", stall)
	}
	t.Logf("bridge recorded: %v", stall)

	// And the verdict the run actually reports, which is the acceptance.
	reported := fmt.Errorf("%w; ffprobe then reported: %v", stall, err)
	if got := CodeOf(reported); got != CodeReadStalled {
		t.Errorf("CodeOf(reported) = %s, want %s", got, CodeReadStalled)
	}
	if got := CodeOf(reported); got == CodeUnprobeable {
		t.Errorf("a stalled read is still reported as %s", got)
	}
	// ErrNoIndex is what gates the sequential fallback; a timed-out read must
	// not be able to open that door.
	if errors.Is(reported, probe.ErrNoIndex) {
		t.Errorf("the reported error still matches probe.ErrNoIndex, so a "+
			"stalled read can still send a healthy file down the sequential "+
			"fallback: %v", reported)
	}
}
