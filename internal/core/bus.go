package core

import (
	"sync"
	"time"
)

// Bus fans events out to every subscriber: the TUI, the web UI and an NDJSON
// writer can all watch the same run.
//
// Two rules shape it. A slow subscriber must never stall the run, so each has
// a buffer and ordinary events are dropped when it overflows - a display that
// misses a progress tick redraws a moment later. Terminal events are worth
// waiting a little for, since a client that misses Done waits forever, but
// only a little: an abandoned subscriber must not hold the run open.
type Bus struct {
	mu     sync.Mutex
	subs   map[int]*subscriber
	nextID int
	closed bool
	buffer int

	// terminalWait bounds how long a terminal event waits for a full buffer.
	terminalWait time.Duration
}

type subscriber struct {
	ch chan Event

	// cancelled is set by the unsubscribe function but acted on by the
	// publisher: closing a channel from the reader's side races with a send
	// in flight, so only the writing side ever closes it.
	cancelled bool
	dropped   int
}

// DefaultBuffer is deep enough to absorb a burst of frame events while a
// client is busy drawing.
const DefaultBuffer = 64

// NewBus returns a bus whose subscribers each buffer up to buffer events.
func NewBus(buffer int) *Bus {
	if buffer <= 0 {
		buffer = DefaultBuffer
	}
	return &Bus{
		subs:         make(map[int]*subscriber),
		buffer:       buffer,
		terminalWait: 2 * time.Second,
	}
}

// Subscribe returns a channel of events and a function to stop listening.
//
// The channel is closed once the bus closes, or shortly after the returned
// function is called - closing is done by the publisher, so cancelling does
// not race with an event already on its way.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}

	id := b.nextID
	b.nextID++
	b.subs[id] = &subscriber{ch: make(chan Event, b.buffer)}

	return b.subs[id].ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if sub, ok := b.subs[id]; ok {
			sub.cancelled = true
		}
	}
}

// Publish delivers an event to every live subscriber.
func (b *Bus) Publish(ev Event) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}

	// Retire anything cancelled since the last publish, then take a snapshot
	// so the sends below do not happen under the lock.
	for id, sub := range b.subs {
		if sub.cancelled {
			delete(b.subs, id)
			close(sub.ch)
		}
	}
	live := make([]*subscriber, 0, len(b.subs))
	for _, sub := range b.subs {
		live = append(live, sub)
	}
	terminalWait := b.terminalWait
	b.mu.Unlock()

	terminal := isTerminal(ev)
	for _, sub := range live {
		if !terminal {
			select {
			case sub.ch <- ev:
			default:
				b.countDrop(sub)
			}
			continue
		}

		timer := time.NewTimer(terminalWait)
		select {
		case sub.ch <- ev:
			timer.Stop()
		case <-timer.C:
			// The subscriber stopped reading without unsubscribing. The run
			// is over either way; closing the bus will still end its channel.
			b.countDrop(sub)
		}
	}
}

func (b *Bus) countDrop(sub *subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub.dropped++
}

// Dropped reports how many events were discarded across all live subscribers,
// so a client can say "the display fell behind" rather than showing a gap with
// no explanation.
func (b *Bus) Dropped() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	total := 0
	for _, sub := range b.subs {
		total += sub.dropped
	}
	return total
}

// Close ends every subscription. Publishing after Close is a no-op, so a
// late-arriving event cannot panic the run.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true
	for id, sub := range b.subs {
		delete(b.subs, id)
		close(sub.ch)
	}
}

func isTerminal(ev Event) bool {
	switch ev.(type) {
	case Done, Failed:
		return true
	default:
		return false
	}
}
