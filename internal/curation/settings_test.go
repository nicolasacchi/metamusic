package curation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func ptr[T any](v T) *T { return &v }

var (
	human  = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin}}
	agent  = auth.Principal{Kind: auth.KindAgent, ID: "tok1", Name: "tok", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin}}
	device = auth.Principal{Kind: auth.KindDevice, ID: "dev1", Name: "phone", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
)

func problemOf(t *testing.T, err error) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) {
		t.Fatalf("want a *problem.Error, got %v", err)
	}
	return pe
}

// TestSettings_HumanOnly: agent, device and system are refused with 403 HUMAN_ONLY before anything is read or written; a human
// changes the flags; the four keys are written together; unchanged values write nothing; an empty patch is 422.
func TestSettings_HumanOnly(t *testing.T) {
	t.Parallel()
	st := open(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := curation.New(st, func() time.Time { return now })
	ctx := context.Background()

	for _, p := range []auth.Principal{agent, device, auth.CLI, {}} {
		_, err := s.Set(ctx, p, curation.SettingsPatch{ExportMuseNames: ptr(true)})
		if pe := problemOf(t, err); pe.Status != 403 || pe.Code != "HUMAN_ONLY" {
			t.Errorf("%s: %+v", p.Kind, pe)
		}
	}
	if _, err := s.Set(ctx, agent, curation.SettingsPatch{}); problemOf(t, err).Code != "HUMAN_ONLY" {
		t.Error("the principal is checked before the patch")
	}
	var n int
	if err := st.R.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'curation.%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused Set wrote %d rows (%v)", n, err)
	}

	if _, err := s.Set(ctx, human, curation.SettingsPatch{}); err == nil {
		t.Fatal("an empty patch must fail")
	} else if pe := problemOf(t, err); pe.Status != 422 || pe.Code != "VALIDATION_FAILED" {
		t.Errorf("empty patch: %+v", pe)
	}

	got, err := s.Set(ctx, human, curation.SettingsPatch{SubsonicMuseNames: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	want := curation.Settings{SubsonicMuseNames: true, UpdatedAt: "2026-10-28T12:00:00.000Z", UpdatedBy: "human:alice"}
	if got != want {
		t.Errorf("Set = %+v, want %+v", got, want)
	}
	// all four keys exist after the first write, the unset flag as an explicit false
	kv := map[string]string{}
	r, err := st.R.Query(`SELECT key, value FROM settings WHERE key LIKE 'curation.%'`)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var k, v string
		if err := r.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		kv[k] = v
	}
	_ = r.Close()
	if len(kv) != 4 || kv[curation.KeyExport] != "false" || kv[curation.KeySubsonic] != "true" || kv[curation.KeyUpdatedBy] != "human:alice" {
		t.Errorf("rows = %v", kv)
	}

	// unchanged values: 200 without a write (updated_at keeps its value even though the clock moved)
	now = now.Add(time.Hour)
	again, err := s.Set(ctx, human, curation.SettingsPatch{SubsonicMuseNames: ptr(true), ExportMuseNames: ptr(false)})
	if err != nil || again != want {
		t.Errorf("unchanged Set = %+v, %v", again, err)
	}

	// a real change moves updated_at and leaves the other flag alone
	other := human
	other.ID, other.Name = "bob", "bob"
	got, err = s.Set(ctx, other, curation.SettingsPatch{ExportMuseNames: ptr(true)})
	if err != nil || !got.ExportMuseNames || !got.SubsonicMuseNames || got.UpdatedAt != "2026-10-28T13:00:00.000Z" || got.UpdatedBy != "human:bob" {
		t.Errorf("second Set = %+v, %v", got, err)
	}
	if v, _ := s.ExportMuseNames(ctx); !v {
		t.Error("ExportMuseNames not visible through the reader")
	}
}

// TestSettings_Visibility: every reader sees the same values Set wrote, and a fresh store reads false, false and no author.
func TestSettings_Visibility(t *testing.T) {
	t.Parallel()
	st := open(t)
	s := curation.New(st, nil)
	ctx := context.Background()
	if got, err := s.Get(ctx); err != nil || got != (curation.Settings{}) {
		t.Fatalf("fresh Get = %+v, %v", got, err)
	}
	if _, err := s.Set(ctx, human, curation.SettingsPatch{ExportMuseNames: ptr(true), SubsonicMuseNames: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	g, _ := s.Get(ctx)
	e, _ := s.ExportMuseNames(ctx)
	sub, _ := s.SubsonicMuseNames(ctx)
	if !g.ExportMuseNames || !g.SubsonicMuseNames || !e || !sub {
		t.Errorf("Get %+v export %v subsonic %v", g, e, sub)
	}
	if _, err := s.Set(ctx, human, curation.SettingsPatch{ExportMuseNames: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.ExportMuseNames(ctx); e {
		t.Error("turning a flag off must stick")
	}
	if sub, _ := s.SubsonicMuseNames(ctx); !sub {
		t.Error("the other flag must be untouched")
	}
}

// TestSettings_UnknownKeysRejectedByVerify pins the contract Verify R9 relies on: Keys is exactly the four keys Set writes, so
// any other curation.* row is the anomaly.
func TestSettings_UnknownKeysRejectedByVerify(t *testing.T) {
	t.Parallel()
	st := open(t)
	s := curation.New(st, nil)
	if _, err := s.Set(context.Background(), human, curation.SettingsPatch{ExportMuseNames: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	r, err := st.R.Query(`SELECT key FROM settings WHERE key LIKE 'curation.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	known := map[string]bool{}
	for _, k := range curation.Keys {
		known[k] = true
	}
	for r.Next() {
		var k string
		if err := r.Scan(&k); err != nil {
			t.Fatal(err)
		}
		if !known[k] {
			t.Errorf("Set wrote %q, which is not in curation.Keys", k)
		}
	}
}

// TestSet_WriteFailure: a closed store surfaces an error (never a silent success).
func TestSet_WriteFailure(t *testing.T) {
	t.Parallel()
	st := open(t)
	s := curation.New(st, nil)
	_ = st.Close()
	if _, err := s.Set(context.Background(), human, curation.SettingsPatch{ExportMuseNames: ptr(true)}); err == nil {
		t.Error("want an error")
	}
}

// TestSet_RollsBackOnWriteFailure: a write that fails midway leaves no key behind.
func TestSet_RollsBackOnWriteFailure(t *testing.T) {
	t.Parallel()
	st := open(t)
	if _, err := st.W.Exec(`CREATE TRIGGER settings_boom BEFORE INSERT ON settings WHEN NEW.key = 'curation.updated_by' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	s := curation.New(st, nil)
	if _, err := s.Set(context.Background(), human, curation.SettingsPatch{ExportMuseNames: ptr(true)}); err == nil {
		t.Fatal("want an error")
	}
	var n int
	if err := st.R.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'curation.%'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d keys survived the failed write (%v)", n, err)
	}
}
