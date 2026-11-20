package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// orderHook records its calls in a shared log and fails AfterTakeTx on demand.
type orderHook struct {
	name string
	log  *[]string
	err  error
}

func (h orderHook) AfterTakeTx(_ context.Context, _ *sql.Tx, _ TakeInfo) error {
	*h.log = append(*h.log, h.name+":tx")
	return h.err
}
func (h orderHook) AfterTakeCommit(_ context.Context, _ TakeInfo) {
	*h.log = append(*h.log, h.name+":commit")
}

// TestMultiTakeHook_OrderAndRollback: the hooks run in the order given; the first error stops the chain and is returned (the take
// transaction rolls back); every hook sees the commit; nil hooks are skipped.
func TestMultiTakeHook_OrderAndRollback(t *testing.T) {
	t.Parallel()
	var log []string
	boom := errors.New("boom")
	h := MultiTakeHook(orderHook{name: "a", log: &log}, nil, orderHook{name: "b", log: &log}, orderHook{name: "c", log: &log})
	if err := h.AfterTakeTx(t.Context(), nil, TakeInfo{}); err != nil {
		t.Fatal(err)
	}
	h.AfterTakeCommit(t.Context(), TakeInfo{})
	if diff := cmp.Diff([]string{"a:tx", "b:tx", "c:tx", "a:commit", "b:commit", "c:commit"}, log); diff != "" {
		t.Errorf("order: %s", diff)
	}
	log = nil
	h = MultiTakeHook(orderHook{name: "a", log: &log}, orderHook{name: "b", log: &log, err: boom}, orderHook{name: "c", log: &log})
	if err := h.AfterTakeTx(t.Context(), nil, TakeInfo{}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
	if diff := cmp.Diff([]string{"a:tx", "b:tx"}, log); diff != "" {
		t.Errorf("after an error the chain stops: %s", diff)
	}
	if err := MultiTakeHook().AfterTakeTx(t.Context(), nil, TakeInfo{}); err != nil {
		t.Errorf("empty chain: %v", err)
	}
	MultiTakeHook(nil).AfterTakeCommit(t.Context(), TakeInfo{})
}

// TestRTF_OnlyPlainRenders: only a generate job whose needs are within {v2, ears, agent, fallback} teaches rtf_stats (C323.8,
// Revision 2).
func TestRTF_OnlyPlainRenders(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		kind  string
		needs []string
		want  bool
	}{
		{"", nil, true},
		{KindGenerate, []string{}, true},
		{KindGenerate, []string{"v2"}, true},
		{KindGenerate, []string{"ears", "v2"}, true},
		{KindGenerate, []string{"agent", "ears", "fallback", "v2"}, true},
		{KindGenerate, []string{"attach", "v2"}, false},
		{KindGenerate, []string{"analyze", "v2"}, false},
		{KindGenerate, []string{"plan", "v2"}, false},
		{KindGenerate, []string{"ears", "future", "v2"}, false},
		{KindAnalyze, []string{"analyze", "ears", "v2"}, false},
		{KindAnalyze, nil, false},
		{KindPlan, []string{"plan", "v2"}, false},
	} {
		if got := LearnsRTF(c.kind, c.needs); got != c.want {
			t.Errorf("LearnsRTF(%q, %v) = %v, want %v", c.kind, c.needs, got, c.want)
		}
	}
}

// TestRTF_GenerateWithEarsStillLearns and TestRTF_GenerateWithAttachDoesNot drive the real bridge (media tools needed).
func TestRTF_GenerateNeedsDecideLearning(t *testing.T) {
	for _, c := range []struct {
		name, needs string
		samples     int
	}{
		{"plain", `[]`, 1},
		{"ears and v2", `["ears","v2"]`, 1},
		{"attach", `["attach","v2"]`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newBridgeRig(t, true)
			job := b.seed(batchSpec{recipe: b.newRecipe("pop"), needs: c.needs}).Jobs[0]
			b.run1(job, b.flac(1), [2]bool{}, nil)
			b.wantState(job, StateCompleted)
			if got := b.count(`SELECT COALESCE(SUM(samples), 0) FROM rtf_stats WHERE gpu_class = 'A5000'`); got != c.samples {
				t.Errorf("rtf_stats samples = %d, want %d", got, c.samples)
			}
		})
	}
}

// planIngester is a KindIngester of tests: it records its calls and returns what it is told.
type planIngester struct {
	mu      sync.Mutex
	txCalls []KindIngest
	after   []KindIngest
	track   *string
	outcome string
	err     error
	seenIn  int // rows of the jobs table visible inside the transaction (it can read what it needs)
}

