package librarytest

import (
	"context"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// L3Feature is one expected take_features / song_features row of the L3 fixture, written down as literals from the tables of
// C135 by hand, so the `features` tests have an oracle that shares no code with `features.DeriveTake`. A nil pointer or an
// empty string is NULL. The digest is not part of the oracle (it is defined by the code that computes it): InsertL3FeatureRows
// stores a placeholder, and detail_json is NULL.
type L3Feature struct {
	Label string // the Song label ("T1".."T8", "u1", "u2", "d1".."d4"); the take row of a take Song has the same label
	// SubjectKind is "take" or "draft"; the take of a take Song is Info.Takes[Label].
	SubjectKind                        string
	BPMx100                            *int
	BPMBasis                           string
	BPMRequestedx100, BPMMeasuredx100  *int
	Energyx10000                       *int
	EnergyBasis                        string
	KeyNum                             *int
	KeyLetter, KeyBasis                string
	DurationS                          *float64
	Family, Vocal, Era, Mood, Language string // the primary Sound values (song_features only)
}

func ip3(v int) *int         { return &v }
func fp3(v float64) *float64 { return &v }

// measured builds the row of a take Song whose Ears analyses and style agree: tempo, key and energy all `measured`.
func measured(label string, bpm, energyX int, camelotNum int, letter string, dur float64, family, vocal, era, mood string) L3Feature {
	return L3Feature{Label: label, SubjectKind: "take", BPMx100: ip3(bpm * 100), BPMBasis: "measured", BPMRequestedx100: ip3(bpm * 100), BPMMeasuredx100: ip3(bpm * 100),
		Energyx10000: ip3(energyX), EnergyBasis: "measured", KeyNum: ip3(camelotNum), KeyLetter: letter, KeyBasis: "measured", DurationS: fp3(dur),
		Family: family, Vocal: vocal, Era: era, Mood: mood}
}

// requested builds a row that has only the declared tempo and a predicted energy (a draft Song, or a take with no Ears row).
func requested(label, kind string, bpm, energyX int, dur *float64, family, vocal, era, mood string) L3Feature {
	return L3Feature{Label: label, SubjectKind: kind, BPMx100: ip3(bpm * 100), BPMBasis: "declared", BPMRequestedx100: ip3(bpm * 100),
		Energyx10000: ip3(energyX), EnergyBasis: "predicted", DurationS: dur, Family: family, Vocal: vocal, Era: era, Mood: mood}
}

// L3Features is the expected derived state of the L3 Songs, in fixture order. T1-T6 are the six-Song set of the sequencer
// golden (BPM 96, 98, 100, 104, 108, 102; keys 8A, 8A, 9A, 9A, 9B, 8B; energies 0.30, 0.45, 0.55, 0.70, 0.90, 0.40). The
// predicted energies are the C135.4 arithmetic: the mean of the facet means (mood, genre family, tempo band), half up.
var L3Features = []L3Feature{
	measured("T1", 96, 3000, 8, "A", 200, "reggae", "male", "", "chill"),
	measured("T2", 98, 4500, 8, "A", 205, "reggae", "female", "", "upbeat"),
	measured("T3", 100, 5500, 9, "A", 210, "pop", "female", "", "warm"),
	measured("T4", 104, 7000, 9, "A", 215, "pop", "male", "", "energetic"),
	measured("T5", 108, 9000, 9, "B", 220, "dance", "female", "", "energetic"),
	measured("T6", 102, 4000, 8, "B", 225, "funk-soul", "male", "", "upbeat"),
	measured("T7", 90, 3500, 5, "A", 230, "jazz", "male", "80s", "chill"),
	measured("T8", 120, 8000, 12, "B", 235, "rock", "male", "", "aggressive"),
	// u1: one take, no Ears analysis: declared tempo, predicted energy (chill 2500, pop 6000, mid 4500 => 13000/3 = 4333), no key
	requested("u1", "take", 100, 4333, fp3(190), "pop", "", "", "chill"),
	// the draft Songs: declared tempo from the style, predicted energy, no key, no duration
	requested("d1", "draft", 76, 3333, nil, "reggae", "", "", "chill"),     // chill 2500, reggae 5000, slow 2500 => 3333
	requested("d2", "draft", 132, 8500, nil, "dance", "", "", "energetic"), // energetic 9000, dance 8500, fast 8000 => 8500
	requested("d3", "draft", 90, 4750, nil, "reggae", "", "", ""),          // reggae 5000, mid 4500 => 4750 (no mood facet)
	requested("u2", "draft", 120, 6500, nil, "electronic", "", "", ""),     // synthpop = electronic 7000 + pop 6000 => 6500; upbeat 6500 => 6500
	requested("d4", "draft", 140, 7000, nil, "rock", "", "", "dark"),       // dark 5500, rock 7500, fast 8000 => 7000
}

// L3FeatureOf returns the literal row of a Song label.
func L3FeatureOf(label string) (L3Feature, bool) {
	for _, f := range L3Features {
		if f.Label == label {
			return f, true
		}
	}
	return L3Feature{}, false
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// InsertL3FeatureRows writes the literal take_features rows of the take Songs and the literal song_features rows of every L3
// Song into st (schema 11), so a test can run the readers (Atlas, adherence, sequencing) without the features service. The
// digests are placeholders (sha256 of the label), so a real ComputeTx rewrites every row.
func InsertL3FeatureRows(tb testing.TB, st *store.Store, now time.Time, info L3Info) {
	tb.Helper()
	tx := begin(tb, st)
	at := store.FormatTime(now)
	for _, f := range L3Features {
		digest := Sha("l3-literal/" + f.Label)
		song := info.Songs[f.Label]
		if song == "" {
			tb.Fatalf("fixture L3: no Song %s", f.Label)
		}
		subject := info.Drafts3[f.Label]
		if f.SubjectKind == "take" {
			take := info.Takes[f.Label]
			subject = take
			_, err := tx.ExecContext(context.Background(), `INSERT INTO take_features(track_id,bpm_x100,bpm_basis,bpm_requested_x100,bpm_measured_x100,bpm_octave,
				energy_x10000,energy_basis,key_num,key_letter,key_basis,duration_s,digest,feature_def,computed_at) VALUES(?,?,?,?,?,0,?,?,?,?,?,?,?,'mm-features/1',?)`,
				take, nullInt(f.BPMx100), nullStr(f.BPMBasis), nullInt(f.BPMRequestedx100), nullInt(f.BPMMeasuredx100), nullInt(f.Energyx10000), nullStr(f.EnergyBasis),
				nullInt(f.KeyNum), nullStr(f.KeyLetter), nullStr(f.KeyBasis), nullFloat(f.DurationS), digest, at)
			check(tb, err)
		}
		_, err := tx.ExecContext(context.Background(), `INSERT INTO song_features(song_id,subject_kind,subject_id,bpm_x100,bpm_basis,bpm_requested_x100,bpm_measured_x100,bpm_octave,
			energy_x10000,energy_basis,key_num,key_letter,key_basis,duration_s,family,vocal,era,mood,language,digest,feature_def,computed_at)
			VALUES(?,?,?,?,?,?,?,0,?,?,?,?,?,?,?,?,?,?,?,?,'mm-features/1',?)`,
			song, f.SubjectKind, subject, nullInt(f.BPMx100), nullStr(f.BPMBasis), nullInt(f.BPMRequestedx100), nullInt(f.BPMMeasuredx100), nullInt(f.Energyx10000), nullStr(f.EnergyBasis),
			nullInt(f.KeyNum), nullStr(f.KeyLetter), nullStr(f.KeyBasis), nullFloat(f.DurationS), nullStr(f.Family), nullStr(f.Vocal), nullStr(f.Era), nullStr(f.Mood), nullStr(f.Language), digest, at)
		check(tb, err)
	}
	check(tb, tx.Commit())
}
