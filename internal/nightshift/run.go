package nightshift

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"uuid"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/schedule"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The audit rows of the night shift (C375.7, registered with audit.AddSystemOps). Every run and every refusal leaves one.
const (
	OpCreate     = "nightshift:create"      // the batch a night created (written inside create's transaction; cost = the ceiling)
	OpSkip       = "nightshift:skip"        // a night that spent nothing, with its reason (status 500 when it failed)
	OpHaltCancel = "nightshift:halt_cancel" // a halt that cancelled the night's unfinished batch
	OpResume     = "nightshift:resume"      // a night picked up again after a crash
)

func init() { audit.AddSystemOps(OpCreate, OpSkip, OpHaltCancel, OpResume) }

// Skip reasons of a night (the `reason` column, stable strings; C375.3 and C375.4).
const (
	SkipDisabled           = "disabled"
	SkipHalted             = "halted"
	SkipPreviousRunning    = "previous_running"
	SkipNoMandate          = "no_mandate"
	SkipMandateScheduled   = "mandate_scheduled"
	SkipMandateChanged     = "mandate_changed"
	SkipGenerationDisabled = "generation_disabled"
	SkipCaps               = "caps"
	SkipNothingPlanned     = "nothing_planned"
	SkipOverCeiling        = "over_ceiling"
	SkipEstimateStale      = "estimate_stale"
	SkipMissed             = "missed" // a redo that would start long after the night's slot
)

// trimmable are the cover reasons a smaller night can cure (C375.4); every other reason skips the night.
var trimmable = []string{mandates.ReasonBatchCeiling, mandates.ReasonBudget, mandates.ReasonPerBatchTakes}

// Generator is the slice of generation.Service the night uses: Preview prices candidate nights and writes nothing, then exactly one
// Estimate and one Create go through the ordinary spend path (D-M6N-3, D-M6N-4).
type Generator interface {
	Preview(ctx context.Context, p auth.Principal, in generation.EstimateIn) (generation.Estimate, error)
	Estimate(ctx context.Context, p auth.Principal, in generation.EstimateIn) (generation.Estimate, error)
	Create(ctx context.Context, p auth.Principal, in generation.CreateIn, encode func(generation.Created) (int, []byte, error)) (generation.Created, error)
}

// Authority is what the mandates service gives the night (C371.2).
type Authority interface {
	Live(ctx context.Context, q mandates.Querier, holderKey string) (*mandates.Mandate, spend.MandateUsage, error)
	Active(ctx context.Context, q mandates.Querier, holderKey string) (*mandates.Mandate, error)
}

// Options builds a Runner.
type Options struct {
	Store      *store.Store
	Audit      *audit.Store
	Generation Generator
	Mandates   Authority
	Reader     *Reader
	// Cancel stops a batch (jobs.Service.CancelBatch behind a func, so this package does not import jobs); used by Halt.
	Cancel func(ctx context.Context, p auth.Principal, batchID, reason string) error
	Now    func() time.Time
	NewID  func() string
	// Loc is the zone of the night's local day and clock time (the Daily's zone); the read side (next_run_at, the preview's night key) uses it. Nil is time.Local.
	Loc *time.Location
	// StaleAfter is how old a `planned` run must be before Resume takes it over (C375.2; default 10 minutes).
	StaleAfter time.Duration
	// AfterCreate is a test seam called between create's commit and the run's update (the crash window of C375.6).
	AfterCreate func()
}

// Runner is the nightly runner and the owner of every statement on the night-shift tables (C375).
type Runner struct {
	o  Options
	mu sync.Mutex // one night at a time in this process; Resume waits for a running night
}

// New builds a Runner.
func New(o Options) *Runner {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = func() string { return uuid.NewV7().String() }
	}
	if o.Loc == nil {
		o.Loc = time.Local
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = 10 * time.Minute
	}
	return &Runner{o: o}
}

// At is the schedule's time source: the configured time, and whether the shift is enabled (off by default, C372).
func (r *Runner) At(ctx context.Context) (string, bool, error) {
	s, err := readSettings(ctx, r.o.Store.R)
	if err != nil {
		return "", false, err
	}
	return s.At, s.Enabled, nil
}

