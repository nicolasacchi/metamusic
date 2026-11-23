package desk_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/desk"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// planScoreText is a two-voice yue2 score with the three sections of the fixture take's lyrics.
const planScoreText = "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\nV:1\n% verse\nCDEF|CDEF|\n% chorus\nCDEF|CDEF|\n% outro\nCDEF|CDEF|]\nV:2\n% verse\nC,4|G,4|\n% chorus\nC,4|G,4|\n% outro\nC,4|G,4|]\n"

// member is one tar member of a test bundle.
type member struct {
	name string
	body string
	typ  byte
	link string
}

func reg(name, body string) member { return member{name: name, body: body, typ: tar.TypeReg} }

func tarOf(t testing.TB, ms ...member) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, m := range ms {
		h := &tar.Header{Name: m.name, Mode: 0o644, Typeflag: m.typ, Linkname: m.link}
		if m.typ == tar.TypeReg {
			h.Size = int64(len(m.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if m.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(m.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func metaJSON(seed int64, cot string, truncated bool) string {
	return fmt.Sprintf(`{"seed":%d,"cot":%q,"truncated":%v,"abc_tokens":120,"seconds":2.5,"yue2":"test"}`, seed, cot, truncated)
}

// goodBundle is the bundle of a valid plan of the given seed.
func goodBundle(t testing.TB, seed int64) []byte {
	t.Helper()
	return tarOf(t, reg("score.abc", planScoreText), reg("plan.json", metaJSON(seed, "full", false)))
}

// planJob is a plan job and its recipe's request, ready to be ingested.
type planJob struct {
	id    string
	seeds []int64
	row   jobs.JobRow
}

var planJobCount atomic.Int64

// planJob inserts a queued plan job of the fixture take's recipe with the given seeds, and the runner lease its fence needs.
func (e *env) planJob(seeds ...int64) planJob {
	e.t.Helper()
	planJobN := int(planJobCount.Add(1))
	id := fmt.Sprintf("01900000-0000-7000-9000-%012d", planJobN)
	spec, _ := json.Marshal(map[string]any{"plan": map[string]any{"seeds": seeds, "seconds_per_seed": 11}})
	var recipe string
	if err := e.st.R.QueryRow(`SELECT recipe_id FROM tracks WHERE id = ?`, lt.TrackLuna).Scan(&recipe); err != nil {
		e.t.Fatal(err)
	}
	ts := store.FormatTime(epoch)
	e.exec(`INSERT INTO jobs(id, batch_id, take_index, kind, engine, backend, recipe_id, source_track_id, operator, mutation_json, title,
		expected_audio_s, expected_s, timeout_s, expected_micro_usd, reserved_micro_usd, state, next_attempt_at, queued_at, created_at, updated_at, needs_json, spec_json)
		VALUES (?,?,?,'plan','yue2','fake',?,?, 'plan_seeds', '{}', 'Luna Park · plans ×2', 200, 100, 600, 10, 20, 'queued', ?,?,?,?, '["plan","v2"]', ?)`,
		id, lt.BatchDone, 100+planJobN, recipe, lt.TrackLuna, ts, ts, ts, ts, string(spec))
	e.exec(`INSERT INTO runner_lease(id, owner, acquired_at, heartbeat_at, expires_at) VALUES (1, 'runner-1', ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET expires_at = excluded.expires_at`, ts, ts, store.FormatTime(epoch.Add(24*time.Hour)))
	return planJob{id: id, seeds: seeds, row: jobs.JobRow{ID: id, BatchID: lt.BatchDone, RecipeID: recipe, Operator: "plan_seeds", Title: "Luna Park · plans ×2",
		State: "ingesting", SourceTrackID: strp(lt.TrackLuna), Kind: jobs.KindPlan, Needs: []string{"plan", "v2"}, SpecJSON: string(spec)}}
}

func strp(s string) *string { return &s }

// bundleIn is what the worker uploaded for a seed: nil raw means the seed reported status error.
type bundleIn struct {
	raw       []byte
	truncated bool
}

// ingest runs IngestKind for job in a transaction that is committed when the call succeeds. mod may change the input first.
func (e *env) ingest(j planJob, bundles []bundleIn, mod ...func(*jobs.KindIngest)) (outcome string, track *string, err error) {
	e.t.Helper()
	tx, outcome, track, err := e.ingestTx(j, bundles, mod...)
	if err != nil {
		_ = tx.Rollback()
		return "", nil, err
	}
	if cerr := tx.Commit(); cerr != nil {
		e.t.Fatal(cerr)
	}
	return outcome, track, nil
}

// ingestTx is ingest without the commit: the caller finishes the transaction.
func (e *env) ingestTx(j planJob, bundles []bundleIn, mod ...func(*jobs.KindIngest)) (*sql.Tx, string, *string, error) {
	e.t.Helper()
	in := jobs.KindIngest{Job: j.row, Attempt: jobs.AttemptRow{ID: ""}, Fence: store.LeaseFence{Owner: "runner-1", Now: epoch},
		Output: provider.Output{Contract: provider.ContractV2, TakeID: j.id, Status: "ok", Kind: provider.KindPlan, Artifacts: map[string]provider.ArtifactDigest{}}}
	for i, b := range bundles {
		po := provider.PlanOut{Index: i, Seed: j.seeds[i], Status: "error", Code: "PLAN_FAILED"}
		if b.raw != nil {
			name := provider.PlanBundleName(i)
			rel := fmt.Sprintf("incoming/%s/%s", j.id, name)
			if err := e.root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
				e.t.Fatal(err)
			}
			if err := e.root.WriteFile(rel, b.raw, 0o644); err != nil {
				e.t.Fatal(err)
			}
			sum := sha256.Sum256(b.raw)
			sha := hex.EncodeToString(sum[:])
			in.Files = append(in.Files, upload.Received{UploadID: "u" + name, Artifact: name, SHA256: sha, Bytes: int64(len(b.raw)), Path: rel})
			in.Output.Artifacts[name] = provider.ArtifactDigest{SHA256: sha, Bytes: int64(len(b.raw))}
			po.Status, po.Code, po.Bundle = "ok", "", name
			if b.truncated {
				po.Truncated = map[string]bool{"abc": true, "semantic": false}
			}
		}
		in.Output.Plans = append(in.Output.Plans, po)
	}
	for _, m := range mod {
		m(&in)
	}
	tx, err := e.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	track, outcome, err := e.svc.IngestKind(context.Background(), tx, in)
	return tx, outcome, track, err
}

func (e *env) planDirs() []string {
	e.t.Helper()
	ents, err := os.ReadDir(filepath.Join(e.dir, "plans"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, en := range ents {
		out = append(out, en.Name())
	}
	return out
}

func violation(t testing.TB, err error, code string) {
	t.Helper()
	var cv *jobs.ContractViolation
	if !errors.As(err, &cv) || cv.Code != code {
		t.Fatalf("error = %v, want contract violation %s", err, code)
	}
}

func TestIngestPlans_HappyPath(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	j := e.planJob(11, 12)
	outcome, track, err := e.ingest(j, []bundleIn{{raw: goodBundle(t, 11)}, {raw: goodBundle(t, 12)}})
	if err != nil || outcome != "clean" || track != nil {
		t.Fatalf("ingest: %q %v %v", outcome, track, err)
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plans WHERE job_id = ?`, j.id); n != "2" {
		t.Fatalf("%s plans", n)
	}
	var id string
	if err := e.st.R.QueryRow(`SELECT id FROM desk_plans WHERE job_id = ? AND seed_index = 1`, j.id).Scan(&id); err != nil {
		t.Fatal(err)
	}
	pl, err := e.svc.Get(context.Background(), alice, id, desk.DefaultInclude)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Source != "job" || pl.Seed != 12 || pl.Cot != "full" || pl.Bars != 6 || pl.Meter != "4/4" || !pl.Score.Aligned || len(pl.Sections) != 3 ||
		pl.JobID == nil || *pl.JobID != j.id || pl.Base.TrackID == nil || *pl.Base.TrackID != lt.TrackLuna || pl.TrackID != nil ||
		pl.SecondsPerBar == nil || *pl.SecondsPerBar != 2 || pl.ExpectedAudioS != 12 || pl.Score.ABCTokens == nil || *pl.Score.ABCTokens != 120 ||
		pl.Truncated == nil || *pl.Truncated || pl.ABC == nil || *pl.ABC != planScoreText || pl.Title != "Luna Park · plan s12" || pl.Base.Title != "Luna Park" {
		t.Errorf("plan = %+v", pl)
	}
	if pl.CreatedBy.ID != "human:fixture" || pl.CreatedBy.Kind != "human" { // the batch's requester
		t.Errorf("created by %+v", pl.CreatedBy)
	}
	// only the two members of the bundle are rows and files, frozen
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plan_files WHERE plan_id = ?`, id); n != "2" {
		t.Errorf("%s files", n)
	}
	fi, err := os.Stat(filepath.Join(e.dir, "plans", id, "plan.json"))
	if err != nil || fi.Mode().Perm() != 0o444 {
		t.Errorf("plan.json: %v %v", fi, err)
	}
	if fs, err := desk.Verify(context.Background(), e.st.R, e.root); err != nil || len(fs) != 0 {
		t.Errorf("verify: %v %v", fs, err)
	}
	// a plan job writes no track, recipe or mutation
	if n := e.scalar(`SELECT track_id FROM jobs WHERE id = ?`, j.id); n != "" {
		t.Errorf("job track %q", n)
	}
}

func TestIngestPlans_OptionalMembersAreStored(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	j := e.planJob(11)
	raw := tarOf(t, reg("score.abc", planScoreText), reg("plan.json", metaJSON(11, "full", false)), reg("abc_tokens.npy", "\x93NUMPYxx"),
		reg("prefix.npy", "\x93NUMPYyy"), reg("plan_manifest.json", `{"files":["plan.json"]}`))
	if _, _, err := e.ingest(j, []bundleIn{{raw: raw}}); err != nil {
		t.Fatal(err)
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plan_files`); n != "5" {
		t.Errorf("%s files", n)
	}
	if fs, err := desk.Verify(context.Background(), e.st.R, e.root); err != nil || len(fs) != 0 {
		t.Errorf("verify: %v %v", fs, err)
	}
}

func TestIngestPlans_BundleRefusals_Table(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("%", 64<<10+1)
	meta := metaJSON(11, "full", false)
	cases := []struct {
		name string
		raw  []byte
		rule string
	}{
		{"symlink", tarOf(t, reg("plan.json", meta), member{name: "score.abc", typ: tar.TypeSymlink, link: "/etc/passwd"}), desk.RuleBundleType},
		{"dir member", tarOf(t, member{name: "sub/", typ: tar.TypeDir}, reg("score.abc", planScoreText), reg("plan.json", meta)), desk.RuleBundleType},
		{"path traversal", tarOf(t, reg("../x", "x"), reg("score.abc", planScoreText), reg("plan.json", meta)), desk.RuleBundleName},
		{"absolute name", tarOf(t, reg("/etc/x", "x"), reg("score.abc", planScoreText), reg("plan.json", meta)), desk.RuleBundleName},
		{"nested name", tarOf(t, reg("sub/score.abc", planScoreText), reg("plan.json", meta)), desk.RuleBundleName},
		{"duplicate", tarOf(t, reg("score.abc", planScoreText), reg("score.abc", planScoreText), reg("plan.json", meta)), desk.RuleBundleDup},
		{"duplicate after five members", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", meta), reg("prefix.npy", "\x93NUMPY"), reg("abc_tokens.npy", "\x93NUMPY"), reg("plan_manifest.json", "{}"), reg("score.abc", planScoreText)), desk.RuleBundleDup},
		{"sixth member a symlink", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", meta), reg("prefix.npy", "\x93NUMPY"), reg("abc_tokens.npy", "\x93NUMPY"), reg("plan_manifest.json", "{}"), member{name: "x", typ: tar.TypeSymlink, link: "/etc/passwd"}), desk.RuleBundleType},
		{"oversize score", tarOf(t, reg("score.abc", big), reg("plan.json", meta)), desk.RuleBundleSize},
		{"oversize npy", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", meta), reg("prefix.npy", "\x93NUMPY"+strings.Repeat("0", 1<<20))), desk.RuleBundleSize},
		{"missing score", tarOf(t, reg("plan.json", meta)), desk.RuleBundleMissing},
		{"missing plan.json", tarOf(t, reg("score.abc", planScoreText)), desk.RuleBundleMissing},
		{"extra name", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", meta), reg("notes.txt", "x")), desk.RuleBundleName},
		{"empty member", tarOf(t, reg("score.abc", ""), reg("plan.json", meta)), desk.RuleBundleEmpty},
		{"invalid UTF-8", tarOf(t, reg("score.abc", "X:1\nK:C\n\xff\xfe|\n"), reg("plan.json", meta)), desk.RuleBundleUTF8},
		{"score does not parse", tarOf(t, reg("score.abc", "X:1\nK:C\nCD&EF|\n"), reg("plan.json", meta)), desk.RuleBundleScore},
		{"plan.json not json", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", "nope")), desk.RuleBundlePlanJSON},
		{"plan.json extra key", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", strings.Replace(meta, `"yue2"`, `"extra":1,"yue2"`, 1))), desk.RuleBundlePlanJSON},
		{"plan.json missing key", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", `{"seed":11,"cot":"full","truncated":false,"abc_tokens":null,"seconds":1}`)), desk.RuleBundlePlanJSON},
		{"plan.json truncated not bool", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", strings.Replace(meta, `false`, `null`, 1))), desk.RuleBundlePlanJSON},
		{"plan.json cot off", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", metaJSON(11, "off", false))), desk.RuleBundlePlanJSON},
		{"plan.json negative seconds", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", strings.Replace(meta, `2.5`, `-1`, 1))), desk.RuleBundlePlanJSON},
		{"plan.json trailing data", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", meta+"{}")), desk.RuleBundlePlanJSON},
		{"other seed", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", metaJSON(99, "full", false))), desk.RuleBundleMismatch},
		{"other cot", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", metaJSON(11, "melody", false))), desk.RuleBundleMismatch},
		{"npy is not numpy", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", meta), reg("abc_tokens.npy", "hello")), desk.RuleBundleNPY},
		{"manifest not an object", tarOf(t, reg("score.abc", planScoreText), reg("plan.json", meta), reg("plan_manifest.json", "[1]")), desk.RuleBundleManifest},
		{"not a tar", []byte(strings.Repeat("garbage!", 100)), desk.RuleBundleTar},
		{"truncated tar", goodBundle(t, 11)[:600], desk.RuleBundleTar},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			j := e.planJob(11, 12)
			outcome, _, err := e.ingest(j, []bundleIn{{raw: c.raw}, {raw: goodBundle(t, 12)}})
			if err != nil || outcome != "clean" {
				t.Fatalf("the good seed must go on: %q %v", outcome, err)
			}
			if n := e.scalar(`SELECT COUNT(*) FROM desk_plans`); n != "1" {
				t.Errorf("%s plans stored", n)
			}
			if got := e.scalar(`SELECT seed_index FROM desk_plans`); got != "1" {
				t.Errorf("stored seed index %q", got)
			}
			if dirs := e.planDirs(); len(dirs) != 1 {
				t.Errorf("plan dirs %v", dirs)
			}
			ev := e.scalar(`SELECT detail_json FROM job_events WHERE job_id = ? AND event = ?`, j.id, desk.EvPlanBundleInvalid)
			var d struct {
				Index int
				Seed  int64
				Rule  string
			}
			if err := json.Unmarshal([]byte(ev), &d); err != nil || d.Index != 0 || d.Seed != 11 || d.Rule != c.rule {
				t.Errorf("event detail %s (%v), want rule %s", ev, err, c.rule)
			}
			if code := e.scalar(`SELECT code FROM job_events WHERE job_id = ?`, j.id); code != desk.CodePlanBundleInvalid {
				t.Errorf("event code %q", code)
			}
		})
	}
}

func TestIngestPlans_PartialSeeds(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	j := e.planJob(11, 12, 13)
	outcome, _, err := e.ingest(j, []bundleIn{{}, {raw: goodBundle(t, 12)}, {}})
	if err != nil || outcome != "clean" {
		t.Fatalf("%q %v", outcome, err)
	}
	if got := e.scalar(`SELECT group_concat(seed_index || ':' || seed) FROM desk_plans`); got != "1:12" {
		t.Errorf("stored %q", got)
	}
	if n := e.scalar(`SELECT COUNT(*) FROM job_events`); n != "0" {
		t.Errorf("a worker-reported failure writes no refusal event: %s", n)
	}
}

func TestIngestPlans_TruncatedOutcome(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	j := e.planJob(11, 12)
	outcome, _, err := e.ingest(j, []bundleIn{{raw: goodBundle(t, 11)}, {raw: tarOf(t, reg("score.abc", planScoreText), reg("plan.json", metaJSON(12, "full", true))), truncated: true}})
	if err != nil || outcome != "truncated" {
		t.Fatalf("%q %v", outcome, err)
	}
	if got := e.scalar(`SELECT group_concat(truncated) FROM (SELECT truncated FROM desk_plans ORDER BY seed_index)`); got != "0,1" {
		t.Errorf("truncated flags %q", got)
	}
}

func TestIngestPlans_AllFailedIsPlansFailed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	j := e.planJob(11, 12)
	_, _, err := e.ingest(j, []bundleIn{{}, {}})
	violation(t, err, desk.CodePlansFailed)
	// every bundle refused is the same
	j = e.planJob(11)
	_, _, err = e.ingest(j, []bundleIn{{raw: tarOf(t, reg("plan.json", metaJSON(11, "full", false)))}})
	violation(t, err, desk.CodePlansFailed)
	if !strings.Contains(err.Error(), "0:"+desk.RuleBundleMissing) {
		t.Errorf("the failed job must keep the refused rule: %v", err)
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plans`); n != "0" {
		t.Errorf("%s plans", n)
	}
	if dirs := e.planDirs(); len(dirs) != 0 {
		t.Errorf("dirs %v", dirs)
	}
}

func TestIngestPlans_RequestPerSeed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	j := e.planJob(11, 12)
	if _, _, err := e.ingest(j, []bundleIn{{raw: goodBundle(t, 11)}, {raw: goodBundle(t, 12)}}); err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal([]byte(e.scalar(`SELECT request_json FROM recipes WHERE id = ?`, j.row.RecipeID)), &base); err != nil {
		t.Fatal(err)
	}
	for i, seed := range []int64{11, 12} {
		var req map[string]any
		if err := json.Unmarshal([]byte(e.scalar(`SELECT request_json FROM desk_plans WHERE seed_index = ?`, i)), &req); err != nil {
			t.Fatal(err)
		}
		if _, hasABC := req["abc"]; hasABC || req["seed"] != float64(seed) || req["cot"] != "full" || req["lyrics"] != base["lyrics"] || req["style"] != base["style"] || req["id"] != base["id"] {
			t.Errorf("seed %d: request %v (recipe %v)", seed, req, base)
		}
	}
	// the recipe the job points at is not changed
	if got := e.scalar(`SELECT seed FROM recipes WHERE id = ?`, j.row.RecipeID); got != "101" {
		t.Errorf("recipe seed %s", got)
	}
}

// TestIngestPlans_RollbackRemovesDirs: a failure inside the call removes what it placed at once; after a rollback that happens
// later the next attempt of the job removes the stale directories, and the startup sweep removes any that remain.
func TestIngestPlans_RollbackRemovesDirs(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// 1. the second seed contradicts its upload: the whole job is a violation and the first seed's directory is gone again
	j := e.planJob(11, 12)
	_, _, err := e.ingest(j, []bundleIn{{raw: goodBundle(t, 11)}, {raw: goodBundle(t, 12)}}, func(in *jobs.KindIngest) {
		d := in.Output.Artifacts["plan-01.tar"]
		d.SHA256 = strings.Repeat("0", 64)
		in.Output.Artifacts["plan-01.tar"] = d
	})
	violation(t, err, "ARTIFACT_MISMATCH")
	if dirs := e.planDirs(); len(dirs) != 0 {
		t.Fatalf("dirs after a violation: %v", dirs)
	}
	// 2. a later rollback leaves the directory; the next attempt of the same job removes it
	j = e.planJob(21)
	tx, _, _, err := e.ingestTx(j, []bundleIn{{raw: goodBundle(t, 21)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if dirs := e.planDirs(); len(dirs) != 1 {
		t.Fatalf("dirs after a rollback: %v", dirs)
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plans`); n != "0" {
		t.Fatalf("%s plans after a rollback", n)
	}
	if _, _, err := e.ingest(j, []bundleIn{{raw: goodBundle(t, 21)}}); err != nil {
		t.Fatal(err)
	}
	dirs := e.planDirs()
	if len(dirs) != 1 || e.scalar(`SELECT COUNT(*) FROM desk_plans WHERE id = ?`, dirs[0]) != "1" {
		t.Errorf("the retry left %v", dirs)
	}
	// AfterKindCommit forgets the placed directories: a later attempt never removes a committed plan
	e.svc.AfterKindCommit(context.Background(), jobs.KindIngest{Job: j.row})
	if _, _, err := e.ingest(j, []bundleIn{{raw: goodBundle(t, 21)}}); err == nil {
		t.Error("a second ingest of the same job stored the plan again")
	}
	if dirs := e.planDirs(); len(dirs) != 1 {
		t.Errorf("committed plan dir lost: %v", dirs)
	}
	// 3. the sweep removes an orphan the retry never came for
	j = e.planJob(31)
	tx, _, _, err = e.ingestTx(j, []bundleIn{{raw: goodBundle(t, 31)}})
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if len(e.planDirs()) != 2 {
		t.Fatalf("dirs %v", e.planDirs())
	}
	e.advance(24 * 365 * time.Hour) // the files carry the real clock; the service's clock is the fixture's
	if n, err := e.svc.Sweep(context.Background()); err != nil || n != 1 {
		t.Errorf("sweep removed %d (%v)", n, err)
	}
}

func TestIngestPlans_UploadMustMatchItsReport(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*testing.T, *env, *jobs.KindIngest){
		"report sha": func(_ *testing.T, _ *env, in *jobs.KindIngest) {
			in.Output.Artifacts["plan-00.tar"] = provider.ArtifactDigest{SHA256: strings.Repeat("a", 64), Bytes: in.Files[0].Bytes}
		},
		"report bytes": func(_ *testing.T, _ *env, in *jobs.KindIngest) {
			in.Output.Artifacts["plan-00.tar"] = provider.ArtifactDigest{SHA256: in.Files[0].SHA256, Bytes: 1}
		},
		"received sha":   func(_ *testing.T, _ *env, in *jobs.KindIngest) { in.Files[0].SHA256 = strings.Repeat("b", 64) },
		"received size":  func(_ *testing.T, _ *env, in *jobs.KindIngest) { in.Files[0].Bytes++ },
		"over the limit": func(_ *testing.T, _ *env, in *jobs.KindIngest) { in.Files[0].Bytes = provider.MaxPlanBundleBytes + 1 },
		"file changed": func(t *testing.T, e *env, in *jobs.KindIngest) {
			if err := e.root.WriteFile(in.Files[0].Path, []byte("tampered after the upload was verified"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"unreported file":  func(_ *testing.T, _ *env, in *jobs.KindIngest) { delete(in.Output.Artifacts, "plan-00.tar") },
		"bundle not named": func(_ *testing.T, _ *env, in *jobs.KindIngest) { in.Output.Plans[0].Bundle = "plan-05.tar" },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			j := e.planJob(11)
			run := func(in *jobs.KindIngest) { mod(t, e, in) }
			_, _, err := e.ingest(j, []bundleIn{{raw: goodBundle(t, 11)}}, run)
			if err == nil {
				t.Fatal("ingested")
			}
			var cv *jobs.ContractViolation
			if !errors.As(err, &cv) {
				t.Fatalf("error %v is not a contract violation", err)
			}
			if name == "bundle not named" && cv.Code != "WORKER_CONTRACT" {
				t.Errorf("%s: code %s", name, cv.Code)
			}
			if n := e.scalar(`SELECT COUNT(*) FROM desk_plans`); n != "0" || len(e.planDirs()) != 0 {
				t.Errorf("stored something: %s %v", n, e.planDirs())
			}
		})
	}
}

func TestIngestPlans_Refusals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	j := e.planJob(11, 12)
	good := []bundleIn{{raw: goodBundle(t, 11)}, {raw: goodBundle(t, 12)}}
	t.Run("not a plan job", func(t *testing.T) {
		_, _, err := e.ingest(j, good, func(in *jobs.KindIngest) { in.Job.Kind = jobs.KindAnalyze })
		if err == nil {
			t.Error("ingested")
		}
	})
	t.Run("no seeds in the spec", func(t *testing.T) {
		_, _, err := e.ingest(j, good, func(in *jobs.KindIngest) { in.Job.SpecJSON = `{}` })
		if err == nil {
			t.Error("ingested")
		}
	})
	t.Run("results for other seeds", func(t *testing.T) {
		_, _, err := e.ingest(j, good, func(in *jobs.KindIngest) { in.Output.Plans[1].Seed = 99 })
		violation(t, err, "WORKER_CONTRACT")
	})
	t.Run("wrong count of results", func(t *testing.T) {
		_, _, err := e.ingest(j, good, func(in *jobs.KindIngest) { in.Output.Plans = in.Output.Plans[:1] })
		violation(t, err, "WORKER_CONTRACT")
	})
	t.Run("recipe that does not plan", func(t *testing.T) {
		e2 := newEnv(t)
		j2 := e2.planJob(11)
		e2.exec(`DROP TRIGGER IF EXISTS recipes_immutable`)
		e2.exec(`UPDATE recipes SET cot = 'off' WHERE id = ?`, j2.row.RecipeID)
		_, _, err := e2.ingest(j2, good[:1])
		violation(t, err, "WORKER_CONTRACT")
	})
	t.Run("lease lost", func(t *testing.T) {
		e2 := newEnv(t)
		j2 := e2.planJob(11, 12)
		_, _, err := e2.ingest(j2, []bundleIn{{raw: []byte("junk")}, {raw: goodBundle(t, 12)}}, func(in *jobs.KindIngest) { in.Fence.Owner = "someone-else" })
		if !errors.Is(err, store.ErrLeaseLost) {
			t.Errorf("error %v", err)
		}
	})
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plans`); n != "0" {
		t.Errorf("%s plans", n)
	}
}

func TestIngestPlans_UnwiredService(t *testing.T) {
	t.Parallel()
	s := desk.New(desk.Options{})
	tx := (*sql.Tx)(nil)
	_, _, err := s.IngestKind(context.Background(), tx, jobs.KindIngest{})
	wantProblem(t, err, 503, "UNAVAILABLE")
}
