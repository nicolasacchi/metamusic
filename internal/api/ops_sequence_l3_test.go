package api_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// seqRig is a library with the T Songs and their real features (the L3 fixture settled with the features step).
func seqRig(t *testing.T) *servertest.Rig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, L3: true, RealFeatures: true})
	r.Settle(t)
	return r
}

func songByTitle(t *testing.T, r *servertest.Rig, label string) string {
	t.Helper()
	var id string
	if err := r.Store.R.QueryRow(`SELECT id FROM songs WHERE title = ?`, "Synthetic Song "+label).Scan(&id); err != nil {
		t.Fatalf("song %s: %v", label, err)
	}
	return id
}

var shuffledT = []string{"T4", "T6", "T1", "T5", "T3", "T2"}

func sequenceQuery(t *testing.T, r *servertest.Rig, labels []string, extra ...string) string {
	t.Helper()
	q := url.Values{}
	for _, l := range labels {
		q.Add("song_id", songByTitle(t, r, l))
	}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return "?" + q.Encode()
}

type sugg struct {
	Def        string   `json:"def"`
	Strategy   string   `json:"strategy"`
	InputOrder []string `json:"input_order"`
	Order      []string `json:"order"`
	Steps      []struct {
		Position int    `json:"position"`
		SongID   string `json:"song_id"`
		Title    string `json:"title"`
		Text     string `json:"text"`
		Reasons  []struct {
			Code   string         `json:"code"`
			Params map[string]int `json:"params"`
		} `json:"reasons"`
		Missing []string `json:"missing"`
	} `json:"steps"`
	Cost struct {
		Input, Output  string
		ImprovementPct *string `json:"improvement_pct"`
	} `json:"cost"`
	Exact    bool             `json:"exact"`
	Warnings []map[string]any `json:"warnings"`
	Digest   string           `json:"digest"`
	Pending  bool             `json:"pending"`
}

func problemBody(t *testing.T, body []byte) (string, map[string]any) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	d, _ := m["details"].(map[string]any)
	code, _ := m["code"].(string)
	return code, d
}

