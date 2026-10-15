package mcpgen_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

var update = flag.Bool("update", false, "rewrite golden files")

type rig struct {
	url    string
	tokens *auth.TokenStore
	api    huma.API
}

// newRig starts a servertest rig holding the M0+M1 fixture library under the
// fixed clock, so every face and every rig of a mutating op agree exactly.
func newRig(t *testing.T) *rig {
	t.Helper()
	sr := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	return &rig{url: sr.URL, tokens: sr.Services.Tokens, api: sr.API}
}

type bearerRT struct{ tok string }

func (b bearerRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(req)
}

func (r *rig) token(t *testing.T, name string, scopes ...auth.Scope) string {
	t.Helper()
	tok, _, err := r.tokens.Create(context.Background(), name, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (r *rig) client(t *testing.T, tok string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   r.url + "/mcp",
		HTTPClient: &http.Client{Transport: bearerRT{tok}},
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func (r *rig) rest(t *testing.T, tok, method, path string, body any) any {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, r.url+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	var v any
	if len(b) > 0 {
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

func structured(t *testing.T, res *mcp.CallToolResult) any {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

// ParityCase is one step of the parity script: an operation, its tool
// arguments (built per rig so cases can refer to rig-specific ids), and the
// response keys that legitimately differ between faces (token ids, ...).
type ParityCase struct {
	Op string
	// Args builds the tool arguments for one rig. prev is the raw (unmasked) response of the previous case on
	// the same rig, so a create_* case reads the confirmation token of its estimate_* case (C86.1); rigs
	// answer different tokens only when their call counts differ, and tokens stay masked in the compared output.
	Args        func(r *servertest.Rig, prev map[string]any) map[string]any
	Masks       []string
	SkipGeneric bool   // do not repeat the call through call_operation (non-replayable mutations)
	Last        bool   // runs after every other case (ops that end a principal)
	Status      int    // expected HTTP status (0 => any 2xx); a case that expects a failure names it, the same on every face
	Variant     string // distinguishes several cases of one operation (e.g. inline and not)
}

func fixedArgs(m map[string]any) func(*servertest.Rig, map[string]any) map[string]any {
	return func(*servertest.Rig, map[string]any) map[string]any { return m }
}

// parityCases holds a case for every tool or generic operation that is not a
// parameterless GET (needsFixture); parameterless GETs get an implicit case.
// An operation without a case fails TestParityEveryOperation, so new ops
// cannot escape parity unnoticed. Each task adds its cases here.
var parityCases = []ParityCase{
	{Op: "search_tracks", Args: fixedArgs(map[string]any{"q": "luna", "limit": 10})},
	{Op: "get_track", Args: fixedArgs(map[string]any{"id": lt.TrackLuna})},
	{Op: "get_track_peaks", Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "level": "detail"})},
	{Op: "get_media_links", Args: fixedArgs(map[string]any{"id": lt.TrackLuna})},
	{Op: "list_drafts", Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "get_draft", Args: fixedArgs(map[string]any{"id": lt.Draft1})},
	{Op: "list_import_runs", Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "list_device_profiles", Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "get_import_run", Args: fixedArgs(map[string]any{"id": lt.RunID, "section": "warnings"})},
	{Op: "put_device_profile", Args: fixedArgs(map[string]any{
		"device_id": "parity-device-1", "label": "Parity", "user_agent": "UA/1", "quality": "high", "quality_source": "probe",
		"probe_result": "failed", "flac_can_play": "maybe", "flac_decode_ok": false, "probe_version": 1,
	})},
}

// m2bCases are the M2b operations over the real duels, Ears and clip services (M2b-B3): all three
// faces must answer the same. Ids come from duelstest and librarytest (fixed), so every rig agrees.
var m2bCases = []ParityCase{
	{Op: "get_clip", Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "from_s": 0.0, "to_s": 12.0, "match_lufs": -20.0})},
	{Op: "create_duel", Variant: "explicit", Args: fixedArgs(map[string]any{"a_track_id": duelstest.TrackAlpha, "b_track_id": duelstest.TrackBeta, "blind": false, "match_loudness": false}), Masks: []string{"id"}},
	{Op: "create_duel", Variant: "defaults", Args: fixedArgs(map[string]any{"a_track_id": duelstest.TrackAlpha, "b_track_id": duelstest.TrackBeta}), Masks: []string{"id"}},
	{Op: "next_duel", Args: fixedArgs(map[string]any{"exclude": []string{duelstest.DuelDecided}})},
	{Op: "get_duel", Args: fixedArgs(map[string]any{"id": duelstest.DuelOpen})},
	{Op: "list_duels", Args: fixedArgs(map[string]any{"state": []string{"open", "decided"}, "mode": "blind", "limit": 10}), Masks: []string{"id"}},
	{Op: "submit_duel", Args: fixedArgs(map[string]any{"id": duelstest.DuelOpen, "verdict": "a", "note": "parity", "latency_ms": 4000, "switches": 3}), SkipGeneric: true, Masks: []string{"id"}},
	{Op: "get_ladder", Args: fixedArgs(map[string]any{"scope": "lineage", "root_track_id": duelstest.TrackAlpha})},
	{Op: "get_track_analysis", Args: fixedArgs(map[string]any{"id": earstest.ClipTrack})},
	{Op: "get_track_spectrogram", Variant: "inline", Args: fixedArgs(map[string]any{"id": earstest.ClipTrack})},
	{Op: "get_track_spectrogram", Variant: "link", Args: fixedArgs(map[string]any{"id": earstest.ClipTrack, "inline": false})},
	{Op: "compare_tracks", Args: fixedArgs(map[string]any{"a": duelstest.TrackAlpha, "b": duelstest.TrackBeta})},
}

// judgmentCases are the M2a-B4b cases. Token ids, transports and the ids drawn
// from the rig's id sequence (which the audit rows of the extra call_operation
// replays advance) legitimately differ between faces.
var judgmentCases = []ParityCase{
	{Op: "rate_track", Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "stars": 4, "verdict": "parity"}), Masks: []string{"by"}},
	{Op: "update_track", Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "status": "final"}), Masks: []string{"status_set_by", "by"}},
	{Op: "set_tags", Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "add": []string{"Ballad", "late night"}})},
	{Op: "list_tags"},
	{Op: "update_comment", Args: fixedArgs(map[string]any{"id": lt.CommentLunaFix, "state": "resolved"})},
	{Op: "list_comments", Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "limit": 10, "order": "time"})},
	{Op: "add_comment", Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "body": "second chorus drags", "kind": "fix", "at_second": 5.04}), Masks: []string{"id", "author", "source"}},
}

