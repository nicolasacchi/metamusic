package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// SourceRef is one occurrence of a recipe in the inbox.
type SourceRef struct {
	Path    string // inbox-relative path, or "name:<hex>"
	Line    int    // 1-based JSONL line / array element; 0 = whole JSON file
	Raw     []byte // exact bytes
	RawKind string // jsonl_line | json_array_item | json_file
}

// NewDraft is one backlog entry. With SourceRecipeID set, RequestID and
// RequestJSON default to the recipe's.
type NewDraft struct {
	Origin          string // import_backlog | varianti_import
	SourceRecipeID  string
	ParentTrackID   string
	GroupKey, Label string
	RequestID       string
	RequestJSON     string
}

// NewRendition is a derived file row.
type NewRendition struct {
	Kind   string // mp3_320 | peaks
	SHA256 string
	Bytes  int64
	Path   string
	Params map[string]any
}

// NewArtifact is a kept result-dir file.
type NewArtifact struct {
	Name, SHA256, Path string
	Bytes              int64
}

// NewMutation links a take to its parent (inferred siblings in M1).
type NewMutation struct {
	Operator  string // inferred_sibling
	Inferred  bool
	Rule      string // same_request_id | seed_family | mapping
	Rationale string
}

// Fulfilment reports which drafts an InsertTake fulfilled.
type Fulfilment struct {
	OwnDraftID string
	CoreDrafts []string // drafts fulfilled through the core_hash fallback
	// EditedSkipped lists the drafts whose recipe matched (own recipe or
	// core_hash) but which a human edited (revision > 1): they stay in the
	// backlog untouched (M2a C27).
	EditedSkipped []string
}

// NewTake is everything one take writes, in one transaction.
type NewTake struct {
	ID, Title, Slug, RecipeID string
	ParentTrackID             string // "" for roots
	RootTrackID               string // "" => the take itself
	Generation                int

	DurationS                            float64
	SampleRate, BitsPerSample, Channels  int
	TotalSamples                         int64
	MasterPath, MasterSHA256             string
	MasterBytes                          int64
	SourceSHA256, FramesSHA256, AudioMD5 string
	TruncatedABC, TruncatedSemantic      *bool
	ImportRule, ImportRunID, PrincipalID string
	RecordedAt                           time.Time

	Renditions []NewRendition
	Loudness   analysis.Loudness
	Spectrum   analysis.Spectrum
	// ToolVersion is analysis.Analyzer.ToolVersion(); the analyses rows carry
	// <definition> + " " + ToolVersion.
	ToolVersion  string
	ReplayGainDB float64
	Artifacts    []NewArtifact
	// ScoreABC, when non-nil, is the take's score.abc (already stored at
	// takes/<ID>/score.abc); the library parses and aligns it.
	ScoreABC []byte
	Mutation *NewMutation

	// Out, when non-nil, receives the fulfilled drafts.
	Out *Fulfilment
}

func execTx(ctx context.Context, tx *sql.Tx, q string, a ...any) error {
	_, err := tx.ExecContext(ctx, q, a...)
	return err
}

// InsertRecipe stores a recipe once (by canonical hash).
func (s *Service) InsertRecipe(ctx context.Context, tx *sql.Tx, p yue2.Parsed) (id string, created bool, err error) {
	r := p.Request
	rid := s.newID()
	err = tx.QueryRowContext(ctx, `INSERT INTO recipes(id, engine, request_id, request_json, style, lyrics, seed, cot,
		sampling_json, hash, core_hash, created_by, created_at) VALUES (?,?,?,?,?,?,?,?,NULL,?,?,?,?)
		ON CONFLICT(hash) DO NOTHING RETURNING id`,
		rid, "yue2", r.ID, string(p.Canonical), r.Style, r.Lyrics, r.Seed, r.Cot, p.Hash, p.CoreHash, "import", s.nowStr()).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("library: insert recipe: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM recipes WHERE hash = ?`, p.Hash).Scan(&id); err != nil {
		return "", false, fmt.Errorf("library: find recipe: %w", err)
	}
	return id, false, nil
}

