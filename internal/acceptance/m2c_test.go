package acceptance_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// refRig is a rig over the imported reference archive (the e2e takes plus two recipe-less FLACs and
// six lossy MP3s, imported with both opt-ins) with the real clip service.
func refRig(t *testing.T, o servertest.Options) (*servertest.Rig, archivetest.Template) {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	tpl := archivetest.SyntheticArchive(t, "reference")
	o.Archive = &tpl
	o.M2b.Media = true
	return servertest.New(t, o), tpl
}

// refTrack is one library row of the reference archive, read from the database.
type refTrack struct {
	ID, Title, Master string
	Lossy             bool
	Engine            string
	SHA               string
}

func refTracks(t *testing.T, r *servertest.Rig) map[string]refTrack {
	t.Helper()
	rows, err := r.Store.R.Query(`SELECT t.id, t.title, t.master_path, t.lossy, rc.engine, t.master_sha256
		FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id WHERE rc.engine = 'unknown'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]refTrack{}
	for rows.Next() {
		var x refTrack
		var lossy int
		if err := rows.Scan(&x.ID, &x.Title, &x.Master, &lossy, &x.Engine, &x.SHA); err != nil {
			t.Fatal(err)
		}
		x.Lossy = lossy == 1
		out[strings.ToLower(x.Title)] = x
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func count(t *testing.T, r *servertest.Rig, q string, args ...any) int {
	t.Helper()
	var n int
	if err := r.Store.R.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sha256File(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// rangeMatrix checks the Range behaviour of one signed media URL.
func rangeMatrix(t *testing.T, r *servertest.Rig, url, contentType string, total int64) {
	t.Helper()
	type answer struct {
		StatusCode int
		Header     http.Header
	}
	get := func(rng string) (answer, []byte) {
		req, _ := http.NewRequest("GET", r.URL+url, nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return answer{resp.StatusCode, resp.Header}, b
	}
	resp, body := get("")
	if resp.StatusCode != 200 || int64(len(body)) != total || resp.Header.Get("Content-Type") != contentType || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("%s: full GET %d, %d bytes (want %d), type %q, accept-ranges %q", contentType, resp.StatusCode, len(body), total, resp.Header.Get("Content-Type"), resp.Header.Get("Accept-Ranges"))
	}
	full := body
	for _, c := range []struct {
		rng        string
		start, end int64
	}{{"bytes=0-99", 0, 99}, {"bytes=-50", total - 50, total - 1}, {fmt.Sprintf("bytes=%d-", total-10), total - 10, total - 1}} {
		resp, body := get(c.rng)
		want := fmt.Sprintf("bytes %d-%d/%d", c.start, c.end, total)
		if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Range") != want || !bytes.Equal(body, full[c.start:c.end+1]) {
			t.Errorf("%s %s: status %d, Content-Range %q (want %q), %d bytes", contentType, c.rng, resp.StatusCode, resp.Header.Get("Content-Range"), want, len(body))
		}
	}
	if resp, _ := get(fmt.Sprintf("bytes=%d-", total+10)); resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("%s: range past the end answered %d, want 416", contentType, resp.StatusCode)
	}
}

func mediaOf(t *testing.T, c *servertest.Client, id string) map[string]any {
	t.Helper()
	return obj(c.OK(t, "GET", "/api/tracks/"+id+"/media", nil))
}

// TestAcceptance_M2c_ReferenceFlac: the two recipe-less FLACs of the reference archive are ordinary,
// frozen, playable, judgeable library tracks that are visibly recipe-unknown (D-M2c-1, 5, 6, 12).
func TestAcceptance_M2c_ReferenceFlac(t *testing.T) {
	r, _ := refRig(t, servertest.Options{M2b: servertest.M2bOptions{ClipCacheMB: 1}})
	h := r.Human(t)
	refs := refTracks(t, r)
	a, b := refs["syn ref a"], refs["syn ref b"]
	if a.ID == "" || b.ID == "" || a.Lossy || b.Lossy {
		t.Fatalf("reference FLACs not found or lossy: %+v %+v", a, b)
	}

	for _, x := range []refTrack{a, b} {
		p := filepath.Join(r.DataDir, x.Master)
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(x.Master, ".flac") || !strings.HasPrefix(x.Master, "audio/masters/") || st.Mode().Perm() != 0o444 {
			t.Errorf("%s: master %s mode %v", x.Title, x.Master, st.Mode().Perm())
		}
		f, err := os.Open(p) //nolint:gosec // test file under the rig's data dir
		if err != nil {
			t.Fatal(err)
		}
		info, err := flacmeta.Read(f, st.Size())
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(info.Comments, "MM_REFERENCE=1") {
			t.Errorf("%s: tags %v lack MM_REFERENCE=1", x.Title, info.Comments)
		}
		for _, c := range info.Comments {
			if strings.HasPrefix(c, "MM_SEED=") {
				t.Errorf("%s: a reference take carries %s", x.Title, c)
			}
		}
		for _, kind := range []string{"mp3_320", "peaks"} {
			if n := count(t, r, `SELECT count(*) FROM renditions WHERE track_id = ? AND kind = ?`, x.ID, kind); n != 1 {
				t.Errorf("%s: %d %s renditions, want 1", x.Title, n, kind)
			}
		}
		for _, an := range []string{"loudness", "spectrum"} {
			if n := count(t, r, `SELECT count(*) FROM analyses WHERE track_id = ? AND analyzer = ?`, x.ID, an); n == 0 {
				t.Errorf("%s: no %s analysis", x.Title, an)
			}
		}
		if x.Engine != "unknown" {
			t.Errorf("%s: engine %q", x.Title, x.Engine)
		}
		v := obj(h.OK(t, "GET", "/api/tracks/"+x.ID, nil))
		if v["recipe_known"] != false || v["lossy"] != false {
			t.Errorf("%s: recipe_known %v lossy %v", x.Title, v["recipe_known"], v["lossy"])
		}

		// Range on both links
		m := mediaOf(t, h, x.ID)
		fl, mp := obj(m["flac"]), obj(m["mp3"])
		if fl == nil || m["lossy"] != false {
			t.Fatalf("%s: media links %v", x.Title, m)
		}
		rangeMatrix(t, r, fl["url"].(string), "audio/flac", int64(fl["bytes"].(float64)))
		rangeMatrix(t, r, mp["url"].(string), "audio/mpeg", int64(mp["bytes"].(float64)))
	}

	// search finds them by title and by the recipe_known filter
	found := obj(h.OK(t, "GET", "/api/tracks?q=ref&recipe_known=false&limit=100", nil))["items"].([]any)
	var ids []string
	for _, it := range found {
		ids = append(ids, obj(it)["id"].(string))
	}
	if !slices.Contains(ids, a.ID) || !slices.Contains(ids, b.ID) {
		t.Errorf("FTS 'ref' with recipe_known=false found %v", ids)
	}

	// judgment: rating, comment, tag, playlist
	h.OK(t, "PUT", "/api/tracks/"+a.ID+"/rating", map[string]any{"stars": 4})
	h.OK(t, "POST", "/api/tracks/"+a.ID+"/comments", map[string]any{"body": "reference note", "kind": "note"}, "Idempotency-Key", "ref-c1")
	h.OK(t, "PATCH", "/api/tracks/"+a.ID+"/tags", map[string]any{"add": []string{"anchor"}})
	pl := obj(h.OK(t, "POST", "/api/playlists", map[string]any{"name": "References", "track_ids": []string{a.ID, b.ID}}, "Idempotency-Key", "ref-pl"))["id"].(string)
	if got := obj(obj(h.OK(t, "GET", "/api/tracks/"+a.ID, nil))["rating"])["human"]; obj(got)["stars"] != 4.0 {
		t.Errorf("rating = %v", got)
	}
	if n := len(obj(h.OK(t, "GET", "/api/playlists/"+pl, nil))["items"].([]any)); n != 2 {
		t.Errorf("playlist holds %d tracks, want 2", n)
	}

	// a blind, loudness-matched duel between the two and a verdict
	d := obj(h.OK(t, "POST", "/api/duels", map[string]any{"a_track_id": a.ID, "b_track_id": b.ID, "blind": true}, "Idempotency-Key", "ref-duel"))
	duel := obj(h.OK(t, "GET", "/api/duels/"+d["id"].(string), nil))
	if duel["revealed"] != false || trackID(slot(duel, "a")) != "" {
		t.Errorf("duel is not blind: %v", duel)
	}
	la, _ := fetchClip(t, r, slot(duel, "a")["clip"].(map[string]any)["url"].(string))
	lb, _ := fetchClip(t, r, slot(duel, "b")["clip"].(map[string]any)["url"].(string))
	if la == 0 || la != lb {
		t.Errorf("clip lengths %d and %d", la, lb)
	}
	sub := obj(h.OK(t, "POST", "/api/duels/"+d["id"].(string)+"/verdicts", map[string]any{"verdict": "a", "latency_ms": 3000, "switches": 2}))
	if obj(sub["duel"])["revealed"] != true || !slices.Contains([]string{a.ID, b.ID}, fmt.Sprint(obj(sub["verdict"])["winner_track_id"])) {
		t.Errorf("verdict answer = %v", sub)
	}

	// songs.jsonl: nothing to export, with the note naming both
	resp := h.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{a.ID, b.ID}}}, "Idempotency-Key", "ref-jsonl")
	if resp.Status != http.StatusUnprocessableEntity {
		t.Fatalf("export of reference tracks answered %d: %s", resp.Status, resp.Body)
	}
	pr := obj(resp.JSON(t))
	notes, _ := obj(pr["details"])["notes"].([]any)
	if pr["code"] != "EXPORT_EMPTY" || len(notes) != 1 || obj(notes[0])["code"] != "recipe_unknown_excluded" {
		t.Errorf("export problem = %v", pr)
	} else if got, _ := obj(obj(notes[0])["details"])["track_ids"].([]any); len(got) != 2 {
		t.Errorf("note names %v, want both tracks", got)
	}

	// a mixed export keeps the known recipes and names every excluded reference track
	resp = h.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks"}, "Idempotency-Key", "ref-jsonl-all")
	if resp.Status/100 != 2 {
		t.Fatalf("export of all tracks answered %d: %s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), "recipe_unknown_excluded") {
		t.Errorf("mixed export lacks the exclusion note: %s", resp.Body)
	}

	// backup and restore keep both
	restoreVerifies(t, r, a, b)
}

// restoreVerifies takes a backup of the rig, restores it into an empty directory and checks that the
// reference masters are there byte for byte and the track count is the live one.
func restoreVerifies(t *testing.T, r *servertest.Rig, want ...refTrack) {
	t.Helper()
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	if resp := agent.Do(t, "POST", "/api/backups", nil, "Idempotency-Key", "ref-bk"); resp.Status != 202 {
		t.Fatalf("create_backup: %d %s", resp.Status, resp.Body)
	}
	var snapshot string
	deadline := time.Now().Add(90 * time.Second)
	for snapshot == "" {
		if time.Now().After(deadline) {
			t.Fatal("backup did not complete")
		}
		for _, x := range agent.OK(t, "GET", "/api/backups", nil).(map[string]any)["runs"].([]any) {
			run := obj(x)
			switch run["state"] {
			case "failed":
				t.Fatalf("backup failed: %v", run["error"])
			case "completed":
				snapshot, _ = run["snapshot"].(string)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	into := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(into, 0o750); err != nil {
		t.Fatal(err)
	}
	rep, err := backup.Restore(context.Background(), filepath.Join(r.BackupDir, snapshot), into, backup.RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if live := count(t, r, `SELECT count(*) FROM tracks`); rep.Counts["tracks"] != live {
		t.Errorf("restored %d tracks, live library has %d", rep.Counts["tracks"], live)
	}
	for _, x := range want {
		if got := sha256File(t, filepath.Join(into, x.Master)); got != x.SHA {
			t.Errorf("restored master of %s has sha %s, row says %s", x.Title, got, x.SHA)
		}
	}
}

// TestAcceptance_M2c_LossyPilot: the six standalone MP3s import as lossy reference tracks, byte for
// byte, without tags or an MP3 rendition, and the fake MP3 and the sidecar import nothing.
func TestAcceptance_M2c_LossyPilot(t *testing.T) {
	r, tpl := refRig(t, servertest.Options{M2b: servertest.M2bOptions{ClipCacheMB: 1, Backfill: true}})
	h := r.Human(t)
	refs := refTracks(t, r)
	names := []string{"syn trio p1", "syn trio p2", "syn trio p3", "syn lone", "syn pair p2", "syn pair p5"}
	if n := count(t, r, `SELECT count(*) FROM tracks WHERE lossy = 1`); n != 6 {
		t.Fatalf("%d lossy tracks, want 6", n)
	}
	if n := count(t, r, `SELECT count(*) FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id WHERE rc.engine = 'unknown'`); n != 8 {
		t.Fatalf("%d recipe-less tracks, want 8 (2 FLAC + 6 MP3)", n)
	}
	if n := count(t, r, `SELECT count(*) FROM tracks WHERE lower(title) LIKE '%fake%'`); n != 0 {
		t.Errorf("the fake MP3 became a track")
	}
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	mc := mcpWithToken(t, r, func() string {
		tok, _, err := r.Services.Tokens.Create(context.Background(), "lossy-agent", []auth.Scope{auth.ScopeRead, auth.ScopeWrite})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}())

	lossyIDs := map[string]string{}
	for _, n := range names {
		x, ok := refs[n]
		if !ok || !x.Lossy {
			t.Fatalf("no lossy reference track %q", n)
		}
		lossyIDs[n] = x.ID
		in := filepath.Join(tpl.Inbox(), "music", "lossy", strings.ReplaceAll(n, " ", "_")+".mp3")
		master := filepath.Join(r.DataDir, x.Master)
		a, err := os.ReadFile(in) //nolint:gosec // generated inbox
		if err != nil {
			t.Fatal(err)
		}
		bts, err := os.ReadFile(master)
		if err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(master)
		if !bytes.Equal(a, bts) || st.Mode().Perm() != 0o444 || !strings.HasSuffix(x.Master, ".mp3") {
			t.Errorf("%s: master differs from the inbox file or is not 0444 (%v, %s)", n, st.Mode().Perm(), x.Master)
		}
		if bytes.HasPrefix(bts, []byte("ID3")) {
			t.Errorf("%s: the master carries an ID3 tag", n)
		}
		if c := count(t, r, `SELECT count(*) FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, x.ID); c != 0 {
			t.Errorf("%s: has an mp3_320 rendition", n)
		}
		if c := count(t, r, `SELECT count(*) FROM renditions WHERE track_id = ? AND kind = 'peaks'`, x.ID); c != 1 {
			t.Errorf("%s: %d peaks renditions", n, c)
		}
		for _, an := range []string{"loudness", "spectrum"} {
			if c := count(t, r, `SELECT count(*) FROM analyses WHERE track_id = ? AND analyzer = ?`, x.ID, an); c == 0 {
				t.Errorf("%s: no %s analysis", n, an)
			}
		}

		// media links: no FLAC link, over REST and MCP; Range on the raw stream
		m := mediaOf(t, h, x.ID)
		mp := obj(m["mp3"])
		if v, present := m["flac"]; !present || v != nil || m["lossy"] != true {
			t.Errorf("%s: get_media_links = %v, want flac null, lossy true", n, m)
		}
		rangeMatrix(t, r, mp["url"].(string), "audio/mpeg", int64(len(bts)))
		tm := obj(callTool(t, mc, "get_media_links", map[string]any{"id": x.ID}, false))
		if v, present := tm["flac"]; !present || v != nil {
			t.Errorf("%s: MCP get_media_links minted %v", n, v)
		}
	}

	// the trio is one lineage, root p1
	trio := lossyIDs["syn trio p1"]
	for _, n := range []string{"syn trio p2", "syn trio p3"} {
		var parent, op, params string
		if err := r.Store.R.QueryRow(`SELECT parent_track_id, operator, params_json FROM mutations WHERE child_track_id = ?`, lossyIDs[n]).Scan(&parent, &op, &params); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if parent != trio || op != "inferred_sibling" || !strings.Contains(params, `"reference_group"`) {
			t.Errorf("%s: parent %s operator %s params %s", n, parent, op, params)
		}
	}
	if c := count(t, r, `SELECT count(*) FROM mutations WHERE child_track_id = ?`, lossyIDs["syn lone"]); c != 0 {
		t.Errorf("the lone MP3 has a lineage")
	}

	// the lossy filter
	items := obj(agent.OK(t, "GET", "/api/tracks?lossy=true&limit=100", nil))["items"].([]any)
	var got, want []string
	for _, it := range items {
		got = append(got, obj(it)["id"].(string))
	}
	for _, id := range lossyIDs {
		want = append(want, id)
	}
	slices.Sort(got)
	slices.Sort(want)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("search_tracks lossy=true (-want +got):\n%s", diff)
	}

	// /context
	ctxv := obj(agent.OK(t, "GET", "/api/tracks/"+trio+"/context", nil))
	if obj(ctxv["track"])["lossy"] != true || obj(ctxv["recipe"])["known"] != false || obj(ctxv["recipe"])["engine"] != "unknown" {
		t.Errorf("context track/recipe = %v / %v", ctxv["track"], ctxv["recipe"])
	}
	if spots := fmt.Sprint(obj(ctxv["perception"])["blind_spots"]); !strings.Contains(spots, "lossy MP3 decode") {
		t.Errorf("blind spots lack the lossy text: %s", spots)
	}
	if st := obj(obj(ctxv["_links"])["stream"]); st == nil || st["mp3"] == nil || st["flac"] != nil || strings.Contains(fmt.Sprint(ctxv), ".flac?") {
		t.Errorf("context stream links = %v", st)
	}

	// a duel between two lossy siblings and the ladder
	d := obj(h.OK(t, "POST", "/api/duels", map[string]any{"a_track_id": lossyIDs["syn trio p2"], "b_track_id": lossyIDs["syn trio p3"], "blind": true}, "Idempotency-Key", "lossy-duel"))
	duel := obj(h.OK(t, "GET", "/api/duels/"+d["id"].(string), nil))
	la, _ := fetchClip(t, r, slot(duel, "a")["clip"].(map[string]any)["url"].(string))
	lb, _ := fetchClip(t, r, slot(duel, "b")["clip"].(map[string]any)["url"].(string))
	if la == 0 || la != lb {
		t.Errorf("lossy clip lengths %d and %d", la, lb)
	}
	h.OK(t, "POST", "/api/duels/"+d["id"].(string)+"/verdicts", map[string]any{"verdict": "b", "latency_ms": 2000, "switches": 1})
	if rows := obj(h.OK(t, "GET", "/api/ladder", nil))["items"].([]any); len(rows) == 0 {
		t.Errorf("the ladder is empty after a verdict")
	}

	// one playlist ZIP holds a lossy track (its master frames behind a fresh ID3 tag, not re-encoded) and a FLAC reference
	refFlac := refs["syn ref a"]
	pl := obj(h.OK(t, "POST", "/api/playlists", map[string]any{"name": "Mixed refs", "track_ids": []string{trio, refFlac.ID}}, "Idempotency-Key", "lossy-pl"))["id"].(string)
	exp := obj(h.OK(t, "POST", "/api/playlists/"+pl+"/exports", nil, "Idempotency-Key", "lossy-zip"))
	resp, err := http.Get(r.URL + obj(exp["download"])["url"].(string)) //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	zb, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		t.Fatal(err)
	}
	master, _ := os.ReadFile(filepath.Join(r.DataDir, refs["syn trio p1"].Master))
	mp3s, sameAsMaster := 0, false
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, ".mp3") {
			mp3s++
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			_ = rc.Close()
			sameAsMaster = sameAsMaster || (bytes.HasSuffix(b, master) && len(b) > len(master))
		}
	}
	if mp3s != 2 || !sameAsMaster {
		t.Errorf("playlist ZIP: %d mp3 entries (want 2), lossy master included byte for byte: %v", mp3s, sameAsMaster)
	}

	// a lossy track cannot be the parent of a draft
	resp2 := h.Do(t, "POST", "/api/drafts", map[string]any{"parent_track_id": trio, "request": map[string]any{"id": "lossy_child", "style": "pop", "lyrics": "[Verse]\nla", "cot": "full", "seed": 3}}, "Idempotency-Key", "lossy-draft")
	if pr := obj(resp2.JSON(t)); resp2.Status != http.StatusUnprocessableEntity || pr["code"] != "RECIPE_UNKNOWN" {
		t.Errorf("draft with a lossy parent: %d %s", resp2.Status, resp2.Body)
	}

	// and the lossy library survives a backup and restore
	restoreVerifies(t, r, refs["syn trio p1"], refs["syn lone"])
}

