package features

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

// The analyzer definitions features reads (C135.2); a row of any other definition is ignored by SelectAnalysis.
const (
	tempoDef  = analysis.TempoDef
	keyDef    = analysis.KeyDef
	energyDef = analysis.EnergyDef
)

// db is the statement surface of a transaction; cache prepares each statement once per transaction.
type db interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

type cache struct {
	tx *sql.Tx
	m  map[string]*sql.Stmt
}

func newCache(tx *sql.Tx) *cache { return &cache{tx: tx, m: map[string]*sql.Stmt{}} }

func (c *cache) prep(ctx context.Context, q string) (*sql.Stmt, error) {
	if st, ok := c.m[q]; ok {
		return st, nil
	}
	st, err := c.tx.PrepareContext(ctx, q)
	if err != nil {
		return nil, err
	}
	c.m[q] = st
	return st, nil
}

func (c *cache) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	st, err := c.prep(ctx, q)
	if err != nil {
		return nil, err
	}
	return st.QueryContext(ctx, args...)
}

func (c *cache) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	st, err := c.prep(ctx, q)
	if err != nil {
		return c.tx.QueryRowContext(ctx, q, args...) // surfaces the prepare error through Scan
	}
	return st.QueryRowContext(ctx, args...)
}

func (c *cache) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	st, err := c.prep(ctx, q)
	if err != nil {
		return nil, err
	}
	return st.ExecContext(ctx, args...)
}

func (c *cache) Close() {
	for _, st := range c.m {
		_ = st.Close()
	}
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func sumJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // the digests marshal plain structs of strings, integers and finite floats
	}
	return sum(b)
}

// ref names an analysis row that fed a digest: its definition text and the hash of its metrics.
type ref struct {
	Version string `json:"v"`
	Sum     string `json:"sha256"`
}

// takeLoad is the derivation input of one take with the facts its digest covers.
type takeLoad struct {
	In     TakeInput
	Digest string
}

// requestText is the part of a request JSON features reads: the style text and the inline ABC score.
type requestText struct {
	Style string  `json:"style"`
	ABC   *string `json:"abc"`
}

func readRequest(raw string) requestText {
	var r requestText
	_ = json.Unmarshal([]byte(raw), &r) // an unreadable request is "no style, no score", never an error
	return r
}

func keyGuess(tonic int, mode string, ok bool) *KeyGuess {
	if !ok {
		return nil
	}
	return &KeyGuess{TonicPC: tonic, Mode: mode}
}

// textInputs derives the tempo and key inputs that come from request text alone (style and inline ABC).
func textInputs(r requestText) (styleBPM *int, styleKey, planKey *KeyGuess, planBPM, expected *float64) {
	if _, b := yue2.StyleTerms(r.Style); b > 0 {
		styleBPM = ptr(b)
	}
	styleKey = keyGuess(ParseStyleKey(r.Style))
	if r.ABC != nil {
		planKey = keyGuess(ParseABCKey(*r.ABC))
		if sc, err := yue2.ParseScore([]byte(*r.ABC)); err == nil {
			if sc.BPM > 0 {
				planBPM = ptr(sc.BPM)
			}
			if sc.SecondsPerBar > 0 && sc.Bars > 0 {
				expected = ptr(float64(sc.Bars) * sc.SecondsPerBar)
			}
		}
	}
	return
}

// facetsOf reads the three facets of the energy prior from a Sound table (take_sounds or song_sounds).
func facetsOf(ctx context.Context, q db, table, keyCol, id string) (Facets, error) {
	//nolint:gosec // G202: table and keyCol are constants of this package
	rows, err := q.QueryContext(ctx, `SELECT facet, value FROM `+table+` WHERE `+keyCol+` = ? AND facet IN ('mood','genre_family','tempo_band') ORDER BY facet, value`, id)
	if err != nil {
		return Facets{}, fmt.Errorf("features: facets: %w", err)
	}
	defer rows.Close()
	var f Facets
	var last [2]string
	for rows.Next() {
		var facet, value string
		if err := rows.Scan(&facet, &value); err != nil {
			return f, err
		}
		if [2]string{facet, value} == last {
			continue
		}
		last = [2]string{facet, value}
		switch facet {
		case "mood":
			f.Mood = append(f.Mood, value)
		case "genre_family":
			f.GenreFamily = append(f.GenreFamily, value)
		case "tempo_band":
			f.TempoBand = append(f.TempoBand, value)
		}
	}
	return f, rows.Err()
}

