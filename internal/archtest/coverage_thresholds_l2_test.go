package archtest

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCoverageThresholds_L2 pins C132.3: coverage.thresholds names every L2 package with a positive minimum. The minimums
// are the coverage measured when the file was written, rounded down, so this test only asserts presence and a sane floor.
func TestCoverageThresholds_L2(t *testing.T) {
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
	want := []string{
		"internal/records", "internal/recordview", "internal/albums", "internal/crates", "internal/curation",
		"internal/tape", "internal/liner", "internal/media/cover", "internal/media/flacmeta",
	}
	for _, pkg := range want {
		v, ok := got[pkg]
		if !ok {
			t.Errorf("coverage.thresholds does not name %s", pkg)
			continue
		}
		if v < 50 {
			t.Errorf("%s threshold %v is below the 50 sanity floor", pkg, v)
		}
		if _, err := os.Stat(filepath.Join("..", "..", pkg)); err != nil {
			t.Errorf("%s is not a package directory: %v", pkg, err)
		}
	}
}
