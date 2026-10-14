package jobs

import (
	"context"
	"sync"
	"uuid"
)

// pool is the bounded set of ingest goroutines (C75.3). A job is in the pool at most once.
type pool struct {
	mu      sync.Mutex
	size    int
	running map[string]struct{}
	wg      sync.WaitGroup
	idle    chan struct{} // closed when running is empty; replaced on the next start
}

func newPool(size int) *pool {
	p := &pool{size: size, running: map[string]struct{}{}, idle: make(chan struct{})}
	close(p.idle)
	return p
}

// start runs fn for id unless it is already running or the pool is full; it reports whether fn was started.
func (p *pool) start(id string, fn func()) bool {
	p.mu.Lock()
	if _, ok := p.running[id]; ok || len(p.running) >= p.size {
		p.mu.Unlock()
		return false
	}
	if len(p.running) == 0 {
		p.idle = make(chan struct{})
	}
	p.running[id] = struct{}{}
	p.wg.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.wg.Done()
		defer func() {
			p.mu.Lock()
			delete(p.running, id)
			if len(p.running) == 0 {
				close(p.idle)
			}
			p.mu.Unlock()
		}()
		fn()
	}()
	return true
}

func (p *pool) has(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.running[id]
	return ok
}

// drain waits until nothing runs, or ctx ends.
func (p *pool) drain(ctx context.Context) error {
	p.mu.Lock()
	idle := p.idle
	p.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// newUUID is the fallback id source; production wiring injects the library's UUIDv7 generator.
func newUUID() string { return uuid.NewV7().String() }
