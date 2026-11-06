package engineadopt_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engineadopt"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// *Service is the AdoptionGate generation consumes: the freeze keeps the two sides compiling against each other.
var _ generation.AdoptionGate = (*engineadopt.Service)(nil)

func TestStateAdopted(t *testing.T) {
	t.Parallel()
	if !(engineadopt.State{State: engineadopt.StateAdopted}).Adopted() || (engineadopt.State{State: engineadopt.StateNone}).Adopted() {
		t.Error("Adopted")
	}
}

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

type rig struct {
	svc *engineadopt.Service
	st  *store.Store
	now time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := &rig{st: st, now: time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)}
	r.svc = engineadopt.New(engineadopt.Options{Store: st, Now: func() time.Time { return r.now }})
	return r
}

func (r *rig) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := r.st.R.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestState_SeedAndNone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	y, err := r.svc.State(ctx, r.st.R, "yue2")
	if err != nil || !y.Adopted() || y.Via != engineadopt.ViaSeed || y.ChangedBy != "system:default" || y.Reason != nil || y.TrialID != nil {
		t.Fatalf("yue2 = %+v, %v", y, err)
	}
	n, err := r.svc.State(ctx, r.st.R, "acestep15")
	if err != nil || n.State != engineadopt.StateNone || n.Adopted() || n.Engine != "acestep15" {
		t.Fatalf("acestep15 = %+v, %v", n, err)
	}
	l, err := r.svc.List(ctx, r.st.R)
	if err != nil || len(l) != 1 || l[0].Engine != "yue2" {
		t.Fatalf("list = %+v, %v", l, err)
	}
}

func TestAdopt_AppendsAuditedRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	st, err := r.svc.Adopt(ctx, "cli:op", "acestep15", " blind A/B passed ")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Adopted() || st.Via != engineadopt.ViaCLI || st.ChangedBy != "cli:op" || st.Reason == nil || *st.Reason != "blind A/B passed" || !st.ChangedAt.Equal(r.now) {
		t.Fatalf("state = %+v", st)
	}
	if err := r.svc.Admitted(ctx, r.st.R, "acestep15", ""); err != nil {
		t.Errorf("adopted engine refused: %v", err)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM engine_adoption_log WHERE engine = 'acestep15'`); n != 1 {
		t.Errorf("%d log rows", n)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM audit_log WHERE op = 'cli:engines-adopt' AND transport = 'cli' AND status = 200`); n != 1 {
		t.Errorf("%d audit rows", n)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM audit_log WHERE op = 'cli:engines-adopt' AND (args_sha256 = '' OR instr(args_sha256, 'blind') > 0)`); n != 0 {
		t.Error("audit row carries the reason")
	}
}

func TestReject_WritesNotAdoptedAndHistoryKept(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.svc.Adopt(ctx, "cli:op", "acestep15", "first"); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(time.Hour)
	st, err := r.svc.Reject(ctx, "cli:op", "acestep15", "regressed")
	if err != nil || st.State != engineadopt.StateNotAdopted || st.Adopted() {
		t.Fatalf("reject = %+v, %v", st, err)
	}
	var pe *problem.Error
	err = r.svc.Admitted(ctx, r.st.R, "acestep15", "")
	if !errors.As(err, &pe) || pe.Status != 409 || pe.Code != "ENGINE_NOT_ADOPTED" || pe.Details["engine"] != "acestep15" ||
		pe.Details["hint"] != engineadopt.AdoptHint("acestep15") {
		t.Errorf("Admitted after reject: %v", err)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM engine_adoption_log WHERE engine = 'acestep15'`); n != 2 {
		t.Errorf("history has %d rows, want 2", n)
	}
	if _, err := r.st.W.ExecContext(ctx, `UPDATE engine_adoption_log SET state = 'adopted'`); err == nil {
		t.Error("the log was updatable")
	}
	if n := r.count(t, `SELECT COUNT(*) FROM audit_log WHERE op = 'cli:engines-reject'`); n != 1 {
		t.Errorf("%d reject audit rows", n)
	}
}

func TestAdmitted_Table(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	for _, row := range []struct {
		engine, trial string
		ok            bool
	}{{"yue2", "", true}, {"acestep15", "", false}, {"acestep15", "trial-1", false}, {"unknown", "", false}} {
		err := r.svc.Admitted(ctx, r.st.R, row.engine, row.trial)
		if (err == nil) != row.ok {
			t.Errorf("Admitted(%s,%q) = %v, want ok=%v", row.engine, row.trial, err, row.ok)
		}
	}
}

func TestAdopt_Refusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	long := strings.Repeat("x", 501)
	for _, row := range []struct {
		by, engine, reason string
		want               error
	}{
		{"op", "acestep15", "", engineadopt.ErrReasonRequired},
		{"op", "acestep15", "   ", engineadopt.ErrReasonRequired},
		{"op", "acestep15", long, engineadopt.ErrReasonRequired},
		{"", "acestep15", "why", engineadopt.ErrByRequired},
		{"op", "yue2", "why", engineadopt.ErrYuE2Fixed},
		{"op", "", "why", engineadopt.ErrBadEngine},
		{"op", "a b", "why", engineadopt.ErrBadEngine},
	} {
		if _, err := r.svc.Adopt(ctx, row.by, row.engine, row.reason); !errors.Is(err, row.want) {
			t.Errorf("Adopt(%q,%q,%d chars) = %v, want %v", row.by, row.engine, len(row.reason), err, row.want)
		}
		if _, err := r.svc.Reject(ctx, row.by, row.engine, row.reason); !errors.Is(err, row.want) {
			t.Errorf("Reject = %v, want %v", err, row.want)
		}
	}
	if n := r.count(t, `SELECT COUNT(*) FROM engine_adoption_log`); n != 1 {
		t.Errorf("a refusal wrote a log row (%d rows)", n)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM audit_log WHERE op LIKE 'cli:engines-%'`); n != 0 {
		t.Errorf("a refusal wrote an audit row")
	}
}

func TestAdopt_RollsBackWhenAuditFails(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.st.W.ExecContext(ctx, `CREATE TRIGGER no_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'x'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Adopt(ctx, "op", "acestep15", "why"); err == nil {
		t.Fatal("adopt succeeded without its audit row")
	}
	if st, _ := r.svc.State(ctx, r.st.R, "acestep15"); st.State != engineadopt.StateNone {
		t.Errorf("state %q after a rolled-back adopt", st.State)
	}
}
