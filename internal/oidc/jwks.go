package oidc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type keyCache struct {
	mu            sync.Mutex
	gen           atomic.Int64 // bumped after every successful fetch
	keys          map[string]*rsa.PublicKey
	fetchedAt     time.Time
	lastAttempt   time.Time
	lastUnknown   time.Time
	lastErr       string
	jwksURL       string // discovered
	statusKeys    atomic.Int64
	statusFetched atomic.Pointer[time.Time]
	statusErr     atomic.Pointer[string]
}

// fetch performs one bounded GET. It returns the body or an error code.
func (v *Verifier) fetch(ctx context.Context, rawURL string) ([]byte, string) {
	if !fetchURLOK(rawURL) {
		return nil, "url_refused"
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "request"
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.httpc.Do(req)
	if err != nil {
		return nil, "transport"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 {
			return nil, "http_5xx"
		}
		return nil, fmt.Sprintf("http_%d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return nil, "read"
	}
	if len(b) > maxFetchBytes {
		return nil, "too_large"
	}
	return b, ""
}

// refreshLocked fetches discovery (if needed) and the JWKS. Callers hold kc.mu. On failure the previous keys
// stay; the failure code is recorded.
func (v *Verifier) refreshLocked(ctx context.Context) {
	kc := &v.keys
	kc.lastAttempt = v.now()
	setErr := func(code string) {
		kc.lastErr = code
		kc.statusErr.Store(&code)
		slog.Warn("oidc jwks refresh failed", "code", code)
	}
	u, code := v.discover(ctx)
	if code != "" {
		setErr(code)
		return
	}
	body, code := v.fetch(ctx, u)
	if code != "" {
		setErr("jwks_" + code)
		return
	}
	keys, ok := parseJWKS(body)
	if !ok {
		setErr("jwks_parse")
		return
	}
	kc.keys = keys
	kc.fetchedAt = v.now()
	kc.lastErr = ""
	empty := ""
	kc.statusErr.Store(&empty)
	t := kc.fetchedAt
	kc.statusFetched.Store(&t)
	kc.statusKeys.Store(int64(len(keys)))
	kc.gen.Add(1)
}

// parseJWKS keeps RSA signature keys of at least 2048 bits with e = 65537; everything else is skipped silently.
func parseJWKS(body []byte) (map[string]*rsa.PublicKey, bool) {
	var set struct {
		Keys []struct {
			Kty, Use, Alg, Kid, N, E string
		} `json:"keys"`
	}
	if json.Unmarshal(body, &set) != nil {
		return nil, false
	}
	out := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != Alg) {
			continue
		}
		nb, err1 := base64.RawURLEncoding.DecodeString(k.N)
		eb, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		n, e := new(big.Int).SetBytes(nb), new(big.Int).SetBytes(eb)
		if n.BitLen() < 2048 || !e.IsInt64() || e.Int64() != 65537 {
			continue
		}
		out[k.Kid] = &rsa.PublicKey{N: n, E: 65537}
	}
	return out, true
}

// keyFor returns the key for kid. errUnavailable means no usable keys at all; (nil, nil) means an unknown kid.
// Fetch policy: load when empty, refresh after an hour (stale keys usable up to 24 h), and one refresh per 60 s
// for unknown kids (deduplicated across concurrent callers).
func (v *Verifier) keyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	kc := &v.keys
	gen0 := kc.gen.Load()
	kc.mu.Lock()
	defer kc.mu.Unlock()
	now := v.now()
	fetched := false
	empty := len(kc.keys) == 0
	due := now.Sub(kc.fetchedAt) >= refreshInterval
	if empty || (due && now.Sub(kc.lastAttempt) >= unknownKidGap) {
		v.refreshLocked(ctx)
		fetched = true
	}
	if len(kc.keys) > 0 && now.Sub(kc.fetchedAt) > staleLimit {
		kc.keys = nil
		kc.statusKeys.Store(0)
	}
	if len(kc.keys) == 0 {
		return nil, ErrUnavailable
	}
	if k, ok := kc.keys[kid]; ok {
		return k, nil
	}
	if fetched { // the load itself was the one allowed refresh for this unknown kid
		kc.lastUnknown = now
		return nil, nil
	}
	if kc.gen.Load() != gen0 {
		return nil, nil
	}
	if !kc.lastUnknown.IsZero() && now.Sub(kc.lastUnknown) < unknownKidGap {
		return nil, nil
	}
	kc.lastUnknown = now
	v.refreshLocked(ctx)
	return kc.keys[kid], nil
}

// Prefetch fetches discovery and the JWKS once; run it in a goroutine at startup. It never fails startup.
func (v *Verifier) Prefetch(ctx context.Context) {
	kc := &v.keys
	kc.mu.Lock()
	defer kc.mu.Unlock()
	v.refreshLocked(ctx)
}

// Status returns the JWKS cache state; LastError is a code, never a message.
func (v *Verifier) Status() Status {
	kc := &v.keys
	s := Status{Keys: int(kc.statusKeys.Load()), FetchedAt: kc.statusFetched.Load()}
	if e := kc.statusErr.Load(); e != nil {
		s.LastError = *e
	}
	return s
}
