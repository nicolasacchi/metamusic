package subsonic

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
)

// spyLimiter and spyVerifier record the call order of C108.3.
type callLog struct {
	mu  sync.Mutex
	seq []string
}

func (l *callLog) add(s string) {
	l.mu.Lock()
	l.seq = append(l.seq, s)
	l.mu.Unlock()
}

func (l *callLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seq...)
}

type spyLimiter struct {
	log  *callLog
	next limiter
}

func (s spyLimiter) Peek(key string, c ratelimit.Class) (bool, time.Duration) {
	s.log.add("peek:" + string(c))
	return s.next.Peek(key, c)
}

func (s spyLimiter) Allow(key string, cs ...ratelimit.Class) (bool, time.Duration, ratelimit.Class) {
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = string(c)
	}
	s.log.add("allow:" + strings.Join(names, ","))
	return s.next.Allow(key, cs...)
}

type spyVerifier struct {
	log  *callLog
	next verifier
	seen []time.Duration
}

func (s *spyVerifier) VerifyAPIKey(ctx context.Context, key, c string) (auth.Principal, error) {
	s.log.add("verify:key")
	return s.next.VerifyAPIKey(ctx, key, c)
}

func (s *spyVerifier) VerifyToken(ctx context.Context, u, t, sl, c string, w time.Duration) (auth.Principal, error) {
	s.log.add("verify:token")
	s.seen = append(s.seen, w)
	return s.next.VerifyToken(ctx, u, t, sl, c, w)
}

func TestSubsonicAuth_Order(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	log := &callLog{}
	r.srv.lim = spyLimiter{log: log, next: r.lim}
	r.srv.creds = &spyVerifier{log: log, next: r.creds}
	r.srv.sleep = func(context.Context, time.Duration) error { log.add("sleep"); return nil }

	// success: peek the per-IP class, verify, then the per-principal class; the global class is never touched
	if w := r.get("ping", keyParams(r.rwKey), "198.51.100.7"); w.code(t) != 0 {
		t.Fatalf("ping: %s", w.Body)
	}
	want := []string{"peek:subsonic_auth_fail", "verify:key", "allow:subsonic"}
	if got := log.get(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("success order %v, want %v", got, want)
	}

	// failure: peek, verify, consume per-IP, global allow, sleep
	log.seq = nil
	if w := r.get("ping", keyParams("mms_"+strings.Repeat("b", 52)), "198.51.100.7"); w.code(t) != errBadAPIKey {
		t.Fatalf("bad key: %s", w.Body)
	}
	want = []string{"peek:subsonic_auth_fail", "verify:key", "allow:subsonic_auth_fail", "allow:subsonic_auth_fail_global", "sleep"}
	if got := log.get(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("failure order %v, want %v", got, want)
	}

	// no credentials work at all before the peek, and a missing credential is not a failure
	log.seq = nil
	q := keyParams("")
	q.Del("apiKey")
	if w := r.get("ping", q, "198.51.100.7"); w.code(t) != errMissingParam {
		t.Fatalf("no creds: %s", w.Body)
	}
	if got := log.get(); fmt.Sprint(got) != fmt.Sprint([]string{"peek:subsonic_auth_fail"}) {
		t.Errorf("missing-credentials order %v", got)
	}

	// getOpenSubsonicExtensions: nothing at all
	log.seq = nil
	if w := r.get("getOpenSubsonicExtensions", url.Values{"f": {"json"}}, "198.51.100.7"); w.code(t) != 0 {
		t.Fatalf("extensions: %s", w.Body)
	}
	if got := log.get(); len(got) != 0 {
		t.Errorf("extensions touched %v", got)
	}
}

