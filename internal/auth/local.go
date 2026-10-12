package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"uuid"

	"golang.org/x/crypto/argon2"

	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	ErrBadCredentials  = errors.New("invalid credentials")
	ErrInvalidSession  = errors.New("invalid session")
	ErrInvalidPassword = errors.New("username and password must not be blank")
	ErrWeakPassword    = errors.New("password must be at least 12 characters")
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB = 64 MiB
	argonThreads = 2
	argonKeyLen  = 32

	SessionCookie     = "mm_session"
	HostSessionCookie = "__Host-mm_session" // used when PublicURL is https: sibling hosts cannot plant or shadow it
	maxSessionCookies = 4                   // duplicate session cookies examined per request
	minPasswordRunes  = 12
	maxPendingLogins  = 16 // login attempts in flight (hasher queue + failure delay); more get 503 at once
	maxFailBackoff    = 32 // cap on the failure-delay multiplier
	sessionTTL        = 12 * time.Hour
	slideAfter        = 5 * time.Minute // slide/re-issue only when this much of the TTL has elapsed
	maxHashers        = 2               // concurrent argon2id computations (64 MiB each)
)

// ErrBusy is returned when the caller's context ends while waiting for a hashing slot.
var ErrBusy = errors.New("auth busy")

type LocalAuth struct {
	st        *store.Store
	key       []byte
	secure    bool
	FailDelay time.Duration // delay applied to failed logins (default 1s)
	now       func() time.Time
	dummyOnce sync.Once
	dummy     string // hash verified when the user is unknown, to equalise timing (lazy)
	sem       chan struct{}
	active    atomic.Int32
	maxActive atomic.Int32 // high-water mark of concurrent hashing (observability/tests)
	pending   atomic.Int32 // login attempts admitted and not yet answered

	failSem     chan struct{} // held during the failure delay: failed logins are answered one at a time
	consecFails int           // consecutive failures since the last success (guarded by failSem)
}

// CookieName is the session cookie name for a deployment: __Host- prefixed over https.
func CookieName(secure bool) string {
	if secure {
		return HostSessionCookie
	}
	return SessionCookie
}

// CookieName returns the session cookie name this instance issues.
func (l *LocalAuth) CookieName() string { return CookieName(l.secure) }

// admit reserves a login slot; false means too many attempts are in flight.
func (l *LocalAuth) admit() bool {
	if l.pending.Add(1) > maxPendingLogins {
		l.pending.Add(-1)
		return false
	}
	return true
}

func (l *LocalAuth) release() { l.pending.Add(-1) }

// penalize makes a failed login cost wall-clock time that parallelism cannot
// avoid: failures are serialised on failMu and the delay grows with the
// consecutive failure count.
func (l *LocalAuth) penalize(ctx context.Context) {
	l.failSem <- struct{}{} // a channel, not a mutex, so waiters stay synctest-visible
	defer func() { <-l.failSem }()
	mult := 1 << min(l.consecFails/4, 5) // 1,1,1,1,2,2,2,2,4 ... capped at maxFailBackoff
	mult = min(mult, maxFailBackoff)
	l.consecFails++
	if d := l.FailDelay * time.Duration(mult); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
		}
	}
}

func (l *LocalAuth) succeeded() {
	l.failSem <- struct{}{}
	l.consecFails = 0
	<-l.failSem
}

// UseClock replaces the clock (app.Build hands it the rig clock; tests inject fakes).
func (l *LocalAuth) UseClock(now func() time.Time) { l.now = now }

func NewLocalAuth(st *store.Store, key []byte, publicURL string) *LocalAuth {
	la := &LocalAuth{
		st: st, key: key,
		secure:    strings.HasPrefix(publicURL, "https://"),
		FailDelay: time.Second,
		now:       time.Now,
		sem:       make(chan struct{}, maxHashers),
		failSem:   make(chan struct{}, 1),
	}
	return la
}

// dummyHash lazily computes a hash of a random per-process password (nobody can
// log in with it) used to equalise timing when the user is unknown.
func (l *LocalAuth) dummyHash() string {
	l.dummyOnce.Do(func() {
		pw := make([]byte, 24)
		_, _ = rand.Read(pw)
		l.dummy, _ = hashPassword(base64.RawStdEncoding.EncodeToString(pw))
	})
	return l.dummy
}

// withHasher bounds concurrent argon2id work; fails if ctx ends while waiting.
func (l *LocalAuth) withHasher(ctx context.Context, f func()) error {
	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		return ErrBusy
	}
	defer func() { <-l.sem }()
	n := l.active.Add(1)
	for {
		m := l.maxActive.Load()
		if n <= m || l.maxActive.CompareAndSwap(m, n) {
			break
		}
	}
	defer l.active.Add(-1)
	f()
	return nil
}

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum)), nil
}

