package songs_test

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// l2w is FixtureL2 (SQL-only, not settled) with a Song service over it.
type l2w struct {
	t   *testing.T
	st  *store.Store
	fx  librarytest.L2Info
	svc *songs.Service
}

func newL2w(t *testing.T) *l2w {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "l2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := 0
	newID := func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd2e000+n) }
	return &l2w{t: t, st: st, fx: librarytest.FixtureL2(t, st, librarytest.FixedNow), svc: songs.New(st, func() time.Time { return librarytest.FixedNow }, newID)}
}

func (w *l2w) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.st.W.Exec(q, args...); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
}

// labels lists the fixture Songs S1..S7 that f matches (the L1 mirror's own Songs are ignored).
func (w *l2w) labels(f songs.ListFilter) []string {
	w.t.Helper()
	f.Limit = 100
	p, err := w.svc.List(context.Background(), f)
	if err != nil {
		w.t.Fatalf("%+v: %v", f, err)
	}
	byID := map[string]string{}
	for l, id := range w.fx.Songs {
		byID[id] = l
	}
	var out []string
	for _, it := range p.Items {
		if l, ok := byID[it.ID]; ok {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}

func eq(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

// dropPickGuard lets a test set any Pick source directly (the trigger that demands a matching event is the only guard).
func (w *l2w) dropPickGuard() { w.exec(`DROP TRIGGER song_picks_event_required`) }

func TestSongs_PickDecided(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	yes, no := true, false
	// the fixture: S1-S3 newest, S4 and S6 stars, S5 pinned, S7 has no Pick row
	eq(t, "pick_decided=true", w.labels(songs.ListFilter{PickDecided: &yes}), "S5")
	eq(t, "pick_decided=false", w.labels(songs.ListFilter{PickDecided: &no}), "S1", "S2", "S3", "S4", "S6", "S7")
	w.dropPickGuard()
	for _, tc := range []struct {
		source  string
		decided bool
	}{{"pinned", true}, {"ladder", true}, {"stars", false}, {"newest", false}} {
		pin := ""
		if tc.source == "pinned" {
			pin = "yes"
		}
		w.exec(`UPDATE song_picks SET source = ?, pinned_take_id = CASE WHEN ? = '' THEN NULL ELSE take_id END,
			pinned_by = CASE WHEN ? = '' THEN NULL ELSE 'human:h' END, pinned_at = CASE WHEN ? = '' THEN NULL ELSE '2026-01-01T00:00:00.000Z' END WHERE song_id = ?`,
			tc.source, pin, pin, pin, w.fx.Songs["S1"])
		in := slices.Contains(w.labels(songs.ListFilter{PickDecided: &yes}), "S1")
		out := slices.Contains(w.labels(songs.ListFilter{PickDecided: &no}), "S1")
		if in != tc.decided || out == tc.decided {
			t.Errorf("source %s: decided=true matches %v, decided=false matches %v", tc.source, in, out)
		}
	}
}

// TestSongs_HasPick_Unchanged: has_pick keeps its shipped meaning, a Pick row with a take of any source.
func TestSongs_HasPick_Unchanged(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	yes, no := true, false
	eq(t, "has_pick=true", w.labels(songs.ListFilter{HasPick: &yes}), "S1", "S2", "S3", "S4", "S5", "S6")
	eq(t, "has_pick=false", w.labels(songs.ListFilter{HasPick: &no}), "S7")
	w.dropPickGuard()
	for _, source := range []string{"pinned", "ladder", "stars", "newest"} {
		w.exec(`UPDATE song_picks SET source = ?, pinned_take_id = take_id, pinned_by = 'human:h', pinned_at = '2026-01-01T00:00:00.000Z' WHERE song_id = ?`, source, w.fx.Songs["S2"])
		if !slices.Contains(w.labels(songs.ListFilter{HasPick: &yes}), "S2") {
			t.Errorf("source %s: has_pick=true lost the Song", source)
		}
	}
	// a Pick row without a take (source none) is not a Pick
	w.exec(`UPDATE song_picks SET take_id = NULL, source = 'none', pinned_take_id = NULL, pinned_by = NULL, pinned_at = NULL WHERE song_id = ?`, w.fx.Songs["S2"])
	if slices.Contains(w.labels(songs.ListFilter{HasPick: &yes}), "S2") || !slices.Contains(w.labels(songs.ListFilter{HasPick: &no}), "S2") {
		t.Error("a none Pick row has no take")
	}
}

func TestSongs_UnheardTable(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	yes, no := true, false
	listen := func(n int, take, kind, principal string) {
		w.exec(`INSERT INTO listen_events(id,track_id,session_id,kind,from_ms,to_ms,format,device_id,source,principal_kind,principal_id,client_at,received_at)
			VALUES(?,?,'l2-test-session',?,0,0,'flac',NULL,'web',?,?,'2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`,
			fmt.Sprintf("ev-%d", n), w.fx.Takes[take], kind, principal, principal+":x")
	}
	// the fixture: a device start on a1 (S2)
	eq(t, "unheard=true", w.labels(songs.ListFilter{Unheard: &yes}), "S1", "S3", "S4", "S5", "S6", "S7")
	eq(t, "unheard=false", w.labels(songs.ListFilter{Unheard: &no}), "S2")
	listen(1, "s1a", "start", "agent")   // an agent's start does not count
	listen(2, "b1", "complete", "human") // a complete without a start does not count
	listen(3, "b1", "segment", "device") // nor does a segment
	eq(t, "agent start, complete and segment", w.labels(songs.ListFilter{Unheard: &no}), "S2")
	listen(4, "c2", "start", "human") // Song level: any Take of the Song, not only the subject take
	eq(t, "human start on a non-subject take", w.labels(songs.ListFilter{Unheard: &no}), "S2", "S4")
	eq(t, "unheard=true after", w.labels(songs.ListFilter{Unheard: &yes}), "S1", "S3", "S5", "S6", "S7")
}

func TestSongs_SubjectTakePredicates(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	yes, no := true, false
	// durations: S4's Pick is c1 (300 s) though its newest take c2 is 240 s
	eq(t, "min_duration_s=280", w.labels(songs.ListFilter{MinDurationS: fp(280)}), "S1", "S2", "S4", "S5")
	eq(t, "max_duration_s=250", w.labels(songs.ListFilter{MaxDurationS: fp(250)}), "S3", "S6")
	eq(t, "min_duration_s=240 includes exactly 240", w.labels(songs.ListFilter{MinDurationS: fp(240)}), "S1", "S2", "S4", "S5", "S6")
	// a Song with no take (S7) never matches a take predicate, in either direction
	for name, f := range map[string]songs.ListFilter{
		"max_duration_s": {MaxDurationS: fp(1e6)}, "min_duration_s": {MinDurationS: fp(0)}, "unrated": {Unrated: &yes}, "rated": {Unrated: &no},
		"lossy=false": {Lossy: &no}, "lossy=true": {Lossy: &yes}, "recipe_known=true": {RecipeKnown: &yes}, "recipe_known=false": {RecipeKnown: &no},
		"tag": {Tags: []string{"anything"}}, "min_stars": {MinStars: ip(1)},
	} {
		if slices.Contains(w.labels(f), "S7") {
			t.Errorf("%s matched the takes-less Song", name)
		}
	}
	// stars: the human 4 on c1 and 3 on s6a
	eq(t, "min_stars=3", w.labels(songs.ListFilter{MinStars: ip(3)}), "S4", "S6")
	eq(t, "min_stars=4", w.labels(songs.ListFilter{MinStars: ip(4)}), "S4")
	eq(t, "unrated=true", w.labels(songs.ListFilter{Unrated: &yes}), "S1", "S2", "S3", "S5")
	eq(t, "unrated=false", w.labels(songs.ListFilter{Unrated: &no}), "S4", "S6")
	if err := (songs.ListFilter{MinStars: ip(3), Unrated: &yes}).ValidateL2(); err == nil {
		t.Error("min_stars and unrated=true are exclusive")
	}
	if err := (songs.ListFilter{MinStars: ip(3), Unrated: &no}).ValidateL2(); err != nil {
		t.Errorf("min_stars with unrated=false is consistent: %v", err)
	}
	if err := (songs.ListFilter{MinStars: ip(6)}).ValidateL2(); err == nil {
		t.Error("min_stars is 1..5")
	}
	if err := (songs.ListFilter{Tags: make([]string, 6)}).ValidateL2(); err == nil {
		t.Error("at most 5 tags")
	}
	// the subject take is the Pick: a rating on S4's non-Pick take c2 does not count
	w.exec(`INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES(?,'human',5,'human','human:h','h','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`, w.fx.Takes["c2"])
	eq(t, "min_stars=5 on a non-Pick take", w.labels(songs.ListFilter{MinStars: ip(5)}))
	// lossy and recipe_known: S5's only take is a lossy reference take
	eq(t, "lossy=true", w.labels(songs.ListFilter{Lossy: &yes}), "S5")
	eq(t, "lossy=false", w.labels(songs.ListFilter{Lossy: &no}), "S1", "S2", "S3", "S4", "S6")
	eq(t, "recipe_known=false", w.labels(songs.ListFilter{RecipeKnown: &no}), "S5")
	eq(t, "recipe_known=true", w.labels(songs.ListFilter{RecipeKnown: &yes}), "S1", "S2", "S3", "S4", "S6")
	// tags: all must match, normalised
	tag := func(take, name string, _ int) {
		w.exec(`INSERT OR IGNORE INTO tags(name,created_at) VALUES(?,'2026-01-01T00:00:00.000Z')`, name)
		var id int
		if err := w.st.R.QueryRow(`SELECT id FROM tags WHERE name = ?`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		w.exec(`INSERT INTO track_tags(track_id,tag_id,source,principal_id,created_at,updated_at) VALUES(?,?,'human','human:h','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`, w.fx.Takes[take], id)
	}
	tag("s1a", "ballad", 901)
	tag("s1a", "slow jam", 902)
	tag("c1", "ballad", 901)
	tag("c2", "slow jam", 902) // not the subject take of S4
	eq(t, "tag", w.labels(songs.ListFilter{Tags: []string{"Ballad"}}), "S1", "S4")
	eq(t, "two tags", w.labels(songs.ListFilter{Tags: []string{"ballad", " SLOW   jam "}}), "S1")
	w.exec(`UPDATE track_tags SET removed_at = '2026-01-02T00:00:00.000Z', removed_by = 'human:h' WHERE track_id = ? AND tag_id = (SELECT id FROM tags WHERE name = 'ballad')`, w.fx.Takes["s1a"])
	eq(t, "removed tag", w.labels(songs.ListFilter{Tags: []string{"ballad"}}), "S4")
	// combined predicates apply to one subject take
	eq(t, "tag and stars", w.labels(songs.ListFilter{Tags: []string{"ballad"}, MinStars: ip(4), MinDurationS: fp(290)}), "S4")
	eq(t, "tag and short", w.labels(songs.ListFilter{Tags: []string{"ballad"}, MaxDurationS: fp(250)}))
}

func TestSongs_InRecord(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	yes, no := true, false
	// R1 (published): S2 S3 S4 S7 S5; R2 (draft): S1 S6
	eq(t, "in_record=true", w.labels(songs.ListFilter{InRecord: &yes}), "S1", "S2", "S3", "S4", "S5", "S6", "S7")
	eq(t, "in_record=false", w.labels(songs.ListFilter{InRecord: &no}))
	// a hidden Record does not count, whatever its status
	w.exec(`UPDATE records SET hidden_at = '2026-01-02T00:00:00.000Z', hidden_by = 'human:h' WHERE id = ?`, w.fx.Records["R2"])
	eq(t, "R2 hidden", w.labels(songs.ListFilter{InRecord: &yes}), "S2", "S3", "S4", "S5", "S7")
	eq(t, "R2 hidden, false", w.labels(songs.ListFilter{InRecord: &no}), "S1", "S6")
	// a removed slot does not count
	w.exec(`UPDATE record_slots SET removed_at = '2026-01-02T00:00:00.000Z', removed_by = 'human:h' WHERE id = ?`, w.fx.Slots["E"])
	w.exec(`UPDATE records SET order_json = ? WHERE id = ?`, fmt.Sprintf(`["%s","%s","%s","%s"]`, w.fx.Slots["A"], w.fx.Slots["B"], w.fx.Slots["C"], w.fx.Slots["D"]), w.fx.Records["R1"])
	eq(t, "slot E removed", w.labels(songs.ListFilter{InRecord: &yes}), "S2", "S3", "S4", "S7")
	// draft and proposed Records count like published ones
	w.exec(`UPDATE records SET hidden_at = NULL, hidden_by = NULL WHERE id = ?`, w.fx.Records["R2"])
	eq(t, "R2 visible again", w.labels(songs.ListFilter{InRecord: &yes}), "S1", "S2", "S3", "S4", "S6", "S7")
}

// TestSongs_CrateID: a manual Crate is a live-membership join; a retired member is followed to the Song it was merged into and
// a removed member no longer matches.
func TestSongs_CrateID(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	manual := w.fx.Crates["manual_song"] // S1 S4 S6
	eq(t, "members", w.labels(songs.ListFilter{CrateID: manual}), "S1", "S4", "S6")
	eq(t, "unknown crate", w.labels(songs.ListFilter{CrateID: "nope"}))
	yes := true
	eq(t, "crate and in_record", w.labels(songs.ListFilter{CrateID: manual, Unheard: &yes, MinDurationS: fp(250)}), "S1", "S4")
	w.exec(`UPDATE crate_items SET removed_at = '2026-01-02T00:00:00.000Z', removed_by = 'human:h' WHERE crate_id = ? AND song_id = ?`, manual, w.fx.Songs["S4"])
	eq(t, "removed member", w.labels(songs.ListFilter{CrateID: manual}), "S1", "S6")
	w.exec(`INSERT INTO crate_items(id,crate_id,song_id,added_by,added_at) VALUES('it-s7',?,?,'human:h','2026-01-01T00:00:00.000Z')`, manual, w.fx.Songs["S7"])
	w.exec(`UPDATE songs SET retired_at = '2026-01-02T00:00:00.000Z', retired_into = ? WHERE id = ?`, w.fx.Songs["S2"], w.fx.Songs["S7"])
	eq(t, "retired member followed", w.labels(songs.ListFilter{CrateID: manual}), "S1", "S2", "S6")
}

func TestSongs_NoneMatchesNothing(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	if got := w.labels(songs.ListFilter{None: true}); len(got) != 0 {
		t.Errorf("None matched %v", got)
	}
	where, _ := songs.FilterSQL(songs.ListFilter{None: true}, "")
	if !strings.Contains(where, "0 = 1") {
		t.Error("None renders a false clause")
	}
}
