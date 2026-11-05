package fake_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

const (
	aceRev  = "0123456789abcdef0123456789abcdef01234567"
	aceRepo = "ACE-Step/Ace-Step1.5"
)

var aceWeights = map[string]string{aceRepo: aceRev}

// srcBytes stands in for a frozen master served by the media route.
var srcBytes = []byte("synthetic master bytes")

func shaHex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// mapFetcher serves fixed bodies by URL (the in-process media route).
type mapFetcher struct {
	mu     sync.Mutex
	bodies map[string][]byte
	gets   int
}

func (f *mapFetcher) Get(_ context.Context, url string) (int, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if b, ok := f.bodies[url]; ok {
		return http.StatusOK, b, nil
	}
	return http.StatusNotFound, nil, nil
}

func aceReq(over map[string]any) map[string]any {
	r := map[string]any{"id": "syn-ace-s1", "task": "text2music", "caption": "synthwave, female vocal", "lyrics": "[verse]\nla la\nmo mo\n\n[chorus]\nfa fa",
		"instrumental": false, "vocal_language": "en", "bpm": 96, "duration_ds": 300, "seed": 4242, "thinking": false,
		"engine_params_version": "ace1", "inference_steps": 8}
	for k, v := range over {
		if v == nil {
			delete(r, k)
		} else {
			r[k] = v
		}
	}
	return r
}

// aceSpec builds an ACE-Step TakeSpec; inputs maps src_audio/reference_audio to a body.
func aceSpec(job string, attempt int, req map[string]any, inputs map[string][]byte) (provider.TakeSpec, *mapFetcher) {
	aid := fmt.Sprintf("%s-a%d", job, attempt)
	list, _ := provider.ArtifactsFor(provider.EngineACEStep)
	up := map[string]provider.UploadTarget{}
	for _, a := range list {
		up[a.Name] = provider.UploadTarget{URL: uploadBase + aid + "/" + a.Name, MaxBytes: a.MaxBytes}
	}
	f := &mapFetcher{bodies: map[string][]byte{}}
	var in map[string]provider.InputRef
	for k, body := range inputs {
		if in == nil {
			in = map[string]provider.InputRef{}
		}
		url := "https://mm.example.com/media/" + aid + "/" + k
		f.bodies[url] = body
		in[k] = provider.InputRef{URL: url, SHA256: shaHex(body), Bytes: int64(len(body))}
	}
	raw, _ := json.Marshal(req)
	return provider.TakeSpec{
		JobID: job, AttemptID: aid, Engine: provider.EngineACEStep, Request: raw,
		TimeoutS: 300, TTL: 2 * time.Hour, UploadBudgetS: 60, Uploads: up,
		StartURL: "https://mm.example.com/worker/v1/attempts/" + aid + "/start?exp=1&sig=x",
		Expect:   provider.WorkerExpect{Weights: aceWeights}, Deadline: t0.Add(3 * time.Hour), Inputs: in,
	}, f
}

func srcRef(ds int) map[string]any {
	return map[string]any{"track_id": "t1", "master_sha256": shaHex(srcBytes), "duration_ds": ds}
}

func aceOutputSchema(tb testing.TB) *jsonschema.Resolved {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "worker", "src", "mmworker", "schema", "mm-worker-1.acestep15.output.schema.json"))
	if err != nil {
		tb.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		tb.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		tb.Fatal(err)
	}
	return rs
}

func validOutput(tb testing.TB, rs *jsonschema.Resolved, raw []byte) {
	tb.Helper()
	var inst any
	if err := json.Unmarshal(raw, &inst); err != nil {
		tb.Fatal(err)
	}
	if err := rs.Validate(inst); err != nil {
		tb.Fatalf("output does not validate against the acestep15 schema: %v\n%s", err, raw)
	}
	if _, err := provider.DecodeOutput(raw); err != nil {
		tb.Fatalf("DecodeOutput: %v", err)
	}
}

