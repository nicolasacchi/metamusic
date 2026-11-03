package transform

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// setState walks a queued transform through the legal transitions to a terminal state without a runner.
func (r *rig) finish(id, state string, cpuMS int64) {
	r.t.Helper()
	stamp := "2026-10-07T12:00:00.000Z"
	if state != StateDone && state != StateFailed && state != StateCancelled {
		r.t.Fatalf("not a terminal state: %s", state)
	}
	if state == StateCancelled {
		r.exec(`UPDATE transforms SET state='cancelled', error_code=?, cpu_ms=?, finished_at=? WHERE id=?`, ErrCancelledByUser, cpuMS, r.stamp(), id)
		return
	}
	r.exec(`UPDATE transforms SET state='running', attempts=1, owner='t', lease_expires_at=?, started_at=?, phase='preparing' WHERE id=?`, stamp, stamp, id)
	if state == StateFailed {
		r.exec(`UPDATE transforms SET state='failed', owner=NULL, lease_expires_at=NULL, error_code=?, cpu_ms=?, finished_at=? WHERE id=?`, ErrTransformFailed, cpuMS, r.stamp(), id)
		return
	}
	r.exec(`UPDATE transforms SET state='done', owner=NULL, lease_expires_at=NULL, pcm_sha256=?, cpu_ms=?, finished_at=? WHERE id=?`, librarytest.Sha("pcm/"+id), cpuMS, r.stamp(), id)
}

func (r *rig) stamp() string { return r.clock().UTC().Format("2006-01-02T15:04:05.000Z") }

