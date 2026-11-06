package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// tableCounts is the row count of every table, so that two ingests can be compared without caring for ids or times.
func tableCounts(t *testing.T, b *bridgeRig) map[string]int {
	t.Helper()
	rows, err := b.st.R.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	_ = rows.Close()
	out := map[string]int{}
	for _, n := range names {
		var c int
		if err := b.st.R.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, n)).Scan(&c); err != nil { //nolint:gosec // G201: table names from sqlite_master
			t.Fatal(err)
		}
		out[n] = c
	}
	return out
}

// noopHook does nothing: it stands for a wired hook that has nothing to do for this take.
type noopHook struct{}

func (noopHook) AfterTakeTx(_ context.Context, _ *sql.Tx, _ TakeInfo) error { return nil }
func (noopHook) AfterTakeCommit(_ context.Context, _ TakeInfo)              {}

// TestIngest_NoHook_Unchanged: with no TakeHook the bridge ingests a take exactly as before M5: the same rows as a bridge whose hook
// does nothing, no row in any M5 table, no patch on the job, and the job completes.
func TestIngest_NoHook_Unchanged(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]map[string]int{}
	t.Run("group", func(t *testing.T) {
		for name, mod := range map[string]func(*BridgeOptions){
			"nil hook":  nil,
			"noop hook": func(o *BridgeOptions) { o.TakeHook = noopHook{} },
		} {
			t.Run(name, func(t *testing.T) {
				var b *bridgeRig
				if mod == nil {
					b = newBridgeRig(t, true)
				} else {
					b = newBridgeRig(t, true, mod)
				}
				job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
				b.run1(job, b.flac(1), [2]bool{}, nil)
				b.wantState(job, StateCompleted)
				if n := b.count(`SELECT COUNT(*) FROM tracks WHERE job_id = ?`, job); n != 1 {
					t.Fatalf("%d tracks for the job", n)
				}
				if n := b.count(`SELECT COUNT(*) FROM jobs WHERE patch_id IS NOT NULL`); n != 0 {
					t.Errorf("%d jobs carry a patch", n)
				}
				c := tableCounts(t, b)
				for _, m5 := range []string{"patches", "patch_comments", "callbacks", "callback_verdicts", "voice_notes"} {
					if c[m5] != 0 {
						t.Errorf("%s has %d rows after an ordinary ingest", m5, c[m5])
					}
				}
				mu.Lock()
				counts[name] = maps.Clone(c)
				mu.Unlock()
			})
		}
	})
	if diff := cmp.Diff(counts["nil hook"], counts["noop hook"]); diff != "" {
		t.Errorf("row counts differ between a nil hook and a hook that does nothing (-nil +noop):\n%s", diff)
	}
}

// TestTakeInfo_ReadsThePatchOfTheJob: the hook sees jobs.patch_id as TakeInfo.PatchID in the transaction and after the commit.
func TestTakeInfo_ReadsThePatchOfTheJob(t *testing.T) {
	h := &recHook{}
	b := newBridgeRig(t, true, func(o *BridgeOptions) { o.TakeHook = h })
	h.b = b
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	// the column is frozen once set and nothing writes it before S1: give the queued job a stub patch the way a Song PR job is born
	b.exec(`PRAGMA foreign_keys = OFF`) // the rig has no track to hang the stub patch on; the writer pool is one connection
	b.exec(`INSERT INTO patches(id, base_track_id, base_recipe_hash, operator, ops_json, compiled_json, scope_json, diff_json, rationale, principal_kind, principal_id, principal_name, created_at)
		VALUES ('stub-patch', 'no-track', 'h', 'lyric_edit', '[]', '{}', '{}', '{}', 'r', 'agent', 'agent:x', 'x', '2026-10-06T10:00:00Z')`)
	b.exec(`DROP TRIGGER jobs_patch_frozen`)
	b.exec(`UPDATE jobs SET patch_id = 'stub-patch' WHERE id = ?`, job)
	b.exec(`PRAGMA foreign_keys = ON`)
	b.run1(job, b.flac(1), [2]bool{}, nil)
	b.wantState(job, StateCompleted)
	if len(h.tx) != 1 || len(h.commit) != 1 {
		t.Fatalf("hook calls: tx %d, commit %d", len(h.tx), len(h.commit))
	}
	for _, info := range []TakeInfo{h.tx[0], h.commit[0]} {
		if info.PatchID == nil || *info.PatchID != "stub-patch" || info.JobID != job {
			t.Errorf("take info %+v, want patch stub-patch", info)
		}
	}
}