func checkPassword(phc, pw string) bool {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var v int
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want))) //nolint:gosec // length of our own decoded hash
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (l *LocalAuth) ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// SetPassword creates or replaces the single human account and revokes every
// existing session.
func (l *LocalAuth) SetPassword(ctx context.Context, username, pw string) error {
	if strings.TrimSpace(username) == "" || strings.TrimSpace(pw) == "" {
		return ErrInvalidPassword
	}
	if utf8.RuneCountInString(pw) < minPasswordRunes {
		return ErrWeakPassword
	}
	var h string
	var herr error
	if err := l.withHasher(ctx, func() { h, herr = hashPassword(pw) }); err != nil {
		return err
	}
	if herr != nil {
		return herr
	}
	tx, err := l.st.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO human_account(id,username,password_hash,updated_at) VALUES(1,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET username=excluded.username, password_hash=excluded.password_hash, updated_at=excluded.updated_at`,
		username, h, l.ts(l.now())); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM human_sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

// Login validates credentials and returns the opaque session cookie value.
func (l *LocalAuth) Login(ctx context.Context, username, pw string) (string, error) {
	var dbUser, hash string
	err := l.st.R.QueryRowContext(ctx, `SELECT username,password_hash FROM human_account WHERE id=1`).Scan(&dbUser, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	known := err == nil
	if !known {
		hash = l.dummyHash()
	}
	var passOK bool
	if err := l.withHasher(ctx, func() { passOK = checkPassword(hash, pw) }); err != nil {
		return "", err
	}
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(dbUser)) == 1
	if !known || !passOK || !userOK {
		return "", ErrBadCredentials
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	cv := b32.EncodeToString(raw)
	id := uuid.NewV7().String()
	n := l.now()
	// opportunistic purge of expired sessions
	_, _ = l.st.W.ExecContext(ctx, `DELETE FROM human_sessions WHERE expires_at < ?`, l.ts(n))
	_, err = l.st.W.ExecContext(ctx,
		`INSERT INTO human_sessions(id,hmac_hash,created_at,expires_at) VALUES(?,?,?,?)`,
		id, mac(l.key, cv), l.ts(n), l.ts(n.Add(sessionTTL)))
	if err != nil {
		return "", err
	}
	return cv, nil
}

// VerifySession checks the cookie value and slides its expiry.
func (l *LocalAuth) VerifySession(ctx context.Context, cookieValue string) (Principal, error) {
	if cookieValue == "" {
		return Principal{}, ErrInvalidSession
	}
	h := mac(l.key, cookieValue)
	var id, exp string
	err := l.st.R.QueryRowContext(ctx, `SELECT id,expires_at FROM human_sessions WHERE hmac_hash=?`, h).Scan(&id, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrInvalidSession
	}
	if err != nil {
		return Principal{}, fmt.Errorf("verify session: %w", err)
	}
	n := l.now()
	expires, err := time.Parse(time.RFC3339, exp)
	if err != nil || !n.Before(expires) {
		return Principal{}, ErrInvalidSession
	}
	var user string
	if err := l.st.R.QueryRowContext(ctx, `SELECT username FROM human_account WHERE id=1`).Scan(&user); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Principal{}, ErrInvalidSession
		}
		return Principal{}, fmt.Errorf("verify session: %w", err)
	}
	// slide only once slideAfter of the TTL has elapsed: avoids a write per request
	if expires.Sub(n) < sessionTTL-slideAfter {
		_, _ = l.st.W.ExecContext(ctx, `UPDATE human_sessions SET expires_at=? WHERE id=?`, l.ts(n.Add(sessionTTL)), id)
	}
	return humanPrincipal(user, "local"), nil
}

func (l *LocalAuth) Logout(ctx context.Context, cookieValue string) error {
	_, err := l.st.W.ExecContext(ctx, `DELETE FROM human_sessions WHERE hmac_hash=?`, mac(l.key, cookieValue))
	return err
}

func (l *LocalAuth) cookie(value string, maxAge int) *http.Cookie {
	return sessionCookie(value, maxAge, l.secure)
}

func sessionCookie(value string, maxAge int, secure bool) *http.Cookie {
	return &http.Cookie{Name: CookieName(secure), Value: value, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

const loginPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>metamusic - sign in</title></head>
<body><main><h1>metamusic</h1>%s
<form method="post" action="/auth/login">
<label>Username <input name="username" autocomplete="username" required autofocus></label>
<label>Password <input name="password" type="password" autocomplete="current-password" required></label>
<button type="submit">Sign in</button></form></main></body></html>`

func writeLogin(w http.ResponseWriter, status int, msg string) {
	if msg != "" {
		msg = `<p role="alert">` + html.EscapeString(msg) + `</p>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	fmt.Fprintf(w, loginPage, msg)
}

// Handler serves /auth/login (GET form, POST submit) and /auth/logout (POST).
// POSTs are wrapped in CrossOriginProtection (login/logout CSRF).
func (l *LocalAuth) Handler() http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, http.StatusForbidden, "CROSS_ORIGIN", "cross-origin request refused")
	}))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", func(w http.ResponseWriter, r *http.Request) { writeLogin(w, 200, "") })
	mux.Handle("POST /auth/login", cop.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			writeLogin(w, http.StatusBadRequest, "Bad request.")
			return
		}
		if !l.admit() {
			w.Header().Set("Retry-After", "1")
			writeLogin(w, http.StatusServiceUnavailable, "Server busy, try again.")
			return
		}
		defer l.release()
		cv, err := l.Login(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"))
		switch {
		case errors.Is(err, ErrBadCredentials):
			l.penalize(r.Context())
			writeLogin(w, http.StatusUnauthorized, "Invalid username or password.")
			return
		case errors.Is(err, ErrBusy), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			w.Header().Set("Retry-After", "1")
			writeLogin(w, http.StatusServiceUnavailable, "Server busy, try again.")
			return
		case err != nil:
			slog.Error("login failed", "err", err)
			writeLogin(w, http.StatusInternalServerError, "Server error.")
			return
		}
		l.succeeded()
		for i, old := range r.CookiesNamed(l.CookieName()) {
			if i >= maxSessionCookies {
				break
			}
			if old.Value != "" {
				_ = l.Logout(r.Context(), old.Value) // rotate: presented sessions die
			}
		}
		http.SetCookie(w, l.cookie(cv, int(sessionTTL.Seconds())))
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})))
	mux.Handle("POST /auth/logout", cop.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i, c := range r.CookiesNamed(l.CookieName()) {
			if i >= maxSessionCookies {
				break
			}
			_ = l.Logout(r.Context(), c.Value)
		}
		http.SetCookie(w, l.cookie("", -1))
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
	})))
	return mux
}
