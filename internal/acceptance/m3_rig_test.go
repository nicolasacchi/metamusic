package acceptance_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// lateUploader lets the simulated worker be built before the rig's upload service exists.
type lateUploader struct {
	mu sync.Mutex
	u  fake.Uploader
}

func (l *lateUploader) set(u fake.Uploader) { l.mu.Lock(); l.u = u; l.mu.Unlock() }

func (l *lateUploader) get() fake.Uploader { l.mu.Lock(); defer l.mu.Unlock(); return l.u }

func (l *lateUploader) Start(ctx context.Context, url string, body []byte) (int, error) {
	return l.get().Start(ctx, url, body)
}

func (l *lateUploader) Put(ctx context.Context, url string, body []byte, digest string) (int, error) {
	return l.get().Put(ctx, url, body, digest)
}

// genRig is a rig over an imported archive with the fake backend, a simulated worker that uploads through the
// rig's own upload handlers and the production ingest bridge (real ffmpeg). The runner is idle: tests drive it
// with pump, so no wall-clock time passes.
func genRig(t *testing.T, tpl archivetest.Template, o servertest.Options) *servertest.Rig {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	testtools.Require(t, "ffmpeg")
	late := &lateUploader{}
	o.Archive = &tpl
	o.Backend = "fake"
	o.M2b.Media = true
	if o.Fake == nil {
		o.Fake = &fake.Options{}
	}
	fo := *o.Fake
	fo.Worker = fake.NewSimWorker(fake.SimWorkerOptions{Uploader: late, Tools: testtools.Tools(), AudioSeconds: 3})
	o.Fake = &fo
	r := servertest.New(t, o)
	late.set(fake.HandlerUploader{UploadHandler: r.Services.Uploads.Handler(), StartHandler: r.Services.Uploads.StartHandler()})
	return r
}

// pump advances the rig's clock and steps the runner (and the reconciler) until done reports true.
func pump(t *testing.T, r *servertest.Rig, done func() bool) {
	t.Helper()
	pumpWith(t, r.Clock, r.Runner, r.Reconciler, done)
}

func pumpWith(t *testing.T, clk *servertest.Clock, run *jobs.Runner, rec *jobs.Reconciler, done func() bool) {
	t.Helper()
	ctx := context.Background()
	for range 600 {
		if done() {
			return
		}
		clk.Advance(3 * time.Second)
		if _, err := run.Step(ctx); err != nil {
			t.Fatalf("runner step: %v", err)
		}
		if err := run.Drain(ctx); err != nil {
			t.Fatalf("runner drain: %v", err)
		}
		if err := rec.Pass(ctx); err != nil {
			t.Fatalf("reconciler pass: %v", err)
		}
	}
	if !done() {
		t.Fatal("pump: the condition never held")
	}
}

// jobState reads a job's state and error code straight from the database.
func jobState(t *testing.T, r *servertest.Rig, id string) (state string, code string) {
	t.Helper()
	var c *string
	if err := r.Store.R.QueryRow(`SELECT state, error_code FROM jobs WHERE id = ?`, id).Scan(&state, &c); err != nil {
		t.Fatal(err)
	}
	if c != nil {
		code = *c
	}
	return state, code
}

func terminalJob(state string) bool {
	switch state {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// provRig is a generation rig over one of the two backends, with the knobs the interrupt tests need.
type provRig struct {
	*servertest.Rig
	kind string // "fake" | "runpod"
	rp   *runpodtest.Server
	keys runpodtest.KeySet
}

// newProvRig builds a rig over the fake backend or over the serverless backend against runpodtest, both with the
// simulated worker uploading through the rig's own handlers and the production ingest bridge.
func newProvRig(t *testing.T, kind string, tpl archivetest.Template, o servertest.Options) *provRig {
	t.Helper()
	if kind == "fake" {
		return &provRig{Rig: genRig(t, tpl, o), kind: kind}
	}
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	testtools.Require(t, "ffmpeg")
	late := &lateUploader{}
	clk := servertest.NewClock(librarytest.FixedNow)
	keys := runpodtest.KeySet{Jobs: "rp-jobs-" + strings.Repeat("a", 8), Read: "rp-read-" + strings.Repeat("b", 8), Write: "rp-write-" + strings.Repeat("c", 8)}
	sim := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: late, Tools: testtools.Tools(), AudioSeconds: 3})
	rp := runpodtest.New(t, runpodtest.Options{Keys: keys, Runner: runpodtest.FakeWorker(sim), Clock: clk, PollsToRun: 2})
	o.Archive, o.Clock, o.M2b.Media = &tpl, clk, true
	o.Backend, o.RunPod = "runpod", &servertest.RunPodOptions{Server: rp, Keys: keys}
	r := servertest.New(t, o)
	late.set(fake.HandlerUploader{UploadHandler: r.Services.Uploads.Handler(), StartHandler: r.Services.Uploads.StartHandler()})
	// the first reconciler pass reads the quote and the endpoint; generation is refused while the quote is stale
	// (the reconciler works only while the runner holds its lease, so the runner steps first)
	if _, err := r.Runner.Step(context.Background()); err != nil {
		t.Fatalf("runner step: %v", err)
	}
	if err := r.Reconciler.Pass(context.Background()); err != nil {
		t.Fatalf("reconciler pass: %v", err)
	}
	return &provRig{Rig: r, kind: kind, rp: rp, keys: keys}
}

// submits is how many times the provider was asked to run a job.
func (p *provRig) submits() int64 {
	if p.kind == "fake" {
		return p.Fake.Counters().Submit
	}
	return int64(p.rp.Calls(runpodtest.OpRun))
}

// workers is how many workers the provider reports running right now.
func (p *provRig) workers(t *testing.T) int {
	t.Helper()
	rep, err := p.Backend.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	return len(rep.Workers)
}

// motherStyle is the style of a track's recipe.
func motherStyle(t *testing.T, r *servertest.Rig, track string) string {
	t.Helper()
	var s string
	if err := r.Store.R.QueryRow(`SELECT json_extract(rc.request_json, '$.style') FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id WHERE t.id = ?`, track).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// startVariant estimates and creates (human path) a one-take style_edit variant whose style gets the fake
// markers appended; it returns the batch id and the job id.
func startVariant(t *testing.T, r *servertest.Rig, mother, markers, key string) (batch, job string) {
	t.Helper()
	h := r.Human(t)
	style := motherStyle(t, r, mother)
	if markers != "" {
		style += " " + markers
	}
	est := obj(h.OK(t, "POST", "/api/tracks/"+mother+"/variants/estimate", map[string]any{"mode": "style_edit", "patch": map[string]any{"style": style}}))
	resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "interrupt"}, "Idempotency-Key", key)
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	b := obj(resp.JSON(t))
	return b["id"].(string), obj(arr(b["jobs"])[0])["id"].(string)
}
