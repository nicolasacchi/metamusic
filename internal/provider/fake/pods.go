package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

// The fake pod backend (BULK-S1, C399.13). `fake-pod` is the real pods.Backend, poll handler and lifecycle loop over this PodSim: an
// in-process pod API (pods.API) plus one simulated agent per pod. Nothing here reaches a network or the wall clock: every instant is the
// injected clock, and an agent acts only when the test calls Tick, so a timeline is a pure function of its inputs.
//
// The simulated agent follows worker/src/mmworker/agent.py: it polls the signed link from MM_POLL_URL, reports booting, then idle with
// the boot report, runs a handed take through a SimWorker (the same uploads as the serverless worker), reports it done on the next poll,
// stops on `stop`, and has a watchdog that ends the pod at MM_DEADLINE_UNIX and after MM_DEADMAN_S without a successful poll.
//
// Markers (in a recipe's style, read like the other fake: markers; unknown tokens are ignored):
//
//	fake:pod_boot_slow=<s>     the weights take s more seconds to load
//	fake:pod_no_self_terminate the agent cannot end its pod (not on stop, not at the deadline, not by the dead-man)
//	fake:pod_silent            the agent never polls
//	fake:pod_crash_mid_take    the agent process dies when it is handed a take (its watchdog lives on)
//	fake:pod_restart           the container restarts once when it is handed a take

// Marker names of the pod half of the simulated agent.
const (
	MarkPodBootSlow        = "fake:pod_boot_slow"
	MarkPodNoSelfTerminate = "fake:pod_no_self_terminate"
	MarkPodSilent          = "fake:pod_silent"
	MarkPodCrashMidTake    = "fake:pod_crash_mid_take"
	MarkPodRestart         = "fake:pod_restart"
)

// Defaults of the simulation, in seconds unless noted.
const (
	podStartS      = 10 // PROVISIONING until the container starts
	podBootS       = 45 // weights loaded this long after the container started
	podRenderS     = 15 // a take with no fake:slow marker
	podPollEveryS  = 5
	podDefaultCost = money.MicroUSD(340_000) // $0.34/h
	podLogLines    = 50
)

// PodMarkers are the pod markers found in a style string.
type PodMarkers struct {
	BootSlow        time.Duration
	NoSelfTerminate bool
	Silent          bool
	CrashMidTake    bool
	Restart         bool
}

// ParsePodMarkers reads the pod markers of a style (whitespace or comma separated, case-sensitive).
func ParsePodMarkers(style string) PodMarkers {
	var m PodMarkers
	for _, tok := range strings.FieldsFunc(style, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		key, val, _ := strings.Cut(tok, "=")
		switch key {
		case MarkPodBootSlow:
			if f, err := strconv.ParseFloat(val, 64); err == nil && f >= 0 && f <= maxMarkerSeconds {
				m.BootSlow = time.Duration(f * float64(time.Second))
			}
		case MarkPodNoSelfTerminate:
			m.NoSelfTerminate = true
		case MarkPodSilent:
			m.Silent = true
		case MarkPodCrashMidTake:
			m.CrashMidTake = true
		case MarkPodRestart:
			m.Restart = true
		}
	}
	return m
}

// PodSimOptions builds a PodSim.
type PodSimOptions struct {
	Clock clock.Clock
	// Worker renders a handed take and uploads its artifacts (nil: the agent reports the take done and uploads nothing, for tests of the
	// lifecycle alone). SetWorker replaces it once the upload handlers exist.
	Worker *SimWorker
	// Prices maps a GPU type id to its catalog price.secure per hour; DefaultPrice (default $0.34) serves the others.
	Prices       map[string]money.MicroUSD
	DefaultPrice money.MicroUSD
	// Reported overrides the hourly cost the pod API reports for a pod (and bills it at): a RunPod that charges more than the catalog.
	Reported money.MicroUSD
	// StartS and BootS are the seconds from create to the container start and from the start to the weights loaded.
	StartS, BootS int
	// RenderS is how long a take takes when its style has no fake:slow marker.
	RenderS int
	// StyleOf returns the styles of the batch the named pod serves; the pod-level markers are read from it. nil: none.
	StyleOf func(ctx context.Context, podName string) string
}

