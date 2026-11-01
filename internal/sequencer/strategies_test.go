package sequencer

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
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
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs (run with -update):\n%s", name, got)
	}
}

func TestSequencer_StrategiesGolden(t *testing.T) {
	t.Parallel()
	golden(t, "strategies.golden.json", Specs)
	if len(Specs) != len(Strategies) {
		t.Fatalf("%d specs for %d strategies", len(Specs), len(Strategies))
	}
	for i, s := range Strategies {
		if Specs[i].Strategy != s {
			t.Errorf("Specs[%d] = %s, registry has %s", i, Specs[i].Strategy, s)
		}
		if got, ok := SpecOf(s); !ok || got != Specs[i] {
			t.Errorf("SpecOf(%s) = %+v, %v", s, got, ok)
		}
	}
	if _, ok := SpecOf("nope"); ok {
		t.Error("SpecOf accepts an unknown strategy")
	}
	if Strategies[0] != StrategyArc {
		t.Error("the default strategy is not first")
	}
}
