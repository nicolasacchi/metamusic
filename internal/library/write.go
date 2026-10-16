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
	"github.com/nicolasacchi/metamusic/internal/lineage"
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

// NewMutation links a take to its parent: inferred siblings at import (M1) and
// the Operator Ledger edges of generated takes (M3, C71.5).
type NewMutation struct {
	Operator  string // inferred_sibling | reroll_seed | same_seed_edit | lyric_edit | style_edit | from_draft | compose_new
	Inferred  bool
	Rule      string // same_request_id | seed_family | mapping | reference_group (inferred_sibling only)
	Rationale string
	// M3 (D-M3-35): written only for job takes. Params becomes params_json (plus "job_id"),
	// RecipePatchJSON the recipe_patch_json column ("" => NULL).
	Params          map[string]any
	JobID           string
	RecipePatchJSON string
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

	// Reference marks a recipe-less take (its recipe is a stub, ImportRule is
	// RuleReference): no score, artifacts, fulfilment or style terms (M2c, C59).
	Reference bool
	// Lossy marks a take whose master is an MP3 (tracks.lossy = 1). It requires
	// Reference and the nominal lossy shape (16 bits, zero audio_md5, equal
	// source/frames/master hashes) and allows only a peaks rendition.
	Lossy bool

	// M3 (D-M3-35). Origin is "" or "import" for imported takes and "job" for generated ones; the
	// columns below are written only for job takes so older-schema fixtures keep building.
	Origin       string
	StatusSetBy  string // default "import" (job takes: "system:job")
	JobID, EnvID string

	// Out, when non-nil, receives the fulfilled drafts.
	Out *Fulfilment
}

func execTx(ctx context.Context, tx *sql.Tx, q string, a ...any) error {
	_, err := tx.ExecContext(ctx, q, a...)
	return err
}

// ErrRecipeExists is returned by InsertRecipe for a non-import caller whose recipe is already stored:
// a job recipe is always a new row (reuse goes through the draft path, never through an insert).
var ErrRecipeExists = errors.New("library: recipe already exists")

// hasTable reports whether the connected database has the named table. Positive answers are cached;
// a negative one is re-asked, because an upgrade test migrates a database after the Service exists.
func (s *Service) hasTable(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	return s.hasTableQ(ctx, tx, name)
}

// hasTableDB is hasTable on the reader pool (read paths).
func (s *Service) hasTableDB(ctx context.Context, name string) (bool, error) {
	return s.hasTableQ(ctx, s.st.R, name)
}

func (s *Service) hasTableQ(ctx context.Context, tx rowQuerier, name string) (bool, error) {
	if _, ok := s.tables.Load(name); ok {
		return true, nil
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("library: probe table %s: %w", name, err)
	}
	if n == 0 {
		return false, nil
	}
	s.tables.Store(name, struct{}{})
	return true, nil
}

// hasColumn is hasTable for a column (schema-gated writes of columns later migrations add, D-M4a-23).
func (s *Service) hasColumn(ctx context.Context, tx *sql.Tx, table, col string) (bool, error) {
	key := table + "." + col
	if _, ok := s.tables.Load(key); ok {
		return true, nil
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
		return false, fmt.Errorf("library: probe column %s: %w", key, err)
	}
	if n == 0 {
		return false, nil
	}
	s.tables.Store(key, struct{}{})
	return true, nil
}