// InsertRecipeSource records one occurrence; repeated calls are no-ops.
func (s *Service) InsertRecipeSource(ctx context.Context, tx *sql.Tx, recipeID string, src SourceRef, runID string) (bool, error) {
	if !oneOf(src.RawKind, enumRawKind...) {
		return false, invalid("raw_kind", "unknown kind %q", src.RawKind)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO recipe_sources(recipe_id, source_path, line_no, raw, raw_sha256, raw_kind, import_run_id, created_at)
		VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(recipe_id, source_path, line_no, raw_sha256) DO NOTHING`,
		recipeID, src.Path, src.Line, src.Raw, sha256Hex(src.Raw), src.RawKind, runID, s.nowStr())
	if err != nil {
		return false, fmt.Errorf("library: insert recipe source: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// InsertRefs stores private reference metadata once per (recipe, source file).
func (s *Service) InsertRefs(ctx context.Context, tx *sql.Tx, recipeID, source string, ref map[string]string) error {
	b, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	if err := execTx(ctx, tx, `INSERT INTO recipe_refs(recipe_id, source, ref_json, created_at) VALUES (?,?,?,?)
		ON CONFLICT(recipe_id, source) DO NOTHING`, recipeID, source, string(b), s.nowStr()); err != nil {
		return fmt.Errorf("library: insert ref: %w", err)
	}
	return nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// InsertDraft stores a backlog draft, once per source recipe.
func (s *Service) InsertDraft(ctx context.Context, tx *sql.Tx, d NewDraft) (id string, created bool, err error) {
	if !oneOf(d.Origin, enumOrigin...) {
		return "", false, invalid("origin", "unknown origin %q", d.Origin)
	}
	reqID, reqJSON := d.RequestID, d.RequestJSON
	if d.SourceRecipeID != "" && (reqID == "" || reqJSON == "") {
		if err := tx.QueryRowContext(ctx, `SELECT request_id, request_json FROM recipes WHERE id = ?`, d.SourceRecipeID).Scan(&reqID, &reqJSON); err != nil {
			return "", false, fmt.Errorf("library: draft recipe: %w", err)
		}
	}
	if reqID == "" || reqJSON == "" {
		return "", false, invalid("request", "request_id and request_json are required")
	}
	now := s.nowStr()
	did := s.newID()
	const cols = `INSERT INTO recipe_drafts(id, origin, source_recipe_id, parent_track_id, group_key, label, request_id, request_json, status, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,'backlog',?,?)`
	args := []any{did, d.Origin, nullStr(d.SourceRecipeID), nullStr(d.ParentTrackID), nullStr(d.GroupKey), nullStr(d.Label), reqID, reqJSON, now, now}
	if d.SourceRecipeID == "" {
		if err := execTx(ctx, tx, cols, args...); err != nil {
			return "", false, fmt.Errorf("library: insert draft: %w", err)
		}
		return did, true, nil
	}
	err = tx.QueryRowContext(ctx, cols+` ON CONFLICT(source_recipe_id) WHERE source_recipe_id IS NOT NULL DO NOTHING RETURNING id`, args...).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("library: insert draft: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM recipe_drafts WHERE source_recipe_id = ?`, d.SourceRecipeID).Scan(&id); err != nil {
		return "", false, fmt.Errorf("library: find draft: %w", err)
	}
	return id, false, nil
}

// AddToCollection adds a draft to a named collection (idempotent).
func (s *Service) AddToCollection(ctx context.Context, tx *sql.Tx, draftID, name, source string) error {
	if err := execTx(ctx, tx, `INSERT INTO draft_collections(draft_id, name, source) VALUES (?,?,?)
		ON CONFLICT(draft_id, name) DO NOTHING`, draftID, name, source); err != nil {
		return fmt.Errorf("library: add to collection: %w", err)
	}
	return nil
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func boolPtr(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return 1
	}
	return 0
}

