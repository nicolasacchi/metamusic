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
	"github.com/nicolasacchi/metamusic/internal/redact"
)

type TokenVerifier interface {
	Verify(ctx context.Context, plaintext string) (Principal, error)
}

type SessionVerifier interface {
	VerifySession(ctx context.Context, cookieValue string) (Principal, error)
}

// ConnectorVerifier verifies the access token of a hosted connector (OAuth, M4a C104/C105).
type ConnectorVerifier interface {
	VerifyConnector(ctx context.Context, bearer string) (Principal, error)
}

// Verifiers are the credential checkers the middleware consults. Devices is a nil interface when device
// auth is off, Connectors a nil interface when OAuth connectors are off (MM_OIDC_ISSUER unset); callers
// never pass a typed-nil pointer.
type Verifiers struct {
	Tokens     TokenVerifier
	Sessions   SessionVerifier
	Devices    DeviceVerifier
	Connectors ConnectorVerifier
}

// Connector refusals (C104 row 1b).
var (
	ErrConnectorForbidden = errors.New("auth: connector not allowed")
	ErrConnectorRevoked   = errors.New("auth: connector grant revoked")
	ErrUnavailable        = errors.New("auth: verifier dependency unavailable")
	ErrInsufficientScope  = errors.New("auth: insufficient token scope")
)

// ScopeError reports a token that lacks a required scope; it matches ErrInsufficientScope.
type ScopeError struct{ Scope string }

func (e *ScopeError) Error() string { return "auth: token lacks scope " + e.Scope }

func (e *ScopeError) Is(target error) bool { return target == ErrInsufficientScope }

// maxJWTBytes bounds a bearer before the verifier sees it.
const maxJWTBytes = 8192

func isMCPPath(path string) bool { return path == "/mcp" || strings.HasPrefix(path, "/mcp/") }

// jwtShape is the cheap shape test of row 1b: at most 8192 bytes and exactly two dots.
func jwtShape(tok string) bool {
	return len(tok) <= maxJWTBytes && strings.Count(tok, ".") == 2
}

// connectorChallenge builds the WWW-Authenticate value of a refused connector request.
func connectorChallenge(resource, errCode, scope string) string {
	c := `Bearer error="` + errCode + `"`
	if scope != "" {
		c += `, scope="` + scope + `"`
	}
	return c + `, resource_metadata="` + resource + `"`
}

// connectorRefusal logs and writes one refusal; it never logs the token.
func connectorRefusal(w http.ResponseWriter, status int, code, msg, challenge string, details map[string]any) {
	slog.Warn("connector request refused", "status", status, "code", code)
	if challenge != "" {
		w.Header().Set("WWW-Authenticate", challenge)
	}
	problem.Write(w, status, code, msg, details)
}

// serveConnector handles row 1b and reports whether the request may proceed (with the principal).
func serveConnector(w http.ResponseWriter, r *http.Request, cv ConnectorVerifier, resource, tok string) (Principal, bool) {
	invalid := connectorChallenge(resource, "invalid_token", "")
	if !jwtShape(tok) {
		connectorRefusal(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required", invalid, nil)
		return Principal{}, false
	}
	p, err := cv.VerifyConnector(r.Context(), tok)
	var se *ScopeError
	switch {
	case err == nil:
		return p, true
	case errors.Is(err, ErrInvalidToken):
		connectorRefusal(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required", invalid, nil)
	case errors.As(err, &se):
		connectorRefusal(w, http.StatusForbidden, "INSUFFICIENT_SCOPE", "the token lacks a required scope",
			connectorChallenge(resource, "insufficient_scope", se.Scope), nil)
	case errors.Is(err, ErrConnectorForbidden):
		connectorRefusal(w, http.StatusForbidden, "CONNECTOR_FORBIDDEN", "this connector is not allowed", "", nil)
	case errors.Is(err, ErrConnectorRevoked):
		connectorRefusal(w, http.StatusForbidden, "CONNECTOR_REVOKED", "this connector grant was revoked", "", nil)
	case errors.Is(err, ErrUnavailable):
		connectorRefusal(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the identity provider is unavailable", "", map[string]any{"component": "oidc"})
	default:
		slog.Error("connector verification failed", "err", redact.Error(err))
		writeProblem(w, http.StatusInternalServerError, "INTERNAL", "internal error")
	}
	return Principal{}, false
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
func Middleware(cfg config.Config, sec config.Secrets, v Verifiers) func(http.Handler) http.Handler {
	tokens, sessions, devices := v.Tokens, v.Sessions, v.Devices
	resource := strings.TrimRight(cfg.PublicURL, "/") + "/.well-known/oauth-protected-resource/mcp"
	cop := http.NewCrossOriginProtection()
	secure := strings.HasPrefix(cfg.PublicURL, "https://")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			class := Classify(path)

			// 1. Authorization header present: bearer only.
			if hdr := r.Header.Values("Authorization"); len(hdr) > 0 {
				tok, isBearer := "", false
				if len(hdr) == 1 {
					if scheme, rest, found := strings.Cut(hdr[0], " "); found && strings.EqualFold(scheme, "Bearer") {
						tok, isBearer = rest, true
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
				// 1b. Any other bearer on /mcp: a hosted connector's access token (D-M4a-2).
				if isBearer && !strings.HasPrefix(tok, "mmp_") && v.Connectors != nil && isMCPPath(path) {
					if p, ok := serveConnector(w, r, v.Connectors, resource, tok); ok {
						next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
					}
					return
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
