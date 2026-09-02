package bridge

import (
	"bytes"
	"context"
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
