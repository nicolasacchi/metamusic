package librarytest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Fixed ids of the M3 rows (C86.2). Job ids equal take ids (jobs.id = the take id).
const (
	BatchDone     = "01900000-0000-7000-8000-000000000401" // finished + reconciled, one completed job
	TrackJobChild = "01900000-0000-7000-8000-000000000108" // the completed job's take (reroll_seed child of TrackLuna)
	JobDone       = TrackJobChild
	BatchAwaiting = "01900000-0000-7000-8000-000000000402" // agent batch awaiting confirmation, two jobs
	JobAwaiting1  = "01900000-0000-7000-8000-000000000412"
	JobAwaiting2  = "01900000-0000-7000-8000-000000000413"
	BatchFailed   = "01900000-0000-7000-8000-000000000403" // finished, one failed job (NO_CAPACITY)
	JobFailed     = "01900000-0000-7000-8000-000000000414"
	EnvFixture    = "env_fixture0000001"
	AgentKey      = "agent:fixture-token"
	humanKey      = "human:fixture"
)

// Money used by the fixture rows (the C72.1 golden example for one 216 s take at 690000 $/h).
const (
	fxTakeMicro     = 38525
	fxOverheadMicro = 38334
	fxMaxMicro      = fxTakeMicro + fxOverheadMicro
	fxExpectedMicro = 18400
)

// JobSeed describes one batch with one queued job for SeedJob. Name makes the ids unique and
// deterministic; the zero value of every other field is a sensible default.
type JobSeed struct {
	Name          string
	RecipeID      string // default: the first yue2 recipe in the library
	ParentTrackID string // jobs.source_track_id; "" = none
	Principal     string // batch owner key; default human:fixture
}

// SeededJob is what SeedJob created.
type SeededJob struct {
	EstimateID, BatchID, JobID, EnvID, RecipeID string
}

func seedID(kind, name string) string {
	h := sha256.Sum256([]byte(kind + "/" + name))
	return fmt.Sprintf("01900000-0000-7000-8000-%012x", h[:6])
}

func tsM3(t time.Time) string { return store.FormatTime(t) }

type batchSpec struct {
	id, estimateID, principal, kind, name, via, state string
	jobs                                              []string
	rationale                                         any
	created                                           time.Time
}

// ceilingFor returns the frozen quote numbers for n takes of the golden example.
func quoteFor(n int) (expected, maxMicro, retry, ceiling int64) {
	expected = int64(n)*fxExpectedMicro + fxOverheadMicro
	maxMicro = int64(n) * fxMaxMicro
	w := n
	if w > 2 {
		w = 2
	}
	retry = int64(w) * fxMaxMicro
	return expected, maxMicro, retry, maxMicro + retry
}

func insertEstimateAndBatch(tb testing.TB, st *store.Store, b batchSpec) {
	tb.Helper()
	n := len(b.jobs)
	expected, maxMicro, retry, ceiling := quoteFor(n)
	h := sha256.Sum256([]byte("token/" + b.id))
	needs := 0
	reasons := `[]`
	if b.state == "awaiting_confirmation" {
		needs, reasons = 1, `["takes_over_limit"]`
	}
	mustExec(tb, st, `INSERT INTO estimates(id, principal_id, principal_kind, engine, backend, items_json, recipe_hashes, n_takes, quote_json,
		expected_micro_usd, max_micro_usd, retry_allowance_micro_usd, ceiling_micro_usd, price_pph_micro_usd, workers_max, idle_s,
		requires_approval, approval_reasons, token_hash, created_at, expires_at, consumed_at, consumed_batch_id)
		VALUES (?,?,?,'yue2','fake','[]','[]',?,'{}',?,?,?,?,690000,2,20,?,?,?,?,?,?,?)`,
		b.estimateID, b.principal, b.kind, n, expected, maxMicro, retry, ceiling, needs, reasons, hex.EncodeToString(h[:]),
		tsM3(b.created.Add(-time.Minute)), tsM3(b.created.Add(9*time.Minute)), tsM3(b.created), b.id)
	fin := any(nil)
	if b.state != "active" && b.state != "awaiting_confirmation" {
		fin = tsM3(b.created.Add(10 * time.Minute))
	}
	mustExec(tb, st, `INSERT INTO batches(id, estimate_id, principal_id, principal_kind, principal_name, via, engine, backend, n_takes, rationale,
		expected_micro_usd, max_micro_usd, retry_allowance_micro_usd, ceiling_micro_usd, price_pph_micro_usd, attempt_overhead_s, kill_slack_s,
		workers_max, idle_s, state, created_at, finished_at) VALUES (?,?,?,?,?,?,'yue2','fake',?,?,?,?,?,?,690000,200,15,2,20,?,?,?)`,
		b.id, b.estimateID, b.principal, b.kind, b.name, b.via, n, b.rationale, expected, maxMicro, retry, ceiling, b.state, tsM3(b.created), fin)
	appr := `INSERT INTO approvals(batch_id, state, reasons, via, decided_by, decided_at, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?)`
	switch b.state {
	case "awaiting_confirmation":
		mustExec(tb, st, `INSERT INTO approvals(batch_id, state, reasons, created_at, expires_at) VALUES (?,'pending',?,?,?)`,
			b.id, reasons, tsM3(b.created), tsM3(b.created.Add(24*time.Hour)))
	default:
		via := "human_create"
		if b.kind == "agent" {
			via = "auto"
		}
		st2 := "approved"
		if via == "auto" {
			st2 = "auto"
		}
		mustExec(tb, st, appr, b.id, st2, `[]`, via, b.principal, tsM3(b.created), tsM3(b.created), tsM3(b.created.Add(24*time.Hour)))
	}
}

