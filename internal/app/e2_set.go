package app

import "github.com/nicolasacchi/metamusic/internal/provider"

// buildE2Set builds the backend set and its gates (C151.1). Lane E2-J1 owns this file: it adds the ACE-Step backends
// (`fake-acestep` under MM_BACKEND=fake and MM_ACESTEP; `runpod-serverless-acestep` under runpod when
// RUNPOD_ACESTEP_ENDPOINT_ID is present), their provider_state rows (disarmed except fake*), their gates and the one
// reconciler per backend. The freeze wires the M3 backend alone, so a process without MM_ACESTEP behaves as before.
func buildE2Set(in e2Inputs) (*provider.Set, *provider.GateSet, error) {
	m := in.m3
	if m.Backend == nil || m.Gate == nil || m.Gate.Config().BackendName() == "" {
		set, err := provider.NewSet("")
		if err != nil {
			return nil, nil, err
		}
		gates, err := provider.NewGateSet("")
		return set, gates, err
	}
	set, err := provider.NewSet(m.Backend.Name(), m.Backend)
	if err != nil {
		return nil, nil, err
	}
	gates, err := provider.NewGateSet(m.Gate.Config().BackendName(), m.Gate)
	return set, gates, err
}
