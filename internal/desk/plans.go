package desk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

// PlanBase says which take or draft a plan was made for, and its title.
type PlanBase struct {
	TrackID *string `json:"track_id"`
	DraftID *string `json:"draft_id"`
	Title   string  `json:"title"`
}

// PlanSummary is one plan as a list shows it (C359.2). CreatedBy holds the raw principal (kind, key, name); the API layer renders
// it for the viewer.
type PlanSummary struct {
	ID             string            `json:"id"`
	Source         string            `json:"source" enum:"job,take,edit"`
	JobID          *string           `json:"job_id"`
	BatchID        *string           `json:"batch_id"`
	SeedIndex      *int              `json:"seed_index"`
	TrackID        *string           `json:"track_id"`
	ParentPlanID   *string           `json:"parent_plan_id"`
	Base           PlanBase          `json:"base"`
	Seed           int64             `json:"seed"`
	Cot            string            `json:"cot" enum:"full,melody"`
	Title          string            `json:"title"`
	Bars           int               `json:"bars"`
	Meter          string            `json:"meter"`
	Q              *string           `json:"q"`
	SecondsPerBar  *float64          `json:"seconds_per_bar"`
	ExpectedAudioS int               `json:"expected_audio_s"`
	Truncated      *bool             `json:"truncated"`
	Verdict        *string           `json:"verdict" enum:"liked,rejected"`
	CriticVerdict  *string           `json:"critic_verdict" enum:"liked,rejected"`
	Rendered       int               `json:"rendered" doc:"Render jobs of this plan that produced a take"`
	CreatedBy      auth.PrincipalRef `json:"created_by"`
	CreatedAt      string            `json:"created_at"`
}

// PlanVoice is one voice of the score.
type PlanVoice struct {
	ID   string  `json:"id"`
	Name *string `json:"name"`
	Bars int     `json:"bars"`
}

// PlanScore describes the stored score text.
type PlanScore struct {
	SHA256    string      `json:"sha256"`
	Bytes     int         `json:"bytes"`
	Dialect   string      `json:"dialect" enum:"yue2,standard"`
	Aligned   bool        `json:"aligned"`
	ABCTokens *int        `json:"abc_tokens"`
	Voices    []PlanVoice `json:"voices"`
}

// PlanSection is one section of the score with its bar range and, when the score is timed, its seconds.
type PlanSection struct {
	Idx      int      `json:"idx"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"`
	StartBar int      `json:"start_bar"`
	Bars     int      `json:"bars"`
	StartS   *float64 `json:"start_s"`
	EndS     *float64 `json:"end_s"`
}

// BarSpan locates one bar of one voice in the score text (byte offsets) and its start time when timed.
type BarSpan struct {
	Voice  string   `json:"voice"`
	Bar    int      `json:"bar"`
	Start  int      `json:"start"`
	End    int      `json:"end"`
	StartS *float64 `json:"start_s"`
}

// Display is the standard-dialect rewrite of a yue2 score (D-DESK-15) with its own bar spans; the stored score never changes.
type Display struct {
	ABC      string    `json:"abc"`
	BarSpans []BarSpan `json:"bar_spans"`
}

// EditInfo is how an edit plan came to be.
type EditInfo struct {
	Ops       []abc.Op `json:"ops"`
	Rationale *string  `json:"rationale"`
}

// Child names a plan edited from this one.
type Child struct {
	PlanID    string `json:"plan_id"`
	CreatedAt string `json:"created_at"`
}

// OwnerVerdict is the Owner's row.
type OwnerVerdict struct {
	Verdict   *string `json:"verdict" enum:"liked,rejected"`
	Note      *string `json:"note"`
	DecidedAt string  `json:"decided_at"`
}

// CriticVerdict is the critic agent's row.
type CriticVerdict struct {
	Verdict   *string           `json:"verdict" enum:"liked,rejected"`
	Note      *string           `json:"note"`
	By        auth.PrincipalRef `json:"by"`
	DecidedAt string            `json:"decided_at"`
}