func insertEnv(tb testing.TB, st *store.Store, now time.Time) {
	tb.Helper()
	mustExec(tb, st, `INSERT OR IGNORE INTO env_fingerprints(env_id, engine, pkg_version, code_commit, weights_json, decoder, backend, gpu_class, image_digest, detail_json, first_seen_at)
		VALUES (?, 'yue2', '2.0.0', 'abcdef0', '{"fixture/stage1":"rev1","fixture/stage2":"rev2"}', 'rtn_v1', 'fake', 'RTX 4090', 'sha256:fixture', '{"gpu_name":"Fixture GPU","cuda":"12.8","torch":"2.10","worker_build":"fixture","python":"3.12"}', ?)`,
		EnvFixture, tsM3(now.Add(-time.Hour)))
}

type jobSpec struct {
	id, batchID, recipeID, parent, operator, title, state string
	index                                                 int
	created                                               time.Time
	mutation                                              string
}

func insertJob(tb testing.TB, st *store.Store, j jobSpec) {
	tb.Helper()
	initial := j.state
	if initial != "awaiting_confirmation" {
		initial = "queued"
	}
	queued := any(nil)
	if initial == "queued" {
		queued = tsM3(j.created)
	}
	mustExec(tb, st, `INSERT INTO jobs(id, batch_id, take_index, kind, engine, backend, recipe_id, source_track_id, operator, mutation_json, title,
		expected_audio_s, expected_s, timeout_s, expected_micro_usd, reserved_micro_usd, state, next_attempt_at, queued_at, created_at, updated_at)
		VALUES (?,?,?,'generate','yue2','fake',?,?,?,?,?,216,96,186,?,?,?,?,?,?,?)`,
		j.id, j.batchID, j.index, j.recipeID, nullStrArg(j.parent), j.operator, j.mutation, j.title, fxExpectedMicro, fxTakeMicro, initial,
		tsM3(j.created), queued, tsM3(j.created), tsM3(j.created))
}

