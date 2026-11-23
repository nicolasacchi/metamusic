package desk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// EvPlanBundleInvalid is the job event of a plan seed whose bundle was refused while the job's other seeds went on (C361.4). Its
// detail keys are index, seed and rule; its code is CodePlanBundleInvalid.
const EvPlanBundleInvalid = "plan_bundle_invalid"

// Codes of a plan job's ingest.
const (
	// CodePlanBundleInvalid marks one seed whose bundle was refused.
	CodePlanBundleInvalid = "PLAN_BUNDLE_INVALID"
	// CodePlansFailed ends a plan job that stored no plan (the job fails, settled normally).
	CodePlansFailed = "PLANS_FAILED"
)

var _ jobs.KindIngester = (*Service)(nil)

// IngestKind is the jobs.KindIngester of kind plan (C361.4). It runs inside the take transaction and turns each ok seed of a
// finished plan job into a Desk plan: the received plan-NN.tar is verified (sha256 and size against the upload and the worker's
// report, then the bundle rules of readBundle), the score must parse in the yue2 dialect, and the plan's files are placed under
// plans/<id>/ with their rows. It never writes a master, a track, a recipe or a mutation: a plan job has no track and returns none.
// A seed whose bundle is refused is lost alone (an event says why) and the others go on; a job that stores no plan fails
// PLANS_FAILED. An upload that contradicts its report is a contract violation of the whole job. Directories placed here are
// removed again when this call fails and, after a rollback that happens later, by the next ingest attempt of the same job or the
// startup sweep.
func (s *Service) IngestKind(ctx context.Context, tx *sql.Tx, in jobs.KindIngest) (trackID *string, outcome string, err error) {
	if err := s.wired(); err != nil {
		return nil, "", err
	}
	if in.Job.Kind != jobs.KindPlan {
		return nil, "", errors.New("desk: IngestKind serves plan jobs only")
	}
	spec, err := jobspec.Parse([]byte(in.Job.SpecJSON))
	if err != nil || spec.Plan == nil || len(spec.Plan.Seeds) == 0 {
		return nil, "", errors.New("desk: a plan job carries its seeds in its spec")
	}
	seeds := spec.Plan.Seeds
	if len(in.Output.Plans) != len(seeds) {
		return nil, "", &jobs.ContractViolation{Code: "WORKER_CONTRACT", Text: "the plan results do not match the job's seeds"}
	}
	s.discardPlaced(ctx, tx, in.Job.ID)
	var placed []string
	defer func() {
		if err != nil {
			for _, id := range placed {
				s.removePlanDir(id)
			}
		}
	}()
	base, err := s.planBase(ctx, tx, in.Job)
	if err != nil {
		return nil, "", err
	}
	byName := map[string]upload.Received{}
	for _, f := range in.Files {
		byName[f.Artifact] = f
	}
	stored, truncated := 0, false
	for i, po := range in.Output.Plans {
		if po.Index != i || po.Seed != seeds[i] {
			return nil, "", &jobs.ContractViolation{Code: "WORKER_CONTRACT", Text: "the plan results do not match the job's seeds"}
		}
		if po.Status != "ok" {
			continue
		}
		name := provider.PlanBundleName(i)
		file, ok := byName[name]
		if !ok || po.Bundle != name {
			return nil, "", &jobs.ContractViolation{Code: "ARTIFACT_MISSING", Text: name + " was not received"}
		}
		raw, rerr := s.readUpload(file, in.Output)
		if rerr != nil {
			return nil, "", rerr
		}
		id, trunc, perr := s.ingestSeed(ctx, tx, in, base, i, po, raw)
		var be *bundleError
		switch {
		case errors.As(perr, &be):
			if err := s.recordInvalid(ctx, tx, in, i, seeds[i], be.rule); err != nil {
				return nil, "", err
			}
			continue
		case perr != nil:
			if id != "" {
				placed = append(placed, id)
			}
			return nil, "", perr
		}
		placed = append(placed, id)
		stored++
		truncated = truncated || trunc
	}
	if stored == 0 {
		return nil, "", &jobs.ContractViolation{Code: CodePlansFailed, Text: "no plan seed produced a valid bundle"}
	}
	s.mu.Lock()
	s.placed[in.Job.ID] = placed
	s.mu.Unlock()
	if truncated {
		return nil, "truncated", nil
	}
	return nil, "clean", nil
}

