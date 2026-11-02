package acceptance_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestNoSecretLeak_L3 extends TestNoSecretLeak to the L3 surfaces. The canaries are the ones an agent can bring into the library: Muse
// text copied into a taxonomy rationale and into a proposed term, and into a Record title that is then sequenced and proposed. They
// appear nowhere they must not: not in the logs, the audit rows (and get_audit, REST and MCP), the idempotency cache's neighbours,
// the stored sequence runs and feature rows, the jobs and ledger tables, the ntfy messages, the exports, a backup's manifest and
// listings, or any problem body; the PAT and the rig's keys stay inside their one door.
func TestNoSecretLeak_L3(t *testing.T) {
	logs := captureLogs(t)
	r := l3Rig(t, servertest.Options{Canary: true})
	l := newLeaks(t)
	l.add("secret_key", keyForms(canaryRigKey)...)
	l.add("edge_secret", canaryEdge)
	l.add("ntfy_token", canaryNtfy)
	l.add("operator_password", canaryPassword)
	tag := "L3canary" + randomTag(t)
	museTitle, museArtist := "Muse Title "+tag, "Muse Artist "+tag
	rationale, term, recTitle := "Rationale "+museTitle, "term"+strings.ToLower(tag), "Record "+museTitle
	l.add("muse_title", museTitle)
	l.add("muse_artist", museArtist)
	l.add("agent_rationale", rationale)
	l.add("proposed_term", term)
	l.add("record_title", recTitle)

	tok, _, err := r.Services.Tokens.Create(context.Background(), "l3-leak", []auth.Scope{auth.ScopeRead, auth.ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	l.add("pat", tok)
	human, agent := r.Human(t), servertest.Bearer(r, tok)
	sess := mcpWithToken(t, r, tok)
	songs := []string{l3Song(t, r, "T1"), l3Song(t, r, "T2"), l3Song(t, r, "T3"), l3Song(t, r, "T4")}

	// the Muse enters through the operator's door; the agent then copies its text into everything it writes
	mu := obj(human.OK(t, "POST", "/api/muses", map[string]any{"kind": "reference", "title": museTitle, "artist": museArtist}, "Idempotency-Key", "l3-leak-muse"))
	human.OK(t, "POST", "/api/muses/"+mu["id"].(string)+"/songs", map[string]any{"song_ids": songs}, "Idempotency-Key", "l3-leak-assign")
	r.Settle(t)
	created := obj(agent.OK(t, "POST", "/api/records", map[string]any{"title": recTitle, "song_ids": songs}, "Idempotency-Key", "l3-leak-rec"))
	rec := obj(created["record"])
	rid := rec["id"].(string)
	agent.OK(t, "GET", "/api/sequence?record_id="+rid+"&strategy=arc", nil)
	prop := obj(agent.OK(t, "POST", "/api/records/"+rid+"/proposal", map[string]any{"revision": rec["revision"], "rationale": rationale, "sequence": map[string]any{"strategy": "arc"}}))
	tp := obj(agent.OK(t, "POST", "/api/taxonomy-proposals", map[string]any{"rationale": rationale,
		"mappings": []map[string]any{{"term": term, "facet": "mood", "value": "chill", "op": "add"}}}, "Idempotency-Key", "l3-leak-tax"))
	human.OK(t, "POST", "/api/taxonomy-proposals/"+tp["id"].(string)+"/decision", map[string]any{"decision": "accept"})
	human.OK(t, "POST", "/human/api/records/"+rid+"/publish", map[string]any{"revision": obj(prop["record"])["revision"], "published": true})
	// the L3 reads and refusals, over REST and MCP
	for _, c := range []*servertest.Client{human, agent} {
		for _, p := range []string{"/api/atlas?x=tempo&y=energy&color_by=muse", "/api/atlas/adherence", "/api/next-actions?limit=50", "/api/taxonomy-proposals?state=accepted&include_impact=true",
			"/api/sequence?song_id=" + songs[0], "/api/sequence?record_id=" + rid, "/api/records/" + rid + "?include=sequence&include=changes"} {
			c.Do(t, "GET", p, nil)
		}
	}
	agent.Do(t, "POST", "/api/taxonomy-proposals/"+tp["id"].(string)+"/decision", map[string]any{"decision": "reject", "note": rationale})
	agent.Do(t, "POST", "/api/taxonomy-proposals", map[string]any{"rationale": rationale, "mappings": []map[string]any{{"term": term, "facet": "no_such_facet", "value": "chill", "op": "add"}}}, "Idempotency-Key", "l3-leak-bad")
	agent.Do(t, "POST", "/api/records/"+rid+"/proposal", map[string]any{"revision": 1, "rationale": rationale, "sequence": map[string]any{"strategy": "zigzag"}})
	for _, op := range []string{"list_next_actions", "get_song_atlas", "get_adherence", "list_taxonomy_proposals"} {
		rawTool(t, sess, op, map[string]any{}, true)
	}
	rawTool(t, sess, "suggest_sequence", map[string]any{"record_id": rid}, true)

	// get_audit, in the canary run: REST for the operator and the agent, and the tool
	audit := []string{
		string(human.Do(t, "GET", "/api/audit?limit=200", nil).Body),
		string(agent.Do(t, "GET", "/api/audit?limit=200", nil).Body),
		fmt.Sprint(rawTool(t, sess, "get_audit", map[string]any{"limit": 200}, true).StructuredContent),
	}
	for i, a := range audit {
		if len(a) < 500 {
			t.Fatalf("get_audit #%d answered %d bytes: the scan would be vacuous", i, len(a))
		}
		l.scan(fmt.Sprintf("get_audit #%d", i), a)
	}
	// an export and a backup
	human.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{l2Take(t, r, "A1")}}}, "Idempotency-Key", "l3-leak-exp")
	human.Do(t, "POST", "/api/backups", nil, "Idempotency-Key", "l3-leak-backup")
	waitBackupsIdle(t, human)

	// responses: secrets never leave their one door; the canaries live only where an operation returns them, and never in a refusal
	secretsOnly := newLeaks(t)
	for _, s := range l.secrets {
		switch s.kind {
		case "muse_title", "muse_artist", "agent_rationale", "proposed_term", "record_title":
		default:
			secretsOnly.add(s.kind, s.value)
		}
	}
	for _, lc := range []leakClient{{name: "human", c: human}, {name: "agent", c: agent}} {
		for _, resp := range lc.c.Responses() {
			path := strings.SplitN(resp.Path, "?", 2)[0]
			secretsOnly.scan(fmt.Sprintf("response of %s %s %s", lc.name, resp.Method, path), responseText(resp, ""))
			if resp.Status >= 400 {
				l.scan(fmt.Sprintf("problem body of %s %s %s", lc.name, resp.Method, path), string(resp.Body))
			}
		}
	}
	secretsOnly.flush()

	// stored text: the tables that must not hold agent or Muse text
	tables := dumpTables(t, r.Store.R, "audit_log", "job_events", "ledger", "jobs", "batches", "exports", "backup_runs", "tapes", "comments", "record_sequences", "song_features", "take_features")
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
		`SELECT title FROM muses WHERE title = ?`:                                 museTitle,
		`SELECT title FROM records WHERE title = ?`:                               recTitle,
		`SELECT rationale FROM taxonomy_proposals WHERE rationale = ?`:            rationale,
		`SELECT mappings_json FROM taxonomy_proposals WHERE mappings_json LIKE ?`: "%" + term + "%",
	} {
		var got string
		if err := r.Store.R.QueryRow(q, want).Scan(&got); err != nil {
			t.Errorf("canary %q is not stored where it belongs: %v", want, err)
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM record_sequences`); n == 0 {
		t.Error("no sequence run was stored: the record_sequences scan is vacuous")
	}
}