// TestAcceptance_M2c_PlainImportUnchanged: the archives imported without the opt-ins hold no
// recipe-less or lossy track and the same counts as before M2c (D-M1-2; goldens measured on the
// pre-M2c tree).
func TestAcceptance_M2c_PlainImportUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	for _, c := range []struct {
		profile         string
		tracks, recipes int
	}{{"e2e", 9, 22}, {"acceptance", 8, 23}} {
		tpl := archivetest.SyntheticArchive(t, c.profile)
		r := servertest.New(t, servertest.Options{Archive: &tpl})
		if n := count(t, r, `SELECT count(*) FROM tracks WHERE lossy = 1`); n != 0 {
			t.Errorf("%s: %d lossy tracks after a plain import", c.profile, n)
		}
		if n := count(t, r, `SELECT count(*) FROM recipes WHERE engine = 'unknown'`); n != 0 {
			t.Errorf("%s: %d stub recipes after a plain import", c.profile, n)
		}
		if n := count(t, r, `SELECT count(*) FROM tracks`); n != c.tracks {
			t.Errorf("%s: %d tracks, want %d", c.profile, n, c.tracks)
		}
		if n := count(t, r, `SELECT count(*) FROM recipes`); n != c.recipes {
			t.Errorf("%s: %d recipes, want %d", c.profile, n, c.recipes)
		}
	}
}
