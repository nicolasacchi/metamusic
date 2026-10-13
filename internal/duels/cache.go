package duels

import (
	"container/list"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/ladder"
)

// fitKey identifies a fit: the scope plus the state of the human verdicts (D-M2b-7).
type fitKey struct {
	scope, root string
	sighted     bool
	n, maxRowid int64
}

// fitEntry is what the cache keeps: the fit and its item order, nothing else. Titles and
// every other mutable library value, and the critic/sighted exclusion counts, are read on
// each request (D-M2b-7).
type fitEntry struct {
	ids []string
	fit ladder.Fit
}

// fitCache is a small LRU of fitEntry keyed by fitKey.
type fitCache struct {
	mu  sync.Mutex
	cap int
	ll  *list.List
	m   map[fitKey]*list.Element
}

type cacheItem struct {
	k fitKey
	v *fitEntry
}

func newFitCache(capacity int) *fitCache {
	return &fitCache{cap: capacity, ll: list.New(), m: map[fitKey]*list.Element{}}
}

func (c *fitCache) get(k fitKey) (*fitEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(e)
	return e.Value.(*cacheItem).v, true
}

func (c *fitCache) put(k fitKey, v *fitEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[k]; ok {
		e.Value.(*cacheItem).v = v
		c.ll.MoveToFront(e)
		return
	}
	c.m[k] = c.ll.PushFront(&cacheItem{k, v})
	for c.ll.Len() > c.cap {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.m, last.Value.(*cacheItem).k)
	}
}

func (c *fitCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
