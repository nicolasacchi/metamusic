package ratelimit

import (
	"testing"
	"time"
)

func TestRateLimit_Peek(t *testing.T) {
	t.Parallel()
	c := newClock()
	l := New(Config{Agent: {60, 2}}, c.now)
	for i := 0; i < 5; i++ { // never consumes
		if ok, _ := l.Peek("k", Agent); !ok {
			t.Fatalf("peek %d refused", i)
		}
	}
	if ok, _, _ := l.Allow("k", Agent); !ok {
		t.Fatal("first allow")
	}
	if ok, _, _ := l.Allow("k", Agent); !ok {
		t.Fatal("second allow (peeks must not have consumed)")
	}
	ok, retry := l.Peek("k", Agent)
	if ok || retry <= 0 {
		t.Fatalf("empty bucket: ok=%v retry=%v", ok, retry)
	}
	if retry != time.Second {
		t.Fatalf("retry = %v", retry)
	}
	c.advance(time.Second)
	if ok, _ := l.Peek("k", Agent); !ok {
		t.Fatal("refilled")
	}
	if ok, _ := l.Peek("k", "nope"); !ok {
		t.Fatal("unknown class must be ok")
	}
	if ok, retry := l.Peek("other", Agent); !ok || retry != 0 {
		t.Fatalf("fresh key: %v %v", ok, retry)
	}
}

func TestRateLimit_M4aClassesScaled(t *testing.T) {
	t.Parallel()
	want := map[Class]Limit{Subsonic: {1200, 300}, SubsonicAuthFail: {10, 5}, SubsonicAuthFailGlobal: {60, 20}}
	if Subsonic != "subsonic" || SubsonicAuthFail != "subsonic_auth_fail" || SubsonicAuthFailGlobal != "subsonic_auth_fail_global" {
		t.Fatal("class names")
	}
	for c, l := range want {
		if Default[c] != l {
			t.Errorf("Default[%s] = %v, want %v", c, Default[c], l)
		}
		if s := Default.Scaled(10)[c]; s.PerMinute != l.PerMinute*10 || s.Burst != l.Burst*10 {
			t.Errorf("Scaled(10)[%s] = %v", c, s)
		}
	}
	// the burst of subsonic_auth_fail is 5 per key
	cl := newClock()
	l := New(Default, cl.now)
	for i := 0; i < 5; i++ {
		if ok, _, _ := l.Allow("1.2.3.4", SubsonicAuthFail); !ok {
			t.Fatalf("attempt %d", i)
		}
	}
	if ok, _, b := l.Allow("1.2.3.4", SubsonicAuthFail); ok || b != SubsonicAuthFail {
		t.Fatalf("6th: %v %v", ok, b)
	}
}