func (p *planIngester) IngestKind(ctx context.Context, tx *sql.Tx, in KindIngest) (*string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.txCalls = append(p.txCalls, in)
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE id = ?`, in.Job.ID).Scan(&p.seenIn)
	return p.track, p.outcome, p.err
}

func (p *planIngester) AfterKindCommit(_ context.Context, in KindIngest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.after = append(p.after, in)
}

func (p *planIngester) calls() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.txCalls), len(p.after)
}

// kindBridge is a bridge rig that never decodes audio: a kind ingester gets the files as they came.
func kindBridge(t *testing.T, kinds map[string]KindIngester) *bridgeRig {
	t.Helper()
	return newBridgeRig(t, false, func(o *BridgeOptions) { o.Kinds = kinds })
}

func (b *bridgeRig) runKind(job string) provider.Output {
	b.t.Helper()
	return b.run1(job, []byte("not audio"), [2]bool{}, nil)
}

// TestIngest_DispatchByKind: a completed plan job goes to the ingester of its kind inside the fenced transaction, completes with
// no track and the ingester's outcome; an analyze job completes with the track the ingester names; a kind with no ingester fails
// INGEST_FAILED with KIND_UNAVAILABLE and leaves no row; a generate job never reaches the map.
func TestIngest_DispatchByKind(t *testing.T) {
	t.Run("plan completes without a track", func(t *testing.T) {
		pi := &planIngester{}
		b := kindBridge(t, map[string]KindIngester{KindPlan: pi})
		job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`, specJSON: `{"plan":{"seeds":[1,2],"seconds_per_seed":30}}`}).Jobs[0]
		b.runKind(job)
		b.wantState(job, StateCompleted)
		if tx, after := pi.calls(); tx != 1 || after != 1 {
			t.Fatalf("ingester calls: tx %d, after %d", tx, after)
		}
		in := pi.txCalls[0]
		if in.Job.ID != job || in.Job.Kind != KindPlan || !strings.Contains(in.Job.SpecJSON, `"seconds_per_seed":30`) || in.EnvID == "" || in.Fence.Owner == "" {
			t.Errorf("KindIngest = %+v", in)
		}
		if diff := cmp.Diff([]string{"plan", "v2"}, in.Job.Needs); diff != "" {
			t.Errorf("needs: %s", diff)
		}
		if pi.seenIn != 1 {
			t.Error("the ingester's transaction did not see the job")
		}
		if got := b.str(`SELECT COALESCE(track_id, 'NULL') || '|' || outcome FROM jobs WHERE id = ?`, job); got != "NULL|clean" {
			t.Errorf("job row = %s", got)
		}
		if b.count(`SELECT COUNT(*) FROM tracks`) != 0 {
			t.Error("a plan job created a track")
		}
		if b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'kind_ingested'`, job) != 1 || b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'ingested'`, job) != 1 {
			t.Errorf("events = %v", b.events(job))
		}
		if b.count(`SELECT COUNT(*) FROM uploads WHERE job_id = ? AND state <> 'consumed'`, job) != 0 {
			t.Error("the uploads were not consumed with the result")
		}
	})
	t.Run("analyze completes with its track and truncated outcome", func(t *testing.T) {
		pi := &planIngester{outcome: "truncated"}
		b := kindBridge(t, map[string]KindIngester{KindAnalyze: pi})
		src, _, _ := masterOf(t, b.rig)
		pi.track = &src
		job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindAnalyze, source: src, needs: `["analyze","ears","v2"]`}).Jobs[0]
		b.runKind(job)
		b.wantState(job, StateCompleted)
		if got := b.str(`SELECT track_id || '|' || outcome FROM jobs WHERE id = ?`, job); got != src+"|truncated" {
			t.Errorf("job row = %s", got)
		}
	})
	t.Run("an ingester error rolls everything back and fails the job", func(t *testing.T) {
		pi := &planIngester{err: errors.New("disk full")}
		b := kindBridge(t, map[string]KindIngester{KindPlan: pi})
		job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`}).Jobs[0]
		b.runKind(job)
		b.wantState(job, StateFailed)
		b.wantCode(job, CodeIngestFailed)
		if b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'kind_ingested'`, job) != 0 {
			t.Error("a rolled-back ingest left its event")
		}
		if _, after := pi.calls(); after != 0 {
			t.Error("AfterKindCommit ran after a rollback")
		}
	})
	t.Run("a bad outcome is refused", func(t *testing.T) {
		pi := &planIngester{outcome: "great"}
		b := kindBridge(t, map[string]KindIngester{KindPlan: pi})
		job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`}).Jobs[0]
		b.runKind(job)
		b.wantState(job, StateFailed)
	})
	for _, kind := range []string{KindPlan, KindAnalyze} {
		t.Run(kind+" without an ingester is KIND_UNAVAILABLE", func(t *testing.T) {
			b := kindBridge(t, nil)
			src := ""
			if kind == KindAnalyze {
				src, _, _ = masterOf(t, b.rig)
			}
			job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: kind, source: src, needs: `["` + kind + `","v2"]`}).Jobs[0]
			b.runKind(job)
			b.wantState(job, StateFailed)
			b.wantCode(job, CodeIngestFailed)
			var detail string
			if err := b.st.R.QueryRow(`SELECT detail_json FROM job_events WHERE job_id = ? AND code = 'INGEST_FAILED' ORDER BY id DESC LIMIT 1`, job).Scan(&detail); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(detail, `"ingest_code":"KIND_UNAVAILABLE"`) {
				t.Errorf("detail = %s", detail)
			}
			if got := b.count(`SELECT COUNT(*) FROM tracks WHERE job_id = ?`, job); got != 0 {
				t.Errorf("%d tracks", got)
			}
		})
	}
	t.Run("an ingester that is not a KindJobIngester fails the same way", func(t *testing.T) {
		r := newRig(t)
		r.run.o.Ingest = onlyTakes{r.ing} // serves generate only
		job := r.seedBatch(batchSpec{kind: KindPlan, needs: `["plan","v2"]`}).Jobs[0]
		spec := r.submitOne(job)
		out := r.deliver(spec, deliverOpts{allOptional: true, e2e: 5})
		r.setStatus(r.refOf(job, 1), completed(out, 5000))
		for range 12 {
			r.step()
			if IsTerminal(r.state(job)) {
				break
			}
			r.advance(3e9)
		}
		r.wantState(job, StateFailed)
		if r.ing.callCount() != 0 {
			t.Error("the stub generate ingester was asked to ingest a plan job")
		}
	})
	t.Run("a generate job never reaches the map", func(t *testing.T) {
		pi := &planIngester{}
		b := newBridgeRig(t, true, func(o *BridgeOptions) { o.Kinds = map[string]KindIngester{KindPlan: pi, KindAnalyze: pi} })
		job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
		b.run1(job, b.flac(1), [2]bool{}, nil)
		b.wantState(job, StateCompleted)
		if tx, after := pi.calls(); tx != 0 || after != 0 {
			t.Errorf("a generate take called a kind ingester: %d %d", tx, after)
		}
	})
	t.Run("the bridge refuses to ingest a plan job as a take", func(t *testing.T) {
		b := kindBridge(t, map[string]KindIngester{KindPlan: &planIngester{}})
		_, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "o"), JobRow{ID: "j", Kind: KindPlan}, AttemptRow{}, []upload.Received{}, provider.Output{})
		var ife *IngestFailedError
		if !errors.As(err, &ife) || ife.Code != CodeKindUnavailable {
			t.Errorf("err = %v", err)
		}
	})
}

// onlyTakes hides the KindJobIngester half of an Ingester.
type onlyTakes struct{ Ingester }

// TestIngest_KindCommittedCompletesFromEvent: the process stops after the kind transaction committed and before the job row
// followed: a runner that adopts the job completes it from the kind_ingested event instead of ingesting again.
func TestIngest_KindCommittedCompletesFromEvent(t *testing.T) {
	pi := &planIngester{}
	b := kindBridge(t, map[string]KindIngester{KindPlan: pi})
	job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`}).Jobs[0]
	jr, ar, files, out := b.capture(job, []byte("x"))
	if _, _, err := b.bridge.IngestKindJob(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out); err != nil {
		t.Fatal(err)
	}
	b.advance(2 * LeaseLength)
	b2 := b.second("runner-2", func(o *RunnerOptions) { o.Ingest = b.bridge })
	for range 4 {
		if _, err := b2.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
		_ = b2.Drain(t.Context())
		if IsTerminal(b.state(job)) {
			break
		}
	}
	b.wantState(job, StateCompleted)
	if tx, _ := pi.calls(); tx != 1 {
		t.Errorf("the ingester ran %d times", tx)
	}
	if got := b.str(`SELECT COALESCE(track_id, 'NULL') || '|' || outcome FROM jobs WHERE id = ?`, job); got != "NULL|clean" {
		t.Errorf("job row = %s", got)
	}
}

