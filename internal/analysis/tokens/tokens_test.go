package tokens_test

import (
	"encoding/json"
	"flag"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/npy"
	"github.com/nicolasacchi/metamusic/internal/analysis/tokens"
)

var update = flag.Bool("update", false, "rewrite golden files")

func randStream(seed uint64, n, vocab int) []int64 {
	r := rand.New(rand.NewPCG(seed, seed^0x5eed))
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(r.IntN(vocab))
	}
	return out
}

func arr(shape []int, data []int64) npy.Array {
	return npy.Array{DType: "<i8", Shape: shape, Data: data}
}

func one(t *testing.T, data []int64, dur float64) tokens.Stream {
	t.Helper()
	rep, err := tokens.Analyze(arr([]int{1, len(data)}, data), dur)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Streams) != 1 {
		t.Fatalf("streams = %d", len(rep.Streams))
	}
	return rep.Streams[0]
}

func TestTokenLoops_Random(t *testing.T) {
	t.Parallel()
	s := one(t, randStream(1, 9000, 1024), 180)
	if s.Distinct4GramRatio < 0.999 {
		t.Errorf("4-gram ratio = %v", s.Distinct4GramRatio)
	}
	if s.PeriodicRun.SpanS >= 1 {
		t.Errorf("periodic = %+v", s.PeriodicRun)
	}
	if s.TokensPerS != 50 || s.NTokens != 9000 || s.Encoding != "u16le" || len(s.GzipRatio10s) != 18 {
		t.Errorf("stream = %+v", s)
	}
	if s.LongestRepeat.Tokens > 6 {
		t.Errorf("longest repeat of random data = %+v", s.LongestRepeat)
	}
}

func TestTokenLoops_Injected(t *testing.T) {
	t.Parallel()
	d := randStream(2, 600, 1024)
	pat := randStream(3, 25, 1024)
	for i := 0; i < 12; i++ {
		copy(d[150+25*i:], pat)
	}
	s := one(t, d, 12)
	p := s.PeriodicRun
	if p.PeriodTokens != 25 || p.SpanTokens < 300 || math.Abs(p.AtS-3.0) > 0.1 || p.Repeats < 12 {
		t.Errorf("periodic = %+v", p)
	}
	if p.PeriodS != 0.5 {
		t.Errorf("period_s = %v", p.PeriodS)
	}
	if s.LongestRepeat.Tokens < 150 {
		t.Errorf("repeat = %+v", s.LongestRepeat)
	}
}

func TestTokenLoops_ConstantRun(t *testing.T) {
	t.Parallel()
	d := randStream(4, 500, 1024)
	for i := 100; i < 300; i++ {
		d[i] = 7
	}
	s := one(t, d, 10)
	if s.PeriodicRun.PeriodTokens != 1 || s.PeriodicRun.SpanTokens < 200 {
		t.Errorf("periodic = %+v", s.PeriodicRun)
	}
}

func TestTokenLoops_LongestRepeatNonOverlapping(t *testing.T) {
	t.Parallel()
	d := randStream(5, 600, 1<<15)
	copy(d[300:340], d[100:140])
	s := one(t, d, 12)
	r := s.LongestRepeat
	if r.Tokens != 40 || r.FirstAtS != 2.0 || r.SecondAtS != 6.0 || r.S != 0.8 {
		t.Errorf("repeat = %+v", r)
	}
	// a constant run of 10 has an overlapping repeat of 9 but a non-overlapping one of only 5
	c := make([]int64, 10)
	for i := range c {
		c[i] = 9
	}
	if got := one(t, c, 1).LongestRepeat.Tokens; got != 5 {
		t.Errorf("constant run repeat = %d, want 5", got)
	}
}

func TestTokenLoops_GzipWindows(t *testing.T) {
	t.Parallel()
	d := randStream(6, 2000, 1024) // 40 s at 50 tokens/s
	for i := 500; i < 1000; i++ {
		d[i] = d[500+i%10]
	}
	s := one(t, d, 40)
	if len(s.GzipRatio10s) != 4 {
		t.Fatalf("windows = %v", s.GzipRatio10s)
	}
	if s.GzipRatioMedian == nil || s.GzipRatioMin == nil {
		t.Fatal("nil summary")
	}
	if s.GzipRatio10s[1] > 0.5**s.GzipRatioMedian {
		t.Errorf("ratios = %v median %v", s.GzipRatio10s, *s.GzipRatioMedian)
	}
	if *s.GzipRatioMin != s.GzipRatio10s[1] {
		t.Errorf("min = %v", *s.GzipRatioMin)
	}
	// a final partial window counts only when at least half full
	if got := len(one(t, randStream(7, 1200, 1024), 24).GzipRatio10s); got != 2 {
		t.Errorf("20%% extra: windows = %d, want 2", got)
	}
	if got := len(one(t, randStream(7, 1240, 1024), 24.8).GzipRatio10s); got != 2 {
		t.Errorf("windows = %d", got)
	}
	short := one(t, randStream(8, 100, 1024), 2)
	if len(short.GzipRatio10s) != 0 || short.GzipRatioMin != nil || short.GzipRatioMedian != nil {
		t.Errorf("short stream = %+v", short)
	}
}

