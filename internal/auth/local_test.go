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
	if err := la.SetPassword(ctx, "alice", "new"); err != nil {
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
	_ = la.SetPassword(ctx, "alice", "pw")
	cv, err := la.Login(ctx, "alice", "pw")
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
	_ = la.SetPassword(ctx, "alice", "pw")
	cv, _ := la.Login(ctx, "alice", "pw")
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
	_ = la.SetPassword(ctx, "alice", "pw")
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
	good := post("alice", "pw")
	if good.Code != 303 || good.Header().Get("Location") != "/" {
		t.Fatalf("good login: %d %s", good.Code, good.Header().Get("Location"))
	}
	cs := good.Result().Cookies()
	if len(cs) != 1 {
		t.Fatal("no cookie")
	}
	c := cs[0]
	if c.Name != "mm_session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 12*3600 {
		t.Fatalf("cookie = %+v", c)
	}
	if _, err := la.VerifySession(ctx, c.Value); err != nil {
		t.Fatal(err)
	}

	// Secure only when https
	la2 := newLocal(t, "http://localhost:8080")
	_ = la2.SetPassword(ctx, "alice", "pw")
	r := httptest.NewRequest("POST", "/auth/login", strings.NewReader("username=alice&password=pw"))
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
	_ = la.SetPassword(context.Background(), "a", "b")
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
	_ = la.SetPassword(ctx, "alice", "pw")
	cv, err := la.Login(ctx, "alice", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := la.SetPassword(ctx, "alice2", "pw2"); err != nil {
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
	_ = la.SetPassword(ctx, "alice", "pw")
	_, _ = la.Login(ctx, "alice", "pw")
	now = now.Add(48 * time.Hour)
	_, _ = la.Login(ctx, "alice", "pw")
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
	_ = la.SetPassword(ctx, "alice", "pw")
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
	_ = la.SetPassword(context.Background(), "alice", "pw")
	for i := 0; i < maxHashers; i++ {
		la.sem <- struct{}{} // saturate
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := la.Login(ctx, "alice", "pw"); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v", err)
	}
	r := httptest.NewRequest("POST", "/auth/login", strings.NewReader("username=alice&password=pw")).WithContext(ctx)
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
	_ = la.SetPassword(ctx, "alice", "pw")
	cv, _ := la.Login(ctx, "alice", "pw")
	r := httptest.NewRequest("POST", "/auth/logout", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: cv})
	rec := httptest.NewRecorder()
	la.Handler().ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatalf("cross-site logout: %d", rec.Code)
	}
	if _, err := la.VerifySession(ctx, cv); err != nil {
		t.Fatal("session destroyed by cross-site logout")
	}
	body := strings.NewReader("username=alice&password=pw")
	r = httptest.NewRequest("POST", "/auth/login", body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	rec = httptest.NewRecorder()
	la.Handler().ServeHTTP(rec, r)
	if rec.Code != 403 || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("cross-site login: %d", rec.Code)
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
	_ = la.SetPassword(ctx, "alice", "pw")
	cv, _ := la.Login(ctx, "alice", "pw")
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
