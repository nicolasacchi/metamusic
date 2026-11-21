package fake_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
)

func loadOutputSchemaV2(t testing.TB) *jsonschema.Resolved {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "worker", "src", "mmworker", "schema", "mm-worker-2.output.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// specV2 is a /2 take spec of the kind with a target for every artifact its allowlist asks for.
func specV2(job, kind string, ears *provider.EarsSpec, plan *provider.PlanSpec) provider.TakeSpec {
	ts := specCot(job, 1, "pop", "off", 296)
	ts.Wire, ts.Kind, ts.Ears, ts.Plan = provider.ContractV2, kind, ears, plan
	if kind == provider.KindAnalyze {
		ts.Request = nil
		ts.MinAudioS = 0
		ts.Inputs = map[string]provider.InputRef{provider.InputSrcAudio: {URL: "https://mm.example.com/media/x.flac?sig=t", SHA256: strings.Repeat("ab", 32), Bytes: 4}}
	}
	ts.Uploads = map[string]provider.UploadTarget{}
	for _, a := range provider.ArtifactsV2(provider.ShapeOf(ts)) {
		ts.Uploads[a.Name] = provider.UploadTarget{URL: uploadBase + ts.AttemptID + "/" + a.Name, MaxBytes: a.MaxBytes}
	}
	return ts
}

var (
	earsSpec = &provider.EarsSpec{BudgetS: 30, Stages: []string{"asr"}}
	planSpec = &provider.PlanSpec{Seeds: []int64{11, 12}, SecondsPerSeed: 10}
)

func okHook(arts ...string) fake.SimKindHook {
	return func(context.Context, *fake.SimWorker, provider.WorkerInput) (fake.SimKindResult, error) {
		var r fake.SimKindResult
		for _, n := range arts {
			r.Artifacts = append(r.Artifacts, fake.SimArtifact{Name: n, Body: []byte("{\"stub\":\"" + n + "\"}")})
		}
		return r, nil
	}
}

func v2Worker(t testing.TB, up fake.Uploader, kinds map[string]fake.SimKindHook, mod ...func(*fake.SimWorkerOptions)) *fake.SimWorker {
	t.Helper()
	o := fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir(), Kinds: kinds}
	for _, m := range mod {
		m(&o)
	}
	return fake.NewSimWorker(o)
}

func runV2(t testing.TB, w *fake.SimWorker, ts provider.TakeSpec) fake.Result {
	t.Helper()
	res := w.Run(context.Background(), inputFor(t, ts), "ref-1")
	var inst any
	_ = json.Unmarshal(res.Raw, &inst)
	if res.Failed { // a failed run returns {"error": ..., "mm": output}
		inst = inst.(map[string]any)["mm"]
	}
	if err := loadOutputSchemaV2(t).Validate(inst); err != nil {
		t.Errorf("output does not validate against the /2 schema: %v\n%s", err, res.Raw)
	}
	return res
}

func TestSimWorkerV2_PlainRenderSameArtifactsAsV1(t *testing.T) {
	t.Parallel()
	v1 := &recUploader{}
	r1 := newSim(t, v1).Run(context.Background(), inputFor(t, specCot("job1", 1, "pop", "off", 296)), "ref")
	up := &recUploader{}
	ts := specV2("job1", "", nil, nil)
	res := runV2(t, v2Worker(t, up, nil), ts)
	if res.Failed || res.Output.Contract != provider.ContractV2 || res.Output.Kind != provider.KindRender {
		t.Fatalf("result = %+v", res.Output)
	}
	if diff := slices.Compare(v1.names(), up.names()); diff != 0 {
		t.Errorf("/1 uploads %v, /2 uploads %v", v1.names(), up.names())
	}
	if !slices.Equal(res.Output.Caps, []string{"v2"}) || res.Output.Ears != nil || res.Output.Truncated == nil || res.Output.AudioSeconds == nil {
		t.Errorf("output = %+v", res.Output)
	}
	if r1.Output.Contract != provider.ContractV1 || r1.Output.Kind != "" || r1.Output.Caps != nil {
		t.Errorf("the /1 output changed: %+v", r1.Output)
	}
	if err := provider.CheckOutputV2(provider.ShapeOf(ts), res.Output); err != nil {
		t.Error(err)
	}
}

