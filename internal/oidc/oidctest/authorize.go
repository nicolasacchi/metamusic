package oidctest

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

type authCode struct {
	clientID, user, redirect, challenge, resource string
	scopes                                        []string
	family                                        string
	used                                          bool
}

// resourceAllowed: the requested resource must equal an allowed audience or sit below it (path prefix).
func resourceAllowed(resource string, aud []string) bool {
	for _, a := range aud {
		if resource == a || strings.HasPrefix(resource, strings.TrimRight(a, "/")+"/") {
			return true
		}
	}
	return false
}

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.record(Recorded{Endpoint: "authorize", ClientID: q.Get("client_id"), Scope: q.Get("scope"), Resource: q.Get("resource")})
	c := p.client(q.Get("client_id"))
	if c == nil || !slices.Contains(c.RedirectURIs, q.Get("redirect_uri")) {
		http.Error(w, "invalid_client or redirect_uri", http.StatusBadRequest) // never redirect to an unverified URI
		return
	}
	redirect := func(params url.Values) {
		u, _ := url.Parse(c.RedirectURIs[slices.Index(c.RedirectURIs, q.Get("redirect_uri"))])
		params.Set("state", q.Get("state"))
		if p.opts.IssParam {
			params.Set("iss", p.URL)
		}
		qq := u.Query()
		for k, v := range params {
			qq[k] = v
		}
		u.RawQuery = qq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	fail := func(code string) { redirect(url.Values{"error": {code}}) }
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type")
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		fail("invalid_request")
		return
	}
	scopes := strings.Fields(q.Get("scope"))
	for _, s := range scopes {
		if !slices.Contains(c.Scopes, s) {
			fail("invalid_scope")
			return
		}
	}
	if res := q.Get("resource"); res != "" && !resourceAllowed(res, c.Audience) {
		fail("invalid_target")
		return
	}
	code := randID(16)
	p.mu.Lock()
	p.codes[code] = &authCode{clientID: c.ID, user: p.opts.LoginAs, redirect: q.Get("redirect_uri"), challenge: q.Get("code_challenge"),
		resource: q.Get("resource"), scopes: scopes, family: randID(6)}
	p.mu.Unlock()
	redirect(url.Values{"code": {code}})
}

func pkceMatches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}
