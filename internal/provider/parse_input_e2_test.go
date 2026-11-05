package provider

import (
	"encoding/json"
	"errors"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
)

var updateE2 = flag.Bool("update", false, "rewrite golden files")

const (
	aceRevMain = "0123456789abcdef0123456789abcdef01234567"
	aceRevBase = "89abcdef0123456789abcdef0123456789abcdef"
	aceSHA     = "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"
)

func aceTarget(name string) map[string]any {
	limit, _ := ArtifactMaxBytesFor(EngineACEStep, name)
	return map[string]any{"url": "https://mm.example.com/worker/v1/uploads/" + name + "?exp=1760000000&sig=abc", "max_bytes": limit}
}

func aceInref() map[string]any {
	return map[string]any{"url": "https://mm.example.com/media/" + aceSHA + ".flac?exp=1760000000&sig=abc", "sha256": aceSHA, "bytes": 1234567}
}

// aceBase is a valid text2music input; rows mutate a deep copy.
func aceBase() map[string]any {
	uploads := map[string]any{}
	for _, n := range []string{"audio.flac", "request.json", "result.json", "mm-result.json"} {
		uploads[n] = aceTarget(n)
	}
	return map[string]any{
		"contract": "mm.worker/1", "take_id": "01900000-0000-7000-8000-00000000b001", "attempt_id": "01900000-0000-7000-8000-00000000b002",
		"engine": "acestep15",
		"request": map[string]any{"id": "syn-ace-s1", "task": "text2music", "caption": "synthwave, female vocal", "lyrics": "[verse]\nla la",
			"instrumental": false, "vocal_language": "en", "bpm": 96, "duration_ds": 1800, "seed": 4242, "thinking": true,
			"engine_params_version": "ace1", "inference_steps": 8},
		"timeout_s": 140, "upload_budget_s": 60, "deadline_unix": 1760000000,
		"start":   map[string]any{"url": "https://mm.example.com/worker/v1/attempts/a/start?exp=1760000000&sig=abc"},
		"uploads": uploads,
		"expect":  map[string]any{"weights": map[string]any{"ACE-Step/Ace-Step1.5": aceRevMain}},
	}
}