// AfterKindCommit forgets the directories of the committed attempt: they now belong to rows.
func (s *Service) AfterKindCommit(_ context.Context, in jobs.KindIngest) {
	s.mu.Lock()
	delete(s.placed, in.Job.ID)
	s.mu.Unlock()
}

// discardPlaced removes the directories an earlier, rolled-back attempt of the job left (a directory whose plan row exists belongs
// to a committed attempt and stays).
func (s *Service) discardPlaced(ctx context.Context, tx *sql.Tx, jobID string) {
	s.mu.Lock()
	ids := s.placed[jobID]
	delete(s.placed, jobID)
	s.mu.Unlock()
	for _, id := range ids {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM desk_plans WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			s.removePlanDir(id)
		}
	}
}

// base is what every plan of a job shares.
type jobBase struct {
	request              yue2.Request
	cot                  string
	title                string
	by                   rawPrincipal
	baseTrack, baseDraft string
}

func (s *Service) planBase(ctx context.Context, tx *sql.Tx, job jobs.JobRow) (jobBase, error) {
	var b jobBase
	var reqJSON string
	if err := tx.QueryRowContext(ctx, `SELECT request_json, cot FROM recipes WHERE id = ? AND engine = 'yue2'`, job.RecipeID).Scan(&reqJSON, &b.cot); err != nil {
		return b, fmt.Errorf("desk: recipe of plan job %s: %w", job.ID, err)
	}
	if b.cot != "full" && b.cot != "melody" {
		return b, &jobs.ContractViolation{Code: "WORKER_CONTRACT", Text: "a plan job's recipe plans with cot full or melody"}
	}
	if err := json.Unmarshal([]byte(reqJSON), &b.request); err != nil {
		return b, fmt.Errorf("desk: recipe of plan job %s: %w", job.ID, err)
	}
	b.request.ABC = nil
	if err := tx.QueryRowContext(ctx, `SELECT principal_id, principal_kind, principal_name FROM batches WHERE id = ?`, job.BatchID).Scan(&b.by.id, &b.by.kind, &b.by.name); err != nil {
		return b, fmt.Errorf("desk: batch of plan job %s: %w", job.ID, err)
	}
	b.title = job.Title
	if i := strings.Index(b.title, " · plans ×"); i > 0 {
		b.title = b.title[:i]
	}
	if job.SourceTrackID != nil {
		b.baseTrack = *job.SourceTrackID
	}
	if job.DraftID != nil {
		b.baseDraft = *job.DraftID
	}
	return b, nil
}

