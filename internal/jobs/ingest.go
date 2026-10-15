package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// JobRow is the claimed job as the runner and the ingest bridge see it.
type JobRow struct {
	ID, BatchID, RecipeID, Operator, Title, State string
	SourceTrackID, DraftID                        *string
	MutationJSON                                  string
	TakeIndex, TimeoutS, ExpectedAudioS           int
}

// AttemptRow is one provider attempt of a job.
type AttemptRow struct {
	ID, JobID, State string
	N                int
	WindowEndsAt     time.Time
	// WorkerKey and ImageDigest identify the worker that ran the attempt (D-M3-26).
	WorkerKey, ImageDigest string
}

// Ingester turns a verified worker result into a library take (C75.5).
type Ingester interface {
	IngestTake(ctx context.Context, job JobRow, att AttemptRow, files []upload.Received, out provider.Output) (trackID string, err error)
}

// ContractViolation is what an Ingester returns when the worker's output contradicts its uploads: the job
// ends failed with Code (WORKER_CONTRACT, ARTIFACT_MISMATCH), settled normally.
type ContractViolation struct {
	Code, Text string
}

func (e *ContractViolation) Error() string { return "jobs: " + e.Code + ": " + e.Text }

// BridgeOptions builds the real ingest bridge.
type BridgeOptions struct {
	Data     *os.Root
	Pipeline *ingest.Pipeline
	Library  *library.Service
	Ears     *ears.Service
	Lineage  *lineage.Service
	Spend    *spend.Service // RTF statistics; nil skips them
	Uploads  *upload.Service
	Store    *store.Store
	Clock    clock.Clock
	NewID    func() string
	Log      *slog.Logger
	// Fault injects a crash at a named point (tests): "after_ingest" runs between the pipeline and the
	// take transaction, and a returned error leaves the pipeline's files behind as a crash would.
	Fault func(point string) error
}

// FaultAfterIngest is the bridge's crash point between the pipeline and the take transaction.
const FaultAfterIngest = "after_ingest"

// IngestBridge is the production Ingester.
type IngestBridge struct{ o BridgeOptions }

// NewIngestBridge builds the bridge.
func NewIngestBridge(o BridgeOptions) *IngestBridge {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.NewID == nil {
		o.NewID = newUUID
	}
	return &IngestBridge{o: o}
}

type leaseOwnerKey struct{}

// WithLeaseOwner carries the runner's lease owner to an Ingester, whose transaction repeats the lease
// predicate (D-M3-30). The runner sets it before every IngestTake.
func WithLeaseOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, leaseOwnerKey{}, owner)
}

func leaseOwnerFrom(ctx context.Context) string {
	s, _ := ctx.Value(leaseOwnerKey{}).(string)
	return s
}

// requiredIngest are the artifacts every take needs (C73.4).
var requiredIngest = []string{"audio.flac", "request.json", "config.json", "result.json"}

func violation(code, format string, a ...any) error {
	return &ContractViolation{Code: code, Text: fmt.Sprintf(format, a...)}
}

