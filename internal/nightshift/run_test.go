package nightshift

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func nightEstimates(g *rig) int {
	return g.count(`SELECT COUNT(*) FROM estimates WHERE principal_id = 'agent:nightshift'`)
}
func nightBatches(g *rig) int {
	return g.count(`SELECT COUNT(*) FROM batches WHERE principal_id = 'agent:nightshift'`)
}

// TestNightShift_CreatesUnderMandate: an enabled shift with a live mandate plans the Owner's lineages, prices them with Preview, makes
// exactly one estimate and one create, and the batch carries the mandate and a mandate approval, never a self-approval.
func TestNightShift_CreatesUnderMandate(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	m := g.grant(2, 3)
	g.r.Run(t.Context(), g.key(g.night))
	run := g.run(g.night)
	if run.State != StateCreated || run.BatchID == "" || run.MandateID != m.ID || run.EstimateID == "" {
		t.Fatalf("run = %+v", run)
	}
	if nightEstimates(g) != 1 || nightBatches(g) != 1 {
		t.Fatalf("estimates %d batches %d, want 1 and 1", nightEstimates(g), nightBatches(g))
	}
	if n := g.count(`SELECT COUNT(*) FROM batches b JOIN approvals a ON a.batch_id = b.id WHERE b.id = ? AND b.mandate_id = ? AND a.state = 'auto' AND a.via = 'mandate'`, run.BatchID, m.ID); n != 1 {
		t.Errorf("mandate approval rows = %d", n)
	}
	if n := g.count(`SELECT COUNT(*) FROM approvals a JOIN batches b ON b.id = a.batch_id WHERE b.principal_id = 'agent:nightshift' AND a.via <> 'mandate'`); n != 0 {
		t.Errorf("%d approvals that are not the mandate's", n)
	}
	if n := g.count(`SELECT COUNT(*) FROM batches WHERE id = ? AND rationale LIKE 'night shift 2026-10-15: 2 exploit, 0 explore'`, run.BatchID); n != 1 {
		t.Error("the rationale does not name the night and its split")
	}
	if !strings.Contains(run.PlanJSON, `"input_digest"`) || !strings.Contains(run.PlanJSON, `"seed"`) || strings.Contains(strings.ToLower(run.PlanJSON), "lyrics") {
		t.Errorf("plan_json = %s", run.PlanJSON)
	}
	rows := g.auditRows("nightshift")
	if len(rows) != 1 || !strings.HasPrefix(rows[0], "nightshift:create|agent:nightshift|runner|201|") || !strings.Contains(rows[0], run.BatchID) {
		t.Errorf("audit rows = %q", rows)
	}
	u, err := g.ms.Usage(t.Context(), m.ID)
	if err != nil || u.Batches != 1 || u.Takes != 2 {
		t.Errorf("usage %+v %v", u, err)
	}
	if rows[0] == "" || !strings.Contains(rows[0], fmt.Sprintf("|%d|", int64(u.Committed))) {
		t.Errorf("the audit cost is not the batch ceiling %d: %s", u.Committed, rows[0])
	}
}

// TestNightShift_TrimsToMandateCeiling: a per-night ceiling that admits one take trims a two-take plan from the end; the trimmed
// lineage is recorded in plan_json and the committed ceiling never exceeds the mandate's.
func TestNightShift_TrimsToMandateCeiling(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	m := g.grant(2, 1)
	g.r.Run(t.Context(), g.key(g.night))
	run := g.run(g.night)
	if run.State != StateCreated {
		t.Fatalf("run = %+v", run)
	}
	if n := g.count(`SELECT n_takes FROM batches WHERE id = ?`, run.BatchID); n != 1 {
		t.Errorf("takes = %d, want 1", n)
	}
	if !strings.Contains(run.PlanJSON, `"trimmed"`) || !strings.Contains(run.PlanJSON, "batch_ceiling") {
		t.Errorf("plan_json does not record the trim: %s", run.PlanJSON)
	}
	if c := g.count(`SELECT ceiling_micro_usd FROM batches WHERE id = ?`, run.BatchID); money.MicroUSD(c) > m.Scope.MaxBatchCeilingMicroUSD {
		t.Errorf("ceiling %d above the mandate's %d", c, m.Scope.MaxBatchCeilingMicroUSD)
	}
	if nightEstimates(g) != 1 {
		t.Errorf("a trimmed night wrote %d estimate rows, want exactly 1 (Preview writes nothing)", nightEstimates(g))
	}
}

