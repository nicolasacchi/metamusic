// Package ratelimit holds the per-principal token buckets of the request gate (C29).
package ratelimit

import (
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Class names a bucket family.
type Class string

const (
	Agent      Class = "agent"
	AgentHeavy Class = "agent_heavy"
	Human      Class = "human"
	HumanHeavy Class = "human_heavy"
	Pair       Class = "pair"

	// M4a (C102): the OpenSubsonic bridge.
	Subsonic               Class = "subsonic"
	SubsonicAuthFail       Class = "subsonic_auth_fail"
	SubsonicAuthFailGlobal Class = "subsonic_auth_fail_global"
)

// Limit is one bucket's refill rate and burst.
type Limit struct {
	PerMinute float64
	Burst     int
}

// Config maps a class to its limit.
type Config map[Class]Limit

// Default is the production configuration.
var Default = Config{
	Agent:      {60, 60},
	AgentHeavy: {6, 6},
	Human:      {600, 200},
	HumanHeavy: {12, 6},
	Pair:       {10, 5},

	Subsonic:               {1200, 300},
	SubsonicAuthFail:       {10, 5},
	SubsonicAuthFailGlobal: {60, 20},
}

// Scaled returns a copy with every PerMinute and Burst multiplied by n.
func (c Config) Scaled(n int) Config {
	if n < 1 {
		n = 1
	}
	out := make(Config, len(c))
	for k, l := range c {
		out[k] = Limit{PerMinute: l.PerMinute * float64(n), Burst: l.Burst * n}
	}
	return out
}

const (
	idleAfter  = 10 * time.Minute
	pruneEvery = time.Minute
)

type bucketKey struct {
	class Class
	key   string
}

type bucket struct {
	lim  *rate.Limiter
	last time.Time
}

// Limiter is a set of lazily created token buckets keyed by (class, key).
// It only uses rate.Limiter's explicit-time methods, so a fake clock drives it.
type Limiter struct {
	cfg Config
	now func() time.Time

	mu        sync.Mutex
	buckets   map[bucketKey]*bucket
	lastPrune time.Time
}

// New builds a Limiter. A nil now means time.Now.
func New(cfg Config, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{cfg: cfg, now: now, buckets: map[bucketKey]*bucket{}}
}

// Allow reserves one token on every class. When any class cannot serve the
// call immediately, every reservation is cancelled and the longest delay
// (whole seconds, at least one) and its class are reported.
func (l *Limiter) Allow(key string, classes ...Class) (ok bool, retryAfter time.Duration, bucketClass Class) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)

	var held []*rate.Reservation
	var worst time.Duration
	var worstClass Class
	for _, c := range classes {
		lim, known := l.cfg[c]
		if !known {
			continue
		}
		b := l.bucketLocked(c, key, lim)
		b.last = now
		r := b.lim.ReserveN(now, 1)
		if !r.OK() {
			// Burst of zero: never allowed.
			worst, worstClass = math.MaxInt64, c
			continue
		}
		if d := r.DelayFrom(now); d > 0 {
			r.CancelAt(now)
			if d > worst {
				worst, worstClass = d, c
			}
			continue
		}
		held = append(held, r)
	}
	if worstClass == "" {
		return true, 0, ""
	}
	for _, r := range held {
		r.CancelAt(now)
	}
	if worst == math.MaxInt64 {
		return false, time.Minute, worstClass
	}
	secs := (worst + time.Second - 1) / time.Second
	if secs < 1 {
		secs = 1
	}
	return false, secs * time.Second, worstClass
}

// Peek reports whether a token is available for (class, key) without consuming one. A class missing
// from the configuration is always ok (like Allow). When empty, retryAfter is the whole-second wait.
func (l *Limiter) Peek(key string, class Class) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, known := l.cfg[class]
	if !known {
		return true, 0
	}
	now := l.now()
	b := l.bucketLocked(class, key, lim)
	b.last = now
	if b.lim.TokensAt(now) >= 1 {
		return true, 0
	}
	if lim.Burst < 1 || lim.PerMinute <= 0 {
		return false, time.Minute
	}
	need := 1 - b.lim.TokensAt(now)
	d := time.Duration(need / (lim.PerMinute / 60) * float64(time.Second))
	secs := (d + time.Second - 1) / time.Second
	if secs < 1 {
		secs = 1
	}
	return false, secs * time.Second
}

func (l *Limiter) bucketLocked(c Class, key string, lim Limit) *bucket {
	k := bucketKey{c, key}
	if b, ok := l.buckets[k]; ok {
		return b
	}
	b := &bucket{lim: rate.NewLimiter(rate.Limit(lim.PerMinute/60), lim.Burst)}
	l.buckets[k] = b
	return b
}

// pruneLocked drops buckets idle for longer than idleAfter. A dropped bucket
// would have refilled completely by then in every configured class.
func (l *Limiter) pruneLocked(now time.Time) {
	if now.Sub(l.lastPrune) < pruneEvery {
		return
	}
	l.lastPrune = now
	for k, b := range l.buckets {
		if now.Sub(b.last) > idleAfter {
			delete(l.buckets, k)
		}
	}
}
