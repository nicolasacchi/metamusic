// Package runpodtest is TEST-ONLY: an httptest server that speaks both RunPod hosts (management under
// /v2/serverless, /v2/catalog, /v2/billing and /graphql; jobs under /v2/<endpoint>/...) with per-key
// permission modes, fault injection, call counters and a pluggable simulated worker (M3 C78.4).
// It must be imported only from _test.go files (see TestNoTestHelpersInBinary).
package runpodtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
)

// Operation names, used by Allow, Fault, Calls and Requests.
const (
	OpRun            = "run"
	OpStatus         = "status"
	OpCancel         = "cancel"
	OpPurge          = "purge"
	OpHealth         = "health"
	OpGetEndpoint    = "get_endpoint"
	OpPatchEndpoint  = "patch_endpoint"
	OpListEndpoints  = "list_endpoints"
	OpCreateEndpoint = "create_endpoint"
	OpWorkers        = "workers"
	OpCatalog        = "catalog"
	OpBilling        = "billing"
	OpGraphQL        = "graphql"
)

// Allow is the set of operations a key may call.
type Allow map[string]bool

// JobsOnly may call the job API.
func JobsOnly() Allow {
	return Allow{OpRun: true, OpStatus: true, OpCancel: true, OpPurge: true, OpHealth: true}
}

// ReadOnly may GET the management API.
func ReadOnly() Allow {
	return Allow{OpGetEndpoint: true, OpListEndpoints: true, OpWorkers: true, OpCatalog: true, OpBilling: true, OpGraphQL: true}
}

// WriteScoped may read and PATCH the endpoint.
func WriteScoped() Allow {
	a := ReadOnly()
	a[OpPatchEndpoint] = true
	return a
}

// AdminAll may call everything.
func AdminAll() Allow {
	a := WriteScoped()
	for k := range JobsOnly() {
		a[k] = true
	}
	a[OpCreateEndpoint] = true
	return a
}

// KeySet holds the bearer tokens the server accepts (canary values in tests).
type KeySet struct{ Jobs, Read, Write, Admin string }

// Kind names a key kind.
type Kind string

// Key kinds.
const (
	KindJobs  Kind = "jobs"
	KindRead  Kind = "read"
	KindWrite Kind = "write"
	KindAdmin Kind = "admin"
)

// Run is what a simulated worker produced for one job.
type Run struct {
	Output      json.RawMessage
	Failed      bool
	ErrText     string
	ExecutionMS int64
}

// Runner executes a job input when the server decides the job runs.
type Runner func(ctx context.Context, jobID string, input json.RawMessage) Run

// FakeWorker delegates to fake.SimWorker over the real HTTP uploads and the start handshake.
func FakeWorker(w *fake.SimWorker) Runner {
	return func(ctx context.Context, jobID string, input json.RawMessage) Run {
		res := w.Run(ctx, input, jobID)
		return Run{Output: res.Raw, Failed: res.Failed, ErrText: res.ErrText, ExecutionMS: 1500}
	}
}

// Fault is one injected misbehaviour for an operation.
type Fault struct {
	Status int         // HTTP status to answer with (0 with Drop)
	Body   string      // raw response body; empty => a problem+json body
	Header http.Header // extra response headers (RateLimit, Retry-After ...)
	Drop   bool        // read the body, then close the connection without answering
	// AfterAccept (run only): the job is created and will run, and the response is still the fault.
	AfterAccept bool
	Sleep       time.Duration // delay before answering
	Times       int           // how many calls it applies to; 0 means 1, negative means forever
}

// Behavior is the fate of the next job created by /run.
type Behavior struct {
	Expire      bool // TTL expiry while queued: TIMED_OUT without executionTime
	WorkerDeath bool // FAILED, no output, no executionTime
	Hold        bool // never leaves the queue
}

// Options builds a Server.
type Options struct {
	EndpointID string
	Keys       KeySet
	Clock      clock.Clock
	// Allow overrides the default permission of a key kind.
	Allow map[Kind]Allow
	// PatchReplacesNested: a PATCH replaces nested objects and fills defaults (workers.max = 3).
	PatchReplacesNested bool
	// PatchForbidden answers every PATCH with 403 whatever the key.
	PatchForbidden bool
	Runner         Runner
	PollsToRun     int           // status polls before the job completes (default 3: queue, running, done)
	PurgeAfter     time.Duration // finished jobs answer 404 after this long (0: never)
	SkewOffset     time.Duration // added to the Date header
	RequestHost    string        // host in requestUrls (default api.runpod.ai)
}

