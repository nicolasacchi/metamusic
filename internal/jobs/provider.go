package jobs

import (
	"context"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// ProviderOptions builds the provider-state service behind get_provider_status and
// update_provider_state (C81).
type ProviderOptions struct {
	Store *store.Store
	Gate  *provider.Gate
	Clock clock.Clock // nil => clock.Real
}

// ProviderAdmin reads and changes provider_state (arming, trip reset). Status never carries the
// endpoint id, keys, refs or worker ids.
type ProviderAdmin struct{ o ProviderOptions }

// NewProviderAdmin builds the service.
func NewProviderAdmin(o ProviderOptions) *ProviderAdmin {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	return &ProviderAdmin{o: o}
}

// ProviderStatus is the read model of C81 ProviderStatus; the lane that fills it adds fields.
type ProviderStatus struct {
	State           provider.State
	LastReconcile   *time.Time
	PricePerHour    money.MicroUSD
	QuoteObservedAt *time.Time
}

// UpdateIn is a provider-state change: arm/disarm and/or reset the open trip.
type UpdateIn struct {
	Armed     *bool
	ResetTrip bool
	Reason    string
}

// Status reads the provider status for viewer. STUB(M3-B6).
func (a *ProviderAdmin) Status(ctx context.Context, viewer auth.Principal) (ProviderStatus, error) {
	return ProviderStatus{}, ErrNotImplemented
}

// Update arms/disarms and/or resets the open trip, writing the audit row in the same transaction.
// STUB(M3-B6).
func (a *ProviderAdmin) Update(ctx context.Context, p auth.Principal, in UpdateIn) (ProviderStatus, error) {
	return ProviderStatus{}, ErrNotImplemented
}
