package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/oidc"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Proxy-mode step-up. In proxy mode the edge proves the first factor (Remote-User) and its access rule is meant to
// demand the second for /human/. With MM_STEPUP_OIDC_CLIENT set the app no longer relies on that rule alone: a
// /human/ call needs a recent step-up cookie, which is only issued after an authorization-code flow (PKCE, state,
// nonce) at the OIDC issuer whose ID token proves a fresh second factor for the same user.

// ProxyStepUpGate is what the middleware needs: the time of the request's step-up (zero if none) and whether it is
// still fresh.
type ProxyStepUpGate interface {
	Check(r *http.Request, user string) (at time.Time, fresh bool)
}

const (
	flowTTL        = 10 * time.Minute
	maxFlows       = 32
	flowCookieName = "mm_stepup_flow"
	flowCookiePath = "/auth/step-up"
)

type stepFlow struct {
	nonce, verifier, next, user string
	expires                     time.Time
}

// ProxyStepUp implements ProxyStepUpGate and serves the /auth/step-up pages.
type ProxyStepUp struct {
	client *oidc.StepUpClient
	key    []byte
	now    func() time.Time
	secure bool

	mu    sync.Mutex
	flows map[string]stepFlow
}

// NewProxyStepUp builds the step-up. key is the app's HMAC pepper; secure follows the public URL's scheme.
func NewProxyStepUp(client *oidc.StepUpClient, key []byte, publicURL string, now func() time.Time) *ProxyStepUp {
	if now == nil {
		now = time.Now
	}
	return &ProxyStepUp{client: client, key: key, now: now, secure: strings.HasPrefix(publicURL, "https://"), flows: map[string]stepFlow{}}
}

func (s *ProxyStepUp) cookieName() string {
	if s.secure {
		return "__Host-mm_stepup"
	}
	return "mm_stepup"
}

func (s *ProxyStepUp) sign(user string, at int64) string {
	u := base64.RawURLEncoding.EncodeToString([]byte(user))
	return fmt.Sprintf("%s.%d.%s", u, at, mac(s.key, fmt.Sprintf("proxy-stepup\x00%s\x00%d", user, at)))
}

// Check reads the step-up cookie of a request whose edge-authenticated user is user.
func (s *ProxyStepUp) Check(r *http.Request, user string) (time.Time, bool) {
	for i, c := range r.CookiesNamed(s.cookieName()) {
		if i >= maxSessionCookies {
			break
		}
		parts := strings.Split(c.Value, ".")
		if len(parts) != 3 {
			continue
		}
		at, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || at < 0 || at > 1<<40 {
			continue
		}
		if !hmac.Equal([]byte(c.Value), []byte(s.sign(user, at))) { // binds the cookie to this user
			continue
		}
		t := time.Unix(at, 0)
		n := s.now()
		return t, !t.After(n.Add(oidc.Leeway)) && n.Sub(t) <= stepUpWindow
	}
	return time.Time{}, false
}

func (s *ProxyStepUp) setCookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, HttpOnly: true, //nolint:gosec // G124: Secure follows PublicURL (plain-http dev); HttpOnly and SameSite are always set
		Secure: s.secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *ProxyStepUp) putFlow(state string, f stepFlow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.now()
	for k, v := range s.flows {
		if !n.Before(v.expires) {
			delete(s.flows, k)
		}
	}
	for len(s.flows) >= maxFlows { // drop the oldest
		var oldest string
		var ot time.Time
		for k, v := range s.flows {
			if oldest == "" || v.expires.Before(ot) {
				oldest, ot = k, v.expires
			}
		}
		delete(s.flows, oldest)
	}
	s.flows[state] = f
}

// takeFlow returns and removes the flow for state (single use).
func (s *ProxyStepUp) takeFlow(state string) (stepFlow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flows[state]
	delete(s.flows, state)
	return f, ok && s.now().Before(f.expires)
}

