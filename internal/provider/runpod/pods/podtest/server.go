// Package podtest is TEST-ONLY: an httptest server that speaks the RunPod v2 pod API the pods client uses (create, get, list,
// delete, queued-pod cancel, logs, catalog prices, pod billing) with fault injection, call counters and request capture. It
// checks every create body against the v2 `CreatePodRequest` (unknown properties are a 422, like `unevaluatedProperties:
// false`). It must be imported only from _test.go files (TestNoTestHelpersInBinary).
package podtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
)

// Operation names, used by Fault, Calls and Requests.
const (
	OpCreate       = "create_pod"
	OpGet          = "get_pod"
	OpList         = "list_pods"
	OpDelete       = "delete_pod"
	OpCancelQueued = "cancel_queued"
	OpLogs         = "pod_logs"
	OpCatalog      = "catalog"
	OpBilling      = "pod_billing"
)

// Options configures New. Key is the bearer token every call must carry (empty: any non-empty token is accepted).
type Options struct {
	Key      string
	Clock    clock.Clock // default clock.Real
	PageSize int         // list page size, default 1000
	// Prices maps a catalog GPU type id to its `price.secure` literal ("0.34"); the default has two types.
	Prices map[string]string
	// KeepTerminated makes a DELETE leave the pod visible with status TERMINATED instead of removing it (a GET then answers 200).
	KeepTerminated bool
}

// Fault replaces the next Count answers of an operation (Count 0 means 1).
type Fault struct {
	Count      int
	Status     int    // the answer status (default 500)
	Body       string // default a problem+json with Detail
	Detail     string
	RetryAfter string // Retry-After header
	Location   string // Location header (a redirect the client must not follow)
	RateLimit  string // RateLimit header
	Reset      bool   // close the connection without an answer
	Hang       bool   // never answer (until the server closes)
	// AfterEffect applies the operation (a create makes the pod, a delete deletes it) before the fault answers: an ambiguous failure.
	AfterEffect bool
}

// Req is one captured request.
type Req struct {
	Op, Method, Path, Query string
	Header                  http.Header
	Body                    []byte
}

// Pod is a pod as the fake keeps it.
type Pod struct {
	ID, Name, Status, Image, GPU string
	Cost                         string // JSON number literal
	CreatedAt                    time.Time
	StartedAt                    *time.Time
	Env                          map[string]string
	Body                         json.RawMessage // the create body as received
}

// BillRecord is one pod billing bucket; amounts are JSON number literals.
type BillRecord struct {
	Start, End            time.Time
	PodID                 string
	Total, GPU, CPU, Disk string
}

// Server is the fake.
type Server struct {
	*httptest.Server
	o Options

	mu       sync.Mutex
	pods     map[string]*Pod
	order    []string
	next     int
	faults   map[string][]*Fault
	counts   map[string]int
	reqs     []Req
	logs     map[string][]string
	holdLogs bool
	billing  []BillRecord
	prices   map[string]string
	queued   map[string]string // queue id -> pod id ("" when none)
	queueOn  bool
	queuePod bool
	done     chan struct{}
	deleted  map[string]bool
}

// New starts the fake and registers its cleanup.
func New(t testing.TB, o Options) *Server {
	t.Helper()
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.PageSize <= 0 {
		o.PageSize = 1000
	}
	s := &Server{o: o, pods: map[string]*Pod{}, faults: map[string][]*Fault{}, counts: map[string]int{}, logs: map[string][]string{},
		prices: map[string]string{"NVIDIA GeForce RTX 4090": "0.69", "NVIDIA L40S": "0.86"}, queued: map[string]string{},
		done: make(chan struct{}), deleted: map[string]bool{}}
	for k, v := range o.Prices {
		s.prices[k] = v
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Close releases hung requests and stops the server.
func (s *Server) Close() {
	s.mu.Lock()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.mu.Unlock()
	s.CloseClientConnections()
	s.Server.Close()
}

// Fault queues a fault for an operation.
func (s *Server) Fault(op string, f Fault) {
	if f.Count <= 0 {
		f.Count = 1
	}
	s.mu.Lock()
	s.faults[op] = append(s.faults[op], &f)
	s.mu.Unlock()
}

// QueueNextCreate makes the next create answer 202 with a queue entry; withPod also resolves the entry to a live pod.
func (s *Server) QueueNextCreate(withPod bool) {
	s.mu.Lock()
	s.queueOn, s.queuePod = true, withPod
	s.mu.Unlock()
}

// SetStatus changes a pod's status (RUNNING sets startedAt).
func (s *Server) SetStatus(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pods[id]; p != nil {
		p.Status = status
		if status == "RUNNING" && p.StartedAt == nil {
			t := s.o.Clock.Now().UTC()
			p.StartedAt = &t
		}
	}
}

// AddPod inserts a pod that was not created through the API (an orphan or a foreign pod).
func (s *Server) AddPod(p Pod) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = s.o.Clock.Now().UTC()
	}
	if p.Cost == "" {
		p.Cost = "0.69"
	}
	if p.Status == "" {
		p.Status = "RUNNING"
	}
	cp := p
	s.pods[p.ID] = &cp
	s.order = append(s.order, p.ID)
}

