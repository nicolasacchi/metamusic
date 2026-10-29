package api_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// The REST tests of the eight Records operations (lane V2) run on the full rig with FixtureL2: Records R1 (published, five
// slots) and R2 (a draft by an agent), Muses with canary-style synthetic text, loudness analyses.

type recRig struct {
	r     *servertest.Rig
	human *servertest.Client
	agent *servertest.Client
}

func newRecRig(t *testing.T) *recRig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true})
	r.Settle(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	return &recRig{r: r, human: r.Human(t), agent: agent}
}

func (x *recRig) id(t *testing.T, table, title string) string {
	t.Helper()
	var id string
	if err := x.r.Store.R.QueryRow(`SELECT id FROM `+table+` WHERE title = ?`, title).Scan(&id); err != nil {
		t.Fatalf("%s %q: %v", table, title, err)
	}
	return id
}
func (x *recRig) record(t *testing.T, title string) string { return x.id(t, "records", title) }
func (x *recRig) song(t *testing.T, label string) string {
	return x.id(t, "songs", "Synthetic Song "+label)
}

const (
	r1Title     = "Synthetic Record One"
	r2Title     = "Synthetic Record Two"
	museCanary1 = "Synthetic Curation Muse 1"
	museCanary2 = "Synthetic Curation Muse 2"
)

// problemOf decodes a problem body into code and details.
func problemOf(t *testing.T, resp *servertest.Response) (string, map[string]any) {
	t.Helper()
	b := m(resp.JSON(t))
	return str(b["code"]), m(b["details"])
}

// TestRecordsREST_GetRecord: slots with mode, state, pin_differs and the plan gain; totals; the app liner with the Muse line;
// the default includes (liner, tapes, changes); pending false after Settle; ids-only 404.
func TestRecordsREST_GetRecord(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	id := x.record(t, r1Title)
	v := m(x.human.OK(t, "GET", "/api/records/"+id, nil))
	slots := arr(v["slots"])
	if len(slots) != 5 {
		t.Fatalf("slots = %d, want 5", len(slots))
	}
	c, d := m(slots[2]), m(slots[3])
	if c["mode"] != "pinned" || c["pin_differs"] != true || c["state"] != "ok" || m(c["take"])["title"] != "Synthetic Take C2" {
		t.Errorf("slot C: %v", c)
	}
	if d["state"] != "empty" || d["take"] != nil || fmt.Sprint(d["warnings"]) != "[empty]" || d["gain_db"] != nil {
		t.Errorf("slot D: %v", d)
	}
	if g := m(slots[0])["gain_db"]; g != float64(1) {
		t.Errorf("slot A gain_db = %v, want 1 (-15.5 LUFS to the -14 target)", g)
	}
	tot := m(v["totals"])
	if tot["slots"] != float64(5) || tot["playable"] != float64(4) || tot["lossy"] != float64(1) || tot["undecided"] != float64(2) || tot["duration_s"] != float64(1020) {
		t.Errorf("totals: %v", tot)
	}
	if v["pending"] != false {
		t.Errorf("pending after Settle = %v", v["pending"])
	}
	l := m(v["liner"])
	if !strings.Contains(str(l["markdown"]), "inspired by "+museCanary2) || m(l["summary"])["playable"] != float64(4) {
		t.Errorf("liner: %v", l)
	}
	if len(arr(v["changes"])) != 8 || v["energy"] != nil || v["preview"] != nil {
		t.Errorf("default includes: changes=%d energy=%v preview=%v", len(arr(v["changes"])), v["energy"], v["preview"])
	}
	// an agent reads the same liner (the app audience), and sees the operator as "human"
	a := m(x.agent.OK(t, "GET", "/api/records/"+id+"?include=liner", nil))
	if m(a["liner"]) == nil || m(m(a["record"])["created_by"])["id"] != "human" {
		t.Errorf("agent view: liner=%v created_by=%v", a["liner"], m(a["record"])["created_by"])
	}
	// unknown ids and unknown includes
	resp := x.human.Do(t, "GET", "/api/records/no-such-record", nil)
	code, det := problemOf(t, resp)
	if resp.Status != 404 || code != "NOT_FOUND" || det["record_id"] != "no-such-record" {
		t.Errorf("unknown record: %d %s", resp.Status, resp.Body)
	}
	if bad := x.human.Do(t, "GET", "/api/records/"+id+"?include=muses", nil); bad.Status != 422 {
		t.Errorf("unknown include: %d", bad.Status)
	}
}

