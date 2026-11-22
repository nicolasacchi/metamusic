package morning

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

func auditCtx(op string, p auth.Principal) context.Context {
	return audit.WithPending(context.Background(), audit.Entry{At: dawn, Principal: p, Transport: reqinfo.REST, Op: op, Method: "POST", ArgsSHA256: "abc"})
}

// TestMorning_AuditRowCommitsWithTheChange: behind the gate a build and a settings change write their audit row in the same
// transaction; a refused change writes none (the gate writes its own).
func TestMorning_AuditRowCommitsWithTheChange(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.take("a", time.Hour)
	c, err := r.svc.Build(auditCtx("build_morning_crate", owner), owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	r.built(c)
	if n := r.scalar(`SELECT COUNT(*) FROM audit_log WHERE op = 'build_morning_crate' AND status = 202 AND created_ids_json LIKE ?`, "%"+c.ID+"%"); n != "1" {
		t.Fatalf("build audit rows = %s", n)
	}
	on := true
	if _, err := r.svc.UpdateSettings(auditCtx("update_morning_crate_settings", owner), owner, SettingsPatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if n := r.scalar(`SELECT COUNT(*) FROM audit_log WHERE op = 'update_morning_crate_settings' AND status = 200`); n != "1" {
		t.Fatalf("settings audit rows = %s", n)
	}
	bad := 1
	if _, err := r.svc.UpdateSettings(auditCtx("update_morning_crate_settings", owner), owner, SettingsPatch{Minutes: &bad}); err == nil {
		t.Fatal("a bad patch was accepted")
	}
	if n := r.scalar(`SELECT COUNT(*) FROM audit_log WHERE op = 'update_morning_crate_settings'`); n != "1" {
		t.Fatalf("a refused change left an audit row: %s", n)
	}
}

// TestMorning_SweepLoopRunsDaily: the sweep loop removes an old side at 04:30 local time.
func TestMorning_SweepLoopRunsDaily(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Until(time.Date(2026, 11, 22, 4, 0, 0, 0, time.UTC)))
		r := newRigAt(t, t.TempDir()+"/s.db", t.TempDir(), nil)
		r.svc.o.Clock = realClockForTest{}
		old := r.side("old", sha(7), 20*24*time.Hour)
		d := r.svc.SweepDaily()
		d.Clock = nil
		ctx, cancel := context.WithCancel(context.Background())
		go d.Loop(ctx)
		time.Sleep(20 * time.Minute) // 04:20: not yet
		synctest.Wait()
		if !r.exists(old) {
			t.Fatal("swept before 04:30")
		}
		time.Sleep(20 * time.Minute) // 04:40
		synctest.Wait()
		if r.exists(old) {
			t.Fatal("not swept after 04:30")
		}
		cancel()
		synctest.Wait()
	})
}
