package acceptance_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// l1Rig is the settled L1 library (FixtureL1 on the M3 base) behind the real server, with the Reconciler idle.
func l1Rig(t *testing.T, o servertest.Options) *servertest.Rig {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	o.Fixture, o.M2c, o.M3, o.L1 = true, true, true, true
	r := servertest.New(t, o)
	r.Settle(t)
	return r
}

// songWithTakes returns a Song with at least two live takes, its Pick take and a take that is not the Pick.
func songWithTakes(t *testing.T, r *servertest.Rig) (song, pick, other string) {
	t.Helper()
	err := r.Store.R.QueryRow(`SELECT ts.song_id, sp.take_id,
		(SELECT t.id FROM take_songs x JOIN tracks t ON t.id = x.track_id WHERE x.song_id = ts.song_id AND t.status <> 'discarded' AND t.id <> sp.take_id ORDER BY t.id LIMIT 1)
		FROM take_songs ts JOIN tracks t ON t.id = ts.track_id JOIN song_picks sp ON sp.song_id = ts.song_id
		WHERE t.status <> 'discarded' GROUP BY ts.song_id HAVING COUNT(*) >= 2 ORDER BY ts.song_id LIMIT 1`).Scan(&song, &pick, &other)
	if err != nil {
		t.Fatalf("no Song with two takes: %v", err)
	}
	return song, pick, other
}