// draftCases are the M2a-B5 cases. Ids drawn from the rig's id sequence, the principal keys and the
// timestamps of rows written by the call_operation replays legitimately differ between faces.
var draftCases = []ParityCase{
	{Op: "lint_recipe", Args: fixedArgs(map[string]any{"items": []map[string]any{{"request": map[string]any{"id": "x", "style": "pop", "lyrics": "[verse]\nla"}}}})},
	{Op: "create_draft", Args: fixedArgs(map[string]any{"request": map[string]any{"id": "pc_x", "style": "english, pop", "lyrics": "[Verse]\nla la\nla\nla\nla\n[Chorus]\nla\nla\nla\nla", "cot": "full", "seed": 4}}),
		Masks: []string{"id", "created_by", "updated_by", "created_at", "updated_at", "hash"}},
	{Op: "update_draft", Args: fixedArgs(map[string]any{"id": lt.Draft1, "revision": 1, "status": "archived"}), SkipGeneric: true,
		Masks: []string{"updated_by", "updated_at"}},
	{Op: "upload_batch_jsonl", Args: fixedArgs(map[string]any{"jsonl": `{"id":"pc_u1","style":"english","lyrics":"[Verse]\nla","cot":"full","seed":1}` + "\n", "collection": "parity"}),
		Masks: []string{"id", "drafts"}},
}