// IngestTake implements Ingester (C75.5): verify the output against the uploads, freeze the audio through
// the M1 pipeline under a fresh TrackID, and write every row of the take in one transaction.
func (b *IngestBridge) IngestTake(ctx context.Context, job JobRow, att AttemptRow, files []upload.Received, out provider.Output) (string, error) {
	owner := leaseOwnerFrom(ctx)
	if owner == "" {
		return "", errors.New("jobs: ingest without a runner lease")
	}
	if b.o.Pipeline == nil || b.o.Data == nil || b.o.Library == nil || b.o.Uploads == nil {
		return "", errors.New("jobs: ingest bridge is not wired")
	}
	byName := map[string]upload.Received{}
	for _, f := range files {
		byName[f.Artifact] = f
	}
	if err := b.verify(job, byName, out); err != nil {
		return "", err
	}
	req, err := b.loadRecipe(ctx, job)
	if err != nil {
		return "", err
	}
	trunc, err := b.truncationOf(byName, out)
	if err != nil {
		return "", err
	}

	// A fresh UUIDv7 per ingest attempt: M1's work dir and takes/<TrackID> must not exist yet.
	trackID := b.o.NewID()
	title := job.Title
	slug := library.Slug(title)
	if slug == "" {
		if slug = library.Slug(req.requestID); slug == "" {
			slug = "take"
		}
	}
	var opened []fs.File
	closeAll := func() {
		for _, f := range opened {
			_ = f.Close()
		}
		opened = nil
	}
	defer closeAll()
	open := func(u upload.Received) (fs.File, error) {
		f, err := b.o.Data.Open(u.Path)
		if err != nil {
			return nil, err
		}
		opened = append(opened, f)
		return f, nil
	}
	src, err := open(byName["audio.flac"])
	if err != nil {
		return "", &IngestFailedError{Code: ingest.CodeSourceChanged, Err: err}
	}
	var arts []ingest.ArtifactSrc
	for _, name := range sortedKeys(byName) {
		if name == "audio.flac" {
			continue
		}
		f, err := open(byName[name])
		if err != nil {
			return "", &IngestFailedError{Code: ingest.CodeSourceChanged, Err: err}
		}
		arts = append(arts, ingest.ArtifactSrc{Name: name, File: f, SHA256: byName[name].SHA256})
	}
	now := b.o.Clock.Now()
	res, err := b.o.Pipeline.Ingest(ctx, ingest.Request{
		TrackID: trackID, Title: title, Slug: slug, RecipeID: job.RecipeID, RequestID: req.requestID, Seed: req.seed,
		Source: src, SourceSHA256: byName["audio.flac"].SHA256, RecordedAt: now, Artifacts: arts,
	})
	if err != nil {
		var ie *ingest.Error
		if errors.As(err, &ie) {
			return "", &IngestFailedError{Code: ie.Code, Err: err}
		}
		return "", &IngestFailedError{Code: ingest.CodeIO, Err: err}
	}
	if b.o.Fault != nil {
		if ferr := b.o.Fault(FaultAfterIngest); ferr != nil {
			return "", ferr // a crash: the pipeline's files stay behind
		}
	}
	// M1 audio identity: a bit-identical render is an answer, not an error (D-M3-32)
	if existing, ok, qerr := b.existingIdentity(ctx, res); qerr != nil {
		_ = b.o.Pipeline.Discard(res)
		return "", qerr
	} else if ok {
		_ = b.o.Pipeline.Discard(res)
		return b.duplicate(job, existing)
	}

	take, err := b.newTake(ctx, job, att, out, trackID, title, slug, req, res, trunc, byName, now)
	if err != nil {
		_ = b.o.Pipeline.Discard(res)
		return "", err
	}
	if err := b.commit(ctx, owner, job, att, out, take, byName); err != nil {
		_ = b.o.Pipeline.Discard(res)
		if existing, ok, qerr := b.existingIdentity(ctx, res); qerr == nil && ok {
			return b.duplicate(job, existing)
		}
		b.o.Log.Warn("ingest commit", "err", err.Error())
		return "", &IngestFailedError{Code: "DB_INSERT", Err: err}
	}
	closeAll()
	ids := make([]string, 0, len(files))
	for _, f := range files {
		ids = append(ids, f.UploadID)
	}
	if err := b.o.Uploads.RemoveConsumedFiles(ctx, ids); err != nil {
		b.o.Log.Warn("remove consumed uploads", "job", job.ID)
	}
	b.analyse(ctx, owner, job, trackID)
	return trackID, nil
}

