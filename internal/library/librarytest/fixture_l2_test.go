package librarytest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// noLadder answers "no duels": the L2 fixture decides its Picks by pins, stars and recency only.
type noLadder struct{}

func (noLadder) Strengths(context.Context, []string) (map[string]ladder.Strength, float64, error) {
	return map[string]ladder.Strength{}, 0, nil
}

// settleL2 runs the real Reconciler (structural pass, picks and a seeded sounds step) over st, as Rig.Settle does.
func settleL2(t *testing.T, st *store.Store) provenance.Report {
	t.Helper()
	ctx := context.Background()
	n := 0
	newID := func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd2f000+n) }
	now := func() time.Time { return FixedNow }
	sv := sounds.New(st, now, newID)
	if err := sv.EnsureSeed(ctx); err != nil {
		t.Fatal(err)
	}
	p := provenance.New(provenance.Options{Store: st, Muses: muses.New(st, now, newID), Songs: songs.New(st, now, newID), Sessions: sessions.New(st, now),
		Picks: picks.New(st, noLadder{}, now, newID), Sounds: sv, Ladder: noLadder{}, Now: now, NewID: newID})
	rep, err := p.Settle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if an, err := p.Verify(ctx); err != nil || len(an) != 0 {
		t.Fatalf("provenance Verify after Settle: %+v %v", an, err)
	}
	return rep
}

