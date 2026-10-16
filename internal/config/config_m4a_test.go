package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestConfig_OIDC(t *testing.T) {
	t.Parallel()
	c, err := Load(env(map[string]string{"MM_AUTH": "local"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.OIDCIssuer != "" || c.OIDCJWKSURL != "" || len(c.OIDCClients) != 0 ||
		!reflect.DeepEqual(c.OIDCScopes, []string{"openid", "metamusic"}) {
		t.Fatalf("defaults: %+v", c)
	}

	base := map[string]string{
		"MM_OIDC_ISSUER":   "https://auth.example.com",
		"MM_OIDC_CLIENTS":  "a=Claude",
		"MM_TRUSTED_USERS": "alice",
		"MM_PUBLIC_URL":    "https://mm.example.com",
	}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == "-" {
				delete(m, kv[i])
			} else {
				m[kv[i]] = kv[i+1]
			}
		}
		return m
	}
	type tc struct {
		name    string
		env     map[string]string
		wantErr string
	}
	cases := []tc{
		{"base ok", with(), ""},
		{"http issuer", with("MM_OIDC_ISSUER", "http://auth.example.com"), "MM_OIDC_ISSUER"},
		{"loopback issuer", with("MM_OIDC_ISSUER", "http://127.0.0.1:9/"), ""},
		{"issuer query", with("MM_OIDC_ISSUER", "https://auth.example.com/?x=1"), "MM_OIDC_ISSUER"},
		{"issuer fragment", with("MM_OIDC_ISSUER", "https://auth.example.com/#x"), "MM_OIDC_ISSUER"},
		{"issuer userinfo", with("MM_OIDC_ISSUER", "https://u:p@auth.example.com"), "MM_OIDC_ISSUER"},
		{"issuer relative", with("MM_OIDC_ISSUER", "auth.example.com"), "MM_OIDC_ISSUER"},
		{"jwks http", with("MM_OIDC_JWKS_URL", "http://auth.example.com/jwks"), "MM_OIDC_JWKS_URL"},
		{"jwks ok", with("MM_OIDC_JWKS_URL", "https://auth.example.com/jwks.json"), ""},
		{"two clients", with("MM_OIDC_CLIENTS", "a=Claude,b=ChatGPT"), ""},
		{"no clients", with("MM_OIDC_CLIENTS", "-"), "MM_OIDC_CLIENTS"},
		{"duplicate id", with("MM_OIDC_CLIENTS", "a=Claude,a=Other"), "MM_OIDC_CLIENTS"},
		{"empty label", with("MM_OIDC_CLIENTS", "a="), "MM_OIDC_CLIENTS"},
		{"label with eq", with("MM_OIDC_CLIENTS", "a=Cl=aude"), "MM_OIDC_CLIENTS"},
		{"bad id", with("MM_OIDC_CLIENTS", "a b=Claude"), "MM_OIDC_CLIENTS"},
		{"no equals", with("MM_OIDC_CLIENTS", "abc"), "MM_OIDC_CLIENTS"},
		{"scopes lack metamusic", with("MM_OIDC_SCOPES", "openid"), "MM_OIDC_SCOPES"},
		{"scopes offline", with("MM_OIDC_SCOPES", "openid offline_access metamusic"), ""},
		{"no trusted users", with("MM_TRUSTED_USERS", "-"), "MM_TRUSTED_USERS"},
		{"http public", with("MM_PUBLIC_URL", "http://mm.example.com"), "MM_PUBLIC_URL"},
		{"loopback public", with("MM_PUBLIC_URL", "http://127.0.0.1:41234"), ""},
	}
	for _, tc := range cases {
		for _, offline := range []bool{false, true} {
			e := map[string]string{"MM_AUTH": "local"}
			for k, v := range tc.env {
				e[k] = v
			}
			var err error
			if offline {
				_, err = LoadOffline(env(e))
			} else {
				_, err = Load(env(e))
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("%s (offline=%v): unexpected error %v", tc.name, offline, err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("%s (offline=%v): want error naming %s", tc.name, offline, tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("%s (offline=%v): error %q does not name %s", tc.name, offline, err, tc.wantErr)
			}
		}
	}

	got, err := Load(env(with("MM_AUTH", "local", "MM_OIDC_CLIENTS", "a=Claude,b=ChatGPT")))
	if err != nil {
		t.Fatal(err)
	}
	want := []OIDCClient{{"a", "Claude"}, {"b", "ChatGPT"}}
	if !reflect.DeepEqual(got.OIDCClients, want) {
		t.Fatalf("clients = %+v", got.OIDCClients)
	}
}

func TestConfig_OIDCNoAlgVariable(t *testing.T) {
	t.Parallel()
	// MM_OIDC_ALG is not a variable: setting it changes nothing and never errors.
	if _, err := Load(env(map[string]string{"MM_AUTH": "local", "MM_OIDC_ALG": "ES256"})); err != nil {
		t.Fatal(err)
	}
}

func TestConfig_OIDCErrorsNeverEchoValues(t *testing.T) {
	t.Parallel()
	_, err := Load(env(map[string]string{
		"MM_AUTH": "local", "MM_OIDC_ISSUER": "https://user:secretpass@auth.example.com",
		"MM_OIDC_CLIENTS": "a=x", "MM_TRUSTED_USERS": "alice", "MM_PUBLIC_URL": "https://mm.example.com",
	}))
	if err == nil || strings.Contains(err.Error(), "secretpass") {
		t.Fatalf("err = %v", err)
	}
	_, err = Load(env(map[string]string{
		"MM_AUTH": "local", "MM_OIDC_ISSUER": "https://auth.example.com",
		"MM_OIDC_CLIENTS": "sekritid=bad=label", "MM_TRUSTED_USERS": "alice", "MM_PUBLIC_URL": "https://mm.example.com",
	}))
	if err == nil || strings.Contains(err.Error(), "sekritid") || strings.Contains(err.Error(), "bad=label") {
		t.Fatalf("err = %v", err)
	}
}
