package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"testing"
	"time"
)

func TestParseRange(t *testing.T) {
	const size = 1000

	cases := []struct {
		header  string
		want    byteRange
		wantErr bool
	}{
		{header: "bytes=0-99", want: byteRange{Start: 0, Length: 100}},
		{header: "bytes=500-", want: byteRange{Start: 500, Length: 500}},
		{header: "bytes=-100", want: byteRange{Start: 900, Length: 100}}, // how ffmpeg reaches a trailing index
		{header: "bytes=-5000", want: byteRange{Start: 0, Length: 1000}}, // suffix larger than the file
		{header: "bytes=900-5000", want: byteRange{Start: 900, Length: 100}},
		{header: "bytes=999-999", want: byteRange{Start: 999, Length: 1}},
		{header: "bytes=1000-", wantErr: true}, // starts past the end
		{header: "bytes=50-10", wantErr: true},
		{header: "bytes=0-10,20-30", wantErr: true},
		{header: "items=0-10", wantErr: true},
		{header: "bytes=abc", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			got, err := parseRange(tc.header, size)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseRange(%q) = %+v, want error", tc.header, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRange(%q): %v", tc.header, err)
			}
			if got != tc.want {
				t.Errorf("parseRange(%q) = %+v, want %+v", tc.header, got, tc.want)
			}
		})
	}
}

// memoryContent is a Content backed by a byte slice, so the server can be
// exercised without a torrent client.
type memoryContent struct {
	data  []byte
	delay time.Duration // simulates pieces that never arrive
}

func (m *memoryContent) Length(int) (int64, error) { return int64(len(m.data)), nil }
func (m *memoryContent) Name(int) string           { return "movie.mkv" }

func (m *memoryContent) Fetch(ctx context.Context, _ int, off, length int64) (io.ReadCloser, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return io.NopCloser(bytes.NewReader(m.data[off : off+length])), nil
}

func startTestBridge(t *testing.T, c Content, timeout time.Duration) *Bridge {
	t.Helper()

	cfg := DefaultConfig()
	if timeout > 0 {
		cfg.RequestTimeout = timeout
	}
	b, err := Start(cfg)
	if err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func TestBridgeServesRanges(t *testing.T) {
	data := make([]byte, 1<<20)
	rand.New(rand.NewSource(7)).Read(data)
	content := &memoryContent{data: data}

	b := startTestBridge(t, content, 0)
	url, withdraw, err := b.Publish(content, 0)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	t.Run("middle range", func(t *testing.T) {
		const off, length = 500_000, 4096
		body, resp := get(t, url, fmt.Sprintf("bytes=%d-%d", off, off+length-1))

		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", resp.StatusCode)
		}
		if got, want := resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-%d/%d", off, off+length-1, len(data)); got != want {
			t.Errorf("Content-Range = %q, want %q", got, want)
		}
		if !bytes.Equal(body, data[off:off+length]) {
			t.Errorf("body does not match the source bytes")
		}
	})

	t.Run("suffix range reaches the tail", func(t *testing.T) {
		body, resp := get(t, url, "bytes=-2048")
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", resp.StatusCode)
		}
		if !bytes.Equal(body, data[len(data)-2048:]) {
			t.Error("suffix range did not return the final bytes")
		}
	})

	t.Run("whole file without a range header", func(t *testing.T) {
		body, resp := get(t, url, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if len(body) != len(data) {
			t.Errorf("got %d bytes, want %d", len(body), len(data))
		}
	})

	t.Run("head reports size and range support", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodHead, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("head: %v", err)
		}
		defer resp.Body.Close()

		if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
			t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
		}
		if resp.ContentLength != int64(len(data)) {
			t.Errorf("Content-Length = %d, want %d", resp.ContentLength, len(data))
		}
	})

	t.Run("unsatisfiable range", func(t *testing.T) {
		_, resp := get(t, url, "bytes=99999999-")
		if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			t.Errorf("status = %d, want 416", resp.StatusCode)
		}
	})

	t.Run("withdrawn publication is gone", func(t *testing.T) {
		withdraw()
		_, resp := get(t, url, "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404 after withdrawal", resp.StatusCode)
		}
	})
}