// Req is one recorded request.
type Req struct {
	Op     string
	Method string
	Path   string
	Query  string
	Key    Kind // "" when no/unknown bearer
	Header http.Header
	Body   []byte
}

type job struct {
	id       string
	input    json.RawMessage
	policy   map[string]any
	polls    int
	state    string
	beh      Behavior
	run      *Run
	finished time.Time
	created  time.Time
}

// Server is the fake RunPod.
type Server struct {
	*httptest.Server
	o Options

	mu       sync.Mutex
	allow    map[Kind]Allow
	endpoint map[string]any
	catalog  []map[string]any
	workers  []map[string]any
	billing  []map[string]any
	balance  string
	jobs     map[string]*job
	nextJob  []Behavior
	faults   map[string][]*Fault
	counts   map[string]int
	reqs     []Req
	seq      int
}

// New starts a server and closes it with t.Cleanup.
func New(t testing.TB, o Options) *Server {
	t.Helper()
	if o.EndpointID == "" {
		o.EndpointID = "ep-canary-0000"
	}
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.PollsToRun <= 0 {
		o.PollsToRun = 3
	}
	if o.RequestHost == "" {
		o.RequestHost = "api.runpod.ai"
	}
	if o.Runner == nil {
		o.Runner = func(context.Context, string, json.RawMessage) Run {
			return Run{Output: json.RawMessage(`{"ok":true}`), ExecutionMS: 1500}
		}
	}
	s := &Server{o: o, jobs: map[string]*job{}, faults: map[string][]*Fault{}, counts: map[string]int{}, balance: "12.34"}
	s.allow = map[Kind]Allow{KindJobs: JobsOnly(), KindRead: ReadOnly(), KindWrite: WriteScoped(), KindAdmin: AdminAll()}
	for k, a := range o.Allow {
		s.allow[k] = a
	}
	s.endpoint = s.defaultEndpoint()
	s.catalog = defaultCatalog()
	s.workers = []map[string]any{}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) defaultEndpoint() map[string]any {
	host := s.o.RequestHost
	u := func(p string) string { return "https://" + host + "/v2/" + s.o.EndpointID + "/" + p }
	return map[string]any{
		"id": s.o.EndpointID, "name": "mm-yue2", "type": "QUEUE",
		"scaling":   map[string]any{"type": "QUEUE_DELAY", "queueDelay": 240},
		"workers":   map[string]any{"min": 0, "max": 1, "idleTimeout": 20},
		"gpu":       map[string]any{"count": 1, "pools": []any{"AMPERE_24", "ADA_24"}, "excludedTypes": []any{"gpu-l4"}, "minCudaVersion": "12.8"},
		"flashboot": "FLASHBOOT", "timeout": 900000,
		"image": "ghcr.io/example/worker@sha256:" + strings.Repeat("0", 64),
		"requestUrls": map[string]any{
			"run": u("run"), "status": u("status/{jobId}"), "cancel": u("cancel/{jobId}"),
			"health": u("health"), "purge-queue": u("purge-queue"),
		},
	}
}

func defaultCatalog() []map[string]any {
	g := func(id, name, pool, price string) map[string]any {
		p := map[string]any{"secure": json.Number("0.27")}
		if price != "" {
			p["serverless"] = json.Number(price)
		}
		return map[string]any{"id": id, "name": name, "pool": pool, "price": p}
	}
	return []map[string]any{
		g("gpu-a5000", "RTX A5000", "AMPERE_24", "0.69"),
		g("gpu-l4", "L4", "AMPERE_24", "0.69"),
		g("gpu-4090", "RTX 4090", "ADA_24", "1.10"),
		g("gpu-h100", "H100", "HOPPER_80", "4.18"),
	}
}

// ---- controls ---------------------------------------------------------------------------------------

// SetAllow replaces the permission of one key kind.
func (s *Server) SetAllow(k Kind, a Allow) { s.mu.Lock(); s.allow[k] = a; s.mu.Unlock() }

// Fault queues a fault for an operation.
func (s *Server) Fault(op string, f Fault) {
	if f.Times == 0 {
		f.Times = 1
	}
	s.mu.Lock()
	s.faults[op] = append(s.faults[op], &f)
	s.mu.Unlock()
}

// NextJob queues the behavior of the next created job.
func (s *Server) NextJob(b Behavior) { s.mu.Lock(); s.nextJob = append(s.nextJob, b); s.mu.Unlock() }