// TestRecordsREST_Include: every include on its own adds exactly its part; the preview carries the tape's chapters, tags and
// warnings; the changelog is bounded by changes_limit.
func TestRecordsREST_Include(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	id := x.record(t, r1Title)
	one := func(inc string) map[string]any {
		return m(x.human.OK(t, "GET", "/api/records/"+id+"?include="+inc, nil))
	}
	for _, inc := range []string{"liner", "energy", "changes", "preview"} {
		v := one(inc)
		for _, part := range []string{"liner", "energy", "changes", "preview"} {
			if (v[part] != nil) != (part == inc) {
				t.Errorf("include=%s: part %s present=%v", inc, part, v[part] != nil)
			}
		}
	}
	e := m(one("energy")["energy"])
	if e["target_lufs"] != float64(-14) || len(arr(e["slots"])) != 5 {
		t.Errorf("energy: target %v slots %d", e["target_lufs"], len(arr(e["slots"])))
	}
	for _, s := range arr(e["slots"]) {
		if n := len(arr(m(s)["lufs"])); n > 120 {
			t.Errorf("energy slot has %d points", n)
		}
	}
	if d := arr(m(one("energy")["energy"])["slots"]); m(d[3])["lufs"] == nil || len(arr(m(d[3])["lufs"])) != 0 {
		t.Errorf("an empty slot has lufs [] (got %v)", m(d[3])["lufs"])
	}
	p := m(one("preview")["preview"])
	if len(arr(p["chapters"])) != 4 || len(arr(p["tags"])) == 0 || strings.Contains(fmt.Sprint(p), museCanary1) {
		t.Errorf("preview: %v", p)
	}
	ch := m(x.human.OK(t, "GET", "/api/records/"+id+"?include=changes&changes_limit=3", nil))
	if got := arr(ch["changes"]); len(got) != 3 || m(got[0])["seq"] != float64(8) {
		t.Errorf("changes_limit=3: %v", got)
	}
}

// TestRecordsREST_List: status, q, song_id and hidden filters, paging with cursor, summaries with signed covers.
func TestRecordsREST_List(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	all := m(x.human.OK(t, "GET", "/api/records", nil))
	if all["total"] != float64(2) || len(arr(all["items"])) != 2 || m(arr(all["items"])[0])["title"] != r2Title {
		t.Fatalf("list: %v", all)
	}
	it := m(arr(all["items"])[1])
	if it["slots"] != float64(5) || it["playable"] != float64(4) || it["warnings"] != float64(3) || !strings.HasPrefix(str(m(it["cover"])["url_512"]), "/media/") {
		t.Errorf("summary: %v", it)
	}
	pub := m(x.human.OK(t, "GET", "/api/records?status=published", nil))
	if pub["total"] != float64(1) || m(arr(pub["items"])[0])["title"] != r1Title {
		t.Errorf("status filter: %v", pub)
	}
	if q := m(x.human.OK(t, "GET", "/api/records?q=Two", nil)); q["total"] != float64(1) {
		t.Errorf("q: %v", q)
	}
	if s := m(x.human.OK(t, "GET", "/api/records?song_id="+x.song(t, "S1"), nil)); s["total"] != float64(1) || m(arr(s["items"])[0])["title"] != r2Title {
		t.Errorf("song filter: %v", s)
	}
	p1 := m(x.human.OK(t, "GET", "/api/records?limit=1", nil))
	if p1["next_cursor"] == nil || len(arr(p1["items"])) != 1 {
		t.Fatalf("page 1: %v", p1)
	}
	p2 := m(x.human.OK(t, "GET", "/api/records?limit=1&cursor="+str(p1["next_cursor"]), nil))
	if len(arr(p2["items"])) != 1 || p2["next_cursor"] != nil || m(arr(p2["items"])[0])["title"] != r1Title {
		t.Errorf("page 2: %v", p2)
	}
	// hide R2: gone unless include_hidden
	r2 := x.record(t, r2Title)
	x.human.OK(t, "PATCH", "/api/records/"+r2, map[string]any{"revision": 1, "hidden": true})
	if l := m(x.human.OK(t, "GET", "/api/records", nil)); l["total"] != float64(1) {
		t.Errorf("hidden listed: %v", l)
	}
	if l := m(x.human.OK(t, "GET", "/api/records?include_hidden=true", nil)); l["total"] != float64(2) {
		t.Errorf("include_hidden: %v", l)
	}
}

