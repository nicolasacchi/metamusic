package servertest

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// PodsOptions tunes the fake pod backend of a rig (Options.Pods). The zero value is the production shape of C390.2 with the 30-take
// minimum and a simulated worker that renders real FLACs with the test ffmpeg.
type PodsOptions struct {
	// Sim tunes the simulation; Clock (the rig's) and StyleOf (the batch's recipe styles and mutations) are filled in when nil.
	Sim fake.PodSimOptions
	// Worker tunes the simulated worker the agents render with; Uploader (the rig's upload handlers) and Clock are filled in when nil.
	// nil builds a worker over the test ffmpeg with 3 s of audio. HeaderOnly replaces the render with header-valid files (no ffmpeg).
	Worker *fake.SimWorkerOptions
	// Config edits the config after the pod defaults are applied (MinTakes, windows, idle).
	Config func(*config.Config)
}

// configurePods applies the pod defaults to cfg, builds the simulation and hands it to the app as its pod API.
func configurePods(tb testing.TB, o Options, cfg *config.Config, ao *app.Options, st *store.Store, clk *Clock) *fake.PodSim {
	tb.Helper()
	if o.Backend != "fake" {
		tb.Fatal("servertest: Pods needs Backend fake")
	}
	cfg.PodBackend = true
	cfg.PodImage = "ghcr.io/example/mm-worker@sha256:" + strings.Repeat("a", 64)
	cfg.PodGPUTypes = []string{"NVIDIA GeForce RTX 4090"}
	cfg.PodMinTakes, cfg.PodMaxTakes, cfg.PodBootS, cfg.PodIdleS, cfg.PodSlackS = 30, 100, 1200, 120, 300
	cfg.PodMaxRuntime, cfg.PodDeadman, cfg.PodOrphanAfter, cfg.PodWindowMax, cfg.PodDiskGB = 6*time.Hour, 10*time.Minute, 180*time.Second, 8*time.Hour, 40
	if f := o.Pods.Config; f != nil {
		f(cfg)
	}
	so := o.Pods.Sim
	if so.Clock == nil {
		so.Clock = clk
	}
	if so.StyleOf == nil {
		so.StyleOf = func(ctx context.Context, name string) string { return batchStyles(ctx, st.R, name) }
	}
	sim := fake.NewPodSim(so)
	ao.M3.PodAPI, ao.M3.PodHold = sim, -1 // the agents poll synchronously from PodSim.Tick
	return sim
}

// batchStyles is every recipe style and mutation of the batch the named pod serves: where the pod markers of a timeline live.
func batchStyles(ctx context.Context, db *sql.DB, podName string) string {
	rows, err := db.QueryContext(ctx, `SELECT COALESCE(rc.style,'') || ' ' || COALESCE(j.mutation_json,'') FROM pods p JOIN jobs j ON j.batch_id = p.batch_id
		JOIN recipes rc ON rc.id = j.recipe_id WHERE p.name = ? ORDER BY j.take_index`, podName)
	if err != nil {
		return ""
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			b.WriteString(s)
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// attachPods connects the simulation to the built app: the agents poll the real poll handler and upload through the real upload handlers.
func attachPods(tb testing.TB, po *PodsOptions, sim *fake.PodSim, svc *app.Services, clk *Clock) {
	tb.Helper()
	if svc.Server.Pods == nil {
		tb.Fatal("servertest: the pod backend built no poll route")
	}
	sim.Attach(svc.Server.Pods)
	wo := fake.SimWorkerOptions{AudioSeconds: 3, DistinctAudio: true}
	if po.Worker != nil {
		wo = *po.Worker
	} else {
		testtools.Require(tb, "ffmpeg")
		wo.Tools = testtools.Tools()
	}
	if wo.Uploader == nil {
		wo.Uploader = fake.HandlerUploader{UploadHandler: svc.Uploads.Handler(), StartHandler: svc.Uploads.StartHandler()}
	}
	if wo.Clock == nil {
		wo.Clock = clk
	}
	sim.SetWorker(fake.NewSimWorker(wo))
}

// PodRound is one deterministic round of a pod timeline at the rig's current instant: the runner claims and submits, the lifecycle
// creates and watches pods, every simulated agent polls once, and the lifecycle and the runner see what they reported. Tests advance the
// clock between rounds. It fails the test on any error.
func (r *Rig) PodRound(tb testing.TB) {
	tb.Helper()
	if err := r.PodRoundErr(context.Background()); err != nil {
		tb.Fatalf("pod round: %v", err)
	}
}

// PodRoundErr is PodRound returning the errors of its steps joined. Every step runs even when an earlier one failed: a chaos test that cuts
// the network expects errors and still needs the agents' watchdogs and the lifecycle's clocks to tick.
func (r *Rig) PodRoundErr(ctx context.Context) error {
	lc := r.Services.PodLifecycle
	if r.Pods == nil || lc == nil {
		return errors.New("servertest: the rig has no pod backend (Options.Pods)")
	}
	var errs []error
	step := func(err error) { errs = append(errs, err) }
	runner := func() {
		_, err := r.Runner.Step(ctx)
		step(err)
		step(r.Runner.Drain(ctx))
	}
	runner()
	step(r.Reconciler.Pass(ctx))
	step(lc.Tick(ctx))
	step(r.Pods.Tick(ctx))
	step(lc.Tick(ctx))
	runner()
	return errors.Join(errs...)
}
