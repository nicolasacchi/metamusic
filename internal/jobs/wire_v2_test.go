package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

func TestWireOf_Table(t *testing.T) {
	t.Parallel()
	earsSpec := `{"ears":{"budget_s":60,"stages":["asr"]}}`
	tests := []struct {
		name, engine, kind, needs, spec string
		wire, takeKind                  string
		shape                           provider.KindShape
		wantErr                         bool
	}{
		{"plain generate", "yue2", KindGenerate, `[]`, `{}`, provider.ContractV1, "", provider.KindShape{Kind: "render"}, false},
		{"empty kind and engine", "", "", `[]`, ``, provider.ContractV1, "", provider.KindShape{Kind: "render"}, false},
		{"needs v2 only", "yue2", KindGenerate, `["v2"]`, `{}`, provider.ContractV2, "", provider.KindShape{Kind: "render"}, false},
		{"render with ears", "yue2", KindGenerate, `["ears","v2"]`, earsSpec, provider.ContractV2, "", provider.KindShape{Kind: "render", Ears: true}, false},
		{"analyze", "yue2", KindAnalyze, `["analyze","ears","v2"]`, defaultSpec(KindAnalyze), provider.ContractV2, "analyze", provider.KindShape{Kind: "analyze", Ears: true}, false},
		{"plan", "yue2", KindPlan, `["plan","v2"]`, defaultSpec(KindPlan), provider.ContractV2, "plan", provider.KindShape{Kind: "plan", Seeds: 1}, false},
		{"attach in the spec alone is v2", "yue2", KindGenerate, `[]`, `{"attach":[{"name":"a","sha256":"` + strings.Repeat("a", 64) + `","source":"plan:x/a"}]}`, provider.ContractV2, "", provider.KindShape{Kind: "render"}, false},
		{"acestep stays v1", "acestep15", KindGenerate, `["v2"]`, `{}`, provider.ContractV1, "", provider.KindShape{Kind: "render"}, false},
		{"broken spec", "yue2", KindGenerate, `[]`, `{"nope":1}`, "", "", provider.KindShape{}, true},
	}
	for _, tc := range tests {
		var needs []string
		_ = json.Unmarshal([]byte(tc.needs), &needs)
		w, err := wireOf(tc.engine, tc.kind, needs, tc.spec)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}
		if tc.wantErr {
			continue
		}
		if w.Wire != tc.wire || w.Kind != tc.takeKind || w.Shape != tc.shape {
			t.Errorf("%s: wire %q kind %q shape %+v, want %q %q %+v", tc.name, w.Wire, w.Kind, w.Shape, tc.wire, tc.takeKind, tc.shape)
		}
	}
	if got := contractOf("yue2", JobRow{Kind: KindPlan, Needs: []string{"plan", "v2"}, SpecJSON: defaultSpec(KindPlan)}); got != provider.ContractV2 {
		t.Errorf("contractOf plan = %q", got)
	}
	if got := contractOf("yue2", JobRow{SpecJSON: `{"bad":1}`}); got != "" {
		t.Errorf("contractOf of a corrupt spec = %q", got)
	}
}