// InsertTake writes a track and everything that hangs off it in the caller's
// transaction, then fulfils the backlog drafts of its recipe.
func (s *Service) InsertTake(ctx context.Context, tx *sql.Tx, t NewTake) error {
	if t.ID == "" || t.Title == "" || t.RecipeID == "" {
		return invalid("take", "id, title and recipe_id are required")
	}
	if !oneOf(t.ImportRule, enumRule...) {
		return invalid("import_rule", "unknown rule %q", t.ImportRule)
	}
	for _, r := range t.Renditions {
		if !oneOf(r.Kind, enumRendKind...) {
			return invalid("rendition", "unknown kind %q", r.Kind)
		}
	}
	var reqID, style, lyrics, coreHash string
	if err := tx.QueryRowContext(ctx, `SELECT request_id, style, lyrics, core_hash FROM recipes WHERE id = ?`, t.RecipeID).
		Scan(&reqID, &style, &lyrics, &coreHash); err != nil {
		return fmt.Errorf("library: take recipe: %w", err)
	}
	terms, bpm := yue2.StyleTerms(style)
	root := t.RootTrackID
	if root == "" {
		root = t.ID
	}
	now := s.nowStr()
	var bpmArg any
	if bpm > 0 {
		bpmArg = bpm
	}
	runArg := nullStr(t.ImportRunID)
	if err := execTx(ctx, tx, `INSERT INTO tracks(id, slug, title, recipe_id, parent_track_id, root_track_id, generation, status, status_set_by, origin,
		duration_s, sample_rate, bits_per_sample, channels, total_samples, master_path, master_sha256, master_bytes, source_sha256, frames_sha256, audio_md5,
		truncated_abc, truncated_semantic, bpm_declared, import_rule, import_run_id, recorded_at, created_at)
		VALUES (?,?,?,?,?,?,?,'draft','import','import',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Slug, t.Title, t.RecipeID, nullStr(t.ParentTrackID), root, t.Generation,
		t.DurationS, t.SampleRate, t.BitsPerSample, t.Channels, t.TotalSamples, t.MasterPath, t.MasterSHA256, t.MasterBytes, t.SourceSHA256, t.FramesSHA256, t.AudioMD5,
		boolPtr(t.TruncatedABC), boolPtr(t.TruncatedSemantic), bpmArg, t.ImportRule, runArg, ts(t.RecordedAt), now); err != nil {
		return fmt.Errorf("library: insert track: %w", err)
	}
	for _, term := range terms {
		if err := execTx(ctx, tx, `INSERT INTO track_style_terms(track_id, term) VALUES (?,?)`, t.ID, term); err != nil {
			return fmt.Errorf("library: insert style term: %w", err)
		}
	}
	for _, r := range t.Renditions {
		pj, err := jsonString(r.Params)
		if err != nil {
			return err
		}
		if err := execTx(ctx, tx, `INSERT INTO renditions(track_id, kind, sha256, bytes, path, params_json, created_at) VALUES (?,?,?,?,?,?,?)`,
			t.ID, r.Kind, r.SHA256, r.Bytes, r.Path, pj, now); err != nil {
			return fmt.Errorf("library: insert rendition: %w", err)
		}
	}
	if err := s.insertAnalyses(ctx, tx, t, now); err != nil {
		return err
	}
	for _, a := range t.Artifacts {
		if err := execTx(ctx, tx, `INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?,?,?,?,?,?)`,
			t.ID, a.Name, a.SHA256, a.Bytes, a.Path, now); err != nil {
			return fmt.Errorf("library: insert artifact: %w", err)
		}
	}
	if err := s.insertScoreAndSections(ctx, tx, t, lyrics, now); err != nil {
		return err
	}
	if t.Mutation != nil {
		m := t.Mutation
		if t.ParentTrackID == "" || !oneOf(m.Operator, "inferred_sibling") {
			return invalid("mutation", "operator %q needs a parent", m.Operator)
		}
		pj, err := jsonString(map[string]string{"rule": m.Rule})
		if err != nil {
			return err
		}
		inf := 0
		if m.Inferred {
			inf = 1
		}
		if err := execTx(ctx, tx, `INSERT INTO mutations(id, parent_track_id, child_track_id, operator, inferred, params_json, rationale, principal_id, created_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, s.newID(), t.ParentTrackID, t.ID, m.Operator, inf, pj, nullStr(m.Rationale), t.PrincipalID, now); err != nil {
			return fmt.Errorf("library: insert mutation: %w", err)
		}
	}
	if t.ParentTrackID != "" && !s.legacyV2 {
		// Tag inheritance (C27): a child starts with its parent's human and inherited tags.
		if err := execTx(ctx, tx, `INSERT INTO track_tags(track_id, tag_id, source, inherited_from, principal_id, created_at, updated_at)
			SELECT ?, tag_id, 'inherited', ?, 'system:inherit', ?, ? FROM track_tags
			WHERE track_id = ? AND source IN ('human','inherited') AND removed_at IS NULL GROUP BY tag_id ON CONFLICT DO NOTHING`,
			t.ID, t.ParentTrackID, now, now, t.ParentTrackID); err != nil {
			return fmt.Errorf("library: inherit tags: %w", err)
		}
	}
	return s.fulfil(ctx, tx, t, coreHash, now)
}

