package fake_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

// The simulated pod API and agent (BULK-S1) against a scripted poll handler: what the agent says on each poll, how it ends itself, and the
// markers that break it. The real handler, the lifecycle and the ledger are exercised by the property, chaos and acceptance tiers.

const (
	podGPU   = "NVIDIA GeForce RTX 4090"
	podImage = "ghcr.io/example/mm-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

var podT0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// scripted is a poll handler that records every request and answers from a function.
type scripted struct {
	mu   sync.Mutex
	reqs []pods.PollRequest
	next func(n int, r pods.PollRequest) (pods.PollResponse, int)
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req pods.PollRequest
	if err := json.Unmarshal(raw, &req); err != nil || r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/worker/v1/pods/") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	n := len(s.reqs)
	s.mu.Unlock()
	resp, code := pods.PollResponse{Action: pods.ActionWait, WaitS: 20, Cancel: []string{}}, http.StatusOK
	if s.next != nil {
		resp, code = s.next(n, req)
	}
	if code != http.StatusOK {
		w.WriteHeader(code)
		return
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *scripted) states() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.reqs {
		out = append(out, r.State)
	}
	return out
}

func podBody(name string, deadline time.Time, deadman int) pods.CreateBody {
	return pods.CreateBody{Name: name, Image: podImage, GPUTypeID: podGPU, DiskGB: 40, Env: map[string]string{
		"MODE": "agent", "MM_POLL_URL": "https://mm.example.com/worker/v1/pods/0190aaaa-0000-7000-8000-000000000001/poll?exp=1&sig=x",
		"MM_DEADLINE_UNIX": strconv.FormatInt(deadline.Unix(), 10), "MM_DEADMAN_S": strconv.Itoa(deadman)}}
}

func takeDoc(t *testing.T, attempt, style string) json.RawMessage {
	t.Helper()
	req, _ := json.Marshal(map[string]any{"id": "syn-a", "seed": 1, "style": style, "lyrics": "la", "cot": "full"})
	ups := map[string]provider.UploadTarget{}
	list, _ := provider.ArtifactsFor(provider.EngineYuE2)
	for i, a := range list {
		ups[a.Name] = provider.UploadTarget{URL: "https://mm.example.com/u" + strconv.Itoa(i) + "?exp=1&sig=s", MaxBytes: a.MaxBytes}
	}
	raw, err := provider.BuildWorkerInput(provider.TakeSpec{JobID: "01900000-0000-7000-8000-0000000a0001", AttemptID: attempt, Engine: "yue2", Request: req, TimeoutS: 120,
		TTL: time.Hour, UploadBudgetS: 600, StartURL: "https://mm.example.com/worker/v1/attempts/x/start?exp=1&sig=s", Uploads: ups, Deadline: podT0.Add(time.Hour),
		Expect: provider.WorkerExpect{Weights: map[string]string{"m-a-p/YuE2-3B": "8f24312f187763b9854adeec87e437e2b03bbff1"}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type podRig struct {
	clk *clocktest.Manual
	sim *fake.PodSim
	h   *scripted
}

func newPodRig(t *testing.T, style string, mod func(*fake.PodSimOptions)) *podRig {
	t.Helper()
	r := &podRig{clk: clocktest.NewManual(podT0), h: &scripted{}}
	o := fake.PodSimOptions{Clock: r.clk, StyleOf: func(context.Context, string) string { return style }}
	if mod != nil {
		mod(&o)
	}
	r.sim = fake.NewPodSim(o)
	r.sim.Attach(r.h)
	return r
}

// tick advances the clock and lets the agents act.
func (r *podRig) tick(t *testing.T, d time.Duration) {
	t.Helper()
	r.clk.Advance(d)
	if err := r.sim.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (r *podRig) create(t *testing.T, deadline time.Time, deadman int) pods.Pod {
	t.Helper()
	p, err := r.sim.CreatePod(t.Context(), podBody("mm-000000000001", deadline, deadman))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPodSim_API(t *testing.T) {
	t.Parallel()
	r := newPodRig(t, "", func(o *fake.PodSimOptions) { o.Prices = map[string]money.MicroUSD{podGPU: 400_000} })
	ctx := t.Context()
	if _, err := r.sim.CreatePod(ctx, pods.CreateBody{Name: "bad"}); !errors.Is(err, pods.ErrInvalid) {
		t.Errorf("an invalid body = %v, want ErrInvalid", err)
	}
	p := r.create(t, podT0.Add(time.Hour), 600)
	if p.Status != pods.StatusProvisioning || !p.Priced || p.CostPPH != 400_000 || p.ID == "" {
		t.Errorf("created pod %+v", p)
	}
	r.clk.Advance(30 * time.Second)
	if g, err := r.sim.GetPod(ctx, p.ID); err != nil || g.Status != pods.StatusRunning || g.StartedAt == nil {
		t.Errorf("get after the start: %+v, %v", g, err)
	}
	if l, err := r.sim.ListPods(ctx); err != nil || len(l) != 1 {
		t.Errorf("list = %v, %v", l, err)
	}
	prices, err := r.sim.CatalogSecurePrices(ctx, []string{podGPU, "other"})
	if err != nil || prices[podGPU] != 400_000 || prices["other"] != 340_000 {
		t.Errorf("catalog = %v, %v", prices, err)
	}
	if err := r.sim.CancelQueued(ctx, "q"); err != nil {
		t.Error(err)
	}
	r.clk.Advance(30 * time.Minute)
	if err := r.sim.DeletePod(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.sim.DeletePod(ctx, p.ID); err != nil || r.sim.DeletePod(ctx, "unknown") != nil {
		t.Error("a delete of a gone pod must succeed")
	}
	if _, err := r.sim.GetPod(ctx, p.ID); !errors.Is(err, pods.ErrPodGone) {
		t.Errorf("get after delete = %v, want ErrPodGone", err)
	}
	if l, _ := r.sim.ListPods(ctx); len(l) != 0 {
		t.Errorf("list after delete = %v", l)
	}
	// the bill is the exact lifetime at the pod's price: 30.5 minutes of $0.40
	if want := money.ForMillis(int64(30*time.Minute+30*time.Second)/int64(time.Millisecond), 400_000); r.sim.Billed() != want {
		t.Errorf("billed %d, want %d", r.sim.Billed(), want)
	}
	bs, err := r.sim.PodBilling(ctx, podT0, podT0.Add(2*time.Hour), []string{p.ID})
	if err != nil || len(bs) != 1 || bs[0].Total != r.sim.Billed() {
		t.Errorf("billing = %v, %v", bs, err)
	}
	if bs, _ := r.sim.PodBilling(ctx, podT0, podT0.Add(2*time.Hour), []string{"other"}); len(bs) != 0 {
		t.Errorf("billing of a foreign pod = %v", bs)
	}
	if _, err := r.sim.PodLogsTail(ctx, "unknown", 5); !errors.Is(err, pods.ErrPodGone) {
		t.Errorf("logs of an unknown pod = %v", err)
	}
	if got := r.sim.Calls("create"); got != 2 {
		t.Errorf("%d creates", got)
	}
}

func TestPodSim_Faults(t *testing.T) {
	t.Parallel()
	r := newPodRig(t, "", nil)
	ctx := t.Context()
	r.sim.Refuse(podGPU, true)
	var rf *pods.ErrRefused
	if _, err := r.sim.CreatePod(ctx, podBody("mm-000000000001", podT0.Add(time.Hour), 600)); !errors.As(err, &rf) || !rf.Capacity {
		t.Errorf("a refused type = %v, want a capacity refusal", err)
	}
	r.sim.Refuse(podGPU, false)
	r.sim.FailNextCreate(&pods.ErrAmbiguous{Op: "create"}, true)
	var amb *pods.ErrAmbiguous
	if _, err := r.sim.CreatePod(ctx, podBody("mm-000000000002", podT0.Add(time.Hour), 600)); !errors.As(err, &amb) {
		t.Errorf("an ambiguous create = %v", err)
	}
	if l, _ := r.sim.ListPods(ctx); len(l) != 1 {
		t.Errorf("the ambiguous create made %d pods, want the one that exists", len(l))
	}
	r.sim.FailNextCreate(pods.ErrBalance, false)
	if _, err := r.sim.CreatePod(ctx, podBody("mm-000000000003", podT0.Add(time.Hour), 600)); !errors.Is(err, pods.ErrBalance) {
		t.Errorf("a failed create = %v", err)
	}
	if got := len(r.sim.Pods()); got != 1 {
		t.Errorf("%d pods after a create that failed without effect", got)
	}
	r.sim.FailAPI(provider.ErrAccess)
	if _, err := r.sim.GetPod(ctx, "x"); !errors.Is(err, provider.ErrAccess) {
		t.Errorf("get with the API failing = %v", err)
	}
	r.sim.FailAPI(nil)
	r.sim.Partition(true)
	if err := r.sim.DeletePod(ctx, "x"); err == nil {
		t.Error("a delete across a partition succeeded")
	}
	if _, err := r.sim.PodBilling(ctx, podT0, podT0.Add(time.Hour), nil); err == nil {
		t.Error("billing across a partition succeeded")
	}
	r.sim.Partition(false)
	if r.sim.Live() != 1 {
		t.Errorf("live = %d", r.sim.Live())
	}
}

// TestPodSim_AgentLifecycle: booting polls until the weights are loaded, then idle with the boot report once, a take makes the agent busy
// for its render and the poll after it carries done, and a stop answer ends the pod.
func TestPodSim_AgentLifecycle(t *testing.T) {
	t.Parallel()
	r := newPodRig(t, "", nil)
	attempt := "01900000-0000-7000-8000-0000000b0001"
	handed := false
	r.h.next = func(_ int, req pods.PollRequest) (pods.PollResponse, int) {
		resp := pods.PollResponse{Action: pods.ActionWait, WaitS: 20, Cancel: []string{}}
		switch {
		case req.State == "idle" && !handed:
			handed = true
			resp.Action, resp.Take = pods.ActionTake, takeDoc(t, attempt, "pop fake:slow=20")
		case req.Done != nil:
			resp.Action = pods.ActionStop
		}
		return resp, http.StatusOK
	}
	p := r.create(t, podT0.Add(3*time.Hour), 600)
	for range 30 {
		r.tick(t, 5*time.Second)
	}
	if got := r.h.states(); len(got) == 0 || got[0] != "booting" {
		t.Fatalf("the first polls = %v, want booting first", got)
	}
	for range 40 {
		r.tick(t, 5*time.Second)
	}
	var busy, boots int
	var done *pods.PollDone
	for _, q := range r.h.reqs {
		if q.State == "busy" {
			busy++
			if q.AttemptID == nil || *q.AttemptID != attempt {
				t.Errorf("a busy poll without the attempt: %+v", q)
			}
		}
		if q.Boot != nil {
			boots++
		}
		if q.Done != nil {
			done = q.Done
		}
	}
	if boots != 1 || busy == 0 || done == nil || done.AttemptID != attempt || done.Status != "ok" || done.ExecutionMS != 20_000 {
		t.Errorf("boots %d, busy polls %d, done %+v", boots, busy, done)
	}
	rec := r.sim.Pods()[0]
	if rec.ID != p.ID || rec.Deleted == nil || rec.DeletedBy != "stop" || rec.Takes != 1 || rec.Polls == 0 {
		t.Errorf("pod record %+v", rec)
	}
	if logs, _ := r.sim.PodLogsTail(t.Context(), p.ID, 2); len(logs) != 2 {
		t.Errorf("log tail = %v", logs)
	}
}

func TestPodSim_Watchdog(t *testing.T) {
	t.Parallel()
	t.Run("the dead-man ends a pod whose polls fail", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t, "", nil)
		r.h.next = func(int, pods.PollRequest) (pods.PollResponse, int) { return pods.PollResponse{}, http.StatusForbidden }
		r.create(t, podT0.Add(5*time.Hour), 600)
		for range 200 {
			r.tick(t, 10*time.Second)
		}
		if rec := r.sim.Pods()[0]; rec.DeletedBy != "deadman" || rec.Deleted.Sub(rec.Created) > 11*time.Minute {
			t.Errorf("pod %+v: want ended by the dead-man after about 10 minutes", rec)
		}
	})
	t.Run("the deadline ends a pod that polls fine", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t, "", nil)
		r.create(t, podT0.Add(20*time.Minute), 600)
		for range 150 {
			r.tick(t, 10*time.Second)
		}
		if rec := r.sim.Pods()[0]; rec.DeletedBy != "deadline" {
			t.Errorf("pod %+v: want ended at its deadline", rec)
		}
	})
	t.Run("a partition silences the polls and the dead-man counts on", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t, "", nil)
		r.create(t, podT0.Add(5*time.Hour), 300)
		for range 20 {
			r.tick(t, 10*time.Second)
		}
		r.sim.Partition(true)
		polls := r.sim.Pods()[0].Polls
		for range 60 {
			r.tick(t, 10*time.Second)
		}
		rec := r.sim.Pods()[0]
		if rec.Polls != polls || rec.DeletedBy != "deadman" {
			t.Errorf("pod %+v after a partition (polls were %d)", rec, polls)
		}
	})
}

func TestPodSim_Markers(t *testing.T) {
	t.Parallel()
	take := func(t *testing.T, r *podRig, style string) {
		t.Helper()
		handed := false
		r.h.next = func(_ int, req pods.PollRequest) (pods.PollResponse, int) {
			resp := pods.PollResponse{Action: pods.ActionWait, WaitS: 20, Cancel: []string{}}
			if req.State == "idle" && !handed {
				handed = true
				resp.Action, resp.Take = pods.ActionTake, takeDoc(t, "01900000-0000-7000-8000-0000000b0002", style)
			}
			if req.State == "idle" && handed && req.AttemptID == nil && req.Done != nil {
				resp.Action = pods.ActionStop
			}
			return resp, http.StatusOK
		}
	}
	t.Run("silent never polls", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t, fake.MarkPodSilent, nil)
		r.create(t, podT0.Add(time.Hour), 600)
		for range 100 {
			r.tick(t, 10*time.Second)
		}
		if len(r.h.states()) != 0 {
			t.Errorf("a silent agent polled: %v", r.h.states())
		}
	})
	t.Run("boot_slow delays the weights", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t, fake.MarkPodBootSlow+"=300", nil)
		r.create(t, podT0.Add(time.Hour), 600)
		for range 30 { // 150 s: still loading
			r.tick(t, 5*time.Second)
		}
		for _, s := range r.h.states() {
			if s != "booting" {
				t.Fatalf("an agent that is still loading reported %q", s)
			}
		}
		for range 60 {
			r.tick(t, 5*time.Second)
		}
		if s := r.h.states(); s[len(s)-1] != "idle" {
			t.Errorf("polls end %q, want idle once loaded", s[len(s)-1])
		}
	})
	t.Run("crash_mid_take kills the agent but not the watchdog", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t, fake.MarkPodCrashMidTake, nil)
		take(t, r, "pop")
		r.create(t, podT0.Add(5*time.Hour), 300)
		for range 150 {
			r.tick(t, 10*time.Second)
		}
		rec := r.sim.Pods()[0]
		if !rec.AgentDead || rec.DeletedBy != "deadman" {
			t.Errorf("pod %+v: want a dead agent whose dead-man ended the pod", rec)
		}
	})
	t.Run("no_self_terminate ignores stop, deadline and dead-man", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t, fake.MarkPodNoSelfTerminate, nil)
		r.h.next = func(int, pods.PollRequest) (pods.PollResponse, int) {
			return pods.PollResponse{Action: pods.ActionStop, Cancel: []string{}}, http.StatusOK
		}
		r.create(t, podT0.Add(30*time.Minute), 300)
		for range 400 {
			r.tick(t, 10*time.Second)
		}
		rec := r.sim.Pods()[0]
		if rec.Deleted != nil || !rec.AgentDead || r.sim.Live() != 1 {
			t.Errorf("pod %+v: it cannot end itself", rec)
		}
		r.sim.Kill(rec.ID)
		if rec := r.sim.Pods()[0]; rec.DeletedBy != "external" {
			t.Errorf("pod %+v after the operator removed it", rec)
		}
	})
	t.Run("restart reports booting again once and loses the take", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t, fake.MarkPodRestart, nil)
		take(t, r, "pop")
		r.create(t, podT0.Add(5*time.Hour), 600)
		for range 150 {
			r.tick(t, 10*time.Second)
		}
		states := strings.Join(r.h.states(), ",")
		if !strings.Contains(states, "idle,booting") {
			t.Errorf("polls %s: want idle then a second booting", states)
		}
		for _, q := range r.h.reqs {
			if q.Done != nil {
				t.Errorf("a take handed to a restarting container was reported done: %+v", q.Done)
			}
		}
	})
	t.Run("a take the worker input parser refuses is an error", func(t *testing.T) {
		t.Parallel()
		r := newPodRig(t, "", nil)
		r.h.next = func(_ int, req pods.PollRequest) (pods.PollResponse, int) {
			resp := pods.PollResponse{Action: pods.ActionWait, Cancel: []string{}}
			if req.State == "idle" {
				resp.Action, resp.Take = pods.ActionTake, json.RawMessage(`{"nonsense":1}`)
			}
			return resp, http.StatusOK
		}
		r.create(t, podT0.Add(time.Hour), 600)
		var err error
		for range 40 {
			r.clk.Advance(5 * time.Second)
			if e := r.sim.Tick(t.Context()); e != nil {
				err = e
			}
		}
		if err == nil {
			t.Error("a malformed take went unnoticed")
		}
	})
}

