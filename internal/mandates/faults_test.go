package mandates

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != status {
		t.Fatalf("err = %v, want status %d", err, status)
	}
}

// TestMandates_StoredRowFaults: a damaged mandate row (a bad scope, a bad timestamp) is an error everywhere it is read, never a
// silently permissive mandate.
func TestMandates_StoredRowFaults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, set string }{
		{"scope", `scope_json = '{"modes":5}'`},
		{"valid_from", `valid_from = '0000'`},
		{"valid_until", `valid_until = 'tomorrow'`},
		{"created_at", `created_at = 'then'`},
		{"revoked_at", `state = 'revoked', revoked_by = 'alice', revoke_reason = 'x', revoked_at = 'when'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			m := r.create()
			r.exec(`DROP TRIGGER mandates_frozen`) // fault injection: the trigger is what normally keeps a row sane
			r.exec(`UPDATE mandates SET `+tc.set+` WHERE id = ?`, m.ID)
			ctx := context.Background()
			if _, err := r.svc.Get(ctx, m.ID); err == nil {
				t.Error("Get read a damaged row")
			}
			if _, err := r.svc.List(ctx, Filter{}); err == nil {
				t.Error("List read a damaged row")
			}
			if _, _, err := r.svc.Cover(ctx, r.st.R, night, m.ID, "yue2", nil, 1, false); err == nil {
				t.Error("Cover read a damaged row")
			}
			tx, terr := r.st.R.BeginTx(ctx, nil)
			if terr != nil {
				t.Fatal(terr)
			}
			defer tx.Rollback()
			if err := r.svc.Recheck(ctx, tx, night, m.ID, "yue2", nil, 1, false); err == nil {
				t.Error("Recheck read a damaged row")
			}
			if _, err := r.svc.Revoke(ctx, owner, m.ID, "why"); err == nil {
				t.Error("Revoke acted on a damaged row")
			}
			if _, err := r.svc.Active(ctx, r.st.R, Holders[HolderNightShift]); err == nil && tc.name != "revoked_at" {
				t.Error("Active read a damaged row")
			}
		})
	}
}

func TestMandates_ClosedStoreFaults(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	m := r.create()
	r.st.Close()
	ctx := context.Background()
	if _, err := r.svc.Create(ctx, owner, r.input()); err == nil {
		t.Error("Create on a closed store")
	}
	if _, err := r.svc.Revoke(ctx, owner, m.ID, "why"); err == nil {
		t.Error("Revoke on a closed store")
	}
	if _, err := r.svc.Get(ctx, m.ID); err == nil {
		t.Error("Get on a closed store")
	}
	if _, err := r.svc.List(ctx, Filter{}); err == nil {
		t.Error("List on a closed store")
	}
	if _, err := r.svc.Usage(ctx, m.ID); err == nil {
		t.Error("Usage on a closed store")
	}
	if _, err := r.svc.Active(ctx, r.st.R, Holders[HolderNightShift]); err == nil {
		t.Error("Active on a closed store")
	}
	if _, _, err := r.svc.Live(ctx, r.st.R, Holders[HolderNightShift]); err == nil {
		t.Error("Live on a closed store")
	}
	if _, err := batchIDs(ctx, r.st.R, m.ID); err == nil {
		t.Error("batchIDs on a closed store")
	}
	if _, err := r.svc.env(ctx, r.st.R); err == nil {
		t.Error("env on a closed store")
	}
	if _, _, err := r.svc.Cover(ctx, r.st.R, night, m.ID, "yue2", nil, 1, false); err == nil {
		t.Error("Cover on a closed store")
	}
	if _, _, err := parentFacts(ctx, r.st.R, "x"); err == nil {
		t.Error("parentFacts on a closed store")
	}
}

func TestMandates_NoSpendService(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	m := r.create()
	bare := New(Options{Store: r.st, Now: r.clk.Now, NewID: func() string { return "m-x" }})
	ctx := context.Background()
	if _, err := bare.Usage(ctx, m.ID); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Usage without spend: %v", err)
	}
	if _, _, err := bare.Live(ctx, r.st.R, Holders[HolderNightShift]); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Live without spend: %v", err)
	}
	_, _, err := bare.Cover(ctx, r.st.R, night, m.ID, "yue2", nil, 1, false)
	wantStatus(t, err, http.StatusServiceUnavailable)
	if err := bare.Recheck(ctx, nil, night, m.ID, "yue2", nil, 1, false); err == nil { // refused before the tx is touched
		t.Error("Recheck without spend")
	}
	if bare.Location() == nil {
		t.Error("Location without spend")
	}
}

func TestCoverSeam_UnknownMandateAndParent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	m := r.create()
	ctx := context.Background()
	_, _, err := r.svc.Cover(ctx, r.st.R, night, "nope", "yue2", nil, 1, false)
	wantStatus(t, err, http.StatusNotFound)
	// a parent that does not exist has no root and no stars: the take is not covered, with no error
	ok, rs, err := r.svc.Cover(ctx, r.st.R, night, m.ID, "yue2", []spend.MandateTake{take("ghost")}, 1, false)
	if err != nil || ok || len(rs) == 0 {
		t.Errorf("unknown parent: ok=%v reasons=%v err=%v", ok, rs, err)
	}
	// a Recheck of a mandate that does not cover is the 409
	tx, err := r.st.R.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	wantStatus(t, r.svc.Recheck(ctx, tx, night, m.ID, "yue2", []spend.MandateTake{take("ghost")}, 1, false), http.StatusConflict)
}

func TestRevokeMandate_Faults(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	m := r.create()
	ctx := context.Background()
	_, err := r.svc.Revoke(ctx, editor, m.ID, "why")
	wantStatus(t, err, http.StatusForbidden)
	_, err = r.svc.Revoke(ctx, owner, m.ID, "")
	wantStatus(t, err, http.StatusUnprocessableEntity)
	_, err = r.svc.Revoke(ctx, owner, "nope", "why")
	wantStatus(t, err, http.StatusNotFound)
	// the canceller fails: nothing is revoked
	r.cancel = func(context.Context, *sql.Tx, []string, string) error { return errors.New("boom") }
	if _, err := r.svc.Revoke(ctx, owner, m.ID, "why"); err == nil {
		t.Fatal("a failed canceller revoked the mandate")
	}
	if got := r.str(`SELECT state FROM mandates WHERE id = ?`, m.ID); got != StateApproved {
		t.Errorf("state after a failed revoke = %q", got)
	}
	r.cancel = nil
	if _, err := r.svc.Revoke(ctx, owner, m.ID, "why"); err != nil {
		t.Fatal(err)
	}
	_, err = r.svc.Revoke(ctx, owner, m.ID, "again")
	wantStatus(t, err, http.StatusConflict)
}

func TestCreateMandate_Faults(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	in := r.input()
	in.Title = ""
	_, err := r.svc.Create(ctx, owner, in)
	wantStatus(t, err, http.StatusUnprocessableEntity)
	in = r.input()
	in.Scope.Modes = nil
	_, err = r.svc.Create(ctx, owner, in)
	wantStatus(t, err, http.StatusUnprocessableEntity)
	// an environment without spend or adoption still validates the scope against the roots
	bare := New(Options{Store: r.st, Now: r.clk.Now, NewID: func() string { return "m-bare" }})
	if _, err := bare.Create(ctx, owner, r.input()); err != nil {
		t.Fatalf("bare create: %v", err)
	}
	_, err = bare.Create(ctx, owner, r.input())
	wantStatus(t, err, http.StatusConflict)
}

func TestMandates_ListLimits(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	_, err := r.svc.List(ctx, Filter{Limit: 101})
	wantStatus(t, err, http.StatusUnprocessableEntity)
	_, err = r.svc.List(ctx, Filter{State: "weird"})
	wantStatus(t, err, http.StatusUnprocessableEntity)
	_, err = r.svc.List(ctx, Filter{Holder: "nobody"})
	wantStatus(t, err, http.StatusUnprocessableEntity)
	_, err = r.svc.List(ctx, Filter{EffectiveStates: []string{"weird"}})
	wantStatus(t, err, http.StatusUnprocessableEntity)
	pg, err := r.svc.List(ctx, Filter{Cursor: "zzzz", Limit: 1})
	if err != nil || pg.NextCursor != "" {
		t.Errorf("page %+v %v", pg, err)
	}
}
