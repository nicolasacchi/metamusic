package tierb_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
)

const sampleLyrics = "[intro]\n\n[verse]\nla chiave del mare\nsotto il cielo\n\n[chorus]\ncanto con te\n\n[outro]\n"

func resolveSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "worker", "src", "mmworker", "schema", "mm-ears-b-1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func baseDoc(t *testing.T, o tierbtest.Opts) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(tierbtest.FromLyrics(sampleLyrics, o), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func obj(m map[string]any, k string) map[string]any { return m[k].(map[string]any) }

func TestParse_SchemaAgreement(t *testing.T) {
	t.Parallel()
	rs := resolveSchema(t)
	manyWords := func(n int) []any {
		w := make([]any, n)
		for i := range w {
			w[i] = map[string]any{"w": "a", "start": 1.0, "end": 1.1, "p": 0.5}
		}
		return w
	}
	manySegs := func(n int) []any {
		s := make([]any, n)
		for i := range s {
			s[i] = map[string]any{"start": 1.0, "end": 2.0, "avg_logprob": -0.1, "no_speech_p": 0.0, "w0": 0, "w1": 0}
		}
		return s
	}
	zeros := func(n int) []any {
		a := make([]any, n)
		for i := range a {
			a[i] = 0
		}
		return a
	}
	pitched := tierbtest.Opts{Transcribe: true}
	cases := []struct {
		name  string
		opts  tierbtest.Opts
		edit  func(m map[string]any)
		valid bool
	}{
		{"ok with pitch", pitched, func(m map[string]any) {}, true},
		{"ok without pitch", tierbtest.Opts{}, func(m map[string]any) {}, true},
		{"error document", tierbtest.Opts{Status: "error", Code: "OOM"}, func(m map[string]any) {}, true},
		{"input null", pitched, func(m map[string]any) { m["input"] = nil }, true},
		{"parent_reserved set", pitched, func(m map[string]any) { obj(m, "resources")["parent_reserved_mib"] = 13210 }, true},
		{"vocals null", pitched, func(m map[string]any) { m["vocals"] = nil }, true},
		{"extra top-level member", pitched, func(m map[string]any) { m["extra"] = 1 }, false},
		{"extra nested member", pitched, func(m map[string]any) { obj(m, "stages")["extra"] = "ok" }, false},
		{"extra word member", pitched, func(m map[string]any) {
			obj(m, "asr")["words"].([]any)[0].(map[string]any)["x"] = 1
		}, false},
		{"missing models", pitched, func(m map[string]any) { delete(m, "models") }, false},
		{"missing word member", pitched, func(m map[string]any) { delete(obj(m, "asr")["words"].([]any)[0].(map[string]any), "p") }, false},
		{"missing timings member", pitched, func(m map[string]any) { delete(obj(m, "timings"), "asr_s") }, false},
		{"missing compute_type", pitched, func(m map[string]any) { delete(obj(obj(m, "models"), "asr"), "compute_type") }, false},
		{"wrong contract", pitched, func(m map[string]any) { m["contract"] = "mm.ears-b/2" }, false},
		{"bogus status", pitched, func(m map[string]any) { m["status"] = "partial" }, false},
		{"ok with a code", pitched, func(m map[string]any) { m["code"] = "OOM" }, false},
		{"error without code", tierbtest.Opts{Status: "error"}, func(m map[string]any) { m["code"] = nil }, false},
		{"error with bogus code", tierbtest.Opts{Status: "error"}, func(m map[string]any) { m["code"] = "NOPE" }, false},
		{"error status with asr ok", tierbtest.Opts{Status: "error"}, func(m map[string]any) { obj(m, "stages")["asr"] = "ok" }, false},
		{"ok status with asr error", pitched, func(m map[string]any) { obj(m, "stages")["asr"] = "error" }, false},
		{"bogus stage value", pitched, func(m map[string]any) { obj(m, "stages")["separate"] = "maybe" }, false},
		{"stages null", pitched, func(m map[string]any) { m["stages"] = nil }, false},
		{"ok with asr null", pitched, func(m map[string]any) { m["asr"] = nil }, false},
		{"transcribe ok with pitch null", pitched, func(m map[string]any) { m["pitch"] = nil }, false},
		{"pitch present when not requested", tierbtest.Opts{}, func(m map[string]any) {
			m["pitch"] = map[string]any{"hop_s": 0.02, "cents": []any{0}, "conf": []any{0}}
		}, false},
		{"short sha256", pitched, func(m map[string]any) { obj(obj(m, "models"), "separator")["sha256"] = "abc" }, false},
		{"uppercase sha256", pitched, func(m map[string]any) {
			obj(obj(m, "models"), "separator")["sha256"] = strings.Repeat("A", 64)
		}, false},
		{"word p over 1", pitched, func(m map[string]any) { obj(m, "asr")["words"].([]any)[0].(map[string]any)["p"] = 1.5 }, false},
		{"conf over 100", pitched, func(m map[string]any) { obj(m, "pitch")["conf"].([]any)[0] = 101 }, false},
		{"hop 30 ms", pitched, func(m map[string]any) { obj(m, "pitch")["hop_s"] = 0.03 }, false},
		{"negative w0", pitched, func(m map[string]any) { obj(m, "asr")["segments"].([]any)[0].(map[string]any)["w0"] = -1 }, false},
		{"bogus error stage", tierbtest.Opts{Status: "error"}, func(m map[string]any) {
			m["errors"].([]any)[0].(map[string]any)["stage"] = "mix"
		}, false},
		{"20000 words", pitched, func(m map[string]any) { obj(m, "asr")["words"] = manyWords(20000); obj(m, "asr")["segments"] = []any{} }, true},
		{"20001 words", pitched, func(m map[string]any) { obj(m, "asr")["words"] = manyWords(20001); obj(m, "asr")["segments"] = []any{} }, false},
		{"4001 segments", pitched, func(m map[string]any) { obj(m, "asr")["segments"] = manySegs(4001) }, false},
		{"36000 pitch frames", pitched, func(m map[string]any) {
			obj(m, "pitch")["cents"], obj(m, "pitch")["conf"] = zeros(36000), zeros(36000)
		}, true},
		{"36001 pitch frames", pitched, func(m map[string]any) {
			obj(m, "pitch")["cents"], obj(m, "pitch")["conf"] = zeros(36001), zeros(36001)
		}, false},
		{"case-variant extra member", pitched, func(m map[string]any) { m["Status"] = "ok" }, false},
		{"case-variant extra nested member", pitched, func(m map[string]any) { obj(m, "stages")["ASR"] = "ok" }, false},
		{"word of 64 runes", pitched, func(m map[string]any) {
			obj(m, "asr")["words"].([]any)[0].(map[string]any)["w"] = strings.Repeat("é", 64)
		}, true},
		{"word of 65 runes", pitched, func(m map[string]any) {
			obj(m, "asr")["words"].([]any)[0].(map[string]any)["w"] = strings.Repeat("é", 65)
		}, false},
		{"error message of 300 runes", tierbtest.Opts{Status: "error"}, func(m map[string]any) {
			m["errors"].([]any)[0].(map[string]any)["message"] = strings.Repeat("è", 300)
		}, true},
		{"error message of 301 runes", tierbtest.Opts{Status: "error"}, func(m map[string]any) {
			m["errors"].([]any)[0].(map[string]any)["message"] = strings.Repeat("è", 301)
		}, false},
		{"language of 8 runes", pitched, func(m map[string]any) { obj(m, "asr")["language"] = strings.Repeat("é", 8) }, true},
		{"language of 9 runes", pitched, func(m map[string]any) { obj(m, "asr")["language"] = strings.Repeat("é", 9) }, false},
		{"language of 1 rune", pitched, func(m map[string]any) { obj(m, "asr")["language"] = "é" }, false},
		{"21 errors", pitched, func(m map[string]any) {
			e := make([]any, 21)
			for i := range e {
				e[i] = map[string]any{"stage": "asr", "code": "OOM", "message": "x"}
			}
			m["errors"] = e
		}, false},
	}
	if len(cases) < 20 {
		t.Fatalf("table has %d documents", len(cases))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m := baseDoc(t, c.opts)
			c.edit(m)
			doc, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			var inst any
			if err := json.Unmarshal(doc, &inst); err != nil {
				t.Fatal(err)
			}
			schemaErr := rs.Validate(inst)
			_, parseErr := tierb.Parse(doc)
			if (schemaErr == nil) != c.valid {
				t.Errorf("schema: valid=%v, want %v (%v)", schemaErr == nil, c.valid, schemaErr)
			}
			if (parseErr == nil) != c.valid {
				t.Errorf("Parse: valid=%v, want %v (%v)", parseErr == nil, c.valid, parseErr)
			}
			if parseErr != nil && !errors.Is(parseErr, tierb.ErrContract) {
				t.Errorf("error does not wrap ErrContract: %v", parseErr)
			}
		})
	}
}

