package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
)

func pathSeg(s string) string { return url.PathEscape(s) }

// ScalingSpec is the queue scaler (a union on Type: QUEUE_DELAY + QueueDelay, REQUEST_COUNT + RequestCount).
type ScalingSpec struct {
	Type         string `json:"type"`
	QueueDelay   int    `json:"queueDelay,omitempty"`
	RequestCount int    `json:"requestCount,omitempty"`
}

// WorkersSpec is workers{min,max,idleTimeout}; IdleTimeout is omitted when nil (rejected for REQUEST_COUNT).
type WorkersSpec struct {
	Min         int  `json:"min"`
	Max         int  `json:"max"`
	IdleTimeout *int `json:"idleTimeout,omitempty"`
}

// GPUSpec selects GPUs. ExcludedTypes is always sent (a PATCH with pools alone clears it); a nil slice
// is sent as an empty list. MinCudaVersion and allowedCudaVersions are mutually exclusive: only the
// former is ever sent.
type GPUSpec struct {
	Count          int      `json:"count"`
	Pools          []string `json:"pools"`
	ExcludedTypes  []string `json:"excludedTypes"`
	MinCudaVersion string   `json:"minCudaVersion,omitempty"`
}

// MarshalJSON sends nil slices as [] (never null).
func (g GPUSpec) MarshalJSON() ([]byte, error) {
	type plain GPUSpec
	if g.Pools == nil {
		g.Pools = []string{}
	}
	if g.ExcludedTypes == nil {
		g.ExcludedTypes = []string{}
	}
	return json.Marshal(plain(g))
}

// EndpointSpec is the complete create body (C77.6).
type EndpointSpec struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Scaling   ScalingSpec       `json:"scaling"`
	Workers   WorkersSpec       `json:"workers"`
	GPU       GPUSpec           `json:"gpu"`
	Flashboot string            `json:"flashboot"`
	Timeout   int64             `json:"timeout"` // milliseconds
	Image     string            `json:"image,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// EndpointPatch carries only the fields to change; nested objects must be complete (C77.5).
type EndpointPatch struct {
	Name      *string           `json:"name,omitempty"`
	Scaling   *ScalingSpec      `json:"scaling,omitempty"`
	Workers   *WorkersSpec      `json:"workers,omitempty"`
	GPU       *GPUSpec          `json:"gpu,omitempty"`
	Flashboot *string           `json:"flashboot,omitempty"`
	Timeout   *int64            `json:"timeout,omitempty"`
	Image     *string           `json:"image,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// Endpoint is a serverless endpoint as RunPod returns it.
type Endpoint struct {
	ID, Name, Type string
	Scaling        struct {
		Type         string
		QueueDelay   *json.Number
		RequestCount *json.Number
	}
	Workers struct {
		Min, Max    int
		IdleTimeout *int
	}
	GPU struct {
		Count                int
		Pools, ExcludedTypes []string
		MinCudaVersion       string
		AllowedCudaVersions  []string
	}
	Flashboot   string
	TimeoutMS   int64
	Image       string
	Env         map[string]string
	RequestURLs map[string]string
}

type endpointWire struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Scaling struct {
		Type         string       `json:"type"`
		QueueDelay   *json.Number `json:"queueDelay"`
		RequestCount *json.Number `json:"requestCount"`
	} `json:"scaling"`
	Workers struct {
		Min         json.Number  `json:"min"`
		Max         json.Number  `json:"max"`
		IdleTimeout *json.Number `json:"idleTimeout"`
	} `json:"workers"`
	GPU struct {
		Count               json.Number `json:"count"`
		Pools               []string    `json:"pools"`
		ExcludedTypes       []string    `json:"excludedTypes"`
		MinCudaVersion      string      `json:"minCudaVersion"`
		AllowedCudaVersions []string    `json:"allowedCudaVersions"`
	} `json:"gpu"`
	Flashboot   string            `json:"flashboot"`
	Timeout     json.Number       `json:"timeout"`
	Image       string            `json:"image"`
	Env         map[string]string `json:"env"`
	RequestURLs map[string]string `json:"requestUrls"`
}

func num(n json.Number) int {
	if v := intOf(n); v != nil {
		return int(*v)
	}
	return 0
}

func (w endpointWire) endpoint() Endpoint {
	var e Endpoint
	e.ID, e.Name, e.Type = w.ID, w.Name, w.Type
	e.Scaling.Type, e.Scaling.QueueDelay, e.Scaling.RequestCount = w.Scaling.Type, w.Scaling.QueueDelay, w.Scaling.RequestCount
	e.Workers.Min, e.Workers.Max = num(w.Workers.Min), num(w.Workers.Max)
	if w.Workers.IdleTimeout != nil {
		v := num(*w.Workers.IdleTimeout)
		e.Workers.IdleTimeout = &v
	}
	e.GPU.Count, e.GPU.Pools, e.GPU.ExcludedTypes = num(w.GPU.Count), w.GPU.Pools, w.GPU.ExcludedTypes
	e.GPU.MinCudaVersion, e.GPU.AllowedCudaVersions = w.GPU.MinCudaVersion, w.GPU.AllowedCudaVersions
	e.Flashboot, e.Image, e.Env, e.RequestURLs = w.Flashboot, w.Image, w.Env, w.RequestURLs
	if v := intOf(w.Timeout); v != nil {
		e.TimeoutMS = *v
	}
	return e
}

