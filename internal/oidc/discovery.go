package oidc

import (
	"context"
	"encoding/json"
	"strings"
)

// discover resolves the JWKS URL: the configured one, else the issuer's discovery document (fetched once on
// success). The document's issuer must equal Config.Issuer. Errors are codes.
func (v *Verifier) discover(ctx context.Context) (string, string) {
	if v.cfg.JWKSURL != "" {
		return v.cfg.JWKSURL, ""
	}
	if v.keys.jwksURL != "" {
		return v.keys.jwksURL, ""
	}
	body, code := v.fetch(ctx, strings.TrimRight(v.cfg.Issuer, "/")+"/.well-known/openid-configuration")
	if code != "" {
		return "", "discovery_" + code
	}
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if json.Unmarshal(body, &doc) != nil || doc.JWKSURI == "" {
		return "", "discovery_parse"
	}
	if doc.Issuer != v.cfg.Issuer {
		return "", "issuer_mismatch"
	}
	v.keys.jwksURL = doc.JWKSURI
	return doc.JWKSURI, ""
}
