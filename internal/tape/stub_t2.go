package tape

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// STUB(L2-T2): the frozen method set of C128.1 and C128.5 to C128.7. Lane T2 deletes this file and adds service
// behaviour in export.go, runner.go, resolve.go and sweep.go; no signature changes. The methods the composition root calls
// in every process (Run, Recover, Sweep, Resolve) are harmless no-ops here; Export, Get and Brief answer
// errors.ErrUnsupported, which the API maps to 501 NOT_IMPLEMENTED.

// Export snapshots a published Record and queues (or reuses) a tape for it (C128.1).
func (s *Service) Export(ctx context.Context, p auth.Principal, recordID string, in ExportInput) (Tape, error) {
	return Tape{}, errors.ErrUnsupported
}

// Get returns one tape.
func (s *Service) Get(ctx context.Context, id string) (Tape, error) {
	return Tape{}, errors.ErrUnsupported
}

// Brief returns the newest tapes of a Record, at most limit.
func (s *Service) Brief(ctx context.Context, recordID string, limit int) ([]Brief, error) {
	return nil, errors.ErrUnsupported
}

// Run is the runner loop (serve only): it claims and renders queued tapes one at a time until ctx ends.
func (s *Service) Run(ctx context.Context) { <-ctx.Done() }

// RunOne claims and processes one queued tape; false means there was none (tests).
func (s *Service) RunOne(ctx context.Context) (bool, error) { return false, nil }

// Recover re-queues running tapes whose lease expired (startup).
func (s *Service) Recover(ctx context.Context) error { return nil }

// Resolve serves a ready tape by the sha256 of its file; any other sha is serve.ErrNotFound, so the media chain falls through.
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) {
	return serve.Object{}, serve.ErrNotFound
}

// Sweep expires ready tapes older than olderThan and removes their files and stray files; it returns the number expired.
func (s *Service) Sweep(ctx context.Context, olderThan time.Duration) (int, error) { return 0, nil }
