package subsonic

import (
	"context"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// TestCatalogUserRatingIsTheViewersOwn pins the rewritten userRating join (C307.2): the Owner and a second user rate the same
// track differently and each sees only their own stars; a user who has not rated sees none; an Editor or Listener role with
// no user key sees none (it never falls back to the Owner's row); a bare context and the Owner's Subsonic client (a device
// without role) keep seeing the Owner's.
func TestCatalogUserRatingIsTheViewersOwn(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	luna := librarytest.TrackLuna
	at := r.clk.now().UTC().Format(time.RFC3339)
	r.rate(luna, 4) // the Owner's row
	r.exec(`INSERT INTO ratings(track_id, source, stars, principal_kind, principal_id, principal_name, created_at, updated_at, rater) VALUES(?, 'human', 2, 'human', 'human:bob', 'bob', ?, ?, 'bob')`, luna, at, at)

	stars := func(p *auth.Principal) int {
		t.Helper()
		ctx := context.Background()
		if p != nil {
			ctx = auth.WithPrincipal(ctx, *p)
		}
		row, ok, err := r.srv.cat.song(ctx, luna)
		if err != nil || !ok {
			t.Fatalf("song: %v %v", ok, err)
		}
		if !row.Stars.Valid {
			return 0
		}
		return int(row.Stars.Int64)
	}
	person := func(role auth.Role, user string) *auth.Principal {
		return &auth.Principal{Kind: auth.KindHuman, Role: role, ID: user, Name: user, User: user, Scopes: []auth.Scope{auth.ScopeRead}}
	}
	eq(t, "owner", stars(person(auth.RoleOwner, "")), 4)
	eq(t, "editor bob", stars(person(auth.RoleEditor, "bob")), 2)
	eq(t, "listener carol (no rating)", stars(person(auth.RoleListener, "carol")), 0)
	eq(t, "editor without a user key", stars(person(auth.RoleEditor, "")), 0)
	eq(t, "owner's subsonic client", stars(&auth.Principal{Kind: auth.KindDevice, ID: "c1", Via: "subsonic", Scopes: []auth.Scope{auth.ScopeRead}}), 4)
	eq(t, "bare context", stars(nil), 4)
	// the same holds over the wire for the client the rig authenticates (the Owner's), and bob's row never reaches it
	eq(t, "getSong as the Owner's client", dig(t, r.call("getSong", "id", luna), "song", "userRating"), 4)
}