// aceRun runs one handler directly (no backend) with a header-only renderer; the returned func reports the
// seconds the renderer was asked for.
func aceRun(tb testing.TB, ts provider.TakeSpec, f fake.InputFetcher, scale float64) (fake.Result, *recUploader, *fake.SimWorker) {
	tb.Helper()
	up := &recUploader{}
	w := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, Fetcher: f, HeaderOnlyFLAC: headerOnly, TempDir: tb.TempDir(), AudioScale: scale})
	raw, err := provider.BuildWorkerInput(ts)
	if err != nil {
		tb.Fatal(err)
	}
	return w.Run(context.Background(), raw, "ref-1"), up, w
}

func TestSimWorker_AceStepContract(t *testing.T) {
	t.Parallel()
	rs := aceOutputSchema(t)
	refBody := []byte("synthetic clip")
	cases := []struct {
		name    string
		req     map[string]any
		inputs  map[string][]byte
		seconds float64
		lrc     bool
	}{
		{"text2music", aceReq(nil), nil, 30, false},
		{"text2music lrc", aceReq(map[string]any{"lrc": true}), nil, 30, true},
		{"repaint", aceReq(map[string]any{"task": "repaint", "duration_ds": nil, "src": srcRef(600), "repaint_from_ds": 80, "repaint_to_ds": 140}),
			map[string][]byte{"src_audio": srcBytes}, 60, false},
		{"extend", aceReq(map[string]any{"task": "extend", "duration_ds": nil, "src": srcRef(2740), "extend_ds": 300}),
			map[string][]byte{"src_audio": srcBytes}, 304, false},
		{"complete", aceReq(map[string]any{"task": "complete", "duration_ds": nil, "src": srcRef(900), "tracks": []string{"drums"}, "inference_steps": 50, "guidance_milli": 7000}),
			map[string][]byte{"src_audio": srcBytes}, 90, false},
		{"extract", aceReq(map[string]any{"task": "extract", "duration_ds": nil, "src": srcRef(900), "tracks": []string{"vocals"}, "inference_steps": 50, "guidance_milli": 7000}),
			map[string][]byte{"src_audio": srcBytes}, 90, false},
		{"reference", aceReq(map[string]any{"ref": map[string]any{"track_id": "t2", "clip_sha256": shaHex(refBody), "from_ds": 100, "to_ds": 300}}),
			map[string][]byte{"reference_audio": refBody}, 30, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ts, f := aceSpec("job-"+strings.ReplaceAll(c.name, " ", "-"), 1, c.req, c.inputs)
			res, up, _ := aceRun(t, ts, f, 0)
			if res.Failed {
				t.Fatalf("failed: %s", res.ErrText)
			}
			validOutput(t, rs, res.Raw)
			if res.Output.Truncated != nil || strings.Contains(string(res.Raw), `"truncated":{`) {
				t.Errorf("truncated must be null: %s", res.Raw)
			}
			if res.Output.Env.Engine != provider.EngineACEStep || res.Output.Env.Weights[aceRepo] != aceRev {
				t.Errorf("env = %+v", res.Output.Env)
			}
			if got, _ := res.Output.AudioSeconds.Float64(); got != c.seconds {
				t.Errorf("audio_seconds = %v, want %v", got, c.seconds)
			}
			req, _ := json.Marshal(c.req)
			required, optional := acestep.Engine{}.Artifacts(req)
			uploaded := up.names()
			for _, n := range required {
				if !slices.Contains(uploaded, n) {
					t.Errorf("required artifact %s not uploaded (%v)", n, uploaded)
				}
			}
			if c.lrc != slices.Contains(uploaded, "lyrics.lrc") || (c.lrc && !slices.Contains(optional, "lyrics.lrc")) {
				t.Errorf("lyrics.lrc uploaded = %v, want %v (%v)", slices.Contains(uploaded, "lyrics.lrc"), c.lrc, uploaded)
			}
			if uploaded[len(uploaded)-1] != "mm-result.json" {
				t.Errorf("mm-result.json must be last: %v", uploaded)
			}
			for _, n := range uploaded {
				if n == "config.json" || strings.HasSuffix(n, ".npy") || n == "score.abc" {
					t.Errorf("yue2 artifact %s uploaded by the ACE-Step fake", n)
				}
			}
			var result struct {
				Task, ID string
				Seconds  float64 `json:"audio_seconds"`
			}
			if err := json.Unmarshal(up.body("result.json"), &result); err != nil || result.Task != c.req["task"] || result.Seconds != c.seconds {
				t.Errorf("result.json = %s (%v)", up.body("result.json"), err)
			}
			if c.lrc {
				lrc := string(up.body("lyrics.lrc"))
				if !strings.HasPrefix(lrc, "[00:00.00] la la\n[00:04.00] mo mo\n[00:08.00] fa fa") {
					t.Errorf("lyrics.lrc = %q", lrc)
				}
			}
		})
	}
}