func str(t *testing.T, st *store.Store, q string, args ...any) string {
	t.Helper()
	var s sql.NullString
	if err := st.R.QueryRow(q, args...).Scan(&s); err != nil && err != sql.ErrNoRows {
		t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

func column(t *testing.T, st *store.Store, q string, args ...any) []string {
	t.Helper()
	rows, err := st.R.Query(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s.String)
	}
	return out
}

// TestFixtureL2_Shape: the counts of C132.2a, before and after a Settle; the Settle changes no Pick and no Record row.
func TestFixtureL2_Shape(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	fx := FixtureL2(t, st, FixedNow)

	const mine = `(SELECT id FROM songs WHERE title LIKE 'Synthetic Song S_')` // FixtureL1's own Songs appear with the Settle
	snap := func() []string {
		var out []string
		for _, q := range []string{
			`SELECT group_concat(song_id||take_id||source||last_event_id||changed_at, '|') FROM (SELECT * FROM song_picks WHERE song_id IN ` + mine + ` ORDER BY song_id)`,
			`SELECT group_concat(id||song_id||seq||COALESCE(from_take_id,'')||COALESCE(to_take_id,'')||reason, '|') FROM (SELECT * FROM pick_events WHERE song_id IN ` + mine + ` ORDER BY id)`,
			`SELECT group_concat(id||status||order_json||revision, '|') FROM (SELECT * FROM records ORDER BY id)`,
			`SELECT group_concat(id||synced_seq||COALESCE(pinned_take_id,''), '|') FROM (SELECT * FROM record_slots ORDER BY id)`,
			`SELECT group_concat(id||seq||kind, '|') FROM (SELECT * FROM record_changes ORDER BY id)`,
		} {
			out = append(out, str(t, st, q))
		}
		return out
	}
	// the fixture's own rows
	for label, want := range map[string]int{"S1": 1, "S2": 1, "S3": 2, "S4": 3, "S5": 2, "S6": 2, "S7": 0} {
		if n := count(t, st, `SELECT COUNT(*) FROM pick_events WHERE song_id = ?`, fx.Songs[label]); n != want {
			t.Errorf("%s: %d pick events, want %d", label, n, want)
		}
	}
	if len(fx.Songs) != 7 || len(fx.Takes) != 8 || len(fx.Muses) != 2 {
		t.Fatalf("songs %d takes %d muses %d", len(fx.Songs), len(fx.Takes), len(fx.Muses))
	}
	before := snap()
	rep := settleL2(t, st)
	if rep.Passes == 0 {
		t.Fatal("the Settle did nothing: the queue was empty")
	}
	if after := snap(); !slices.Equal(before, after) {
		for i := range before {
			if before[i] != after[i] {
				t.Errorf("Settle changed state %d:\n%s\n%s", i, before[i], after[i])
			}
		}
	}
	if n := count(t, st, `SELECT COUNT(*) FROM provenance_dirty`); n != 0 {
		t.Errorf("provenance_dirty holds %d rows after Settle", n)
	}

	// the Songs: eight Takes, one of them lossy, the Picks of the table, loudness for every Take
	if n := count(t, st, `SELECT COUNT(*) FROM take_songs WHERE song_id IN (SELECT id FROM songs WHERE title LIKE 'Synthetic Song S_')`); n != 8 {
		t.Errorf("%d Takes in S1-S7, want 8", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tracks WHERE title LIKE 'Synthetic Take %' AND lossy = 1`); n != 1 {
		t.Errorf("%d lossy takes", n)
	}
	for _, tk := range fx.Takes {
		if n := count(t, st, `SELECT COUNT(*) FROM analyses WHERE track_id = ? AND analyzer = 'loudness'`, tk); n != 1 {
			t.Errorf("take %s has %d loudness analyses", tk, n)
		}
	}
	for label, want := range map[string]string{"S1": "s1a/newest", "S2": "a1/newest", "S3": "b1/newest", "S4": "c1/stars", "S5": "e1/pinned", "S6": "s6a/stars"} {
		got := str(t, st, `SELECT take_id||'/'||source FROM song_picks WHERE song_id = ?`, fx.Songs[label])
		w := fx.Takes[strings.Split(want, "/")[0]] + "/" + strings.Split(want, "/")[1]
		if got != w {
			t.Errorf("%s: Pick %s, want %s", label, got, want)
		}
	}
	if n := count(t, st, `SELECT COUNT(*) FROM song_picks WHERE song_id = ?`, fx.Songs["S7"]); n != 0 {
		t.Errorf("S7 has a Pick row")
	}
	for _, c := range []struct{ song, family string }{{"S1", "reggae"}, {"S2", "reggae"}, {"S3", "reggae"}, {"S4", "dance"}, {"S6", "reggae"}, {"S7", "reggae"}} {
		if got := str(t, st, `SELECT value FROM song_sounds WHERE song_id = ? AND facet = 'genre_family'`, fx.Songs[c.song]); got != c.family {
			t.Errorf("%s: genre_family %q, want %q", c.song, got, c.family)
		}
	}
	// a lossy take is a reference take with no style: S5 has no derivable genre (an addendum of the F1 PR)
	if got := str(t, st, `SELECT COUNT(*) FROM song_sounds WHERE song_id = ? AND facet = 'genre_family'`, fx.Songs["S5"]); got != "0" {
		t.Errorf("S5 genre rows = %s", got)
	}

	// the C125.5 worked example, evaluated in SQL on the settled fixture: {S1, S6}
	got := column(t, st, `SELECT s.id FROM songs s
		JOIN song_sounds ss ON ss.song_id = s.id AND ss.facet = 'genre_family' AND ss.value = 'reggae'
		JOIN song_picks sp ON sp.song_id = s.id JOIN tracks t ON t.id = sp.take_id
		WHERE s.retired_at IS NULL AND t.duration_s >= 240 AND sp.source NOT IN ('pinned', 'ladder')
		  AND NOT EXISTS (SELECT 1 FROM listen_events le JOIN take_songs ts ON ts.track_id = le.track_id
		                   WHERE ts.song_id = s.id AND le.kind = 'start' AND le.principal_kind IN ('human', 'device'))
		ORDER BY s.id`)
	if want := []string{fx.Songs["S1"], fx.Songs["S6"]}; !slices.Equal(got, want) {
		t.Errorf("the worked example matches %v, want {S1, S6} = %v", got, want)
	}

	// Records: R1 published with five live slots, R2 an agent's draft with two
	r1, r2 := fx.Records["R1"], fx.Records["R2"]
	if got := str(t, st, `SELECT status||'/'||json_array_length(order_json)||'/'||revision FROM records WHERE id = ?`, r1); got != "published/5/3" {
		t.Errorf("R1: %s", got)
	}
	if got := str(t, st, `SELECT status||'/'||json_array_length(order_json)||'/'||created_by FROM records WHERE id = ?`, r2); got != "draft/2/"+FixtureAgent {
		t.Errorf("R2: %s", got)
	}
	var wantOrder []string
	for _, k := range []string{"A", "B", "C", "D", "E"} {
		wantOrder = append(wantOrder, fx.Slots[k])
	}
	var gotOrder []string
	if err := json.Unmarshal([]byte(str(t, st, `SELECT order_json FROM records WHERE id = ?`, r1)), &gotOrder); err != nil || !slices.Equal(gotOrder, wantOrder) {
		t.Errorf("R1 order %v (%v), want A-E", gotOrder, err)
	}
	// the resolved Take of each R1 slot: floating follows the Pick, C is pinned to the non-Pick c2, D is empty
	for k, want := range map[string]string{"A": "a1", "B": "b1", "C": "c2", "D": "", "E": "e1"} {
		got := str(t, st, `SELECT COALESCE(s.pinned_take_id, sp.take_id) FROM record_slots s LEFT JOIN song_picks sp ON sp.song_id = s.song_id WHERE s.id = ?`, fx.Slots[k])
		if w := fx.Takes[want]; got != w {
			t.Errorf("slot %s resolves to %q, want %s", k, got, want)
		}
	}
	if got := str(t, st, `SELECT sp.take_id FROM record_slots s JOIN song_picks sp ON sp.song_id = s.song_id WHERE s.id = ?`, fx.Slots["C"]); got != fx.Takes["c1"] {
		t.Errorf("slot C: the Song's Pick is %q, want c1 (the pin differs from it)", got)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM record_slots WHERE song_id = ? AND record_id = ?`, fx.Songs["S7"], r1); n != 1 {
		t.Errorf("the empty slot on S7: %d", n)
	}
	// R2 holds S1 and S6 in that order
	if got := column(t, st, `SELECT s.song_id FROM record_slots s, json_each((SELECT order_json FROM records WHERE id = ?)) j WHERE s.id = j.value ORDER BY j.key`, r2); !slices.Equal(got, []string{fx.Songs["S1"], fx.Songs["S6"]}) {
		t.Errorf("R2 songs %v", got)
	}

	// Crates: two manual with 3 and 4 members, two smart with the stored filters
	for k, want := range map[string]int{"manual_song": 3, "manual_take": 4, "smart_song": 0, "smart_take": 0} {
		if n := count(t, st, `SELECT COUNT(*) FROM crate_items WHERE crate_id = ? AND removed_at IS NULL`, fx.Crates[k]); n != want {
			t.Errorf("crate %s: %d members, want %d", k, n, want)
		}
	}
	if n := count(t, st, `SELECT COUNT(*) FROM crates`); n != 4 {
		t.Errorf("%d crates", n)
	}
	if got := str(t, st, `SELECT filter_json FROM crates WHERE id = ?`, fx.Crates["smart_song"]); got != L2SmartSongFilter {
		t.Errorf("smart song filter %s", got)
	}
	for _, tb := range []string{"tapes"} {
		if n := count(t, st, `SELECT COUNT(*) FROM `+tb); n != 0 {
			t.Errorf("%s has %d rows", tb, n)
		}
	}

	// the Muses carry the frozen ordinals 1 and 2 (FixtureL1's Muses come from the Settle, after them)
	if got := str(t, st, `SELECT group_concat(ordinal, ',') FROM (SELECT ordinal FROM muses WHERE id IN (?, ?) ORDER BY ordinal)`, fx.Muses["M1"], fx.Muses["M2"]); got != "1,2" {
		t.Errorf("ordinals of M1, M2 = %s", got)
	}
	var fk int
	if err := st.R.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Errorf("foreign_key_check rows = %d %v", fk, err)
	}
}

// TestFixtureL2_RecordsReplay: what Verify R1, R4, R5 and R6 will check, evaluated on the fixture: the order lists exactly the
// live slots, each Record's changelog is a chain 1..n opening with `created`, folding a slot's take-bearing rows reproduces
// its resolved Take, and the cover seed is records.CoverSeed.
func TestFixtureL2_RecordsReplay(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	fx := FixtureL2(t, st, FixedNow)
	for label, rid := range fx.Records {
		var order []string
		if err := json.Unmarshal([]byte(str(t, st, `SELECT order_json FROM records WHERE id = ?`, rid)), &order); err != nil {
			t.Fatal(err)
		}
		live := column(t, st, `SELECT id FROM record_slots WHERE record_id = ? AND removed_at IS NULL ORDER BY id`, rid)
		if sorted := slices.Sorted(slices.Values(order)); !slices.Equal(sorted, live) {
			t.Errorf("%s: order %v is not the live slots %v", label, order, live)
		}
		if got := column(t, st, `SELECT seq FROM record_changes WHERE record_id = ? ORDER BY seq`, rid); got[0] != "1" || len(got) != count(t, st, `SELECT MAX(seq) FROM record_changes WHERE record_id = ?`, rid) {
			t.Errorf("%s: changelog seqs %v", label, got)
		}
		if k := str(t, st, `SELECT kind FROM record_changes WHERE record_id = ? AND seq = 1`, rid); k != "created" {
			t.Errorf("%s: first row is %q", label, k)
		}
		var seed int64
		var rerolls int
		if err := st.R.QueryRow(`SELECT cover_seed, cover_rerolls FROM records WHERE id = ?`, rid).Scan(&seed, &rerolls); err != nil || seed != records.CoverSeed(rid, rerolls) {
			t.Errorf("%s: cover_seed %d, want %d (%v)", label, seed, records.CoverSeed(rid, rerolls), err)
		}
		for _, sid := range order {
			var last sql.NullString
			prev := sql.NullString{}
			rows, err := st.R.Query(`SELECT from_take_id, to_take_id FROM record_changes WHERE slot_id = ? AND kind IN
				('slot_added','slot_pinned','slot_unpinned','slot_upgraded','slot_filled','slot_emptied','slot_song_followed') ORDER BY seq`, sid)
			if err != nil {
				t.Fatal(err)
			}
			first := true
			for rows.Next() {
				var from, to sql.NullString
				if err := rows.Scan(&from, &to); err != nil {
					t.Fatal(err)
				}
				if !first && from != prev {
					t.Errorf("%s slot %s: from %v does not follow the previous to %v", label, sid, from, prev)
				}
				first, prev, last = false, to, to
			}
			rows.Close()
			want := str(t, st, `SELECT COALESCE(s.pinned_take_id, sp.take_id) FROM record_slots s LEFT JOIN song_picks sp ON sp.song_id = s.song_id WHERE s.id = ?`, sid)
			if last.String != want {
				t.Errorf("%s slot %s: the changelog replays to %q, the slot resolves to %q", label, sid, last.String, want)
			}
			// a floating slot has consumed every Pick event of its Song
			var pinned sql.NullString
			var synced, maxSeq int
			if err := st.R.QueryRow(`SELECT s.pinned_take_id, s.synced_seq, COALESCE((SELECT MAX(seq) FROM pick_events WHERE song_id = s.song_id), 0) FROM record_slots s WHERE s.id = ?`, sid).Scan(&pinned, &synced, &maxSeq); err != nil {
				t.Fatal(err)
			}
			if synced != maxSeq {
				t.Errorf("%s slot %s: synced_seq %d, the Song's events end at %d", label, sid, synced, maxSeq)
			}
		}
	}
}

// TestFixtureL2_CrateFiltersParse: the two stored filters are objects the vocabulary of C125.2 accepts (the keys are checked
// against the shipped list operations by the crates package's own test).
func TestFixtureL2_CrateFiltersParse(t *testing.T) {
	t.Parallel()
	for _, f := range []string{L2SmartSongFilter, L2SmartTakeFilter} {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(f), &m); err != nil || len(m) == 0 || len(f) > 4096 {
			t.Errorf("filter %s: %v", f, err)
		}
	}
}

