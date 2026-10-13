// Package drafts owns the draft editor and JSONL upload (contract C32).
// M2a-B4a freezes the signatures; M2a-B5 implements them.
package drafts

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// NewDraft is the input of Create.
type NewDraft struct {
	Request       json.RawMessage
	Label         *string
	ParentTrackID *string
	Collection    *string
}

// DraftPatch is the input of Update; Revision is required.
type DraftPatch struct {
	Revision      int
	Request       json.RawMessage
	Label         *string // "" clears
	Status        *string // backlog | archived
	ParentTrackID *string // "" clears
}

// Upload is the input of Upload.
type Upload struct {
	JSONL         string
	Collection    *string
	ParentTrackID *string
	AllowPartial  bool
	ABCFiles      map[string]string // path -> ABC text
}

// UploadDraft names one created draft.
type UploadDraft struct {
	Line      int     `json:"line"`
	ID        string  `json:"id"`
	RequestID string  `json:"request_id"`
	Label     *string `json:"label"`
}

// UploadIssue is one error or warning of an upload line.
type UploadIssue struct {
	Line  int    `json:"line"`
	Code  string `json:"code"`
	Field string `json:"field"`
}

// UploadResult is the outcome of Upload.
type UploadResult struct {
	Created    int           `json:"created"`
	Drafts     []UploadDraft `json:"drafts" nullable:"false"`
	Errors     []UploadIssue `json:"errors" nullable:"false"`
	Warnings   []UploadIssue `json:"warnings" nullable:"false"`
	Collection *string       `json:"collection"`
}

// Service implements C32.
type Service struct {
	st    *store.Store
	lib   *library.Service
	now   func() time.Time
	newID func() string
}

// New builds the drafts service; nil now means time.Now.
func New(st *store.Store, lib *library.Service, now func() time.Time, newID func() string) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, lib: lib, now: now, newID: newID}
}

// Lint lints items with the duplicate check backed by the database.
func (s *Service) Lint(ctx context.Context, items []yue2.LintItem, batch bool) (yue2.LintReport, error) {
	// STUB(M2a-B5)
	return yue2.LintReport{}, errors.ErrUnsupported
}

// Decorate fills the lint counts of ds.
func (s *Service) Decorate(ctx context.Context, ds []library.DraftSummary) error {
	// STUB(M2a-B5): no-op until the lint rules exist
	return nil
}

// DecorateDetail fills the lint result of d.
func (s *Service) DecorateDetail(ctx context.Context, d *library.DraftDetail) error {
	// STUB(M2a-B5): no-op until the lint rules exist
	return nil
}

// Create stores a new draft.
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewDraft) (library.DraftDetail, error) {
	// STUB(M2a-B5)
	return library.DraftDetail{}, errors.ErrUnsupported
}

// Update edits a draft under optimistic locking.
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, in DraftPatch) (library.DraftDetail, error) {
	// STUB(M2a-B5)
	return library.DraftDetail{}, errors.ErrUnsupported
}

// Upload creates drafts from a JSONL document.
func (s *Service) Upload(ctx context.Context, p auth.Principal, in Upload) (UploadResult, error) {
	// STUB(M2a-B5)
	return UploadResult{}, errors.ErrUnsupported
}