// dataCases are the M2a-B6 cases (playlists and exports). Ids come from the rig's id sequence, which the
// audit rows of the extra call_operation replays advance, and agent token ids differ per rig; the media
// links and shas are equal at the fixed clock (content addressing).
var dataCases = []ParityCase{
	{Op: "list_playlists", Args: fixedArgs(map[string]any{"include_hidden": true})},
	{Op: "get_playlist", Args: fixedArgs(map[string]any{"id": lt.PlaylistMix})},
	{Op: "create_playlist", Args: fixedArgs(map[string]any{"name": "Night", "track_ids": []string{lt.TrackCitta, lt.TrackLuna}}), Masks: []string{"id", "created_by", "updated_by"}},
	{Op: "update_playlist", Args: fixedArgs(map[string]any{"id": lt.PlaylistMix, "revision": 1, "name": "Day", "add_track_ids": []string{lt.TrackShort}}), Masks: []string{"created_by", "updated_by"}},
	{Op: "export_playlist_zip", Args: fixedArgs(map[string]any{"id": lt.PlaylistMix}), Masks: []string{"id"}},
	{Op: "list_export_sources"},
	{Op: "export_jsonl", Args: fixedArgs(map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{lt.TrackLuna, lt.TrackCitta}}}), Masks: []string{"id"}},
	{Op: "get_export", Args: fixedArgs(map[string]any{"id": "no-such-export"}), Status: 404},
}

// backupCases are the M2a-B7 cases. list_backups runs before create_backup, so no rig has a run in flight
// (and no snapshot on disk) when it is read; the run id comes from the rig's id sequence. Each rig's
// cleanup Close waits for the backup its create_backup started.
var backupCases = []ParityCase{
	{Op: "list_backups"},
	{Op: "create_backup", Masks: []string{"id"}},
}

func init() {
	parityCases = append(parityCases, backupCases...)
	parityCases = append(parityCases, judgmentCases...)
	parityCases = append(parityCases, draftCases...)
	parityCases = append(parityCases, dataCases...)
	parityCases = append(parityCases, m2bCases...)
}

// needsFixture reports whether an operation must carry a parity fixture: any
// non-GET method, or any path/query/header parameter, or a request body.
func needsFixture(method string, o *huma.Operation) bool {
	return method != http.MethodGet || len(o.Parameters) > 0 || o.RequestBody != nil
}

func pathOperation(a huma.API, op api.OpInfo) *huma.Operation {
	item := a.OpenAPI().Paths[op.Path]
	if item == nil {
		return nil
	}
	return map[string]*huma.Operation{
		"GET": item.Get, "PUT": item.Put, "POST": item.Post, "DELETE": item.Delete, "PATCH": item.Patch,
	}[op.Method]
}

