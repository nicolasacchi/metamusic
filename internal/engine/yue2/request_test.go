package yue2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "update golden files")

func hasWarn(p Parsed, w string) bool {
	for _, x := range p.Warnings {
		if x == w {
			return true
		}
	}
	return false
}

func hasDrop(p Parsed, w string) bool {
	for _, x := range p.Dropped {
		if x == w {
			return true
		}
	}
	return false
}

func fakeResolver(m map[string]string) func(string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		if v, ok := m[rel]; ok {
			return []byte(v), nil
		}
		return nil, errors.New("nope")
	}
}

func promptFor(cot, style, lyrics string) string {
	return Instructions[cot] + "\n[Tags]\n" + style + "\n[Lyrics]\n" + lyrics + "\n"
}

func TestParseRequest_Errors(t *testing.T) {
	longID := strings.Repeat("a", 181)
	rows := []struct {
		name string
		raw  string
		opt  ParseOptions
		code string
	}{
		{"not object array", `[1]`, ParseOptions{}, "not_object"},
		{"not object garbage", `nope`, ParseOptions{}, "not_object"},
		{"missing id", `{"style":"s","lyrics":"l"}`, ParseOptions{}, "missing_id"},
		{"bad id traversal", `{"id":"../x","style":"s","lyrics":"l"}`, ParseOptions{}, "bad_id"},
		{"bad id dot", `{"id":".","style":"s","lyrics":"l"}`, ParseOptions{}, "bad_id"},
		{"bad id dotdot", `{"id":"..","style":"s","lyrics":"l"}`, ParseOptions{}, "bad_id"},
		{"bad id too long", `{"id":"` + longID + `","style":"s","lyrics":"l"}`, ParseOptions{}, "bad_id"},
		{"bad id non-string", `{"id":5,"style":"s","lyrics":"l"}`, ParseOptions{}, "bad_id"},
		{"missing lyrics", `{"id":"a","style":"s"}`, ParseOptions{}, "missing_lyrics"},
		{"missing style", `{"id":"a","lyrics":"l"}`, ParseOptions{}, "missing_style"},
		{"style tags mismatch", `{"id":"a","style":"s","tags":"t","lyrics":"l"}`, ParseOptions{}, "style_tags_mismatch"},
		{"bad cot none", `{"id":"a","style":"s","lyrics":"l","cot":"none"}`, ParseOptions{}, "bad_cot"},
		{"bad cot number", `{"id":"a","style":"s","lyrics":"l","cot":1}`, ParseOptions{}, "bad_cot"},
		{"seed float", `{"id":"a","style":"s","lyrics":"l","seed":1.5}`, ParseOptions{}, "seed_out_of_range"},
		{"seed string", `{"id":"a","style":"s","lyrics":"l","seed":"7"}`, ParseOptions{}, "seed_out_of_range"},
		{"seed negative", `{"id":"a","style":"s","lyrics":"l","seed":-1}`, ParseOptions{}, "seed_out_of_range"},
		{"seed exponent", `{"id":"a","style":"s","lyrics":"l","seed":1e3}`, ParseOptions{}, "seed_out_of_range"},
		{"seed 2^53", `{"id":"a","style":"s","lyrics":"l","seed":9007199254740992}`, ParseOptions{}, "seed_out_of_range"},
		{"seed huge", `{"id":"a","style":"s","lyrics":"l","seed":99999999999999999999999}`, ParseOptions{}, "seed_out_of_range"},
		{"seed fraction zero", `{"id":"a","style":"s","lyrics":"l","seed":7.0}`, ParseOptions{}, "seed_out_of_range"},
		{"cfg 21", `{"id":"a","style":"s","lyrics":"l","cfg_scale":21}`, ParseOptions{}, "bad_cfg_scale"},
		{"cfg negative", `{"id":"a","style":"s","lyrics":"l","cfg_scale":-0.5}`, ParseOptions{}, "bad_cfg_scale"},
		{"cfg string", `{"id":"a","style":"s","lyrics":"l","cfg_scale":"2"}`, ParseOptions{}, "bad_cfg_scale"},
		{"cfg overflow", `{"id":"a","style":"s","lyrics":"l","cfg_scale":1e999}`, ParseOptions{}, "bad_cfg_scale"},
		{"abc non-string", `{"id":"a","style":"s","lyrics":"l","abc":5}`, ParseOptions{}, "bad_abc"},
		{"abc empty", `{"id":"a","style":"s","lyrics":"l","abc":""}`, ParseOptions{}, "empty_abc"},
		{"abc blank", `{"id":"a","style":"s","lyrics":"l","abc":"  \n"}`, ParseOptions{}, "empty_abc"},
		{"abc with cot off", `{"id":"a","style":"s","lyrics":"l","cot":"off","abc":"X:1"}`, ParseOptions{}, "abc_requires_cot"},
		{"abc and abc_path", `{"id":"a","style":"s","lyrics":"l","abc":"X:1","abc_path":"a.abc"}`, ParseOptions{}, "abc_conflict"},
		{"abc_path nil resolver", `{"id":"a","style":"s","lyrics":"l","abc_path":"a.abc"}`, ParseOptions{}, "abc_path_unresolved"},
		{"abc_path missing target", `{"id":"a","style":"s","lyrics":"l","abc_path":"a.abc"}`, ParseOptions{ResolveABCPath: fakeResolver(nil)}, "abc_path_unresolved"},
		{"abc_path non-string", `{"id":"a","style":"s","lyrics":"l","abc_path":5}`, ParseOptions{ResolveABCPath: fakeResolver(nil)}, "abc_path_unresolved"},
		{"abc_path empty content", `{"id":"a","style":"s","lyrics":"l","abc_path":"a.abc"}`, ParseOptions{ResolveABCPath: fakeResolver(map[string]string{"a.abc": " "})}, "empty_abc"},
		{"prompt mismatch", `{"id":"a","style":"s","lyrics":"l","prompt":"hello"}`, ParseOptions{}, "prompt_mismatch"},
		{"abc_sampling", `{"id":"a","style":"s","lyrics":"l","abc_sampling":{"temperature":1}}`, ParseOptions{}, "sampling_unsupported"},
		{"semantic_sampling", `{"id":"a","style":"s","lyrics":"l","semantic_sampling":{}}`, ParseOptions{}, "sampling_unsupported"},
		{"bad utf8", "{\"id\":\"a\",\"style\":\"s\xff\",\"lyrics\":\"l\"}", ParseOptions{}, "bad_utf8"},
	}
	if len(rows) < 30 {
		t.Fatalf("need >= 30 rows, have %d", len(rows))
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, err := ParseRequest([]byte(r.raw), r.opt)
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v, want *ParseError %s", err, r.code)
			}
			if pe.Code != r.code {
				t.Fatalf("code = %q, want %q (detail %q)", pe.Code, r.code, pe.Detail)
			}
			// Detail must never echo field values.
			for _, secret := range []string{"hello", "../x"} {
				if strings.Contains(pe.Detail, secret) {
					t.Fatalf("detail leaks value: %q", pe.Detail)
				}
			}
		})
	}
}