// ingestTake writes the Take of a done transform through the library write path, as the runner's bridge will (B3), so the
// transform is a real Take in chains. parent is the primary input; the recipe is the transform's.
func (r *rig) ingestTake(id, kind, parent string) {
	r.t.Helper()
	var rid string
	if err := r.st.R.QueryRow(`SELECT recipe_id FROM transforms WHERE id = ?`, id).Scan(&rid); err != nil {
		r.t.Fatal(err)
	}
	bw := 16000
	nt := library.NewTake{
		ID: id, Title: "Transform take " + id[len(id)-4:], Slug: "tt_" + id[len(id)-6:], RecipeID: rid, Origin: "transform", ImportRule: "transform", TransformID: id,
		PrincipalID: "agent:t", RecordedAt: r.clock(), DurationS: 20, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 960000,
		MasterPath: "audio/masters/tt_" + id[len(id)-6:] + ".flac", MasterSHA256: librarytest.Sha("master/" + id), MasterBytes: 10,
		SourceSHA256: librarytest.Sha("src/" + id), FramesSHA256: librarytest.Sha("frames/" + id), AudioMD5: strings.Repeat("0", 32),
		Loudness: analysis.Loudness{IntegratedLUFS: -14}, Spectrum: analysis.Spectrum{BandwidthHz: &bw}, ToolVersion: "ffmpeg n9.0.1",
		ParentTrackID: parent, RootTrackID: parent, Generation: 1,
		Mutation: &library.NewMutation{Operator: kind, Params: map[string]any{"transform_id": id}},
	}
	tx, err := r.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.svcLib().InsertTake(context.Background(), tx, nt); err != nil {
		_ = tx.Rollback()
		r.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) svcLib() *library.Service { return r.svc.o.Library }

func (r *rig) view(id string) View {
	r.t.Helper()
	v, err := r.svc.Get(r.t.Context(), human, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return v
}

func (r *rig) create(p auth.Principal, q Request) CreateResult {
	r.t.Helper()
	res, err := r.svc.Create(r.t.Context(), p, q, nil)
	if err != nil {
		r.t.Fatalf("create: %v", err)
	}
	return res
}

// TestCreate_HappyPath: one transaction writes the recipe, the transform (queued), its inputs, the idempotency completion and the
// audit row; the runner is woken once; nothing else exists yet (no take, no ledger row).
func TestCreate_HappyPath(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	takes := r.count(`SELECT COUNT(*) FROM tracks`)
	ctx := r.gated(agentA, "k1")
	q := r.morphAB("")
	q.Rationale = ptrStr("a bridge")
	res, err := r.svc.Create(ctx, agentA, q, encodeJSON)
	if err != nil {
		t.Fatal(err)
	}
	v := res.View
	if res.Status != 202 || v.State != StateQueued || v.Reused || v.Restored || v.Kind != KindMorph || v.ID == "" || v.PrincipalID != "agent:tok-a" || v.PrincipalKind != "agent" {
		t.Fatalf("result %+v", res)
	}
	if len(v.Inputs) != 2 || v.Inputs[0].Role != "a" || v.Inputs[0].TrackID != r.tx("A") || v.Inputs[1].Role != "b" || v.Inputs[1].Title != "Synthetic Take XB" {
		t.Errorf("inputs %+v", v.Inputs)
	}
	if v.Rationale == nil || *v.Rationale != "a bridge" || v.ExpectedMS <= 0 || v.MaxMS < v.ExpectedMS || v.UsedMS != nil || v.Error != nil || len(v.Warnings) != 0 {
		t.Errorf("view %+v", v)
	}
	// the recipe: engine dsp, the canonical spec, recipes.hash != spec_hash
	var engine, reqJSON, hash, style, reqID string
	if err := r.st.R.QueryRow(`SELECT r.engine, r.request_json, r.hash, r.style, r.request_id FROM transforms t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, v.ID).
		Scan(&engine, &reqJSON, &hash, &style, &reqID); err != nil {
		t.Fatal(err)
	}
	if engine != "dsp" || style != "" || hash == v.SpecHash || hash != sha256Hex([]byte(reqJSON)) || reqID != "dsp-morph-"+v.SpecHash[:20] {
		t.Errorf("recipe %q %q %q", engine, hash, reqID)
	}
	var spec Spec
	if err := json.Unmarshal([]byte(reqJSON), &spec); err != nil || spec.ID != reqID || spec.Alignment == nil || len(spec.Inputs) != 2 || spec.TargetLUFS != -14 {
		t.Errorf("spec %+v (%v)", spec, err)
	}
	if got := r.count(`SELECT COUNT(*) FROM transform_inputs WHERE transform_id = ?`, v.ID); got != 2 {
		t.Errorf("%d input rows", got)
	}
	var sha string
	if err := r.st.R.QueryRow(`SELECT master_sha256 FROM transform_inputs WHERE transform_id = ? AND ord = 1`, v.ID).Scan(&sha); err != nil || sha == "" {
		t.Errorf("input hash %q %v", sha, err)
	}
	if got := r.count(`SELECT COUNT(*) FROM idempotency WHERE principal_key = 'agent:tok-a' AND key = 'k1' AND state = 'done' AND status = 202`); got != 1 {
		t.Errorf("idempotency rows %d", got)
	}
	if got := r.count(`SELECT COUNT(*) FROM audit_log WHERE op = 'create_transform' AND status = 202 AND cost_micro_usd = 0 AND idempotency = 'stored' AND created_ids_json LIKE ?`, "%"+v.ID+"%"); got != 1 {
		t.Errorf("audit rows %d", got)
	}
	if r.wakes.Load() != 1 {
		t.Errorf("Wake called %d times", r.wakes.Load())
	}
	if r.count(`SELECT COUNT(*) FROM tracks`) != takes || r.count(`SELECT COUNT(*) FROM mutations WHERE transform_id IS NOT NULL`) != 0 {
		t.Error("create wrote a take or a ledger row: that is the runner's job")
	}
	// the stored title and numbers are the plan's
	if got := r.view(v.ID); got.Title != v.Title || got.SpecHash != v.SpecHash || got.ExpectedMS != v.ExpectedMS {
		t.Errorf("get = %+v", got)
	}
}

// TestCreate_OrderOfChecks: each check fires before the ones after it (C210.1).
func TestCreate_OrderOfChecks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// 1. disabled, then tools
	r := newRig(t, func(c *config.Config, _ *Options) { c.TransformEnabled = false })
	_, err := r.svc.Create(ctx, human, req(KindGlacier, `{}`), nil)
	if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "transform" {
		t.Errorf("details %v", pe.Details)
	}
	r = newRig(t, func(_ *config.Config, o *Options) { o.ToolStatus = func() ToolStatus { return ToolStatus{FLAC: true} } })
	_, err = r.svc.Create(ctx, human, req(KindGlacier, `{}`), nil)
	if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "ffmpeg" {
		t.Errorf("details %v", pe.Details)
	}
	// a score render without soundfonts or fluidsynth is unavailable (T1b is not part of this lane)
	r = newRig(t)
	_, err = r.svc.Create(ctx, human, req(KindScoreRender, `{}`), nil)
	if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "soundfont" {
		t.Errorf("details %v", pe.Details)
	}

	// 2 before 3: a range error on a take that does not exist is a range error, not a 404
	_, err = r.svc.Create(ctx, human, req(KindGlacier, `{"source_id":"nope","factor":1}`), nil)
	wantRule(t, err, "factor", RuleRange)
	_, err = r.svc.Create(ctx, human, Request{Kind: KindGlacier, Members: map[Kind]json.RawMessage{KindMorph: json.RawMessage(`{}`)}}, nil)
	wantRule(t, err, "morph", RuleKindParamsMismatch)
	// 3 before 4: an unknown take is a 404 even where alignment would also fail; a reference take is RECIPE_UNKNOWN
	_, err = r.svc.Create(ctx, human, req(KindMorph, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":"nope"}`, r.tx("A"))), nil)
	wantProblem(t, err, 404, "NOT_FOUND")
	_, err = r.svc.Create(ctx, human, req(KindMorph, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q}`, r.tx("A"), librarytest.TrackRefFlac)), nil)
	if pe := wantProblem(t, err, 422, "RECIPE_UNKNOWN"); pe.Details["track_id"] != librarytest.TrackRefFlac {
		t.Errorf("details %v", pe.Details)
	}
	// 4 before 5: unresolved alignment is reported before a limit
	r2 := newRig(t, func(c *config.Config, _ *Options) { c.TransformMaxCPUS = 30 })
	_, err = r2.svc.Create(ctx, human, req(KindMorph, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q}`, r2.tx("A"), r2.tx("B"))), nil)
	wantProblem(t, err, 422, ErrAlignmentUnresolved)
	// 5 before 6/7/8: a too-large request is refused although the same spec... (limit exceeded wins over a full queue)
	r3 := newRig(t, func(c *config.Config, _ *Options) { c.TransformQueueMax = 1 })
	r3.create(human, glacier(r3.tx("A")))
	r3.svc.o.Config.TransformMaxOutS = 60
	_, err = r3.svc.Create(ctx, human, req(KindShepard, fmt.Sprintf(`{"source_id":%q,"loop_s":2}`, r3.tx("A"))), nil)
	wantProblem(t, err, 422, ErrTransformTooLarge)
	r3.svc.o.Config.TransformMaxOutS = 900
	// 6 before 7: with the queue full, the same spec still answers reused
	res, err := r3.svc.Create(ctx, agentA, glacier(r3.tx("A")), nil)
	if err != nil || !res.View.Reused {
		t.Fatalf("reuse with a full queue: %+v %v", res, err)
	}
	// 7 before 8: a full queue is reported before an exhausted quota
	r3.svc.o.Config.TransformAgentDailyCPUS = 0
	_, err = r3.svc.Create(ctx, agentA, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":11}`, r3.tx("A"))), nil)
	wantProblem(t, err, 429, ErrQueueFull)
	// 8: the quota, once the queue has room
	r3.svc.o.Config.TransformQueueMax = 6
	_, err = r3.svc.Create(ctx, agentA, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":11}`, r3.tx("A"))), nil)
	wantProblem(t, err, 429, ErrQuotaExceeded)
}

// TestCreate_SameSpecReuses: a live or done transform of the same spec is returned, with 202 and 200 respectively, and nothing is
// written or woken.
func TestCreate_SameSpecReuses(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	first := r.create(human, glacier(r.tx("A")))
	rows := r.tableCounts()
	again := r.create(human, glacier(r.tx("A")))
	if !again.View.Reused || again.View.ID != first.View.ID || again.Status != 202 || again.View.State != StateQueued {
		t.Fatalf("live reuse %+v", again)
	}
	if r.wakes.Load() != 1 {
		t.Errorf("reuse woke the runner: %d", r.wakes.Load())
	}
	after := r.tableCounts()
	for n, c := range rows {
		if after[n] != c && n != "audit_log" && n != "idempotency" {
			t.Errorf("table %s changed on reuse: %d -> %d", n, c, after[n])
		}
	}
	r.finish(first.View.ID, StateDone, 1200)
	done := r.create(human, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"fade_in_s":4.0}`, r.tx("A")))) // spelled differently, same spec
	if !done.View.Reused || done.View.ID != first.View.ID || done.Status != 200 || done.View.State != StateDone || done.View.Restored {
		t.Fatalf("done reuse %+v", done)
	}
	if done.View.UsedMS == nil || *done.View.UsedMS != 1200 {
		t.Errorf("used %v", done.View.UsedMS)
	}
}

// TestCreate_DiscardedDoneRestored: a done transform whose Take was discarded is restored to draft in the create transaction
// (a deterministic re-render would be identical audio), with the principal as the setter and an audit row.
func TestCreate_DiscardedDoneRestored(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	first := r.create(human, glacier(r.tx("A")))
	r.ingestTake(first.View.ID, "glacier", r.tx("A"))
	r.finish(first.View.ID, StateDone, 900)
	r.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, first.View.ID)
	res, err := r.svc.Create(r.gated(agentA, ""), agentA, glacier(r.tx("A")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.View.Reused || !res.View.Restored || res.Status != 200 || res.View.ID != first.View.ID {
		t.Fatalf("result %+v", res)
	}
	var status, by string
	if err := r.st.R.QueryRow(`SELECT status, status_set_by FROM tracks WHERE id = ?`, first.View.ID).Scan(&status, &by); err != nil || status != "draft" || by != "agent:tok-a" {
		t.Errorf("status %q by %q (%v)", status, by, err)
	}
	if got := r.count(`SELECT COUNT(*) FROM audit_log WHERE op = 'transform_restored' AND created_ids_json LIKE ?`, "%"+first.View.ID+"%"); got != 1 {
		t.Errorf("restore audit rows %d", got)
	}
	// a draft Take is not restored again, and no second transform exists
	res, _ = r.svc.Create(r.gated(agentA, ""), agentA, glacier(r.tx("A")), nil)
	if res.View.Restored || r.count(`SELECT COUNT(*) FROM transforms`) != 1 {
		t.Errorf("second create %+v", res.View)
	}
}

// TestCreate_CrossPrincipalReuse: another principal asking for a live spec receives the first one's transform, charges nothing and
// does not become its owner.
func TestCreate_CrossPrincipalReuse(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.create(agentA, glacier(r.tx("A")))
	reserved := func() int64 {
		q, err := r.svc.quotaOf(t.Context(), r.st.R, "agent")
		if err != nil {
			t.Fatal(err)
		}
		return q.ReservedMS
	}
	before := reserved()
	b := r.create(agentB, glacier(r.tx("A")))
	if !b.View.Reused || b.View.ID != a.View.ID || b.View.PrincipalID != "agent:tok-a" || b.View.PrincipalName != "agent a" {
		t.Fatalf("b = %+v", b.View)
	}
	if reserved() != before || r.count(`SELECT COUNT(*) FROM transforms`) != 1 {
		t.Error("the second principal was charged or created a row")
	}
	// the owner may cancel; the other agent may not
	_, err := r.svc.Cancel(t.Context(), agentB, a.View.ID)
	if pe := wantProblem(t, err, 403, "FORBIDDEN"); pe.Details["owner"] != "agent:tok-a" {
		t.Errorf("details %v", pe.Details)
	}
}

// TestCancel_CrossPrincipalReuse: the reuser's wait ends cancelled when the owner cancels, and the reuser may then create again.
func TestCancel_CrossPrincipalReuse(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.create(agentA, glacier(r.tx("A")))
	b := r.create(agentB, glacier(r.tx("A")))
	if b.View.ID != a.View.ID {
		t.Fatal("not reused")
	}
	done := make(chan WaitResult, 1)
	go func() {
		w, err := r.svc.Wait(context.Background(), agentB, a.View.ID, StateQueued, 10*time.Second)
		if err != nil {
			t.Error(err)
		}
		done <- w
	}()
	time.Sleep(50 * time.Millisecond)
	v, err := r.svc.Cancel(t.Context(), agentA, a.View.ID)
	if err != nil || v.State != StateCancelled || v.Error == nil || v.Error.Code != ErrCancelledByUser || v.UsedMS == nil || *v.UsedMS != 0 {
		t.Fatalf("cancel = %+v %v", v, err)
	}
	w := <-done
	if !w.Changed || w.TimedOut || w.View.State != StateCancelled || w.View.Error.Code != ErrCancelledByUser {
		t.Errorf("wait = %+v", w)
	}
	// B may create it again: a cancelled row does not block the live-spec index
	again := r.create(agentB, glacier(r.tx("A")))
	if again.View.Reused || again.View.ID == a.View.ID || again.View.PrincipalID != "agent:tok-b" {
		t.Errorf("again %+v", again.View)
	}
}

// TestCancel_Rules: a human cancels anyone's, a running row only gets the intent, a terminal row is returned unchanged.
func TestCancel_Rules(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	q := r.create(agentA, glacier(r.tx("A")))
	run := r.create(agentA, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":11}`, r.tx("A"))))
	r.exec(`UPDATE transforms SET state='running', attempts=1, owner='t', lease_expires_at='2026-10-07T12:00:00.000Z', started_at='2026-10-07T12:00:00.000Z' WHERE id=?`, run.View.ID)
	v, err := r.svc.Cancel(t.Context(), human, run.View.ID)
	if err != nil || v.State != StateRunning || v.CancelRequestedAt == nil {
		t.Fatalf("running cancel %+v %v", v, err)
	}
	v, err = r.svc.Cancel(t.Context(), human, q.View.ID)
	if err != nil || v.State != StateCancelled || v.FinishedAt == nil {
		t.Fatalf("queued cancel %+v %v", v, err)
	}
	v2, err := r.svc.Cancel(t.Context(), agentA, q.View.ID) // already terminal: unchanged, no error
	if err != nil || v2.State != StateCancelled || *v2.FinishedAt != *v.FinishedAt {
		t.Fatalf("terminal cancel %+v %v", v2, err)
	}
	_, err = r.svc.Cancel(t.Context(), human, "nope")
	wantProblem(t, err, 404, "NOT_FOUND")
}