func (s *Service) insertAnalyses(ctx context.Context, tx *sql.Tx, t NewTake, now string) error {
	type lm struct {
		analysis.Loudness
		ReplayGain float64 `json:"replaygain_track_gain_db"`
	}
	lj, err := jsonString(lm{t.Loudness, t.ReplayGainDB})
	if err != nil {
		return err
	}
	sj, err := jsonString(t.Spectrum)
	if err != nil {
		return err
	}
	for _, a := range []struct{ name, def, metrics string }{
		{"loudness", analysis.LoudnessDef, lj}, {"spectrum", analysis.SpectrumDef, sj},
	} {
		if err := execTx(ctx, tx, `INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?,?,?,?,?,?)`,
			t.ID, a.name, a.def+" "+t.ToolVersion, "0", a.metrics, now); err != nil {
			return fmt.Errorf("library: insert analysis: %w", err)
		}
	}
	return nil
}

func (s *Service) insertScoreAndSections(ctx context.Context, tx *sql.Tx, t NewTake, lyrics, now string) error {
	lyr := yue2.LyricsSections(lyrics)
	lines := func(sec yue2.Section) any {
		l := sec.Lines
		if l == nil {
			l = []string{}
		}
		b, _ := json.Marshal(l)
		return string(b)
	}
	for _, sec := range lyr {
		if err := execTx(ctx, tx, `INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence, lines_json)
			VALUES (?,'lyrics',?,?,?,?,NULL,NULL,NULL,?)`, t.ID, sec.Idx, sec.Kind, sec.Label, sec.Raw, lines(sec)); err != nil {
			return fmt.Errorf("library: insert section: %w", err)
		}
	}
	if t.ScoreABC == nil {
		return nil
	}
	sc, perr := yue2.ParseScore(t.ScoreABC)
	if perr != nil {
		// An unparsable score is simply not attached; the importer reports it.
		return nil //nolint:nilerr // intentional: the take is valid without a score
	}
	times, aligned := yue2.AlignSections(lyr, sc)
	var q, bpm, spb any
	if sc.Q != "" {
		q = sc.Q
	}
	if sc.BPM > 0 {
		bpm = sc.BPM
	}
	if sc.SecondsPerBar > 0 {
		spb = sc.SecondsPerBar
	}
	al := 0
	if aligned {
		al = 1
	}
	if err := execTx(ctx, tx, `INSERT INTO scores(track_id, abc_path, abc_sha256, bars, meter, q, bpm, seconds_per_bar, aligned, source, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,'import',?)`, t.ID, "takes/"+t.ID+"/score.abc", sha256Hex(t.ScoreABC), sc.Bars, sc.Meter, q, bpm, spb, al, now); err != nil {
		return fmt.Errorf("library: insert score: %w", err)
	}
	if !aligned {
		return nil
	}
	for i, sec := range lyr {
		if err := execTx(ctx, tx, `INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence, lines_json)
			VALUES (?,'plan',?,?,?,?,?,?,0.7,?)`, t.ID, sec.Idx, sec.Kind, sec.Label, sec.Raw, times[i][0], times[i][1], lines(sec)); err != nil {
			return fmt.Errorf("library: insert plan section: %w", err)
		}
	}
	return nil
}

