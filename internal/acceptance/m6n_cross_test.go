package acceptance_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/morning/morningtest"
	"github.com/nicolasacchi/metamusic/internal/schedule"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// TestAcceptance_M6N_NightFeedsMorningCrate (C389.2, lane loop-int): on the fake backend, with no spend outside the rig, a live mandate
// and an enabled night make one night of takes; the next 07:00 Morning Crate carries those takes first and blind; the Owner's blind stars
// reveal the crate; the reveal writes the ratings and the shared playlist a Subsonic client then lists. The money bounds hold throughout:
// the night's ceiling is at most $1.00, it fits the mandate's remaining budget, and no global cap moved.
func TestAcceptance_M6N_NightFeedsMorningCrate(t *testing.T) {
	r := newProvRig(t, "fake", archivetest.SyntheticArchive(t, "acceptance"),
		servertest.Options{M2b: servertest.M2bOptions{Backfill: true}, MorningBuilder: &morningtest.Fake{}})
	h := r.Human(t)
	night, crates := r.Services.NightShift, r.Services.Deps.Morning
	if night == nil || crates == nil {
		t.Fatal("the rig built no night shift or no morning crate service")
	}
	capsOf := func() string {
		var s string
		if err := r.Store.R.QueryRow(`SELECT monthly_micro_usd || '|' || batch_micro_usd || '|' || human_daily_micro_usd || '|' || agent_daily_micro_usd || '|' || auto_approve_takes || '|' || auto_approve_micro_usd
			FROM spend_caps WHERE id = 1`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	capsBefore := capsOf()

	// the Owner has rated three lineages (the night works on rated lineages); the night's takes are the only new takes
	rows, err := r.Store.R.Query(`SELECT t.root_track_id, MIN(t.id) FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id
		WHERE rc.engine = 'yue2' GROUP BY 1 ORDER BY 1 LIMIT 3`)
	if err != nil {
		t.Fatal(err)
	}
	var seeds []string
	for rows.Next() {
		var root, track string
		if err := rows.Scan(&root, &track); err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, track)
	}
	rows.Close()
	if len(seeds) != 3 {
		t.Fatalf("the archive holds %d yue2 lineages, want 3", len(seeds))
	}
	for _, id := range seeds {
		h.OK(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 5})
	}

	// the Owner's mandate ($1.00 over a week, $1.00 a night) and the night
	until := r.Clock.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339)
	const mandateBudget = 800_000 // under the night's $1.00 ceiling: three takes estimate to about $0.60 and a budget of $0.60 trims the night to two, so the mandate's budget, not the per-night ceiling, is what bounds it
	mandateID := obj(h.OK(t, "POST", "/human/api/mandates", map[string]any{"holder": "night_shift", "title": "Night shift", "budget_micro_usd": mandateBudget, "valid_until": until,
		"scope": map[string]any{"modes": []string{"new_seed", "style_edit"}, "engines": []string{"yue2"}, "min_stars": 3, "root_track_ids": nil,
			"max_takes_per_lineage": 5, "max_takes_per_batch": 6, "max_batch_ceiling_micro_usd": mandateBudget, "max_batches_per_day": 1, "tier_b": false}},
		"Idempotency-Key", "cross-mandate"))["id"].(string)
	h.OK(t, "PUT", "/human/api/night-shift", map[string]any{"enabled": true, "at": "01:30", "max_takes": 3, "min_stars": 4, "explore_ppm": 300000, "listening_checks": "off"})
	h.OK(t, "PUT", "/api/morning-crates/settings", map[string]any{"enabled": true})

	// the night runs and every job settles
	pv := obj(h.OK(t, "GET", "/api/night-shift/preview", nil))
	night1 := pv["night"].(string)
	night.Run(context.Background(), schedule.Key{Day: night1, At: "01:30", Planned: r.Clock.Now()})
	run := obj(arr(obj(h.OK(t, "GET", "/api/night-shift", nil))["runs"])[0])
	batch, _ := run["batch_id"].(string)
	if batch == "" || run["takes"] != float64(3) {
		t.Fatalf("the night made no batch of three takes: %v", run)
	}
	pump(t, r.Rig, func() bool {
		var state string
		_ = r.Store.R.QueryRow(`SELECT state FROM batches WHERE id = ?`, batch).Scan(&state)
		return state == "finished" && r.workers(t) == 0
	})
	nightTakes := map[string]bool{}
	trows, err := r.Store.R.Query(`SELECT track_id FROM jobs WHERE batch_id = ? AND state = 'completed' AND track_id IS NOT NULL`, batch)
	if err != nil {
		t.Fatal(err)
	}
	for trows.Next() {
		var id string
		if err := trows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		nightTakes[id] = true
	}
	trows.Close()
	if len(nightTakes) != 3 {
		t.Fatalf("the night completed %d takes, want 3", len(nightTakes))
	}

	// money bounds: the estimate ceiling is within $1.00 and the mandate, what was settled fits the ceiling and the mandate's budget, and no cap moved
	var ceiling, settled, reserved, used int64
	if err := r.Store.R.QueryRow(`SELECT ceiling_micro_usd FROM batches WHERE id = ?`, batch).Scan(&ceiling); err != nil {
		t.Fatal(err)
	}
	if err := r.Store.R.QueryRow(`SELECT COALESCE(SUM(CASE WHEN kind = 'settle' THEN micro_usd END), 0), COALESCE(SUM(CASE WHEN kind = 'reserve' THEN micro_usd END), 0)
		FROM ledger WHERE batch_id = ?`, batch).Scan(&settled, &reserved); err != nil {
		t.Fatal(err)
	}
	mu := obj(h.OK(t, "GET", "/api/mandates/"+mandateID, nil))
	used = int64(obj(mu["usage"])["used_micro_usd"].(float64))
	if ceiling <= 0 || ceiling > mandateBudget || settled <= 0 || settled > ceiling || used > mandateBudget || used < settled {
		t.Fatalf("money: ceiling %d settled %d reserved %d mandate used %d", ceiling, settled, reserved, used)
	}
	if capsAfter := capsOf(); capsAfter != capsBefore {
		t.Fatalf("a cap moved: %s -> %s", capsBefore, capsAfter)
	}
	// the batch was approved by the mandate (not a click, not the global auto-approve cap), and the mandate's own id is the authority
	var via, viaMandate string
	if err := r.Store.R.QueryRow(`SELECT a.via, COALESCE(n.mandate_id, '') FROM approvals a JOIN night_shift_runs n ON n.batch_id = a.batch_id WHERE a.batch_id = ?`, batch).Scan(&via, &viaMandate); err != nil || via != "mandate" || viaMandate != mandateID {
		t.Fatalf("approval: via %q mandate %q (%v), want mandate %s", via, viaMandate, err, mandateID)
	}

	// 07:00 the same day: the crate is built from the night's takes. It spends nothing.
	ledgerBefore := nightCounts(t, r.Rig)
	crates.RunScheduled(context.Background(), schedule.Key{Day: night1, At: "07:00", Planned: r.Clock.Now()})
	crates.Wait()
	if after := nightCounts(t, r.Rig); after != ledgerBefore {
		t.Fatalf("the crate spent or created generation rows: %s -> %s", ledgerBefore, after)
	}
	list := obj(h.OK(t, "GET", "/api/morning-crates", nil))
	items := arr(list["items"])
	if len(items) != 1 {
		t.Fatalf("crates: %v", list)
	}
	crateID := obj(items[0])["id"].(string)
	crate := obj(h.OK(t, "GET", "/api/morning-crates/"+crateID, nil))
	if crate["state"] != "ready" || crate["revealed"] != false || crate["playlist_id"] != nil {
		t.Fatalf("crate: %v", crate)
	}
	type item struct {
		n      int
		side   int
		anchor bool
		track  string
		source string
		start  int64
	}
	var its []item
	irows, err := r.Store.R.Query(`SELECT n, side_index, anchor, track_id, source, audio_start_frame FROM morning_crate_items WHERE crate_id = ? ORDER BY n`, crateID)
	if err != nil {
		t.Fatal(err)
	}
	for irows.Next() {
		var it item
		var anchor int
		if err := irows.Scan(&it.n, &it.side, &anchor, &it.track, &it.source, &it.start); err != nil {
			t.Fatal(err)
		}
		it.anchor = anchor != 0
		its = append(its, it)
	}
	irows.Close()
	var fromNight, rateable int
	seenOther := false
	for _, it := range its {
		if it.anchor {
			continue
		}
		rateable++
		if nightTakes[it.track] {
			fromNight++
			if it.source != "night_shift" || seenOther {
				t.Fatalf("night take %s has source %q (after another source: %v)", it.track, it.source, seenOther)
			}
		} else {
			seenOther = true
		}
	}
	if fromNight != 3 || rateable != 3 {
		t.Fatalf("crate carries %d night takes among %d non-anchor chapters, want 3 of 3", fromNight, rateable)
	}

	// blind: before the reveal nothing in the crate names a take, the lineage or the night's batch
	for _, c := range arr(crate["chapters"]) {
		ch := obj(c)
		if ch["track"] != nil || ch["anchor"] != nil || ch["gain_db"] != nil {
			t.Fatalf("a chapter is not blind before the reveal: %v", ch)
		}
	}
	for id := range nightTakes {
		if strings.Contains(string(h.Do(t, "GET", "/api/morning-crates/"+crateID, nil).Body), id) {
			t.Fatalf("the blind crate names take %s", id)
		}
	}
	// the agent cannot rate or reveal it
	ag, _ := r.Agent(t, auth.ScopeWrite)
	agentEvs := []map[string]any{{"id": "01900000-0000-7000-8000-0000000cffff", "side": 0, "chapter": 1, "kind": "rate", "stars": 3, "side_ms": 1000, "client_at": r.Clock.Now().Format(time.RFC3339)}}
	for name, resp := range map[string]*servertest.Response{
		"reveal": ag.Do(t, "POST", "/api/morning-crates/"+crateID+"/reveal", nil, "Idempotency-Key", "cross-agent-reveal"),
		"events": ag.Do(t, "POST", "/api/morning-crates/"+crateID+"/events", map[string]any{"events": agentEvs}, "Idempotency-Key", "cross-agent-events"),
	} {
		if body, _ := resp.JSON(t).(map[string]any); resp.Status != 403 || body["code"] != "HUMAN_ONLY" {
			t.Fatalf("agent %s: %d %s, want 403 HUMAN_ONLY", name, resp.Status, resp.Body)
		}
	}

	// blind stars on every non-anchor chapter reveal it in the same call
	stars := map[string]int{}
	var evs []map[string]any
	for i, it := range its {
		if it.anchor {
			continue
		}
		st := 2 + i%3 // 2, 3, 4: none equals the stars a lineage seed has, so the ratings visibly change
		stars[it.track] = st
		evs = append(evs, map[string]any{"id": fmt.Sprintf("01900000-0000-7000-8000-0000000c%04x", i), "side": it.side, "chapter": it.n, "kind": "rate", "stars": st,
			"side_ms": it.start*1000/48000 + 1000, "client_at": r.Clock.Now().Format(time.RFC3339)})
	}
	res := obj(h.OK(t, "POST", "/api/morning-crates/"+crateID+"/events", map[string]any{"events": evs}, "Idempotency-Key", "cross-events"))
	if res["revealed"] != true || res["accepted"] != float64(len(evs)) {
		t.Fatalf("events: %v", res)
	}
	crate = obj(h.OK(t, "GET", "/api/morning-crates/"+crateID, nil))
	if crate["revealed"] != true || crate["playlist_id"] == nil {
		t.Fatalf("crate after the stars: %v", crate)
	}
	named := 0
	for _, c := range arr(crate["chapters"]) {
		if tr, ok := obj(c)["track"].(map[string]any); ok && nightTakes[tr["id"].(string)] {
			named++
		}
	}
	if named != 3 {
		t.Fatalf("the reveal names %d night takes, want 3", named)
	}
	for id, st := range stars {
		var got int
		if err := r.Store.R.QueryRow(`SELECT stars FROM owner_ratings WHERE track_id = ?`, id).Scan(&got); err != nil || got != st {
			t.Fatalf("rating of %s: %d (%v), want %d", id, got, err, st)
		}
	}

	// the shared playlist is visible to a Subsonic client, and it lists the night's takes
	sub := r.Subsonic(t, subsonictest.ClientOpts{})
	pl := sub.Call(t, "getPlaylists", url.Values{}, subsonictest.CallOpts{JSON: true})
	if pl.Status != "ok" || !strings.Contains(string(pl.Body), "Morning crate") {
		t.Fatalf("getPlaylists: %s", pl.Body)
	}
	one := sub.Call(t, "getPlaylist", url.Values{"id": {crate["playlist_id"].(string)}}, subsonictest.CallOpts{JSON: true})
	if one.Status != "ok" {
		t.Fatalf("getPlaylist: %s", one.Body)
	}
	for id := range nightTakes {
		if !strings.Contains(string(one.Body), id) {
			t.Fatalf("the playlist misses night take %s: %s", id, one.Body)
		}
	}

	// the mandate was never raised: used is still within its budget and the caps are as they were
	if capsAfter := capsOf(); capsAfter != capsBefore {
		t.Fatalf("a cap moved by the end: %s -> %s", capsBefore, capsAfter)
	}
	mu = obj(h.OK(t, "GET", "/api/mandates/"+mandateID, nil))
	if obj(mu["usage"])["used_micro_usd"].(float64) > mandateBudget || mu["budget_micro_usd"].(float64) != mandateBudget {
		t.Fatalf("mandate after the crate: %v", mu)
	}
}
