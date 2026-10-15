package redact

import (
	"strings"
	"testing"
)

func TestRedact_M4aPatterns(t *testing.T) {
	t.Parallel()
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"
	for _, tc := range []struct{ name, in, want string }{
		{"subsonic key", "key mms_" + strings.Repeat("a2b3", 13) + " end", "key mms_<redacted> end"},
		{"jwt", "token " + jwt + " rejected", "token <jwt> rejected"},
		{"jwt empty signature", "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0. x", "<jwt> x"},
		{"jwt in bearer", "Bearer " + jwt, "Bearer <redacted>"},
		{"form t s", "t=0123abcd&s=salt99&u=bob", "t=<redacted>&s=<redacted>&u=bob"},
		{"form p", "u=bob&p=hunter2", "u=bob&p=<redacted>"},
		{"form apiKey", "u=bob&apiKey=mms_short", "u=bob&apiKey=<redacted>"},
		{"form oauth", "grant_type=authorization_code&code=abc&code_verifier=xyz&client_secret=s3", "grant_type=authorization_code&code=<redacted>&code_verifier=<redacted>&client_secret=<redacted>"},
		{"form tokens", "refresh_token=r1 access_token=a1", "refresh_token=<redacted> access_token=<redacted>"},
		{"unrelated kept", "format=json&v=1.16.1", "format=json&v=1.16.1"},
		{"word suffix kept", "dest=home part=2 post=1", "dest=home part=2 post=1"},
		{"plain kept", "no secrets here", "no secrets here"},
	} {
		if got := String(tc.in); got != tc.want {
			t.Errorf("%s: String(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
