package acceptance_test

import (
	"testing"
	"time"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// seedMandateM6N gives the scripted agent session one live mandate (M6N-M1): the operator creates mandates in the app, so the
// session's reads and its revoke need a row that exists before the first call. It returns the mandate id.
func seedMandateM6N(t *testing.T, r *servertest.Rig) string {
	t.Helper()
	const id = "01900000-0000-7000-8000-00000000ee01"
	now := lt.FixedNow
	if _, err := r.Store.W.Exec(`INSERT INTO mandates(id,holder_key,title,budget_micro_usd,valid_from,valid_until,scope_json,state,created_by,created_at)
		VALUES(?, 'agent:nightshift', 'Night shift', 1000000, ?, ?, ?, 'approved', 'human:alice', ?)`, id,
		store.FormatTime(now.Add(-time.Hour)), store.FormatTime(now.Add(24*time.Hour)),
		`{"modes":["new_seed","style_edit"],"engines":["yue2"],"min_stars":3,"root_track_ids":null,"max_takes_per_lineage":3,"max_takes_per_batch":6,`+
			`"max_batch_ceiling_micro_usd":943587,"max_batches_per_day":1,"tier_b":true}`, store.FormatTime(now)); err != nil {
		t.Fatal(err)
	}
	return id
}
