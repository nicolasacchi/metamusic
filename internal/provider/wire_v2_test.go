package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testSHA   = "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"
	testStart = "https://mm.example.com/worker/v1/attempts/a2/start?exp=1760000000&sig=abc"
)

var testRequest = json.RawMessage(`{"cot":"melody","id":"syn-a-s1102","lyrics":"[verse]\nla la","seed":1102,"style":"pop, 100 BPM"}`)

// specV2 is a well-formed /2 take spec of the shape, with a target for every artifact its allowlist asks for.
func specV2(kind string, ears *EarsSpec, plan *PlanSpec) TakeSpec {
	t := TakeSpec{
		JobID: "a1", AttemptID: "a2", Engine: EngineYuE2, Request: testRequest, TimeoutS: 296, UploadBudgetS: 60,
		Deadline: time.Unix(1760000000, 0), StartURL: testStart, Kind: kind, Ears: ears, Plan: plan, Wire: ContractV2,
		Expect: WorkerExpect{Weights: map[string]string{"m-a-p/YuE2-3B": rev3B, "m-a-p/YuE2-Vae": revVAE}},
	}
	if kind == KindAnalyze {
		t.Request = nil
		t.Inputs = map[string]InputRef{InputSrcAudio: {URL: "https://mm.example.com/media/x.flac?sig=abc", SHA256: testSHA, Bytes: 1 << 20}}
	}
	t.Uploads = map[string]UploadTarget{}
	for _, a := range ArtifactsV2(ShapeOf(t)) {
		t.Uploads[a.Name] = UploadTarget{URL: "https://mm.example.com/worker/v1/uploads/" + a.Name + "?sig=abc", MaxBytes: a.MaxBytes}
	}
	return t
}

var (
	earsAll    = &EarsSpec{BudgetS: 110, Stages: []string{"separate", "asr", "transcribe"}}
	earsKeep   = &EarsSpec{Lang: "it", BudgetS: 60, Stages: []string{"separate", "asr"}, KeepVocals: true}
	planThree  = &PlanSpec{Seeds: []int64{1101, 1102, 1103}, SecondsPerSeed: 30}
	attachFile = map[string]AttachFile{"score.abc": {SHA256: testSHA, Data: []byte("X:1\nK:C\nCDEF|\n")}}
)