// TestSuggestSequence_REST: the shapes of C138.1 over the T Songs: titles injected, integer reason params, costs as the
// worked values, the error codes, and a crate (manual and smart) as the source.
func TestSuggestSequence_REST(t *testing.T) {
	t.Parallel()
	r := seqRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead)
	resp := agent.Do(t, "GET", "/api/sequence"+sequenceQuery(t, r, shuffledT), nil)
	if resp.Status != 200 {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
	var s sugg
	if err := json.Unmarshal(resp.Body, &s); err != nil {
		t.Fatal(err)
	}
	if s.Def != "mm.sequence/1" || s.Strategy != "arc" || !s.Exact || s.Cost.Input != "8.9847" || s.Cost.Output != "5.5198" || s.Cost.ImprovementPct == nil || *s.Cost.ImprovementPct != "38.6" {
		t.Errorf("suggestion = %+v", s)
	}
	if len(s.Steps) != 6 || len(s.Digest) != 64 || s.Pending {
		t.Errorf("steps %d digest %q pending %v", len(s.Steps), s.Digest, s.Pending)
	}
	for i, st := range s.Steps {
		if st.Title != fmt.Sprintf("Synthetic Song T%d", i+1) || st.Position != i+1 || st.Text == "" || len(st.Reasons) == 0 || st.Missing == nil {
			t.Errorf("step %d = %+v", i+1, st)
		}
	}
	if s.Steps[0].Reasons[0].Code != "opener" || s.Steps[0].Reasons[0].Params["bpm"] != 9600 {
		t.Errorf("opener = %+v", s.Steps[0].Reasons[0])
	}
	if s.Warnings == nil || s.InputOrder[0] != songByTitle(t, r, "T4") {
		t.Errorf("warnings %v input %v", s.Warnings, s.InputOrder)
	}

	// endpoints and strategies
	var e sugg
	resp = agent.Do(t, "GET", "/api/sequence"+sequenceQuery(t, r, shuffledT, "strategy", "steady", "first_song_id", songByTitle(t, r, "T5")), nil)
	if resp.Status != 200 || json.Unmarshal(resp.Body, &e) != nil || e.Order[0] != songByTitle(t, r, "T5") || e.Strategy != "steady" {
		t.Errorf("endpoints: %d %s", resp.Status, resp.Body)
	}

	for name, c := range map[string]struct {
		query  string
		status int
		code   string
	}{
		"no source":        {"", 422, "SEQUENCE_SOURCE"},
		"two sources":      {sequenceQuery(t, r, shuffledT[:2], "record_id", "x"), 422, "SEQUENCE_SOURCE"},
		"unknown record":   {"?record_id=nope", 404, "NOT_FOUND"},
		"one song":         {sequenceQuery(t, r, shuffledT[:1]), 422, "SEQUENCE_TOO_SMALL"},
		"unknown strategy": {sequenceQuery(t, r, shuffledT, "strategy", "zigzag"), 422, "UNKNOWN_STRATEGY"},
		"unknown song":     {"?song_id=ghost&song_id=ghost2", 422, "UNKNOWN_SONG"},
		"endpoint not in":  {sequenceQuery(t, r, shuffledT[:3], "last_song_id", songByTitle(t, r, "T8")), 422, "SEQUENCE_ENDPOINT_NOT_IN_SET"},
		"unknown crate":    {"?crate_id=ghost", 404, ""},
	} {
		resp := agent.Do(t, "GET", "/api/sequence"+c.query, nil)
		code, _ := problemBody(t, resp.Body)
		if resp.Status != c.status || (c.code != "" && code != c.code) {
			t.Errorf("%s: %d %s, want %d %s", name, resp.Status, resp.Body, c.status, c.code)
		}
	}
	// 61 ids fail the schema before the service
	var many []string
	for i := 0; i < 61; i++ {
		many = append(many, fmt.Sprintf("song_id=s%d", i))
	}
	if resp := agent.Do(t, "GET", "/api/sequence?"+strings.Join(many, "&"), nil); resp.Status != 422 {
		t.Errorf("61 song ids: %d", resp.Status)
	}

	// Crates: a manual one by membership, a smart one by its stored filter
	human := r.Human(t)
	var items []map[string]string
	for _, l := range []string{"T1", "T2", "T3", "T4"} {
		items = append(items, map[string]string{"song_id": songByTitle(t, r, l)})
	}
	mc := human.Do(t, "POST", "/api/crates", map[string]any{"name": "Synthetic Crate Seq", "kind": "manual", "items": items}, "Idempotency-Key", "crate-seq-1")
	if mc.Status != 201 {
		t.Fatalf("create crate: %d %s", mc.Status, mc.Body)
	}
	var crate struct {
		Crate struct{ ID string } `json:"crate"`
	}
	_ = json.Unmarshal(mc.Body, &crate)
	var m sugg
	resp = agent.Do(t, "GET", "/api/sequence?crate_id="+crate.Crate.ID, nil)
	if resp.Status != 200 || json.Unmarshal(resp.Body, &m) != nil || len(m.Order) != 4 {
		t.Fatalf("manual crate: %d %s", resp.Status, resp.Body)
	}
	var muse string
	if err := r.Store.R.QueryRow(`SELECT muse_id FROM songs WHERE title = 'Synthetic Song T1'`).Scan(&muse); err != nil {
		t.Fatal(err)
	}
	sc := human.Do(t, "POST", "/api/crates", map[string]any{"name": "Synthetic Smart Seq", "kind": "smart", "filter": map[string]any{"muse_id": muse}}, "Idempotency-Key", "crate-seq-2")
	if sc.Status != 201 {
		t.Fatalf("create smart crate: %d %s", sc.Status, sc.Body)
	}
	_ = json.Unmarshal(sc.Body, &crate)
	var sm sugg
	resp = agent.Do(t, "GET", "/api/sequence?crate_id="+crate.Crate.ID, nil)
	if resp.Status != 200 || json.Unmarshal(resp.Body, &sm) != nil || len(sm.Order) != 8 {
		t.Fatalf("smart crate: %d %s", resp.Status, resp.Body)
	}
	// more than 60 matches: a smart Crate of every Song is refused with the count
	all := human.Do(t, "POST", "/api/crates", map[string]any{"name": "Synthetic Smart All", "kind": "smart", "filter": map[string]any{"q": "Synthetic"}}, "Idempotency-Key", "crate-seq-3")
	if all.Status != 201 {
		t.Fatalf("create smart crate: %d %s", all.Status, all.Body)
	}
	_ = json.Unmarshal(all.Body, &crate)
	if resp := agent.Do(t, "GET", "/api/sequence?crate_id="+crate.Crate.ID, nil); resp.Status != 200 {
		code, d := problemBody(t, resp.Body)
		if resp.Status != 422 || code != "TOO_MANY_SONGS" || d["count"] == nil {
			t.Errorf("a big crate: %d %s", resp.Status, resp.Body)
		}
	}
}

