package generation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// On-demand analyze (M5b C340, D-EARS-12): item mode analyze, one job of kind analyze per chosen take. The job attaches a listening
// check (Tier B) to an EXISTING take: it never renders, never writes the take's audio, tags or recipe, and creates no track and no
// mutation. It is an ordinary spend: it goes through the estimate, the approval and the create path like any render, and it is never
// planned by a loop (D-EARS-OP-5: Tier B on new takes only, an analyze only of takes the operator names).

// Analyze warning codes (C340.2). Stable API values.
const (
	WarnNoLyrics    = "no_lyrics"
	WarnLossyMaster = "lossy_master"
)

// Analyze limits (C340.1, C340.2).
const (
	maxAnalyzeTracks   = 50
	maxAnalyzeAudioS   = 600 // jobs.expected_audio_s CHECK
	analyzeTitlePrefix = "Listening check: "
	maxTitleRunes      = 200
	defaultFetchS      = 20
)

// Default Ears quote values of a Service whose seam was never wired (internal/app always wires it from the config).
const defaultEarsLoadS, defaultEarsRTFMilli, defaultEarsTranscribeRTFMilli = 40, 150, 40

const analyzeField = "track_ids"

// analyzeBatchShape checks what an analyze estimate may be combined with: nothing. It runs before any engine or backend is resolved
// (C340.1): another mode in the same estimate, options.tier_b, an engine trial, and an engine other than yue2 are all 422
// VALIDATION_FAILED rule analyze_alone.
func analyzeBatchShape(in EstimateIn) error {
	has := false
	for _, it := range in.Items {
		if it.Mode == lineage.ModeAnalyze {
			has = true
		}
	}
	if !has {
		return nil
	}
	alone := func(field string) error {
		return perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "an analyze estimate holds analyze items only, on the yue2 engine, without "+field,
			map[string]any{"field": field, "rule": "analyze_alone"})
	}
	for _, it := range in.Items {
		switch {
		case it.Mode != lineage.ModeAnalyze:
			return alone("items")
		case it.Engine != "" && it.Engine != DefaultEngine:
			return alone("items.engine")
		}
	}
	switch {
	case in.TierB != nil:
		return alone("options.tier_b")
	case in.TrialID != "":
		return alone("options.trial_id")
	case in.Engine != "" && in.Engine != DefaultEngine:
		return alone("engine")
	}
	return nil
}

// validateAnalyzeItem is the static shape of an analyze item: track_ids 1..50 and unique, and no other field (C340.1).
func validateAnalyzeItem(i int, it Item) error {
	hasParent := it.ParentTrackID != nil && *it.ParentTrackID != ""
	if hasParent || len(it.ParentTrackIDs) > 0 || len(it.DraftIDs) > 0 || len(it.Recipe) > 0 || it.Patch != nil || it.TemplateMode != "" ||
		it.NSeeds != 0 || len(it.Seeds) > 0 || it.Title != nil || it.PatchID != nil || it.Edit != nil || it.Reference != nil || it.EnginePatch != nil {
		return invalidItem(i, "mode", "analyze_fields")
	}
	switch n := len(it.TrackIDs); {
	case n == 0:
		return invalidItem(i, analyzeField, "track_ids_required")
	case n > maxAnalyzeTracks:
		return invalidItem(i, analyzeField, "track_ids_at_most_50")
	}
	return uniqueStrings(i, analyzeField, it.TrackIDs)
}

// analyzeFacts are the facts of an analysed take the planner reads in one query.
type analyzeFacts struct {
	Title, RecipeID, RequestID, RecipeHash, Lyrics, MasterSHA string
	Seed, MasterBytes                                         int64
	DurationS                                                 float64
	Lossy, HasScore, HasAnalysis                              bool
}