func TestPodSim_NoHandler(t *testing.T) {
	t.Parallel()
	clk := clocktest.NewManual(podT0)
	sim := fake.NewPodSim(fake.PodSimOptions{Clock: clk})
	if _, err := sim.CreatePod(t.Context(), podBody("mm-000000000001", podT0.Add(time.Hour), 600)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	if err := sim.Tick(t.Context()); err == nil {
		t.Error("a tick with no poll handler attached must say so")
	}
}

func TestParsePodMarkers(t *testing.T) {
	t.Parallel()
	m := fake.ParsePodMarkers("pop, fake:pod_boot_slow=90 fake:pod_silent fake:pod_no_self_terminate\nfake:pod_crash_mid_take fake:pod_restart fake:other=1 fake:pod_boot_slow=-3")
	if m.BootSlow != 90*time.Second || !m.Silent || !m.NoSelfTerminate || !m.CrashMidTake || !m.Restart {
		t.Errorf("markers %+v", m)
	}
	if z := fake.ParsePodMarkers("pop fake:slow=3 fake:pod_boot_slow=x"); z != (fake.PodMarkers{}) {
		t.Errorf("unrelated tokens set %+v", z)
	}
}

// podUploader answers every start and put with success and records the uploaded artifact names' order of arrival.
type podUploader struct {
	mu     sync.Mutex
	puts   int
	starts int
	code   int
}

func (u *podUploader) Start(context.Context, string, []byte) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.starts++
	return http.StatusOK, nil
}