// verify checks the output against the uploads (C75.5): the required set, sha and size of every reported
// artifact, and a truncation object of exactly two booleans.
func (b *IngestBridge) verify(job JobRow, byName map[string]upload.Received, out provider.Output) error {
	if out.Contract != provider.Contract || out.Status != "ok" || out.TakeID != job.ID {
		return violation("WORKER_CONTRACT", "output does not belong to this take")
	}
	if out.AudioSeconds == nil {
		return violation("WORKER_CONTRACT", "audio_seconds missing")
	}
	if !validTruncated(out.Truncated) {
		return violation("WORKER_CONTRACT", "truncated is not an object of abc and semantic")
	}
	for _, name := range requiredIngest {
		u, ok := byName[name]
		if !ok {
			return violation("ARTIFACT_MISSING", "required artifact %s was not received", name)
		}
		rep, ok := out.Artifacts[name]
		if !ok {
			return violation("ARTIFACT_MISSING", "required artifact %s is not reported", name)
		}
		if rep.SHA256 != u.SHA256 || rep.Bytes != u.Bytes {
			return violation("ARTIFACT_MISMATCH", "artifact %s differs from its report", name)
		}
	}
	for name, rep := range out.Artifacts {
		u, ok := byName[name]
		if !ok {
			return violation("ARTIFACT_MISSING", "reported artifact %s was not received", name)
		}
		if rep.SHA256 != u.SHA256 || rep.Bytes != u.Bytes {
			return violation("ARTIFACT_MISMATCH", "artifact %s differs from its report", name)
		}
	}
	if _, ok := byName[provider.ResultArtifact]; !ok {
		return violation("ARTIFACT_MISSING", "%s was not received", provider.ResultArtifact)
	}
	return nil
}

// truncationOf compares result.json's truncated object with the worker output's (C75.5).
func (b *IngestBridge) truncationOf(byName map[string]upload.Received, out provider.Output) ([2]bool, error) {
	var zero [2]bool
	f, err := b.o.Data.Open(byName["result.json"].Path)
	if err != nil {
		return zero, &IngestFailedError{Code: ingest.CodeSourceChanged, Err: err}
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 4<<20+1))
	if err != nil || len(raw) > 4<<20 {
		return zero, violation("WORKER_CONTRACT", "result.json is unreadable")
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &doc) != nil {
		return zero, violation("WORKER_CONTRACT", "result.json is not an object")
	}
	var tr struct {
		ABC      *bool `json:"abc"`
		Semantic *bool `json:"semantic"`
	}
	if t, ok := doc["truncated"]; ok {
		dec := json.NewDecoder(bytes.NewReader(t))
		dec.DisallowUnknownFields()
		if dec.Decode(&tr) != nil || tr.ABC == nil || tr.Semantic == nil {
			return zero, violation("WORKER_CONTRACT", "result.json truncated is not an object of abc and semantic")
		}
		if *tr.ABC != out.Truncated["abc"] || *tr.Semantic != out.Truncated["semantic"] {
			return zero, violation("WORKER_CONTRACT", "result.json truncated differs from the worker output")
		}
	}
	return [2]bool{out.Truncated["abc"], out.Truncated["semantic"]}, nil
}

type recipeInfo struct {
	requestID string
	seed      int64
}

func (b *IngestBridge) loadRecipe(ctx context.Context, job JobRow) (recipeInfo, error) {
	var r recipeInfo
	if err := b.o.Store.R.QueryRowContext(ctx, `SELECT request_id, seed FROM recipes WHERE id = ?`, job.RecipeID).Scan(&r.requestID, &r.seed); err != nil {
		return r, fmt.Errorf("jobs: recipe of job %s: %w", job.ID, err)
	}
	return r, nil
}

// existingIdentity finds a track with the same M1 audio identity (frames hash and sample count).
func (b *IngestBridge) existingIdentity(ctx context.Context, res ingest.Result) (existing struct{ ID, JobID string }, ok bool, err error) {
	var jid sql.NullString
	err = b.o.Store.R.QueryRowContext(ctx, `SELECT id, job_id FROM tracks WHERE frames_sha256 = ? AND total_samples = ?`,
		res.FramesSHA256, int64(res.Info.StreamInfo.TotalSamples)).Scan(&existing.ID, &jid) //nolint:gosec // FLAC totals are 36 bits
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return existing, false, nil
	case err != nil:
		return existing, false, err
	}
	existing.JobID = jid.String
	return existing, true, nil
}