func TestJobWire_OutputRules(t *testing.T) {
	t.Parallel()
	mk := func(kind string, mutate func(*provider.Output)) provider.Output {
		n := json.Number("5")
		o := provider.Output{Contract: provider.ContractV2, Kind: kind, Status: "ok", Artifacts: map[string]provider.ArtifactDigest{}}
		switch kind {
		case "render":
			o.AudioSeconds, o.Truncated = &n, map[string]bool{"abc": false, "semantic": false}
		case "analyze":
			o.AudioSeconds, o.Ears = &n, &provider.EarsOut{Status: "ok"}
		case "plan":
			o.Plans = []provider.PlanOut{{Index: 0, Seed: 1, Status: "ok", Bundle: "plan-00.tar"}, {Index: 1, Seed: 2, Status: "error", Code: "X"}}
		}
		if mutate != nil {
			mutate(&o)
		}
		return o
	}
	v1, _ := wireOf("yue2", KindGenerate, nil, "{}")
	if code, _ := v1.outputProblem(provider.Output{Contract: provider.ContractV1}); code != "" {
		t.Errorf("a /1 job has no /2 rule: %q", code)
	}
	if got := v1.kindRequired(true, []string{"audio.flac", "audio.flac"}, provider.Output{}); !cmp.Equal(got, []string{"audio.flac"}) {
		t.Errorf("v1 required = %v", got)
	}
	analyze, _ := wireOf("yue2", KindAnalyze, []string{"analyze", "ears", "v2"}, defaultSpec(KindAnalyze))
	plan, _ := wireOf("yue2", KindPlan, []string{"plan", "v2"}, `{"plan":{"seeds":[1,2],"seconds_per_seed":10}}`)
	ears, _ := wireOf("yue2", KindGenerate, []string{"ears", "v2"}, `{"ears":{"budget_s":5,"stages":["asr"]}}`)
	tests := []struct {
		name string
		w    jobWire
		out  provider.Output
		want string // "" = fine
	}{
		{"analyze ok", analyze, mk("analyze", func(o *provider.Output) { o.Artifacts["analysis.json"] = provider.ArtifactDigest{} }), ""},
		{"analyze wrong kind", analyze, mk("render", nil), "WORKER_CONTRACT"},
		{"analyze with a failed stage", analyze, mk("analyze", func(o *provider.Output) { o.Ears.Status = "error" }), "WORKER_CONTRACT"},
		{"analyze reports audio.flac", analyze, mk("analyze", func(o *provider.Output) { o.Artifacts["audio.flac"] = provider.ArtifactDigest{} }), "WORKER_CONTRACT"},
		{"plan ok", plan, mk("plan", func(o *provider.Output) { o.Artifacts["plan-00.tar"] = provider.ArtifactDigest{} }), ""},
		{"plan reports a bundle beyond its seeds", plan, mk("plan", func(o *provider.Output) { o.Artifacts["plan-02.tar"] = provider.ArtifactDigest{} }), "WORKER_CONTRACT"},
		{"plan with the wrong number of entries", plan, mk("plan", func(o *provider.Output) { o.Plans = o.Plans[:1] }), "WORKER_CONTRACT"},
		{"plan reports mm-result.json", plan, mk("plan", func(o *provider.Output) { o.Artifacts["mm-result.json"] = provider.ArtifactDigest{} }), "WORKER_CONTRACT"},
		{"render with ears answered", ears, mk("render", func(o *provider.Output) { o.Ears = &provider.EarsOut{Status: "error", Code: "X"} }), ""},
		{"render with ears unanswered", ears, mk("render", nil), "WORKER_CONTRACT"},
		{"v1 contract on a v2 job", ears, mk("render", func(o *provider.Output) { o.Contract = provider.ContractV1 }), "WORKER_CONTRACT"},
	}
	for _, tc := range tests {
		if code, _ := tc.w.outputProblem(tc.out); code != tc.want {
			t.Errorf("%s: outputProblem = %q, want %q", tc.name, code, tc.want)
		}
	}
	if got := ears.kindRequired(true, []string{"audio.flac", "mm-result.json"}, mk("render", func(o *provider.Output) { o.Ears = &provider.EarsOut{Status: "ok"} })); !cmp.Equal(got, []string{"analysis.json", "audio.flac", "mm-result.json"}) {
		t.Errorf("render required = %v", got)
	}
	if got := ears.kindRequired(true, []string{"audio.flac"}, mk("render", func(o *provider.Output) { o.Ears = &provider.EarsOut{Status: "error"} })); !cmp.Equal(got, []string{"audio.flac"}) {
		t.Errorf("a failed stage requires no analysis: %v", got)
	}
	if got := plan.kindRequired(false, []string{"audio.flac"}, mk("plan", nil)); !cmp.Equal(got, []string{"plan-00.tar"}) {
		t.Errorf("plan required = %v", got)
	}
	if artifactAllowed(plan, false, "yue2", "audio.flac") || !artifactAllowed(plan, false, "yue2", "plan-01.tar") || artifactAllowed(plan, false, "yue2", "plan-02.tar") {
		t.Error("a plan's allowlist is its bundles")
	}
	if !artifactAllowed(v1, true, "yue2", "audio.flac") || artifactAllowed(v1, true, "yue2", "analysis.json") || artifactAllowed(v1, false, "yue2", "audio.flac") {
		t.Error("a /1 job keeps the engine allowlist, and only for generate")
	}
}