// PodRecord is a snapshot of one simulated pod.
type PodRecord struct {
	ID, Name, GPU string
	Created       time.Time
	Deleted       *time.Time // when its billing ended; nil while it lives
	DeletedBy     string     // api | self | deadline | deadman | stop | external
	Cost          money.MicroUSD
	AgentDead     bool
	Polls, Takes  int
}

type simPod struct {
	id, name, gpu string
	cost          money.MicroUSD
	created       time.Time
	deleted       *time.Time
	deletedBy     string
	pollURL       string
	deadline      time.Time
	deadman       time.Duration

	marks       *PodMarkers // read at the first tick after the container started
	readyAt     time.Time
	agentDead   bool
	lastOK      time.Time
	nextPoll    time.Time
	booting     bool // the next poll reports booting (first poll, or after a restart)
	restarted   bool
	task        *simTask
	logs        []string
	polls, took int
}

type simTask struct {
	attemptID string
	raw       []byte
	until     time.Time
	execMS    int64
	done      *pods.PollDone
}

// PodSim is the fake pod API and its simulated agents. Safe for concurrent use; Tick must not be called concurrently with itself.
type PodSim struct {
	o   PodSimOptions
	clk clock.Clock

	mu       sync.Mutex
	pods     map[string]*simPod
	order    []string
	seq      int
	worker   *SimWorker
	handler  http.Handler
	refused  map[string]bool
	failNext []createFault
	apiErr   error
	cut      bool // Partition: no poll reaches the app and no API call reaches RunPod
	calls    map[string]int
}

type createFault struct {
	err    error
	effect bool
}

var _ pods.API = (*PodSim)(nil)

// NewPodSim builds the simulation. It starts nothing.
func NewPodSim(o PodSimOptions) *PodSim {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.DefaultPrice <= 0 {
		o.DefaultPrice = podDefaultCost
	}
	if o.StartS <= 0 {
		o.StartS = podStartS
	}
	if o.BootS <= 0 {
		o.BootS = podBootS
	}
	if o.RenderS <= 0 {
		o.RenderS = podRenderS
	}
	return &PodSim{o: o, clk: o.Clock, pods: map[string]*simPod{}, worker: o.Worker, refused: map[string]bool{}, calls: map[string]int{}}
}

// Attach sets the poll handler the agents call (the app's poll route; its route lives after the app is built).
func (p *PodSim) Attach(h http.Handler) {
	p.mu.Lock()
	p.handler = h
	p.mu.Unlock()
}

// SetWorker sets the simulated worker that renders handed takes.
func (p *PodSim) SetWorker(w *SimWorker) {
	p.mu.Lock()
	p.worker = w
	p.mu.Unlock()
}

// Refuse makes a GPU type refuse creates with a capacity error (or stop refusing).
func (p *PodSim) Refuse(gpu string, on bool) {
	p.mu.Lock()
	p.refused[gpu] = on
	p.mu.Unlock()
}

// FailNextCreate makes the next create answer err; with effect the pod is created anyway (an ambiguous outcome).
func (p *PodSim) FailNextCreate(err error, effect bool) {
	p.mu.Lock()
	p.failNext = append(p.failNext, createFault{err, effect})
	p.mu.Unlock()
}

// FailAPI makes every API call answer err until it is cleared with nil (the pod key removed or refused: provider.ErrAccess).
func (p *PodSim) FailAPI(err error) {
	p.mu.Lock()
	p.apiErr = err
	p.mu.Unlock()
}

// Partition cuts the pods from the app and the app from RunPod: while it is on, no agent poll reaches the poll route (the agents' dead-man
// keeps counting) and every API call fails like a dead network. The pods themselves keep running and billing.
func (p *PodSim) Partition(on bool) {
	p.mu.Lock()
	p.cut = on
	p.mu.Unlock()
}

