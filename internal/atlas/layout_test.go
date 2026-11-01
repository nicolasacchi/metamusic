package atlas_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if d := cmp.Diff(string(want), string(got)); d != "" {
		t.Errorf("%s differs (-want +got):\n%s", name, d)
	}
}

func ax(t *testing.T, name string) atlas.AxisDef {
	t.Helper()
	a, ok := atlas.Axis(name)
	if !ok {
		t.Fatalf("no axis %q", name)
	}
	return a
}

func TestAtlasAxes_Registry(t *testing.T) {
	t.Parallel()
	golden(t, "axes_v1.golden.json", atlas.Axes)
	seen := map[string]bool{}
	for _, a := range atlas.Axes {
		if seen[a.Name] {
			t.Errorf("duplicate axis %s", a.Name)
		}
		seen[a.Name] = true
		switch a.Kind {
		case atlas.KindNumeric:
			if a.Max <= a.Min || len(a.Order) != 0 {
				t.Errorf("%s: bad numeric definition", a.Name)
			}
		case atlas.KindCategorical:
			if len(a.Order) == 0 || a.Scale != "" {
				t.Errorf("%s: bad categorical definition", a.Name)
			}
		}
	}
	// the categorical orders are subsets of the L1 seed values, in seed order
	byFacet := map[string][]string{"family": nil, "vocal": nil, "era": nil, "mood": nil, "language": nil}
	facetOf := map[string]string{"genre_family": "family", "vocal": "vocal", "era": "era", "mood": "mood", "language": "language"}
	for _, st := range sounds.SeedV1() {
		if f, ok := facetOf[st.Facet]; ok && !slices.Contains(byFacet[f], st.Value) {
			byFacet[f] = append(byFacet[f], st.Value)
		}
	}
	for f, vals := range byFacet {
		a := ax(t, f)
		for _, v := range a.Order {
			if !slices.Contains(vals, v) {
				t.Errorf("%s order has %q, not a seed value", f, v)
			}
		}
		if len(a.Order) != len(vals) {
			t.Errorf("%s order has %d values, seed has %d", f, len(a.Order), len(vals))
		}
	}
	if k := ax(t, "key"); len(k.Order) != 24 || k.Order[0] != "1A" || k.Order[1] != "1B" || k.Order[23] != "12B" {
		t.Errorf("key order = %v", k.Order)
	}
	if _, ok := atlas.Axis("nope"); ok {
		t.Error("unknown axis found")
	}
}

func TestIlog2_AgainstMath(t *testing.T) {
	t.Parallel()
	for v := 6000; v <= 20000; v++ {
		got := atlas.Ilog2Q16(v)
		want := int(math.Round(math.Log2(float64(v)) * 65536))
		if d := got - want; d < -2 || d > 2 {
			t.Fatalf("ilog2q16(%d) = %d, math says %d", v, got, want)
		}
	}
	for _, v := range []int{1, 2, 3, 1 << 20, 1<<31 + 5, 1 << 40} {
		got := atlas.Ilog2Q16(v)
		want := int(math.Round(math.Log2(float64(v)) * 65536))
		if d := got - want; d < -2 || d > 2 {
			t.Errorf("ilog2q16(%d) = %d, math says %d", v, got, want)
		}
	}
	if atlas.Ilog2Q16(0) != 0 || atlas.Ilog2Q16(-4) != 0 {
		t.Error("non-positive input must give 0")
	}
}

func TestIsqrt(t *testing.T) {
	t.Parallel()
	for _, v := range []int64{0, 1, 2, 3, 4, 15, 16, 17, 1 << 32, 99 << 32, math.MaxInt64} {
		r := atlas.Isqrt(v)
		if r*r > v && v > 0 || (r+1) < 3037000500 && (r+1)*(r+1) <= v {
			t.Errorf("isqrt(%d) = %d", v, r)
		}
	}
	if atlas.Isqrt(-5) != 0 {
		t.Error("isqrt of a negative is 0")
	}
}

