package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func newLocal(t *testing.T, publicURL string) *LocalAuth {
	t.Helper()
	la := NewLocalAuth(newStore(t), key, publicURL)
	la.FailDelay = 0
	return la
}

func TestPasswordRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	la := newLocal(t, "http://x")
	if err := la.SetPassword(ctx, "alice", "correct horse"); err != nil {
		t.Fatal(err)
	}
	var h string
	if err := la.st.R.QueryRowContext(ctx, `SELECT password_hash FROM human_account`).Scan(&h); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("hash = %q", h)
	}
	if _, err := la.Login(ctx, "alice", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong pw err = %v", err)
	}
	if _, err := la.Login(ctx, "bob", "correct horse"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong user err = %v", err)
	}
	if _, err := la.Login(ctx, "alice", "correct horse"); err != nil {
		t.Fatal(err)
	}
	// changing the password replaces the single account
	if err := la.SetPassword(ctx, "alice", "new-password-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := la.Login(ctx, "alice", "correct horse"); err == nil {
		t.Fatal("old password still works")
	}
}

func TestSessionLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	la := newLocal(t, "http://x")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	la.now = func() time.Time { return now }
	_ = la.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	cv, err := la.Login(ctx, "alice", "pw-pw-pw-pw-pw")
	if err != nil {
		t.Fatal(err)
	}
	p, err := la.VerifySession(ctx, cv)
	if err != nil || p.Kind != KindHuman || p.Name != "alice" || p.Via != "local" || !p.Has(ScopeAdmin) {
		t.Fatalf("p=%+v err=%v", p, err)
	}
	// sliding: 11h later still valid and extends
	now = now.Add(11 * time.Hour)
	if _, err := la.VerifySession(ctx, cv); err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Hour) // 22h total, but only 11h since last use
	if _, err := la.VerifySession(ctx, cv); err != nil {
		t.Fatalf("sliding expiry not applied: %v", err)
	}
	now = now.Add(13 * time.Hour)
	if _, err := la.VerifySession(ctx, cv); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired err = %v", err)
	}
	if _, err := la.VerifySession(ctx, "garbage"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("garbage err = %v", err)
	}
	// stored hashed, not plaintext
	var n int
	_ = la.st.R.QueryRowContext(ctx, `SELECT count(*) FROM human_sessions WHERE id=? OR hmac_hash=?`, cv, cv).Scan(&n)
	if n != 0 {
		t.Fatal("session cookie stored in clear")
	}
}

func TestLogoutDeletesSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	la := newLocal(t, "http://x")
	_ = la.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	cv, _ := la.Login(ctx, "alice", "pw-pw-pw-pw-pw")
	if err := la.Logout(ctx, cv); err != nil {
		t.Fatal(err)
	}
	if _, err := la.VerifySession(ctx, cv); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginHandlers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	la := newLocal(t, "https://mm.example.com")
	_ = la.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	h := la.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/login", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<form") || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET login: %d %q", rec.Code, rec.Body.String())
	}

	post := func(user, pw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(url.Values{"username": {user}, "password": {pw}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	bad := post("alice", "nope")
	if bad.Code != 401 || len(bad.Result().Cookies()) != 0 {
		t.Fatalf("bad login: %d", bad.Code)
	}
	good := post("alice", "pw-pw-pw-pw-pw")
	if good.Code != 303 || good.Header().Get("Location") != "/" {
		t.Fatalf("good login: %d %s", good.Code, good.Header().Get("Location"))
	}
	cs := good.Result().Cookies()
	if len(cs) != 1 {
		t.Fatal("no cookie")
	}
	c := cs[0]
	if c.Name != "__Host-mm_session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 12*3600 {
		t.Fatalf("cookie = %+v", c)
	}
	if _, err := la.VerifySession(ctx, c.Value); err != nil {
		t.Fatal(err)
	}

	// Secure only when https
	la2 := newLocal(t, "http://localhost:8080")
	_ = la2.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	r := httptest.NewRequest("POST", "/auth/login", strings.NewReader("username=alice&password=pw-pw-pw-pw-pw"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	la2.Handler().ServeHTTP(rec, r)
	if rec.Result().Cookies()[0].Secure {
		t.Fatal("cookie Secure on http")
	}

	// logout
	lr := httptest.NewRequest("POST", "/auth/logout", nil)
	lr.AddCookie(c)
	lrec := httptest.NewRecorder()
	h.ServeHTTP(lrec, lr)
	if lrec.Code != 303 {
		t.Fatalf("logout code %d", lrec.Code)
	}
	if _, err := la.VerifySession(ctx, c.Value); err == nil {
		t.Fatal("session survived logout")
	}
}

func TestFailureDelay(t *testing.T) {
	la := newLocal(t, "http://x")
	la.FailDelay = 50 * time.Millisecond
	_ = la.SetPassword(context.Background(), "a", "bbbbbbbbbbbb")
	synctest.Test(t, func(t *testing.T) {
		r := httptest.NewRequest("POST", "/auth/login", strings.NewReader("username=a&password=x"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		start := time.Now()
		rec := httptest.NewRecorder()
		la.Handler().ServeHTTP(rec, r)
		if got := time.Since(start); got != 50*time.Millisecond {
			t.Fatalf("failure delay = %v, want 50ms", got)
		}
		if rec.Code != 401 {
			t.Fatalf("code %d", rec.Code)
		}
	})
}

func TestSetPasswordRevokesSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	la := newLocal(t, "http://x")
	_ = la.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	cv, err := la.Login(ctx, "alice", "pw-pw-pw-pw-pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := la.SetPassword(ctx, "alice2", "pw-two-pw-two-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := la.VerifySession(ctx, cv); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("old session err = %v", err)
	}
}

func TestExpiredSessionsPurgedOnLogin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	la := newLocal(t, "http://x")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	la.now = func() time.Time { return now }
	_ = la.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	_, _ = la.Login(ctx, "alice", "pw-pw-pw-pw-pw")
	now = now.Add(48 * time.Hour)
	_, _ = la.Login(ctx, "alice", "pw-pw-pw-pw-pw")
	var n int
	_ = la.st.R.QueryRowContext(ctx, `SELECT count(*) FROM human_sessions`).Scan(&n)
	if n != 1 {
		t.Fatalf("sessions = %d, want 1", n)
	}
}

func TestLoginWithoutAccountFails(t *testing.T) {
	t.Parallel()
	la := newLocal(t, "http://x")
	for _, c := range [][2]string{{"", "dummy-password-for-timing"}, {"", ""}, {"alice", "x"}} {
		if _, err := la.Login(context.Background(), c[0], c[1]); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("Login(%q,%q) err = %v", c[0], c[1], err)
		}
	}
	// the dummy password is random per instance, not a public constant
	other := newLocal(t, "http://x")
	if la.dummy == other.dummy {
		t.Fatal("dummy hash identical across instances")
	}
	// even knowing the old constant, the empty user must not pass
	h, _ := hashPassword("dummy-password-for-timing")
	la.dummy = h
	if _, err := la.Login(context.Background(), "", "dummy-password-for-timing"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("!known guard missing: %v", err)
	}
}

func TestLoginConcurrencyBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	la := newLocal(t, "http://x")
	_ = la.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	la.maxActive.Store(0)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = la.Login(ctx, "alice", "bad")
		}()
	}
	wg.Wait()
	if m := la.maxActive.Load(); m < 1 || m > maxHashers {
		t.Fatalf("max concurrent hashing = %d, want 1..%d", m, maxHashers)
	}
}

func TestLoginBusyWhenContextDone(t *testing.T) {
	t.Parallel()
	la := newLocal(t, "http://x")
	_ = la.SetPassword(context.Background(), "alice", "pw-pw-pw-pw-pw")
	for i := 0; i < maxHashers; i++ {
		la.sem <- struct{}{} // saturate
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := la.Login(ctx, "alice", "pw-pw-pw-pw-pw"); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v", err)
	}
	r := httptest.NewRequest("POST", "/auth/login", strings.NewReader("username=alice&password=pw-pw-pw-pw-pw")).WithContext(ctx)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	la.Handler().ServeHTTP(rec, r)
	if rec.Code != 503 {
		t.Fatalf("code %d, want 503", rec.Code)
	}
}

