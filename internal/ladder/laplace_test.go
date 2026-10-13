package ladder

import "testing"

// TestLaplace_PriorOnlyItemsKeepPriorSD checks that items without comparisons are left
// at the prior SD while the others shrink below it.
func TestLaplace_PriorOnlyItemsKeepPriorSD(t *testing.T) {
	t.Parallel()
	f, err := FitBTD(3, []Comparison{{Winner: 0, Loser: 1}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "idle SD", f.SD[2], 1.5, 0)
	near(t, "idle theta", f.Theta[2], 0, 0)
	if !(f.SD[0] < 1.5 && f.SD[1] < 1.5) {
		t.Fatalf("SDs not shrunk: %v", f.SD)
	}
}