// Calls is how many times an API method was called ("create", "get", "list", "delete", "billing", "catalog", "logs").
func (p *PodSim) Calls(op string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[op]
}

func (p *PodSim) price(gpu string) money.MicroUSD {
	if v, ok := p.o.Prices[gpu]; ok {
		return v
	}
	return p.o.DefaultPrice
}

var errPartition = errors.New("fake: network unreachable")

func (p *PodSim) call(op string) error {
	p.calls[op]++
	if p.cut {
		return errPartition
	}
	return p.apiErr
}

// ---- pods.API ----------------------------------------------------------------------------------------------------------------

// CreatePod implements pods.API.
func (p *PodSim) CreatePod(_ context.Context, b pods.CreateBody) (pods.Pod, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call("create"); err != nil {
		return pods.Pod{}, err
	}
	if err := b.Validate(); err != nil {
		return pods.Pod{}, pods.ErrInvalid
	}
	var f *createFault
	if len(p.failNext) > 0 {
		f, p.failNext = &p.failNext[0], p.failNext[1:]
		if !f.effect {
			return pods.Pod{}, f.err
		}
	}
	if p.refused[b.GPUTypeID] {
		return pods.Pod{}, &pods.ErrRefused{Status: http.StatusBadRequest, Capacity: true, Detail: "no capacity"}
	}
	p.seq++
	now := p.clk.Now()
	sp := &simPod{id: fmt.Sprintf("simpod%06d", p.seq), name: b.Name, gpu: b.GPUTypeID, cost: p.price(b.GPUTypeID), created: now,
		pollURL: b.Env["MM_POLL_URL"], booting: true}
	if p.o.Reported > 0 {
		sp.cost = p.o.Reported
	}
	if n, err := strconv.ParseInt(b.Env["MM_DEADLINE_UNIX"], 10, 64); err == nil {
		sp.deadline = time.Unix(n, 0)
	}
	if n, err := strconv.Atoi(b.Env["MM_DEADMAN_S"]); err == nil {
		sp.deadman = time.Duration(n) * time.Second
	}
	sp.lastOK = now
	p.pods[sp.id] = sp
	p.order = append(p.order, sp.id)
	if f != nil {
		return pods.Pod{}, f.err
	}
	return p.view(sp), nil
}

func (p *PodSim) status(sp *simPod) string {
	if sp.deleted != nil {
		return pods.StatusTerminated
	}
	if p.clk.Now().Before(sp.created.Add(time.Duration(p.o.StartS) * time.Second)) {
		return pods.StatusProvisioning
	}
	return pods.StatusRunning
}

func (p *PodSim) view(sp *simPod) pods.Pod {
	c := sp.created
	out := pods.Pod{ID: sp.id, Name: sp.name, Status: p.status(sp), Priced: true, CostPPH: sp.cost, CreatedAt: &c}
	if out.Status == pods.StatusRunning {
		s := sp.created.Add(time.Duration(p.o.StartS) * time.Second)
		out.StartedAt = &s
	}
	return out
}

// GetPod implements pods.API.
func (p *PodSim) GetPod(_ context.Context, id string) (pods.Pod, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call("get"); err != nil {
		return pods.Pod{}, err
	}
	sp := p.pods[id]
	if sp == nil || sp.deleted != nil {
		return pods.Pod{}, pods.ErrPodGone
	}
	return p.view(sp), nil
}

// ListPods implements pods.API (live pods only).
func (p *PodSim) ListPods(context.Context) ([]pods.Pod, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call("list"); err != nil {
		return nil, err
	}
	var out []pods.Pod
	for _, id := range p.order {
		if sp := p.pods[id]; sp.deleted == nil {
			out = append(out, p.view(sp))
		}
	}
	return out, nil
}

