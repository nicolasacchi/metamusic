package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const settingsPath = "/api/curation/settings"
const setSettingsPath = "/human/api/curation/settings"

func settingsRig(t *testing.T) (*rig, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	return l2Rig(t, api.Deps{Curation: curation.New(st, func() time.Time { return now })}), st
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return m
}

// TestSettings_HumanOnly: set_curation_settings refuses every non-human principal with 403 HUMAN_ONLY and leaves the store
// untouched; the operator changes the flags; an empty patch is 422; unchanged values answer 200 without a write.
func TestSettings_HumanOnly(t *testing.T) {
	t.Parallel()
	r, st := settingsRig(t)
	all := []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}
	for name, p := range map[string]auth.Principal{
		"agent with every scope": l2Agent(all...),
		"paired device":          {Kind: auth.KindDevice, ID: "dev", Name: "phone", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "device"},
		"oauth connector":        {Kind: auth.KindAgent, ID: "grant", Name: "Claude", Scopes: all, Via: "oauth", Client: "test-claude"},
		"subsonic client":        {Kind: auth.KindDevice, ID: "sub", Name: "app", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "subsonic"},
		"system":                 auth.System("runner"),
	} {
		rec := r.callWith(p, "PUT", setSettingsPath, `{"export_muse_names":true}`)
		if m := decode(t, rec.Body.Bytes()); rec.Code != 403 || m["code"] != "HUMAN_ONLY" {
			t.Errorf("%s: %d %v, want 403 HUMAN_ONLY", name, rec.Code, m)
		}
	}
	var n int
	if err := st.R.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'curation.%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused calls wrote %d rows (%v)", n, err)
	}

	rec := r.callWith(human, "PUT", setSettingsPath, `{}`)
	if m := decode(t, rec.Body.Bytes()); rec.Code != 422 || m["code"] != "VALIDATION_FAILED" {
		t.Errorf("empty patch: %d %v", rec.Code, m)
	}
	rec = r.callWith(human, "PUT", setSettingsPath, `{"subsonic_muse_names":true}`)
	m := decode(t, rec.Body.Bytes())
	by, _ := m["updated_by"].(map[string]any)
	if rec.Code != 200 || m["subsonic_muse_names"] != true || m["export_muse_names"] != false || m["updated_at"] != "2026-10-28T12:00:00.000Z" || by["name"] != "alice" || by["kind"] != "human" {
		t.Errorf("set: %d %v", rec.Code, m)
	}
	again := r.callWith(human, "PUT", setSettingsPath, `{"subsonic_muse_names":true}`)
	if again.Code != 200 || again.Body.String() != rec.Body.String() {
		t.Errorf("unchanged: %d %s", again.Code, again.Body)
	}
}

// TestSettings_Visibility: every principal with read scope reads the two booleans (so an agent knows what it may expect), a
// fresh store reads false with no author, and an agent sees the operator as "human" (RefFor), never the username.
func TestSettings_Visibility(t *testing.T) {
	t.Parallel()
	r, _ := settingsRig(t)
	rec := r.callWith(l2Agent(auth.ScopeRead), "GET", settingsPath, "")
	m := decode(t, rec.Body.Bytes())
	if rec.Code != 200 || m["export_muse_names"] != false || m["subsonic_muse_names"] != false || m["updated_at"] != nil || m["updated_by"] != nil {
		t.Fatalf("fresh: %d %v", rec.Code, m)
	}
	if rec := r.callWith(human, "PUT", setSettingsPath, `{"export_muse_names":true}`); rec.Code != 200 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	rec = r.callWith(l2Agent(auth.ScopeRead), "GET", settingsPath, "")
	m = decode(t, rec.Body.Bytes())
	by, _ := m["updated_by"].(map[string]any)
	if m["export_muse_names"] != true || m["subsonic_muse_names"] != false || by["id"] != "human" || by["name"] != "human" || strings.Contains(rec.Body.String(), "alice") {
		t.Errorf("agent view: %v", m)
	}
	if rec := r.callWith(human, "GET", settingsPath, ""); !strings.Contains(rec.Body.String(), "alice") {
		t.Errorf("the operator sees their own name: %s", rec.Body)
	}
	if rec := r.callWith(auth.Principal{Kind: auth.KindAgent, ID: "t", Name: "t", Via: "token"}, "GET", settingsPath, ""); rec.Code != 403 {
		t.Errorf("no read scope: %d", rec.Code)
	}
}