func TestSpiralTable_AgainstMath(t *testing.T) {
	t.Parallel()
	for d := range 360 {
		c, s := atlas.SpiralEntry(d)
		a := float64(d) * math.Pi / 180
		if math.Abs(float64(c)-math.Cos(a)*65536) > 1 || math.Abs(float64(s)-math.Sin(a)*65536) > 1 {
			t.Errorf("degree %d: (%d, %d)", d, c, s)
		}
	}
}

func TestLayout_Normalise(t *testing.T) {
	t.Parallel()
	tempo, energy, dur := ax(t, "tempo"), ax(t, "energy"), ax(t, "duration")
	for _, c := range []struct {
		axis atlas.AxisDef
		v    int
		want float64
	}{
		{tempo, 6000, 0}, {tempo, 20000, 1}, {tempo, 12000, 0.5757}, {tempo, 7600, 0.1963},
		{tempo, 100, 0}, {tempo, 99999, 1}, // clamped
		{energy, 6500, 0.65}, {energy, 0, 0}, {energy, 10000, 1}, {energy, -5, 0},
		{dur, 21000, 0.5}, {dur, 100, 0}, {dur, 99999, 1},
	} {
		// a single point sits exactly at its normalised coordinate on the x axis (y is the energy mid-point)
		got := atlas.Layout([]atlas.Input{{ID: "a", XV: c.v, YV: 5000}}, c.axis, energy)
		if got[0].X != c.want || got[0].Y != 0.5 {
			t.Errorf("%s %d = %v, want x %v", c.axis.Name, c.v, got[0], c.want)
		}
	}
}

func TestLayout_CategoricalBands(t *testing.T) {
	t.Parallel()
	vocal, energy := ax(t, "vocal"), ax(t, "energy")
	// three present categories in fixed order, plus (none) last; input order is scrambled on purpose
	pts := []atlas.Input{
		{ID: "d", XC: "", YV: 100}, {ID: "c", XC: "rap", YV: 100}, {ID: "a", XC: "male", YV: 100}, {ID: "b", XC: "female", YV: 100},
	}
	got := atlas.Layout(pts, vocal, energy)
	want := map[string]float64{"a": 0.125, "b": 0.375, "c": 0.625, "d": 0.875}
	for i, p := range pts {
		if got[i].X != want[p.ID] {
			t.Errorf("%s: x = %v, want %v", p.ID, got[i].X, want[p.ID])
		}
	}
	// exactly the three present categories
	got = atlas.Layout(pts[1:], vocal, energy)
	for i, w := range []float64{0.8333, 0.1667, 0.5} { // rap, male, female -> bands 2, 0, 1 of 3
		if got[i].X != w {
			t.Errorf("point %d: x = %v, want %v", i, got[i].X, w)
		}
	}
	// unknown values share the (none) band
	got = atlas.Layout([]atlas.Input{{ID: "a", XC: "male"}, {ID: "b", XC: "kazoo"}}, vocal, vocal)
	if got[0].X >= got[1].X {
		t.Errorf("unknown value must sort after known: %v", got)
	}
}

func TestLayout_Collisions(t *testing.T) {
	t.Parallel()
	tempo, energy := ax(t, "tempo"), ax(t, "energy")
	mk := func() []atlas.Input {
		pts := make([]atlas.Input, 100)
		for i := range pts {
			pts[i] = atlas.Input{ID: fmt.Sprintf("s%03d", i), XV: 12000, YV: 6500}
		}
		return pts
	}
	pts := mk()
	base := atlas.Layout(pts, tempo, energy)
	seen := map[atlas.XY]bool{}
	for i, p := range base {
		if seen[p] {
			t.Fatalf("duplicate position %v at %d", p, i)
		}
		seen[p] = true
		if d := math.Hypot(p.X-0.5757, p.Y-0.65); d > 0.036 {
			t.Errorf("point %d is %.4f from the exact value", i, d)
		}
	}
	if base[0] != (atlas.XY{X: 0.5757, Y: 0.65}) {
		t.Errorf("first member must not be offset: %v", base[0])
	}
	// order independent: shuffle the input, every id keeps its position
	byID := map[string]atlas.XY{}
	for i, p := range pts {
		byID[p.ID] = base[i]
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 5 {
		sh := mk()
		rng.Shuffle(len(sh), func(i, j int) { sh[i], sh[j] = sh[j], sh[i] })
		got := atlas.Layout(sh, tempo, energy)
		for i, p := range sh {
			if got[i] != byID[p.ID] {
				t.Fatalf("%s moved with input order: %v vs %v", p.ID, got[i], byID[p.ID])
			}
		}
	}
	if !reflect.DeepEqual(base, atlas.Layout(mk(), tempo, energy)) {
		t.Error("layout is not deterministic across runs")
	}
	// the same holds in the categorical and mixed arrangements
	for _, pair := range [][2]string{{"family", "vocal"}, {"family", "energy"}, {"tempo", "key"}} {
		x, y := ax(t, pair[0]), ax(t, pair[1])
		cp := mk()
		for i := range cp {
			cp[i].XC, cp[i].YC = "pop", "male"
			if pair[1] == "key" {
				cp[i].YC = "8A"
			}
		}
		out := atlas.Layout(cp, x, y)
		u := map[atlas.XY]bool{}
		for _, p := range out {
			if p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
				t.Fatalf("%v outside [0,1]", p)
			}
			u[p] = true
		}
		if len(u) != 100 {
			t.Errorf("%v: %d distinct positions of 100", pair, len(u))
		}
	}
}

