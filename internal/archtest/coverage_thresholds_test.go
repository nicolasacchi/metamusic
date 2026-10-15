package archtest

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCoverageThresholds_L1 pins C101.3: coverage.thresholds names every L1 package with at least its minimum.
func TestCoverageThresholds_L1(t *testing.T) {
	t.Parallel()
	f, err := os.Open(filepath.Join("..", "..", "coverage.thresholds"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := map[string]float64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
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
	want := map[string]float64{
		"internal/picks": 95, "internal/provenance": 95,
		"internal/muses": 90, "internal/songs": 90, "internal/sessions": 90, "internal/sounds": 90, "internal/board": 90,
	}
	for pkg, min := range want {
		v, ok := got[pkg]
		if !ok {
			t.Errorf("coverage.thresholds does not name %s", pkg)
			continue
		}
		if v < min {
			t.Errorf("%s threshold %v < %v", pkg, v, min)
		}
		if _, err := os.Stat(filepath.Join("..", "..", pkg)); err != nil {
			t.Errorf("%s is not a package directory: %v", pkg, err)
		}
	}
}