// TestRecordsREST_Create: the key is required, a replay returns the stored 201 and one Record, the response is liner-less, the
// rules answer ids and fields.
func TestRecordsREST_Create(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	body := map[string]any{"title": "Synthetic New Record", "song_ids": []string{x.song(t, "S1"), x.song(t, "S4")}}
	if resp := x.agent.Do(t, "POST", "/api/records", body); resp.Status != 428 {
		t.Errorf("missing key: %d %s", resp.Status, resp.Body)
	}
	resp := x.agent.Do(t, "POST", "/api/records", body, "Idempotency-Key", "k-rec-1")
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	v := m(resp.JSON(t))
	rec := m(v["record"])
	if rec["status"] != "draft" || rec["revision"] != float64(1) || len(arr(v["slots"])) != 2 || v["liner"] != nil || m(rec["created_by"])["kind"] != "agent" {
		t.Errorf("created: %v", v)
	}
	if m(arr(v["slots"])[0])["mode"] != "floating" || m(m(arr(v["slots"])[0])["take"])["title"] != "Synthetic Take S1A" {
		t.Errorf("slot 1: %v", arr(v["slots"])[0])
	}
	rep := x.agent.Do(t, "POST", "/api/records", body, "Idempotency-Key", "k-rec-1")
	if rep.Status != 201 || m(m(rep.JSON(t))["record"])["id"] != rec["id"] || rep.Header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay: %d replayed=%q %s", rep.Status, rep.Header.Get("Idempotent-Replayed"), rep.Body)
	}
	var n int
	if err := x.r.Store.R.QueryRow(`SELECT COUNT(*) FROM records WHERE title = 'Synthetic New Record'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("records after replay = %d, %v", n, err)
	}
	for name, c := range map[string]struct {
		body   map[string]any
		status int
		code   string
	}{
		"empty title":  {map[string]any{"title": ""}, 422, "VALIDATION_FAILED"},
		"unknown song": {map[string]any{"title": "T", "song_ids": []string{"no-such-song"}}, 422, "UNKNOWN_SONG"},
		"duplicate":    {map[string]any{"title": "T", "song_ids": []string{x.song(t, "S1"), x.song(t, "S1")}}, 422, "VALIDATION_FAILED"},
	} {
		resp := x.agent.Do(t, "POST", "/api/records", c.body, "Idempotency-Key", "k-"+strings.ReplaceAll(name, " ", "-"))
		if code, _ := problemOf(t, resp); resp.Status != c.status || code != c.code {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Body)
		}
	}
	ro, _ := x.r.Agent(t, auth.ScopeRead)
	if resp := ro.Do(t, "POST", "/api/records", body, "Idempotency-Key", "k-rec-ro"); resp.Status != 403 {
		t.Errorf("read scope: %d", resp.Status)
	}
}

// TestRecordsREST_UpdateEditPropose: the revision lock, slot edits (all or nothing, add at the end by default), the proposal
// rules and the principal rules, with the response being the Record view every time.
func TestRecordsREST_UpdateEditPropose(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	id := x.record(t, r2Title)
	path := "/api/records/" + id

	// update: stale revision, then a rename
	resp := x.agent.Do(t, "PATCH", path, map[string]any{"revision": 9, "title": "Synthetic Renamed"})
	if code, det := problemOf(t, resp); resp.Status != 409 || code != "STALE_REVISION" || det["revision"] != float64(1) {
		t.Errorf("stale: %d %s", resp.Status, resp.Body)
	}
	v := m(x.agent.OK(t, "PATCH", path, map[string]any{"revision": 1, "title": "Synthetic Renamed", "cover_reroll": true}))
	if m(v["record"])["title"] != "Synthetic Renamed" || m(v["record"])["revision"] != float64(2) || m(v["record"])["cover_rerolls"] != float64(1) {
		t.Errorf("update: %v", m(v["record"]))
	}

	// edit: add S3 at the end (absent position), pin it, set a side; one bad op rolls everything back
	s3 := x.song(t, "S3")
	v = m(x.agent.OK(t, "POST", path+"/slots", map[string]any{"revision": 2, "ops": []any{map[string]any{"op": "add", "song_id": s3}}}))
	slots := arr(v["slots"])
	if len(slots) != 3 || m(slots[2])["song"].(map[string]any)["id"] != s3 || m(v["record"])["revision"] != float64(3) {
		t.Fatalf("add: %v", v)
	}
	slotID := str(m(slots[2])["id"])
	bad := x.agent.Do(t, "POST", path+"/slots", map[string]any{"revision": 3, "ops": []any{
		map[string]any{"op": "set_side", "slot_id": slotID, "side": "B"}, map[string]any{"op": "move", "slot_id": slotID, "position": 9}}})
	if code, det := problemOf(t, bad); bad.Status != 422 || code != "INVALID_POSITION" || det["op"] != float64(1) {
		t.Errorf("rolled back op: %d %s", bad.Status, bad.Body)
	}
	if after := m(x.agent.OK(t, "GET", path, nil)); m(after["record"])["revision"] != float64(3) || m(arr(after["slots"])[2])["side"] != nil {
		t.Errorf("a failed edit changed the record: %v", m(after["record"]))
	}
	v = m(x.agent.OK(t, "POST", path+"/slots", map[string]any{"revision": 3, "ops": []any{
		map[string]any{"op": "set_side", "slot_id": slotID, "side": "B"}, map[string]any{"op": "move", "slot_id": slotID, "position": 1}}}))
	if m(arr(v["slots"])[0])["id"] != slotID || m(arr(v["slots"])[0])["side"] != "B" {
		t.Errorf("move+side: %v", arr(v["slots"])[0])
	}
	if stale := x.agent.Do(t, "POST", path+"/slots", map[string]any{"revision": 2, "ops": []any{map[string]any{"op": "pin_all"}}}); stale.Status != 409 {
		t.Errorf("stale edit: %d", stale.Status)
	}
	if ops := x.agent.Do(t, "POST", path+"/slots", map[string]any{"revision": 4, "ops": []any{}}); ops.Status != 422 {
		t.Errorf("no ops: %d", ops.Status)
	}

	// propose: rationale rules, then the agent cannot publish and the human can
	rev := int(m(v["record"])["revision"].(float64))
	if empty := x.agent.Do(t, "POST", path+"/proposal", map[string]any{"revision": rev, "rationale": ""}); empty.Status != 422 {
		t.Errorf("empty rationale: %d", empty.Status)
	}
	p := m(x.agent.OK(t, "POST", path+"/proposal", map[string]any{"revision": rev, "rationale": "ready to hear"}))
	prop := m(m(p["record"])["proposal"])
	if m(p["record"])["status"] != "proposed" || prop["note"] != "ready to hear" || p["liner"] != nil {
		t.Errorf("proposed: %v", p["record"])
	}
	if again := x.agent.Do(t, "POST", path+"/proposal", map[string]any{"revision": rev + 1, "rationale": "again"}); again.Status != 409 {
		t.Errorf("propose twice: %d %s", again.Status, again.Body)
	}
	// a non-human edit of a proposed record sends it back to draft
	d := m(x.agent.OK(t, "PATCH", path, map[string]any{"revision": rev + 1, "subtitle": "edited"}))
	if m(d["record"])["status"] != "draft" || m(d["record"])["proposal"] != nil {
		t.Errorf("edit of a proposed record: %v", m(d["record"]))
	}
}

// TestRecordsREST_PublishedIsHumanEdited: a published Record refuses agent edits (403 HUMAN_ONLY, no state change) and accepts the
// operator's.
func TestRecordsREST_PublishedIsHumanEdited(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	id := x.record(t, r1Title)
	resp := x.agent.Do(t, "PATCH", "/api/records/"+id, map[string]any{"revision": 3, "title": "Hijacked"})
	if code, _ := problemOf(t, resp); resp.Status != 403 || code != "HUMAN_ONLY" {
		t.Errorf("agent edit of a published record: %d %s", resp.Status, resp.Body)
	}
	v := m(x.human.OK(t, "PATCH", "/api/records/"+id, map[string]any{"revision": 3, "subtitle": "Operator edit"}))
	if m(v["record"])["status"] != "published" || m(v["record"])["subtitle"] != "Operator edit" {
		t.Errorf("human edit: %v", m(v["record"]))
	}
}

// TestRecordsREST_Changes: paging in both orders with the cursor, the kind filter, the text with titles read now, and the 404.
func TestRecordsREST_Changes(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	id := x.record(t, r1Title)
	path := "/api/records/" + id + "/changes"
	desc := m(x.human.OK(t, "GET", path+"?limit=3", nil))
	items := arr(desc["items"])
	if len(items) != 3 || m(items[0])["seq"] != float64(8) || desc["next_cursor"] != float64(6) {
		t.Fatalf("desc page 1: %v", desc)
	}
	if m(items[1])["text"] != "Slot 3 pinned to Synthetic Take C2" || m(m(items[1])["from"])["title"] != "Synthetic Take C1" {
		t.Errorf("text and titles: %v", items[1])
	}
	var seqs []float64
	for cur := ""; ; {
		pg := m(x.human.OK(t, "GET", path+"?order=asc&limit=3"+cur, nil))
		for _, it := range arr(pg["items"]) {
			seqs = append(seqs, m(it)["seq"].(float64))
		}
		if pg["next_cursor"] == nil {
			break
		}
		cur = fmt.Sprintf("&cursor=%v", pg["next_cursor"])
	}
	if fmt.Sprint(seqs) != "[1 2 3 4 5 6 7 8]" {
		t.Errorf("asc walk: %v", seqs)
	}
	k := m(x.human.OK(t, "GET", path+"?kind=slot_pinned&kind=published", nil))
	if len(arr(k["items"])) != 2 {
		t.Errorf("kind filter: %v", k)
	}
	resp := x.human.Do(t, "GET", "/api/records/nope/changes", nil)
	if code, det := problemOf(t, resp); resp.Status != 404 || code != "NOT_FOUND" || det["record_id"] != "nope" {
		t.Errorf("unknown record: %d %s", resp.Status, resp.Body)
	}
	if bad := x.human.Do(t, "GET", path+"?limit=201", nil); bad.Status != 422 {
		t.Errorf("limit 201: %d", bad.Status)
	}
}

// TestPublish_Rules: a human session publishes (200), a PAT agent is refused with 403 HUMAN_ONLY (a paired device with 401, its cookie does not reach /human/api), no state
// changes and an audit row records the 403; unpublish and decline work; the revision is checked.
func TestPublish_Rules(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	id := x.record(t, r2Title)
	path := "/human/api/records/" + id + "/publish"
	status := func() string {
		var s string
		if err := x.r.Store.R.QueryRow(`SELECT status FROM records WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	dev := x.r.Device(t)
	resp := x.agent.Do(t, "POST", path, map[string]any{"revision": 1, "published": true})
	if code, _ := problemOf(t, resp); resp.Status != 403 || code != "HUMAN_ONLY" {
		t.Errorf("agent: %d %s", resp.Status, resp.Body)
	}
	// a device cookie does not authenticate on /human/api at all (401); the 403 for a device principal is pinned by
	// TestOps_AuthorizationL2 and TestHumanOnly_RefusedForAgentsDevicesStdio
	if resp := dev.Do(t, "POST", path, map[string]any{"revision": 1, "published": true}); resp.Status != 401 {
		t.Errorf("device cookie: %d %s", resp.Status, resp.Body)
	}
	if status() != "draft" {
		t.Fatalf("a refused publish changed the record to %s", status())
	}
	var n int
	if err := x.r.Store.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE op = 'publish_record' AND status = 403`).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit rows with 403 = %d, %v", n, err)
	}
	if resp := x.human.Do(t, "POST", path, map[string]any{"revision": 7, "published": true}); resp.Status != 409 {
		t.Errorf("stale publish: %d %s", resp.Status, resp.Body)
	}
	v := m(x.human.OK(t, "POST", path, map[string]any{"revision": 1, "published": true}))
	rec := m(v["record"])
	if rec["status"] != "published" || m(rec["published"])["by"] == nil || v["liner"] != nil || status() != "published" {
		t.Errorf("published: %v", rec)
	}
	back := m(x.human.OK(t, "POST", path, map[string]any{"revision": int(rec["revision"].(float64)), "published": false}))
	if m(back["record"])["status"] != "draft" || m(back["record"])["published"] != nil {
		t.Errorf("unpublished: %v", m(back["record"]))
	}
}

// TestMuseText_MutatingOpsLinerless: no mutating Records operation carries the liner or any canary Muse byte in its response, in
// its audit row or in the stored idempotency row (D-L2-27); get_record is the one place it appears.
func TestMuseText_MutatingOpsLinerless(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	canaries := []string{museCanary1, museCanary2, "Synthetic Curation Artist"}
	check := func(what string, resp *servertest.Response) {
		t.Helper()
		if resp.Status/100 != 2 {
			t.Fatalf("%s: %d %s", what, resp.Status, resp.Body)
		}
		if m(resp.JSON(t))["liner"] != nil {
			t.Errorf("%s carries a liner", what)
		}
		for _, c := range canaries {
			if strings.Contains(string(resp.Body), c) {
				t.Errorf("%s carries Muse text %q", what, c)
			}
		}
	}
	s4 := x.song(t, "S4") // a Song with a Muse
	resp := x.agent.Do(t, "POST", "/api/records", map[string]any{"title": "Synthetic Linerless", "song_ids": []string{s4}}, "Idempotency-Key", "k-less-1")
	check("create_record", resp)
	id := str(m(m(resp.JSON(t))["record"])["id"])
	path := "/api/records/" + id
	check("update_record", x.agent.Do(t, "PATCH", path, map[string]any{"revision": 1, "subtitle": "x"}))
	check("edit_record_slots", x.agent.Do(t, "POST", path+"/slots", map[string]any{"revision": 2, "ops": []any{map[string]any{"op": "pin_all"}}}))
	check("propose_record", x.agent.Do(t, "POST", path+"/proposal", map[string]any{"revision": 3, "rationale": "ready"}))
	check("publish_record", x.human.Do(t, "POST", "/human/api/records/"+id+"/publish", map[string]any{"revision": 4, "published": true}))
	// the stored idempotency row and the audit rows hold no Muse text either
	rows, err := x.r.Store.R.Query(`SELECT response FROM idempotency`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		for _, c := range canaries {
			if strings.Contains(string(body), c) {
				t.Errorf("an idempotency row holds Muse text %q", c)
			}
		}
	}
	// get_record shows it
	if g := x.agent.Do(t, "GET", path+"?include=liner", nil); !strings.Contains(string(g.Body), museCanary2) {
		t.Errorf("get_record include=liner lacks the Muse line: %.300s", g.Body)
	}
}

// TestRecordView_EnergyBudget: 60 slots with include=energy stay under 90 000 bytes, every curve has at most 120 points, the plan
// gain is added and a take without an analysis has lufs [].
func TestRecordView_EnergyBudget(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{})
	info := lt.FixtureL2Scale(t, r.Store, lt.FixedNow, 60, 3, 0)
	var songs []string
	for i := 1; i <= 60; i++ {
		songs = append(songs, info.Songs[fmt.Sprintf("S%d", i)])
	}
	series := strings.TrimSuffix(strings.Repeat("-14.0,", 300), ",")
	n := 0
	for _, s := range songs {
		var take string
		if err := r.Store.R.QueryRow(`SELECT take_id FROM song_picks WHERE song_id = ?`, s).Scan(&take); err != nil {
			t.Fatal(err)
		}
		n++
		if n == 60 {
			continue // the last slot has no analysis
		}
		if _, err := r.Store.W.Exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES(?, 'loudness', 'mm-loudness/1 ffmpeg test', 'A', ?, '2026-10-06T10:00:00.000Z')`,
			take, fmt.Sprintf(`{"integrated_lufs":%d.5,"lra_lu":5,"lra_low_lufs":-18,"lra_high_lufs":-12,"true_peak_dbtp":-2,"sample_peak_dbfs":-2.5,"short_term_max_lufs":-10,"short_term_1hz":[%s]}`, -14-n%4, series)); err != nil {
			t.Fatal(err)
		}
	}
	human := r.Human(t)
	created := human.Do(t, "POST", "/api/records", map[string]any{"title": "Synthetic Sixty", "song_ids": songs}, "Idempotency-Key", "k-sixty")
	if created.Status != 201 {
		t.Fatalf("create: %d %s", created.Status, created.Body)
	}
	id := str(m(m(created.JSON(t))["record"])["id"])
	resp := human.Do(t, "GET", "/api/records/"+id+"?include=energy", nil)
	if resp.Status != 200 {
		t.Fatalf("get: %d %s", resp.Status, resp.Body)
	}
	if len(resp.Body) >= 90_000 {
		t.Errorf("energy payload = %d bytes, want under 90 000", len(resp.Body))
	}
	slots := arr(m(m(resp.JSON(t))["energy"])["slots"])
	if len(slots) != 60 {
		t.Fatalf("energy slots = %d", len(slots))
	}
	for i, s := range slots[:59] {
		sl := m(s)
		l := arr(sl["lufs"])
		if len(l) == 0 || len(l) > 120 {
			t.Fatalf("slot %d has %d points", i+1, len(l))
		}
		g := sl["gain_db"].(float64)
		if want := math.Round((-14+g)*10) / 10; l[0].(float64) != want {
			t.Errorf("slot %d: lufs[0] = %v, want %v (the -14.0 series plus the %v dB gain)", i+1, l[0], want, g)
		}
	}
	last := m(slots[59])
	if len(arr(last["lufs"])) != 0 || last["gain_db"] != nil {
		t.Errorf("a take without an analysis: %v", last)
	}
}

