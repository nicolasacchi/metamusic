package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// ProvisionOptions drives `runpod provision` (C77.6). The client must carry the admin key; its endpoint
// id is ignored (the endpoint is found by name).
type ProvisionOptions struct {
	Desired    DesiredConfig
	Weights    map[string]string
	Image      string // name@sha256:<digest>; empty keeps an existing endpoint's image
	MaxWorkers int    // 0 = inactive (the default); --activate passes 1 or 2
	DryRun     bool
}

// ProvisionResult is what provision did or would do. The serialised form never carries the endpoint id:
// Body and ConsoleSteps pass the client's redactor. EndpointID (json:"-") is set only after a successful
// create, for the CLI's one-line hint to append it to runpod.env.
type ProvisionResult struct {
	Action       string          `json:"action"` // "create" | "patch" | "none"
	Name         string          `json:"name"`
	Diff         []string        `json:"diff,omitempty"` // top-level fields that differ
	Body         json.RawMessage `json:"body,omitempty"` // the create body or the PATCH body
	WorkersMax   int             `json:"workers_max"`
	Applied      bool            `json:"applied"`
	ConsoleSteps []string        `json:"console_steps,omitempty"`
	Warnings     []string        `json:"warnings,omitempty"`
	EndpointID   string          `json:"-"`
}

// withEndpoint returns a client for another endpoint id sharing the options (and the recorder).
func (c *Client) withEndpoint(id string) *Client {
	o := c.o
	o.Keys.EndpointID = id
	return NewClient(o)
}

// Provision creates the endpoint when absent, else diffs it against the desired spec and PATCHes only
// the differing top-level fields, each nested object complete (C77.5). It reads the catalog with the
// admin key (the catalog answers 401 without a bearer) and never touches the job API.
func Provision(ctx context.Context, c *Client, o ProvisionOptions) (ProvisionResult, error) {
	if o.Image != "" {
		if err := CheckImage(o.Image); err != nil {
			return ProvisionResult{}, err
		}
	}
	catalog, err := c.CatalogGPUs(ctx, KeyAdmin)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("reading the GPU catalog: %w", err)
	}
	want, err := DesiredSpec(o.Desired, o.Weights, o.Image, o.MaxWorkers, catalog)
	if err != nil {
		return ProvisionResult{}, err
	}
	res := ProvisionResult{Name: want.Name, WorkersMax: want.Workers.Max, ConsoleSteps: consoleSteps(o, want)}
	eps, err := c.ListEndpoints(ctx, KeyAdmin)
	if err != nil {
		return res, fmt.Errorf("listing endpoints: %w", err)
	}
	var found []Endpoint
	for _, e := range eps {
		if e.Name == want.Name {
			found = append(found, e)
		}
	}
	switch {
	case len(found) > 1:
		return res, fmt.Errorf("runpod: provision: %d endpoints are named %q; remove the duplicates in the console", len(found), want.Name)
	case len(found) == 0:
		return res, c.create(ctx, &res, want, o)
	}
	ep := found[0]
	ec := c.withEndpoint(ep.ID)
	diff, patch, err := diffEndpoint(want, ep, o.Image != "")
	if err != nil {
		return res, err
	}
	res.Diff = diff
	if len(diff) == 0 {
		res.Action = "none"
		return res, nil
	}
	res.Action = "patch"
	body, _ := json.Marshal(patch)
	res.Body = json.RawMessage(ec.Redactor().String(string(body)))
	if o.DryRun {
		return res, nil
	}
	if _, err := ec.PatchEndpoint(ctx, KeyAdmin, patch); err != nil {
		return res, fmt.Errorf("patching the endpoint: %w", err)
	}
	got, err := ec.GetEndpoint(ctx, KeyAdmin)
	if err != nil {
		return res, fmt.Errorf("reading the endpoint back: %w", err)
	}
	if left, _, _ := diffEndpoint(want, got, o.Image != ""); len(left) > 0 {
		return res, fmt.Errorf("%w: %s", ErrPatchMismatch, strings.Join(left, ", "))
	}
	res.Applied = true
	return res, nil
}

