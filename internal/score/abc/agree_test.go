package abc_test

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

func fixtures(t *testing.T) []string {
	t.Helper()
	fs, err := filepath.Glob("testdata/*.abc")
	if err != nil || len(fs) < 5 {
		t.Fatalf("fixtures: %v %v", fs, err)
	}
	return fs
}

// TestParseABC_MatchesParseScoreBars: bars, meter, Q: and seconds per bar of the first voice equal yue2.ParseScore
// whenever the latter times the score.
func TestParseABC_MatchesParseScoreBars(t *testing.T) {
	t.Parallel()
	timed := 0
	for _, f := range fixtures(t) {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sc, err := yue2.ParseScore(src)
		if err != nil {
			t.Fatal(err)
		}
		tu, err := abc.Parse(src, abc.Options{Dialect: abc.DialectYuE2})
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if got := len(tu.Voices[0].Bars); got != sc.Bars {
			t.Errorf("%s: bars %d, ParseScore %d", f, got, sc.Bars)
		}
		if got := tu.Header.Tempo; got == nil || got.Raw != sc.Q {
			t.Errorf("%s: Q %+v, ParseScore %q", f, got, sc.Q)
		}
		if got := tu.Header.MeterNum*1000 + tu.Header.MeterDen; got != meterKey(sc.Meter) {
			t.Errorf("%s: meter %d/%d, ParseScore %s", f, tu.Header.MeterNum, tu.Header.MeterDen, sc.Meter)
		}
		spb, ok := tu.SecondsPerBar()
		if sc.SecondsPerBar > 0 {
			timed++
			if !ok || math.Abs(spb-sc.SecondsPerBar) > 1e-9 {
				t.Errorf("%s: seconds per bar %v %v, ParseScore %v", f, spb, ok, sc.SecondsPerBar)
			}
		} else if ok {
			t.Errorf("%s: timed where ParseScore is not", f)
		}
	}
	if timed < 4 {
		t.Fatalf("only %d timed fixtures", timed)
	}
}

func meterKey(m string) int {
	var n, d int
	for i := 0; i < len(m); i++ {
		if m[i] == '/' {
			for _, c := range m[:i] {
				n = n*10 + int(c-'0')
			}
			for _, c := range m[i+1:] {
				d = d*10 + int(c-'0')
			}
		}
	}
	return n*1000 + d
}

func TestParse_SectionsMatchParseScore(t *testing.T) {
	t.Parallel()
	withSections := 0
	for _, f := range fixtures(t) {
		src, _ := os.ReadFile(f)
		sc, _ := yue2.ParseScore(src)
		tu, err := abc.Parse(src, abc.Options{Dialect: abc.DialectYuE2})
		if err != nil {
			t.Fatal(err)
		}
		if len(tu.Sections) != len(sc.Sections) {
			t.Errorf("%s: %d sections, ParseScore %d (%+v vs %+v)", f, len(tu.Sections), len(sc.Sections), tu.Sections, sc.Sections)
			continue
		}
		for i, s := range sc.Sections {
			g := tu.Sections[i]
			if g.Label != s.Label || g.Kind != s.Kind || g.StartBar != s.StartBar || g.Bars != s.Bars {
				t.Errorf("%s: section %d = %+v, ParseScore %+v", f, i, g, s)
			}
		}
		if len(sc.Sections) > 0 {
			withSections++
		}
	}
	if withSections < 4 {
		t.Fatalf("only %d fixtures with sections", withSections)
	}
}