// Daily wires the runner into the program's one scheduler (C375.1): 2 h of catch-up, done = a run row exists for the local day.
func (r *Runner) Daily(loc *time.Location) *schedule.Daily {
	return &schedule.Daily{Name: "night-shift", At: r.At, Loc: loc, CatchUp: 2 * time.Hour, Done: r.Done, Run: r.Run}
}

// Run is the scheduled run of the night k.Day. It claims the night first (one row per local day, whatever the number of processes) and
// never panics or returns an error: whatever happens is the night's row and its audit row.
func (r *Runner) Run(ctx context.Context, k schedule.Key) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, won, err := r.claim(ctx, k.Day)
	if err != nil {
		slog.Error("night shift: claim failed", "night", k.Day, "err", err)
		return
	}
	if !won {
		return
	}
	r.execute(ctx, id, k.Day)
}

// Resume takes over the planned runs that nothing is working on any more (C375.6): a run whose estimate was already consumed adopts
// its batch; any other redoes the night from the pricing step (the old estimate expires unconsumed and never reserved anything).
func (r *Runner) Resume(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := store.FormatTime(r.o.Now().Add(-r.o.StaleAfter))
	rows, err := r.o.Store.R.QueryContext(ctx, `SELECT `+runCols+` FROM night_shift_runs WHERE state = 'planned' AND batch_id IS NULL AND updated_at <= ? ORDER BY night`, cutoff)
	if err != nil {
		slog.Error("night shift: resume query failed", "err", err)
		return
	}
	var stale []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			slog.Error("night shift: resume scan failed", "err", err)
			break
		}
		stale = append(stale, run)
	}
	_ = rows.Close()
	for _, run := range stale {
		r.resumeOne(ctx, run)
	}
}

// resumeGrace is how long after its claim a crashed night may still be redone: the scheduler's own catch-up window.
const resumeGrace = 2 * time.Hour

func (r *Runner) resumeOne(ctx context.Context, run Run) {
	if run.EstimateID != "" {
		var batch, mandate sql.NullString
		err := r.o.Store.R.QueryRowContext(ctx, `SELECT e.consumed_batch_id, b.mandate_id FROM estimates e LEFT JOIN batches b ON b.id = e.consumed_batch_id WHERE e.id = ?`,
			run.EstimateID).Scan(&batch, &mandate)
		if err == nil && batch.Valid && mandate.Valid {
			if err := r.adopt(ctx, run.ID, batch.String, mandate.String); err != nil {
				slog.Error("night shift: adopt failed", "night", run.Night, "err", err)
				return
			}
			r.audit(ctx, OpResume, map[string]any{"night": run.Night, "adopted_batch": batch.String}, false)
			if st, err := readSettings(ctx, r.o.Store.R); err == nil && st.Halted() { // a halt arrived while the run was unadopted
				if err := r.cancelRuns(ctx); err != nil {
					slog.Error("night shift: halt cancel after adopt failed", "night", run.Night, "err", err)
				}
			}
			return
		}
	}
	if r.o.Now().Sub(run.CreatedAt) > resumeGrace { // the night's slot is long gone: nothing was spent, do not start it now
		r.audit(ctx, OpResume, map[string]any{"night": run.Night, "missed": true}, false)
		r.settle(ctx, run.ID, run.Night, StateSkipped, SkipMissed, false)
		return
	}
	r.audit(ctx, OpResume, map[string]any{"night": run.Night, "redo": true}, false)
	r.execute(ctx, run.ID, run.Night)
}

// Watch resumes stale runs now and then every interval until ctx ends.
func (r *Runner) Watch(ctx context.Context, every time.Duration) {
	r.Resume(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Resume(ctx)
		}
	}
}

// outcome of one attempt at a night.
type outcome struct {
	skip   string // a reason the night spent nothing
	failed error  // an unexpected error
	batch  string
	mandID string
}

