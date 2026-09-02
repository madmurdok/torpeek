package web

import "sync"

// hub fans every run's events out to every connected browser and remembers
// them so a page that connects late, or reloads, sees the runs the server
// holds rather than only what happens next.
//
// It is a transport, not a second place where run logic lives: it copies the
// bytes the server already encoded and never decides anything about a run
// (ARCHITECTURE.md - clients only read events).
type hub struct {
	mu sync.Mutex

	// head opens every replay. It is the one record that belongs to no run:
	// it tells a page that what follows is the whole history the server has,
	// rather than more of what the page already had.
	head record

	// runs is one history per run. Keeping them apart rather than in one
	// list is what lets a second run start without erasing the first, lets a
	// finished run be dropped whole when the registry trims it, and keeps
	// each run's progress heartbeats collapsing onto its own previous one
	// instead of onto another run's - two runs both start at file 0.
	runs map[string]*runHistory
	// order is the run ids in the order their histories opened, so a replay
	// is deterministic: every run's own events in order, runs in start order.
	order []string

	clients map[*client]struct{}
	closed  bool
}

// runHistory is one run's replay.
type runHistory struct {
	records []record
	// progressAt remembers where each of this run's files put its last
	// heartbeat, so a new one replaces it in place. Ordering is preserved and
	// a ten minute run does not accumulate hundreds of stale ticks to replay.
	progressAt map[int]int
}

// record is one message, already encoded. Encoding once and broadcasting the
// same bytes keeps a fan-out from re-marshalling per client.
type record struct {
	data []byte
	// progress marks a heartbeat, which collapses onto the previous one for
	// the same file of the same run rather than accumulating.
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

func newHub(head record) *hub {
	return &hub{
		head:    head,
		runs:    make(map[string]*runHistory),
		clients: make(map[*client]struct{}),
	}
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

	backlog := [][]byte{h.head.data}
	for _, id := range h.order {
		for _, rec := range h.runs[id].records {
			backlog = append(backlog, rec.data)
		}
	}
	return c, backlog
}

// remove ends one subscription.
func (h *hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropLocked(c)
}

// begin opens one run's history with its first message, usually the run's
// state. Connected clients are told; they do not have to reconnect to learn
// about a run that has just been accepted.
func (h *hub) begin(run string, rec record) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}
	if _, ok := h.runs[run]; !ok {
		h.runs[run] = &runHistory{progressAt: make(map[int]int)}
		h.order = append(h.order, run)
	}
	h.appendLocked(run, rec)
	h.broadcastLocked(rec)
}

// publish records a message against its run and sends it to every connected
// client.
func (h *hub) publish(run string, rec record) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}
	// A run whose history has been dropped is one the registry has already
	// forgotten; its late events still reach whoever is watching, but there
	// is nothing to replay them from later.
	h.appendLocked(run, rec)
	h.broadcastLocked(rec)
}

// drop forgets one run's history. The registry calls it when a finished run
// falls out of what memory keeps.
func (h *hub) drop(run string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.runs[run]; !ok {
		return
	}
	delete(h.runs, run)
	for i, id := range h.order {
		if id == run {
			h.order = append(h.order[:i], h.order[i+1:]...)
			break
		}
	}
}

func (h *hub) appendLocked(run string, rec record) {
	history, ok := h.runs[run]
	if !ok {
		return
	}
	if at, ok := history.progressAt[rec.file]; rec.progress && ok {
		history.records[at] = rec
		return
	}
	history.records = append(history.records, rec)
	if rec.progress {
		history.progressAt[rec.file] = len(history.records) - 1
	}
}

func (h *hub) broadcastLocked(rec record) {
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
