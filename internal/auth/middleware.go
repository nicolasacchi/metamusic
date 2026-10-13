package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

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

// Middleware resolves the request principal per the C3 table. devices may be
// nil (device auth disabled); callers pass a nil interface, never a typed-nil
// *DeviceStore. Rows 6 and 7 of the C26 table are the device cookie: it is
// read only on UI-class paths outside /human/, and only a cookie that was read
// and rejected is cleared.
func Middleware(cfg config.Config, sec config.Secrets, tokens TokenVerifier, sessions SessionVerifier, devices DeviceVerifier) func(http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	secure := strings.HasPrefix(cfg.PublicURL, "https://")
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
			var deviceRefresh string
			rejectedDevice := false
			underHuman := path == "/human" || strings.HasPrefix(path, "/human/")
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
			if !ok && devices != nil && !underHuman {
				for i, c := range r.CookiesNamed(DeviceCookie) {
					if i >= maxSessionCookies {
						break
					}
					dp, slid, err := verifyDevice(r.Context(), devices, c.Value)
					if err == nil {
						p, ok, rejectedDevice = dp, true, false
						if slid {
							deviceRefresh = c.Value
						}
						break
					}
					if !errors.Is(err, ErrInvalidDevice) {
						internalError(w, "device verification failed", err)
						return
					}
					rejectedDevice = true
				}
			}
			if !ok {
				if rejectedDevice {
					http.SetCookie(w, deviceCookie("", -1, secure))
				}
				isAPI := strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/human/api/")
				switch {
				case isAPI:
					unauthenticated(w, false)
				case rejectedDevice:
					http.Redirect(w, r, "/start?expired=1", http.StatusSeeOther)
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
			if p.Via == "local" && (path == "/human" || strings.HasPrefix(path, "/human/")) && !stepUpFresh(sessions, p) {
				if strings.HasPrefix(path, "/human/api/") {
					writeProblem(w, http.StatusForbidden, "STEP_UP_REQUIRED", "confirm your password to continue")
				} else {
					http.Redirect(w, r, "/auth/step-up?next="+url.QueryEscape(path), http.StatusSeeOther)
				}
				return
			}
			if deviceRefresh != "" {
				http.SetCookie(w, deviceCookie(deviceRefresh, int(DeviceTTL.Seconds()), secure))
			}
			if refresh != "" {
				http.SetCookie(w, sessionCookie(refresh, int(sessionTTL.Seconds()), strings.HasPrefix(cfg.PublicURL, "https://")))
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// verifyDevice resolves a device cookie; slid reports a server-side expiry
// slide (the cookie is then re-issued) when the verifier can tell.
func verifyDevice(ctx context.Context, v DeviceVerifier, cookie string) (Principal, bool, error) {
	if s, ok := v.(deviceSlider); ok {
		return s.verify(ctx, cookie)
	}
	p, err := v.VerifyDevice(ctx, cookie)
	return p, false, err
}

// stepUpChecker is implemented by session verifiers that own a clock.
type stepUpChecker interface{ StepUpFresh(p Principal) bool }

// stepUpFresh reports whether a local human session confirmed its password recently.
func stepUpFresh(sessions SessionVerifier, p Principal) bool {
	if c, ok := sessions.(stepUpChecker); ok {
		return c.StepUpFresh(p)
	}
	return !p.StepUpAt.IsZero() && time.Since(p.StepUpAt) <= stepUpWindow
}

func deviceCookie(value string, maxAge int, secure bool) *http.Cookie {
	return &http.Cookie{Name: DeviceCookie, //nolint:gosec // G124: Secure follows PublicURL (plain-http dev); HttpOnly and SameSite are always set
		Value: value, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}
