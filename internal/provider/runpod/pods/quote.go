package pods

import (
	"context"
	"errors"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// ErrNoGPUTypes means the backend has no GPU type to price (MM_POD_GPU_TYPES is required with the backend).
var ErrNoGPUTypes = errors.New("pods: no GPU types configured")

// ImageDigest is the digest part of a pinned image reference ("sha256:<64 hex>"), or "" when the reference is not pinned. It is the
// value the worker capability gate binds opened capabilities to (provider.QuoteImageDigest).
func ImageDigest(image string) string {
	_, d, ok := strings.Cut(image, "@")
	hex, found := strings.CutPrefix(d, "sha256:")
	if !ok || !found || len(hex) != 64 {
		return ""
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return d
}

// fakeDigest is the digest the fake pod backend quotes when it is given no pinned image: the capability gate skips digest binding for a
// fake, but the quote check still needs a digest to be present.
var fakeDigest = "sha256:" + strings.Repeat("0", 64)

// Quote implements provider.Backend (the reconciler only, never a request path): the dearest price.secure over the configured types
// (the estimate prices every pod at its maximum, D-BULK-8), the type ids as GPU classes and a POD endpoint with one worker. Without the key
// the catalog is not read and the error wraps provider.ErrNotPermitted (the gate reads that as keys_absent).
func (b *Backend) Quote(ctx context.Context) (provider.Quote, error) {
	if len(b.o.GPUTypes) == 0 {
		return provider.Quote{}, ErrNoGPUTypes
	}
	if b.o.API == nil {
		return provider.Quote{}, ErrNoKey
	}
	prices, err := b.o.API.CatalogSecurePrices(ctx, b.o.GPUTypes)
	if err != nil {
		return provider.Quote{}, err
	}
	var top money.MicroUSD
	for _, t := range b.o.GPUTypes {
		p, ok := prices[t]
		if !ok || p <= 0 {
			return provider.Quote{}, &PriceError{TypeID: t}
		}
		top = max(top, p)
	}
	digest := ImageDigest(b.o.Image)
	if digest == "" && b.o.Name == provider.BackendFakePod {
		digest = fakeDigest
	}
	return provider.Quote{
		Backend: b.o.Name, ListPricePPH: top, Pools: []string{}, GPUClasses: append([]string(nil), b.o.GPUTypes...),
		Endpoint:   provider.EndpointConfig{Name: b.o.Name, Type: provider.EndpointTypePod, WorkersMax: 1, GPUCount: 1, ImageDigest: digest},
		ObservedAt: b.o.Clock.Now(),
	}, nil
}
