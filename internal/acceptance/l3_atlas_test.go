package acceptance_test

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// atlasAll follows next_cursor to the end and returns the first page (header, totals, axes) and every point.
func atlasAll(t *testing.T, c *servertest.Client, query string) (atlas.Atlas, []atlas.Point) {
	t.Helper()
	var first atlas.Atlas
	var pts []atlas.Point
	cursor := ""
	for page := 0; ; page++ {
		q := "/api/atlas?limit=600&" + query
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		resp := c.Do(t, "GET", q, nil)
		if resp.Status != 200 {
			t.Fatalf("atlas page %d: %d %s", page, resp.Status, resp.Body)
		}
		var a atlas.Atlas
		if err := json.Unmarshal(resp.Body, &a); err != nil {
			t.Fatal(err)
		}
		if page == 0 {
			first = a
		}
		pts = append(pts, a.Points...)
		if a.NextCursor == nil {
			return first, pts
		}
		cursor = *a.NextCursor
	}
}

// TestAcceptance_L3_AtlasRendersMirror: the Atlas of the settled L3 library (the mirror assertions that need only features rows;
// the full mirror is INT-B). Tempo x energy returns every Song with both values, measured ones with basis m, drafts with d and
// p, and the totals equal an independent count over song_features; family x vocal returns every live Song.
func TestAcceptance_L3_AtlasRendersMirror(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, L3: true, RealFeatures: true})
	r.Settle(t)
	agent, _ := r.Agent(t, auth.ScopeRead)

	head, pts := atlasAll(t, agent, "x=tempo&y=energy")
	const live = `s.retired_at IS NULL AND s.hidden_at IS NULL`
	want := count(t, r, `SELECT COUNT(*) FROM song_features sf JOIN songs s ON s.id = sf.song_id WHERE `+live+` AND sf.bpm_x100 IS NOT NULL AND sf.energy_x10000 IS NOT NULL`)
	if want < 14 || len(pts) != want || head.Totals.Plotted != want {
		t.Fatalf("tempo x energy: %d points, plotted %d, independent count %d", len(pts), head.Totals.Plotted, want)
	}
	basis := func(col, b string) int {
		return count(t, r, `SELECT COUNT(*) FROM song_features sf JOIN songs s ON s.id = sf.song_id WHERE `+live+` AND sf.bpm_x100 IS NOT NULL AND sf.energy_x10000 IS NOT NULL AND sf.`+col+` = ?`, b)
	}
	got := head.Totals.Basis
	if got.X.M != basis("bpm_basis", "measured") || got.X.D != basis("bpm_basis", "declared") || got.X.L != basis("bpm_basis", "plan") ||
		got.Y.M != basis("energy_basis", "measured") || got.Y.P != basis("energy_basis", "predicted") {
		t.Errorf("totals.basis %+v differs from the independent counts", got)
	}
	if got.X.M == 0 || got.X.D == 0 || got.Y.P == 0 {
		t.Errorf("the library should hold measured, declared and predicted values: %+v", got)
	}
	seen := map[string]bool{}
	for _, p := range pts {
		if seen[p.ID] {
			t.Errorf("point %s twice", p.ID)
		}
		seen[p.ID] = true
		if p.XB == "m" && p.YB != "m" && p.YB != "p" {
			t.Errorf("%s: bases %q %q", p.ID, p.XB, p.YB)
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM song_features sf JOIN songs s ON s.id = sf.song_id WHERE `+live+` AND sf.bpm_basis = 'declared' AND sf.energy_basis = 'predicted'`); n == 0 {
		t.Error("no draft-like Song (declared tempo, predicted energy) to check")
	}

	cat, cpts := atlasAll(t, agent, "x=family&y=vocal&color_by=muse")
	total := int(agent.OK(t, "GET", "/api/songs?limit=1", nil).(map[string]any)["total"].(float64))
	if len(cpts) != total || cat.Totals.Plotted != total || cat.Totals.Missing.X != 0 || cat.Totals.Missing.Y != 0 {
		t.Errorf("family x vocal: %d points, plotted %d, list_songs total %d, missing %+v", len(cpts), cat.Totals.Plotted, total, cat.Totals.Missing)
	}
	if len(cat.Axes.X.Categories) == 0 || cat.Axes.X.Categories[len(cat.Axes.X.Categories)-1].Lo >= 1 {
		t.Errorf("bands %+v", cat.Axes.X.Categories)
	}
	if len(cat.Muses) == 0 || len(cat.Muses) > 12 {
		t.Errorf("Muse dictionary %v", cat.Muses)
	}
}

// TestAcceptance_L3_MirrorScale: the mirror of the real library's structure (120 Muses, 4 originals, 5 lossy groups, no L3
// fixture) settled with the real feature step, as serve starts it. Every live Song has a feature row; tempo x energy plots every
// Song that has both values (drafts hollow: declared tempo, predicted energy) and the totals equal an independent count;
// family x vocal plots every Song; the next actions list the unrendered Muses five at a time with the rest counted; and both
// verifiers are silent.
func TestAcceptance_L3_MirrorScale(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2Mirror: true, RealFeatures: true})
	rep := r.Settle(t)
	t.Logf("settle: %+v", rep)
	agent, _ := r.Agent(t, auth.ScopeRead)

	const live = `s.retired_at IS NULL AND s.hidden_at IS NULL`
	songs := count(t, r, `SELECT COUNT(*) FROM songs s WHERE `+live)
	withRow := count(t, r, `SELECT COUNT(*) FROM song_features sf JOIN songs s ON s.id = sf.song_id WHERE `+live)
	if songs < 120 || withRow == 0 || withRow > songs {
		t.Fatalf("%d live Songs, %d with a feature row", songs, withRow)
	}
	both := count(t, r, `SELECT COUNT(*) FROM song_features sf JOIN songs s ON s.id = sf.song_id WHERE `+live+` AND sf.bpm_x100 IS NOT NULL AND sf.energy_x10000 IS NOT NULL`)
	head, pts := atlasAll(t, agent, "x=tempo&y=energy")
	if both == 0 || len(pts) != both || head.Totals.Plotted != both {
		t.Errorf("tempo x energy: %d points, plotted %d, independent count %d", len(pts), head.Totals.Plotted, both)
	}
	if head.Totals.Basis.Y.P == 0 {
		t.Errorf("the mirror holds draft Songs, predicted energy must be counted: %+v", head.Totals.Basis)
	}
	hollow := 0
	for _, p := range pts {
		if p.YB == "p" {
			hollow++
		}
	}
	if hollow != head.Totals.Basis.Y.P {
		t.Errorf("%d hollow points, totals say %d", hollow, head.Totals.Basis.Y.P)
	}
	cat, cpts := atlasAll(t, agent, "x=family&y=vocal&color_by=muse")
	if len(cpts) != songs || cat.Totals.Plotted != songs {
		t.Errorf("family x vocal: %d points, plotted %d, live Songs %d", len(cpts), cat.Totals.Plotted, songs)
	}

	na := obj(agent.OK(t, "GET", "/api/next-actions?limit=50", nil))
	unrendered, listed := 0, 0
	for _, it := range na["items"].([]any) {
		if obj(it)["code"] == "muse_unrendered" {
			listed++
		}
	}
	for _, g := range na["groups"].([]any) {
		if obj(g)["code"] == "muse_unrendered" {
			unrendered = int(obj(g)["more"].(float64))
		}
	}
	if listed != 5 || unrendered < 100 {
		t.Errorf("muse_unrendered: %d listed, %d more", listed, unrendered)
	}
	if an, err := r.Services.Features.Verify(t.Context()); err != nil || len(an) != 0 {
		t.Errorf("features.Verify: %v %v", an, err)
	}
	if an, err := r.Provenance.Verify(t.Context()); err != nil || len(an) != 0 {
		t.Errorf("provenance.Verify: %v %v", an, err)
	}
}
