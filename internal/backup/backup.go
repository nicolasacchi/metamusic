// Package backup takes, lists, retains and restores snapshots (contract C34).
// M2a-B4a freezes the signatures; M2a-B7 implements them.
package backup

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/export"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options builds a Service.
type Options struct {
	Store              *store.Store
	DataDir, BackupDir string
	Exports            export.Source
	Now                func() time.Time
	NewID              func() string
	Version            string
	Link               func(oldname, newname string) error // default os.Link; tests inject EXDEV
	Notify             notify.Notifier
}

// RunStats summarises a finished run.
type RunStats struct {
	Files  int   `json:"files"`
	Bytes  int64 `json:"bytes"`
	Linked int   `json:"linked"`
	Copied int   `json:"copied"`
}

// Run is one backup run (the BackupRun of C38).
type Run struct {
	ID         string    `json:"id"`
	Cause      string    `json:"cause" enum:"manual,scheduled,cli"`
	State      string    `json:"state" enum:"running,completed,failed"`
	Snapshot   *string   `json:"snapshot"`
	StartedAt  string    `json:"started_at"`
	FinishedAt *string   `json:"finished_at"`
	Stats      *RunStats `json:"stats" doc:"Null while running"`
	Error      *string   `json:"error"`
}

// Snapshot is one complete snapshot on disk.
type Snapshot struct {
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Bytes     int64  `json:"bytes"`
	Files     int    `json:"files"`
	Linked    int    `json:"linked"`
	Copied    int    `json:"copied"`
}

// Schedule reports the daily schedule.
type Schedule struct {
	At        string  `json:"at" doc:"HH:MM in the server time zone, or off"`
	NextRunAt *string `json:"next_run_at"`
}

// Listing is the BackupListing of C38.
type Listing struct {
	Runs           []Run      `json:"runs" nullable:"false" doc:"Newest 30"`
	Snapshots      []Snapshot `json:"snapshots" nullable:"false"`
	Schedule       Schedule   `json:"schedule"`
	BackupDirLabel string     `json:"backup_dir_label" example:"backup"`
}

// Service implements C34.
type Service struct{ o Options }

// New builds the backup service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

// Start begins a backup asynchronously; BACKUP_IN_PROGRESS when one is running.
func (s *Service) Start(ctx context.Context, cause string, p auth.Principal) (Run, error) {
	// STUB(M2a-B7)
	return Run{}, errors.ErrUnsupported
}

// RunSync takes a backup and waits for it (CLI).
func (s *Service) RunSync(ctx context.Context, cause string, p auth.Principal) (Run, error) {
	// STUB(M2a-B7)
	return Run{}, errors.ErrUnsupported
}

// List returns recent runs, snapshots on disk and the schedule.
func (s *Service) List(ctx context.Context) (Listing, error) {
	// STUB(M2a-B7)
	return Listing{}, errors.ErrUnsupported
}

// Close refuses new runs and waits for in-flight ones until ctx ends. A nil
// receiver is a no-op.
func (s *Service) Close(ctx context.Context) error {
	// STUB(M2a-B7)
	return nil
}

// RestoreOptions tunes Restore.
type RestoreOptions struct {
	Link    func(oldname, newname string) error
	Now     func() time.Time
	NewID   func() string
	Version string
}

// RestoreReport is the outcome of Restore.
type RestoreReport struct {
	Files, Linked, Copied int
	SchemaFrom, SchemaTo  int
	Counts                map[string]int
}

// Restore rebuilds a data directory from a snapshot (CLI only).
func Restore(ctx context.Context, from, into string, o RestoreOptions) (RestoreReport, error) {
	// STUB(M2a-B7)
	return RestoreReport{}, errors.ErrUnsupported
}

// SnapshotName returns "<yyyymmddThhmmssZ>", then "-2", "-3", ... while taken.
func SnapshotName(t time.Time, exists func(name string) bool) string {
	// STUB(M2a-B7)
	return t.UTC().Format("20060102T150405Z")
}

// Retain splits snapshot names into those to keep and to drop; names that do
// not parse are in neither list.
func Retain(names []string, now time.Time, loc *time.Location) (keep, drop []string) {
	// STUB(M2a-B7)
	return nil, nil
}

// NextRun returns the first instant after now whose local wall clock is at ("HH:MM").
func NextRun(now time.Time, at string, loc *time.Location) time.Time {
	// STUB(M2a-B7)
	return time.Time{}
}

// Scheduler runs Run daily at At in Loc.
type Scheduler struct {
	At  string
	Loc *time.Location
	Run func(context.Context)
}

// Loop sleeps until the next run, runs, and repeats until ctx ends.
func (s *Scheduler) Loop(ctx context.Context) {
	// STUB(M2a-B7)
	<-ctx.Done()
}

// nullableObject renders T (a method-less shadow of the real type) as an inline
// object schema that may be null: Huma refuses nullable fields that point at a
// $ref object (see library/schema.go).
func nullableObject[T any](r huma.Registry) *huma.Schema {
	cp := *huma.SchemaFromType(r, reflect.TypeFor[T]())
	cp.Nullable = true
	return &cp
}

type runStatsShape RunStats

// Schema implements huma.SchemaProvider: a run's stats are null while it runs.
func (RunStats) Schema(r huma.Registry) *huma.Schema { return nullableObject[runStatsShape](r) }
