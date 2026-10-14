package jobs

import "sync"

// Hub fans a "something changed" nudge out to waiters in this process (C75.8). Another process may run
// the runner, so waiters also re-check the database.
type Hub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// NewHub builds an empty hub.
func NewHub() *Hub { return &Hub{subs: map[chan struct{}]struct{}{}} }

// Subscribe returns a channel that receives a nudge on every Notify and a cancel func.
func (h *Hub) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// Notify nudges every subscriber without blocking.
func (h *Hub) Notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
