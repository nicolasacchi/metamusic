package ears_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// lunaTune is what the fixture's Luna score plays in each of its three sections: C D E F C D E F (two bars of four eighths).
var lunaTune = []int{60, 62, 64, 65, 60, 62, 64, 65}

// singDoc returns a Tier B document for Luna whose pitch contour sings lunaTune (shifted by semis) at the start of every
// section the synthetic performance places, six frames per note.
func singDoc(t *testing.T, semis int) []byte {
	t.Helper()
	opts := tierbtest.Opts{Transcribe: true}
	raw, err := tierb.Parse(tierbtest.FromLyrics(lunaLyrics, opts))
	if err != nil {
		t.Fatal(err)
	}
	raw.Pitch = &tierb.Pitch{HopS: tierb.PitchHopS, Cents: make([]int, len(raw.Pitch.Cents)), Conf: make([]int, len(raw.Pitch.Cents))}
	for _, sp := range tierbtest.Truth(lunaLyrics, opts) {
		for i, p := range lunaTune {
			for f := 0; f < 6; f++ {
				at := int(sp.StartS/tierb.PitchHopS) + 6*i + f
				raw.Pitch.Cents[at], raw.Pitch.Conf[at] = (p+semis)*100, 90
			}
		}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// alignedABC is the fixture's score of Luna (internal/library/librarytest): 120 quarters per minute, key C, three sections.
const alignedABC = "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|CDEF|\n% chorus\nCDEF|CDEF|\n% outro\nCDEF|CDEF|]\n"

// putScore writes the score file the fixture's scores row points at (the fixture stores the row only).
func (r *rig) putScore(trackID string) {
	r.t.Helper()
	path := r.str(`SELECT abc_path FROM scores WHERE track_id = ?`, trackID)
	if err := r.root.WriteFile(path, []byte(alignedABC), 0o444); err != nil {
		r.t.Fatal(err)
	}
}

// putTierA stores Tier A tempo and key rows (104 BPM, A minor) of a track.
func (r *rig) putTierA(trackID string) {
	r.t.Helper()
	for an, row := range map[string][2]string{
		"tempo": {analysis.TempoDef, `{"bpm":104,"confidence":6,"candidates":[],"octave_ambiguous":false,"analyzed_s":12}`},
		"key":   {analysis.KeyDef, `{"key":"A minor","tonic":9,"mode":"minor","camelot":"8A","r":0.9,"margin":0.2,"ambiguous_relative":false,"tuning_cents":0,"alternatives":[],"analyzed_s":12}`},
	} {
		r.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, ?, ?, 'A', ?, ?)`,
			trackID, an, row[0]+" test", row[1], store.FormatTime(r.now))
	}
}

func (r *rig) adherenceRow(trackID string) (version string, doc ears.TierBAdherence) {
	r.t.Helper()
	var m string
	if err := r.st.R.QueryRow(`SELECT version, metrics_json FROM analyses WHERE track_id = ? AND analyzer = 'tier_b_adherence'`, trackID).Scan(&version, &m); err != nil {
		r.t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(m), &doc); err != nil {
		r.t.Fatal(err)
	}
	return version, doc
}

// TestAnalyzeTierB_AdherenceStored: a take with a plan score and a pitch contour gets a tier_b_adherence row; the read surface shows
// it, perception carries the block and a clean performance raises no flag.
func TestAnalyzeTierB_AdherenceStored(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putScore(lt.TrackLuna)
	r.putTierA(lt.TrackLuna)
	sha := r.putDoc(lt.TrackLuna, "analysis.json", singDoc(t, 0), time.Hour)
	out := r.analyze(lt.TrackLuna)
	if !contains(out.Wrote, "tier_b_adherence") {
		t.Fatalf("wrote %v", out.Wrote)
	}
	version, a := r.adherenceRow(lt.TrackLuna)
	if want := "mm-adherence/1 go1 " + sha[:12]; version != want {
		t.Errorf("version = %q, want %q", version, want)
	}
	if a.Basis != "coarse" || a.TimingSource != "measured" || a.MelodyChroma == nil || *a.MelodyChroma != 1 || a.TransposeMode == nil || *a.TransposeMode != 0 || len(a.Sections) != 3 {
		t.Fatalf("adherence = %+v", a)
	}
	if a.Sections[0].Label != "verse" || a.Sections[0].NotesPlan != 8 || a.Sections[0].NotesSung != 8 {
		t.Errorf("verse = %+v", a.Sections[0])
	}
	if a.TempoRatio == nil || *a.TempoRatio != 0.867 || a.KeyRelation != "relative" {
		t.Errorf("Tier A relation: tempo %v key %q, want 0.867 relative", a.TempoRatio, a.KeyRelation)
	}
	ctx := context.Background()
	tb, err := r.svc.TierB(ctx, lt.TrackLuna)
	if err != nil || tb.Adherence == nil || *tb.Adherence.MelodyChroma != 1 {
		t.Fatalf("TierB adherence = %+v, %v", tb.Adherence, err)
	}
	for _, f := range tb.Flags {
		if f.Code == "melody_off_plan" || f.Code == "sung_transposed" {
			t.Errorf("unexpected flag %s", f.Code)
		}
	}
	if !slicesContainsPrefix(tb.BlindSpots, "melody adherence measured coarsely") {
		t.Errorf("blind spots = %q", tb.BlindSpots)
	}
	if p, err := r.svc.Perception(ctx, lt.TrackLuna); err != nil || p.Adherence == nil || p.Adherence.Basis != "coarse" || p.Adherence.MelodyChroma == nil {
		t.Errorf("perception adherence = %+v, %v", p.Adherence, err)
	}
	// idempotent: the row exists, nothing is written twice
	if again := r.analyze(lt.TrackLuna); contains(again.Wrote, "tier_b_adherence") {
		t.Errorf("second run wrote %v", again.Wrote)
	}
}

// TestAnalyzeTierB_AdherenceTransposedFlags: a take sung a fifth up carries sung_transposed (info) and no melody_off_plan,
// because the best transposition matches the plan.
func TestAnalyzeTierB_AdherenceTransposedFlags(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putScore(lt.TrackLuna)
	r.putDoc(lt.TrackLuna, "analysis.json", singDoc(t, 7), time.Hour)
	r.analyze(lt.TrackLuna)
	_, a := r.adherenceRow(lt.TrackLuna)
	if a.TransposeMode == nil || *a.TransposeMode != -5 || *a.MelodyChroma != 1 {
		t.Fatalf("adherence = %+v", a)
	}
	tb, err := r.svc.TierB(context.Background(), lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range tb.Flags {
		got = append(got, f.Code)
	}
	if !contains(got, "sung_transposed") || contains(got, "melody_off_plan") {
		t.Errorf("flags = %v", got)
	}
}

func TestAnalyzeTierB_AdherenceAbsent(t *testing.T) {
	t.Parallel()
	t.Run("no pitch contour", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, withTierARows())

		r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{}), time.Hour)
		r.analyze(lt.TrackLuna)
		if n := r.count(`SELECT count(*) FROM analyses WHERE analyzer = 'tier_b_adherence'`); n != 0 {
			t.Errorf("rows = %d", n)
		}
	})
	t.Run("no score", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, withTierARows())

		r.putDoc(lt.TrackCitta, "analysis.json", tierbtest.FromLyrics("[Intro]\nhum la la", tierbtest.Opts{Transcribe: true}), time.Hour)
		if out := r.analyze(lt.TrackCitta); contains(out.Wrote, "tier_b_adherence") {
			t.Errorf("wrote %v", out.Wrote)
		}
	})
	t.Run("error document", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, withTierARows())

		r.putDoc(lt.TrackLuna, "analysis.json", tierbtest.FromLyrics(lunaLyrics, tierbtest.Opts{Transcribe: true, Status: "error"}), time.Hour)
		r.analyze(lt.TrackLuna)
		if n := r.count(`SELECT count(*) FROM analyses WHERE analyzer = 'tier_b_adherence'`); n != 0 {
			t.Errorf("rows = %d", n)
		}
	})
	t.Run("score file missing leaves no row", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, withTierARows())

		r.dropTriggers("scores_immutable")
		r.exec(`UPDATE scores SET abc_path = 'takes/nowhere/score.abc' WHERE track_id = ?`, lt.TrackLuna)
		r.putDoc(lt.TrackLuna, "analysis.json", singDoc(t, 0), time.Hour)
		r.analyze(lt.TrackLuna)
		if n := r.count(`SELECT count(*) FROM analyses WHERE analyzer = 'tier_b_adherence'`); n != 0 {
			t.Errorf("rows = %d", n)
		}
	})
	t.Run("score that no longer matches its hash stores an empty comparison", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, withTierARows())

		r.putScore(lt.TrackLuna)
		r.dropTriggers("scores_immutable")
		r.exec(`UPDATE scores SET abc_sha256 = ? WHERE track_id = ?`, strings.Repeat("0", 64), lt.TrackLuna)
		r.putDoc(lt.TrackLuna, "analysis.json", singDoc(t, 0), time.Hour)
		r.analyze(lt.TrackLuna)
		_, a := r.adherenceRow(lt.TrackLuna)
		if a.MelodyChroma != nil || a.TimingSource != "none" || len(a.Sections) != 0 {
			t.Errorf("adherence = %+v", a)
		}
	})
}

// TestTierBSweep_RecomputesSupersededAdherence: an adherence row of a superseded definition makes the take a candidate; the pass
// writes the current row and leaves the old one.
func TestTierBSweep_RecomputesSupersededAdherence(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	r.putScore(lt.TrackLuna)
	r.putDoc(lt.TrackLuna, "analysis.json", singDoc(t, 0), time.Hour)
	r.analyze(lt.TrackLuna)
	r.dropTriggers("analyses_immutable")
	r.exec(`UPDATE analyses SET version = 'mm-adherence/0 go1 old' WHERE analyzer = 'tier_b_adherence'`)
	rep, err := r.svc.TierBSweep(context.Background())
	if err != nil || rep.Analysed != 1 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
	if n := r.count(`SELECT count(*) FROM analyses WHERE analyzer = 'tier_b_adherence'`); n != 2 {
		t.Errorf("adherence rows = %d, want the old and the current", n)
	}
	if rep, _ := r.svc.TierBSweep(context.Background()); rep.Candidates != 0 {
		t.Errorf("second pass = %+v", rep)
	}
}
