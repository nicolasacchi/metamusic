// Package runpod is the RunPod REST v2 client, the serverless provider.Backend, the probe, provisioning
// and the fixture recorder (M3 C77). M3-B2 freezes the backend constructor and method set; M3-B8 and
// M3-B9 replace the STUB bodies.
package runpod

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// ErrNotImplemented marks a STUB(M3-B8) body.
var ErrNotImplemented = errors.New("runpod: not implemented (STUB(M3-B8))")

// BackendOptions builds the serverless backend.
type BackendOptions struct {
	Keys     config.RunPodKeys // the binary reads runpod.env; an agent never does
	HTTP     *http.Client
	Clock    clock.Clock
	Version  string
	MgmtBase string // tests point both bases at runpodtest; empty => production hosts
	JobsBase string
	// MaxRuntime and ColdStartS are the provisioned values the quote checks compare against (C77.5).
	MaxRuntime time.Duration
	ColdStartS int
	// RecordDir, when set, records response fixtures (MM_RUNPOD_RECORD_DIR, C77.7).
	RecordDir string
}

// Backend implements provider.Backend over the RunPod serverless API.
type Backend struct{ o BackendOptions }

var _ provider.Backend = (*Backend)(nil)

// NewBackend builds the backend.
func NewBackend(o BackendOptions) *Backend {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	return &Backend{o: o}
}

// Name implements provider.Backend.
func (b *Backend) Name() string { return provider.BackendRunPod }

// Quote implements provider.Backend. STUB(M3-B8).
func (b *Backend) Quote(ctx context.Context) (provider.Quote, error) {
	return provider.Quote{}, ErrNotImplemented
}

// Submit implements provider.Backend. STUB(M3-B8).
func (b *Backend) Submit(ctx context.Context, t provider.TakeSpec) (provider.Ref, error) {
	return provider.Ref{}, ErrNotImplemented
}

// Poll implements provider.Backend. STUB(M3-B8).
func (b *Backend) Poll(ctx context.Context, r provider.Ref) (provider.RemoteStatus, error) {
	return provider.RemoteStatus{}, ErrNotImplemented
}

// Cancel implements provider.Backend. STUB(M3-B8).
func (b *Backend) Cancel(ctx context.Context, r provider.Ref) error { return ErrNotImplemented }

// Sweep implements provider.Backend. STUB(M3-B8).
func (b *Backend) Sweep(ctx context.Context) (provider.SweepReport, error) {
	return provider.SweepReport{}, ErrNotImplemented
}

// PurgeQueue implements provider.Backend. STUB(M3-B8).
func (b *Backend) PurgeQueue(ctx context.Context) error { return ErrNotImplemented }

// ScaleToZero implements provider.Backend. STUB(M3-B8).
func (b *Backend) ScaleToZero(ctx context.Context) error { return ErrNotImplemented }

// RevertDrift implements provider.Backend. STUB(M3-B8).
func (b *Backend) RevertDrift(ctx context.Context, d []provider.Drift) error {
	return ErrNotImplemented
}

// Billing implements provider.Backend. STUB(M3-B8).
func (b *Backend) Billing(ctx context.Context, from, to time.Time) ([]provider.BillBucket, error) {
	return nil, ErrNotImplemented
}

// Capabilities implements provider.Backend: conservative until key-capabilities.json lands (M3-B8).
func (b *Backend) Capabilities() provider.Capabilities {
	return provider.Capabilities{Billing: "none"}
}
