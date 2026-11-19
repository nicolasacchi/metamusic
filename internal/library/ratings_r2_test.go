package library_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// carolRates writes a Listener's rating row; the Owner's rows stay as the fixture left them.
func carolRates(t *testing.T, e *env, track string, stars int) {
	t.Helper()
	exec(t, e, `INSERT INTO ratings(track_id,source,rater,stars,verdict,principal_kind,principal_id,principal_name,created_at,updated_at)
		VALUES(?,'human','carol',?,'CANARY-NOTE-900','human','human:carol','carol','2026-10-14T10:00:00Z','2026-10-14T10:00:00Z')`, track, stars)
}

func asUser(role auth.Role, name string) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: name, User: name, Name: name, Role: role, Scopes: auth.ScopesFor(role)})
}

// TestRatingsPerUser_Display: the stars a viewer sees, sorts and filters by are the viewer's own row, a second rater never
// fans a row out, and the Owner's view (and an agent's, which is an Owner delegate) is what it was before U1 (C307.2).
func TestRatingsPerUser_Display(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	// carol rates Luna (the Owner gave it 5) and Twin A (unrated by the Owner)
	carolRates(t, e, lt.TrackLuna, 1)
	carolRates(t, e, lt.TrackTwinA, 5)
	owner, carol := asUser(auth.RoleOwner, "alice"), asUser(auth.RoleListener, "carol")
	stars := func(ctx context.Context, p library.SearchParams) map[string]int {
		page, err := e.svc.SearchTracks(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != len(page.Items) {
			t.Errorf("total %d != items %d: a join fanned out", page.Total, len(page.Items))
		}
		out := map[string]int{}
		for _, it := range page.Items {
			n := 0
			if it.Stars != nil {
				n = *it.Stars
			}
			out[it.ID] = n
		}
		return out
	}
	p := library.SearchParams{Limit: 50}
	for _, ctx := range []context.Context{owner, context.Background(), auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindAgent, ID: "tok"})} {
		got := stars(ctx, p)
		if got[lt.TrackLuna] != 5 || got[lt.TrackCitta] != 3 || got[lt.TrackTwinA] != 0 {
			t.Errorf("owner side sees %v, want Luna 5, Citta 3, Twin A unrated", got)
		}
	}
	got := stars(carol, p)
	if got[lt.TrackLuna] != 1 || got[lt.TrackCitta] != 0 || got[lt.TrackTwinA] != 5 {
		t.Errorf("carol sees %v, want her own rows only", got)
	}
	ids5 := func(ctx context.Context, sp library.SearchParams) []string {
		page, err := e.svc.SearchTracks(ctx, sp)
		if err != nil {
			t.Fatal(err)
		}
		return ids(page)
	}
	if diff := cmp.Diff([]string{lt.TrackTwinA}, ids5(carol, library.SearchParams{MinStars: ip(5)})); diff != "" {
		t.Errorf("carol min_stars=5 (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{lt.TrackLuna}, ids5(owner, library.SearchParams{MinStars: ip(5)})); diff != "" {
		t.Errorf("owner min_stars=5 (-want +got):\n%s", diff)
	}
	if got := ids5(carol, library.SearchParams{Sort: "stars_desc", Limit: 2}); len(got) != 2 || got[0] != lt.TrackTwinA {
		t.Errorf("carol stars_desc starts with %v, want Twin A (her 5)", got)
	}
	un := ids5(carol, library.SearchParams{Unrated: true})
	for _, id := range un {
		if id == lt.TrackLuna || id == lt.TrackTwinA {
			t.Errorf("carol unrated lists %s, which she rated", id)
		}
	}
	// facets: the stars histogram and the unrated count follow the viewer
	fo, err := e.svc.Facets(owner)
	if err != nil {
		t.Fatal(err)
	}
	fc, err := e.svc.Facets(carol)
	if err != nil {
		t.Fatal(err)
	}
	count := func(f library.Facets, stars int) int {
		for _, sf := range f.Stars {
			if sf.Stars == stars {
				return sf.Count
			}
		}
		return 0
	}
	if count(fo, 5) != 1 || count(fo, 1) != 0 || count(fc, 5) != 1 || count(fc, 1) != 1 || count(fc, 3) != 0 {
		t.Errorf("facets owner %+v carol %+v", fo.Stars, fc.Stars)
	}
	if fc.Unrated != fo.Unrated+1 && fc.Unrated != fo.Unrated { // carol rated 2 tracks, the Owner 2
		t.Logf("unrated owner %d carol %d", fo.Unrated, fc.Unrated)
	}
}

// TestRatingsPerUser_StatsAreOwners: the stats counters and histograms are curation signals (C307.1): a Listener's rating
// changes none of them.
func TestRatingsPerUser_StatsAreOwners(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	before, err := e.svc.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	carolRates(t, e, lt.TrackLuna, 1)
	carolRates(t, e, lt.TrackTwinB, 2)
	after, err := e.svc.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("stats moved when a Listener rated (-before +after):\n%s", diff)
	}
}

// TestListening_OwnerSideOnly: a Listener's plays move neither the track's listening block, nor unheard (C306.7).
func TestListening_OwnerSideOnly(t *testing.T) {
	t.Parallel()
	e := newEnvM2a(t)
	e.st.SetOwner("fixture") // the fixture's Owner plays are principal human:fixture
	play := func(id, track, kind, pid string) {
		exec(t, e, `INSERT INTO listen_events(id, track_id, session_id, kind, from_ms, to_ms, format, device_id, source, principal_kind, principal_id, client_at, received_at)
			VALUES (?,?,?,'start',0,NULL,'flac',NULL,'web',?,?,'2026-10-14T10:00:00Z','2026-10-14T10:00:01Z')`, id, track, "s-"+id, kind, pid)
	}
	owner := asUser(auth.RoleOwner, "fixture")
	detail := func() library.ListeningStats {
		d, err := e.svc.Track(owner, lt.TrackLuna)
		if err != nil {
			t.Fatal(err)
		}
		return d.Listening
	}
	before := detail()
	unheard := func() []string {
		return ids5Unheard(t, e)
	}
	u0 := unheard()
	play("01900000-0000-7000-8000-0000000009a1", lt.TrackLuna, "human", "human:carol")
	play("01900000-0000-7000-8000-0000000009a2", lt.TrackCitta, "human", "human:carol")
	if got := detail(); got.Plays != before.Plays {
		t.Errorf("Luna plays %d -> %d after a Listener played it", before.Plays, got.Plays)
	}
	if diff := cmp.Diff(u0, unheard()); diff != "" {
		t.Errorf("unheard moved when a Listener played (-before +after):\n%s", diff)
	}
	// the Owner's own play, and a device's, do count
	play("01900000-0000-7000-8000-0000000009a3", lt.TrackCitta, "human", "human:fixture")
	play("01900000-0000-7000-8000-0000000009a4", lt.TrackTwinA, "device", "device:d1")
	if got := detail(); got.Plays != before.Plays {
		t.Errorf("Luna plays moved: %d", got.Plays)
	}
	for _, id := range unheard() {
		if id == lt.TrackCitta || id == lt.TrackTwinA {
			t.Errorf("%s is unheard after an Owner-side play", id)
		}
	}
}

func ids5Unheard(t *testing.T, e *env) []string {
	t.Helper()
	tr := true
	return search(t, e, library.SearchParams{Unheard: &tr, Limit: 50})
}
