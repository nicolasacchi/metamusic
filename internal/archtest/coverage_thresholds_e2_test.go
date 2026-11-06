package archtest

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCoverageThresholds_E2 pins E2-I1: coverage.thresholds names the E2 packages that exist, inside a named E2-I1 block, with
// a positive floor, and leaves the T1-I1 block alone. internal/enginetrial and internal/suites (E2c) are not listed: the gate
// refuses a pattern that matches no package, so the lane that creates a package adds its floor.
func TestCoverageThresholds_E2(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "coverage.thresholds"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, m := range []string{"# BEGIN E2-I1", "# END E2-I1", "# BEGIN T1-I1", "# END T1-I1"} {
		if !strings.Contains(text, m) {
			t.Errorf("coverage.thresholds lacks the %q marker", m)
		}
	}
	got := map[string]float64{}
	inBlock := false
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "# BEGIN E2-I1"):
			inBlock = true
		case strings.HasPrefix(line, "# END E2-I1"):
			inBlock = false
		}
		if line == "" || strings.HasPrefix(line, "#") || !inBlock {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed line %q", line)
		}
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		got[fields[0]] = v
	}
	// acestep: floor = measured (97.4) - 1, rounded down; engineadopt is a stub until E2-OP lands, so its floor is staged.
	want := map[string]float64{"internal/engine/acestep": 96, "internal/engineadopt": 5}
	for pkg, min := range want {
		v, ok := got[pkg]
		if !ok {
			t.Errorf("the E2-I1 block does not name %s", pkg)
			continue
		}
		if v < min || v > 100 {
			t.Errorf("%s threshold %v outside [%v, 100]", pkg, v, min)
		}
		if _, err := os.Stat(filepath.Join("..", "..", pkg)); err != nil {
			t.Errorf("%s is not a package directory: %v", pkg, err)
		}
	}
}
