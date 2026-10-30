package librarytest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestFixtureL3_Shape: the row counts of C146.1 by SQL; no Settle is run.
func TestFixtureL3_Shape(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	fx := FixtureL3(t, st, FixedNow)
	for k, want := range map[string]int{"T": 8, "u": 2, "d": 4} {
		n := 0
		for label := range fx.Songs {
			if strings.HasPrefix(label, k) {
				n++
			}
		}
		if k == "T" && n != 8 || k == "d" && n != 4 || k == "u" && n != 2 {
			t.Errorf("labels %s*: %d, want %d", k, n, want)
		}
	}
	if len(fx.Takes) != 8+9 || len(fx.Drafts3) != 5 {
		t.Fatalf("takes %d drafts %d", len(fx.Takes), len(fx.Drafts3))
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM tracks WHERE title LIKE 'Synthetic Take %'`:                                                                       8 + 9,
		`SELECT COUNT(*) FROM songs WHERE title LIKE 'Synthetic Song T_' OR title LIKE 'Synthetic Song u_' OR title LIKE 'Synthetic Song d_'`:   8 + 2 + 4,
		`SELECT COUNT(*) FROM songs WHERE origin = 'draft' AND (title LIKE 'Synthetic Song d_' OR title LIKE 'Synthetic Song u_')`:              5,
		`SELECT COUNT(*) FROM songs WHERE muse_id = '` + fx.Muses["M5"] + `'`:                                                                   8,
		`SELECT COUNT(*) FROM songs WHERE muse_id = '` + fx.Muses["M3"] + `'`:                                                                   3,
		`SELECT COUNT(*) FROM songs WHERE muse_id = '` + fx.Muses["M4"] + `'`:                                                                   2,
		`SELECT COUNT(*) FROM songs WHERE title = 'Synthetic Song d4' AND muse_id IS NULL`:                                                      1,
		`SELECT COUNT(*) FROM ratings r JOIN tracks t ON t.id = r.track_id WHERE t.title LIKE 'Synthetic Take T_' AND r.source = 'human'`:       7,
		`SELECT COUNT(*) FROM analyses a JOIN tracks t ON t.id = a.track_id WHERE t.title LIKE 'Synthetic Take T_' AND a.analyzer = 'tempo'`:    8,
		`SELECT COUNT(*) FROM analyses a JOIN tracks t ON t.id = a.track_id WHERE t.title LIKE 'Synthetic Take T_' AND a.analyzer = 'key'`:      8,
		`SELECT COUNT(*) FROM analyses a JOIN tracks t ON t.id = a.track_id WHERE t.title LIKE 'Synthetic Take T_' AND a.analyzer = 'energy'`:   8,
		`SELECT COUNT(*) FROM analyses WHERE analyzer = 'loudness' AND track_id IN (SELECT id FROM tracks WHERE title LIKE 'Synthetic Take %')`: 8 + 9,
		`SELECT COUNT(*) FROM analyses WHERE track_id = '` + fx.Takes["u1"] + `' AND analyzer IN ('tempo','key','energy')`:                      0,
		`SELECT COUNT(*) FROM recipe_drafts WHERE request_id LIKE 'l3_%'`:                                                                       5,
		`SELECT COUNT(*) FROM recipe_drafts WHERE request_json LIKE '%skank%'`:                                                                  5,
		`SELECT COUNT(*) FROM record_slots WHERE song_id IN (SELECT id FROM songs WHERE title LIKE 'Synthetic Song T_')`:                        0,
		`SELECT COUNT(*) FROM take_features`: 0,
		`SELECT COUNT(*) FROM song_features`: 0,
		`SELECT COUNT(*) FROM song_sounds WHERE song_id IN (SELECT id FROM songs WHERE title LIKE 'Synthetic Song T_')`:                                  0,
		`SELECT COUNT(*) FROM song_picks WHERE song_id IN (SELECT id FROM songs WHERE title LIKE 'Synthetic Song T_' OR title LIKE 'Synthetic Song u1')`: 9,
	} {
		if n := count(t, st, q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	// the three new Muses are ranked after the two of FixtureL2
	if got := str(t, st, `SELECT group_concat(ordinal, ',') FROM (SELECT ordinal FROM muses WHERE id IN (?, ?, ?) ORDER BY ordinal)`, fx.Muses["M3"], fx.Muses["M4"], fx.Muses["M5"]); got != "3,4,5" {
		t.Errorf("ordinals of M3, M4, M5 = %s", got)
	}
	var fk int
	if err := st.R.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Errorf("foreign_key_check rows = %d %v", fk, err)
	}
}

// oracle tables of C135.4, written out again here so the check shares nothing with `features`.
var (
	oracleMood   = map[string]int{"upbeat": 7500, "chill": 2500, "melancholic": 3000, "romantic": 3500, "dark": 5500, "dreamy": 2500, "energetic": 9000, "nostalgic": 4000, "aggressive": 9500, "warm": 3500}
	oracleGenre  = map[string]int{"ambient": 1500, "classical": 3000, "folk": 3500, "singer-songwriter": 3000, "jazz": 4000, "blues": 4000, "country": 4500, "reggae": 5000, "world": 5000, "cinematic": 5000, "r-and-b": 5000, "funk-soul": 6500, "pop": 6000, "latin": 6500, "electronic": 7000, "hip-hop": 7000, "rock": 7500, "dance": 8500, "punk": 9000, "metal": 9500}
	oracleBand   = map[string]int{"slow": 2500, "mid": 4500, "upbeat": 6500, "fast": 8000, "rapid": 9000}
	oracleBandOf = func(bpm int) string {
		switch {
		case bpm < 80:
			return "slow"
		case bpm < 110:
			return "mid"
		case bpm < 130:
			return "upbeat"
		case bpm < 160:
			return "fast"
		}
		return "rapid"
	}
)

// halfUp is the integer division n/d rounded half up.
func halfUp(n, d int) int { return (2*n + d) / (2 * d) }

// TestFixtureL3_LiteralFeatureRows: every literal row satisfies the C134 CHECKs (it is inserted into a real schema), the T1-T6
// rows equal the sequencer table, the predicted energies are the C135.4 arithmetic, and the measured columns equal the
// fixture's own analyses rows.
func TestFixtureL3_LiteralFeatureRows(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	fx := FixtureL3(t, st, FixedNow)
	InsertL3FeatureRows(t, st, FixedNow, fx)
	if n := count(t, st, `SELECT COUNT(*) FROM take_features`); n != 9 {
		t.Errorf("take_features rows = %d, want 9 (T1-T8, u1)", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM song_features`); n != 14 {
		t.Errorf("song_features rows = %d, want 14", n)
	}
	// the six-Song set of the sequencer table: BPM, energy, key
	for label, w := range map[string]struct {
		bpm, energy int
		key         string
	}{"T1": {96, 3000, "8A"}, "T2": {98, 4500, "8A"}, "T3": {100, 5500, "9A"}, "T4": {104, 7000, "9A"}, "T5": {108, 9000, "9B"}, "T6": {102, 4000, "8B"}} {
		f, ok := L3FeatureOf(label)
		if !ok {
			t.Fatalf("no literal for %s", label)
		}
		if *f.BPMx100 != w.bpm*100 || *f.Energyx10000 != w.energy || fmt.Sprintf("%d%s", *f.KeyNum, f.KeyLetter) != w.key || f.BPMBasis != "measured" || f.EnergyBasis != "measured" || f.KeyBasis != "measured" {
			t.Errorf("%s literal %+v differs from the table %+v", label, f, w)
		}
	}
	// predicted energies by plain arithmetic over the facets of the literal row
	for _, f := range L3Features {
		if f.EnergyBasis != "predicted" {
			continue
		}
		var means []int
		if f.Mood != "" {
			means = append(means, oracleMood[f.Mood])
		}
		if f.Family != "" {
			g := oracleGenre[f.Family]
			if f.Label == "u2" { // synthpop is two values of one facet: mean(electronic, pop)
				g = halfUp(oracleGenre["electronic"]+oracleGenre["pop"], 2)
			}
			means = append(means, g)
		}
		means = append(means, oracleBand[oracleBandOf(*f.BPMx100/100)])
		sum := 0
		for _, m := range means {
			sum += m
		}
		if want := halfUp(sum, len(means)); *f.Energyx10000 != want {
			t.Errorf("%s: predicted energy %d, the arithmetic gives %d", f.Label, *f.Energyx10000, want)
		}
	}
	// the C135.4 worked examples
	if got := halfUp(oracleMood["chill"]+oracleGenre["reggae"]+oracleBand["slow"], 3); got != 3333 {
		t.Errorf("chill + reggae + slow = %d", got)
	}
	if f, _ := L3FeatureOf("d1"); *f.Energyx10000 != 3333 {
		t.Errorf("d1 = %d", *f.Energyx10000)
	}
	if f, _ := L3FeatureOf("d2"); *f.Energyx10000 != 8500 {
		t.Errorf("d2 = %d: energetic + dance + fast", *f.Energyx10000)
	}
	// the measured columns equal what the fixture's analyses rows say
	for _, f := range L3Features {
		if f.SubjectKind != "take" || f.BPMMeasuredx100 == nil {
			continue
		}
		take := fx.Takes[f.Label]
		bpm := str(t, st, `SELECT json_extract(metrics_json, '$.bpm') FROM analyses WHERE track_id = ? AND analyzer = 'tempo'`, take)
		if want := fmt.Sprintf("%g", float64(*f.BPMMeasuredx100)/100); bpm != want {
			t.Errorf("%s: tempo analysis %s, literal %s", f.Label, bpm, want)
		}
		cam := str(t, st, `SELECT json_extract(metrics_json, '$.camelot') FROM analyses WHERE track_id = ? AND analyzer = 'key'`, take)
		if want := fmt.Sprintf("%d%s", *f.KeyNum, f.KeyLetter); cam != want {
			t.Errorf("%s: key analysis %s, literal %s", f.Label, cam, want)
		}
		en := str(t, st, `SELECT CAST(ROUND(json_extract(metrics_json, '$.energy') * 10000) AS INTEGER) FROM analyses WHERE track_id = ? AND analyzer = 'energy'`, take)
		if want := fmt.Sprint(*f.Energyx10000); en != want {
			t.Errorf("%s: energy analysis %s, literal %s", f.Label, en, want)
		}
		// the style declares the measured tempo: no mismatch anywhere
		style := str(t, st, `SELECT r.style FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, take)
		if !strings.Contains(style, fmt.Sprintf("%dbpm", *f.BPMx100/100)) {
			t.Errorf("%s: style %q does not declare %d bpm", f.Label, style, *f.BPMx100/100)
		}
	}
	// a literal row with a bad CHECK would not have been insertable: prove the CHECKs are live
	if _, err := st.W.Exec(`UPDATE take_features SET energy_x10000 = 10001`); err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("the CHECK on energy_x10000 is not live: %v", err)
	}
}

// TestFixtureL3_SettleAgrees runs the real Reconciler (Sounds only; the features step does not exist yet) over the fixture and
// checks that it changes no Pick and that the primary Sound values (smallest ord) equal the literal rows' columns.
func TestFixtureL3_SettleAgrees(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	fx := FixtureL3(t, st, FixedNow)
	picks := func() string {
		return str(t, st, `SELECT group_concat(song_id||take_id||source||last_event_id||changed_at, '|') FROM (SELECT * FROM song_picks WHERE song_id IN
			(SELECT id FROM songs WHERE title LIKE 'Synthetic Song T_' OR title = 'Synthetic Song u1') ORDER BY song_id)`)
	}
	before := picks()
	if rep := settleL2(t, st); rep.Passes == 0 {
		t.Fatal("the Settle did nothing")
	}
	if after := picks(); after != before {
		t.Errorf("Settle changed the Picks:\n%s\n%s", before, after)
	}
	primary := func(song, facet string) string {
		return str(t, st, `SELECT value FROM song_sounds WHERE song_id = ? AND facet = ? ORDER BY ord LIMIT 1`, song, facet)
	}
	for _, f := range L3Features {
		song := fx.Songs[f.Label]
		for facet, want := range map[string]string{"genre_family": f.Family, "vocal": f.Vocal, "era": f.Era, "mood": f.Mood, "language": f.Language} {
			if got := primary(song, facet); got != want {
				t.Errorf("%s: primary %s = %q, literal %q", f.Label, facet, got, want)
			}
		}
		want := oracleBandOf(*f.BPMx100 / 100)
		if got := primary(song, "tempo_band"); got != want {
			t.Errorf("%s: tempo_band %q, want %q", f.Label, got, want)
		}
	}
	// `skank` is unmapped on exactly the five Songs that carry it
	if n := count(t, st, `SELECT COUNT(DISTINCT song_id) FROM song_unmapped_terms WHERE term = 'skank'`); n != 5 {
		t.Errorf("skank unmapped on %d Songs, want 5", n)
	}
	// ord is unique within every Song of the world
	if n := count(t, st, `SELECT COUNT(*) FROM (SELECT song_id, ord FROM song_sounds GROUP BY song_id, ord HAVING COUNT(*) > 1)`); n != 0 {
		t.Errorf("%d tied ord values", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM provenance_dirty`); n != 0 {
		t.Errorf("provenance_dirty holds %d rows after Settle", n)
	}
}

func TestFixtureL3Scale_Shape(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	info := FixtureL3Scale(t, st, FixedNow, 45, 4)
	if len(info.Songs) != 45 || len(info.Muses) != 4 {
		t.Fatalf("songs %d muses %d", len(info.Songs), len(info.Muses))
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM songs`:         45,
		`SELECT COUNT(*) FROM tracks`:        90,
		`SELECT COUNT(*) FROM take_features`: 90,
		`SELECT COUNT(*) FROM song_features`: 45,
		`SELECT COUNT(*) FROM song_features sf JOIN song_picks sp ON sp.song_id = sf.song_id WHERE sf.subject_id = sp.take_id`: 45,
		`SELECT COUNT(*) FROM records`: 0,
	} {
		if n := count(t, st, q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	var fk int
	if err := st.R.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Errorf("foreign_key_check rows = %d %v", fk, err)
	}
}

var syntheticL3 = regexp.MustCompile(`^Synthetic [A-Za-z0-9 ]+\.?$`)

// TestFixtures_L3_SyntheticOnly: every text the L3 fixtures write is synthetic, and none is a URL, a path or a name.
func TestFixtures_L3_SyntheticOnly(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	fx := FixtureL3(t, st, FixedNow)
	InsertL3FeatureRows(t, st, FixedNow, fx)
	scale := openL1(t)
	FixtureL3Scale(t, scale, FixedNow, 12, 3)
	for _, db := range []*store.Store{st, scale} {
		for _, q := range []string{
			`SELECT title FROM muses WHERE title LIKE 'Synthetic Agent%'`, `SELECT artist FROM muses WHERE artist LIKE 'Synthetic Agent%'`,
			`SELECT title FROM songs WHERE title LIKE 'Synthetic Song T%' OR title LIKE 'Synthetic Song u%' OR title LIKE 'Synthetic Song d%'`,
			`SELECT title FROM tracks WHERE title LIKE 'Synthetic Take T%' OR title LIKE 'Synthetic Take u%'`,
		} {
			for _, v := range column(t, db, q) {
				if !syntheticL3.MatchString(v) {
					t.Errorf("%s: %q is not synthetic text", q, v)
				}
			}
		}
	}
	for _, q := range []string{`SELECT style FROM recipes WHERE request_id LIKE 'l3_%'`, `SELECT request_json FROM recipe_drafts WHERE request_id LIKE 'l3_%'`,
		`SELECT metrics_json FROM analyses WHERE analyzer IN ('key', 'energy', 'tempo')`} {
		for _, v := range column(t, st, q) {
			if strings.Contains(v, "http") || strings.Contains(v, "@") || strings.Contains(v, "://") || strings.Contains(v, "/home") {
				t.Errorf("%s: %q", q, v)
			}
		}
	}
	// the labels are stable
	labels := make([]string, 0, len(L3Features))
	for _, f := range L3Features {
		labels = append(labels, f.Label)
	}
	if !slices.Equal(labels, []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8", "u1", "d1", "d2", "d3", "u2", "d4"}) {
		t.Errorf("literal labels %v", labels)
	}
}
