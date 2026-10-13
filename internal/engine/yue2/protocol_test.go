package yue2

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func codes(is []Issue, sev string) []string {
	var out []string
	for _, i := range is {
		if i.Severity == sev {
			out = append(out, i.Code)
		}
	}
	sort.Strings(out)
	return out
}

func abcRes(m map[string]string) ABCResolver { return ABCResolver(fakeResolver(m)) }

func TestCheckRequest_Table(t *testing.T) {
	const base = `"id":"a","style":"s","lyrics":"l"`
	longID := strings.Repeat("a", 181)
	type row struct {
		name         string
		raw          string
		abc          ABCResolver
		yueErr, appE []string // error codes per mode
		yueFatal     bool
		appWarn      []string // must be contained in ModeApp warnings
	}
	rows := []row{
		{name: "ok minimal", raw: `{` + base + `}`},
		{name: "title key", raw: `{` + base + `,"title":"T"}`, yueErr: []string{"unknown_key"}, yueFatal: true, appWarn: []string{"dropped_key:title"}},
		{name: "unknown key x", raw: `{` + base + `,"x":1}`, yueErr: []string{"unknown_key"}, yueFatal: true, appWarn: []string{"dropped_key:x"}},
		{name: "lang key", raw: `{` + base + `,"lang":"it"}`, appWarn: []string{"dropped_key:lang"}},
		{name: "eval_index", raw: `{` + base + `,"eval_index":3}`, appWarn: []string{"dropped_key:eval_index"}},
		{name: "clip_id", raw: `{` + base + `,"clip_id":"c"}`, appWarn: []string{"dropped_key:clip_id"}},
		{name: "seed 2^53", raw: `{` + base + `,"seed":9007199254740992}`, appE: []string{"seed_out_of_range"}},
		{name: "seed 2^53-1", raw: `{` + base + `,"seed":9007199254740991}`},
		{name: "seed 2^63", raw: `{` + base + `,"seed":9223372036854775808}`, yueErr: []string{"seed_out_of_range"}, appE: []string{"seed_out_of_range"}},
		{name: "seed 2^63-1", raw: `{` + base + `,"seed":9223372036854775807}`, appE: []string{"seed_out_of_range"}},
		{name: "seed true", raw: `{` + base + `,"seed":true}`, yueErr: []string{"seed_out_of_range"}, appE: []string{"seed_out_of_range"}},
		{name: "seed 1.0", raw: `{` + base + `,"seed":1.0}`, yueErr: []string{"seed_out_of_range"}, appE: []string{"seed_out_of_range"}},
		{name: "seed 1e3", raw: `{` + base + `,"seed":1e3}`, yueErr: []string{"seed_out_of_range"}, appE: []string{"seed_out_of_range"}},
		{name: "seed string", raw: `{` + base + `,"seed":"7"}`, yueErr: []string{"seed_out_of_range"}, appE: []string{"seed_out_of_range"}},
		{name: "seed -1", raw: `{` + base + `,"seed":-1}`, yueErr: []string{"seed_out_of_range"}, appE: []string{"seed_out_of_range"}},
		{name: "seed null", raw: `{` + base + `,"seed":null}`, yueErr: []string{"seed_out_of_range"}, appE: []string{"seed_out_of_range"}},
		{name: "abc empty", raw: `{` + base + `,"abc":""}`, yueErr: []string{"empty_abc"}, appE: []string{"empty_abc"}},
		{name: "abc blank", raw: `{` + base + `,"abc":"  \n"}`, yueErr: []string{"empty_abc"}, appE: []string{"empty_abc"}},
		{name: "abc non-string", raw: `{` + base + `,"abc":5}`, yueErr: []string{"bad_abc"}, appE: []string{"bad_abc"}},
		{name: "abc ok", raw: `{` + base + `,"abc":"X:1"}`},
		{name: "abc cot off", raw: `{` + base + `,"cot":"off","abc":"X:1"}`, yueErr: []string{"abc_requires_cot"}, appE: []string{"abc_requires_cot"}},
		{name: "abc and path", raw: `{` + base + `,"abc":"X:1","abc_path":"a.abc"}`, yueErr: []string{"abc_conflict"}, appE: []string{"abc_conflict"}},
		{name: "abc_path resolved", raw: `{` + base + `,"abc_path":"a.abc"}`, abc: abcRes(map[string]string{"a.abc": "X:1"})},
		{name: "abc_path nil resolver", raw: `{` + base + `,"abc_path":"a.abc"}`, yueErr: []string{"abc_path_unresolved"}, appE: []string{"abc_path_unresolved"}},
		{name: "abc_path missing", raw: `{` + base + `,"abc_path":"b.abc"}`, abc: abcRes(nil), yueErr: []string{"abc_path_unresolved"}, appE: []string{"abc_path_unresolved"}},
		{name: "abc_path empty content", raw: `{` + base + `,"abc_path":"a.abc"}`, abc: abcRes(map[string]string{"a.abc": " "}), yueErr: []string{"empty_abc"}, appE: []string{"empty_abc"}},
		{name: "cfg 20", raw: `{` + base + `,"cfg_scale":20}`},
		{name: "cfg 20.5", raw: `{` + base + `,"cfg_scale":20.5}`, yueErr: []string{"bad_cfg_scale"}, appE: []string{"bad_cfg_scale"}},
		{name: "cfg negative", raw: `{` + base + `,"cfg_scale":-1}`, yueErr: []string{"bad_cfg_scale"}, appE: []string{"bad_cfg_scale"}},
		{name: "cfg null", raw: `{` + base + `,"cfg_scale":null}`},
		{name: "cfg overflow", raw: `{` + base + `,"cfg_scale":1e999}`, yueErr: []string{"bad_cfg_scale"}, appE: []string{"bad_cfg_scale"}},
		{name: "prompt match", raw: `{` + base + `,"prompt":` + jsonString(promptFor("full", "s", "l")) + `}`, appWarn: []string{"prompt_verified"}},
		{name: "prompt mismatch", raw: `{` + base + `,"prompt":"other"}`, yueErr: []string{"prompt_mismatch"}, appE: []string{"prompt_mismatch"}},
		{name: "semantic_sampling empty obj", raw: `{` + base + `,"semantic_sampling":{}}`, appE: []string{"sampling_unsupported"}},
		{name: "abc_sampling obj", raw: `{` + base + `,"abc_sampling":{"temperature":1}}`, appE: []string{"sampling_unsupported"}},
		{name: "semantic_sampling string", raw: `{` + base + `,"semantic_sampling":"x"}`, yueErr: []string{"bad_sampling"}, appE: []string{"sampling_unsupported"}},
		{name: "style tags mismatch", raw: `{"id":"a","style":"s","tags":"t","lyrics":"l"}`, yueErr: []string{"style_tags_mismatch"}, appE: []string{"style_tags_mismatch"}},
		{name: "tags only", raw: `{"id":"a","tags":"s","lyrics":"l"}`, appWarn: []string{"tags_alias"}},
		{name: "style and tags equal", raw: `{"id":"a","style":"s","tags":"s","lyrics":"l"}`},
		{name: "missing style", raw: `{"id":"a","lyrics":"l"}`, yueErr: []string{"missing_style"}, appE: []string{"missing_style"}},
		{name: "missing lyrics", raw: `{"id":"a","style":"s"}`, yueErr: []string{"missing_lyrics"}, appE: []string{"missing_lyrics"}},
		{name: "missing id", raw: `{"style":"s","lyrics":"l"}`, yueErr: []string{"missing_id"}, appE: []string{"missing_id"}},
		{name: "id dotdot", raw: `{"id":"..","style":"s","lyrics":"l"}`, yueErr: []string{"bad_id"}, appE: []string{"bad_id"}},
		{name: "id dot", raw: `{"id":".","style":"s","lyrics":"l"}`, yueErr: []string{"bad_id"}, appE: []string{"bad_id"}},
		{name: "id 181 chars", raw: `{"id":"` + longID + `","style":"s","lyrics":"l"}`, yueErr: []string{"bad_id"}, appE: []string{"bad_id"}},
		{name: "id non-string", raw: `{"id":5,"style":"s","lyrics":"l"}`, yueErr: []string{"bad_id"}, appE: []string{"bad_id"}},
		{name: "cot none", raw: `{` + base + `,"cot":"none"}`, yueErr: []string{"bad_cot"}, appE: []string{"bad_cot"}},
		{name: "cot null", raw: `{` + base + `,"cot":null}`, yueErr: []string{"bad_cot"}, appE: []string{"bad_cot"}},
		{name: "cot melody", raw: `{` + base + `,"cot":"melody"}`},
		{name: "invalid utf8", raw: "{\"id\":\"a\",\"style\":\"s\xff\",\"lyrics\":\"l\"}", yueErr: []string{"bad_utf8"}, appE: []string{"bad_utf8"}},
		{name: "lone surrogate", raw: `{"id":"a","style":"s\ud800","lyrics":"l"}`, yueErr: []string{"bad_utf8"}, appE: []string{"bad_utf8"}},
		{name: "array", raw: `[1]`, yueErr: []string{"not_object"}, appE: []string{"not_object"}},
		{name: "duplicate keys last wins", raw: `{"id":"a","id":"b","style":"s","lyrics":"l"}`},
	}
	if len(rows) < 35 {
		t.Fatalf("need >= 35 rows, got %d", len(rows))
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			y := CheckRequest([]byte(r.raw), ModeYuE2, r.abc)
			if got := codes(y, "error"); !reflect.DeepEqual(got, orNil(r.yueErr)) {
				t.Fatalf("ModeYuE2 errors = %v, want %v", got, r.yueErr)
			}
			fatal := false
			for _, i := range y {
				fatal = fatal || i.Fatal
				if i.Severity != "error" && i.Severity != "warning" {
					t.Fatalf("bad severity %q", i.Severity)
				}
			}
			if fatal != r.yueFatal {
				t.Fatalf("fatal = %v, want %v", fatal, r.yueFatal)
			}
			a := CheckRequest([]byte(r.raw), ModeApp, r.abc)
			appE := r.appE
			if r.yueFatal { // unknown keys are app warnings, not errors
				appE = nil
			}
			if got := codes(a, "error"); !reflect.DeepEqual(got, orNil(appE)) {
				t.Fatalf("ModeApp errors = %v, want %v", got, appE)
			}
			w := codes(a, "warning")
			for _, want := range r.appWarn {
				if !contains(w, want) {
					t.Fatalf("ModeApp warnings = %v, missing %s", w, want)
				}
			}
		})
	}
}

