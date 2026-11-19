package acceptance_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// TestRealListenerDeviceAndSubsonicClient is the U1-D1 acceptance row (D-U1-14, C312, C314.5). A Listener pairs a REAL phone and
// creates a REAL Subsonic client through the real pairing and creation operations (not an injected principal); both then act as
// the Listener: no discarded take by any path, clean renditions only (never the master's bytes or hash), ratings that land on the
// Listener's own row, every Owner-only door shut, and not one piece of Owner-derived state moves. The Owner's device and client
// are checked to keep today's shape beside them.
func TestRealListenerDeviceAndSubsonicClient(t *testing.T) {
	t.Parallel()
	r, owner, _, carolWeb, tn := v3Rig(t)
	var ownerPID string
	if err := r.Store.R.QueryRow(`SELECT principal_id FROM listen_events WHERE principal_kind = 'human' LIMIT 1`).Scan(&ownerPID); err != nil {
		t.Fatal(err)
	}
	r.Store.SetOwner(strings.TrimPrefix(ownerPID, "human:"))
	discarded, kept := lt.TrackTwinB, lt.TrackTwinA
	if _, err := r.Store.W.Exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, discarded); err != nil {
		t.Fatal(err)
	}
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

	ownerDev := r.Device(t)
	ownerSub := r.Subsonic(t, subsonictest.ClientOpts{Name: "Owner app", Write: true})
	dev := r.DeviceFor(t, servertest.UserListener)
	bobDev := r.DeviceFor(t, servertest.UserEditor)
	sub := r.SubsonicClientFor(t, servertest.UserListener, subsonictest.ClientOpts{Name: "Carol app", Write: true})
	if _, err := r.Store.W.Exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, discarded); err != nil {
		t.Fatal(err)
	}
	owner.OK(t, "PUT", "/api/tracks/"+kept+"/rating", map[string]any{"stars": 4, "verdict": "owner verdict"})

	t.Run("owner shapes are unchanged", func(t *testing.T) {
		me := obj(ownerDev.OK(t, "GET", "/api/me", nil))
		if me["kind"] != "device" || strings.Contains(fmt.Sprint(me["scopes"]), "listen") && !strings.Contains(fmt.Sprint(me["scopes"]), "read") {
			t.Errorf("the Owner's device whoami: %v", me)
		}
		if resp := ownerDev.Do(t, "GET", "/api/offline-set", nil); resp.Status != 200 {
			t.Errorf("the Owner's device lost the offline set: %d %s", resp.Status, resp.Body)
		}
		if resp := ownerDev.Do(t, "GET", "/api/tracks/"+discarded, nil); resp.Status != 200 {
			t.Errorf("the Owner's device lost a discarded take: %d", resp.Status)
		}
		songOf := func(c *subsonictest.Client) map[string]any {
			var doc map[string]any
			resp := c.Call(t, "getSong", url.Values{"id": {kept}}, subsonictest.CallOpts{JSON: true})
			if err := json.Unmarshal(resp.Body, &doc); err != nil {
				t.Fatal(err)
			}
			return obj(obj(doc["subsonic-response"])["song"])
		}
		own, lis := songOf(ownerSub), songOf(sub)
		if own["comment"] == nil || own["size"] == nil || !strings.Contains(fmt.Sprint(own["path"]), "__") {
			t.Errorf("the Owner's client lost the master's shape: %v", own)
		}
		if lis["comment"] != nil || lis["size"] == own["size"] || strings.Contains(fmt.Sprint(lis["path"]), "__") {
			t.Errorf("the Listener's client carries the style, the seed or the master's size: %v", lis)
		}
	})

	t.Run("no discarded takes", func(t *testing.T) {
		if resp := dev.Do(t, "GET", "/api/tracks/"+discarded, nil); resp.Status != 404 {
			t.Errorf("device GET discarded take: %d %s", resp.Status, resp.Body)
		}
		if resp := dev.Do(t, "GET", "/api/tracks/"+discarded+"/media", nil); resp.Status != 404 {
			t.Errorf("device media of a discarded take: %d", resp.Status)
		}
		for _, p := range []string{"/api/tracks?status=discarded&limit=100", "/api/tracks?limit=100&status=draft&status=to_relisten&status=final&status=discarded"} {
			if body := string(dev.Do(t, "GET", p, nil).Body); strings.Contains(body, discarded) {
				t.Errorf("device %s lists the discarded take", p)
			}
		}
		for _, ep := range []string{"getSong", "stream", "download", "getCoverArt"} {
			if resp := sub.Call(t, ep, url.Values{"id": {discarded}}, subsonictest.CallOpts{JSON: true}); resp.HTTP == 200 && resp.Code == 0 {
				t.Errorf("Subsonic %s of a discarded take: %d %d", ep, resp.HTTP, resp.Code)
			}
		}
		for _, q := range []struct {
			ep string
			v  url.Values
		}{
			{"getAlbum", url.Values{"id": {"al-" + kept}}}, {"search3", url.Values{"query": {"twin"}}},
			{"getRandomSongs", url.Values{"size": {"50"}}}, {"getAlbumList2", url.Values{"type": {"newest"}}},
		} {
			if resp := sub.Call(t, q.ep, q.v, subsonictest.CallOpts{JSON: true}); bytes.Contains(resp.Body, []byte(discarded)) {
				t.Errorf("Subsonic %s names the discarded take: %s", q.ep, resp.Body)
			}
		}
	})

	t.Run("clean renditions only", func(t *testing.T) {
		master := tn[kept].MasterSHA
		ml := obj(dev.OK(t, "GET", "/api/tracks/"+kept+"/media", nil))
		for _, k := range []string{"flac", "mp3"} {
			b := fetch(t, r, obj(ml[k])["url"].(string))
			if sum256(b) == master || bytes.Contains(b, []byte("MM_")) {
				t.Errorf("device %s link serves the master or hidden tags", k)
			}
		}
		for _, f := range []string{"raw", "flac", "original", "mp3", ""} {
			q := url.Values{"id": {kept}}
			if f != "" {
				q.Set("format", f)
			}
			resp := sub.Call(t, "stream", q, subsonictest.CallOpts{})
			if resp.HTTP != 200 || sum256(resp.Body) == master || bytes.Contains(resp.Body, []byte("MM_")) || bytes.Contains(resp.Body, []byte(tn[kept].MasterPath)) {
				t.Errorf("Subsonic stream format %q: %d, master or hidden tags served", f, resp.HTTP)
			}
		}
		if dl := sub.Call(t, "download", url.Values{"id": {kept}}, subsonictest.CallOpts{}); dl.HTTP != 200 || sum256(dl.Body) == master {
			t.Errorf("Subsonic download: %d", dl.HTTP)
		}
		// the master itself, by its hash, is not reachable by the Listener's device
		if resp := dev.Do(t, "GET", "/media/"+master, nil); resp.Status == 200 {
			t.Errorf("the master is served to a Listener's device by hash")
		}
		if bytes.Contains(dev.Do(t, "GET", "/api/tracks/"+kept+"/media", nil).Body, []byte(master)) {
			t.Error("the media links name the master's hash")
		}
	})

	t.Run("owner-only doors are shut", func(t *testing.T) {
		for _, c := range [][2]string{{"GET", "/api/offline-set"}, {"GET", "/api/audit"}, {"GET", "/api/tokens"}, {"GET", "/api/spend"}, {"GET", "/api/ladder"}} {
			if resp := dev.Do(t, c[0], c[1], nil); resp.Status != 403 {
				t.Errorf("device %s %s: %d %s", c[0], c[1], resp.Status, resp.Body)
			}
		}
		for _, c := range [][2]string{{"GET", "/human/api/devices"}, {"POST", "/human/api/devices/pairing-codes"}, {"GET", "/human/api/subsonic-clients"}} {
			// a device cookie is not accepted under /human/ at all (401), and would be HUMAN_ONLY (403) if it were
			if resp := dev.Do(t, c[0], c[1], map[string]any{"name": "x"}, "Idempotency-Key", "k-door"); resp.Status != 401 && resp.Status != 403 {
				t.Errorf("device %s %s: %d %s", c[0], c[1], resp.Status, resp.Body)
			}
		}
		if resp := dev.Do(t, "GET", "/mcp", nil); resp.Status != 401 {
			t.Errorf("device on /mcp: %d", resp.Status)
		}
		for _, ep := range []string{"star", "unstar", "createBookmark", "getBookmarks", "savePlayQueue"} {
			if resp := sub.Call(t, ep, url.Values{"id": {kept}}, subsonictest.CallOpts{JSON: true}); resp.Code != 50 {
				t.Errorf("Subsonic %s by a Listener's client: code %d", ep, resp.Code)
			}
		}
	})

	t.Run("ratings are the Listener's own and the Owner's state does not move", func(t *testing.T) {
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
			b, _ := json.Marshal(out)
			var norm map[string]any
			_ = json.Unmarshal(b, &norm)
			if s := obj(norm["/api/stats"]); s != nil {
				delete(s, "generated_at")
			}
			return norm
		}
		before := views()
		at := lt.FixedNow
		for i, id := range tracks {
			if id == discarded {
				continue
			}
			if resp := dev.Do(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 2, "verdict": "CANARY-DEV-NOTE"}); resp.Status != 200 {
				t.Fatalf("device rates %s: %d %s", id, resp.Status, resp.Body)
			}
			evs := []map[string]any{}
			for j, kind := range []string{"start", "complete"} {
				evs = append(evs, map[string]any{"id": fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd1000+i*10+j), "track_id": id, "session_id": fmt.Sprintf("dev-session-%d", i),
					"kind": kind, "client_at": at.Add(-time.Duration(30-j) * time.Second).Format(time.RFC3339)})
			}
			if resp := dev.Do(t, "POST", "/api/listen-events", map[string]any{"events": evs}); resp.Status/100 != 2 || strings.Contains(string(resp.Body), `"rejected":[{`) {
				t.Fatalf("device plays %s: %d %s", id, resp.Status, resp.Body)
			}
		}
		// the Subsonic client rates and plays the kept take
		if resp := sub.Call(t, "setRating", url.Values{"id": {kept}, "rating": {"1"}}, subsonictest.CallOpts{JSON: true}); resp.Code != 0 || resp.HTTP != 200 {
			t.Fatalf("Subsonic setRating: %d %d %s", resp.HTTP, resp.Code, resp.Body)
		}
		if resp := sub.Call(t, "scrobble", url.Values{"id": {kept}}, subsonictest.CallOpts{JSON: true}); resp.Code != 0 {
			t.Fatalf("Subsonic scrobble: %d %s", resp.Code, resp.Body)
		}
		// the rows are really there, attributed to the Listener, and never the Owner's
		var nCarol, nOwner int
		if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM ratings WHERE rater = 'carol'`).Scan(&nCarol); err != nil || nCarol != len(tracks)-1 {
			t.Fatalf("carol's rows: %d %v", nCarol, err)
		}
		if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM ratings WHERE rater = 'owner' AND source = 'human'`).Scan(&nOwner); err != nil {
			t.Fatal(err)
		}
		var ownerStars int
		_ = r.Store.R.QueryRow(`SELECT stars FROM ratings WHERE track_id = ? AND rater = 'owner'`, kept).Scan(&ownerStars)
		if ownerStars != 4 {
			t.Errorf("the Owner's rating of the kept take is %d, want 4", ownerStars)
		}
		var carolStars int
		_ = r.Store.R.QueryRow(`SELECT stars FROM ratings WHERE track_id = ? AND rater = 'carol'`, kept).Scan(&carolStars)
		if carolStars != 1 { // the Subsonic setRating came last
			t.Errorf("carol's rating of the kept take is %d, want 1", carolStars)
		}
		var nDev int
		_ = r.Store.R.QueryRow(`SELECT COUNT(*) FROM listen_events WHERE principal_kind = 'device' AND principal_id IN (SELECT 'device:' || id FROM devices WHERE owner_key = 'carol' UNION SELECT 'device:' || id FROM subsonic_clients WHERE owner_key = 'carol')`).Scan(&nDev)
		if nDev < 2*(len(tracks)-1) {
			t.Fatalf("the Listener's phone and client wrote %d events", nDev)
		}
		if diff := cmp.Diff(before, views()); diff != "" {
			t.Errorf("a Listener's device and Subsonic client moved Owner-derived state (-before +after):\n%s", diff)
		}
		// each reads back its own rating
		if m := obj(obj(obj(dev.OK(t, "GET", "/api/tracks/"+kept, nil))["rating"])["mine"]); m == nil || m["stars"] != float64(1) {
			t.Errorf("the device's own rating: %v", m)
		}
		if body := string(dev.Do(t, "GET", "/api/tracks/"+kept, nil).Body); strings.Contains(body, "owner verdict") {
			t.Error("the device sees the Owner's verdict")
		}
	})

	t.Run("device lists, revocation and the limit", func(t *testing.T) {
		own := obj(carolWeb.OK(t, "GET", "/human/api/devices", nil))
		items, _ := own["items"].([]any)
		if len(items) != 1 || obj(items[0])["owner"] != nil {
			t.Errorf("carol's list: %v", own)
		}
		all := obj(owner.OK(t, "GET", "/human/api/devices", nil))
		owners := map[string]int{}
		for _, it := range all["items"].([]any) {
			owners[fmt.Sprint(obj(it)["owner"])]++
		}
		if owners["owner"] < 1 || owners["carol"] != 1 || owners["bob"] != 1 {
			t.Errorf("the Owner's list by owner column: %v", owners)
		}
		bobID := ""
		for _, it := range all["items"].([]any) {
			if obj(it)["owner"] == "bob" {
				bobID, _ = obj(it)["id"].(string)
			}
		}
		if resp := carolWeb.Do(t, "POST", "/human/api/devices/"+bobID+"/revoke", nil); resp.Status != 404 {
			t.Errorf("carol revokes bob's device: %d %s", resp.Status, resp.Body)
		}
		if resp := bobDev.Do(t, "GET", "/api/me", nil); resp.Status != 200 {
			t.Errorf("bob's device after the refused revoke: %d", resp.Status)
		}
		if resp := owner.Do(t, "POST", "/human/api/devices/"+bobID+"/revoke", nil); resp.Status != 200 {
			t.Errorf("the Owner revokes bob's device: %d %s", resp.Status, resp.Body)
		}
		if resp := bobDev.Do(t, "GET", "/api/me", nil); resp.Status != 401 {
			t.Errorf("bob's revoked device: %d", resp.Status)
		}
		// a request body cannot name the owner of a pairing code
		if resp := carolWeb.Do(t, "POST", "/human/api/devices/pairing-codes", map[string]any{"name": "sneaky", "owner": "owner", "owner_key": "owner"}, "Idempotency-Key", "k-sneak"); resp.Status == 201 {
			t.Error("a body naming an owner was accepted")
		}
		for range 4 { // carol holds 1 live device: pair four more, the 6th is refused
			r.DeviceFor(t, servertest.UserListener)
		}
		resp := carolWeb.Do(t, "POST", "/human/api/devices/pairing-codes", map[string]any{"name": "more"}, "Idempotency-Key", "k-sixth")
		if resp.Status != 422 || !strings.Contains(string(resp.Body), "DEVICE_LIMIT") {
			t.Errorf("the 6th device: %d %s", resp.Status, resp.Body)
		}
	})
}
