package main

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// l3Env is a data directory holding the unsettled L3 fixture and its ids.
func l3Env(t *testing.T) (map[string]string, librarytest.L3Info) {
	t.Helper()
	env := testEnv(t, "local")
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	fx := librarytest.FixtureL3(t, st, librarytest.FixedNow)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return env, fx
}

// TestCLI_LibraryReconcile_Features: `library reconcile` derives the feature rows through the same step the server runs and
// reports how many it computed.
func TestCLI_LibraryReconcile_Features(t *testing.T) {
	t.Parallel()
	env, fx := l3Env(t)
	if n := dbRow(t, env, `SELECT COUNT(*) FROM song_features`); n != 0 {
		t.Fatalf("%d rows before the reconcile", n)
	}
	code, out, errs := do(t, env, "", "library", "reconcile")
	if code != 0 {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	m := regexp.MustCompile(`features (\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the report has no features count: %q", out)
	}
	if n, _ := strconv.Atoi(m[1]); n == 0 {
		t.Errorf("features count = 0: %q", out)
	}
	if n, want := dbRow(t, env, `SELECT COUNT(*) FROM take_features`), dbRow(t, env, `SELECT COUNT(*) FROM tracks`); n != want {
		t.Errorf("take rows %d, tracks %d", n, want)
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM song_features WHERE song_id = '`+fx.Songs["T1"]+`' AND bpm_basis = 'measured'`); n != 1 {
		t.Errorf("T1 has no measured row")
	}
}

// TestCLI_LibraryVerify_L3: a settled library verifies clean; a planted feature fault makes `library verify` exit 1 and print
// its line in the L1 form.
func TestCLI_LibraryVerify_L3(t *testing.T) {
	t.Parallel()
	env, fx := l3Env(t)
	if code, out, errs := do(t, env, "", "library", "reconcile"); code != 0 {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	code, out, errs := do(t, env, "", "library", "verify")
	if code != 0 || !strings.Contains(out, "consistent") {
		t.Fatalf("verify on a settled library: %d %q %q", code, out, errs)
	}
	l2Exec(t, env, `DELETE FROM song_features WHERE song_id = '`+fx.Songs["T1"]+`'`)
	code, out, errs = do(t, env, "", "library", "verify")
	if code != 1 || !strings.Contains(out, "song_features_missing "+fx.Songs["T1"]) || !strings.Contains(errs, "anomalies") {
		t.Errorf("verify on a planted fault: %d %q %q", code, out, errs)
	}
}