func TestSubsonicAuth_Matrix(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	var ip string
	// a test-only write endpoint stands in for setRating (S3)
	r.srv.add("setRating", true, func(c *call) error { return nil })

	type tc struct {
		name string
		ep   string
		q    url.Values
		code int
	}
	good := tokenParams("rwclient", r.rwPW, "salt-good-1")
	conflict := keyParams(r.rwKey)
	conflict.Set("u", "rwclient")
	conflictP := keyParams(r.rwKey)
	conflictP.Set("p", "x")
	conflictT := keyParams(r.rwKey)
	conflictT.Set("t", "abc")
	conflictS := keyParams(r.rwKey)
	conflictS.Set("s", "abcdef")
	badToken := tokenParams("rwclient", "wrong-password", "salt-bad-0")
	unknownUser := tokenParams("nobody", r.rwPW, "salt-bad-1")
	userPass := url.Values{"u": {"rwclient"}, "p": {r.rwPW}, "c": {"app"}, "v": {"1.16.1"}, "f": {"json"}}
	enc := url.Values{"u": {"rwclient"}, "p": {"enc:" + fmt.Sprintf("%x", r.rwPW)}, "c": {"app"}, "v": {"1.16.1"}, "f": {"json"}}
	nothing := url.Values{"c": {"app"}, "v": {"1.16.1"}, "f": {"json"}}
	onlyU := url.Values{"u": {"rwclient"}, "c": {"app"}, "v": {"1.16.1"}, "f": {"json"}}

	for _, c := range []tc{
		{"token ok", "ping", good, 0},
		{"api key ok", "ping", keyParams(r.rwKey), 0},
		{"apiKey+u", "ping", conflict, 43},
		{"apiKey+p", "ping", conflictP, 43},
		{"apiKey+t", "ping", conflictT, 43},
		{"apiKey+s", "ping", conflictS, 43},
		{"bad key", "ping", keyParams("mms_" + strings.Repeat("c", 52)), 44},
		{"garbage key", "ping", keyParams("nope"), 44},
		{"empty key", "ping", keyParams(""), 44},
		{"bad token", "ping", badToken, 40},
		{"unknown user", "ping", unknownUser, 40},
		{"u+p", "ping", userPass, 42},
		{"u+enc:p", "ping", enc, 42},
		{"nothing", "ping", nothing, 10},
		{"only u", "ping", onlyU, 10},
		{"read-only client reads", "ping", tokenParams("roclient", r.roPW, "salt-ro-001"), 0},
		{"read-only client write", "setRating", tokenParams("roclient", r.roPW, "salt-ro-002"), 50},
		{"read-only key write", "setRating", keyParams(r.roKey), 50},
		{"read-write client write", "setRating", tokenParams("rwclient", r.rwPW, "salt-rw-002"), 0},
	} {
		// every case from its own address so earlier failures never rate-limit later ones
		ip = "198.51.100." + fmt.Sprint(30+len(c.name))
		if got := r.get(c.ep, c.q, ip).code(t); got != c.code {
			t.Errorf("%s: error %d, want %d", c.name, got, c.code)
		}
	}

	// a revoked client: 40 with a token, 44 with its key
	if _, err := r.creds.Revoke(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "alice"}, r.rw.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.get("ping", tokenParams("rwclient", r.rwPW, "salt-rev-01"), "198.51.100.90").code(t); got != 40 {
		t.Errorf("revoked token: %d", got)
	}
	if got := r.get("ping", keyParams(r.rwKey), "198.51.100.91").code(t); got != 44 {
		t.Errorf("revoked key: %d", got)
	}
}

func TestSubsonicAuth_WindowByEndpoint(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	for _, ep := range []string{"stream", "download", "getCoverArt", "getSong"} {
		r.srv.add(ep, false, func(c *call) error { return nil })
	}
	salt := "windowsalt-1"
	q := func() url.Values { return tokenParams("rwclient", r.rwPW, salt) }
	ip := "198.51.100.40"
	// first use on stream
	if got := r.get("stream", q(), ip).code(t); got != 0 {
		t.Fatalf("stream first use: %d", got)
	}
	r.clk.advance(11 * time.Minute)
	for _, ep := range []string{"stream", "download", "getCoverArt"} {
		if got := r.get(ep, q(), ip).code(t); got != 0 {
			t.Errorf("%s after 11 min: error %d", ep, got)
		}
	}
	w := r.get("getSong", q(), ip)
	if w.code(t) != 40 || !strings.Contains(w.Body.String(), "salt expired") {
		t.Errorf("getSong after 11 min: %s", w.Body)
	}
	r.clk.advance(13 * time.Hour)
	if got := r.get("stream", q(), "198.51.100.41").code(t); got != 40 {
		t.Errorf("stream after 12 h: %d", got)
	}
	// the handler passes SaltWindowFor(endpoint)
	spy := &spyVerifier{log: &callLog{}, next: r.creds}
	r.srv.creds = spy
	r.get("stream", tokenParams("rwclient", r.rwPW, "windowsalt-2"), "198.51.100.42")
	r.get("getSong", tokenParams("rwclient", r.rwPW, "windowsalt-3"), "198.51.100.42")
	if len(spy.seen) != 2 || spy.seen[0] != auth.SaltWindowMedia || spy.seen[1] != auth.SaltWindowAPI {
		t.Errorf("windows passed: %v", spy.seen)
	}
}

func failLimits() ratelimit.Config {
	return ratelimit.Config{
		ratelimit.Subsonic:               {PerMinute: 1e6, Burst: 1e6},
		ratelimit.SubsonicAuthFail:       {PerMinute: 10, Burst: 5},
		ratelimit.SubsonicAuthFailGlobal: {PerMinute: 60, Burst: 20},
	}
}

func TestSubsonicAuth_FailureRateLimit(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{limits: failLimits()})
	bad := func() url.Values { return keyParams("mms_" + strings.Repeat("d", 52)) }
	good := func() url.Values { return keyParams(r.rwKey) }

	// 5 failures from one address pass the burst
	for i := 0; i < 5; i++ {
		if w := r.get("ping", bad(), "198.51.100.50"); w.Code != 200 || w.code(t) != 44 {
			t.Fatalf("failure %d: %d %s", i, w.Code, w.Body)
		}
	}
	// the next request from that address is 429 with Retry-After, even with valid credentials
	w := r.get("ping", good(), "198.51.100.50")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || w.code(t) != 0 {
		t.Fatalf("blocked: %d retry-after=%q %s", w.Code, w.Header().Get("Retry-After"), w.Body)
	}
	if !strings.Contains(w.Body.String(), "too many failed attempts") {
		t.Errorf("blocked body: %s", w.Body)
	}
	// another address with valid credentials is fine
	if w := r.get("ping", good(), "198.51.100.51"); w.Code != 200 || w.doc(t)["status"] != "ok" {
		t.Errorf("other address: %d %s", w.Code, w.Body)
	}
	// after Retry-After the address works again
	r.clk.advance(7 * time.Second) // one token refills every 6 s
	if w := r.get("ping", good(), "198.51.100.50"); w.Code != 200 || w.doc(t)["status"] != "ok" {
		t.Errorf("after Retry-After: %d %s", w.Code, w.Body)
	}
}