// TestAcceptance_L1_EndToEnd_Agent: an agent with a read+write PAT walks the whole L1 flow over MCP; the human pins
// over REST with a human principal; the feed shows the pin after the initial Pick and the audit lists every step.
func TestAcceptance_L1_EndToEnd_Agent(t *testing.T) {
	r := l1Rig(t, servertest.Options{})
	generic := map[string]bool{}
	for _, op := range api.Operations(r.API) {
		generic[op.ID] = op.Meta.MCP != "tool"
	}
	tok, agentID, err := r.Services.Tokens.Create(context.Background(), "l1-agent", []auth.Scope{auth.ScopeRead, auth.ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	sess := mcpWithToken(t, r, tok)
	var steps []string
	call := func(op string, args map[string]any) map[string]any {
		t.Helper()
		steps = append(steps, op)
		return obj(callTool(t, sess, op, args, generic[op]))
	}
	song, pickTake, other := songWithTakes(t, r)

	// 1. read the Muses; open one
	muses := call("list_muses", map[string]any{"limit": 5})
	items, _ := muses["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("list_muses: %v", muses)
	}
	first := obj(items[0])["id"].(string)
	if m := call("get_muse", map[string]any{"id": first}); obj(m["muse"])["id"] != first {
		t.Fatalf("get_muse: %v", m)
	}
	// 2. create a Muse and assign the Song to it
	canary := "Synthetic Muse e2e " + strings.ReplaceAll(song[len(song)-6:], "-", "")
	created := call("create_muse", map[string]any{"kind": "reference", "title": canary, "artist": "Synthetic Artist e2e"})
	museID, _ := created["id"].(string)
	if museID == "" {
		t.Fatalf("create_muse: %v", created)
	}
	call("assign_muse_songs", map[string]any{"id": museID, "song_ids": []string{song}})
	// 3. read the Song: its Muse, its Pick and its takes
	sv := call("get_song", map[string]any{"id": song})
	if got := obj(sv["muse"])["id"]; got != museID {
		t.Errorf("get_song muse = %v, want %s", got, museID)
	}
	if got := obj(sv["pick"])["take_id"]; got != pickTake {
		t.Errorf("get_song pick = %v, want %s", got, pickTake)
	}
	// 4. propose another take
	prop := call("propose_pick", map[string]any{"id": song, "take_id": other, "rationale": "scripted agent rationale"})
	propID, _ := prop["id"].(string)
	if propID == "" || prop["state"] != "open" {
		t.Fatalf("propose_pick: %v", prop)
	}
	// 5. the Sound browser
	if s := call("search_sounds", map[string]any{"limit": 5}); s == nil {
		t.Fatal("search_sounds returned nothing")
	}
	// 6. the human pins the proposed take over REST; an agent cannot
	if resp := servertest.Bearer(r, tok).Do(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": other}); resp.Status != 403 {
		t.Errorf("an agent pin answered %d, want 403", resp.Status)
	}
	h := r.Human(t)
	pin := obj(h.OK(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": other, "proposal_id": propID}))
	if obj(pin["pick"])["take_id"] != other || obj(pin["proposal"])["state"] != "accepted" {
		t.Fatalf("pin: %v", pin)
	}
	// 7. the feed: the initial Pick, then the pin that accepted the proposal
	feed := call("list_pick_events", map[string]any{"song_ids": []string{song}, "limit": 50})
	var reasons []string
	var pinEvidence map[string]any
	evs, _ := feed["items"].([]any)
	for _, e := range evs {
		ev := obj(e)
		reasons = append(reasons, ev["reason"].(string))
		if ev["reason"] == "pin" {
			pinEvidence = obj(ev["evidence"])
		}
	}
	idxInitial, idxPin := indexOf(reasons, "initial"), indexOf(reasons, "pin")
	if idxInitial < 0 || idxPin < 0 {
		t.Fatalf("feed reasons %v lack initial or pin", reasons)
	}
	// the feed is newest first or oldest first; either way the pin is the later event
	seq := func(i int) float64 { return obj(evs[i])["seq"].(float64) }
	if seq(idxPin) <= seq(idxInitial) {
		t.Errorf("pin seq %v is not after initial seq %v", seq(idxPin), seq(idxInitial))
	}
	if pinEvidence["proposal_id"] != propID {
		t.Errorf("pin evidence %v does not name proposal %s", pinEvidence, propID)
	}
	// 8. the audit lists every step in order and carries no Muse text
	page := obj(callTool(t, sess, "get_audit", map[string]any{"principal_id": "agent:" + agentID, "limit": 100}, true))
	var ops []string
	rows, _ := page["items"].([]any)
	for i := len(rows) - 1; i >= 0; i-- {
		ops = append(ops, obj(rows[i])["op"].(string))
	}
	// only the agent's MCP steps are audited under its principal (the refused REST pin is a separate row; the human pin is the human's)
	if !containsInOrder(ops, steps) {
		t.Errorf("audit ops %v do not contain the steps %v in order", ops, steps)
	}
	b, _ := json.Marshal(page)
	for _, text := range []string{canary, "Synthetic Artist e2e", "scripted agent rationale"} {
		if strings.Contains(string(b), text) {
			t.Errorf("the audit listing carries %q", text)
		}
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func containsInOrder(have, want []string) bool {
	j := 0
	for _, h := range have {
		if j < len(want) && h == want[j] {
			j++
		}
	}
	return j == len(want)
}

// TestNoSecretLeak_L1 extends TestNoSecretLeak to the library model: the L1 canaries (a Muse's title, artist and brief,
// a Session name, an agent's pick rationale and an agent's sound term) and the confirmation token of a Board-style
// render estimate appear nowhere they must not: not in the logs, the audit rows, the job events, the other stored
// tables, the ntfy messages, the exports, a backup's manifest and listings (the snapshot's own database copy holds
// Muses by construction, like lyrics, and is not scanned) or any problem body.
func TestNoSecretLeak_L1(t *testing.T) {
	logs := captureLogs(t)
	r := l1Rig(t, servertest.Options{Canary: true, Backend: "fake"})
	l := newLeaks(t)
	l.add("secret_key", keyForms(canaryRigKey)...)
	l.add("edge_secret", canaryEdge)
	l.add("ntfy_token", canaryNtfy)
	l.add("operator_password", canaryPassword)
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tag := "L1canary" + hex.EncodeToString(rnd[:])
	museTitle, museArtist, museBrief := "Muse Title "+tag, "Muse Artist "+tag, "Muse Brief Text "+tag
	sessionName, rationale, term := "Session Name "+tag, "Rationale Text "+tag, "sound term "+tag
	l.add("muse_title", museTitle)
	l.add("muse_artist", museArtist)
	l.add("muse_brief", museBrief)
	l.add("session_name", sessionName)
	l.add("pick_rationale", rationale)
	l.add("sound_term", term)

	tok, _, err := r.Services.Tokens.Create(context.Background(), "l1-leak", []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate})
	if err != nil {
		t.Fatal(err)
	}
	l.add("pat", tok)
	human, agent := r.Human(t), servertest.Bearer(r, tok)
	song, _, other := songWithTakes(t, r)

	// the canaries enter by their documented doors
	mu := obj(human.OK(t, "POST", "/api/muses", map[string]any{"kind": "reference", "title": museTitle, "artist": museArtist}, "Idempotency-Key", "l1-leak-muse"))
	human.OK(t, "POST", "/api/muses", map[string]any{"kind": "brief", "title": "Brief " + museTitle, "brief": museBrief}, "Idempotency-Key", "l1-leak-brief")
	human.OK(t, "POST", "/api/muses/"+mu["id"].(string)+"/songs", map[string]any{"song_ids": []string{song}}, "Idempotency-Key", "l1-leak-assign")
	var sid string
	var rev int
	if err := r.Store.R.QueryRow(`SELECT id, revision FROM sessions ORDER BY id LIMIT 1`).Scan(&sid, &rev); err != nil {
		t.Fatal(err)
	}
	human.OK(t, "PATCH", "/api/sessions/"+sid, map[string]any{"revision": rev, "name": sessionName})
	agent.OK(t, "POST", "/api/songs/"+song+"/pick-proposals", map[string]any{"take_id": other, "rationale": rationale}, "Idempotency-Key", "l1-leak-prop")
	agent.OK(t, "POST", "/api/sound-terms", map[string]any{"mappings": []map[string]any{{"term": term, "facet": "genre_family", "value": "reggae", "op": "add"}}}, "Idempotency-Key", "l1-leak-term")

	// a Board-style render estimate: the confirmation token is shown once, in the estimate response
	var draft string
	if err := r.Store.R.QueryRow(`SELECT id FROM recipe_drafts ORDER BY id LIMIT 1`).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	est := agent.Do(t, "POST", "/api/generations/estimate", map[string]any{"items": []any{map[string]any{"mode": "from_draft", "draft_ids": []string{draft}}}})
	var confirm string
	if est.Status/100 == 2 {
		confirm, _ = obj(est.JSON(t))["confirmation_token"].(string)
		l.add("confirmation_token", confirm)
	}

	// reads and refusals (problem bodies) of the L1 surface
	for _, c := range []*servertest.Client{human, agent} {
		for _, p := range []string{"/api/muses", "/api/muses/" + mu["id"].(string), "/api/songs/" + song, "/api/songs?limit=20", "/api/sessions", "/api/board?limit=10",
			"/api/sounds/search?limit=5", "/api/sounds/taxonomy?limit=5", "/api/pick-events?limit=20", "/api/muses/nope", "/api/songs/nope"} {
			c.Do(t, "GET", p, nil)
		}
	}
	agent.Do(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": other}) // refused: human only
	agent.Do(t, "POST", "/api/muses", map[string]any{"kind": "reference", "title": museTitle, "artist": museArtist}, "Idempotency-Key", "l1-leak-dup")

	// exports and a backup
	exp := human.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "drafts", "drafts": map[string]any{"draft_ids": []string{draft}}}, "Idempotency-Key", "l1-leak-exp")
	l.scan("export_jsonl response", string(exp.Body))
	human.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{other}}}, "Idempotency-Key", "l1-leak-exp2")
	human.Do(t, "POST", "/api/backups", nil, "Idempotency-Key", "l1-leak-backup")
	waitBackupsIdle(t, human)

	// responses: the PAT, the keys and the token never leave their one door (Muse text is checked per operation in
	// internal/api: TestMuseText_OnlyInAllowlistedOps)
	secretsOnly := newLeaks(t)
	for _, s := range l.secrets {
		switch s.kind {
		case "muse_title", "muse_artist", "muse_brief", "session_name", "pick_rationale", "sound_term":
		default:
			secretsOnly.add(s.kind, s.value)
		}
	}
	for _, lc := range []leakClient{{name: "human", c: human}, {name: "agent", c: agent}} {
		for _, resp := range lc.c.Responses() {
			var allowed []string
			if strings.HasSuffix(strings.SplitN(resp.Path, "?", 2)[0], "/estimate") {
				allowed = append(allowed, confirm)
			}
			secretsOnly.scan(fmt.Sprintf("response of %s %s %s", lc.name, resp.Method, strings.SplitN(resp.Path, "?", 2)[0]), responseText(resp, ""), allowed...)
			if resp.Status >= 400 { // problem bodies carry no L1 canary at all
				l.scan(fmt.Sprintf("problem body of %s %s %s", lc.name, resp.Method, strings.SplitN(resp.Path, "?", 2)[0]), string(resp.Body))
			}
		}
	}
	secretsOnly.flush()

	// stored text: nothing but the Muse, Session and proposal tables of the model holds the canaries
	tables := dumpTables(t, r.Store.R, "audit_log", "job_events", "ledger", "jobs", "batches", "tracks", "recipes", "comments", "exports", "backup_runs")
	for name, text := range tables {
		l.scan("table "+name, text)
	}
	l.scan("captured logs", logs.String())
	for i, nr := range r.Ntfy.Requests() {
		l.scan(fmt.Sprintf("ntfy request %d body", i), nr.Body)
		l.scan(fmt.Sprintf("ntfy request %d path", i), nr.Path)
	}
	scanBackupText(t, l, r.BackupDir)
	l.flush()
	if l.scanned < 20 {
		t.Errorf("scanned only %d texts", l.scanned)
	}
	// the canaries did enter the places they belong to (the scan is not vacuous)
	for q, want := range map[string]string{
		`SELECT title FROM muses WHERE title = ?`:                  museTitle,
		`SELECT name FROM sessions WHERE name = ?`:                 sessionName,
		`SELECT rationale FROM pick_proposals WHERE rationale = ?`: rationale,
	} {
		var got string
		if err := r.Store.R.QueryRow(q, want).Scan(&got); err != nil {
			t.Errorf("canary %q is not stored where it belongs: %v", want, err)
		}
	}
}

// scanBackupText scans the manifest and listing files of every snapshot under dir, never the database copies.
func scanBackupText(t *testing.T, l *leaks, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".db", ".sqlite", ".flac", ".mp3", ".wav", ".png", ".zip", ".gz", ".wal", ".shm":
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // files of a temp tree this test created
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		l.scan("backup file "+rel, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("scanning the backups: %v", err)
	}
}
