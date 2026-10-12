package export

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

func loadInputs(t *testing.T) []yue2.Request {
	t.Helper()
	b, err := os.ReadFile("testdata/pyjson.inputs.json")
	if err != nil {
		t.Fatal(err)
	}
	var rs []yue2.Request
	if err := json.Unmarshal(b, &rs); err != nil {
		t.Fatal(err)
	}
	return rs
}

func loadGolden(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/pyjson.golden.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "#") {
			continue
		}
		out = append(out, sc.Text())
	}
	return out
}

var designOrder = []string{"id", "lyrics", "style", "seed", "cot"}

// Cases in the order the CPython generator wrote them (see the golden file header).
func styles() []Style {
	var out []Style
	for _, order := range [][]string{nil, designOrder} {
		for _, seps := range [][2]string{{", ", ": "}, {",", ":"}} {
			for _, ea := range []bool{true, false} {
				out = append(out, Style{KeyOrder: order, ItemSep: seps[0], KeySep: seps[1], EnsureASCII: ea})
			}
		}
	}
	return out
}

func TestPyJSON_Golden(t *testing.T) {
	ins, gold := loadInputs(t), loadGolden(t)
	st := styles()
	if len(gold) != len(ins)*len(st) {
		t.Fatalf("golden has %d lines, want %d", len(gold), len(ins)*len(st))
	}
	for i, r := range ins {
		for j, s := range st {
			got := string(EncodeRequest(r, s))
			if want := gold[i*len(st)+j]; got != want {
				t.Errorf("input %d (%s) style %d %+v:\n got %s\nwant %s", i, r.ID, j, s, got, want)
			}
		}
	}
}

func TestPyJSON_ParsesBack(t *testing.T) {
	for _, r := range loadInputs(t) {
		for _, s := range styles() {
			line := EncodeRequest(r, s)
			var back yue2.Request
			if err := json.Unmarshal(line, &back); err != nil {
				t.Fatalf("%s: %v\n%s", r.ID, err, line)
			}
			if d := cmp.Diff(r, back); d != "" {
				t.Fatalf("%s round trip: %s", r.ID, d)
			}
		}
	}
}

func TestPyFloat(t *testing.T) {
	for in, want := range map[float64]string{1.5: "1.5", 2: "2.0", 0.00001: "1e-05", 0.0001: "0.0001", 20: "20.0",
		1e16: "1e+16", 1e15: "1000000000000000.0", 1.25e-7: "1.25e-07", 0: "0.0", 123456.789: "123456.789"} {
		if got := PyFloat(in); got != want {
			t.Errorf("PyFloat(%v) = %s, want %s", in, got, want)
		}
	}
}