func orNil(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func TestCheckRequest_FieldsAndNoValueLeak(t *testing.T) {
	is := CheckRequest([]byte(`{"id":"a","style":"SECRETSTYLE","lyrics":"l","seed":-1,"title":"SECRETTITLE"}`), ModeApp, nil)
	for _, i := range is {
		if strings.Contains(i.Message, "SECRET") {
			t.Fatalf("message leaks value: %q", i.Message)
		}
	}
	if len(is) < 1 || is[0].Code != "seed_out_of_range" || is[0].Field != "seed" {
		t.Fatalf("issues = %+v", is)
	}
	w := CheckRequest([]byte(`{"id":"a","style":"s","lyrics":"l","seed":1,"cot":"full","title":"T"}`), ModeApp, nil)
	var found bool
	for _, i := range w {
		if i.Code == "dropped_key:title" && i.Field == "title" && i.Severity == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v", w)
	}
}

// TestCheckRequest_AgreesWithParseRequest (C32): ModeApp is ParseRequest mapped to issues.
func TestCheckRequest_AgreesWithParseRequest(t *testing.T) {
	long := strings.Repeat("a", 181)
	raws := []string{
		`[1]`, `nope`, `{"style":"s","lyrics":"l"}`, `{"id":"../x","style":"s","lyrics":"l"}`,
		`{"id":"` + long + `","style":"s","lyrics":"l"}`, `{"id":"a","style":"s"}`, `{"id":"a","lyrics":"l"}`,
		`{"id":"a","style":"s","tags":"t","lyrics":"l"}`, `{"id":"a","style":"s","lyrics":"l","cot":"none"}`,
		`{"id":"a","style":"s","lyrics":"l","seed":1.5}`, `{"id":"a","style":"s","lyrics":"l","seed":9007199254740992}`,
		`{"id":"a","style":"s","lyrics":"l","cfg_scale":21}`, `{"id":"a","style":"s","lyrics":"l","abc":5}`,
		`{"id":"a","style":"s","lyrics":"l","abc":"  "}`, `{"id":"a","style":"s","lyrics":"l","cot":"off","abc":"X:1"}`,
		`{"id":"a","style":"s","lyrics":"l","abc":"X:1","abc_path":"a.abc"}`,
		`{"id":"a","style":"s","lyrics":"l","abc_path":"a.abc"}`, `{"id":"a","style":"s","lyrics":"l","abc_path":"z.abc"}`,
		`{"id":"a","style":"s","lyrics":"l","prompt":"hello"}`, `{"id":"a","style":"s","lyrics":"l","semantic_sampling":{}}`,
		"{\"id\":\"a\",\"style\":\"s\xff\",\"lyrics\":\"l\"}", `{"id":"a","style":"s\ud800","lyrics":"l"}`,
		// accepted rows
		`{"id":"a","style":"s","lyrics":"l"}`, `{"id":"a","tags":"s","lyrics":"l","title":"T","x":1}`,
		`{"id":"a","style":"s","lyrics":"l","cot":"off","cfg_scale":1.01,"seed":7}`,
		"\xef\xbb\xbf{\"id\":\"a\",\"style\":\"s\",\"lyrics\":\"l\"}\r\n",
		`{"id":"a","style":"s","lyrics":"l","prompt":` + jsonString(promptFor("full", "s", "l")) + `}`,
		`{"id":"a","style":"s","lyrics":"l","abc_path":"a.abc"}`,
	}
	res := abcRes(map[string]string{"a.abc": "X:1"})
	for i, raw := range raws {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			p, err := ParseRequest([]byte(raw), ParseOptions{ResolveABCPath: res})
			is := CheckRequest([]byte(raw), ModeApp, res)
			var errs, warns []string
			for _, x := range is {
				if x.Severity == "error" {
					errs = append(errs, x.Code)
				} else {
					warns = append(warns, x.Code)
				}
			}
			if err != nil {
				var pe *ParseError
				if !errors.As(err, &pe) || len(errs) != 1 || errs[0] != pe.Code || len(warns) != 0 {
					t.Fatalf("err %v vs issues %+v", err, is)
				}
				return
			}
			if len(errs) != 0 || !reflect.DeepEqual(warns, orNil(p.Warnings)) {
				t.Fatalf("warnings %v vs %v (errs %v)", warns, p.Warnings, errs)
			}
		})
	}
}
