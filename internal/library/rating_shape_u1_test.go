package library_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// TestShapeRating pins the TrackRating shape per viewer (C306.4, D-U1-16): the Owner sees mine, every other user and the
// average; an Editor mine and the average; a Listener, and a Listener's device, only mine; the Owner's device is the Owner;
// an agent sees the critic as mine, the Owner's row in the deprecated human alias and never another user's row.
func TestShapeRating(t *testing.T) {
	t.Parallel()
	i := func(n int) *int { return &n }
	s := func(v string) *string { return &v }
	row := func(stars *int, verdict *string) library.RatingRow {
		return library.RatingRow{Stars: stars, Verdict: verdict, By: auth.PrincipalRef{Kind: "human", ID: "x", Name: "x"}, UpdatedAt: "2026-10-14T10:00:00Z"}
	}
	crit := row(i(2), s("critic note"))
	raw := library.TrackRating{
		TrackID: "t1", Critic: &crit,
		Raters: []library.RaterRow{
			{Rater: "bob", Row: row(i(3), s("CANARY-NOTE-900 bob"))},
			{Rater: "carol", Row: row(i(4), nil)},
			{Rater: "dave", Row: row(nil, nil)}, // a cleared rating: no stars, no verdict, no entry in others
			{Rater: "owner", Row: row(i(5), s("owner verdict"))},
		},
	}
	owner := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleOwner, ID: "alice", User: "alice"}
	ownerDevice := auth.Principal{Kind: auth.KindDevice, ID: "d1"}
	editor := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", User: "bob"}
	listener := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "carol", User: "carol"}
	listenerDevice := auth.Principal{Kind: auth.KindDevice, Role: auth.RoleListener, ID: "d2", User: "carol"}
	agent := auth.Principal{Kind: auth.KindAgent, ID: "tok"}

	stars := func(r *library.RatingRow) int {
		if r == nil || r.Stars == nil {
			return 0
		}
		return *r.Stars
	}
	for _, c := range []struct {
		name                string
		p                   auth.Principal
		mine, human, critic int
		others              []string
		avgN                int
		avg                 float64
	}{
		{name: "owner", p: owner, mine: 5, human: 5, critic: 2, others: []string{"bob", "carol"}, avgN: 3, avg: 4},
		{name: "owner device", p: ownerDevice, mine: 5, human: 5, critic: 2, others: []string{"bob", "carol"}, avgN: 3, avg: 4},
		{name: "editor", p: editor, mine: 3, human: 3, critic: 2, avgN: 3, avg: 4},
		{name: "listener", p: listener, mine: 4, human: 4},
		{name: "listener device", p: listenerDevice, mine: 4, human: 4},
		{name: "agent", p: agent, mine: 2, human: 5, critic: 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := library.ShapeRating(c.p, raw)
			if stars(got.Mine) != c.mine || stars(got.Human) != c.human || stars(got.Critic) != c.critic {
				t.Errorf("mine=%d human=%d critic=%d, want %d %d %d", stars(got.Mine), stars(got.Human), stars(got.Critic), c.mine, c.human, c.critic)
			}
			var names []string
			for _, o := range got.Others {
				names = append(names, o.User)
			}
			if diff := cmp.Diff(c.others, names); diff != "" {
				t.Errorf("others (-want +got):\n%s", diff)
			}
			switch {
			case c.avgN == 0 && got.Average != nil:
				t.Errorf("average %+v, want none", got.Average)
			case c.avgN > 0 && (got.Average == nil || got.Average.N != c.avgN || got.Average.Mean != c.avg):
				t.Errorf("average %+v, want n=%d mean=%v", got.Average, c.avgN, c.avg)
			}
			if got.Raters != nil {
				t.Error("shaped value still carries the raw rows")
			}
			if got.Mine != nil && got.Human != nil && got.Mine == got.Human {
				t.Error("mine and the human alias share one row (masking would apply twice)")
			}
			// a viewer other than the Owner side never sees another user's verdict text, anywhere in the JSON
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if c.others == nil && c.p.Role != auth.RoleEditor && strings.Contains(string(b), "CANARY-NOTE-900") {
				t.Errorf("another user's verdict leaked: %s", b)
			}
			if strings.Contains(string(b), "raters") {
				t.Errorf("raw rows serialised: %s", b)
			}
			if c.p.Role == auth.RoleListener && (strings.Contains(string(b), `"average"`) || strings.Contains(string(b), `"others"`) || strings.Contains(string(b), "critic note")) {
				t.Errorf("listener JSON has average, others or the critic: %s", b)
			}
		})
	}
	// idempotent on a shaped value, and the empty case
	once := library.ShapeRating(owner, raw)
	if diff := cmp.Diff(once, library.ShapeRating(owner, once)); diff != "" {
		t.Errorf("not idempotent:\n%s", diff)
	}
	empty := library.ShapeRating(listener, library.TrackRating{TrackID: "t2"})
	if empty.Mine != nil || empty.Human != nil || empty.Average != nil || empty.TrackID != "t2" {
		t.Errorf("empty = %+v", empty)
	}
}