func TestNeedsFixture(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		method string
		op     *huma.Operation
		want   bool
	}{
		{"bare GET", "GET", &huma.Operation{}, false},
		{"GET with optional query", "GET", &huma.Operation{Parameters: []*huma.Param{{Name: "limit", In: "query"}}}, true},
		{"GET with path param", "GET", &huma.Operation{Parameters: []*huma.Param{{Name: "id", In: "path"}}}, true},
		{"bare POST", "POST", &huma.Operation{}, true},
		{"GET with body", "GET", &huma.Operation{RequestBody: &huma.RequestBody{}}, true},
		{"DELETE", "DELETE", &huma.Operation{}, true},
	}
	for _, tc := range tests {
		if got := needsFixture(tc.method, tc.op); got != tc.want {
			t.Errorf("%s: needsFixture = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// restRequest renders tool arguments as the REST call of op: path, query and
// header parameters come from the OpenAPI operation; the remaining arguments
// are the JSON body (the "body" argument alone when the body is nested).
func restRequest(t *testing.T, a huma.API, op api.OpInfo, args map[string]any) (path string, body any, headers []string) {
	t.Helper()
	o := pathOperation(a, op)
	if o == nil {
		t.Fatalf("%s: not in the OpenAPI document", op.ID)
	}
	path = op.Path
	q := url.Values{}
	rest := map[string]any{}
	for k, v := range args {
		rest[k] = v
	}
	for _, p := range o.Parameters {
		arg := p.Name
		if p.In == "header" {
			arg = strings.ToLower(strings.ReplaceAll(p.Name, "-", "_"))
		}
		v, ok := rest[arg]
		if !ok {
			continue
		}
		delete(rest, arg)
		switch p.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(fmt.Sprint(v)))
		case "query":
			if list, isList := v.([]string); isList {
				for _, e := range list {
					q.Add(p.Name, e)
				}
			} else {
				q.Set(p.Name, fmt.Sprint(v))
			}
		case "header":
			headers = append(headers, p.Name, fmt.Sprint(v))
		}
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	if nested, ok := rest["body"]; ok && len(rest) == 1 {
		return path, nested, headers
	}
	if len(rest) > 0 || o.RequestBody != nil {
		return path, rest, headers
	}
	return path, nil, headers
}

type paritySteps struct {
	cases []ParityCase
	ops   map[string]api.OpInfo
}

// parityScript orders the cases (Last ones at the end) and adds the implicit
// no-argument case of every operation that needs none.
func parityScript(t *testing.T, a huma.API) paritySteps {
	t.Helper()
	ps := paritySteps{ops: map[string]api.OpInfo{}}
	have := map[string]bool{}
	for _, c := range parityCases {
		key := c.Op + "/" + c.Variant
		if have[key] {
			t.Errorf("duplicate parity case for %s", key)
		}
		have[key] = true
		have[c.Op] = true
	}
	for _, op := range api.Operations(a) {
		if op.Meta.MCP != "tool" && op.Meta.MCP != "generic" {
			continue
		}
		ps.ops[op.ID] = op
		o := pathOperation(a, op)
		if o == nil {
			t.Errorf("%s: not found in the OpenAPI document", op.ID)
			continue
		}
		if !have[op.ID] {
			if needsFixture(op.Method, o) {
				t.Errorf("%s: %s %s has inputs or side effects and needs a parityCases entry", op.ID, op.Method, op.Path)
				continue
			}
			ps.cases = append(ps.cases, ParityCase{Op: op.ID})
		}
	}
	var last []ParityCase
	for _, c := range parityCases {
		if _, ok := ps.ops[c.Op]; !ok {
			t.Errorf("parity case for unknown or non-MCP operation %q", c.Op)
			continue
		}
		if c.Last {
			last = append(last, c)
		} else {
			ps.cases = append(ps.cases, c)
		}
	}
	ps.cases = append(ps.cases, last...)
	return ps
}

// faceCall runs one case on an MCP session through the first-class tool (or
// call_operation when generic) and returns the normalised structured result.
func mcpCall(t *testing.T, sess *mcp.ClientSession, op api.OpInfo, args map[string]any, viaGeneric bool, masks []string, wantErr bool) any {
	t.Helper()
	res, name := mcpRaw(t, sess, op, args, viaGeneric)
	return normalizeResult(t, res, op, name, masks, wantErr)
}

// mcpRaw performs the call and returns the result and the tool name used.
func mcpRaw(t *testing.T, sess *mcp.ClientSession, op api.OpInfo, args map[string]any, viaGeneric bool) (*mcp.CallToolResult, string) {
	t.Helper()
	name, params := op.ID, any(args)
	if viaGeneric || op.Meta.MCP != "tool" {
		name, params = "call_operation", map[string]any{"operation_id": op.ID, "arguments": args}
	}
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: params})
	if err != nil {
		t.Fatalf("%s via %s: %v", op.ID, name, err)
	}
	return res, name
}

func normalizeResult(t *testing.T, res *mcp.CallToolResult, op api.OpInfo, name string, masks []string, wantErr bool) any {
	t.Helper()
	if wantErr {
		if !res.IsError || len(res.Content) == 0 {
			t.Fatalf("%s via %s: expected a tool error, got %+v", op.ID, name, res)
		}
		tc, ok := res.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("%s via %s: error content %T", op.ID, name, res.Content[0])
		}
		var v any
		if err := json.Unmarshal([]byte(tc.Text), &v); err != nil {
			t.Fatalf("%s via %s: error text is not JSON: %v", op.ID, name, err)
		}
		return servertest.Normalize(v, masks...)
	}
	return servertest.Normalize(structured(t, res), masks...)
}