func TestAuthPostsCrossOriginProtected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	la := newLocal(t, "http://x")
	_ = la.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	cv, _ := la.Login(ctx, "alice", "pw-pw-pw-pw-pw")
	r := httptest.NewRequest("POST", "/auth/logout", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: cv})
	rec := httptest.NewRecorder()
	la.Handler().ServeHTTP(rec, r)
	if rec.Code != 403 || rec.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(rec.Body.String(), `"CROSS_ORIGIN"`) {
		t.Fatalf("cross-site logout: %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if _, err := la.VerifySession(ctx, cv); err != nil {
		t.Fatal("session destroyed by cross-site logout")
	}
	body := strings.NewReader("username=alice&password=pw-pw-pw-pw-pw")
	r = httptest.NewRequest("POST", "/auth/login", body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	rec = httptest.NewRecorder()
	la.Handler().ServeHTTP(rec, r)
	if rec.Code != 403 || len(rec.Result().Cookies()) != 0 ||
		rec.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(rec.Body.String(), `"CROSS_ORIGIN"`) {
		t.Fatalf("cross-site login: %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	rec = httptest.NewRecorder()
	la.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/auth/login", nil))
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("login page lacks frame-ancestors")
	}
}

func TestSessionSlideThrottled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	la := newLocal(t, "http://x")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	la.now = func() time.Time { return now }
	_ = la.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	cv, _ := la.Login(ctx, "alice", "pw-pw-pw-pw-pw")
	now = now.Add(time.Minute)
	_, _ = la.VerifySession(ctx, cv)
	var exp string
	_ = la.st.R.QueryRowContext(ctx, `SELECT expires_at FROM human_sessions`).Scan(&exp)
	if exp != "2026-01-01T12:00:00Z" {
		t.Fatalf("slid within throttle window: %s", exp)
	}
	now = now.Add(10 * time.Minute)
	_, _ = la.VerifySession(ctx, cv)
	_ = la.st.R.QueryRowContext(ctx, `SELECT expires_at FROM human_sessions`).Scan(&exp)
	if exp != "2026-01-01T12:11:00Z" {
		t.Fatalf("did not slide: %s", exp)
	}
}

func TestSetPasswordRejectsBlank(t *testing.T) {
	t.Parallel()
	la := newLocal(t, "http://x")
	for _, c := range []struct{ u, p string }{{"", ""}, {"alice", ""}, {"", "pw-pw-pw-pw-pw"}, {"  ", "pw-pw-pw-pw-pw"}, {"alice", "   "}} {
		if err := la.SetPassword(context.Background(), c.u, c.p); !errors.Is(err, ErrInvalidPassword) {
			t.Errorf("SetPassword(%q,%q) = %v", c.u, c.p, err)
		}
	}
	if _, err := la.Login(context.Background(), "", ""); err == nil {
		t.Fatal("empty login succeeded")
	}
}

func TestLoginRotatesPresentedSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	la := newLocal(t, "http://x")
	_ = la.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	old, _ := la.Login(ctx, "alice", "pw-pw-pw-pw-pw")
	r := httptest.NewRequest("POST", "/auth/login", strings.NewReader("username=alice&password=pw-pw-pw-pw-pw"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: old})
	rec := httptest.NewRecorder()
	la.Handler().ServeHTTP(rec, r)
	if rec.Code != 303 {
		t.Fatalf("login: %d", rec.Code)
	}
	if _, err := la.VerifySession(ctx, old); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("old session still valid: %v", err)
	}
}

func TestSetPasswordRejectsShort(t *testing.T) {
	t.Parallel()
	la := newLocal(t, "http://x")
	for _, pw := range []string{"x", "elevenchars", "short"} {
		if err := la.SetPassword(context.Background(), "alice", pw); !errors.Is(err, ErrWeakPassword) {
			t.Errorf("SetPassword(%q) = %v, want ErrWeakPassword", pw, err)
		}
	}
	if err := la.SetPassword(context.Background(), "alice", "twelve-chars"); err != nil {
		t.Errorf("12-char password rejected: %v", err)
	}
}

// Parallel bad logins must not beat the failure budget: failures are answered
// one at a time (FailDelay each) and excess attempts are refused with 503.
func TestParallelBadLoginsAreThrottled(t *testing.T) {
	la := newLocal(t, "http://x")
	la.FailDelay = 100 * time.Millisecond
	_ = la.SetPassword(context.Background(), "alice", "pw-pw-pw-pw-pw")
	synctest.Test(t, func(t *testing.T) {
		// channels must belong to the bubble for waiters to count as durably blocked
		la.sem = make(chan struct{}, maxHashers)
		la.failSem = make(chan struct{}, 1)
		const n = 64
		var wg sync.WaitGroup
		var mu sync.Mutex
		codes := map[int]int{}
		start := time.Now()
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := httptest.NewRequest("POST", "/auth/login", strings.NewReader("username=alice&password=wrong-guess"))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rec := httptest.NewRecorder()
				la.Handler().ServeHTTP(rec, r)
				mu.Lock()
				codes[rec.Code]++
				mu.Unlock()
			}()
		}
		wg.Wait()
		elapsed := time.Since(start)
		if codes[401] > maxPendingLogins {
			t.Errorf("%d guesses answered, want <= %d: %v", codes[401], maxPendingLogins, codes)
		}
		if codes[503] < n-maxPendingLogins {
			t.Errorf("only %d refused with 503: %v", codes[503], codes)
		}
		// answered failures are serialised: at least FailDelay each
		if floor := time.Duration(codes[401]) * la.FailDelay; elapsed < floor {
			t.Errorf("elapsed %v < serial budget %v", elapsed, floor)
		}
	})
}

func TestFailureBackoffGrowsAndResets(t *testing.T) {
	la := newLocal(t, "http://x")
	la.FailDelay = 10 * time.Millisecond
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		var total time.Duration
		for range 8 {
			s := time.Now()
			la.penalize(ctx)
			total += time.Since(s)
		}
		if total <= 8*la.FailDelay {
			t.Errorf("no backoff: %v", total)
		}
		la.succeeded()
		s := time.Now()
		la.penalize(ctx)
		if d := time.Since(s); d != la.FailDelay {
			t.Errorf("after success delay = %v", d)
		}
	})
}

