// Package runpod is the RunPod REST v2 client, the serverless provider.Backend, the probe, provisioning
// and the fixture recorder (M3 C77). M3-B8 holds the client, the backend and the quote; M3-B9 adds the
// probe, provisioning and the recorder.
package runpod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// ErrPatchMismatch means a GET after a PATCH did not show the intended values (C77.5). The reconciler trips
// endpoint_config drift with a critical notification.
var ErrPatchMismatch = errors.New("runpod: endpoint differs from the patch after PATCH")

// MaxOutput is the largest job output a RemoteStatus carries (C73.1).
const MaxOutput = 64 << 10

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
	// RecordDir, when set, records response fixtures (MM_RUNPOD_RECORD_DIR, C77.7). The file recorder
	// itself arrives with M3-B9; wiring passes it through Recorder.
	RecordDir string
	// Recorder receives every exchange (nil: none).
	Recorder Recorder
	// ProbeJSON is the embedded key-capabilities.json. Nil or unprobed means conservative capabilities:
	// no ScaleToZero/RevertDrift, billing "none".
	ProbeJSON []byte
	// Desired, when set, extends the drift check with the image, the GPU selection and the pinned env.
	Desired *EndpointSpec
}

// Backend implements provider.Backend over the RunPod serverless API.
type Backend struct {
	o  BackendOptions
	c  *Client
	pc probeCaps

	mu       sync.Mutex
	catalog  []GPUType
	catalogT time.Time
}

var _ provider.Backend = (*Backend)(nil)

// NewBackend builds the backend.
func NewBackend(o BackendOptions) *Backend {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	c := NewClient(ClientOptions{MgmtBase: o.MgmtBase, JobsBase: o.JobsBase, HTTP: o.HTTP, UserAgent: UserAgentFor(o.Version),
		Keys: o.Keys, Clock: o.Clock, Recorder: o.Recorder})
	return &Backend{o: o, c: c, pc: parseProbe(o.ProbeJSON)}
}

// Client returns the backend's client (for the CLI and the reconciler's balance read).
func (b *Backend) Client() *Client { return b.c }

// Name implements provider.Backend.
func (b *Backend) Name() string { return provider.BackendRunPod }

// isCode reports whether err is a *Error with one of the codes.
func isCode(err error, codes ...Code) bool {
	var re *Error
	if !errors.As(err, &re) {
		return false
	}
	for _, c := range codes {
		if re.Code == c {
			return true
		}
	}
	return false
}

// endpointErr maps a 404 on an endpoint-level call to ErrEndpointNotFound; everything else passes through
// (access errors already match provider.ErrAccess).
func endpointErr(err error) error {
	if isCode(err, NotFound) {
		return fmt.Errorf("%w", provider.ErrEndpointNotFound)
	}
	return err
}

// Submit implements provider.Backend: one /run with the C73.3 input and the policy. Never retried.
func (b *Backend) Submit(ctx context.Context, t provider.TakeSpec) (provider.Ref, error) {
	if t.TimeoutS <= 0 || t.TTL <= 0 {
		return provider.Ref{}, errors.New("runpod: submit: timeout_s and ttl must be positive")
	}
	input, err := provider.BuildWorkerInput(t)
	if err != nil {
		return provider.Ref{}, fmt.Errorf("runpod: submit: building the input: %w", err)
	}
	if _, err := provider.ParseWorkerInput(input); err != nil {
		return provider.Ref{}, fmt.Errorf("runpod: submit: %w", err)
	}
	res, err := b.c.Run(ctx, input, Policy{ExecutionTimeoutMS: int64(t.TimeoutS) * 1000, TTLMS: t.TTL.Milliseconds()})
	if err != nil {
		return provider.Ref{}, err
	}
	return provider.Ref{Backend: provider.BackendRunPod, ID: res.ID}, nil
}

// mapState maps RunPod status strings (C73.2). Anything else is Unknown.
func mapState(s string) provider.RemoteState {
	switch s {
	case "IN_QUEUE":
		return provider.Queued
	case "IN_PROGRESS":
		return provider.Running
	case "COMPLETED":
		return provider.Completed
	case "FAILED":
		return provider.Failed
	case "CANCELLED":
		return provider.Cancelled
	case "TIMED_OUT":
		return provider.TimedOut
	}
	return provider.Unknown
}

