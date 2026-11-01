package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// This file is the relying-party side used by the proxy-mode step-up: an authorization-code flow with PKCE
// against the issuer the app already trusts, whose ID token proves a recent second factor. It shares the
// Verifier's key cache, HTTP client (no redirects) and clock.

// ErrStepUp wraps every step-up failure; the message after the colon is a short code, never token content.
var ErrStepUp = errors.New("oidc: step-up refused")

func stepErr(code string) error { return fmt.Errorf("%w: %s", ErrStepUp, code) }

// SecondFactorAMR are the RFC 8176 authentication method values that prove a second factor ("mfa" is what Authelia
// adds when more than one factor was used; the others name the factor itself).
var SecondFactorAMR = []string{"mfa", "otp", "hwk", "swk"}

// StepUpConfig configures the client. ClientSecret "" means a public client (PKCE only).
type StepUpConfig struct {
	ClientID, ClientSecret string
	RedirectURL            string
	MaxAge                 time.Duration // sent as max_age and enforced against auth_time
}

// StepUpClient runs the flow. Safe for concurrent use.
type StepUpClient struct {
	v   *Verifier
	cfg StepUpConfig
	mu  sync.Mutex
	ep  *endpoints
}

type endpoints struct{ authorize, token string }

// NewStepUpClient builds a client over v, whose Config.Issuer is the issuer. It never fetches.
func NewStepUpClient(v *Verifier, cfg StepUpConfig) *StepUpClient {
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 5 * time.Minute
	}
	return &StepUpClient{v: v, cfg: cfg}
}

// PKCEChallenge is the S256 challenge of a verifier.
func PKCEChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (c *StepUpClient) endpoints(ctx context.Context) (*endpoints, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ep != nil {
		return c.ep, nil
	}
	body, code := c.v.fetch(ctx, strings.TrimRight(c.v.cfg.Issuer, "/")+"/.well-known/openid-configuration")
	if code != "" {
		return nil, stepErr("discovery_" + code)
	}
	var doc struct {
		Issuer string `json:"issuer"`
		Authz  string `json:"authorization_endpoint"`
		Token  string `json:"token_endpoint"`
	}
	if json.Unmarshal(body, &doc) != nil || doc.Authz == "" || doc.Token == "" {
		return nil, stepErr("discovery_parse")
	}
	if doc.Issuer != c.v.cfg.Issuer {
		return nil, stepErr("issuer_mismatch")
	}
	if !fetchURLOK(doc.Token) || !fetchURLOK(doc.Authz) {
		return nil, stepErr("endpoint_refused")
	}
	c.ep = &endpoints{authorize: doc.Authz, token: doc.Token}
	return c.ep, nil
}

// AuthURL is where the browser is sent. prompt=login and max_age make the provider ask again.
func (c *StepUpClient) AuthURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	ep, err := c.endpoints(ctx)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(ep.authorize)
	if err != nil {
		return "", stepErr("endpoint_refused")
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", c.cfg.ClientID)
	q.Set("redirect_uri", c.cfg.RedirectURL)
	q.Set("scope", "openid profile")
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", PKCEChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("prompt", "login")
	q.Set("max_age", fmt.Sprint(int(c.cfg.MaxAge.Seconds())))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Exchange trades the authorization code for the ID token.
func (c *StepUpClient) Exchange(ctx context.Context, code, verifier string) (string, error) {
	ep, err := c.endpoints(ctx)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.cfg.RedirectURL},
		"code_verifier": {verifier},
	}
	if c.cfg.ClientSecret == "" {
		form.Set("client_id", c.cfg.ClientID)
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", stepErr("request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.cfg.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(c.cfg.ClientID), url.QueryEscape(c.cfg.ClientSecret))
	}
	resp, err := c.v.httpc.Do(req)
	if err != nil {
		return "", stepErr("token_transport")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", stepErr(fmt.Sprintf("token_http_%d", resp.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil || len(b) > maxFetchBytes {
		return "", stepErr("token_read")
	}
	var tr struct {
		IDToken string `json:"id_token"`
	}
	if json.Unmarshal(b, &tr) != nil || tr.IDToken == "" {
		return "", stepErr("token_parse")
	}
	return tr.IDToken, nil
}

