package view_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/view"
)

func TestFor(t *testing.T) {
	t.Parallel()
	person := func(k auth.Kind, r auth.Role) auth.Principal {
		return auth.Principal{Kind: k, ID: "x", User: "x", Role: r}
	}
	tests := []struct {
		name string
		p    auth.Principal
		hide bool
	}{
		{"owner", person(auth.KindHuman, auth.RoleOwner), false},
		{"editor", person(auth.KindHuman, auth.RoleEditor), false},
		{"listener", person(auth.KindHuman, auth.RoleListener), true},
		{"listener device keys on the role, not the kind", person(auth.KindDevice, auth.RoleListener), true},
		{"editor device", person(auth.KindDevice, auth.RoleEditor), false},
		{"owner device (no role)", person(auth.KindDevice, ""), false},
		{"agent (an Owner delegate)", person(auth.KindAgent, ""), false},
		{"bare context", auth.Principal{}, false},
	}
	for _, tc := range tests {
		v := view.For(tc.p)
		if v.HideDiscarded != tc.hide {
			t.Errorf("%s: HideDiscarded = %v, want %v", tc.name, v.HideDiscarded, tc.hide)
		}
		if got := v.NotDiscarded("t") != ""; got != tc.hide {
			t.Errorf("%s: NotDiscarded fragment present = %v", tc.name, got)
		}
	}
	if got := view.For(person(auth.KindHuman, auth.RoleListener)).NotDiscarded("t"); got != " AND t.status <> 'discarded'" {
		t.Errorf("fragment: %q", got)
	}
	if got := view.For(person(auth.KindHuman, auth.RoleListener)).NotDiscarded(""); got != " AND status <> 'discarded'" {
		t.Errorf("bare fragment: %q", got)
	}
	if got := view.For(person(auth.KindHuman, auth.RoleListener)).NotDiscardedBy("x.track_id"); got != " AND x.track_id NOT IN (SELECT id FROM tracks WHERE status = 'discarded')" {
		t.Errorf("by fragment: %q", got)
	}
	if view.FromContext(context.Background()).HideDiscarded {
		t.Errorf("a bare context hides discarded takes")
	}
	if view.FromContext(auth.WithPrincipal(context.Background(), person(auth.KindHuman, auth.RoleListener))).Listener() == false {
		t.Errorf("FromContext lost the Listener")
	}
}
