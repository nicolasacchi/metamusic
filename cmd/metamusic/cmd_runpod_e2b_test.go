package main

import (
	"os"
	"testing"
)

func TestCLI_CanaryDryRun_PartB(t *testing.T) {
	t.Parallel()
	r := newRPRigNoDB(t)
	code, out, errs := rp(t, r.env, "canary", "--engine", "acestep15", "--all", "--dry-run", "--part", "b")
	if code != 0 {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	want, err := os.ReadFile("../../internal/provider/runpod/testdata/canary_worst_case_b.golden")
	if err != nil {
		t.Fatal(err)
	}
	if out != string(want) {
		t.Errorf("part b differs from the library golden:\n%s", out)
	}
	// without --part both parts are printed, a first and a second worst case
	code, out, _ = rp(t, r.env, "canary", "--engine", "acestep15", "--all", "--dry-run")
	if code != 0 || len(out) < len(want) {
		t.Errorf("both parts: %d %d bytes", code, len(out))
	}
}
