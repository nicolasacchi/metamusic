package acceptance_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// t1Names are the tests the T1 plan's acceptance mapping and C216/C224 name that belong to the integration lane. The test checks
// that each exists as a Test function in internal/acceptance or internal/transform, so a rename or a deletion fails here.
var t1Names = []string{
	"TestAcceptance_T1_TakeShape", "TestAcceptance_T1_MastersFrozen", "TestAcceptance_T1_Deterministic", "TestAcceptance_T1_SameSpecReusesTake",
	"TestAcceptance_T1_VerifyCLI", "TestAcceptance_T1_MorphSameLyrics", "TestAcceptance_T1_ShepardLoop", "TestAcceptance_T1_Glacier50x",
	"TestAcceptance_T1_AgentFlow", "TestAcceptance_T1_JoinsLibrary", "TestAcceptance_T1_JoinsLibrary_Tape", "TestAcceptance_T1_CancelRunning",
	"TestAcceptance_T1_LoudnessSafe", "TestAcceptance_T1_LoudnessUnsafeRefused", "TestAcceptance_T1_NoSpendRows", "TestAcceptance_T1_RealChild",
	"TestAcceptance_T1_Quota",
	"TestAcceptance_T1_HiFi_TakeShape", "TestAcceptance_T1_HiFi_LoudnessMatched", "TestAcceptance_T1_HiFi_Chain", "TestAcceptance_T1_HiFi_SameSpecReuses",
	"TestAcceptance_T1_HiFi_JoinsLibrary",
	"TestProperty_AnyTransformIsSafe", "TestProperty_SpecCanonical", "TestProperty_TransformsNeverSpend",
	"TestChaos_KillChildAtProgress", "TestChaos_ServerRestartMidRender", "TestChaos_DiskFullAtEncode", "TestChaos_DiskFullAtIngest",
	"TestChaos_ParentRowTampered", "TestChaos_ChildHangs", "TestChaos_ChildMemoryBomb", "TestChaos_LeaseExpiry", "TestChaos_OrphanMasterCrashWindow",
	"TestGuards_TransformsNeverSpend", "TestT1_NoLeak",
}

// t1Deferred are named by the plan for T1b (score_render) and are not written here; the list keeps them from being forgotten.
var t1Deferred = []string{"TestAcceptance_T1_ScoreRenderFaithful", "TestAcceptance_T1_ScoreFromTake"}

func testFuncs(t *testing.T, dirs ...string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			af, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range af.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
					out[fn.Name.Name] = true
				}
			}
		}
	}
	return out
}

// TestT1_NamesExist: every test the plan names for T1's acceptance, property, chaos and leak suites exists.
func TestT1_NamesExist(t *testing.T) {
	have := testFuncs(t, ".", filepath.Join("..", "transform"))
	for _, n := range t1Names {
		if !have[n] {
			t.Errorf("the plan names %s and no such test exists", n)
		}
	}
	for _, n := range t1Deferred {
		if have[n] {
			t.Errorf("%s exists: move it from t1Deferred to t1Names", n)
		}
	}
	if _, err := os.Stat(filepath.Join("..", "transform")); err != nil {
		t.Fatal(err)
	}
}
