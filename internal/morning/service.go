package morning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"sync"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/schedule"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Crate states.
const (
	StateBuilding = "building"
	StateReady    = "ready"
	StateEmpty    = "empty"
	StateFailed   = "failed"
)

// Triggers.
const (
	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"
)

// Defaults of the service's timing (C380.2).
const (
	// StaleBuild is how old a building row is before it is failed as INTERRUPTED.
	StaleBuild = 30 * time.Minute
	// NightPoll is how often the scheduled run re-checks an unfinished night batch.
	NightPoll = 5 * time.Minute
	// CatchUp is how late a missed 07:00 slot still builds after a restart (D-M6N-13).
	CatchUp = 2 * time.Hour
	// SweepAt is the local time of the daily sweep.
	SweepAt = "04:30"
	// DefaultSince is the look-back of the first scheduled crate.
	defaultSince = 24 * time.Hour
	maxInclude   = 30
)

// Querier is the read side (*sql.DB and *sql.Tx satisfy it).
type Querier = interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// NightBatchFunc reports tonight's night-shift batch for a local day: its id, whether it has finished, and ok=false when there
// was none. It is nil when the night shift is absent.
type NightBatchFunc func(ctx context.Context, night string) (batchID string, finished bool, ok bool, err error)

// ServiceOptions builds a Service (Options is the planner's, C378). The package imports neither generation, spend, jobs nor the night shift (TestImportGraph_M6N): the
// night batch is injected.
type ServiceOptions struct {
	Store      *store.Store
	Data       *os.Root // the data root, for stat and removal of sides under derived/morning
	Builder    SideBuilder
	Signer     *serve.Signer
	Clock      clock.Clock // nil: the wall clock
	NewID      func() string
	Loc        *time.Location // nil: the process zone
	NightBatch NightBatchFunc
}

// Service owns every SQL statement of the Morning Crate.
type Service struct {
	o       ServiceOptions
	buildMu sync.Mutex // one build at a time, in this process
	wg      sync.WaitGroup
	bg      context.Context
	cancel  context.CancelFunc
}

// New builds the service.
func New(o ServiceOptions) *Service {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.NewID == nil {
		o.NewID = func() string { return uuid.NewV7().String() }
	}
	if o.Loc == nil {
		o.Loc = time.Local
	}
	bg, cancel := context.WithCancel(context.Background())
	return &Service{o: o, bg: bg, cancel: cancel}
}

// Close interrupts running builds (their rows become failed INTERRUPTED) and waits for them.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

// Wait blocks until every background build has finished (tests).
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) now() time.Time { return s.o.Clock.Now() }

func (s *Service) writeTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		idem.MarksFrom(ctx).Reset() // the audit row rolled back with the rest: the gate writes its own
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// completeAudit writes the gate's precomputed audit row inside tx, so the row commits with the change (none outside the gate).
func (s *Service) completeAudit(ctx context.Context, tx *sql.Tx, status int, created []string) error {
	e, ok := audit.PendingFrom(ctx)
	if !ok {
		return nil
	}
	e.Status = status
	e.Duration = max(0, s.now().Sub(e.At))
	e.CreatedIDs = created
	return audit.InsertTx(ctx, tx, e)
}

// BuildIn is the body of build_morning_crate.
type BuildIn struct {
	IncludeTrackIDs []string
	Minutes         int // 0: the setting
}

// buildSpec is everything one build needs beyond the row.
type buildSpec struct {
	Opts       Options
	Include    []string
	NightBatch string
	Seed       uint64
}

// storedOptions is options_json: what the crate was built with, kept for the record.
type storedOptions struct {
	Minutes          int      `json:"minutes"`
	SideMinutes      int      `json:"side_minutes"`
	MaxTakes         int      `json:"max_takes"`
	Anchors          int      `json:"anchors"`
	IncludeTruncated bool     `json:"include_truncated"`
	Include          []string `json:"include_track_ids"`
	NightBatch       string   `json:"night_batch,omitempty"`
}

