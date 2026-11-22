package nightshift

import (
	"testing"
	"time"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// viewRig is the rig on UTC so the slot arithmetic is independent of the machine's zone.
func viewRig(t *testing.T) *rig {
	t.Helper()
	g := newRig(t)
	g.r.o.Loc = time.UTC
	return g
}

// snapshot counts the rows a read must never change.
func (g *rig) snapshot() [7]int {
	g.t.Helper()
	var s [7]int
	for i, q := range []string{`SELECT COUNT(*) FROM night_shift_runs`, `SELECT COUNT(*) FROM estimates`, `SELECT COUNT(*) FROM batches`,
		`SELECT COUNT(*) FROM jobs`, `SELECT COUNT(*) FROM ledger`, `SELECT COUNT(*) FROM audit_log`, `SELECT COUNT(*) FROM approvals`} {
		s[i] = g.count(q)
	}
	return s
}

// TestPreviewNightShift_WritesNothing: a preview shows the plan the night would make and its price, with no claim, estimate, batch,
// ledger row, audit row or approval; and the night that then runs makes exactly the previewed takes.
func TestPreviewNightShift_WritesNothing(t *testing.T) {
	t.Parallel()
	g := viewRig(t)
	g.enable()
	g.grant(2, 3)
	before := g.snapshot()
	pv, err := g.r.Preview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after := g.snapshot(); after != before {
		t.Fatalf("a preview wrote: %v -> %v", before, after)
	}
	if pv.WouldSkip != "" || pv.Mandate == nil || !pv.Mandate.Covered || pv.Quote == nil || len(pv.Items) == 0 || pv.Quote.Takes != len(pv.Items) || pv.Night == "" || pv.Seed == "" {
		t.Fatalf("preview = %+v", pv)
	}
	if pv.Night != "2026-10-07" { // the fixture clock reads 2026-10-06 12:00 UTC, past tonight's slot
		t.Errorf("night = %s", pv.Night)
	}
	g.r.Run(t.Context(), g.key(pv.Night))
	run := g.run(pv.Night)
	if run.State != StateCreated {
		t.Fatalf("run = %+v", run)
	}
	if n := g.count(`SELECT n_takes FROM batches WHERE id = ?`, run.BatchID); n != len(pv.Items) {
		t.Errorf("the night made %d takes, the preview showed %d", n, len(pv.Items))
	}
}

// TestPreviewNightShift_WouldSkip: without a live mandate, when halted and when disabled the preview says why a night would do nothing.
func TestPreviewNightShift_WouldSkip(t *testing.T) {
	t.Parallel()
	g := viewRig(t)
	pv, err := g.r.Preview(t.Context())
	if err != nil || pv.WouldSkip != AuthNoMandate || pv.Mandate != nil || pv.Quote != nil || pv.Items == nil {
		t.Fatalf("no mandate: %+v %v", pv, err)
	}
	g.grant(2, 3)
	if pv, err = g.r.Preview(t.Context()); err != nil || pv.WouldSkip != SkipDisabled || len(pv.Items) == 0 {
		t.Fatalf("disabled: %+v %v", pv, err)
	}
	g.enable()
	if _, err := g.r.Halt(t.Context(), alice, "stop"); err != nil {
		t.Fatal(err)
	}
	if pv, err = g.r.Preview(t.Context()); err != nil || pv.WouldSkip != SkipHalted {
		t.Fatalf("halted: %+v %v", pv, err)
	}
}

// TestPreviewNightShift_TrimsToMandate: a mandate that covers fewer takes than the planner chose trims the preview and records what was cut.
func TestPreviewNightShift_TrimsToMandate(t *testing.T) {
	t.Parallel()
	g := viewRig(t)
	g.enable()
	g.grant(2, 1) // a per-night ceiling of one take
	pv, err := g.r.Preview(t.Context())
	if err != nil || len(pv.Items) != 1 || len(pv.Trimmed) == 0 || pv.Mandate == nil || !pv.Mandate.Covered || pv.WouldSkip != "" {
		t.Fatalf("preview = %+v %v", pv, err)
	}
}

// TestPreviewNightShift_NothingPlanned: a lineage filter nothing passes previews as nothing_planned with the planner's refusals.
func TestPreviewNightShift_NothingPlanned(t *testing.T) {
	t.Parallel()
	g := viewRig(t)
	g.enable()
	c := g.ceilingOfOne()
	if _, err := g.ms.Create(t.Context(), alice, mandates.CreateInput{Holder: mandates.HolderNightShift, Title: "picky", Budget: 4 * c, ValidUntil: lt.FixedNow.Add(24 * time.Hour),
		Scope: mandates.Scope{Modes: []string{"new_seed"}, Engines: []string{"yue2"}, MinStars: 5, RootTrackIDs: []string{lt.TrackCitta}, MaxTakesPerLineage: 5,
			MaxTakesPerBatch: 2, MaxBatchCeilingMicroUSD: 2 * c, MaxBatchesPerDay: 1}}); err != nil {
		t.Fatal(err)
	}
	pv, err := g.r.Preview(t.Context())
	if err != nil || pv.WouldSkip != SkipNothingPlanned || len(pv.Dropped) == 0 || pv.Quote != nil {
		t.Fatalf("preview = %+v %v", pv, err)
	}
}

// TestNextRunAt: the next slot is today's when it is still ahead, tomorrow's when it has passed, and null while disabled or halted.
func TestNextRunAt(t *testing.T) {
	t.Parallel()
	g := viewRig(t)
	if at, err := g.r.NextRunAt(t.Context()); err != nil || at != nil {
		t.Fatalf("disabled: %v %v", at, err)
	}
	g.enable()
	slot, night := g.r.nextSlot(g.clock(), "01:30")
	at, err := g.r.NextRunAt(t.Context())
	if err != nil || at == nil || !at.Equal(slot) || !at.After(g.clock()) || at.Sub(g.clock()) > 24*time.Hour || night != slot.Format(time.DateOnly) {
		t.Fatalf("next = %v (slot %v, now %v) %v", at, slot, g.clock(), err)
	}
	// just before and just after the slot
	day := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	if s, n := g.r.nextSlot(day.Add(time.Hour), "01:30"); s != day.Add(90*time.Minute) || n != "2026-10-15" {
		t.Errorf("before the slot: %v %s", s, n)
	}
	if s, n := g.r.nextSlot(day.Add(90*time.Minute), "01:30"); s != day.Add(24*time.Hour+90*time.Minute) || n != "2026-10-16" {
		t.Errorf("at the slot: %v %s", s, n)
	}
	if _, err := g.r.Halt(t.Context(), alice, "stop"); err != nil {
		t.Fatal(err)
	}
	if at, err := g.r.NextRunAt(t.Context()); err != nil || at != nil {
		t.Fatalf("halted: %v %v", at, err)
	}
}

// TestGetNightShift_DerivedRunStates: a stored created night reads as running until its batch finishes and done after; skipped, halted
// and failed stay as stored; takes, ceiling and settled come from the batch and its ledger rows, never from the plan.
func TestGetNightShift_DerivedRunStates(t *testing.T) {
	t.Parallel()
	g := viewRig(t)
	g.enable()
	g.grant(4, 3)
	g.r.Run(t.Context(), g.key("2026-10-15"))
	created := g.run("2026-10-15")
	g.r.Run(t.Context(), g.key("2026-10-16")) // the first batch is unfinished: skipped previous_running
	views, err := g.r.RunViews(t.Context(), 0)
	if err != nil || len(views) != 2 {
		t.Fatalf("views = %+v %v", views, err)
	}
	if v := views[0]; v.Night != "2026-10-16" || v.State != StateSkipped || v.Reason != SkipPreviousRunning || v.Takes != 0 || v.BatchID != "" || v.MorningCrateID != "" {
		t.Errorf("skipped = %+v", v)
	}
	v := views[1]
	if v.Night != "2026-10-15" || v.State != ViewRunning || v.BatchID != created.BatchID || v.Takes == 0 || v.CeilingMicroUSD <= 0 || v.SettledMicroUSD != 0 {
		t.Fatalf("running = %+v", v)
	}
	// the batch finishes with settled spend; a crate is built afterwards
	if _, err := g.st.W.Exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, store.FormatTime(lt.FixedNow), created.BatchID); err != nil {
		t.Fatal(err)
	}
	var jobID string
	if err := g.st.R.QueryRow(`SELECT id FROM jobs WHERE batch_id = ? LIMIT 1`, created.BatchID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := g.st.W.Exec(`INSERT INTO ledger(at, kind, micro_usd, batch_id, job_id, principal_id, reason) VALUES (?, 'settle', 1234, ?, ?, 'agent:nightshift', 'attempt')`,
		store.FormatTime(lt.FixedNow), created.BatchID, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := g.st.W.Exec(`INSERT INTO ledger(at, kind, micro_usd, batch_id, job_id, principal_id, reason) VALUES (?, 'settle', 99, ?, ?, 'agent:nightshift', 'excess')`,
		store.FormatTime(lt.FixedNow), created.BatchID, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := g.st.W.Exec(`INSERT INTO morning_crates(id, trigger, state, seed, options_json, created_by, created_at, built_at)
		VALUES ('crate-1', 'manual', 'ready', 1, '{}', 'human:alice', ?, ?)`, store.FormatTime(lt.FixedNow.Add(time.Hour)), store.FormatTime(lt.FixedNow.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	views, err = g.r.RunViews(t.Context(), 14)
	if err != nil {
		t.Fatal(err)
	}
	if v := views[1]; v.State != ViewDone || v.SettledMicroUSD != 1234 || v.MorningCrateID != "crate-1" {
		t.Fatalf("done = %+v", v)
	}
	// a halted night stays halted
	if _, err := g.st.W.Exec(`UPDATE night_shift_runs SET state = 'halted', reason = 'halted' WHERE night = '2026-10-15'`); err != nil {
		t.Fatal(err)
	}
	if views, err = g.r.RunViews(t.Context(), 1); err != nil || len(views) != 1 || views[0].Night != "2026-10-16" {
		t.Fatalf("limit: %+v %v", views, err)
	}
}

// TestAuthority: a live mandate, a scheduled one and none.
func TestAuthority(t *testing.T) {
	t.Parallel()
	g := viewRig(t)
	if m, _, reason, err := g.r.Authority(t.Context()); err != nil || m != nil || reason != AuthNoMandate {
		t.Fatalf("none: %v %q %v", m, reason, err)
	}
	c := g.ceilingOfOne()
	m, err := g.ms.Create(t.Context(), alice, mandates.CreateInput{Holder: mandates.HolderNightShift, Title: "later", Budget: 4 * c,
		ValidFrom: new(lt.FixedNow.Add(time.Hour)), ValidUntil: lt.FixedNow.Add(24 * time.Hour),
		Scope: mandates.Scope{Modes: []string{"new_seed"}, Engines: []string{"yue2"}, MinStars: 3, MaxTakesPerLineage: 5,
			MaxTakesPerBatch: 2, MaxBatchCeilingMicroUSD: 2 * c, MaxBatchesPerDay: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _, reason, err := g.r.Authority(t.Context()); err != nil || got == nil || got.ID != m.ID || reason != AuthMandateScheduled {
		t.Fatalf("scheduled: %v %q %v", got, reason, err)
	}
	g.advance(2 * time.Hour)
	if got, _, reason, err := g.r.Authority(t.Context()); err != nil || got == nil || got.ID != m.ID || reason != "" {
		t.Fatalf("live: %v %q %v", got, reason, err)
	}
}