func TestCheckPassword_MalformedPHC(t *testing.T) {
	t.Parallel()
	good, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !checkPassword(good, "correct horse battery") || checkPassword(good, "other") {
		t.Fatal("round trip")
	}
	parts := strings.Split(good, "$")
	with := func(i int, v string) string {
		p := append([]string(nil), parts...)
		p[i] = v
		return strings.Join(p, "$")
	}
	for name, phc := range map[string]string{
		"empty":         "",
		"too few parts": "$argon2id$v=19$m=1,t=1,p=1$c2FsdA",
		"wrong alg":     with(1, "argon2i"),
		"bad version":   with(2, "v=x"),
		"old version":   with(2, "v=16"),
		"bad params":    with(3, "m=abc"),
		"bad salt":      with(4, "!!!"),
		"bad hash":      with(5, "!!!"),
		"empty hash":    with(5, ""),
	} {
		if checkPassword(phc, "correct horse battery") {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSetPassword_Errors(t *testing.T) {
	t.Parallel()
	la := newLocal(t, "http://x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// hasher slots full + cancelled context => ErrBusy
	for range cap(la.sem) {
		la.sem <- struct{}{}
	}
	if err := la.SetPassword(ctx, "alice", "long enough password"); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	for range cap(la.sem) {
		<-la.sem
	}
	// closed store => the transaction cannot begin
	la2 := newLocal(t, "http://x")
	_ = la2.st.Close()
	if err := la2.SetPassword(context.Background(), "alice", "long enough password"); err == nil {
		t.Fatal("closed store accepted")
	}
	// a failing statement inside the transaction
	la3 := newLocal(t, "http://x")
	if _, err := la3.st.W.Exec(`DROP TABLE human_sessions`); err != nil {
		t.Fatal(err)
	}
	if err := la3.SetPassword(context.Background(), "alice", "long enough password"); err == nil {
		t.Fatal("missing table accepted")
	}
	if _, err := la3.st.W.Exec(`DROP TABLE human_account`); err != nil {
		t.Fatal(err)
	}
	if err := la3.SetPassword(context.Background(), "alice", "long enough password"); err == nil {
		t.Fatal("missing account table accepted")
	}
}

func TestVerifySession_ErrorPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	la := newLocal(t, "http://x")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	la.now = func() time.Time { return now }
	if _, err := la.VerifySession(ctx, ""); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := la.VerifySession(ctx, "unknown"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unknown: %v", err)
	}
	_ = la.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	cv, err := la.Login(ctx, "alice", "pw-pw-pw-pw-pw")
	if err != nil {
		t.Fatal(err)
	}
	// unparseable expiry
	if _, err := la.st.W.Exec(`UPDATE human_sessions SET expires_at='garbage'`); err != nil {
		t.Fatal(err)
	}
	if _, err := la.VerifySession(ctx, cv); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("garbage expiry: %v", err)
	}
	// exactly expired
	if _, err := la.st.W.Exec(`UPDATE human_sessions SET expires_at=?`, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := la.VerifySession(ctx, cv); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired: %v", err)
	}
	// valid session but the account row is gone
	if _, err := la.st.W.Exec(`UPDATE human_sessions SET expires_at=?`, now.Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := la.st.W.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := la.st.W.Exec(`DELETE FROM human_account`); err != nil {
		t.Fatal(err)
	}
	if _, err := la.VerifySession(ctx, cv); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("no account: %v", err)
	}
	// store failures are plain errors, not ErrInvalidSession
	_, _ = la.st.W.Exec(`DROP TABLE human_account`)
	if _, err := la.VerifySession(ctx, cv); err == nil || errors.Is(err, ErrInvalidSession) {
		t.Fatalf("account table error: %v", err)
	}
	_, _ = la.st.W.Exec(`DROP TABLE human_sessions`)
	if _, err := la.VerifySession(ctx, cv); err == nil || errors.Is(err, ErrInvalidSession) {
		t.Fatalf("sessions table error: %v", err)
	}
}

func TestLocalAuth_UseClock(t *testing.T) {
	ctx := context.Background()
	la := newLocal(t, "http://x")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	la.UseClock(func() time.Time { return now })
	_ = la.SetPassword(ctx, "alice", "pw-pw-pw-pw-pw")
	cv, err := la.Login(ctx, "alice", "pw-pw-pw-pw-pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := la.VerifySession(ctx, cv); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * 24 * time.Hour)
	if _, err := la.VerifySession(ctx, cv); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("session must expire by the injected clock: %v", err)
	}
}