// Worker is one worker of the endpoint (GET /v2/serverless/{ep}/workers).
type Worker struct {
	ID, Status, Image, GPUTypeID string
	IsStale                      bool
	StartedAt                    *time.Time
	UptimeSeconds                *int
}

// GPUType is one catalog entry. ServerlessPrice is the per-GPU list price literal (absent when the type is
// in no serverless pool available to the caller).
type GPUType struct {
	ID, Name, Pool  string
	ServerlessPrice string
}

// BillingQuery selects hourly serverless billing buckets. Start and End are snapped to hour bounds by
// RunPod; callers pass aligned values.
type BillingQuery struct {
	ServerlessID string // default: the client's endpoint id
	Start, End   time.Time
}

// BillingRecord is one hourly bucket; amounts are decimal literals ("0.123456789"), never floats.
type BillingRecord struct {
	Start, End                 time.Time
	ServerlessID               string
	Total, GPU, CPU, Disk, Fee string
}

func (c *Client) getMgmt(ctx context.Context, op, path, template string, k KeyKind, q url.Values, timeout time.Duration) ([]byte, error) {
	return c.do(ctx, call{op: op, method: http.MethodGet, path: path, template: template, query: q, key: k, timeout: timeout,
		maxBody: MaxRequestBody, retry: true})
}

// GetEndpoint reads the endpoint (incl. requestUrls).
func (c *Client) GetEndpoint(ctx context.Context, k KeyKind) (Endpoint, error) {
	raw, err := c.getMgmt(ctx, "get_endpoint", "/v2/serverless/"+pathSeg(c.o.Keys.EndpointID), "/v2/serverless/<endpoint>", k, nil, OtherTimeout)
	if err != nil {
		return Endpoint{}, err
	}
	var w endpointWire
	if err := decode("get_endpoint", raw, &w); err != nil {
		return Endpoint{}, err
	}
	return w.endpoint(), nil
}

// PatchEndpoint updates the endpoint. It never retries.
func (c *Client) PatchEndpoint(ctx context.Context, k KeyKind, p EndpointPatch) (Endpoint, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return Endpoint{}, err
	}
	raw, err := c.do(ctx, call{op: "patch_endpoint", method: http.MethodPatch, path: "/v2/serverless/" + pathSeg(c.o.Keys.EndpointID),
		template: "/v2/serverless/<endpoint>", key: k, body: body, timeout: OtherTimeout, maxBody: MaxRequestBody})
	if err != nil {
		return Endpoint{}, err
	}
	var w endpointWire
	if err := decode("patch_endpoint", raw, &w); err != nil {
		return Endpoint{}, err
	}
	return w.endpoint(), nil
}

// ListEndpoints lists the account's serverless endpoints.
func (c *Client) ListEndpoints(ctx context.Context, k KeyKind) ([]Endpoint, error) {
	raw, err := c.getMgmt(ctx, "list_endpoints", "/v2/serverless", "/v2/serverless", k, nil, OtherTimeout)
	if err != nil {
		return nil, err
	}
	var ws []endpointWire
	if err := decodeList("list_endpoints", raw, &ws, "endpoints"); err != nil {
		return nil, err
	}
	out := make([]Endpoint, len(ws))
	for i, w := range ws {
		out[i] = w.endpoint()
	}
	return out, nil
}

// CreateEndpoint creates an endpoint with the admin key; any other client returns ErrNotPermitted.
func (c *Client) CreateEndpoint(ctx context.Context, s EndpointSpec) (Endpoint, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return Endpoint{}, err
	}
	raw, err := c.do(ctx, call{op: "create_endpoint", method: http.MethodPost, path: "/v2/serverless", key: KeyAdmin, body: body,
		timeout: OtherTimeout, maxBody: MaxRequestBody})
	if err != nil {
		return Endpoint{}, err
	}
	var w endpointWire
	if err := decode("create_endpoint", raw, &w); err != nil {
		return Endpoint{}, err
	}
	return w.endpoint(), nil
}

