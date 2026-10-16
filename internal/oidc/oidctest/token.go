package oidctest

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
)

type refreshTok struct {
	clientID, user, family string
	scopes                 []string
	resource               string
	expires                time.Time
	used                   bool
}

func tokenErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// authenticate enforces exactly the client's configured method; anything else is invalid_client.
func (p *Provider) authenticate(r *http.Request) *Client {
	user, pass, basic := r.BasicAuth()
	bodyID, bodySecret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	var c *Client
	switch {
	case basic:
		c = p.client(user)
	default:
		c = p.client(bodyID)
	}
	if c == nil {
		return nil
	}
	method := c.AuthMethod
	if method == "" {
		method = "none"
		if c.Secret != "" {
			method = "client_secret_basic"
		}
	}
	switch method {
	case "none":
		if basic || bodySecret != "" || bodyID != c.ID {
			return nil
		}
	case "client_secret_post":
		if basic || bodyID != c.ID || bodySecret == "" || bodySecret != c.Secret {
			return nil
		}
	case "client_secret_basic":
		if !basic || bodySecret != "" || user != c.ID || pass != c.Secret || c.Secret == "" {
			return nil
		}
	default:
		return nil
	}
	return c
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if mt := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])); mt != "application/x-www-form-urlencoded" {
		tokenErr(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := r.ParseForm(); err != nil {
		tokenErr(w, http.StatusBadRequest, "invalid_request")
		return
	}
	f := r.PostForm
	rec := Recorded{Endpoint: "token", GrantType: f.Get("grant_type"), Scope: f.Get("scope"), Resource: f.Get("resource")}
	if u, _, ok := r.BasicAuth(); ok {
		rec.ClientID = u
	} else {
		rec.ClientID = f.Get("client_id")
	}
	p.record(rec)
	c := p.authenticate(r)
	if c == nil {
		tokenErr(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	if res := f.Get("resource"); res != "" && !resourceAllowed(res, c.Audience) {
		tokenErr(w, http.StatusBadRequest, "invalid_target")
		return
	}
	switch f.Get("grant_type") {
	case "authorization_code":
		p.grantCode(w, c, f.Get("code"), f.Get("redirect_uri"), f.Get("code_verifier"))
	case "refresh_token":
		p.grantRefresh(w, c, f.Get("refresh_token"))
	default:
		tokenErr(w, http.StatusBadRequest, "unsupported_grant_type")
	}
}

func (p *Provider) grantCode(w http.ResponseWriter, c *Client, code, redirect, verifier string) {
	p.mu.Lock()
	ac := p.codes[code]
	if ac == nil || ac.clientID != c.ID {
		p.mu.Unlock()
		tokenErr(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	if ac.used { // replay: revoke what the code produced
		p.revoked[ac.family] = true
		p.mu.Unlock()
		tokenErr(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	ac.used = true
	if ac.redirect != redirect || !pkceMatches(verifier, ac.challenge) {
		p.mu.Unlock()
		tokenErr(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	p.mu.Unlock()
	p.issue(w, c, ac.user, ac.scopes, ac.resource, ac.family)
}

func (p *Provider) grantRefresh(w http.ResponseWriter, c *Client, tok string) {
	p.mu.Lock()
	rt := p.refresh[tok]
	if rt == nil || rt.clientID != c.ID {
		p.mu.Unlock()
		tokenErr(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	if rt.used || p.revoked[rt.family] { // reuse detection: the whole family dies
		p.revoked[rt.family] = true
		p.mu.Unlock()
		tokenErr(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	if p.opts.Now().After(rt.expires) {
		p.mu.Unlock()
		tokenErr(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	rt.used = true
	p.mu.Unlock()
	p.issue(w, c, rt.user, rt.scopes, rt.resource, rt.family)
}

// issue mints the access token and, with offline_access granted, a rotated refresh token.
func (p *Provider) issue(w http.ResponseWriter, c *Client, user string, scopes []string, resource, family string) {
	aud := c.Audience
	resp := map[string]any{
		"access_token": p.accessToken(c.ID, user, scopes, aud), "token_type": "Bearer",
		"expires_in": int(p.opts.AccessTTL / time.Second), "scope": strings.Join(scopes, " "),
	}
	if slices.Contains(scopes, "offline_access") {
		rt := randID(24)
		p.mu.Lock()
		p.refresh[rt] = &refreshTok{clientID: c.ID, user: user, family: family, scopes: scopes, resource: resource,
			expires: p.opts.Now().Add(p.opts.RefreshTTL)}
		p.mu.Unlock()
		resp["refresh_token"] = rt
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}