// readUpload reads a received bundle through the data root and holds it to its size limit, the upload's own sha256 and size, and
// the worker's report. A difference is a contract violation of the job: the upload service verified these bytes once already.
func (s *Service) readUpload(f upload.Received, out provider.Output) ([]byte, error) {
	mismatch := &jobs.ContractViolation{Code: "ARTIFACT_MISMATCH", Text: f.Artifact + " differs from what was received"}
	if f.Bytes > provider.MaxPlanBundleBytes {
		return nil, mismatch
	}
	fh, err := s.o.Data.Open(f.Path)
	if err != nil {
		return nil, fmt.Errorf("desk: open %s: %w", f.Artifact, unwrapPath(err))
	}
	defer func() { _ = fh.Close() }()
	raw, err := io.ReadAll(io.LimitReader(fh, provider.MaxPlanBundleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("desk: read %s: %w", f.Artifact, unwrapPath(err))
	}
	sum := sha256.Sum256(raw)
	rep, ok := out.Artifacts[f.Artifact]
	if int64(len(raw)) != f.Bytes || hex.EncodeToString(sum[:]) != f.SHA256 || !ok || rep.SHA256 != f.SHA256 || rep.Bytes != f.Bytes {
		return nil, mismatch
	}
	return raw, nil
}

// ingestSeed verifies one bundle and stores its plan. It returns the plan id once a directory was placed (also with an error, so
// the caller removes it) and whether the plan is truncated. A refused bundle is a *bundleError.
func (s *Service) ingestSeed(ctx context.Context, tx *sql.Tx, in jobs.KindIngest, base jobBase, i int, po provider.PlanOut, raw []byte) (string, bool, error) {
	b, err := readBundle(raw, int64(len(raw)))
	if err != nil {
		return "", false, err
	}
	if b.meta.Seed != po.Seed || b.meta.Cot != base.cot {
		return "", false, bad(RuleBundleMismatch, "plan.json names another seed or cot than the job")
	}
	src := b.score()
	if len(src) > MaxScoreBytes {
		return "", false, bad(RuleBundleSize, "score.abc is over %d bytes", MaxScoreBytes)
	}
	tune, perr := abc.Parse(src, abc.Options{Dialect: abc.DialectYuE2})
	if perr != nil {
		return "", false, bad(RuleBundleScore, "the score does not parse as yue2 ABC")
	}
	sc, perr := yue2.ParseScore(src)
	if perr != nil {
		return "", false, bad(RuleBundleScore, "the score has no yue2 header")
	}
	req := base.request
	req.Seed = po.Seed
	withABC := string(src)
	timed := req
	timed.ABC = &withABC
	fallback := 0
	if secs, err := (yue2.Engine{}).ExpectedAudioSeconds(yue2.CanonicalJSON(timed)); err == nil {
		fallback = secs
	}
	prof := profileOf(tune, max(fallback, 1))
	// the scores table is the timing source of a take (D-DESK-10); a plan reads its score the same way
	_, aligned := yue2.AlignSections(yue2.LyricsSections(req.Lyrics), sc)
	prof.bars, prof.meter = sc.Bars, sc.Meter
	prof.q, prof.spb = nil, nil
	if sc.Q != "" {
		q := sc.Q
		prof.q = &q
	}
	if sc.SecondsPerBar > 0 {
		v := sc.SecondsPerBar
		prof.spb = &v
	}
	truncated := b.meta.Truncated
	for _, t := range po.Truncated {
		truncated = truncated || t
	}
	id := s.o.NewID()
	dir := plansRoot + "/" + id + "/"
	files := make([]fileRow, 0, len(b.files))
	for _, name := range bundleOrder {
		body, ok := b.files[name]
		if !ok {
			continue
		}
		sum := sha256.Sum256(body)
		files = append(files, fileRow{name: name, sha: hex.EncodeToString(sum[:]), path: dir + name, bytes: len(body)})
	}
	title := []rune(base.title + " · plan s" + fmt.Sprint(po.Seed))
	if len(title) > 200 {
		title = title[:200]
	}
	idx := i
	n := newPlan{id: id, source: "job", jobID: in.Job.ID, seedIndex: &idx, baseTrackID: base.baseTrack, baseDraftID: base.baseDraft,
		requestJSON: string(yue2.CanonicalJSON(req)), seed: po.Seed, cot: base.cot, score: src, sha: sha256Hex(src), scorePath: dir + scoreName,
		dialect: string(abc.DialectYuE2), prof: prof, aligned: aligned, abcTokens: b.meta.ABCTokens, truncated: &truncated,
		title: string(title), raw: &base.by, files: files}
	if err := s.writePlanDir(id, b.files); err != nil {
		return "", false, err
	}
	if err := s.insert(ctx, tx, n); err != nil {
		s.removePlanDir(id)
		return "", false, fmt.Errorf("desk: store plan of job %s: %w", in.Job.ID, err)
	}
	return id, truncated, nil
}

// recordInvalid appends the plan_bundle_invalid event under the lease fence, like every job event.
func (s *Service) recordInvalid(ctx context.Context, tx *sql.Tx, in jobs.KindIngest, index int, seed int64, rule string) error {
	var attempt any
	if in.Attempt.ID != "" {
		attempt = in.Attempt.ID
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO job_events(job_id, attempt_id, at, event, code, detail_json) SELECT ?, ?, ?, ?, ?, json_object('index', ?, 'seed', ?, 'rule', ?) WHERE `+in.Fence.Where(), //nolint:gosec // G202: fixed lease predicate, values are bound
		append([]any{in.Job.ID, attempt, store.FormatTime(in.Fence.Now), EvPlanBundleInvalid, CodePlanBundleInvalid, index, seed, rule}, in.Fence.Args()...)...)
	n, err := in.Fence.Fenced(res, err)
	if err != nil {
		return fmt.Errorf("desk: bundle event: %w", err)
	}
	if n == 0 {
		return store.ErrLeaseLost
	}
	return nil
}