// Pod returns a copy of a pod.
func (s *Server) Pod(id string) (Pod, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pods[id]
	if p == nil {
		return Pod{}, false
	}
	return *p, true
}

// Pods returns every live pod in creation order.
func (s *Server) Pods() []Pod {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Pod
	for _, id := range s.order {
		if p := s.pods[id]; p != nil {
			out = append(out, *p)
		}
	}
	return out
}

// Deleted reports whether a pod id (or queue id) was deleted through the API.
func (s *Server) Deleted(id string) bool { s.mu.Lock(); defer s.mu.Unlock(); return s.deleted[id] }

// SetLogs sets the container log lines of a pod; Hold keeps the stream open after the lines (the client must stop on its own).
func (s *Server) SetLogs(id string, lines ...string) { s.mu.Lock(); s.logs[id] = lines; s.mu.Unlock() }

// HoldLogs keeps every log stream open after its lines.
func (s *Server) HoldLogs(b bool) { s.mu.Lock(); s.holdLogs = b; s.mu.Unlock() }

// SetBilling replaces the billing records.
func (s *Server) SetBilling(r []BillRecord) { s.mu.Lock(); s.billing = r; s.mu.Unlock() }

// SetPrice sets or (empty literal) removes the secure price of a catalog type; "null" answers a null price.
func (s *Server) SetPrice(typeID, lit string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lit == "" {
		delete(s.prices, typeID)
		return
	}
	s.prices[typeID] = lit
}

// Calls counts the requests of an operation.
func (s *Server) Calls(op string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.counts[op] }

// Requests returns every captured request.
func (s *Server) Requests() []Req {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Req(nil), s.reqs...)
}

// Last returns the last captured request of an operation.
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

