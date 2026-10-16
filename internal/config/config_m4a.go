package config

import (
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// OIDCClient is one allow-listed OAuth client of the hosted connectors (C102).
type OIDCClient struct{ ID, Label string }

var oidcClientIDRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,100}$`)

// loadM4a reads the hosted-connector settings (C102). Errors name the
// variable, never a value.
func loadM4a(c *Config, getenv func(string) string) error {
	c.OIDCScopes = []string{"openid", "metamusic"}
	if v := getenv("MM_OIDC_SCOPES"); v != "" {
		c.OIDCScopes = strings.Fields(v)
		found := false
		for _, s := range c.OIDCScopes {
			if s == "metamusic" {
				found = true
			}
		}
		if !found {
			return errors.New(`config: MM_OIDC_SCOPES must contain "metamusic"`)
		}
	}
	if v := getenv("MM_OIDC_JWKS_URL"); v != "" {
		if err := checkOIDCURL(v); err != nil {
			return errors.New("config: MM_OIDC_JWKS_URL must be an absolute https URL without query, fragment or credentials (http only for loopback)")
		}
		c.OIDCJWKSURL = v
	}
	iss := getenv("MM_OIDC_ISSUER")
	raw := getenv("MM_OIDC_CLIENTS")
	if iss == "" {
		if raw != "" {
			if _, err := parseOIDCClients(raw); err != nil {
				return err
			}
		}
		return nil
	}
	if err := checkOIDCURL(iss); err != nil {
		return errors.New("config: MM_OIDC_ISSUER must be an absolute https URL without query, fragment or credentials (http only for loopback)")
	}
	c.OIDCIssuer = iss
	clients, err := parseOIDCClients(raw)
	if err != nil {
		return err
	}
	if len(clients) == 0 {
		return errors.New("config: MM_OIDC_CLIENTS needs at least one client when MM_OIDC_ISSUER is set")
	}
	c.OIDCClients = clients
	if len(c.TrustedUsers) == 0 {
		return errors.New("config: MM_TRUSTED_USERS must name at least one user when MM_OIDC_ISSUER is set")
	}
	if err := checkOIDCURL(c.PublicURL); err != nil || strings.Contains(c.PublicURL, "?") {
		return errors.New("config: MM_PUBLIC_URL must be https (http only for loopback) when MM_OIDC_ISSUER is set")
	}
	return nil
}

// checkOIDCURL accepts an absolute https URL without query, fragment or
// userinfo; http only when the host is loopback.
func checkOIDCURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("bad url")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if ip, perr := netip.ParseAddr(u.Hostname()); u.Hostname() == "localhost" || (perr == nil && ip.IsLoopback()) {
			return nil
		}
	}
	return errors.New("bad url scheme")
}

func parseOIDCClients(raw string) ([]OIDCClient, error) {
	bad := errors.New(`config: MM_OIDC_CLIENTS must be "<client_id>=<label>[,…]" (unique ids ^[A-Za-z0-9._~-]{1,100}$, labels 1..80 characters without "," "=" or controls)`)
	var out []OIDCClient
	seen := map[string]bool{}
	for _, p := range splitList(raw) {
		id, label, ok := strings.Cut(p, "=")
		if !ok || !oidcClientIDRE.MatchString(id) || seen[id] {
			return nil, bad
		}
		if n := utf8.RuneCountInString(label); n < 1 || n > 80 || strings.ContainsAny(label, "=,") ||
			strings.IndexFunc(label, unicode.IsControl) >= 0 {
			return nil, bad
		}
		seen[id] = true
		out = append(out, OIDCClient{ID: id, Label: label})
	}
	return out, nil
}