// TestNightShift_NoMandateSkips: with no live mandate the night spends nothing and writes no estimate; a scheduled mandate says so.
func TestNightShift_NoMandateSkips(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.r.Run(t.Context(), g.key("2026-10-15"))
	if run := g.run("2026-10-15"); run.State != StateSkipped || run.Reason != SkipNoMandate {
		t.Fatalf("run = %+v", run)
	}
	c := g.ceilingOfOne()
	from := lt.FixedNow.Add(2 * time.Hour)
	if _, err := g.ms.Create(t.Context(), alice, mandates.CreateInput{Holder: mandates.HolderNightShift, Title: "later", Budget: 4 * c, ValidFrom: &from,
		ValidUntil: lt.FixedNow.Add(48 * time.Hour), Scope: mandates.Scope{Modes: []string{"new_seed"}, Engines: []string{"yue2"}, MinStars: 3,
			MaxTakesPerLineage: 5, MaxTakesPerBatch: 2, MaxBatchCeilingMicroUSD: 2 * c, MaxBatchesPerDay: 1}}); err != nil {
		t.Fatal(err)
	}
	g.r.Run(t.Context(), g.key("2026-10-16"))
	if run := g.run("2026-10-16"); run.State != StateSkipped || run.Reason != SkipMandateScheduled {
		t.Fatalf("run = %+v", run)
	}
	if nightEstimates(g) != 0 || nightBatches(g) != 0 || g.count(`SELECT COUNT(*) FROM ledger`) != g.count(`SELECT COUNT(*) FROM ledger WHERE 1`) {
		t.Fatal("a night without a live mandate wrote an estimate or a batch")
	}
	rows := g.auditRows("nightshift")
	if len(rows) != 2 || !strings.HasPrefix(rows[0], "nightshift:skip|system:nightshift|runner|200|0|") {
		t.Errorf("audit rows = %q", rows)
	}
}

// TestNightShift_RunOncePerNight: concurrent and repeated runs of one night make one row and one batch.
func TestNightShift_RunOncePerNight(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(4, 3)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); g.r.Run(t.Context(), g.key(g.night)) }()
	}
	wg.Wait()
	if n := g.count(`SELECT COUNT(*) FROM night_shift_runs`); n != 1 || nightBatches(g) != 1 || nightEstimates(g) != 1 {
		t.Fatalf("runs %d batches %d estimates %d, want 1 each", n, nightBatches(g), nightEstimates(g))
	}
	if done, err := g.r.Done(t.Context(), g.key(g.night)); err != nil || !done {
		t.Errorf("Done = %v %v", done, err)
	}
	if done, _ := g.r.Done(t.Context(), g.key("2026-10-16")); done {
		t.Error("another day is not done")
	}
}

// TestNightShift_PreviousRunningSkips: a night whose batch has not finished blocks the next one.
func TestNightShift_PreviousRunningSkips(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(4, 3)
	g.r.Run(t.Context(), g.key("2026-10-15"))
	g.r.Run(t.Context(), g.key("2026-10-16"))
	if run := g.run("2026-10-16"); run.State != StateSkipped || run.Reason != SkipPreviousRunning {
		t.Fatalf("run = %+v", run)
	}
	if nightBatches(g) != 1 {
		t.Fatalf("batches = %d", nightBatches(g))
	}
}

// TestNightShift_MandateExhaustedSkips: once the mandate's day count is used the next night spends nothing.
func TestNightShift_MandateExhaustedSkips(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	c := g.ceilingOfOne()
	if _, err := g.ms.Create(t.Context(), alice, mandates.CreateInput{Holder: mandates.HolderNightShift, Title: "once", Budget: 10 * c, ValidUntil: lt.FixedNow.Add(24 * time.Hour),
		Scope: mandates.Scope{Modes: []string{"new_seed"}, Engines: []string{"yue2"}, MinStars: 3, MaxTakesPerLineage: 5, MaxTakesPerBatch: 2,
			MaxBatchCeilingMicroUSD: 2 * c, MaxBatchesPerDay: 1}}); err != nil {
		t.Fatal(err)
	}
	g.r.Run(t.Context(), g.key("2026-10-15"))
	finishBatches(t, g)
	g.r.Run(t.Context(), g.key("2026-10-16"))
	if run := g.run("2026-10-16"); run.State != StateSkipped || run.Reason != mandates.ReasonBatchesPerDay {
		t.Fatalf("run = %+v", run)
	}
	if nightBatches(g) != 1 {
		t.Fatalf("batches = %d", nightBatches(g))
	}
}