// duplicate answers an identity hit: the job's own earlier track (the take committed, then the process
// stopped before the job did) completes the job from it; any other track is DUPLICATE_AUDIO.
func (b *IngestBridge) duplicate(job JobRow, existing struct{ ID, JobID string }) (string, error) {
	if existing.JobID != job.ID {
		return "", &DuplicateAudioError{TrackID: existing.ID}
	}
	return existing.ID, nil
}

// fencedTx runs fn in one write transaction whose first statement confirms that this runner still owns
// the job and holds the lease; a zero-row guard is store.ErrLeaseLost.
func (b *IngestBridge) fencedTx(ctx context.Context, owner string, job JobRow, fn func(tx *sql.Tx, f store.LeaseFence) error) error {
	tx, err := b.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	f := store.LeaseFence{Owner: owner, Now: b.o.Clock.Now()}
	err = b.guard(ctx, tx, f, job)
	if err == nil {
		err = fn(tx, f)
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// guard touches the job row under the lease predicate: the job must still be ingesting and leased to us.
func (b *IngestBridge) guard(ctx context.Context, tx *sql.Tx, f store.LeaseFence, job JobRow) error {
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET updated_at = ? WHERE id = ? AND state = 'ingesting' AND lease_owner = ?`+f.And(), //nolint:gosec // G202: fixed C69 predicate
		append([]any{store.FormatTime(f.Now), job.ID, f.Owner}, f.Args()...)...)
	n, err := f.Fenced(res, err)
	if err != nil {
		return fmt.Errorf("jobs: ingest guard: %w", err)
	}
	if n == 0 {
		if cerr := f.Confirm(ctx, tx, 0); cerr != nil {
			return cerr
		}
		return errStale
	}
	return nil
}

type mutationDoc struct {
	Operator    string          `json:"operator"`
	Params      map[string]any  `json:"params"`
	RecipePatch json.RawMessage `json:"recipe_patch"`
	Rationale   *string         `json:"rationale"`
	PrincipalID string          `json:"principal_id"`
}

func (b *IngestBridge) newTake(ctx context.Context, job JobRow, att AttemptRow, out provider.Output, trackID, title, slug string, req recipeInfo,
	res ingest.Result, trunc [2]bool, byName map[string]upload.Received, now time.Time) (library.NewTake, error) {
	var m mutationDoc
	if err := json.Unmarshal([]byte(job.MutationJSON), &m); err != nil {
		return library.NewTake{}, fmt.Errorf("jobs: mutation of job %s: %w", job.ID, err)
	}
	parent, root, gen := "", "", 0
	if job.SourceTrackID != nil {
		parent = *job.SourceTrackID
		var g int
		if err := b.o.Store.R.QueryRowContext(ctx, `SELECT root_track_id, generation FROM tracks WHERE id = ?`, parent).Scan(&root, &g); err != nil {
			return library.NewTake{}, fmt.Errorf("jobs: mother of job %s: %w", job.ID, err)
		}
		gen = g + 1
	}
	si := res.Info.StreamInfo
	envID, err := b.envFor(ctx, att, out)
	if err != nil {
		return library.NewTake{}, err
	}
	principal := m.PrincipalID
	if principal == "" {
		principal = "system:job"
	}
	abc, sem := trunc[0], trunc[1]
	nt := library.NewTake{
		ID: trackID, Title: title, Slug: slug, RecipeID: job.RecipeID,
		ParentTrackID: parent, RootTrackID: root, Generation: gen,
		DurationS: res.Info.Duration(), SampleRate: int(si.SampleRate), BitsPerSample: int(si.BitsPerSample), Channels: int(si.Channels),
		TotalSamples: int64(si.TotalSamples), //nolint:gosec // FLAC totals are 36 bits
		MasterPath:   res.MasterRelPath, MasterSHA256: res.MasterSHA256, MasterBytes: res.MasterBytes,
		SourceSHA256: byName["audio.flac"].SHA256, FramesSHA256: res.FramesSHA256, AudioMD5: res.AudioMD5,
		TruncatedABC: &abc, TruncatedSemantic: &sem,
		ImportRule: "job", PrincipalID: principal, RecordedAt: now,
		Loudness: res.Loudness, Spectrum: res.Spectrum, ToolVersion: res.ToolVersion, ReplayGainDB: res.ReplayGainDB,
		Origin: "job", StatusSetBy: "system:job", JobID: job.ID, EnvID: envID,
	}
	for _, r := range []struct {
		kind string
		r    ingest.Rendition
	}{{"mp3_320", res.MP3}, {"peaks", res.Peaks}} {
		nt.Renditions = append(nt.Renditions, library.NewRendition{Kind: r.kind, SHA256: r.r.SHA256, Bytes: r.r.Bytes, Path: r.r.RelPath, Params: r.r.Params})
	}
	for _, a := range res.Artifacts {
		nt.Artifacts = append(nt.Artifacts, library.NewArtifact{Name: path.Base(a.RelPath), SHA256: a.SHA256, Path: a.RelPath, Bytes: a.Bytes})
	}
	if u, ok := byName["score.abc"]; ok {
		raw, err := b.o.Data.ReadFile(u.Path)
		if err != nil {
			return library.NewTake{}, &IngestFailedError{Code: ingest.CodeSourceChanged, Err: err}
		}
		nt.ScoreABC = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	}
	patch := ""
	if len(m.RecipePatch) > 0 && string(m.RecipePatch) != "null" {
		patch = string(m.RecipePatch)
	}
	rationale := ""
	if m.Rationale != nil {
		rationale = *m.Rationale
	}
	op := m.Operator
	if op == "" {
		op = job.Operator
	}
	nt.Mutation = &library.NewMutation{Operator: op, Rationale: rationale, Params: m.Params, JobID: job.ID, RecipePatchJSON: patch}
	return nt, nil
}

// envFor returns the id of the fingerprint of this attempt's environment (D-M3-26).
func (b *IngestBridge) envFor(ctx context.Context, att AttemptRow, out provider.Output) (string, error) {
	digest, err := imageDigestOf(ctx, b.o.Store.R, att.ImageDigest, att.WorkerKey, out.Env.WorkerBuild)
	if err != nil {
		return "", err
	}
	return EnvID(EnvInput{Engine: out.Env.Engine, PkgVersion: out.Env.PkgVersion, CodeCommit: out.Env.CodeCommit, Weights: out.Env.Weights,
		Decoder: out.Env.Decoder, Backend: b.backendOf(ctx, att.JobID), GPUClass: GPUClass(out.Env.GPUName), ImageDigest: digest})
}

func (b *IngestBridge) backendOf(ctx context.Context, jobID string) string {
	var be string
	_ = b.o.Store.R.QueryRowContext(ctx, `SELECT backend FROM jobs WHERE id = ?`, jobID).Scan(&be)
	return be
}

const envInsertSQL = `INSERT INTO env_fingerprints(env_id, engine, pkg_version, code_commit, weights_json, decoder, backend, gpu_class, image_digest, detail_json, first_seen_at)
	SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE `

const draftFulfilSQL = `UPDATE recipe_drafts SET status = 'fulfilled', fulfilled_by_track_id = ?, updated_by = 'system:job', updated_at = ?
	WHERE id = ? AND status = 'backlog'`

// commit writes the take, its environment, the RTF sample, the draft fulfilment and the upload
// consumption in one transaction under the lease predicate.
func (b *IngestBridge) commit(ctx context.Context, owner string, job JobRow, att AttemptRow, out provider.Output, take library.NewTake, byName map[string]upload.Received) error {
	return b.fencedTx(ctx, owner, job, func(tx *sql.Tx, f store.LeaseFence) error {
		digest, err := imageDigestOf(ctx, tx, att.ImageDigest, att.WorkerKey, out.Env.WorkerBuild)
		if err != nil {
			return err
		}
		gpu := GPUClass(out.Env.GPUName)
		weights, _ := json.Marshal(out.Env.Weights)
		if len(out.Env.Weights) == 0 {
			weights = []byte(`{}`)
		}
		detail, _ := json.Marshal(map[string]string{"gpu_name": out.Env.GPUName, "cuda": out.Env.CUDA, "torch": out.Env.Torch,
			"worker_build": out.Env.WorkerBuild, "python": out.Env.Python})
		args := []any{take.EnvID, out.Env.Engine, out.Env.PkgVersion, out.Env.CodeCommit, string(weights), out.Env.Decoder, b.backendIn(ctx, tx, job.ID), gpu, digest,
			string(detail), store.FormatTime(f.Now)}
		if _, err := tx.ExecContext(ctx, envInsertSQL+f.Where()+` ON CONFLICT(env_id) DO NOTHING`, append(args, f.Args()...)...); err != nil { //nolint:gosec // G202: fixed C69 predicate
			return fmt.Errorf("jobs: env fingerprint: %w", err)
		}
		if b.o.Spend != nil && gpu != "" {
			if e2e, ok := numberOf(out.Timings["e2e_seconds"]); ok && out.AudioSeconds != nil {
				if audio, err := out.AudioSeconds.Float64(); err == nil && audio > 0 {
					// the raw reported name: RecordRTF normalises it, so rows learned under it still match
					var cot string
					_ = tx.QueryRowContext(ctx, `SELECT cot FROM recipes WHERE id = ?`, job.RecipeID).Scan(&cot)
					if err := b.o.Spend.RecordRTF(ctx, tx, out.Env.GPUName, int64(e2e*1000+0.5), int64(audio*1000+0.5), cot == "full"); err != nil {
						return err
					}
				}
			}
		}
		if err := b.o.Library.InsertTake(ctx, tx, take); err != nil {
			return err
		}
		if job.DraftID != nil {
			if _, err := tx.ExecContext(ctx, draftFulfilSQL+f.And(), append([]any{take.ID, store.FormatTime(f.Now), *job.DraftID}, f.Args()...)...); err != nil { //nolint:gosec // G202: fixed C69 predicate
				return fmt.Errorf("jobs: fulfil draft: %w", err)
			}
		}
		ids := make([]string, 0, len(byName))
		for _, u := range byName {
			ids = append(ids, u.UploadID)
		}
		return b.o.Uploads.Consume(ctx, tx, ids)
	})
}

func (b *IngestBridge) backendIn(ctx context.Context, x q, jobID string) string {
	var be string
	_ = x.QueryRowContext(ctx, `SELECT backend FROM jobs WHERE id = ?`, jobID).Scan(&be)
	return be
}

func numberOf(n *json.Number) (float64, bool) {
	if n == nil {
		return 0, false
	}
	v, err := n.Float64()
	return v, err == nil && v >= 0
}

// analyse runs Ears Tier A after the take committed; failures are warnings recorded as a job event.
func (b *IngestBridge) analyse(ctx context.Context, owner string, job JobRow, trackID string) {
	if b.o.Ears == nil {
		return
	}
	warnings := 0
	if _, err := b.o.Ears.AnalyzeTrack(ctx, trackID, ears.AnalyzeOptions{}); err != nil {
		warnings++
		b.o.Log.Warn("ears analysis", "job", job.ID, "err", strings.TrimSpace(err.Error()))
	}
	if warnings == 0 {
		return
	}
	tx, err := b.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	f := store.LeaseFence{Owner: owner, Now: b.o.Clock.Now()}
	if err := insertEvent(ctx, tx, f, Event{JobID: job.ID, Name: EvEars, Detail: map[string]any{"warnings": warnings}}); err != nil {
		_ = tx.Rollback()
		return
	}
	_ = tx.Commit()
}

// sortedKeys is the keys of m in order, for deterministic iteration.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