func TestTokenLoops_TimeAxis(t *testing.T) {
	t.Parallel()
	a, b := randStream(9, 300, 1024), randStream(10, 300, 1024)
	rowMajor := arr([]int{2, 300}, slices.Concat(a, b))
	inter := make([]int64, 0, 600)
	for i := range a {
		inter = append(inter, a[i], b[i])
	}
	r1, err := tokens.Analyze(rowMajor, 6)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := tokens.Analyze(arr([]int{300, 2}, inter), 6)
	if err != nil {
		t.Fatal(err)
	}
	if r1.TimeAxis != 1 || r2.TimeAxis != 0 || len(r1.Streams) != 2 || len(r2.Streams) != 2 {
		t.Fatalf("axes %d %d", r1.TimeAxis, r2.TimeAxis)
	}
	if !slices.EqualFunc(r1.Streams, r2.Streams, func(x, y tokens.Stream) bool {
		xb, _ := json.Marshal(x)
		yb, _ := json.Marshal(y)
		return string(xb) == string(yb)
	}) {
		t.Errorf("streams differ:\n%+v\n%+v", r1.Streams, r2.Streams)
	}
	// 17 streams are refused
	if _, err := tokens.Analyze(arr([]int{17, 40}, make([]int64, 17*40)), 1); err == nil {
		t.Error("17 streams accepted")
	}
	// ndim 3: the other axes flatten row-major
	r3, err := tokens.Analyze(arr([]int{2, 2, 50}, randStream(11, 200, 100)), 1)
	if err != nil || len(r3.Streams) != 4 || r3.TimeAxis != 2 {
		t.Errorf("3-d: %v %+v", err, r3)
	}
	if _, err := tokens.Analyze(arr([]int{1, 5}, []int64{1, 2, 3, 4, -5}), 1); err == nil {
		t.Error("negative token accepted")
	}
	if _, err := tokens.Analyze(arr([]int{1, 5}, make([]int64, 5)), 0); err == nil {
		t.Error("zero duration accepted")
	}
}

func TestTokenLoops_Encoding(t *testing.T) {
	t.Parallel()
	d := randStream(12, 500, 1024)
	d[3] = 70000
	if s := one(t, d, 10); s.Encoding != "u32le" {
		t.Errorf("encoding = %s", s.Encoding)
	}
}

func TestTokenLoops_Deterministic(t *testing.T) {
	t.Parallel()
	a := arr([]int{1, 1000}, randStream(13, 1000, 50))
	var prev []byte
	for i := 0; i < 3; i++ {
		rep, err := tokens.Analyze(a, 20)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(rep)
		if prev != nil && string(prev) != string(b) {
			t.Fatal("non-deterministic")
		}
		prev = b
	}
}

func TestTokenLoops_JSONShape(t *testing.T) {
	t.Parallel()
	d := randStream(14, 1200, 1024)
	pat := randStream(15, 10, 1024)
	for i := 0; i < 20; i++ {
		copy(d[100+10*i:], pat)
	}
	rep, err := tokens.Analyze(arr([]int{1, 1200}, d), 24)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "report.golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got) {
		t.Errorf("golden mismatch:\n%s", got)
	}
	// only the snake_case keys of C49.3
	var generic map[string]any
	_ = json.Unmarshal(got, &generic)
	for _, k := range []string{"dtype", "shape", "time_axis", "streams"} {
		if _, ok := generic[k]; !ok {
			t.Errorf("missing key %s", k)
		}
	}
	if len(generic) != 4 || strings.Contains(string(got), `"Streams"`) {
		t.Errorf("unexpected keys: %v", generic)
	}
}

func TestTokenLoops_Version(t *testing.T) {
	t.Parallel()
	if v := tokens.Version(); !strings.HasPrefix(v, "mm-token-loops/1 go") {
		t.Errorf("version = %q", v)
	}
}
