package provider

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nicolasacchi/metamusic/internal/money"
)

func schemaDir() string { return filepath.Join("..", "..", "worker", "src", "mmworker", "schema") }

func loadSchema(t *testing.T, name string) (*jsonschema.Resolved, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(schemaDir(), name))
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
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	return rs, generic
}

const (
	rev3B  = "0123456789abcdef0123456789abcdef01234567"
	revVAE = "89abcdef0123456789abcdef0123456789abcdef"
)

// exampleInput is the C73.3 example with concrete ids; urls use the documentation host.
func exampleInput() map[string]any {
	target := func(name string) map[string]any {
		limit, _ := ArtifactMaxBytes(name)
		return map[string]any{"url": "https://mm.example.com/worker/v1/uploads/" + name + "?exp=1760000000&sig=abc", "max_bytes": limit}
	}
	uploads := map[string]any{}
	for _, n := range []string{"audio.flac", "request.json", "config.json", "result.json", "mm-result.json"} {
		uploads[n] = target(n)
	}
	return map[string]any{
		"contract": "mm.worker/1", "take_id": "01900000-0000-7000-8000-00000000a001", "attempt_id": "01900000-0000-7000-8000-00000000a002",
		"engine":    "yue2",
		"request":   map[string]any{"cot": "full", "id": "syn-a-s1102", "lyrics": "[Verse]\nla la", "seed": 1102, "style": "pop, 100 BPM"},
		"timeout_s": 186, "upload_budget_s": 60, "deadline_unix": 1760000000,
		"start":   map[string]any{"url": "https://mm.example.com/worker/v1/attempts/01900000-0000-7000-8000-00000000a002/start?exp=1760000000&sig=abc"},
		"uploads": uploads,
		"expect":  map[string]any{"weights": map[string]any{"m-a-p/YuE2-3B": rev3B, "m-a-p/YuE2-Vae": revVAE}},
	}
}

