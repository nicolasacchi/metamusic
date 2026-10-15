package mcpgen

import (
	"strings"
	"testing"
)

func TestDispatch_ClientLabel_OAuth(t *testing.T) {
	t.Parallel()
	r := newGatedRig(t)
	p, _ := r.agent(t)
	p.Via, p.Client = "oauth", "test-claude"
	call(t, r.httpSession(t, p, "claude-ai/1.0 (x)"), "t_create", createArgs("oa", nil))
	rows := r.auditRows(t)
	if len(rows) != 1 || rows[0]["client"] != "oauth:test-claude/claude-ai" {
		t.Fatalf("audit rows %v", rows)
	}
}

func TestOAuthClientLabel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ client, label, want string }{
		{"c1", "claude-ai/1.0 (x)", "oauth:c1/claude-ai"},
		{"c1", "claude-ai", "oauth:c1/claude-ai"},
		{"c1", "", "oauth:c1"},
		{"c1", "  /1.0", "oauth:c1"},
		{"c1", strings.Repeat("a", 300), "oauth:c1/" + strings.Repeat("a", 111)},
	} {
		if got := oauthClientLabel(tc.client, tc.label); got != tc.want {
			t.Errorf("oauthClientLabel(%q,%q) = %q, want %q", tc.client, tc.label, got, tc.want)
		}
	}
}