func (c *Client) create(ctx context.Context, res *ProvisionResult, want EndpointSpec, o ProvisionOptions) error {
	if o.Image == "" {
		return ErrImageRequired
	}
	res.Action = "create"
	body, _ := json.Marshal(want)
	res.Body = json.RawMessage(c.Redactor().String(string(body)))
	if o.DryRun {
		return nil
	}
	ep, err := c.CreateEndpoint(ctx, want)
	if err != nil {
		return fmt.Errorf("creating the endpoint: %w", err)
	}
	res.Applied = true
	res.EndpointID = ep.ID
	if left, _, _ := diffEndpoint(want, ep, true); len(left) > 0 {
		res.Warnings = append(res.Warnings, "the create response differs from the request in: "+strings.Join(left, ", ")+"; run provision again to verify")
	}
	return nil
}

// consoleSteps are the settings RunPod's API snapshot has no field for. They name the endpoint by its
// name only.
func consoleSteps(o ProvisionOptions, want EndpointSpec) []string {
	d, err := parseSpec(snapshotJSON)
	if err == nil && d.hasCachedModelField() {
		return nil
	}
	steps := []string{fmt.Sprintf("In the RunPod console open the endpoint %q, Model settings, and set the cached model to %s at revision %s.",
		want.Name, yue2.WeightsRepo3B, o.Weights[yue2.WeightsRepo3B])}
	switch o.Desired.VAESource {
	case "", "image":
		// the VAE is baked into the worker image: nothing to do in the console for it
	case "cached":
		steps = append(steps, fmt.Sprintf("Add %s at revision %s as a second cached model (the worker reads it from %s). RunPod offers one cached model per endpoint, so this step may be impossible; prefer --vae-source image.",
			yue2.WeightsRepoVAE, o.Weights[yue2.WeightsRepoVAE], CachedModelDir))
	default:
		steps = append(steps, "Attach the network volume that holds the VAE and check that its path matches --vae-source.")
	}
	return steps
}

// diffEndpoint compares the desired spec with the endpoint as read and returns the differing top-level
// fields plus a PATCH carrying exactly those, nested objects complete. The image is compared only when
// the caller named one. A different type cannot be patched.
func diffEndpoint(want EndpointSpec, ep Endpoint, checkImage bool) ([]string, EndpointPatch, error) {
	var diff []string
	var p EndpointPatch
	if ep.Type != "" && ep.Type != want.Type {
		return nil, p, fmt.Errorf("runpod: provision: the endpoint is of type %s; the type cannot be changed by PATCH (recreate it in the console)", ep.Type)
	}
	if !sameScaling(want.Scaling, ep) {
		s := want.Scaling
		p.Scaling = &s
		diff = append(diff, "scaling")
	}
	if !sameWorkers(want.Workers, ep) {
		w := want.Workers
		p.Workers = &w
		diff = append(diff, "workers")
	}
	if !sameGPU(want.GPU, ep) {
		g := want.GPU
		p.GPU = &g
		diff = append(diff, "gpu")
	}
	if ep.Flashboot != want.Flashboot {
		f := want.Flashboot
		p.Flashboot = &f
		diff = append(diff, "flashboot")
	}
	if ep.TimeoutMS != want.Timeout {
		t := want.Timeout
		p.Timeout = &t
		diff = append(diff, "timeout")
	}
	if checkImage && ep.Image != want.Image {
		i := want.Image
		p.Image = &i
		diff = append(diff, "image")
	}
	if !reflect.DeepEqual(emptyIfNil(ep.Env), want.Env) {
		p.Env = want.Env
		diff = append(diff, "env")
	}
	sort.Strings(diff)
	return diff, p, nil
}

func emptyIfNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func sameScaling(w ScalingSpec, ep Endpoint) bool {
	if ep.Scaling.Type != w.Type || ep.Scaling.QueueDelay == nil {
		return false
	}
	return scalarLit(*ep.Scaling.QueueDelay) == fmt.Sprint(w.QueueDelay)
}

func sameWorkers(w WorkersSpec, ep Endpoint) bool {
	if ep.Workers.Min != w.Min || ep.Workers.Max != w.Max {
		return false
	}
	return (w.IdleTimeout == nil) == (ep.Workers.IdleTimeout == nil) && (w.IdleTimeout == nil || *w.IdleTimeout == *ep.Workers.IdleTimeout)
}

func sameGPU(g GPUSpec, ep Endpoint) bool {
	return ep.GPU.Count == g.Count && ep.GPU.MinCudaVersion == g.MinCudaVersion && len(ep.GPU.AllowedCudaVersions) == 0 &&
		sameSet(ep.GPU.Pools, g.Pools) && sameSet(ep.GPU.ExcludedTypes, g.ExcludedTypes)
}