// Poll implements provider.Backend. A 404 is followed by /health: a live endpoint means the job is gone
// (NotFound); a missing endpoint is a provider fault (ErrEndpointNotFound), never job loss.
func (b *Backend) Poll(ctx context.Context, r provider.Ref) (provider.RemoteStatus, error) {
	js, err := b.c.Status(ctx, r.ID)
	if isCode(err, NotFound) {
		if _, herr := b.c.Health(ctx); herr != nil {
			if isCode(herr, NotFound) {
				return provider.RemoteStatus{}, fmt.Errorf("%w", provider.ErrEndpointNotFound)
			}
			return provider.RemoteStatus{}, herr
		}
		return provider.RemoteStatus{State: provider.NotFound, Raw: "NOT_FOUND"}, nil
	}
	if err != nil {
		return provider.RemoteStatus{}, err
	}
	rs := provider.RemoteStatus{State: mapState(js.Status), Raw: js.Status, ExecutionMS: js.ExecutionMS, DelayMS: js.DelayMS,
		WorkerID: js.WorkerID, ErrorText: js.Error}
	if len(js.Output) > MaxOutput {
		rs.ErrorText = "output exceeds 64 KiB; dropped"
	} else {
		rs.Output = js.Output
	}
	return rs, nil
}

// Cancel implements provider.Backend: exactly one POST, no waiting; confirmation comes from later polls.
// A job RunPod no longer knows is already gone, which is what the caller wants.
func (b *Backend) Cancel(ctx context.Context, r provider.Ref) error {
	_, err := b.c.Cancel(ctx, r.ID)
	if isCode(err, NotFound) {
		return nil
	}
	return err
}

// PurgeQueue implements provider.Backend.
func (b *Backend) PurgeQueue(ctx context.Context) error { return b.c.PurgeQueue(ctx) }

// Sweep implements provider.Backend: workers, endpoint, drift and the Date-header skew.
func (b *Backend) Sweep(ctx context.Context) (provider.SweepReport, error) {
	ws, err := b.c.Workers(ctx)
	if err != nil {
		return provider.SweepReport{}, endpointErr(err)
	}
	ep, err := b.c.GetEndpoint(ctx, KeyRead)
	if err != nil {
		return provider.SweepReport{}, endpointErr(err)
	}
	cfg := b.endpointConfig(ep)
	rep := provider.SweepReport{Endpoint: &cfg, Drift: b.drift(ep), ObservedAt: b.o.Clock.Now()}
	for _, w := range ws {
		rep.Workers = append(rep.Workers, provider.Worker{ID: w.ID, Status: w.Status, UptimeS: w.UptimeSeconds, StartedAt: w.StartedAt,
			Image: w.Image, IsStale: w.IsStale})
	}
	// /health: the counters RunPod itself keeps. A failure other than lost access leaves Health nil (the
	// listing alone is then all there is); lost access is an access trip like any other sweep read.
	if h, herr := b.c.Health(ctx); herr == nil {
		rep.Health = &provider.Health{Idle: h.Workers["idle"], Initializing: h.Workers["initializing"], Ready: h.Workers["ready"],
			Running: h.Workers["running"], Throttled: h.Workers["throttled"], Unhealthy: h.Workers["unhealthy"],
			InQueue: h.Jobs["inQueue"], InProgress: h.Jobs["inProgress"]}
	} else if e := endpointErr(herr); errors.Is(e, provider.ErrAccess) {
		return provider.SweepReport{}, e
	}
	if d, ok := b.c.Skew(); ok {
		rep.Skew = &d
	}
	return rep, nil
}

// ---- PATCH: always complete nested objects -------------------------------------------------------------

// completePatch builds a PATCH with complete workers, gpu and scaling objects from the endpoint as read.
func completePatch(ep Endpoint) EndpointPatch {
	w := WorkersSpec{Min: ep.Workers.Min, Max: ep.Workers.Max, IdleTimeout: ep.Workers.IdleTimeout}
	g := GPUSpec{Count: ep.GPU.Count, Pools: append([]string(nil), ep.GPU.Pools...),
		ExcludedTypes: append([]string(nil), ep.GPU.ExcludedTypes...), MinCudaVersion: ep.GPU.MinCudaVersion}
	s := ScalingSpec{Type: ep.Scaling.Type}
	if ep.Scaling.QueueDelay != nil {
		if v := intOf(*ep.Scaling.QueueDelay); v != nil {
			s.QueueDelay = int(*v)
		}
	}
	if ep.Scaling.RequestCount != nil {
		if v := intOf(*ep.Scaling.RequestCount); v != nil {
			s.RequestCount = int(*v)
		}
	}
	return EndpointPatch{Workers: &w, GPU: &g, Scaling: &s}
}