func nullStrArg(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func firstYue2Recipe(tb testing.TB, st *store.Store) string {
	tb.Helper()
	var id string
	if err := st.R.QueryRow(`SELECT id FROM recipes WHERE engine = 'yue2' ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		tb.Fatal(err)
	}
	return id
}

// SeedJob inserts one batch (human_create, state active, approved) with one queued job and the
// environment fingerprint row, straight through SQL. library, jobs and spend tests build on it.
func SeedJob(tb testing.TB, st *store.Store, now time.Time, o JobSeed) SeededJob {
	tb.Helper()
	if o.Name == "" {
		o.Name = "seed"
	}
	if o.Principal == "" {
		o.Principal = humanKey
	}
	if o.RecipeID == "" {
		o.RecipeID = firstYue2Recipe(tb, st)
	}
	s := SeededJob{EstimateID: seedID("estimate", o.Name), BatchID: seedID("batch", o.Name), JobID: seedID("job", o.Name), EnvID: EnvFixture, RecipeID: o.RecipeID}
	kind := "human"
	if len(o.Principal) > 6 && o.Principal[:6] == "agent:" {
		kind = "agent"
	}
	insertEnv(tb, st, now)
	insertEstimateAndBatch(tb, st, batchSpec{id: s.BatchID, estimateID: s.EstimateID, principal: o.Principal, kind: kind, name: o.Name,
		via: map[string]string{"human": "human_create", "agent": "api"}[kind], state: "active", jobs: []string{s.JobID}, created: now})
	insertJob(tb, st, jobSpec{id: s.JobID, batchID: s.BatchID, recipeID: o.RecipeID, parent: o.ParentTrackID, operator: "reroll_seed",
		title: "Seed " + o.Name, state: "queued", created: now,
		mutation: `{"operator":"reroll_seed","params":{"mode":"new_seed"},"recipe_patch":null,"rationale":null,"principal_id":"` + o.Principal + `"}`})
	return s
}

func reserve(tb testing.TB, st *store.Store, at time.Time, kind string, micro int64, batch, job, attempt, reason, principal string) {
	tb.Helper()
	mustExec(tb, st, `INSERT INTO ledger(at, kind, micro_usd, batch_id, job_id, attempt_id, principal_id, reason) VALUES (?,?,?,?,?,?,?,?)`,
		tsM3(at), kind, micro, batch, nullStrArg(job), nullStrArg(attempt), principal, reason)
}

// addM3 inserts the M3 rows through the real write paths where one exists (the job take goes
// through library.InsertTake) and SQL elsewhere.
func addM3(tb testing.TB, st *store.Store, now time.Time, fx Fixtures) Fixtures {
	tb.Helper()
	ctx := context.Background()
	recipe := firstYue2Recipe(tb, st)
	insertEnv(tb, st, now)
	t0 := now.Add(-6 * time.Hour)

	// 1. A finished, reconciled batch with one completed reroll_seed job (child of the first track).
	insertEstimateAndBatch(tb, st, batchSpec{id: BatchDone, estimateID: seedID("estimate", "done"), principal: humanKey, kind: "human", name: "fixture",
		via: "human_create", state: "active", jobs: []string{JobDone}, created: t0})
	// the job's own recipe (a generated recipe, created_by = the owner)
	svc := library.New(st, nil, func() time.Time { return now })
	n := 0xa00
	svc.UseIDFunc(func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", n) })
	jobReq := mustParse(tb, reqJSON("fx-luna-s1102", "synth pop, 100 bpm", "[Verse 1]\nline one\n[Chorus]\nline two", 1102))
	tx := begin(tb, st)
	rid, _, err := svc.InsertRecipe(ctx, tx, jobReq, humanKey)
	check(tb, err)
	check(tb, tx.Commit())
	fx.Recipes = append(fx.Recipes, rid)
	insertJob(tb, st, jobSpec{id: JobDone, batchID: BatchDone, recipeID: rid, parent: TrackLuna, operator: "reroll_seed", title: "Luna · s1102", state: "queued", created: t0,
		mutation: `{"operator":"reroll_seed","params":{"mode":"new_seed","seed_policy":"auto","n_seeds":1,"via":null},"recipe_patch":null,"rationale":null,"principal_id":"` + humanKey + `"}`})
	attempt := seedID("attempt", "done")
	mustExec(tb, st, `INSERT INTO job_attempts(id, job_id, n, backend, state, ref, submitted_at, window_ends_at, price_pph_micro_usd, image_digest, remote_status,
		in_progress_seen, execution_ms, terminal_basis, terminal_at, settled, created_at)
		VALUES (?,?,1,'fake','done','fixture-ref-0001',?,?,690000,'sha256:fixture','COMPLETED',1,95040,'provider',?,1,?)`,
		attempt, JobDone, tsM3(t0.Add(time.Second)), tsM3(t0.Add(30*time.Minute)), tsM3(t0.Add(5*time.Minute)), tsM3(t0.Add(time.Second)))
	for i, to := range []string{"submitting", "starting", "generating", "transferring", "ingesting"} {
		mustExec(tb, st, `UPDATE jobs SET state = ?, updated_at = ? WHERE id = ?`, to, tsM3(t0.Add(time.Duration(i+1)*time.Minute)), JobDone)
	}
	master, mp3, peaks := fixtureJobFiles()
	tx = begin(tb, st)
	nt := library.NewTake{
		ID: TrackJobChild, Title: "Luna · s1102", Slug: "luna_s1102", RecipeID: rid, ParentTrackID: TrackLuna, RootTrackID: TrackLuna, Generation: 1,
		DurationS: 6, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 6 * 48000,
		MasterPath: master.path, MasterSHA256: sha(master.data), MasterBytes: int64(len(master.data)),
		SourceSHA256: sha([]byte("fixture-job-source")), FramesSHA256: sha([]byte("fixture-job-frames")), AudioMD5: fmt.Sprintf("%032x", 0x300),
		ImportRule: "job", Origin: "job", StatusSetBy: "system:job", JobID: JobDone, EnvID: EnvFixture, PrincipalID: humanKey, RecordedAt: t0.Add(5 * time.Minute),
		Renditions: []library.NewRendition{
			{Kind: "mp3_320", SHA256: sha(mp3.data), Bytes: int64(len(mp3.data)), Path: mp3.path, Params: map[string]any{"encoder": "libmp3lame", "bitrate": "320k", "sample_rate": 48000, "gain_db": -0.6, "ffmpeg": "n9.0.1"}},
			{Kind: "peaks", SHA256: sha(peaks.data), Bytes: int64(len(peaks.data)), Path: peaks.path, Params: map[string]any{"format": analysis.PeaksFormat}},
		},
		Loudness:    analysis.Loudness{IntegratedLUFS: -14, LRA: 5, LRALowLUFS: -19, LRAHighLUFS: -13, TruePeakDBTP: -1.2, SamplePeakDBFS: -1.5, ShortTermMaxLUFS: -12, ShortTerm1Hz: []float64{-14, -13.5}},
		Spectrum:    analysis.Spectrum{BandwidthHz: bptrInt(16000), RefDB: -40, ThresholdDB: -110, Frames: 30},
		ToolVersion: "ffmpeg n9.0.1", ReplayGainDB: -4,
		Mutation: &library.NewMutation{Operator: "reroll_seed", JobID: JobDone, Params: map[string]any{"mode": "new_seed", "seed_policy": "auto", "n_seeds": 1}},
	}
	check(tb, svc.InsertTake(ctx, tx, nt))
	check(tb, tx.Commit())
	fx.Tracks = append(fx.Tracks, TrackJobChild)
	mustExec(tb, st, `UPDATE jobs SET state = 'completed', outcome = 'clean', track_id = ?, env_id = ?, audio_s = 216, started_at = ?, finished_at = ?, updated_at = ? WHERE id = ?`,
		TrackJobChild, EnvFixture, tsM3(t0.Add(time.Minute)), tsM3(t0.Add(6*time.Minute)), tsM3(t0.Add(6*time.Minute)), JobDone)
	// ledger: reserves at create and claim, settlements when the attempt ended and the batch finished, residual, reconciliation.
	reserve(tb, st, t0, "reserve", fxTakeMicro, BatchDone, JobDone, "", "take", humanKey)
	reserve(tb, st, t0.Add(time.Second), "reserve", fxOverheadMicro, BatchDone, JobDone, attempt, "attempt_overhead", humanKey)
	reserve(tb, st, t0.Add(5*time.Minute), "settle", 18216, BatchDone, JobDone, attempt, "execution", humanKey)
	reserve(tb, st, t0.Add(6*time.Minute), "settle", 12000, BatchDone, "", "", "overhead", humanKey)
	mustExec(tb, st, `UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, tsM3(t0.Add(6*time.Minute)), BatchDone)
	outstanding := int64(fxMaxMicro - 18216 - 12000)
	reserve(tb, st, t0.Add(6*time.Minute), "residual", outstanding, BatchDone, "", "", "held", humanKey)
	win := now.Add(-4 * time.Hour).Truncate(time.Hour)
	reserve(tb, st, t0.Add(2*time.Hour), "release", outstanding, BatchDone, "", "", "reconciled", humanKey)
	mustExec(tb, st, `INSERT INTO ledger(at, kind, micro_usd, window_start, principal_id, reason) VALUES (?,?,?,?,?,?)`,
		tsM3(t0.Add(2*time.Hour)), "bill", -216, tsM3(win), "system:billing", "reconcile")
	mustExec(tb, st, `UPDATE batches SET reconciled_at = ? WHERE id = ?`, tsM3(t0.Add(2*time.Hour)), BatchDone)
	mustExec(tb, st, `INSERT INTO billing_buckets(bucket_start, fetched_at, total_micro_usd, compute_micro_usd, disk_micro_usd) VALUES (?,?,?,?,?)`,
		tsM3(win), tsM3(t0.Add(2*time.Hour)), 30000, 30000, 0)
	mustExec(tb, st, `INSERT INTO billing_windows(window_start, window_end, granularity, buckets, billed_micro_usd, billed_compute_micro_usd, provisional_micro_usd,
		reserved_micro_usd, batches, verdict, fetched_at) VALUES (?,?,'hour',1,30000,30000,30216,?,1,'ok',?)`,
		tsM3(win), tsM3(win.Add(time.Hour)), fxMaxMicro, tsM3(t0.Add(2*time.Hour)))

	// 2. An agent batch awaiting confirmation (two jobs, approval pending, only the take reserves written).
	tAw := now.Add(-30 * time.Minute)
	insertEstimateAndBatch(tb, st, batchSpec{id: BatchAwaiting, estimateID: seedID("estimate", "awaiting"), principal: AgentKey, kind: "agent", name: "fixture-agent",
		via: "api", state: "awaiting_confirmation", jobs: []string{JobAwaiting1, JobAwaiting2}, created: tAw, rationale: "try two seeds"})
	for i, j := range []string{JobAwaiting1, JobAwaiting2} {
		insertJob(tb, st, jobSpec{id: j, batchID: BatchAwaiting, recipeID: recipe, parent: TrackCitta, operator: "reroll_seed", index: i,
			title: fmt.Sprintf("Citta · s%d", 2000+i), state: "awaiting_confirmation", created: tAw,
			mutation: `{"operator":"reroll_seed","params":{"mode":"new_seed"},"recipe_patch":null,"rationale":"try two seeds","principal_id":"` + AgentKey + `"}`})
		reserve(tb, st, tAw, "reserve", fxTakeMicro, BatchAwaiting, j, "", "take", AgentKey)
	}

	// 3. A finished batch whose only job failed with NO_CAPACITY (released never_ran).
	tF := now.Add(-3 * time.Hour)
	insertEstimateAndBatch(tb, st, batchSpec{id: BatchFailed, estimateID: seedID("estimate", "failed"), principal: humanKey, kind: "human", name: "fixture",
		via: "human_create", state: "active", jobs: []string{JobFailed}, created: tF})
	insertJob(tb, st, jobSpec{id: JobFailed, batchID: BatchFailed, recipeID: recipe, parent: TrackCitta, operator: "reroll_seed", title: "Citta · s3000", state: "queued", created: tF,
		mutation: `{"operator":"reroll_seed","params":{"mode":"new_seed"},"recipe_patch":null,"rationale":null,"principal_id":"` + humanKey + `"}`})
	reserve(tb, st, tF, "reserve", fxTakeMicro, BatchFailed, JobFailed, "", "take", humanKey)
	mustExec(tb, st, `UPDATE jobs SET state = 'failed', error_code = 'NO_CAPACITY', finished_at = ?, updated_at = ? WHERE id = ?`,
		tsM3(tF.Add(24*time.Hour-time.Hour)), tsM3(tF.Add(24*time.Hour-time.Hour)), JobFailed)
	reserve(tb, st, tF.Add(23*time.Hour), "release", fxTakeMicro, BatchFailed, JobFailed, "", "never_ran", humanKey)
	mustExec(tb, st, `UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, tsM3(tF.Add(23*time.Hour)), BatchFailed)

	// provider state: the fake backend, armed.
	mustExec(tb, st, `INSERT OR IGNORE INTO provider_state(backend, armed, changed_by, changed_at) VALUES ('fake', 1, 'system:default', ?)`, tsM3(now.Add(-24*time.Hour)))
	return fx
}

func fixtureJobFiles() (master, mp3, peaks fxFile) {
	m := []byte("fixture-job-master")
	p := []byte("fixture-job-mp3")
	ps := sha(p)
	mp3 = fxFile{fmt.Sprintf("derived/%s/%s.mp3", ps[:2], ps), p}
	pk := []byte(`{"fixture":"job-peaks"}` + "\n")
	ks := sha(pk)
	peaks = fxFile{fmt.Sprintf("derived/%s/%s.peaks.json", ks[:2], ks), pk}
	master = fxFile{"audio/masters/luna_s1102__1102__20261006-0600.flac", m}
	return
}

func writeJobFiles(tb testing.TB, dataDir string) {
	tb.Helper()
	m, p, k := fixtureJobFiles()
	for _, f := range []fxFile{m, p, k} {
		full := filepath.Join(dataDir, f.path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(full, f.data, 0o644); err != nil {
			tb.Fatal(err)
		}
	}
}

// ApplyM3 adds the M3 rows to a library that already holds FixtureM2c's rows (servertest uses it).
func ApplyM3(tb testing.TB, st *store.Store, now time.Time) {
	tb.Helper()
	addM3(tb, st, now, Fixtures{})
}
