package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// PairingOptions configures the public pairing pages (C26).
type PairingOptions struct {
	Cfg      config.Config
	Devices  *DeviceStore
	Sessions SessionVerifier // local mode: a valid mm_session on /start goes straight to "/"
	Allow    func() (ok bool, retryAfter time.Duration)
	OnPaired func(ctx context.Context, d DeviceInfo)
	// FailDelay defaults to one second.
	FailDelay time.Duration
}

const (
	startCookie    = "mm_start"
	startCookieAge = 400 * 24 * time.Hour
	maxPairBody    = 1 << 10
	maxFailWaiters = 8 // wrong-code requests sleeping at once; more get 429 immediately
)

// PairingHandler serves /start, /pair, /pair.js and /auth/pair.
func PairingHandler(o PairingOptions) http.Handler {
	failDelay := o.FailDelay
	if failDelay <= 0 {
		failDelay = time.Second
	}
	secure := strings.HasPrefix(o.Cfg.PublicURL, "https://")
	ssoTarget := "/"
	if o.Cfg.AuthMode == "local" {
		ssoTarget = "/auth/login"
	}
	waiters := make(chan struct{}, maxFailWaiters)

	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, http.StatusForbidden, "CROSS_ORIGIN", "cross-origin request refused", nil)
	}))
	notFound := func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, http.StatusNotFound, "NOT_FOUND", "no such resource", nil)
	}
	startCk := func(value string, maxAge int) *http.Cookie {
		return &http.Cookie{Name: startCookie, //nolint:gosec // G124: Secure follows PublicURL (plain-http dev); HttpOnly and SameSite are always set
			Value: value, Path: "/", HttpOnly: true, Secure: secure,
			SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /start", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("expired") == "1" {
			http.SetCookie(w, startCk("", -1))
			writePage(w, http.StatusOK, "sign in", expiredNoticeChooser())
			return
		}
		if o.Devices != nil {
			for i, c := range r.CookiesNamed(DeviceCookie) {
				if i >= maxSessionCookies {
					break
				}
				if _, slid, err := o.Devices.verify(r.Context(), c.Value); err == nil {
					if slid {
						http.SetCookie(w, deviceCookie(c.Value, int(DeviceTTL.Seconds()), secure))
					}
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}
			}
		}
		if o.Cfg.AuthMode == "local" && o.Sessions != nil {
			for i, c := range r.CookiesNamed(CookieName(secure)) {
				if i >= maxSessionCookies {
					break
				}
				if _, err := o.Sessions.VerifySession(r.Context(), c.Value); err == nil {
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}
			}
		}
		if c, err := r.Cookie(startCookie); err == nil && c.Value == "sso" {
			http.Redirect(w, r, ssoTarget, http.StatusFound)
			return
		}
		writePage(w, http.StatusOK, "sign in", chooser(""))
	})
	mux.Handle("POST /start", cop.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, startCk("sso", int(startCookieAge.Seconds())))
		http.Redirect(w, r, ssoTarget, http.StatusSeeOther)
	})))
	mux.HandleFunc("GET /pair", func(w http.ResponseWriter, r *http.Request) {
		if o.Devices == nil {
			notFound(w, r)
			return
		}
		writePage(w, http.StatusOK, "pair this device", pairForm(""))
	})
	mux.HandleFunc("GET /pair.js", func(w http.ResponseWriter, r *http.Request) {
		securePage(w, "text/javascript; charset=utf-8")
		_, _ = w.Write(pairJS)
	})
	mux.Handle("POST /auth/pair", cop.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o.Devices == nil {
			notFound(w, r)
			return
		}
		if o.Allow != nil {
			if ok, retry := o.Allow(); !ok {
				tooMany(w, retry)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxPairBody)
		if err := r.ParseForm(); err != nil {
			writePage(w, http.StatusBadRequest, "pair this device", pairForm(`<p role="alert">Bad request.</p>`))
			return
		}
		cookie, dev, err := o.Devices.Redeem(r.Context(), r.PostForm.Get("code"), r.UserAgent())
		switch {
		case errors.Is(err, ErrInvalidCode):
			select {
			case waiters <- struct{}{}:
			default:
				tooMany(w, time.Second)
				return
			}
			t := time.NewTimer(failDelay)
			select {
			case <-t.C:
			case <-r.Context().Done():
			}
			t.Stop()
			<-waiters
			writePage(w, http.StatusUnauthorized, "pair this device", pairForm(invalidNotice))
			return
		case err != nil:
			slog.Error("pairing failed", "err", err)
			writePage(w, http.StatusInternalServerError, "pair this device", pairForm(`<p role="alert">Server error.</p>`))
			return
		}
		http.SetCookie(w, deviceCookie(cookie, int(DeviceTTL.Seconds()), secure))
		if o.OnPaired != nil {
			o.OnPaired(r.Context(), dev)
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})))
	return mux
}

func tooMany(w http.ResponseWriter, retry time.Duration) {
	secs := max(int((retry+time.Second-1)/time.Second), 1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writePage(w, http.StatusTooManyRequests, "too many attempts", `<p role="alert">Too many attempts. Try again shortly.</p>`)
}

func chooser(notice string) string  { return sprintfBody(chooserBody, notice) }
func pairForm(notice string) string { return sprintfBody(pairFormBody, notice) }
func expiredNoticeChooser() string  { return chooser(expiredNotice) }