func clone(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(marshal(t, m), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func req(m map[string]any) map[string]any { return m["request"].(map[string]any) }

// aceRows are the ACE-Step input table: every row says whether the schema and the Go parser accept it.
func aceRows() []struct {
	name   string
	mutate func(m map[string]any)
	ok     bool
} {
	src := map[string]any{"track_id": "t1", "master_sha256": aceSHA, "duration_ds": 600}
	ref := map[string]any{"track_id": "t2", "clip_sha256": aceSHA, "from_ds": 100, "to_ds": 300}
	edit := func(task string, extra map[string]any) func(m map[string]any) {
		return func(m map[string]any) {
			r := req(m)
			delete(r, "duration_ds")
			r["task"], r["src"], r["thinking"] = task, src, false
			maps.Copy(r, extra)
			m["inputs"] = map[string]any{"src_audio": aceInref()}
		}
	}
	base := map[string]any{"inference_steps": 50, "guidance_milli": 7000}
	return []struct {
		name   string
		mutate func(m map[string]any)
		ok     bool
	}{
		{"text2music", func(m map[string]any) {}, true},
		{"repaint", edit("repaint", map[string]any{"repaint_from_ds": 80, "repaint_to_ds": 200}), true},
		{"extend", edit("extend", map[string]any{"extend_ds": 300}), true},
		{"complete", edit("complete", map[string]any{"tracks": []any{"drums", "bass"}, "inference_steps": 50, "guidance_milli": 7000}), true},
		{"extract", edit("extract", map[string]any{"tracks": []any{"vocals"}, "inference_steps": 50, "guidance_milli": 7000}), true},
		{"lrc flag and lrc upload", func(m map[string]any) {
			req(m)["lrc"] = true
			m["uploads"].(map[string]any)["lyrics.lrc"] = aceTarget("lyrics.lrc")
		}, true},
		{"reference", func(m map[string]any) {
			req(m)["ref"] = ref
			m["inputs"] = map[string]any{"reference_audio": aceInref()}
		}, true},
		{"two weights repos", func(m map[string]any) {
			m["expect"].(map[string]any)["weights"].(map[string]any)["ACE-Step/acestep-v15-base"] = aceRevBase
		}, true},
		{"unknown top key", func(m map[string]any) { m["extra"] = 1 }, false},
		{"unknown request key", func(m map[string]any) { req(m)["cover"] = true }, false},
		{"min_audio_s is not an ace key", func(m map[string]any) { m["min_audio_s"] = 0 }, false},
		{"config.json is not allowlisted", func(m map[string]any) { m["uploads"].(map[string]any)["config.json"] = aceTarget("request.json") }, false},
		{"missing audio.flac", func(m map[string]any) { delete(m["uploads"].(map[string]any), "audio.flac") }, false},
		{"src without src_audio", func(m map[string]any) {
			edit("repaint", map[string]any{"repaint_from_ds": 80, "repaint_to_ds": 200})(m)
			delete(m, "inputs")
		}, false},
		{"src_audio without src", func(m map[string]any) { m["inputs"] = map[string]any{"src_audio": aceInref()} }, false},
		{"ref without reference_audio", func(m map[string]any) { req(m)["ref"] = ref }, false},
		{"input digest malformed", func(m map[string]any) {
			edit("repaint", map[string]any{"repaint_from_ds": 80, "repaint_to_ds": 200})(m)
			m["inputs"].(map[string]any)["src_audio"].(map[string]any)["sha256"] = "zz"
		}, false},
		{"input bytes zero", func(m map[string]any) {
			edit("repaint", map[string]any{"repaint_from_ds": 80, "repaint_to_ds": 200})(m)
			m["inputs"].(map[string]any)["src_audio"].(map[string]any)["bytes"] = 0
		}, false},
		{"unknown input name", func(m map[string]any) { m["inputs"] = map[string]any{"clip": aceInref()} }, false},
		{"base task with turbo steps", edit("complete", map[string]any{"tracks": []any{"drums"}, "inference_steps": 8, "guidance_milli": 7000}), false},
		{"turbo task with base steps", func(m map[string]any) { req(m)["inference_steps"] = 50 }, false},
		{"turbo task with guidance", func(m map[string]any) { req(m)["guidance_milli"] = 7000 }, false},
		{"complete without tracks", edit("complete", base), false},
		{"extract with two tracks", edit("extract", map[string]any{"tracks": []any{"vocals", "drums"}, "inference_steps": 50, "guidance_milli": 7000}), false},
		{"unknown stem name", edit("extract", map[string]any{"tracks": []any{"kazoo"}, "inference_steps": 50, "guidance_milli": 7000}), false},
		{"text2music without duration", func(m map[string]any) { delete(req(m), "duration_ds") }, false},
		{"text2music above 600 s", func(m map[string]any) { req(m)["duration_ds"] = 6001 }, false},
		{"repaint with thinking", func(m map[string]any) {
			edit("repaint", map[string]any{"repaint_from_ds": 80, "repaint_to_ds": 200})(m)
			req(m)["thinking"] = true
		}, false},
		{"unknown task", func(m map[string]any) { req(m)["task"] = "cover" }, false},
		{"bpm out of range", func(m map[string]any) { req(m)["bpm"] = 301 }, false},
		{"bad keyscale", func(m map[string]any) { req(m)["keyscale"] = "H flat" }, false},
		{"bad time signature", func(m map[string]any) { req(m)["timesignature"] = "5" }, false},
		{"three weights repos", func(m map[string]any) {
			w := m["expect"].(map[string]any)["weights"].(map[string]any)
			w["a/b"], w["c/d"] = aceRevBase, aceRevBase
		}, false},
		{"truncated revision", func(m map[string]any) {
			m["expect"].(map[string]any)["weights"].(map[string]any)["ACE-Step/Ace-Step1.5"] = "abc"
		}, false},
		{"timeout too large", func(m map[string]any) { m["timeout_s"] = 2701 }, false},
		{"audio upload over the ace limit", func(m map[string]any) {
			m["uploads"].(map[string]any)["audio.flac"].(map[string]any)["max_bytes"] = 192<<20 + 1
		}, false},
	}
}

// TestSchemas_ACEStep_Table: the committed acestep15 input schema and the Go parser accept and refuse the same documents.
func TestSchemas_ACEStep_Table(t *testing.T) {
	t.Parallel()
	rs, _ := loadSchema(t, "mm-worker-1.acestep15.input.schema.json")
	rows := aceRows()
	if len(rows) < 20 {
		t.Fatalf("%d rows, want at least 20", len(rows))
	}
	for _, r := range rows {
		m := clone(t, aceBase())
		r.mutate(m)
		var inst any
		if err := json.Unmarshal(marshal(t, m), &inst); err != nil {
			t.Fatal(err)
		}
		schemaOK := rs.Validate(inst) == nil
		_, perr := ParseWorkerInput(marshal(t, m))
		if perr == nil && !r.ok {
			// the request content is the engine's to judge (as yue2's is): the Go envelope parser only needs a non-empty object
			if _, _, is := acestep.Parse(marshal(t, m["request"])); acestep.HasErrors(is) {
				perr = errors.Join(ErrWorkerInput, errors.New("engine refuses the request"))
			}
		}
		if schemaOK != r.ok {
			t.Errorf("%s: schema accepts %v, want %v", r.name, schemaOK, r.ok)
		}
		if (perr == nil) != r.ok {
			t.Errorf("%s: Go parser accepts %v (%v), want %v", r.name, perr == nil, perr, r.ok)
		}
		if perr != nil && !errors.Is(perr, ErrWorkerInput) {
			t.Errorf("%s: error does not wrap ErrWorkerInput: %v", r.name, perr)
		}
	}
}

// TestSchemaGoWorker_CrossValidate (Go side): for every task the Go side builds a job input from a canonical request
// (acestep.Parse) through BuildWorkerInput; the schema and ParseWorkerInput accept it. Outputs: the ACE output schema
// accepts a clean output with truncated null and refuses a yue2-shaped one.
func TestSchemaGoWorker_CrossValidate(t *testing.T) {
	t.Parallel()
	in, _ := loadSchema(t, "mm-worker-1.acestep15.input.schema.json")
	src := map[string]any{"track_id": "t1", "master_sha256": aceSHA, "duration_ds": 900}
	reqs := map[string]map[string]any{
		"text2music": {},
		"repaint":    {"task": "repaint", "src": src, "repaint_from_ds": 80, "repaint_to_ds": 300, "thinking": false},
		"extend":     {"task": "extend", "src": src, "extend_ds": 300, "thinking": false},
		"complete":   {"task": "complete", "src": src, "tracks": []any{"drums"}, "thinking": false, "inference_steps": 50, "guidance_milli": 7000},
		"extract":    {"task": "extract", "src": src, "tracks": []any{"vocals"}, "thinking": false, "inference_steps": 50, "guidance_milli": 7000},
		"reference":  {"ref": map[string]any{"track_id": "t2", "clip_sha256": aceSHA, "from_ds": 100, "to_ds": 300}},
	}
	for name, patch := range reqs {
		r := clone(t, aceBase())["request"].(map[string]any)
		r["engine_params_version"] = "ace1"
		if name != "text2music" && name != "reference" {
			delete(r, "duration_ds")
		}
		maps.Copy(r, patch)
		parsed, canonical, issues := acestep.Parse(marshal(t, r))
		if acestep.HasErrors(issues) {
			t.Fatalf("%s: acestep.Parse: %v", name, issues)
		}
		_ = parsed
		spec := TakeSpec{JobID: "job1", AttemptID: "att1", Engine: EngineACEStep, Request: canonical, TimeoutS: 140, UploadBudgetS: 60,
			Uploads: map[string]UploadTarget{}, StartURL: "https://mm.example.com/start", Deadline: time.Unix(1760000000, 0),
			Expect: WorkerExpect{Weights: map[string]string{"ACE-Step/Ace-Step1.5": aceRevMain}}}
		for _, n := range []string{"audio.flac", "request.json", "result.json", "mm-result.json"} {
			limit, _ := ArtifactMaxBytesFor(EngineACEStep, n)
			spec.Uploads[n] = UploadTarget{URL: "https://mm.example.com/u/" + n, MaxBytes: limit}
		}
		spec.Inputs = map[string]InputRef{}
		if parsed.Src != nil {
			spec.Inputs[InputSrcAudio] = InputRef{URL: "https://mm.example.com/media/x.flac", SHA256: aceSHA, Bytes: 100}
		}
		if parsed.Ref != nil {
			spec.Inputs[InputReferenceAudio] = InputRef{URL: "https://mm.example.com/media/y.flac", SHA256: aceSHA, Bytes: 100}
		}
		raw, err := BuildWorkerInput(spec)
		if err != nil {
			t.Fatal(err)
		}
		var inst any
		_ = json.Unmarshal(raw, &inst)
		if err := in.Validate(inst); err != nil {
			t.Errorf("%s: schema refuses the Go-built input: %v", name, err)
		}
		if _, err := ParseWorkerInput(raw); err != nil {
			t.Errorf("%s: ParseWorkerInput refuses the Go-built input: %v", name, err)
		}
	}

	out, _ := loadSchema(t, "mm-worker-1.acestep15.output.schema.json")
	o := exampleOutput()
	o["truncated"] = nil
	o["env"].(map[string]any)["engine"] = "acestep15"
	o["artifacts"].(map[string]any)["lyrics.lrc"] = map[string]any{"sha256": strings.Repeat("ef", 32), "bytes": 100}
	valid := func(m map[string]any) bool {
		var inst any
		_ = json.Unmarshal(marshal(t, m), &inst)
		return out.Validate(inst) == nil
	}
	if !valid(o) {
		t.Error("the ACE output schema refuses a clean ACE output")
	}
	if _, err := DecodeOutput(marshal(t, o)); err != nil {
		t.Errorf("DecodeOutput refuses a null truncated: %v", err)
	}
	y := clone(t, o)
	y["truncated"] = map[string]any{"abc": false, "semantic": false}
	if valid(y) {
		t.Error("the ACE output schema accepts a truncated object")
	}
	y = clone(t, o)
	y["env"].(map[string]any)["engine"] = "yue2"
	if valid(y) {
		t.Error("the ACE output schema accepts env.engine yue2")
	}
	y = clone(t, o)
	y["artifacts"].(map[string]any)["config.json"] = map[string]any{"sha256": strings.Repeat("ef", 32), "bytes": 1}
	if valid(y) {
		t.Error("the ACE output schema accepts config.json")
	}
	y = clone(t, o)
	y["status"], y["error"] = "error", map[string]any{"code": "INPUT_MISMATCH", "message": "sha"}
	y["audio_seconds"] = nil
	if !valid(y) {
		t.Error("the ACE output schema refuses INPUT_MISMATCH")
	}
}

// TestTakeSpec_Yue2InputByteIdentical: the job input of a yue2 take is the M3 document (no engine-specific keys).
func TestTakeSpec_Yue2InputByteIdentical(t *testing.T) {
	t.Parallel()
	spec := TakeSpec{JobID: "job1", AttemptID: "att1", Engine: EngineYuE2,
		Request:  json.RawMessage(`{"cot":"full","id":"syn-a-s1102","lyrics":"[Verse]\nla la","seed":1102,"style":"pop, 100 BPM"}`),
		TimeoutS: 186, UploadBudgetS: 60, Deadline: time.Unix(1760000000, 0), StartURL: "https://mm.example.com/start?sig=abc",
		Uploads: map[string]UploadTarget{"audio.flac": {URL: "https://mm.example.com/u/audio", MaxBytes: 160 << 20}},
		Expect:  WorkerExpect{Weights: map[string]string{"m-a-p/YuE2-3B": rev3B}}}
	got, err := BuildWorkerInput(spec)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	_ = json.Unmarshal(got, &generic)
	for _, k := range []string{"inputs", "min_audio_s"} {
		if _, ok := generic[k]; ok {
			t.Errorf("yue2 input carries %q", k)
		}
	}
	golden := filepath.Join("testdata", "yue2_worker_input.golden.json")
	if *updateE2 {
		if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(want)) != string(got) {
		t.Errorf("yue2 job input changed\n got %s\nwant %s", got, want)
	}
	// a yue2 input with inputs is refused by the parser
	m := clone(t, exampleInput())
	m["inputs"] = map[string]any{"src_audio": aceInref()}
	if _, err := ParseWorkerInput(marshal(t, m)); err == nil {
		t.Error("a yue2 input with inputs was accepted")
	}
}
