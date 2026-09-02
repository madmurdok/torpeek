package web

import "sync"

// hub fans one run's events out to every connected browser and remembers them
// so a page that connects late, or reloads, sees the whole run rather than
// only what happens next.
//
// It is a transport, not a second place where run logic lives: it copies the
// bytes the server already encoded and never decides anything about the run
// (ARCHITECTURE.md - clients only read events).
type hub struct {
	mu sync.Mutex

	// history is replayed to a client on connect, in order.
	history []record
	// progressAt remembers where each file's last heartbeat sits in history,
	// so a new one replaces it in place. Ordering is preserved and a ten
	// minute run does not accumulate hundreds of stale ticks to replay.
	progressAt map[int]int

	clients map[*client]struct{}
	closed  bool
}

// record is one message, already encoded. Encoding once and broadcasting the
// same bytes keeps a fan-out from re-marshalling per client.
type record struct {
	data []byte
	// progress marks a heartbeat, which collapses onto the previous one for
	// the same file rather than accumulating.
	progress bool
	file     int
}

// client is one WebSocket connection's outbound queue.
type client struct {
	out chan []byte
}

// clientBuffer is deep enough that a browser on loopback never reaches it. If
// one does, the connection is dropped rather than events silently skipped:
// the page reconnects and replays the history, which is self-healing, whereas
// a dropped frame_ready would leave a hole in the grid forever.
const clientBuffer = 512

func newHub() *hub {
	return &hub{progressAt: make(map[int]int), clients: make(map[*client]struct{})}
}

// subscribe registers a connection and hands back everything published so far.
// Both happen under one lock, so no event can slip between the snapshot and
// the subscription.
func (h *hub) subscribe() (*client, [][]byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c := &client{out: make(chan []byte, clientBuffer)}
	if h.closed {
		close(c.out)
		return c, nil
	}
	h.clients[c] = struct{}{}

	backlog := make([][]byte, 0, len(h.history))
	for _, rec := range h.history {
		backlog = append(backlog, rec.data)
	}
	return c, backlog
}

// remove ends one subscription.
func (h *hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropLocked(c)
}

// publish records a message and sends it to every connected client.
func (h *hub) publish(rec record) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	if at, ok := h.progressAt[rec.file]; rec.progress && ok {
		h.history[at] = rec
	} else {
		h.history = append(h.history, rec)
		if rec.progress {
			h.progressAt[rec.file] = len(h.history) - 1
		}
	}

	for c := range h.clients {
		select {
		case c.out <- rec.data:
		default:
			h.dropLocked(c)
		}
	}
}

// reset starts a new run's history from one message, usually the run's state.
// Connected clients are told; they do not have to reconnect to follow it.
func (h *hub) reset(rec record) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	h.history = []record{rec}
	h.progressAt = make(map[int]int)

	for c := range h.clients {
		select {
		case c.out <- rec.data:
		default:
			h.dropLocked(c)
		}
	}
}

// close ends every subscription.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}
	h.closed = true
	for c := range h.clients {
		h.dropLocked(c)
	}
}

// dropLocked is the single place a client's queue is closed, so the write
// pump's channel is never closed twice.
func (h *hub) dropLocked(c *client) {
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	close(c.out)
}