// TestCreate_AfterFailureNewRow: a failed or cancelled transform does not block the same spec: a new row on the same recipe.
func TestCreate_AfterFailureNewRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	first := r.create(human, glacier(r.tx("A")))
	r.finish(first.View.ID, StateFailed, 300)
	second := r.create(human, glacier(r.tx("A")))
	if second.View.Reused || second.View.ID == first.View.ID || second.View.SpecHash != first.View.SpecHash || second.Status != 202 {
		t.Fatalf("second %+v", second.View)
	}
	if r.count(`SELECT COUNT(DISTINCT recipe_id) FROM transforms`) != 1 || r.count(`SELECT COUNT(*) FROM transforms WHERE spec_hash = ?`, first.View.SpecHash) != 2 {
		t.Error("expected two rows on one recipe")
	}
	if _, err := r.svc.Cancel(t.Context(), human, second.View.ID); err != nil {
		t.Fatal(err)
	}
	third := r.create(human, glacier(r.tx("A")))
	if third.View.Reused || third.View.ID == second.View.ID {
		t.Errorf("third %+v", third.View)
	}
	if r.wakes.Load() != 3 {
		t.Errorf("wakes %d", r.wakes.Load())
	}
}

// TestCreate_InputQualifies: inputs are lossless, known, ordinary Takes (D-T1-11). References (all the fixture's lossy imports,
// the multi-part trio included) are RECIPE_UNKNOWN, a lossy master with a known recipe is LOSSY_MASTER, a transform Take is a fine
// input, and the transform_inputs_lossless trigger is the belt behind the Go check.
func TestCreate_InputQualifies(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for name, id := range map[string]string{"reference flac": librarytest.TrackRefFlac, "lossy reference": librarytest.TrackLossy, "trio p1": r.fx.Parts[0], "trio p2": r.fx.Parts[1], "trio p3": r.fx.Parts[2]} {
		_, err := r.svc.Create(t.Context(), human, glacier(id), nil)
		if pe := wantProblem(t, err, 422, "RECIPE_UNKNOWN"); pe.Details["track_id"] != id {
			t.Errorf("%s: details %v", name, pe.Details)
		}
	}
	if r.count(`SELECT COUNT(*) FROM transforms`) != 0 {
		t.Error("a refused input left a row")
	}
	// a known recipe with a lossy master
	r.unfrozen(func() { r.exec(`UPDATE tracks SET lossy = 1 WHERE id = ?`, r.tx("NF")) })
	_, err := r.svc.Create(t.Context(), human, glacier(r.tx("NF")), nil)
	if pe := wantProblem(t, err, 422, "LOSSY_MASTER"); pe.Details["track_id"] != r.tx("NF") {
		t.Errorf("details %v", pe.Details)
	}
	// ... and the trigger refuses the same input if Go validation were skipped
	var rid string
	first := r.create(human, glacier(r.tx("A")))
	if err := r.st.R.QueryRow(`SELECT recipe_id FROM transforms WHERE id = ?`, first.View.ID).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	_, terr := r.st.W.Exec(`INSERT INTO transform_inputs(transform_id, ord, role, track_id, master_sha256) SELECT ?, 1, 'a', id, master_sha256 FROM tracks WHERE id = ?`, first.View.ID, r.tx("NF"))
	if terr == nil || !strings.Contains(terr.Error(), "lossy or reference input") {
		t.Errorf("trigger did not refuse a lossy input: %v", terr)
	}
	_, terr = r.st.W.Exec(`INSERT INTO transform_inputs(transform_id, ord, role, track_id, master_sha256) SELECT ?, 1, 'a', id, master_sha256 FROM tracks WHERE id = ?`, first.View.ID, r.fx.Parts[0])
	if terr == nil {
		t.Error("trigger did not refuse a multi-part lossy import")
	}
	// a transform Take qualifies as an input (chains)
	r.finish(first.View.ID, StateDone, 100)
	r.ingestTake(first.View.ID, "glacier", r.tx("A"))
	chained := r.create(human, req(KindEnsemble, fmt.Sprintf(`{"source_id":%q}`, first.View.ID)))
	if chained.View.State != StateQueued || chained.View.Inputs[0].TrackID != first.View.ID {
		t.Errorf("chained %+v", chained.View)
	}
	// draft-only Songs have no Take to transform, and Takes with and without a Pick are all ordinary inputs
	for _, label := range []string{"A", "B", "I", "WS", "S", "NF2", "OCT"} {
		id, ok := r.fx.Tx[label]
		if !ok {
			continue
		}
		if label == "NF2" {
			continue
		}
		if _, err := r.svc.Estimate(t.Context(), human, glacier(id)); err != nil && label != "NF" {
			t.Errorf("%s refused: %v", label, err)
		}
	}
}