// Verdicts holds both raters' rows; a rater that never spoke is null.
type Verdicts struct {
	Owner  *OwnerVerdict  `json:"owner"`
	Critic *CriticVerdict `json:"critic"`
}

// AuditionRef is an audition of the plan as the plan shows it.
type AuditionRef struct {
	ID        string  `json:"id"`
	Soundfont string  `json:"soundfont"`
	Chords    string  `json:"chords" enum:"none,block"`
	Seconds   float64 `json:"seconds"`
	URL       string  `json:"url"`
	CreatedAt string  `json:"created_at"`
}

// RenderRef is a render_plan job of the plan.
type RenderRef struct {
	JobID   string  `json:"job_id"`
	BatchID string  `json:"batch_id"`
	State   string  `json:"state"`
	Seed    int64   `json:"seed"`
	TrackID *string `json:"track_id"`
}

// Plan is a plan with its score (C359.2). The optional parts follow the include list.
type Plan struct {
	PlanSummary
	Score     PlanScore      `json:"score"`
	Sections  []PlanSection  `json:"sections"`
	ABC       *string        `json:"abc,omitempty"`
	Display   *Display       `json:"display,omitempty"`
	BarSpans  []BarSpan      `json:"bar_spans,omitempty"`
	Edit      *EditInfo      `json:"edit"`
	Children  []Child        `json:"children"`
	Verdicts  Verdicts       `json:"verdicts"`
	Auditions *[]AuditionRef `json:"auditions,omitempty"`
	Renders   *[]RenderRef   `json:"renders,omitempty"`
}

// row is a desk_plans row with what the views join to it.
type row struct {
	PlanSummary
	requestJSON string
	scorePath   string
	scoreSHA    string
	scoreBytes  int
	dialect     string
	aligned     bool
	sections    []PlanSection
	abcTokens   *int
	edit        *EditInfo
}

const summaryCols = `p.id, p.source, p.job_id, j.batch_id, p.seed_index, p.track_id, p.parent_plan_id, p.base_track_id, p.base_draft_id,
	COALESCE(bt.title, p.title), p.seed, p.cot, p.title, p.bars, p.meter, p.q, p.seconds_per_bar, p.expected_audio_s, p.truncated,
	ov.verdict, cv.verdict,
	(SELECT COUNT(*) FROM jobs rj WHERE rj.operator = 'render_plan' AND json_extract(rj.mutation_json, '$.params.plan_id') = p.id AND rj.track_id IS NOT NULL),
	p.principal_id, p.principal_kind, p.principal_name, p.created_at`

const summaryFrom = ` FROM desk_plans p LEFT JOIN jobs j ON j.id = p.job_id LEFT JOIN tracks bt ON bt.id = p.base_track_id
	LEFT JOIN desk_plan_verdicts ov ON ov.plan_id = p.id AND ov.rater = 'owner'
	LEFT JOIN desk_plan_verdicts cv ON cv.plan_id = p.id AND cv.rater = 'critic'`

func scanSummary(sc interface{ Scan(...any) error }, extra ...any) (PlanSummary, error) {
	var p PlanSummary
	var job, batch, track, parent, baseTrack, baseDraft, q, ov, cv sql.NullString
	var seedIdx sql.NullInt64
	var spb sql.NullFloat64
	var trunc sql.NullInt64
	var pk, pkind, pname string
	dest := []any{&p.ID, &p.Source, &job, &batch, &seedIdx, &track, &parent, &baseTrack, &baseDraft,
		&p.Base.Title, &p.Seed, &p.Cot, &p.Title, &p.Bars, &p.Meter, &q, &spb, &p.ExpectedAudioS, &trunc, &ov, &cv, &p.Rendered,
		&pk, &pkind, &pname, &p.CreatedAt}
	if err := sc.Scan(append(dest, extra...)...); err != nil {
		return p, err
	}
	p.JobID, p.BatchID, p.TrackID, p.ParentPlanID = strp(job), strp(batch), strp(track), strp(parent)
	p.Base.TrackID, p.Base.DraftID, p.Q, p.Verdict, p.CriticVerdict = strp(baseTrack), strp(baseDraft), strp(q), strp(ov), strp(cv)
	if seedIdx.Valid {
		v := int(seedIdx.Int64)
		p.SeedIndex = &v
	}
	if spb.Valid {
		v := spb.Float64
		p.SecondsPerBar = &v
	}
	if trunc.Valid {
		v := trunc.Int64 == 1
		p.Truncated = &v
	}
	p.CreatedBy = auth.PrincipalRef{Kind: pkind, ID: pk, Name: pname}
	return p, nil
}

