package app

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/morning"
	"github.com/nicolasacchi/metamusic/internal/morning/morningtest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestWireMorning: a full build wires the Morning Crate service into Deps, its resolver and its three loops, hands it the injected
// builder, and fails a building row a dead process left behind.
func TestWireMorning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	if _, err := st.W.Exec(`INSERT INTO morning_crates(id,trigger,state,seed,options_json,created_by,created_at) VALUES('dead','manual','building',1,'{}','human:alice',?)`,
		store.FormatTime(now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	fake := &morningtest.Fake{}
	var seen []any
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir}
	s, err := Build(context.Background(), cfg, config.Secrets{Key: make([]byte, 32)}, st, root, Options{
		Now: func() time.Time { return now }, MorningBuilder: fake,
		Hooks: []BuildHook{func(env *BuildEnv) error {
			for _, r := range env.resolvers {
				seen = append(seen, r)
			}
			return nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Deps.Morning == nil {
		t.Fatal("Deps.Morning not set")
	}
	found := false
	for _, r := range seen {
		if r == any(s.Deps.Morning) {
			found = true
		}
	}
	if !found {
		t.Error("the morning service is not among the media resolvers")
	}
	names := map[string]bool{}
	for _, l := range s.Loops {
		names[l.Name] = true
	}
	for _, want := range []string{"morning-crate", "morning-sweep", "morning-close"} {
		if !names[want] {
			t.Errorf("loop %s missing from %v", want, names)
		}
	}
	var state, code string
	if err := st.R.QueryRow(`SELECT state, COALESCE(error_code,'') FROM morning_crates WHERE id = 'dead'`).Scan(&state, &code); err != nil || state != "failed" || code != morning.CodeInterrupted {
		t.Errorf("dead build = %s %s %v", state, code, err)
	}
	set, err := s.Deps.Morning.Settings(context.Background())
	if err != nil || set.At != "07:00" {
		t.Errorf("settings %+v %v", set, err)
	}
	// without media tools and without an injected builder the service still wires (a build then fails TOOL_FAILED)
	s2, err := Build(context.Background(), cfg, config.Secrets{Key: make([]byte, 32)}, st, root, Options{Media: &MediaOptions{ClipsMissing: context.Canceled, TierAMissing: context.Canceled}})
	if err != nil || s2.Deps.Morning == nil {
		t.Fatalf("no-builder build: %v", err)
	}
}

// TestNightBatchOf: the morning build learns tonight's batch and whether it is final from the night shift's own record.
func TestNightBatchOf(t *testing.T) {
	t.Parallel()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.W.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	ts := "2026-11-22T01:30:00.000Z"
	exec(`INSERT INTO mandates(id,holder_key,title,budget_micro_usd,valid_from,valid_until,scope_json,state,created_by,created_at)
		VALUES('m1','agent:nightshift','t',1000000,'2026-11-20T00:00:00.000Z','2026-12-20T00:00:00.000Z','{}','approved','human:alice',?)`, ts)
	batch := func(n int, state string) string {
		eid, bid := fmt.Sprintf("e%d", n), fmt.Sprintf("b%d", n)
		exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
			expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
			requires_approval,approval_reasons,token_hash,created_at,expires_at,consumed_at,consumed_batch_id)
			VALUES(?,'agent:nightshift','agent','yue2','fake','[]','[]',1,'{}',10,10,0,10,690000,2,20,0,'[]',?,?,?,?,?)`, eid, "h"+eid, ts, ts, ts, bid)
		fin := any(nil)
		if state == "finished" || state == "rejected" || state == "expired" {
			fin = ts
		}
		exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
			expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
			workers_max,idle_s,state,created_at,finished_at) VALUES(?,?,'agent:nightshift','agent','nightshift','api','yue2','fake',1,NULL,10,10,0,10,690000,200,15,2,20,?,?,?)`,
			bid, eid, state, ts, fin)
		return bid
	}
	run := func(night, bid string) {
		exec(`INSERT INTO night_shift_runs(id,night,state,estimate_id,batch_id,mandate_id,created_at,updated_at) VALUES(?,?,'created',?,?,'m1',?,?)`,
			"r-"+night, night, "e"+bid[1:], bid, ts, ts)
	}
	run("2026-11-22", batch(1, "active"))
	run("2026-11-21", batch(2, "finished"))
	exec(`INSERT INTO night_shift_runs(id,night,state,reason,created_at,updated_at) VALUES('r-skip','2026-11-20','skipped','no_mandate',?,?)`, ts, ts)
	f := nightBatchOf(st)
	for _, tc := range []struct {
		night, id string
		fin, ok   bool
	}{{"2026-11-22", "b1", false, true}, {"2026-11-21", "b2", true, true}, {"2026-11-20", "", false, false}, {"2026-11-19", "", false, false}} {
		id, fin, ok, err := f(context.Background(), tc.night)
		if err != nil || id != tc.id || fin != tc.fin || ok != tc.ok {
			t.Errorf("%s = (%q, %v, %v, %v), want (%q, %v, %v)", tc.night, id, fin, ok, err, tc.id, tc.fin, tc.ok)
		}
	}
}
