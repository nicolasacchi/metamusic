package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/config"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// l3Ops are the seven L3 operations (C142).
var l3Ops = []string{"suggest_sequence", "list_next_actions", "propose_taxonomy", "list_taxonomy_proposals", "decide_taxonomy_proposal", "get_song_atlas", "get_adherence"}

// TestNoPendingL3Stubs: no L3 stub is left. Statically, no source file carries a STUB(L3-...) marker; dynamically, none of the
// seven L3 operations answers 501 NOT_IMPLEMENTED to an admin agent or to the operator on the real rig, whatever the arguments
// (a refusal such as 422 is fine, a stub is not). The by-name skips of the scripted agent, the registry sweep and the parity
// runner are failures now (TestAcceptance_M2a_AgentPerformsEveryUIAction, TestNoPendingM4aStubs, TestParityEveryOperation).
func TestNoPendingL3Stubs(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "l3_test.go") {
				return err
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			if bytes.Contains(b, []byte("STUB(L3-")) {
				t.Errorf("%s still carries an L3 stub marker", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	r := l3Rig(t, servertest.Options{})
	admin, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)
	human := r.Human(t)
	missing := "01900000-0000-7000-8000-00000000ffff"
	probed := 0
	for _, op := range api.Operations(r.API) {
		if !slices.Contains(l3Ops, op.ID) {
			continue
		}
		var body any
		if op.Method != "GET" {
			body = map[string]any{}
		}
		for who, c := range map[string]*servertest.Client{"agent": admin, "human": human} {
			resp := c.Do(t, op.Method, strings.ReplaceAll(op.Path, "{id}", missing), body, "Idempotency-Key", "l3-stub-"+op.ID+who)
			if resp.Status == http.StatusNotImplemented || bytes.Contains(resp.Body, []byte("NOT_IMPLEMENTED")) {
				t.Errorf("%s as %s answers 501 NOT_IMPLEMENTED: %s", op.ID, who, resp.Body)
			}
		}
		probed++
	}
	if probed != len(l3Ops) {
		t.Errorf("probed %d L3 operations, want %d", probed, len(l3Ops))
	}
}

// TestAcceptance_L3_TaxonomyProposalFlow: an agent proposes mapping the unmapped term `skank` to a mood; the proposal carries
// an impact preview; the agent cannot decide it; the operator accepts it; and the Sound rows, the feature rows of the Songs that
// carried the term (refreshed by the recompute hook, no Settle) and the unmapped-term count all follow, with no verify finding.
func TestAcceptance_L3_TaxonomyProposalFlow(t *testing.T) {
	r := l3Rig(t, servertest.Options{})
	human := r.Human(t)
	ag, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)

	moodless := func() []string {
		rows, err := r.Store.R.Query(`SELECT DISTINCT u.song_id FROM song_unmapped_terms u WHERE u.term = 'skank'
			AND NOT EXISTS (SELECT 1 FROM song_sounds s WHERE s.song_id = u.song_id AND s.facet = 'mood') ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	targets := moodless()
	if len(targets) < 2 {
		t.Fatalf("the fixture has %d Songs with skank and no mood, want at least 2", len(targets))
	}
	moodOf := func(song string) string {
		var m *string
		if err := r.Store.R.QueryRow(`SELECT mood FROM song_features WHERE song_id = ?`, song).Scan(&m); err != nil {
			t.Fatal(err)
		}
		if m == nil {
			return ""
		}
		return *m
	}
	for _, s := range targets {
		if moodOf(s) != "" {
			t.Fatalf("Song %s has mood %q before the decision", s, moodOf(s))
		}
	}

	prop := obj(ag.OK(t, "POST", "/api/taxonomy-proposals", map[string]any{
		"rationale": "synthetic rationale", "mappings": []map[string]any{{"term": "skank", "facet": "mood", "value": "chill", "op": "add"}},
	}, "Idempotency-Key", "l3-tax-1"))
	id := prop["id"].(string)
	if prop["state"] != "open" || len(prop["mappings"].([]any)) != 1 {
		t.Fatalf("proposal %v", prop)
	}
	gained := 0
	for _, g := range obj(prop["impact"])["gain"].([]any) {
		if gm := obj(g); gm["facet"] == "mood" && gm["value"] == "chill" {
			gained = int(gm["songs"].(float64))
		}
	}
	if gained < len(targets) {
		t.Errorf("the impact preview promises %d Songs for mood chill, at least %d are moodless", gained, len(targets))
	}
	// an identical open proposal is returned instead of a second one
	if again := obj(ag.OK(t, "POST", "/api/taxonomy-proposals", map[string]any{
		"rationale": "synthetic rationale", "mappings": []map[string]any{{"term": "skank", "facet": "mood", "value": "chill", "op": "add"}},
	}, "Idempotency-Key", "l3-tax-2")); again["id"] != id || again["reused"] != true {
		t.Errorf("duplicate proposal: %v", again)
	}
	// the agent cannot decide; nothing changed
	if resp := ag.Do(t, "POST", "/api/taxonomy-proposals/"+id+"/decision", map[string]any{"decision": "accept"}); resp.Status != 403 {
		t.Fatalf("agent decision: %d %s", resp.Status, resp.Body)
	}
	if got := moodOf(targets[0]); got != "" {
		t.Fatalf("a refused decision changed a feature row: %q", got)
	}
	res := obj(human.OK(t, "POST", "/api/taxonomy-proposals/"+id+"/decision", map[string]any{"decision": "accept", "note": "ok"}))
	if obj(res["proposal"])["state"] != "accepted" || res["applied"] == nil {
		t.Fatalf("decision %v", res)
	}
	for _, s := range targets {
		if got := moodOf(s); got != "chill" {
			t.Errorf("Song %s mood = %q after the decision, want chill (the recompute hook did not refresh features)", s, got)
		}
	}
	if n := len(moodless()); n != 0 {
		t.Errorf("%d Songs still moodless", n)
	}
	// the agent reads the outcome
	got := obj(ag.OK(t, "GET", "/api/taxonomy-proposals?state=accepted", nil))
	if items := got["items"].([]any); len(items) != 1 || obj(items[0])["id"] != id {
		t.Errorf("accepted proposals %v", got)
	}
	// nothing is left for verify
	if an, err := r.Services.Features.Verify(t.Context()); err != nil || len(an) != 0 {
		t.Errorf("features.Verify: %v %v", an, err)
	}
	if an, err := r.Provenance.Verify(t.Context()); err != nil || len(an) != 0 {
		t.Errorf("provenance.Verify: %v %v", an, err)
	}
	// a second decision is refused, the proposal stays decided
	if resp := human.Do(t, "POST", "/api/taxonomy-proposals/"+id+"/decision", map[string]any{"decision": "reject"}); resp.Status/100 == 2 {
		t.Errorf("a decided proposal was decided again: %s", resp.Body)
	}
}

// l3Built is what the agent's build of a Record leaves behind.
type l3Built struct {
	record   string
	songs    []string // list_songs min_stars=4 of the Muse, in the order the agent put them in the Record
	order    []string // suggest_sequence's order
	sug      map[string]any
	proposed map[string]any
}

// l3AgentSession opens an MCP session with a new read+write PAT and returns it with the agent's principal key.
func l3AgentSession(t *testing.T, r *servertest.Rig) (*l3Agent, string) {
	t.Helper()
	tok, id, err := r.Services.Tokens.Create(t.Context(), "l3-agent", []auth.Scope{auth.ScopeRead, auth.ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	return newL3Agent(t, r, mcpWithToken(t, r, tok)), "agent:" + id
}

// l3AgentBuild is the concept's L3 acceptance: an agent finds the Muse, lists its Songs with a 4-star-or-better Pick, builds a
// Record from them, previews a sequence, and proposes the Record with that sequence applied.
func l3AgentBuild(t *testing.T, a *l3Agent, title, rationale string) l3Built {
	t.Helper()
	var museID string
	for _, m := range obj(a.call("list_muses", map[string]any{"limit": 100}))["items"].([]any) {
		if obj(m)["title"] == lt.L3Muse5Title {
			museID = obj(m)["id"].(string)
		}
	}
	if museID == "" {
		t.Fatal("the agent does not find Muse 5")
	}
	var b l3Built
	for _, s := range a.call("list_songs", map[string]any{"muse_id": museID, "min_stars": 4, "limit": 50})["items"].([]any) {
		b.songs = append(b.songs, obj(s)["id"].(string))
	}
	if len(b.songs) != 6 {
		t.Fatalf("%d Songs of Muse 5 with a 4-star Pick, want 6 (T1-T6)", len(b.songs))
	}
	created := a.call("create_record", map[string]any{"title": title, "song_ids": b.songs})
	rec := obj(created["record"])
	b.record = rec["id"].(string)

	b.sug = a.call("suggest_sequence", map[string]any{"record_id": b.record, "strategy": "arc"})
	for _, id := range b.sug["order"].([]any) {
		b.order = append(b.order, id.(string))
	}
	b.proposed = a.call("propose_record", map[string]any{"id": b.record, "revision": rec["revision"], "rationale": rationale,
		"sequence": map[string]any{"strategy": "arc"}})
	return b
}

// TestAcceptance_L3_AgentBuildsAndProposesRecord: an MCP agent builds a Record from the Muse's 4-star Picks, previews a sequence with
// an explanation, proposes it sequenced, and the human publishes it; the stored order is the previewed one, the changelog and
// the Record view say it was sequenced, and every invariant still holds.
func TestAcceptance_L3_AgentBuildsAndProposesRecord(t *testing.T) {
	r := l3Rig(t, servertest.Options{})
	a, _ := l3AgentSession(t, r)
	b := l3AgentBuild(t, a, "Synthetic Sequenced Record", "ready to hear")

	if len(b.order) != len(b.songs) || !sameSet(b.order, b.songs) {
		t.Fatalf("the plan %v is not a permutation of the Songs %v", b.order, b.songs)
	}
	steps := b.sug["steps"].([]any)
	if len(steps) != len(b.songs) || b.sug["pending"] != false {
		t.Fatalf("the preview has %d steps (pending %v)", len(steps), b.sug["pending"])
	}
	for _, s := range steps {
		if obj(s)["text"] == "" || len(obj(s)["reasons"].([]any)) == 0 && obj(s)["position"] != float64(1) {
			t.Errorf("step %v carries no explanation", s)
		}
	}
	if obj(b.sug["coverage"])["tempo"] != float64(100) || obj(b.sug["coverage"])["energy"] != float64(100) {
		t.Errorf("coverage %v, want full tempo and energy for the measured Songs", b.sug["coverage"])
	}
	if obj(b.proposed["record"])["status"] != "proposed" {
		t.Fatalf("record %v", b.proposed["record"])
	}
	var got []string
	for _, s := range b.proposed["slots"].([]any) {
		got = append(got, obj(obj(s)["song"])["id"].(string))
	}
	if !slices.Equal(got, b.order) {
		t.Errorf("stored order %v, previewed %v", got, b.order)
	}

	// the explanation of the run is on the Record, fresh
	view := a.call("get_record", map[string]any{"id": b.record, "include": []string{"sequence", "changes"}})
	seq := obj(view["sequence"])
	if seq["strategy"] != "arc" || seq["stale"] != false || len(seq["steps"].([]any)) != len(b.songs) {
		t.Errorf("sequence explanation %v", seq)
	}
	var sequenced bool
	for _, c := range view["changes"].([]any) {
		if obj(c)["sequence"] != nil && strings.Contains(obj(c)["text"].(string), "sequenced: arc") {
			sequenced = true
		}
	}
	if !sequenced && !slices.Equal(b.order, b.songs) {
		t.Error("no changelog row says the Record was sequenced")
	}

	// only the human publishes
	h := r.Human(t)
	rev := obj(b.proposed["record"])["revision"]
	pub := obj(h.OK(t, "POST", "/human/api/records/"+b.record+"/publish", map[string]any{"revision": rev, "published": true}))
	if obj(pub["record"])["status"] != "published" {
		t.Fatalf("publish: %v", pub)
	}
	r.Settle(t)
	l2Verify(t, r)
	if an, err := r.Services.Features.Verify(t.Context()); err != nil || len(an) != 0 {
		t.Errorf("features.Verify: %v %v", an, err)
	}
	if an, err := r.Provenance.Verify(t.Context()); err != nil || len(an) != 0 {
		t.Errorf("provenance.Verify: %v %v", an, err)
	}
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// TestAcceptance_L3_EndToEnd_Agent: one agent session over MCP walks the L3 loop the concept describes: list_next_actions, read
// the Muse it names, list its Songs, build a Record, preview and propose it sequenced, propose a taxonomy mapping and read the
// Atlas. The human then decides the proposal and publishes the Record over REST. get_audit lists every step of the agent in order
// and carries none of the text the agent wrote (title, rationale, term) nor any Muse text.
func TestAcceptance_L3_EndToEnd_Agent(t *testing.T) {
	r := l3Rig(t, servertest.Options{})
	a, key := l3AgentSession(t, r)
	h := r.Human(t)
	tag := "L3e2e" + randomTag(t)
	title, rationale, term := "Record Title "+tag, "Rationale Text "+tag, "term"+strings.ToLower(tag)

	// 1. what next: the agent follows the Record-candidate item of a Muse
	var museID string
	var listArgs map[string]any
	for _, it := range a.call("list_next_actions", map[string]any{"limit": 30})["items"].([]any) {
		if m := obj(it); m["code"] == "muse_record_candidate" {
			museID, _ = obj(m["subject"])["id"].(string)
			if hint := obj(m["hint"]); hint["op"] == "list_songs" {
				listArgs = obj(hint["args"])
			}
			break
		}
	}
	if museID == "" || listArgs == nil {
		t.Fatal("list_next_actions names no Muse with Songs worth a Record")
	}
	// 2. the Muse and its Songs, with the arguments the hint gave
	muse := obj(a.call("get_muse", map[string]any{"id": museID})["muse"])
	listArgs["limit"] = 50
	songs := a.call("list_songs", listArgs)
	var ids []string
	for _, s := range songs["items"].([]any) {
		ids = append(ids, obj(s)["id"].(string))
	}
	if len(ids) != 6 {
		t.Fatalf("Muse %v has %d Songs worth a Record, want 6", muse["id"], len(ids))
	}
	// 3. a Record, its sequence preview, and the proposal with the sequence applied
	rec := obj(a.call("create_record", map[string]any{"title": title, "song_ids": ids})["record"])
	sug := a.call("suggest_sequence", map[string]any{"record_id": rec["id"], "strategy": "ramp_up"})
	if len(sug["order"].([]any)) != len(ids) {
		t.Fatalf("suggestion %v", sug)
	}
	prop := a.call("propose_record", map[string]any{"id": rec["id"], "revision": rec["revision"], "rationale": rationale, "sequence": map[string]any{"strategy": "ramp_up"}})
	if obj(prop["record"])["status"] != "proposed" {
		t.Fatalf("record %v", prop["record"])
	}
	// 4. a taxonomy proposal and the Atlas
	tp := a.call("propose_taxonomy", map[string]any{"rationale": rationale, "mappings": []map[string]any{{"term": term, "facet": "mood", "value": "chill", "op": "add"}}})
	atlas := a.call("get_song_atlas", map[string]any{"x": "tempo", "y": "energy", "limit": 50})
	if pts, _ := atlas["points"].([]any); len(pts) < 10 {
		t.Errorf("the Atlas has %d points", len(pts))
	}
	// 5. the human decides and publishes over REST
	if resp := h.Do(t, "POST", "/api/taxonomy-proposals/"+tp["id"].(string)+"/decision", map[string]any{"decision": "accept"}); resp.Status/100 != 2 {
		t.Fatalf("decision: %d %s", resp.Status, resp.Body)
	}
	pub := obj(h.OK(t, "POST", "/human/api/records/"+rec["id"].(string)+"/publish", map[string]any{"revision": obj(prop["record"])["revision"], "published": true}))
	if obj(pub["record"])["status"] != "published" {
		t.Fatalf("publish: %v", pub)
	}
	// 6. the audit lists every step in order, and no text
	rows := a.call("get_audit", map[string]any{"principal_id": key, "limit": 200})["items"].([]any)
	var ops []string
	for i := len(rows) - 1; i >= 0; i-- {
		row := obj(rows[i])
		if row["transport"] != "mcp" || int(row["status"].(float64))/100 != 2 {
			t.Errorf("audit row %v", row)
		}
		ops = append(ops, row["op"].(string))
	}
	want := []string{"list_next_actions", "get_muse", "list_songs", "create_record", "suggest_sequence", "propose_record", "propose_taxonomy", "get_song_atlas"}
	if !slices.Equal(ops, want) {
		t.Errorf("audit ops %v, want %v", ops, want)
	}
	raw, _ := json.Marshal(rows)
	for kind, text := range map[string]string{"record title": title, "rationale": rationale, "term": term,
		"Muse title": muse["title"].(string), "Song title": obj(songs["items"].([]any)[0])["title"].(string)} {
		if strings.Contains(string(raw), text) {
			t.Errorf("get_audit carries the %s", kind)
		}
	}
	if a, ok := muse["artist"].(string); ok && strings.Contains(string(raw), a) {
		t.Error("get_audit carries the Muse artist")
	}
	r.Settle(t)
	if an, err := r.Services.Features.Verify(t.Context()); err != nil || len(an) != 0 {
		t.Errorf("features.Verify: %v %v", an, err)
	}
}

// tableDigest is a row count and a content digest of every table but the ones a read legitimately appends to (the audit trail).
func tableDigest(t *testing.T, r *servertest.Rig) map[string]string {
	t.Helper()
	rows, err := r.Store.R.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != "audit_log" {
			names = append(names, n)
		}
	}
	_ = rows.Close()
	out := map[string]string{}
	for name, text := range dumpTables(t, r.Store.R, names...) {
		sum := sha256.Sum256([]byte(text))
		out[name] = fmt.Sprintf("%d:%x", strings.Count(text, "\n"), sum[:8])
	}
	return out
}

// TestL3Reads_NeverMutate: every readable L3 operation, over REST (operator and agent) and MCP, and the L3 includes of the Record view,
// change no row of any table (rule 5: GET never mutates; the reader pool is query_only, and this pins that the L3 readers do not
// reach for the writer pool either). The feature rows stay put even when a read finds them stale.
func TestL3Reads_NeverMutate(t *testing.T) {
	r := l3Rig(t, servertest.Options{})
	human := r.Human(t)
	ag, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	sess := r.MCP(t, auth.ScopeRead, auth.ScopeWrite)
	rid := l2ID(t, r, "records", l2R1)
	// a proposal so the proposal reads have a row, and a sequence run so include=sequence has one
	ag.OK(t, "POST", "/api/taxonomy-proposals", map[string]any{"rationale": "synthetic rationale",
		"mappings": []map[string]any{{"term": "skank", "facet": "mood", "value": "chill", "op": "add"}}}, "Idempotency-Key", "l3-ro-prop")
	rec := obj(obj(human.OK(t, "GET", "/api/records/"+rid, nil))["record"])
	human.OK(t, "POST", "/api/records/"+rid+"/slots", map[string]any{"revision": rec["revision"], "ops": []map[string]any{{"op": "sequence", "strategy": "steady"}}})
	r.Settle(t)

	t1, t2, t3 := l3Song(t, r, "T1"), l3Song(t, r, "T2"), l3Song(t, r, "T3")
	paths := []string{
		"/api/sequence?song_id=" + t1 + "&song_id=" + t2 + "&song_id=" + t3, "/api/sequence?record_id=" + rid + "&strategy=arc",
		"/api/next-actions?limit=50", "/api/taxonomy-proposals?include_impact=true", "/api/taxonomy-proposals?state=open&state=accepted",
		"/api/atlas?x=tempo&y=energy", "/api/atlas?x=family&y=vocal&color_by=muse", "/api/atlas?x=bpm_requested&y=bpm_measured", "/api/atlas/adherence",
		"/api/records/" + rid + "?include=sequence&include=changes&include=energy", "/api/records/" + rid + "/changes",
		"/api/songs/" + t1, "/api/muses?limit=50", "/api/board?limit=20", "/api/sounds/taxonomy?unmapped=true",
	}
	before := tableDigest(t, r)
	for _, p := range paths {
		for who, c := range map[string]*servertest.Client{"human": human, "agent": ag} {
			if resp := c.Do(t, "GET", p, nil); resp.Status/100 != 2 {
				t.Errorf("%s GET %s: %d %.200s", who, p, resp.Status, resp.Body)
			}
		}
	}
	for op, args := range map[string]map[string]any{
		"suggest_sequence": {"record_id": rid}, "list_next_actions": {"limit": 50}, "list_taxonomy_proposals": {"include_impact": true},
		"get_song_atlas": {"x": "tempo", "y": "energy"}, "get_adherence": {}, "get_record": {"id": rid, "include": []string{"sequence"}},
	} {
		rawTool(t, sess, op, args, true)
	}
	after := tableDigest(t, r)
	if len(before) < 40 {
		t.Fatalf("only %d tables compared", len(before))
	}
	for name, was := range before {
		if after[name] != was {
			t.Errorf("table %s changed during reads: %s -> %s", name, was, after[name])
		}
	}
}

// l3RestoredServices serves a restored data directory as `serve` builds it (the real feature step), without HTTP.
func l3RestoredServices(t *testing.T, dataDir string, now time.Time) *app.Services {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(dataDir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := config.Config{
		Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy",
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TrustedUsers: []string{"alice"},
		DataDir: dataDir, SecretsDir: filepath.Join(filepath.Dir(dataDir), "secrets"), BackupDir: filepath.Join(filepath.Dir(dataDir), "backup2"),
		BackupAt: "off", RateScale: 1,
	}
	fv := "flac 1.5.0 (restore test)"
	svc, err := app.Build(ctx, cfg, config.Secrets{Key: []byte(strings.Repeat("0123456789abcdef", 2)), EdgeSecret: "edge"}, st, root,
		app.Options{Now: func() time.Time { return now }, Version: "v-test", L2: app.L2Options{FlacVersion: &fv}, L3: app.L3Options{RealFeatures: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// featureDump lists the derived feature rows without their computation time, for comparing two databases.
func featureDump(t *testing.T, db *sql.DB) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT track_id, bpm_x100, bpm_basis, energy_x10000, energy_basis, key_num, key_letter, key_basis, digest, feature_def FROM take_features ORDER BY track_id`,
		`SELECT song_id, subject_kind, subject_id, bpm_x100, bpm_basis, energy_x10000, energy_basis, key_num, key_letter, key_basis, family, vocal, era, mood, language, digest, feature_def FROM song_features ORDER BY song_id`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&b, vals...)
		}
		_ = rows.Close()
	}
	return b.String()
}

