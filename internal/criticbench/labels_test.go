package criticbench_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/criticbench"
	"github.com/nicolasacchi/metamusic/internal/diction"
)

type fakeLabels struct {
	lines   []diction.LabelledLine
	th      diction.Thresholds
	version string
	err     error
	langs   []string
	limits  []int
}

func (f *fakeLabels) HumanLabelledLines(_ context.Context, lang string, limit int) ([]diction.LabelledLine, error) {
	f.langs, f.limits = append(f.langs, lang), append(f.limits, limit)
	return f.lines, f.err
}
func (f *fakeLabels) Thresholds() diction.Thresholds          { return f.th }
func (f *fakeLabels) Version(context.Context) (string, error) { return f.version, nil }

// line makes a marked line of a synthetic take.
func line(i int, cer float64, mark string) diction.LabelledLine {
	return diction.LabelledLine{Key: diction.LabelKey{TrackID: fmt.Sprintf("t%d", i/10), Section: i % 10, Line: 0}, Lang: "it", CER: cer, Human: mark,
		MarkedAt: epoch.Add(time.Duration(i) * time.Minute)}
}

// lines40 is 40 lines: the Owner calls "unclear" exactly the lines whose CER is above 0.35, except 4 mislabelled lines.
func lines40() []diction.LabelledLine {
	var out []diction.LabelledLine
	for i := 0; i < 40; i++ {
		cer := 0.0125 * float64(i+1) // 0.0125 .. 0.5
		mark := "clear"
		if cer > 0.35 {
			mark = "unclear"
		}
		out = append(out, line(i, cer, mark))
	}
	return out
}

func TestLabels_LineCERAgreement(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	src := &fakeLabels{lines: lines40(), th: diction.Thresholds{Clear: 0.15, Mixed: 0.35}, version: "v1"}
	e.svc.Use(criticbench.NewLabels(src))
	b := e.bench(criticbench.Query{IncludeUnknown: true})
	c := find(b, "diction.line_cer")
	if c == nil {
		t.Fatalf("label critic missing: %+v", b.Critics)
	}
	if c.Kind != criticbench.KindLabels || c.Tier != criticbench.TierDiction || c.Direction != nil || c.MinDelta != nil || c.Scores.Ratings != nil || c.Scores.Duels != nil {
		t.Errorf("shape: %+v", c)
	}
	s := c.Scores.Lines
	if s == nil || s.Pairs != 40 || s.NEff != 40 || s.Agree != 40 || s.Predicted != 40 || s.Rate != 1 || s.Coverage != 1 {
		t.Fatalf("lines score: %+v", s)
	}
	// 40 of 40: Wilson lower bound 0.9123 > 0.55
	if s.State != criticbench.StateTrusted || c.State != criticbench.StateTrusted {
		t.Errorf("state %s / %s", s.State, c.State)
	}
	if b.Window.Lines != 40 {
		t.Errorf("window.lines %d", b.Window.Lines)
	}
	if len(src.langs) == 0 || src.langs[0] != "" || src.limits[0] != criticbench.WindowLines {
		t.Errorf("asked lang %q limit %v (every language, the newest %d)", src.langs, src.limits, criticbench.WindowLines)
	}
}

func TestLabels_DisagreementsAndContrary(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// the Owner calls the high-CER lines clear: the critic is contrary at every threshold above 0.
	var ls []diction.LabelledLine
	for i := 0; i < 40; i++ {
		cer := 0.0125 * float64(i+1)
		mark := "unclear"
		if cer > 0.35 {
			mark = "clear"
		}
		ls = append(ls, line(i, cer, mark))
	}
	e.svc.Use(criticbench.NewLabels(&fakeLabels{lines: ls, th: diction.Thresholds{Clear: 0.15, Mixed: 0.35}}))
	c := find(e.bench(criticbench.Query{Critic: "diction.line_cer"}), "diction.line_cer")
	if c.State != criticbench.StateContrary || c.Scores.Lines.Agree != 0 {
		t.Fatalf("%+v", c.Scores.Lines)
	}
	if n := len(c.Scores.Lines.Disagreements); n != 10 {
		t.Errorf("disagreements %d, want the 10 first by key", n)
	}
	if find(e.bench(criticbench.Query{IncludeUnknown: false}), "diction.line_cer") == nil {
		t.Error("a contrary critic stays listed when unknowns are dropped")
	}
}

func TestLabels_ThresholdSweep(t *testing.T) {
	t.Parallel()
	// CERs 0.10 0.20 0.30 0.40 0.50 0.60, human: clear clear unclear clear unclear unclear
	ls := []diction.LabelledLine{
		line(0, 0.10, "clear"), line(1, 0.20, "clear"), line(2, 0.30, "unclear"),
		line(3, 0.40, "clear"), line(4, 0.50, "unclear"), line(5, 0.60, "unclear"),
	}
	got := criticbench.Sweep(ls)
	// agree at mixed m = lines where (CER > m) == unclear
	exp := map[float64]int{0.15: 4, 0.20: 5, 0.25: 5, 0.30: 4, 0.35: 4, 0.40: 5, 0.45: 5, 0.50: 4}
	if len(got) != 8 {
		t.Fatalf("sweep has %d points, want 8", len(got))
	}
	for _, p := range got {
		a, ok := exp[p.Mixed]
		if !ok || p.N != 6 || p.Agree != a {
			t.Errorf("mixed %.2f: %+v (want agree %d)", p.Mixed, p, a)
		}
		wantRate := float64(int(float64(a)/6*10000+0.5)) / 10000
		if p.Rate != wantRate {
			t.Errorf("mixed %.2f rate %v want %v", p.Mixed, p.Rate, wantRate)
		}
	}
	// exact figures at one point: 5 of 6 -> Wilson [0.4365, 0.9699]
	for _, p := range got {
		if p.Mixed == 0.20 && (p.Rate != 0.8333 || p.CI95 != [2]float64{0.4365, 0.9699}) {
			t.Errorf("0.20: %+v", p)
		}
	}
	for i, p := range got {
		if p.Mixed != criticbench.SweepMixed(i) {
			t.Errorf("point %d mixed %v", i, p.Mixed)
		}
	}
	if d := cmp.Diff([]float64{0.15, 0.2, 0.25, 0.3, 0.35, 0.4, 0.45, 0.5}, func() []float64 {
		var m []float64
		for _, p := range got {
			m = append(m, p.Mixed)
		}
		return m
	}()); d != "" {
		t.Errorf("sweep grid (-want +got):\n%s", d)
	}
	// the boundary is strict: CER == mixed is not unclear
	b := criticbench.Sweep([]diction.LabelledLine{line(0, 0.35, "clear")})
	for _, p := range b {
		if p.Mixed == 0.35 && p.Agree != 1 {
			t.Errorf("CER equal to the threshold is clear: %+v", p)
		}
	}
}

