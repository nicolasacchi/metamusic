package runpod

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// Policy is the per-job policy (milliseconds). Zero values are omitted.
type Policy struct {
	ExecutionTimeoutMS int64 `json:"executionTimeout,omitempty"`
	TTLMS              int64 `json:"ttl,omitempty"`
}

// RunResult is the answer to /run.
type RunResult struct{ ID, Status string }

// JobStatus is one /status (or /cancel) record. DelayMS and ExecutionMS are nil when absent.
type JobStatus struct {
	ID, Status           string
	DelayMS, ExecutionMS *int64
	WorkerID             string
	Output               json.RawMessage
	Error                string // redacted, at most 500 bytes
}

// Health is the /health record.
type Health struct{ Jobs, Workers map[string]int }

type runBody struct {
	Input  json.RawMessage `json:"input"`
	Policy *Policy         `json:"policy,omitempty"`
}

// Run submits one job. It never retries and never carries an idempotency header (C77.1).
func (c *Client) Run(ctx context.Context, input json.RawMessage, p Policy) (RunResult, error) {
	b := runBody{Input: input}
	if p != (Policy{}) {
		b.Policy = &p
	}
	body, err := json.Marshal(b)
	if err != nil {
		return RunResult{}, err
	}
	raw, err := c.do(ctx, call{op: "run", method: http.MethodPost, jobsHost: true, path: c.endpointPath("/run"), template: "/v2/<endpoint>/run",
		key: KeyJobs, body: body, timeout: RunTimeout, maxBody: provider.MaxRunBody})
	if err != nil {
		return RunResult{}, err
	}
	var r struct{ ID, Status string }
	if err := decode("run", raw, &r); err != nil {
		return RunResult{}, err
	}
	if r.ID == "" {
		return RunResult{}, decodeError("run", 200, nil)
	}
	return RunResult{ID: r.ID, Status: r.Status}, nil
}

// Status reads one job.
func (c *Client) Status(ctx context.Context, id string) (JobStatus, error) {
	return c.jobRecord(ctx, call{op: "status", method: http.MethodGet, jobsHost: true, path: c.endpointPath("/status/" + pathSeg(id)),
		template: "/v2/<endpoint>/status/{id}", key: KeyJobs, timeout: OtherTimeout, maxBody: MaxRequestBody, retry: true})
}

// Cancel asks RunPod to cancel one job (one POST, no retry).
func (c *Client) Cancel(ctx context.Context, id string) (JobStatus, error) {
	return c.jobRecord(ctx, call{op: "cancel", method: http.MethodPost, jobsHost: true, path: c.endpointPath("/cancel/" + pathSeg(id)),
		template: "/v2/<endpoint>/cancel/{id}", key: KeyJobs, timeout: OtherTimeout, maxBody: MaxRequestBody})
}

// PurgeQueue removes every queued job of the endpoint.
func (c *Client) PurgeQueue(ctx context.Context) error {
	_, err := c.do(ctx, call{op: "purge_queue", method: http.MethodPost, jobsHost: true, path: c.endpointPath("/purge-queue"),
		template: "/v2/<endpoint>/purge-queue", key: KeyJobs, timeout: OtherTimeout, maxBody: MaxRequestBody})
	return err
}

// Health reads the endpoint's queue and worker counters.
func (c *Client) Health(ctx context.Context) (Health, error) {
	raw, err := c.do(ctx, call{op: "health", method: http.MethodGet, jobsHost: true, path: c.endpointPath("/health"),
		template: "/v2/<endpoint>/health", key: KeyJobs, timeout: OtherTimeout, maxBody: MaxRequestBody, retry: true})
	if err != nil {
		return Health{}, err
	}
	var h struct {
		Jobs, Workers map[string]json.RawMessage
	}
	if err := decode("health", raw, &h); err != nil {
		return Health{}, err
	}
	conv := func(m map[string]json.RawMessage) map[string]int {
		out := make(map[string]int, len(m))
		for k, v := range m {
			if n := intOf(json.Number(strings.TrimSpace(string(v)))); n != nil {
				out[k] = int(*n)
			}
		}
		return out
	}
	return Health{Jobs: conv(h.Jobs), Workers: conv(h.Workers)}, nil
}

func (c *Client) jobRecord(ctx context.Context, k call) (JobStatus, error) {
	raw, err := c.do(ctx, k)
	if err != nil {
		return JobStatus{}, err
	}
	var s struct {
		ID            string
		Status        string
		DelayTime     *json.Number
		ExecutionTime *json.Number
		WorkerID      string
		Output        json.RawMessage
		Error         json.RawMessage
	}
	if err := decode(k.op, raw, &s); err != nil {
		return JobStatus{}, err
	}
	js := JobStatus{ID: s.ID, Status: s.Status, WorkerID: s.WorkerID, Output: nullToNil(s.Output)}
	if s.DelayTime != nil {
		js.DelayMS = intOf(*s.DelayTime)
	}
	if s.ExecutionTime != nil {
		js.ExecutionMS = intOf(*s.ExecutionTime)
	}
	if e := nullToNil(s.Error); e != nil {
		var str string
		if json.Unmarshal(e, &str) != nil {
			str = string(e)
		}
		js.Error = cutUTF8(c.red.String(str), 500)
	}
	return js, nil
}

func nullToNil(r json.RawMessage) json.RawMessage {
	if len(r) == 0 || strings.TrimSpace(string(r)) == "null" {
		return nil
	}
	return r
}

// intOf truncates a JSON number to an integer; nil when it is not numeric.
func intOf(n json.Number) *int64 {
	if v, err := n.Int64(); err == nil {
		return &v
	}
	if strings.ContainsAny(n.String(), "eE") {
		return nil
	}
	s, _, _ := strings.Cut(n.String(), ".")
	var v int64
	if s == "" {
		return nil
	}
	neg := strings.HasPrefix(s, "-")
	for _, ch := range strings.TrimPrefix(s, "-") {
		if ch < '0' || ch > '9' {
			return nil
		}
		v = v*10 + int64(ch-'0')
	}
	if neg {
		v = -v
	}
	return &v
}
