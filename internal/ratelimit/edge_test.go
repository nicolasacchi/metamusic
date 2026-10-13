package ratelimit

import (
	"testing"
	"time"
)

func TestScaled_FloorsAtOne(t *testing.T) {
	got := Config{Agent: {PerMinute: 60, Burst: 60}}.Scaled(0)
	if got[Agent].Burst != 60 || got[Agent].PerMinute != 60 {
		t.Fatalf("Scaled(0) must behave as 1: %+v", got[Agent])
	}
	if got := (Config{Agent: {PerMinute: 6, Burst: 2}}).Scaled(100); got[Agent].Burst != 200 || got[Agent].PerMinute != 600 {
		t.Fatalf("Scaled(100): %+v", got[Agent])
	}
}

func TestNew_NilClockUsesWallTime(t *testing.T) {
	l := New(Default, nil)
	if ok, _, _ := l.Allow("k", Human); !ok {
		t.Fatal("a fresh bucket must allow")
	}
}

func TestAllow_UnknownClassIgnoredAndZeroBurstRefused(t *testing.T) {
	c := newClock()
	l := New(Config{Agent: {PerMinute: 60, Burst: 1}, Pair: {PerMinute: 1, Burst: 0}}, c.now)
	if ok, _, _ := l.Allow("k", "no-such-class", Agent); !ok {
		t.Fatal("unknown classes are skipped")
	}
	ok, retry, class := l.Allow("k2", Agent, Pair)
	if ok || class != Pair || retry != time.Minute {
		t.Fatalf("zero burst: ok=%v retry=%v class=%q", ok, retry, class)
	}
	// the refusal released the reservation made on the healthy class
	if ok, _, _ := l.Allow("k2", Agent); !ok {
		t.Fatal("the agent token reserved before the refusal must have been returned")
	}
}
