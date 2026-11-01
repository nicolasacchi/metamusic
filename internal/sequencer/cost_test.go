package sequencer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestCost_Tempo(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		a, b int
		want int64
	}{
		{7600, 15200, 300_000}, {7600, 8000, 263_155}, {7600, 9500, 1_000_000}, {12000, 12500, 208_330},
		{9600, 10800, 625_000}, {7600, 7600, 0}, {7600, 8400, 526_315}, {0, 9000, 500_000}, {9000, 0, 500_000}, {0, 0, 500_000},
	} {
		if got := tempoMicro(c.a, c.b); got != c.want {
			t.Errorf("tempo(%d,%d) = %d, want %d", c.a, c.b, got, c.want)
		}
		if tempoMicro(c.a, c.b) != tempoMicro(c.b, c.a) {
			t.Errorf("tempo(%d,%d) not symmetric", c.a, c.b)
		}
	}
}

func TestCost_Key(t *testing.T) {
	t.Parallel()
	k := func(n int, l byte) *Camelot { return &Camelot{n, l} }
	for _, c := range []struct {
		a, b *Camelot
		want int64
		rel  string
	}{
		{k(8, 'A'), k(8, 'A'), 0, "same"}, {k(8, 'A'), k(8, 'B'), 100_000, "relative"}, {k(8, 'A'), k(9, 'A'), 150_000, "adjacent"},
		{k(8, 'A'), k(10, 'A'), 450_000, "boost"}, {k(12, 'A'), k(1, 'A'), 150_000, "adjacent"}, {k(8, 'A'), k(9, 'B'), 500_000, "diagonal"},
		{k(8, 'A'), k(11, 'B'), 1_000_000, "clash"}, {k(8, 'A'), k(11, 'A'), 700_000, "clash"}, {k(8, 'A'), k(12, 'A'), 1_000_000, "clash"},
		{nil, k(8, 'A'), 500_000, "unknown"}, {k(8, 'A'), nil, 500_000, "unknown"}, {k(0, 'A'), k(8, 'A'), 500_000, "unknown"},
		{k(8, 'C'), k(8, 'A'), 500_000, "unknown"},
	} {
		if got := keyMicro(c.a, c.b); got != c.want {
			t.Errorf("key(%v,%v) = %d, want %d", c.a, c.b, got, c.want)
		}
		if rel, _ := keyFacts(c.a, c.b); rel != c.rel {
			t.Errorf("relation(%v,%v) = %s, want %s", c.a, c.b, rel, c.rel)
		}
		if keyMicro(c.a, c.b) != keyMicro(c.b, c.a) {
			t.Errorf("key(%v,%v) not symmetric", c.a, c.b)
		}
	}
}

func TestCost_Energy(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		a, b int
		want int64
	}{
		{2000, 3000, 250_000}, {3000, 1000, 500_000}, {0, 4000, 1_000_000}, {0, 5000, 1_000_000}, {10000, 0, 1_000_000},
		{NoEnergy, 5000, 500_000}, {5000, NoEnergy, 500_000}, {5000, 5000, 0},
	} {
		if got := energyMicro(c.a, c.b); got != c.want {
			t.Errorf("energy(%d,%d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCost_Shape(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		e, tgt int
		want   int64
	}{{5000, 5000, 0}, {0, 5000, 1_000_000}, {10000, 5000, 1_000_000}, {NoEnergy, 5000, 250_000}, {4000, 5000, 40_000}, {0, 10000, 1_000_000}} {
		if got := shapeMicro(c.e, c.tgt); got != c.want {
			t.Errorf("shape(%d,%d) = %d, want %d", c.e, c.tgt, got, c.want)
		}
	}
}

func TestTarget_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		s    Strategy
		n    int
		want []int
	}{
		{StrategyArc, 6, []int{3000, 5000, 7000, 9000, 8334, 5000}},
		{StrategyRampUp, 6, []int{0, 2000, 4000, 6000, 8000, 10000}},
		{StrategySteady, 3, []int{-1, -1, -1}},
		{StrategyArc, 1, []int{5000}}, {StrategyRampUp, 1, []int{5000}},
	} {
		var got []int
		for p := range c.n {
			got = append(got, targetFor(c.s, p, c.n))
		}
		if len(got) != len(c.want) {
			t.Fatalf("%s n=%d: %v", c.s, c.n, got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s n=%d: target = %v, want %v", c.s, c.n, got, c.want)
				break
			}
		}
	}
	if targetFor(StrategySteady, 0, 1) != -1 {
		t.Error("steady has a shape with n = 1")
	}
}