// runParity executes the script on identically seeded rigs: REST on A (agent
// bearer, explicit Idempotency-Key parity-<n> for create ops), MCP on B (no
// key, so derived) and, when stdio is set, stdio on C. Normalised responses
// must be equal at every step.
func runParity(t *testing.T, stdio bool) {
	t.Helper()
	scopes := []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}
	opts := servertest.Options{Fixture: true, Files: true, M2c: true, M3: true, Backend: "fake", M2b: servertest.M2bOptions{Fixture: true}}
	ra, rb := servertest.New(t, opts), servertest.New(t, opts)
	restClient, _ := ra.Agent(t, scopes...)
	mcpSess := rb.MCP(t, scopes...)
	var stdioSess *mcp.ClientSession
	var rc *servertest.Rig
	if stdio {
		rc = servertest.New(t, opts)
		stdioSess = rc.Stdio(t, scopes...)
	}
	script := parityScript(t, ra.API)
	if len(script.cases) == 0 {
		t.Fatal("no operations exercised")
	}
	var prevA, prevB, prevC map[string]any
	for i, c := range script.cases {
		op := script.ops[c.Op]
		argsFor := func(r *servertest.Rig, prev map[string]any) map[string]any {
			if c.Args == nil {
				return nil
			}
			return c.Args(r, prev)
		}
		args := argsFor(ra, prevA)
		path, body, headers := restRequest(t, ra.API, op, args)
		if op.Meta.Idempotency == "create" {
			headers = append(headers, "Idempotency-Key", fmt.Sprintf("parity-%d", i))
		}
		resp := restClient.Do(t, op.Method, path, body, headers...)
		if c.Status != 0 && resp.Status != c.Status || c.Status == 0 && resp.Status/100 != 2 {
			t.Fatalf("%s: REST status %d: %s", op.ID, resp.Status, resp.Body)
		}
		restJSON := resp.JSON(t)
		prevA, _ = restJSON.(map[string]any)
		isImage := op.Meta.MCPImage && c.Status == 0
		if isImage {
			restJSON = withoutImageData(restJSON) // MCP never carries data_base64 in structured content
		}
		want := servertest.Normalize(restJSON, c.Masks...)
		type face struct {
			sess *mcp.ClientSession
			rig  *servertest.Rig
			prev *map[string]any
		}
		faces := map[string]face{"mcp": {mcpSess, rb, &prevB}}
		if stdio {
			faces["stdio"] = face{stdioSess, rc, &prevC}
		}
		for name, f := range faces {
			fargs := argsFor(f.rig, *f.prev)
			res, toolName := mcpRaw(t, f.sess, op, fargs, false)
			if isImage {
				checkImageParity(t, op.ID+"/"+name, resp.JSON(t), res)
			}
			*f.prev = rawResult(t, res)
			if diff := cmp.Diff(want, normalizeResult(t, res, op, toolName, c.Masks, c.Status != 0)); diff != "" {
				t.Errorf("%s: REST vs %s mismatch (-rest +%s):\n%s", op.ID, name, name, diff)
			}
			if op.Meta.MCP == "tool" && !c.SkipGeneric {
				// Replays (derived key) or idempotent writes: the generic route must agree too.
				if diff := cmp.Diff(want, mcpCall(t, f.sess, op, fargs, true, c.Masks, c.Status != 0)); diff != "" {
					t.Errorf("%s: REST vs %s call_operation mismatch:\n%s", op.ID, name, diff)
				}
			}
		}
	}
}

// rawResult is the unmasked structured result of a tool call (the problem body of a tool error).
func rawResult(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.IsError {
		if len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcp.TextContent); ok {
				var v map[string]any
				if json.Unmarshal([]byte(tc.Text), &v) == nil {
					return v
				}
			}
		}
		return nil
	}
	m, _ := structured(t, res).(map[string]any)
	return m
}

// TestParityEveryOperation: every registered, non-excluded operation has a
// case, and REST, MCP tools and call_operation agree on each.
func TestParityEveryOperation(t *testing.T) {
	t.Parallel()
	runParity(t, false)
}

// TestParity_Stdio: the M0 and M1 cases are equal over REST, MCP and stdio.
func TestParity_Stdio(t *testing.T) {
	t.Parallel()
	runParity(t, true)
}

func TestWhoamiPropagatesAgentPrincipal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	tok := r.token(t, "agent-7", auth.ScopeRead)
	res, err := r.client(t, tok).CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatal(err)
	}
	m := structured(t, res).(map[string]any)
	if m["kind"] != "agent" || m["name"] != "agent-7" || m["via"] != "token" {
		t.Fatalf("principal not propagated: %v", m)
	}
	if sc := m["scopes"].([]any); len(sc) != 1 || sc[0] != "read" {
		t.Fatalf("scopes %v", sc)
	}
}

