package api

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// The Owner sees a rater that is no longer in MM_USERS as a former user; the rating still counts in the average.
func TestMaskRating_FormerUser(t *testing.T) {
	t.Parallel()
	star := func(n int) library.RatingRow { return library.RatingRow{Stars: &n, UpdatedAt: "2026-01-01T00:00:00Z"} }
	raw := func() library.TrackRating {
		return library.TrackRating{TrackID: "t1", Raters: []library.RaterRow{
			{Rater: "owner", Row: star(5)}, {Rater: "bob", Row: star(4)}, {Rater: "dave", Row: star(1)},
		}}
	}
	users := map[string]config.Role{"alice": config.RoleOwner, "bob": config.RoleEditor}
	owner := auth.Principal{Kind: auth.KindHuman, ID: "alice", User: "alice", Role: auth.RoleOwner}

	tr := raw()
	maskRating(owner, users, &tr)
	got := map[string]bool{}
	for _, o := range tr.Others {
		got[o.User] = o.Former
	}
	if len(got) != 2 || got["bob"] || !got["dave"] {
		t.Errorf("former flags = %v, want bob configured and dave former", got)
	}
	if tr.Average == nil || tr.Average.N != 3 {
		t.Errorf("average = %+v, want the former user's stars counted (n=3)", tr.Average)
	}
}

// With no configured users (local/bearer-only setups) nobody can be told apart as former: none is flagged.
func TestMaskRating_NoConfiguredUsersFlagsNobody(t *testing.T) {
	t.Parallel()
	n := 4
	tr := library.TrackRating{TrackID: "t1", Raters: []library.RaterRow{
		{Rater: "owner", Row: library.RatingRow{Stars: &n, UpdatedAt: "2026-01-01T00:00:00Z"}},
		{Rater: "bob", Row: library.RatingRow{Stars: &n, UpdatedAt: "2026-01-01T00:00:00Z"}},
	}}
	owner := auth.Principal{Kind: auth.KindHuman, ID: "alice", User: "alice", Role: auth.RoleOwner}
	maskRating(owner, nil, &tr)
	for _, o := range tr.Others {
		if o.Former {
			t.Errorf("%s flagged former with no configured users", o.User)
		}
	}
}