func (sp buildSpec) json() string {
	inc := sp.Include
	if inc == nil {
		inc = []string{}
	}
	b, _ := json.Marshal(storedOptions{Minutes: sp.Opts.Minutes, SideMinutes: sp.Opts.SideMinutes, MaxTakes: sp.Opts.MaxTakes, Anchors: sp.Opts.Anchors,
		IncludeTruncated: sp.Opts.IncludeTruncated, Include: inc, NightBatch: sp.NightBatch})
	return string(b)
}

func seedOf(id string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return h.Sum64() >> 1 // 63 bits, so it round-trips through SQLite's signed integer
}

func inProgress() *problem.Error {
	return problem.E(409, "MORNING_CRATE_BUILD_IN_PROGRESS", "a morning crate is already being built", nil)
}

// Build starts a manual build and returns the new row (state building); the work runs in the background, one build at a time.
func (s *Service) Build(ctx context.Context, p auth.Principal, in BuildIn) (Crate, error) {
	if err := ownerSide(p); err != nil {
		return Crate{}, err
	}
	if len(in.IncludeTrackIDs) > maxInclude {
		return Crate{}, problem.E(422, "VALIDATION", fmt.Sprintf("at most %d takes may be included", maxInclude), map[string]any{"field": "include_track_ids"})
	}
	if in.Minutes != 0 && (in.Minutes < 5 || in.Minutes > 120) {
		return Crate{}, badSetting("minutes", "5..120")
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return Crate{}, err
	}
	spec := buildSpec{Opts: set.Options(), Include: dedupe(in.IncludeTrackIDs)}
	if in.Minutes != 0 {
		spec.Opts.Minutes = in.Minutes
	}
	if err := s.recoverStale(ctx); err != nil {
		return Crate{}, err
	}
	if !s.buildMu.TryLock() {
		return Crate{}, inProgress()
	}
	started := false
	defer func() {
		if !started {
			s.buildMu.Unlock()
		}
	}()
	id := s.o.NewID()
	spec.Seed = seedOf(id)
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM morning_crates WHERE state = 'building'`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return inProgress()
		}
		if bad, err := missingTrack(ctx, tx, spec.Include); err != nil {
			return err
		} else if bad != "" {
			return problem.E(422, "UNKNOWN_TRACK", "an included take does not exist", map[string]any{"track_id": bad})
		}
		if err := s.insertCrate(ctx, tx, id, "", TriggerManual, p.Key(), spec); err != nil {
			return err
		}
		return s.completeAudit(ctx, tx, 202, []string{id})
	})
	if err != nil {
		return Crate{}, err
	}
	started = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.buildMu.Unlock()
		s.run(s.bg, id, spec)
	}()
	return s.Get(ctx, p, id)
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func missingTrack(ctx context.Context, q Querier, ids []string) (string, error) {
	for _, id := range ids {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE id = ?`, id).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return id, nil
		}
	}
	return "", nil
}

func (s *Service) insertCrate(ctx context.Context, tx *sql.Tx, id, key, trigger, by string, spec buildSpec) error {
	var k any
	if key != "" {
		k = key
	}
	// #nosec G115 -- seedOf keeps 63 bits, so the conversion never changes the value
	res, err := tx.ExecContext(ctx, `INSERT INTO morning_crates(id, schedule_key, trigger, state, seed, options_json, created_by, created_at)
		VALUES(?,?,?,'building',?,?,?,?) ON CONFLICT(schedule_key) DO NOTHING`, id, k, trigger, int64(spec.Seed), spec.json(), by, store.FormatTime(s.now()))
	if err != nil {
		return fmt.Errorf("morning: insert crate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errAlreadyBuilt
	}
	return nil
}

var errAlreadyBuilt = errors.New("morning: the day already has its crate")

// RunScheduled is the Daily run: at most one scheduled crate per local day (the unique schedule_key makes a second process's attempt a
// no-op). It waits for tonight's batch, then builds what completed. It never spends: the crate only reads existing takes.
func (s *Service) RunScheduled(ctx context.Context, k schedule.Key) {
	set, err := s.Settings(ctx)
	if err != nil || !set.Enabled {
		return
	}
	key := "morning:" + k.Day
	if done, err := s.scheduledDone(ctx, key); err != nil || done {
		return
	}
	night := s.waitForNight(ctx, k.Day, time.Duration(set.WaitMinutes)*time.Minute)
	if ctx.Err() != nil {
		return
	}
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	if err := s.recoverStale(ctx); err != nil {
		slog.Error("morning: recover", "err", redact.Error(err))
	}
	id := s.o.NewID()
	spec := buildSpec{Opts: set.Options(), NightBatch: night, Seed: seedOf(id)}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		return s.insertCrate(ctx, tx, id, key, TriggerScheduled, auth.System("morning").Key(), spec)
	})
	if err != nil {
		if !errors.Is(err, errAlreadyBuilt) {
			slog.Error("morning: scheduled crate not started", "err", redact.Error(err))
		}
		return
	}
	s.wg.Add(1)
	defer s.wg.Done()
	s.run(ctx, id, spec)
}

