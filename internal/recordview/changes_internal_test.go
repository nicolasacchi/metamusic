package recordview

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/records"
)

// TestHideUnseenTakes: for a viewer that hides discarded takes a from/to take the lookup does not know is dropped from a copy of
// the rows; any other viewer gets the rows back untouched (C308.2).
func TestHideUnseenTakes(t *testing.T) {
	t.Parallel()
	known := func(id string) bool { return id == "live" }
	rows := func() []records.Change {
		return []records.Change{{ID: "c1", FromTakeID: sp("gone"), ToTakeID: sp("live")}, {ID: "c2", FromTakeID: sp("live"), ToTakeID: sp("gone")}, {ID: "c3"}}
	}
	as := func(role auth.Role) context.Context {
		return auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "u", User: "u", Name: "u", Role: role, Scopes: auth.ScopesFor(role)})
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want [][2]string // per row: from, to ("" = nil)
	}{
		{"listener", as(auth.RoleListener), [][2]string{{"", "live"}, {"live", ""}, {"", ""}}},
		{"owner", as(auth.RoleOwner), [][2]string{{"gone", "live"}, {"live", "gone"}, {"", ""}}},
		{"bare", context.Background(), [][2]string{{"gone", "live"}, {"live", "gone"}, {"", ""}}},
	} {
		in := rows()
		out := hideUnseenTakes(tc.ctx, in, known)
		for i, c := range out {
			var got [2]string
			if c.FromTakeID != nil {
				got[0] = *c.FromTakeID
			}
			if c.ToTakeID != nil {
				got[1] = *c.ToTakeID
			}
			if got != tc.want[i] {
				t.Errorf("%s row %d = %v, want %v", tc.name, i, got, tc.want[i])
			}
		}
		if in[0].FromTakeID == nil || in[1].ToTakeID == nil {
			t.Errorf("%s: the input rows were mutated", tc.name)
		}
	}
	if !has(map[string]library.TrackRef{"a": {}}, "a") || has(nil, "a") {
		t.Error("has")
	}
}