func strp(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}

// load reads one plan row with the columns the detail view needs.
func load(ctx context.Context, q queryer, id string) (row, error) {
	r := q.QueryRowContext(ctx, `SELECT `+summaryCols+`, p.request_json, p.score_path, p.score_sha256, p.score_bytes, p.dialect, p.aligned,
		p.sections_json, p.abc_tokens, p.edit_json`+summaryFrom+` WHERE p.id = ?`, id)
	var out row
	var aligned int
	var secs string
	var tokens sql.NullInt64
	var edit sql.NullString
	sum, err := scanSummary(r, &out.requestJSON, &out.scorePath, &out.scoreSHA, &out.scoreBytes, &out.dialect, &aligned, &secs, &tokens, &edit)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return out, notFound("plan")
	case err != nil:
		return out, err
	}
	out.PlanSummary, out.aligned = sum, aligned == 1
	if err := json.Unmarshal([]byte(secs), &out.sections); err != nil {
		return out, fmt.Errorf("desk: plan %s sections: %w", id, err)
	}
	if tokens.Valid {
		v := int(tokens.Int64)
		out.abcTokens = &v
	}
	if edit.Valid {
		out.edit = &EditInfo{}
		if err := json.Unmarshal([]byte(edit.String), out.edit); err != nil {
			return out, fmt.Errorf("desk: plan %s edit: %w", id, err)
		}
	}
	return out, nil
}

// errScoreTooLarge marks a stored score over MaxScoreBytes.
var errScoreTooLarge = errors.New("score too large")

// readScore reads a stored score file, refusing one over the limit.
func (s *Service) readScore(rel string) ([]byte, error) {
	f, err := s.o.Data.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("desk: open %s: %w", rel, unwrapPath(err))
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, MaxScoreBytes+1))
	if err != nil {
		return nil, fmt.Errorf("desk: read %s: %w", rel, unwrapPath(err))
	}
	if len(b) > MaxScoreBytes {
		return nil, fmt.Errorf("desk: %s is over %d bytes: %w", rel, MaxScoreBytes, errScoreTooLarge)
	}
	return b, nil
}

// unwrapPath drops the absolute-looking path from a filesystem error.
func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// profile is what a plan stores about its score, derived from the parsed tune.
type profile struct {
	bars     int
	meter    string
	q        *string
	spb      *float64
	sections []PlanSection
	audioS   int
}

// profileOf summarises t. fallbackS is the expected audio length when the score does not time itself.
func profileOf(t *abc.Tune, fallbackS int) profile {
	p := profile{meter: fmt.Sprintf("%d/%d", t.Header.MeterNum, t.Header.MeterDen)}
	if len(t.Voices) > 0 {
		p.bars = len(t.Voices[0].Bars)
	}
	if t.Header.Tempo != nil && t.Header.Tempo.Raw != "" {
		q := t.Header.Tempo.Raw
		p.q = &q
	}
	spb, timed := t.SecondsPerBar()
	if timed {
		p.spb = &spb
	}
	for i, sec := range t.Sections {
		ps := PlanSection{Idx: i, Label: sec.Label, Kind: sec.Kind, StartBar: sec.StartBar, Bars: sec.Bars}
		if timed {
			a, b := round6(float64(sec.StartBar)*spb), round6(float64(sec.StartBar+sec.Bars)*spb)
			ps.StartS, ps.EndS = &a, &b
		}
		p.sections = append(p.sections, ps)
	}
	if p.sections == nil {
		p.sections = []PlanSection{}
	}
	secs := float64(fallbackS)
	switch {
	case timed:
		secs = float64(p.bars) * spb
	default:
		if sc, err := t.Score(abc.EventOptions{Chords: abc.ChordsNone}); err == nil {
			var end int64
			for _, v := range sc.Voices {
				for _, n := range v.Notes {
					end = max(end, n.Tick+n.Dur)
				}
			}
			if d := sc.SecondsAt(end); d > 0 {
				secs = d
			}
		}
	}
	p.audioS = int(min(max(math.Ceil(secs), 1), MaxAudioS))
	return p
}

