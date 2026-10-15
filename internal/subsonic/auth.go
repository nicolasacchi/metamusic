package subsonic

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

// failGlobalKey is the one bucket key of the global failed-authentication class.
const failGlobalKey = "all"

// clientKey is the per-IP rate-limit key of the request.
func (c *call) clientKey() string {
	ip := reqinfo.ClientIP(c.r, c.s.o.Cfg.TrustedProxies)
	if ip == (netip.Addr{}) {
		return "unknown"
	}
	return ip.String()
}

// authenticate runs the exact order of C108.3 (steps 2 to 6; step 1 is the caller's). The global failure class is
// consulted only after a verification failed, so valid credentials always pass however many failures other
// addresses cause.
func (s *server) authenticate(c *call) (auth.Principal, *apiError) {
	if s.creds == nil {
		return auth.Principal{}, fail(errGeneric, "service unavailable")
	}
	ip := c.clientKey()
	if s.lim != nil {
		if ok, retry := s.lim.Peek(ip, ratelimit.SubsonicAuthFail); !ok {
			return auth.Principal{}, tooMany("too many failed attempts", retry)
		}
	}
	client := c.str("c")
	u, p, t, sl := c.params.Has("u"), c.params.Has("p"), c.params.Has("t"), c.params.Has("s")
	var principal auth.Principal
	var err error
	var aerr *apiError
	switch {
	case c.params.Has("apiKey"):
		if u || p || t || sl {
			return auth.Principal{}, fail(errAuthConflict, "multiple conflicting authentication mechanisms provided")
		}
		principal, err = s.creds.VerifyAPIKey(c.r.Context(), c.str("apiKey"), client)
		if err != nil {
			aerr = fail(errBadAPIKey, "invalid API key")
		}
	case u && t && sl:
		principal, err = s.creds.VerifyToken(c.r.Context(), c.str("u"), c.str("t"), c.str("s"), client, auth.SaltWindowFor(c.name))
		if errors.Is(err, auth.ErrSaltExpired) {
			aerr = fail(errBadCreds, "salt expired: use a new salt")
		} else if err != nil {
			aerr = fail(errBadCreds, "wrong username or password")
		}
	case u && p:
		return auth.Principal{}, fail(errAuthMechanism, "password authentication is not supported; use an API key or token authentication")
	default:
		return auth.Principal{}, fail(errMissingParam, "required parameter is missing: authentication")
	}
	if err == nil {
		return principal, nil
	}
	if !isCredentialFailure(err) { // a store error is not the client's fault: no failure is charged
		s.logger().LogAttrs(c.r.Context(), slog.LevelError, "subsonic auth error", slog.String("err", redact.Error(err)))
		return auth.Principal{}, fail(errGeneric, "internal error")
	}
	return auth.Principal{}, s.failedAuth(c, ip, aerr)
}

func isCredentialFailure(err error) bool {
	return errors.Is(err, auth.ErrSubsonicCredentials) || errors.Is(err, auth.ErrSubsonicAPIKey) || errors.Is(err, auth.ErrSaltExpired)
}

// failedAuth charges one failure to the address and to the global class, then delays the protocol error. A failure
// that finds the global class empty, or no free sleeper slot, is answered 429 at once.
func (s *server) failedAuth(c *call, ip string, aerr *apiError) *apiError {
	if s.lim == nil {
		return aerr
	}
	s.lim.Allow(ip, ratelimit.SubsonicAuthFail)
	if ok, retry, _ := s.lim.Allow(failGlobalKey, ratelimit.SubsonicAuthFailGlobal); !ok {
		return tooMany("too many failed attempts", retry)
	}
	select {
	case s.sleeper <- struct{}{}:
	default:
		return tooMany("too many failed attempts", s.delay)
	}
	defer func() { <-s.sleeper }()
	_ = s.sleepCtx(c.r.Context(), s.delay)
	return aerr
}

func (s *server) sleepCtx(ctx context.Context, d time.Duration) error { return s.sleep(ctx, d) }
