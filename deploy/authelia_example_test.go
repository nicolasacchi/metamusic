// Package deploy holds tests over the public-safe example files in this directory.
package deploy

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// The repo has no YAML dependency (the routing contract reads labels.example.yml line by line too),
// so this reads the example with a tiny indentation-based subset reader: enough for scalars, lists and
// nested keys as the example is written.

// indent is the effective column of a line's key: the "- " of a list item that opens a mapping
// ("- client_id: x") counts as indentation.
func indent(l string) int {
	n := len(l) - len(strings.TrimLeft(l, " "))
	if strings.HasPrefix(strings.TrimSpace(l), "- ") {
		n += 2
	}
	return n
}

func readExample(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("authelia-oidc.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if tr := strings.TrimSpace(l); tr == "" || strings.HasPrefix(tr, "#") {
			continue
		}
		out = append(out, strings.TrimRight(l, " "))
	}
	return out
}

// child returns the inline value of key and the lines nested under it.
func child(lines []string, key string) (string, []string, bool) {
	if len(lines) == 0 {
		return "", nil, false
	}
	base := indent(lines[0])
	for _, l := range lines {
		if ind := indent(l); ind < base {
			base = ind
		}
	}
	for i, l := range lines {
		tr := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- "))
		if indent(l) != base || !strings.HasPrefix(tr, key+":") {
			continue
		}
		val := strings.Trim(strings.TrimSpace(strings.TrimPrefix(tr, key+":")), `'"`)
		var sub []string
		for _, n := range lines[i+1:] {
			if indent(n) <= base {
				break
			}
			sub = append(sub, n)
		}
		return val, sub, true
	}
	return "", nil, false
}

func path(t *testing.T, lines []string, keys ...string) (string, []string) {
	t.Helper()
	var val string
	for _, k := range keys {
		var ok bool
		if val, lines, ok = child(lines, k); !ok {
			t.Fatalf("missing key %q in path %v", k, keys)
		}
	}
	return val, lines
}

func items(lines []string) []string {
	var out []string
	for _, l := range lines {
		if tr := strings.TrimSpace(l); strings.HasPrefix(tr, "- ") {
			out = append(out, strings.Trim(strings.TrimSpace(tr[2:]), `'"`))
		}
	}
	return out
}

