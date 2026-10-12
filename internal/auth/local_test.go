package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newLocal(t *testing.T, publicURL string) *LocalAuth {
	t.Helper()
	la := NewLocalAuth(newStore(t), key, publicURL)
	la.FailDelay = 0
	return la
}

func TestPasswordRoundTrip(t *testing.T) {
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
	r := httptest.NewRequest("POST", "/auth/login", strings.NewReader("username=a&password=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start := time.Now()
	la.Handler().ServeHTTP(httptest.NewRecorder(), r)
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("no failure delay")
	}
}