func (r *Runner) execute(ctx context.Context, id, night string) {
	var out outcome
	for try := 0; ; try++ { // a stale estimate gets one re-plan (C375.5)
		var stale bool
		out, stale = r.attempt(ctx, id, night)
		if !stale || try >= 1 {
			if stale {
				out = outcome{skip: SkipEstimateStale}
			}
			break
		}
	}
	switch {
	case out.batch != "":
		if r.o.AfterCreate != nil {
			r.o.AfterCreate()
		}
		if err := r.adopt(ctx, id, out.batch, out.mandID); err != nil {
			slog.Error("night shift: the batch is created but the run row was not updated; Resume adopts it", "night", night, "err", err)
		}
	case out.skip != "":
		r.settle(ctx, id, night, StateSkipped, out.skip, false)
	default:
		reason := "internal"
		var pe *problem.Error
		if errors.As(out.failed, &pe) {
			reason = strings.ToLower(pe.Code)
		}
		slog.Error("night shift: the night failed", "night", night, "reason", reason, "err", out.failed)
		r.settle(ctx, id, night, StateFailed, reason, true)
	}
}

func (r *Runner) settle(ctx context.Context, id, night, state, reason string, failed bool) {
	if err := r.finish(ctx, id, state, reason); err != nil {
		slog.Error("night shift: could not record the night", "night", night, "err", err)
	}
	r.audit(ctx, OpSkip, map[string]any{"night": night, "state": state, "reason": reason}, failed)
}

// audit writes one system row; a failure is logged, never fatal (the row of the night is the record).
func (r *Runner) audit(ctx context.Context, op string, details any, failed bool) {
	if r.o.Audit == nil {
		return
	}
	e, err := audit.SystemEntry(op, r.o.Now(), details, 0, 0, failed)
	if err == nil {
		err = r.o.Audit.Insert(ctx, e)
	}
	if err != nil {
		slog.Error("night shift: audit row not written", "op", op, "err", err)
	}
}

// attempt is C375.3 to C375.5 for one night. stale reports that create refused a stale estimate (the caller re-plans once).
func (r *Runner) attempt(ctx context.Context, id, night string) (out outcome, stale bool) {
	fail := func(err error) (outcome, bool) { return outcome{failed: err}, false }
	skip := func(reason string) (outcome, bool) { return outcome{skip: reason}, false }
	q := r.o.Store.R

	// preconditions (C375.3)
	s, err := readSettings(ctx, q)
	if err != nil {
		return fail(err)
	}
	switch {
	case s.Halted():
		return skip(SkipHalted)
	case !s.Enabled:
		return skip(SkipDisabled)
	}
	if prev, err := r.unfinished(ctx, q, night); err != nil {
		return fail(err)
	} else if len(prev) > 0 {
		return skip(SkipPreviousRunning)
	}
	m, usage, err := r.o.Mandates.Live(ctx, q, HolderKey)
	if err != nil {
		return fail(err)
	}
	if m == nil {
		a, err := r.o.Mandates.Active(ctx, q, HolderKey)
		if err != nil {
			return fail(err)
		}
		if a != nil {
			return skip(SkipMandateScheduled)
		}
		return skip(SkipNoMandate)
	}

	// plan (C373)
	now := r.o.Now()
	lineages, err := r.o.Reader.Lineages(ctx, q, m.Scope, now)
	if err != nil {
		return fail(err)
	}
	in := Input{Lineages: lineages, Scope: m.Scope, ByRoot: usage.ByRoot, MaxTakes: s.MaxTakes, MinStars: s.MinStars,
		ExplorePPM: int64(s.ExplorePPM), Seed: NightSeed(night)}
	planned, dropped := Plan(in)
	doc := planDoc{Night: night, Seed: strconv.FormatUint(in.Seed, 10), Digest: digest(lineages), MandateID: m.ID}
	for _, p := range planned {
		doc.Planned = append(doc.Planned, planItemOf(p))
	}
	for _, d := range dropped {
		doc.Dropped = append(doc.Dropped, droppedDoc(d))
	}
	if len(planned) == 0 {
		r.savePlan(ctx, id, doc)
		return skip(SkipNothingPlanned)
	}

	// price and trim (C374, C375.4): Preview writes nothing
	items, why, err := r.trim(ctx, m, planned, &doc)
	if err != nil {
		var pe *problem.Error
		if errors.As(err, &pe) {
			if reason := refusal(pe); reason != "" {
				r.savePlan(ctx, id, doc)
				return skip(reason)
			}
		}
		return fail(err)
	}
	r.savePlan(ctx, id, doc)
	if why != "" {
		return skip(why)
	}

	// a halt that arrived while the night was priced still stops it before anything is written
	if s2, err := readSettings(ctx, q); err != nil {
		return fail(err)
	} else if s2.Halted() {
		return skip(SkipHalted)
	} else if !s2.Enabled {
		return skip(SkipDisabled)
	}

	// one estimate, one create (C375.5)
	exploit, explore := 0, 0
	for _, p := range items {
		if p.Kind == KindExplore {
			explore++
		} else {
			exploit++
		}
	}
	in2 := generation.EstimateIn{Items: itemsOf(items), Via: spend.ViaAPI, MandateID: m.ID}
	est, err := r.o.Generation.Estimate(ctx, Principal(), in2)
	if err != nil {
		return r.refused(err)
	}
	if est.Cost.Ceiling > m.Scope.MaxBatchCeilingMicroUSD {
		return skip(SkipOverCeiling) // belt and braces: the mandate's own per-night ceiling, compared with the estimate ceiling
	}
	if err := r.setEstimate(ctx, id, est.ID); err != nil {
		return fail(err)
	}
	entry, err := audit.SystemEntry(OpCreate, now, map[string]any{"night": night, "mandate_id": m.ID, "estimate_id": est.ID, "takes": len(items)}, 0, 0, false)
	if err != nil {
		return fail(err)
	}
	entry.Principal = Principal() // the row names the night's own principal; the transport stays runner
	cctx := audit.WithPending(ctx, entry)
	created, err := r.o.Generation.Create(cctx, Principal(), generation.CreateIn{Token: est.Token, Via: spend.ViaAPI,
		Rationale: fmt.Sprintf("night shift %s: %d exploit, %d explore", night, exploit, explore)},
		func(generation.Created) (int, []byte, error) { return 201, nil, nil })
	if err != nil {
		var pe *problem.Error
		if errors.As(err, &pe) && pe.Status == 409 && pe.Code == "ESTIMATE_STALE" {
			return outcome{}, true
		}
		return r.refused(err)
	}
	return outcome{batch: created.BatchID, mandID: m.ID}, false
}