func buildV2(t *testing.T, spec TakeSpec) map[string]any {
	t.Helper()
	raw, err := BuildWorkerInputV2(spec)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWorkerInputV2_Golden(t *testing.T) {
	t.Parallel()
	withAttach := specV2(KindRender, nil, nil)
	withAttach.Attach = attachFile
	cases := map[string]TakeSpec{
		"render-ears":   specV2(KindRender, earsAll, nil),
		"analyze":       specV2(KindAnalyze, earsKeep, nil),
		"plan":          specV2(KindPlan, nil, planThree),
		"render-attach": withAttach,
	}
	for name, spec := range cases {
		raw, err := BuildWorkerInput(spec) // Wire v2 dispatches
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := ParseWorkerInput(raw); err != nil {
			t.Errorf("%s: the parser refuses the built document: %v", name, err)
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err != nil {
			t.Fatal(err)
		}
		pretty.WriteByte('\n')
		golden := filepath.Join("testdata", "worker-input-v2-"+name+".json")
		if *updateE2 {
			if err := os.WriteFile(golden, pretty.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, pretty.Bytes()) {
			t.Errorf("%s: the /2 document changed\n got %s\nwant %s", name, pretty.Bytes(), want)
		}
	}
}

// TestBuildWorkerInput_V1Unchanged: a spec on /1 (Wire "" or V1) is built without any /2 key, even when /2 fields are set.
func TestBuildWorkerInput_V1Unchanged(t *testing.T) {
	t.Parallel()
	spec := specV2(KindRender, nil, nil)
	spec.Wire = ""
	spec.Uploads = map[string]UploadTarget{"audio.flac": {URL: "https://mm.example.com/u", MaxBytes: 1}}
	a, err := BuildWorkerInput(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Wire = ContractV1
	b, _ := BuildWorkerInput(spec)
	if !bytes.Equal(a, b) {
		t.Error("Wire V1 and Wire empty build different documents")
	}
	var m map[string]any
	_ = json.Unmarshal(a, &m)
	for _, k := range append([]string{"contract"}, v2OnlyKeys...) {
		if _, ok := m[k]; ok != (k == "contract") {
			t.Errorf("/1 document key %q present=%v", k, ok)
		}
	}
	if m["contract"] != ContractV1 {
		t.Errorf("contract = %v", m["contract"])
	}
}

func TestBuildWorkerInputV2_Refusals(t *testing.T) {
	t.Parallel()
	ace := specV2(KindRender, nil, nil)
	ace.Engine = EngineACEStep
	if _, err := BuildWorkerInputV2(ace); !errors.Is(err, ErrWorkerInput) {
		t.Errorf("acestep on /2: %v", err)
	}
	big := specV2(KindRender, nil, nil)
	big.Attach = map[string]AttachFile{"a.bin": {SHA256: testSHA, Data: make([]byte, MaxAttachBytes+1)}}
	if _, err := BuildWorkerInputV2(big); !errors.Is(err, ErrAttachTooLarge) {
		t.Errorf("oversized attachments: %v", err)
	}
	body := specV2(KindRender, nil, nil)
	body.Request = json.RawMessage(`{"lyrics":"` + strings.Repeat("a", MaxRunBody) + `"}`)
	if _, err := BuildWorkerInputV2(body); !errors.Is(err, ErrWorkerInput) {
		t.Errorf("oversized body: %v", err)
	}
	// the empty lang is "auto" on the wire
	m := buildV2(t, specV2(KindRender, &EarsSpec{BudgetS: 5, Stages: []string{"asr"}}, nil))
	if m["ears"].(map[string]any)["lang"] != "auto" {
		t.Errorf("ears.lang = %v", m["ears"])
	}
	// a plain render on /2 states its kind
	if m := buildV2(t, specV2("", nil, nil)); m["kind"] != KindRender {
		t.Errorf("kind = %v", m["kind"])
	}
}

type v2Row struct {
	name       string
	base       TakeSpec
	mutate     func(m map[string]any)
	wantErr    bool
	parserOnly bool // a limit the schema cannot state
}

func uploadsOf(m map[string]any) map[string]any { return m["uploads"].(map[string]any) }

func tgt(name string) map[string]any {
	limit, _ := UploadLimitV2(name)
	if limit == 0 {
		limit, _ = ArtifactMaxBytesFor(EngineYuE2, name)
	}
	return map[string]any{"url": "https://mm.example.com/worker/v1/uploads/" + name + "?sig=abc", "max_bytes": limit}
}

func v2Rows() []v2Row {
	render := specV2(KindRender, nil, nil)
	ears := specV2(KindRender, earsAll, nil)
	keep := specV2(KindRender, earsKeep, nil)
	an := specV2(KindAnalyze, earsAll, nil)
	anKeep := specV2(KindAnalyze, earsKeep, nil)
	plan := specV2(KindPlan, nil, planThree)
	att := specV2(KindRender, nil, nil)
	att.Attach = attachFile
	earsOf := func(m map[string]any) map[string]any { return m["ears"].(map[string]any) }
	planOf := func(m map[string]any) map[string]any { return m["plan"].(map[string]any) }
	attOf := func(m map[string]any) map[string]any {
		return m["attach"].(map[string]any)["score.abc"].(map[string]any)
	}
	srcOf := func(m map[string]any) map[string]any {
		return m["inputs"].(map[string]any)["src_audio"].(map[string]any)
	}
	fbTarget := func(m map[string]any, n string) {
		uploadsOf(m)[n].(map[string]any)["fallback"] = map[string]any{"url": "https://r2.example.com/x?sig=abc"}
	}
	return []v2Row{
		{name: "valid plain render", base: render},
		{name: "valid render with ears", base: ears},
		{name: "valid render with ears and keep_vocals", base: keep},
		{name: "valid analyze", base: an},
		{name: "valid analyze keep_vocals", base: anKeep},
		{name: "valid plan three seeds", base: plan},
		{name: "valid render attach", base: att},
		{name: "valid kind absent is render", base: render, mutate: func(m map[string]any) { delete(m, "kind") }},
		{name: "valid fallback pair", base: render, mutate: func(m map[string]any) { fbTarget(m, "audio.flac"); m["fallback_after_s"] = 60 }},
		{name: "valid min_audio_s on render", base: render, mutate: func(m map[string]any) { m["min_audio_s"] = 200 }},
		{name: "valid plan sixteen seeds", base: specV2(KindPlan, nil, &PlanSpec{Seeds: seeds(16), SecondsPerSeed: 360})},
		{name: "valid attach exactly 1 MiB", base: render, parserOnly: true, mutate: func(m map[string]any) {
			m["attach"] = map[string]any{"a.bin": map[string]any{"sha256": testSHA, "data_base64": base64.StdEncoding.EncodeToString(make([]byte, MaxAttachBytes))}}
		}},
		{name: "unknown top-level key", base: render, wantErr: true, mutate: func(m map[string]any) { m["extra"] = 1 }},
		{name: "unknown key in ears", base: ears, wantErr: true, mutate: func(m map[string]any) { earsOf(m)["x"] = 1 }},
		{name: "unknown key in plan", base: plan, wantErr: true, mutate: func(m map[string]any) { planOf(m)["x"] = 1 }},
		{name: "unknown key in attach file", base: att, wantErr: true, mutate: func(m map[string]any) { attOf(m)["x"] = 1 }},
		{name: "unknown key in inputs", base: an, wantErr: true, mutate: func(m map[string]any) { m["inputs"].(map[string]any)["ref"] = srcOf(m) }},
		{name: "unknown key in an upload target", base: render, wantErr: true, mutate: func(m map[string]any) { uploadsOf(m)["audio.flac"].(map[string]any)["x"] = 1 }},
		{name: "unknown kind", base: render, wantErr: true, mutate: func(m map[string]any) { m["kind"] = "mix" }},
		{name: "kind empty", base: render, wantErr: true, mutate: func(m map[string]any) { m["kind"] = "" }},
		{name: "kind null", base: render, wantErr: true, mutate: func(m map[string]any) { m["kind"] = nil }},
		{name: "analyze without inputs", base: an, wantErr: true, mutate: func(m map[string]any) { delete(m, "inputs") }},
		{name: "analyze with request", base: an, wantErr: true, mutate: func(m map[string]any) { m["request"] = map[string]any{"a": 1} }},
		{name: "analyze without ears", base: an, wantErr: true, mutate: func(m map[string]any) { delete(m, "ears") }},
		{name: "analyze with plan", base: an, wantErr: true, mutate: func(m map[string]any) { m["plan"] = map[string]any{"seeds": []int{1}, "s_per_seed": 5} }},
		{name: "analyze with min_audio_s", base: an, wantErr: true, mutate: func(m map[string]any) { m["min_audio_s"] = 10 }},
		{name: "analyze inputs reference_audio", base: an, wantErr: true, mutate: func(m map[string]any) {
			m["inputs"].(map[string]any)["reference_audio"] = srcOf(m)
		}},
		{name: "analyze src sha256 short", base: an, wantErr: true, mutate: func(m map[string]any) { srcOf(m)["sha256"] = "abc" }},
		{name: "analyze src bytes zero", base: an, wantErr: true, mutate: func(m map[string]any) { srcOf(m)["bytes"] = 0 }},
		{name: "analyze src url bad", base: an, wantErr: true, mutate: func(m map[string]any) { srcOf(m)["url"] = "nope" }},
		{name: "analyze lists audio.flac", base: an, wantErr: true, mutate: func(m map[string]any) { uploadsOf(m)["audio.flac"] = tgt("audio.flac") }},
		{name: "analyze without analysis.json target", base: an, wantErr: true, mutate: func(m map[string]any) { delete(uploadsOf(m), "analysis.json") }},
		{name: "analyze stem target without keep_vocals", base: an, wantErr: true, mutate: func(m map[string]any) { uploadsOf(m)["stem-vocals.flac"] = tgt("stem-vocals.flac") }},
		{name: "analyze keep_vocals without stem target", base: anKeep, wantErr: true, mutate: func(m map[string]any) { delete(uploadsOf(m), "stem-vocals.flac") }},
		{name: "plan without plan block", base: plan, wantErr: true, mutate: func(m map[string]any) { delete(m, "plan") }},
		{name: "plan without request", base: plan, wantErr: true, mutate: func(m map[string]any) { delete(m, "request") }},
		{name: "plan with empty seeds", base: plan, wantErr: true, mutate: func(m map[string]any) { planOf(m)["seeds"] = []int{} }},
		{name: "plan with seventeen seeds", base: plan, wantErr: true, mutate: func(m map[string]any) { planOf(m)["seeds"] = seeds(17) }},
		{name: "plan with repeated seeds", base: plan, wantErr: true, mutate: func(m map[string]any) { planOf(m)["seeds"] = []int{5, 5, 6} }},
		{name: "plan s_per_seed zero", base: plan, wantErr: true, mutate: func(m map[string]any) { planOf(m)["s_per_seed"] = 0 }},
		{name: "plan s_per_seed 361", base: plan, wantErr: true, mutate: func(m map[string]any) { planOf(m)["s_per_seed"] = 361 }},
		{name: "plan with ears", base: plan, wantErr: true, mutate: func(m map[string]any) {
			m["ears"] = map[string]any{"budget_s": 5, "stages": []string{"asr"}}
		}},
		{name: "plan with inputs", base: plan, wantErr: true, mutate: func(m map[string]any) { m["inputs"] = map[string]any{"src_audio": tgt("x")} }},
		{name: "plan with attach", base: plan, wantErr: true, mutate: func(m map[string]any) { m["attach"] = att.Attach }},
		{name: "plan missing a bundle target", base: plan, wantErr: true, mutate: func(m map[string]any) { delete(uploadsOf(m), "plan-02.tar") }},
		{name: "plan extra bundle target", base: plan, wantErr: true, mutate: func(m map[string]any) { uploadsOf(m)["plan-03.tar"] = tgt("plan-03.tar") }},
		{name: "plan lists audio.flac", base: plan, wantErr: true, mutate: func(m map[string]any) { uploadsOf(m)["audio.flac"] = tgt("audio.flac") }},
		{name: "plan bundle over 2 MiB", base: plan, wantErr: true, mutate: func(m map[string]any) {
			uploadsOf(m)["plan-00.tar"].(map[string]any)["max_bytes"] = MaxPlanBundleBytes + 1
		}},
		{name: "ears on render without analysis.json target", base: ears, wantErr: true, mutate: func(m map[string]any) { delete(uploadsOf(m), "analysis.json") }},
		{name: "render without ears lists analysis.json", base: render, wantErr: true, mutate: func(m map[string]any) { uploadsOf(m)["analysis.json"] = tgt("analysis.json") }},
		{name: "render lists a plan bundle", base: render, wantErr: true, mutate: func(m map[string]any) { uploadsOf(m)["plan-00.tar"] = tgt("plan-00.tar") }},
		{name: "keep_vocals without stem target", base: keep, wantErr: true, mutate: func(m map[string]any) { delete(uploadsOf(m), "stem-vocals.flac") }},
		{name: "stem target without keep_vocals", base: ears, wantErr: true, mutate: func(m map[string]any) { uploadsOf(m)["stem-vocals.flac"] = tgt("stem-vocals.flac") }},
		{name: "ears stage unknown", base: ears, wantErr: true, mutate: func(m map[string]any) { earsOf(m)["stages"] = []string{"mix"} }},
		{name: "ears stages repeat", base: ears, wantErr: true, mutate: func(m map[string]any) { earsOf(m)["stages"] = []string{"asr", "asr"} }},
		{name: "ears stages empty", base: ears, wantErr: true, mutate: func(m map[string]any) { earsOf(m)["stages"] = []string{} }},
		{name: "ears stages absent", base: ears, wantErr: true, mutate: func(m map[string]any) { delete(earsOf(m), "stages") }},
		{name: "ears budget zero", base: ears, wantErr: true, mutate: func(m map[string]any) { earsOf(m)["budget_s"] = 0 }},
		{name: "ears budget absent", base: ears, wantErr: true, mutate: func(m map[string]any) { delete(earsOf(m), "budget_s") }},
		{name: "ears lang uppercase", base: ears, wantErr: true, mutate: func(m map[string]any) { earsOf(m)["lang"] = "IT" }},
		{name: "ears lang empty", base: ears, wantErr: true, mutate: func(m map[string]any) { earsOf(m)["lang"] = "" }},
		{name: "ears keep_vocals not boolean", base: ears, wantErr: true, mutate: func(m map[string]any) { earsOf(m)["keep_vocals"] = "yes" }},
		{name: "ears null", base: ears, wantErr: true, mutate: func(m map[string]any) { m["ears"] = nil }},
		{name: "ears lang null", base: ears, wantErr: true, mutate: func(m map[string]any) { earsOf(m)["lang"] = nil }},
		{name: "inputs on a yue2 render", base: render, wantErr: true, mutate: func(m map[string]any) {
			m["inputs"] = map[string]any{"src_audio": map[string]any{"url": "https://h/x", "sha256": testSHA, "bytes": 5}}
		}},
		{name: "render without request", base: render, wantErr: true, mutate: func(m map[string]any) { delete(m, "request") }},
		{name: "render with plan block", base: render, wantErr: true, mutate: func(m map[string]any) { m["plan"] = map[string]any{"seeds": []int{1}, "s_per_seed": 5} }},
		{name: "attach sha256 short", base: att, wantErr: true, mutate: func(m map[string]any) { attOf(m)["sha256"] = "abc" }},
		{name: "attach not base64", base: att, wantErr: true, mutate: func(m map[string]any) { attOf(m)["data_base64"] = "%%%" }},
		{name: "attach line break in base64", base: att, wantErr: true, mutate: func(m map[string]any) { attOf(m)["data_base64"] = "AAAA\nAAAA" }},
		{name: "attach name with slash", base: att, wantErr: true, mutate: func(m map[string]any) {
			m["attach"] = map[string]any{"../x": attOf(m)}
		}},
		{name: "attach name leading dot", base: att, wantErr: true, mutate: func(m map[string]any) { m["attach"] = map[string]any{".x": attOf(m)} }},
		{name: "attach seventeen files", base: att, wantErr: true, mutate: func(m map[string]any) {
			files := map[string]any{}
			for i := range 17 {
				files["f"+string(rune('a'+i))] = attOf(m)
			}
			m["attach"] = files
		}},
		{name: "attach over 1 MiB decoded", base: att, wantErr: true, parserOnly: true, mutate: func(m map[string]any) {
			m["attach"] = map[string]any{"a.bin": map[string]any{"sha256": testSHA, "data_base64": base64.StdEncoding.EncodeToString(make([]byte, MaxAttachBytes+1))}}
		}},
		{name: "fallback without fallback_after_s", base: render, wantErr: true, mutate: func(m map[string]any) { fbTarget(m, "audio.flac") }},
		{name: "fallback_after_s without a fallback", base: render, wantErr: true, mutate: func(m map[string]any) { m["fallback_after_s"] = 120 }},
		{name: "fallback_after_s 59", base: render, wantErr: true, mutate: func(m map[string]any) { fbTarget(m, "audio.flac"); m["fallback_after_s"] = 59 }},
		{name: "fallback_after_s 601", base: render, wantErr: true, mutate: func(m map[string]any) { fbTarget(m, "audio.flac"); m["fallback_after_s"] = 601 }},
		{name: "fallback_after_s zero", base: render, wantErr: true, mutate: func(m map[string]any) { m["fallback_after_s"] = 0 }},
		{name: "fallback url bad", base: render, wantErr: true, mutate: func(m map[string]any) {
			uploadsOf(m)["audio.flac"].(map[string]any)["fallback"] = map[string]any{"url": "nope"}
			m["fallback_after_s"] = 60
		}},
		{name: "engine acestep15", base: render, wantErr: true, mutate: func(m map[string]any) { m["engine"] = "acestep15" }},
		{name: "contract absent", base: render, wantErr: true, mutate: func(m map[string]any) { delete(m, "contract") }},
		{name: "take_id malformed", base: render, wantErr: true, mutate: func(m map[string]any) { m["take_id"] = "a b" }},
		{name: "timeout 2701", base: render, wantErr: true, mutate: func(m map[string]any) { m["timeout_s"] = 2701 }},
		{name: "min_audio_s 361", base: render, wantErr: true, mutate: func(m map[string]any) { m["min_audio_s"] = 361 }},
		{name: "min_audio_s null", base: render, wantErr: true, mutate: func(m map[string]any) { m["min_audio_s"] = nil }},
		{name: "start url bad", base: render, wantErr: true, mutate: func(m map[string]any) { m["start"] = map[string]any{"url": "h/x"} }},
		{name: "weights empty", base: render, wantErr: true, mutate: func(m map[string]any) { m["expect"] = map[string]any{"weights": map[string]any{}} }},
		{name: "weights short revision", base: render, wantErr: true, mutate: func(m map[string]any) {
			m["expect"].(map[string]any)["weights"].(map[string]any)["m-a-p/YuE2-3B"] = "abc"
		}},
		{name: "uploads without mm-result.json", base: render, wantErr: true, mutate: func(m map[string]any) { delete(uploadsOf(m), "mm-result.json") }},
		{name: "uploads without audio.flac", base: render, wantErr: true, mutate: func(m map[string]any) { delete(uploadsOf(m), "audio.flac") }},
		{name: "upload max_bytes over limit", base: render, wantErr: true, mutate: func(m map[string]any) { uploadsOf(m)["mm-result.json"].(map[string]any)["max_bytes"] = 64*1024 + 1 }},
		{name: "upload not allowlisted", base: render, wantErr: true, mutate: func(m map[string]any) { uploadsOf(m)["latent.npy"] = tgt("latent.npy") }},
		{name: "request body over 2 MiB", base: render, wantErr: true, parserOnly: true, mutate: func(m map[string]any) {
			m["request"] = map[string]any{"lyrics": strings.Repeat("a", MaxRunBody)}
		}},
	}
}

func seeds(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(1000 + i)
	}
	return out
}

func (r v2Row) doc(t *testing.T) []byte {
	m := buildV2(t, r.base)
	if r.mutate != nil {
		r.mutate(m)
	}
	return marshal(t, m)
}

func TestParseWorkerInput_V2_Table(t *testing.T) {
	t.Parallel()
	rows := v2Rows()
	if len(rows) < 25 {
		t.Fatalf("only %d rows", len(rows))
	}
	for _, r := range rows {
		in, err := ParseWorkerInput(r.doc(t))
		switch {
		case r.wantErr && err == nil:
			t.Errorf("%s: accepted", r.name)
		case r.wantErr && !errors.Is(err, ErrWorkerInput):
			t.Errorf("%s: error does not wrap ErrWorkerInput: %v", r.name, err)
		case !r.wantErr && err != nil:
			t.Errorf("%s: refused: %v", r.name, err)
		case !r.wantErr && in.Kind == "":
			t.Errorf("%s: the parsed kind is empty", r.name)
		}
	}
}

func TestWorkerSchemasV2_MatchParser(t *testing.T) {
	t.Parallel()
	rs, _ := loadSchema(t, "mm-worker-2.input.schema.json")
	for _, r := range v2Rows() {
		raw := r.doc(t)
		var inst any
		if err := json.Unmarshal(raw, &inst); err != nil {
			t.Fatal(err)
		}
		schemaOK := rs.Validate(inst) == nil
		_, perr := ParseWorkerInput(raw)
		if r.parserOnly {
			if !schemaOK {
				t.Errorf("%s: a parser-only limit must be schema-valid", r.name)
			}
			continue
		}
		if schemaOK != (perr == nil) {
			t.Errorf("%s: schema valid=%v, ParseWorkerInput err=%v", r.name, schemaOK, perr)
		}
		if schemaOK == r.wantErr {
			t.Errorf("%s: schema valid=%v, want error=%v", r.name, schemaOK, r.wantErr)
		}
	}
	// a /1 document against the /2 schema is refused, and a /2 document is refused by the /1 schema
	rs1, _ := loadSchema(t, "mm-worker-1.input.schema.json")
	var v2 any
	_ = json.Unmarshal(marshal(t, buildV2(t, specV2(KindRender, nil, nil))), &v2)
	if rs1.Validate(v2) == nil {
		t.Error("the /1 schema accepts a /2 document")
	}
	var v1 any
	_ = json.Unmarshal(marshal(t, exampleInput()), &v1)
	if rs.Validate(v1) == nil {
		t.Error("the /2 schema accepts a /1 document")
	}
}

// TestParseWorkerInput_V1RefusesV2Keys: every /2 key is refused in a /1 document, and the /2 schema/parser are not reachable
// through a /1 contract string.
func TestParseWorkerInput_V1RefusesV2Keys(t *testing.T) {
	t.Parallel()
	for k, v := range map[string]any{
		"kind": "render", "ears": map[string]any{"budget_s": 5, "stages": []string{"asr"}}, "plan": map[string]any{},
		"attach": map[string]any{}, "fallback_after_s": 60,
	} {
		doc := exampleInput()
		doc[k] = v
		if _, err := ParseWorkerInput(marshal(t, doc)); !errors.Is(err, ErrWorkerInput) {
			t.Errorf("a /1 document with %q: %v", k, err)
		}
	}
}

func TestWireFor_Table(t *testing.T) {
	t.Parallel()
	plain := TakeSpec{Engine: EngineYuE2}
	ace := TakeSpec{Engine: EngineACEStep}
	tests := []struct {
		name         string
		spec         TakeSpec
		needs        []string
		fallbackOpen bool
		want         string
	}{
		{"plain yue2", plain, nil, false, ContractV1},
		{"empty needs", plain, []string{}, false, ContractV1},
		{"empty engine is yue2", TakeSpec{}, nil, false, ContractV1},
		{"needs v2", plain, []string{"v2"}, false, ContractV2},
		{"needs ears", plain, []string{"ears", "v2"}, false, ContractV2},
		{"fallback open", plain, nil, true, ContractV2},
		{"acestep stays v1", ace, nil, false, ContractV1},
		{"acestep with needs stays v1", ace, []string{"v2"}, false, ContractV1},
		{"acestep with fallback open stays v1", ace, nil, true, ContractV1},
		{"analyze kind without needs", TakeSpec{Engine: EngineYuE2, Kind: KindAnalyze}, nil, false, ContractV2},
		{"render kind alone is plain", TakeSpec{Engine: EngineYuE2, Kind: KindRender}, nil, false, ContractV1},
		{"ears spec without needs", TakeSpec{Engine: EngineYuE2, Ears: earsAll}, nil, false, ContractV2},
		{"plan spec without needs", TakeSpec{Engine: EngineYuE2, Plan: planThree}, nil, false, ContractV2},
		{"attach without needs", TakeSpec{Engine: EngineYuE2, Attach: attachFile}, nil, false, ContractV2},
	}
	for _, tc := range tests {
		if got := WireFor(tc.spec, tc.needs, tc.fallbackOpen); got != tc.want {
			t.Errorf("%s: WireFor = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ---- outputs ----

func v2Output(kind string) map[string]any {
	m := exampleOutput()
	m["contract"] = ContractV2
	m["kind"] = kind
	return m
}

func TestDecodeOutput_V1RejectsV2Keys(t *testing.T) {
	t.Parallel()
	for k, v := range map[string]any{
		"kind": "render", "ears": map[string]any{"status": "ok"}, "plans": []any{}, "caps": []string{"v2"},
	} {
		o := exampleOutput()
		o[k] = v
		if _, err := DecodeOutput(marshal(t, o)); !errors.Is(err, ErrWorkerInput) {
			t.Errorf("a /1 output with %q: %v", k, err)
		}
	}
	if _, err := DecodeOutput(marshal(t, exampleOutput())); err != nil {
		t.Errorf("the plain /1 output: %v", err)
	}
}

type outRow struct {
	name    string
	kind    string
	mutate  func(m map[string]any)
	wantErr bool
}

func outRows() []outRow {
	okPlans := []any{
		map[string]any{"index": 0, "seed": 1101, "status": "ok", "bundle": "plan-00.tar", "truncated": map[string]any{"abc": false, "semantic": false}},
		map[string]any{"index": 1, "seed": 1102, "status": "error", "code": "PIPELINE_ERROR"},
	}
	planOut := func(m map[string]any) {
		m["audio_seconds"], m["truncated"], m["plans"] = nil, nil, okPlans
		m["artifacts"] = map[string]any{"plan-00.tar": map[string]any{"sha256": testSHA, "bytes": 5}}
	}
	analyzeOut := func(m map[string]any) {
		m["truncated"] = nil
		m["ears"] = map[string]any{"status": "ok", "seconds": 12.5}
		m["artifacts"] = map[string]any{"analysis.json": map[string]any{"sha256": testSHA, "bytes": 5}}
	}
	return []outRow{
		{name: "render", kind: KindRender},
		{name: "render with ears ok and caps", kind: KindRender, mutate: func(m map[string]any) {
			m["ears"] = map[string]any{"status": "ok", "seconds": 9.25}
			m["caps"] = []string{"v2", "ears"}
		}},
		{name: "render with ears skipped", kind: KindRender, mutate: func(m map[string]any) {
			m["ears"] = map[string]any{"status": "skipped", "code": "DEADLINE"}
		}},
		{name: "analyze", kind: KindAnalyze, mutate: analyzeOut},
		{name: "plan", kind: KindPlan, mutate: planOut},
		{name: "plan error output", kind: KindPlan, mutate: func(m map[string]any) {
			planOut(m)
			m["status"] = "error"
			m["error"] = map[string]any{"code": "PLANS_FAILED", "message": "every seed failed"}
			m["plans"] = []any{map[string]any{"index": 0, "seed": 1, "status": "error", "code": "PIPELINE_ERROR"}}
		}},
		{name: "analyze error output", kind: KindAnalyze, mutate: func(m map[string]any) {
			m["status"], m["audio_seconds"], m["truncated"] = "error", nil, nil
			m["error"] = map[string]any{"code": "ANALYZE_FAILED", "message": "no vocals"}
			m["ears"] = map[string]any{"status": "error", "code": "ASR_FAILED"}
		}},
		{name: "unavailable kind error output", kind: KindRender, mutate: func(m map[string]any) {
			m["status"], m["audio_seconds"], m["truncated"] = "error", nil, nil
			m["error"] = map[string]any{"code": "KIND_UNAVAILABLE", "message": "no analyze stage"}
		}},
		{name: "kind absent", kind: KindRender, wantErr: true, mutate: func(m map[string]any) { delete(m, "kind") }},
		{name: "kind unknown", kind: KindRender, wantErr: true, mutate: func(m map[string]any) { m["kind"] = "mix" }},
		{name: "unknown top-level key", kind: KindRender, wantErr: true, mutate: func(m map[string]any) { m["extra"] = 1 }},
		{name: "caps unknown", kind: KindRender, wantErr: true, mutate: func(m map[string]any) { m["caps"] = []string{"warp"} }},
		{name: "caps repeat", kind: KindRender, wantErr: true, mutate: func(m map[string]any) { m["caps"] = []string{"v2", "v2"} }},
		{name: "ears null", kind: KindRender, wantErr: true, mutate: func(m map[string]any) { m["ears"] = nil }},
		{name: "ears status unknown", kind: KindRender, wantErr: true, mutate: func(m map[string]any) { m["ears"] = map[string]any{"status": "maybe"} }},
		{name: "ears extra key", kind: KindRender, wantErr: true, mutate: func(m map[string]any) { m["ears"] = map[string]any{"status": "ok", "x": 1} }},
		{name: "render with plans", kind: KindRender, wantErr: true, mutate: func(m map[string]any) { m["plans"] = okPlans }},
		{name: "ok render without audio_seconds", kind: KindRender, wantErr: true, mutate: func(m map[string]any) { m["audio_seconds"] = nil }},
		{name: "ok render with null truncated", kind: KindRender, wantErr: true, mutate: func(m map[string]any) { m["truncated"] = nil }},
		{name: "analyze with truncated dict", kind: KindAnalyze, wantErr: true, mutate: func(m map[string]any) {
			analyzeOut(m)
			m["truncated"] = map[string]any{"abc": false, "semantic": false}
		}},
		{name: "analyze with ears error but ok status", kind: KindAnalyze, wantErr: true, mutate: func(m map[string]any) {
			analyzeOut(m)
			m["ears"] = map[string]any{"status": "error", "code": "ASR_FAILED"}
		}},
		{name: "analyze without ears", kind: KindAnalyze, wantErr: true, mutate: func(m map[string]any) { analyzeOut(m); delete(m, "ears") }},
		{name: "analyze with plans", kind: KindAnalyze, wantErr: true, mutate: func(m map[string]any) { analyzeOut(m); m["plans"] = okPlans }},
		{name: "plan with audio_seconds", kind: KindPlan, wantErr: true, mutate: func(m map[string]any) { planOut(m); m["audio_seconds"] = 5.5 }},
		{name: "plan with truncated", kind: KindPlan, wantErr: true, mutate: func(m map[string]any) { planOut(m); m["truncated"] = map[string]any{"abc": false, "semantic": false} }},
		{name: "plan with ears", kind: KindPlan, wantErr: true, mutate: func(m map[string]any) { planOut(m); m["ears"] = map[string]any{"status": "ok"} }},
		{name: "ok plan without an ok seed", kind: KindPlan, wantErr: true, mutate: func(m map[string]any) {
			planOut(m)
			m["plans"] = []any{map[string]any{"index": 0, "seed": 1, "status": "error", "code": "PIPELINE_ERROR"}}
		}},
		{name: "plan index 16", kind: KindPlan, wantErr: true, mutate: func(m map[string]any) {
			planOut(m)
			m["plans"] = append(okPlans[:1:1], map[string]any{"index": 16, "seed": 2, "status": "error", "code": "X"})
		}},
		{name: "ok plan entry without bundle", kind: KindPlan, wantErr: true, mutate: func(m map[string]any) {
			planOut(m)
			m["plans"] = []any{map[string]any{"index": 0, "seed": 1, "status": "ok"}}
		}},
		{name: "error plan entry with bundle", kind: KindPlan, wantErr: true, mutate: func(m map[string]any) {
			planOut(m)
			m["plans"] = append(okPlans[:1:1], map[string]any{"index": 1, "seed": 2, "status": "error", "code": "X", "bundle": "plan-01.tar"})
		}},
		{name: "error plan entry without code", kind: KindPlan, wantErr: true, mutate: func(m map[string]any) {
			planOut(m)
			m["plans"] = append(okPlans[:1:1], map[string]any{"index": 1, "seed": 2, "status": "error"})
		}},
		{name: "plan entry status unknown", kind: KindPlan, wantErr: true, mutate: func(m map[string]any) {
			planOut(m)
			m["plans"] = []any{map[string]any{"index": 0, "seed": 1, "status": "skipped", "code": "X"}}
		}},
	}
}

func TestDecodeOutput_V2Kinds(t *testing.T) {
	t.Parallel()
	for _, r := range outRows() {
		m := v2Output(r.kind)
		if r.mutate != nil {
			r.mutate(m)
		}
		o, err := DecodeOutput(marshal(t, m))
		switch {
		case r.wantErr && !errors.Is(err, ErrWorkerInput):
			t.Errorf("%s: err = %v, want ErrWorkerInput", r.name, err)
		case !r.wantErr && err != nil:
			t.Errorf("%s: %v", r.name, err)
		case !r.wantErr && o.Kind != r.kind:
			t.Errorf("%s: kind %q", r.name, o.Kind)
		}
	}
	// the decoded blocks carry their values
	m := v2Output(KindRender)
	m["ears"] = map[string]any{"status": "ok", "seconds": 9.25}
	o, _ := DecodeOutput(marshal(t, m))
	if o.Ears == nil || o.Ears.Seconds == nil || o.Ears.Seconds.String() != "9.25" {
		t.Errorf("ears.seconds: %+v", o.Ears)
	}
}

func TestWorkerSchemasV2_OutputMatchesDecoder(t *testing.T) {
	t.Parallel()
	rs, _ := loadSchema(t, "mm-worker-2.output.schema.json")
	for _, r := range outRows() {
		m := v2Output(r.kind)
		if r.mutate != nil {
			r.mutate(m)
		}
		raw := marshal(t, m)
		var inst any
		_ = json.Unmarshal(raw, &inst)
		schemaOK := rs.Validate(inst) == nil
		_, derr := DecodeOutput(raw)
		if schemaOK != (derr == nil) || schemaOK == r.wantErr {
			t.Errorf("%s: schema valid=%v, DecodeOutput err=%v, want error=%v", r.name, schemaOK, derr, r.wantErr)
		}
	}
	// a /1 output is not a /2 output
	var v1 any
	_ = json.Unmarshal(marshal(t, exampleOutput()), &v1)
	if rs.Validate(v1) == nil {
		t.Error("the /2 output schema accepts a /1 output")
	}
}

func TestCheckOutputV2(t *testing.T) {
	t.Parallel()
	decode := func(kind string, mutate func(map[string]any)) Output {
		m := v2Output(kind)
		if mutate != nil {
			mutate(m)
		}
		o, err := DecodeOutput(marshal(t, m))
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	plainRender := decode(KindRender, nil)
	earsRender := decode(KindRender, func(m map[string]any) { m["ears"] = map[string]any{"status": "ok"} })
	plan := decode(KindPlan, outRows()[4].mutate)
	analyze := decode(KindAnalyze, outRows()[3].mutate)
	tests := []struct {
		name    string
		shape   KindShape
		out     Output
		wantErr bool
	}{
		{"plain render", KindShape{Kind: KindRender}, plainRender, false},
		{"empty kind is render", KindShape{}, plainRender, false},
		{"ears asked, none reported", KindShape{Kind: KindRender, Ears: true}, plainRender, true},
		{"ears reported, none asked", KindShape{Kind: KindRender}, earsRender, true},
		{"ears asked and reported", KindShape{Kind: KindRender, Ears: true}, earsRender, false},
		{"kind mismatch", KindShape{Kind: KindAnalyze, Ears: true}, plainRender, true},
		{"analyze", KindShape{Kind: KindAnalyze, Ears: true}, analyze, false},
		{"plan with two seeds", KindShape{Kind: KindPlan, Seeds: 2}, plan, false},
		{"plan count mismatch", KindShape{Kind: KindPlan, Seeds: 3}, plan, true},
		{"v1 contract", KindShape{Kind: KindRender}, func() Output { o := plainRender; o.Contract = ContractV1; return o }(), true},
		{"error output only needs the kind", KindShape{Kind: KindRender, Ears: true}, func() Output { o := plainRender; o.Status = "error"; return o }(), false},
		{"plan index out of order", KindShape{Kind: KindPlan, Seeds: 2}, func() Output {
			o := plan
			o.Plans = []PlanOut{plan.Plans[1], plan.Plans[0]}
			return o
		}(), true},
		{"plan with no ok seed", KindShape{Kind: KindPlan, Seeds: 1}, func() Output {
			o := plan
			o.Plans = []PlanOut{{Index: 0, Seed: 1, Status: "error", Code: "X"}}
			return o
		}(), true},
		{"analyze with a failed stage", KindShape{Kind: KindAnalyze, Ears: true}, func() Output {
			o := analyze
			o.Ears = &EarsOut{Status: "error"}
			return o
		}(), true},
		{"analyze without audio_seconds", KindShape{Kind: KindAnalyze, Ears: true}, func() Output { o := analyze; o.AudioSeconds = nil; return o }(), true},
		{"render without audio_seconds", KindShape{Kind: KindRender}, func() Output { o := plainRender; o.AudioSeconds = nil; return o }(), true},
	}
	for _, tc := range tests {
		err := CheckOutputV2(tc.shape, tc.out)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, want error %v", tc.name, err, tc.wantErr)
		}
	}
}

// ---- the artifact table ----

var v2Shapes = []struct {
	Name  string
	Shape KindShape
}{
	{"render", KindShape{Kind: KindRender}},
	{"render-ears", KindShape{Kind: KindRender, Ears: true}},
	{"render-ears-keep", KindShape{Kind: KindRender, Ears: true, KeepVocals: true}},
	{"analyze", KindShape{Kind: KindAnalyze, Ears: true}},
	{"analyze-keep", KindShape{Kind: KindAnalyze, Ears: true, KeepVocals: true}},
	{"plan-1", KindShape{Kind: KindPlan, Seeds: 1}},
	{"plan-16", KindShape{Kind: KindPlan, Seeds: 16}},
}

// shapeDoc builds a valid document of the shape, with the targets of its allowlist.
func shapeDoc(t *testing.T, s KindShape) map[string]any {
	var ears *EarsSpec
	if s.Ears {
		ears = &EarsSpec{BudgetS: 10, Stages: []string{"asr"}, KeepVocals: s.KeepVocals}
	}
	var plan *PlanSpec
	if s.Seeds > 0 {
		plan = &PlanSpec{Seeds: seeds(s.Seeds), SecondsPerSeed: 10}
	}
	return buildV2(t, specV2(s.Kind, ears, plan))
}

// TestArtifacts_V2SchemaEnum: for every shape the Go table and the /2 input schema accept the same upload sets: every name the
// table lists is accepted at its maximum and refused above it, a name outside the table is refused, a missing mandatory target
// is refused. The output schema lists exactly the union of the names the table reports.
func TestArtifacts_V2SchemaEnum(t *testing.T) {
	t.Parallel()
	rs, in := loadSchema(t, "mm-worker-2.input.schema.json")
	props := in["properties"].(map[string]any)["uploads"].(map[string]any)["properties"].(map[string]any)
	union := map[string]int64{}
	valid := func(m map[string]any) bool {
		var inst any
		_ = json.Unmarshal(marshal(t, m), &inst)
		return rs.Validate(inst) == nil
	}
	for _, sh := range v2Shapes {
		table := ArtifactsV2(sh.Shape)
		listed := map[string]Artifact{}
		for _, a := range table {
			listed[a.Name] = a
			union[a.Name] = a.MaxBytes
		}
		if got := table[len(table)-1].Name; got != ResultArtifact {
			t.Errorf("%s: the last artifact is %s, want %s", sh.Name, got, ResultArtifact)
		}
		if !valid(shapeDoc(t, sh.Shape)) {
			t.Errorf("%s: the table's own document is refused by the schema", sh.Name)
		}
		for _, a := range table {
			over := shapeDoc(t, sh.Shape)
			uploadsOf(over)[a.Name].(map[string]any)["max_bytes"] = a.MaxBytes + 1
			if valid(over) {
				t.Errorf("%s: %s above its maximum is accepted", sh.Name, a.Name)
			}
			if a.Required {
				gone := shapeDoc(t, sh.Shape)
				delete(uploadsOf(gone), a.Name)
				if valid(gone) {
					t.Errorf("%s: %s is required by the table, the schema accepts it missing", sh.Name, a.Name)
				}
			}
		}
		for name := range props { // every other name the schema knows is refused for this shape, or optional in the table
			if _, ok := listed[name]; ok {
				continue
			}
			extra := shapeDoc(t, sh.Shape)
			uploadsOf(extra)[name] = tgt(name)
			if valid(extra) {
				t.Errorf("%s: schema accepts %s, which is not in the table", sh.Name, name)
			}
		}
	}
	// the schema's property list is the union of the tables plus the /1 optional names
	for name, limit := range union {
		p, ok := props[name].(map[string]any)
		if !ok {
			t.Errorf("%s missing from the schema", name)
			continue
		}
		if got := int64(p["properties"].(map[string]any)["max_bytes"].(map[string]any)["maximum"].(float64)); got != limit {
			t.Errorf("%s: schema maximum %d, table %d", name, got, limit)
		}
	}
	for name := range props {
		if _, ok := union[name]; !ok {
			if _, v1 := ArtifactMaxBytesFor(EngineYuE2, name); !v1 {
				t.Errorf("schema lists %s, which no table lists", name)
			}
		}
		if limit, ok := UploadLimitV2(name); ok && union[name] != limit {
			t.Errorf("%s: UploadLimitV2 %d, table %d", name, limit, union[name])
		}
	}
	// outputs: every table name except mm-result.json is reportable, nothing else
	_, out := loadSchema(t, "mm-worker-2.output.schema.json")
	oprops := out["properties"].(map[string]any)["artifacts"].(map[string]any)["properties"].(map[string]any)
	for name := range union {
		if _, ok := oprops[name]; !ok && name != ResultArtifact {
			t.Errorf("the output schema cannot report %s", name)
		}
	}
	for name := range oprops {
		if _, ok := union[name]; !ok {
			if _, v1 := ArtifactMaxBytesFor(EngineYuE2, name); !v1 {
				t.Errorf("the output schema reports %s, which no table lists", name)
			}
		}
	}
}

func TestArtifactsV2_Shapes(t *testing.T) {
	t.Parallel()
	names := func(s KindShape) []string {
		var n []string
		for _, a := range ArtifactsV2(s) {
			n = append(n, a.Name)
		}
		return n
	}
	eq := func(got []string, want ...string) bool { return strings.Join(got, ",") == strings.Join(want, ",") }
	if got := names(KindShape{Kind: KindAnalyze, Ears: true}); !eq(got, "analysis.json", "mm-result.json") {
		t.Errorf("analyze: %v", got)
	}
	if got := names(KindShape{Kind: KindAnalyze, Ears: true, KeepVocals: true}); !eq(got, "analysis.json", "stem-vocals.flac", "mm-result.json") {
		t.Errorf("analyze keep: %v", got)
	}
	if got := names(KindShape{Kind: KindPlan, Seeds: 2}); !eq(got, "plan-00.tar", "plan-01.tar", "mm-result.json") {
		t.Errorf("plan: %v", got)
	}
	if got := names(KindShape{Kind: KindPlan, Seeds: 99}); len(got) != MaxPlanSeeds+1 {
		t.Errorf("plan seeds are capped at %d: %d names", MaxPlanSeeds, len(got))
	}
	if got := names(KindShape{Kind: "mix"}); got != nil {
		t.Errorf("unknown kind: %v", got)
	}
	// a render keeps the /1 set exactly, with ears it adds the two stage names before mm-result.json
	v1 := names(KindShape{})
	if len(v1) != len(Artifacts) {
		t.Errorf("plain render has %d artifacts, /1 has %d", len(v1), len(Artifacts))
	}
	for i, a := range Artifacts {
		if v1[i] != a.Name {
			t.Errorf("render artifact %d is %s, /1 has %s", i, v1[i], a.Name)
		}
	}
	if _, ok := ArtifactMaxBytesFor(EngineYuE2, AnalysisArtifact); ok {
		t.Error("the /1 allowlist gained analysis.json")
	}
	if _, ok := ArtifactMaxBytesV2(KindShape{Kind: KindPlan, Seeds: 1}, "plan-01.tar"); ok {
		t.Error("a one-seed plan allows plan-01.tar")
	}
	if _, ok := UploadLimitV2("plan-16.tar"); ok {
		t.Error("plan-16.tar is allowed")
	}
	if _, ok := UploadLimitV2("audio.flac"); ok {
		t.Error("UploadLimitV2 answers for a /1 name")
	}
	if got := ShapeOf(TakeSpec{Ears: earsKeep, Plan: planThree}); got != (KindShape{Kind: KindRender, Ears: true, KeepVocals: true, Seeds: 3}) {
		t.Errorf("ShapeOf = %+v", got)
	}
}

func TestExtraRequiredV2(t *testing.T) {
	t.Parallel()
	ok := func(s string) *EarsOut { return &EarsOut{Status: s} }
	tests := []struct {
		name  string
		shape KindShape
		out   Output
		want  string
	}{
		{"render without ears", KindShape{Kind: KindRender}, Output{}, ""},
		{"render ears ok", KindShape{Kind: KindRender, Ears: true}, Output{Ears: ok("ok")}, "analysis.json"},
		{"render ears error", KindShape{Kind: KindRender, Ears: true}, Output{Ears: ok("error")}, ""},
		{"render ears skipped", KindShape{Kind: KindRender, Ears: true}, Output{Ears: ok("skipped")}, ""},
		{"analyze", KindShape{Kind: KindAnalyze, Ears: true}, Output{}, "analysis.json"},
		{"plan with a failed seed", KindShape{Kind: KindPlan, Seeds: 3}, Output{Plans: []PlanOut{{Index: 2, Status: "ok"}, {Index: 0, Status: "ok"}, {Index: 1, Status: "error"}}}, "plan-00.tar,plan-02.tar"},
	}
	for _, tc := range tests {
		if got := strings.Join(ExtraRequiredV2(tc.shape, tc.out), ","); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestWriteArtifactsV2 exports the table to worker/contract/testdata/artifacts-v2.json, which the Python test
// test_artifacts_v2_equal_go_table reads. Run with -update to rewrite it; without, the file must equal the table.
func TestWriteArtifactsV2(t *testing.T) {
	type art struct {
		Name     string `json:"name"`
		MaxBytes int64  `json:"max_bytes"`
		Required bool   `json:"required"`
	}
	type shape struct {
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		Ears       bool   `json:"ears"`
		KeepVocals bool   `json:"keep_vocals"`
		Seeds      int    `json:"seeds"`
		Artifacts  []art  `json:"artifacts"`
	}
	doc := struct {
		Contract string  `json:"contract"`
		Shapes   []shape `json:"shapes"`
	}{Contract: ContractV2}
	for _, sh := range v2Shapes {
		s := shape{Name: sh.Name, Kind: sh.Shape.Kind, Ears: sh.Shape.Ears, KeepVocals: sh.Shape.KeepVocals, Seeds: sh.Shape.Seeds}
		for _, a := range ArtifactsV2(sh.Shape) {
			s.Artifacts = append(s.Artifacts, art(a))
		}
		doc.Shapes = append(doc.Shapes, s)
	}
	got, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("..", "..", "worker", "contract", "testdata", "artifacts-v2.json")
	if *updateE2 {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s is stale: run go test ./internal/provider -run TestWriteArtifactsV2 -update", path)
	}
}

// TestUploadTarget_V1Bytes: the new Fallback field leaves the /1 target encoding untouched.
func TestUploadTarget_V1Bytes(t *testing.T) {
	t.Parallel()
	got, _ := json.Marshal(UploadTarget{URL: "https://h/x", MaxBytes: 5})
	if string(got) != `{"url":"https://h/x","max_bytes":5}` {
		t.Errorf("target = %s", got)
	}
}