// TestCreate_BinauralChain: the HRTF is never applied twice (C218.5), and a master or widen of a binaural Take warns.
func TestCreate_BinauralChain(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b1 := r.create(human, req(KindBinaural, fmt.Sprintf(`{"source_id":%q}`, r.tx("A"))))
	r.finish(b1.View.ID, StateDone, 100)
	r.ingestTake(b1.View.ID, "binaural", r.tx("A"))
	// direct parent
	_, err := r.svc.Create(t.Context(), human, req(KindBinaural, fmt.Sprintf(`{"source_id":%q}`, b1.View.ID)), nil)
	wantRule(t, err, "source_id", RuleAlreadyBinaural)
	// a two-hop ancestor: master of the binaural Take, then binaural of that
	m := r.create(human, req(KindMaster, fmt.Sprintf(`{"source_id":%q}`, b1.View.ID)))
	if !slices.Contains(codes(r.mustBuild(req(KindMaster, fmt.Sprintf(`{"source_id":%q}`, b1.View.ID))).Warnings), WarnAfterBinaural) {
		t.Error("master after binaural did not warn")
	}
	r.finish(m.View.ID, StateDone, 100)
	r.ingestTake(m.View.ID, "master", b1.View.ID)
	_, err = r.svc.Create(t.Context(), human, req(KindBinaural, fmt.Sprintf(`{"source_id":%q}`, m.View.ID)), nil)
	wantRule(t, err, "source_id", RuleAlreadyBinaural)
	// everything else chains freely
	pl := r.mustBuild(req(KindWiden, fmt.Sprintf(`{"source_id":%q}`, r.tx("B"))))
	if slices.Contains(codes(pl.Warnings), WarnAfterBinaural) {
		t.Error("a plain take warned after_binaural")
	}
}

