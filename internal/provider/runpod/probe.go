package runpod

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed" // the OpenAPI snapshot is compiled in: `runpod probe --schema` compares against it
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

//go:embed testdata/openapi.snapshot.json
var snapshotJSON []byte

// Snapshot returns the committed OpenAPI snapshot (fetched 2026-10-12) the client's field table is
// pinned against.
func Snapshot() []byte { return append([]byte(nil), snapshotJSON...) }

// PublicSpecURL is the public management OpenAPI document.
const PublicSpecURL = "https://api.runpod.io/v2/openapi.json"

// CapabilitiesFormat is the format tag of key-capabilities.json (C77.8).
const CapabilitiesFormat = "mm.runpod-probe/2"

// ---- schema probe (no keys) -----------------------------------------------------------------------------

// SchemaReport is the result of comparing the live public spec with the snapshot. It carries short
// hash prefixes only: no run of 32 or more characters may reach key-capabilities.json.
type SchemaReport struct {
	SnapshotSHA      string   `json:"snapshot_sha"`
	LiveSHA          string   `json:"live_sha"`
	Drift            bool     `json:"drift"`
	Changed          []string `json:"changed,omitempty"`
	CachedModelField bool     `json:"cached_model_field"`
	FlashbootValues  []string `json:"flashboot_values"`
	Failures         []string `json:"failures,omitempty"`
	OK               bool     `json:"ok"`
}

// FetchSpec downloads the live spec (no credentials, bounded to 4 MiB).
func FetchSpec(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.New("runpod: probe: invalid spec URL")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgentFor("probe"))
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("runpod: probe: the public spec is unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runpod: probe: the public spec answered %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil || len(b) > 4<<20 {
		return nil, errors.New("runpod: probe: the public spec could not be read")
	}
	return b, nil
}

type specDoc map[string]any

func parseSpec(raw []byte) (specDoc, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var d specDoc
	if err := dec.Decode(&d); err != nil {
		return nil, errors.New("runpod: probe: the spec is not valid JSON")
	}
	return d, nil
}

func (d specDoc) at(path ...string) any {
	var cur any = map[string]any(d)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func (d specDoc) deref(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	if ref, ok := m["$ref"].(string); ok {
		m, _ = d.at(strings.Split(strings.TrimPrefix(ref, "#/"), "/")...).(map[string]any)
	}
	return m
}

func (d specDoc) property(schema map[string]any, name string) map[string]any {
	schema = d.deref(schema)
	if schema == nil {
		return nil
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		if p, ok := props[name]; ok {
			return d.deref(p)
		}
	}
	for _, k := range []string{"oneOf", "anyOf", "allOf"} {
		if alts, ok := schema[k].([]any); ok {
			for _, a := range alts {
				if p := d.property(d.deref(a), name); p != nil {
					return p
				}
			}
		}
	}
	return nil
}

func (d specDoc) resolve(schema, path string) map[string]any {
	cur := d.deref(d.at("components", "schemas", schema))
	for _, seg := range strings.Split(path, ".") {
		if cur == nil {
			return nil
		}
		cur = d.property(cur, seg)
	}
	return cur
}

// flatten renders every leaf of a document as path -> canonical literal.
func flatten(prefix string, v any, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			out[prefix] = "{}"
		}
		for k, e := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flatten(p, e, out)
		}
	case []any:
		if len(x) == 0 {
			out[prefix] = "[]"
		}
		for i, e := range x {
			flatten(fmt.Sprintf("%s[%d]", prefix, i), e, out)
		}
	default:
		b, _ := json.Marshal(x)
		out[prefix] = string(b)
	}
}

func shortSHA(raw []byte) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		v = string(raw)
	}
	canon, _ := json.Marshal(v) // map keys are sorted by encoding/json
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])[:12]
}