// finishBatches ends every active night batch having spent its whole ceiling, as the reconciler records it when the jobs are over.
func finishBatches(t *testing.T, g *rig) {
	t.Helper()
	if _, err := g.st.W.Exec(`INSERT INTO ledger(at, kind, micro_usd, batch_id, principal_id, reason)
		SELECT ?, 'settle', ceiling_micro_usd, id, principal_id, 'test' FROM batches WHERE state = 'active' AND principal_id = 'agent:nightshift'`, store.FormatTime(g.clock())); err != nil {
		t.Fatal(err)
	}
	if _, err := g.st.W.Exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE state = 'active'`, store.FormatTime(g.clock())); err != nil {
		t.Fatal(err)
	}
}

// TestNightShift_MandateBudgetTrimsToNothing: a budget with room for less than one more batch leaves the next night over the ceiling.
func TestNightShift_MandateBudgetTrimsToNothing(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	c := g.ceilingOfOne()
	if _, err := g.ms.Create(t.Context(), alice, mandates.CreateInput{Holder: mandates.HolderNightShift, Title: "small", Budget: c + c/2, ValidUntil: lt.FixedNow.Add(24 * time.Hour),
		Scope: mandates.Scope{Modes: []string{"new_seed"}, Engines: []string{"yue2"}, MinStars: 3, MaxTakesPerLineage: 5, MaxTakesPerBatch: 2,
			MaxBatchCeilingMicroUSD: c, MaxBatchesPerDay: 4}}); err != nil {
		t.Fatal(err)
	}
	g.r.Run(t.Context(), g.key("2026-10-15"))
	if run := g.run("2026-10-15"); run.State != StateCreated {
		t.Fatalf("first night: %+v", run)
	}
	finishBatches(t, g)
	g.r.Run(t.Context(), g.key("2026-10-16"))
	run := g.run("2026-10-16")
	if run.State != StateSkipped || run.Reason != SkipOverCeiling {
		t.Fatalf("run = %+v", run)
	}
	if nightBatches(g) != 1 {
		t.Fatalf("batches = %d", nightBatches(g))
	}
}

// TestNightShift_CapsRefusalSkips: a spend cap that the night would hit skips it (never trimmed to squeeze under a cap) and spends nothing.
func TestNightShift_CapsRefusalSkips(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(2, 3)
	if _, err := g.st.W.Exec(`UPDATE spend_caps SET batch_micro_usd = 1, updated_by = 'system:test' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	g.r.Run(t.Context(), g.key(g.night))
	if run := g.run(g.night); run.State != StateSkipped || run.Reason != SkipCaps {
		t.Fatalf("run = %+v", run)
	}
	if nightEstimates(g) != 0 || nightBatches(g) != 0 {
		t.Fatal("a capped night wrote rows")
	}
}