func (s *Service) scheduledDone(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM morning_crates WHERE schedule_key = ?`, key).Scan(&n)
	return n > 0, err
}

// waitForNight waits, re-checking every NightPoll for at most wait, until the night batch of day has finished, and returns its id
// ("" when there was none or it could not be read).
func (s *Service) waitForNight(ctx context.Context, day string, wait time.Duration) string {
	if s.o.NightBatch == nil {
		return ""
	}
	deadline := s.now().Add(wait)
	for {
		id, finished, ok, err := s.o.NightBatch(ctx, day)
		if err != nil {
			slog.Warn("morning: night batch not readable, building without it", "err", redact.Error(err))
			return ""
		}
		if !ok {
			return ""
		}
		left := deadline.Sub(s.now())
		if finished || left <= 0 {
			return id
		}
		t := s.o.Clock.NewTimer(min(NightPoll, left))
		select {
		case <-ctx.Done():
			t.Stop()
			return id
		case <-t.C():
		}
	}
}

// Daily is the schedule of the crate: its time from the settings, catch-up within two hours, done when the day has a crate.
func (s *Service) Daily() *schedule.Daily {
	return &schedule.Daily{
		Name: "morning-crate", At: s.dailyAt, Loc: s.o.Loc, Clock: s.o.Clock, CatchUp: CatchUp,
		Done: func(ctx context.Context, k schedule.Key) (bool, error) { return s.scheduledDone(ctx, "morning:"+k.Day) },
		Run:  s.RunScheduled,
	}
}

// SweepDaily is the schedule of the sweep.
func (s *Service) SweepDaily() *schedule.Daily {
	return &schedule.Daily{
		Name: "morning-sweep", At: func(context.Context) (string, bool, error) { return SweepAt, true, nil }, Loc: s.o.Loc, Clock: s.o.Clock,
		Run: func(ctx context.Context, _ schedule.Key) {
			if _, err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
				slog.Error("morning: sweep", "err", redact.Error(err))
			}
		},
	}
}

// Recover fails every building row older than StaleBuild (a process that died mid-build) as INTERRUPTED; call it at startup.
func (s *Service) Recover(ctx context.Context) error { return s.recoverStale(ctx) }

func (s *Service) recoverStale(ctx context.Context) error {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT id, created_at FROM morning_crates WHERE state = 'building'`)
	if err != nil {
		return fmt.Errorf("morning: recover: %w", err)
	}
	var stale []string
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			_ = rows.Close()
			return err
		}
		if t, err := store.ParseInstant(at); err == nil && s.now().Sub(t) > StaleBuild {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := s.o.Store.W.ExecContext(ctx, `UPDATE morning_crates SET state = 'failed', error_code = ?, built_at = ? WHERE id = ? AND state = 'building'`,
			CodeInterrupted, store.FormatTime(s.now()), id); err != nil {
			return fmt.Errorf("morning: recover: %w", err)
		}
	}
	return nil
}
