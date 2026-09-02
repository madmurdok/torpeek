package web

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// fileSet is what the UI is allowed to read from disk.
//
// The core announces absolute paths of the frames, sheets and manifests it
// produced (internal/output owns where those live). Rather than mapping a URL
// back onto that layout - a second, drifting notion of where results are, and
// a path-traversal question to get wrong - the server only ever serves a path
// the event stream itself named. Anything not announced does not exist here.
type fileSet struct {
	mu     sync.RWMutex
	byID   map[string]string
	prefix string
}

func newFileSet() *fileSet {
	// Relative, so the URL works whether the UI sits at the site root or under
	// a base path such as /torpeek (REQUIREMENTS.md section 3.3).
	return &fileSet{byID: make(map[string]string), prefix: "files/"}
}

// publish registers a path and returns the relative URL it is served at.
// Publishing the same path twice yields the same URL.
func (f *fileSet) publish(path string) string {
	if path == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(path))
	id := hex.EncodeToString(sum[:16])

	f.mu.Lock()
	f.byID[id] = path
	f.mu.Unlock()

	return f.prefix + id
}

// lookup resolves an id back to the path it was published for.
func (f *fileSet) lookup(id string) (string, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	path, ok := f.byID[id]
	return path, ok
}