func exampleOutput() map[string]any {
	return map[string]any{
		"contract": "mm.worker/1", "take_id": "01900000-0000-7000-8000-00000000a001", "attempt_id": "01900000-0000-7000-8000-00000000a002",
		"status": "ok", "error": nil,
		"artifacts": map[string]any{
			"audio.flac":   map[string]any{"sha256": strings.Repeat("ab", 32), "bytes": 31457280},
			"request.json": map[string]any{"sha256": strings.Repeat("cd", 32), "bytes": 212},
		},
		"audio_seconds": 215.8,
		"truncated":     map[string]any{"abc": false, "semantic": false},
		"timings":       map[string]any{"load_s": nil, "generate_s": 70.1, "save_s": 1.2, "upload_s": 3.4, "handler_s": 75.0, "e2e_seconds": 71.0},
		"env": map[string]any{"engine": "yue2", "pkg_version": "0.1.6", "code_commit": strings.Repeat("a", 40),
			"weights": map[string]any{"m-a-p/YuE2-3B": rev3B, "m-a-p/YuE2-Vae": revVAE}, "decoder": "m-a-p/YuE2-Vae",
			"gpu_name": "NVIDIA RTX A5000", "cuda": "12.8", "torch": "2.10.0", "worker_build": strings.Repeat("b", 40), "python": "3.12.12"},
	}
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestArtifacts_SchemaEnum: the Go allowlist and its sizes equal the names and maximums of the schema.
func TestArtifacts_SchemaEnum(t *testing.T) {
	t.Parallel()
	_, in := loadSchema(t, "mm-worker-1.input.schema.json")
	uploads := in["properties"].(map[string]any)["uploads"].(map[string]any)
	props := uploads["properties"].(map[string]any)
	if len(props) != len(Artifacts) {
		t.Fatalf("schema lists %d artifacts, Go %d", len(props), len(Artifacts))
	}
	wantReq := map[string]bool{}
	for _, a := range Artifacts {
		p, ok := props[a.Name].(map[string]any)
		if !ok {
			t.Errorf("%s missing from the input schema", a.Name)
			continue
		}
		limit := p["properties"].(map[string]any)["max_bytes"].(map[string]any)["maximum"].(float64)
		if int64(limit) != a.MaxBytes {
			t.Errorf("%s: schema maximum %v, Go %d", a.Name, limit, a.MaxBytes)
		}
		if a.Required {
			wantReq[a.Name] = true
		}
	}
	for _, n := range uploads["required"].([]any) {
		if !wantReq[n.(string)] {
			t.Errorf("schema requires %v, Go does not", n)
		}
		delete(wantReq, n.(string))
	}
	if len(wantReq) != 0 {
		t.Errorf("Go requires %v, schema does not", wantReq)
	}
	// the output schema reports every artifact but mm-result.json itself
	_, out := loadSchema(t, "mm-worker-1.output.schema.json")
	oprops := out["properties"].(map[string]any)["artifacts"].(map[string]any)["properties"].(map[string]any)
	if len(oprops) != len(Artifacts)-1 {
		t.Errorf("output schema lists %d artifacts, want %d", len(oprops), len(Artifacts)-1)
	}
	for _, a := range Artifacts {
		if _, ok := oprops[a.Name]; ok == (a.Name == ResultArtifact) {
			t.Errorf("output schema membership of %s wrong", a.Name)
		}
	}
	if _, ok := ArtifactMaxBytes("latent.npy"); ok {
		t.Error("latent.npy must never be allowlisted")
	}
}

func TestWorkerSchemas_Valid(t *testing.T) {
	t.Parallel()
	in, _ := loadSchema(t, "mm-worker-1.input.schema.json")
	out, _ := loadSchema(t, "mm-worker-1.output.schema.json")
	check := func(rs *jsonschema.Resolved, doc map[string]any) error {
		t.Helper()
		var inst any
		if err := json.Unmarshal(marshal(t, doc), &inst); err != nil {
			t.Fatal(err)
		}
		return rs.Validate(inst)
	}
	if err := check(in, exampleInput()); err != nil {
		t.Errorf("C73.3 example invalid: %v", err)
	}
	if err := check(out, exampleOutput()); err != nil {
		t.Errorf("C73.5 example invalid: %v", err)
	}
	// an extra property is invalid, at the top level and nested
	i := exampleInput()
	i["extra"] = 1
	if check(in, i) == nil {
		t.Error("input: extra property accepted")
	}
	o := exampleOutput()
	o["extra"] = 1
	if check(out, o) == nil {
		t.Error("output: extra property accepted")
	}
	o = exampleOutput()
	o["env"].(map[string]any)["extra"] = 1
	if check(out, o) == nil {
		t.Error("output: extra env property accepted")
	}
	// status error needs an error object; status ok needs none
	o = exampleOutput()
	o["status"] = "error"
	if check(out, o) == nil {
		t.Error("output: status error without error object accepted")
	}
	o["error"] = map[string]any{"code": "DEADLINE", "message": "stopped at the generation deadline"}
	o["truncated"], o["audio_seconds"] = nil, nil
	if err := check(out, o); err != nil {
		t.Errorf("output: error result invalid: %v", err)
	}
	o["error"] = map[string]any{"code": "NOT_A_CODE", "message": "x"}
	if check(out, o) == nil {
		t.Error("output: unknown error code accepted")
	}
	o = exampleOutput()
	o["error"] = map[string]any{"code": "DEADLINE", "message": "x"}
	if check(out, o) == nil {
		t.Error("output: status ok with an error object accepted")
	}
	// strict Go decoding agrees on the happy path and rejects unknown keys
	if _, err := DecodeOutput(marshal(t, exampleOutput())); err != nil {
		t.Errorf("DecodeOutput: %v", err)
	}
	o = exampleOutput()
	o["extra"] = true
	if _, err := DecodeOutput(marshal(t, o)); !errors.Is(err, ErrWorkerInput) {
		t.Errorf("DecodeOutput accepted an extra key: %v", err)
	}
	dec, _ := DecodeOutput(marshal(t, exampleOutput()))
	if dec.AudioSeconds == nil || dec.AudioSeconds.String() != "215.8" || dec.Timings["load_s"] != nil || dec.Timings["generate_s"] == nil ||
		dec.Truncated["abc"] || dec.Env.Python != "3.12.12" || dec.Artifacts["audio.flac"].Bytes != 31457280 {
		t.Errorf("decoded output: %+v", dec)
	}
}

func TestParseWorkerInput_MatchesSchema(t *testing.T) {
	t.Parallel()
	rs, _ := loadSchema(t, "mm-worker-1.input.schema.json")
	type mut = func(m map[string]any)
	uploads := func(m map[string]any) map[string]any { return m["uploads"].(map[string]any) }
	target := func(m map[string]any, n string) map[string]any { return uploads(m)[n].(map[string]any) }
	cases := map[string]mut{
		"valid": func(m map[string]any) {},
		"valid with score.abc": func(m map[string]any) {
			uploads(m)["score.abc"] = map[string]any{"url": "https://h/x", "max_bytes": 1 << 20}
		},
		"valid http loopback": func(m map[string]any) {
			m["start"] = map[string]any{"url": "http://127.0.0.1:8080/worker/v1/attempts/a/start"}
		},
		"contract v2":      func(m map[string]any) { m["contract"] = "mm.worker/2" },
		"contract absent":  func(m map[string]any) { delete(m, "contract") },
		"extra top-level":  func(m map[string]any) { m["extra"] = true },
		"extra in start":   func(m map[string]any) { m["start"].(map[string]any)["x"] = 1 },
		"extra in target":  func(m map[string]any) { target(m, "audio.flac")["x"] = 1 },
		"extra in expect":  func(m map[string]any) { m["expect"].(map[string]any)["x"] = 1 },
		"take_id empty":    func(m map[string]any) { m["take_id"] = "" },
		"take_id space":    func(m map[string]any) { m["take_id"] = "a b" },
		"take_id 64":       func(m map[string]any) { m["take_id"] = strings.Repeat("a", 64) },
		"take_id 65":       func(m map[string]any) { m["take_id"] = strings.Repeat("a", 65) },
		"attempt_id bad":   func(m map[string]any) { m["attempt_id"] = "a/b" },
		"engine upper":     func(m map[string]any) { m["engine"] = "YuE2" },
		"engine long":      func(m map[string]any) { m["engine"] = strings.Repeat("a", 33) },
		"request empty":    func(m map[string]any) { m["request"] = map[string]any{} },
		"request array":    func(m map[string]any) { m["request"] = []any{1} },
		"request string":   func(m map[string]any) { m["request"] = "x" },
		"timeout 0":        func(m map[string]any) { m["timeout_s"] = 0 },
		"timeout 2700":     func(m map[string]any) { m["timeout_s"] = 2700 },
		"timeout 2701":     func(m map[string]any) { m["timeout_s"] = 2701 },
		"timeout string":   func(m map[string]any) { m["timeout_s"] = "186" },
		"budget 0":         func(m map[string]any) { m["upload_budget_s"] = 0 },
		"budget 2701":      func(m map[string]any) { m["upload_budget_s"] = 2701 },
		"deadline 0":       func(m map[string]any) { m["deadline_unix"] = 0 },
		"deadline absent":  func(m map[string]any) { delete(m, "deadline_unix") },
		"start ftp":        func(m map[string]any) { m["start"] = map[string]any{"url": "ftp://h/x"} },
		"start no scheme":  func(m map[string]any) { m["start"] = map[string]any{"url": "h/x"} },
		"start whitespace": func(m map[string]any) { m["start"] = map[string]any{"url": "https://h/x y"} },
		"start 2048": func(m map[string]any) {
			m["start"] = map[string]any{"url": "https://h/" + strings.Repeat("a", 2048-10)}
		},
		"start 2049": func(m map[string]any) {
			m["start"] = map[string]any{"url": "https://h/" + strings.Repeat("a", 2049-10)}
		},
		"start absent":      func(m map[string]any) { delete(m, "start") },
		"uploads no result": func(m map[string]any) { delete(uploads(m), "mm-result.json") },
		"uploads no audio":  func(m map[string]any) { delete(uploads(m), "audio.flac") },
		"uploads latent": func(m map[string]any) {
			uploads(m)["latent.npy"] = map[string]any{"url": "https://h/x", "max_bytes": 1}
		},
		"max_bytes 0":          func(m map[string]any) { target(m, "audio.flac")["max_bytes"] = 0 },
		"max_bytes at limit":   func(m map[string]any) { target(m, "mm-result.json")["max_bytes"] = 64 * 1024 },
		"max_bytes over limit": func(m map[string]any) { target(m, "mm-result.json")["max_bytes"] = 64*1024 + 1 },
		"target url bad":       func(m map[string]any) { target(m, "audio.flac")["url"] = "nope" },
		"target no max":        func(m map[string]any) { delete(target(m, "audio.flac"), "max_bytes") },
		"expect absent":        func(m map[string]any) { delete(m, "expect") },
		"weights empty":        func(m map[string]any) { m["expect"] = map[string]any{"weights": map[string]any{}} },
		"weights uppercase hex": func(m map[string]any) {
			m["expect"].(map[string]any)["weights"].(map[string]any)["m-a-p/YuE2-3B"] = strings.ToUpper(rev3B)
		},
		"weights short": func(m map[string]any) {
			m["expect"].(map[string]any)["weights"].(map[string]any)["m-a-p/YuE2-3B"] = "abc"
		},
		"weights non-string": func(m map[string]any) { m["expect"].(map[string]any)["weights"].(map[string]any)["m-a-p/YuE2-3B"] = 5 },
		"uploads is array":   func(m map[string]any) { m["uploads"] = []any{} },
		"min_audio_s 200":    func(m map[string]any) { m["min_audio_s"] = 200 },
		"min_audio_s 0":      func(m map[string]any) { m["min_audio_s"] = 0 },
		"min_audio_s 360":    func(m map[string]any) { m["min_audio_s"] = 360 },
		"min_audio_s 361":    func(m map[string]any) { m["min_audio_s"] = 361 },
		"min_audio_s -1":     func(m map[string]any) { m["min_audio_s"] = -1 },
		"min_audio_s string": func(m map[string]any) { m["min_audio_s"] = "200" },
		"min_audio_s float":  func(m map[string]any) { m["min_audio_s"] = 200.5 },
		"min_audio_s null":   func(m map[string]any) { m["min_audio_s"] = nil },
	}
	for name, mutate := range cases {
		doc := exampleInput()
		mutate(doc)
		raw := marshal(t, doc)
		var inst any
		if err := json.Unmarshal(raw, &inst); err != nil {
			t.Fatal(err)
		}
		schemaOK := rs.Validate(inst) == nil
		_, perr := ParseWorkerInput(raw)
		if schemaOK != (perr == nil) {
			t.Errorf("%s: schema valid=%v, ParseWorkerInput err=%v", name, schemaOK, perr)
		}
		if perr != nil && !errors.Is(perr, ErrWorkerInput) {
			t.Errorf("%s: error does not wrap ErrWorkerInput: %v", name, perr)
		}
		if name == "valid" && !schemaOK || strings.HasPrefix(name, "valid") && perr != nil {
			t.Errorf("%s must be valid on both sides", name)
		}
	}
	// documents that are not objects, trailing data and invalid JSON are refused by the parser
	for _, raw := range []string{"", "null", "[]", `{"a":`, string(marshal(t, exampleInput())) + " {}"} {
		if _, err := ParseWorkerInput([]byte(raw)); !errors.Is(err, ErrWorkerInput) {
			t.Errorf("ParseWorkerInput(%.20q) = %v", raw, err)
		}
	}
	// errors never echo values
	doc := exampleInput()
	doc["start"] = map[string]any{"url": "https://h/secret-sig-value x"}
	if _, err := ParseWorkerInput(marshal(t, doc)); err == nil || strings.Contains(err.Error(), "secret-sig-value") {
		t.Errorf("error echoes a value: %v", err)
	}
}

func TestBuildWorkerInput_MinAudio(t *testing.T) {
	t.Parallel()
	ex := exampleInput()
	spec := TakeSpec{
		JobID: "a1", AttemptID: "a2", Engine: "yue2", Request: marshal(t, ex["request"]), TimeoutS: 186, UploadBudgetS: 60,
		Deadline: time.Unix(1760000000, 0), StartURL: ex["start"].(map[string]any)["url"].(string),
		Uploads: map[string]UploadTarget{}, Expect: WorkerExpect{Weights: map[string]string{"m-a-p/YuE2-3B": rev3B}},
	}
	for n, v := range ex["uploads"].(map[string]any) {
		m := v.(map[string]any)
		spec.Uploads[n] = UploadTarget{URL: m["url"].(string), MaxBytes: m["max_bytes"].(int64)}
	}
	raw, err := BuildWorkerInput(spec)
	if err != nil || strings.Contains(string(raw), "min_audio_s") {
		t.Fatalf("a take without a forced minimum must not carry the key (an older worker rejects it): %s %v", raw, err)
	}
	spec.MinAudioS = 252
	raw, err = BuildWorkerInput(spec)
	if err != nil {
		t.Fatal(err)
	}
	in, err := ParseWorkerInput(raw)
	if err != nil || in.MinAudioS != 252 {
		t.Fatalf("round trip: %+v %v", in, err)
	}
	rs, _ := loadSchema(t, "mm-worker-1.input.schema.json")
	var inst any
	_ = json.Unmarshal(raw, &inst)
	if err := rs.Validate(inst); err != nil {
		t.Errorf("schema: %v", err)
	}
}

func TestBuildWorkerInput_PassesParser(t *testing.T) {
	t.Parallel()
	ex := exampleInput()
	spec := TakeSpec{
		JobID: "01900000-0000-7000-8000-00000000a001", AttemptID: "01900000-0000-7000-8000-00000000a002", Engine: "yue2",
		Request: marshal(t, ex["request"]), TimeoutS: 186, UploadBudgetS: 60, Deadline: time.Unix(1760000000, 0),
		StartURL: ex["start"].(map[string]any)["url"].(string),
		Uploads:  map[string]UploadTarget{},
		Expect:   WorkerExpect{Weights: map[string]string{"m-a-p/YuE2-3B": rev3B, "m-a-p/YuE2-Vae": revVAE}},
	}
	for n, v := range ex["uploads"].(map[string]any) {
		m := v.(map[string]any)
		spec.Uploads[n] = UploadTarget{URL: m["url"].(string), MaxBytes: m["max_bytes"].(int64)}
	}
	raw, err := BuildWorkerInput(spec)
	if err != nil {
		t.Fatal(err)
	}
	in, err := ParseWorkerInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	if in.TakeID != spec.JobID || in.AttemptID != spec.AttemptID || in.DeadlineUnix != 1760000000 || in.Start.URL != spec.StartURL ||
		in.Uploads["audio.flac"].MaxBytes != 160<<20 || in.Expect.Weights["m-a-p/YuE2-Vae"] != revVAE {
		t.Errorf("round trip: %+v", in)
	}
	rs, _ := loadSchema(t, "mm-worker-1.input.schema.json")
	var inst any
	_ = json.Unmarshal(raw, &inst)
	if err := rs.Validate(inst); err != nil {
		t.Errorf("built input violates the schema: %v", err)
	}
	if len(raw) > MaxRunBody {
		t.Errorf("input of %d bytes exceeds MaxRunBody", len(raw))
	}
}

func TestObservedArtifacts(t *testing.T) {
	t.Parallel()
	if got := ObservedArtifacts(); len(got) != 0 {
		t.Errorf("placeholder must be empty until M3-X3, got %v", got)
	}
	copyA := ObservedArtifacts()
	copyA["full"] = []string{"x"}
	if len(ObservedArtifacts()) != 0 {
		t.Error("ObservedArtifacts returned shared state")
	}
	if _, err := parseObserved([]byte(`{"full":["score.abc","plan.json"],"melody":["score.abc"],"off":[]}`)); err != nil {
		t.Errorf("valid observed file refused: %v", err)
	}
	for _, bad := range []string{`{"loud":["score.abc"]}`, `{"full":["latent.npy"]}`, `{"full":["mm-result.json"]}`, `[]`, `{"full":"score.abc"}`} {
		if _, err := parseObserved([]byte(bad)); err == nil {
			t.Errorf("parseObserved(%s) accepted", bad)
		}
	}
}

// compile-time check that the money type is used by the interface types
var _ = money.MicroUSD(0)