func (u *podUploader) Put(context.Context, string, []byte, string) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.puts++
	if u.code != 0 {
		return u.code, nil
	}
	return http.StatusCreated, nil
}

// TestPodSim_RendersTheTake: with a worker the agent uploads the take's artifacts (once, whatever the fate of the report that follows) and
// reports it ok; a worker that cannot upload makes the report an error with a fixed code.
func TestPodSim_RendersTheTake(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		code   int
		status string
	}{{"ok", 0, "ok"}, {"refused uploads", http.StatusForbidden, "error"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := &podUploader{code: tc.code}
			w := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: func(id uint64, secs float64) []byte { return []byte("fLaC" + strconv.FormatUint(id, 10)) }, TempDir: t.TempDir()})
			r := newPodRig(t, "", func(o *fake.PodSimOptions) { o.Worker = w })
			handed, refusedReports := false, 0
			r.h.next = func(_ int, req pods.PollRequest) (pods.PollResponse, int) {
				resp := pods.PollResponse{Action: pods.ActionWait, WaitS: 20, Cancel: []string{}}
				switch {
				case req.Done != nil && refusedReports == 0:
					refusedReports++
					return resp, http.StatusServiceUnavailable // the first report is lost: the uploads must not repeat
				case req.State == "idle" && !handed:
					handed = true
					resp.Action, resp.Take = pods.ActionTake, takeDoc(t, "01900000-0000-7000-8000-0000000b0003", "pop fake:slow=10")
				case req.Done != nil:
					resp.Action = pods.ActionStop
				}
				return resp, http.StatusOK
			}
			r.sim.SetWorker(w)
			r.create(t, podT0.Add(3*time.Hour), 600)
			for range 60 {
				r.tick(t, 5*time.Second)
			}
			var done *pods.PollDone
			for _, q := range r.h.reqs {
				if q.Done != nil {
					done = q.Done
				}
			}
			if done == nil || done.Status != tc.status || (tc.status == "error" && (done.ErrorCode == nil || *done.ErrorCode != "PIPELINE_ERROR")) {
				t.Fatalf("done = %+v, want %s", done, tc.status)
			}
			if tc.status == "ok" && (up.starts != 1 || up.puts == 0) {
				t.Errorf("%d starts, %d puts", up.starts, up.puts)
			}
			first := up.puts
			for range 10 {
				r.tick(t, 5*time.Second)
			}
			if up.puts != first {
				t.Errorf("the uploads repeated after the pod ended: %d then %d puts", first, up.puts)
			}
		})
	}
}
