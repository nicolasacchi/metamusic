package spend

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

var mmcRE = regexp.MustCompile(`^mmc_[a-z2-7]{52}$`)

func TestToken_Format(t *testing.T) {
	t.Parallel()
	s := New(Options{ConfirmKey: []byte("k")})
	for range 20 {
		tok, err := s.NewToken()
		if err != nil || !mmcRE.MatchString(tok) {
			t.Fatalf("token %q %v", tok, err)
		}
	}
}

// seedTokenEstimate inserts an estimate whose token hash is HashToken(token).
func (r *rig) seedTokenEstimate(id, token, principal string, expires time.Time) {
	r.t.Helper()
	ts := store.FormatTime(t0)
	r.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at)
		VALUES(?,?, 'agent','yue2','fake','[]','[]',1,'{}',10,20,5,25,690000,2,20,0,'[]',?,?,?)`,
		id, principal, r.s.HashToken(token), ts, store.FormatTime(expires))
}

func TestToken_OnlyHashStored(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	tok, err := r.s.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	r.seedTokenEstimate("e1", tok, "agent:a1", t0.Add(10*time.Minute))
	rows, err := r.st.R.Query(`SELECT * FROM estimates`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for i, v := range vals {
			if strings.Contains(v.String, tok) || strings.Contains(v.String, strings.TrimPrefix(tok, "mmc_")) {
				t.Errorf("column %s holds the plaintext token", cols[i])
			}
		}
	}
	var h string
	if err := r.st.R.QueryRow(`SELECT token_hash FROM estimates`).Scan(&h); err != nil || h != r.s.HashToken(tok) {
		t.Errorf("stored hash %q", h)
	}
}

func TestConsumeToken(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r.seedTokenEstimate("e1", "tok-ok", "agent:a1", t0.Add(10*time.Minute))
	r.seedTokenEstimate("e2", "tok-old", "agent:a1", t0.Add(-time.Minute))
	consume := func(tok, who, batch string) (id string, err error) {
		err = r.tx(func(tx *sql.Tx) error {
			var e error
			id, e = r.s.ConsumeToken(ctx, tx, tok, who, batch)
			return e
		})
		return
	}
	var te *TokenError
	if _, err := consume("tok-ok", "agent:other", "b1"); !errors.As(err, &te) || te.Reason != TokenUnknown || te.BatchID != "" {
		t.Errorf("other principal: %v", err)
	}
	if _, err := consume("nope", "agent:a1", "b1"); !errors.As(err, &te) || te.Reason != TokenUnknown {
		t.Errorf("unknown: %v", err)
	}
	if _, err := consume("tok-old", "agent:a1", "b1"); !errors.As(err, &te) || te.Reason != TokenExpired {
		t.Errorf("expired: %v", err)
	}
	if id, err := consume("tok-ok", "agent:a1", "b1"); err != nil || id != "e1" {
		t.Fatalf("consume: %q %v", id, err)
	}
	if _, err := consume("tok-ok", "agent:a1", "b2"); !errors.As(err, &te) || te.Reason != TokenConsumed || te.BatchID != "b1" {
		t.Errorf("consumed same principal: %+v", te)
	}
	if _, err := consume("tok-ok", "agent:other", "b2"); !errors.As(err, &te) || te.Reason != TokenUnknown || te.BatchID != "" {
		t.Errorf("consumed other principal must not reveal the batch: %+v", te)
	}
	if strings.Contains(te.Error(), "tok-ok") {
		t.Error("error text leaks the token")
	}
}