// TestSettings_AuditHashOnly: the audited row of the operator's change carries the request hash and no value.
func TestSettings_AuditHashOnly(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true})
	c := r.Human(t)
	c.OK(t, "PUT", setSettingsPath, map[string]any{"export_muse_names": true})
	var op string
	var hash, created, errCode string
	var status int
	err := r.Store.R.QueryRow(`SELECT op, args_sha256, status, created_ids_json, COALESCE(error_code, '') FROM audit_log WHERE op = 'set_curation_settings'`).
		Scan(&op, &hash, &status, &created, &errCode)
	if err != nil || status != http.StatusOK || len(hash) != 64 || created != "[]" || errCode != "" {
		t.Errorf("audit row: %q %q %d %q %q (%v)", op, hash, status, created, errCode, err)
	}
	if hash == "true" || strings.Contains(hash, "export") {
		t.Error("the hash column holds a value")
	}
}

// TestGetStats_Curation: get_stats carries the block with the counts of FixtureL2, the opt-ins as the operator set them, and the
// review queue counts published Records only. The block is null when the curation service is absent.
func TestGetStats_Curation(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true})
	c := r.Human(t)
	stats := func() map[string]any {
		t.Helper()
		m, _ := c.OK(t, "GET", "/api/stats", nil).(map[string]any)
		cur, _ := m["curation"].(map[string]any)
		if cur == nil {
			t.Fatalf("get_stats.curation is null: %v", m["curation"])
		}
		return cur
	}
	cur := stats()
	if rs := cur["records"].(map[string]any); rs["total"] != float64(2) || rs["published"] != float64(1) || rs["draft"] != float64(1) || rs["proposed"] != float64(0) {
		t.Errorf("records = %v", rs)
	}
	if cs := cur["crates"].(map[string]any); cs["manual"] != float64(2) || cs["smart"] != float64(2) {
		t.Errorf("crates = %v", cs)
	}
	if ts := cur["tapes"].(map[string]any); ts["queued"] != float64(0) || ts["ready"] != float64(0) {
		t.Errorf("tapes = %v", ts)
	}
	if rv := cur["review"].(map[string]any); rv["undecided"] != float64(2) || rv["pin_ineligible"] != float64(0) {
		t.Errorf("review = %v", rv)
	}
	if oi := cur["opt_ins"].(map[string]any); oi["export_muse_names"] != false || oi["subsonic_muse_names"] != false {
		t.Errorf("opt_ins = %v", oi)
	}

	info := newWorldLabels(t)
	c.OK(t, "PUT", setSettingsPath, map[string]any{"subsonic_muse_names": true})
	if _, err := r.Store.W.Exec(`UPDATE tracks SET status = 'discarded', status_set_by = 'x' WHERE id = ?`, info.Takes["c2"]); err != nil {
		t.Fatal(err)
	}
	cur = stats()
	if oi := cur["opt_ins"].(map[string]any); oi["subsonic_muse_names"] != true || oi["export_muse_names"] != false {
		t.Errorf("opt_ins = %v", oi)
	}
	if rv := cur["review"].(map[string]any); rv["pin_ineligible"] != float64(1) {
		t.Errorf("review = %v", rv)
	}
}

// newWorldLabels names the fixture rows: FixtureL2 ids are deterministic, so a scratch database tells the rig's labels.
func newWorldLabels(t *testing.T) librarytest.L2Info {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "labels.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return librarytest.FixtureL2(t, st, librarytest.FixedNow)
}