// TestTakeSpec_FromSpec: the spec of a job becomes the /2 fields of its take spec at submit (C323.5); a plain job stays on /1.
func TestTakeSpec_FromSpec(t *testing.T) {
	t.Run("analyze", func(t *testing.T) {
		b := newBridgeRig(t, false)
		src, _, _ := masterOf(t, b.rig)
		job := b.seed(batchSpec{kind: KindAnalyze, source: src, needs: `["analyze","ears","v2"]`, specJSON: `{"ears":{"lang":"it","budget_s":60,"stages":["separate","asr"],"keep_vocals":true},
			"inputs":{"src_audio":{"track_id":"` + src + `","master_sha256":"` + strings.Repeat("b", 64) + `","bytes":2048}}}`, recipe: b.newRecipe("pop")}).Jobs[0]
		spec := b.specOf(job)
		if spec.Wire != provider.ContractV2 || spec.Kind != provider.KindAnalyze || spec.Request != nil || spec.MinAudioS != 0 {
			t.Errorf("spec = wire %q kind %q request %s", spec.Wire, spec.Kind, spec.Request)
		}
		if e := spec.Ears; e == nil || e.Lang != "it" || e.BudgetS != 60 || !e.KeepVocals || !cmp.Equal(e.Stages, []string{"separate", "asr"}) {
			t.Errorf("ears = %+v", e)
		}
		in := spec.Inputs[provider.InputSrcAudio]
		if in.SHA256 != strings.Repeat("b", 64) || in.Bytes != 2048 || in.URL == "" {
			t.Errorf("inputs = %+v", spec.Inputs)
		}
		if diff := cmp.Diff([]string{"analysis.json", "mm-result.json", "stem-vocals.flac"}, sortedKeys(spec.Uploads)); diff != "" {
			t.Errorf("uploads (-want +got): %s", diff)
		}
		if err := checkSpecParses(spec); err != nil {
			t.Errorf("the document does not parse: %v", err)
		}
	})
	t.Run("plan", func(t *testing.T) {
		b := newBridgeRig(t, false)
		job := b.seed(batchSpec{kind: KindPlan, needs: `["plan","v2"]`, specJSON: `{"plan":{"seeds":[7,8,9],"seconds_per_seed":20}}`, recipe: b.newRecipe("pop")}).Jobs[0]
		spec := b.specOf(job)
		if spec.Kind != provider.KindPlan || spec.Plan == nil || !cmp.Equal(spec.Plan.Seeds, []int64{7, 8, 9}) || spec.Plan.SecondsPerSeed != 20 || len(spec.Request) == 0 {
			t.Errorf("spec = %+v", spec)
		}
		if diff := cmp.Diff([]string{"mm-result.json", "plan-00.tar", "plan-01.tar", "plan-02.tar"}, sortedKeys(spec.Uploads)); diff != "" {
			t.Errorf("uploads (-want +got): %s", diff)
		}
		if err := checkSpecParses(spec); err != nil {
			t.Errorf("the document does not parse: %v", err)
		}
	})
	t.Run("render with ears keeps the request and adds the stage target", func(t *testing.T) {
		b := newBridgeRig(t, false)
		job := b.seed(batchSpec{needs: `["ears","v2"]`, specJSON: `{"ears":{"budget_s":30,"stages":["asr","transcribe"]}}`, recipe: b.newRecipe("pop")}).Jobs[0]
		spec := b.specOf(job)
		if spec.Wire != provider.ContractV2 || spec.Kind != "" || spec.Ears == nil || len(spec.Request) == 0 || spec.Uploads["analysis.json"].MaxBytes != 4<<20 || spec.Uploads["audio.flac"].URL == "" {
			t.Errorf("spec = %+v", spec)
		}
		if err := checkSpecParses(spec); err != nil {
			t.Errorf("the document does not parse: %v", err)
		}
	})
	t.Run("plain render stays byte for byte on /1", func(t *testing.T) {
		b := newBridgeRig(t, false)
		job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
		spec := b.specOf(job)
		if spec.Wire != "" || spec.Kind != "" || spec.Ears != nil || spec.Plan != nil || spec.Attach != nil || spec.Inputs != nil {
			t.Errorf("a plain job carries /2 fields: %+v", spec)
		}
		raw, _ := provider.BuildWorkerInput(spec)
		if !strings.Contains(string(raw), `"contract":"mm.worker/1"`) || strings.Contains(string(raw), `"kind"`) {
			t.Errorf("document = %.80s", raw)
		}
	})
	t.Run("needs v2 alone is a plain render on /2", func(t *testing.T) {
		b := newBridgeRig(t, false)
		job := b.seed(batchSpec{needs: `["v2"]`, recipe: b.newRecipe("pop")}).Jobs[0]
		spec := b.specOf(job)
		raw, err := provider.BuildWorkerInput(spec)
		if err != nil || !strings.Contains(string(raw), `"contract":"mm.worker/2"`) || !strings.Contains(string(raw), `"kind":"render"`) {
			t.Fatalf("document = %.120s (%v)", raw, err)
		}
		for _, n := range []string{"audio.flac", "config.json", "mm-result.json", "request.json", "result.json"} {
			if _, ok := spec.Uploads[n]; !ok {
				t.Errorf("a plain /2 render lacks the /1 target %s", n)
			}
		}
		if _, ok := spec.Uploads["analysis.json"]; ok {
			t.Error("a render without ears asks for analysis.json")
		}
	})
}

