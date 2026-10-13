// Package backup takes, lists, retains and restores snapshots (contract C34).
// M2a-B4a freezes the signatures; M2a-B7 implements them.
package backup

import (
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"

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