// TestRestore_RebuildsFeatures: the feature rows are a derived cache. A snapshot is restored, the restored copy loses every feature
// row and gets a stale definition on the rest (an older release's snapshot), and the startup sweep of the restored server rebuilds
// exactly the rows the live server held; both verifiers are clean and a Pick that moves after the restore refreshes the Song's row.
func TestRestore_RebuildsFeatures(t *testing.T) {
	r := l3Rig(t, servertest.Options{Files: true})
	l2MaterialiseMasters(t, r)
	h := r.Human(t)
	want := featureDump(t, r.Store.R)
	if n := count(t, r, `SELECT COUNT(*) FROM song_features`); n < 20 {
		t.Fatalf("only %d feature rows to restore", n)
	}

	h.OK(t, "POST", "/api/backups", nil, "Idempotency-Key", "restore-feat-bk")
	var snapshot string
	for deadline := time.Now().Add(60 * time.Second); snapshot == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, x := range obj(h.OK(t, "GET", "/api/backups", nil))["runs"].([]any) {
			run := obj(x)
			if run["state"] == "failed" {
				t.Fatalf("backup failed: %v", run["error"])
			}
			if s, ok := run["snapshot"].(string); ok && run["state"] == "completed" && run["id"] != lt.BackupRunID {
				snapshot = s
			}
		}
	}
	if snapshot == "" {
		t.Fatal("the backup did not complete")
	}
	into := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(into, 0o750); err != nil {
		t.Fatal(err)
	}
	now := lt.FixedNow.Add(2 * time.Hour)
	if _, err := backup.Restore(context.Background(), filepath.Join(r.BackupDir, snapshot), into, backup.RestoreOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}

	// the restored copy as an older release left it: no take rows, no song rows for half the Songs, a stale definition on the rest
	func() {
		st, err := store.Open(context.Background(), filepath.Join(into, "metamusic.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		for _, q := range []string{
			`DELETE FROM take_features`,
			`DELETE FROM song_features WHERE substr(song_id, -1) IN ('0','2','4','6','8','a','c','e')`,
			`UPDATE song_features SET feature_def = 'mm-features/0'`,
		} {
			if _, err := st.W.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}()

	svc := l3RestoredServices(t, into, now)
	ctx := context.Background()
	if err := svc.Provenance.Sweep(ctx); err != nil { // the startup sweep of serve
		t.Fatal(err)
	}
	if _, err := svc.Provenance.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	if got := featureDump(t, svc.Deps.Store.R); got != want {
		t.Errorf("the rebuilt feature rows differ from the live ones:\n--- live\n%s--- rebuilt\n%s", want, got)
	}
	if an, err := svc.Features.Verify(ctx); err != nil || len(an) != 0 {
		t.Errorf("features.Verify after the rebuild: %v %v", an, err)
	}
	if an, err := svc.Provenance.Verify(ctx); err != nil || len(an) != 0 {
		t.Errorf("provenance.Verify after the rebuild: %v %v", an, err)
	}
	// a rating after the restore refreshes the Song's row through the queue
	op := auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "local"}
	stars := 5
	b2 := l2Take(t, r, "B2")
	if _, err := svc.Deps.Judgments.Rate(ctx, op, b2, &stars, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Provenance.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	if an, err := svc.Features.Verify(ctx); err != nil || len(an) != 0 {
		t.Errorf("features.Verify after a new Pick event: %v %v", an, err)
	}
}

// TestL3Wiring_LiveInServe: the L3 seams the lanes left to the composition root are live on the real rig (built the way serve
// builds it): the Crate resolver of suggest_sequence (a Crate id answers a plan, never 503), and the next-action reader of /context
// (the strings the engine renders for a take appear in that take's context).
func TestL3Wiring_LiveInServe(t *testing.T) {
	r := l3Rig(t, servertest.Options{})
	h := r.Human(t)
	crate := l2ID(t, r, "crates", "Synthetic Remake Candidates")
	sug := obj(h.OK(t, "GET", "/api/sequence?crate_id="+crate+"&strategy=steady", nil))
	if n := len(sug["order"].([]any)); n < 2 {
		t.Errorf("a Crate plan has %d Songs", n)
	}
	// a smart Crate resolves through its stored filter
	if resp := h.Do(t, "GET", "/api/sequence?crate_id="+l2ID(t, r, "crates", "Synthetic Unheard Reggae"), nil); resp.Status == http.StatusServiceUnavailable {
		t.Errorf("a smart Crate answers 503: %s", resp.Body)
	}

	var seen int
	rows, err := r.Store.R.Query(`SELECT id FROM tracks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var takes []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		takes = append(takes, id)
	}
	_ = rows.Close()
	op := auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "local"}
	for _, id := range takes {
		want, err := r.Services.NextActions.ForTake(t.Context(), op, id, 2)
		if err != nil || len(want) == 0 {
			continue
		}
		ctxDoc := obj(h.OK(t, "GET", "/api/tracks/"+id+"/context", nil))
		got := fmt.Sprint(ctxDoc["next_actions"])
		for _, a := range want {
			if !strings.Contains(got, a.Text) {
				t.Errorf("take %s: /context next_actions %v lacks the engine's %q", id, got, a.Text)
			}
		}
		seen++
	}
	if seen == 0 {
		t.Error("no take has a Muse-aware next action: the /context reader is not exercised")
	}
}

// nextPage reads list_next_actions as c and returns its items, the per-code overflow counts and the total.
func nextPage(t *testing.T, c *servertest.Client) (items []map[string]any, more map[string]int, total int) {
	t.Helper()
	pg := obj(c.OK(t, "GET", "/api/next-actions?limit=50", nil))
	for _, it := range pg["items"].([]any) {
		items = append(items, obj(it))
	}
	more = map[string]int{}
	for _, g := range pg["groups"].([]any) {
		more[obj(g)["code"].(string)] = int(obj(g)["more"].(float64))
	}
	return items, more, int(pg["total"].(float64))
}

func perCode(items []map[string]any) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		out[it["code"].(string)]++
	}
	return out
}

// TestAcceptance_L3_NextActions_FullList: the whole list of the settled L3 library (real feature step, so the rules of NA3 see the
// derived rows), per viewer: an agent with generate scope, a read-only agent and the operator. The Muse-aware rules, the Record
// rules and the NA3 rules (unmapped terms, Muse-less Songs, missing measurements) fire in priority order with their per-code caps,
// agent-only items never reach the operator and operator-only ones never reach an agent, and the list moves when the library does:
// a taxonomy decision removes the term's item, assigning a Song to a Muse removes its song_no_muse item.
func TestAcceptance_L3_NextActions_FullList(t *testing.T) {
	r := l3Rig(t, servertest.Options{})
	h := r.Human(t)
	gen, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)
	ro, _ := r.Agent(t, auth.ScopeRead)

	wantAgent := map[string]int{"record_undecided_slots": 2, "muse_record_candidate": 1, "muse_unrendered": 5, "song_duel": 5, "muse_unjudged": 2,
		"muse_partial_angles": 1, "record_empty_slot": 1, "taxonomy_unmapped": 2, "song_no_muse": 5, "open_fix_unaddressed": 1}
	wantMore := map[string]int{"muse_unrendered": 3, "song_duel": 2, "song_no_muse": 1}
	for name, c := range map[string]*servertest.Client{"generate agent": gen, "read-only agent": ro} {
		items, more, total := nextPage(t, c)
		if d := cmp.Diff(wantAgent, perCode(items)); d != "" {
			t.Errorf("%s: items per code (-want +got):\n%s", name, d)
		}
		if d := cmp.Diff(wantMore, more); d != "" {
			t.Errorf("%s: overflow (-want +got):\n%s", name, d)
		}
		sum := len(items)
		for _, n := range more {
			sum += n
		}
		if total != sum {
			t.Errorf("%s: total %d, items %d plus overflow", name, total, sum)
		}
		for i := 1; i < len(items); i++ { // priority order, never ascending
			if items[i]["priority"].(float64) > items[i-1]["priority"].(float64) {
				t.Errorf("%s: item %d (%v) outranks the one before it", name, i, items[i]["code"])
			}
		}
		var terms []string
		for _, it := range items {
			if it["code"] == "taxonomy_unmapped" {
				terms = append(terms, obj(it["subject"])["id"].(string))
			}
			if it["code"] == "features_missing" {
				t.Errorf("%s sees an operator-only item", name)
			}
		}
		if !slices.Equal(terms, []string{"piano", "skank"}) {
			t.Errorf("%s: unmapped terms %v", name, terms)
		}
	}
	hItems, _, _ := nextPage(t, h)
	hc := perCode(hItems)
	if hc["features_missing"] != 1 || hc["muse_unrendered"] != 0 || hc["taxonomy_unmapped"] != 0 || hc["song_no_muse"] != 0 || hc["muse_partial_angles"] != 0 || hc["muse_record_candidate"] != 1 {
		t.Errorf("operator list per code: %v", hc)
	}

	// the list follows the library: map the term `skank`, give a Muse-less Song a Muse
	tp := obj(gen.OK(t, "POST", "/api/taxonomy-proposals", map[string]any{"rationale": "synthetic rationale",
		"mappings": []map[string]any{{"term": "skank", "facet": "mood", "value": "chill", "op": "add"}}}, "Idempotency-Key", "l3-na-tax"))
	h.OK(t, "POST", "/api/taxonomy-proposals/"+tp["id"].(string)+"/decision", map[string]any{"decision": "accept"})
	var noMuse string
	for _, it := range func() []map[string]any { i, _, _ := nextPage(t, gen); return i }() {
		if it["code"] == "song_no_muse" {
			noMuse = obj(it["subject"])["id"].(string)
			break
		}
	}
	mu := obj(h.OK(t, "POST", "/api/muses", map[string]any{"kind": "brief", "title": "Synthetic Next Muse", "brief": "synthetic brief"}, "Idempotency-Key", "l3-na-muse"))
	h.OK(t, "POST", "/api/muses/"+mu["id"].(string)+"/songs", map[string]any{"song_ids": []string{noMuse}}, "Idempotency-Key", "l3-na-assign")
	r.Settle(t)
	items, more, _ := nextPage(t, gen)
	for _, it := range items {
		switch {
		case it["code"] == "taxonomy_unmapped" && obj(it["subject"])["id"] == "skank":
			t.Error("skank is mapped but still listed as unmapped")
		case it["code"] == "song_no_muse" && obj(it["subject"])["id"] == noMuse:
			t.Error("a Song with a Muse is still listed as Muse-less")
		}
	}
	if perCode(items)["taxonomy_unmapped"] != 1 {
		t.Errorf("unmapped items after the decision: %v", perCode(items))
	}
	if n := perCode(items)["song_no_muse"] + more["song_no_muse"]; n != 5 { // 6 before, one assigned
		t.Errorf("Muse-less Songs after the assignment: %d, want 5", n)
	}
}