// attachSource serves attachments from a map and counts the reads.
type attachSource map[string][]byte

func (a attachSource) ReadAttach(_ context.Context, ref jobspec.AttachRef) ([]byte, error) {
	if b, ok := a[ref.Source]; ok {
		return b, nil
	}
	return nil, errors.New("no such source " + ref.Source)
}

func TestTakeSpec_AttachFromSource(t *testing.T) {
	score := []byte("X:1\nK:C\nCDEF|\n")
	sum := sha256.Sum256(score)
	sha := hex.EncodeToString(sum[:])
	spec := `{"attach":[{"name":"score.abc","sha256":"` + sha + `","source":"plan:p1/score.abc"}]}`
	for _, c := range []struct {
		name   string
		src    AttachSource
		wantOK bool
	}{
		{"read and verified", attachSource{"plan:p1/score.abc": score}, true},
		{"changed since the estimate", attachSource{"plan:p1/score.abc": []byte("other")}, false},
		{"source unreadable", attachSource{}, false},
		{"no source wired", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newBridgeRig(t, false)
			b.run.o.Attach = c.src
			job := b.seed(batchSpec{needs: `["attach","v2"]`, specJSON: spec, recipe: b.newRecipe("pop")}).Jobs[0]
			if c.wantOK {
				got := b.specOf(job)
				if f := got.Attach["score.abc"]; f.SHA256 != sha || string(f.Data) != string(score) {
					t.Errorf("attach = %+v", got.Attach)
				}
				if err := checkSpecParses(got); err != nil {
					t.Errorf("the document does not parse: %v", err)
				}
				return
			}
			for range 6 {
				b.step()
				b.advance(3e9)
			}
			b.wantState(job, StateFailed)
			b.wantCode(job, CodeSubmitRejected)
			if b.count(`SELECT COUNT(*) FROM job_attempts WHERE job_id = ? AND ref IS NOT NULL AND ref <> ''`, job) != 0 {
				t.Error("an unreadable attachment was still submitted")
			}
		})
	}
}