// InsertRecipe stores a recipe once (by canonical hash) on behalf of createdBy ("import" for the
// importer, the principal key for a job recipe) and, when the schema has recipe_keys (version 6),
// the recipe's content and render keys (C71.7). Any createdBy other than "import" gets
// ErrRecipeExists (with the existing id) unless the row was created now.
func (s *Service) InsertRecipe(ctx context.Context, tx *sql.Tx, p yue2.Parsed, createdBy string) (id string, created bool, err error) {
	if createdBy == "" {
		return "", false, invalid("created_by", "created_by is required")
	}
	r := p.Request
	rid := s.newID()
	err = tx.QueryRowContext(ctx, `INSERT INTO recipes(id, engine, request_id, request_json, style, lyrics, seed, cot,
		sampling_json, hash, core_hash, created_by, created_at) VALUES (?,?,?,?,?,?,?,?,NULL,?,?,?,?)
		ON CONFLICT(hash) DO NOTHING RETURNING id`,
		rid, "yue2", r.ID, string(p.Canonical), r.Style, r.Lyrics, r.Seed, r.Cot, p.Hash, p.CoreHash, createdBy, s.nowStr()).Scan(&id)
	if err == nil {
		if err := s.insertRecipeKeys(ctx, tx, id, p.Canonical); err != nil {
			return "", false, err
		}
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("library: insert recipe: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM recipes WHERE hash = ?`, p.Hash).Scan(&id); err != nil {
		return "", false, fmt.Errorf("library: find recipe: %w", err)
	}
	if createdBy != "import" {
		return id, false, ErrRecipeExists
	}
	return id, false, nil
}

// insertRecipeKeys writes the recipe_keys row of a yue2 recipe in the recipe's transaction.
func (s *Service) insertRecipeKeys(ctx context.Context, tx *sql.Tx, recipeID string, canonical []byte) error {
	ok, err := s.hasTable(ctx, tx, "recipe_keys")
	if err != nil || !ok {
		return err
	}
	content, render := lineage.Keys(canonical)
	if content == "" || render == "" {
		return fmt.Errorf("library: recipe keys of %s: request is not an object", recipeID)
	}
	if err := execTx(ctx, tx, `INSERT INTO recipe_keys(recipe_id, content_hash, render_hash) VALUES (?,?,?) ON CONFLICT(recipe_id) DO NOTHING`,
		recipeID, content, render); err != nil {
		return fmt.Errorf("library: insert recipe keys: %w", err)
	}
	return nil
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
	if err := validateReferenceTake(t); err != nil {
		return err
	}
	if err := validateJobTake(t); err != nil {
		return err
	}
	var reqID, style, lyrics, coreHash string
	if err := tx.QueryRowContext(ctx, `SELECT request_id, style, lyrics, core_hash FROM recipes WHERE id = ?`, t.RecipeID).
		Scan(&reqID, &style, &lyrics, &coreHash); err != nil {
		return fmt.Errorf("library: take recipe: %w", err)
	}
	terms, bpm := yue2.StyleTerms(style)
	if t.Reference {
		terms, bpm = nil, 0
	}
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
	// The lossy column exists from schema version 5; it is written only for lossy
	// takes so FixtureV2 keeps working against older databases.
	// status_set_at exists from the M4a migration; older databases (upgrade tests) have no column.
	statusCol, statusArg := "", ""
	if ok, err := s.hasColumn(ctx, tx, "tracks", "status_set_at"); err != nil {
		return err
	} else if ok {
		statusCol, statusArg = ", status_set_at", ",?"
	}
	lossyCol, lossyArg := "", ""
	if t.Lossy {
		lossyCol, lossyArg = ", lossy", ",1"
	}
	// The M3 columns (v6) are written only for job takes, for the same reason (D-M3-35).
	origin, setBy := "import", "import"
	args := []any{t.ID, t.Slug, t.Title, t.RecipeID, nullStr(t.ParentTrackID), root, t.Generation}
	jobCols, jobArgs := "", ""
	if t.isJob() {
		origin, setBy = "job", t.StatusSetBy
		if setBy == "" {
			setBy = "system:job"
		}
		jobCols, jobArgs = ", job_id, env_id", ",?,?"
	}
	args = append(args, setBy, origin,
		t.DurationS, t.SampleRate, t.BitsPerSample, t.Channels, t.TotalSamples, t.MasterPath, t.MasterSHA256, t.MasterBytes, t.SourceSHA256, t.FramesSHA256, t.AudioMD5,
		boolPtr(t.TruncatedABC), boolPtr(t.TruncatedSemantic), bpmArg, t.ImportRule, runArg, ts(t.RecordedAt), now)
	if statusCol != "" {
		args = append(args, now)
	}
	if t.isJob() {
		args = append(args, nullStr(t.JobID), nullStr(t.EnvID))
	}
	if err := execTx(ctx, tx, `INSERT INTO tracks(id, slug, title, recipe_id, parent_track_id, root_track_id, generation, status, status_set_by, origin,
		duration_s, sample_rate, bits_per_sample, channels, total_samples, master_path, master_sha256, master_bytes, source_sha256, frames_sha256, audio_md5,
		truncated_abc, truncated_semantic, bpm_declared, import_rule, import_run_id, recorded_at, created_at`+statusCol+lossyCol+jobCols+`)
		VALUES (?,?,?,?,?,?,?,'draft',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?`+statusArg+lossyArg+jobArgs+`)`, args...); err != nil {
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
		if err := s.insertMutation(ctx, tx, t, now); err != nil {
			return err
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
	if t.Reference {
		return nil // a stub recipe never fulfils a draft (C59)
	}
	if t.isJob() {
		return nil // a generated take never fulfils an imported draft; the job path marks its own draft (D-M3-35)
	}
	return s.fulfil(ctx, tx, t, coreHash, now)
}

func (t NewTake) isJob() bool { return t.Origin == "job" }

func scoreSource(t NewTake) string {
	if t.isJob() {
		return "job"
	}
	return "import"
}

// enumOperator lists the Operator Ledger operators (C71.5); only inferred_sibling is written by import.
var enumOperator = []string{"inferred_sibling", "reroll_seed", "same_seed_edit", "lyric_edit", "style_edit", "from_draft", "compose_new"}

// rootlessOperators may record an edge without a parent (a draft with no mother, a composed song).
var rootlessOperators = []string{"from_draft", "compose_new"}

// validateJobTake enforces the Go side of the job-take shape.
func validateJobTake(t NewTake) error {
	switch t.Origin {
	case "", "import":
		if t.JobID != "" || t.EnvID != "" {
			return invalid("job_id", "only job takes carry a job or environment")
		}
		if t.StatusSetBy != "" && t.StatusSetBy != "import" {
			return invalid("status_set_by", "only job takes set status_set_by")
		}
		if t.Mutation != nil && t.Mutation.Operator != "inferred_sibling" {
			return invalid("mutation", "operator %q needs a job take", t.Mutation.Operator)
		}
	case "job":
		if t.JobID == "" {
			return invalid("job_id", "a job take needs its job")
		}
		if t.Reference || t.Lossy {
			return invalid("origin", "a job take is never a reference take")
		}
	default:
		return invalid("origin", "unknown origin %q", t.Origin)
	}
	if m := t.Mutation; m != nil {
		if !oneOf(m.Operator, enumOperator...) {
			return invalid("mutation", "unknown operator %q", m.Operator)
		}
		if m.Operator == "inferred_sibling" && (t.ParentTrackID == "" || t.isJob()) {
			return invalid("mutation", "operator %q needs an imported take with a parent", m.Operator)
		}
		if m.Operator != "inferred_sibling" && t.ParentTrackID == "" && !oneOf(m.Operator, rootlessOperators...) {
			return invalid("mutation", "operator %q needs a parent", m.Operator)
		}
	}
	return nil
}

// insertMutation writes the Operator Ledger row of a take (inferred at import, planned for a job).
func (s *Service) insertMutation(ctx context.Context, tx *sql.Tx, t NewTake, now string) error {
	m := t.Mutation
	if !t.isJob() {
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
		return nil
	}
	params := map[string]any{}
	for k, v := range m.Params {
		params[k] = v
	}
	params["job_id"] = m.JobID
	if m.JobID == "" {
		params["job_id"] = t.JobID
	}
	pj, err := jsonString(params) // encoding/json sorts map keys: stable bytes
	if err != nil {
		return err
	}
	jobID := m.JobID
	if jobID == "" {
		jobID = t.JobID
	}
	if m.RecipePatchJSON != "" && !json.Valid([]byte(m.RecipePatchJSON)) {
		return invalid("mutation", "recipe_patch_json is not JSON")
	}
	if err := execTx(ctx, tx, `INSERT INTO mutations(id, parent_track_id, child_track_id, operator, inferred, params_json, rationale, principal_id, created_at, job_id, recipe_patch_json)
		VALUES (?,?,?,?,0,?,?,?,?,?,?)`, s.newID(), nullStr(t.ParentTrackID), t.ID, m.Operator, pj, nullStr(m.Rationale), t.PrincipalID, now, jobID, nullStr(m.RecipePatchJSON)); err != nil {
		return fmt.Errorf("library: insert mutation: %w", err)
	}
	return nil
}

// zeroMD5 is the audio_md5 of a take whose master has no STREAMINFO (lossy).
const zeroMD5 = "00000000000000000000000000000000"

// validateReferenceTake enforces the Go side of the reference/lossy rules (the
// database triggers enforce the same shape for any other writer).
func validateReferenceTake(t NewTake) error {
	if (t.ImportRule == RuleReference) != t.Reference {
		return invalid("import_rule", "rule %q and reference=%v disagree", t.ImportRule, t.Reference)
	}
	if t.Lossy && !t.Reference {
		return invalid("lossy", "a lossy take must be a reference take")
	}
	if t.Reference {
		if t.ScoreABC != nil {
			return invalid("score", "a reference take has no score")
		}
		if len(t.Artifacts) > 0 {
			return invalid("artifacts", "a reference take keeps no artifacts")
		}
		if m := t.Mutation; m != nil && !oneOf(m.Rule, "reference_group", "mapping") {
			return invalid("mutation", "rule %q is not valid for a reference take", m.Rule)
		}
	}
	if !t.Lossy {
		return nil
	}
	for _, r := range t.Renditions {
		if r.Kind != "peaks" {
			return invalid("rendition", "a lossy take has only a peaks rendition, got %q", r.Kind)
		}
	}
	switch {
	case t.BitsPerSample != 16:
		return invalid("bits_per_sample", "a lossy take stores the nominal 16")
	case t.AudioMD5 != zeroMD5:
		return invalid("audio_md5", "a lossy take has an all-zero audio_md5")
	case t.SourceSHA256 != t.MasterSHA256 || t.FramesSHA256 != t.MasterSHA256:
		return invalid("master_sha256", "a lossy take has equal source, frames and master hashes")
	}
	return nil
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
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, t.ID, "takes/"+t.ID+"/score.abc", sha256Hex(t.ScoreABC), sc.Bars, sc.Meter, q, bpm, spb, al, scoreSource(t), now); err != nil {
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
	rows, err := tx.QueryContext(ctx, `SELECT t.root_track_id, r.request_id FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE r.engine <> 'unknown'`)
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
