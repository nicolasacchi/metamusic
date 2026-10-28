package library_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestSearchTracks_L2Filters: unheard, in_record (the resolved Take of a live slot), sound over take_sounds and crate_id on
// FixtureL2.
func TestSearchTracks_L2Filters(t *testing.T) {
	t.Parallel()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "l2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fx := lt.FixtureL2(t, st, lt.FixedNow)
	svc := library.New(st, nil, func() time.Time { return lt.FixedNow })
	yes, no := true, false
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.W.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mine := map[string]string{}
	for l, id := range fx.Takes {
		mine[id] = l
	}
	labels := func(p library.SearchParams) []string {
		t.Helper()
		p.Status = allStatuses
		p.Limit = 100
		page, err := svc.SearchTracks(context.Background(), p)
		if err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
		out := []string{}
		for _, id := range ids(page) {
			if l, ok := mine[id]; ok {
				out = append(out, l)
			}
		}
		return out
	}
	all := []string{"s1a", "a1", "b1", "b2", "c1", "c2", "e1", "s6a"}

	// unheard: the fixture's device listen of a1; an agent start and a complete do not count
	sameSet(t, "unheard=true", labels(library.SearchParams{Unheard: &yes}), "s1a", "b1", "b2", "c1", "c2", "e1", "s6a")
	sameSet(t, "unheard=false", labels(library.SearchParams{Unheard: &no}), "a1")
	exec(`INSERT INTO listen_events(id,track_id,session_id,kind,from_ms,to_ms,format,device_id,source,principal_kind,principal_id,client_at,received_at)
		VALUES('e-agent',?,'l2-test-session','start',0,0,'flac',NULL,'mcp','agent','agent:x','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z'),
		('e-complete',?,'l2-test-session','complete',0,0,'flac',NULL,'web','human','human:h','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z'),
		('e-human',?,'l2-test-session','start',0,0,'flac',NULL,'web','human','human:h','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`, fx.Takes["s1a"], fx.Takes["b1"], fx.Takes["c2"])
	sameSet(t, "unheard=false after", labels(library.SearchParams{Unheard: &no}), "a1", "c2")

	// in_record: the resolved Take of a live slot. R1: A a1, B b1 (floating, the Picks), C pinned c2, D empty, E e1; R2: s1a, s6a
	sameSet(t, "in_record=true", labels(library.SearchParams{InRecord: &yes}), "a1", "b1", "c2", "e1", "s1a", "s6a")
	sameSet(t, "in_record=false", labels(library.SearchParams{InRecord: &no}), "b2", "c1")
	exec(`UPDATE records SET hidden_at = '2026-01-02T00:00:00.000Z', hidden_by = 'human:h' WHERE id = ?`, fx.Records["R2"])
	sameSet(t, "hidden record", labels(library.SearchParams{InRecord: &yes}), "a1", "b1", "c2", "e1")

	// sound: OR inside a facet, AND across facets, over take_sounds
	sound := func(take, facet, value string) {
		exec(`INSERT INTO take_sounds(track_id,facet,value,origin) VALUES(?,?,?,'declared')`, fx.Takes[take], facet, value)
	}
	sound("s1a", "genre_family", "reggae")
	sound("s1a", "mood", "calm")
	sound("a1", "genre_family", "reggae")
	sound("c1", "genre_family", "dance")
	sameSet(t, "sound reggae", labels(library.SearchParams{Sounds: []string{"genre_family:reggae"}}), "s1a", "a1")
	sameSet(t, "sound reggae or dance", labels(library.SearchParams{Sounds: []string{"genre_family:reggae", "genre_family:dance"}}), "s1a", "a1", "c1")
	sameSet(t, "sound across facets", labels(library.SearchParams{Sounds: []string{"genre_family:reggae", "mood:calm"}}), "s1a")
	sameSet(t, "sound unknown value", labels(library.SearchParams{Sounds: []string{"genre_family:polka"}}))
	for _, bad := range []string{"reggae", "nofacet:x", "genre_family:"} {
		if _, err := svc.SearchTracks(context.Background(), library.SearchParams{Sounds: []string{bad}}); err == nil {
			t.Errorf("sound %q must be refused", bad)
		}
	}

	// crate_id: live membership of a manual Take crate (s1a b1 c2 s6a)
	crate := fx.Crates["manual_take"]
	sameSet(t, "crate_id", labels(library.SearchParams{CrateID: crate}), "s1a", "b1", "c2", "s6a")
	sameSet(t, "crate_id and in_record=false (R2 is hidden)", labels(library.SearchParams{CrateID: crate, InRecord: &no}), "s1a", "s6a")
	exec(`UPDATE crate_items SET removed_at = '2026-01-02T00:00:00.000Z', removed_by = 'human:h' WHERE crate_id = ? AND track_id = ?`, crate, fx.Takes["b1"])
	sameSet(t, "removed member", labels(library.SearchParams{CrateID: crate}), "s1a", "c2", "s6a")
	sameSet(t, "unknown crate", labels(library.SearchParams{CrateID: "nope"}))

	// None matches nothing; the others are untouched
	sameSet(t, "none", labels(library.SearchParams{None: true}))
	if got := labels(library.SearchParams{}); len(got) != len(all) {
		t.Errorf("no filter lists %v, want all of %v", got, all)
	}
	// MatchTrackIDs uses the same predicates
	got, err := svc.MatchTrackIDs(context.Background(), library.SearchParams{Status: allStatuses, CrateID: crate})
	if err != nil || len(got) != 3 {
		t.Errorf("MatchTrackIDs: %v %v", got, err)
	}
}
