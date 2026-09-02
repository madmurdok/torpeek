package core

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/probe"
	"github.com/madmurdok/torpeek/internal/swarm"
)

func TestBusFansOutToEverySubscriber(t *testing.T) {
	b := NewBus(8)
	defer b.Close()

	first, _ := b.Subscribe()
	second, _ := b.Subscribe()

	b.Publish(FrameReady{Index: 3})

	for i, ch := range []<-chan Event{first, second} {
		select {
		case ev := <-ch:
			frame, ok := ev.(FrameReady)
			if !ok {
				t.Fatalf("subscriber %d got %T, want FrameReady", i, ev)
			}
			if frame.Index != 3 {
				t.Errorf("subscriber %d got index %d, want 3", i, frame.Index)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}
}

// TestSlowSubscriberDoesNotStallTheRun is the property the whole design exists
// for: a client that stops reading must not be able to hold the run open.
func TestSlowSubscriberDoesNotStallTheRun(t *testing.T) {
	b := NewBus(2)
	defer b.Close()

	if _, ok := interface{}(b).(interface{ Publish(Event) }); !ok {
		t.Fatal("bus does not publish")
	}
	b.Subscribe() // subscribed and never read from

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			b.Publish(Progress{FramesDone: i})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked on a subscriber that stopped reading")
	}

	if b.Dropped() == 0 {
		t.Error("nothing was recorded as dropped, so the overflow went unnoticed")
	}
}

// TestTerminalEventWaitsButNotForever: Done matters enough to wait for, and
// not enough to hang the process.
func TestTerminalEventWaitsButNotForever(t *testing.T) {
	b := NewBus(1)
	defer b.Close()

	b.terminalWait = 200 * time.Millisecond
	b.Subscribe() // never read

	b.Publish(Progress{}) // fills the buffer
	b.Publish(Progress{}) // dropped
	start := time.Now()
	b.Publish(Done{Reason: StopCompleted})
	elapsed := time.Since(start)

	if elapsed < 150*time.Millisecond {
		t.Errorf("terminal event gave up after %s, without really waiting", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("terminal event blocked for %s on an unread subscriber", elapsed)
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	b := NewBus(8)
	defer b.Close()

	ch, cancel := b.Subscribe()
	b.Publish(Progress{FramesDone: 1})
	cancel()

	// The publisher retires cancelled subscriptions, so the channel closes on
	// the next publish rather than racing with an in-flight send.
	b.Publish(Progress{FramesDone: 2})

	seen := 0
	for range ch {
		seen++
		if seen > 10 {
			t.Fatal("channel never closed after unsubscribing")
		}
	}
	if seen > 1 {
		t.Errorf("received %d events after unsubscribing, want at most the one already queued", seen)
	}
}

func TestCloseEndsSubscriptions(t *testing.T) {
	b := NewBus(4)
	ch, _ := b.Subscribe()

	b.Close()

	select {
	case _, open := <-ch:
		if open {
			t.Error("channel delivered an event after Close")
		}
	case <-time.After(time.Second):
		t.Error("channel was not closed by Close")
	}

	// Publishing after Close must be harmless, not a panic on a closed channel.
	b.Publish(Done{})
}

func TestConcurrentPublishAndSubscribe(t *testing.T) {
	b := NewBus(4)
	defer b.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cancel := b.Subscribe()
			go func() {
				for range ch {
				}
			}()
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				b.Publish(Progress{FramesDone: n})
			}
		}(i)
	}

	wg.Wait()
}

func TestCodeOfClassifiesPipelineErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorCode
	}{
		{name: "nil", err: nil, want: ""},
		{name: "already coded", err: Fail(CodeStorage, errors.New("disk full")), want: CodeStorage},
		{name: "wrapped coded", err: fmt.Errorf("writing: %w", Fail(CodeStorage, errors.New("x"))), want: CodeStorage},
		{name: "cancelled", err: context.Canceled, want: CodeCancelled},
		{name: "deadline", err: context.DeadlineExceeded, want: CodeCancelled},
		{name: "no metadata", err: fmt.Errorf("open: %w", swarm.ErrNoMetadata), want: CodeNoMetadata},
		{name: "privacy", err: swarm.ErrPrivacyUnresolvable, want: CodePrivacyUnresolvable},
		{name: "no index", err: fmt.Errorf("inspect: %w", probe.ErrNoIndex), want: CodeUnprobeable},
		{name: "tool missing", err: &ffmpeg.NotFoundError{Tool: "ffmpeg"}, want: CodeToolMissing},
		{name: "tool failed", err: &ffmpeg.ExitError{Tool: "ffmpeg", Code: 1}, want: CodeDecodeFailed},
		{name: "unknown", err: errors.New("something new"), want: CodeInternal},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CodeOf(tc.err); got != tc.want {
				t.Errorf("CodeOf(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestCoreNeverPrints enforces requirements section 3.1 structurally rather
// than by review: the core must not write to a terminal, or the TUI, the web
// UI and a future service could not all sit on top of it.
func TestCoreNeverPrints(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	banned := map[string][]string{
		"fmt": {"Print", "Printf", "Println", "Fprint", "Fprintf", "Fprintln"},
		"log": {"Print", "Printf", "Println", "Fatal", "Fatalf", "Fatalln", "Panic"},
		"os":  {"Stdout", "Stderr"},
		"":    {"print", "println"},
	}

	for name, pkg := range pkgs {
		if strings.HasSuffix(name, "_test") {
			continue
		}
		for path, file := range pkg.Files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				for _, bad := range banned[ident.Name] {
					if sel.Sel.Name == bad {
						t.Errorf("%s uses %s.%s - the core must not write to a terminal",
							path, ident.Name, sel.Sel.Name)
					}
				}
				return true
			})
		}
	}
}
