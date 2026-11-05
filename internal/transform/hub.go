package transform

import "sync"

// hub is the in-process broadcast of row changes (C210.7): every change of a transform row is announced here, and Wait wakes on
// it. A subscriber holds a channel of capacity one, so a notification that arrives while it is busy is never lost and never
// blocks the sender.
type hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{} // transform id -> subscribers
}

func newHub() *hub { return &hub{subs: map[string]map[chan struct{}]struct{}{}} }

// subscribe returns a channel that receives a value after each change of id, and the function that ends the subscription.
func (h *hub) subscribe(id string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[id] == nil {
		h.subs[id] = map[chan struct{}]struct{}{}
	}
	h.subs[id][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[id], ch)
		if len(h.subs[id]) == 0 {
			delete(h.subs, id)
		}
		h.mu.Unlock()
	}
}

// notify announces a change of id; it never blocks.
func (h *hub) notify(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Broadcast announces that the row of transform id changed (state, phase, progress, cancel intent). The runner calls it after
// every write of a transform row; Wait is the reader.
func (s *Service) Broadcast(id string) { s.hub.notify(id) }