func TestSubsonicAuth_GlobalFailureClass(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{limits: failLimits()})
	bad := func() url.Values { return keyParams("mms_" + strings.Repeat("e", 52)) }
	// 5 failures from 4 addresses = 20: the global burst is spent; each failure still slept
	for i := 0; i < 20; i++ {
		ip := fmt.Sprintf("198.51.100.%d", 60+i/5)
		if w := r.get("ping", bad(), ip); w.Code != 200 || w.code(t) != 44 {
			t.Fatalf("failure %d from %s: %d %s", i, ip, w.Code, w.Body)
		}
	}
	if n := r.sleeps.count(); n != 20 {
		t.Fatalf("sleeps = %d, want 20", n)
	}
	// a sixth address with VALID credentials passes
	if w := r.get("ping", keyParams(r.rwKey), "198.51.100.70"); w.Code != 200 || w.doc(t)["status"] != "ok" {
		t.Fatalf("valid credentials from a fresh address: %d %s", w.Code, w.Body)
	}
	// the same address with INVALID credentials: 429 at once, no sleep recorded
	before := r.sleeps.count()
	w := r.get("ping", bad(), "198.51.100.70")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("invalid credentials with the global class empty: %d %s", w.Code, w.Body)
	}
	if r.sleeps.count() != before {
		t.Error("the 429 failure slept")
	}
}

func TestSubsonic_FailDelayBounded(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	release := make(chan struct{})
	entered := make(chan struct{}, 32)
	r.sleeps.hook = func(ctx context.Context, _ time.Duration) error {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	bad := keyParams("mms_" + strings.Repeat("f", 52))
	var wg sync.WaitGroup
	codes := make(chan int, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- r.get("ping", bad, fmt.Sprintf("203.0.113.%d", i+1)).Code
		}()
	}
	for i := 0; i < 16; i++ { // all 16 sleepers are in
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("sleepers did not arrive")
		}
	}
	w := r.get("ping", bad, "203.0.113.99")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("17th failure: %d %s", w.Code, w.Body)
	}
	select {
	case <-entered:
		t.Error("the 17th failure slept")
	default:
	}
	close(release)
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != 200 {
			t.Errorf("a sleeper answered %d", c)
		}
	}
}

func TestSubsonic_FailDelayReal(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{failDelay: time.Second})
	r.srv.sleep = timerSleep // the one test with the real timer
	bad := keyParams("mms_" + strings.Repeat("g", 52))
	start := time.Now()
	if w := r.get("ping", bad, "198.51.100.80"); w.code(t) != 44 {
		t.Fatalf("bad key: %s", w.Body)
	}
	if d := time.Since(start); d < time.Second {
		t.Errorf("one failure took %v, want at least 1 s", d)
	}
	// a cancelled context ends the wait early
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := newRequest(ctx, t, "ping?"+bad.Encode(), "198.51.100.81")
	start = time.Now()
	r.do(req)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("cancelled request waited %v", d)
	}
}

func TestSubsonic_GetOpenSubsonicExtensionsAnonymous(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	w := r.get("getOpenSubsonicExtensions", url.Values{"f": {"json"}}, "198.51.100.82")
	d := w.doc(t)
	exts, _ := d["openSubsonicExtensions"].([]any)
	var names []string
	for _, e := range exts {
		m := e.(map[string]any)
		names = append(names, m["name"].(string))
		if v := m["versions"].([]any); len(v) != 1 || v[0].(float64) != 1 {
			t.Errorf("%v versions %v", m["name"], v)
		}
	}
	if got := strings.Join(names, ","); got != "apiKeyAuthentication,formPost,playbackReport,songLyrics" {
		t.Errorf("extensions %s", got)
	}
	// even wrong credentials do not matter, and missing c/v is fine
	if got := r.get("getOpenSubsonicExtensions", url.Values{"apiKey": {"junk"}, "f": {"json"}}, "198.51.100.82").code(t); got != 0 {
		t.Errorf("with junk key: %d", got)
	}
	// XML shape
	w = r.get("getOpenSubsonicExtensions", url.Values{}, "198.51.100.82")
	if !strings.Contains(w.Body.String(), `<openSubsonicExtensions name="apiKeyAuthentication"><versions>1</versions></openSubsonicExtensions>`) {
		t.Errorf("xml: %s", w.Body)
	}
}