// refused maps a refusal of estimate or create to a skip, or to a failure for anything unexpected.
func (r *Runner) refused(err error) (outcome, bool) {
	var pe *problem.Error
	if errors.As(err, &pe) {
		if reason := refusal(pe); reason != "" {
			return outcome{skip: reason}, false
		}
	}
	return outcome{failed: err}, false
}

// refusal names the skip reason of an error the night expects, "" for any other.
func refusal(pe *problem.Error) string {
	switch {
	case pe.Status == 402:
		return SkipCaps
	case pe.Status == 404 || pe.Code == "MANDATE_UNAVAILABLE":
		return SkipMandateChanged
	case pe.Code == "GENERATION_DISABLED":
		return SkipGenerationDisabled
	case pe.Code == "UNAVAILABLE":
		return "unavailable"
	case pe.Code == "ESTIMATE_STALE":
		return SkipEstimateStale
	}
	return ""
}

// trim prices the planned night with Preview and drops items from the end until the mandate covers it (C374.1, C375.4). It returns
// the final items, or a skip reason (why) when nothing fits.
func (r *Runner) trim(ctx context.Context, m *mandates.Mandate, planned []Planned, doc *planDoc) (items []Planned, why string, err error) {
	items = slices.Clone(planned)
	for len(items) > 0 {
		pv, err := r.o.Generation.Preview(ctx, Principal(), generation.EstimateIn{Items: itemsOf(items), Via: spend.ViaAPI, MandateID: m.ID})
		if err != nil {
			var pe *problem.Error
			if !errors.As(err, &pe) {
				return nil, "", err
			}
			switch {
			case pe.Status == 422 && pe.Code == "INVALID_ITEM":
				i, ok := intDetail(pe, "item")
				if !ok || i < 0 || i >= len(items) {
					return nil, "", err
				}
				doc.Trimmed = append(doc.Trimmed, droppedDoc{RootID: items[i].RootID, Reason: "invalid_item"})
				items = slices.Delete(items, i, i+1)
				continue
			case pe.Status == 409 && pe.Code == "CALLBACKS_BACKLOG":
				root, _ := pe.Details["root_track_id"].(string)
				before := len(items)
				for _, it := range items {
					if it.RootID == root {
						doc.Trimmed = append(doc.Trimmed, droppedDoc{RootID: root, Reason: "backlogged"})
					}
				}
				items = slices.DeleteFunc(items, func(p Planned) bool { return p.RootID == root })
				if root == "" || len(items) == before {
					return nil, "", err
				}
				continue
			}
			return nil, "", err
		}
		doc.Quote = &quoteDoc{Takes: pv.Takes, ExpectedMicroUSD: int64(pv.Cost.Expected), MaxMicroUSD: int64(pv.Cost.Max), CeilingMicroUSD: int64(pv.Cost.Ceiling)}
		if !pv.Budget.Fits {
			return items, SkipCaps, nil // never trimmed to squeeze under a cap
		}
		if pv.Mandate == nil {
			return nil, "", errors.New("nightshift: preview did not report the mandate")
		}
		if pv.Mandate.Covered {
			return items, "", nil
		}
		for _, reason := range pv.Mandate.Reasons {
			if !slices.Contains(trimmable, reason) {
				return items, reason, nil
			}
		}
		last := items[len(items)-1]
		doc.Trimmed = append(doc.Trimmed, droppedDoc{RootID: last.RootID, Reason: strings.Join(pv.Mandate.Reasons, ",")})
		items = items[:len(items)-1]
	}
	return nil, SkipOverCeiling, nil
}