func TestAutheliaExample_Shape(t *testing.T) {
	t.Parallel()
	all := readExample(t)
	_, oidc := path(t, all, "identity_providers", "oidc")

	t.Run("claims policy copies mm_user into the access token", func(t *testing.T) {
		t.Parallel()
		_, p := path(t, oidc, "claims_policies", "metamusic")
		if _, at := path(t, p, "access_token"); !slices.Equal(items(at), []string{"mm_user"}) {
			// inline form: access_token: ['mm_user']
			if v, _ := path(t, p, "access_token"); v != "[mm_user]" && v != "['mm_user']" {
				t.Errorf("access_token claims = %v / %q, want [mm_user]", items(at), v)
			}
		}
		_, c := path(t, p, "custom_claims", "mm_user")
		if v, _ := path(t, c, "attribute"); v != "username" {
			t.Errorf("mm_user attribute = %q, want username", v)
		}
	})

	t.Run("scope and authorization policy", func(t *testing.T) {
		t.Parallel()
		if v, _ := path(t, oidc, "scopes", "metamusic", "claims"); v != "[mm_user]" && v != "['mm_user']" {
			_, cl := path(t, oidc, "scopes", "metamusic", "claims")
			if !slices.Equal(items(cl), []string{"mm_user"}) {
				t.Errorf("scope metamusic claims = %q %v, want [mm_user]", v, items(cl))
			}
		}
		_, pol := path(t, oidc, "authorization_policies", "metamusic_owner")
		if v, _ := path(t, pol, "default_policy"); v != "deny" {
			t.Errorf("default_policy = %q, want deny", v)
		}
		joined := strings.Join(pol, "\n")
		for _, want := range []string{"subject: 'user:owner'", "policy: 'two_factor'"} {
			if !strings.Contains(joined, want) {
				t.Errorf("authorization policy lacks %q", want)
			}
		}
	})

	t.Run("lifespans", func(t *testing.T) {
		t.Parallel()
		_, l := path(t, oidc, "lifespans", "custom", "metamusic")
		if v, _ := path(t, l, "access_token"); v != "1h" {
			t.Errorf("access_token lifespan = %q, want 1h", v)
		}
		if v, _ := path(t, l, "refresh_token"); v != "30d" {
			t.Errorf("refresh_token lifespan = %q, want 30d", v)
		}
	})

	_, cl := path(t, oidc, "clients")
	var clients [][]string
	for _, l := range cl {
		if strings.HasPrefix(strings.TrimSpace(l), "- client_id:") {
			clients = append(clients, nil)
		}
		if len(clients) > 0 {
			clients[len(clients)-1] = append(clients[len(clients)-1], l)
		}
	}
	byID := map[string][]string{}
	for _, c := range clients {
		id, _ := path(t, c, "client_id")
		byID[id] = c
	}
	for _, id := range []string{"metamusic-claude", "metamusic-chatgpt", "metamusic-probe"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("client %s missing; have %d clients", id, len(byID))
		}
	}
	if len(byID) != 3 {
		t.Errorf("%d clients, want 3", len(byID))
	}

	for id, c := range byID {
		t.Run("client "+id, func(t *testing.T) {
			t.Parallel()
			scalars := map[string]string{
				"requested_audience_mode":          "implicit",
				"access_token_signed_response_alg": "RS256",
				"require_pkce":                     "true",
				"pkce_challenge_method":            "S256",
				"consent_mode":                     "explicit",
				"authorization_policy":             "metamusic_owner",
				"claims_policy":                    "metamusic",
				"lifespan":                         "metamusic",
			}
			for k, want := range scalars {
				if v, _ := path(t, c, k); v != want {
					t.Errorf("%s = %q, want %q", k, v, want)
				}
			}
			if _, _, ok := child(c, "pre_configured_consent_duration"); ok {
				t.Error("pre_configured_consent_duration must be absent (explicit consent on every authorization)")
			}
			for k, want := range map[string][]string{
				"audience":       {"https://mm.example.com/mcp"},
				"scopes":         {"openid", "offline_access", "metamusic"},
				"grant_types":    {"authorization_code", "refresh_token"},
				"response_types": {"code"},
			} {
				if _, sub := path(t, c, k); !slices.Equal(items(sub), want) {
					t.Errorf("%s = %v, want %v", k, items(sub), want)
				}
			}
			pub, _ := path(t, c, "public")
			method, _, hasMethod := child(c, "token_endpoint_auth_method")
			switch id {
			case "metamusic-chatgpt":
				if pub == "true" || method != "client_secret_post" {
					t.Errorf("confidential client: public=%q method=%q, want client_secret_post", pub, method)
				}
				if v, _ := path(t, c, "client_secret"); !strings.Contains(v, "DIGEST") && !strings.Contains(v, "digest") {
					t.Errorf("client_secret must be a digest placeholder, got %q", v)
				}
			default:
				if pub != "true" || !hasMethod || method != "none" {
					t.Errorf("public client: public=%q method=%q, want true/none", pub, method)
				}
				if _, _, ok := child(c, "client_secret"); ok {
					t.Error("public client must not carry a client_secret")
				}
			}
		})
	}

	t.Run("redirects", func(t *testing.T) {
		t.Parallel()
		want := map[string]string{
			"metamusic-claude":  "https://claude.ai/api/mcp/auth_callback",
			"metamusic-chatgpt": "https://chatgpt.com/connector_platform_oauth_redirect",
			"metamusic-probe":   "http://127.0.0.1:8765/callback",
		}
		for id, r := range want {
			_, sub := path(t, byID[id], "redirect_uris")
			if !slices.Equal(items(sub), []string{r}) {
				t.Errorf("%s redirect_uris = %v, want [%s]", id, items(sub), r)
			}
		}
	})

	t.Run("public safe", func(t *testing.T) {
		t.Parallel()
		for _, l := range all {
			for _, bad := range []string{"-----BEGIN", "$pbkdf2", "$argon2"} {
				if strings.Contains(l, bad) {
					t.Errorf("example carries key or hash material: %q", l)
				}
			}
		}
	})
}
