package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

// The pod bulk backend wires itself here (C396.7, C391.3: the app wires everything that crosses the package lines of internal/provider/runpod/pods):
// the backend as a constructor of the extra-backend list, the meter, the trips, the fence, the system disarm and the lifecycle loop. With
// MM_POD_BACKEND=0 (production) none of it is built: no backend, no client, no loop, no poll route, no provider_state row.
func init() {
	e2ExtraBackends = append(e2ExtraBackends, podBackends)
	RegisterBuildHook(wirePods)
	audit.AddSystemOps(jobs.OpBulkDisarm, pods.OpPodCreate, pods.OpPodSettle, pods.OpPodCanary)
}

// podEstimatePriced says whether the estimate prices a pod batch with QuotePod (BULK-P6, C398). It does since BULK-P6 (generation.UsePods is
// called below), so the real backend's gate no longer holds it closed; the operator's own steps still do: a pod backend is disarmed until the
// operator arms it, which needs a probe row, the pod key file and a verified canary before an ordinary batch. With MM_POD_BACKEND=0 nothing
// of it is built.
const podEstimatePriced = true

// podWiring is what the pod constructor leaves for the build hook.
type podWiring struct {
	name       string
	fake       bool
	backend    *pods.Backend
	api        pods.API
	keyPresent func() bool
}

// podMeter adapts pods.Meter to the runner's meter hook (the packages share no type, only a method set).
type podMeter struct{ m pods.Meter }

func (a podMeter) OverheadSeconds(ctx context.Context, x jobs.Querier, batchID string) (int64, bool, error) {
	return a.m.OverheadSeconds(ctx, x, batchID)
}

// podBackends builds the pod backend of the set: `fake-pod` under MM_BACKEND=fake, `runpod-pod` under runpod, only with MM_POD_BACKEND=1.
func podBackends(in e2Inputs) ([]e2Extra, error) {
	cfg, m := in.cfg, in.m3
	if !cfg.PodBackend {
		return nil, nil
	}
	w := &podWiring{name: provider.BackendRunPodPod}
	switch cfg.Backend {
	case config.BackendFake:
		w.name, w.fake = provider.BackendFakePod, true
		w.keyPresent = func() bool { return true }
	case config.BackendRunPod:
		dir := cfg.SecretsDir
		w.keyPresent = func() bool {
			if dir == "" { // an empty directory would make the loader look in the working directory
				return false
			}
			_, ok, err := config.LoadPodKey(dir)
			return ok && err == nil
		}
	default:
		return nil, nil
	}
	switch {
	case m.opts.PodAPI != nil:
		w.api = m.opts.PodAPI
	case !w.fake:
		w.api = pods.NewClient(pods.Options{KeyDir: cfg.SecretsDir, UserAgent: pods.UserAgentFor("dev"), Clock: m.clk})
	}
	bo := pods.BackendOptions{Name: w.name, Store: in.st, Queue: pods.NewQueue(), Clock: m.clk, Image: cfg.PodImage, GPUTypes: cfg.PodGPUTypes,
		MaxRuntime: cfg.JobMaxRuntime, KillSlackS: cfg.KillSlackS, KeyPresent: w.keyPresent}
	if w.api != nil {
		bo.API = w.api
	}
	w.backend = pods.NewBackend(bo)
	m.pods = w
	if m.Generation == nil {
		return nil, errors.New("app: the pod backend needs the generation service built first")
	}
	m.Generation.UsePods(generation.PodConfig{MinTakes: cfg.PodMinTakes, MaxTakes: cfg.PodMaxTakes, BootS: cfg.PodBootS, IdleS: cfg.PodIdleS, SlackS: cfg.PodSlackS,
		MaxRuntimeS: int(cfg.PodMaxRuntime / time.Second), MaxRecreate: 1, GPUTypes: cfg.PodGPUTypes, RequireCanary: !w.fake})
	return []e2Extra{{Backend: w.backend, KeysLoaded: w.fake || podEstimatePriced, Expect: provider.WorkerExpect{Weights: yue2.Weights},
		GateColdStartS: cfg.PodBootS, GateMaxRuntime: cfg.PodMaxRuntime, Meter: podMeter{}}}, nil
}

// wirePods mounts the poll route and starts the lifecycle loop (a Loop of serve) when the pod backend was built.
func wirePods(env *BuildEnv) error {
	s := env.Services
	w := s.pods
	if w == nil {
		return nil
	}
	gate, ok := s.Gates.For(w.name)
	rec := s.Reconciler.For(w.name)
	if !ok || s.Provider == nil {
		return nil
	}
	clk := env.Options.M3.Clock
	if clk == nil {
		clk = clockFromNow{env.Now}
	}
	log := env.Options.M3.Log
	if log == nil {
		log = slog.Default()
	}
	s.Provider.UseSystemAudit(systemAudit(s.Audit, env.Now))
	info := &api.PodsInfo{ImageDigest: pods.ImageDigest(env.Cfg.PodImage), KeyPresent: w.keyPresent}
	if !w.fake {
		info.WindowMax = env.Cfg.PodWindowMax
	}
	s.Deps.Pods = info
	signer := pods.NewSigner(env.Sec.PodPollKey())
	var lc *pods.Lifecycle
	if s.Runner != nil && rec != nil {
		cfg := env.Cfg
		lc = pods.NewLifecycle(pods.LifecycleOptions{
			Store: env.Store, API: w.api, Queue: w.backend.Queue(), Signer: signer, PublicURL: cfg.PublicURL, Spend: s.Spend, Clock: clk, Log: log, NewID: env.NewID,
			Backend: w.name, Image: cfg.PodImage, GPUTypes: cfg.PodGPUTypes, DiskGB: cfg.PodDiskGB, Weights3B: yue2.Weights[yue2.WeightsRepo3B],
			BootS: cfg.PodBootS, IdleS: cfg.PodIdleS, SlackS: cfg.PodSlackS, KillSlackS: cfg.KillSlackS, MaxRecreate: 1,
			MaxRuntime: cfg.PodMaxRuntime, Deadman: cfg.PodDeadman, OrphanAfter: cfg.PodOrphanAfter, WindowMax: cfg.PodWindowMax,
			RequireCanary: !w.fake, NoWindows: w.fake,
			KeyPresent: w.keyPresent, Holding: s.Runner.Holding, Fence: s.Runner.Fence, State: gate.State, Trip: rec.Trip,
			Disarm: func(ctx context.Context, reason string) error {
				_, err := s.Provider.SystemDisarm(ctx, w.name, reason)
				return err
			},
			Audit: pods.AuditFunc(systemAudit(s.Audit, env.Now)),
		})
		env.AddLoop(Loop{Name: "pod-lifecycle", Run: func(ctx context.Context) { _ = lc.Run(ctx) }})
	}
	po := pods.PollOptions{Store: env.Store, Signer: signer, Queue: w.backend.Queue(), Clock: clk, Log: log, KillSlackS: env.Cfg.KillSlackS}
	if lc != nil {
		po.Nudge = lc.Nudge
	}
	s.podHandler = pods.NewPoller(po).Handler()
	return nil
}

// systemAudit records a system audit row after its effect, retrying inside the audit budget (the effect already happened).
func systemAudit(st *audit.Store, now func() time.Time) jobs.SystemAuditFunc {
	return func(ctx context.Context, op string, details map[string]any, cost money.MicroUSD, failed bool) {
		if st == nil {
			return
		}
		e, err := audit.SystemEntry(op, now(), details, 0, cost, failed)
		if err != nil {
			return
		}
		audit.InsertRetrying(ctx, st, e, 2*time.Second, 10*time.Millisecond)
	}
}
