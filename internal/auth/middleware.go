package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

type TokenVerifier interface {
	Verify(ctx context.Context, plaintext string) (Principal, error)
}

type SessionVerifier interface {
	VerifySession(ctx context.Context, cookieValue string) (Principal, error)
}

func writeProblem(w http.ResponseWriter, status int, code, msg string) {
	problem.Write(w, status, code, msg, nil)
}

func internalError(w http.ResponseWriter, what string, err error) {
	slog.Error(what, "err", err)
	writeProblem(w, http.StatusInternalServerError, "INTERNAL", "internal error")
}

func unauthenticated(w http.ResponseWriter, withChallenge bool) {
	if withChallenge {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	writeProblem(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
}

func trustedPeer(remoteAddr string, prefixes []netip.Prefix) bool {
	ap, err := netip.ParseAddrPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := ap.Addr().Unmap()
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func proxyPrincipal(r *http.Request, cfg config.Config, sec config.Secrets) (Principal, bool) {
	if sec.EdgeSecret == "" || !trustedPeer(r.RemoteAddr, cfg.TrustedProxies) {
		return Principal{}, false
	}
	edge := r.Header.Values("X-MM-Edge")
	if len(edge) != 1 || subtle.ConstantTimeCompare([]byte(edge[0]), []byte(sec.EdgeSecret)) != 1 {
		return Principal{}, false
	}
	user := r.Header.Values("Remote-User")
	if len(user) != 1 || user[0] == "" || strings.Contains(user[0], ",") || !slices.Contains(cfg.TrustedUsers, user[0]) {
		return Principal{}, false
	}
	return humanPrincipal(user[0], "proxy"), true
}

// Middleware resolves the request principal per the C3 table.
func Middleware(cfg config.Config, sec config.Secrets, tokens TokenVerifier, sessions SessionVerifier) func(http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			class := Classify(path)

			// 1. Authorization header present: bearer only.
			if hdr := r.Header.Values("Authorization"); len(hdr) > 0 {
				tok := ""
				if len(hdr) == 1 {
					if scheme, rest, found := strings.Cut(hdr[0], " "); found && strings.EqualFold(scheme, "Bearer") {
						tok = rest
					}
				}
				if strings.HasPrefix(tok, "mmp_") {
					p, err := tokens.Verify(r.Context(), tok)
					if err == nil {
						next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
						return
					}
					if !errors.Is(err, ErrInvalidToken) {
						internalError(w, "token verification failed", err)
						return
					}
				}
				unauthenticated(w, true)
				return
			}

			// 2./3. Public and machine paths: anonymous.
			if class != ClassUI {
				next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), Principal{Kind: KindAnonymous})))
				return
			}

			// 4./5. UI class.
			var p Principal
			var ok bool
			var refresh string // session cookie value to re-issue (sliding expiry)
			switch cfg.AuthMode {
			case "proxy":
				p, ok = proxyPrincipal(r, cfg, sec)
			case "local":
				// a planted duplicate must not shadow the real cookie: first valid one wins
				for i, c := range r.CookiesNamed(CookieName(strings.HasPrefix(cfg.PublicURL, "https://"))) {
					if i >= maxSessionCookies {
						break
					}
					sp, err := sessions.VerifySession(r.Context(), c.Value)
					if err == nil {
						p, ok, refresh = sp, true, c.Value
						break
					}
					if !errors.Is(err, ErrInvalidSession) {
						internalError(w, "session verification failed", err)
						return
					}
				}
			}
			if !ok {
				isAPI := strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/human/api/")
				switch {
				case isAPI:
					unauthenticated(w, false)
				case cfg.AuthMode == "local":
					http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
				default:
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte("Not authenticated"))
				}
				return
			}
			if err := cop.Check(r); err != nil {
				writeProblem(w, http.StatusForbidden, "CROSS_ORIGIN", "cross-origin request refused")
				return
			}
			if refresh != "" {
				http.SetCookie(w, sessionCookie(refresh, int(sessionTTL.Seconds()), strings.HasPrefix(cfg.PublicURL, "https://")))
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}