// SetEndpoint edits the endpoint document under the server lock.
func (s *Server) SetEndpoint(f func(ep map[string]any)) { s.mu.Lock(); f(s.endpoint); s.mu.Unlock() }

// Endpoint returns a deep copy of the endpoint document.
func (s *Server) Endpoint() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return deepCopy(s.endpoint)
}

// SetCatalog replaces the GPU catalog.
func (s *Server) SetCatalog(c []map[string]any) { s.mu.Lock(); s.catalog = c; s.mu.Unlock() }

// SetWorkers replaces the worker list.
func (s *Server) SetWorkers(w []map[string]any) { s.mu.Lock(); s.workers = w; s.mu.Unlock() }

// SetBilling replaces the billing records.
func (s *Server) SetBilling(b []map[string]any) { s.mu.Lock(); s.billing = b; s.mu.Unlock() }

// SetBalance sets the GraphQL clientBalance literal ("" makes the field null).
func (s *Server) SetBalance(lit string) { s.mu.Lock(); s.balance = lit; s.mu.Unlock() }

// Calls returns how many requests reached an operation (including faulted and refused ones).
func (s *Server) Calls(op string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.counts[op] }

// Requests returns a copy of every recorded request, oldest first.
func (s *Server) Requests() []Req {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Req(nil), s.reqs...)
}

// Last returns the most recent request of an operation.
func (s *Server) Last(op string) (Req, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.reqs) - 1; i >= 0; i-- {
		if s.reqs[i].Op == op {
			return s.reqs[i], true
		}
	}
	return Req{}, false
}

