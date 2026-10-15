package oidc

import (
	"net"
	"net/http"
	"net/url"
	"time"
)

const (
	maxTokenBytes   = 8192
	maxFetchBytes   = 64 << 10
	fetchTimeout    = 5 * time.Second
	refreshInterval = time.Hour
	staleLimit      = 24 * time.Hour
	unknownKidGap   = 60 * time.Second
)

// Verifier verifies access tokens. It is safe for concurrent use.
type Verifier struct {
	cfg   Config
	httpc *http.Client
	now   func() time.Time
	keys  keyCache
}

// NewVerifier builds a verifier; it never fetches. A nil httpc or now means the default client and time.Now.
// The injected client is never mutated (redirects are refused on a copy).
func NewVerifier(c Config, httpc *http.Client, now func() time.Time) (*Verifier, error) {
	if now == nil {
		now = time.Now
	}
	if httpc == nil {
		httpc = &http.Client{}
	}
	cp := *httpc
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Verifier{cfg: c, httpc: &cp, now: now}, nil
}

// fetchURLOK reports whether u may be fetched: https, or http on a loopback host.
func fetchURLOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