// CompareSchema compares the live spec with the snapshot: the changed paths, whether a cached-model
// field exists, the flashboot values, and a check of every pinned fact of C77.2 against the LIVE spec.
func CompareSchema(snapshot, live []byte) (SchemaReport, error) {
	sd, err := parseSpec(snapshot)
	if err != nil {
		return SchemaReport{}, err
	}
	ld, err := parseSpec(live)
	if err != nil {
		return SchemaReport{}, err
	}
	a, b := map[string]string{}, map[string]string{}
	flatten("", map[string]any(sd), a)
	flatten("", map[string]any(ld), b)
	var changed []string
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			changed = append(changed, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	r := SchemaReport{SnapshotSHA: shortSHA(snapshot), LiveSHA: shortSHA(live), Changed: changed, Drift: len(changed) > 0}
	r.CachedModelField = ld.hasCachedModelField()
	r.FlashbootValues = ld.enumValues("CreateEndpointRequest", "flashboot")
	r.Failures = ld.checkFacts()
	r.OK = len(r.Failures) == 0
	return r, nil
}

func (d specDoc) hasCachedModelField() bool {
	var walk func(m map[string]any, depth int) bool
	walk = func(m map[string]any, depth int) bool {
		m = d.deref(m)
		props, _ := m["properties"].(map[string]any)
		for name, p := range props {
			n := strings.ToLower(name)
			if strings.Contains(n, "cachedmodel") || strings.Contains(n, "modelname") || n == "model" {
				return true
			}
			if depth < 3 {
				if pm, _ := p.(map[string]any); pm != nil && walk(pm, depth+1) {
					return true
				}
			}
		}
		return false
	}
	root, _ := d.at("components", "schemas", "CreateEndpointRequest").(map[string]any)
	return root != nil && walk(root, 0)
}

func (d specDoc) enumValues(schema, path string) []string {
	p := d.resolve(schema, path)
	var out []string
	if p == nil {
		return out
	}
	vals, _ := p["enum"].([]any)
	for _, v := range vals {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// checkFacts verifies the pinned facts of C77.2 and the client's field table against this document.
func (d specDoc) checkFacts() []string {
	var f []string
	for _, op := range ManagementOperations {
		m, p, _ := strings.Cut(op, " ")
		if d.at("paths", p, strings.ToLower(m)) == nil {
			f = append(f, "operation missing: "+op)
		}
	}
	for _, fld := range ManagementFields {
		if fld.Schema == "" {
			m, p, _ := strings.Cut(fld.Operation, " ")
			found := false
			params, _ := d.at("paths", p, strings.ToLower(m), "parameters").([]any)
			for _, x := range params {
				if pm, _ := x.(map[string]any); pm != nil && pm["name"] == fld.Path {
					found = true
				}
			}
			if !found {
				f = append(f, "query parameter missing: "+fld.Operation+" "+fld.Path)
			}
			continue
		}
		if d.resolve(fld.Schema, fld.Path) == nil {
			f = append(f, "field missing: "+fld.Schema+"."+fld.Path)
		}
	}
	if g := d.resolve("CreateEndpointRequest", "gpu.minCudaVersion"); g == nil {
		f = append(f, "fact: gpu.minCudaVersion")
	} else if desc, _ := g["description"].(string); !strings.Contains(desc, "allowedCudaVersions") {
		f = append(f, "fact: gpu.minCudaVersion is no longer documented as exclusive with allowedCudaVersions")
	}
	qd := d.resolve("CreateEndpointRequest", "scaling.queueDelay")
	if qd == nil {
		f = append(f, "fact: scaling union has no queueDelay")
	} else if lo, ok := qd["minimum"].(json.Number); !ok || lo.String() != "0.5" {
		f = append(f, "fact: scaling.queueDelay minimum is no longer 0.5")
	}
	if d.resolve("CreateEndpointRequest", "scaling.requestCount") == nil {
		f = append(f, "fact: scaling union has no requestCount")
	}
	if tm := d.resolve("CreateEndpointRequest", "timeout"); tm == nil {
		f = append(f, "fact: timeout")
	} else if desc, _ := tm["description"].(string); !strings.Contains(strings.ToLower(desc), "milliseconds") {
		f = append(f, "fact: timeout is no longer documented in milliseconds")
	}
	fb := d.enumValues("CreateEndpointRequest", "flashboot")
	for _, want := range []string{"OFF", "FLASHBOOT", "PRIORITY_FLASHBOOT"} {
		if !containsStr(fb, want) {
			f = append(f, "fact: flashboot value "+want)
		}
	}
	if req, _ := d.deref(d.at("components", "schemas", "CreateEndpointRequest"))["required"].([]any); len(req) != 3 {
		f = append(f, "fact: CreateEndpointRequest required fields changed")
	}
	return f
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ---- key probe -------------------------------------------------------------------------------------------

// Verdicts of one probed call.
const (
	VerdictAllowed     = "allowed"
	VerdictForbidden   = "forbidden"
	VerdictEdgeBlocked = "edge_blocked"
	VerdictError       = "error"
	VerdictSkipped     = "skipped"
)

// ErrProbeCritical aborts the probe: the name-only PATCH changed the endpoint. The message is critical;
// the operator must inspect the endpoint before anything else.
var ErrProbeCritical = errors.New("runpod: probe: CRITICAL: the endpoint differs after a name-only PATCH; inspect it in the console now")

// CallResult is the verdict on one call. HTTP is the status code (0 when none was received).
type CallResult struct {
	Verdict string `json:"verdict"`
	HTTP    int    `json:"http,omitempty"`
	Note    string `json:"note,omitempty"`
}

// ErrShape is the shape of a not-found answer.
type ErrShape struct {
	HTTP  int    `json:"http"`
	Code  string `json:"code"`
	Title string `json:"title,omitempty"`
}

// TTLShape records what a job that expires in the queue looks like.
type TTLShape struct {
	Status          string `json:"status"`
	HTTP            int    `json:"http"`
	ExecutionTimeMS *int64 `json:"execution_time_ms"`
	Polls           int    `json:"polls"`
	Terminal        bool   `json:"terminal"`
}

// CancelShape records what a job cancelled while queued looks like.
type CancelShape struct {
	CancelHTTP      int    `json:"cancel_http"`
	CancelStatus    string `json:"cancel_status"`
	StatusAfter     string `json:"status_after"`
	ExecutionTimeMS *int64 `json:"execution_time_ms"`
}

// NeverRanShapes pins C73.2 rules 4/5: neither shape carries an executionTime (nothing was billed).
type NeverRanShapes struct {
	TTLHasExecutionTime       bool `json:"ttl_has_execution_time"`
	CancelledHasExecutionTime bool `json:"cancelled_has_execution_time"`
}

// Recommendation is derived from the probe.
type Recommendation struct {
	WriteKeyScoped bool   `json:"write_key_scoped"`
	Fallback       string `json:"fallback"` // "manual-max-0" | "patch"
	VAE            string `json:"vae"`      // "second-cached-model" | "network-volume"
}

// KeysReport is the outcome of `runpod probe --keys`.
type KeysReport struct {
	Keys               map[string]map[string]CallResult `json:"keys"`
	TTLExpiry          *TTLShape                        `json:"ttl_expiry,omitempty"`
	CancelledShape     *CancelShape                     `json:"cancelled_shape,omitempty"`
	NeverRanShapes     *NeverRanShapes                  `json:"never_ran_shapes,omitempty"`
	JobNotFound        *ErrShape                        `json:"job_not_found,omitempty"`
	EndpointNotFound   *ErrShape                        `json:"endpoint_not_found,omitempty"`
	RetryAllowed       bool                             `json:"retry_allowed"`
	BillingReadable    bool                             `json:"billing_readable"`
	IdleBillingZero24h *bool                            `json:"idle_billing_zero_24h"`
	SkewMS             *int64                           `json:"skew_ms"`
	Recommendation     Recommendation                   `json:"recommendation"`
}

// Capabilities is key-capabilities.json.
type Capabilities struct {
	Format   string        `json:"format"`
	Probed   bool          `json:"probed"`
	ProbedAt string        `json:"probed_at,omitempty"`
	Schema   *SchemaReport `json:"schema,omitempty"`
	*KeysReport
}

// ProbeOptions builds the key probe. The client must have been built without an admin key.
type ProbeOptions struct {
	Client *Client
	Keys   config.RunPodKeys
	Clock  clock.Clock
	// NewID returns a random UUID-shaped string (default: crypto/rand).
	NewID func() string
	// PollEvery and PollFor shape the TTL observation of job A (defaults 2 s and 30 s).
	PollEvery, PollFor time.Duration
}

type prober struct {
	o   ProbeOptions
	c   *Client
	rep *KeysReport
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (p *prober) record(kind, name string, r CallResult) {
	if p.rep.Keys[kind] == nil {
		p.rep.Keys[kind] = map[string]CallResult{}
	}
	p.rep.Keys[kind][name] = r
}

// verdict classifies an outcome (C77.8): a 2xx, or a 400/404/422 that proves authentication passed, is
// allowed; 401/403 forbidden; the Cloudflare 1010 block edge_blocked.
func verdict(err error) CallResult {
	if err == nil {
		return CallResult{Verdict: VerdictAllowed, HTTP: 200}
	}
	var re *Error
	if !errors.As(err, &re) {
		return CallResult{Verdict: VerdictError, Note: "no response"}
	}
	switch {
	case re.Code == EdgeBlocked:
		return CallResult{Verdict: VerdictEdgeBlocked, HTTP: re.Status}
	case re.Code == Unauthorized || re.Code == Forbidden:
		return CallResult{Verdict: VerdictForbidden, HTTP: re.Status}
	case re.Status == 400 || re.Status == 404 || re.Status == 422:
		return CallResult{Verdict: VerdictAllowed, HTTP: re.Status}
	}
	return CallResult{Verdict: VerdictError, HTTP: re.Status, Note: string(re.Code)}
}

// try records a call. Cancellation of ctx aborts the probe; a missing key is not recorded.
func (p *prober) try(ctx context.Context, kind, name string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, provider.ErrNotPermitted) {
		return nil
	}
	p.record(kind, name, verdict(err))
	return nil
}

func shapeOf(err error) *ErrShape {
	var re *Error
	if !errors.As(err, &re) {
		return nil
	}
	return &ErrShape{HTTP: re.Status, Code: string(re.Code), Title: cutUTF8(re.Title, 80)}
}

// ProbeKeys runs the spend-free key probe of C77.8. It never reads the admin key and never sends
// `workers`, `scaling` or `gpu` in a PATCH.
func ProbeKeys(ctx context.Context, o ProbeOptions) (*KeysReport, error) {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.NewID == nil {
		o.NewID = randomID
	}
	if o.PollEvery <= 0 {
		o.PollEvery = 2 * time.Second
	}
	if o.PollFor <= 0 {
		o.PollFor = 30 * time.Second
	}
	p := &prober{o: o, c: o.Client, rep: &KeysReport{Keys: map[string]map[string]CallResult{}}}
	haveRead := o.Keys.Read != ""
	readKind := KeyRead
	if !haveRead {
		readKind = KeyWrite
	}

	// 1. the endpoint, before anything else (the spend-free guard).
	var ep Endpoint
	var epErr error
	if haveRead || o.Keys.Write != "" {
		ep, epErr = p.c.GetEndpoint(ctx, readKind)
		if err := p.try(ctx, string(readKind), "get_endpoint", epErr); err != nil {
			return p.rep, err
		}
	}
	active := epErr != nil || ep.Workers.Max != 0

	// 2. the job API (jobs key).
	if o.Keys.Jobs != "" {
		if err := p.jobAPI(ctx); err != nil {
			return p.rep, err
		}
		if err := p.runs(ctx, readKind, active); err != nil {
			return p.rep, err
		}
		// purge_queue would delete real queued jobs of an active endpoint: probe it only while inactive.
		if active {
			p.record("jobs", "purge_queue", CallResult{Verdict: VerdictSkipped, Note: "endpoint active"})
		} else {
			err := p.c.PurgeQueue(ctx)
			if e := p.try(ctx, "jobs", "purge_queue", err); e != nil {
				return p.rep, e
			}
			_, err = p.c.Health(ctx)
			if e := p.try(ctx, "jobs", "health_after", err); e != nil {
				return p.rep, e
			}
		}
	}

	// 3. name-only PATCH tests (write and read keys).
	if epErr == nil {
		for _, k := range []KeyKind{KeyWrite, KeyRead} {
			if (k == KeyWrite && o.Keys.Write == "") || (k == KeyRead && !haveRead) {
				continue
			}
			if err := p.namePatch(ctx, k, readKind, ep.Name); err != nil {
				return p.rep, err
			}
		}
	}

	// 4. reads.
	if haveRead {
		_, err := p.c.ListEndpoints(ctx, KeyRead)
		if e := p.try(ctx, "read", "list_endpoints", err); e != nil {
			return p.rep, e
		}
		_, err = p.c.Workers(ctx)
		if e := p.try(ctx, "read", "workers", err); e != nil {
			return p.rep, e
		}
		_, err = p.c.CatalogGPUs(ctx, KeyRead)
		if e := p.try(ctx, "read", "catalog", err); e != nil {
			return p.rep, e
		}
		if err := p.billing(ctx, active && epErr == nil); err != nil {
			return p.rep, err
		}
		_, _, err = p.c.Balance(ctx)
		if e := p.try(ctx, "read", "graphql_balance", err); e != nil {
			return p.rep, e
		}
	}
	if d, ok := p.c.Skew(); ok {
		ms := d.Milliseconds()
		p.rep.SkewMS = &ms
	}
	p.recommend()
	return p.rep, nil
}

// jobAPI probes the job API calls that cannot spend.
func (p *prober) jobAPI(ctx context.Context) error {
	_, err := p.c.Health(ctx)
	if e := p.try(ctx, "jobs", "health", err); e != nil {
		return e
	}
	_, err = p.c.Status(ctx, p.o.NewID())
	if e := p.try(ctx, "jobs", "status", err); e != nil {
		return e
	}
	p.rep.JobNotFound = shapeOf(err)
	// health on a random endpoint id records the endpoint-not-found shape
	_, err = p.c.do(ctx, call{op: "health_other", method: http.MethodGet, jobsHost: true, path: "/v2/" + pathSeg(p.o.NewID()) + "/health",
		template: "/v2/<endpoint>/health", key: KeyJobs, timeout: OtherTimeout, maxBody: MaxRequestBody})
	if e := p.try(ctx, "jobs", "health_other_endpoint", err); e != nil {
		return e
	}
	p.rep.EndpointNotFound = shapeOf(err)
	if err == nil {
		p.rep.EndpointNotFound = &ErrShape{HTTP: 200, Code: "OK"}
	}
	_, err = p.c.Cancel(ctx, p.o.NewID())
	if e := p.try(ctx, "jobs", "cancel", err); e != nil {
		return e
	}
	// retry is recorded only: the app never implements /retry. The id is random, so nothing is requeued.
	_, err = p.c.do(ctx, call{op: "retry", method: http.MethodPost, jobsHost: true, path: p.c.endpointPath("/retry/" + pathSeg(p.o.NewID())),
		template: "/v2/<endpoint>/retry/{id}", key: KeyJobs, timeout: OtherTimeout, maxBody: MaxRequestBody})
	r := verdict(err)
	if e := p.try(ctx, "jobs", "retry", err); e != nil {
		return e
	}
	p.rep.RetryAllowed = r.Verdict == VerdictAllowed
	return nil
}

// runs submits the two spend-free jobs, only while the endpoint is inactive, re-checked before each /run.
func (p *prober) runs(ctx context.Context, readKind KeyKind, active bool) error {
	input := json.RawMessage(`{"contract":"probe"}`)
	pol := Policy{ExecutionTimeoutMS: 5000, TTLMS: 10000}
	skip := func(note string) { p.record("jobs", "run", CallResult{Verdict: VerdictSkipped, Note: note}) }
	if readKind == KeyWrite && p.o.Keys.Write == "" {
		skip("endpoint state unknown")
		return nil
	}
	guard := func() (bool, error) {
		ep, err := p.c.GetEndpoint(ctx, readKind)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return err == nil && ep.Workers.Max == 0, nil // unreadable counts as not safe
	}
	if active {
		skip("endpoint active")
		return nil
	}
	var ids []string
	for i := 0; i < 2; i++ {
		ok, err := guard()
		if err != nil {
			return err
		}
		if !ok {
			name := "run"
			if i == 1 {
				name = "run_b"
			}
			p.record("jobs", name, CallResult{Verdict: VerdictSkipped, Note: "endpoint active"})
			ids = append(ids, "")
			continue
		}
		res, err := p.c.Run(ctx, input, pol)
		name := "run"
		if i == 1 {
			name = "run_b"
		}
		if e := p.try(ctx, "jobs", name, err); e != nil {
			return e
		}
		if err != nil {
			ids = append(ids, "")
			continue
		}
		ids = append(ids, res.ID)
	}
	if len(ids) == 2 && ids[1] != "" {
		st, err := p.c.Cancel(ctx, ids[1])
		if e := p.try(ctx, "jobs", "cancel_queued", err); e != nil {
			return e
		}
		if err == nil {
			cs := &CancelShape{CancelHTTP: 200, CancelStatus: st.Status}
			after, aerr := p.c.Status(ctx, ids[1])
			if aerr == nil {
				cs.StatusAfter, cs.ExecutionTimeMS = after.Status, after.ExecutionMS
			} else if re := new(*Error); errors.As(aerr, re) {
				cs.StatusAfter = fmt.Sprintf("http_%d", (*re).Status)
			}
			if cs.ExecutionTimeMS == nil {
				cs.ExecutionTimeMS = st.ExecutionMS
			}
			p.rep.CancelledShape = cs
		}
	}
	if len(ids) > 0 && ids[0] != "" {
		if err := p.observeTTL(ctx, ids[0]); err != nil {
			return err
		}
	}
	p.rep.NeverRanShapes = &NeverRanShapes{
		TTLHasExecutionTime:       p.rep.TTLExpiry != nil && p.rep.TTLExpiry.ExecutionTimeMS != nil,
		CancelledHasExecutionTime: p.rep.CancelledShape != nil && p.rep.CancelledShape.ExecutionTimeMS != nil,
	}
	return nil
}

func terminal(s string) bool {
	switch s {
	case "COMPLETED", "FAILED", "CANCELLED", "TIMED_OUT":
		return true
	}
	return false
}

// observeTTL polls job A every PollEvery for up to PollFor to record the TTL-expiry shape.
func (p *prober) observeTTL(ctx context.Context, id string) error {
	start := p.o.Clock.Now()
	shape := &TTLShape{}
	for {
		st, err := p.c.Status(ctx, id)
		shape.Polls++
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			var re *Error
			if errors.As(err, &re) {
				shape.HTTP, shape.Status, shape.Terminal = re.Status, "http_"+fmt.Sprint(re.Status), true
			}
			break
		}
		shape.HTTP, shape.Status, shape.ExecutionTimeMS = 200, st.Status, st.ExecutionMS
		if terminal(st.Status) {
			shape.Terminal = true
			break
		}
		if p.o.Clock.Now().Sub(start)+p.o.PollEvery > p.o.PollFor {
			break
		}
		if err := sleep(ctx, p.o.Clock, p.o.PollEvery); err != nil {
			return err
		}
	}
	p.rep.TTLExpiry = shape
	return nil
}

// namePatch sends {"name": <current name>} and nothing else, with a GET before and after; any
// difference aborts the probe.
func (p *prober) namePatch(ctx context.Context, k, readKind KeyKind, name string) error {
	before, err := p.c.GetEndpoint(ctx, readKind)
	if err != nil {
		return p.try(ctx, string(k), "endpoint_patch", err)
	}
	n := name
	_, perr := p.c.PatchEndpoint(ctx, k, EndpointPatch{Name: &n})
	if e := p.try(ctx, string(k), "endpoint_patch", perr); e != nil {
		return e
	}
	after, err := p.c.GetEndpoint(ctx, readKind)
	if err != nil {
		return ErrProbeCritical
	}
	if !reflect.DeepEqual(before, after) {
		p.record(string(k), "endpoint_patch", CallResult{Verdict: VerdictError, HTTP: 200, Note: "endpoint changed"})
		return ErrProbeCritical
	}
	return nil
}

// billing reads the last 25 hourly buckets; with an inactive endpoint they must sum to zero.
func (p *prober) billing(ctx context.Context, active bool) error {
	end := p.o.Clock.Now().UTC().Truncate(time.Hour).Add(time.Hour)
	recs, err := p.c.BillingServerless(ctx, BillingQuery{Start: end.Add(-25 * time.Hour), End: end})
	if e := p.try(ctx, "read", "billing", err); e != nil {
		return e
	}
	if err != nil {
		p.rep.BillingReadable = false
		return nil //nolint:nilerr // the verdict is recorded by try; an unreadable bill is a result, not a failure
	}
	p.rep.BillingReadable = true
	if active {
		return nil // an active endpoint may have billed: no conclusion
	}
	zero := true
	for _, r := range recs {
		for _, lit := range []string{r.Total, r.GPU, r.CPU, r.Disk} {
			if m, perr := money.ParseUSD(lit); perr != nil || m != 0 {
				zero = false
			}
		}
	}
	p.rep.IdleBillingZero24h = &zero
	return nil
}

func (p *prober) recommend() {
	w := p.rep.Keys["write"]["endpoint_patch"]
	scoped := w.Verdict == VerdictAllowed
	p.rep.Recommendation = Recommendation{WriteKeyScoped: scoped, Fallback: "manual-max-0", VAE: "second-cached-model"}
	if scoped {
		p.rep.Recommendation.Fallback = "patch"
	}
}

// BuildCapabilities assembles key-capabilities.json from a schema report and/or a keys report.
func BuildCapabilities(now time.Time, schema *SchemaReport, keys *KeysReport) Capabilities {
	c := Capabilities{Format: CapabilitiesFormat, Schema: schema, KeysReport: keys}
	if keys != nil {
		c.Probed = true
		c.ProbedAt = now.UTC().Format(time.RFC3339)
	}
	return c
}

// EmbeddedCapabilities is the committed key-capabilities.json (a placeholder until M3-X2); wiring passes
// it to BackendOptions.ProbeJSON.
//
//go:embed key-capabilities.json
var EmbeddedCapabilities []byte

// Placeholder is the committed key-capabilities.json until M3-X2 runs the key probe.
const Placeholder = `{"format":"mm.runpod-probe/2","probed":false}`

var longRun = regexp.MustCompile(`[A-Za-z0-9_-]{32,}`)

// letterRun matches a run of letters only: a schema identifier such as a long CamelCase component name.
// Key material, account ids and endpoint ids always carry digits (or separators), so letter-only runs are
// the one shape allowed inside the schema path lists.
var letterRun = regexp.MustCompile(`^[A-Za-z]+$`)

// digestHex is a spec digest (the SHA-256 prefix or the whole digest) in a known digest field.
var digestHex = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// CheckFileSafe refuses a capabilities document that could carry key material, an account id or the
// endpoint id: any run of 32 or more [A-Za-z0-9_-] characters, the endpoint id, a bearer or URL.
//
// Two places hold values that are not secrets but can legitimately be long, and only these are relaxed:
//   - schema.snapshot_sha / schema.live_sha: lowercase hex digests (up to a full 64-digit SHA-256);
//   - schema.changed[] / schema.failures[]: paths and messages built from the public spec, whose
//     identifiers (long CamelCase component names) may exceed 31 characters. Within them, a long run of
//     letters only passes; a long run with digits, "_" or "-" (token-like) is still refused.
//
// Everywhere else, including any other field of the schema report, the 32-character rule is unchanged.
func CheckFileSafe(raw []byte, endpointID string) error {
	if longRun.Match(stripKnownLongFields(raw)) {
		return errors.New("runpod: capabilities contain a long opaque run; refusing to write")
	}
	if endpointID != "" && bytes.Contains(raw, []byte(endpointID)) {
		return errors.New("runpod: capabilities contain the endpoint id; refusing to write")
	}
	for _, bad := range []string{"Bearer ", "http://", "https://", "Authorization"} {
		if bytes.Contains(raw, []byte(bad)) {
			return errors.New("runpod: capabilities contain a URL or credential marker; refusing to write")
		}
	}
	return nil
}

// stripKnownLongFields returns raw with the relaxed values of CheckFileSafe replaced by "-" (the
// document is returned unchanged when it is not JSON with a schema object).
func stripKnownLongFields(raw []byte) []byte {
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return raw
	}
	schema, _ := doc["schema"].(map[string]any)
	if schema == nil {
		return raw
	}
	for _, k := range []string{"snapshot_sha", "live_sha"} {
		if v, ok := schema[k].(string); ok && digestHex.MatchString(v) {
			schema[k] = "-"
		}
	}
	for _, k := range []string{"changed", "failures"} {
		list, _ := schema[k].([]any)
		for i, e := range list {
			if str, ok := e.(string); ok {
				list[i] = softenIdentifiers(str)
			}
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

// softenIdentifiers replaces every long letters-only run of s by "-"; other runs are kept for the check.
func softenIdentifiers(s string) string {
	return longRun.ReplaceAllStringFunc(s, func(run string) string {
		if letterRun.MatchString(run) {
			return "-"
		}
		return run
	})
}

// ValidateCapabilities checks the structure of key-capabilities.json (the placeholder included).
func ValidateCapabilities(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c Capabilities
	if err := dec.Decode(&c); err != nil {
		return fmt.Errorf("runpod: key-capabilities.json: %w", errors.Unwrap(err))
	}
	if c.Format != CapabilitiesFormat {
		return errors.New("runpod: key-capabilities.json: unknown format")
	}
	if !c.Probed {
		if c.KeysReport != nil {
			return errors.New("runpod: key-capabilities.json: an unprobed file carries key results")
		}
		return nil
	}
	if c.KeysReport == nil || c.Keys == nil || c.ProbedAt == "" {
		return errors.New("runpod: key-capabilities.json: probed file lacks key results")
	}
	if _, err := time.Parse(time.RFC3339, c.ProbedAt); err != nil {
		return errors.New("runpod: key-capabilities.json: probed_at is not RFC 3339")
	}
	for kind, calls := range c.Keys {
		if kind != "jobs" && kind != "read" && kind != "write" {
			return errors.New("runpod: key-capabilities.json: unknown key kind")
		}
		for _, r := range calls {
			switch r.Verdict {
			case VerdictAllowed, VerdictForbidden, VerdictEdgeBlocked, VerdictError, VerdictSkipped:
			default:
				return errors.New("runpod: key-capabilities.json: unknown verdict")
			}
		}
	}
	if c.Recommendation.Fallback != "manual-max-0" && c.Recommendation.Fallback != "patch" {
		return errors.New("runpod: key-capabilities.json: bad recommendation.fallback")
	}
	if c.Recommendation.VAE != "second-cached-model" && c.Recommendation.VAE != "network-volume" {
		return errors.New("runpod: key-capabilities.json: bad recommendation.vae")
	}
	return nil
}
