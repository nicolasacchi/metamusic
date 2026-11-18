package api

import (
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// TestValidateOwnerOnlyRules pins the C302.3 registration rules that lane F1b switches on (enforceOwnerOnlyTags).
func TestValidateOwnerOnlyRules(t *testing.T) {
	t.Parallel()
	op := func(id string) huma.Operation { return huma.Operation{OperationID: id} }
	for _, tc := range []struct {
		name string
		id   string
		m    Meta
		want string
	}{
		{"human only untagged", "x", Meta{HumanOnly: true}, "human_only"},
		{"approve untagged", "x", Meta{Cost: "approve"}, "cost approve"},
		{"admin untagged", "x", Meta{Scope: auth.ScopeAdmin}, "scope admin"},
		{"sensitive read untagged", "get_audit", Meta{Scope: auth.ScopeRead}, "sensitive"},
		{"sensitive read tagged", "get_audit", Meta{Scope: auth.ScopeRead, OwnerOnly: true}, ""},
		{"plain read", "search_tracks", Meta{Scope: auth.ScopeListen}, ""},
		{"human only tagged", "x", Meta{HumanOnly: true, OwnerOnly: true}, ""},
	} {
		err := validateOwnerOnly(op(tc.id), tc.m, true)
		switch {
		case tc.want == "" && err != nil, tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
		if err := validateOwnerOnly(op(tc.id), tc.m, false); err != nil {
			t.Errorf("%s: not enforced, yet %v", tc.name, err)
		}
	}
}
