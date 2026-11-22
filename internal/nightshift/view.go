package nightshift

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The read side of the night shift (M6N-N3, C377.2): what get_night_shift and preview_night_shift show. Nothing here writes.

// Derived run states (the stored `created` splits by whether its batch is still working).
const (
	ViewRunning = "running" // created, batch not finished
	ViewDone    = "done"    // created, batch finished
)

// Authority reasons of C377.2.
const (
	AuthNoMandate        = "no_mandate"
	AuthMandateScheduled = "mandate_scheduled"
)

// RunView is a stored run with what its batch did.
type RunView struct {
	Run
	State           string // planned | running | done | skipped | halted | failed
	Takes           int
	CeilingMicroUSD money.MicroUSD
	SettledMicroUSD money.MicroUSD
	MorningCrateID  string
}

// RunViews lists the newest runs (default 14) with their batch facts: takes and ceiling from the batch, settled from the ledger
// (provider overshoot, reason `excess`, is not counted), and the first Morning Crate built after the night was claimed.
func (r *Runner) RunViews(ctx context.Context, limit int) ([]RunView, error) {
	runs, err := r.Runs(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]RunView, 0, len(runs))
	for i, run := range runs {
		v := RunView{Run: run, State: run.State}
		if run.BatchID != "" {
			var bstate string
			if err := r.o.Store.R.QueryRowContext(ctx, `SELECT b.state, b.n_takes, b.ceiling_micro_usd,
				COALESCE((SELECT SUM(l.micro_usd) FROM ledger l WHERE l.batch_id = b.id AND l.kind = 'settle' AND l.reason <> 'excess'), 0)
				FROM batches b WHERE b.id = ?`, run.BatchID).Scan(&bstate, &v.Takes, &v.CeilingMicroUSD, &v.SettledMicroUSD); err != nil {
				return nil, err
			}
			if run.State == StateCreated {
				v.State = ViewRunning
				if bstate == "finished" || bstate == "rejected" || bstate == "expired" {
					v.State = ViewDone
				}
			}
			// the crate window ends where the next newer night was claimed (runs are newest first), so a later night's crate is never attributed here
			upper := "9999-12-31T23:59:59Z"
			if i > 0 {
				upper = store.FormatTime(runs[i-1].CreatedAt)
			}
			var crate sql.NullString
			if err := r.o.Store.R.QueryRowContext(ctx, `SELECT id FROM morning_crates WHERE created_at > ? AND created_at < ? AND state <> 'failed' ORDER BY created_at, id LIMIT 1`,
				store.FormatTime(run.CreatedAt), upper).Scan(&crate); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			v.MorningCrateID = crate.String
		}
		out = append(out, v)
	}
	return out, nil
}

// Authority is the mandate the night would spend under: the live one, else a scheduled one (reason mandate_scheduled), else none
// (reason no_mandate). reason is "" when a live mandate exists.
func (r *Runner) Authority(ctx context.Context) (m *mandates.Mandate, u spend.MandateUsage, reason string, err error) {
	m, u, err = r.o.Mandates.Live(ctx, r.o.Store.R, HolderKey)
	if err != nil || m != nil {
		return m, u, "", err
	}
	a, err := r.o.Mandates.Active(ctx, r.o.Store.R, HolderKey)
	if err != nil {
		return nil, u, "", err
	}
	if a != nil {
		return a, u, AuthMandateScheduled, nil
	}
	return nil, u, AuthNoMandate, nil
}

// nextSlot is the first time the configured clock reads `at` after now, in the runner's zone, and the night key (local date) it belongs to.
func (r *Runner) nextSlot(now time.Time, at string) (time.Time, string) {
	n := now.In(r.o.Loc)
	h, _ := strconv.Atoi(at[:2])
	m, _ := strconv.Atoi(at[3:])
	slot := time.Date(n.Year(), n.Month(), n.Day(), h, m, 0, 0, r.o.Loc)
	if !slot.After(n) {
		slot = time.Date(n.Year(), n.Month(), n.Day()+1, h, m, 0, 0, r.o.Loc)
	}
	return slot, slot.Format(time.DateOnly)
}

// NextRunAt is when the scheduler will next start a night: nil while the shift is disabled or halted.
func (r *Runner) NextRunAt(ctx context.Context) (*time.Time, error) {
	s, err := r.Settings(ctx)
	if err != nil || !s.Enabled || s.Halted() {
		return nil, err
	}
	slot, _ := r.nextSlot(r.o.Now(), s.At)
	return &slot, nil
}

