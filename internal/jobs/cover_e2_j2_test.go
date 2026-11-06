package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// Edge cases of the E2-J2 helpers that the end-to-end tests do not reach.

func TestReportsTruncation_Table(t *testing.T) {
	t.Parallel()
	full := registry.Default(registry.Options{ACEStep: true})
	for _, c := range []struct {
		name string
		reg  engine.Registry
		eng  string
		want bool
	}{
		{"yue2 without a registry", nil, "yue2", true},
		{"yue2 registered", full, "yue2", true},
		{"acestep registered", full, "acestep15", false},
		{"acestep not registered", nil, "acestep15", false},
		{"unknown engine", full, "nope", false},
	} {
		if got := reportsTruncation(c.reg, c.eng); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDeclaredBPM_Table(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int{`{"bpm":96}`: 96, `{"bpm":0}`: 0, `{"bpm":-3}`: 0, `{}`: 0, `{"bpm":null}`: 0, `nope`: 0, `{"bpm":"96"}`: 0} {
		if got := declaredBPM(in); got != want {
			t.Errorf("declaredBPM(%s) = %d, want %d", in, got, want)
		}
	}
}

func TestParseInputRefs(t *testing.T) {
	t.Parallel()
	if _, ok := parseInputRefs([]byte(`nope`)); ok {
		t.Error("garbage parsed as inputs")
	}
	if _, ok := parseInputRefs([]byte(`{"id":"x"}`)); ok {
		t.Error("a request without src and ref has inputs")
	}
	if r, ok := parseInputRefs([]byte(`{"ref":{"track_id":"t","clip_sha256":"c"}}`)); !ok || r.Ref == nil || r.Src != nil {
		t.Errorf("ref only: %+v %v", r, ok)
	}
}

func TestDurationTolerance(t *testing.T) {
	t.Parallel()
	for task, want := range map[string]float64{"text2music": 1.5, "": 1.5, "extend": 0.5, "repaint": 0.1, "complete": 0.1, "extract": 0.1} {
		if got := durationTolerance(task); got != want {
			t.Errorf("%q: %v, want %v", task, got, want)
		}
	}
}

func TestRecordEngineRTF_Edges(t *testing.T) {
	b := newACERig(t, false, aceEngines())
	job := b.seedACE(b.aceRecipe(aceRequest(t, nil)))
	eng := ingestEngine{name: "acestep15", eng: aceEngines()["acestep15"], required: nil}
	n := func(s string) *json.Number { v := json.Number(s); return &v }
	good := provider.Output{AudioSeconds: n("4"), Env: provider.OutputEnv{GPUName: "NVIDIA GeForce RTX 4090"},
		Timings: map[string]*json.Number{"generate_s": n("3.5"), "save_s": n("0.5"), "load_s": n("1.0")}}
	valid := recipeInfo{requestJSON: aceRequest(t, nil)}
	run := func(name string, eng ingestEngine, req recipeInfo, out provider.Output, want int) {
		t.Helper()
		tx, err := b.st.W.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.bridge.recordEngineRTF(t.Context(), tx, JobRow{ID: job}, eng, req, out); err != nil {
			_ = tx.Rollback()
			t.Fatalf("%s: %v", name, err)
		}
		var got int
		if err := tx.QueryRowContext(t.Context(), `SELECT COALESCE(SUM(samples), 0) FROM engine_rtf`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback()
		if got != want {
			t.Errorf("%s: %d samples, want %d", name, got, want)
		}
	}
	run("learns", eng, valid, good, 1)
	run("no registered engine", ingestEngine{name: "acestep15"}, valid, good, 0)
	run("no audio length", eng, valid, provider.Output{Env: good.Env, Timings: good.Timings}, 0)
	zero := good
	zero.AudioSeconds = n("0")
	run("zero audio", eng, valid, zero, 0)
	run("unparsable request", eng, recipeInfo{requestJSON: `{"nope":1}`}, good, 0)
	noTimings := good
	noTimings.Timings = nil
	run("no timings", eng, valid, noTimings, 0)
	allLoad := good
	allLoad.Timings = map[string]*json.Number{"generate_s": n("1.0"), "save_s": n("0.0"), "load_s": n("2.0")}
	run("work not above the load", eng, valid, allLoad, 0)
	e2eOnly := good
	e2eOnly.Timings = map[string]*json.Number{"e2e_seconds": n("3.0")}
	run("e2e_seconds fallback", eng, valid, e2eOnly, 1)
}

func TestDurationEvent_Edges(t *testing.T) {
	b := newACERig(t, false, aceEngines())
	job := b.seedACE(b.aceRecipe(aceRequest(t, nil)))
	if ok, err := b.run.acquire(t.Context()); err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	call := func(req string, got float64) int {
		t.Helper()
		tx, err := b.st.W.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := durationEvent(t.Context(), tx, b.run.fence(), JobRow{ID: job}, AttemptRow{}, recipeInfo{requestJSON: req}, got); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = ?`, job, EvDurationMismatch).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	plain := aceRequest(t, nil) // 10.0 s
	if call(plain, 10.0) != 0 || call(plain, 11.5) != 0 || call(plain, 8.5) != 0 {
		t.Error("a length within 1.5 s of the plan was flagged")
	}
	if call(plain, 11.6) != 1 || call(plain, 8.4) != 1 {
		t.Error("a length beyond 1.5 s of the plan was not flagged")
	}
	if call(`{"nope":1}`, 99) != 0 {
		t.Error("an unparsable request was flagged")
	}
	src := func(task string, over map[string]any) string {
		m := map[string]any{"task": task, "duration_ds": nil, "thinking": false, "src": srcOf(1000)}
		for k, v := range over {
			m[k] = v
		}
		return aceRequest(t, m)
	}
	repaint := src("repaint", map[string]any{"repaint_from_ds": 100, "repaint_to_ds": 300})
	if call(repaint, 100.05) != 0 || call(repaint, 100.2) != 1 {
		t.Error("repaint keeps the source length within 0.1 s")
	}
	extend := src("extend", map[string]any{"extend_ds": 300})
	if call(extend, 130.4) != 0 || call(extend, 130.6) != 1 {
		t.Error("extend is source plus extension within 0.5 s")
	}
}

func TestInputMinter_Edges(t *testing.T) {
	r, _, o := minterRig(t)
	id, sha, _ := masterOf(t, r)
	clipSHA := strings.Repeat("e", 64)
	req := json.RawMessage(fmt.Sprintf(`{"ref":{"track_id":%q,"clip_sha256":%q}}`, id, clipSHA))
	exp := r.clk.Now().Add(time.Hour)

	// no resolver, no data root: a reference cannot be served
	if _, err := NewInputMinter(o).MintInputs(t.Context(), req, exp); err == nil {
		t.Error("a reference without a clip resolver was minted")
	}
	// the resolver answers a path that is not on disk
	o.Clips = clipResolver{clipSHA + ".flac": {RelPath: "derived/clips/ee/missing.flac"}}
	if _, err := NewInputMinter(o).MintInputs(t.Context(), req, exp); !errors.Is(err, ErrInputChanged) {
		t.Errorf("clip off disk: %v", err)
	}
	// a directory is not a clip
	if err := r.data.MkdirAll("derived/clips/ee/dir.flac", 0o755); err != nil {
		t.Fatal(err)
	}
	o.Clips = clipResolver{clipSHA + ".flac": {RelPath: "derived/clips/ee/dir.flac"}}
	if _, err := NewInputMinter(o).MintInputs(t.Context(), req, exp); !errors.Is(err, ErrInputChanged) {
		t.Errorf("clip is a directory: %v", err)
	}
	// the resolver itself fails
	o.Clips = failingResolver{}
	if _, err := NewInputMinter(o).MintInputs(t.Context(), req, exp); err == nil || errors.Is(err, ErrInputChanged) {
		t.Errorf("resolver failure must not read as a changed input: %v", err)
	}
	// a reference window past the horizon is refused as well
	if err := r.data.WriteFile("derived/clips/ee/ok.flac", []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	o.Clips = clipResolver{clipSHA + ".flac": {RelPath: "derived/clips/ee/ok.flac"}}
	if _, err := NewInputMinter(o).MintInputs(t.Context(), req, r.clk.Now().Add(15*time.Hour)); !errors.Is(err, ErrInputHorizon) {
		t.Errorf("reference past the horizon: %v", err)
	}
	// an expiry in the past still yields a link valid for one bucket, never a negative TTL
	got, err := NewInputMinter(o).MintInputs(t.Context(), srcRequest(id, sha), r.clk.Now().Add(-time.Hour))
	if err != nil || got[provider.InputSrcAudio].URL == "" {
		t.Errorf("past expiry: %v %v", got, err)
	}
	// a closed store surfaces as an error, not as a changed input
	c := o
	c.Store = nil
	if _, err := NewInputMinter(c).MintInputs(t.Context(), srcRequest(id, sha), exp); err == nil {
		t.Error("a minter without a store minted")
	}
}

type failingResolver struct{}

func (failingResolver) Resolve(context.Context, string, string) (serve.Object, error) {
	return serve.Object{}, errors.New("disk on fire")
}

func TestPreflight_MediaRouteEdges(t *testing.T) {
	t.Parallel()
	var pe *PreflightError
	// the media check alone: an invalid public URL and an unreachable host
	if err := checkMediaRoute(t.Context(), PreflightOptions{PublicURL: "http://[::1", HTTP: http.DefaultClient}); !errors.As(err, &pe) || pe.Check != "media" {
		t.Errorf("invalid URL: %v", err)
	}
	if err := checkMediaRoute(t.Context(), PreflightOptions{PublicURL: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: time.Second}}); !errors.As(err, &pe) || pe.Check != "media" {
		t.Errorf("unreachable: %v", err)
	}
}

// TestVerifyTransfer_ACEStepTruncatedShape: the runner's own verification applies the engine's IngestPolicy too, so an
// ACE-Step output that carries a truncated object never reaches the ingest bridge.
func TestVerifyTransfer_ACEStepTruncatedShape(t *testing.T) {
	b := newACERig(t, false, aceEngines())
	job := b.seedACE(b.aceRecipe(aceRequest(t, nil)))
	b.runACE(job, []byte("not a flac"), func(o *provider.Output) { o.Truncated = map[string]bool{"abc": false, "semantic": false} })
	b.wantState(job, StateFailed)
	if got := b.job(job).ErrorCode; got != CodeWorkerContract {
		t.Errorf("error code %q, want %s", got, CodeWorkerContract)
	}
	if b.count(`SELECT COUNT(*) FROM tracks WHERE job_id = ?`, job) != 0 {
		t.Error("a track was written")
	}
}