func TestBridgeUnknownTokenIsNotFound(t *testing.T) {
	content := &memoryContent{data: make([]byte, 16)}
	b := startTestBridge(t, content, 0)

	_, resp := get(t, b.URL()+"/deadbeef/movie.mkv", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for an unknown token", resp.StatusCode)
	}
}

// TestBridgeRequestTimesOut is the half of the acceptance criteria that keeps
// an external process from hanging the run: pieces that never arrive must end
// the request, not block it forever.
func TestBridgeRequestTimesOut(t *testing.T) {
	content := &memoryContent{data: make([]byte, 4096), delay: time.Hour}
	b := startTestBridge(t, content, 300*time.Millisecond)

	url, _, err := b.Publish(content, 0)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.Get(url)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("request did not return - the bridge can be hung by a slow fetch")
	}
}

// TestBridgeRecordsAStalledRead is the bridge's half of TOR-45: a read that
// ran out of time has to be visible as such, because nothing downstream can
// tell it from a defective file. The client sees headers, then a body that
// stops early - which is exactly what a truncated container looks like.
func TestBridgeRecordsAStalledRead(t *testing.T) {
	content := &memoryContent{data: make([]byte, 4096), delay: time.Hour}
	b := startTestBridge(t, content, 300*time.Millisecond)

	url, _, err := b.Publish(content, 0)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	if n, last := b.Stalls(url); n != 0 || last != nil {
		t.Fatalf("Stalls before any request = (%d, %v), want (0, nil)", n, last)
	}

	// Not the get helper: the whole point is that the response is unfinished,
	// so reading it fails with an unexpected EOF - which is precisely what
	// ffprobe sees and mistakes for a defective file.
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=100-1099")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	read, readErr := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	t.Logf("client saw Content-Length %d, read %d bytes, then: %v", resp.ContentLength, read, readErr)

	n, last := b.Stalls(url)
	if n != 1 {
		t.Fatalf("Stalls after a timed-out read = %d, want 1", n)
	}
	if !errors.Is(last, ErrStalled) {
		t.Fatalf("recorded error %v does not match ErrStalled", last)
	}

	var stall *StallError
	if !errors.As(last, &stall) {
		t.Fatalf("recorded error %v is not a *StallError", last)
	}
	if stall.Off != 100 || stall.Length != 1000 || stall.Delivered != 0 {
		t.Errorf("stall = %+v, want the 1000 bytes asked for at offset 100, none delivered", stall)
	}
	if stall.Elapsed < 300*time.Millisecond {
		t.Errorf("stall elapsed %s, want at least the 300ms timeout", stall.Elapsed)
	}
	t.Logf("recorded: %v", last)
}

// TestBridgeDoesNotCountANormalReadAsAStall is the control for the test above.
// Without it, "a stall was recorded" would prove nothing: a counter that
// increments on every request would pass the test above and mislabel every
// healthy run.
//
// The second case is the one that matters. ffmpeg asks for "this offset to the
// end of the file" and hangs up after a few hundred kilobytes, leaving the
// response unfinished on purpose. That is the single most common request shape
// in a run, and calling it a stall would put read_stalled on every file.
func TestBridgeDoesNotCountANormalReadAsAStall(t *testing.T) {
	// Larger than any socket buffer, so the copy is still going when the
	// client walks away.
	content := &memoryContent{data: make([]byte, 8<<20)}
	b := startTestBridge(t, content, 30*time.Second)

	url, _, err := b.Publish(content, 0)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	t.Run("a served range", func(t *testing.T) {
		get(t, url, "bytes=0-4095")
		if n, last := b.Stalls(url); n != 0 {
			t.Errorf("Stalls after a healthy read = (%d, %v), want 0", n, last)
		}
	})

	t.Run("a client that hangs up mid-body", func(t *testing.T) {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if _, err := io.ReadFull(resp.Body, make([]byte, 4096)); err != nil {
			t.Fatalf("read the first 4 KiB: %v", err)
		}
		resp.Body.Close()

		// The handler notices on its next write, not instantly; give it room
		// to record a stall if it were going to.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if n, last := b.Stalls(url); n != 0 {
				t.Fatalf("a client hanging up was recorded as a stall: (%d, %v)", n, last)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}

func get(t *testing.T, url, rangeHeader string) ([]byte, *http.Response) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body, resp
}