// DeletePod implements pods.API.
func (p *PodSim) DeletePod(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call("delete"); err != nil {
		return err
	}
	if sp := p.pods[id]; sp != nil {
		p.end(sp, "api")
	}
	return nil
}

// CancelQueued implements pods.API.
func (p *PodSim) CancelQueued(context.Context, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.call("cancel")
}

// PodLogsTail implements pods.API.
func (p *PodSim) PodLogsTail(_ context.Context, id string, n int) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call("logs"); err != nil {
		return nil, err
	}
	sp := p.pods[id]
	if sp == nil {
		return nil, pods.ErrPodGone
	}
	l := sp.logs
	if n > 0 && len(l) > n {
		l = l[len(l)-n:]
	}
	return slices.Clone(l), nil
}

// CatalogSecurePrices implements pods.API.
func (p *PodSim) CatalogSecurePrices(_ context.Context, ids []string) (map[string]money.MicroUSD, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call("catalog"); err != nil {
		return nil, err
	}
	out := make(map[string]money.MicroUSD, len(ids))
	for _, id := range ids {
		out[id] = p.price(id)
	}
	return out, nil
}

// PodBilling implements pods.API: one hourly bucket per hour each requested pod lived in [from, to).
func (p *PodSim) PodBilling(_ context.Context, from, to time.Time, ids []string) ([]provider.BillBucket, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call("billing"); err != nil {
		return nil, err
	}
	now := p.clk.Now()
	var out []provider.BillBucket
	for h := from.Truncate(time.Hour); h.Before(to); h = h.Add(time.Hour) {
		var total money.MicroUSD
		for _, id := range ids {
			if sp := p.pods[id]; sp != nil {
				total += p.billed(sp, h, h.Add(time.Hour), now)
			}
		}
		if total > 0 {
			out = append(out, provider.BillBucket{Start: h, End: h.Add(time.Hour), Total: total, Compute: total})
		}
	}
	return out, nil
}

// billed is what sp cost during [from, to).
func (p *PodSim) billed(sp *simPod, from, to, now time.Time) money.MicroUSD {
	end := now
	if sp.deleted != nil {
		end = *sp.deleted
	}
	lo, hi := sp.created, end
	if from.After(lo) {
		lo = from
	}
	if to.Before(hi) {
		hi = to
	}
	if !hi.After(lo) {
		return 0
	}
	return money.ForMillis(hi.Sub(lo).Milliseconds(), sp.cost)
}

// end stops a pod's billing at now. Callers hold p.mu.
func (p *PodSim) end(sp *simPod, by string) {
	if sp.deleted != nil {
		return
	}
	t := p.clk.Now()
	sp.deleted, sp.deletedBy = &t, by
	sp.task = nil
}

// ---- observation -------------------------------------------------------------------------------------------------------------------

// Pods snapshots every pod the simulation ever made, oldest first.
func (p *PodSim) Pods() []PodRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PodRecord, 0, len(p.order))
	for _, id := range p.order {
		sp := p.pods[id]
		r := PodRecord{ID: sp.id, Name: sp.name, GPU: sp.gpu, Created: sp.created, DeletedBy: sp.deletedBy, Cost: sp.cost, AgentDead: sp.agentDead, Polls: sp.polls, Takes: sp.took}
		if sp.deleted != nil {
			d := *sp.deleted
			r.Deleted = &d
		}
		out = append(out, r)
	}
	return out
}

// Live is the number of pods billing now.
func (p *PodSim) Live() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, sp := range p.pods {
		if sp.deleted == nil {
			n++
		}
	}
	return n
}

// Billed is the ground truth: the sum over every pod of its exact lifetime (create to end, or to now while it lives) at its hourly cost.
func (p *PodSim) Billed() money.MicroUSD {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clk.Now()
	var sum money.MicroUSD
	for _, sp := range p.pods {
		sum += p.billed(sp, time.Time{}, now.Add(time.Hour), now)
	}
	return sum
}