func TestSimWorker_ExtendLength(t *testing.T) {
	t.Parallel()
	ts, f := aceSpec("job-ext", 1, aceReq(map[string]any{"task": "extend", "duration_ds": nil, "src": srcRef(2740), "extend_ds": 300}),
		map[string][]byte{"src_audio": srcBytes})
	res, _, _ := aceRun(t, ts, f, 5)
	if res.Failed {
		t.Fatal(res.ErrText)
	}
	if got, _ := res.Output.AudioSeconds.Float64(); got != 304 {
		t.Errorf("274 s source + 30 s = %v s, want 304", got)
	}
}

func TestFake_ReferenceShiftsTone(t *testing.T) {
	t.Parallel()
	audio := func(refBody []byte) []byte {
		req := aceReq(nil)
		in := map[string][]byte(nil)
		if refBody != nil {
			req["ref"] = map[string]any{"track_id": "t2", "clip_sha256": shaHex(refBody), "from_ds": 100, "to_ds": 300}
			in = map[string][]byte{"reference_audio": refBody}
		}
		ts, f := aceSpec("job-ref", 1, req, in) // same attempt id and seed throughout
		res, up, _ := aceRun(t, ts, f, 0)
		if res.Failed {
			t.Fatal(res.ErrText)
		}
		return up.body("audio.flac")
	}
	plain, a, b, a2 := audio(nil), audio([]byte("clip a")), audio([]byte("clip b")), audio([]byte("clip a"))
	if string(plain) == string(a) || string(a) == string(b) {
		t.Error("a reference (or a different one) must change the rendered audio")
	}
	if string(a) != string(a2) {
		t.Error("the same reference must render the same audio")
	}
}

func TestFake_StemTonePerName(t *testing.T) {
	t.Parallel()
	stem := func(name string) []byte {
		ts, f := aceSpec("job-stem", 1, aceReq(map[string]any{"task": "extract", "duration_ds": nil, "src": srcRef(300), "tracks": []string{name}, "inference_steps": 50, "guidance_milli": 7000}),
			map[string][]byte{"src_audio": srcBytes})
		res, up, _ := aceRun(t, ts, f, 0)
		if res.Failed {
			t.Fatal(res.ErrText)
		}
		return up.body("audio.flac")
	}
	vocals := stem("vocals")
	if string(vocals) == string(stem("drums")) {
		t.Error("stems of different names must differ")
	}
	if string(vocals) != string(stem("vocals")) {
		t.Error("a stem must be deterministic")
	}
}

func TestSimWorker_InputFailures(t *testing.T) {
	t.Parallel()
	rs := aceOutputSchema(t)
	req := aceReq(map[string]any{"task": "repaint", "duration_ds": nil, "src": srcRef(600), "repaint_from_ds": 80, "repaint_to_ds": 140})
	t.Run("sha mismatch", func(t *testing.T) {
		t.Parallel()
		ts, f := aceSpec("job-mm", 1, req, map[string][]byte{"src_audio": srcBytes})
		for u := range f.bodies {
			f.bodies[u] = []byte("other bytes")
		}
		res, up, _ := aceRun(t, ts, f, 0)
		if !res.Failed || res.Output.Error.Code != "INPUT_MISMATCH" {
			t.Fatalf("got %+v", res.Output.Error)
		}
		raw, _ := json.Marshal(res.Output)
		validOutput(t, rs, raw)
		if slices.Contains(up.names(), "audio.flac") {
			t.Error("nothing is rendered after a mismatch")
		}
	})
	t.Run("fetch fails", func(t *testing.T) {
		t.Parallel()
		ts, f := aceSpec("job-ff", 1, req, map[string][]byte{"src_audio": srcBytes})
		f.bodies = map[string][]byte{}
		res, _, _ := aceRun(t, ts, f, 0)
		if !res.Failed || res.Output.Error.Code != "INPUT_FAILED" {
			t.Fatalf("got %+v", res.Output.Error)
		}
	})
}

