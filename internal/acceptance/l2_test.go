package acceptance_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/config"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestNoPendingL2Stubs: no L2 stub is left. Statically, no source file carries a STUB(L2-...) marker; dynamically, no L2
// operation answers 501 NOT_IMPLEMENTED for an admin agent or for the operator, with ids that exist or not. The by-name skips
// of the scripted agent, the registry test and the parity runner are failures now (TestAcceptance_M2a_AgentPerformsEveryUIAction,
// TestNoPendingM4aStubs, TestParityEveryOperation).
func TestNoPendingL2Stubs(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "l2_test.go") {
				return err
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			if bytes.Contains(b, []byte("STUB(L2-")) {
				t.Errorf("%s still carries an L2 stub marker", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true})
	admin, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)
	human := r.Human(t)
	missing := "01900000-0000-7000-8000-00000000ffff"
	probed := 0
	for _, op := range api.Operations(r.API) {
		if op.Meta.UI == "" || !(strings.Contains(op.Path, "records") || strings.Contains(op.Path, "crates") || strings.Contains(op.Path, "tapes") || strings.Contains(op.Path, "curation")) {
			continue
		}
		path := strings.ReplaceAll(op.Path, "{id}", missing)
		var body any
		if op.Method != "GET" {
			body = map[string]any{}
		}
		c := admin
		if op.Meta.HumanOnly {
			c = human
		}
		resp := c.Do(t, op.Method, path, body, "Idempotency-Key", "stub-"+op.ID)
		probed++
		if resp.Status == http.StatusNotImplemented || bytes.Contains(resp.Body, []byte("NOT_IMPLEMENTED")) {
			t.Errorf("%s (%s %s) answers 501 NOT_IMPLEMENTED: %s", op.ID, op.Method, op.Path, resp.Body)
		}
	}
	if probed != 17 {
		t.Errorf("probed %d L2 operations, want 17", probed)
	}
}

// TestAcceptance_L2_PickUpgradesFloatingSlot: four of the five slots of the published Record follow their Song's Pick; when a
// human rating moves the Pick of Song S3 (slot 2), Settle upgrades exactly that slot and the changelog gains exactly one row,
// `slot_upgraded`, from the old Take to the new one, with the reason of the Pick; the other floating slots, the pinned slot and
// the empty slot change nothing and log nothing.
func TestAcceptance_L2_PickUpgradesFloatingSlot(t *testing.T) {
	r := l2Rig(t, servertest.Options{})
	h := r.Human(t)
	rid := l2ID(t, r, "records", l2R1)
	beforeSlots, beforeLog := l2Slots(t, h, rid), l2Changes(t, h, rid)
	if got := takeTitleOf(beforeSlots[1]); got != "Synthetic Take B1" {
		t.Fatalf("slot 2 resolves to %q before the rating, want B1 (the newest)", got)
	}
	// a counted human verdict for the older Take of S3 makes it the Pick by stars
	b2 := l2Take(t, r, "B2")
	h.OK(t, "PUT", "/api/tracks/"+b2+"/rating", map[string]any{"stars": 4, "verdict": "synthetic"})
	r.Settle(t)
	afterSlots, afterLog := l2Slots(t, h, rid), l2Changes(t, h, rid)
	for i := range beforeSlots {
		want := takeTitleOf(beforeSlots[i])
		if i == 1 {
			want = "Synthetic Take B2"
		}
		if got := takeTitleOf(afterSlots[i]); got != want {
			t.Errorf("slot %d resolves to %q, want %q", i+1, got, want)
		}
	}
	added := afterLog[len(beforeLog):]
	if len(added) != 1 {
		t.Fatalf("the changelog gained %d rows, want exactly one: %v", len(added), added)
	}
	row := added[0]
	if row["kind"] != "slot_upgraded" || row["reason"] != "stars" || obj(row["from"])["title"] != "Synthetic Take B1" || obj(row["to"])["title"] != "Synthetic Take B2" || row["position"] != float64(2) {
		t.Errorf("the new row: %v", row)
	}
	if text, _ := row["text"].(string); !strings.HasPrefix(text, "Slot 2 upgraded: Synthetic Take B1 → Synthetic Take B2 (4★") {
		t.Errorf("text = %q", text)
	}
	if ev := obj(row["evidence"]); ev["stars"] != float64(4) {
		t.Errorf("evidence = %v", ev)
	}
	// the pinned slot (C) and the empty slot (D) are untouched, and an agent's rating moves nothing
	if takeTitleOf(afterSlots[2]) != "Synthetic Take C2" || obj(afterSlots[3])["take"] != nil {
		t.Errorf("pinned or empty slot changed: %v %v", afterSlots[2], afterSlots[3])
	}
	l2Verify(t, r)
	if an, err := r.Provenance.Verify(context.Background()); err != nil || len(an) != 0 {
		t.Errorf("provenance.Verify: %v %v", an, err)
	}
}