func round6(x float64) float64 { return math.Round(x*1e6) / 1e6 }

// newPlan is a plan about to be stored.
type newPlan struct {
	id, source, jobID, trackID, parentID, baseTrackID, baseDraftID string
	seedIndex                                                      *int
	requestJSON                                                    string
	seed                                                           int64
	cot                                                            string
	score                                                          []byte
	sha, scorePath                                                 string
	dialect                                                        string
	prof                                                           profile
	aligned                                                        bool
	abcTokens                                                      *int
	truncated                                                      *bool
	edit                                                           *EditInfo
	title                                                          string
	by                                                             auth.Principal
	// raw, when set, holds the principal columns as stored (a plan job's plans carry the batch's requester, who is not a session).
	raw *rawPrincipal
	// files lists the desk_plan_files rows to write with the plan (name, sha, bytes, path).
	files []fileRow
}

// rawPrincipal is a principal as the principal_* columns hold it.
type rawPrincipal struct{ id, kind, name string }

type fileRow struct {
	name, sha, path string
	bytes           int
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullBool(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return 1
	}
	return 0
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// insert stores the row and its files inside tx.
func (s *Service) insert(ctx context.Context, tx *sql.Tx, n newPlan) error {
	secs, err := json.Marshal(n.prof.sections)
	if err != nil {
		return err
	}
	var editJSON any
	if n.edit != nil {
		b, err := json.Marshal(n.edit)
		if err != nil {
			return err
		}
		editJSON = string(b)
	}
	var q, spb any
	if n.prof.q != nil {
		q = *n.prof.q
	}
	if n.prof.spb != nil {
		spb = *n.prof.spb
	}
	pid, pkind, pname := n.by.Key(), string(n.by.Kind), n.by.Name
	if n.raw != nil {
		pid, pkind, pname = n.raw.id, n.raw.kind, n.raw.name
	}
	al := 0
	if n.aligned {
		al = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO desk_plans(id, source, job_id, seed_index, track_id, parent_plan_id, base_track_id, base_draft_id,
		engine, request_json, seed, cot, score_path, score_sha256, score_bytes, dialect, bars, meter, q, seconds_per_bar, aligned, sections_json,
		expected_audio_s, abc_tokens, truncated, edit_json, title, principal_id, principal_kind, principal_name, created_at)
		VALUES (?,?,?,?,?,?,?,?, 'yue2',?,?,?,?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?)`,
		n.id, n.source, nullStr(n.jobID), nullInt(n.seedIndex), nullStr(n.trackID), nullStr(n.parentID), nullStr(n.baseTrackID), nullStr(n.baseDraftID),
		n.requestJSON, n.seed, n.cot, n.scorePath, n.sha, len(n.score), n.dialect, n.prof.bars, n.prof.meter, q, spb, al, string(secs),
		n.prof.audioS, nullInt(n.abcTokens), nullBool(n.truncated), editJSON, n.title, pid, pkind, pname, s.now()); err != nil {
		return err
	}
	for _, f := range n.files {
		if _, err := tx.ExecContext(ctx, `INSERT INTO desk_plan_files(plan_id, name, sha256, bytes, path, created_at) VALUES (?,?,?,?,?,?)`,
			n.id, f.name, f.sha, f.bytes, f.path, s.now()); err != nil {
			return err
		}
	}
	return nil
}

// auditionURL is the signed playback link of an audition MP3 for the calling principal.
func (s *Service) auditionURL(p auth.Principal, sha string) string {
	if s.o.Signer == nil {
		return ""
	}
	ttl, bucket := serve.TTLFor(p)
	return s.o.Signer.Sign(sha, "mp3", ttl, bucket).URL
}

// splitCSV splits a comma list, dropping blanks.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
