package connectors_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var humanOp = auth.Principal{Kind: auth.KindHuman, ID: "op", Name: "op", Via: "proxy", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}}

func micro(v int64) *money.MicroUSD { m := money.MicroUSD(v); return &m }
func intp(v int) *int               { return &v }
func boolp(v bool) *bool            { return &v }

func TestConnectorsStore_List_Order(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cs, st, _ := newStore(t)
	a, _, _ := cs.Ensure(ctx, "c", "s-a", "u", "A")
	b, _, _ := cs.Ensure(ctx, "c", "s-b", "u", "B")
	c, _, _ := cs.Ensure(ctx, "c", "s-c", "u", "C")
	// created_at all equal (fixed clock): used grants first (newest use first), then never-used by created_at, id
	if _, err := st.W.Exec(`UPDATE connector_grants SET last_used_at = ? WHERE id = ?`, store.FormatTime(fixedNow.Add(time.Hour)), c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`UPDATE connector_grants SET last_used_at = ? WHERE id = ?`, store.FormatTime(fixedNow.Add(2*time.Hour)), b.ID); err != nil {
		t.Fatal(err)
	}
	got, err := cs.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, g := range got {
		ids = append(ids, g.ID)
	}
	if diff := cmp.Diff([]string{b.ID, c.ID, a.ID}, ids); diff != "" {
		t.Errorf("order (-want +got):\n%s", diff)
	}
	empty, _ := newStore2(t)
	if l, err := empty.List(ctx); err != nil || l == nil || len(l) != 0 {
		t.Errorf("empty list = %#v %v, want a non-nil empty slice", l, err)
	}
}

func newStore2(t *testing.T) (*connectors.Store, *store.Store) {
	cs, st, _ := newStore(t)
	return cs, st
}