func TestParse_Limits(t *testing.T) {
	t.Parallel()
	mut := func(t *testing.T, o tierbtest.Opts, edit func(m map[string]any)) error {
		t.Helper()
		m := baseDoc(t, o)
		edit(m)
		b, _ := json.Marshal(m)
		_, err := tierb.Parse(b)
		return err
	}
	pitched := tierbtest.Opts{Transcribe: true}
	for _, c := range []struct {
		name string
		edit func(m map[string]any)
	}{
		{"cents and conf differ in length", func(m map[string]any) { obj(m, "pitch")["conf"] = obj(m, "pitch")["conf"].([]any)[1:] }},
		{"segment range past words", func(m map[string]any) { obj(m, "asr")["segments"].([]any)[0].(map[string]any)["w1"] = 9999 }},
		{"segment range reversed", func(m map[string]any) {
			s := obj(m, "asr")["segments"].([]any)[0].(map[string]any)
			s["w0"], s["w1"] = 3, 1
		}},
		{"word times decrease inside a segment", func(m map[string]any) {
			w := obj(m, "asr")["words"].([]any)
			w[1].(map[string]any)["start"], w[1].(map[string]any)["end"] = 0.5, 0.6
		}},
		{"word ends before it starts", func(m map[string]any) {
			obj(m, "asr")["words"].([]any)[0].(map[string]any)["end"] = 0.0
		}},
		{"trailing data", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var err error
			if c.edit == nil {
				_, err = tierb.Parse(append(tierbtest.FromLyrics(sampleLyrics, pitched), []byte(" {}")...))
			} else {
				err = mut(t, pitched, c.edit)
			}
			if !errors.Is(err, tierb.ErrContract) {
				t.Errorf("want ErrContract, got %v", err)
			}
		})
	}
	t.Run("oversize document", func(t *testing.T) {
		t.Parallel()
		big := bytes.Repeat([]byte(" "), tierb.MaxDocBytes+1)
		if _, err := tierb.Parse(append(tierbtest.FromLyrics(sampleLyrics, pitched), big...)); !errors.Is(err, tierb.ErrContract) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("not JSON", func(t *testing.T) {
		t.Parallel()
		if _, err := tierb.Parse([]byte("nope")); !errors.Is(err, tierb.ErrContract) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("a good document parses", func(t *testing.T) {
		t.Parallel()
		raw, err := tierb.Parse(tierbtest.FromLyrics(sampleLyrics, pitched))
		if err != nil {
			t.Fatal(err)
		}
		if raw.ASR == nil || len(raw.ASR.Words) != 10 || raw.Pitch == nil {
			t.Errorf("unexpected parse: %+v", raw.Stages)
		}
	})
}

func TestParse_StatusRules(t *testing.T) {
	t.Parallel()
	// a failed transcribe leaves pitch null with an errors entry; the document stays ok
	m := baseDoc(t, tierbtest.Opts{})
	obj(m, "stages")["transcribe"] = "error"
	m["errors"] = []any{map[string]any{"stage": "transcribe", "code": "OOM", "message": "CUDA out of memory"}}
	b, _ := json.Marshal(m)
	raw, err := tierb.Parse(b)
	if err != nil {
		t.Fatalf("failed transcribe: %v", err)
	}
	if raw.Status != tierb.StatusOK || raw.Pitch != nil || len(raw.Errors) != 1 {
		t.Errorf("got status %q pitch %v errors %d", raw.Status, raw.Pitch, len(raw.Errors))
	}
	// an error document carries its code and no asr
	raw, err = tierb.Parse(tierbtest.FromLyrics(sampleLyrics, tierbtest.Opts{Status: "error", Code: "DEADLINE"}))
	if err != nil {
		t.Fatal(err)
	}
	if raw.Status != tierb.StatusError || raw.Code == nil || *raw.Code != "DEADLINE" || raw.ASR != nil {
		t.Errorf("error document: %+v", raw)
	}
	// every documented code is accepted, an undocumented one is not
	for _, code := range []string{"MODELS_MISSING", "INPUT_DECODE", "SEPARATION_FAILED", "ASR_FAILED", "DEADLINE", "OOM", "SUBPROCESS_FAILED"} {
		if _, err := tierb.Parse(tierbtest.FromLyrics(sampleLyrics, tierbtest.Opts{Status: "error", Code: code})); err != nil {
			t.Errorf("%s: %v", code, err)
		}
	}
}
