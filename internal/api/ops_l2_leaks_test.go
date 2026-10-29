package api_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

// l2MuseTexts are the synthetic Muse strings FixtureL2 puts on the slot Songs; they stand in for real Muse text.
var l2MuseTexts = []string{museCanary1, museCanary2, "Synthetic Curation Artist 1", "Synthetic Curation Artist 2"}

// l2MuseTextOps are the L2 operations whose responses may carry Muse text: get_record alone (the app liner notes, C127.4).
var l2MuseTextOps = []string{"get_record"}

func crateByName(t *testing.T, x *recRig, name string) string {
	t.Helper()
	var id string
	if err := x.r.Store.R.QueryRow(`SELECT id FROM crates WHERE name = ?`, name).Scan(&id); err != nil {
		t.Fatalf("crate %q: %v", name, err)
	}
	return id
}

// mcpGeneric calls an operation through call_operation and returns the text of the result.
func mcpGeneric(t *testing.T, s *mcp.ClientSession, op string, args map[string]any) string {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "call_operation", Arguments: map[string]any{"operation_id": op, "arguments": args}})
	if err != nil {
		t.Fatalf("call_operation %s: %v", op, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.StructuredContent != nil {
		b.WriteString(fmt.Sprint(res.StructuredContent))
	}
	return b.String()
}

// TestMuseText_L2_OnlyInAllowlistedOps: every readable L2 operation is called over REST (operator and read-scope agent) and over
// MCP with Muses on the fixture Songs; Muse text appears only in get_record (the L1 allowlist gained exactly that op).
func TestMuseText_L2_OnlyInAllowlistedOps(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	reader, _ := x.r.Agent(t, auth.ScopeRead)
	sess := x.r.MCP(t, auth.ScopeRead)
	rid := x.record(t, r1Title)
	crate := crateByName(t, x, "Synthetic Remake Candidates")
	// a tape the human exported through the fake pipeline, so get_tape has an object
	exp := x.human.Do(t, "POST", "/api/records/"+rid+"/tapes", map[string]any{"accept_warnings": true}, "Idempotency-Key", "sweep-tape")
	if exp.Status != 201 {
		t.Fatalf("export: %d %s", exp.Status, exp.Body)
	}
	tapeID := str(m(exp.JSON(t))["id"])
	x.r.Tapes.RunOne(t)

	ids := map[string]string{"/api/records/": rid, "/api/crates/": crate, "/api/tapes/": tapeID}
	called := map[string]bool{}
	for _, op := range api.Operations(x.r.API) {
		if op.Method != "GET" || op.Meta.HumanOnly || !strings.Contains(op.Path, "records") && !strings.Contains(op.Path, "crates") &&
			!strings.Contains(op.Path, "tapes") && !strings.Contains(op.Path, "curation") {
			continue
		}
		path := op.Path
		args := map[string]any{}
		for prefix, id := range ids {
			if strings.HasPrefix(path, prefix) {
				path = strings.Replace(path, "{id}", id, 1)
				args["id"] = id
			}
		}
		if strings.Contains(path, "{") {
			t.Errorf("%s: no object for %s", op.ID, path)
			continue
		}
		bodies := map[string]string{}
		for who, cl := range map[string]string{"human": "", "agent": ""} {
			_ = cl
			c := x.human
			if who == "agent" {
				c = reader
			}
			bodies["rest "+who] = string(c.Do(t, "GET", path, nil).Body)
		}
		bodies["mcp"] = mcpGeneric(t, sess, op.ID, args)
		called[op.ID] = true
		for where, body := range bodies {
			for _, text := range l2MuseTexts {
				if strings.Contains(body, text) && !slices.Contains(l2MuseTextOps, op.ID) {
					t.Errorf("%s over %s leaks Muse text %q: %.200s", op.ID, where, text, body)
				}
			}
		}
	}
	for _, id := range []string{"list_records", "get_record", "get_record_changes", "list_crates", "get_crate", "get_tape", "get_curation_settings"} {
		if !called[id] {
			t.Errorf("%s was not swept", id)
		}
	}
	// the sweep is not vacuous: get_record does carry the Muse line (and only with the liner included)
	for where, body := range map[string]string{
		"rest": string(reader.Do(t, "GET", "/api/records/"+rid+"?include=liner", nil).Body),
		"mcp":  mcpGeneric(t, sess, "get_record", map[string]any{"id": rid, "include": []string{"liner"}}),
	} {
		if !strings.Contains(body, museCanary2) {
			t.Errorf("get_record over %s does not show the Muse line: the allowlist is stale", where)
		}
	}
}

// TestRecordOps_ErrorsCarryIDsOnly: the refusals of the Record, Crate, tape and settings operations, driven with canary text in
// every free-text field of the request (title, subtitle, name, description, rationale; a bad id or filter key is the caller's own
// token and is echoed), answer problem+json that names codes and ids and never a title, a note, a filter key or Muse text.
func TestRecordOps_ErrorsCarryIDsOnly(t *testing.T) {
	t.Parallel()
	x := newRecRig(t)
	canary := "Canary Record Text " + randHex(t)
	rid, draft := x.record(t, r1Title), x.record(t, r2Title)
	crate := crateByName(t, x, "Synthetic Remake Candidates")
	smart := crateByName(t, x, "Synthetic Unheard Reggae")
	s1, s2 := x.song(t, "S1"), x.song(t, "S2")
	missing := "01900000-0000-7000-8000-00000000ffff"
	var rev1, rev2 int
	if err := x.r.Store.R.QueryRow(`SELECT revision FROM records WHERE id = ?`, rid).Scan(&rev1); err != nil {
		t.Fatal(err)
	}
	if err := x.r.Store.R.QueryRow(`SELECT revision FROM records WHERE id = ?`, draft).Scan(&rev2); err != nil {
		t.Fatal(err)
	}
	type call struct {
		name, method, path string
		body               any
		agent              bool
		status             int
		code               string
	}
	calls := []call{
		{"create validation", "POST", "/api/records", map[string]any{"title": "", "subtitle": canary}, true, 422, "VALIDATION_FAILED"},
		{"create unknown song", "POST", "/api/records", map[string]any{"title": canary, "song_ids": []string{missing}}, true, 422, "UNKNOWN_SONG"},
		{"create duplicate song", "POST", "/api/records", map[string]any{"title": canary, "song_ids": []string{s1, s1}}, true, 422, "VALIDATION_FAILED"},
		{"get unknown", "GET", "/api/records/" + missing, nil, true, 404, "NOT_FOUND"},
		{"update stale", "PATCH", "/api/records/" + draft, map[string]any{"revision": rev2 + 5, "title": canary}, true, 409, "STALE_REVISION"},
		{"update published as agent", "PATCH", "/api/records/" + rid, map[string]any{"revision": rev1, "title": canary}, true, 403, "HUMAN_ONLY"},
		{"update unknown", "PATCH", "/api/records/" + missing, map[string]any{"revision": 1, "title": canary}, true, 404, "NOT_FOUND"},
		{"slots unknown slot", "POST", "/api/records/" + draft + "/slots", map[string]any{"revision": rev2, "ops": []any{map[string]any{"op": "remove", "slot_id": "no-such-slot"}}}, true, 422, "UNKNOWN_SLOT"},
		{"slots unknown song", "POST", "/api/records/" + draft + "/slots", map[string]any{"revision": rev2, "ops": []any{map[string]any{"op": "add", "song_id": missing}}}, true, 422, "UNKNOWN_SONG"},
		{"slots song twice", "POST", "/api/records/" + draft + "/slots", map[string]any{"revision": rev2, "ops": []any{map[string]any{"op": "add", "song_id": s1}}}, true, 409, "SONG_ALREADY_IN_RECORD"},
		{"slots order mismatch", "POST", "/api/records/" + draft + "/slots", map[string]any{"revision": rev2, "ops": []any{map[string]any{"op": "reorder", "slot_ids": []string{"no-such-slot"}}}}, true, 422, "SLOT_ORDER_MISMATCH"},
		{"slots stale", "POST", "/api/records/" + draft + "/slots", map[string]any{"revision": rev2 + 9, "ops": []any{map[string]any{"op": "pin_all"}}}, true, 409, "STALE_REVISION"},
		{"slots on published as agent", "POST", "/api/records/" + rid + "/slots", map[string]any{"revision": rev1, "ops": []any{map[string]any{"op": "pin_all"}}}, true, 403, "HUMAN_ONLY"},
		{"propose published", "POST", "/api/records/" + rid + "/proposal", map[string]any{"revision": rev1, "rationale": canary}, true, 409, "INVALID_RECORD_STATE"},
		{"propose stale", "POST", "/api/records/" + draft + "/proposal", map[string]any{"revision": rev2 + 9, "rationale": canary}, true, 409, "STALE_REVISION"},
		{"publish as agent", "POST", "/human/api/records/" + draft + "/publish", map[string]any{"revision": rev2, "published": true}, true, 403, "HUMAN_ONLY"},
		{"publish stale", "POST", "/human/api/records/" + draft + "/publish", map[string]any{"revision": rev2 + 9, "published": true}, false, 409, "STALE_REVISION"},
		{"changes unknown", "GET", "/api/records/" + missing + "/changes", nil, true, 404, "NOT_FOUND"},
		{"tape of a draft", "POST", "/api/records/" + draft + "/tapes", map[string]any{}, true, 409, "RECORD_NOT_PUBLISHED"},
		{"tape needs review", "POST", "/api/records/" + rid + "/tapes", map[string]any{}, true, 409, "TAPE_NEEDS_REVIEW"},
		{"tape accept as agent", "POST", "/api/records/" + rid + "/tapes", map[string]any{"accept_warnings": true}, true, 403, "HUMAN_ONLY"},
		{"tape stale", "POST", "/api/records/" + rid + "/tapes", map[string]any{"revision": rev1 + 9}, false, 409, "STALE_REVISION"},
		{"tape unknown record", "POST", "/api/records/" + missing + "/tapes", map[string]any{}, true, 404, "NOT_FOUND"},
		{"tape unknown", "GET", "/api/tapes/" + missing, nil, true, 404, "NOT_FOUND"},
		{"crate validation", "POST", "/api/crates", map[string]any{"name": "", "kind": "manual", "description": canary}, true, 422, "VALIDATION_FAILED"},
		{"crate unknown filter key", "POST", "/api/crates", map[string]any{"name": canary, "kind": "smart", "filter": map[string]any{"no_such_key": true}}, true, 422, "UNKNOWN_FILTER_KEY"},
		{"crate invalid filter", "POST", "/api/crates", map[string]any{"name": canary, "kind": "smart", "filter": map[string]any{"min_duration_s": "no-number"}}, true, 422, "INVALID_FILTER"},
		{"crate get unknown", "GET", "/api/crates/" + missing, nil, true, 404, "NOT_FOUND"},
		{"crate update stale", "PATCH", "/api/crates/" + crate, map[string]any{"revision": 99, "name": canary}, true, 409, "STALE_REVISION"},
		{"crate items on smart", "POST", "/api/crates/" + smart + "/items", map[string]any{"add": []any{map[string]any{"song_id": s2}}}, true, 409, "CRATE_IS_SMART"},
		{"crate items unknown song", "POST", "/api/crates/" + crate + "/items", map[string]any{"add": []any{map[string]any{"song_id": missing}}}, true, 422, "UNKNOWN_SONG"},
		{"crate items unknown crate", "POST", "/api/crates/" + missing + "/items", map[string]any{"add": []any{map[string]any{"song_id": s2}}}, true, 404, "NOT_FOUND"},
		{"settings as agent", "PUT", "/human/api/curation/settings", map[string]any{"export_muse_names": true}, true, 403, "HUMAN_ONLY"},
		{"settings empty patch", "PUT", "/human/api/curation/settings", map[string]any{}, false, 422, "VALIDATION_FAILED"},
	}
	forbidden := append(slices.Clone(l2MuseTexts), canary, r1Title, r2Title, "Synthetic Song S", "Synthetic Take", "Synthetic Remake Candidates", "Synthetic Unheard Reggae")
	codes := map[string]bool{}
	for _, c := range calls {
		cl := x.human
		if c.agent {
			cl = x.agent
		}
		var extra []string
		if c.method == "POST" && c.path == "/api/records" || c.method == "POST" && c.path == "/api/crates" {
			extra = []string{"Idempotency-Key", "err-" + strings.ReplaceAll(c.name, " ", "-")}
		}
		if strings.HasSuffix(c.path, "/tapes") && c.method == "POST" {
			extra = []string{"Idempotency-Key", "err-" + strings.ReplaceAll(c.name, " ", "-")}
		}
		resp := cl.Do(t, c.method, c.path, c.body, extra...)
		code, _ := problemOf(t, resp)
		if resp.Status != c.status || code != c.code {
			t.Errorf("%s: %d %s, want %d %s: %s", c.name, resp.Status, code, c.status, c.code, resp.Body)
			continue
		}
		codes[code] = true
		for _, f := range forbidden {
			if strings.Contains(string(resp.Body), f) {
				t.Errorf("%s: the problem body carries %q: %s", c.name, f, resp.Body)
			}
		}
	}
	if len(codes) < 14 {
		t.Errorf("only %d distinct codes driven: %v", len(codes), codes)
	}
	// the same refusals over MCP: the tool error text is the same problem body
	sess := x.r.MCP(t, auth.ScopeRead, auth.ScopeWrite)
	for op, args := range map[string]map[string]any{
		"update_record":      {"id": rid, "revision": rev1, "title": canary},
		"get_record":         {"id": missing},
		"export_record_tape": {"id": rid},
		"edit_crate_items":   {"id": smart, "add": []any{map[string]any{"song_id": s2}}},
		"create_crate":       {"name": canary, "kind": "smart", "filter": map[string]any{"no_such_key": true}},
	} {
		body := mcpGeneric(t, sess, op, args)
		for _, f := range forbidden {
			if strings.Contains(body, f) {
				t.Errorf("%s over MCP: the error carries %q: %.300s", op, f, body)
			}
		}
	}
}