func TestScopeEnforcedThroughMCP(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	tok := r.token(t, "w", auth.ScopeWrite) // no read
	res, err := r.client(t, tok).CallTool(context.Background(), &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "FORBIDDEN_SCOPE") {
		t.Fatalf("want FORBIDDEN_SCOPE error result, got %+v", res)
	}
}

func TestAnonymousMCPIs401(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, auth := range []string{"", "Bearer mmp_bogus"} {
		req, _ := http.NewRequest("POST", r.url+"/mcp", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		wa := resp.Header.Get("WWW-Authenticate")
		if resp.StatusCode != 401 || !strings.HasPrefix(wa, "Bearer") {
			t.Fatalf("auth=%q: %d %q", auth, resp.StatusCode, wa)
		}
		if auth == "" && !strings.Contains(wa, `resource_metadata="http://mm.example.com/.well-known/oauth-protected-resource/mcp"`) {
			t.Fatalf("missing resource_metadata: %q", wa)
		}
	}
}

func TestToolsListGolden(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "g", auth.ScopeRead))
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(res.Tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	// Budget first, independent of -update: the ceiling is a committed constant.
	if err := checkToolsListBudget(len(got)); err != nil {
		t.Error(err)
	}
	path := filepath.Join("testdata", "tools_list.golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("tools/list differs from golden (-want +got; rerun with -update):\n%s", diff)
	}
}

// toolsListBaselineBytes is the size of testdata/tools_list.golden.json when the
// budget was last reviewed. Tool descriptions and schemas cost agent context;
// growth beyond 1.5x this is a deliberate decision (edit this constant in
// review), never an -update side effect.
//
// M1-B4 re-baseline 4412 -> 42997: six new tools (search_tracks, get_track, get_media_links, list_drafts,
// get_draft, get_stats) carry full output schemas (TrackDetail alone is ~15 KB); the 13 M1 operations
// are the milestone's whole read surface.
//
// M2b-B2 re-baseline 85917 -> 130984: four first-class M2b tools (next_duel, submit_duel, get_track_analysis,
// get_track_spectrogram) with full output schemas; create_duel, get_ladder and compare_tracks are generic (D-M2b-22).
//
// M2c-B1 re-baseline 130984 -> 132866: recipe_known/lossy on the track, media-link and duel/compare shapes, the
// nullable flac link and two search_tracks parameters (no tool added or demoted).
//
// M3-B2 re-baseline 132866 -> 207451: seven first-class M3 tools (estimate_generation, create_generation, estimate_variant,
// create_variant, wait_for_job, get_job, cancel_job) with full output schemas; the other sixteen M3 operations are
// generic (D-M3-36 demoted get_budget, list_jobs, get_batch and request_approval), and the track, draft and stats
// shapes gained the C81.1 fields.
//
// L1-B2 re-baselined 207451 -> 243029 (+35 578): the seven first-class L1 tools with their full output schemas
// (SongView, MuseDetail, SoundSearch, ...) and the L1 fields of the track shapes' neighbours; no other tool changed.
const toolsListBaselineBytes = 243029

func checkToolsListBudget(n int) error {
	if n*2 > toolsListBaselineBytes*3 {
		return fmt.Errorf("tools/list is %d bytes, over 1.5x the committed baseline (%d)", n, toolsListBaselineBytes)
	}
	return nil
}

func TestToolsListBudgetFires(t *testing.T) {
	t.Parallel()
	if checkToolsListBudget(toolsListBaselineBytes) != nil {
		t.Fatal("baseline size must pass")
	}
	if checkToolsListBudget(toolsListBaselineBytes*3/2+10) == nil {
		t.Fatal("oversized tools/list must fail the budget")
	}
}

