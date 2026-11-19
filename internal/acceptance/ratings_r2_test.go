package acceptance_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/config"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestListenerActivityLeavesOwnerStateAlone is the U1-R2 safety net (C307, C306.7; D-U1-9): a Listener rates every take and
// plays every take through the real endpoints, and nothing the Owner curates from moves: stats and histograms, Song state
// and picks, next actions, unheard, the track's listening block and heat, and the Owner's own stars.
func TestListenerActivityLeavesOwnerStateAlone(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, Files: true})
	// the fixture's Owner plays are one human principal; that is the Owner key the listening readers match
	var ownerPID string
	if err := r.Store.R.QueryRow(`SELECT principal_id FROM listen_events WHERE principal_kind = 'human' LIMIT 1`).Scan(&ownerPID); err != nil {
		t.Fatal(err)
	}
	r.Store.SetOwner(strings.TrimPrefix(ownerPID, "human:"))
	var tracks []string
	rows, err := r.Store.R.Query(`SELECT id FROM tracks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, id)
	}
	_ = rows.Close()
	owner := r.Human(t)
	carol := r.User(t, servertest.UserListener, config.RoleListener)

	// views reads the Owner-curated state; the per-user rating shape is the one thing allowed to change, so it is reduced to
	// the Owner's own row before comparing.
	views := func() map[string]any {
		out := map[string]any{}
		for _, p := range []string{"/api/stats", "/api/songs?limit=100", "/api/next-actions", "/api/tracks?unheard=true&limit=100&status=draft&status=to_relisten&status=final"} {
			out[p] = owner.OK(t, "GET", p, nil)
		}
		for _, id := range tracks {
			tr := obj(owner.OK(t, "GET", "/api/tracks/"+id, nil))
			if rt := obj(tr["rating"]); rt != nil {
				tr["rating"] = map[string]any{"human": rt["human"], "mine": rt["mine"], "critic": rt["critic"]}
			}
			out["track "+id] = tr
			out["heat "+id] = owner.OK(t, "GET", "/api/tracks/"+id+"/heat", nil)
		}
		return out
	}
	// the stats timestamp (if any) is not state
	norm := func(m map[string]any) map[string]any {
		b, _ := json.Marshal(m)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		if s := obj(out["/api/stats"]); s != nil {
			delete(s, "generated_at")
		}
		return out
	}
	before := norm(views())
	if n, _ := obj(before["/api/songs?limit=100"])["total"].(float64); n < 3 {
		t.Fatalf("the fixture has %v songs; the Song state half of this test would be vacuous", n)
	}

	at := lt.FixedNow // the rig clock stands at the fixture time
	for i, id := range tracks {
		if resp := carol.Do(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 5, "verdict": "CANARY-NOTE-900"}); resp.Status != 200 {
			t.Fatalf("carol rates %s: %d %s", id, resp.Status, resp.Body)
		}
		sess := fmt.Sprintf("carol-session-%d", i)
		evs := []map[string]any{}
		for j, kind := range []string{"start", "complete"} {
			evs = append(evs, map[string]any{
				"id": fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xc0000+i*10+j), "track_id": id, "session_id": sess, "kind": kind,
				"client_at": at.Add(-time.Duration(30-j) * time.Second).Format(time.RFC3339),
			})
		}
		resp := carol.Do(t, "POST", "/api/listen-events", map[string]any{"events": evs})
		if resp.Status/100 != 2 || strings.Contains(string(resp.Body), `"rejected":[{`) {
			t.Fatalf("carol plays %s: %d %s", id, resp.Status, resp.Body)
		}
	}
	// the rows are really there (the test would pass vacuously otherwise) ...
	var nRatings, nEvents int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM ratings WHERE rater = 'carol'`).Scan(&nRatings); err != nil {
		t.Fatal(err)
	}
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM listen_events WHERE principal_id = 'human:carol'`).Scan(&nEvents); err != nil {
		t.Fatal(err)
	}
	if nRatings != len(tracks) || nEvents != 2*len(tracks) {
		t.Fatalf("carol wrote %d ratings and %d events", nRatings, nEvents)
	}
	// ... and her own view of them is hers
	tr := obj(carol.OK(t, "GET", "/api/tracks/"+lt.TrackCitta, nil))
	if m := obj(obj(tr["rating"])["mine"]); m == nil || m["stars"] != float64(5) {
		t.Errorf("carol's own rating of Citta: %v", tr["rating"])
	}
	if strings.Contains(string(owner.Do(t, "GET", "/api/tracks/"+lt.TrackCitta, nil).Body), `"carol"`) == false {
		t.Errorf("the Owner no longer sees carol among the raters")
	}

	after := norm(views())
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("a Listener's rating and plays moved Owner-derived state (-before +after):\n%s", diff)
	}
}