func intDetail(pe *problem.Error, key string) (int, bool) {
	switch v := pe.Details[key].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	}
	return 0, false
}

func itemsOf(ps []Planned) []generation.Item {
	out := make([]generation.Item, len(ps))
	for i, p := range ps {
		out[i] = p.Item
	}
	return out
}

// planDoc is plan_json (C375.4): the seed, a digest of what the planner read, what it chose and refused, and the last quote. It holds
// no lyrics: only ids, modes and the planner's short reasons.
type planDoc struct {
	Night     string       `json:"night"`
	Seed      string       `json:"seed"`
	Digest    string       `json:"input_digest"`
	MandateID string       `json:"mandate_id"`
	Planned   []planItem   `json:"planned"`
	Dropped   []droppedDoc `json:"dropped,omitempty"`
	Trimmed   []droppedDoc `json:"trimmed,omitempty"`
	Quote     *quoteDoc    `json:"quote,omitempty"`
}

type planItem struct {
	Kind          string `json:"kind"`
	RootTrackID   string `json:"root_track_id"`
	ParentTrackID string `json:"parent_track_id,omitempty"`
	Mode          string `json:"mode"`
	Reason        string `json:"reason"`
}

type droppedDoc struct {
	RootID string `json:"root_track_id"`
	Reason string `json:"reason"`
}

type quoteDoc struct {
	Takes            int   `json:"takes"`
	ExpectedMicroUSD int64 `json:"expected_micro_usd"`
	MaxMicroUSD      int64 `json:"max_micro_usd"`
	CeilingMicroUSD  int64 `json:"ceiling_micro_usd"`
}

func planItemOf(p Planned) planItem {
	it := planItem{Kind: p.Kind, RootTrackID: p.RootID, Mode: p.Item.Mode, Reason: p.Reason}
	if p.Item.ParentTrackID != nil {
		it.ParentTrackID = *p.Item.ParentTrackID
	}
	return it
}

func digest(ls []Lineage) string {
	b, _ := json.Marshal(ls)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

func (r *Runner) savePlan(ctx context.Context, id string, doc planDoc) {
	b, err := json.Marshal(doc)
	if err == nil {
		err = r.setPlan(ctx, id, string(b))
	}
	if err != nil {
		slog.Error("night shift: plan not stored", "night", doc.Night, "err", err)
	}
}