// Workers lists the endpoint's workers (read key).
func (c *Client) Workers(ctx context.Context) ([]Worker, error) {
	raw, err := c.getMgmt(ctx, "workers", "/v2/serverless/"+pathSeg(c.o.Keys.EndpointID)+"/workers", "/v2/serverless/<endpoint>/workers", KeyRead, nil, OtherTimeout)
	if err != nil {
		return nil, err
	}
	var ws []struct {
		ID            string       `json:"id"`
		Status        string       `json:"status"`
		IsStale       bool         `json:"isStale"`
		Image         string       `json:"image"`
		StartedAt     *time.Time   `json:"startedAt"`
		UptimeSeconds *json.Number `json:"uptimeSeconds"`
		GPUTypeID     string       `json:"gpuTypeId"`
	}
	if err := decodeList("workers", raw, &ws, "workers"); err != nil {
		return nil, err
	}
	out := make([]Worker, len(ws))
	for i, w := range ws {
		out[i] = Worker{ID: w.ID, Status: w.Status, IsStale: w.IsStale, Image: w.Image, StartedAt: w.StartedAt, GPUTypeID: w.GPUTypeID}
		if w.UptimeSeconds != nil {
			if v := intOf(*w.UptimeSeconds); v != nil {
				n := int(*v)
				out[i].UptimeSeconds = &n
			}
		}
	}
	return out, nil
}

// CatalogGPUs reads the GPU catalog; the answer is 401 without a bearer token.
func (c *Client) CatalogGPUs(ctx context.Context, k KeyKind) ([]GPUType, error) {
	raw, err := c.getMgmt(ctx, "catalog", "/v2/catalog/gpus", "/v2/catalog/gpus", k, nil, OtherTimeout)
	if err != nil {
		return nil, err
	}
	var ws []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Pool  string `json:"pool"`
		Price struct {
			Serverless *json.Number `json:"serverless"`
		} `json:"price"`
	}
	if err := decodeList("catalog", raw, &ws, "gpus"); err != nil {
		return nil, err
	}
	out := make([]GPUType, len(ws))
	for i, w := range ws {
		out[i] = GPUType{ID: w.ID, Name: w.Name, Pool: w.Pool}
		if w.Price.Serverless != nil {
			out[i].ServerlessPrice = w.Price.Serverless.String()
		}
	}
	return out, nil
}

// BillingServerless reads hourly buckets (bucketSize=hour always).
func (c *Client) BillingServerless(ctx context.Context, q BillingQuery) ([]BillingRecord, error) {
	id := q.ServerlessID
	if id == "" {
		id = c.o.Keys.EndpointID
	}
	v := url.Values{}
	v.Set("serverlessId", id)
	v.Set("bucketSize", "hour")
	v.Set("startTime", q.Start.UTC().Format(time.RFC3339))
	v.Set("endTime", q.End.UTC().Format(time.RFC3339))
	raw, err := c.getMgmt(ctx, "billing", "/v2/billing/serverless", "/v2/billing/serverless", KeyRead, v, BillingTimeout)
	if err != nil {
		return nil, err
	}
	var ws []struct {
		StartTime    time.Time   `json:"startTime"`
		EndTime      time.Time   `json:"endTime"`
		ServerlessID string      `json:"serverlessId"`
		Total        json.Number `json:"totalAmount"`
		GPU          json.Number `json:"gpuAmount"`
		CPU          json.Number `json:"cpuAmount"`
		Disk         json.Number `json:"diskAmount"`
		Fee          json.Number `json:"feeAmount"`
	}
	if err := decodeList("billing", raw, &ws, "records"); err != nil {
		return nil, err
	}
	out := make([]BillingRecord, len(ws))
	for i, w := range ws {
		out[i] = BillingRecord{Start: w.StartTime, End: w.EndTime, ServerlessID: w.ServerlessID,
			Total: w.Total.String(), GPU: w.GPU.String(), CPU: w.CPU.String(), Disk: w.Disk.String(), Fee: w.Fee.String()}
	}
	return out, nil
}

// Balance reads the account balance through GraphQL (D-M3-20). ok is false when RunPod answered without
// a balance (the field is optional and will disappear with GraphQL).
func (c *Client) Balance(ctx context.Context) (money.MicroUSD, bool, error) {
	body := []byte(`{"query":"query { myself { clientBalance } }"}`)
	raw, err := c.do(ctx, call{op: "balance", method: http.MethodPost, path: "/graphql", key: KeyRead, body: body,
		timeout: OtherTimeout, maxBody: MaxRequestBody})
	if err != nil {
		return 0, false, err
	}
	var r struct {
		Data struct {
			Myself struct {
				ClientBalance json.RawMessage `json:"clientBalance"`
			} `json:"myself"`
		} `json:"data"`
	}
	if err := decode("balance", raw, &r); err != nil {
		return 0, false, err
	}
	raw = nullToNil(r.Data.Myself.ClientBalance)
	if raw == nil {
		return 0, false, nil
	}
	m, err := money.ParseUSD(strings.Trim(string(raw), `"`))
	return m, err == nil, nil
}

// decodeList decodes a JSON array, or an object that wraps it under key (or "items"/"data").
func decodeList(op string, raw []byte, v any, key string) error {
	t := bytes.TrimSpace(raw)
	if len(t) > 0 && t[0] == '[' {
		return decode(op, raw, v)
	}
	var m map[string]json.RawMessage
	if err := decode(op, raw, &m); err != nil {
		return err
	}
	for _, k := range []string{key, "items", "data"} {
		if inner, ok := m[k]; ok {
			return decode(op, inner, v)
		}
	}
	return decodeError(op, 200, nil)
}