// TestAcceptance_L2_PinnedNeverChanges: slot C is pinned to Take C2 of Song S4. New ratings, a Song-level pin of another Take
// and the discarding of the pinned Take never move it or write a slot_upgraded row; a discarded pin is flagged
// pin_ineligible, the catalog omits the slot and a tape needs a human's review; unpinning makes the slot follow the Pick.
func TestAcceptance_L2_PinnedNeverChanges(t *testing.T) {
	r := l2Rig(t, servertest.Options{})
	h := r.Human(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	rid := l2ID(t, r, "records", l2R1)
	c1, c2, s4 := l2Take(t, r, "C1"), l2Take(t, r, "C2"), l2Song(t, r, "S4")
	slotC := func() map[string]any { return l2Slots(t, h, rid)[2] }
	upgrades := func() int {
		n := 0
		for _, row := range l2Changes(t, h, rid) {
			if row["kind"] == "slot_upgraded" {
				n++
			}
		}
		return n
	}
	if s := slotC(); s["mode"] != "pinned" || takeTitleOf(s) != "Synthetic Take C2" || s["pin_differs"] != true {
		t.Fatalf("slot C at the start: %v", s)
	}
	// ratings that favour the other Take, then a Song-level pin of it
	h.OK(t, "PUT", "/api/tracks/"+c2+"/rating", map[string]any{"stars": 1, "verdict": "synthetic"})
	h.OK(t, "PUT", "/api/tracks/"+c1+"/rating", map[string]any{"stars": 5, "verdict": "synthetic"})
	agent.OK(t, "PUT", "/api/tracks/"+c2+"/rating", map[string]any{"stars": 5, "verdict": "critic"})
	h.OK(t, "PUT", "/api/songs/"+s4+"/pin", map[string]any{"take_id": c1})
	r.Settle(t)
	if s := slotC(); takeTitleOf(s) != "Synthetic Take C2" || s["mode"] != "pinned" {
		t.Errorf("a rating or a Song pin moved the pinned slot: %v", s)
	}
	if n := upgrades(); n != 0 {
		t.Errorf("%d slot_upgraded rows with only a pinned slot affected", n)
	}
	// the pinned Take is discarded: the slot is flagged, never silently replaced
	h.OK(t, "PATCH", "/api/tracks/"+c2, map[string]any{"status": "discarded"})
	r.Settle(t)
	s := slotC()
	if s["state"] != "pin_ineligible" || !strings.Contains(strings.Join(strs(s["warnings"]), ","), "pin_ineligible") {
		t.Errorf("discarded pin: state %v warnings %v", s["state"], s["warnings"])
	}
	if n := upgrades(); n != 0 {
		t.Errorf("%d slot_upgraded rows after the pinned Take was discarded", n)
	}
	// the catalog omits the slot; a tape export is refused until a human accepts
	_, songs, err := r.Services.Albums.Get(context.Background(), albums.PrefixRecord+rid)
	if err != nil {
		t.Fatal(err)
	}
	for _, sg := range songs {
		if sg.TakeID == c2 {
			t.Errorf("the catalog lists the ineligible pinned Take")
		}
	}
	resp := agent.Do(t, "POST", "/api/records/"+rid+"/tapes", map[string]any{}, "Idempotency-Key", "pinned-tape")
	if resp.Status != 409 || !bytes.Contains(resp.Body, []byte("TAPE_NEEDS_REVIEW")) {
		t.Errorf("agent export: %d %s", resp.Status, resp.Body)
	}
	// unpin: the slot follows the Pick (C1, pinned at the Song level) and logs slot_unpinned
	rev := obj(obj(h.OK(t, "GET", "/api/records/"+rid, nil))["record"])["revision"].(float64)
	h.OK(t, "POST", "/api/records/"+rid+"/slots", map[string]any{"revision": rev, "ops": []any{map[string]any{"op": "unpin", "slot_id": s["id"]}}})
	r.Settle(t)
	s = slotC()
	if s["mode"] != "floating" || takeTitleOf(s) != "Synthetic Take C1" {
		t.Errorf("after unpin: %v", s)
	}
	var kinds []string
	for _, row := range l2Changes(t, h, rid) {
		kinds = append(kinds, row["kind"].(string))
	}
	if !containsInOrder(kinds, []string{"slot_pinned", "slot_unpinned"}) {
		t.Errorf("changelog kinds %v lack slot_unpinned", kinds)
	}
	l2Verify(t, r)
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// l2Agent is an MCP client with a read+write PAT and the table of which operations go through call_operation.
type l2AgentSession struct {
	sess    *mcp.ClientSession
	tok     string
	id      string
	generic map[string]bool
	steps   []string
}

func newL2Agent(t *testing.T, r *servertest.Rig) *l2AgentSession {
	t.Helper()
	tok, id, err := r.Services.Tokens.Create(context.Background(), "l2-agent", []auth.Scope{auth.ScopeRead, auth.ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	a := &l2AgentSession{sess: mcpWithToken(t, r, tok), tok: tok, id: id, generic: map[string]bool{}}
	for _, op := range api.Operations(r.API) {
		a.generic[op.ID] = op.Meta.MCP != "tool"
	}
	return a
}

// call runs an operation as the agent and records the step.
func (a *l2AgentSession) call(t *testing.T, op string, args map[string]any) map[string]any {
	t.Helper()
	a.steps = append(a.steps, op)
	return obj(callTool(t, a.sess, op, args, a.generic[op]))
}

// raw runs an operation as the agent and returns the tool result, error or not.
func (a *l2AgentSession) raw(t *testing.T, op string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	a.steps = append(a.steps, op)
	return rawTool(t, a.sess, op, args, a.generic[op])
}

// audit returns the operations the agent was audited for, oldest first, and the JSON text of the listing.
func (a *l2AgentSession) audit(t *testing.T) (ops []string, text string) {
	t.Helper()
	page := obj(callTool(t, a.sess, "get_audit", map[string]any{"principal_id": "agent:" + a.id, "limit": 200}, true))
	rows, _ := page["items"].([]any)
	for i := len(rows) - 1; i >= 0; i-- {
		ops = append(ops, obj(rows[i])["op"].(string))
	}
	b, _ := json.Marshal(page)
	return ops, string(b)
}

func resultErrorText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestAcceptance_L2_AlbumCatalog: the Subsonic-facing catalog reads published Records only, through the real publish operation.
// R1 is listed as rec-<id> with its listable slots in order (the empty slot is not listed, a pinned slot only while its pin is
// eligible); the agent's draft R2 is absent until a human publishes it; unpublishing, publishing, a Pick change and a discarded
// pin each change the catalog token.
func TestAcceptance_L2_AlbumCatalog(t *testing.T) {
	r := l2Rig(t, servertest.Options{})
	ctx := context.Background()
	h := r.Human(t)
	agent := newL2Agent(t, r)
	r1, r2 := l2ID(t, r, "records", l2R1), l2ID(t, r, "records", l2R2)
	cat := r.Services.Albums
	tokenOf := func() string {
		tk, err := cat.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	ids := func() []string {
		list, err := cat.List(ctx, albums.KindRecord)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range list {
			out = append(out, a.ID)
		}
		return out
	}
	if got := ids(); len(got) != 1 || got[0] != albums.PrefixRecord+r1 {
		t.Fatalf("record albums = %v, want only R1", got)
	}
	album, songs, err := cat.Get(ctx, albums.PrefixRecord+r1)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, sg := range songs {
		var title string
		if err := r.Store.R.QueryRow(`SELECT title FROM tracks WHERE id = ?`, sg.TakeID).Scan(&title); err != nil {
			t.Fatal(err)
		}
		titles = append(titles, title)
	}
	if album.Name != l2R1 || album.Kind != albums.KindRecord || !containsInOrder(titles, []string{"Synthetic Take A1", "Synthetic Take B1", "Synthetic Take C2", "Synthetic Take E1"}) || len(titles) != 4 {
		t.Errorf("R1 album %q lists %v (the empty slot is not listed)", album.Name, titles)
	}
	if _, _, err := cat.Get(ctx, albums.PrefixRecord+r2); err == nil {
		t.Error("the agent's draft R2 is in the catalog")
	}
	// the agent proposes R2; only the human's publish puts it in the catalog
	t0 := tokenOf()
	rev := recordRevision(t, r, r2)
	agent.call(t, "propose_record", map[string]any{"id": r2, "revision": rev, "rationale": "synthetic"})
	if got := ids(); len(got) != 1 {
		t.Errorf("a proposal reached the catalog: %v", got)
	}
	h.OK(t, "POST", "/human/api/records/"+r2+"/publish", map[string]any{"revision": recordRevision(t, r, r2), "published": true})
	if got := ids(); len(got) != 2 {
		t.Errorf("the published R2 is not listed: %v", got)
	}
	t1 := tokenOf()
	if t1 == t0 {
		t.Error("publishing did not change the token")
	}
	h.OK(t, "POST", "/human/api/records/"+r2+"/publish", map[string]any{"revision": recordRevision(t, r, r2), "published": false})
	t2 := tokenOf()
	if got := ids(); len(got) != 1 || t2 == t1 {
		t.Errorf("unpublishing: ids %v, token changed %v", got, t2 != t1)
	}
	// a Pick that moves under a floating slot, and a discarded pin, are catalog changes too
	h.OK(t, "PUT", "/api/tracks/"+l2Take(t, r, "B2")+"/rating", map[string]any{"stars": 4, "verdict": "synthetic"})
	r.Settle(t)
	t3 := tokenOf()
	if t3 == t2 {
		t.Error("a Pick change did not change the token")
	}
	h.OK(t, "PATCH", "/api/tracks/"+l2Take(t, r, "C2"), map[string]any{"status": "discarded"})
	_, songs, err = cat.Get(ctx, albums.PrefixRecord+r1)
	if err != nil {
		t.Fatal(err)
	}
	if len(songs) != 3 || tokenOf() == t3 {
		t.Errorf("after the pin became ineligible: %d songs, token changed %v", len(songs), tokenOf() != t3)
	}
}

func recordRevision(t *testing.T, r *servertest.Rig, rid string) int {
	t.Helper()
	var rev int
	if err := r.Store.R.QueryRow(`SELECT revision FROM records WHERE id = ?`, rid).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

// TestL2_PublishGate: an agent drafts a Record with a canary title; the catalog does not list it and a tape export is refused
// until a human publishes; after a human unpublishes it vanishes again; an agent edit of a published Record is HUMAN_ONLY.
func TestL2_PublishGate(t *testing.T) {
	r := l2Rig(t, servertest.Options{})
	ctx := context.Background()
	h := r.Human(t)
	agent := newL2Agent(t, r)
	title := "Gate Canary " + randomTag(t)
	created := agent.call(t, "create_record", map[string]any{"title": title, "song_ids": []string{l2Song(t, r, "S1"), l2Song(t, r, "S2")}})
	rec := obj(created["record"])
	rid, rev := rec["id"].(string), int(rec["revision"].(float64))
	inCatalog := func() bool {
		_, _, err := r.Services.Albums.Get(ctx, albums.PrefixRecord+rid)
		return err == nil
	}
	listed := func() bool {
		list, _ := r.Services.Albums.List(ctx)
		for _, a := range list {
			if strings.Contains(a.Name, title) {
				return true
			}
		}
		return false
	}
	if inCatalog() || listed() {
		t.Fatal("a draft is in the catalog")
	}
	if res := agent.raw(t, "export_record_tape", map[string]any{"id": rid}); !res.IsError || !strings.Contains(resultErrorText(res), "RECORD_NOT_PUBLISHED") {
		t.Errorf("a draft export answered %+v", res)
	}
	h.OK(t, "POST", "/human/api/records/"+rid+"/publish", map[string]any{"revision": rev, "published": true})
	if !inCatalog() || !listed() {
		t.Error("the published Record is not in the catalog")
	}
	// an agent edit of a published Record is refused
	rev = recordRevision(t, r, rid)
	if res := agent.raw(t, "update_record", map[string]any{"id": rid, "revision": rev, "subtitle": "x"}); !res.IsError || !strings.Contains(resultErrorText(res), "HUMAN_ONLY") {
		t.Errorf("agent edit of a published Record: %+v", res)
	}
	h.OK(t, "POST", "/human/api/records/"+rid+"/publish", map[string]any{"revision": rev, "published": false})
	if inCatalog() || listed() {
		t.Error("an unpublished Record is still in the catalog")
	}
	if res := agent.raw(t, "export_record_tape", map[string]any{"id": rid}); !res.IsError || !strings.Contains(resultErrorText(res), "RECORD_NOT_PUBLISHED") {
		t.Errorf("an unpublished export answered %+v", res)
	}
}

func randomTag(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// l2ExportTape has the operator export a published Record's tape (a human may accept the review warnings), runs the fake
// pipeline once and returns the ready tape view.
func l2ExportTape(t *testing.T, r *servertest.Rig, h *servertest.Client, rid, key string) map[string]any {
	t.Helper()
	exp := obj(h.OK(t, "POST", "/api/records/"+rid+"/tapes", map[string]any{"accept_warnings": true}, "Idempotency-Key", key))
	id, _ := exp["id"].(string)
	if id == "" {
		t.Fatalf("export_record_tape: %v", exp)
	}
	if exp["state"] == "queued" {
		r.Tapes.RunOne(t)
	}
	tp := obj(h.OK(t, "GET", "/api/tapes/"+id, nil))
	if tp["state"] != "ready" {
		t.Fatalf("tape %s is %v, want ready: %v", id, tp["state"], tp["error"])
	}
	return tp
}

// TestL2_AgentTitleCanary: an agent writes a Muse-looking Take title (update_track) and Song title (update_song) on a published
// Record's Songs. Catalog text is read live (D-L2-27 b): the string is in the publish preview's chapter names and in the tape
// the next export plans, and nowhere else (no log, audit row, job event, notification or backup manifest).
func TestL2_AgentTitleCanary(t *testing.T) {
	logs := captureLogs(t)
	r := l2Rig(t, servertest.Options{Canary: true})
	h := r.Human(t)
	agent := newL2Agent(t, r)
	rid := l2ID(t, r, "records", l2R1)
	tag := randomTag(t)
	takeCanary, songCanary := "Inspired By Canary Artist "+tag, "Canary Muse Song "+tag
	l := newLeaks(t)
	l.add("agent_take_title", takeCanary)
	l.add("agent_song_title", songCanary)

	// A1 is the Take of slot 1 and S2 its Song
	agent.call(t, "update_track", map[string]any{"id": l2Take(t, r, "A1"), "title": takeCanary})
	var songRev int
	if err := r.Store.R.QueryRow(`SELECT revision FROM songs WHERE title = 'Synthetic Song S2'`).Scan(&songRev); err != nil {
		t.Fatal(err)
	}
	agent.call(t, "update_song", map[string]any{"id": l2Song(t, r, "S2"), "revision": songRev, "title": songCanary})

	view := obj(h.OK(t, "GET", "/api/records/"+rid+"?include=preview&include=liner", nil))
	preview := obj(view["preview"])
	var chapter1 string
	for _, c := range preview["chapters"].([]any) {
		if obj(c)["position"] == float64(1) {
			chapter1, _ = obj(c)["name"].(string)
		}
	}
	if !strings.Contains(chapter1, takeCanary) {
		t.Errorf("the preview's first chapter is %q, want the live Take title", chapter1)
	}
	if tp := l2ExportTape(t, r, h, rid, "canary-tape"); len(tp["chapters"].([]any)) != 4 {
		t.Errorf("the tape has %d chapters, want 4", len(tp["chapters"].([]any)))
	}
	// the tape key and the stored plan carry no titles; the chapter names live in the file (not asserted here)
	planJSON := dumpTables(t, r.Store.R, "tapes")["tapes"]
	l.scan("tapes table", planJSON)
	for name, text := range dumpTables(t, r.Store.R, "audit_log", "job_events", "ledger", "jobs", "batches", "exports", "backup_runs") {
		l.scan("table "+name, text)
	}
	l.scan("captured logs", logs.String())
	for i, nr := range r.Ntfy.Requests() {
		l.scan(fmt.Sprintf("ntfy request %d", i), nr.Body+nr.Path)
	}
	h.Do(t, "POST", "/api/backups", nil, "Idempotency-Key", "canary-backup")
	waitBackupsIdle(t, h)
	scanBackupText(t, l, r.BackupDir)
	l.flush()
	// the audit listing the agent reads names operations and ids, never the strings it wrote
	_, auditText := agent.audit(t)
	l.scan("get_audit listing", auditText)
	l.flush()
	if l.scanned < 8 {
		t.Errorf("scanned only %d texts", l.scanned)
	}
}

// TestAcceptance_L2_AgentProposesHumanPublishes: an agent with a read+write PAT drafts a Record over MCP and proposes it; it
// cannot publish (the operation is not a tool and REST answers HUMAN_ONLY for its PAT); the operator publishes; the agent's next
// edit is HUMAN_ONLY; the changelog tells the story in order and the audit lists every step without titles.
func TestAcceptance_L2_AgentProposesHumanPublishes(t *testing.T) {
	r := l2Rig(t, servertest.Options{})
	h := r.Human(t)
	agent := newL2Agent(t, r)
	title := "Proposed Canary " + randomTag(t)

	songs := agent.call(t, "list_songs", map[string]any{"limit": 3, "has_pick": true})
	var ids []string
	for _, it := range songs["items"].([]any) {
		ids = append(ids, obj(it)["id"].(string))
	}
	if len(ids) < 2 {
		t.Fatalf("list_songs: %v", songs)
	}
	created := agent.call(t, "create_record", map[string]any{"title": title, "song_ids": ids[:2]})
	rec := obj(created["record"])
	rid, rev := rec["id"].(string), int(rec["revision"].(float64))
	edited := agent.call(t, "edit_record_slots", map[string]any{"id": rid, "revision": rev, "ops": []any{map[string]any{"op": "pin_all"}}})
	rev = int(obj(edited["record"])["revision"].(float64))
	prop := agent.call(t, "propose_record", map[string]any{"id": rid, "revision": rev, "rationale": "ready to hear"})
	rev = int(obj(prop["record"])["revision"].(float64))
	got := agent.call(t, "get_record", map[string]any{"id": rid})
	if r := obj(got["record"]); r["status"] != "proposed" || obj(r["proposal"])["note"] != "ready to hear" {
		t.Fatalf("get_record: %v", r)
	}
	// publishing is a human act: not a tool, not reachable through call_operation, 403 over REST for the PAT
	tools, err := agent.sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "publish_record" {
			t.Error("publish_record is listed as a tool")
		}
	}
	if resp := servertest.Bearer(r, agent.tok).Do(t, "POST", "/human/api/records/"+rid+"/publish", map[string]any{"revision": rev, "published": true}); resp.Status != 403 {
		t.Errorf("a PAT publish answered %d, want 403", resp.Status)
	}
	if res := agent.raw(t, "call_operation", map[string]any{"operation_id": "publish_record", "arguments": map[string]any{"id": rid}}); res == nil {
		t.Fatal("no result")
	}
	pub := obj(h.OK(t, "POST", "/human/api/records/"+rid+"/publish", map[string]any{"revision": rev, "published": true}))
	if obj(pub["record"])["status"] != "published" {
		t.Fatalf("publish: %v", pub)
	}
	rev = int(obj(pub["record"])["revision"].(float64))
	if res := agent.raw(t, "update_record", map[string]any{"id": rid, "revision": rev, "subtitle": "after"}); !res.IsError || !strings.Contains(resultErrorText(res), "HUMAN_ONLY") {
		t.Errorf("the agent's edit of a published Record: %+v", res)
	}
	var kinds []string
	for _, row := range l2Changes(t, h, rid) {
		kinds = append(kinds, row["kind"].(string))
	}
	if !containsInOrder(kinds, []string{"created", "slot_added", "slot_added", "slot_pinned", "proposed", "published"}) {
		t.Errorf("changelog kinds = %v", kinds)
	}
	ops, auditText := agent.audit(t)
	if !containsInOrder(ops, []string{"list_songs", "create_record", "edit_record_slots", "propose_record", "get_record"}) {
		t.Errorf("audit ops %v lack the agent's steps", ops)
	}
	if strings.Contains(auditText, title) || strings.Contains(auditText, "ready to hear") {
		t.Errorf("the audit listing carries a title or a note")
	}
}

// TestAcceptance_L2_EndToEnd_Agent: an agent with a read+write PAT walks the whole L2 flow over MCP: list_songs, a Crate of the
// chosen Songs, a Record from the Crate's Songs, slot edits, the liner and energy views, a proposal and the changelog; the
// operator publishes over REST with a human principal; the agent exports the tape (the fake pipeline) and reads it back ready;
// the audit lists every step and carries no title and no Muse text.
func TestAcceptance_L2_EndToEnd_Agent(t *testing.T) {
	r := l2Rig(t, servertest.Options{})
	h := r.Human(t)
	agent := newL2Agent(t, r)
	tag := randomTag(t)
	crateName, recTitle, note := "E2E Crate "+tag, "E2E Record "+tag, "e2e rationale "+tag

	songs := agent.call(t, "list_songs", map[string]any{"limit": 4, "has_pick": true})
	var ids []string
	for _, it := range songs["items"].([]any) {
		ids = append(ids, obj(it)["id"].(string))
	}
	if len(ids) < 3 {
		t.Fatalf("list_songs: %v", songs)
	}
	ids = ids[:3]
	crate := obj(agent.call(t, "create_crate", map[string]any{"name": crateName, "kind": "manual"})["crate"])
	cid := crate["id"].(string)
	var add []any
	for _, id := range ids {
		add = append(add, map[string]any{"song_id": id})
	}
	if res := agent.call(t, "edit_crate_items", map[string]any{"id": cid, "add": add}); res["count"] != float64(3) {
		t.Fatalf("edit_crate_items: %v", res)
	}
	fromCrate := agent.call(t, "list_songs", map[string]any{"crate_id": cid, "limit": 10})
	var members []string
	for _, it := range fromCrate["items"].([]any) {
		members = append(members, obj(it)["id"].(string))
	}
	slices.Sort(members)
	want := slices.Clone(ids)
	slices.Sort(want)
	if !slices.Equal(members, want) {
		t.Fatalf("the Crate's Songs = %v, want %v", members, want)
	}
	created := agent.call(t, "create_record", map[string]any{"title": recTitle, "song_ids": members})
	rec := obj(created["record"])
	rid, rev := rec["id"].(string), int(rec["revision"].(float64))
	edited := agent.call(t, "edit_record_slots", map[string]any{"id": rid, "revision": rev, "ops": []any{map[string]any{"op": "pin_all"}}})
	rev = int(obj(edited["record"])["revision"].(float64))
	view := agent.call(t, "get_record", map[string]any{"id": rid, "include": []string{"liner", "energy"}})
	if obj(view["liner"])["markdown"] == "" || len(obj(view["energy"])["slots"].([]any)) != 3 {
		t.Errorf("get_record include=liner,energy: %v", view)
	}
	prop := agent.call(t, "propose_record", map[string]any{"id": rid, "revision": rev, "rationale": note})
	rev = int(obj(prop["record"])["revision"].(float64))
	changes := agent.call(t, "get_record_changes", map[string]any{"id": rid, "order": "asc"})
	var kinds []string
	for _, it := range changes["items"].([]any) {
		kinds = append(kinds, obj(it)["kind"].(string))
	}
	if !containsInOrder(kinds, []string{"created", "slot_added", "slot_pinned", "proposed"}) {
		t.Errorf("changelog kinds = %v", kinds)
	}
	h.OK(t, "POST", "/human/api/records/"+rid+"/publish", map[string]any{"revision": rev, "published": true})
	exp := agent.call(t, "export_record_tape", map[string]any{"id": rid})
	tid, _ := exp["id"].(string)
	if tid == "" {
		t.Fatalf("export_record_tape: %v", exp)
	}
	r.Tapes.RunOne(t)
	tp := agent.call(t, "get_tape", map[string]any{"id": tid})
	if tp["state"] != "ready" || obj(tp["download"])["url"] == nil || len(tp["chapters"].([]any)) != 3 {
		t.Errorf("get_tape: %v", tp)
	}
	ops, auditText := agent.audit(t)
	if !containsInOrder(ops, agent.steps) {
		t.Errorf("audit ops %v do not contain the steps %v in order", ops, agent.steps)
	}
	for _, text := range []string{crateName, recTitle, note, "Synthetic Curation Muse", "Synthetic Song S"} {
		if strings.Contains(auditText, text) {
			t.Errorf("the audit listing carries %q", text)
		}
	}
}

// sigsOf returns the signature parameters of the signed links (/media/...?exp=..&sig=..) found in a JSON text.
var sigRe = regexp.MustCompile(`[?&]sig=([A-Za-z0-9_-]{16,})`)

func sigsOf(text string) []string {
	var out []string
	for _, m := range sigRe.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestNoSecretLeak_L2 extends TestNoSecretLeak to curation: the PAT, the rig's keys and the signatures of the signed cover and
// tape links appear only in the responses that mint them; the agent-authored Record, Crate and rationale text (canaries) appears
// in no log, audit row, job event, notification, stored plan, export, backup manifest or problem body.
func TestNoSecretLeak_L2(t *testing.T) {
	logs := captureLogs(t)
	r := l2Rig(t, servertest.Options{Canary: true})
	l := newLeaks(t)
	l.add("secret_key", keyForms(canaryRigKey)...)
	l.add("edge_secret", canaryEdge)
	l.add("ntfy_token", canaryNtfy)
	l.add("operator_password", canaryPassword)
	tag := "L2canary" + randomTag(t)
	recTitle, recSub, recLiner, rationale := "Record Title "+tag, "Record Subtitle "+tag, "Record Liner "+tag, "Rationale Text "+tag
	crateName, crateDesc := "Crate Name "+tag, "Crate Description "+tag
	l.add("record_title", recTitle)
	l.add("record_subtitle", recSub)
	l.add("record_liner", recLiner)
	l.add("proposal_rationale", rationale)
	l.add("crate_name", crateName)
	l.add("crate_description", crateDesc)

	tok, _, err := r.Services.Tokens.Create(context.Background(), "l2-leak", []auth.Scope{auth.ScopeRead, auth.ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	l.add("pat", tok)
	human, agent := r.Human(t), servertest.Bearer(r, tok)
	s1, s2 := l2Song(t, r, "S1"), l2Song(t, r, "S2")

	// the agent writes its canaries by their documented doors
	created := obj(agent.OK(t, "POST", "/api/records", map[string]any{"title": recTitle, "subtitle": recSub, "liner_note": recLiner, "song_ids": []string{s1, s2}}, "Idempotency-Key", "l2-leak-rec"))
	rec := obj(created["record"])
	rid, rev := rec["id"].(string), int(rec["revision"].(float64))
	agent.OK(t, "POST", "/api/records/"+rid+"/proposal", map[string]any{"revision": rev, "rationale": rationale})
	agent.OK(t, "POST", "/api/crates", map[string]any{"name": crateName, "description": crateDesc, "kind": "manual"}, "Idempotency-Key", "l2-leak-crate")
	// refusals that must not echo them
	agent.Do(t, "PATCH", "/api/records/"+rid, map[string]any{"revision": 1, "title": recTitle})
	agent.Do(t, "POST", "/api/crates", map[string]any{"name": crateName, "kind": "smart", "filter": map[string]any{"no_such_key": recTitle}}, "Idempotency-Key", "l2-leak-bad")
	agent.Do(t, "POST", "/api/records/"+l2ID(t, r, "records", l2R2)+"/tapes", map[string]any{}, "Idempotency-Key", "l2-leak-tape")

	// the operator's published Record: its cover links and tape link are signed
	r1 := l2ID(t, r, "records", l2R1)
	var sigs []string
	view := human.Do(t, "GET", "/api/records/"+r1+"?include=tapes", nil)
	sigs = append(sigs, sigsOf(string(view.Body))...)
	tp := l2ExportTape(t, r, human, r1, "l2-leak-tape-ok")
	tb, _ := json.Marshal(tp)
	sigs = append(sigs, sigsOf(string(tb))...)
	list := human.Do(t, "GET", "/api/records", nil)
	sigs = append(sigs, sigsOf(string(list.Body))...)
	if len(sigs) < 2 {
		t.Fatalf("found %d signed-link signatures, the scan would be vacuous", len(sigs))
	}
	for _, s := range sigs {
		l.add("signed_link_sig", s)
	}
	human.Do(t, "GET", "/api/records/"+r1, nil)
	agent.Do(t, "GET", "/api/records/"+rid+"?include=changes", nil)
	agent.Do(t, "GET", "/api/crates", nil)

	// an export and a backup
	human.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{l2Take(t, r, "A1")}}}, "Idempotency-Key", "l2-leak-exp")
	human.Do(t, "POST", "/api/backups", nil, "Idempotency-Key", "l2-leak-backup")
	waitBackupsIdle(t, human)

	// responses: secrets never leave their one door; the canaries live only where the op returns them
	secretsOnly := newLeaks(t)
	for _, s := range l.secrets {
		switch s.kind {
		case "record_title", "record_subtitle", "record_liner", "proposal_rationale", "crate_name", "crate_description", "signed_link_sig":
		default:
			secretsOnly.add(s.kind, s.value)
		}
	}
	for _, lc := range []leakClient{{name: "human", c: human}, {name: "agent", c: agent}} {
		for _, resp := range lc.c.Responses() {
			path := strings.SplitN(resp.Path, "?", 2)[0]
			loc := fmt.Sprintf("response of %s %s %s", lc.name, resp.Method, path)
			secretsOnly.scan(loc, responseText(resp, ""))
			if resp.Status >= 400 {
				l.scan("problem body of "+lc.name+" "+resp.Method+" "+path, string(resp.Body))
			}
		}
	}
	secretsOnly.flush()
	tables := dumpTables(t, r.Store.R, "audit_log", "job_events", "ledger", "jobs", "batches", "exports", "backup_runs", "tapes", "comments")
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
	if l.scanned < 15 {
		t.Errorf("scanned only %d texts", l.scanned)
	}
	// the canaries did enter the places they belong to (the scan is not vacuous)
	for q, want := range map[string]string{
		`SELECT title FROM records WHERE title = ?`:            recTitle,
		`SELECT name FROM crates WHERE name = ?`:               crateName,
		`SELECT description FROM crates WHERE description = ?`: crateDesc,
		`SELECT liner_note FROM records WHERE liner_note = ?`:  recLiner,
		`SELECT subtitle FROM records WHERE subtitle = ?`:      recSub,
	} {
		var got string
		if err := r.Store.R.QueryRow(q, want).Scan(&got); err != nil {
			t.Errorf("canary %q is not stored where it belongs: %v", want, err)
		}
	}
	// the signatures belong to the responses that minted them: the same links are not in the tables, logs or notifications
	for name, text := range tables {
		if name == "tapes" {
			continue
		}
		for _, s := range sigs {
			if strings.Contains(text, s) {
				t.Errorf("a signed-link signature is stored in %s", name)
			}
		}
	}
}

// l2RestoredServices serves an already restored data directory as `serve` would build it, without HTTP.
func l2RestoredServices(t *testing.T, dataDir string, now time.Time) *app.Services {
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
		app.Options{Now: func() time.Time { return now }, Version: "v-test", L2: app.L2Options{FlacVersion: &fv}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// TestRestore_RebuildsRecordChangelog: a snapshot taken with a published Record and a ready tape is restored into an empty
// directory; Pick events that arrive after the restore are followed by the Syncer (one slot_upgraded row), records.Verify is
// clean, and the tape's file, which no backup holds, is handled: its row is flagged by the file check and expires with the sweep.
func TestRestore_RebuildsRecordChangelog(t *testing.T) {
	r := l2Rig(t, servertest.Options{Files: true})
	l2MaterialiseMasters(t, r)
	h := r.Human(t)
	rid := l2ID(t, r, "records", l2R1)
	tp := l2ExportTape(t, r, h, rid, "restore-tape")
	tapeID := tp["id"].(string)
	var tapePath string
	if err := r.Store.R.QueryRow(`SELECT path FROM tapes WHERE id = ?`, tapeID).Scan(&tapePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.DataDir, tapePath)); err != nil {
		t.Fatalf("the live tape file: %v", err)
	}
	beforeLog := len(l2Changes(t, h, rid))

	h.OK(t, "POST", "/api/backups", nil, "Idempotency-Key", "restore-bk")
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
	if _, err := os.Stat(filepath.Join(into, tapePath)); err == nil {
		t.Fatal("a tape file is in the restored directory: backups never hold tapes")
	}
	svc := l2RestoredServices(t, into, now)
	ctx := context.Background()
	// the restored changelog equals the original, and Verify is clean before anything new happens
	rows, err := svc.Records.Changes(ctx, records.ChangeFilter{RecordID: rid, Limit: 200})
	if err != nil || len(rows.Items) != beforeLog {
		t.Fatalf("restored changelog: %d rows (%v), want %d", len(rows.Items), err, beforeLog)
	}
	// a Pick event after the restore: a human rating for the older Take of S3
	op := auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "local"}
	stars := 4
	b2 := l2Take(t, r, "B2")
	if _, err := svc.Deps.Judgments.Rate(ctx, op, b2, &stars, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Provenance.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Syncer.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Records.Changes(ctx, records.ChangeFilter{RecordID: rid, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != beforeLog+1 {
		t.Fatalf("changelog after the new Pick event: %d rows, want %d", len(after.Items), beforeLog+1)
	}
	an, err := svc.Records.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range an {
		if a.Severity == "error" {
			t.Errorf("records.Verify after the restore: %+v", a)
		}
	}
	// the tape row says ready, its file is gone: the file check sees it, the sweep expires it, nothing else breaks
	var state string
	if err := svc.Deps.Store.R.QueryRow(`SELECT state FROM tapes WHERE id = ?`, tapeID).Scan(&state); err != nil || state != "ready" {
		t.Fatalf("restored tape row: %q %v", state, err)
	}
	if n, err := svc.Tapes.Sweep(ctx, 0); err != nil || n != 1 {
		t.Fatalf("sweep expired %d tapes (%v), want 1", n, err)
	}
	if err := svc.Deps.Store.R.QueryRow(`SELECT state FROM tapes WHERE id = ?`, tapeID).Scan(&state); err != nil || state != "expired" {
		t.Errorf("after the sweep: %q %v", state, err)
	}
	if an, err := svc.Records.Verify(ctx); err != nil {
		t.Fatal(err)
	} else {
		for _, a := range an {
			if a.Severity == "error" {
				t.Errorf("records.Verify after the sweep: %+v", a)
			}
		}
	}
}

// l2MaterialiseMasters writes a small synthetic file (mode 0444) for every master, artifact and rendition row whose file the
// fixture does not provide and points the row's hash (and size) at it, so a backup reads and a restore verifies them all.
func l2MaterialiseMasters(t *testing.T, r *servertest.Rig) {
	t.Helper()
	// the frozen-column guards are dropped in this scratch rig only: the fixture's hashes are placeholders
	if _, err := r.Store.W.Exec(`DROP TRIGGER tracks_frozen`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ sel, upd string }{
		{`SELECT master_path FROM tracks WHERE master_path <> ''`, `UPDATE tracks SET master_sha256 = ?, master_bytes = ? WHERE master_path = ?`},
		{`SELECT path FROM artifacts`, `UPDATE artifacts SET sha256 = ? WHERE path = ?`},
		{`SELECT path FROM renditions`, `UPDATE renditions SET sha256 = ? WHERE path = ?`},
	} {
		rows, err := r.Store.R.Query(c.sel)
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, p)
		}
		_ = rows.Close()
		for _, p := range paths {
			full := filepath.Join(r.DataDir, p)
			if _, err := os.Stat(full); err == nil {
				continue
			}
			body := []byte("synthetic file " + p)
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, body, 0o444); err != nil { //nolint:gosec // 0444 is the point
				t.Fatal(err)
			}
			sum := sha256.Sum256(body)
			args := []any{hex.EncodeToString(sum[:]), p}
			if strings.Contains(c.upd, "master_bytes") {
				args = []any{hex.EncodeToString(sum[:]), len(body), p}
			}
			if _, err := r.Store.W.Exec(c.upd, args...); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestAcceptance_L2_Mirror: on the mirror of the real library's structure (FixtureL2Mirror: 120 Muses, 4 originals, 5 lossy groups,
// all settled into Songs) two Records are built over the Songs it derives and published; records.Verify and the L1
// provenance.Verify are empty, and the Record view, the catalog and a tape made through the fake pipeline all agree on the same
// Takes in the same order.
func TestAcceptance_L2_Mirror(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2Mirror: true})
	r.Settle(t)
	ctx := context.Background()
	h := r.Human(t)
	songsOf := func(where string) []string {
		rows, err := r.Store.R.Query(`SELECT DISTINCT ts.song_id FROM take_songs ts JOIN tracks t ON t.id = ts.track_id JOIN songs s ON s.id = ts.song_id
			WHERE s.retired_at IS NULL AND t.import_rule = 'reference' AND ` + where + ` ORDER BY ts.song_id`)
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
	originals, groups := songsOf(`t.title LIKE 'Synthetic Original %'`), songsOf(`t.title LIKE 'Synthetic Pilot %'`)
	if len(originals) != 4 || len(groups) != 5 {
		t.Fatalf("the mirror settles into %d original Songs and %d group Songs, want 4 and 5", len(originals), len(groups))
	}
	type built struct {
		id    string
		songs []string
		lossy bool
	}
	var recs []built
	for i, c := range []struct {
		title string
		songs []string
		lossy bool
	}{{"Synthetic Mirror Originals", originals, false}, {"Synthetic Mirror Groups", groups, true}} {
		created := obj(h.OK(t, "POST", "/api/records", map[string]any{"title": c.title, "song_ids": c.songs}, "Idempotency-Key", "mirror-"+strconv.Itoa(i)))
		rec := obj(created["record"])
		h.OK(t, "POST", "/human/api/records/"+rec["id"].(string)+"/publish", map[string]any{"revision": rec["revision"], "published": true})
		recs = append(recs, built{rec["id"].(string), c.songs, c.lossy})
	}
	r.Settle(t) // the Syncer has nothing left to follow
	if an, err := r.Services.Records.Verify(ctx); err != nil {
		t.Fatal(err)
	} else {
		for _, a := range an {
			if a.Severity == "error" {
				t.Errorf("records.Verify: %+v", a)
			}
		}
	}
	if an, err := r.Provenance.Verify(ctx); err != nil || len(an) != 0 {
		t.Errorf("provenance.Verify: %v %v", an, err)
	}
	for _, b := range recs {
		view := obj(h.OK(t, "GET", "/api/records/"+b.id, nil))
		slots := l2Slots(t, h, b.id)
		if len(slots) != len(b.songs) || obj(view["totals"])["slots"] != float64(len(b.songs)) || obj(view["totals"])["playable"] != float64(len(b.songs)) {
			t.Fatalf("view totals %v for %d slots", view["totals"], len(slots))
		}
		var takes []string
		for _, s := range slots {
			takes = append(takes, obj(s["take"])["id"].(string))
			if obj(s["take"])["lossy"] != b.lossy {
				t.Errorf("slot take lossy = %v, want %v", obj(s["take"])["lossy"], b.lossy)
			}
		}
		// the catalog lists exactly those Takes, in order
		_, songs, err := r.Services.Albums.Get(ctx, albums.PrefixRecord+b.id)
		if err != nil {
			t.Fatal(err)
		}
		var listed []string
		for _, sg := range songs {
			listed = append(listed, sg.TakeID)
		}
		if !slices.Equal(listed, takes) {
			t.Errorf("catalog takes %v, view takes %v", listed, takes)
		}
		// and so does a tape
		tp := l2ExportTape(t, r, h, b.id, "mirror-tape-"+b.id)
		items, _ := tp["items"].([]any)
		var tapeTakes []string
		for _, it := range items {
			tapeTakes = append(tapeTakes, obj(it)["take_id"].(string))
		}
		if !slices.Equal(tapeTakes, takes) || len(tp["chapters"].([]any)) != len(takes) || tp["contains_lossy"] != b.lossy {
			t.Errorf("tape takes %v (lossy %v), view takes %v", tapeTakes, tp["contains_lossy"], takes)
		}
	}
	// the other direction: the catalog holds both Records and nothing else of the curation
	list, err := r.Services.Albums.List(ctx, albums.KindRecord)
	if err != nil || len(list) != 2 {
		t.Errorf("record albums = %d (%v), want 2", len(list), err)
	}
}
