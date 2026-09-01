package probe

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/madmurdok/torpeek/internal/bridge"
)

// loggingContent records what the external tool actually asks the bridge for.
//
// It earned its place: it is what revealed that ffprobe requests "this offset
// to the end of the file" and that the bridge was ordering all of that from
// the swarm. Any future question about what a profile costs starts here.
type loggingContent struct {
	inner bridge.Content
	mu    sync.Mutex
	calls []call
}

type call struct {
	off, length int64
}

func (l *loggingContent) Length(f int) (int64, error) { return l.inner.Length(f) }
func (l *loggingContent) Name(f int) string           { return l.inner.Name(f) }

func (l *loggingContent) Fetch(ctx context.Context, f int, off, length int64) (io.ReadCloser, error) {
	l.mu.Lock()
	l.calls = append(l.calls, call{off, length})
	l.mu.Unlock()
	return l.inner.Fetch(ctx, f, off, length)
}

func (l *loggingContent) report(t *testing.T, label string, fileSize int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var requested int64
	for _, c := range l.calls {
		requested += c.length
	}
	t.Logf("%s: %d requests, %d KiB requested (file is %d KiB)", label, len(l.calls), requested/1024, fileSize/1024)
	for i, c := range l.calls {
		if i >= 12 {
			t.Logf("  ... and %d more", len(l.calls)-i)
			break
		}
		t.Logf("  request %d: offset %d (%.1f%%), length %d KiB", i, c.off,
			100*float64(c.off)/float64(fileSize), c.length/1024)
	}
}