// PlanView is tonight's plan as preview_night_shift shows it (C377.2).
type PlanView struct {
	Night   string
	Seed    string
	Items   []PlanItem
	Dropped []DroppedRoot
	Trimmed []DroppedRoot
	Quote   *PlanQuote
	// Mandate is nil when no mandate is live; Covered is false when the night would be skipped on the mandate's account.
	Mandate *PlanMandate
	// WouldSkip is the reason a night run right now would spend nothing (a Skip* constant or an Auth* reason), "" when it would create a batch.
	WouldSkip string
}

// PlanItem is one chosen take: ids, the mode, the style a style edit would apply (style tags, never lyrics) and the planner's short reason.
type PlanItem struct {
	Kind, RootTrackID, ParentTrackID, Mode, PatchStyle, Reason string
}

// DroppedRoot is a lineage the planner or the trim left out, with why.
type DroppedRoot struct{ RootTrackID, Reason string }

// PlanQuote prices the previewed night (Generation.Preview); the mandate's per-night ceiling is compared with CeilingMicroUSD.
type PlanQuote struct {
	Takes                                          int
	ExpectedMicroUSD, MaxMicroUSD, CeilingMicroUSD money.MicroUSD
}

// PlanMandate is the live mandate's verdict on the previewed night.
type PlanMandate struct {
	ID      string
	Covered bool
	Reasons []string
}

// Preview plans the coming night and prices it the way the runner does (same reader, planner and trim), without a claim, an estimate,
// a batch, an audit row or a cap check beyond Generation.Preview. It never spends and writes nothing.
func (r *Runner) Preview(ctx context.Context) (PlanView, error) {
	s, err := r.Settings(ctx)
	if err != nil {
		return PlanView{}, err
	}
	out, err := r.preview(ctx, s)
	if err != nil {
		return PlanView{}, err
	}
	// halted and disabled take precedence on every exit path (the runner skips a halted or disabled night before it looks at a mandate)
	switch {
	case s.Halted():
		out.WouldSkip = SkipHalted
	case !s.Enabled:
		out.WouldSkip = SkipDisabled
	}
	return out, nil
}

func (r *Runner) preview(ctx context.Context, s Settings) (PlanView, error) {
	now := r.o.Now()
	_, night := r.nextSlot(now, s.At)
	out := PlanView{Night: night, Seed: strconv.FormatUint(NightSeed(night), 10), Items: []PlanItem{}, Dropped: []DroppedRoot{}, Trimmed: []DroppedRoot{}}
	m, usage, reason, err := r.Authority(ctx)
	if err != nil {
		return PlanView{}, err
	}
	if m == nil || reason != "" {
		out.WouldSkip = reason
		return out, nil
	}
	lineages, err := r.o.Reader.Lineages(ctx, r.o.Store.R, m.Scope, now)
	if err != nil {
		return PlanView{}, err
	}
	in := Input{Lineages: lineages, Scope: m.Scope, ByRoot: usage.ByRoot, MaxTakes: s.MaxTakes, MinStars: s.MinStars,
		ExplorePPM: int64(s.ExplorePPM), Seed: NightSeed(night)}
	planned, dropped := Plan(in)
	doc := planDoc{Night: night}
	for _, d := range dropped {
		out.Dropped = append(out.Dropped, DroppedRoot{RootTrackID: d.RootID, Reason: d.Reason})
	}
	if len(planned) == 0 {
		out.WouldSkip = SkipNothingPlanned
		return out, nil
	}
	items, why, err := r.trim(ctx, m, planned, &doc)
	if err != nil {
		return PlanView{}, err
	}
	for _, p := range items {
		it := PlanItem{Kind: p.Kind, RootTrackID: p.RootID, Mode: p.Item.Mode, Reason: p.Reason}
		if p.Item.ParentTrackID != nil {
			it.ParentTrackID = *p.Item.ParentTrackID
		}
		if p.Item.Patch != nil && p.Item.Patch.Style != nil {
			it.PatchStyle = *p.Item.Patch.Style
		}
		out.Items = append(out.Items, it)
	}
	for _, d := range doc.Trimmed {
		out.Trimmed = append(out.Trimmed, DroppedRoot{RootTrackID: d.RootID, Reason: d.Reason})
	}
	if q := doc.Quote; q != nil {
		out.Quote = &PlanQuote{Takes: q.Takes, ExpectedMicroUSD: money.MicroUSD(q.ExpectedMicroUSD), MaxMicroUSD: money.MicroUSD(q.MaxMicroUSD), CeilingMicroUSD: money.MicroUSD(q.CeilingMicroUSD)}
	}
	out.Mandate = &PlanMandate{ID: m.ID, Covered: why == "", Reasons: []string{}}
	if why != "" {
		out.Mandate.Reasons = []string{why}
		out.WouldSkip = why
		if why == SkipCaps {
			out.Mandate.Covered = true // the cap, not the mandate, stopped it
			out.Mandate.Reasons = []string{}
		}
	}
	return out, nil
}