// TestNightShift_DisabledAndHaltedSkip: the Run itself refuses when the shift was switched off or halted after the schedule woke.
func TestNightShift_DisabledAndHaltedSkip(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.grant(2, 3)
	g.r.Run(t.Context(), g.key("2026-10-15")) // off by default
	if run := g.run("2026-10-15"); run.State != StateSkipped || run.Reason != SkipDisabled {
		t.Fatalf("run = %+v", run)
	}
	g.enable()
	if _, err := g.r.Halt(t.Context(), alice, "stop"); err != nil {
		t.Fatal(err)
	}
	g.r.Run(t.Context(), g.key("2026-10-16"))
	if run := g.run("2026-10-16"); run.State != StateSkipped || run.Reason != SkipHalted {
		t.Fatalf("run = %+v", run)
	}
	on := true
	if _, err := g.r.UpdateSettings(t.Context(), alice, SettingsPatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	g.r.Run(t.Context(), g.key("2026-10-17"))
	if run := g.run("2026-10-17"); run.State != StateCreated {
		t.Fatalf("after enabling again: %+v", run)
	}
}

// TestNightShift_NothingPlanned: a mandate that no lineage fits makes the night skip with the planner's refusals on record.
func TestNightShift_NothingPlanned(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	c := g.ceilingOfOne()
	if _, err := g.ms.Create(t.Context(), alice, mandates.CreateInput{Holder: mandates.HolderNightShift, Title: "picky", Budget: 4 * c, ValidUntil: lt.FixedNow.Add(24 * time.Hour),
		Scope: mandates.Scope{Modes: []string{"new_seed"}, Engines: []string{"yue2"}, MinStars: 5, RootTrackIDs: []string{lt.TrackCitta}, MaxTakesPerLineage: 5,
			MaxTakesPerBatch: 2, MaxBatchCeilingMicroUSD: 2 * c, MaxBatchesPerDay: 1}}); err != nil {
		t.Fatal(err)
	}
	g.r.Run(t.Context(), g.key(g.night))
	run := g.run(g.night)
	if run.State != StateSkipped || run.Reason != SkipNothingPlanned || !strings.Contains(run.PlanJSON, `"dropped"`) {
		t.Fatalf("run = %+v", run)
	}
}

// TestNightShift_HaltCancelsTonight: a halt cancels the unfinished batch of the night in progress and marks the run halted; later
// nights are skipped. It is idempotent: a second halt changes nothing and cancels nothing more.
func TestNightShift_HaltCancelsTonight(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(2, 3)
	g.r.Run(t.Context(), g.key(g.night))
	run := g.run(g.night)
	s, err := g.r.Halt(t.Context(), alice, "too loud")
	if err != nil || !s.Halted() || *s.HaltReason != "too loud" || *s.HaltedBy != "human:alice" {
		t.Fatalf("halt: %+v %v", s, err)
	}
	if got := g.run(g.night); got.State != StateHalted || got.BatchID != run.BatchID {
		t.Fatalf("run = %+v", got)
	}
	if n := g.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND cancel_requested_by = 'system:nightshift'`, run.BatchID); n == 0 {
		t.Error("the halt did not send cancel intents for the night's jobs")
	}
	rows := g.auditRows("nightshift:halt_cancel")
	if len(rows) != 1 {
		t.Fatalf("halt_cancel rows = %q", rows)
	}
	s2, err := g.r.Halt(t.Context(), phone, "again")
	if err != nil || *s2.HaltReason != "too loud" || len(g.auditRows("nightshift:halt_cancel")) != 1 {
		t.Fatalf("second halt: %+v %v", s2, err)
	}
	g.r.Run(t.Context(), g.key("2026-10-16"))
	if got := g.run("2026-10-16"); got.State != StateSkipped || got.Reason != SkipHalted {
		t.Fatalf("run = %+v", got)
	}
}

// TestNightShift_HaltedBatchStillBlocksUntilFinished: re-enabling while the halted night's batch has not finished does not start a second one.
func TestNightShift_HaltedBatchStillBlocksUntilFinished(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(4, 3)
	g.r.Run(t.Context(), g.key("2026-10-15"))
	if _, err := g.r.Halt(t.Context(), alice, "stop"); err != nil {
		t.Fatal(err)
	}
	g.enable()
	g.r.Run(t.Context(), g.key("2026-10-16"))
	if got := g.run("2026-10-16"); got.Reason != SkipPreviousRunning {
		t.Fatalf("run = %+v", got)
	}
}

// TestHalt_Guards: Editors and Listeners may not halt; the reason is 1..500 characters; every Owner-side principal may.
func TestHalt_Guards(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	for _, p := range []auth.Principal{bob, carol} {
		if _, err := g.r.Halt(t.Context(), p, "x"); !hasCode(err, "OWNER_ONLY") {
			t.Errorf("%s: %v", p.Name, err)
		}
	}
	for _, reason := range []string{"", strings.Repeat("é", 501)} {
		if _, err := g.r.Halt(t.Context(), alice, reason); !hasCode(err, "VALIDATION_FAILED") {
			t.Errorf("reason %d runes: %v", len([]rune(reason)), err)
		}
	}
	if _, err := g.r.Halt(t.Context(), agent, strings.Repeat("é", 500)); err != nil {
		t.Errorf("an agent halts: %v", err)
	}
}

func hasCode(err error, code string) bool {
	var pe *problem.Error
	return errors.As(err, &pe) && pe.Code == code
}

// TestNightShift_NeverApprovesItself: without a mandate nothing is created, so there is nothing to approve; with one the only approval is the mandate's.
func TestNightShift_NeverApprovesItself(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.r.Run(t.Context(), g.key("2026-10-15"))
	if n := g.count(`SELECT COUNT(*) FROM approvals WHERE batch_id IN (SELECT id FROM batches WHERE principal_id = 'agent:nightshift')`); n != 0 {
		t.Fatalf("%d approvals without a mandate", n)
	}
	g.grant(2, 3)
	g.r.Run(t.Context(), g.key("2026-10-16"))
	if n := g.count(`SELECT COUNT(*) FROM approvals a JOIN batches b ON b.id = a.batch_id WHERE b.principal_id = 'agent:nightshift' AND (a.via <> 'mandate' OR a.state <> 'auto')`); n != 0 {
		t.Fatalf("%d approvals that are not the mandate's", n)
	}
	if n := g.count(`SELECT COUNT(*) FROM spend_caps WHERE updated_by <> 'system:test'`); n != 0 {
		t.Fatal("the night shift touched the caps")
	}
}

// TestNightShift_ResumeAfterCreateCrash: a crash between create's commit and the run's update leaves a planned run whose estimate
// is consumed; Resume adopts that batch and makes no second one.
func TestNightShift_ResumeAfterCreateCrash(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(4, 3)
	g.r.o.AfterCreate = func() { panic("simulated crash") }
	func() {
		defer func() { _ = recover() }()
		g.r.Run(t.Context(), g.key(g.night))
	}()
	g.r.o.AfterCreate = nil
	if run := g.run(g.night); run.State != StatePlanned || run.BatchID != "" || nightBatches(g) != 1 {
		t.Fatalf("after the crash: %+v, %d batches", run, nightBatches(g))
	}
	g.r.Resume(t.Context()) // too young: nothing yet
	if run := g.run(g.night); run.State != StatePlanned {
		t.Fatalf("a young run was resumed: %+v", run)
	}
	g.advance(11 * time.Minute)
	g.r.Resume(t.Context())
	run := g.run(g.night)
	if run.State != StateCreated || run.BatchID == "" || nightBatches(g) != 1 || nightEstimates(g) != 1 {
		t.Fatalf("after resume: %+v, %d batches, %d estimates", run, nightBatches(g), nightEstimates(g))
	}
	if rows := g.auditRows("nightshift:resume"); len(rows) != 1 {
		t.Errorf("resume rows = %q", rows)
	}
}

// TestNightShift_ResumeAfterEstimateCrash: a crash after the estimate but before create leaves an unconsumed estimate; Resume redoes
// the night and one batch results.
func TestNightShift_ResumeAfterEstimateCrash(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	m := g.grant(4, 3)
	parent := lt.TrackLuna
	est, err := g.gen.Estimate(t.Context(), Principal(), generation.EstimateIn{Items: []generation.Item{{Mode: "new_seed", ParentTrackID: &parent, NSeeds: 1}}, Via: spend.ViaAPI, MandateID: m.ID})
	if err != nil {
		t.Fatal(err)
	}
	id, won, err := g.r.claim(t.Context(), g.night)
	if err != nil || !won {
		t.Fatal(err)
	}
	if err := g.r.setEstimate(t.Context(), id, est.ID); err != nil {
		t.Fatal(err)
	}
	g.advance(11 * time.Minute)
	g.r.Resume(t.Context())
	if run := g.run(g.night); run.State != StateCreated || nightBatches(g) != 1 {
		t.Fatalf("after resume: %+v, %d batches", run, nightBatches(g))
	}
}

// stub is a Generator whose every answer a test scripts.
type stub struct {
	preview  func(in generation.EstimateIn) (generation.Estimate, error)
	estimate func(in generation.EstimateIn) (generation.Estimate, error)
	create   func() (generation.Created, error)
	previews int
}

func (s *stub) Preview(_ context.Context, _ auth.Principal, in generation.EstimateIn) (generation.Estimate, error) {
	s.previews++
	return s.preview(in)
}
func (s *stub) Estimate(_ context.Context, _ auth.Principal, in generation.EstimateIn) (generation.Estimate, error) {
	return s.estimate(in)
}
func (s *stub) Create(_ context.Context, _ auth.Principal, _ generation.CreateIn, _ func(generation.Created) (int, []byte, error)) (generation.Created, error) {
	return s.create()
}

func covered(in generation.EstimateIn) generation.Estimate {
	return generation.Estimate{Takes: len(in.Items), Budget: generation.CapFit{Fits: true}, Cost: generation.CostBlock{Ceiling: 1000},
		Mandate: &generation.MandateCoverage{ID: in.MandateID, Covered: true}}
}

func perr(status int, code string, d map[string]any) error { return problem.E(status, code, code, d) }

// withStub runs one night through a scripted generator.
func withStub(t *testing.T, s *stub) (*rig, Run) {
	t.Helper()
	g := newRig(t)
	g.enable()
	g.grant(4, 3)
	g.r.o.Generation = s
	g.r.Run(t.Context(), g.key(g.night))
	return g, g.run(g.night)
}

// TestNightShift_PreviewRefusals: the errors Preview may answer either trim the night (an invalid item, a backlogged root) or skip it with a stable reason.
func TestNightShift_PreviewRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		err        func(call int, in generation.EstimateIn) error
		state, why string
		takes      int
	}{
		{"invalid item trims it", func(call int, in generation.EstimateIn) error {
			if call == 1 {
				return perr(422, "INVALID_ITEM", map[string]any{"item": 1})
			}
			return nil
		}, StateFailed, "internal", 1}, // the scripted estimate below fails after the trim: only the trim is asserted
		{"invalid item out of range fails", func(int, generation.EstimateIn) error { return perr(422, "INVALID_ITEM", map[string]any{"item": 9}) }, StateFailed, "invalid_item", 0},
		{"invalid item without index fails", func(int, generation.EstimateIn) error { return perr(422, "INVALID_ITEM", nil) }, StateFailed, "invalid_item", 0},
		{"backlog without root fails", func(int, generation.EstimateIn) error { return perr(409, "CALLBACKS_BACKLOG", nil) }, StateFailed, "callbacks_backlog", 0},
		{"backlog of an unknown root fails", func(int, generation.EstimateIn) error {
			return perr(409, "CALLBACKS_BACKLOG", map[string]any{"root_track_id": "nope"})
		}, StateFailed, "callbacks_backlog", 0},
		{"generation disabled", func(int, generation.EstimateIn) error { return perr(503, "GENERATION_DISABLED", nil) }, StateSkipped, SkipGenerationDisabled, 0},
		{"unavailable", func(int, generation.EstimateIn) error { return perr(503, "UNAVAILABLE", nil) }, StateSkipped, "unavailable", 0},
		{"unknown mandate", func(int, generation.EstimateIn) error { return perr(404, "NOT_FOUND", nil) }, StateSkipped, SkipMandateChanged, 0},
		{"plain error", func(int, generation.EstimateIn) error { return errors.New("boom") }, StateFailed, "internal", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := 0
			s := &stub{
				preview: func(in generation.EstimateIn) (generation.Estimate, error) {
					call++
					if err := tc.err(call, in); err != nil {
						return generation.Estimate{}, err
					}
					return covered(in), nil
				},
				estimate: func(in generation.EstimateIn) (generation.Estimate, error) {
					return generation.Estimate{}, errors.New("stop here")
				},
			}
			g, run := withStub(t, s)
			if run.State != tc.state || run.Reason != tc.why {
				t.Fatalf("run = %+v", run)
			}
			if tc.takes > 0 && !strings.Contains(run.PlanJSON, `"invalid_item"`) {
				t.Errorf("the trim is not recorded: %s", run.PlanJSON)
			}
			if rows := g.auditRows("nightshift:skip"); len(rows) != 1 {
				t.Errorf("audit rows = %q", rows)
			}
		})
	}
}

// TestNightShift_BacklogTrimsRoot: a backlogged root is dropped with all its items and the rest of the night goes on.
func TestNightShift_BacklogTrimsRoot(t *testing.T) {
	t.Parallel()
	calls := 0
	s := &stub{
		preview: func(in generation.EstimateIn) (generation.Estimate, error) {
			calls++
			if calls == 1 {
				return generation.Estimate{}, perr(409, "CALLBACKS_BACKLOG", map[string]any{"root_track_id": lt.TrackLuna})
			}
			if len(in.Items) != 1 {
				t.Errorf("%d items after the backlog trim", len(in.Items))
			}
			return covered(in), nil
		},
		estimate: func(in generation.EstimateIn) (generation.Estimate, error) {
			return generation.Estimate{}, errors.New("stop")
		},
	}
	_, run := withStub(t, s)
	if !strings.Contains(run.PlanJSON, `"backlogged"`) {
		t.Errorf("plan_json = %s", run.PlanJSON)
	}
}

// TestNightShift_TrimDecisions: how the price of a candidate night decides to trim, skip or stop.
func TestNightShift_TrimDecisions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		preview func(in generation.EstimateIn) generation.Estimate
		reason  string
		calls   int
	}{
		{"a non-trimmable reason skips", func(in generation.EstimateIn) generation.Estimate {
			e := covered(in)
			e.Mandate = &generation.MandateCoverage{ID: in.MandateID, Reasons: []string{mandates.ReasonBatchCeiling, mandates.ReasonEngine}}
			return e
		}, mandates.ReasonEngine, 1},
		{"trimmable reasons trim to nothing", func(in generation.EstimateIn) generation.Estimate {
			e := covered(in)
			e.Mandate = &generation.MandateCoverage{ID: in.MandateID, Reasons: []string{mandates.ReasonBudget}}
			return e
		}, SkipOverCeiling, 2},
		{"caps skip without trimming", func(in generation.EstimateIn) generation.Estimate {
			e := covered(in)
			e.Budget.Fits = false
			return e
		}, SkipCaps, 1},
		{"a preview that forgets the mandate fails", func(in generation.EstimateIn) generation.Estimate {
			e := covered(in)
			e.Mandate = nil
			return e
		}, "internal", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &stub{preview: func(in generation.EstimateIn) (generation.Estimate, error) { return tc.preview(in), nil }}
			_, run := withStub(t, s)
			if run.Reason != tc.reason || s.previews != tc.calls {
				t.Fatalf("run = %+v after %d previews, want %s after %d", run, s.previews, tc.reason, tc.calls)
			}
		})
	}
}

// wrap runs the real estimate (a real estimate row the run can point at) and scripts the rest.
type wrap struct {
	Generator
	estimate func(est generation.Estimate) (generation.Estimate, error)
	create   func() (generation.Created, error)
	creates  int
}

func (w *wrap) Estimate(ctx context.Context, p auth.Principal, in generation.EstimateIn) (generation.Estimate, error) {
	est, err := w.Generator.Estimate(ctx, p, in)
	if err != nil || w.estimate == nil {
		return est, err
	}
	return w.estimate(est)
}

func (w *wrap) Create(ctx context.Context, p auth.Principal, in generation.CreateIn, enc func(generation.Created) (int, []byte, error)) (generation.Created, error) {
	w.creates++
	if w.create == nil {
		return w.Generator.Create(ctx, p, in, enc)
	}
	return w.create()
}

// TestNightShift_EstimateAndCreateRefusals maps the refusals of the one estimate and the one create.
func TestNightShift_EstimateAndCreateRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		estimate      func(est generation.Estimate) (generation.Estimate, error)
		create        func() (generation.Created, error)
		state, reason string
	}{
		{"estimate: mandate changed", func(generation.Estimate) (generation.Estimate, error) {
			return generation.Estimate{}, perr(409, "MANDATE_UNAVAILABLE", nil)
		}, nil, StateSkipped, SkipMandateChanged},
		{"estimate: unexpected", func(generation.Estimate) (generation.Estimate, error) {
			return generation.Estimate{}, perr(500, "INTERNAL", nil)
		}, nil, StateFailed, "internal"},
		{"estimate above the mandate's ceiling", func(e generation.Estimate) (generation.Estimate, error) {
			e.Cost.Ceiling = 1 << 40
			return e, nil
		}, nil, StateSkipped, SkipOverCeiling},
		{"create: caps", nil, func() (generation.Created, error) { return generation.Created{}, perr(402, "BUDGET_EXCEEDED", nil) }, StateSkipped, SkipCaps},
		{"create: mandate changed", nil, func() (generation.Created, error) { return generation.Created{}, perr(409, "MANDATE_UNAVAILABLE", nil) }, StateSkipped, SkipMandateChanged},
		{"create: stale twice", nil, func() (generation.Created, error) { return generation.Created{}, perr(409, "ESTIMATE_STALE", nil) }, StateSkipped, SkipEstimateStale},
		{"create: unexpected", nil, func() (generation.Created, error) { return generation.Created{}, errors.New("boom") }, StateFailed, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newRig(t)
			g.enable()
			g.grant(4, 3)
			w := &wrap{Generator: g.gen, estimate: tc.estimate, create: tc.create}
			g.r.o.Generation = w
			g.r.Run(t.Context(), g.key(g.night))
			run := g.run(g.night)
			if run.State != tc.state || run.Reason != tc.reason {
				t.Fatalf("run = %+v", run)
			}
			if nightBatches(g) != 0 {
				t.Fatal("a refused night made a batch")
			}
			rows := g.auditRows("nightshift:skip")
			wantStatus := "|200|"
			if tc.state == StateFailed {
				wantStatus = "|500|"
			}
			if len(rows) != 1 || !strings.Contains(rows[0], wantStatus) {
				t.Errorf("audit rows = %q, want status %s", rows, wantStatus)
			}
		})
	}
}

// TestNightShift_StaleEstimateReplansOnce: a stale estimate gets exactly one re-plan, which then succeeds with a fresh estimate.
func TestNightShift_StaleEstimateReplansOnce(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(4, 3)
	w := &wrap{Generator: g.gen}
	w.create = func() (generation.Created, error) {
		if w.creates == 1 {
			return generation.Created{}, perr(409, "ESTIMATE_STALE", nil)
		}
		return generation.Created{}, errors.New("unused")
	}
	g.r.o.Generation = &staleOnce{wrap: w, g: g}
	g.r.Run(t.Context(), g.key(g.night))
	if run := g.run(g.night); run.State != StateCreated || w.creates != 2 || nightEstimates(g) != 2 || nightBatches(g) != 1 {
		t.Fatalf("run %+v, creates %d, estimates %d, batches %d", run, w.creates, nightEstimates(g), nightBatches(g))
	}
}

// staleOnce refuses the first create as stale and lets the second through to the real service.
type staleOnce struct {
	*wrap
	g *rig
}

func (s *staleOnce) Create(ctx context.Context, p auth.Principal, in generation.CreateIn, enc func(generation.Created) (int, []byte, error)) (generation.Created, error) {
	s.creates++
	if s.creates == 1 {
		return generation.Created{}, perr(409, "ESTIMATE_STALE", nil)
	}
	return s.Generator.Create(ctx, p, in, enc)
}

// TestNightShift_Infrastructure: a broken store or authority fails the night rather than spending: the row says failed.
func TestNightShift_Infrastructure(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(2, 3)
	g.r.o.Mandates = brokenAuthority{}
	g.r.Run(t.Context(), g.key(g.night))
	if run := g.run(g.night); run.State != StateFailed || nightBatches(g) != 0 {
		t.Fatalf("run = %+v", run)
	}
	g2 := newRig(t)
	g2.enable()
	g2.grant(2, 3)
	g2.r.o.Reader = &Reader{Strengths: fakeStrengths{err: errors.New("ladder down")}}
	g2.r.Run(t.Context(), g2.key(g2.night))
	if run := g2.run(g2.night); run.State != StateFailed || nightBatches(g2) != 0 {
		t.Fatalf("run = %+v", run)
	}
}

type brokenAuthority struct{}

func (brokenAuthority) Live(context.Context, mandates.Querier, string) (*mandates.Mandate, spend.MandateUsage, error) {
	return nil, spend.MandateUsage{}, errors.New("authority down")
}
func (brokenAuthority) Active(context.Context, mandates.Querier, string) (*mandates.Mandate, error) {
	return nil, errors.New("authority down")
}

// TestNightShift_HaltBeforeCreate: a halt that lands while the night is priced stops it before anything is written.
func TestNightShift_HaltBeforeCreate(t *testing.T) {
	t.Parallel()
	var g *rig
	s := &stub{
		preview: func(in generation.EstimateIn) (generation.Estimate, error) {
			if _, err := g.r.haltNoLock(t.Context(), alice, "now"); err != nil {
				t.Error(err)
			}
			return covered(in), nil
		},
	}
	g = newRig(t)
	g.enable()
	g.grant(4, 3)
	g.r.o.Generation = s
	g.r.Run(t.Context(), g.key(g.night))
	if run := g.run(g.night); run.State != StateSkipped || run.Reason != SkipHalted {
		t.Fatalf("run = %+v", run)
	}
}

// TestRunner_OptionsAndSchedule: the defaults, the schedule wiring (time from the settings, 2 h catch-up) and the audit-less runner.
func TestRunner_OptionsAndSchedule(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	r := New(Options{Store: g.st})
	if r.o.StaleAfter != 10*time.Minute || r.o.Now == nil || r.o.NewID() == "" {
		t.Errorf("defaults: %+v", r.o)
	}
	r.audit(t.Context(), OpSkip, nil, false) // no audit store: nothing to write, no panic
	if at, on, err := g.r.At(t.Context()); err != nil || at != "01:30" || on {
		t.Errorf("At = %q %v %v, want 01:30 and off", at, on, err)
	}
	d := g.r.Daily(time.UTC)
	if d.Name != "night-shift" || d.CatchUp != 2*time.Hour || d.At == nil || d.Done == nil || d.Run == nil {
		t.Errorf("daily = %+v", d)
	}
	runs, err := g.r.Runs(t.Context(), 0)
	if err != nil || len(runs) != 0 {
		t.Errorf("runs = %v %v", runs, err)
	}
	g.enable()
	g.grant(2, 3)
	g.r.Run(t.Context(), g.key(g.night))
	if runs, _ := g.r.Runs(t.Context(), 5); len(runs) != 1 || runs[0].Night != g.night {
		t.Errorf("runs = %+v", runs)
	}
}

// TestWatch_ResumesAndStops: Watch resumes a stale night at once and again on its tick, and returns when ctx ends.
func TestWatch_ResumesAndStops(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(4, 3)
	g.r.o.AfterCreate = func() { panic("simulated crash") }
	func() {
		defer func() { _ = recover() }()
		g.r.Run(t.Context(), g.key(g.night))
	}()
	g.r.o.AfterCreate = nil
	g.advance(11 * time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { g.r.Watch(ctx, 5*time.Millisecond); close(done) }()
	if run := waitRun(t, g, g.night); run.State != StateCreated {
		t.Fatalf("run = %+v", run)
	}
	cancel()
	<-done
}

// TestHalt_CancelFailures: the halt is recorded before the cancel is tried, so a failing cancel still stops later nights, and is reported.
func TestHalt_CancelFailures(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(4, 3)
	g.r.Run(t.Context(), g.key(g.night))
	cancel := g.r.o.Cancel
	g.r.o.Cancel = nil
	s, err := g.r.Halt(t.Context(), alice, "stop")
	if err == nil || !s.Halted() {
		t.Fatalf("halt without a canceller: %+v %v", s, err)
	}
	g.r.o.Cancel = func(context.Context, auth.Principal, string, string) error { return errors.New("jobs down") }
	if _, err := g.r.Halt(t.Context(), alice, "again"); err == nil || !strings.Contains(err.Error(), "jobs down") {
		t.Fatalf("halt with a failing cancel: %v", err)
	}
	if run := g.run(g.night); run.State != StateCreated {
		t.Fatalf("a failed cancel must not mark the run halted: %+v", run)
	}
	g.r.o.Cancel = cancel
	if _, err := g.r.Halt(t.Context(), alice, "third"); err != nil {
		t.Fatal(err)
	}
	if run := g.run(g.night); run.State != StateHalted {
		t.Fatalf("run = %+v", run)
	}
}

// TestNightShift_AuditOpsRegistered: the four rows of C375.7 are registered system operations and the registration is well formed.
func TestNightShift_AuditOpsRegistered(t *testing.T) {
	t.Parallel()
	if err := audit.ValidateSystemOps(); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{OpCreate, OpSkip, OpHaltCancel, OpResume} {
		if !audit.IsSystemOp(op) || !strings.HasPrefix(op, "nightshift:") {
			t.Errorf("%s is not a registered nightshift system op", op)
		}
	}
}

// TestNightShift_DisabledWhilePriced: a shift switched off while the night is priced is recorded as `disabled`, not `halted`.
func TestNightShift_DisabledWhilePriced(t *testing.T) {
	t.Parallel()
	var g *rig
	s := &stub{
		preview: func(in generation.EstimateIn) (generation.Estimate, error) {
			if _, err := g.st.W.ExecContext(t.Context(), `UPDATE night_shift_settings SET enabled = 0 WHERE id = 1`); err != nil {
				t.Error(err)
			}
			return covered(in), nil
		},
	}
	g = newRig(t)
	g.enable()
	g.grant(4, 3)
	g.r.o.Generation = s
	g.r.Run(t.Context(), g.key(g.night))
	if run := g.run(g.night); run.State != StateSkipped || run.Reason != SkipDisabled {
		t.Fatalf("run = %+v", run)
	}
}

// TestNightShift_ResumeMissedNight: a crashed night older than the catch-up window is settled `missed`, not started in the daytime.
func TestNightShift_ResumeMissedNight(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(4, 3)
	id, won, err := g.r.claim(t.Context(), g.night)
	if err != nil || !won {
		t.Fatal(err)
	}
	_ = id
	g.advance(3 * time.Hour)
	g.r.Resume(t.Context())
	if run := g.run(g.night); run.State != StateSkipped || run.Reason != SkipMissed || nightBatches(g) != 0 {
		t.Fatalf("run = %+v, %d batches", run, nightBatches(g))
	}
}

// TestNightShift_ResumeAdoptedBatchHonoursHalt: a halt that arrives while a created batch is still unadopted cancels it at adoption.
func TestNightShift_ResumeAdoptedBatchHonoursHalt(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(4, 3)
	g.r.o.AfterCreate = func() { panic("simulated crash") }
	func() {
		defer func() { _ = recover() }()
		g.r.Run(t.Context(), g.key(g.night))
	}()
	g.r.o.AfterCreate = nil
	if _, err := g.r.haltNoLock(t.Context(), alice, "stop"); err != nil {
		t.Fatal(err)
	}
	if run := g.run(g.night); run.State != StatePlanned {
		t.Fatalf("the unadopted run was touched: %+v", run)
	}
	g.advance(11 * time.Minute)
	g.r.Resume(t.Context())
	if run := g.run(g.night); run.State != StateHalted || len(g.auditRows("nightshift:halt_cancel")) != 1 {
		t.Fatalf("run = %+v", run)
	}
}