// recordingFLAC counts the seconds the renderer is asked for.
type recordingFLAC struct {
	mu   sync.Mutex
	secs []float64
}

func (r *recordingFLAC) render(id uint64, seconds float64) []byte {
	r.mu.Lock()
	r.secs = append(r.secs, seconds)
	r.mu.Unlock()
	return headerOnly(id, seconds)
}

func TestFake_600s_Accounted(t *testing.T) {
	t.Parallel()
	rec := &recordingFLAC{}
	up := &recUploader{}
	w := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: rec.render, TempDir: t.TempDir(), AudioScale: 5})
	ts, _ := aceSpec("job-600", 1, aceReq(map[string]any{"duration_ds": 6000}), nil)
	raw, _ := provider.BuildWorkerInput(ts)
	res := w.Run(context.Background(), raw, "r")
	if res.Failed {
		t.Fatal(res.ErrText)
	}
	if got, _ := res.Output.AudioSeconds.Float64(); got != 600 {
		t.Errorf("accounted audio_seconds = %v, want 600", got)
	}
	var result struct {
		Seconds float64 `json:"audio_seconds"`
	}
	_ = json.Unmarshal(up.body("result.json"), &result)
	if result.Seconds != 600 {
		t.Errorf("result.json audio_seconds = %v, want 600", result.Seconds)
	}
	if len(rec.secs) != 1 || rec.secs[0] != 5 || w.SynthesizedSeconds() != 5 {
		t.Errorf("rendered %v (counter %v), want one 5 s render", rec.secs, w.SynthesizedSeconds())
	}
}

func TestFake_AudioScale_Env(t *testing.T) {
	t.Setenv("MM_FAKE_AUDIO_SCALE", "2")
	ts, _ := aceSpec("job-env", 1, aceReq(map[string]any{"duration_ds": 4800}), nil)
	res, _, w := aceRun(t, ts, nil, 0)
	if res.Failed {
		t.Fatal(res.ErrText)
	}
	if got, _ := res.Output.AudioSeconds.Float64(); got != 480 || w.SynthesizedSeconds() != 2 {
		t.Errorf("accounted %v, synthesised %v", got, w.SynthesizedSeconds())
	}
}

func TestFake_AudioScale_BudgetProof(t *testing.T) {
	t.Setenv("MM_FAKE_AUDIO_SCALE", "5")
	up := &recUploader{}
	w := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir()})
	for i, ds := range []int{6000, 4800, 3000, 1800, 6000, 4800, 3000, 1800, 6000, 4800, 3000, 1800} { // 12 long takes, 4 h of accounted audio
		ts, _ := aceSpec(fmt.Sprintf("job-b%d", i), 1, aceReq(map[string]any{"duration_ds": ds}), nil)
		raw, _ := provider.BuildWorkerInput(ts)
		if res := w.Run(context.Background(), raw, "r"); res.Failed {
			t.Fatal(res.ErrText)
		}
	}
	if got := w.SynthesizedSeconds(); got > 60 {
		t.Errorf("synthesised %v s, budget 60", got)
	}
}