func TestSimWorkerV2_KindUnavailableBeforeAnyWork(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		ts   provider.TakeSpec
		msg  string
	}{
		{"analyze", specV2("j-an", provider.KindAnalyze, earsSpec, nil), "analyze"},
		{"plan", specV2("j-pl", provider.KindPlan, nil, planSpec), "plan"},
		{"ears on a render", specV2("j-ea", "", earsSpec, nil), "ears"},
	} {
		up := &recUploader{}
		fetched := 0
		w := v2Worker(t, up, nil, func(o *fake.SimWorkerOptions) { o.Fetcher = countingFetcher{&fetched} })
		res := runV2(t, w, tc.ts)
		if !res.Failed || res.Output.Error == nil || res.Output.Error.Code != "KIND_UNAVAILABLE" || !strings.Contains(res.Output.Error.Message, tc.msg) {
			t.Errorf("%s: %+v", tc.name, res.Output.Error)
		}
		if res.Output.Contract != provider.ContractV2 || res.Output.Kind == "" {
			t.Errorf("%s: an error output of a /2 run carries its contract and kind: %+v", tc.name, res.Output)
		}
		for _, n := range up.names() {
			if n != provider.ResultArtifact {
				t.Errorf("%s: uploaded %s before refusing", tc.name, n)
			}
		}
		if fetched != 0 {
			t.Errorf("%s: fetched an input before refusing", tc.name)
		}
	}
}

type countingFetcher struct{ n *int }

func (c countingFetcher) Get(context.Context, string) (int, []byte, error) {
	*c.n++
	return 200, []byte("abcd"), nil
}

func TestSimWorkerV2_RenderWithEars(t *testing.T) {
	t.Parallel()
	up := &recUploader{}
	ts := specV2("j-ears", "", earsSpec, nil)
	res := runV2(t, v2Worker(t, up, map[string]fake.SimKindHook{fake.SimKindEars: okHook("analysis.json")}), ts)
	if res.Failed || res.Output.Ears == nil || res.Output.Ears.Status != "ok" || !slices.Equal(res.Output.Caps, []string{"ears", "v2"}) {
		t.Fatalf("output = %+v", res.Output)
	}
	names := up.names()
	if names[len(names)-1] != provider.ResultArtifact || !slices.Contains(names, "analysis.json") || !slices.Contains(names, "audio.flac") {
		t.Errorf("uploads = %v", names)
	}
	if _, ok := res.Output.Artifacts["analysis.json"]; !ok {
		t.Error("analysis.json is not reported")
	}
	// a Tier B failure never fails the take (C321.4)
	up2 := &recUploader{}
	boom := func(context.Context, *fake.SimWorker, provider.WorkerInput) (fake.SimKindResult, error) {
		return fake.SimKindResult{}, errors.New("asr crashed")
	}
	res = runV2(t, v2Worker(t, up2, map[string]fake.SimKindHook{fake.SimKindEars: boom}), ts)
	if res.Failed || res.Output.Ears == nil || res.Output.Ears.Status != "error" || res.Output.Ears.Code != "EARS_FAILED" || slices.Contains(up2.names(), "analysis.json") {
		t.Errorf("failed stage: %+v uploads %v", res.Output, up2.names())
	}
}

