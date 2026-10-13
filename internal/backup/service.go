package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

const (
	heartbeatEvery = 30 * time.Second
	staleAfter     = 2 * time.Minute
	runsListed     = 30
	maxErrorLen    = 500
	// BackupDirLabel is the only name of the backup directory the API reveals.
	BackupDirLabel = "backup"
)

// Error is a failure with an HTTP status and a stable code; the API layer
// renders it as a problem.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func errInProgress() error {
	return &Error{http.StatusConflict, "BACKUP_IN_PROGRESS", "a backup is already running"}
}

// Service implements C34.
type Service struct {
	o Options

	mu      sync.Mutex
	closed  bool
	running bool
	wg      sync.WaitGroup

	base   context.Context
	cancel context.CancelFunc

	// hook is a test seam: called with the stage and a path. Stages: "started"
	// (the partial directory exists), "db" (database copied), "imports",
	// "export" and "manifest" (about to run that step; the path is the partial
	// directory) and "copied" (a fallback copy was written, before it is verified).
	hook func(stage, path string)
}

// New builds the backup service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = func() string { return uuid.NewV7().String() }
	}
	if o.Link == nil {
		o.Link = os.Link
	}
	if o.Notify == nil {
		o.Notify = notify.Nop{}
	}
	s := &Service{o: o}
	s.base, s.cancel = context.WithCancel(context.Background())
	return s
}

func (s *Service) stamp() string { return s.o.Now().UTC().Format(time.RFC3339) }

func (s *Service) callHook(stage, path string) {
	if s.hook != nil {
		s.hook(stage, path)
	}
}

// Start begins a backup asynchronously; BACKUP_IN_PROGRESS when one is running.
// The run belongs to this service: Close waits for it.
func (s *Service) Start(ctx context.Context, cause string, p auth.Principal) (Run, error) {
	run, err := s.begin(ctx, cause, p)
	if err != nil {
		return Run{}, err
	}
	go func() {
		defer s.end()
		_, _ = s.execute(s.base, run)
	}()
	return run, nil
}

// RunSync takes a backup and waits for it (CLI). A failed run is returned
// together with an error.
func (s *Service) RunSync(ctx context.Context, cause string, p auth.Principal) (Run, error) {
	run, err := s.begin(ctx, cause, p)
	if err != nil {
		return Run{}, err
	}
	defer s.end()
	return s.execute(ctx, run)
}

func validCause(c string) bool { return c == "manual" || c == "scheduled" || c == "cli" }

func (s *Service) begin(ctx context.Context, cause string, p auth.Principal) (Run, error) {
	if !validCause(cause) {
		return Run{}, &Error{http.StatusUnprocessableEntity, "VALIDATION_FAILED", "unknown backup cause"}
	}
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		return Run{}, &Error{http.StatusServiceUnavailable, "UNAVAILABLE", "backups are shutting down"}
	case s.running:
		s.mu.Unlock()
		return Run{}, errInProgress()
	}
	s.running = true
	s.wg.Add(1)
	s.mu.Unlock()
	run, err := s.insertRun(ctx, cause, p)
	if err != nil {
		s.end()
		return Run{}, err
	}
	return run, nil
}

func (s *Service) end() {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
	s.wg.Done()
}

// insertRun marks abandoned runs failed and inserts the running row in one
// transaction; the partial unique index turns a live run elsewhere into
// BACKUP_IN_PROGRESS.
func (s *Service) insertRun(ctx context.Context, cause string, p auth.Principal) (Run, error) {
	now := s.stamp()
	cut := s.o.Now().Add(-staleAfter).UTC().Format(time.RFC3339)
	id := s.o.NewID()
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE backup_runs SET state='failed', finished_at=?, error='interrupted'
		WHERE state='running' AND heartbeat_at < ?`, now, cut); err != nil {
		return Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO backup_runs(id, cause, principal_id, state, started_at, heartbeat_at)
		VALUES (?,?,?,'running',?,?)`, id, cause, p.Key(), now, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Run{}, errInProgress()
		}
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return Run{ID: id, Cause: cause, State: "running", StartedAt: now}, nil
}

// execute runs the snapshot, records the outcome and applies retention.
func (s *Service) execute(ctx context.Context, run Run) (Run, error) {
	stats, name, err := s.snapshot(ctx, run)
	fin := context.WithoutCancel(ctx)
	now := s.stamp()
	var msg string
	if err != nil {
		msg = redact.Error(errors.New(s.scrubPaths(err.Error())))
		if len(msg) > maxErrorLen {
			msg = msg[:maxErrorLen]
		}
		s.finalize(fin, run.ID, `state='failed', finished_at=?, error=?`, now, msg)
		_ = s.o.Notify.Notify(fin, notify.Message{
			Title: "Backup failed", Body: "A " + run.Cause + " backup failed. See the backups page.",
			Priority: 4, Tags: []string{"warning"},
		})
	} else {
		sj, _ := json.Marshal(stats)
		s.finalize(fin, run.ID, `state='completed', snapshot=?, finished_at=?, stats_json=?`, name, now, string(sj))
		s.applyRetention()
	}
	out, gerr := s.getRun(fin, run.ID)
	if gerr != nil {
		return Run{}, gerr
	}
	if err != nil {
		return out, &Error{http.StatusInternalServerError, "BACKUP_FAILED", msg}
	}
	return out, nil
}