type recView struct {
	Record struct {
		ID       string `json:"id"`
		Revision int    `json:"revision"`
		Status   string `json:"status"`
	} `json:"record"`
	Slots []struct {
		ID   string `json:"id"`
		Song struct {
			Title string `json:"title"`
		} `json:"song"`
	} `json:"slots"`
	Sequence *struct {
		ID       string `json:"id"`
		Strategy string `json:"strategy"`
		Stale    bool   `json:"stale"`
		Exact    bool   `json:"exact"`
		Actor    struct {
			ID string `json:"id"`
		} `json:"actor"`
		Cost  struct{ Input, Output string }
		Steps []struct {
			Title     string `json:"title"`
			TakeTitle string `json:"take_title"`
			Text      string `json:"text"`
		} `json:"steps"`
		Warnings []any  `json:"warnings"`
		Digest   string `json:"digest"`
	} `json:"sequence"`
	Changes []struct {
		Kind     string `json:"kind"`
		Text     string `json:"text"`
		Sequence *struct {
			ID       string `json:"id"`
			Strategy string `json:"strategy"`
		} `json:"sequence"`
	} `json:"changes"`
}

func (v recView) titles() []string {
	var out []string
	for _, s := range v.Slots {
		out = append(out, strings.TrimPrefix(s.Song.Title, "Synthetic Song "))
	}
	return out
}

