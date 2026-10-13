package redact

import (
	"errors"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	tok := "mmp_" + strings.Repeat("a2b3", 13)
	for _, tc := range []struct{ name, in, want string }{
		{"agent token", "call failed for " + tok + " now", "call failed for mmp_<redacted> now"},
		{"device cookie", "mmd_" + strings.Repeat("q", 52), "mmd_<redacted>"},
		{"connector", "mmc_" + strings.Repeat("z", 52), "mmc_<redacted>"},
		{"bearer", "Bearer abc.def", "Bearer <redacted>"},
		{"bearer lower", "header bearer abc.def end", "header bearer <redacted> end"},
		{"authorization", "Authorization: Basic Zm9vOmJhcg==", "Authorization: <redacted>"},
		{"url query", "GET https://h.example/x?exp=1&sig=abc failed", "GET https://h.example/x?<redacted> failed"},
		{"hex run", strings.Repeat("0123456789abcdef", 4), "<redacted>"},
		{"base64url run", strings.Repeat("Ab-_", 8), "<redacted>"},
		{"plain error", "dial tcp: connection refused", "dial tcp: connection refused"},
		{"short hex kept", "id 0123456789abcdef", "id 0123456789abcdef"},
		{"uuid kept", "track 01900000-0000-7000-8000-000000000101", "track 01900000-0000-7000-8000-000000000101"},
	} {
		if got := String(tc.in); got != tc.want {
			t.Errorf("%s: String(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	if Error(nil) != "" {
		t.Error("Error(nil) must be empty")
	}
	if got := Error(errors.New("Post https://x.example/p?key=secret: boom")); got != "Post https://x.example/p?<redacted> boom" {
		t.Errorf("Error = %q", got)
	}
}