func TestSimWorker_YuE2IgnoresAudioScale(t *testing.T) {
	t.Setenv("MM_FAKE_AUDIO_SCALE", "1")
	var secs []float64
	up := &recUploader{}
	w := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, TempDir: t.TempDir(), AudioSeconds: 6,
		HeaderOnlyFLAC: func(id uint64, s float64) []byte { secs = append(secs, s); return headerOnly(id, s) }})
	raw, _ := provider.BuildWorkerInput(spec("job-y", 1, "pop", 300))
	if res := w.Run(context.Background(), raw, "r"); res.Failed || len(secs) != 1 || secs[0] != 6 {
		t.Errorf("yue2 render changed: %v %v", secs, res.ErrText)
	}
}

func newACE(tb testing.TB, o fake.Options) (*fake.Backend, *recUploader, func(time.Duration)) {
	tb.Helper()
	up := &recUploader{}
	o.Worker = fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: headerOnly, TempDir: tb.TempDir(), Weights: aceWeights})
	clk := clocktest.NewManual(t0)
	o.Clock = clk
	return fake.NewACEStep(o), up, clk.Advance
}

func TestFakeACEStep_Identity(t *testing.T) {
	t.Parallel()
	b, _, adv := newACE(t, fake.Options{})
	if b.Name() != provider.BackendFakeACEStep || b.Engine() != provider.EngineACEStep {
		t.Fatalf("name %q engine %q", b.Name(), b.Engine())
	}
	q, err := b.Quote(context.Background())
	if err != nil || q.Backend != provider.BackendFakeACEStep {
		t.Fatalf("quote %+v %v", q, err)
	}
	if b.ColdStartS() < 1 || b.MaxRuntime() <= 0 {
		t.Errorf("cold %d max %v", b.ColdStartS(), b.MaxRuntime())
	}
	ts, _ := aceSpec("job-id", 1, aceReq(nil), nil)
	ref := mustSubmit(t, b, ts)
	if ref.Backend != provider.BackendFakeACEStep {
		t.Errorf("ref backend %q", ref.Backend)
	}
	y := fake.New(fake.Options{})
	if y.Name() != provider.BackendFake || y.Engine() != provider.EngineYuE2 {
		t.Errorf("yue2 fake changed: %q %q", y.Name(), y.Engine())
	}
	if st, _ := y.Poll(context.Background(), ref); st.State != provider.NotFound {
		t.Errorf("the yue2 fake must not know an ACE-Step ref: %v", st.State)
	}
	adv(time.Minute)
	if st := mustPoll(t, b, ref); st.State != provider.Completed {
		t.Fatalf("state %v (%s)", st.State, st.ErrorText)
	}
}

func TestFakeACEStep_BackendOutputValidates(t *testing.T) {
	t.Parallel()
	b, up, adv := newACE(t, fake.Options{})
	ts, _ := aceSpec("job-out", 1, aceReq(map[string]any{"lrc": true}), nil)
	ref := mustSubmit(t, b, ts)
	adv(time.Minute)
	st := mustPoll(t, b, ref)
	if st.State != provider.Completed {
		t.Fatalf("state %v %s", st.State, st.ErrorText)
	}
	validOutput(t, aceOutputSchema(t), st.Output)
	if !slices.Contains(up.names(), "lyrics.lrc") {
		t.Errorf("uploaded %v", up.names())
	}
}

