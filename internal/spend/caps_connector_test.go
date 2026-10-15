package spend

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// grant inserts a connector grant row with the given limits (nil = the global default).
func (r *rig) grant(id string, daily, takes, micro any) {
	r.t.Helper()
	ts := store.FormatTime(t0)
	r.exec(`INSERT INTO connector_grants(id, client_id, subject, username, label, scopes, daily_budget_micro_usd, auto_approve_takes,
		auto_approve_micro_usd, created_at, updated_at, updated_by) VALUES(?,?,?,?,?,'read,write',?,?,?,?,?,'system:oauth')`,
		id, "client-"+id, "subject-"+id, "user-"+id, "Label "+id, daily, takes, micro, ts, ts)
}

func TestTokenLimitsFor_Table(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r.token("pat-1")
	r.exec(`UPDATE api_tokens SET daily_budget_micro_usd = 111, auto_approve_takes = 1, auto_approve_micro_usd = 222 WHERE id = 'pat-1'`)
	r.grant("grant-1", 200_000, 0, 300)
	r.grant("grant-2", nil, nil, nil)
	p := func(v int64) *money.MicroUSD { m := money.MicroUSD(v); return &m }
	n := func(v int) *int { return &v }
	cases := []struct {
		name, key string
		want      TokenLimits
	}{
		{"pat id", "agent:pat-1", TokenLimits{DailyMicro: p(111), AutoApproveMicro: p(222), AutoApproveTakes: n(1)}},
		{"grant id", "agent:grant-1", TokenLimits{DailyMicro: p(200_000), AutoApproveMicro: p(300), AutoApproveTakes: n(0)}},
		{"grant with null limits is all-nil", "agent:grant-2", TokenLimits{}},
		{"unknown id", "agent:nope", TokenLimits{}},
		{"human", "human:h1", TokenLimits{}},
		{"system", "system:runner", TokenLimits{}},
		{"device", "device:grant-1", TokenLimits{}},
	}
	for _, c := range cases {
		got, err := r.s.TokenLimitsFor(ctx, r.st.R, c.key)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !sameToken(got, c.want) {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
	// a connector principal and its key give the same limits
	pr := auth.Principal{Kind: auth.KindAgent, ID: "grant-1", Via: "oauth"}
	a, _ := r.s.TokenLimitsFor(ctx, r.st.R, pr.Key())
	b, _ := r.s.TokenLimitsFor(ctx, r.st.R, "agent:grant-1")
	if !sameToken(a, b) || a.DailyMicro == nil || *a.DailyMicro != 200_000 {
		t.Errorf("principal key %q: %+v vs %+v", pr.Key(), a, b)
	}
	// the all-nil zero value reads as the global default, never $0
	if d := PrincipalDaily(pr, TokenLimits{}, DefaultCaps); d != DefaultCaps.AgentDailyMicro {
		t.Errorf("daily for all-nil = %d", d)
	}
}

func TestSpend_ConnectorDailyCap(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r.grant("grant-1", 200_000, nil, nil)
	pr := auth.Principal{Kind: auth.KindAgent, ID: "grant-1", Via: "oauth"}
	tok, err := r.s.TokenLimitsFor(ctx, r.st.R, pr.Key())
	if err != nil {
		t.Fatal(err)
	}
	caps, err := r.s.LoadCaps(ctx, r.st.R)
	if err != nil {
		t.Fatal(err)
	}
	daily := PrincipalDaily(pr, tok, caps)
	if daily != 200_000 {
		t.Fatalf("daily = %d", daily)
	}
	if ce := CheckCaps(Usage{}, caps, daily, 200_000, 200_000); ce != nil {
		t.Errorf("exactly the grant daily fits: %+v", ce)
	}
	ce := CheckCaps(Usage{}, caps, daily, 300_000, 300_000)
	if ce == nil || ce.Cap != CapDaily || ce.Limit != 200_000 {
		t.Errorf("a ceiling of $0.30 over a $0.20 grant: %+v", ce)
	}
}

func TestSpend_ConnectorTopUpUsesGrantDaily(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r.exec(`UPDATE spend_caps SET agent_daily_micro_usd = 900000`) // global agent daily far above the grant
	r.grant("grant-1", 200_000, nil, nil)
	r.token("pat-1") // nil daily: the global agent daily applies
	top := func(b seeded, amt money.MicroUSD) (ce *CapError) {
		r.must(func(tx *sql.Tx) (err error) {
			ce, err = r.s.ReserveTopUp(ctx, tx, TopUp{BatchID: b.Batch, JobID: b.Jobs[0], Reason: "attempt_overhead", Amount: amt, Lease: r.lease})
			return
		})
		return
	}
	conn := r.seedBatch(seedSpec{name: "conn", principal: "agent:grant-1", max: 400_000, retry: 400_000})
	seedOutstanding(t, r, conn, "agent:grant-1", 150_000)
	ce := top(conn, 60_000) // 150k + 60k > 200k
	if ce == nil || ce.Cap != CapDaily || ce.Limit != 200_000 {
		t.Fatalf("connector top-up: %+v", ce)
	}
	if ce := top(conn, 50_000); ce != nil {
		t.Fatalf("up to the grant daily fits: %+v", ce)
	}
	pat := r.seedBatch(seedSpec{name: "pat", principal: "agent:pat-1", max: 400_000, retry: 400_000})
	seedOutstanding(t, r, pat, "agent:pat-1", 150_000)
	if ce := top(pat, 60_000); ce != nil {
		t.Fatalf("a PAT with a nil daily passes: %+v", ce)
	}
}

func TestSpend_ConnectorAutoApproveZero(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.grant("grant-1", nil, 0, nil)
	tok, err := r.s.TokenLimitsFor(context.Background(), r.st.R, "agent:grant-1")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AutoApproveTakes == nil || *tok.AutoApproveTakes != 0 {
		t.Fatalf("takes = %v", tok.AutoApproveTakes)
	}
	// the approval rule reads takes > limit: one take over a limit of 0 needs approval
	pr := auth.Principal{Kind: auth.KindAgent, ID: "grant-1", Via: "oauth"}
	if need := NeedsApproval(pr, "api", 1, 1, tok, DefaultCaps); !need.Needed {
		t.Errorf("auto-approve takes 0 must require approval, got %+v", need)
	}
	if need := NeedsApproval(pr, "api", 1, 1, TokenLimits{}, DefaultCaps); need.Needed {
		t.Errorf("the global default would have auto-approved: %+v", need)
	}
}
