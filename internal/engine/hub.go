package engine

import "sync"

// Hub fans events out to subscribers. A subscriber that falls behind is not
// waited for: the hub drops it and closes its channel. The subscriber then
// knows that it missed events and must load the state again.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan Event]struct{})}
}

// Subscribe returns a channel of events and a function that stops it. The
// channel closes when the subscription stops or when the hub drops it.
func (h *Hub) Subscribe(buffer int) (<-chan Event, func()) {
	ch := make(chan Event, buffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.dropLocked(ch)
	}
}

// Publish sends e to every subscriber without blocking. A subscriber whose
// buffer is full has missed an event, so the hub drops it.
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
			h.dropLocked(ch)
		}
	}
}

// dropLocked removes a subscriber and closes its channel, once. The caller
// holds h.mu.
func (h *Hub) dropLocked(ch chan Event) {
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
}