// TestCreate_QueueFull429: one overall limit on live (queued and running) transforms; the details carry the counts.
func TestCreate_QueueFull429(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *config.Config, _ *Options) { c.TransformQueueMax = 2 })
	r.create(human, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":11}`, r.tx("A"))))
	r.create(agentA, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":12}`, r.tx("A"))))
	_, err := r.svc.Create(t.Context(), human, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":13}`, r.tx("A"))), nil)
	pe := wantProblem(t, err, 429, ErrQueueFull)
	if pe.Details["queued"] != 2 || pe.Details["running"] != 0 || pe.Details["max"] != 2 || pe.Details["retry_after_s"] == nil {
		t.Errorf("details %v", pe.Details)
	}
	// a terminal row frees a slot
	var id string
	_ = r.st.R.QueryRow(`SELECT id FROM transforms ORDER BY created_at LIMIT 1`).Scan(&id)
	r.finish(id, StateFailed, 0)
	r.create(human, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":13}`, r.tx("A"))))
}

// TestQuota_DailyCPU: live rows reserve their budget, finished rows count their measured CPU inside the rolling day, and the
// window rolls with a fake clock.
func TestQuota_DailyCPU(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// the fade length changes the spec and not the cost, so every request below has the same budget
	one := func(f int) Request {
		return req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"fade_in_s":%d}`, r.tx("A"), f))
	}
	e, err := r.svc.Estimate(t.Context(), agentA, one(10))
	if err != nil {
		t.Fatal(err)
	}
	budget := e.CPU.MaxMS
	r.svc.o.Config.TransformAgentDailyCPUS = int(2*budget/1000) + 1 // room for two reservations of about this size, not three
	a := r.create(agentA, one(10))
	used, _ := DailyCPUUsedMS(t.Context(), r.st.R, "agent", r.clock().Add(-QuotaWindow))
	if used != a.View.MaxMS {
		t.Errorf("live row reserves %d, want %d", used, a.View.MaxMS)
	}
	b := r.create(agentA, one(11))
	_, err = r.svc.Create(t.Context(), agentA, one(12), nil)
	pe := wantProblem(t, err, 429, ErrQuotaExceeded)
	if pe.Details["cap_ms"] != r.svc.capMS("agent") || pe.Details["reserved_ms"] == nil || pe.Details["resets_at"] == nil {
		t.Errorf("details %v", pe.Details)
	}
	// finishing releases the reservation and counts the measured CPU instead
	r.finish(a.View.ID, StateDone, 10)
	r.finish(b.View.ID, StateDone, 10)
	used, _ = DailyCPUUsedMS(t.Context(), r.st.R, "agent", r.clock().Add(-QuotaWindow))
	if used != 20 {
		t.Errorf("used %d, want 20", used)
	}
	r.create(agentA, one(12))
	// a day later the finished rows have left the window
	r.advance(25 * time.Hour)
	used, _ = DailyCPUUsedMS(t.Context(), r.st.R, "agent", r.clock().Add(-QuotaWindow))
	q, _ := r.svc.quotaOf(t.Context(), r.st.R, "agent")
	if used != q.ReservedMS || q.UsedMS != 0 {
		t.Errorf("rolled window: used %d reserved %d usedMS %d", used, q.ReservedMS, q.UsedMS)
	}
}

// TestQuota_AgentPoolSharedHumanUnaffected: all agent tokens draw from one agent pool, so one can starve the others, and a human
// has a pool of its own that no agent can touch (D-T1-10).
func TestQuota_AgentPoolSharedHumanUnaffected(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e, _ := r.svc.Estimate(t.Context(), agentA, glacier(r.tx("A")))
	r.svc.o.Config.TransformAgentDailyCPUS = int(e.CPU.MaxMS/1000) + 1 // exactly one reservation fits
	r.create(agentA, glacier(r.tx("A")))
	_, err := r.svc.Create(t.Context(), agentB, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":11}`, r.tx("A"))), nil)
	wantProblem(t, err, 429, ErrQuotaExceeded) // agent B is starved by agent A
	res, err := r.svc.Create(t.Context(), human, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":11}`, r.tx("A"))), nil)
	if err != nil || res.View.PrincipalKind != "human" {
		t.Fatalf("human refused: %+v %v", res, err)
	}
	// a cap of 0 means agents cannot transform at all
	r.svc.o.Config.TransformAgentDailyCPUS = 0
	_, err = r.svc.Create(t.Context(), agentB, req(KindGlacier, fmt.Sprintf(`{"source_id":%q,"factor":12}`, r.tx("A"))), nil)
	wantProblem(t, err, 429, ErrQuotaExceeded)
	// the other kinds of principal map onto the human pool; anonymous is refused
	for _, p := range []auth.Principal{{Kind: auth.KindDevice, ID: "d", Name: "phone"}, auth.CLI} {
		if pool, err := principalPool(p); err != nil || pool != "human" {
			t.Errorf("%v -> %q %v", p.Kind, pool, err)
		}
	}
	_, err = r.svc.Create(t.Context(), auth.Principal{Kind: auth.KindAnonymous}, glacier(r.tx("A")), nil)
	wantProblem(t, err, 403, "FORBIDDEN")
}

// TestQuota_CancelledQueuedReleases: a cancelled queued job never ran, so create-then-cancel loops release their reservation at
// once.
func TestQuota_CancelledQueuedReleases(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e, _ := r.svc.Estimate(t.Context(), agentA, glacier(r.tx("A")))
	r.svc.o.Config.TransformAgentDailyCPUS = int(e.CPU.MaxMS/1000) + 1
	for i := 0; i < 5; i++ {
		v := r.create(agentA, glacier(r.tx("A")))
		if _, err := r.svc.Cancel(t.Context(), agentA, v.View.ID); err != nil {
			t.Fatal(err)
		}
		q, _ := r.svc.quotaOf(t.Context(), r.st.R, "agent")
		if q.UsedMS != 0 || q.ReservedMS != 0 {
			t.Fatalf("loop %d: used %d reserved %d", i, q.UsedMS, q.ReservedMS)
		}
	}
	var n sql.NullInt64
	_ = r.st.R.QueryRow(`SELECT SUM(cpu_ms) FROM transforms WHERE state = 'cancelled'`).Scan(&n)
	if n.Int64 != 0 || r.count(`SELECT COUNT(*) FROM transforms WHERE state = 'cancelled'`) != 5 {
		t.Errorf("cancelled rows record cpu_ms %d", n.Int64)
	}
}
