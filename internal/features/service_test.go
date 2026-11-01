package features_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

// TestFeatures_SubjectEqualsSounds: every Song of FixtureL3 (and FixtureL2 under it) has a song_features row whose subject
// equals sounds.SubjectOf; Songs without a subject have none.
func TestFeatures_SubjectEqualsSounds(t *testing.T) {
	t.Parallel()
	w, _ := newL3World(t)
	rows, err := w.st.R.Query(`SELECT id, retired_at IS NOT NULL FROM songs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	type s struct {
		id      string
		retired bool
	}
	var all []s
	for rows.Next() {
		var x s
		if err := rows.Scan(&x.id, &x.retired); err != nil {
			t.Fatal(err)
		}
		all = append(all, x)
	}
	rows.Close()
	if len(all) < 14 {
		t.Fatalf("only %d Songs", len(all))
	}
	withRow := 0
	for _, x := range all {
		sub, err := sounds.SubjectOf(context.Background(), w.st.R, x.id)
		if err != nil {
			t.Fatal(err)
		}
		var kind, id sql.NullString
		err = w.st.R.QueryRow(`SELECT subject_kind, subject_id FROM song_features WHERE song_id = ?`, x.id).Scan(&kind, &id)
		if err != nil && err != sql.ErrNoRows {
			t.Fatal(err)
		}
		switch {
		case x.retired || sub.Kind == sounds.SubjectNone:
			if kind.Valid {
				t.Errorf("Song %s (retired %v, subject %s) has a row", x.id, x.retired, sub.Kind)
			}
		case kind.String != sub.Kind || id.String != sub.ID:
			t.Errorf("Song %s: row %s %s, SubjectOf %s %s", x.id, kind.String, id.String, sub.Kind, sub.ID)
		default:
			withRow++
		}
	}
	if withRow < 14 {
		t.Errorf("%d Songs with a row, want at least the 14 of the L3 world", withRow)
	}
	w.verifyEmpty()
}

// TestFeatures_ComputeTx_EqualsLiteralRows: after a Settle on FixtureL3 the computed value columns equal the F1 literal rows
// (the literals carry a placeholder digest and no detail, so only value columns are compared).
func TestFeatures_ComputeTx_EqualsLiteralRows(t *testing.T) {
	t.Parallel()
	w, info := newL3World(t)
	ctx := context.Background()
	for _, lit := range librarytest.L3Features {
		song := info.Songs[lit.Label]
		got, err := w.svc.Songs(ctx, []string{song})
		if err != nil {
			t.Fatal(err)
		}
		g, ok := got[song]
		if !ok {
			t.Errorf("%s: no song row", lit.Label)
			continue
		}
		if g.SubjectKind != lit.SubjectKind {
			t.Errorf("%s: subject kind %s, want %s", lit.Label, g.SubjectKind, lit.SubjectKind)
		}
		want := map[string]any{"bpm": lit.BPMx100, "bpmBasis": lit.BPMBasis, "req": lit.BPMRequestedx100, "meas": lit.BPMMeasuredx100,
			"energy": lit.Energyx10000, "energyBasis": lit.EnergyBasis, "dur": lit.DurationS}
		have := map[string]any{"bpm": g.BPMx100, "bpmBasis": string(g.BPMBasis), "req": g.BPMRequestedx100, "meas": g.BPMMeasuredx100,
			"energy": g.Energyx10000, "energyBasis": string(g.EnergyBasis), "dur": g.DurationS}
		if d := cmp.Diff(want, have); d != "" {
			t.Errorf("%s: value columns differ (-literal +computed):\n%s", lit.Label, d)
		}
		var kn *int
		if g.Key != nil {
			kn = ip(g.Key.Num)
		}
		letter := ""
		if g.Key != nil {
			letter = g.Key.Letter
		}
		if d := cmp.Diff([]any{lit.KeyNum, lit.KeyLetter, lit.KeyBasis}, []any{kn, letter, string(g.KeyBasis)}); d != "" {
			t.Errorf("%s: key differs:\n%s", lit.Label, d)
		}
		str := func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		}
		if d := cmp.Diff([]string{lit.Family, lit.Vocal, lit.Era, lit.Mood, lit.Language}, []string{str(g.Family), str(g.Vocal), str(g.Era), str(g.Mood), str(g.Language)}); d != "" {
			t.Errorf("%s: primary values differ:\n%s", lit.Label, d)
		}
		if g.SubjectNote != nil {
			t.Errorf("%s: unexpected subject note %s", lit.Label, *g.SubjectNote)
		}
	}
}

// TestAcceptance_L3_FixtureCounts: the take, Song and feature counts of C146.1 after a Settle.
func TestAcceptance_L3_FixtureCounts(t *testing.T) {
	t.Parallel()
	w, info := newL3World(t)
	if got := w.count(`SELECT COUNT(*) FROM take_features f JOIN tracks t ON t.id = f.track_id WHERE t.title LIKE 'Synthetic Take %'`); got != 17 {
		t.Errorf("take rows of the L3 takes = %d, want 17", got)
	}
	if got, want := w.count(`SELECT COUNT(*) FROM take_features`), w.count(`SELECT COUNT(*) FROM tracks`); got != want {
		t.Errorf("take rows %d, tracks %d", got, want)
	}
	if got := w.count(`SELECT COUNT(*) FROM song_features WHERE song_id IN (SELECT id FROM songs WHERE (title LIKE 'Synthetic Song T_' OR title LIKE 'Synthetic Song u_' OR title LIKE 'Synthetic Song d_'))`); got != 14 {
		t.Errorf("L3 Song rows = %d, want 14 (T1-T8, u1, u2, d1-d4)", got)
	}
	if got := w.count(`SELECT COUNT(*) FROM song_features WHERE subject_kind = 'draft' AND song_id IN (SELECT id FROM songs WHERE (title LIKE 'Synthetic Song T_' OR title LIKE 'Synthetic Song u_' OR title LIKE 'Synthetic Song d_'))`); got != 5 {
		t.Errorf("draft-subject Song rows = %d, want 5", got)
	}
	if got := w.count(`SELECT COUNT(*) FROM song_features WHERE energy_basis = 'measured' AND song_id IN (SELECT id FROM songs WHERE (title LIKE 'Synthetic Song T_' OR title LIKE 'Synthetic Song u_' OR title LIKE 'Synthetic Song d_'))`); got != 8 {
		t.Errorf("measured-energy Songs = %d, want 8", got)
	}
	if info.Songs["T1"] == "" {
		t.Fatal("no T1")
	}
	w.verifyEmpty()
}

// TestFeatures_TakeFromAnalyses: tempo, key and energy analyses rows give the columns and bases of C135.2 end to end.
func TestFeatures_TakeFromAnalyses(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	decl := 100
	cases := []struct {
		name     string
		style    string
		declared *int
		setup    func(id string)
		check    func(t *testing.T, r features.Row)
	}{
		{"measured beats declared when close", "pop, 100bpm", &decl, func(id string) { w.tempo(id, 102.4) }, func(t *testing.T, r features.Row) {
			if *r.BPMx100 != 10240 || r.BPMBasis != "measured" || *r.BPMRequestedx100 != 10000 || r.BPMOctave {
				t.Errorf("%+v", r)
			}
		}},
		{"octave keeps the requested value", "pop, 100bpm", &decl, func(id string) { w.tempo(id, 200.2) }, func(t *testing.T, r features.Row) {
			if *r.BPMx100 != 10000 || r.BPMBasis != "declared" || !r.BPMOctave || *r.BPMMeasuredx100 != 20020 {
				t.Errorf("%+v", r)
			}
		}},
		{"style bpm is declared", "pop, 96bpm", nil, func(string) {}, func(t *testing.T, r features.Row) {
			if *r.BPMx100 != 9600 || r.BPMBasis != "declared" {
				t.Errorf("%+v", r)
			}
		}},
		{"out of range measured is discarded", "pop", nil, func(id string) { w.tempo(id, 400) }, func(t *testing.T, r features.Row) {
			if r.BPMx100 != nil || !strings.Contains(string(r.Detail), `"discarded":{"bpm":["measured"]}`) {
				t.Errorf("%+v %s", r, r.Detail)
			}
		}},
		{"key measured, style disagrees", "pop, in A minor", nil, func(id string) { w.key(id, 0, "major") }, func(t *testing.T, r features.Row) {
			if r.Key == nil || *r.Key != (features.Camelot{Num: 8, Letter: "B"}) || r.KeyBasis != "measured" || !strings.Contains(string(r.Detail), `"relation":"relative"`) {
				t.Errorf("%+v %s", r, r.Detail)
			}
		}},
		{"key declared from the style", "pop, in A minor", nil, func(string) {}, func(t *testing.T, r features.Row) {
			if r.Key == nil || *r.Key != (features.Camelot{Num: 8, Letter: "A"}) || r.KeyBasis != "declared" {
				t.Errorf("%+v", r)
			}
		}},
		{"null measured key falls back to the style key", "pop, in E minor", nil, func(id string) {
			w.analysis(id, "key", "mm-key/1 ffmpeg n9", `{"key":null,"tonic":null,"mode":null,"r":0.2,"margin":0.0}`)
		}, func(t *testing.T, r features.Row) {
			if r.KeyBasis != "declared" || r.Key.Num != 9 {
				t.Errorf("%+v", r)
			}
		}},
		{"energy measured", "pop", nil, func(id string) { w.energy(id, 0.5321) }, func(t *testing.T, r features.Row) {
			if *r.Energyx10000 != 5321 || r.EnergyBasis != "measured" {
				t.Errorf("%+v", r)
			}
		}},
		{"energy predicted from the facets", "chill, reggae, 76bpm", nil, func(string) {}, func(t *testing.T, r features.Row) {
			if *r.Energyx10000 != 3333 || r.EnergyBasis != "predicted" {
				t.Errorf("%+v", r)
			}
		}},
		{"another definition is ignored", "pop", nil, func(id string) {
			w.analysis(id, "energy", "mm-energy/2 x", `{"energy":0.9}`)
		}, func(t *testing.T, r features.Row) {
			if r.EnergyBasis != "predicted" {
				t.Errorf("a mm-energy/2 row counted: %+v", r)
			}
		}},
	}
	ids := make([]string, len(cases))
	for i, c := range cases {
		ids[i] = w.track(w.recipe(c.style, ""), trackOpts{Declared: c.declared, AgeMin: i + 1})
		c.setup(ids[i])
	}
	w.settle()
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok, err := w.svc.Take(context.Background(), ids[i])
			if err != nil || !ok {
				t.Fatalf("Take: %v %v", ok, err)
			}
			c.check(t, r)
		})
	}
	w.verifyEmpty()
}

// TestFeatures_PrimaryFacets: the five primary Sound values are copied into the Song row.
func TestFeatures_PrimaryFacets(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	id := w.track(w.recipe("reggae, chill, female vocal, 80s, 90bpm", ""), trackOpts{})
	w.settle()
	song := w.songOf(id)
	got, err := w.svc.Songs(context.Background(), []string{song})
	if err != nil {
		t.Fatal(err)
	}
	g := got[song]
	for facet, p := range map[string]*string{"genre_family": g.Family, "vocal": g.Vocal, "era": g.Era, "mood": g.Mood} {
		want := w.str(`SELECT value FROM song_sounds WHERE song_id = ? AND facet = ? ORDER BY ord, value LIMIT 1`, song, facet)
		if want == "" || p == nil || *p != want {
			t.Errorf("%s = %v, want %q", facet, p, want)
		}
	}
	if g.Language != nil && *g.Language == "" {
		t.Error("empty language string")
	}
}

// TestFeatures_DigestUnchangedNoRewrite: an unchanged digest writes nothing; computed_at is stable.
func TestFeatures_DigestUnchangedNoRewrite(t *testing.T) {
	t.Parallel()
	w, info := newL3World(t)
	take, song := info.Takes["T1"], info.Songs["T1"]
	before := [2]string{w.str(`SELECT computed_at FROM take_features WHERE track_id = ?`, take), w.str(`SELECT computed_at FROM song_features WHERE song_id = ?`, song)}
	var ch features.Changes
	w.inTx(func(ctx context.Context, tx *sql.Tx, env features.Env) (err error) {
		ch, err = w.svc.ComputeTx(ctx, tx, env, []string{take}, []string{song})
		return err
	})
	if ch != (features.Changes{}) {
		t.Errorf("an unchanged ComputeTx changed %+v", ch)
	}
	after := [2]string{w.str(`SELECT computed_at FROM take_features WHERE track_id = ?`, take), w.str(`SELECT computed_at FROM song_features WHERE song_id = ?`, song)}
	if before != after {
		t.Errorf("computed_at moved: %v -> %v", before, after)
	}
	// a new analysis row of the current definition changes the digest: the take and its Song are rewritten
	w.analysis(take, "energy", "mm-energy/1 zz", `{"energy":0.99}`)
	w.settle()
	if got := w.count(`SELECT energy_x10000 FROM take_features WHERE track_id = ?`, take); got != 9900 {
		t.Errorf("energy after the new analysis = %d, want 9900 (the greatest version wins)", got)
	}
	if got := w.count(`SELECT energy_x10000 FROM song_features WHERE song_id = ?`, song); got != 9900 {
		t.Errorf("Song energy = %d, want 9900", got)
	}
	w.verifyEmpty()
}

// TestFeatures_StaleSweep: a row of an older feature_def or with a changed digest is rewritten by the sweep, a retired Song's
// row and a missing row too; a second sweep finds nothing.
func TestFeatures_StaleSweep(t *testing.T) {
	t.Parallel()
	w, info := newL3World(t)
	take, song := info.Takes["T2"], info.Songs["T2"]
	w.exec(`UPDATE take_features SET feature_def = 'mm-features/0' WHERE track_id = ?`, take)
	w.exec(`UPDATE song_features SET digest = ? WHERE song_id = ?`, strings.Repeat("0", 64), song)
	w.exec(`DELETE FROM song_features WHERE song_id = ?`, info.Songs["T3"])
	w.exec(`UPDATE take_features SET digest = ? WHERE track_id = ?`, strings.Repeat("1", 64), info.Takes["T4"])
	if err := w.prov.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.str(`SELECT feature_def FROM take_features WHERE track_id = ?`, take); got != features.Def {
		t.Errorf("feature_def = %s after the sweep", got)
	}
	if got := w.count(`SELECT COUNT(*) FROM song_features WHERE song_id IN (?, ?)`, song, info.Songs["T3"]); got != 2 {
		t.Errorf("rows after the sweep = %d, want 2", got)
	}
	w.verifyEmpty()
	// the sweep batch reports what it wrote, and nothing the second time
	var n int
	w.inTx(func(ctx context.Context, tx *sql.Tx, env features.Env) (err error) {
		n, err = w.svc.SweepStaleTx(ctx, tx, env, features.BatchSongs)
		return err
	})
	if n != 0 {
		t.Errorf("a second sweep wrote %d rows", n)
	}
	// the batch is bounded
	w.exec(`UPDATE take_features SET digest = ?`, strings.Repeat("2", 64))
	w.inTx(func(ctx context.Context, tx *sql.Tx, env features.Env) (err error) {
		n, err = w.svc.SweepStaleTx(ctx, tx, env, 3)
		return err
	})
	if n != 3 {
		t.Errorf("a sweep with limit 3 wrote %d rows", n)
	}
}

// TestFeatures_ReferenceTakeMeasuredOnly: a reference take (stub recipe) has only measured values and no predicted energy.
func TestFeatures_ReferenceTakeMeasuredOnly(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	bare := w.track(w.stubRecipe(), trackOpts{Ref: true, AgeMin: 1})
	meas := w.track(w.stubRecipe(), trackOpts{Ref: true, AgeMin: 2})
	w.tempo(meas, 120)
	w.key(meas, 9, "minor")
	w.energy(meas, 0.4)
	w.settle()
	r, _, _ := w.svc.Take(context.Background(), bare)
	if r.BPMx100 != nil || r.Key != nil || r.Energyx10000 != nil || r.BPMRequestedx100 != nil {
		t.Errorf("a bare reference take has values: %+v", r)
	}
	r, _, _ = w.svc.Take(context.Background(), meas)
	if r.BPMBasis != "measured" || r.KeyBasis != "measured" || r.EnergyBasis != "measured" || r.BPMRequestedx100 != nil {
		t.Errorf("measured reference take: %+v", r)
	}
	w.verifyEmpty()
}

// TestFeatures_NoTextColumns: the two tables hold numbers, codes and ids only: no column of either is free text beyond
// the primary Sound values and the detail JSON, and a request's lyrics never reach a row.
func TestFeatures_NoTextColumns(t *testing.T) {
	t.Parallel()
	w, _ := newL3World(t)
	for _, table := range []string{"take_features", "song_features"} {
		rows, err := w.st.R.Query(`SELECT name FROM pragma_table_info('` + table + `') WHERE type = 'TEXT'`)
		if err != nil {
			t.Fatal(err)
		}
		allowed := map[string]bool{"track_id": true, "song_id": true, "subject_kind": true, "subject_id": true, "subject_note": true, "bpm_basis": true, "energy_basis": true,
			"key_letter": true, "key_basis": true, "detail_json": true, "digest": true, "feature_def": true, "computed_at": true,
			"family": true, "vocal": true, "era": true, "mood": true, "language": true}
		for rows.Next() {
			var name string
			_ = rows.Scan(&name)
			if !allowed[name] {
				t.Errorf("%s.%s is a text column outside the allow-list", table, name)
			}
		}
		rows.Close()
	}
	var leaks int
	for _, q := range []string{
		`SELECT COUNT(*) FROM take_features WHERE detail_json LIKE '%line%' OR detail_json LIKE '%Synthetic%'`,
		`SELECT COUNT(*) FROM song_features WHERE family LIKE '% %' OR vocal LIKE '% %' OR mood LIKE '% %'`,
	} {
		leaks += w.count(q)
	}
	if leaks != 0 {
		t.Errorf("%d rows leak text", leaks)
	}
}

// TestFeatures_ComputeTx_Subjects: Pick take, newest non-discarded take, draft; a Pick change moves the row.
func TestFeatures_ComputeTx_Subjects(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	sc := librarytest.NewScene(t, w.st, librarytest.FixedNow)
	root := w.track(w.recipe("pop, 100bpm", ""), trackOpts{AgeMin: 30})
	child := w.track(w.recipe("rock, 140bpm", ""), trackOpts{Parent: root, AgeMin: 10})
	w.settle()
	song := w.songOf(root)
	subject := func() (string, string) {
		var k, id string
		if err := w.st.R.QueryRow(`SELECT subject_kind, subject_id FROM song_features WHERE song_id = ?`, song).Scan(&k, &id); err != nil {
			t.Fatal(err)
		}
		return k, id
	}
	oracle := func() string {
		s, err := sounds.SubjectOf(context.Background(), w.st.R, song)
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	if k, id := subject(); k != "take" || id != oracle() {
		t.Fatalf("subject %s %s, oracle %s", k, id, oracle())
	}
	sc.Rate(root, 5)
	sc.Rate(child, 2)
	w.settle()
	if _, id := subject(); id != root || oracle() != root {
		t.Errorf("after rating the root higher, subject = %s (oracle %s), want the root", id, oracle())
	}
	if got := w.count(`SELECT bpm_x100 FROM song_features WHERE song_id = ?`, song); got != 10000 {
		t.Errorf("Song bpm = %d, want the root's 10000", got)
	}
	sc.Rate(root, 1)
	sc.Rate(child, 5)
	w.settle()
	if _, id := subject(); id != child || oracle() != child {
		t.Errorf("after the Pick moved, subject = %s, want the child", id)
	}
	if got := w.count(`SELECT bpm_x100 FROM song_features WHERE song_id = ?`, song); got != 14000 {
		t.Errorf("Song bpm = %d, want the child's 14000", got)
	}
	w.verifyEmpty()
	// no take at all: the Song of a fulfilled-less draft derives from the draft, bases declared/plan/predicted only
	d := sc.Draft("drft_v1", librarytest.DraftOpts{Label: "variant-a", Group: "g"})
	w.exec(`UPDATE recipe_drafts SET request_json = json_set(request_json, '$.style', 'reggae, chill, in A minor, 76bpm') WHERE id = ?`, d)
	w.settle()
	ds := w.str(`SELECT id FROM songs WHERE draft_id = ?`, d)
	var kind, bb, eb, kb string
	if err := w.st.R.QueryRow(`SELECT subject_kind, bpm_basis, energy_basis, key_basis FROM song_features WHERE song_id = ?`, ds).Scan(&kind, &bb, &eb, &kb); err != nil {
		t.Fatal(err)
	}
	if kind != "draft" || bb != "declared" || eb != "predicted" || kb != "declared" {
		t.Errorf("draft row: %s %s %s %s", kind, bb, eb, kb)
	}
	w.verifyEmpty()
}

// TestFeatures_AllTakesDiscarded: a Song whose every take is discarded falls through to its draft and carries the note.
func TestFeatures_AllTakesDiscarded(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	sc := librarytest.NewScene(t, w.st, librarytest.FixedNow)
	rec := w.recipe("pop, chill, 100bpm", "")
	d := sc.Draft("dd_v1", librarytest.DraftOpts{Recipe: rec, Label: "variant-a", Group: "g"})
	// a Song created from the draft, then fulfilled by two takes that are both discarded
	w.settle()
	song := w.str(`SELECT id FROM songs WHERE draft_id = ?`, d)
	if song == "" {
		t.Fatal("no draft Song")
	}
	// attach a take to the draft's Song: a root fulfilling the draft keeps the Song's draft_id when the Song survives
	root := w.track(rec, trackOpts{AgeMin: 5})
	sc.Fulfil(d, root)
	w.settle()
	song = w.songOf(root)
	if w.str(`SELECT draft_id FROM songs WHERE id = ?`, song) == "" {
		t.Skip("the Reconciler drops the draft link once a take exists; the note applies to Songs that keep it")
	}
	sc.Status(root, "discarded")
	w.settle()
	var note sql.NullString
	var kind string
	if err := w.st.R.QueryRow(`SELECT subject_kind, subject_note FROM song_features WHERE song_id = ?`, song).Scan(&kind, &note); err != nil {
		t.Fatal(err)
	}
	if kind != "draft" || note.String != "all_takes_discarded" {
		t.Errorf("subject %s note %q", kind, note.String)
	}
	w.verifyEmpty()
}

// TestFeatures_RetiredSongDropped: the row of a retired Song is removed, and a Song without a subject has none.
func TestFeatures_RetiredSongDropped(t *testing.T) {
	t.Parallel()
	w, info := newL3World(t)
	song := info.Songs["d4"]
	if w.count(`SELECT COUNT(*) FROM song_features WHERE song_id = ?`, song) != 1 {
		t.Fatal("no row to drop")
	}
	w.exec(`UPDATE songs SET retired_at = ?, retired_into = ? WHERE id = ?`, "2026-10-06T12:00:00.000Z", info.Songs["d3"], song)
	w.inTx(func(ctx context.Context, tx *sql.Tx, env features.Env) error {
		ch, err := w.svc.ComputeTx(ctx, tx, env, nil, []string{song})
		if ch.Songs != 1 {
			t.Errorf("Songs changed = %d, want 1", ch.Songs)
		}
		return err
	})
	if w.count(`SELECT COUNT(*) FROM song_features WHERE song_id = ?`, song) != 0 {
		t.Error("the retired Song kept its row")
	}
	w.verifyEmpty()
}

// TestFeatures_SongDigest: the Song digest moves with the subject, the primary values and the draft text, and with nothing else.
func TestFeatures_SongDigest(t *testing.T) {
	t.Parallel()
	w, info := newL3World(t)
	digest := func(song string) string { return w.str(`SELECT digest FROM song_features WHERE song_id = ?`, song) }
	d1 := info.Songs["d1"]
	before := digest(d1)
	// an unrelated change (another Song's draft) leaves it
	w.exec(`UPDATE recipe_drafts SET request_json = json_set(request_json, '$.style', 'dance, energetic, skank, 132bpm, extra') WHERE id = ?`, info.Drafts3["d2"])
	w.settle()
	if digest(d1) != before {
		t.Error("another Song's edit moved the digest")
	}
	// the Song's own draft text moves it
	w.exec(`UPDATE recipe_drafts SET request_json = json_set(request_json, '$.style', 'reggae, chill, skank, 78bpm') WHERE id = ?`, info.Drafts3["d1"])
	w.settle()
	if digest(d1) == before {
		t.Error("the draft text did not move the digest")
	}
	if got := w.count(`SELECT bpm_x100 FROM song_features WHERE song_id = ?`, d1); got != 7800 {
		t.Errorf("bpm = %d, want 7800", got)
	}
	w.verifyEmpty()
}

// TestFeatures_HookPath: ComputeTx(nil, songIDs), as the sounds recompute hook calls it, rewrites only song_features rows,
// the primary values follow the Sound rows, and a second call changes nothing.
func TestFeatures_HookPath(t *testing.T) {
	t.Parallel()
	w, info := newL3World(t)
	song := info.Songs["T1"]
	takeHash := w.str(`SELECT group_concat(digest || computed_at) FROM take_features`)
	// change the Song's Sound rows as a taxonomy recompute would
	w.exec(`UPDATE song_sounds SET value = 'dub' WHERE song_id = ? AND facet = 'genre_family'`, song)
	var ch features.Changes
	w.inTx(func(ctx context.Context, tx *sql.Tx, env features.Env) (err error) {
		ch, err = w.svc.ComputeTx(ctx, tx, env, nil, []string{song})
		return err
	})
	if ch.Takes != 0 || ch.Songs != 1 {
		t.Errorf("hook ComputeTx changed %+v, want one Song and no take", ch)
	}
	if got := w.str(`SELECT family FROM song_features WHERE song_id = ?`, song); got != "dub" {
		t.Errorf("family = %q, want dub", got)
	}
	if w.str(`SELECT group_concat(digest || computed_at) FROM take_features`) != takeHash {
		t.Error("the hook path wrote take rows")
	}
	w.inTx(func(ctx context.Context, tx *sql.Tx, env features.Env) (err error) {
		ch, err = w.svc.ComputeTx(ctx, tx, env, nil, []string{song})
		return err
	})
	if ch != (features.Changes{}) {
		t.Errorf("second hook call changed %+v", ch)
	}
}

// TestFeatures_ReadersOneStatement: Take, Takes and Songs read what was written; absent ids are absent.
func TestFeatures_Readers(t *testing.T) {
	t.Parallel()
	w, info := newL3World(t)
	ctx := context.Background()
	takes, err := w.svc.Takes(ctx, []string{info.Takes["T1"], info.Takes["T2"], "nope"})
	if err != nil || len(takes) != 2 {
		t.Fatalf("Takes = %d %v", len(takes), err)
	}
	if _, ok, _ := w.svc.Take(ctx, "nope"); ok {
		t.Error("Take found a missing id")
	}
	var detail map[string]any
	if err := json.Unmarshal(takes[info.Takes["T1"]].Detail, &detail); err != nil {
		t.Errorf("detail is not JSON: %v", err)
	}
	if m, _ := w.svc.Takes(ctx, nil); len(m) != 0 {
		t.Error("Takes(nil) not empty")
	}
	if m, _ := w.svc.Songs(ctx, nil); len(m) != 0 {
		t.Error("Songs(nil) not empty")
	}
}
