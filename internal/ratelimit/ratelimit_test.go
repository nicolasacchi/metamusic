package ratelimit

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)} }

func TestRateLimit_AgentBucket(t *testing.T) {
	c := newClock()
	l := New(Default, c.now)
	for i := 0; i < 60; i++ {
		if ok, _, _ := l.Allow("agent:a", Agent); !ok {
			t.Fatalf("call %d refused", i+1)
		}
	}
	ok, retry, b := l.Allow("agent:a", Agent)
	if ok || retry != time.Second || b != Agent {
		t.Fatalf("61st: ok=%v retry=%v bucket=%q", ok, retry, b)
	}
	if ok, _, _ := l.Allow("agent:other", Agent); !ok {
		t.Fatal("other principal must have its own bucket")
	}
	c.advance(time.Second)
	if ok, _, _ := l.Allow("agent:a", Agent); !ok {
		t.Fatal("one call allowed after 1 s")
	}
	if ok, _, _ := l.Allow("agent:a", Agent); ok {
		t.Fatal("only one token refilled")
	}
}

func TestRateLimit_HeavyBucket(t *testing.T) {
	c := newClock()
	l := New(Default, c.now)
	for i := 0; i < 6; i++ {
		if ok, _, _ := l.Allow("agent:a", Agent, AgentHeavy); !ok {
			t.Fatalf("heavy call %d refused", i+1)
		}
	}
	ok, retry, b := l.Allow("agent:a", Agent, AgentHeavy)
	if ok || b != AgentHeavy || retry < time.Second {
		t.Fatalf("7th heavy: ok=%v retry=%v bucket=%q", ok, retry, b)
	}
	// The refusal must not have consumed a default token: 60 - 6 = 54 remain.
	for i := 0; i < 54; i++ {
		if ok, _, _ := l.Allow("agent:a", Agent); !ok {
			t.Fatalf("default call %d refused after heavy refusal", i+1)
		}
	}
	if ok, _, _ := l.Allow("agent:a", Agent); ok {
		t.Fatal("default bucket should now be empty")
	}
}

func TestRateLimit_HumanCeiling(t *testing.T) {
	c := newClock()
	l := New(Default, c.now)
	for i := 0; i < 200; i++ {
		if ok, _, _ := l.Allow("human:alice", Human); !ok {
			t.Fatalf("call %d refused", i+1)
		}
	}
	if ok, _, b := l.Allow("human:alice", Human); ok || b != Human {
		t.Fatalf("201st: ok=%v bucket=%q", ok, b)
	}
}

func TestRateLimit_RetryAfter(t *testing.T) {
	c := newClock()
	l := New(Config{Agent: {PerMinute: 1, Burst: 1}}, c.now) // one token per minute
	l.Allow("k", Agent)
	c.advance(10 * time.Second)
	ok, retry, _ := l.Allow("k", Agent)
	if ok || retry != 50*time.Second {
		t.Fatalf("retry = %v, want 50s", retry)
	}
	c.advance(49*time.Second + 500*time.Millisecond)
	ok, retry, _ = l.Allow("k", Agent)
	if ok || retry != time.Second {
		t.Fatalf("sub-second wait must round up to 1s, got ok=%v retry=%v", ok, retry)
	}
	if retry%time.Second != 0 {
		t.Fatal("retry not whole seconds")
	}
}

func TestRateLimit_PruneIdle(t *testing.T) {
	c := newClock()
	l := New(Default, c.now)
	l.Allow("agent:a", Agent)
	l.Allow("agent:b", Agent)
	if n := len(l.buckets); n != 2 {
		t.Fatalf("buckets = %d", n)
	}
	c.advance(11 * time.Minute)
	l.Allow("agent:c", Agent)
	if n := len(l.buckets); n != 1 {
		t.Fatalf("idle buckets not pruned: %d", n)
	}
}

func TestRateLimit_Scaled(t *testing.T) {
	c := newClock()
	l := New(Default.Scaled(100), c.now)
	for i := 0; i < 6000; i++ {
		if ok, _, _ := l.Allow("agent:a", Agent); !ok {
			t.Fatalf("call %d refused", i+1)
		}
	}
	if Default[Agent].Burst != 60 {
		t.Fatal("Scaled must not mutate the receiver")
	}
}
