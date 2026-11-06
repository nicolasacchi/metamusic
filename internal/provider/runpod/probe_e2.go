package runpod

import "context"

// withEndpointKeys returns a client for another endpoint id (and, when jobsKey is set, another jobs key)
// sharing the options and the recorder.
func (c *Client) withEndpointKeys(id, jobsKey string) *Client {
	o := c.o
	o.Keys.EndpointID = id
	if jobsKey != "" {
		o.Keys.Jobs = jobsKey
	}
	return NewClient(o)
}

// aceStep probes the second endpoint (C155.3): one GET /health with the shared jobs key, and, when that key does
// not reach it and RUNPOD_ACESTEP_JOBS_KEY is set, one with that key. Spend-free (a GET), skipped when no
// ACE-Step endpoint is configured. A 404 means the id names no endpoint, so it is "not reachable", not "allowed".
func (p *prober) aceStep(ctx context.Context) error {
	id := p.o.Keys.ACEStepEndpointID
	if id == "" || p.o.Keys.Jobs == "" {
		return nil
	}
	_, err := p.withACE(id, "").Health(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p.record("jobs", "acestep_health", verdict(err))
	scoped := err == nil
	reachable := scoped
	if !scoped && p.o.Keys.ACEStepJobsKey != "" {
		_, err2 := p.withACE(id, p.o.Keys.ACEStepJobsKey).Health(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.record("jobs", "acestep_health_own_key", verdict(err2))
		reachable = err2 == nil
	}
	p.rep.ACEStepJobsKeyScoped, p.rep.ACEStepEndpointReachable = &scoped, &reachable
	return nil
}

func (p *prober) withACE(id, key string) *Client { return p.c.withEndpointKeys(id, key) }
