package runpod_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
)

// TestCanary_WorstCaseDryRun_Golden pins the arithmetic of C162.4 at the illustrative price.
func TestCanary_WorstCaseDryRun_Golden(t *testing.T) {
	t.Parallel()
	for _, part := range []string{"a"} {
		r, err := runpod.CanaryWorstCase(part, runpod.BudgetParams{})
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "canary_worst_case_"+part+".golden", r.Format())
	}
	if _, err := runpod.CanaryWorstCase("c", runpod.BudgetParams{}); err == nil {
		t.Error("an unknown part was priced")
	}
}

func TestCanary_WorstCase_PlanFigures(t *testing.T) {
	t.Parallel()
	r, err := runpod.CanaryWorstCase("a", runpod.BudgetParams{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[runpod.CanaryCase]runpod.BudgetCase{}
	for _, c := range r.Cases {
		by[c.Case] = c
	}
	// C162.4: one 30 s turbo case is 84 143 (take 22 809 + attempt overhead 61 334); a 480 s case 104 843
	if got := by[runpod.CaseT2M].MaxMicro; got != 84_143 {
		t.Errorf("t2m = %d, want 84143", got)
	}
	if got := by[runpod.CaseT2MLong].MaxMicro; got != 104_843 {
		t.Errorf("t2m-long = %d, want 104843", got)
	}
	var sum int64
	for _, c := range r.Cases {
		sum += int64(c.MaxMicro)
	}
	if want := sum + int64(r.InitFailMicro); int64(r.WorstMicro) != want {
		t.Errorf("worst %d, want sum of cases + init-fail window = %d", r.WorstMicro, want)
	}
	// init-fail submits one job that never starts: its take plus the whole start window (C162.4)
	if len(by[runpod.CaseInitFail].Takes) != 1 || r.InitFailMicro != 138_000 {
		t.Errorf("init-fail: %+v window %d (12 min at 690000/h = 138000)", by[runpod.CaseInitFail], r.InitFailMicro)
	}
	if len(by[runpod.CaseFirstPull].Takes) != 1 {
		t.Errorf("first-pull has one billed take and no pull term: %+v", by[runpod.CaseFirstPull])
	}
	// the ceiling the estimate is compared with is never below the case's worst case
	for _, c := range r.Cases {
		if c.CeilingMicro < c.MaxMicro {
			t.Errorf("%s: ceiling %d < max %d", c.Case, c.CeilingMicro, c.MaxMicro)
		}
	}
	// a supplied source track removes the source takes, a 600 s candidate and a dearer price raise the figure
	with, _ := runpod.CanaryWorstCase("a", runpod.BudgetParams{SourceTrack: true})
	if with.WorstMicro >= r.WorstMicro {
		t.Errorf("--source-track did not lower the worst case: %d >= %d", with.WorstMicro, r.WorstMicro)
	}
	long, _ := runpod.CanaryWorstCase("a", runpod.BudgetParams{MaxS: 600, Pools: []string{"ADA_48_PRO"}})
	if long.WorstMicro <= r.WorstMicro {
		t.Errorf("a 600 s candidate on a larger pool did not raise the worst case")
	}
	dear, _ := runpod.CanaryWorstCase("a", runpod.BudgetParams{PricePPH: 1_380_000})
	if dear.WorstMicro <= r.WorstMicro {
		t.Error("a dearer price did not raise the worst case")
	}
	if !strings.Contains(r.Format(), "ILLUSTRATIVE") || strings.Contains(dear.Format(), "ILLUSTRATIVE") {
		t.Error("only the default price is labelled illustrative")
	}
	// the timeout case is priced with the 30 s runtime it runs under
	if tc := by[runpod.CaseTimeout].Takes; len(tc) != 1 || tc[0].TimeoutS != 30 {
		t.Errorf("timeout takes %+v", tc)
	}
	_ = time.Second
}

// TestCanary_PlaceholdersMatchSeed: the dry run's RTFs are the committed unsourced seeds of the spend package.
func TestCanary_PlaceholdersMatchSeed(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../spend/engine_rtf.seed.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Engines map[string]map[string]map[string]struct {
			RTFMilli int `json:"rtf_milli"`
			LoadS    int `json:"load_s"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for class, rows := range f.Engines["acestep15"] {
		for gpu, v := range rows {
			if got := runpod.PlaceholderRTF(class, gpu); got != [2]int{v.RTFMilli, v.LoadS} {
				t.Errorf("%s/%s: dry run uses %v, seed %v", class, gpu, got, v)
			}
		}
	}
}