// The T set of the plan fixture (synthetic), in the shuffled input order.
func tSet() []Song {
	mk := func(id string, bpm, en int, n int, l byte) Song {
		return Song{ID: id, BPMx100: bpm, Energyx10000: en, Key: &Camelot{n, l}}
	}
	return []Song{
		mk("t4", 10400, 7000, 9, 'A'), mk("t6", 10200, 4000, 8, 'B'), mk("t1", 9600, 3000, 8, 'A'),
		mk("t5", 10800, 9000, 9, 'B'), mk("t3", 10000, 5500, 9, 'A'), mk("t2", 9800, 4500, 8, 'A'),
	}
}

func pick(ss []Song, ids ...string) []Song {
	var out []Song
	for _, id := range ids {
		for _, s := range ss {
			if s.ID == id {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestObjective_TSet(t *testing.T) {
	t.Parallel()
	ts := tSet()
	for _, c := range []struct {
		name  string
		s     Strategy
		order []string
		want  int64 // J in units of 1e-10, rounded to 4 decimals for all but the first row
	}{
		{"arc input", StrategyArc, []string{"t4", "t6", "t1", "t5", "t3", "t2"}, 89_847_130_000},
		{"arc output", StrategyArc, []string{"t1", "t2", "t3", "t4", "t5", "t6"}, 55_198_370_000},
		{"ramp output", StrategyRampUp, []string{"t1", "t6", "t2", "t3", "t4", "t5"}, 25_712_000_000},
		{"steady input", StrategySteady, []string{"t4", "t6", "t1", "t5", "t3", "t2"}, 56_840_000_000},
		{"steady output", StrategySteady, []string{"t1", "t6", "t2", "t3", "t4", "t5"}, 33_709_000_000},
		{"arc endpoints", StrategyArc, []string{"t5", "t4", "t3", "t2", "t6", "t1"}, 92_694_000_000},
	} {
		spec, _ := SpecOf(c.s)
		got := objective(pick(ts, c.order...), spec)
		if c.name == "arc input" || c.name == "arc output" {
			if got != c.want {
				t.Errorf("%s: J = %d, want %d", c.name, got, c.want)
			}
			continue
		}
		if (got+500_000)/1_000_000 != c.want/1_000_000 {
			t.Errorf("%s: J = %d, want about %d", c.name, got, c.want)
		}
	}
}

func TestRelEnergy_And_Coverage(t *testing.T) {
	t.Parallel()
	ss := []Song{{ID: "a", Energyx10000: 1000}, {ID: "b", Energyx10000: NoEnergy}, {ID: "c", Energyx10000: 3000}}
	if got := relEnergies(ss); got[0] != 0 || got[1] != NoEnergy || got[2] != 10000 {
		t.Errorf("rel = %v", got)
	}
	flat := []Song{{Energyx10000: 4000}, {Energyx10000: 4400}}
	if got := relEnergies(flat); got[0] != 5000 || got[1] != 5000 {
		t.Errorf("flat rel = %v", got)
	}
	if c := coverageOf(tSet()[:3]); c != (Coverage{100, 100, 100}) {
		t.Errorf("coverage = %+v", c)
	}
	six := tSet()
	six[0].Key, six[1].Key = nil, nil
	if c := coverageOf(six); c.Key != 66 || c.Tempo != 100 {
		t.Errorf("coverage = %+v, want key 66", c)
	}
}

func TestDistance_Steady(t *testing.T) {
	t.Parallel()
	a, b := tSet()[2], tSet()[5] // t1, t2
	want := int64(100*100)*tempoMicro(9600, 9800) + int64(80*100)*keyMicro(a.Key, b.Key) + int64(80*100)*energyMicro(3000, 4500)
	if got := Distance(a, b); got != want || got == 0 {
		t.Errorf("Distance = %d, want %d", got, want)
	}
	if Distance(a, b) != Distance(b, a) {
		t.Error("Distance not symmetric")
	}
	if Distance(a, a) != 0 {
		t.Error("Distance(a,a) != 0")
	}
	if p := TransitionMicro(a, b); p.Tempo != tempoMicro(9600, 9800) || p.Key != 0 || p.Energy != 375_000 {
		t.Errorf("parts = %+v", p)
	}
}

// TestSequencer_NoFloatInCost keeps floating point out of the cost model (C137.3).
func TestSequencer_NoFloatInCost(t *testing.T) {
	t.Parallel()
	for _, f := range []string{"cost.go", "target.go", "strategies.go", "digest.go", "explain.go", "plan.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			if strings.Contains(err.Error(), "no such file") {
				continue
			}
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if strings.Contains(strings.ToLower(x.Name), "float") {
					t.Errorf("%s: float identifier %s", f, x.Name)
				}
			case *ast.BasicLit:
				if x.Kind == token.FLOAT {
					t.Errorf("%s: float literal %s", f, x.Value)
				}
			}
			return true
		})
	}
}
