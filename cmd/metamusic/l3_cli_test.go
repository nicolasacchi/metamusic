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

// TestCLI_LibraryVerify_L3_Invariants: `library verify` covers the three L3 families end to end on a settled library: the feature
// rows (F1 to F3), the sequence runs (F4) and the taxonomy proposals (F5). Legitimate rows verify clean; each planted fault prints
// its code and subject in the L1 form and the command exits 1.
func TestCLI_LibraryVerify_L3_Invariants(t *testing.T) {
	t.Parallel()
	env, fx := l3Env(t)
	if code, out, errs := do(t, env, "", "library", "reconcile"); code != 0 {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	rec := fx.Records["R1"]
	const digest = "0000000000000000000000000000000000000000000000000000000000000000"
	at := "2026-10-06T12:00:00.000Z"
	seq := func(id, before, after string, in, out int) string {
		return `INSERT INTO record_sequences(id, record_id, record_revision, strategy, order_before_json, order_after_json, exact, cost_input, cost_output,
			coverage_json, digest, explanation_json, actor, actor_kind, actor_name, at)
			VALUES('` + id + `', '` + rec + `', 1, 'arc', '` + before + `', '` + after + `', 1, ` + strconv.Itoa(in) + `, ` + strconv.Itoa(out) + `,
			'{}', '` + digest + `', '{}', 'human:x', 'human', 'human', '` + at + `')`
	}
	const okID, badID, costID = "01900000-0000-7000-8000-00000000e301", "01900000-0000-7000-8000-00000000e302", "01900000-0000-7000-8000-00000000e303"
	l2Exec(t, env, seq(okID, `["a","b","c"]`, `["c","a","b"]`, 100, 60))
	if code, out, errs := do(t, env, "", "library", "verify"); code != 0 || !strings.Contains(out, "consistent") {
		t.Fatalf("verify with a legitimate sequence run: %d %q %q", code, out, errs)
	}
	const openID, acceptedID = "01900000-0000-7000-8000-00000000e311", "01900000-0000-7000-8000-00000000e312"
	prop := func(id, state, mappings string, decided bool) string {
		decidedCols, decidedVals := "", ""
		if decided {
			decidedCols, decidedVals = ", decided_at, decided_by, applied_version, accepted_json", ", '"+at+"', 'human:x', 99, '[0]'"
		}
		return `INSERT INTO taxonomy_proposals(id, state, base_version, rationale, mappings_json, mappings_digest, impact_json, principal_id, principal_kind, principal_name, created_at` + decidedCols + `)
			VALUES('` + id + `', '` + state + `', 1, 'synthetic rationale', '` + mappings + `', '` + digest + `', '{}', 'agent:x', 'agent', 'agent', '` + at + `'` + decidedVals + `)`
	}
	l2Exec(t, env,
		`DELETE FROM song_features WHERE song_id = '`+fx.Songs["T2"]+`'`,
		seq(badID, `["a","b"]`, `["a","z"]`, 10, 10),
		seq(costID, `["a","b"]`, `["b","a"]`, 10, 20),
		prop(openID, "open", `[{"term":7,"facet":"mood","value":"chill","op":"add"}]`, false),
		prop(acceptedID, "accepted", `[{"term":"x","facet":"mood","value":"chill","op":"add"}]`, true),
	)
	code, out, errs := do(t, env, "", "library", "verify")
	if code != 1 || !strings.Contains(errs, "anomalies") {
		t.Fatalf("verify on planted faults: %d %q %q", code, out, errs)
	}
	for _, want := range []string{
		"song_features_missing " + fx.Songs["T2"], "sequence_not_permutation " + badID, "sequence_cost_regressed " + costID,
		"proposal_malformed " + openID, "proposal_version_missing " + acceptedID,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("verify output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sequence_not_permutation "+okID) || strings.Contains(out, "sequence_cost_regressed "+okID) {
		t.Errorf("the legitimate run %s was reported:\n%s", okID, out)
	}
}

// TestCLI_LibraryReconcile_BackfillsDraftSubjects: a library settled before the feature step existed has a drained queue and
// no song_features row for its draft-subject Songs (no take yet). `library reconcile` must backfill them (the sweep the server
// runs at start, C135.5) so `library verify` reports nothing; the rows are subject_kind draft with declared/predicted bases only.
func TestCLI_LibraryReconcile_BackfillsDraftSubjects(t *testing.T) {
	t.Parallel()
	env, _ := l3Env(t)
	if code, out, errs := do(t, env, "", "library", "reconcile"); code != 0 {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	drafts := dbRow(t, env, `SELECT COUNT(*) FROM song_features WHERE subject_kind = 'draft'`)
	if drafts == 0 {
		t.Fatal("the fixture has no draft-subject Songs")
	}
	l2Exec(t, env, `DELETE FROM song_features WHERE subject_kind = 'draft'`)
	if code, out, _ := do(t, env, "", "library", "verify"); code != 1 || !strings.Contains(out, "song_features_missing") {
		t.Fatalf("planted gap not reported: %d %q", code, out)
	}
	if code, out, errs := do(t, env, "", "library", "reconcile"); code != 0 {
		t.Fatalf("second reconcile: %d %q %q", code, out, errs)
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM song_features WHERE subject_kind = 'draft'`); n != drafts {
		t.Errorf("draft rows after reconcile = %d, want %d", n, drafts)
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM song_features WHERE subject_kind = 'draft' AND (bpm_basis = 'measured' OR energy_basis = 'measured' OR key_basis = 'measured')`); n != 0 {
		t.Errorf("%d draft rows carry a measured basis", n)
	}
	if code, out, errs := do(t, env, "", "library", "verify"); code != 0 || !strings.Contains(out, "consistent") {
		t.Errorf("verify after reconcile: %d %q %q", code, out, errs)
	}
}
