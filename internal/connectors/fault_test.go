package connectors_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/notify"
)

func TestStore_Defaults(t *testing.T) {
	t.Parallel()
	_, st, _ := newStore(t)
	cs := connectors.NewStore(st, nil, nil) // time.Now and a UUIDv7 generator
	g, created, err := cs.Ensure(context.Background(), "c", "s", "u", "L")
	if err != nil || !created || len(g.ID) != 36 || g.ID[14] != '7' || time.Since(g.CreatedAt) > time.Minute {
		t.Fatalf("%+v %v %v", g, created, err)
	}
}

func TestStore_ClosedStoreErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cs, st, _ := newStore(t)
	g, _, err := cs.Ensure(ctx, "c", "s", "u", "L")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Get(ctx, g.ID); err == nil || errors.Is(err, connectors.ErrNotFound) {
		t.Errorf("Get: %v", err)
	}
	if _, _, err := cs.Ensure(ctx, "c", "s2", "u", "L"); err == nil {
		t.Error("Ensure succeeded on a closed store")
	}
	if _, err := cs.List(ctx); err == nil {
		t.Error("List succeeded on a closed store")
	}
	if err := cs.Touch(ctx, g.ID); err == nil {
		t.Error("Touch succeeded on a closed store")
	}
	if _, err := cs.Set(ctx, humanOp, g.ID, connectors.SetIn{Revoked: boolp(true)}); err == nil || errors.Is(err, connectors.ErrNotFound) {
		t.Errorf("Set: %v", err)
	}
}

// TestStore_CorruptRows: a row whose timestamps do not parse is an error, not a zero time.
func TestStore_CorruptRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range []struct{ name, created, updated, lastUsed, revoked string }{
		{"created_at", "garbage", "2026-10-15T10:00:00.000Z", "", ""},
		{"updated_at", "2026-10-15T10:00:00.000Z", "garbage", "", ""},
		{"last_used_at", "2026-10-15T10:00:00.000Z", "2026-10-15T10:00:00.000Z", "garbage", ""},
		{"revoked_at", "2026-10-15T10:00:00.000Z", "2026-10-15T10:00:00.000Z", "", "garbage"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cs, st, _ := newStore(t)
			var lu, ra, rb any
			if c.lastUsed != "" {
				lu = c.lastUsed
			}
			if c.revoked != "" {
				ra, rb = c.revoked, "human:op"
			}
			if _, err := st.W.Exec(`INSERT INTO connector_grants(id, client_id, subject, username, label, scopes, created_at, last_used_at, updated_at, updated_by, revoked_at, revoked_by)
				VALUES('bad-1','c','s','u','L','read',?,?,?,'system:oauth',?,?)`, c.created, lu, c.updated, ra, rb); err != nil {
				t.Fatal(err)
			}
			if _, err := cs.Get(ctx, "bad-1"); err == nil || !strings.Contains(err.Error(), c.name) && c.name != "last_used_at" && c.name != "revoked_at" {
				t.Errorf("Get: %v", err)
			}
			if _, err := cs.List(ctx); err == nil {
				t.Error("List accepted a corrupt row")
			}
		})
	}
}

func TestStore_EnsureInsertRefused(t *testing.T) {
	t.Parallel()
	cs, _, _ := newStore(t)
	// a label over the 80-character CHECK: the SELECT misses, the INSERT is refused, and that is an error
	if _, _, err := cs.Ensure(context.Background(), "c", "s", "u", strings.Repeat("x", 81)); err == nil {
		t.Fatal("an over-long label was accepted")
	}
}

