package runpod

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

const (
	catalogTTL    = time.Hour
	wantCUDA      = "12.8"
	wantFlashboot = "FLASHBOOT"
	wantIdleMax   = 60
	jobsHost      = "api.runpod.ai"
)

// scalarLit renders a JSON number as the shortest decimal literal ("240.0" => "240").
func scalarLit(n json.Number) string {
	s := n.String()
	if strings.Contains(s, ".") && !strings.ContainsAny(s, "eE") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// endpointConfig converts an endpoint to the provider type. requestUrls pass the redactor, so the
// endpoint id never reaches the cached quote (C77.5).
func (b *Backend) endpointConfig(ep Endpoint) provider.EndpointConfig {
	c := provider.EndpointConfig{
		Name: ep.Name, Type: ep.Type, ScalerType: ep.Scaling.Type,
		WorkersMin: ep.Workers.Min, WorkersMax: ep.Workers.Max, IdleTimeoutS: ep.Workers.IdleTimeout,
		TimeoutMS: ep.TimeoutMS, GPUCount: ep.GPU.Count, MinCUDA: ep.GPU.MinCudaVersion,
		Pools: ep.GPU.Pools, ExcludedTypes: ep.GPU.ExcludedTypes, Flashboot: ep.Flashboot,
	}
	switch {
	case ep.Scaling.Type == "REQUEST_COUNT" && ep.Scaling.RequestCount != nil:
		c.ScalerValue = scalarLit(*ep.Scaling.RequestCount)
	case ep.Scaling.QueueDelay != nil:
		c.ScalerValue = scalarLit(*ep.Scaling.QueueDelay)
	}
	if _, d, ok := strings.Cut(ep.Image, "@"); ok {
		c.ImageDigest = d
	}
	if len(ep.RequestURLs) > 0 {
		c.RequestURLs = make(map[string]string, len(ep.RequestURLs))
		for k, v := range ep.RequestURLs {
			c.RequestURLs[k] = b.c.red.String(v)
		}
	}
	return c
}

// gpuCatalog returns the catalog, cached for an hour on the backend's clock.
func (b *Backend) gpuCatalog(ctx context.Context) ([]GPUType, error) {
	b.mu.Lock()
	if b.catalog != nil && b.o.Clock.Now().Sub(b.catalogT) < catalogTTL {
		g := b.catalog
		b.mu.Unlock()
		return g, nil
	}
	b.mu.Unlock()
	g, err := b.c.CatalogGPUs(ctx, KeyRead)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.catalog, b.catalogT = g, b.o.Clock.Now()
	b.mu.Unlock()
	return g, nil
}

// listPrice is the maximum list price per hour over the endpoint's allowed GPU types times gpu.count, and
// their names. Zero means unreadable: no allowed type, or a missing/zero/unparsable price for one.
func listPrice(ep Endpoint, gpus []GPUType) (money.MicroUSD, []string) {
	var best money.MicroUSD
	var names []string
	for _, g := range gpus {
		if !slices.Contains(ep.GPU.Pools, g.Pool) || slices.Contains(ep.GPU.ExcludedTypes, g.ID) {
			continue
		}
		p, err := money.ParseUSD(g.ServerlessPrice)
		if g.ServerlessPrice == "" || err != nil || p <= 0 {
			return 0, nil
		}
		best = max(best, p)
		names = append(names, g.Name)
	}
	if best == 0 {
		return 0, nil
	}
	sort.Strings(names)
	names = slices.Compact(names)
	return best * money.MicroUSD(max(ep.GPU.Count, 1)), names
}

// Quote implements provider.Backend (reconciler only). It never fails for a misconfigured endpoint: the
// returned quote carries the facts and provider.QuoteCheck turns them into the C68 reason. Errors are
// transport faults, access refusals (401/1010) and a missing endpoint.
func (b *Backend) Quote(ctx context.Context) (provider.Quote, error) {
	q := provider.Quote{Backend: provider.BackendRunPod, ObservedAt: b.o.Clock.Now()}
	ep, err := b.c.GetEndpoint(ctx, KeyRead)
	switch {
	case isCode(err, Forbidden):
		q.SweepUnreadable = true
		return q, nil
	case err != nil:
		return provider.Quote{}, endpointErr(err)
	}
	q.Endpoint = b.endpointConfig(ep)
	q.Pools = ep.GPU.Pools
	if _, err := b.c.Workers(ctx); err != nil {
		if !isCode(err, Forbidden) {
			return provider.Quote{}, endpointErr(err)
		}
		q.SweepUnreadable = true
	}
	gpus, err := b.gpuCatalog(ctx)
	switch {
	case isCode(err, Forbidden, Decode):
		return q, nil // catalog_unreadable: price stays zero
	case err != nil:
		return provider.Quote{}, err
	}
	q.ListPricePPH, q.GPUClasses = listPrice(ep, gpus)
	return q, nil
}

// QuoteCheck applies the fail-closed checks of C77.5 to a cached quote and returns the disabled reason
// ("" when usable). It never touches the network.
func (b *Backend) QuoteCheck(q provider.Quote, now time.Time) string {
	return provider.QuoteCheck(q, now, provider.GateConfig{Mode: provider.ModeRunPod, KeysLoaded: true,
		ColdStartS: b.o.ColdStartS, MaxRuntime: b.o.MaxRuntime})
}

// drift lists the endpoint fields that differ from the desired invariants (C76.4).
func (b *Backend) drift(ep Endpoint) []provider.Drift {
	var out []provider.Drift
	add := func(field, want, got string) { out = append(out, provider.Drift{Field: field, Want: want, Got: got}) }
	itoa := strconv.Itoa
	if ep.Type != "QUEUE" {
		add("type", "QUEUE", ep.Type)
	}
	if ep.Scaling.Type != "QUEUE_DELAY" {
		add("scaling.type", "QUEUE_DELAY", ep.Scaling.Type)
	}
	wantDelay := itoa(b.o.ColdStartS + 60)
	if gotDelay := ""; ep.Scaling.QueueDelay != nil {
		if gotDelay = scalarLit(*ep.Scaling.QueueDelay); gotDelay != wantDelay {
			add("scaling.queueDelay", wantDelay, gotDelay)
		}
	} else if ep.Scaling.Type == "QUEUE_DELAY" {
		add("scaling.queueDelay", wantDelay, "")
	}
	if want := b.o.MaxRuntime.Milliseconds(); ep.TimeoutMS != want {
		add("timeout", itoa(int(want)), itoa(int(ep.TimeoutMS)))
	}
	if ep.GPU.Count != 1 {
		add("gpu.count", "1", itoa(ep.GPU.Count))
	}
	if ep.GPU.MinCudaVersion != wantCUDA || len(ep.GPU.AllowedCudaVersions) > 0 {
		add("gpu.minCudaVersion", wantCUDA, ep.GPU.MinCudaVersion)
	}
	if ep.Flashboot != wantFlashboot {
		add("flashboot", wantFlashboot, ep.Flashboot)
	}
	if ep.Workers.Min != 0 {
		add("workers.min", "0", itoa(ep.Workers.Min))
	}
	if ep.Workers.Max > provider.MaxWorkers || ep.Workers.Max < 0 {
		add("workers.max", itoa(provider.MaxWorkers), itoa(ep.Workers.Max))
	}
	if ep.Workers.IdleTimeout == nil {
		add("workers.idleTimeout", "20", "")
	} else if *ep.Workers.IdleTimeout > wantIdleMax {
		add("workers.idleTimeout", "20", itoa(*ep.Workers.IdleTimeout))
	}
	for _, raw := range ep.RequestURLs {
		if u, err := url.Parse(raw); err != nil || u.Hostname() != jobsHost {
			add("requestUrls", jobsHost, "other host")
			break
		}
	}
	if d := b.o.Desired; d != nil {
		if d.Image != "" && ep.Image != d.Image {
			add("image", d.Image, ep.Image)
		}
		if !sameSet(ep.GPU.Pools, d.GPU.Pools) || !sameSet(ep.GPU.ExcludedTypes, d.GPU.ExcludedTypes) {
			add("gpu.selection", strings.Join(d.GPU.Pools, ","), strings.Join(ep.GPU.Pools, ","))
		}
		keys := make([]string, 0, len(d.Env))
		for k := range d.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ep.Env[k] != d.Env[k] {
				add("env."+k, "pinned", "differs")
			}
		}
		if _, ok := ep.Env["MM_WORKER_DEADLINE"]; ok {
			add("env.MM_WORKER_DEADLINE", "absent", "present")
		}
	}
	return out
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	sort.Strings(x)
	sort.Strings(y)
	return slices.Equal(x, y)
}
