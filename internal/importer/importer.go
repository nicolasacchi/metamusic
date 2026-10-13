// Package importer turns an inbox of YuE2 files into a reviewed plan (the
// dry-run inventory) and applies that plan: recipes, drafts, takes (C16).
//
// The inbox is reached only through inbox.ReadOnly; ingest receives files that
// are already open. The dry-run writes nothing outside <DataDir>/import/.
package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Ingester freezes one take into the library (internal/media/ingest).
type Ingester interface {
	Ingest(ctx context.Context, r ingest.Request) (ingest.Result, error)
	// IngestLossy freezes a standalone MP3 as a lossy reference master (M2c).
	IngestLossy(ctx context.Context, r ingest.Request) (ingest.Result, error)
	Discard(r ingest.Result) error
}

// source is the read side of the inbox the importer needs; *inbox.ReadOnly
// implements it, tests substitute spies.
type source interface {
	FS() fs.FS
	OpenRegular(rel string, want inbox.FileID) (fs.File, error)
}

// Options configures an Importer.
type Options struct {
	Inbox      *inbox.ReadOnly
	InboxLabel string // goes into reports; never an absolute path. Default "inbox".
	// MappingPath is an optional operator CSV (C16.6). Empty: <DataDir>/import/mapping.csv when it exists.
	MappingPath string
	// MinAge: files modified more recently than Now-MinAge are "unsettled" and skipped.
	// 0 disables the check (the CLI passes its --min-age default of 2m).
	MinAge time.Duration
	// ReferenceUnmatched: unmatched (never ambiguous) FLAC identities become reference
	// tracks (D-M2c-2a).
	ReferenceUnmatched bool
	// ReferenceLossy: standalone MP3 candidates become lossy reference tracks (D-M2c-2b).
	ReferenceLossy bool

	Jobs      int    // concurrent ingests, default 2
	Principal string // 'cli' | 'seed-synthetic'; default "cli"
	Now       func() time.Time

	src source // tests only: replaces Inbox
}

// ApplyOptions controls Apply.
type ApplyOptions struct {
	ExpectDigest string // plan_digest of the reviewed dry-run report ("" only with Force)
	Force        bool   // apply the current plan even if it differs from ExpectDigest
}

// RunResult is what Apply reports.
type RunResult struct {
	RunID          string
	State          string // completed | failed
	ReportPath     string // DataDir-relative
	Report         *Report
	TracksImported int
	TracksFailed   int
	Errors         int
}

// Errors returned by Apply.
var (
	// ErrPlanChanged: the current plan differs from the reviewed digest (code PLAN_CHANGED).
	ErrPlanChanged = errors.New("importer: PLAN_CHANGED: the inbox plan differs from the reviewed dry-run")
	// ErrImportInProgress: another run holds a fresh heartbeat (code IMPORT_IN_PROGRESS).
	ErrImportInProgress = library.ErrImportInProgress
)

// PlanChangedError carries the plan entries that differ; it matches ErrPlanChanged.
type PlanChangedError struct {
	Expected, Actual string
	Added, Removed   []string // entries in the current plan only / the reviewed plan only (when known)
}

func (e *PlanChangedError) Error() string   { return ErrPlanChanged.Error() }
func (e *PlanChangedError) Is(t error) bool { return t == ErrPlanChanged }

// Importer builds inventories and applies them.
type Importer struct {
	st    *store.Store
	lib   *library.Service
	data  *os.Root
	ing   Ingester
	o     Options
	src   source
	newID func() string
}

// New builds an Importer. For a dry-run st and lib come from store.OpenReadOnly
// (or are nil when the database does not exist: an empty library) and ing is nil.
func New(st *store.Store, lib *library.Service, data *os.Root, ing Ingester, o Options) (*Importer, error) {
	im := &Importer{st: st, lib: lib, data: data, ing: ing, o: o}
	switch {
	case o.src != nil:
		im.src = o.src
	case o.Inbox != nil:
		im.src = o.Inbox
	default:
		return nil, errors.New("importer: no inbox")
	}
	if im.o.Jobs <= 0 {
		im.o.Jobs = 2
	}
	if im.o.InboxLabel == "" {
		im.o.InboxLabel = "inbox"
	}
	if im.o.Principal == "" {
		im.o.Principal = "cli"
	}
	if im.o.Now == nil {
		im.o.Now = time.Now
	}
	im.newID = func() string { return uuid.NewV7().String() }
	return im, nil
}

func (im *Importer) now() time.Time { return im.o.Now().UTC() }

// mappingFile loads the operator mapping, if any.
func (im *Importer) loadMapping() (*Mapping, error) {
	const def = "import/mapping.csv"
	var f fs.File
	var name string
	switch {
	case im.o.MappingPath != "":
		of, err := os.Open(im.o.MappingPath)
		if err != nil {
			return nil, fmt.Errorf("importer: open mapping: %w", err)
		}
		f, name = of, "mapping"
	case im.data != nil:
		of, err := im.data.Open(def)
		if errors.Is(err, fs.ErrNotExist) {
			return &Mapping{ByPath: map[string]*MapRow{}, BySHA: map[string]*MapRow{}, Drafts: map[string]*MapRow{}}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("importer: open mapping: %w", err)
		}
		f, name = of, def
	default:
		return &Mapping{ByPath: map[string]*MapRow{}, BySHA: map[string]*MapRow{}, Drafts: map[string]*MapRow{}}, nil
	}
	defer func() { _ = f.Close() }()
	return ParseMapping(f, im.src.FS(), name)
}