func TestStore_SetWriteFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("update", func(t *testing.T) {
		t.Parallel()
		cs, st, _ := newStore(t)
		g, _, _ := cs.Ensure(ctx, "c", "s", "u", "L")
		if _, err := st.W.Exec(`CREATE TRIGGER no_update BEFORE UPDATE ON connector_grants BEGIN SELECT RAISE(ABORT, 'nope'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := cs.Set(ctx, humanOp, g.ID, connectors.SetIn{Revoked: boolp(true)}); err == nil {
			t.Fatal("Set ignored a failed UPDATE")
		}
	})
	t.Run("cap_change_rolls_back", func(t *testing.T) {
		t.Parallel()
		cs, st, _ := newStore(t)
		g, _, _ := cs.Ensure(ctx, "c", "s", "u", "L")
		if _, err := st.W.Exec(`CREATE TRIGGER no_cap_change BEFORE INSERT ON spend_cap_changes BEGIN SELECT RAISE(ABORT, 'nope'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := cs.Set(ctx, humanOp, g.ID, connectors.SetIn{Limits: &connectors.Limits{DailyBudgetMicro: micro(5)}}); err == nil {
			t.Fatal("Set ignored a failed spend_cap_changes insert")
		}
		got, _ := cs.Get(ctx, g.ID)
		if got.Limits.DailyBudgetMicro != nil {
			t.Errorf("the limits changed although the history row failed: %+v", got.Limits)
		}
	})
}

type failNotifier struct{}

func (failNotifier) Notify(context.Context, notify.Message) error { return errors.New("ntfy down") }

// TestConnector_SideEffectFailuresDoNotRefuse: a failing audit insert, push or touch is logged and the request goes on.
func TestConnector_SideEffectFailuresDoNotRefuse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("audit_and_push", func(t *testing.T) {
		t.Parallel()
		r := newVRig(t, nil)
		if _, err := r.st.W.Exec(`DROP TABLE audit_log`); err != nil {
			t.Fatal(err)
		}
		before := audit.Failures()
		if _, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser)); err != nil {
			t.Fatalf("a failing audit insert refused the token: %v", err)
		}
		if audit.Failures() <= before {
			t.Error("the audit failure was not counted")
		}
		if !strings.Contains(r.log.String(), "audit failed") {
			t.Errorf("log = %s", r.log.String())
		}
	})
	t.Run("push", func(t *testing.T) {
		t.Parallel()
		r := newVRig(t, nil)
		r2 := connectors.NewVerifier(connectors.VerifierOptions{OIDC: r.oidcVerifier(t), Store: r.cs, TrustedUsers: []string{vUser}, Notify: failNotifier{}, Now: r.clock,
			Logger: r.logger()})
		if _, err := r2.VerifyConnector(ctx, r.token("test-claude", vUser)); err != nil {
			t.Fatalf("a failing push refused the token: %v", err)
		}
		if !strings.Contains(r.log.String(), "push failed") {
			t.Errorf("log = %s", r.log.String())
		}
	})
	t.Run("touch", func(t *testing.T) {
		t.Parallel()
		r := newVRig(t, nil)
		if _, err := r.st.W.Exec(`CREATE TRIGGER no_touch BEFORE UPDATE OF last_used_at ON connector_grants BEGIN SELECT RAISE(ABORT, 'nope'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser)); err != nil {
			t.Fatalf("a failing touch refused the token: %v", err)
		}
		if !strings.Contains(r.log.String(), "touch failed") {
			t.Errorf("log = %s", r.log.String())
		}
	})
	t.Run("ensure_error_is_internal", func(t *testing.T) {
		t.Parallel()
		r := newVRig(t, nil)
		if _, err := r.st.W.Exec(`DROP TABLE connector_grants`); err != nil {
			t.Fatal(err)
		}
		_, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser))
		if err == nil || errors.Is(err, auth.ErrInvalidToken) || errors.Is(err, auth.ErrConnectorForbidden) {
			t.Fatalf("err = %v, want an internal error (500 at the middleware)", err)
		}
	})
	t.Run("label_falls_back_to_the_client_id", func(t *testing.T) {
		t.Parallel()
		r := newVRig(t, nil)
		ov := r.oidcVerifier(t)
		v := connectors.NewVerifier(connectors.VerifierOptions{OIDC: ov, Store: r.cs, TrustedUsers: []string{vUser}, Now: r.clock, Logger: r.logger()})
		p, err := v.VerifyConnector(ctx, r.token("test-claude", vUser))
		if err != nil || p.Name != "test-claude" {
			t.Fatalf("%+v %v", p, err)
		}
		// an existing grant keeps its own label when the config has none for the client
		p, err = v.VerifyConnector(ctx, r.token("test-claude", vUser))
		if err != nil || p.Name != "test-claude" {
			t.Fatalf("%+v %v", p, err)
		}
	})
}