func TestSimWorkerV2_Analyze(t *testing.T) {
	t.Parallel()
	ts := specV2("j-an", provider.KindAnalyze, &provider.EarsSpec{BudgetS: 30, Stages: []string{"asr"}, KeepVocals: true}, nil)
	hook := func(context.Context, *fake.SimWorker, provider.WorkerInput) (fake.SimKindResult, error) {
		return fake.SimKindResult{AudioSeconds: 12.5, Artifacts: []fake.SimArtifact{{Name: "analysis.json", Body: []byte("{}")}, {Name: "stem-vocals.flac", Body: []byte("flac")}}}, nil
	}
	sum := sha256.Sum256([]byte("abcd"))
	ts.Inputs[provider.InputSrcAudio] = provider.InputRef{URL: "https://mm.example.com/media/x.flac?sig=t", SHA256: hex.EncodeToString(sum[:]), Bytes: 4}
	fetched := 0
	withFetch := func(o *fake.SimWorkerOptions) { o.Fetcher = countingFetcher{&fetched} }
	up := &recUploader{}
	res := runV2(t, v2Worker(t, up, map[string]fake.SimKindHook{fake.SimKindAnalyze: hook}, withFetch), ts)
	if res.Failed || fetched != 1 || res.Output.Truncated != nil || res.Output.AudioSeconds == nil || res.Output.AudioSeconds.String() != "12.5" {
		t.Fatalf("output = %+v fetched %d", res.Output, fetched)
	}
	if got := up.names(); !slices.Equal(got, []string{"analysis.json", "stem-vocals.flac", "mm-result.json"}) {
		t.Errorf("uploads = %v", got)
	}
	// the input digest is checked before the stage
	bad := specV2("j-an2", provider.KindAnalyze, earsSpec, nil)
	res = runV2(t, v2Worker(t, &recUploader{}, map[string]fake.SimKindHook{fake.SimKindAnalyze: hook}, withFetch), bad)
	if !res.Failed || res.Output.Error.Code != "INPUT_MISMATCH" {
		t.Errorf("mismatch: %+v", res.Output.Error)
	}
	// ANALYZE_FAILED: an error from the hook, or a stage that did not finish ok
	for name, h := range map[string]fake.SimKindHook{
		"error": func(context.Context, *fake.SimWorker, provider.WorkerInput) (fake.SimKindResult, error) {
			return fake.SimKindResult{}, errors.New("no vocals")
		},
		"stage error": func(context.Context, *fake.SimWorker, provider.WorkerInput) (fake.SimKindResult, error) {
			return fake.SimKindResult{Ears: &provider.EarsOut{Status: "error", Code: "ASR_FAILED"}}, nil
		},
	} {
		res = runV2(t, v2Worker(t, &recUploader{}, map[string]fake.SimKindHook{fake.SimKindAnalyze: h}), specV2("j-an3", provider.KindAnalyze, earsSpec, nil))
		if !res.Failed || res.Output.Error.Code != "ANALYZE_FAILED" {
			t.Errorf("%s: %+v", name, res.Output.Error)
		}
	}
	// the default length is the worker's
	def := func(context.Context, *fake.SimWorker, provider.WorkerInput) (fake.SimKindResult, error) {
		return fake.SimKindResult{Artifacts: []fake.SimArtifact{{Name: "analysis.json", Body: []byte("{}")}}}, nil
	}
	res = runV2(t, v2Worker(t, &recUploader{}, map[string]fake.SimKindHook{fake.SimKindAnalyze: def}), specV2("j-an4", provider.KindAnalyze, earsSpec, nil))
	if res.Failed || res.Output.AudioSeconds.String() != "6" {
		t.Errorf("default seconds: %+v", res.Output)
	}
}

