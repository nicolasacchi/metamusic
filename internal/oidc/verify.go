package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func invalid(code string) error { return fmt.Errorf("%w: %s", ErrInvalid, code) }

// Verify checks token in the exact order of C105: shape, header, key, signature, claims, client allow-list,
// scope, mm_user. Errors carry a short code, never token content.
func (v *Verifier) Verify(ctx context.Context, token string) (Claims, error) {
	// 1. shape
	if len(token) > maxTokenBytes {
		return Claims{}, invalid("shape")
	}
	segs := strings.Split(token, ".")
	if len(segs) != 3 {
		return Claims{}, invalid("shape")
	}
	for _, s := range segs {
		if s == "" || strings.ContainsRune(s, '=') {
			return Claims{}, invalid("shape")
		}
		if _, err := base64.RawURLEncoding.DecodeString(s); err != nil {
			return Claims{}, invalid("shape")
		}
	}
	// 2. header
	hb, _ := base64.RawURLEncoding.DecodeString(segs[0])
	var hdr map[string]any
	if err := json.Unmarshal(hb, &hdr); err != nil || hdr == nil {
		return Claims{}, invalid("header")
	}
	if alg, _ := hdr["alg"].(string); alg != Alg {
		return Claims{}, invalid("alg")
	}
	typ, _ := hdr["typ"].(string)
	if t := strings.ToLower(typ); t != "at+jwt" && t != "application/at+jwt" {
		return Claims{}, invalid("typ")
	}
	kid, _ := hdr["kid"].(string)
	if kid == "" {
		return Claims{}, invalid("kid")
	}
	if _, has := hdr["crit"]; has {
		return Claims{}, invalid("crit")
	}
	// 3. key
	key, err := v.keyFor(ctx, kid)
	if err != nil {
		return Claims{}, err
	}
	if key == nil {
		return Claims{}, invalid("kid_unknown")
	}
	// 4. signature (claims are validated below in the contract order)
	var mc jwt.MapClaims
	parsed, err := jwt.NewParser(jwt.WithValidMethods([]string{Alg}), jwt.WithStrictDecoding(), jwt.WithoutClaimsValidation()).
		ParseWithClaims(token, &mc, func(*jwt.Token) (any, error) { return key, nil })
	if err != nil || !parsed.Valid {
		return Claims{}, invalid("signature")
	}
	// 5. claims
	now := v.now()
	if iss, _ := mc["iss"].(string); iss != v.cfg.Issuer || iss == "" {
		return Claims{}, invalid("iss")
	}
	aud, ok := audience(mc["aud"])
	if !ok || !contains(aud, v.cfg.Resource) {
		return Claims{}, invalid("aud")
	}
	exp, ok := numDate(mc["exp"])
	if !ok || now.After(exp.Add(Leeway)) {
		return Claims{}, invalid("exp")
	}
	iat, ok := numDate(mc["iat"])
	if !ok || iat.After(now.Add(Leeway)) {
		return Claims{}, invalid("iat")
	}
	if raw, has := mc["nbf"]; has {
		nbf, ok := numDate(raw)
		if !ok || nbf.After(now.Add(Leeway)) {
			return Claims{}, invalid("nbf")
		}
	}
	sub, _ := mc["sub"].(string)
	if len(sub) < 1 || len(sub) > 255 {
		return Claims{}, invalid("sub")
	}
	clientID, _ := mc["client_id"].(string)
	if clientID == "" {
		return Claims{}, invalid("client_id")
	}
	// 6. allow-list, before the scope
	if _, ok := v.cfg.Clients[clientID]; !ok {
		return Claims{}, ErrClientUnknown
	}
	// 7. scope
	scopes := scopeUnion(mc)
	if !contains(scopes, RequiredScope) {
		return Claims{}, &MissingScopeError{Scope: RequiredScope}
	}
	// 8. mm_user
	user, _ := mc[UserClaim].(string)
	jti, _ := mc["jti"].(string)
	return Claims{Subject: sub, ClientID: clientID, User: user, JTI: jti, Scopes: scopes, Audience: aud, IssuedAt: iat, Expires: exp}, nil
}

func audience(raw any) ([]string, bool) {
	switch a := raw.(type) {
	case string:
		return []string{a}, a != ""
	case []any:
		out := make([]string, 0, len(a))
		for _, e := range a {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, len(out) > 0
	}
	return nil, false
}

func numDate(raw any) (time.Time, bool) {
	f, ok := raw.(float64)
	if !ok || f < 0 || f > 1<<40 {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

func contains(l []string, s string) bool {
	for _, e := range l {
		if e == s {
			return true
		}
	}
	return false
}

// scopeUnion merges scope (space-delimited string) and scp (string or array), order-preserving, deduplicated.
func scopeUnion(mc jwt.MapClaims) []string {
	var out []string
	add := func(s string) {
		if s != "" && !contains(out, s) {
			out = append(out, s)
		}
	}
	if s, ok := mc["scope"].(string); ok {
		for _, f := range strings.Fields(s) {
			add(f)
		}
	}
	switch p := mc["scp"].(type) {
	case []any:
		for _, e := range p {
			if s, ok := e.(string); ok {
				add(s)
			}
		}
	case string:
		for _, f := range strings.Fields(p) {
			add(f)
		}
	}
	return out
}
