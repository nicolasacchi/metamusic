package runpod

import (
	"context"
	"fmt"
)

// ModelsReport is the read-only answer of `provision --verify-models` (C155.2).
type ModelsReport struct {
	Endpoint     string   `json:"endpoint"`
	Found        bool     `json:"found"`
	WorkersMax   *int     `json:"workers_max,omitempty"`
	ModelRefs    []string `json:"model_refs"`
	ConsoleSteps []string `json:"console_steps,omitempty"`
	// MountVisibleInAPI: the REST v2 worker listing carries no mount information (E2-P0a), so the cached model's
	// presence cannot be read from the API; ModelsMode says how it will be decided.
	MountVisibleInAPI bool   `json:"mount_visible_in_api"`
	ModelsMode        string `json:"models_mode"`
}

// VerifyModels reports, without any write, whether the profile's endpoint exists and which cached-model console steps the
// operator must have taken. It cannot see the mount: models_mode stays "unverified" until the canary's first t2m case
// reaches a job (a worker that starts proves the cached model is visible, C155.2).
func VerifyModels(ctx context.Context, c *Client, o ProvisionOptions) (ModelsReport, error) {
	prof := o.profile()
	catalog, err := c.CatalogGPUs(ctx, KeyAdmin)
	if err != nil {
		return ModelsReport{}, fmt.Errorf("reading the GPU catalog: %w", err)
	}
	want, err := DesiredSpecFor(o.Desired, prof, o.Image, 0, catalog)
	if err != nil {
		return ModelsReport{}, err
	}
	rep := ModelsReport{Endpoint: want.Name, ConsoleSteps: consoleSteps(o, prof, want), ModelsMode: "unverified: decided by the canary's t2m case"}
	for _, m := range prof.ModelRefs {
		rep.ModelRefs = append(rep.ModelRefs, fmt.Sprintf("%s@%s", m.Repo(), m.Revision))
	}
	eps, err := c.ListEndpoints(ctx, KeyAdmin)
	if err != nil {
		return rep, fmt.Errorf("listing endpoints: %w", err)
	}
	for _, e := range eps {
		if e.Name == want.Name {
			rep.Found = true
			w := e.Workers.Max
			rep.WorkersMax = &w
		}
	}
	return rep, nil
}
