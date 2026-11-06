package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// eng runs `metamusic engines ...` through cmdEngines with run()'s error-to-exit-code mapping.
func eng(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := cmdEngines(context.Background(), args, envFn(env), streams{strings.NewReader(""), &out, &errb})
	code := 0
	if err != nil {
		var x exitError
		var u usageError
		switch {
		case errors.As(err, &x):
			code = x.code
			if x.err != nil {
				errb.WriteString("metamusic: " + x.err.Error() + "\n")
			}
		case errors.As(err, &u):
			code = 2
			errb.WriteString("metamusic: " + string(u) + "\n")
		default:
			code = 1
			errb.WriteString("metamusic: " + err.Error() + "\n")
		}
	}
	return code, out.String(), errb.String()
}

func (r *rpRig) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := r.db(t).R.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnginesAdopt_AppendsAuditedRow(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	code, out, errs := eng(t, r.env, "adopt", "acestep15", "--reason", "blind A/B listened and approved")
	if code != 0 || strings.TrimSpace(out) != "acestep15: adopted" {
		t.Fatalf("adopt = %d %q %q", code, out, errs)
	}
	var state, via, by, reason string
	if err := r.db(t).R.QueryRow(`SELECT state, via, changed_by, reason FROM engine_adoption WHERE engine = 'acestep15'`).Scan(&state, &via, &by, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "adopted" || via != "cli" || by != "system:cli" || reason != "blind A/B listened and approved" {
		t.Errorf("row = %s %s %s %q", state, via, by, reason)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM audit_log WHERE op = 'cli:engines-adopt' AND transport = 'cli' AND principal_id = 'system:cli'`); n != 1 {
		t.Errorf("%d audit rows", n)
	}
	// the flag may come first, and a second adopt appends (history kept)
	if code, _, errs := eng(t, r.env, "adopt", "--reason", "again", "acestep15"); code != 0 {
		t.Fatalf("flag first: %d %s", code, errs)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM engine_adoption_log WHERE engine = 'acestep15'`); n != 2 {
		t.Errorf("%d log rows, want 2 (append-only)", n)
	}
}

func TestEnginesReject_WritesNotAdopted(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	eng(t, r.env, "adopt", "acestep15", "--reason", "ok")
	code, out, errs := eng(t, r.env, "reject", "acestep15", "--reason", "regressed on long takes")
	if code != 0 || strings.TrimSpace(out) != "acestep15: not_adopted" {
		t.Fatalf("reject = %d %q %q", code, out, errs)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM engine_adoption WHERE engine = 'acestep15' AND state = 'not_adopted'`); n != 1 {
		t.Error("the current state is not not_adopted")
	}
	if n := r.count(t, `SELECT COUNT(*) FROM engine_adoption_log WHERE engine = 'acestep15'`); n != 2 {
		t.Errorf("%d log rows, want adopt + reject", n)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM audit_log WHERE op = 'cli:engines-reject'`); n != 1 {
		t.Errorf("%d reject audit rows", n)
	}
}

func TestEngines_Refusals(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	long := strings.Repeat("x", 501)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no reason", []string{"adopt", "acestep15"}, "--reason is required"},
		{"blank reason", []string{"adopt", "acestep15", "--reason", "   "}, "--reason is required"},
		{"long reason", []string{"adopt", "acestep15", "--reason", long}, "--reason is required"},
		{"no engine", []string{"adopt", "--reason", "x"}, "ENGINE is required"},
		{"yue2", []string{"adopt", "yue2", "--reason", "x"}, "seed"},
		{"yue2 reject", []string{"reject", "yue2", "--reason", "x"}, "seed"},
		{"unknown engine", []string{"adopt", "nope", "--reason", "x"}, "unknown engine"},
		{"two engines", []string{"adopt", "acestep15", "other", "--reason", "x"}, "unexpected argument"},
		{"no subcommand", nil, "expected list|adopt|reject"},
		{"unknown subcommand", []string{"approve"}, "unknown subcommand"},
	} {
		code, _, errs := eng(t, r.env, tc.args...)
		if code != 2 || !strings.Contains(errs, tc.want) {
			t.Errorf("%s: %d %q, want 2 and %q", tc.name, code, errs, tc.want)
		}
	}
	if n := r.count(t, `SELECT COUNT(*) FROM engine_adoption_log`); n != 1 {
		t.Errorf("a refusal wrote a log row (%d rows)", n)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM audit_log WHERE op LIKE 'cli:engines-%'`); n != 0 {
		t.Errorf("a refusal wrote an audit row")
	}
}

func TestEnginesList(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	code, out, errs := eng(t, r.env, "list")
	if code != 0 || !strings.Contains(out, "yue2\tadopted\tvia seed") || !strings.Contains(out, "acestep15\tnone") {
		t.Fatalf("list = %d %q %q", code, out, errs)
	}
	eng(t, r.env, "adopt", "acestep15", "--reason", "approved")
	_, out, _ = eng(t, r.env, "list")
	if !strings.Contains(out, "acestep15\tadopted\tvia cli\tby system:cli") || !strings.Contains(out, "\tapproved") {
		t.Errorf("list after adopt = %q", out)
	}
	if code, _, _ := eng(t, r.env, "list", "extra"); code != 2 {
		t.Errorf("list with an argument: %d", code)
	}
	if code, out, _ := eng(t, r.env, "help"); code != 0 || !strings.Contains(out, "adopt ENGINE") {
		t.Errorf("help = %d %q", code, out)
	}
}

func TestEngines_NeedsADatabase(t *testing.T) {
	t.Parallel()
	r := newRPRigNoDB(t)
	if code, _, errs := eng(t, r.env, "adopt", "acestep15", "--reason", "x"); code != 2 || !strings.Contains(errs, "no database") {
		t.Errorf("%d %q", code, errs)
	}
}