func TestLayout_EdgesStayInside(t *testing.T) {
	t.Parallel()
	tempo, energy := ax(t, "tempo"), ax(t, "energy")
	var pts []atlas.Input
	for _, c := range [][2]int{{6000, 0}, {20000, 10000}, {6000, 10000}, {20000, 0}} {
		for i := range 60 {
			pts = append(pts, atlas.Input{ID: fmt.Sprintf("%d-%d-%02d", c[0], c[1], i), XV: c[0], YV: c[1]})
		}
	}
	for i, p := range atlas.Layout(pts, tempo, energy) {
		if p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
			t.Fatalf("point %d at %v is outside the unit square", i, p)
		}
	}
}

func TestLayout_EmptyAndSingle(t *testing.T) {
	t.Parallel()
	tempo, energy := ax(t, "tempo"), ax(t, "energy")
	if got := atlas.Layout(nil, tempo, energy); got == nil || len(got) != 0 {
		t.Errorf("empty = %#v, want a non-nil empty slice", got)
	}
}

// seeded builders for the goldens: synthetic ids and values only
func ids(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%04d", prefix, i)
	}
	return out
}

type gp struct {
	ID   string  `json:"id"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	XVal string  `json:"xv"`
	YVal string  `json:"yv"`
}

func render(pts []atlas.Input, x, y atlas.AxisDef) []gp {
	out := atlas.Layout(pts, x, y)
	res := make([]gp, len(pts))
	val := func(a atlas.AxisDef, v int, c string) string {
		if a.Kind == atlas.KindNumeric {
			return strconv.Itoa(v)
		}
		return c
	}
	for i, p := range pts {
		res[i] = gp{p.ID, out[i].X, out[i].Y, val(x, p.XV, p.XC), val(y, p.YV, p.YC)}
	}
	return res
}

func fixtures(t *testing.T) map[string][]gp {
	t.Helper()
	rng := rand.New(rand.NewPCG(7, 11))
	tempo, energy := ax(t, "tempo"), ax(t, "energy")
	fx := map[string][]gp{}

	var a []atlas.Input // tempo x energy with duplicates
	for i, id := range ids("t", 40) {
		a = append(a, atlas.Input{ID: id, XV: 7000 + (i%6)*1500, YV: 2000 + (i%4)*2000})
	}
	fx["layout_tempo_energy.golden.json"] = render(a, tempo, energy)

	fam, voc := ax(t, "family"), ax(t, "vocal")
	var b []atlas.Input
	for _, id := range ids("f", 30) {
		b = append(b, atlas.Input{ID: id, XC: fam.Order[rng.IntN(4)], YC: []string{"male", "female", "rap", ""}[rng.IntN(4)]})
	}
	fx["layout_family_vocal.golden.json"] = render(b, fam, voc)

	req, got := ax(t, "bpm_requested"), ax(t, "bpm_measured")
	var c []atlas.Input
	for _, id := range ids("r", 24) {
		r := 6000 + rng.IntN(14000)
		m := r
		switch rng.IntN(3) {
		case 1:
			m = min(r*2, 20000)
		case 2:
			m = r + rng.IntN(800)
		}
		c = append(c, atlas.Input{ID: id, XV: r, YV: m})
	}
	fx["layout_requested_got.golden.json"] = render(c, req, got)

	key := ax(t, "key")
	var d []atlas.Input
	for _, id := range ids("k", 36) {
		d = append(d, atlas.Input{ID: id, XC: key.Order[rng.IntN(5)*2+rng.IntN(2)], YV: rng.IntN(10001)})
	}
	fx["layout_key_energy.golden.json"] = render(d, key, energy)

	fx["layout_single.golden.json"] = render([]atlas.Input{{ID: "only", XV: 9000, YV: 4000}}, tempo, energy)
	fx["layout_empty.golden.json"] = render(nil, tempo, energy)
	return fx
}

func TestLayout_Goldens(t *testing.T) {
	t.Parallel()
	fx := fixtures(t)
	if len(fx) != 6 {
		t.Fatalf("%d goldens, want 6", len(fx))
	}
	for name, v := range fx {
		golden(t, name, v)
	}
}

func TestLayout_DenseStacks(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(3, 5))
	tempo, energy := ax(t, "tempo"), ax(t, "energy")
	xs := make([]int, 59)
	for i := range xs {
		xs[i] = 6500 + i*230
	}
	// predicted energy is a function of three facets: about a dozen distinct y values
	ys := make([]int, 12)
	for i := range ys {
		ys[i] = 1500 + rng.IntN(7000)
	}
	var pts []atlas.Input
	for _, id := range ids("d", 456) {
		pts = append(pts, atlas.Input{ID: id, XV: xs[rng.IntN(len(xs))], YV: ys[rng.IntN(len(ys))]})
	}
	out := atlas.Layout(pts, tempo, energy)
	seen := map[atlas.XY]bool{}
	for i, p := range out {
		if seen[p] {
			t.Fatalf("point %d collides at %v", i, p)
		}
		seen[p] = true
		if p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
			t.Fatalf("point %d outside [0,1]: %v", i, p)
		}
		ex := atlas.Layout([]atlas.Input{{ID: "x", XV: pts[i].XV, YV: pts[i].YV}}, tempo, energy)[0]
		if d := math.Hypot(p.X-ex.X, p.Y-ex.Y); d > 0.03 {
			t.Errorf("point %d is %.4f from its exact position", i, d)
		}
	}
	res := make([]gp, len(pts))
	for i, p := range pts {
		res[i] = gp{p.ID, out[i].X, out[i].Y, strconv.Itoa(p.XV), strconv.Itoa(p.YV)}
	}
	golden(t, "layout_dense.golden.json", res)
}

func TestAtlas_NoFloatInLayout(t *testing.T) {
	t.Parallel()
	for _, f := range []string{"layout.go", "ilog2.go", "spiral_table.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "math" {
				t.Errorf("%s imports math", f)
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				if n.Name == "float32" || n.Name == "float64" {
					t.Errorf("%s uses %s", f, n.Name)
				}
			case *ast.BasicLit:
				if n.Kind == token.FLOAT || n.Kind == token.IMAG {
					t.Errorf("%s has the literal %s", f, n.Value)
				}
			case *ast.SelectorExpr:
				if id, ok := n.X.(*ast.Ident); ok && id.Name == "math" {
					t.Errorf("%s calls math.%s", f, n.Sel.Name)
				}
			}
			return true
		})
	}
}

func TestPerf_AtlasLayout(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 9))
	tempo, energy := ax(t, "tempo"), ax(t, "energy")
	pts := make([]atlas.Input, atlas.MaxScope)
	for i, id := range ids("p", len(pts)) {
		pts[i] = atlas.Input{ID: id, XV: 6000 + rng.IntN(14001), YV: rng.IntN(10001)}
	}
	start := time.Now()
	atlas.Layout(pts, tempo, energy)
	if d := time.Since(start); d > time.Second {
		t.Errorf("layout of %d points took %v, budget 1s", len(pts), d)
	}
}