// applyPatch sends p with the write key and verifies it with a GET (read key).
func (b *Backend) applyPatch(ctx context.Context, p EndpointPatch) error {
	if b.o.Keys.Write == "" {
		return provider.ErrNotPermitted
	}
	if _, err := b.c.PatchEndpoint(ctx, KeyWrite, p); err != nil {
		if isCode(err, Forbidden) {
			return fmt.Errorf("%w: the write key cannot PATCH", provider.ErrNotPermitted)
		}
		return endpointErr(err)
	}
	got, err := b.c.GetEndpoint(ctx, KeyRead)
	if err != nil {
		return endpointErr(err)
	}
	if diff := patchMismatch(p, got); len(diff) > 0 {
		return fmt.Errorf("%w: %v", ErrPatchMismatch, diff)
	}
	return nil
}

func patchMismatch(p EndpointPatch, got Endpoint) (diff []string) {
	if w := p.Workers; w != nil {
		if got.Workers.Min != w.Min {
			diff = append(diff, "workers.min")
		}
		if got.Workers.Max != w.Max {
			diff = append(diff, "workers.max")
		}
		if (w.IdleTimeout == nil) != (got.Workers.IdleTimeout == nil) || (w.IdleTimeout != nil && *w.IdleTimeout != *got.Workers.IdleTimeout) {
			diff = append(diff, "workers.idleTimeout")
		}
	}
	if g := p.GPU; g != nil {
		if got.GPU.Count != g.Count {
			diff = append(diff, "gpu.count")
		}
		if got.GPU.MinCudaVersion != g.MinCudaVersion {
			diff = append(diff, "gpu.minCudaVersion")
		}
		if !sameSet(got.GPU.Pools, g.Pools) || !sameSet(got.GPU.ExcludedTypes, g.ExcludedTypes) {
			diff = append(diff, "gpu.selection")
		}
	}
	if s := p.Scaling; s != nil {
		if got.Scaling.Type != s.Type {
			diff = append(diff, "scaling.type")
		}
		if s.QueueDelay != 0 && (got.Scaling.QueueDelay == nil || scalarLit(*got.Scaling.QueueDelay) != fmt.Sprint(s.QueueDelay)) {
			diff = append(diff, "scaling.queueDelay")
		}
	}
	if p.Timeout != nil && got.TimeoutMS != *p.Timeout {
		diff = append(diff, "timeout")
	}
	if p.Flashboot != nil && got.Flashboot != *p.Flashboot {
		diff = append(diff, "flashboot")
	}
	return diff
}

// ScaleToZero implements provider.Backend: workers.max = 0 with complete nested objects.
func (b *Backend) ScaleToZero(ctx context.Context) error {
	if b.o.Keys.Write == "" {
		return provider.ErrNotPermitted
	}
	ep, err := b.c.GetEndpoint(ctx, KeyRead)
	if err != nil {
		return endpointErr(err)
	}
	p := completePatch(ep)
	p.Workers.Max = 0
	return b.applyPatch(ctx, p)
}

// RevertDrift implements provider.Backend: sets the Want value of each reported scalar field, sending
// complete nested objects, then verifies with a GET. Fields it cannot revert are named in the error.
func (b *Backend) RevertDrift(ctx context.Context, d []provider.Drift) error {
	if b.o.Keys.Write == "" {
		return provider.ErrNotPermitted
	}
	ep, err := b.c.GetEndpoint(ctx, KeyRead)
	if err != nil {
		return endpointErr(err)
	}
	p := completePatch(ep)
	var unsupported []string
	for _, x := range d {
		if !setField(&p, x) {
			unsupported = append(unsupported, x.Field)
		}
	}
	if err := b.applyPatch(ctx, p); err != nil {
		return err
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("runpod: drift fields %v cannot be reverted by PATCH", unsupported)
	}
	return nil
}