// dspEngine is recipes.engine of a transform Take (library.EngineDSP; features imports no library).
const dspEngine = "dsp"

type measuredDoc struct {
	BPM             *float64 `json:"bpm"`
	Confidence      float64  `json:"confidence"`
	OctaveAmbiguous bool     `json:"octave_ambiguous"`
	Tonic           *int     `json:"tonic"`
	Mode            *string  `json:"mode"`
	R               float64  `json:"r"`
	Margin          float64  `json:"margin"`
	AmbiguousRel    bool     `json:"ambiguous_relative"`
	Energy          *float64 `json:"energy"`
}

// loadTake reads everything DeriveTake needs for one take. ok is false when the take does not exist.
func loadTake(ctx context.Context, q db, id string) (tl takeLoad, ok bool, err error) {
	var engine, style, reqJSON string
	var declared sql.NullInt64
	var dur float64
	var plan, spb sql.NullFloat64
	var bars sql.NullInt64
	err = q.QueryRowContext(ctx, `SELECT r.engine, r.style, r.request_json, t.bpm_declared, t.duration_s, sc.bpm, sc.bars, sc.seconds_per_bar
		FROM tracks t JOIN recipes r ON r.id = t.recipe_id LEFT JOIN scores sc ON sc.track_id = t.id WHERE t.id = ?`, id).
		Scan(&engine, &style, &reqJSON, &declared, &dur, &plan, &bars, &spb)
	if errors.Is(err, sql.ErrNoRows) {
		return tl, false, nil
	}
	if err != nil {
		return tl, false, fmt.Errorf("features: load take: %w", err)
	}
	req := readRequest(reqJSON)
	if strings.TrimSpace(req.Style) == "" {
		req.Style = style
	}
	if engine == dspEngine {
		// D-T1-24: a transform Take is measured-only, like a reference Take. Its recipe is a spec, not a request: no declared,
		// plan, style, facet or expected-duration input may reach the derivation.
		declared, plan, bars, spb = sql.NullInt64{}, sql.NullFloat64{}, sql.NullInt64{}, sql.NullFloat64{}
	}
	in := TakeInput{DurationS: dur, RecipeKnown: engine != "unknown" && engine != dspEngine}
	if declared.Valid {
		in.DeclaredBPM = ptr(float64(declared.Int64))
	}
	if plan.Valid {
		in.PlanBPM = ptr(plan.Float64)
	}
	if bars.Valid && spb.Valid && bars.Int64 > 0 && spb.Float64 > 0 {
		in.ExpectedDurationS = ptr(float64(bars.Int64) * spb.Float64)
	}
	if in.RecipeKnown {
		var planBPM, expected *float64
		in.StyleBPM, in.StyleKey, in.PlanKey, planBPM, expected = textInputs(req)
		if in.PlanBPM == nil {
			in.PlanBPM = planBPM
		}
		if in.ExpectedDurationS == nil {
			in.ExpectedDurationS = expected
		}
		applyEngineInputs(engine, reqJSON, &in)
		if in.Facets, err = facetsOf(ctx, q, "take_sounds", "track_id", id); err != nil {
			return tl, false, err
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT analyzer, version, metrics_json FROM analyses WHERE track_id = ? AND analyzer IN ('tempo','key','energy')`, id)
	if err != nil {
		return tl, false, fmt.Errorf("features: load analyses: %w", err)
	}
	by := map[string][]AnalysisRow{}
	for rows.Next() {
		var analyzer string
		var r AnalysisRow
		if err := rows.Scan(&analyzer, &r.Version, &r.MetricsJSON); err != nil {
			_ = rows.Close()
			return tl, false, err
		}
		by[analyzer] = append(by[analyzer], r)
	}
	if err := rows.Close(); err != nil {
		return tl, false, err
	}
	var tref, kref, eref *ref
	if r, found := SelectAnalysis(by["tempo"], tempoDef); found {
		var m measuredDoc
		if json.Unmarshal([]byte(r.MetricsJSON), &m) == nil && m.BPM != nil {
			in.MeasuredTempo = &TempoMeasure{BPM: *m.BPM, Confidence: m.Confidence, OctaveAmbiguous: m.OctaveAmbiguous}
		}
		tref = &ref{r.Version, sum([]byte(r.MetricsJSON))}
	}
	if r, found := SelectAnalysis(by["key"], keyDef); found {
		var m measuredDoc
		km := &KeyMeasure{}
		if json.Unmarshal([]byte(r.MetricsJSON), &m) == nil {
			km.R, km.Margin, km.AmbiguousRelative = m.R, m.Margin, m.AmbiguousRel
			if m.Tonic != nil && m.Mode != nil && *m.Tonic >= 0 && *m.Tonic < 12 && (*m.Mode == "major" || *m.Mode == "minor") {
				c := CamelotOf(*m.Tonic, *m.Mode)
				km.Key = &c
			}
		}
		in.MeasuredKey = km
		kref = &ref{r.Version, sum([]byte(r.MetricsJSON))}
	}
	if r, found := SelectAnalysis(by["energy"], energyDef); found {
		var m measuredDoc
		em := &EnergyMeasure{}
		if json.Unmarshal([]byte(r.MetricsJSON), &m) == nil && m.Energy != nil && *m.Energy >= 0 && *m.Energy <= 1 {
			em.Energy = m.Energy
		}
		in.MeasuredEnergy = em
		eref = &ref{r.Version, sum([]byte(r.MetricsJSON))}
	}
	tl.In = in
	tl.Digest = sumJSON(struct {
		Def      string    `json:"def"`
		Declared *float64  `json:"declared_bpm"`
		StyleBPM *int      `json:"style_bpm"`
		PlanBPM  *float64  `json:"plan_bpm"`
		Expected *float64  `json:"expected_s"`
		StyleKey *KeyGuess `json:"style_key"`
		PlanKey  *KeyGuess `json:"plan_key"`
		Tempo    *ref      `json:"tempo"`
		Key      *ref      `json:"key"`
		Energy   *ref      `json:"energy"`
		Facets   Facets    `json:"facets"`
		Duration float64   `json:"duration_s"`
		Known    bool      `json:"recipe_known"`
	}{Def, in.DeclaredBPM, in.StyleBPM, in.PlanBPM, in.ExpectedDurationS, in.StyleKey, in.PlanKey, tref, kref, eref, in.Facets, in.DurationS, in.RecipeKnown})
	return tl, true, nil
}

// draftSubject is the derivation input of a draft subject and the digest of its text inputs.
func loadDraft(ctx context.Context, q db, songID, draftID string) (in TakeInput, textDigest string, ok bool, err error) {
	var reqJSON string
	err = q.QueryRowContext(ctx, `SELECT request_json FROM recipe_drafts WHERE id = ?`, draftID).Scan(&reqJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return in, "", false, nil
	}
	if err != nil {
		return in, "", false, fmt.Errorf("features: load draft: %w", err)
	}
	req := readRequest(reqJSON)
	var planBPM, expected *float64
	in.RecipeKnown = true
	in.StyleBPM, in.StyleKey, in.PlanKey, planBPM, expected = textInputs(req)
	in.PlanBPM, in.ExpectedDurationS = planBPM, expected
	if in.Facets, err = facetsOf(ctx, q, "song_sounds", "song_id", songID); err != nil {
		return in, "", false, err
	}
	abc := ""
	if req.ABC != nil {
		abc = *req.ABC
	}
	textDigest = sumJSON(struct {
		Style string `json:"style_sha256"`
		ABC   string `json:"abc_sha256"`
		BPM   *int   `json:"declared_bpm"`
	}{sum([]byte(req.Style)), sum([]byte(abc)), in.StyleBPM})
	return in, textDigest, true, nil
}

// primarySounds reads the five primary Sound values of a Song: the value with the smallest ord of each facet (D-L3-9).
func primarySounds(ctx context.Context, q db, songID string) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT facet, value FROM song_sounds WHERE song_id = ? AND facet IN ('genre_family','vocal','era','mood','language')
		ORDER BY ord, value`, songID)
	if err != nil {
		return nil, fmt.Errorf("features: primary sounds: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var facet, value string
		if err := rows.Scan(&facet, &value); err != nil {
			return nil, err
		}
		if _, seen := out[facet]; !seen {
			out[facet] = value
		}
	}
	return out, rows.Err()
}

// subjectNote is "all_takes_discarded" for a Song that has takes but whose subject fell through to its draft.
func subjectNote(ctx context.Context, q db, sub sounds.Subject, songID string) (*string, error) {
	if sub.Kind != sounds.SubjectDraft {
		return nil, nil
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM take_songs WHERE song_id = ?)`, songID).Scan(&n); err != nil {
		return nil, fmt.Errorf("features: subject note: %w", err)
	}
	if n == 1 {
		return ptr("all_takes_discarded"), nil
	}
	return nil, nil
}