// TestTakeSpec_V2RefusedBeforeSubmit: a /2 document the worker would refuse is never sent (a worker BAD_INPUT closes the gate).
func TestTakeSpec_V2RefusedBeforeSubmit(t *testing.T) {
	for _, c := range []struct {
		name, kind, needs, spec string
		pins                    InputMinter
		code                    string // the failure code, SUBMIT_REJECTED unless set
	}{
		{"analyze without a pin minter", KindAnalyze, `["analyze","ears","v2"]`, defaultSpec(KindAnalyze), minterFunc(func(context.Context, json.RawMessage, time.Time) (map[string]provider.InputRef, error) {
			return nil, nil
		}), ""},
		{"analyze without inputs", KindAnalyze, `["analyze","ears","v2"]`, `{"ears":{"budget_s":60,"stages":["asr"]}}`, nil, ""},
		{"plan without a plan block", KindPlan, `["plan","v2"]`, `{}`, nil, ""},
		{"pins that no longer match", KindAnalyze, `["analyze","ears","v2"]`, defaultSpec(KindAnalyze), changedPins{}, CodeInputMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newBridgeRig(t, false)
			if c.pins != nil {
				b.run.o.Inputs = c.pins
			}
			src := ""
			if c.kind == KindAnalyze {
				src, _, _ = masterOf(t, b.rig)
			}
			job := b.seed(batchSpec{kind: c.kind, source: src, needs: c.needs, specJSON: c.spec, recipe: b.newRecipe("pop")}).Jobs[0]
			for range 6 {
				b.step()
				b.advance(3e9)
			}
			b.wantState(job, StateFailed)
			want := c.code
			if want == "" {
				want = CodeSubmitRejected
			}
			b.wantCode(job, want)
			if got := b.str(`SELECT COALESCE(MAX(detail_json), '') FROM job_events WHERE job_id = ?`, job); strings.Contains(got, "sig=") {
				t.Errorf("an event carries a link: %s", got)
			}
		})
	}
}

// changedPins is a PinMinter whose master changed since the estimate.
type changedPins struct{ fakePins }

func (changedPins) MintPins(context.Context, jobspec.Inputs, time.Time) (map[string]provider.InputRef, error) {
	return nil, ErrInputChanged
}