func TestWhoamiAcrossProtocolVersions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	tok := r.token(t, "agent-pv", auth.ScopeRead)
	for _, ver := range []string{"", "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"} {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`
		req, _ := http.NewRequest("POST", r.url+"/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if ver != "" {
			req.Header.Set("MCP-Protocol-Version", ver)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("version %q: status %d %s", ver, resp.StatusCode, b)
			continue
		}
		var out struct {
			Result struct {
				StructuredContent map[string]any `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Errorf("version %q: %v: %s", ver, err, b)
			continue
		}
		m := out.Result.StructuredContent
		if m["kind"] != "agent" || m["name"] != "agent-pv" {
			t.Errorf("version %q: principal not propagated: %s", ver, b)
		}
	}
}

func TestMCPTransportErrorsAreProblemJSON(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	tok := r.token(t, "agent-1", auth.ScopeRead)
	tests := []struct {
		name, method, body string
		status             int
		code               string
	}{
		{"GET not allowed", "GET", "", 405, "METHOD_NOT_ALLOWED"},
		{"DELETE not allowed", "DELETE", "", 405, "METHOD_NOT_ALLOWED"},
		{"malformed payload", "POST", `{not json`, 400, "BAD_REQUEST"},
		{"unsupported rpc method", "POST", `{"jsonrpc":"2.0","id":1,"method":"nope/nothing"}`, 400, "BAD_REQUEST"},
	}
	for _, tc := range tests {
		req, _ := http.NewRequest(tc.method, r.url+"/mcp", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var p struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		}
		if resp.StatusCode != tc.status || resp.Header.Get("Content-Type") != "application/problem+json" ||
			json.Unmarshal(b, &p) != nil || p.Code != tc.code || p.Message == "" || p.Details == nil {
			t.Errorf("%s: %d %q %s", tc.name, resp.StatusCode, resp.Header.Get("Content-Type"), b)
		}
	}
}

// search_tracks?style=a&style=b over REST (repeated keys) equals the MCP call
// {"style":["a","b"]}: array parameters are exploded on both faces.
func TestParity_MultiValueQuery(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	tok := r.token(t, "agent-mv", auth.ScopeRead)
	sess := r.client(t, tok)
	for _, tc := range []struct {
		name string
		args map[string]any
		path string
		want int
	}{
		{"style AND", map[string]any{"style": []string{"ambient", "drone"}}, "/api/tracks?style=ambient&style=drone", 1},
		{"style single", map[string]any{"style": []string{"ambient"}}, "/api/tracks?style=ambient", 2},
		{"status OR", map[string]any{"status": []string{"final", "draft"}}, "/api/tracks?status=final&status=draft", 5},
		{"both", map[string]any{"status": []string{"draft"}, "style": []string{"rock", "guitar"}}, "/api/tracks?status=draft&style=rock&style=guitar", 2},
	} {
		viaREST := r.rest(t, tok, "GET", tc.path, nil)
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_tracks", Arguments: tc.args})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		viaMCP := structured(t, res)
		if diff := cmp.Diff(viaREST, viaMCP); diff != "" {
			t.Errorf("%s: REST vs MCP (-rest +mcp):\n%s", tc.name, diff)
		}
		if got := viaREST.(map[string]any)["total"]; got != float64(tc.want) {
			t.Errorf("%s: total = %v, want %d", tc.name, got, tc.want)
		}
	}
}

// MCP output ends up in third-party transcripts: an agent never sees the
// private reference metadata, only has_reference.
func TestGetDraft_ReferenceHumanOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	tok := r.token(t, "agent-ref", auth.ScopeRead)
	viaREST := r.rest(t, tok, "GET", "/api/drafts/"+lt.Draft1, nil).(map[string]any)
	res, err := r.client(t, tok).CallTool(context.Background(), &mcp.CallToolParams{Name: "get_draft", Arguments: map[string]any{"id": lt.Draft1}})
	if err != nil {
		t.Fatal(err)
	}
	viaMCP := structured(t, res).(map[string]any)
	for name, m := range map[string]map[string]any{"rest": viaREST, "mcp": viaMCP} {
		if m["has_reference"] != true || m["reference"] != nil {
			t.Errorf("%s: has_reference=%v reference=%v", name, m["has_reference"], m["reference"])
		}
	}
	b, _ := json.Marshal(viaMCP)
	if strings.Contains(string(b), "Synthetic Reference Title") {
		t.Errorf("reference leaked over MCP: %s", b)
	}
}