func writeStepUpMsg(w http.ResponseWriter, status int, msg, next string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>metamusic - second factor</title></head><body><main><h1>Second factor</h1><p role="alert">%s</p><p><a href="/auth/step-up?next=%s">Try again</a></p></main></body></html>`,
		html.EscapeString(msg), html.EscapeString(url.QueryEscape(next)))
}

func (s *ProxyStepUp) human(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	p := FromContext(r.Context())
	if p.Kind != KindHuman || p.Via != "proxy" {
		writeProblem(w, http.StatusForbidden, "FORBIDDEN", "this page is for the signed-in operator")
		return p, false
	}
	return p, true
}

// Handler serves /auth/step-up, /auth/step-up/callback, /auth/step-up/done(.js); every other /auth/ path is a 404.
func (s *ProxyStepUp) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, http.StatusNotFound, "NOT_FOUND", "no such resource")
	})
	mux.HandleFunc("GET /auth/step-up", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.human(w, r)
		if !ok {
			return
		}
		next := safeNext(r.URL.Query().Get("next"))
		state, e1 := randToken()
		nonce, e2 := randToken()
		verifier, e3 := randToken()
		if err := errors.Join(e1, e2, e3); err != nil {
			slog.Error("step-up: randomness failed", "err", redact.Error(err))
			writeStepUpMsg(w, http.StatusInternalServerError, "Server error.", next)
			return
		}
		to, err := s.client.AuthURL(r.Context(), state, nonce, verifier)
		if err != nil {
			slog.Warn("step-up: provider unavailable", "err", redact.Error(err))
			writeStepUpMsg(w, http.StatusBadGateway, "The sign-in provider is unavailable.", next)
			return
		}
		s.putFlow(state, stepFlow{nonce: nonce, verifier: verifier, next: next, user: p.Name, expires: s.now().Add(flowTTL)})
		s.setCookie(w, flowCookieName, state, flowCookiePath, int(flowTTL.Seconds()))
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, to, http.StatusSeeOther) //nolint:gosec // G710: to is built from the issuer's discovered authorization endpoint
	})
	mux.HandleFunc("GET /auth/step-up/callback", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.human(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		state := q.Get("state")
		var cookieState string
		if c, err := r.Cookie(flowCookieName); err == nil {
			cookieState = c.Value
		}
		s.setCookie(w, flowCookieName, "", flowCookiePath, -1)
		flow, found := s.takeFlow(state)
		if state == "" || !found || subtle.ConstantTimeCompare([]byte(cookieState), []byte(state)) != 1 || flow.user != p.Name {
			slog.Warn("step-up: callback refused", "reason", "state")
			writeStepUpMsg(w, http.StatusForbidden, "The sign-in expired or did not start here.", defaultNext)
			return
		}
		if e := q.Get("error"); e != "" || q.Get("code") == "" {
			slog.Warn("step-up: provider returned an error")
			writeStepUpMsg(w, http.StatusForbidden, "The second factor was not confirmed.", flow.next)
			return
		}
		idt, err := s.client.Exchange(r.Context(), q.Get("code"), flow.verifier)
		if err == nil {
			_, err = s.client.VerifyIDToken(r.Context(), idt, flow.nonce, p.Name)
		}
		if err != nil {
			slog.Warn("step-up: refused", "err", redact.Error(err))
			writeStepUpMsg(w, http.StatusForbidden, "The second factor was not confirmed.", flow.next)
			return
		}
		s.setCookie(w, s.cookieName(), s.sign(p.Name, s.now().Unix()), "/", int(stepUpWindow.Seconds()))
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, flow.next, http.StatusSeeOther) //nolint:gosec // G710: next passed safeNext: a /human/ path, no "//", backslash or control character
	})
	mux.HandleFunc("GET /auth/step-up/done", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.human(w, r)
		if !ok {
			return
		}
		_, fresh := s.Check(r, p.Name)
		writeStepUpDone(w, fresh)
	})
	mux.HandleFunc("GET /auth/step-up/done.js", func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(stepUpDoneJS)
	})
	return mux
}