// TestContext_L2Curation: /context lists the Records whose slot resolves to the take (position, mode) and the manual Crates
// holding the take or its Song; a take in neither has no curation key.
func TestContext_L2Curation(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	ctxOf := func(take string) map[string]any {
		return m(x.agent.OK(t, "GET", "/api/tracks/"+take+"/context", nil))
	}
	c2 := x.id(t, "tracks", "Synthetic Take C2")
	cur := m(ctxOf(c2)["curation"])
	recs, crates := arr(cur["records"]), arr(cur["crates"])
	if len(recs) != 1 || m(recs[0])["title"] != r1Title || m(recs[0])["position"] != float64(3) || m(recs[0])["mode"] != "pinned" || m(recs[0])["status"] != "published" {
		t.Errorf("records of C2: %v", recs)
	}
	// the Take Crate holding C2 and the Song Crate holding its Song S4, newest first (equal times: by id)
	if len(crates) != 2 || m(crates[0])["name"] != "Synthetic To Judge" || m(crates[1])["name"] != "Synthetic Remake Candidates" {
		t.Errorf("crates of C2: %v", crates)
	}
	// C1 is the Pick of S4 but the slot is pinned elsewhere: not the resolved take, so no record; S4 is in a song crate
	c1 := x.id(t, "tracks", "Synthetic Take C1")
	if cur := m(ctxOf(c1)["curation"]); len(arr(cur["records"])) != 0 || len(arr(cur["crates"])) != 1 || m(arr(cur["crates"])[0])["name"] != "Synthetic Remake Candidates" {
		t.Errorf("curation of C1 (a Song crate through its Song): %v", cur)
	}
	if body := ctxOf(lt.TrackLuna); body["curation"] != nil {
		t.Errorf("a take in no record or crate has curation %v", body["curation"])
	} else if _, present := body["curation"]; present {
		t.Error("the curation key must be absent, not null")
	}
	// a hidden Record is not listed
	x.human.OK(t, "PATCH", "/api/records/"+x.record(t, r1Title), map[string]any{"revision": 3, "hidden": true})
	if cur := m(ctxOf(c2)["curation"]); len(arr(cur["records"])) != 0 {
		t.Errorf("hidden record listed: %v", cur)
	}
}