// ---- routing ----------------------------------------------------------------------------------------

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	op, arg := s.route(r)
	kind := s.keyKind(r)

	s.mu.Lock()
	s.counts[op]++
	s.reqs = append(s.reqs, Req{Op: op, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Key: kind, Header: r.Header.Clone(), Body: body})
	f := s.takeFault(op)
	s.mu.Unlock()

	w.Header().Set("Date", s.o.Clock.Now().Add(s.o.SkewOffset).UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Type", "application/json")
	if op == "" {
		s.problem(w, 404, "Not Found", "no such route")
		return
	}
	if kind == "" {
		s.problem(w, 401, "Unauthorized", "missing or invalid bearer token")
		return
	}
	s.mu.Lock()
	ok := s.allow[kind][op]
	s.mu.Unlock()
	if !ok {
		s.problem(w, 403, "Forbidden", "key lacks permission")
		return
	}
	if op == OpPatchEndpoint && s.o.PatchForbidden {
		s.problem(w, 403, "Forbidden", "patch not permitted")
		return
	}
	if f != nil {
		if f.AfterAccept && op == OpRun {
			s.createJob(body)
		}
		s.applyFault(w, r, f)
		return
	}
	switch op {
	case OpRun:
		s.doRun(w, body)
	case OpStatus:
		s.doStatus(r.Context(), w, arg)
	case OpCancel:
		s.doCancel(w, arg)
	case OpPurge:
		s.mu.Lock()
		n := 0
		for _, j := range s.jobs {
			if j.state == "IN_QUEUE" {
				j.state = "CANCELLED"
				n++
			}
		}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"removed": n, "status": "completed"})
	case OpHealth:
		s.mu.Lock()
		q, p := 0, 0
		for _, j := range s.jobs {
			switch j.state {
			case "IN_QUEUE":
				q++
			case "IN_PROGRESS":
				p++
			}
		}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"jobs": map[string]any{"inQueue": q, "inProgress": p, "completed": 0, "failed": 0, "retried": 0},
			"workers": map[string]any{"idle": 0, "running": 0, "initializing": 0, "ready": 0, "throttled": 0, "unhealthy": 0}})
	case OpGetEndpoint:
		s.mu.Lock()
		writeJSON(w, 200, s.endpoint)
		s.mu.Unlock()
	case OpListEndpoints:
		s.mu.Lock()
		writeJSON(w, 200, []any{s.endpoint})
		s.mu.Unlock()
	case OpPatchEndpoint:
		s.doPatch(w, body)
	case OpCreateEndpoint:
		var m map[string]any
		if json.Unmarshal(body, &m) != nil {
			s.problem(w, 400, "Bad Request", "invalid JSON")
			return
		}
		s.mu.Lock()
		m["id"] = s.o.EndpointID
		s.endpoint = m
		s.mu.Unlock()
		writeJSON(w, 201, m)
	case OpWorkers:
		s.mu.Lock()
		writeJSON(w, 200, s.workers)
		s.mu.Unlock()
	case OpCatalog:
		s.mu.Lock()
		writeJSON(w, 200, s.catalog)
		s.mu.Unlock()
	case OpBilling:
		s.mu.Lock()
		writeJSON(w, 200, s.billing)
		s.mu.Unlock()
	case OpGraphQL:
		s.mu.Lock()
		bal := "null"
		if s.balance != "" {
			bal = s.balance
		}
		s.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"data":{"myself":{"clientBalance":%s}}}`, bal)
	}
}

// route maps a request to an operation and its path argument.
func (s *Server) route(r *http.Request) (op, arg string) {
	seg := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	m := r.Method
	switch {
	case len(seg) == 1 && seg[0] == "graphql" && m == "POST":
		return OpGraphQL, ""
	case len(seg) < 2 || seg[0] != "v2":
		return "", ""
	case seg[1] == "serverless":
		switch {
		case len(seg) == 2 && m == "GET":
			return OpListEndpoints, ""
		case len(seg) == 2 && m == "POST":
			return OpCreateEndpoint, ""
		case len(seg) == 3 && m == "GET":
			return OpGetEndpoint, seg[2]
		case len(seg) == 3 && m == "PATCH":
			return OpPatchEndpoint, seg[2]
		case len(seg) == 4 && seg[3] == "workers" && m == "GET":
			return OpWorkers, seg[2]
		}
	case seg[1] == "catalog" && len(seg) == 3 && seg[2] == "gpus" && m == "GET":
		return OpCatalog, ""
	case seg[1] == "billing" && len(seg) == 3 && seg[2] == "serverless" && m == "GET":
		return OpBilling, ""
	case len(seg) >= 3:
		ep := seg[1]
		switch {
		case seg[2] == "run" && len(seg) == 3 && m == "POST":
			return OpRun, ep
		case seg[2] == "health" && len(seg) == 3 && m == "GET":
			return OpHealth, ep
		case seg[2] == "purge-queue" && len(seg) == 3 && m == "POST":
			return OpPurge, ep
		case seg[2] == "status" && len(seg) == 4 && m == "GET":
			return OpStatus, seg[3]
		case seg[2] == "cancel" && len(seg) == 4 && m == "POST":
			return OpCancel, seg[3]
		}
	}
	return "", ""
}

func (s *Server) keyKind(r *http.Request) Kind {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return ""
	}
	k := s.o.Keys
	switch tok {
	case k.Jobs:
		return KindJobs
	case k.Read:
		return KindRead
	case k.Write:
		return KindWrite
	case k.Admin:
		return KindAdmin
	}
	return ""
}

func (s *Server) takeFault(op string) *Fault {
	q := s.faults[op]
	if len(q) == 0 {
		return nil
	}
	f := q[0]
	if f.Times > 0 {
		f.Times--
		if f.Times == 0 {
			s.faults[op] = q[1:]
		}
	}
	return f
}

func (s *Server) applyFault(w http.ResponseWriter, r *http.Request, f *Fault) {
	if f.Sleep > 0 {
		select {
		case <-time.After(f.Sleep):
		case <-r.Context().Done():
		}
	}
	if f.Drop {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
		return
	}
	for k, v := range f.Header {
		for _, x := range v {
			w.Header().Add(k, x)
		}
	}
	if f.Body != "" {
		w.WriteHeader(f.Status)
		_, _ = io.WriteString(w, f.Body)
		return
	}
	s.problem(w, f.Status, http.StatusText(f.Status), "injected fault")
}

func (s *Server) problem(w http.ResponseWriter, status int, title, detail string) {
	writeJSON(w, status, map[string]any{"title": title, "status": status, "detail": detail, "errors": []string{}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var b bytes.Buffer
	_ = json.NewEncoder(&b).Encode(v)
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}

// ---- jobs -------------------------------------------------------------------------------------------

func (s *Server) createJob(body []byte) *job {
	var req struct {
		Input  json.RawMessage `json:"input"`
		Policy map[string]any  `json:"policy"`
	}
	_ = json.Unmarshal(body, &req)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	j := &job{id: fmt.Sprintf("job-%04d-canary", s.seq), input: req.Input, policy: req.Policy, state: "IN_QUEUE", created: s.o.Clock.Now()}
	if len(s.nextJob) > 0 {
		j.beh, s.nextJob = s.nextJob[0], s.nextJob[1:]
	}
	s.jobs[j.id] = j
	return j
}

func (s *Server) doRun(w http.ResponseWriter, body []byte) {
	var req struct {
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.Input) == 0 {
		s.problem(w, 400, "Bad Request", "input required")
		return
	}
	j := s.createJob(body)
	writeJSON(w, 200, map[string]any{"id": j.id, "status": "IN_QUEUE"})
}

func (s *Server) doStatus(ctx context.Context, w http.ResponseWriter, id string) {
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil || (s.o.PurgeAfter > 0 && !j.finished.IsZero() && s.o.Clock.Now().Sub(j.finished) >= s.o.PurgeAfter) {
		s.mu.Unlock()
		s.problem(w, 404, "Not Found", "job not found")
		return
	}
	j.polls++
	resp := map[string]any{"id": j.id}
	var runner func() Run
	switch {
	case j.state == "IN_QUEUE" && j.beh.Hold:
	case j.state == "IN_QUEUE" && j.beh.Expire:
		j.state, j.finished = "TIMED_OUT", s.o.Clock.Now()
	case j.state == "IN_QUEUE" && j.beh.WorkerDeath && j.polls >= 2:
		j.state, j.finished = "FAILED", s.o.Clock.Now()
		j.run = &Run{Failed: true}
	case j.state == "IN_QUEUE" && j.polls >= 2:
		j.state = "IN_PROGRESS"
	case j.state == "IN_PROGRESS" && j.polls >= s.o.PollsToRun && !j.beh.WorkerDeath:
		input := j.input
		runner = func() Run { return s.o.Runner(ctx, j.id, input) }
	}
	s.mu.Unlock()
	if runner != nil {
		res := runner() // outside the lock: the simulated worker calls back over HTTP
		s.mu.Lock()
		j.run = &res
		j.finished = s.o.Clock.Now()
		j.state = "COMPLETED"
		if res.Failed {
			j.state = "FAILED"
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	resp["status"] = j.state
	if j.state != "IN_QUEUE" {
		resp["delayTime"] = 800
		resp["workerId"] = "worker-canary-1"
	}
	if j.run != nil {
		if j.run.ExecutionMS > 0 {
			resp["executionTime"] = j.run.ExecutionMS
		}
		if len(j.run.Output) > 0 {
			resp["output"] = json.RawMessage(j.run.Output)
		}
		if j.run.ErrText != "" {
			resp["error"] = j.run.ErrText
		}
	}
	writeJSON(w, 200, resp)
}

func (s *Server) doCancel(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil {
		s.problem(w, 404, "Not Found", "job not found")
		return
	}
	if j.state == "IN_QUEUE" || j.state == "IN_PROGRESS" {
		j.state, j.finished = "CANCELLED", s.o.Clock.Now()
	}
	writeJSON(w, 200, map[string]any{"id": j.id, "status": j.state})
}

// JobState returns the current status string of a job.
func (s *Server) JobState(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.jobs[id]; j != nil {
		return j.state
	}
	return ""
}

// JobIDs returns the ids of all jobs created so far, oldest first.
func (s *Server) JobIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.jobs))
	for i := 1; i <= s.seq; i++ {
		ids = append(ids, fmt.Sprintf("job-%04d-canary", i))
	}
	return ids
}

// ---- endpoint PATCH ---------------------------------------------------------------------------------

var nestedKeys = []string{"workers", "gpu", "scaling"}

func (s *Server) doPatch(w http.ResponseWriter, body []byte) {
	var p map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&p) != nil {
		s.problem(w, 400, "Bad Request", "invalid JSON")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range p {
		nested := false
		for _, n := range nestedKeys {
			nested = nested || n == k
		}
		pm, isMap := v.(map[string]any)
		if nested && isMap && !s.o.PatchReplacesNested {
			cur, _ := s.endpoint[k].(map[string]any)
			if cur == nil {
				cur = map[string]any{}
			}
			for ik, iv := range pm {
				cur[ik] = iv
			}
			s.endpoint[k] = cur
			continue
		}
		if nested && isMap && s.o.PatchReplacesNested {
			s.endpoint[k] = withDefaults(k, pm)
			continue
		}
		s.endpoint[k] = v
	}
	writeJSON(w, 200, s.endpoint)
}

func withDefaults(key string, m map[string]any) map[string]any {
	set := func(k string, v any) {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	switch key {
	case "workers":
		set("min", 0)
		set("max", 3)
		set("idleTimeout", 5)
	case "gpu":
		set("count", 1)
	}
	return m
}

func deepCopy(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// Num is a helper for tests editing the endpoint: ints decode as float64 after a round trip.
func Num(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	case json.Number:
		n, _ := strconv.Atoi(x.String())
		return n
	}
	return -1
}
