package money

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

func TestMoney(t *testing.T) {
	t.Parallel()
	eq := func(name string, got, want MicroUSD) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
	eq("ForSeconds(1)", ForSeconds(1, 690000), 192)
	eq("ForSeconds(186)", ForSeconds(186, 690000), 35650)
	eq("ForSeconds(400)", ForSeconds(400, 690000), 76667)
	eq("ForSeconds(201)", ForSeconds(201, 690000), 38525)
	eq("ForSeconds(200)", ForSeconds(200, 690000), 38334)
	eq("ForSeconds(0)", ForSeconds(0, 690000), 0)
	eq("ForMillis(95040)", ForMillis(95040, 690000), 18216)
	eq("ForMillis(1)", ForMillis(1, 690000), 1)
	eq("WithMarginPPM", WithMarginPPM(690000, 100000), 759000)
	eq("WithMarginPPM(0 ppm)", WithMarginPPM(690000, 0), 690000)

	for _, tc := range []struct {
		in   string
		want MicroUSD
	}{
		{"0.69", 690000}, {"0.0001916", 192}, {"1.5e-3", 1500}, {"12", 12000000},
		{"0", 0}, {".5", 500000}, {"0.000001", 1}, {"0.0000011", 2}, {"1E2", 100000000},
		{"0.00000000000000000000001", 1}, {"0.000000", 0},
	} {
		got, err := ParseUSD(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseUSD(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"-1", "abc", "", ".", "1.2.3", "1e", "--1", "0x10", "1 0", "99999999999999999999", "1e30", "-0.5"} {
		if v, err := ParseUSD(in); err == nil {
			t.Errorf("ParseUSD(%q) = %d, want error", in, v)
		}
	}
	for _, tc := range []struct {
		in   string
		want MicroUSD
	}{{"-0.000001", -1}, {"-1", -1000000}, {"0.5", 500000}, {"+0.5", 500000}, {"-0.0000015", -1}} {
		got, err := ParseUSDSigned(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseUSDSigned(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
	if _, err := ParseUSDSigned("abc"); err == nil {
		t.Error("ParseUSDSigned(abc): want error")
	}

	for _, tc := range []struct {
		m    MicroUSD
		want string
	}{{326217, "0.326217"}, {690000, "0.69"}, {0, "0"}, {-5, "-0.000005"}, {12000000, "12"}, {1, "0.000001"}, {1500000, "1.5"}} {
		if got := tc.m.USD(); got != tc.want {
			t.Errorf("(%d).USD() = %q, want %q", tc.m, got, tc.want)
		}
	}
	for _, tc := range []struct {
		m    MicroUSD
		want string
	}{{326217, "$0.33"}, {5800, "$0.0058"}, {10000, "$0.01"}, {0, "$0.00"}, {9999, "$0.0100"}, {-326217, "-$0.33"}, {12000000, "$12.00"}} {
		if got := tc.m.Display(); got != tc.want {
			t.Errorf("(%d).Display() = %q, want %q", tc.m, got, tc.want)
		}
	}
}

func TestMoney_DisplayCeil(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		m    MicroUSD
		want string
	}{{324999, "$0.33"}, {691731, "$0.70"}, {5801, "$0.0059"}, {0, "$0.00"}, {330000, "$0.33"}, {1, "$0.0001"}} {
		if got := tc.m.DisplayCeil(); got != tc.want {
			t.Errorf("(%d).DisplayCeil() = %q, want %q", tc.m, got, tc.want)
		}
	}
	if got := MicroUSD(324999).Display(); got != "$0.32" {
		t.Errorf("half-up Display(324999) = %q, want $0.32", got)
	}
}

func TestMoney_Overflow(t *testing.T) {
	t.Parallel()
	for name, f := range map[string]func(){
		"ForSeconds big":   func() { ForSeconds(1<<62, 1<<62) },
		"ForSeconds neg":   func() { ForSeconds(-1, 1) },
		"ForMillis big":    func() { ForMillis(1<<62, 1<<62) },
		"ForMillis result": func() { ForMillis(1<<62, 3_600_000*4) },
		"WithMargin big":   func() { WithMarginPPM(1<<62, 1<<62) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}

type sharedCases struct {
	Display []struct {
		Micro int64  `json:"micro"`
		Want  string `json:"display"`
		Ceil  string `json:"display_ceil"`
		USD   string `json:"usd"`
	} `json:"format"`
	Parse []struct {
		In    string `json:"in"`
		Micro *int64 `json:"micro"` // null ⇒ refused
	} `json:"parse"`
}

// TestMoney_SharedCases reads the table web/src/lib/money.cases.json, which
// money.test.ts reads too: Go and TypeScript must agree on every row.
func TestMoney_SharedCases(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "lib", "money.cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c sharedCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Display) < 8 || len(c.Parse) < 8 {
		t.Fatalf("table too small: %d/%d", len(c.Display), len(c.Parse))
	}
	for _, r := range c.Display {
		m := MicroUSD(r.Micro)
		if m.Display() != r.Want || m.DisplayCeil() != r.Ceil || m.USD() != r.USD {
			t.Errorf("%d: got %q %q %q, want %q %q %q", r.Micro, m.Display(), m.DisplayCeil(), m.USD(), r.Want, r.Ceil, r.USD)
		}
	}
	for _, r := range c.Parse {
		got, err := ParseUSD(r.In)
		switch {
		case r.Micro == nil && err == nil:
			t.Errorf("ParseUSD(%q) = %d, want refusal", r.In, got)
		case r.Micro != nil && (err != nil || int64(got) != *r.Micro):
			t.Errorf("ParseUSD(%q) = %d, %v; want %d", r.In, got, err, *r.Micro)
		}
	}
}

// TestMoney_NoFloat asserts the package never mentions float32 or float64.
func TestMoney_NoFloat(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 2 {
		t.Fatalf("glob: %v %v", files, err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && (id.Name == "float32" || id.Name == "float64") {
				t.Errorf("%s: %s", name, id.Name)
			}
			if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.FLOAT {
				t.Errorf("%s: float literal %s", name, bl.Value)
			}
			return true
		})
	}
}