func newTRecord(t *testing.T, r *servertest.Rig, c *servertest.Client, key string) recView {
	t.Helper()
	var ids []string
	for _, l := range shuffledT {
		ids = append(ids, songByTitle(t, r, l))
	}
	resp := c.Do(t, "POST", "/api/records", map[string]any{"title": "Synthetic Sequenced Record", "song_ids": ids}, "Idempotency-Key", key)
	if resp.Status != 201 && resp.Status != 200 {
		t.Fatalf("create_record: %d %s", resp.Status, resp.Body)
	}
	var v recView
	if err := json.Unmarshal(resp.Body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestEditRecordSlots_Sequence_REST: the sequence slot op and propose_record.sequence over REST, and include=sequence.
func TestEditRecordSlots_Sequence_REST(t *testing.T) {
	t.Parallel()
	r := seqRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	rec := newTRecord(t, r, agent, "rec-1")
	if got := rec.titles(); !slices.Equal(got, shuffledT) {
		t.Fatalf("created order = %v", got)
	}
	// forbidden extra fields and an unknown strategy are 422 and write nothing
	bad := agent.Do(t, "POST", "/api/records/"+rec.Record.ID+"/slots", map[string]any{"revision": rec.Record.Revision, "ops": []map[string]any{{"op": "sequence", "slot_id": rec.Slots[0].ID}}})
	if code, d := problemBody(t, bad.Body); bad.Status != 422 || code != "VALIDATION_FAILED" || d["op"] != float64(0) {
		t.Errorf("slot_id on sequence: %d %s", bad.Status, bad.Body)
	}
	bad = agent.Do(t, "POST", "/api/records/"+rec.Record.ID+"/slots", map[string]any{"revision": rec.Record.Revision, "ops": []map[string]any{{"op": "move", "slot_id": rec.Slots[0].ID, "position": 2, "strategy": "arc"}}})
	if code, _ := problemBody(t, bad.Body); bad.Status != 422 || code != "VALIDATION_FAILED" {
		t.Errorf("strategy on move: %d %s", bad.Status, bad.Body)
	}
	bad = agent.Do(t, "POST", "/api/records/"+rec.Record.ID+"/slots", map[string]any{"revision": rec.Record.Revision, "ops": []map[string]any{{"op": "sequence", "strategy": "zigzag"}}})
	if code, _ := problemBody(t, bad.Body); bad.Status != 422 || code != "UNKNOWN_STRATEGY" {
		t.Errorf("unknown strategy: %d %s", bad.Status, bad.Body)
	}
	stale := agent.Do(t, "POST", "/api/records/"+rec.Record.ID+"/slots", map[string]any{"revision": rec.Record.Revision + 3, "ops": []map[string]any{{"op": "sequence"}}})
	if code, _ := problemBody(t, stale.Body); stale.Status != 409 || code != "STALE_REVISION" {
		t.Errorf("stale: %d %s", stale.Status, stale.Body)
	}

	// the preview equals what the op stores
	pre := agent.Do(t, "GET", "/api/sequence?record_id="+rec.Record.ID, nil)
	var preview sugg
	if pre.Status != 200 || json.Unmarshal(pre.Body, &preview) != nil {
		t.Fatalf("preview: %d %s", pre.Status, pre.Body)
	}
	resp := agent.Do(t, "POST", "/api/records/"+rec.Record.ID+"/slots", map[string]any{"revision": rec.Record.Revision, "ops": []map[string]any{{"op": "sequence", "strategy": "arc"}}})
	if resp.Status != 200 {
		t.Fatalf("sequence: %d %s", resp.Status, resp.Body)
	}
	var after recView
	_ = json.Unmarshal(resp.Body, &after)
	if got := after.titles(); !slices.Equal(got, []string{"T1", "T2", "T3", "T4", "T5", "T6"}) || after.Record.Revision != rec.Record.Revision+1 || after.Sequence != nil {
		t.Errorf("after = %v rev %d sequence %v (a write answers without include=sequence)", got, after.Record.Revision, after.Sequence)
	}
	get := agent.Do(t, "GET", "/api/records/"+rec.Record.ID+"?include=sequence&include=changes", nil)
	var v recView
	if get.Status != 200 || json.Unmarshal(get.Body, &v) != nil || v.Sequence == nil {
		t.Fatalf("get_record: %d %s", get.Status, get.Body)
	}
	sq := v.Sequence
	if sq.Stale || sq.Strategy != "arc" || !sq.Exact || sq.Digest != preview.Digest || sq.Cost.Input != preview.Cost.Input || sq.Cost.Output != preview.Cost.Output || sq.Actor.ID == "" {
		t.Errorf("sequence = %+v vs preview %+v", sq, preview.Cost)
	}
	if len(sq.Steps) != 6 || sq.Steps[0].Title != "Synthetic Song T1" || sq.Steps[0].TakeTitle == "" || sq.Steps[0].Text == "" || sq.Warnings == nil {
		t.Errorf("steps = %+v", sq.Steps)
	}
	seen := 0
	for _, c := range v.Changes {
		if c.Sequence != nil {
			seen++
			if c.Kind != "slot_moved" || c.Sequence.ID != sq.ID || c.Sequence.Strategy != "arc" || !strings.HasSuffix(c.Text, "(sequenced: arc)") {
				t.Errorf("change = %+v", c)
			}
		}
	}
	if seen == 0 {
		t.Error("no changelog row names the run")
	}
	// the default get_record does not carry it
	plain := agent.Do(t, "GET", "/api/records/"+rec.Record.ID, nil)
	var pv recView
	_ = json.Unmarshal(plain.Body, &pv)
	if plain.Status != 200 || pv.Sequence != nil {
		t.Errorf("default include carries the sequence: %s", plain.Body)
	}
	// a manual reorder makes it stale
	swap := []string{v.Slots[1].ID, v.Slots[0].ID}
	for _, sl := range v.Slots[2:] {
		swap = append(swap, sl.ID)
	}
	re := agent.Do(t, "POST", "/api/records/"+rec.Record.ID+"/slots", map[string]any{"revision": after.Record.Revision, "ops": []map[string]any{{"op": "reorder", "slot_ids": swap}}})
	if re.Status != 200 {
		t.Fatalf("reorder: %d %s", re.Status, re.Body)
	}
	get = agent.Do(t, "GET", "/api/records/"+rec.Record.ID+"?include=sequence", nil)
	var v2 recView
	_ = json.Unmarshal(get.Body, &v2)
	if v2.Sequence == nil || !v2.Sequence.Stale {
		t.Errorf("a manual reorder must make the run stale: %s", get.Body)
	}

	// propose_record with a sequence body: sequenced and proposed in one call
	rec2 := newTRecord(t, r, agent, "rec-2")
	pr := agent.Do(t, "POST", "/api/records/"+rec2.Record.ID+"/proposal", map[string]any{"revision": rec2.Record.Revision, "rationale": "ready to hear", "sequence": map[string]any{"strategy": "ramp_up"}})
	if pr.Status != 200 {
		t.Fatalf("propose: %d %s", pr.Status, pr.Body)
	}
	var pv2 recView
	_ = json.Unmarshal(pr.Body, &pv2)
	if pv2.Record.Status != "proposed" || pv2.Record.Revision != rec2.Record.Revision+1 {
		t.Errorf("record = %+v", pv2.Record)
	}
	g2 := agent.Do(t, "GET", "/api/records/"+rec2.Record.ID+"?include=sequence&include=changes", nil)
	var gv recView
	_ = json.Unmarshal(g2.Body, &gv)
	if gv.Sequence == nil || gv.Sequence.Strategy != "ramp_up" || gv.Sequence.Stale {
		t.Fatalf("sequence = %s", g2.Body)
	}
	if last := gv.Changes[0]; last.Kind != "proposed" || last.Sequence == nil || last.Sequence.ID != gv.Sequence.ID || last.Text != "Proposed for publishing (sequenced: ramp_up)" {
		t.Errorf("proposed row = %+v", last)
	}
	// without a sequence body the proposal is the L2 one
	rec3 := newTRecord(t, r, agent, "rec-3")
	p3 := agent.Do(t, "POST", "/api/records/"+rec3.Record.ID+"/proposal", map[string]any{"revision": rec3.Record.Revision, "rationale": "ready"})
	g3 := agent.Do(t, "GET", "/api/records/"+rec3.Record.ID+"?include=sequence&include=changes", nil)
	var v3 recView
	_ = json.Unmarshal(g3.Body, &v3)
	if p3.Status != 200 || v3.Sequence != nil || v3.Changes[0].Text != "Proposed for publishing" || v3.Changes[0].Sequence != nil {
		t.Errorf("plain proposal: %d %s", p3.Status, g3.Body)
	}
}

// TestSequence_NoMuseText: the Muse titles of the fixture appear in no sequencing response.
func TestSequence_NoMuseText(t *testing.T) {
	t.Parallel()
	r := seqRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	rec := newTRecord(t, r, agent, "rec-nm")
	var bodies []string
	bodies = append(bodies, string(agent.Do(t, "GET", "/api/sequence?record_id="+rec.Record.ID, nil).Body))
	bodies = append(bodies, string(agent.Do(t, "POST", "/api/records/"+rec.Record.ID+"/slots", map[string]any{"revision": rec.Record.Revision, "ops": []map[string]any{{"op": "sequence"}}}).Body))
	bodies = append(bodies, string(agent.Do(t, "GET", "/api/records/"+rec.Record.ID+"?include=sequence&include=changes", nil).Body))
	rows, err := r.Store.R.Query(`SELECT explanation_json || order_before_json || order_after_json FROM record_sequences`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		bodies = append(bodies, s)
	}
	for i, b := range bodies {
		for _, canary := range []string{"Synthetic Agent Muse", "Synthetic Agent Artist", "Synthetic Curation Muse", "Synthetic Curation Artist"} {
			if strings.Contains(b, canary) {
				t.Errorf("response %d carries %q", i, canary)
			}
		}
	}
}