// Kill deletes a pod from outside (the console, a RunPod incident).
func (p *PodSim) Kill(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if sp := p.pods[id]; sp != nil {
		p.end(sp, "external")
	}
}

// ---- the simulated agent -----------------------------------------------------------------------------------------------------------

// Tick lets every live agent act once at the current instant: its watchdog, then one poll when one is due. A held take finishes in the
// tick at or after its end. It returns the first error the simulation could not explain (a malformed answer of the poll handler).
func (p *PodSim) Tick(ctx context.Context) error {
	p.mu.Lock()
	ids := slices.Clone(p.order)
	p.mu.Unlock()
	var errs []error
	for _, id := range ids {
		if err := p.tickPod(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *PodSim) logf(sp *simPod, format string, a ...any) {
	sp.logs = append(sp.logs, p.clk.Now().UTC().Format(time.RFC3339)+" "+fmt.Sprintf(format, a...))
	if len(sp.logs) > podLogLines {
		sp.logs = sp.logs[len(sp.logs)-podLogLines:]
	}
}

func (p *PodSim) tickPod(ctx context.Context, id string) error {
	p.mu.Lock()
	sp := p.pods[id]
	now := p.clk.Now()
	if sp == nil || sp.deleted != nil || now.Before(sp.created.Add(time.Duration(p.o.StartS)*time.Second)) {
		p.mu.Unlock()
		return nil
	}
	if sp.marks == nil { // the container started: read what the batch asks of this pod
		m := PodMarkers{}
		if p.o.StyleOf != nil {
			p.mu.Unlock()
			style := p.o.StyleOf(ctx, sp.name)
			p.mu.Lock()
			m = ParsePodMarkers(style)
		}
		sp.marks = &m
		sp.readyAt = sp.created.Add(time.Duration(p.o.StartS+p.o.BootS) * time.Second).Add(m.BootSlow)
		sp.lastOK = now
		p.logf(sp, "container started")
	}
	// the watchdog is a separate thread of the container: it survives a crashed main loop, not a pod that cannot terminate itself
	if !sp.marks.NoSelfTerminate {
		switch {
		case !sp.deadline.IsZero() && !now.Before(sp.deadline):
			p.logf(sp, "deadline reached: terminating")
			p.end(sp, "deadline")
			p.mu.Unlock()
			return nil
		case sp.deadman > 0 && !now.Before(sp.lastOK.Add(sp.deadman)):
			p.logf(sp, "dead-man: no successful poll: terminating")
			p.end(sp, "deadman")
			p.mu.Unlock()
			return nil
		}
	}
	if sp.agentDead || sp.marks.Silent || p.cut || now.Before(sp.nextPoll) { // a cut agent reaches nothing: no poll, no upload
		p.mu.Unlock()
		return nil
	}
	handler := p.handler
	worker := p.worker
	var req pods.PollRequest
	req.Agent = pods.PollAgent{Version: "0.0.0-sim", Build: workerBuild}
	var finished *simTask
	switch {
	case sp.booting && now.Before(sp.readyAt):
		req.State = "booting"
	case sp.booting: // weights loaded: the boot report goes out with the first idle poll
		sp.booting = false
		req.State = "idle"
		req.Boot = &pods.PollBoot{GPUName: sp.gpu, CUDA: "12.8", WeightsReady: true, LoadS: float64(now.Sub(sp.created) / time.Second)}
	case sp.task != nil && now.Before(sp.task.until):
		req.State = "busy"
		a := sp.task.attemptID
		req.AttemptID = &a
	case sp.task != nil:
		finished = sp.task
		req.State = "idle"
	default:
		req.State = "idle"
	}
	sp.nextPoll = now.Add(podPollEveryS * time.Second)
	p.mu.Unlock()
	if handler == nil {
		return errors.New("fake: PodSim has no poll handler (Attach)")
	}
	if finished != nil {
		if finished.done == nil { // the uploads happen once, even when the report that follows is refused
			finished.done = p.finish(ctx, worker, finished)
		}
		req.Done = finished.done
	}
	resp, ok, err := p.poll(ctx, handler, sp, req)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if sp.deleted != nil {
		return nil
	}
	sp.polls++
	if !ok {
		return nil // refused or failed: the dead-man keeps running
	}
	sp.lastOK = p.clk.Now()
	if finished != nil && sp.task == finished {
		sp.task = nil
	}
	return p.act(sp, resp)
}

// finish runs the handed take at the end of its render: the uploads, then the done report of the next poll.
func (p *PodSim) finish(ctx context.Context, w *SimWorker, t *simTask) *pods.PollDone {
	status, code := "ok", (*string)(nil)
	if w != nil {
		if s, early := w.Begin(ctx, t.raw, t.attemptID, true); early != nil {
			status = "error"
			c := "PIPELINE_ERROR"
			code = &c
		} else if r := s.Finish(ctx); r.Failed {
			status = "error"
			c := "PIPELINE_ERROR"
			code = &c
		}
	}
	return &pods.PollDone{AttemptID: t.attemptID, Status: status, ExecutionMS: t.execMS, ErrorCode: code}
}

// poll posts one request to the poll handler. ok is false when the handler refused it (the link is bad, the body was refused).
func (p *PodSim) poll(ctx context.Context, h http.Handler, sp *simPod, req pods.PollRequest) (pods.PollResponse, bool, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return pods.PollResponse{}, false, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, sp.pollURL, bytes.NewReader(body))
	if err != nil {
		return pods.PollResponse{}, false, nil //nolint:nilerr // an unparseable link: no poll ever succeeds, which is what the pod sees
	}
	r.RequestURI = r.URL.RequestURI()
	r.Header.Set("Content-Type", "application/json")
	rec := &bodyRecorder{statusRecorder: statusRecorder{h: http.Header{}}}
	h.ServeHTTP(rec, r)
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	if rec.code != http.StatusOK {
		return pods.PollResponse{}, false, nil
	}
	var resp pods.PollResponse
	if err := json.Unmarshal(rec.buf.Bytes(), &resp); err != nil {
		return pods.PollResponse{}, false, errors.New("fake: poll answer is not JSON")
	}
	return resp, true, nil
}

// act applies an answer of the poll handler. Callers hold p.mu.
func (p *PodSim) act(sp *simPod, resp pods.PollResponse) error {
	now := p.clk.Now()
	switch resp.Action {
	case pods.ActionStop:
		if sp.marks.NoSelfTerminate {
			p.logf(sp, "stop received; cannot terminate")
			sp.agentDead = true
			return nil
		}
		p.logf(sp, "stop received: terminating")
		p.end(sp, "stop")
	case pods.ActionTake:
		raw := []byte(resp.Take)
		in, err := provider.ParseWorkerInput(raw)
		if err != nil {
			return fmt.Errorf("fake: the poll handler handed a take the worker input parser refuses: %w", err)
		}
		var rf requestFields
		_ = json.Unmarshal(in.Request, &rf)
		tm := ParsePodMarkers(rf.Style)
		mk := parseMarkers(rf.Style)
		sp.took++
		p.logf(sp, "take %d handed", sp.took)
		switch {
		case tm.CrashMidTake || sp.marks.CrashMidTake:
			p.logf(sp, "agent crashed")
			sp.agentDead = true
		case (tm.Restart || sp.marks.Restart) && !sp.restarted:
			p.logf(sp, "container restarted")
			sp.restarted, sp.booting, sp.task = true, true, nil
			sp.readyAt = now.Add(time.Duration(p.o.BootS) * time.Second)
		default:
			d := time.Duration(p.o.RenderS) * time.Second
			if mk.slow > 0 {
				d = mk.slow
			}
			sp.task = &simTask{attemptID: in.AttemptID, raw: raw, until: now.Add(d), execMS: d.Milliseconds()}
		}
	}
	return nil
}
