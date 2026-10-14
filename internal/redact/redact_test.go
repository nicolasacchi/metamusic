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

func TestRedact_M3(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"job api path", `Post "https://api.runpod.ai/v2/abc123/run?x=1": dial tcp 10.0.0.1:443: i/o timeout`,
			`Post "https://api.runpod.ai/v2/<endpoint>/run?<redacted>": dial tcp 10.0.0.1:443: i/o timeout`},
		{"status path", "GET https://api.runpod.ai/v2/ep_1-X/status/0190abcd-0000-7000-8000-000000000001 failed",
			"GET https://api.runpod.ai/v2/<endpoint>/status/0190abcd-0000-7000-8000-000000000001 failed"},
		{"runsync", "/v2/abc123/runsync", "/v2/<endpoint>/runsync"},
		{"cancel and health", "/v2/abc123/cancel /v2/abc123/health", "/v2/<endpoint>/cancel /v2/<endpoint>/health"},
		{"purge-queue", "/v2/abc123/purge-queue", "/v2/<endpoint>/purge-queue"},
		{"bare job host", "dial api.runpod.ai/v2/abc123", "dial api.runpod.ai/v2/<endpoint>"},
		{"management path", "PATCH https://api.runpod.io/v2/endpoints/abc123: 500", "PATCH https://api.runpod.io/v2/endpoints/<endpoint>: 500"},
		{"management list untouched", "GET https://api.runpod.io/v2/endpoints: 500", "GET https://api.runpod.io/v2/endpoints: 500"},
		{"cookie", "Cookie: a=b", "Cookie: <redacted>"},
		{"set-cookie", "got set-cookie: sid=zz; Path=/\nnext", "got set-cookie: <redacted>\nnext"},
		{"unrelated v2 path", "GET /v2/billing/reports ok", "GET /v2/billing/reports ok"},
	} {
		if got := String(tc.in); got != tc.want {
			t.Errorf("%s: String(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestRedact_EndpointIDLiteral(t *testing.T) {
	t.Parallel()
	r := WithEndpoint("xk29fq81a")
	for _, tc := range []struct{ in, want string }{
		{"worker image for xk29fq81a failed", "worker image for <endpoint> failed"},
		{"endpoint=xk29fq81a;xk29fq81a", "endpoint=<endpoint>;<endpoint>"},
		{"https://api.runpod.ai/v2/xk29fq81a/run?sig=1", "https://api.runpod.ai/v2/<endpoint>/run?<redacted>"},
		{"no id here", "no id here"},
	} {
		if got := r.String(tc.in); got != tc.want {
			t.Errorf("String(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := r.Error(errors.New("bad xk29fq81a")); got != "bad <endpoint>" {
		t.Errorf("Error = %q", got)
	}
	if r.Error(nil) != "" {
		t.Error("Error(nil) must be empty")
	}
	// degenerate ids never rewrite ordinary text; the package function is unaffected by any Redactor
	for _, id := range []string{"", "a", "abc"} {
		if got := WithEndpoint(id).String("a cab abc"); got != "a cab abc" {
			t.Errorf("id %q rewrote text: %q", id, got)
		}
	}
	if got := String("xk29fq81a"); got != "xk29fq81a" {
		t.Errorf("package String changed: %q", got)
	}
}