func (s *Service) fulfil(ctx context.Context, tx *sql.Tx, t NewTake, coreHash, now string) error {
	revOne, revCol := ` AND revision = 1`, `d.revision`
	if s.legacyV2 {
		revOne, revCol = ``, `1`
	}
	// Edited drafts (revision > 1) are never touched by an import (C27): they are
	// reported in EditedSkipped and stay in the backlog.
	var own string
	err := tx.QueryRowContext(ctx, `SELECT id FROM recipe_drafts WHERE source_recipe_id=? AND status='backlog'`+revOne, t.RecipeID).Scan(&own)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("library: find own draft: %w", err)
	}
	if own != "" { // C11 form, verbatim, plus the revision guard
		if _, err := tx.ExecContext(ctx, `UPDATE recipe_drafts SET status='fulfilled', fulfilled_by_track_id=?, updated_at=? WHERE id=? AND status='backlog'`+revOne, t.ID, now, own); err != nil {
			return fmt.Errorf("library: fulfil draft: %w", err)
		}
	}
	var skipped []string
	if own == "" && !s.legacyV2 {
		var edited string
		err := tx.QueryRowContext(ctx, `SELECT id FROM recipe_drafts WHERE source_recipe_id=? AND status='backlog' AND revision > 1`, t.RecipeID).Scan(&edited)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("library: find edited draft: %w", err)
		}
		if edited != "" {
			skipped = append(skipped, edited)
		}
	}
	if t.Out != nil {
		t.Out.OwnDraftID = own
	}
	rows, err := tx.QueryContext(ctx, `SELECT d.id, `+revCol+` FROM recipe_drafts d JOIN recipes r ON r.id = d.source_recipe_id
		WHERE d.status='backlog' AND r.core_hash = ? AND r.id <> ? ORDER BY d.id`, coreHash, t.RecipeID)
	if err != nil {
		return fmt.Errorf("library: core match: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		var rev int
		if err := rows.Scan(&id, &rev); err != nil {
			_ = rows.Close()
			return err
		}
		if rev > 1 {
			skipped = append(skipped, id)
			continue
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := execTx(ctx, tx, `UPDATE recipe_drafts SET status='fulfilled', fulfilled_by_track_id=?, updated_at=?
			WHERE id=? AND status='backlog'`+revOne, t.ID, now, id); err != nil {
			return fmt.Errorf("library: fulfil core draft: %w", err)
		}
	}
	if t.Out != nil {
		t.Out.CoreDrafts = ids
		t.Out.EditedSkipped = skipped
	}
	return nil
}

// staleAfter is how long a running import may go without a heartbeat.
const staleAfter = 2 * time.Minute

// BeginRun opens an import run, refusing while a live one exists and failing
// stale ones.
func (s *Service) BeginRun(ctx context.Context, principal, inboxLabel, planDigest string) (string, error) {
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	rows, err := tx.QueryContext(ctx, `SELECT id, heartbeat_at FROM import_runs WHERE state='running'`)
	if err != nil {
		return "", err
	}
	var stale []string
	live := false
	for rows.Next() {
		var id, hb string
		if err := rows.Scan(&id, &hb); err != nil {
			_ = rows.Close()
			return "", err
		}
		t, perr := time.Parse(time.RFC3339, hb)
		if perr == nil && t.After(now.Add(-staleAfter)) {
			live = true
		} else {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if live {
		return "", ErrImportInProgress
	}
	for _, id := range stale {
		if err := execTx(ctx, tx, `UPDATE import_runs SET state='failed', finished_at=?, error='stale: no heartbeat' WHERE id=? AND state='running'`, ts(now), id); err != nil {
			return "", err
		}
	}
	id := s.newID()
	if err := execTx(ctx, tx, `INSERT INTO import_runs(id, started_at, heartbeat_at, state, principal, inbox_label, plan_digest) VALUES (?,?,?,'running',?,?,?)`,
		id, ts(now), ts(now), principal, inboxLabel, planDigest); err != nil {
		return "", fmt.Errorf("library: begin run: %w", err)
	}
	return id, tx.Commit()
}

// Heartbeat refreshes a running run's heartbeat.
func (s *Service) Heartbeat(ctx context.Context, runID string) error {
	_, err := s.st.W.ExecContext(ctx, `UPDATE import_runs SET heartbeat_at=? WHERE id=? AND state='running'`, s.nowStr(), runID)
	return err
}

// FinishRun closes a running run; finishing a finished run is a no-op.
func (s *Service) FinishRun(ctx context.Context, runID, state string, summary, report []byte, runErr error) error {
	if !oneOf(state, "completed", "failed") {
		return invalid("state", "must be completed or failed")
	}
	var sum, rep, es any
	if summary != nil {
		sum = string(summary)
	}
	if report != nil {
		rep = string(report)
	}
	if runErr != nil {
		es = runErr.Error()
	}
	_, err := s.st.W.ExecContext(ctx, `UPDATE import_runs SET state=?, finished_at=?, summary_json=?, report_json=?, error=? WHERE id=? AND state='running'`,
		state, s.nowStr(), sum, rep, es, runID)
	if err != nil {
		return fmt.Errorf("library: finish run: %w", err)
	}
	return nil
}

// RelinkVariantiMothers fills NULL mother links of varianti drafts once the
// mother (a unique lineage root with the same group key) exists. Edited drafts
// (revision > 1) are never touched (C27).
func (s *Service) RelinkVariantiMothers(ctx context.Context, tx *sql.Tx) (int, error) {
	roots := map[string]map[string]bool{} // group key -> root track ids
	rows, err := tx.QueryContext(ctx, `SELECT t.root_track_id, r.request_id FROM tracks t JOIN recipes r ON r.id = t.recipe_id`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var root, rid string
		if err := rows.Scan(&root, &rid); err != nil {
			_ = rows.Close()
			return 0, err
		}
		g := yue2.GroupKey(rid)
		if roots[g] == nil {
			roots[g] = map[string]bool{}
		}
		roots[g][root] = true
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id, group_key FROM recipe_drafts WHERE origin='varianti_import' AND parent_track_id IS NULL AND group_key IS NOT NULL AND revision = 1`)
	if err != nil {
		return 0, err
	}
	type link struct{ draft, mother string }
	var links []link
	for rows.Next() {
		var id, g string
		if err := rows.Scan(&id, &g); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if set := roots[g]; len(set) == 1 {
			for m := range set {
				links = append(links, link{id, m})
			}
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	n := 0
	now := s.nowStr()
	for _, l := range links {
		res, err := tx.ExecContext(ctx, `UPDATE recipe_drafts SET parent_track_id=?, updated_at=? WHERE id=? AND parent_track_id IS NULL AND revision = 1`, l.mother, now, l.draft)
		if err != nil {
			return n, fmt.Errorf("library: relink mother: %w", err)
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	return n, nil
}