// TestTakeSpec_InputsNeverPersisted_Analyze extends TestTakeSpec_InputsNeverPersisted to a yue2 analyze job: the signed link
// reaches the worker spec and nowhere else.
func TestTakeSpec_InputsNeverPersisted_Analyze(t *testing.T) {
	b := newBridgeRig(t, false)
	id, sha, size := masterOf(t, b.rig)
	sg := serve.NewSigner([]byte("synthetic-media-key-for-tests-0001"), b.clk.Now)
	m := NewInputMinter(InputMinterOptions{Signer: sg, Store: b.st, Data: b.data, PublicURL: testPublicURL, Clock: b.clk})
	b.run.o.Inputs = m
	pin := `{"ears":{"budget_s":60,"stages":["asr"]},"inputs":{"src_audio":{"track_id":"` + id + `","master_sha256":"` + sha + `","bytes":` + itoa(size) + `}}}`
	job := b.seed(batchSpec{kind: KindAnalyze, source: id, needs: `["analyze","ears","v2"]`, specJSON: pin, recipe: b.newRecipe("pop")}).Jobs[0]
	spec := b.specOf(job)
	in := spec.Inputs[provider.InputSrcAudio]
	u, err := url.Parse(in.URL)
	if err != nil || !strings.HasPrefix(u.String(), testPublicURL+"/media/"+sha+".flac?exp=") || u.Query().Get("sig") == "" || in.SHA256 != sha || in.Bytes != size {
		t.Fatalf("the worker did not get a signed link: %+v", in)
	}
	if err := checkSpecParses(spec); err != nil {
		t.Fatalf("the document does not parse: %v", err)
	}
	db := dumpDB(t, b.rig)
	for _, leak := range []string{u.Query().Get("sig"), "sig=", "exp=", in.URL} {
		if strings.Contains(db, leak) {
			t.Fatalf("the database holds %q", leak)
		}
	}
	raw, _ := json.Marshal(b.job(job))
	if strings.Contains(string(raw), "sig=") || strings.Contains(string(raw), "/media/") {
		t.Errorf("job JSON leaks an input link: %s", raw)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// TestVerify_V2ArtifactRules drives the bridge's verify with /2 outputs: the contract, the kind's own artifacts and the shape.
func TestVerify_V2ArtifactRules(t *testing.T) {
	b := newBridgeRig(t, false)
	eng := ingestEngine{name: "yue2", reportsTruncation: true, required: []string{"audio.flac", "request.json", "config.json", "result.json", provider.ResultArtifact}}
	job := JobRow{ID: "j1", Needs: []string{"ears", "v2"}, SpecJSON: `{"ears":{"budget_s":30,"stages":["asr"]}}`}
	files := map[string]upload.Received{}
	out := provider.Output{Contract: provider.ContractV2, Kind: "render", TakeID: "j1", Status: "ok", Artifacts: map[string]provider.ArtifactDigest{},
		Truncated: map[string]bool{"abc": false, "semantic": false}}
	n := json.Number("5")
	out.AudioSeconds = &n
	add := func(name string) {
		sum := strings.Repeat("c", 64)
		files[name] = upload.Received{Artifact: name, SHA256: sum, Bytes: 3}
		if name != provider.ResultArtifact {
			out.Artifacts[name] = provider.ArtifactDigest{SHA256: sum, Bytes: 3}
		}
	}
	for _, n := range eng.required {
		add(n)
	}
	cv := func(err error) string {
		var c *ContractViolation
		if errors.As(err, &c) {
			return c.Code
		}
		if err != nil {
			return err.Error()
		}
		return ""
	}
	out.Ears = &provider.EarsOut{Status: "error", Code: "ASR_FAILED"}
	if got := cv(b.bridge.verify(job, eng, files, out)); got != "" {
		t.Errorf("a failed stage needs no analysis.json: %q", got)
	}
	out.Ears = &provider.EarsOut{Status: "ok"}
	if got := cv(b.bridge.verify(job, eng, files, out)); got != "ARTIFACT_MISSING" {
		t.Errorf("an ok stage without analysis.json: %q", got)
	}
	add("analysis.json")
	if got := cv(b.bridge.verify(job, eng, files, out)); got != "" {
		t.Errorf("complete /2 render: %q", got)
	}
	v1 := out
	v1.Contract = provider.ContractV1
	if got := cv(b.bridge.verify(job, eng, files, v1)); got != "WORKER_CONTRACT" {
		t.Errorf("a /1 output of a /2 job: %q", got)
	}
	noEars := out
	noEars.Ears = nil
	if got := cv(b.bridge.verify(job, eng, files, noEars)); got != "WORKER_CONTRACT" {
		t.Errorf("an output without the ears block: %q", got)
	}
	// a /1 job still refuses a /2 output
	plain := JobRow{ID: "j1"}
	if got := cv(b.bridge.verify(plain, eng, files, out)); got != "WORKER_CONTRACT" {
		t.Errorf("a /2 output of a /1 job: %q", got)
	}
}

// TestIngestKindJob_V2ArtifactRules: the kind ingester is only reached by an output that fits the document of its kind.
func TestIngestKindJob_V2ArtifactRules(t *testing.T) {
	run := func(t *testing.T, mutate func(*provider.Output), skip ...string) (provider.Output, *planIngester, string) {
		t.Helper()
		pi := &planIngester{}
		b := kindBridge(t, map[string]KindIngester{KindPlan: pi})
		job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`, specJSON: `{"plan":{"seeds":[1,2],"seconds_per_seed":10}}`}).Jobs[0]
		spec := b.specOf(job)
		skipped := map[string]bool{}
		for _, s := range skip {
			skipped[s] = true
		}
		out := b.deliver(spec, deliverOpts{allOptional: true, e2e: 71, skip: skipped, mutateOut: mutate})
		b.setStatus(b.refOf(job, 1), completed(out, 71000))
		for range 12 {
			b.step()
			if IsTerminal(b.state(job)) {
				break
			}
			b.advance(3e9)
		}
		return out, pi, job
	}
	t.Run("a complete plan completes", func(t *testing.T) {
		_, pi, job := run(t, nil)
		if n, _ := pi.calls(); n != 1 {
			t.Errorf("ingester calls %d (job %s)", n, job)
		}
	})
	t.Run("an ok seed without its bundle fails ARTIFACT_MISSING at ingest", func(t *testing.T) {
		_, pi, _ := run(t, func(o *provider.Output) { delete(o.Artifacts, "plan-01.tar") }, "plan-01.tar")
		if n, _ := pi.calls(); n != 0 {
			t.Errorf("the ingester ran %d times for an output that lacks a bundle", n)
		}
	})
	t.Run("a v1 contract is a violation", func(t *testing.T) {
		_, pi, _ := run(t, func(o *provider.Output) { o.Contract = provider.ContractV1 })
		if n, _ := pi.calls(); n != 0 {
			t.Errorf("the ingester ran %d times", n)
		}
	})
	t.Run("a plan output that carries audio_seconds is refused", func(t *testing.T) {
		_, pi, _ := run(t, func(o *provider.Output) { n := json.Number("5"); o.AudioSeconds = &n })
		if n, _ := pi.calls(); n != 0 {
			t.Errorf("the ingester ran %d times", n)
		}
	})
}

// TestIngestKindJob_V2Violations calls the kind ingest directly with outputs that break the rules of the job's kind.
func TestIngestKindJob_V2Violations(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(*provider.Output)
		want   string
	}{
		{"a /1 contract", func(o *provider.Output) { o.Contract = provider.ContractV1 }, "WORKER_CONTRACT"},
		{"one entry for two seeds", func(o *provider.Output) { o.Plans = o.Plans[:1] }, "WORKER_CONTRACT"},
		{"an ok seed whose bundle is not reported", func(o *provider.Output) { delete(o.Artifacts, "plan-00.tar") }, "ARTIFACT_MISSING"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pi := &planIngester{}
			b := kindBridge(t, map[string]KindIngester{KindPlan: pi})
			job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`, specJSON: `{"plan":{"seeds":[1,2],"seconds_per_seed":10}}`}).Jobs[0]
			jr, ar, files, out := b.capture(job, []byte("x"))
			c.mutate(&out)
			_, _, err := b.bridge.IngestKindJob(WithLeaseOwner(context.Background(), "runner-1"), jr, ar, files, out)
			var cv *ContractViolation
			if !errors.As(err, &cv) || cv.Code != c.want {
				t.Fatalf("err = %v, want violation %s", err, c.want)
			}
			if n, _ := pi.calls(); n != 0 {
				t.Errorf("the ingester ran %d times", n)
			}
		})
	}
}