func TestSimWorkerV2_Plan(t *testing.T) {
	t.Parallel()
	ts := specV2("j-pl", provider.KindPlan, nil, planSpec)
	hook := func(context.Context, *fake.SimWorker, provider.WorkerInput) (fake.SimKindResult, error) {
		return fake.SimKindResult{
			Artifacts: []fake.SimArtifact{{Name: "plan-00.tar", Body: []byte("tar")}},
			Plans:     []provider.PlanOut{{Index: 0, Seed: 11, Status: "ok", Bundle: "plan-00.tar"}, {Index: 1, Seed: 12, Status: "error", Code: "PIPELINE_ERROR"}},
		}, nil
	}
	up := &recUploader{}
	res := runV2(t, v2Worker(t, up, map[string]fake.SimKindHook{fake.SimKindPlan: hook}), ts)
	if res.Failed || res.Output.AudioSeconds != nil || res.Output.Truncated != nil || len(res.Output.Plans) != 2 || !slices.Equal(res.Output.Caps, []string{"plan", "v2"}) {
		t.Fatalf("output = %+v", res.Output)
	}
	if got := up.names(); !slices.Equal(got, []string{"plan-00.tar", "mm-result.json"}) {
		t.Errorf("uploads = %v", got)
	}
	if err := provider.CheckOutputV2(provider.ShapeOf(ts), res.Output); err != nil {
		t.Error(err)
	}
	allFail := func(context.Context, *fake.SimWorker, provider.WorkerInput) (fake.SimKindResult, error) {
		return fake.SimKindResult{Plans: []provider.PlanOut{{Index: 0, Seed: 11, Status: "error", Code: "X"}}}, nil
	}
	res = runV2(t, v2Worker(t, &recUploader{}, map[string]fake.SimKindHook{fake.SimKindPlan: allFail}), ts)
	if !res.Failed || res.Output.Error.Code != "PLANS_FAILED" {
		t.Errorf("every seed failed: %+v", res.Output.Error)
	}
}

func TestSimWorkerV2_AttachChecked(t *testing.T) {
	t.Parallel()
	data := []byte("X:1\nK:C\n")
	sum := sha256.Sum256(data)
	for name, c := range map[string]struct {
		sha  string
		fail string
	}{
		"matching":   {hex.EncodeToString(sum[:]), ""},
		"mismatched": {strings.Repeat("0", 64), "ATTACH_MISMATCH"},
	} {
		ts := specV2("j-at", "", nil, nil)
		ts.Attach = map[string]provider.AttachFile{"score.abc": {SHA256: c.sha, Data: data}}
		res := runV2(t, v2Worker(t, &recUploader{}, nil), ts)
		if got := ""; res.Failed {
			if got = res.Output.Error.Code; got != c.fail {
				t.Errorf("%s: code %q, want %q", name, got, c.fail)
			}
		} else if c.fail != "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSimWorkerV2_BadInputAnswersV1ErrorOutput(t *testing.T) {
	t.Parallel()
	w := v2Worker(t, &recUploader{}, nil)
	res := w.Run(context.Background(), []byte(`{"contract":"mm.worker/2","take_id":"t","attempt_id":"a"}`), "r")
	if !res.Failed || res.Output.Error.Code != "BAD_INPUT" {
		t.Errorf("output = %+v", res.Output)
	}
}

// TestRegisterSimKind: the process-wide table serves every worker without a hook of its own, a worker's own table wins, and a
// repeated or unknown registration panics. Not parallel: it touches the process-wide table.
func TestRegisterSimKind(t *testing.T) {
	t.Cleanup(fake.ResetSimKinds)
	panics := func(f func()) (p bool) {
		defer func() { p = recover() != nil }()
		f()
		return
	}
	if !panics(func() { fake.RegisterSimKind("mix", okHook()) }) || !panics(func() { fake.RegisterSimKind(fake.SimKindEars, nil) }) {
		t.Error("an unknown stage or a nil hook was accepted")
	}
	fake.RegisterSimKind(fake.SimKindEars, okHook("analysis.json"))
	if !panics(func() { fake.RegisterSimKind(fake.SimKindEars, okHook()) }) {
		t.Error("a repeated registration was accepted")
	}
	ts := specV2("j-reg", "", earsSpec, nil)
	up := &recUploader{}
	if res := runV2(t, v2Worker(t, up, nil), ts); res.Failed || !slices.Contains(up.names(), "analysis.json") {
		t.Errorf("the process-wide hook was not used: %+v", res.Output)
	}
	own := &recUploader{}
	res := runV2(t, v2Worker(t, own, map[string]fake.SimKindHook{fake.SimKindEars: okHook()}), ts)
	if res.Failed || slices.Contains(own.names(), "analysis.json") {
		t.Errorf("the worker's own hook did not win: %v", own.names())
	}
}