func route(r *http.Request) (op, id string) {
	p := r.URL.Path
	switch {
	case p == "/v2/pods" && r.Method == http.MethodPost:
		return OpCreate, ""
	case p == "/v2/pods" && r.Method == http.MethodGet:
		return OpList, ""
	case strings.HasPrefix(p, "/v2/pods/") && strings.HasSuffix(p, "/logs") && r.Method == http.MethodGet:
		return OpLogs, strings.TrimSuffix(strings.TrimPrefix(p, "/v2/pods/"), "/logs")
	case strings.HasPrefix(p, "/v2/pods/") && !strings.Contains(strings.TrimPrefix(p, "/v2/pods/"), "/"):
		id = strings.TrimPrefix(p, "/v2/pods/")
		switch r.Method {
		case http.MethodGet:
			return OpGet, id
		case http.MethodDelete:
			return OpDelete, id
		}
	case strings.HasPrefix(p, "/v2/queued-pods/") && r.Method == http.MethodDelete:
		return OpCancelQueued, strings.TrimPrefix(p, "/v2/queued-pods/")
	case p == "/v2/catalog/gpus" && r.Method == http.MethodGet:
		return OpCatalog, ""
	case p == "/v2/billing/pods" && r.Method == http.MethodGet:
		return OpBilling, ""
	}
	return "", ""
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	op, id := route(r)
	s.mu.Lock()
	s.counts[op]++
	s.reqs = append(s.reqs, Req{Op: op, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
	var f *Fault
	if q := s.faults[op]; len(q) > 0 {
		f = q[0]
		if f.Count--; f.Count <= 0 {
			s.faults[op] = q[1:]
		}
	}
	s.mu.Unlock()

	if op == "" {
		problem(w, 404, "Not Found", "no such route")
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" || tok == r.Header.Get("Authorization") || (s.o.Key != "" && tok != s.o.Key) {
		problem(w, 401, "Unauthorized", "missing or invalid bearer token")
		return
	}
	if f != nil && !f.AfterEffect {
		s.fault(w, r, f)
		return
	}
	out := w
	if f != nil {
		out = &discard{header: http.Header{}}
	}
	switch op {
	case OpCreate:
		s.create(out, body)
	case OpGet:
		s.get(out, id)
	case OpList:
		s.list(out, r)
	case OpDelete:
		s.deletePod(out, id)
	case OpCancelQueued:
		s.cancelQueued(out, id)
	case OpLogs:
		s.streamLogs(out, r, id)
	case OpCatalog:
		s.catalog(out)
	case OpBilling:
		s.billingList(out)
	}
	if f != nil {
		s.fault(w, r, f)
	}
}

// discard swallows the answer of an operation whose fault answers instead (an applied effect with a lost reply).
type discard struct {
	header http.Header
}

func (d *discard) Header() http.Header         { return d.header }
func (d *discard) Write(b []byte) (int, error) { return len(b), nil }
func (d *discard) WriteHeader(int)             {}

func (s *Server) fault(w http.ResponseWriter, r *http.Request, f *Fault) {
	switch {
	case f.Hang:
		select {
		case <-r.Context().Done():
		case <-s.done:
		}
		return
	case f.Reset:
		if h, ok := w.(http.Hijacker); ok {
			if c, _, err := h.Hijack(); err == nil {
				_ = c.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	status := f.Status
	if status == 0 {
		status = 500
	}
	if f.RetryAfter != "" {
		w.Header().Set("Retry-After", f.RetryAfter)
	}
	if f.Location != "" {
		w.Header().Set("Location", f.Location)
	}
	if f.RateLimit != "" {
		w.Header().Set("RateLimit", f.RateLimit)
	}
	if f.Body != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, f.Body)
		return
	}
	detail := f.Detail
	if detail == "" {
		detail = "injected fault"
	}
	problem(w, status, http.StatusText(status), detail)
}

func problem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"title": title, "status": status, "detail": detail})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var (
	createProps = map[string]bool{"name": true, "image": true, "gpu": true, "cpu": true, "cloud": true, "disk": true, "env": true, "ports": true,
		"mounts": true, "dataCenterIds": true, "startSsh": true, "startJupyter": true, "templateId": true, "globalNetworking": true, "queue": true,
		"args": true, "cmd": true, "entrypoint": true, "registry": true}
	gpuProps = map[string]bool{"id": true, "count": true, "minCudaVersion": true, "allowedCudaVersions": true, "minRamPerGpu": true, "minVcpuCountPerGpu": true}
)

// checkCreate validates a create body against CreatePodRequest: required name, image unless templateId, gpu.id, no unknown property.
func checkCreate(body []byte) (map[string]json.RawMessage, []string) {
	var m map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&m); err != nil {
		return nil, []string{"body is not a JSON object"}
	}
	var errs []string
	for k := range m {
		if !createProps[k] {
			errs = append(errs, "unevaluated property: "+k)
		}
	}
	var name string
	if json.Unmarshal(m["name"], &name) != nil || name == "" {
		errs = append(errs, "name is required")
	}
	if _, ok := m["templateId"]; !ok {
		var image string
		if json.Unmarshal(m["image"], &image) != nil || image == "" {
			errs = append(errs, "image is required without templateId")
		}
	}
	_, hasGPU := m["gpu"]
	_, hasCPU := m["cpu"]
	if hasGPU == hasCPU {
		errs = append(errs, "exactly one of gpu or cpu")
	}
	if hasGPU {
		var g map[string]json.RawMessage
		if json.Unmarshal(m["gpu"], &g) != nil {
			errs = append(errs, "gpu must be an object")
		} else {
			for k := range g {
				if !gpuProps[k] {
					errs = append(errs, "unevaluated property: gpu."+k)
				}
			}
			var id string
			if json.Unmarshal(g["id"], &id) != nil || id == "" {
				errs = append(errs, "gpu.id is required")
			}
		}
	}
	sort.Strings(errs)
	return m, errs
}

func (s *Server) create(w http.ResponseWriter, body []byte) {
	if len(body) > 102400 {
		problem(w, 413, "Payload Too Large", "request body over 102400 bytes")
		return
	}
	m, errs := checkCreate(body)
	if len(errs) > 0 {
		writeJSON(w, 422, map[string]any{"title": "Unprocessable Entity", "status": 422, "detail": "the body does not match the contract", "errors": errs})
		return
	}
	var name, image string
	_ = json.Unmarshal(m["name"], &name)
	_ = json.Unmarshal(m["image"], &image)
	var g struct{ ID string }
	_ = json.Unmarshal(m["gpu"], &g)
	var env map[string]string
	_ = json.Unmarshal(m["env"], &env)

	s.mu.Lock()
	defer s.mu.Unlock()
	price, ok := s.prices[g.ID]
	if !ok {
		problem(w, 400, "Bad Request", "this GPU could not be placed")
		return
	}
	s.next++
	p := &Pod{ID: fmt.Sprintf("p%09d", s.next), Name: name, Status: "PROVISIONING", Image: image, GPU: g.ID, Cost: price,
		CreatedAt: s.o.Clock.Now().UTC(), Env: env, Body: append(json.RawMessage(nil), body...)}
	if s.queueOn {
		s.queueOn = false
		qid := fmt.Sprintf("q%09d", s.next)
		podID := ""
		if s.queuePod {
			s.pods[p.ID] = p
			s.order = append(s.order, p.ID)
			podID = p.ID
		}
		s.queued[qid] = podID
		var pid any
		if podID != "" {
			pid = podID
		}
		writeJSON(w, 202, map[string]any{"id": qid, "status": "QUEUED", "podId": pid, "name": name})
		return
	}
	s.pods[p.ID] = p
	s.order = append(s.order, p.ID)
	writeJSON(w, 201, podJSON(p))
}