func TestApplyL2_OnTheShippedStack(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	FixtureM3(t, st, FixedNow)
	ApplyL1(t, st, FixedNow)
	ApplyL2(t, st, FixedNow)
	if n := count(t, st, `SELECT COUNT(*) FROM songs WHERE title LIKE 'Synthetic Song S_'`); n != 7 {
		t.Errorf("%d fixture songs", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM records`); n != 2 {
		t.Errorf("%d records", n)
	}
	// it also builds on an empty library (the rows are self-contained)
	st2 := openL1(t)
	ApplyL2(t, st2, FixedNow)
	if n := count(t, st2, `SELECT COUNT(*) FROM crates`); n != 4 {
		t.Errorf("%d crates on an empty library", n)
	}
}

func TestFixtureL2Scale_Shape(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	info := FixtureL2Scale(t, st, FixedNow, 45, 4, 3)
	if len(info.Songs) != 45 || len(info.Muses) != 4 || len(info.Records) != 3 {
		t.Fatalf("songs %d muses %d records %d", len(info.Songs), len(info.Muses), len(info.Records))
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM songs`:          45,
		`SELECT COUNT(*) FROM tracks`:         90,
		`SELECT COUNT(*) FROM pick_events`:    90,
		`SELECT COUNT(*) FROM song_picks`:     45,
		`SELECT COUNT(*) FROM muses`:          4,
		`SELECT COUNT(*) FROM records`:        3,
		`SELECT COUNT(*) FROM record_slots`:   60,
		`SELECT COUNT(*) FROM record_changes`: 3 + 60,
		`SELECT COUNT(*) FROM song_picks sp JOIN tracks t ON t.id = sp.take_id WHERE t.generation = 1`: 45,
	} {
		if n := count(t, st, q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	// every slot is up to date with its Song's events and the orders are the live slots
	if n := count(t, st, `SELECT COUNT(*) FROM record_slots s WHERE s.synced_seq <> (SELECT MAX(seq) FROM pick_events WHERE song_id = s.song_id)`); n != 0 {
		t.Errorf("%d slots behind", n)
	}
	// the Songs are spread over the Muses round-robin
	if got := column(t, st, `SELECT COUNT(*) FROM songs GROUP BY muse_id ORDER BY 1`); fmt.Sprint(got) != "[11 11 11 12]" {
		t.Errorf("songs per Muse %v", got)
	}
	// and no Muse at all is fine
	st2 := openL1(t)
	if info := FixtureL2Scale(t, st2, FixedNow, 3, 0, 1); len(info.Songs) != 3 || count(t, st2, `SELECT COUNT(*) FROM songs WHERE muse_id IS NULL`) != 3 {
		t.Errorf("no-Muse scale fixture: %+v", info.Songs)
	}
	var fk int
	if err := st.R.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Errorf("foreign_key_check rows = %d %v", fk, err)
	}
}

var syntheticL2 = regexp.MustCompile(`^Synthetic [A-Za-z0-9 ]+\.?$`)

// TestFixtures_L2_SyntheticOnly: every text the L2 fixtures write is synthetic, and none is a URL, a path or a name.
func TestFixtures_L2_SyntheticOnly(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	FixtureL2(t, st, FixedNow)
	scale := openL1(t)
	FixtureL2Scale(t, scale, FixedNow, 12, 3, 2)
	for _, db := range []*store.Store{st, scale} {
		for _, q := range []string{
			`SELECT title FROM muses WHERE title LIKE 'Synthetic %Muse%'`, `SELECT artist FROM muses WHERE artist IS NOT NULL AND artist LIKE '%Curation%'`,
			`SELECT title FROM songs WHERE title LIKE 'Synthetic Song%' OR title LIKE 'Synthetic Scale%'`,
			`SELECT title FROM tracks WHERE title LIKE 'Synthetic Take%' OR title LIKE 'Synthetic Scale%'`,
			`SELECT title FROM records`, `SELECT subtitle FROM records WHERE subtitle <> ''`, `SELECT liner_note FROM records WHERE liner_note <> ''`,
			`SELECT name FROM crates`, `SELECT description FROM crates WHERE description <> ''`,
		} {
			for _, v := range column(t, db, q) {
				if !syntheticL2.MatchString(v) {
					t.Errorf("%s: %q is not synthetic text", q, v)
				}
			}
		}
	}
	// the owners of the rows are the fixture principals, never a name
	for _, v := range column(t, st, `SELECT DISTINCT created_by FROM records UNION SELECT DISTINCT created_by FROM crates`) {
		if v != FixtureHuman && v != FixtureAgent {
			t.Errorf("principal %q", v)
		}
	}
	// no row of the L2 world names a host, path or address
	for _, q := range []string{`SELECT style FROM recipes WHERE request_id LIKE 'l2_%'`, `SELECT request_json FROM recipe_drafts WHERE request_id LIKE 'l2_%'`} {
		for _, v := range column(t, st, q) {
			if strings.Contains(v, "http") || strings.Contains(v, "@") || strings.Contains(v, "://") {
				t.Errorf("%s: %q", q, v)
			}
		}
	}
}