// TestJobsReaders_PlanJobVisible: list_jobs, get_job and get_batch show a completed plan job with track_id null and its kind.
func TestJobsReaders_PlanJobVisible(t *testing.T) {
	pi := &planIngester{}
	b := kindBridge(t, map[string]KindIngester{KindPlan: pi})
	seeded := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`})
	job := seeded.Jobs[0]
	b.runKind(job)
	b.wantState(job, StateCompleted)
	svc := New(Options{Store: b.st, Clock: b.clk, Hub: b.hub, Spend: b.sp})
	ctx := context.Background()
	p := auth.CLI
	got, err := svc.Job(ctx, p, job)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindPlan || got.TrackID != nil || got.State != StateCompleted || got.Outcome == nil || *got.Outcome != "clean" {
		t.Errorf("get_job: kind %q track %v state %s outcome %v", got.Kind, got.TrackID, got.State, got.Outcome)
	}
	page, err := svc.ListJobs(ctx, p, JobQuery{BatchID: seeded.Batch})
	if err != nil || len(page.Items) != 1 || page.Items[0].Kind != KindPlan || page.Items[0].TrackID != nil {
		t.Errorf("list_jobs: %+v %v", page, err)
	}
	bt, err := svc.Batch(ctx, p, seeded.Batch)
	if err != nil || len(bt.Jobs) != 1 || bt.Jobs[0].Kind != KindPlan || bt.Jobs[0].TrackID != nil {
		t.Errorf("get_batch: %+v %v", bt.Jobs, err)
	}
	// a plain job reads as generate
	plain := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	if pj, err := svc.Job(ctx, p, plain); err != nil || pj.Kind != KindGenerate {
		t.Errorf("plain job kind = %q (%v)", pj.Kind, err)
	}
}

// TestTakeInfo_CarriesKindAndNeeds: a take hook sees the kind and needs of the job it hooks.
func TestTakeInfo_CarriesKindAndNeeds(t *testing.T) {
	h := &recHook{}
	b := newBridgeRig(t, true, func(o *BridgeOptions) { o.TakeHook = h })
	h.b = b
	job := b.seed(batchSpec{recipe: b.newRecipe("pop"), needs: `["ears","v2"]`}).Jobs[0]
	b.run1(job, b.flac(1), [2]bool{}, nil)
	b.wantState(job, StateCompleted)
	if len(h.tx) != 1 || len(h.commit) != 1 {
		t.Fatalf("hook calls: tx %d, commit %d", len(h.tx), len(h.commit))
	}
	for _, info := range []TakeInfo{h.tx[0], h.commit[0]} {
		if info.Kind != KindGenerate || fmt.Sprint(info.Needs) != "[ears v2]" {
			t.Errorf("take info kind %q needs %v", info.Kind, info.Needs)
		}
	}
}

// TestScanJob_KindNeedsSpec: the runner's row carries the three G1 columns.
func TestScanJob_KindNeedsSpec(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id := r.seedBatch(batchSpec{kind: KindPlan, needs: `["plan","v2"]`, specJSON: `{"plan":{"seeds":[7],"seconds_per_seed":20}}`}).Jobs[0]
	j := r.job(id)
	if j.Kind != KindPlan || fmt.Sprint(j.Needs) != "[plan v2]" {
		t.Errorf("job = %+v", j.JobRow)
	}
	sp, err := jobspec.Parse([]byte(j.SpecJSON))
	if err != nil || sp.Plan == nil || sp.Plan.SecondsPerSeed != 20 {
		t.Errorf("spec %q: %+v %v", j.SpecJSON, sp, err)
	}
	plain := r.job(r.seedBatch(batchSpec{}).Jobs[0])
	if plain.Kind != KindGenerate || len(plain.Needs) != 0 || plain.SpecJSON != "{}" {
		t.Errorf("plain job = %+v", plain.JobRow)
	}
}

// IngestKindJob makes the stub ingester a KindJobIngester for tests that capture what the runner hands over: it records the call
// like IngestTake (blocking on block when set) and completes the job without a track.
func (s *stubIngest) IngestKindJob(ctx context.Context, job JobRow, att AttemptRow, files []upload.Received, out provider.Output) (string, string, error) {
	s.mu.Lock()
	s.calls++
	s.last, s.lastJob, s.lastAtt, s.lastOut = files, job, att, out
	block, entered, err := s.block, s.entered, s.err
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	return "", "clean", err
}

// TestInputs_FromSpecPins: MintPins signs the pinned master for any engine: the link verifies on the media route, outlives the
// window, carries the digest and size, uses the master's own extension, and a changed, unknown or re-pointed master is
// ErrInputChanged without a link in the error.
func TestInputs_FromSpecPins(t *testing.T) {
	r, sg, o := minterRig(t)
	id, sha, size := masterOf(t, r)
	m := NewInputMinter(o).(PinMinter)
	expires := r.clk.Now().Add(2*time.Hour + 5*time.Second)
	got, err := m.MintPins(t.Context(), jobspec.Inputs{SrcAudio: &jobspec.Pin{TrackID: id, MasterSHA256: sha, Bytes: size}}, expires)
	if err != nil {
		t.Fatal(err)
	}
	src, ok := got[provider.InputSrcAudio]
	if len(got) != 1 || !ok || src.SHA256 != sha || src.Bytes != size {
		t.Fatalf("inputs %+v", got)
	}
	if exp := verifyLink(t, sg, src.URL, sha); exp.Before(expires) || exp.After(expires.Add(2*linkBucket)) {
		t.Errorf("expiry %v must outlive %v by less than two buckets", exp, expires)
	}
	// no pin: nothing to mint, and an unwired minter still answers
	if got, err := m.MintPins(t.Context(), jobspec.Inputs{}, expires); got != nil || err != nil {
		t.Errorf("no pin: %v %v", got, err)
	}
	for _, c := range []struct {
		name string
		pin  jobspec.Pin
	}{
		{"master changed", jobspec.Pin{TrackID: id, MasterSHA256: strings.Repeat("0", 64), Bytes: size}},
		{"unknown track", jobspec.Pin{TrackID: "no-such-track", MasterSHA256: sha, Bytes: size}},
	} {
		t.Run(c.name, func(t *testing.T) {
			pin := c.pin
			_, err := m.MintPins(t.Context(), jobspec.Inputs{SrcAudio: &pin}, expires)
			if !errors.Is(err, ErrInputChanged) {
				t.Fatalf("err = %v, want ErrInputChanged", err)
			}
			if strings.Contains(err.Error(), "sig=") || strings.Contains(err.Error(), "/media/") {
				t.Errorf("error leaks a link: %v", err)
			}
		})
	}
	if _, err := NewInputMinter(InputMinterOptions{Store: r.st}).(PinMinter).MintPins(t.Context(), jobspec.Inputs{SrcAudio: &jobspec.Pin{TrackID: id, MasterSHA256: sha, Bytes: size}}, expires); err == nil {
		t.Error("an unwired minter must refuse")
	}
	if _, err := m.MintPins(t.Context(), jobspec.Inputs{SrcAudio: &jobspec.Pin{TrackID: id, MasterSHA256: sha, Bytes: size}}, r.clk.Now().Add(maxLinkHorizon)); !errors.Is(err, ErrInputHorizon) {
		t.Errorf("a window beyond the horizon: %v", err)
	}
}

// TestInputs_FromSpecPins_LossyMaster: a lossy (mp3) reference master is signed as <sha>.mp3, so the media route resolves it.
func TestInputs_FromSpecPins_LossyMaster(t *testing.T) {
	r, sg, o := minterRig(t)
	sha := strings.Repeat("e", 64)
	// the shape triggers belong to the importer; this test only needs a lossy row to read
	r.exec(`DROP TRIGGER tracks_lossy_shape`)
	r.exec(`DROP TRIGGER tracks_reference_rule`)
	r.exec(`INSERT INTO tracks(id, slug, title, recipe_id, parent_track_id, root_track_id, generation, status, status_set_by, origin,
		duration_s, sample_rate, bits_per_sample, channels, total_samples, master_path, master_sha256, master_bytes, source_sha256, frames_sha256, audio_md5,
		import_rule, recorded_at, created_at, lossy) VALUES('lossy-1', 'lossy', 'L', ?, NULL, 'lossy-1', 0, 'draft', 'system:job', 'import',
		30, 44100, 16, 2, 1323000, 'audio/masters/lossy.mp3', ?, 4242, ?, ?, '00000000000000000000000000000000', 'reference', ?, ?, 1)`,
		testRecipe, sha, sha, sha, store.FormatTime(t0), store.FormatTime(t0))
	m := NewInputMinter(o).(PinMinter)
	got, err := m.MintPins(t.Context(), jobspec.Inputs{SrcAudio: &jobspec.Pin{TrackID: "lossy-1", MasterSHA256: sha, Bytes: 4242}}, r.clk.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	src := got[provider.InputSrcAudio]
	u, err := url.Parse(src.URL)
	if err != nil || !strings.HasPrefix(u.Path, "/media/"+sha+".mp3") || src.Bytes != 4242 || src.SHA256 != sha {
		t.Fatalf("link %q (%v) ref %+v", src.URL, err, src)
	}
	q := u.Query()
	if err := sg.Verify(sha, "mp3", q.Get("exp"), q.Get("sig")); err != nil {
		t.Errorf("the media route would refuse the mp3 link: %v", err)
	}
}