func setField(p *EndpointPatch, x provider.Drift) bool {
	atoi := func() (int, bool) {
		var n int
		if _, err := fmt.Sscanf(x.Want, "%d", &n); err != nil || fmt.Sprint(n) != x.Want {
			return 0, false
		}
		return n, true
	}
	switch x.Field {
	case "workers.min", "workers.max", "workers.idleTimeout", "gpu.count", "scaling.queueDelay":
		n, ok := atoi()
		if !ok {
			return false
		}
		switch x.Field {
		case "workers.min":
			p.Workers.Min = n
		case "workers.max":
			p.Workers.Max = n
		case "workers.idleTimeout":
			p.Workers.IdleTimeout = &n
		case "gpu.count":
			p.GPU.Count = n
		case "scaling.queueDelay":
			p.Scaling.Type, p.Scaling.QueueDelay = "QUEUE_DELAY", n
		}
	case "gpu.minCudaVersion":
		p.GPU.MinCudaVersion = x.Want
	case "scaling.type":
		if x.Want != "QUEUE_DELAY" {
			return false
		}
		p.Scaling.Type = x.Want
	case "timeout":
		n, ok := atoi()
		if !ok {
			return false
		}
		v := int64(n)
		p.Timeout = &v
	case "flashboot":
		v := x.Want
		p.Flashboot = &v
	case "image":
		v := x.Want
		p.Image = &v
	default:
		return false
	}
	return true
}

// ---- billing --------------------------------------------------------------------------------------------

// Billing implements provider.Backend: hourly buckets, bounds aligned outward to the hour.
func (b *Backend) Billing(ctx context.Context, from, to time.Time) ([]provider.BillBucket, error) {
	if b.o.Keys.Read == "" {
		return nil, provider.ErrNotPermitted
	}
	from = from.UTC().Truncate(time.Hour)
	if end := to.UTC().Truncate(time.Hour); end.Before(to) {
		to = end.Add(time.Hour)
	} else {
		to = end
	}
	recs, err := b.c.BillingServerless(ctx, BillingQuery{Start: from, End: to})
	if err != nil {
		if isCode(err, Forbidden) {
			return nil, fmt.Errorf("%w: billing is not readable with the read key", provider.ErrNotPermitted)
		}
		return nil, endpointErr(err)
	}
	out := make([]provider.BillBucket, 0, len(recs))
	for _, r := range recs {
		var bk provider.BillBucket
		bk.Start, bk.End = r.Start.UTC(), r.End.UTC()
		var err error
		if bk.Total, err = money.ParseUSD(r.Total); err != nil {
			return nil, decodeError("billing", 200, nil)
		}
		gpu, err1 := money.ParseUSD(r.GPU)
		cpu, err2 := money.ParseUSD(r.CPU)
		disk, err3 := money.ParseUSD(r.Disk)
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, decodeError("billing", 200, nil)
		}
		bk.Compute, bk.Disk = gpu+cpu, disk
		out = append(out, bk)
	}
	return out, nil
}

// ---- capabilities ---------------------------------------------------------------------------------------

type probeCaps struct {
	probed          bool
	writeKeyScoped  bool
	billingReadable bool
}

func parseProbe(raw []byte) probeCaps {
	var p struct {
		Probed          bool `json:"probed"`
		BillingReadable bool `json:"billing_readable"`
		Recommendation  struct {
			WriteKeyScoped bool `json:"write_key_scoped"`
		} `json:"recommendation"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil || !p.Probed {
		return probeCaps{}
	}
	return probeCaps{probed: true, writeKeyScoped: p.Recommendation.WriteKeyScoped, billingReadable: p.BillingReadable}
}

// Capabilities implements provider.Backend: key presence and key-capabilities.json; absent or unprobed
// means conservative (no ScaleToZero/RevertDrift, billing none).
func (b *Backend) Capabilities() provider.Capabilities {
	c := provider.Capabilities{Billing: "none"}
	if b.pc.probed && b.o.Keys.Write != "" && b.pc.writeKeyScoped {
		c.ScaleToZero, c.RevertDrift = true, true
	}
	if b.pc.probed && b.o.Keys.Read != "" && b.pc.billingReadable {
		c.Billing = "hour"
	}
	return c
}