// TestApplyWire_Refusals covers the refusals of applyWire and attemptArtifacts that need no runner loop.
func TestApplyWire_Refusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := t.Context()
	broken := jobRec{JobRow: JobRow{ID: "j", SpecJSON: `{"nope":1}`}, Engine: "yue2"}
	var spec provider.TakeSpec
	if err := r.run.applyWire(ctx, &claim{job: broken}, &spec); err == nil || !strings.Contains(err.Error(), "spec_json") {
		t.Errorf("applyWire on a corrupt spec: %v", err)
	}
	broken.RecipeID = testRecipe
	if _, _, err := r.run.attemptArtifacts(ctx, r.st.R, &broken); err == nil || !strings.Contains(err.Error(), "spec_json") {
		t.Errorf("attemptArtifacts on a corrupt spec: %v", err)
	}
	// attachments past the 1 MiB limit are refused at submit, before anything is sent
	big := make([]byte, provider.MaxAttachBytes+1)
	sum := sha256.Sum256(big)
	r.run.o.Attach = attachSource{"plan:p/big": big}
	oversize := jobRec{JobRow: JobRow{ID: "j", Needs: []string{"attach", "v2"},
		SpecJSON: `{"attach":[{"name":"big.bin","sha256":"` + hex.EncodeToString(sum[:]) + `","source":"plan:p/big"}]}`}, Engine: "yue2"}
	if err := r.run.applyWire(ctx, &claim{job: oversize}, &provider.TakeSpec{Engine: "yue2", Request: []byte(`{"a":1}`)}); err == nil || !strings.Contains(err.Error(), "cannot be built") {
		t.Errorf("oversized attachments: %v", err)
	}
	// a document the parser refuses (a plan job without a plan block) is not sent either
	noPlan := jobRec{JobRow: JobRow{ID: "j", Kind: KindPlan, Needs: []string{"plan", "v2"}, SpecJSON: `{}`}, Engine: "yue2"}
	if err := r.run.applyWire(ctx, &claim{job: noPlan}, &provider.TakeSpec{Engine: "yue2", Request: []byte(`{"a":1}`)}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("a plan job without a plan: %v", err)
	}
}