func loadAnalyzeFacts(ctx context.Context, q lineage.Querier, id string) (analyzeFacts, bool, error) {
	var f analyzeFacts
	err := q.QueryRowContext(ctx, `SELECT t.title, t.recipe_id, r.request_id, r.hash, r.lyrics, t.master_sha256, r.seed, t.master_bytes, t.duration_s, t.lossy,
		EXISTS(SELECT 1 FROM scores s WHERE s.track_id = t.id),
		EXISTS(SELECT 1 FROM artifacts a WHERE a.track_id = t.id AND a.name LIKE 'analysis%.json')
		FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, id).
		Scan(&f.Title, &f.RecipeID, &f.RequestID, &f.RecipeHash, &f.Lyrics, &f.MasterSHA, &f.Seed, &f.MasterBytes, &f.DurationS, &f.Lossy, &f.HasScore, &f.HasAnalysis)
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, nil
	}
	if err != nil {
		return f, false, fmt.Errorf("generation: analyze facts: %w", err)
	}
	return f, true, nil
}

// liveAnalyses lists, in the order given, the tracks that have an analyze job that is not terminal.
func liveAnalyses(ctx context.Context, q lineage.Querier, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT source_track_id FROM jobs WHERE kind = 'analyze' AND state NOT IN `+terminalJobStates+ //nolint:gosec // G202: constant state list
		` AND source_track_id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("generation: live analyses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	live := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []string
	for _, id := range ids {
		if live[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func analysisInProgress(ids []string) error {
	return perr(http.StatusConflict, "ANALYSIS_IN_PROGRESS", "these takes already have an analysis in progress", map[string]any{"track_ids": ids})
}

// earsTerms are the quote terms of a listening check; a Service whose seam was never wired uses the defaults.
func (s *Service) earsTerms() (loadS, rtfMilli, trMilli, fetchS int) {
	o := s.earsQ.o
	loadS, rtfMilli, trMilli, fetchS = o.LoadS, o.RTFMilli, o.TranscribeRTFMilli, o.AnalyzeFetchS
	if rtfMilli == 0 { // a hand-built config (all zero) has no valid value: the defaults apply, as in internal/app
		loadS, rtfMilli, trMilli = defaultEarsLoadS, defaultEarsRTFMilli, defaultEarsTranscribeRTFMilli
	}
	if fetchS == 0 {
		fetchS = defaultFetchS
	}
	return loadS, rtfMilli, trMilli, fetchS
}

// expandAnalyze plans one analyze take per chosen track (C340.3). The take's recipe is the analysed take's own (ReuseRecipeID), its
// ParentTrackID becomes jobs.source_track_id, and the spec pins the master and carries the Ears block.
func (p *planner) expandAnalyze(i int, it Item) error {
	loadS, rtf, trMilli, _ := p.s.earsTerms()
	var live []string
	for _, id := range it.TrackIDs {
		f, ok, err := loadAnalyzeFacts(p.ctx, p.q, id)
		if err != nil {
			return err
		}
		if !ok {
			return unknownTrack(id)
		}
		audioS := max(1, int(math.Ceil(f.DurationS)))
		switch {
		case audioS > maxAnalyzeAudioS:
			return invalidItem(i, analyzeField, "audio_too_long")
		case f.HasAnalysis:
			return invalidItem(i, analyzeField, "already_analyzed")
		}
		inProgress, err := liveAnalyses(p.ctx, p.q, []string{id})
		if err != nil {
			return err
		}
		live = append(live, inProgress...)

		stages := []string{"separate", "asr"}
		if f.HasScore {
			stages = append(stages, "transcribe") // D-EARS-4: the pitch contour only where a plan score can be compared
		}
		earsS := max(1, spend.EarsExpectedS(audioS, loadS, rtf, trMilli, f.HasScore))
		spec := jobspec.Spec{
			Inputs: &jobspec.Inputs{SrcAudio: &jobspec.Pin{TrackID: id, MasterSHA256: f.MasterSHA, Bytes: f.MasterBytes}},
			Ears:   &jobspec.Ears{Lang: p.s.earsQ.o.language(f.Lyrics), BudgetS: spend.EarsBudgetS(earsS), Stages: stages},
		}
		raw, err := spec.Canonical()
		if err != nil {
			return err
		}
		track, recipe := id, f.RecipeID
		t := &planTake{
			TakeIndex: len(p.takes), ItemIndex: i, Mode: lineage.ModeAnalyze, Operator: lineage.OpAnalyze, ParentTrackID: &track, ReuseRecipeID: &recipe,
			RequestJSON: "{}", RecipeHash: f.RecipeHash, RequestID: f.RequestID, Seed: f.Seed, Title: analyzeTitle(f.Title),
			Params:         paramsDoc{Mode: lineage.ModeAnalyze, SeedPolicy: string(lineage.SeedsParentOnly), NSeeds: 1},
			ExpectedAudioS: audioS, Warnings: []Issue{},
			Kind: jobspec.KindAnalyze, Needs: jobspec.Needs(jobspec.KindAnalyze, spec), Spec: raw,
		}
		if f.Lossy {
			t.Warnings = append(t.Warnings, warnIssue(WarnLossyMaster, analyzeField, "the master is a lossy MP3: the analysis hears it as it is"))
		}
		if p.s.earsQ.o.lyricLines(f.Lyrics) == 0 {
			t.Warnings = append(t.Warnings, warnIssue(WarnNoLyrics, analyzeField, "the take has no lyric line: no diction is scored, the transcript still serves Recover"))
		}
		if p.analyze == nil {
			p.analyze = map[int]int{}
		}
		p.analyze[t.TakeIndex] = earsS
		p.takes = append(p.takes, t)
	}
	if len(live) > 0 {
		return analysisInProgress(live)
	}
	return nil
}

func analyzeTitle(title string) string {
	if n := maxTitleRunes - utf8.RuneCountInString(analyzeTitlePrefix); utf8.RuneCountInString(title) > n {
		title = string([]rune(title)[:n])
	}
	return analyzeTitlePrefix + title
}

// quoteTakeIn is the quote input of planned take k: a render's audio, cot and listening check, or an analyze take's fetch seconds and
// listening check (C340.4: AudioS 0, LoadS = MM_ANALYZE_FETCH_S).
func (p *planner) quoteTakeIn(k int, t *planTake, ears earsPlan) spend.TakeIn {
	if n, ok := p.planJobs[k]; ok { // a plan job (D-DESK-4): n seeds of audio at the planning rate, no render phases
		return spend.TakeIn{AudioS: n * t.ExpectedAudioS, RTFMilli: spend.PlanMilli, LoadS: spend.LoadS}
	}
	if earsS, ok := p.analyze[k]; ok {
		_, _, _, fetchS := p.s.earsTerms()
		return spend.TakeIn{AudioS: 0, LoadS: fetchS, EarsS: earsS}
	}
	return spend.TakeIn{AudioS: t.ExpectedAudioS, Full: planPhase(t, cotIsFull(t.RequestJSON)), EarsS: ears.secs(k)}
}

// analyzeFits refuses an analyze take whose timeout would pass the runtime cap with the listening check on top (the same rule as
// tier_b_exceeds_runtime of a render).
func (p *planner) analyzeFits(qp spend.QuoteParams) error {
	for k, t := range p.takes { // slice order: the first offending take is always the one reported
		earsS, ok := p.analyze[k]
		if !ok {
			continue
		}
		_, _, _, fetchS := p.s.earsTerms()
		if !spend.TierBFits(qp, spend.TakeIn{AudioS: 0, LoadS: fetchS, EarsS: earsS}) {
			return invalidItem(t.ItemIndex, analyzeField, "tier_b_exceeds_runtime")
		}
	}
	return nil
}

// recheckAnalyze re-reads, through tx, what can change between an analyze estimate and its create: another job on the take
// (ANALYSIS_IN_PROGRESS, C340.2) or a document that arrived since (the estimate is stale).
func (s *Service) recheckAnalyze(ctx context.Context, tx lineage.Querier, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	live, err := liveAnalyses(ctx, tx, ids)
	if err != nil {
		return err
	}
	if len(live) > 0 {
		return analysisInProgress(live)
	}
	var done []string
	for _, id := range ids {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifacts WHERE track_id = ? AND name LIKE 'analysis%.json')`, id).Scan(&n); err != nil {
			return fmt.Errorf("generation: analysis artifact: %w", err)
		}
		if n == 1 {
			done = append(done, id)
		}
	}
	if len(done) > 0 {
		return staleErr(map[string]any{"reason": "library_changed", "track_ids": done})
	}
	return nil
}

// recoveredDraftRule refuses a mother override on an item that renders a recovered draft (C343.4, rule recovered_draft_parent): the
// take is a new root whose ledger parent is the reference, and a reference can never be a mother.
func recoveredDraftRule(item int, it Item, d draftInfo) error {
	hasMother := func(m *string) bool { return m != nil && *m != "" }
	// the draft's own stored parent counts too: update_draft could have set it, and ingest refuses a recovered take with a parent
	if d.RecoveredFrom != "" && (hasMother(it.ParentTrackID) || hasMother(d.ParentTrackID)) {
		return invalidItem(item, "parent_track_id", "recovered_draft_parent")
	}
	return nil
}
