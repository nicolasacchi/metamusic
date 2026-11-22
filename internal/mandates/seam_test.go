package mandates

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var night = auth.Principal{Kind: auth.KindAgent, ID: "nightshift", Name: "nightshift", Via: "internal", Scopes: []auth.Scope{auth.ScopeGenerate}}

func (r *rig) rate(track string, stars int) {
	r.t.Helper()
	ts := store.FormatTime(t0)
	r.exec(`INSERT INTO ratings(track_id, source, rater, stars, principal_kind, principal_id, principal_name, created_at, updated_at)
		VALUES (?, 'human', 'owner', ?, 'human', 'alice', 'alice', ?, ?)
		ON CONFLICT(track_id, source, rater) DO UPDATE SET stars = excluded.stars`, track, stars, ts, ts)
}

func take(parent string) spend.MandateTake {
	return spend.MandateTake{Mode: "new_seed", ParentTrackID: &parent}
}

func TestCoverSeam_ResolvesRootsAndOwnerStars(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	m := r.create()
	root := r.rootIDs[0]
	ctx := context.Background()
	cover := func(p auth.Principal, id string, takes ...spend.MandateTake) (bool, []string) {
		t.Helper()
		ok, rs, err := r.svc.Cover(ctx, r.st.R, p, id, "yue2", takes, 100_000, false)
		if err != nil {
			t.Fatal(err)
		}
		if rs == nil {
			t.Fatal("reasons must be a non-nil slice")
		}
		return ok, rs
	}
	if ok, rs := cover(night, m.ID, take(root)); ok || !slices.Equal(rs, []string{ReasonMinStars}) { // unrated
		t.Errorf("unrated: %v %v", ok, rs)
	}
	r.rate(root, 2)
	if ok, rs := cover(night, m.ID, take(root)); ok || !slices.Equal(rs, []string{ReasonMinStars}) {
		t.Errorf("2 stars: %v %v", ok, rs)
	}
	r.rate(root, 3)
	if ok, rs := cover(night, m.ID, take(root)); !ok || len(rs) != 0 {
		t.Errorf("3 stars: %v %v", ok, rs)
	}
	if ok, rs := cover(auth.Principal{Kind: auth.KindAgent, ID: "other"}, m.ID, take(root)); ok || !slices.Equal(rs, []string{ReasonHolder}) {
		t.Errorf("other principal: %v %v", ok, rs)
	}
	if ok, rs := cover(night, m.ID, spend.MandateTake{Mode: "new_seed"}); ok || !slices.Contains(rs, ReasonMinStars) { // a motherless take
		t.Errorf("no parent: %v %v", ok, rs)
	}
	if ok, rs := cover(night, m.ID, take("no-such-track")); ok || !slices.Contains(rs, ReasonMinStars) {
		t.Errorf("unknown parent: %v %v", ok, rs)
	}
	// the parent's lineage root is resolved: a mandate focused on another root does not cover it
	if len(r.rootIDs) < 2 {
		t.Fatal("the fixture has one root")
	}
	if _, err := r.svc.Revoke(ctx, owner, m.ID, "refocus"); err != nil {
		t.Fatal(err)
	}
	in := r.input()
	in.Scope.RootTrackIDs = []string{r.rootIDs[1]}
	focused, err := r.svc.Create(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if ok, rs := cover(night, focused.ID, take(root)); ok || !slices.Equal(rs, []string{ReasonRootFilter}) {
		t.Errorf("other root: %v %v", ok, rs)
	}
	r.rate(r.rootIDs[1], 4)
	if ok, rs := cover(night, focused.ID, take(r.rootIDs[1])); !ok || len(rs) != 0 {
		t.Errorf("focused root: %v %v", ok, rs)
	}
	if _, _, err := r.svc.Cover(ctx, r.st.R, night, "no-such-mandate", "yue2", nil, 1, false); !isStatus(err, 404) {
		t.Errorf("unknown mandate: %v", err)
	}
	// without a spend service the seam is unavailable: a 503 UNAVAILABLE problem, never a silent pass
	if _, _, err := New(Options{Store: r.st}).Cover(ctx, r.st.R, night, focused.ID, "yue2", nil, 1, false); !isProblem(err, 503, "UNAVAILABLE") {
		t.Errorf("no spend service: %v, want 503 UNAVAILABLE", err)
	}
}

func isStatus(err error, status int) bool {
	var pe *problem.Error
	return errors.As(err, &pe) && pe.Status == status
}

func isProblem(err error, status int, code string) bool {
	var pe *problem.Error
	return errors.As(err, &pe) && pe.Status == status && pe.Code == code
}

func TestRecheck_ReadsThroughTheTransaction(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	m := r.create()
	root := r.rootIDs[0]
	r.rate(root, 5)
	ctx := context.Background()
	recheck := func(tx *sql.Tx, ceiling int64) error {
		return r.svc.Recheck(ctx, tx, night, m.ID, "yue2", []spend.MandateTake{take(root)}, money.MicroUSD(ceiling), false)
	}
	tx, err := r.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := recheck(tx, 500_000); err != nil {
		t.Fatalf("covered: %v", err)
	}
	// an uncommitted batch of this very transaction commits budget the reader pool cannot see yet
	ts := store.FormatTime(t0)
	if _, err := tx.Exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at,mandate_id)
		VALUES('e1','agent:nightshift','agent','yue2','fake','[]','[]',1,'{}',10,900000,0,900000,690000,2,20,0,'[]','h1',?,?,?)`, ts, ts, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at,mandate_id) VALUES('b1','e1','agent:nightshift','agent','nightshift','api','yue2','fake',1,NULL,10,900000,0,900000,690000,200,15,2,20,'active',?,?)`, ts, m.ID); err != nil {
		t.Fatal(err)
	}
	err = recheck(tx, 500_000)
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != 409 || pe.Code != "MANDATE_UNAVAILABLE" || pe.Details["mandate_id"] != m.ID {
		t.Fatalf("over budget in tx: %v", err)
	}
	if rs, _ := pe.Details["reasons"].([]string); !slices.Contains(rs, ReasonBudget) {
		t.Errorf("reasons = %v", pe.Details["reasons"])
	}
	// the reader pool sees none of it
	if ok, _, err := r.svc.Cover(ctx, r.st.R, night, m.ID, "yue2", []spend.MandateTake{take(root)}, 500_000, false); err != nil || !ok {
		t.Errorf("reader: %v %v", ok, err)
	}
}