func mustParse(t *testing.T, raw string, o ParseOptions) Parsed {
	t.Helper()
	p, err := ParseRequest([]byte(raw), o)
	if err != nil {
		t.Fatalf("ParseRequest(%s): %v", raw, err)
	}
	return p
}

func TestParseRequest_Canonical(t *testing.T) {
	p := mustParse(t, `{"style":"pop","seed":7,"lyrics":"[Verse]\nla","id":"syn-a","cot":"full"}`, ParseOptions{})
	const want = `{"cot":"full","id":"syn-a","lyrics":"[Verse]\nla","seed":7,"style":"pop"}`
	if string(p.Canonical) != want {
		t.Fatalf("canonical = %s", p.Canonical)
	}
	sum := sha256.Sum256([]byte(want))
	if p.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash mismatch")
	}
	if p.CoreHash != p.Hash {
		t.Fatalf("core hash should equal hash without cfg/abc")
	}
	golden := filepath.Join("testdata", "canonical.sha256")
	if *update {
		if err := os.WriteFile(golden, []byte(p.Hash+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != p.Hash {
		t.Fatalf("golden hash %q != %q", b, p.Hash)
	}
}

func TestParseRequest_SortedUnsortedSameHash(t *testing.T) {
	a := mustParse(t, `{"cot":"full","id":"x","lyrics":"l","seed":1,"style":"s"}`, ParseOptions{})
	b := mustParse(t, `{"style":"s", "seed": 1, "lyrics": "l", "id": "x", "cot": "full"}`, ParseOptions{})
	if a.Hash != b.Hash {
		t.Fatal("hash differs by key order/whitespace")
	}
}

func TestParseRequest_TagsAlias(t *testing.T) {
	a := mustParse(t, `{"id":"x","tags":"s","lyrics":"l"}`, ParseOptions{})
	b := mustParse(t, `{"id":"x","style":"s","lyrics":"l"}`, ParseOptions{})
	if a.Hash != b.Hash || !hasWarn(a, "tags_alias") {
		t.Fatalf("tags alias: %v", a.Warnings)
	}
	c := mustParse(t, `{"id":"x","style":"s","tags":"s","lyrics":"l"}`, ParseOptions{})
	if c.Hash != b.Hash {
		t.Fatal("equal style+tags should hash same")
	}
}

func TestParseRequest_Defaults(t *testing.T) {
	p := mustParse(t, `{"id":"x","style":"s","lyrics":"l"}`, ParseOptions{})
	if p.Request.Seed != DefaultSeed || p.Request.Cot != "full" {
		t.Fatalf("defaults: %+v", p.Request)
	}
	if !hasWarn(p, "seed_defaulted") || !hasWarn(p, "cot_defaulted") {
		t.Fatalf("warnings: %v", p.Warnings)
	}
	q := mustParse(t, `{"id":"x","style":"s","lyrics":"l","cot":"full","seed":831001}`, ParseOptions{})
	if q.Hash != p.Hash {
		t.Fatal("default and explicit differ")
	}
}

func TestParseRequest_SeedBoundary(t *testing.T) {
	p := mustParse(t, `{"id":"x","style":"s","lyrics":"l","seed":9007199254740991}`, ParseOptions{})
	if p.Request.Seed != MaxSeed {
		t.Fatal(p.Request.Seed)
	}
	z := mustParse(t, `{"id":"x","style":"s","lyrics":"l","seed":0}`, ParseOptions{})
	if z.Request.Seed != 0 {
		t.Fatal("seed 0")
	}
}

func TestParseRequest_YuE2RequestJSON(t *testing.T) {
	line := `{"cot": "full", "id": "syn-a", "lyrics": "[Verse]\nla", "seed": 7, "style": "pop"}`
	yue := "{\n  \"style\": \"pop\",\n  \"lyrics\": \"[Verse]\\nla\",\n  \"cot\": \"full\",\n  \"seed\": 7,\n  \"abc\": null,\n  \"cfg_scale\": null,\n  \"id\": \"syn-a\"\n}\n"
	a := mustParse(t, line, ParseOptions{})
	b := mustParse(t, yue, ParseOptions{})
	if a.Hash != b.Hash {
		t.Fatalf("hash differs:\n%s\n%s", a.Canonical, b.Canonical)
	}
	if b.Request.ABC != nil || b.Request.CFGScale != nil {
		t.Fatal("nulls must be absent")
	}
}

func TestParseRequest_CFG(t *testing.T) {
	base := `{"id":"x","style":"s","lyrics":"l",`
	rows := []struct {
		name string
		tail string
		keep bool
	}{
		{"1.0 full dropped", `"cot":"full","cfg_scale":1.0}`, false},
		{"1 full dropped", `"cot":"full","cfg_scale":1}`, false},
		{"1.01 off dropped", `"cot":"off","cfg_scale":1.01}`, false},
		{"1.0 off kept", `"cot":"off","cfg_scale":1.0}`, true},
		{"1.01 full kept", `"cot":"full","cfg_scale":1.01}`, true},
		{"3.5 kept", `"cot":"full","cfg_scale":3.5}`, true},
		{"0 kept", `"cot":"full","cfg_scale":0}`, true},
		{"20 kept", `"cot":"full","cfg_scale":20}`, true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			p := mustParse(t, base+r.tail, ParseOptions{})
			if (p.Request.CFGScale != nil) != r.keep {
				t.Fatalf("keep = %v", p.Request.CFGScale != nil)
			}
			if r.keep != !hasWarn(p, "cfg_scale_default_dropped") {
				t.Fatalf("warnings %v", p.Warnings)
			}
		})
	}
}

func TestParseRequest_CFGHashes(t *testing.T) {
	a := mustParse(t, `{"id":"x","style":"s","lyrics":"l","cfg_scale":3}`, ParseOptions{})
	b := mustParse(t, `{"id":"x","style":"s","lyrics":"l","cfg_scale":4}`, ParseOptions{})
	c := mustParse(t, `{"id":"x","style":"s","lyrics":"l"}`, ParseOptions{})
	if a.Hash == b.Hash {
		t.Fatal("different cfg must differ in Hash")
	}
	if a.CoreHash != b.CoreHash || a.CoreHash != c.CoreHash {
		t.Fatal("CoreHash must ignore cfg_scale")
	}
	if !strings.Contains(string(a.Canonical), `"cfg_scale":3`) {
		t.Fatalf("canonical: %s", a.Canonical)
	}
}

func TestParseRequest_ABC(t *testing.T) {
	res := fakeResolver(map[string]string{"a.abc": "X:1\nK:C\nCDEF|"})
	inline := mustParse(t, `{"id":"x","style":"s","lyrics":"l","abc":"X:1\nK:C\nCDEF|"}`, ParseOptions{})
	viaPath := mustParse(t, `{"id":"x","style":"s","lyrics":"l","abc_path":"a.abc"}`, ParseOptions{ResolveABCPath: res})
	if inline.Hash != viaPath.Hash {
		t.Fatal("abc_path must hash like inline abc")
	}
	if viaPath.ABCPath != "a.abc" || viaPath.Request.ABC == nil {
		t.Fatalf("ABCPath = %q", viaPath.ABCPath)
	}
	if inline.CoreHash == inline.Hash {
		t.Fatal("abc must not be part of CoreHash")
	}
	n := mustParse(t, `{"id":"x","style":"s","lyrics":"l","abc":null}`, ParseOptions{})
	if n.Request.ABC != nil {
		t.Fatal("null abc must be absent")
	}
}

func TestParseRequest_Prompt(t *testing.T) {
	prompt := promptFor("full", "pop", "[Verse]\nla")
	raw := `{"id":"x","style":"pop","lyrics":"[Verse]\nla","cot":"full","prompt":` + jsonString(prompt) + `}`
	p := mustParse(t, raw, ParseOptions{})
	if !hasWarn(p, "prompt_verified") {
		t.Fatalf("warnings %v", p.Warnings)
	}
	plain := mustParse(t, `{"id":"x","style":"pop","lyrics":"[Verse]\nla","cot":"full"}`, ParseOptions{})
	if plain.Hash != p.Hash {
		t.Fatal("prompt must not change hash")
	}
	// Prompt for another cot is a mismatch.
	bad := `{"id":"x","style":"pop","lyrics":"[Verse]\nla","cot":"off","prompt":` + jsonString(prompt) + `}`
	if _, err := ParseRequest([]byte(bad), ParseOptions{}); err == nil {
		t.Fatal("want prompt_mismatch")
	}
	// null prompt is ignored.
	mustParse(t, `{"id":"x","style":"s","lyrics":"l","prompt":null}`, ParseOptions{})
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\n':
			b.WriteString(`\n`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestParseRequest_DroppedKeys(t *testing.T) {
	base := mustParse(t, `{"id":"x","style":"s","lyrics":"l"}`, ParseOptions{})
	t1 := mustParse(t, `{"id":"x","style":"s","lyrics":"l","title":"T"}`, ParseOptions{})
	if !hasDrop(t1, "title") || !hasWarn(t1, "dropped_key:title") || t1.Title != "" || t1.Hash != base.Hash {
		t.Fatalf("title w/o AllowTitle: %+v", t1)
	}
	t2 := mustParse(t, `{"id":"x","style":"s","lyrics":"l","title":"T"}`, ParseOptions{AllowTitle: true})
	if t2.Title != "T" || hasDrop(t2, "title") || t2.Hash != base.Hash {
		t.Fatalf("title with AllowTitle: %+v", t2)
	}
	for _, k := range []string{"lang", "eval_index", "clip_id", "whatever"} {
		p := mustParse(t, `{"id":"x","style":"s","lyrics":"l","`+k+`":1}`, ParseOptions{})
		if !hasDrop(p, k) || !hasWarn(p, "dropped_key:"+k) || p.Hash != base.Hash {
			t.Fatalf("%s: dropped=%v", k, p.Dropped)
		}
	}
}

func TestParseRequest_BOMAndCRLF(t *testing.T) {
	raw := "\xef\xbb\xbf{\"id\":\"x\",\r\n\"style\":\"s\",\"lyrics\":\"l\"}\r\n"
	p := mustParse(t, raw, ParseOptions{})
	if !hasWarn(p, "bom") || !hasWarn(p, "crlf") {
		t.Fatalf("warnings %v", p.Warnings)
	}
}

func TestCanonicalJSON_NoHTMLEscape(t *testing.T) {
	got := string(CanonicalJSON(Request{ID: "a", Style: "a<b>&c", Lyrics: "é", Cot: "off", Seed: 1}))
	want := `{"cot":"off","id":"a","lyrics":"é","seed":1,"style":"a<b>&c"}`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}