func TestLabels_ThresholdSweepInBench(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.svc.Use(criticbench.NewLabels(&fakeLabels{lines: lines40(), th: diction.Thresholds{Mixed: 0.35}}))
	c := find(e.bench(criticbench.Query{IncludeUnknown: true}), "diction.line_cer")
	if len(c.Thresholds) != 8 {
		t.Fatalf("thresholds %+v", c.Thresholds)
	}
	for _, th := range c.Thresholds {
		if th.Mixed == 0.35 && th.Agree != 40 {
			t.Errorf("the threshold in force agrees with every line: %+v", th)
		}
		if th.N != 40 {
			t.Errorf("n: %+v", th)
		}
	}
	// other critics carry no sweep
	e.lineages(2, 2)
	if o := find(e.bench(criticbench.Query{IncludeUnknown: true}), "diction.line_cer"); o.Thresholds == nil {
		t.Error("lost the sweep")
	}
}

func TestBench_LabelCriticAbsentWithoutDiction(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.svc.Use(criticbench.NewLabels(nil)) // no Diction Lab wired
	b := e.bench(criticbench.Query{IncludeUnknown: true})
	if find(b, "diction.line_cer") != nil || b.Window.Lines != 0 {
		t.Errorf("absent, not failing: %+v", b)
	}
	e2 := newEnv(t)
	e2.svc.Use(criticbench.NewLabels(&fakeLabels{})) // wired, but the Owner marked nothing
	b = e2.bench(criticbench.Query{IncludeUnknown: true})
	if find(b, "diction.line_cer") != nil {
		t.Error("no marked line: the critic is absent")
	}
}

func TestLabels_SmallSampleIsInsufficientAndVersionJoinsCacheKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	src := &fakeLabels{lines: lines40()[:10], th: diction.Thresholds{Mixed: 0.35}, version: "v1"}
	e.svc.Use(criticbench.NewLabels(src))
	if c := find(e.bench(criticbench.Query{IncludeUnknown: true}), "diction.line_cer"); c.State != criticbench.StateInsufficient {
		t.Errorf("10 lines: %s", c.State)
	}
	src.lines = lines40()
	if c := find(e.bench(criticbench.Query{IncludeUnknown: true}), "diction.line_cer"); c.State != criticbench.StateInsufficient {
		t.Error("an unchanged version must serve the cached bench")
	}
	src.version = "v2"
	if c := find(e.bench(criticbench.Query{IncludeUnknown: true}), "diction.line_cer"); c.State != criticbench.StateTrusted {
		t.Errorf("a new version must recompute: %s", c.State)
	}
}

func TestLabels_SourceErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.svc.Use(criticbench.NewLabels(&fakeLabels{err: errors.New("boom"), version: "v"}))
	if _, err := e.svc.Bench(context.Background(), criticbench.Query{IncludeUnknown: true}); err == nil {
		t.Error("expected the labelled-lines error")
	}
}

func TestScoreLines_Table(t *testing.T) {
	t.Parallel()
	ls := []diction.LabelledLine{line(0, 0.1, "clear"), line(1, 0.5, "unclear"), line(2, 0.5, "clear"), line(3, 0.2, "unclear")}
	s := criticbench.ScoreLines(ls, 0.35)
	if s.Pairs != 4 || s.Agree != 2 || s.Rate != 0.5 || s.State != criticbench.StateInsufficient {
		t.Errorf("%+v", s)
	}
	if d := cmp.Diff([]string{"t0:2:0", "t0:3:0"}, s.Disagreements); d != "" {
		t.Errorf("disagreement keys (-want +got):\n%s", d)
	}
}

// The bench scores exactly the lines the source returns (the app passes the Owner-only Diction source), and asks for every language.
func TestLabels_OnlySourceLinesScore(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := &fakeLabels{lines: lines40()[:30], th: diction.Thresholds{Mixed: 0.35}}
	e.svc.Use(criticbench.NewLabels(f))
	b := e.bench(criticbench.Query{IncludeUnknown: true})
	if b.Window.Lines != 30 {
		t.Errorf("window lines %d, want the 30 the source returned", b.Window.Lines)
	}
	if c := find(b, "diction.line_cer"); c == nil || c.Thresholds[0].N != 30 {
		t.Errorf("critic %+v", c)
	}
	if len(f.langs) == 0 || f.langs[0] != "" || f.limits[0] != criticbench.WindowLines {
		t.Errorf("asked %v %v", f.langs, f.limits)
	}
}