func TestFake_InitFailReportsUnhealthy(t *testing.T) {
	t.Parallel()
	b, _, adv := newACE(t, fake.Options{})
	y, yclk := newFake(t, fake.Options{})
	ts, _ := aceSpec("job-if", 1, aceReq(map[string]any{"caption": "synthwave fake:init_fail"}), nil)
	ref := mustSubmit(t, b, ts)
	adv(6 * time.Minute)
	rep, err := b.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if countStatus(rep.Workers, "UNHEALTHY") != 1 || rep.Health == nil || rep.Health.Unhealthy != 1 {
		t.Fatalf("workers %+v health %+v", rep.Workers, rep.Health)
	}
	if st := mustPoll(t, b, ref); st.State != provider.Queued {
		t.Errorf("a job on a worker that cannot load never runs: %v", st.State)
	}
	// the worker stays UNHEALTHY across sweeps (continuous evidence), until scaled to zero
	adv(10 * time.Minute)
	if rep, _ = b.Sweep(context.Background()); rep.Health == nil || rep.Health.Unhealthy != 1 {
		t.Errorf("still unhealthy: %+v", rep.Health)
	}
	if err := b.ScaleToZero(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rep, _ = b.Sweep(context.Background()); countStatus(rep.Workers, "UNHEALTHY") != 0 {
		t.Errorf("scale to zero must stop the unhealthy worker: %+v", rep.Workers)
	}
	// a yue2 fake is untouched and never reports Health
	yr := mustSubmit(t, y, spec("job-y", 1, "pop", 300))
	yclk.Advance(time.Minute)
	if mustPoll(t, y, yr).State != provider.Completed {
		t.Error("yue2 job must complete")
	}
	if rep, _ := y.Sweep(context.Background()); rep.Health != nil {
		t.Errorf("yue2 sweep must keep Health nil: %+v", rep.Health)
	}
}

func TestFake_SlowLoadExceedsColdStart(t *testing.T) {
	t.Parallel()
	b, _, adv := newACE(t, fake.Options{ColdStartS: 30})
	ts, _ := aceSpec("job-sl", 1, aceReq(map[string]any{"caption": "synthwave fake:slow_load"}), nil)
	ref := mustSubmit(t, b, ts)
	adv(time.Duration(30+60) * time.Second) // the scaler window (cold start + 60 s)
	if st := mustPoll(t, b, ref); st.State == provider.Running || st.State == provider.Completed {
		t.Errorf("the worker is still loading: %v", st.State)
	}
	if rep, _ := b.Sweep(context.Background()); countStatus(rep.Workers, "INITIALIZING") != 1 {
		t.Errorf("workers %+v", rep.Workers)
	}
	adv(3 * time.Minute)
	if st := mustPoll(t, b, ref); st.State != provider.Completed {
		t.Errorf("after the load the job completes: %v", st.State)
	}
	// an explicit value
	b2, _, adv2 := newACE(t, fake.Options{ColdStartS: 30})
	ts2, _ := aceSpec("job-sl2", 1, aceReq(map[string]any{"caption": "x fake:slow_load=600"}), nil)
	r2 := mustSubmit(t, b2, ts2)
	adv2(9 * time.Minute)
	if st := mustPoll(t, b2, r2); st.State == provider.Completed {
		t.Error("fake:slow_load=600 loads for 10 minutes")
	}
}

func TestFake_ACEStep_FaultMarkersRideInCaption(t *testing.T) {
	t.Parallel()
	b, _, adv := newACE(t, fake.Options{})
	ts, _ := aceSpec("job-fw", 1, aceReq(map[string]any{"caption": "synthwave fake:fail=worker_error"}), nil)
	ref := mustSubmit(t, b, ts)
	adv(time.Minute)
	if st := mustPoll(t, b, ref); st.State != provider.Failed {
		t.Errorf("state %v", st.State)
	}
}

// The real (ffmpeg) render path: a 600 s take synthesises only the scaled seconds but accounts 600.
func TestFake_ACEStep_RealRenderScaled(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	up := &recUploader{}
	w := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, Tools: testtools.Tools(), TempDir: t.TempDir(), AudioScale: 2})
	ts, _ := aceSpec("job-real", 1, aceReq(map[string]any{"duration_ds": 6000}), nil)
	raw, _ := provider.BuildWorkerInput(ts)
	res := w.Run(context.Background(), raw, "r")
	if res.Failed {
		t.Fatal(res.ErrText)
	}
	if got, _ := res.Output.AudioSeconds.Float64(); got != 600 {
		t.Errorf("accounted %v", got)
	}
	validOutput(t, aceOutputSchema(t), res.Raw)
	flac := up.body("audio.flac")
	if len(flac) < 4 || string(flac[:4]) != "fLaC" {
		t.Errorf("audio.flac is not FLAC (%d bytes)", len(flac))
	}
	if len(flac) > 2<<20 {
		t.Errorf("a 2 s render is %d bytes", len(flac))
	}
}