// TestContractOf_FailsClosed: a job whose spec_json does not parse matches no contract, so its result is a WORKER_CONTRACT.
func TestContractOf_FailsClosed(t *testing.T) {
	t.Parallel()
	if got := contractOf("yue2", JobRow{SpecJSON: `{"nope":1}`}); got != "" {
		t.Errorf("contractOf on a corrupt spec = %q", got)
	}
}

// TestApplyWire_InputChangedKeepsChain: a pin whose master changed keeps ErrInputChanged so submit can classify it (C323.5).
func TestApplyWire_InputChangedKeepsChain(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.run.o.Inputs = changedPins{}
	job := jobRec{JobRow: JobRow{ID: "j", Kind: KindAnalyze, Needs: []string{"analyze", "ears", "v2"},
		SpecJSON: `{"ears":{"budget_s":60,"stages":["asr"]},"inputs":{"src_audio":{"track_id":"t","master_sha256":"` + strings.Repeat("a", 64) + `","bytes":5}}}`}, Engine: "yue2"}
	err := r.run.applyWire(t.Context(), &claim{job: job}, &provider.TakeSpec{Engine: "yue2"})
	if !errors.Is(err, ErrInputChanged) {
		t.Errorf("applyWire error %v does not wrap ErrInputChanged", err)
	}
}

// TestHandleResult_V1ErrorOnV2Job: a /1 worker answering a /2 job with an error output keeps its error code.
func TestHandleResult_V1ErrorOnV2Job(t *testing.T) {
	b := newBridgeRig(t, false)
	job := b.seed(batchSpec{kind: KindGenerate, needs: `["ears","v2"]`, specJSON: `{"ears":{"budget_s":30,"stages":["asr"]}}`, recipe: b.newRecipe("pop")}).Jobs[0]
	spec := b.specOf(job)
	b.deliver(spec, deliverOpts{errorOut: true, mutateOut: func(o *provider.Output) {
		o.Contract, o.Kind, o.Ears = provider.ContractV1, "", nil
	}})
	for range 6 {
		b.step()
		b.advance(3e9)
	}
	if n := b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND code = ? AND json_extract(detail_json, '$.worker_error') = 'PIPELINE_ERROR'`, job, CodeWorkerError); n == 0 {
		t.Errorf("the /1 error output lost its code: %v", b.events(job))
	}
}
