package reqinfo

import (
	"context"
	"strings"
	"testing"
)

func TestSanitizeClient(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"first product token", "claude-code/2.1.80 (linux)", "claude-code/2.1.80"},
		{"control bytes dropped", "cu\x00rl/\x077.1\r\n", "curl/7.1"},
		{"long token capped", strings.Repeat("a", 300), strings.Repeat("a", 120)},
		{"empty", "", ""},
		{"only spaces", "   ", ""},
		{"non ascii dropped", "mü/1", "m/1"},
		{"leading space", "  agent/1 x", "agent/1"},
	} {
		if got := SanitizeClient(tc.in); got != tc.want {
			t.Errorf("%s: SanitizeClient(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if _, ok := TransportFrom(ctx); ok {
		t.Fatal("transport present on empty context")
	}
	if Client(ctx) != "" {
		t.Fatal("client present on empty context")
	}
	ctx = WithTransport(ctx, "mcp")
	ctx = WithClient(ctx, "x/1 (evil\x00)")
	if tr, ok := TransportFrom(ctx); !ok || tr != "mcp" {
		t.Fatalf("transport = %q, %v", tr, ok)
	}
	if got := Client(ctx); got != "x/1" {
		t.Fatalf("client = %q (must be sanitised on entry)", got)
	}
}

func TestTransport_Runner(t *testing.T) {
	t.Parallel()
	ctx := WithTransport(context.Background(), Runner)
	if tr, ok := TransportFrom(ctx); !ok || tr != "runner" {
		t.Fatalf("transport = %q, %v", tr, ok)
	}
}