// TestSongView_RecordsCrates: get_song lists the non-hidden Records and the manual Crates holding the Song, at most ten each,
// and a Crate holding a Song retired into this one counts.
func TestSongView_RecordsCrates(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	s1 := x.song(t, "S1")
	get := func(id string) map[string]any { return m(x.agent.OK(t, "GET", "/api/songs/"+id, nil)) }
	v := get(s1)
	if recs := arr(v["records"]); len(recs) != 1 || m(recs[0])["title"] != r2Title || m(recs[0])["mode"] != "floating" || m(recs[0])["status"] != "draft" {
		t.Errorf("records of S1: %v", v["records"])
	}
	if cr := arr(v["crates"]); len(cr) != 1 || m(cr[0])["name"] != "Synthetic Remake Candidates" {
		t.Errorf("crates of S1: %v", v["crates"])
	}
	// the pinned slot of S5 reads "floating" for the Song with a floating slot and "pinned" for a pinned slot
	if recs := arr(get(x.song(t, "S4"))["records"]); len(recs) != 1 || m(recs[0])["mode"] != "pinned" {
		t.Errorf("records of S4: %v", recs)
	}
	// a Song retired into S1 keeps its Crate membership visible on S1
	s7 := x.song(t, "S7")
	if _, err := x.r.Store.W.Exec(`UPDATE songs SET retired_at = '2026-10-06T12:00:00.000Z', retired_into = ? WHERE id = ?`, s1, s7); err != nil {
		t.Fatal(err)
	}
	if _, err := x.r.Store.W.Exec(`INSERT INTO crates(id, name, kind, subject, created_by, created_at, updated_by, updated_at) VALUES('crate-retired', 'Synthetic Retired Holder', 'manual', 'song', 'human:fixture', '2026-10-06T11:00:00.000Z', 'human:fixture', '2026-10-06T11:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := x.r.Store.W.Exec(`INSERT INTO crate_items(id, crate_id, song_id, added_by, added_at) VALUES('item-retired', 'crate-retired', ?, 'human:fixture', '2026-10-06T11:00:00.000Z')`, s7); err != nil {
		t.Fatal(err)
	}
	names := func(v map[string]any) []string {
		var out []string
		for _, c := range arr(v["crates"]) {
			out = append(out, str(m(c)["name"]))
		}
		return out
	}
	if got := names(get(s1)); len(got) != 2 || got[0] != "Synthetic Remake Candidates" && got[1] != "Synthetic Remake Candidates" {
		t.Errorf("crates of S1 after the merge: %v", got)
	}
	// at most ten of each, newest first
	for i := 0; i < 12; i++ {
		x.human.OK(t, "POST", "/api/records", map[string]any{"title": fmt.Sprintf("Synthetic Extra %02d", i), "song_ids": []string{s1}}, "Idempotency-Key", fmt.Sprintf("k-extra-%d", i))
		if _, err := x.r.Store.W.Exec(`INSERT INTO crates(id, name, kind, subject, created_by, created_at, updated_by, updated_at) VALUES(?, ?, 'manual', 'song', 'human:fixture', '2026-10-06T11:00:00.000Z', 'human:fixture', '2026-10-06T11:00:00.000Z')`,
			fmt.Sprintf("crate-x%02d", i), fmt.Sprintf("Synthetic Extra Crate %02d", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := x.r.Store.W.Exec(`INSERT INTO crate_items(id, crate_id, song_id, added_by, added_at) VALUES(?, ?, ?, 'human:fixture', '2026-10-06T11:00:00.000Z')`,
			fmt.Sprintf("item-x%02d", i), fmt.Sprintf("crate-x%02d", i), s1); err != nil {
			t.Fatal(err)
		}
	}
	v = get(s1)
	if len(arr(v["records"])) != 10 || len(arr(v["crates"])) != 10 {
		t.Errorf("records %d crates %d, want 10 and 10", len(arr(v["records"])), len(arr(v["crates"])))
	}
	if first := m(arr(v["records"])[0])["title"]; first != "Synthetic Extra 11" {
		t.Errorf("newest record first: %v", first)
	}
}
