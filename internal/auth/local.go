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
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	ErrBadCredentials = errors.New("invalid credentials")
	ErrInvalidSession = errors.New("invalid session")
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB = 64 MiB
	argonThreads = 2
	argonKeyLen  = 32

	SessionCookie = "mm_session"
	sessionTTL    = 12 * time.Hour
)

type LocalAuth struct {
	st        *store.Store
	key       []byte
	secure    bool
	FailDelay time.Duration // delay applied to failed logins (default 1s)
	now       func() time.Time
	dummy     string // hash verified when the user is unknown, to equalise timing
}

func NewLocalAuth(st *store.Store, key []byte, publicURL string) *LocalAuth {
	la := &LocalAuth{
		st: st, key: key,
		secure:    strings.HasPrefix(publicURL, "https://"),
		FailDelay: time.Second,
		now:       time.Now,
	}
	la.dummy, _ = hashPassword("dummy-password-for-timing")
	return la
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
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (l *LocalAuth) ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// SetPassword creates or replaces the single human account.
func (l *LocalAuth) SetPassword(ctx context.Context, username, pw string) error {
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	_, err = l.st.W.ExecContext(ctx,
		`INSERT INTO human_account(id,username,password_hash,updated_at) VALUES(1,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET username=excluded.username, password_hash=excluded.password_hash, updated_at=excluded.updated_at`,
		username, h, l.ts(l.now()))
	return err
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
		hash = l.dummy
	}
	passOK := checkPassword(hash, pw)
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(dbUser)) == 1
	if !known || !passOK || !userOK {
		return "", ErrBadCredentials
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	cv := b32.EncodeToString(raw)
	id, err := randomHex(8)
	if err != nil {
		return "", err
	}
	n := l.now()
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
	if err != nil {
		return Principal{}, ErrInvalidSession
	}
	n := l.now()
	expires, err := time.Parse(time.RFC3339, exp)
	if err != nil || !n.Before(expires) {
		return Principal{}, ErrInvalidSession
	}
	var user string
	if err := l.st.R.QueryRowContext(ctx, `SELECT username FROM human_account WHERE id=1`).Scan(&user); err != nil {
		return Principal{}, ErrInvalidSession
	}
	_, _ = l.st.W.ExecContext(ctx, `UPDATE human_sessions SET expires_at=? WHERE id=?`, l.ts(n.Add(sessionTTL)), id)
	return humanPrincipal(user, "local"), nil
}

func (l *LocalAuth) Logout(ctx context.Context, cookieValue string) error {
	_, err := l.st.W.ExecContext(ctx, `DELETE FROM human_sessions WHERE hmac_hash=?`, mac(l.key, cookieValue))
	return err
}

func (l *LocalAuth) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: SessionCookie, Value: value, Path: "/", HttpOnly: true,
		Secure: l.secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
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
	w.WriteHeader(status)
	fmt.Fprintf(w, loginPage, msg)
}

// Handler serves /auth/login (GET form, POST submit) and /auth/logout (POST).
func (l *LocalAuth) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", func(w http.ResponseWriter, r *http.Request) { writeLogin(w, 200, "") })
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			writeLogin(w, http.StatusBadRequest, "Bad request.")
			return
		}
		cv, err := l.Login(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"))
		if err != nil {
			time.Sleep(l.FailDelay)
			writeLogin(w, http.StatusUnauthorized, "Invalid username or password.")
			return
		}
		http.SetCookie(w, l.cookie(cv, int(sessionTTL.Seconds())))
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(SessionCookie); err == nil {
			_ = l.Logout(r.Context(), c.Value)
		}
		http.SetCookie(w, l.cookie("", -1))
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
	})
	return mux
}