func TestConnectorsStore_Set_Table(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	type capRow struct{ Principal, Target, Before, After string }
	caps := func(st *store.Store) []capRow {
		rows, err := st.R.Query(`SELECT principal_id, target, before_json, after_json FROM spend_cap_changes ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []capRow
		for rows.Next() {
			var r capRow
			if err := rows.Scan(&r.Principal, &r.Target, &r.Before, &r.After); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	t.Run("scopes", func(t *testing.T) {
		t.Parallel()
		cs, st, _ := newStore(t)
		g, _, _ := cs.Ensure(ctx, "c", "s", "u", "L")
		got, err := cs.Set(ctx, humanOp, g.ID, connectors.SetIn{Scopes: []auth.Scope{auth.ScopeGenerate, auth.ScopeRead}})
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]auth.Scope{auth.ScopeRead, auth.ScopeGenerate}, got.Scopes); diff != "" {
			t.Errorf("canonical order (-want +got):\n%s", diff)
		}
		if got.UpdatedBy != humanOp.Key() || got.RevokedAt != nil {
			t.Errorf("updated_by %q revoked %v", got.UpdatedBy, got.RevokedAt)
		}
		if n := len(caps(st)); n != 0 {
			t.Errorf("a scope change wrote %d spend_cap_changes rows", n)
		}
		for _, bad := range [][]auth.Scope{
			{auth.ScopeWrite}, {}, {auth.ScopeRead, auth.ScopeAdmin}, {auth.ScopeRead, "bogus"}, {auth.ScopeRead, auth.ScopeRead},
		} {
			if _, err := cs.Set(ctx, humanOp, g.ID, connectors.SetIn{Scopes: bad}); !errors.Is(err, connectors.ErrInvalidScopes) {
				t.Errorf("scopes %v: err = %v, want ErrInvalidScopes", bad, err)
			}
		}
		after, _ := cs.Get(ctx, g.ID)
		if diff := cmp.Diff([]auth.Scope{auth.ScopeRead, auth.ScopeGenerate}, after.Scopes); diff != "" {
			t.Errorf("a refused change altered the row:\n%s", diff)
		}
	})
	t.Run("limits_record_one_change", func(t *testing.T) {
		t.Parallel()
		cs, st, _ := newStore(t)
		g, _, _ := cs.Ensure(ctx, "c", "s", "u", "L")
		in := connectors.SetIn{Limits: &connectors.Limits{DailyBudgetMicro: micro(200_000), AutoApproveTakes: intp(0), AutoApproveMicro: nil}}
		got, err := cs.Set(ctx, humanOp, g.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		if got.Limits.DailyBudgetMicro == nil || *got.Limits.DailyBudgetMicro != 200_000 || *got.Limits.AutoApproveTakes != 0 || got.Limits.AutoApproveMicro != nil {
			t.Errorf("limits %+v", got.Limits)
		}
		rows := caps(st)
		if len(rows) != 1 || rows[0].Target != "connector:"+g.ID || rows[0].Principal != humanOp.Key() {
			t.Fatalf("spend_cap_changes = %+v", rows)
		}
		var before, after map[string]any
		_ = json.Unmarshal([]byte(rows[0].Before), &before)
		_ = json.Unmarshal([]byte(rows[0].After), &after)
		wantBefore := map[string]any{"daily_budget_micro_usd": nil, "auto_approve_takes": nil, "auto_approve_micro_usd": nil}
		wantAfter := map[string]any{"daily_budget_micro_usd": float64(200_000), "auto_approve_takes": float64(0), "auto_approve_micro_usd": nil}
		if diff := cmp.Diff(wantBefore, before); diff != "" {
			t.Errorf("before:\n%s", diff)
		}
		if diff := cmp.Diff(wantAfter, after); diff != "" {
			t.Errorf("after:\n%s", diff)
		}
		// the same limits again change nothing and record nothing
		if _, err := cs.Set(ctx, humanOp, g.ID, in); err != nil {
			t.Fatal(err)
		}
		if n := len(caps(st)); n != 1 {
			t.Errorf("an unchanged limits object wrote a row: %d", n)
		}
		// all-null clears back to the global default (and records it)
		cleared, err := cs.Set(ctx, humanOp, g.ID, connectors.SetIn{Limits: &connectors.Limits{}})
		if err != nil || cleared.Limits != (connectors.Limits{}) {
			t.Fatalf("clear: %+v %v", cleared.Limits, err)
		}
		if n := len(caps(st)); n != 2 {
			t.Errorf("clearing must record a change: %d", n)
		}
	})
	t.Run("limit_range", func(t *testing.T) {
		t.Parallel()
		cs, st, _ := newStore(t)
		g, _, _ := cs.Ensure(ctx, "c", "s", "u", "L")
		for _, l := range []connectors.Limits{
			{DailyBudgetMicro: micro(-1)}, {DailyBudgetMicro: micro(1_000_000_001)},
			{AutoApproveMicro: micro(-5)}, {AutoApproveMicro: micro(1_000_000_001)},
			{AutoApproveTakes: intp(-1)}, {AutoApproveTakes: intp(101)},
		} {
			if _, err := cs.Set(ctx, humanOp, g.ID, connectors.SetIn{Limits: &l}); !errors.Is(err, connectors.ErrInvalidLimits) {
				t.Errorf("limits %+v: err = %v, want ErrInvalidLimits", l, err)
			}
		}
		if n := len(caps(st)); n != 0 {
			t.Errorf("refused limits wrote %d rows", n)
		}
	})
	t.Run("revoke_and_restore", func(t *testing.T) {
		t.Parallel()
		cs, _, _ := newStore(t)
		g, _, _ := cs.Ensure(ctx, "c", "s", "u", "L")
		got, err := cs.Set(ctx, humanOp, g.ID, connectors.SetIn{Revoked: boolp(true)})
		if err != nil || got.RevokedAt == nil || got.RevokedBy != humanOp.Key() || !got.RevokedAt.Equal(fixedNow) {
			t.Fatalf("revoke: %+v %v", got, err)
		}
		agent := auth.Principal{Kind: auth.KindAgent, ID: "tok-1", Name: "bot", Via: "token", Scopes: []auth.Scope{auth.ScopeAdmin}}
		again, err := cs.Set(ctx, agent, g.ID, connectors.SetIn{Revoked: boolp(true)})
		if err != nil || again.RevokedBy != humanOp.Key() {
			t.Errorf("revoking twice keeps the first revoker: %+v %v", again, err)
		}
		back, err := cs.Set(ctx, agent, g.ID, connectors.SetIn{Revoked: boolp(false)})
		if err != nil || back.RevokedAt != nil || back.RevokedBy != "" || back.UpdatedBy != agent.Key() {
			t.Errorf("restore: %+v %v", back, err)
		}
	})
	t.Run("errors", func(t *testing.T) {
		t.Parallel()
		cs, _, _ := newStore(t)
		g, _, _ := cs.Ensure(ctx, "c", "s", "u", "L")
		if _, err := cs.Set(ctx, humanOp, "00000000-0000-7000-8000-00000000dead", connectors.SetIn{Revoked: boolp(true)}); !errors.Is(err, connectors.ErrNotFound) {
			t.Errorf("unknown id: %v", err)
		}
		if _, err := cs.Set(ctx, humanOp, g.ID, connectors.SetIn{}); !errors.Is(err, connectors.ErrEmptyChange) {
			t.Errorf("empty change: %v", err)
		}
	})
}

func TestConnectorsStore_Touch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := fixedNow
	st0, st, _ := newStore(t)
	_ = st0
	cs := connectors.NewStore(st, func() time.Time { return now }, nil)
	g, _, err := cs.Ensure(ctx, "c", "s", "u", "L")
	if err != nil {
		t.Fatal(err)
	}
	last := func() string {
		var s *string
		if err := st.R.QueryRow(`SELECT last_used_at FROM connector_grants WHERE id = ?`, g.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		if s == nil {
			return ""
		}
		return *s
	}
	if err := cs.Touch(ctx, g.ID); err != nil || last() != store.FormatTime(fixedNow) {
		t.Fatalf("first touch: %q %v", last(), err)
	}
	now = fixedNow.Add(30 * time.Second)
	_ = cs.Touch(ctx, g.ID)
	if last() != store.FormatTime(fixedNow) {
		t.Errorf("a touch within a minute wrote: %q", last())
	}
	now = fixedNow.Add(61 * time.Second)
	_ = cs.Touch(ctx, g.ID)
	if last() != store.FormatTime(now) {
		t.Errorf("a touch after a minute did not write: %q", last())
	}
}