// scrubPaths replaces the absolute data and backup directories (and their
// symlink-resolved forms) so run errors never carry server paths.
func (s *Service) scrubPaths(msg string) string {
	var pairs []string
	for _, d := range []struct{ dir, label string }{{s.o.BackupDir, "<backup>"}, {s.o.DataDir, "<data>"}} {
		if d.dir == "" {
			continue
		}
		pairs = append(pairs, d.dir, d.label)
		if r, err := filepath.EvalSymlinks(d.dir); err == nil && r != d.dir {
			pairs = append(pairs, r, d.label)
		}
	}
	return strings.NewReplacer(pairs...).Replace(msg)
}

func (s *Service) finalize(ctx context.Context, id, set string, args ...any) {
	q := "UPDATE backup_runs SET " + set + " WHERE id=? AND state='running'" //nolint:gosec // set is a constant at every call site
	if _, err := s.o.Store.W.ExecContext(ctx, q, append(args, id)...); err != nil {
		slog.Error("backup: recording the run outcome failed", "err", redact.Error(err))
	}
}

func (s *Service) heartbeat(ctx context.Context, id string) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = s.o.Store.W.ExecContext(ctx, `UPDATE backup_runs SET heartbeat_at=? WHERE id=? AND state='running'`, s.stamp(), id)
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

const runColumns = `id, cause, state, snapshot, started_at, finished_at, stats_json, error`

type rowScanner interface{ Scan(...any) error }

func scanRun(r rowScanner) (Run, error) {
	var (
		run              Run
		snap, fin, stats sql.NullString
		errMsg           sql.NullString
	)
	if err := r.Scan(&run.ID, &run.Cause, &run.State, &snap, &run.StartedAt, &fin, &stats, &errMsg); err != nil {
		return Run{}, err
	}
	if snap.Valid {
		run.Snapshot = &snap.String
	}
	if fin.Valid {
		run.FinishedAt = &fin.String
	}
	if errMsg.Valid {
		run.Error = &errMsg.String
	}
	if stats.Valid {
		var st RunStats
		if json.Unmarshal([]byte(stats.String), &st) == nil {
			run.Stats = &st
		}
	}
	return run, nil
}

func (s *Service) getRun(ctx context.Context, id string) (Run, error) {
	return scanRun(s.o.Store.W.QueryRowContext(ctx, `SELECT `+runColumns+` FROM backup_runs WHERE id=?`, id))
}

// List returns recent runs, snapshots on disk and the schedule.
func (s *Service) List(ctx context.Context) (Listing, error) {
	l := Listing{Runs: []Run{}, Snapshots: []Snapshot{}, Schedule: Schedule{At: "off"}, BackupDirLabel: BackupDirLabel}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT `+runColumns+` FROM backup_runs ORDER BY started_at DESC, id DESC LIMIT ?`, runsListed)
	if err != nil {
		return Listing{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return Listing{}, err
		}
		l.Runs = append(l.Runs, r)
	}
	if err := rows.Err(); err != nil {
		return Listing{}, err
	}
	ents, err := os.ReadDir(s.o.BackupDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Listing{}, err
	}
	var snaps []snap
	byName := map[string]Snapshot{}
	for _, e := range ents {
		t, n, ok := parseName(e.Name())
		if !ok || !e.IsDir() {
			continue
		}
		m, err := readManifest(filepath.Join(s.o.BackupDir, e.Name()))
		if err != nil {
			continue // not a complete snapshot
		}
		snaps = append(snaps, snap{e.Name(), t, n})
		sn := Snapshot{Name: e.Name(), CreatedAt: m.CreatedAt, Bytes: m.DB.Bytes, Files: len(m.Files) + 1, Linked: m.Link.Linked, Copied: m.Link.Copied}
		for _, f := range m.Files {
			sn.Bytes += f.Bytes
		}
		byName[e.Name()] = sn
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[j].before(snaps[i]) })
	for _, sp := range snaps {
		l.Snapshots = append(l.Snapshots, byName[sp.name])
	}
	return l, nil
}

// ScheduleFor describes the daily schedule at ("HH:MM" or "off") from now.
func ScheduleFor(at string, now time.Time, loc *time.Location) Schedule {
	sc := Schedule{At: at}
	if next := NextRun(now, at, loc); !next.IsZero() {
		s := next.UTC().Format(time.RFC3339)
		sc.NextRunAt = &s
	}
	return sc
}

// Close refuses new runs and waits for in-flight ones until ctx ends. A nil
// receiver is a no-op.
func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		s.cancel()
		return nil
	case <-ctx.Done():
	}
	s.cancel() // abort the run; it is recorded as failed
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	return fmt.Errorf("backup: closing while a run was in flight: %w", ctx.Err())
}