// IDClaims are the verified claims the step-up cares about.
type IDClaims struct {
	Subject, User string
	AuthTime      time.Time
	AMR           []string
}

// VerifyIDToken checks signature (RS256 only), iss, aud (and azp), exp/iat, nonce, a recent auth_time, a
// second-factor amr and that preferred_username is wantUser (the proxy-authenticated user).
func (c *StepUpClient) VerifyIDToken(ctx context.Context, token, nonce, wantUser string) (IDClaims, error) {
	if len(token) > maxTokenBytes || strings.Count(token, ".") != 2 {
		return IDClaims{}, stepErr("shape")
	}
	hb, err := base64.RawURLEncoding.DecodeString(strings.SplitN(token, ".", 2)[0])
	var hdr map[string]any
	if err != nil || json.Unmarshal(hb, &hdr) != nil || hdr == nil {
		return IDClaims{}, stepErr("header")
	}
	if alg, _ := hdr["alg"].(string); alg != Alg {
		return IDClaims{}, stepErr("alg")
	}
	if _, has := hdr["crit"]; has {
		return IDClaims{}, stepErr("crit")
	}
	kid, _ := hdr["kid"].(string)
	if kid == "" {
		return IDClaims{}, stepErr("kid")
	}
	key, err := c.v.keyFor(ctx, kid)
	if err != nil {
		return IDClaims{}, stepErr("keys_unavailable")
	}
	if key == nil {
		return IDClaims{}, stepErr("kid_unknown")
	}
	var mc jwt.MapClaims
	parsed, err := jwt.NewParser(jwt.WithValidMethods([]string{Alg}), jwt.WithStrictDecoding(), jwt.WithoutClaimsValidation()).
		ParseWithClaims(token, &mc, func(*jwt.Token) (any, error) { return key, nil })
	if err != nil || !parsed.Valid {
		return IDClaims{}, stepErr("signature")
	}
	now := c.v.now()
	if iss, _ := mc["iss"].(string); iss == "" || iss != c.v.cfg.Issuer {
		return IDClaims{}, stepErr("iss")
	}
	aud, ok := audience(mc["aud"])
	if !ok || !contains(aud, c.cfg.ClientID) {
		return IDClaims{}, stepErr("aud")
	}
	if azp, has := mc["azp"]; has && azp != c.cfg.ClientID {
		return IDClaims{}, stepErr("azp")
	}
	if len(aud) > 1 && mc["azp"] != c.cfg.ClientID {
		return IDClaims{}, stepErr("azp")
	}
	exp, ok := numDate(mc["exp"])
	if !ok || now.After(exp.Add(Leeway)) {
		return IDClaims{}, stepErr("exp")
	}
	iat, ok := numDate(mc["iat"])
	if !ok || iat.After(now.Add(Leeway)) {
		return IDClaims{}, stepErr("iat")
	}
	if got, _ := mc["nonce"].(string); nonce == "" || got != nonce {
		return IDClaims{}, stepErr("nonce")
	}
	authTime, ok := numDate(mc["auth_time"])
	if !ok {
		return IDClaims{}, stepErr("auth_time_missing")
	}
	if now.Sub(authTime) > c.cfg.MaxAge+Leeway || authTime.After(now.Add(Leeway)) {
		return IDClaims{}, stepErr("auth_time_stale")
	}
	var amr []string
	if raw, ok := mc["amr"].([]any); ok {
		for _, e := range raw {
			if s, ok := e.(string); ok {
				amr = append(amr, s)
			}
		}
	}
	second := false
	for _, m := range SecondFactorAMR {
		second = second || contains(amr, m)
	}
	if !second {
		return IDClaims{}, stepErr("amr_no_second_factor")
	}
	user, _ := mc["preferred_username"].(string)
	if user == "" || user != wantUser {
		return IDClaims{}, stepErr("user_mismatch")
	}
	sub, _ := mc["sub"].(string)
	return IDClaims{Subject: sub, User: user, AuthTime: authTime, AMR: amr}, nil
}
