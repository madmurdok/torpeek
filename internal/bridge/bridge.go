// Package bridge publishes a torrent's file as an ordinary HTTP resource on
// loopback, so ffmpeg and ffprobe can read it with Range requests while the
// data is still being fetched from the swarm.
//
// It is the single point where an external process can stall the run, so every
// request carries its own timeout and a context cancelled with the run.
//
// Requirements: sections 2.4 and 4.1.
package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Content is what the bridge can publish: a sized, seekable resource fetched
// on demand. Defined here rather than taken from the swarm package so the
// server can be tested without a torrent client.
type Content interface {
	// Length is the size of the file in bytes.
	Length(file int) (int64, error)

	// Name is used only to give the URL a plausible extension - ffmpeg will
	// happily probe without it, but a wrong guess costs a round trip.
	Name(file int) string

	// Fetch returns a reader over exactly [off, off+length) of the file. The
	// caller closes it, which also releases whatever claim it holds.
	Fetch(ctx context.Context, file int, off, length int64) (io.ReadCloser, error)
}

// Config configures the bridge server.
type Config struct {
	// Addr is the listen address. The port must be explicit on a managed host
	// that allocates a range (section 4.1); "127.0.0.1:0" is fine locally.
	Addr string

	// RequestTimeout bounds a single request. Without it, an external process
	// waiting on pieces nobody has would hang until the run is cancelled.
	RequestTimeout time.Duration
}

// DefaultConfig binds to a free loopback port.
func DefaultConfig() Config {
	return Config{Addr: "127.0.0.1:0", RequestTimeout: 60 * time.Second}
}

// Bridge serves published files over HTTP for the lifetime of a run.
type Bridge struct {
	cfg      Config
	listener net.Listener
	server   *http.Server
	baseURL  string

	mu        sync.RWMutex
	published map[string]publication
}

type publication struct {
	content Content
	file    int
}

// Start begins listening. The returned bridge must be closed.
func Start(cfg Config) (*Bridge, error) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 60 * time.Second
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("bridge listen on %s: %w", cfg.Addr, err)
	}

	b := &Bridge{
		cfg:       cfg,
		listener:  ln,
		baseURL:   "http://" + ln.Addr().String(),
		published: make(map[string]publication),
	}
	b.server = &http.Server{Handler: http.HandlerFunc(b.serve)}

	go func() {
		// http.ErrServerClosed is the normal shutdown path.
		_ = b.server.Serve(ln)
	}()

	return b, nil
}

// URL is the bridge's base address.
func (b *Bridge) URL() string { return b.baseURL }

// Publish exposes one file and returns its URL plus a function that withdraws
// it. Each publication gets an unguessable path: the bridge listens on
// loopback, but every other process on the machine can reach loopback too.
func (b *Bridge) Publish(c Content, file int) (string, func(), error) {
	if _, err := c.Length(file); err != nil {
		return "", nil, fmt.Errorf("publish file %d: %w", file, err)
	}

	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, fmt.Errorf("generate publication token: %w", err)
	}
	token := hex.EncodeToString(raw[:])

	b.mu.Lock()
	b.published[token] = publication{content: c, file: file}
	b.mu.Unlock()

	// The trailing name is decoration for ffmpeg's format guessing; the token
	// alone identifies the publication.
	url := fmt.Sprintf("%s/%s/%s", b.baseURL, token, path.Base(c.Name(file)))

	return url, func() {
		b.mu.Lock()
		delete(b.published, token)
		b.mu.Unlock()
	}, nil
}

// Close stops the server.
func (b *Bridge) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := b.server.Shutdown(ctx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (b *Bridge) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")

	b.mu.RLock()
	pub, ok := b.published[token]
	b.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}

	size, err := pub.content.Length(pub.file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")

	span := byteRange{Start: 0, Length: size}
	partial := false

	if header := r.Header.Get("Range"); header != "" {
		span, err = parseRange(header, size)
		if errors.Is(err, errUnsatisfiable) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		partial = true
	}

	w.Header().Set("Content-Length", strconv.FormatInt(span.Length, 10))
	if partial {
		w.Header().Set("Content-Range", span.contentRange(size))
		w.WriteHeader(http.StatusPartialContent)
	}

	if r.Method == http.MethodHead || span.Length == 0 {
		return
	}

	// Every request gets its own deadline: an external process must never be
	// able to hold the run open waiting for pieces nobody is offering.
	ctx, cancel := context.WithTimeout(r.Context(), b.cfg.RequestTimeout)
	defer cancel()

	body, err := pub.content.Fetch(ctx, pub.file, span.Start, span.Length)
	if err != nil {
		// Headers are already written, so the only honest signal left is to
		// cut the body short; the client sees a truncated response.
		return
	}
	defer body.Close()

	_, _ = io.Copy(w, body)
}
