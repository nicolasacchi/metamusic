package acceptance_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/schedule"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// nightCounts snapshots the rows a skipped night must leave alone.
func nightCounts(t *testing.T, r *servertest.Rig) string {
	t.Helper()
	var s string
	if err := r.Store.R.QueryRow(`SELECT (SELECT COUNT(*) FROM estimates) || '|' || (SELECT COUNT(*) FROM batches) || '|' || (SELECT COUNT(*) FROM jobs) || '|' ||
		(SELECT COUNT(*) FROM approvals) || '|' || (SELECT COUNT(*) FROM ledger)`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestAcceptance_M6N_SimulatedNight (C389.2): over an imported library and the fake backend, an Owner who enabled the night shift under a
// mandate gets one night: one batch under the mandate (approval via mandate, no click), one take per lineage, its ceiling inside the
// mandate's per-night ceiling, the explore count the settings ask for, every job settled and the workers idle at the end. A second night
// under a mandate that is spent up to less than one take skips, writes no estimate, batch or approval, and leaves no notification to send.
func TestAcceptance_M6N_SimulatedNight(t *testing.T) {
	r := newProvRig(t, "fake", archivetest.SyntheticArchive(t, "acceptance"), servertest.Options{M2b: servertest.M2bOptions{Backfill: true}})
	h := r.Human(t)
	night := r.Services.NightShift
	if night == nil {
		t.Fatal("the rig built no night shift")
	}

	// three lineages the Owner has rated: two loved, one liked
	rows, err := r.Store.R.Query(`SELECT t.root_track_id, MIN(t.id) FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id JOIN recipe_keys k ON k.recipe_id = rc.id
		WHERE rc.engine = 'yue2' GROUP BY 1 ORDER BY 1 LIMIT 3`)
	if err != nil {
		t.Fatal(err)
	}
	var lineages []string
	for stars := 5; rows.Next(); stars-- {
		var root, track string
		if err := rows.Scan(&root, &track); err != nil {
			t.Fatal(err)
		}
		lineages = append(lineages, root)
		h.OK(t, "PUT", "/api/tracks/"+track+"/rating", map[string]any{"stars": min(stars, 5)})
		if stars == 5 {
			stars++ // the first two get 5 stars, the third 4
		}
	}
	rows.Close()
	if len(lineages) != 3 {
		t.Fatalf("the archive holds %d yue2 lineages, want 3", len(lineages))
	}

	// the Owner's mandate ($1.00 a night) and the shift, as the app does it
	until := r.Clock.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339)
	mandateID := obj(h.OK(t, "POST", "/human/api/mandates", map[string]any{"holder": "night_shift", "title": "Night shift", "budget_micro_usd": 1_000_000, "valid_until": until,
		"scope": map[string]any{"modes": []string{"new_seed", "style_edit"}, "engines": []string{"yue2"}, "min_stars": 3, "root_track_ids": nil,
			"max_takes_per_lineage": 5, "max_takes_per_batch": 6, "max_batch_ceiling_micro_usd": 1_000_000, "max_batches_per_day": 1, "tier_b": true}},
		"Idempotency-Key", "night-mandate"))["id"].(string)
	h.OK(t, "PUT", "/human/api/night-shift", map[string]any{"enabled": true, "at": "01:30", "max_takes": 3, "min_stars": 4, "explore_ppm": 300000, "listening_checks": "off"})

	// the preview names the night, and the night then makes exactly that plan
	pv := obj(h.OK(t, "GET", "/api/night-shift/preview", nil))
	items := arr(pv["items"])
	if len(items) != 3 || pv["would_skip"] != nil {
		t.Fatalf("preview: %v", pv)
	}
	explore, roots := 0, map[string]bool{}
	for _, it := range items {
		m := obj(it)
		if m["kind"] == "explore" {
			explore++
		}
		roots[m["root_track_id"].(string)] = true
	}
	if explore != 1 || len(roots) != 3 { // 3 takes at 30 % explore is one; one take per lineage
		t.Fatalf("explore %d over %d lineages: %v", explore, len(roots), items)
	}
	before := nightCounts(t, r.Rig)
	night1 := pv["night"].(string)
	plannedAt := r.Clock.Now()
	night.Run(context.Background(), schedule.Key{Day: night1, At: "01:30", Planned: plannedAt})

	ns := obj(h.OK(t, "GET", "/api/night-shift", nil))
	runs := arr(ns["runs"])
	if len(runs) != 1 {
		t.Fatalf("runs: %v", ns)
	}
	run := obj(runs[0])
	batch := run["batch_id"].(string)
	if run["night"] != night1 || run["state"] != "running" || run["takes"] != float64(3) || run["ceiling_micro_usd"].(float64) > 1_000_000 || run["ceiling_micro_usd"].(float64) <= 0 {
		t.Fatalf("run: %v", run)
	}
	var via, state string
	if err := r.Store.R.QueryRow(`SELECT via, state FROM approvals WHERE batch_id = ?`, batch).Scan(&via, &state); err != nil || via != "mandate" || state != "auto" {
		t.Fatalf("approval: via %q state %q (%v)", via, state, err)
	}
	var mid string
	var created int
	if err := r.Store.R.QueryRow(`SELECT mandate_id, (SELECT COUNT(*) FROM audit_log WHERE op = 'nightshift:create') FROM batches WHERE id = ?`, batch).Scan(&mid, &created); err != nil || mid != mandateID || created != 1 {
		t.Fatalf("batch mandate %q, %d create rows (%v)", mid, created, err)
	}
	var distinct int
	if err := r.Store.R.QueryRow(`SELECT COUNT(DISTINCT t.root_track_id) FROM jobs j JOIN tracks t ON t.id = j.source_track_id WHERE j.batch_id = ?`, batch).Scan(&distinct); err != nil || distinct != 3 {
		t.Fatalf("%d lineages in the batch (%v)", distinct, err)
	}
	if after := nightCounts(t, r.Rig); after == before {
		t.Fatal("the night wrote nothing")
	}

	// every job settles, then the workers go idle
	pump(t, r.Rig, func() bool {
		var open int
		if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state NOT IN ('completed','failed','cancelled')`, batch).Scan(&open); err != nil {
			t.Fatal(err)
		}
		return open == 0
	})
	var completed int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state = 'completed'`, batch).Scan(&completed); err != nil || completed != 3 {
		t.Fatalf("%d of 3 jobs completed (%v)", completed, err)
	}
	pump(t, r.Rig, func() bool {
		var b string
		_ = r.Store.R.QueryRow(`SELECT state FROM batches WHERE id = ?`, batch).Scan(&b)
		return b == "finished" && r.workers(t) == 0
	})
	run = obj(arr(obj(h.OK(t, "GET", "/api/night-shift", nil))["runs"])[0])
	if run["state"] != "done" || run["settled_micro_usd"].(float64) <= 0 || run["settled_micro_usd"].(float64) > run["ceiling_micro_usd"].(float64) {
		t.Fatalf("finished run: %v", run)
	}

	// the night's takes are rated, so the lineages are eligible again; the mandate is then spent to less than one take (a heavy night, simulated
	// by one settle row: the ledger never lets a batch settle above its reservation, so this is the only way to meet an exhausted mandate)
	rows, err = r.Store.R.Query(`SELECT t.id FROM jobs j JOIN tracks t ON t.id = j.track_id WHERE j.batch_id = ?`, batch)
	if err != nil {
		t.Fatal(err)
	}
	var children []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		children = append(children, id)
	}
	rows.Close()
	for _, id := range children {
		h.OK(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 5})
	}
	var jobID string
	var usedN int64
	if err := r.Store.R.QueryRow(`SELECT COALESCE(SUM(micro_usd), 0), (SELECT id FROM jobs WHERE batch_id = ? LIMIT 1) FROM ledger WHERE batch_id = ? AND kind = 'settle' AND reason <> 'excess'`, batch, batch).Scan(&usedN, &jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store.W.Exec(`INSERT INTO ledger(at, kind, micro_usd, batch_id, job_id, principal_id, reason) VALUES (?, 'settle', ?, ?, ?, 'agent:nightshift', 'attempt')`,
		store.FormatTime(r.Clock.Now()), 1_000_000-usedN-50_000, batch, jobID); err != nil {
		t.Fatal(err)
	}
	r.Clock.Advance(24 * time.Hour)
	before = nightCounts(t, r.Rig)
	next, err := time.Parse(time.DateOnly, night1)
	if err != nil {
		t.Fatal(err)
	}
	night2 := next.AddDate(0, 0, 1).Format(time.DateOnly)
	night.Run(context.Background(), schedule.Key{Day: night2, At: "01:30", Planned: r.Clock.Now()})
	if after := nightCounts(t, r.Rig); after != before {
		t.Fatalf("a skipped night wrote: %s -> %s", before, after)
	}
	skipped := obj(arr(obj(h.OK(t, "GET", "/api/night-shift", nil))["runs"])[0])
	// budget is a reason the night can trim for; with no take left to trim to, the night is over its ceiling (as built, M6N-N2)
	if skipped["night"] != night2 || skipped["state"] != "skipped" || skipped["reason"] != "over_ceiling" || skipped["batch_id"] != nil || skipped["takes"] != float64(0) {
		t.Fatalf("second night: %v", skipped)
	}
	var skips int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE op = 'nightshift:skip' AND status = 200`).Scan(&skips); err != nil || skips != 1 {
		t.Fatalf("%d skip rows (%v)", skips, err)
	}
}
