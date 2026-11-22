package ears_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// recoverSung is an invented Italian lyric (no real lyrics in tests).
const recoverSung = "[verse]\nla luna cammina sul prato\nil vento racconta una storia\n[chorus]\nsole sole porta il giorno\ncanta con me fino a sera"

// refRig is a rig whose library also holds the fixture's reference FLAC and lossy reference.
func refRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(t.Context(), filepath.Join(dir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.WriteFiles(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	now := lt.FixedNow
	r := &rig{t: t, st: st, root: root, dir: dir, now: now, tier: earstest.NewTierA()}
	r.fx = lt.FixtureM2c(t, st, now)
	r.lib = library.New(st, root, func() time.Time { return now })
	r.opts = ears.Options{Store: st, Library: r.lib, Data: root, DataDir: dir, Analyzer: r.tier, Now: func() time.Time { return now }}
	r.svc = ears.New(r.opts)
	return r
}

func wantProblem(t *testing.T, err error, status int, code string) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != status || pe.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
	return pe
}

func (r *rig) recoverInput(id, style string) (ears.RecoverInput, error) {
	r.t.Helper()
	return r.svc.RecoverInput(r.t.Context(), id, ears.RecoverOptions{Style: style})
}

// TestRecoverInput_Refusals: the states of the listening check that cannot be recovered from, each with its state and hint; a track with a
// known recipe; an unknown track.
func TestRecoverInput_Refusals(t *testing.T) {
	t.Parallel()
	r := refRig(t)
	if _, err := r.recoverInput("nope", ""); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
	if _, err := r.recoverInput(lt.TrackLuna, ""); err == nil {
		t.Error("a known recipe was accepted")
	} else {
		wantProblem(t, err, http.StatusUnprocessableEntity, "RECIPE_KNOWN")
	}
	state := func(id string, want, hint string) {
		t.Helper()
		_, err := r.recoverInput(id, "")
		pe := wantProblem(t, err, http.StatusConflict, "ANALYSIS_UNAVAILABLE")
		if pe.Details["analyzer"] != "tier_b" || pe.Details["state"] != want || !strings.Contains(pe.Details["hint"].(string), hint) {
			t.Errorf("details = %v, want state %s hint %q", pe.Details, want, hint)
		}
	}
	state(lt.TrackRefFlac, "none", "estimate an analyze item")
	// a stored document nobody analysed yet
	r.putDoc(lt.TrackRefFlac, "analysis.json", tierbtest.FromLyrics(recoverSung, tierbtest.Opts{}), time.Hour)
	state(lt.TrackRefFlac, "pending", "still running")
	r.analyze(lt.TrackRefFlac)
	if _, err := r.recoverInput(lt.TrackRefFlac, ""); err != nil {
		t.Fatal(err)
	}
	// an error document and a skipped stage hold no transcript
	r.putDoc(lt.TrackLossy, "analysis.json", tierbtest.FromLyrics(recoverSung, tierbtest.Opts{Status: "error"}), time.Hour)
	r.analyze(lt.TrackLossy)
	state(lt.TrackLossy, "error", "no transcript")
}

// TestRecoverInput_StyleAndEvidence: the caller's style wins and is trimmed; without one the placeholder is built from the language and the
// stored tempo and key; the evidence carries what was measured, and nothing is written.
func TestRecoverInput_StyleAndEvidence(t *testing.T) {
	t.Parallel()
	r := refRig(t)
	r.putDoc(lt.TrackRefFlac, "analysis.json", tierbtest.FromLyrics(recoverSung, tierbtest.Opts{}), time.Hour)
	r.analyze(lt.TrackRefFlac)
	rows := func() int {
		return r.count(`SELECT (SELECT COUNT(*) FROM analyses) + (SELECT COUNT(*) FROM recipe_drafts) + (SELECT COUNT(*) FROM jobs) + (SELECT COUNT(*) FROM sections)`)
	}
	in, err := r.recoverInput(lt.TrackRefFlac, "")
	if err != nil {
		t.Fatal(err)
	}
	if in.StyleSource != ears.StylePlaceholder || in.Style != "italian vocals" || in.Evidence.TempoBPM != nil || in.Evidence.Key != nil {
		t.Errorf("no Tier A tempo or key: style %q (%s) evidence %+v", in.Style, in.StyleSource, in.Evidence)
	}
	if in.Evidence.LufsI == nil || in.Evidence.DurationS <= 0 || in.TrackID != lt.TrackRefFlac || len(in.AnalysisSHA256) != 64 || in.Title == "" {
		t.Errorf("input = %+v", in)
	}
	if in.Transcript.Language != "it" || in.Transcript.Words != 21 || !strings.Contains(in.Transcript.Lyrics, "[verse]\nla luna cammina sul prato\n") {
		t.Errorf("transcript = %+v", in.Transcript)
	}

	r.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'tempo', 'mm-tempo/1 x', '1', '{"bpm":103.6,"confidence":0.9,"candidates":[],"octave_ambiguous":false,"analyzed_s":6}', ?)`, lt.TrackRefFlac, "2026-10-01T12:00:00Z")
	r.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'key', 'mm-key/1 x', '1', '{"key":"A minor","tonic":9,"mode":"minor","camelot":"8A","r":0.8,"margin":0.1,"ambiguous_relative":false,"tuning_cents":0,"alternatives":[],"analyzed_s":6}', ?)`, lt.TrackRefFlac, "2026-10-01T12:00:00Z")
	r.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'energy', 'mm-energy/1 x', '1', '{"energy":0.7,"onset_rate":2,"centroid_hz":1500,"lufs":-13,"analyzed_s":6}', ?)`, lt.TrackRefFlac, "2026-10-01T12:00:00Z")
	before := rows()
	in, err = r.recoverInput(lt.TrackRefFlac, "")
	if err != nil {
		t.Fatal(err)
	}
	if in.Style != "italian vocals, 104 BPM, A minor" || in.Evidence.TempoBPM == nil || *in.Evidence.TempoBPM != 103.6 || in.Evidence.Energy == nil || *in.Evidence.Energy != 0.7 {
		t.Errorf("style %q evidence %+v", in.Style, in.Evidence)
	}
	in, err = r.recoverInput(lt.TrackRefFlac, "  dream pop, female vocal  ")
	if err != nil || in.StyleSource != ears.StyleCaller || in.Style != "dream pop, female vocal" {
		t.Errorf("caller style: %+v (%v)", in, err)
	}
	if got := rows(); got != before {
		t.Errorf("RecoverInput wrote rows: %d -> %d", before, got)
	}
}

// TestRecoverInput_BadDocuments: a stored document that no longer matches its row, or an analysis that holds no sung word, is refused with
// the matching problem, never a 500.
func TestRecoverInput_BadDocuments(t *testing.T) {
	t.Parallel()
	r := refRig(t)
	r.putDoc(lt.TrackRefFlac, "analysis.json", tierbtest.FromLyrics(recoverSung, tierbtest.Opts{DropSections: map[int]bool{0: true, 1: true}}), time.Hour)
	r.analyze(lt.TrackRefFlac)
	_, err := r.recoverInput(lt.TrackRefFlac, "")
	wantProblem(t, err, http.StatusUnprocessableEntity, "TRANSCRIPT_EMPTY")

	r2 := refRig(t)
	r2.putDoc(lt.TrackRefFlac, "analysis.json", tierbtest.FromLyrics(recoverSung, tierbtest.Opts{}), time.Hour)
	r2.analyze(lt.TrackRefFlac)
	r2.dropTriggers("artifacts_immutable")
	r2.exec(`UPDATE artifacts SET sha256 = ? WHERE track_id = ? AND name = 'analysis.json'`, strings.Repeat("0", 64), lt.TrackRefFlac)
	_, err = r2.recoverInput(lt.TrackRefFlac, "")
	pe := wantProblem(t, err, http.StatusConflict, "ANALYSIS_UNAVAILABLE")
	if pe.Details["state"] != "error" {
		t.Errorf("details = %v", pe.Details)
	}
}