func podJSON(p *Pod) map[string]any {
	m := map[string]any{"id": p.ID, "name": p.Name, "status": p.Status, "image": p.Image, "cost": json.RawMessage(p.Cost),
		"createdAt": p.CreatedAt.Format(time.RFC3339), "startedAt": nil, "cloud": "SECURE", "gpu": map[string]any{"id": p.GPU, "count": 1}}
	if p.StartedAt != nil {
		m["startedAt"] = p.StartedAt.Format(time.RFC3339)
	}
	return m
}

func (s *Server) get(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pods[id]
	if p == nil {
		problem(w, 404, "Not Found", "pod not found")
		return
	}
	writeJSON(w, 200, podJSON(p))
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// newest first, like the real API
	var ids []string
	for i := len(s.order) - 1; i >= 0; i-- {
		if s.pods[s.order[i]] != nil {
			ids = append(ids, s.order[i])
		}
	}
	start := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil || n < 0 {
			problem(w, 422, "Unprocessable Entity", "bad cursor")
			return
		}
		start = n
	}
	size := s.o.PageSize
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l < size {
		size = l
	}
	end := min(start+size, len(ids))
	pods := []map[string]any{}
	if start < len(ids) {
		for _, id := range ids[start:end] {
			pods = append(pods, podJSON(s.pods[id]))
		}
	}
	pg := map[string]any{"hasNextPage": end < len(ids), "nextCursor": nil}
	if end < len(ids) {
		pg["nextCursor"] = strconv.Itoa(end)
	}
	writeJSON(w, 200, map[string]any{"pods": pods, "pagination": pg})
}

func (s *Server) deletePod(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pods[id]
	if p == nil {
		problem(w, 404, "Not Found", "pod not found")
		return
	}
	s.deleted[id] = true
	if s.o.KeepTerminated {
		p.Status = "TERMINATED"
	} else {
		delete(s.pods, id)
	}
	w.WriteHeader(204)
}

func (s *Server) cancelQueued(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.queued[id]; !ok {
		problem(w, 404, "Not Found", "queue entry not found")
		return
	}
	delete(s.queued, id)
	s.deleted[id] = true
	w.WriteHeader(204)
}

func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	p := s.pods[id]
	lines := append([]string(nil), s.logs[id]...)
	hold := s.holdLogs
	s.mu.Unlock()
	if p == nil {
		problem(w, 404, "Not Found", "pod not found")
		return
	}
	if t, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && t >= 0 && t < len(lines) {
		lines = lines[len(lines)-t:]
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	for i, l := range lines {
		ev, _ := json.Marshal(map[string]string{"ts": "2026-06-01T12:02:03Z", "source": "container", "line": l})
		fmt.Fprintf(w, "id: 2026-06-01T12:02:03Z/%012d\ndata: %s\n\n", i+1, ev)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if hold {
		select {
		case <-r.Context().Done():
		case <-s.done:
		}
	}
}

func (s *Server) catalog(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.prices))
	for id := range s.prices {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	gpus := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		lit := s.prices[id]
		price := map[string]any{"community": json.RawMessage("0.2"), "secure": json.RawMessage(lit)}
		gpus = append(gpus, map[string]any{"id": id, "name": id, "memory": 24, "secure": true, "price": price})
	}
	writeJSON(w, 200, map[string]any{"gpus": gpus})
}

func (s *Server) billingList(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs := make([]map[string]any, 0, len(s.billing))
	for _, b := range s.billing {
		recs = append(recs, map[string]any{"startTime": b.Start.UTC().Format(time.RFC3339), "endTime": b.End.UTC().Format(time.RFC3339), "podId": b.PodID,
			"totalAmount": json.RawMessage(num(b.Total)), "gpuAmount": json.RawMessage(num(b.GPU)), "cpuAmount": json.RawMessage(num(b.CPU)),
			"diskAmount": json.RawMessage(num(b.Disk))})
	}
	writeJSON(w, 200, map[string]any{"records": recs, "metadata": map[string]any{"recordCount": len(recs)}})
}

func num(s string) string {
	if s == "" {
		return "0"
	}
	return s
}
