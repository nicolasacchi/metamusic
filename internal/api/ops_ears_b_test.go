package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const earsBLyrics = "[Verse 1]\nla la luna\n[Chorus]\nsing it loud\n[Outro]\nfade away"

func earsBRig(t *testing.T) *servertest.Rig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2b: servertest.M2bOptions{Fixture: true}})
	r.Settle(t)
	return r
}

// storeEarsDoc writes an analysis document of the track as the ingest bridge would (file and artifact row) and analyses it.
func storeEarsDoc(t *testing.T, r *servertest.Rig, trackID string, doc []byte) {
	t.Helper()
	dir := filepath.Join(r.DataDir, "takes", trackID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "analysis.json"), doc, 0o444); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	if _, err := r.Store.W.Exec(`INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?, 'analysis.json', ?, ?, ?, ?)`,
		trackID, hex.EncodeToString(sum[:]), len(doc), "takes/"+trackID+"/analysis.json", store.FormatTime(lt.FixedNow)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Services.Ears.AnalyzeTierB(context.Background(), trackID); err != nil {
		t.Fatal(err)
	}
}

// TestGetTrackTierB_REST: a take without a listening check answers 409 ANALYSIS_UNAVAILABLE with its state; after the analysis the
// Owner, an Editor and a read-scoped agent get the same 200 body; a Listener is refused by scope; an unknown take is 404; the
// GET writes nothing (the reader pool is read-only and the counts stay).
func TestGetTrackTierB_REST(t *testing.T) {
	t.Parallel()
	r := earsBRig(t)
	owner := r.Human(t)
	editor := r.User(t, "bob", config.RoleEditor)
	listener := r.User(t, "carol", config.RoleListener)
	agent, _ := r.Agent(t, auth.ScopeRead)
	path := "/api/tracks/" + lt.TrackLuna + "/tier-b"

	resp := owner.Do(t, "GET", path, nil)
	if resp.Status != 409 {
		t.Fatalf("before: %d %s", resp.Status, resp.Body)
	}
	p := obj(t, resp.JSON(t))
	d := obj(t, p["details"])
	if p["code"] != "ANALYSIS_UNAVAILABLE" || d["analyzer"] != "tier_b" || d["state"] != "none" {
		t.Errorf("problem = %s", resp.Body)
	}
	if resp := owner.Do(t, "GET", "/api/tracks/no-such-track/tier-b", nil); resp.Status != 404 {
		t.Errorf("unknown track: %d %s", resp.Status, resp.Body)
	}

	storeEarsDoc(t, r, lt.TrackLuna, tierbtest.FromLyrics(earsBLyrics, tierbtest.Opts{Subs: map[string]string{"luna": "lena"}}))
	count := func() int {
		var n int
		if err := r.Store.R.QueryRow(`SELECT (SELECT count(*) FROM analyses) + (SELECT count(*) FROM sections) + (SELECT count(*) FROM artifacts)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	resp = owner.Do(t, "GET", path, nil)
	if resp.Status != 200 {
		t.Fatalf("after: %d %s", resp.Status, resp.Body)
	}
	b := obj(t, resp.JSON(t))
	if b["track_id"] != lt.TrackLuna || b["state"] != "ok" || b["status"] != "ok" || b["language"] != "it" || b["adherence"] != nil {
		t.Errorf("body = %s", resp.Body)
	}
	dict := obj(t, b["diction"])
	if overall := obj(t, dict["overall"]); overall["cer"].(float64) <= 0 || overall["ref_words"] != float64(8) {
		t.Errorf("diction.overall = %v", overall)
	}
	if secs, _ := dict["sections"].([]any); len(secs) != 3 {
		t.Errorf("diction.sections = %v", dict["sections"])
	}
	if ms, _ := b["measured_sections"].([]any); len(ms) != 3 {
		t.Errorf("measured_sections = %v", b["measured_sections"])
	}
	if _, ok := b["blind_spots"].([]any); !ok {
		t.Errorf("blind_spots missing: %s", resp.Body)
	}
	for _, c := range []struct {
		name string
		c    *servertest.Client
	}{{"editor", editor}, {"agent", agent}} {
		got := c.c.Do(t, "GET", path, nil)
		if got.Status != 200 || string(got.Body) != string(resp.Body) {
			t.Errorf("%s: %d, body differs from the Owner's", c.name, got.Status)
		}
	}
	if got := listener.Do(t, "GET", path, nil); got.Status != 403 {
		t.Errorf("listener: %d %s, want 403", got.Status, got.Body)
	}
	if after := count(); after != before {
		t.Errorf("GET wrote rows: %d -> %d", before, after)
	}
}
