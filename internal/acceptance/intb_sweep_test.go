package acceptance_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// listenerBannedKeys are JSON keys no Listener answer may carry, on top of the money key shapes (the value sweep below is
// the net for a hidden value stored under a key nobody listed).
var listenerBannedKeys = []string{
	"seed", "request_id", "critic_stars", "status_set_by", "muse_id", "muse", "queued_jobs", "draft_id", "open_proposals",
	"cost", "cost_basis", "cot", "hash", "master_sha256", "audio_md5", "import", "playlists", "listening", "comments",
	"open_comments", "proposals", "events", "sessions", "draft", "liner", "operator", "rationale", "job_id", "mi", "muses",
	"pick.evidence", "duels", "ladder", "average", "others", "provider", "error_message", "principal", "token_prefix",
	"armed_changed_by", "proposed_by", "proposal_note", "voice_notes", "callbacks",
}

// editorBannedKeys are the money key shapes: an Editor never sees a cost (D-U1-12 for Editors, C305).
var editorBannedKeys = []string{"cost", "cost_basis", "cost_display_usd", "cost_display_micro_usd"}

func isMoneyKey(k string) bool {
	return strings.HasSuffix(k, "_micro_usd") || strings.HasSuffix(k, "_usd") || strings.HasPrefix(k, "display_") || strings.HasPrefix(k, "cost_display")
}

// intbHit is one leak.
type intbHit struct{ Where, What string }

// intbLeaf is one scalar of a body: where it is and its text (numbers as their token, ids the caller sent removed).
type intbLeaf struct{ Path, Text string }

// intbScan walks a decoded JSON body and reports every banned key and every secret value in it. Strings are matched as
// substrings (a hash inside a URL counts), numbers as whole number tokens. scrub values (ids the caller itself sent) are
// removed first: an echo of what the caller typed is not a leak.
func intbScan(op string, body []byte, secrets []intbSecret, banned []string, scrub []string) []intbHit {
	var hits []intbHit
	var v any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil { // not JSON (XML, audio): the raw text is scanned
		return intbScanText(op, string(body), secrets, scrub, "raw")
	}
	var leaves []intbLeaf
	var walk func(path string, n any)
	walk = func(path string, n any) {
		switch x := n.(type) {
		case map[string]any:
			for k, c := range x {
				if intbBanned(banned, path+"."+k, k) {
					hits = append(hits, intbHit{path + "." + k, "banned key " + k})
				}
				walk(path+"."+k, c)
			}
		case []any:
			for _, c := range x {
				walk(path+"[]", c)
			}
		case string:
			leaves = append(leaves, intbLeaf{path, intbScrub(x, scrub)})
		case json.Number:
			leaves = append(leaves, intbLeaf{path, x.String()})
		}
	}
	walk("$", v)
	return append(hits, intbMatch(op, leaves, secrets)...)
}

func intbScrub(s string, scrub []string) string {
	for _, sc := range scrub {
		if sc != "" {
			s = strings.ReplaceAll(s, sc, "")
		}
	}
	return s
}

// intbMatch looks for every secret in the leaves. One substring search of the whole joined text per secret keeps a body of
// thousands of strings against hundreds of secrets cheap; only a candidate is then located and checked against its token
// boundaries.
func intbMatch(op string, leaves []intbLeaf, secrets []intbSecret) []intbHit {
	var sb strings.Builder
	for _, l := range leaves {
		sb.WriteString(l.Text)
		sb.WriteByte(0)
	}
	all := sb.String()
	var hits []intbHit
	for _, sc := range secrets {
		if len(sc.Ops) > 0 && !slices.Contains(sc.Ops, op) {
			continue
		}
		if !strings.Contains(all, sc.Value) {
			continue
		}
		for _, l := range leaves {
			if intbMatchOne(sc, l.Text) {
				hits = append(hits, intbHit{l.Path, sc.Kind + " " + sc.Value})
				break
			}
		}
	}
	return hits
}

var tokenRe sync.Map // pattern -> *regexp.Regexp

func tokenRegexp(prefix, class, value string) *regexp.Regexp {
	key := prefix + "\x00" + value
	if re, ok := tokenRe.Load(key); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`(^|[^` + class + `])` + regexp.QuoteMeta(value) + `($|[^` + class + `])`)
	tokenRe.Store(key, re)
	return re
}

func intbMatchOne(sc intbSecret, s string) bool {
	switch {
	case sc.Word:
		return tokenRegexp("w", "A-Za-z0-9_", sc.Value).MatchString(s)
	case sc.Num:
		return tokenRegexp("n", "0-9", sc.Value).MatchString(s)
	}
	return strings.Contains(s, sc.Value)
}

// intbScanText scans one text.
func intbScanText(op, s string, secrets []intbSecret, scrub []string, where string) []intbHit {
	return intbMatch(op, []intbLeaf{{where, intbScrub(s, scrub)}}, secrets)
}

// intbBanned reports whether the key at path is banned: a plain entry bans the key anywhere, an entry with a dot bans that
// path suffix only, "*money*" bans every money key shape.
func intbBanned(banned []string, path, key string) bool {
	for _, b := range banned {
		switch {
		case b == "*money*":
			if isMoneyKey(key) {
				return true
			}
		case strings.Contains(b, "."):
			if strings.HasSuffix(strings.ReplaceAll(path, "[]", ""), "."+b) {
				return true
			}
		case b == key:
			return true
		}
	}
	return false
}

// intbCall is one request of the sweep.
type intbCall struct {
	Op     intbOp
	Method string
	Path   string
	Body   any
	Scrub  []string // ids the request itself carries
	Gone   bool     // the id is a discarded take: the answer must be a 404
}

var (
	intbSeq   atomic.Int64
	pathParam = regexp.MustCompile(`\{[a-z_]+\}`)
)

// intbIDs are the realistic ids for each path parameter kind, by route prefix.
func (w *intbWorld) idsFor(tb testing.TB, op intbOp) (ids []string, gone map[string]bool, ok bool) {
	gone = map[string]bool{}
	p := op.Path
	switch {
	case strings.HasPrefix(p, "/api/tracks/"):
		ids = append(ids, w.Visible[:min(8, len(w.Visible))]...)
		ids = append(ids, w.Visible[len(w.Visible)-2:]...)
		for _, d := range w.Discarded {
			ids = append(ids, d)
			gone[d] = true
		}
		// the tainted fixtures, so the audio routes see the hidden-metadata masters
		for id := range w.Taint {
			if !slices.Contains(ids, id) && !slices.Contains(w.Discarded, id) {
				ids = append(ids, id)
			}
		}
	case strings.HasPrefix(p, "/api/songs/"):
		ids = append(ids, w.Songs[:min(10, len(w.Songs))]...)
		ids = append(ids, w.GoneSong)
	case strings.HasPrefix(p, "/api/records/"):
		ids = append(ids, w.Records...)
		ids = append(ids, w.Draft)
	case strings.HasPrefix(p, "/api/playlists/"):
		ids = w.Playlists
	case strings.HasPrefix(p, "/api/crates/"):
		ids = w.Crates
	case strings.HasPrefix(p, "/api/tapes/"):
		ids = []string{w.Tape}
	case strings.HasPrefix(p, "/api/device-profiles/"):
		ids = []string{"dev-intb-sweep-1"}
	case strings.HasPrefix(p, "/api/prompts/"):
		ids = []string{"critique_track", "write_variant", "session_review"}
	default:
		table := ""
		for prefix, tbl := range map[string]string{"/api/batches/": "batches", "/api/jobs/": "jobs", "/api/muses/": "muses", "/api/drafts/": "recipe_drafts",
			"/api/exports/": "exports", "/api/callbacks/": "callbacks", "/api/patches/": "patches", "/api/transforms/": "transforms", "/api/comments/": "comments",
			"/api/connectors/": "connector_grants", "/api/plans/": "desk_plans", "/api/duels/": "duels", "/api/imports/": "import_runs", "/api/sessions/": "sessions", "/api/mandates/": "mandates"} {
			if strings.HasPrefix(p, prefix) {
				table = tbl
			}
		}
		if table == "" {
			return nil, nil, false
		}
		ids = intbSQLStrings(tb, w.R, `SELECT id FROM `+table+` ORDER BY id LIMIT 6`)
	}
	ids = append(ids, "01900000-0000-7000-8000-00000000dead")
	return slices.Compact(ids), gone, true
}

// intbEnumQueries are the query strings of one operation worth a leak check: none, then each enum value of each enum
// parameter (arrays of enums with every value at once), and a limit large enough to read everything.
func intbEnumQueries(op intbOp) []string {
	qs := []string{""}
	has := func(n string) bool {
		for _, q := range op.Query {
			if q.Name == n {
				return true
			}
		}
		return false
	}
	if has("limit") {
		qs = []string{"limit=200"}
	}
	for _, q := range op.Query {
		if len(q.Enum) == 0 {
			continue
		}
		for _, e := range q.Enum {
			qs = append(qs, q.Name+"="+url.QueryEscape(e))
		}
		if q.Type == "array" {
			var all []string
			for _, e := range q.Enum {
				all = append(all, q.Name+"="+url.QueryEscape(e))
			}
			qs = append(qs, strings.Join(all, "&"))
		}
	}
	return qs
}

// intbMutations are the state-changing operations a Listener may call, with a realistic body. Every other mutation of a role
// is skipped with the reason in intbSkipReason.
func (w *intbWorld) mutationBody(op intbOp, id string) (body any, ok bool) {
	n := intbSeq.Add(1)
	switch op.ID {
	case "rate_track":
		return map[string]any{"stars": 3, "verdict": fmt.Sprintf("sweep verdict %d", n)}, true
	case "log_listen_events":
		return map[string]any{"events": []map[string]any{{"id": fmt.Sprintf("0190aaaa-0000-7000-8000-%012d", n), "track_id": id, "session_id": "sweep-session",
			"kind": "start", "client_at": "2026-11-19T10:00:00Z"}}}, true
	case "put_device_profile":
		return map[string]any{"label": "sweep", "user_agent": "sweep", "quality": "auto", "quality_source": "default", "probe_result": "none", "flac_can_play": "", "probe_version": 0}, true
	case "create_pairing_code":
		return map[string]any{"name": fmt.Sprintf("sweep phone %d", n)}, true
	case "create_subsonic_client":
		return map[string]any{"name": fmt.Sprintf("sweep app %d", n), "write": false}, true
	}
	return nil, false
}

// intbSkipMutation is the written reason an operation that changes state is not called by the sweep of a role; the
// operations that revoke the sweep's own credentials are called last by the caller.
func intbSkipMutation(op intbOp) string {
	switch op.ID {
	case "revoke_device", "revoke_own_device", "revoke_subsonic_client":
		return "revokes the sweep's own credential: called once, last, by the ownership test"
	}
	return "an Editor mutation (spend, curation, drafts, comments): its answers are pinned by TestEditorHeldOps, the E1 tests and the redaction rules of its own response"
}

// restCalls expands one allowed operation into the sweep's requests.
func (w *intbWorld) restCalls(tb testing.TB, op intbOp) (calls []intbCall, skip string) {
	tb.Helper()
	if strings.HasPrefix(op.ID, "wait_for_") {
		return nil, "a long poll that holds the request until the job ends; its body is the get_job and get_transform shape the sweep reads"
	}
	if op.Method != "GET" {
		if _, ok := w.mutationBody(op, ""); !ok {
			return nil, intbSkipMutation(op)
		}
	}
	paths := []string{op.Path}
	var gone map[string]bool
	if len(op.PathParams) > 0 {
		ids, g, ok := w.idsFor(tb, op)
		if !ok {
			tb.Errorf("operation %s (%s) has path parameters %v the sweep has no realistic ids for: add them to idsFor", op.ID, op.Path, op.PathParams)
			return nil, "no ids"
		}
		gone = g
		paths = nil
		for _, id := range ids {
			paths = append(paths, pathParam.ReplaceAllString(op.Path, id))
		}
	}
	for _, p := range paths {
		id := ""
		if i := strings.LastIndex(strings.TrimSuffix(p, "/"), "/"); len(op.PathParams) > 0 {
			segs := strings.Split(p, "/")
			for _, sgm := range segs {
				if len(sgm) > 12 && strings.Contains(sgm, "-") {
					id = sgm
				}
			}
			_ = i
		}
		qs := []string{""}
		if op.Method == "GET" {
			qs = intbEnumQueries(op)
		}
		for _, q := range qs {
			c := intbCall{Op: op, Method: op.Method, Path: p, Gone: gone[id], Scrub: []string{id}}
			if q != "" {
				c.Path += "?" + q
			}
			if op.Method != "GET" {
				c.Body, _ = w.mutationBody(op, firstVisible(w, id))
			}
			calls = append(calls, c)
		}
	}
	return calls, ""
}

func firstVisible(w *intbWorld, id string) string {
	if id != "" && !slices.Contains(w.Discarded, id) {
		return id
	}
	return w.Visible[0]
}

// intbRole is one principal of a sweep: the client that speaks as it and the Permitted row that says what it may call.
type intbRole struct {
	Name    string
	Client  *servertest.Client
	P       auth.Principal
	Secrets []intbSecret
	Banned  []string
	// HideDiscarded: a discarded take is a 404 for this role (the Listener's)
	HideDiscarded bool
	// MoneyOK lists the operations whose answers legitimately carry money keys for this role, with the reason (the Editor's
	// own estimate and batch, C310.5); the amounts themselves stay secret by value except the role's own
	MoneyOK map[string]string
	// Collect, when set, records the kind of every hit instead of failing: the negative control that proves the secrets are
	// really in the world and the scan really finds them (the Owner sees all of them)
	Collect map[string]bool
}

// intbRun calls every operation role may call and scans every answer. It returns the operation ids it called, and fails
// the test for an operation it neither called nor skipped with a written reason.
func (w *intbWorld) run(t *testing.T, role intbRole, ops []intbOp, skipHuman, mutate bool) (called map[string][]*servertest.Response) {
	t.Helper()
	called = map[string][]*servertest.Response{}
	skipped := map[string]string{}
	reported := map[string]bool{}
	fetched := map[string]bool{}
	for _, op := range allowedFor(ops, role.P) {
		if skipHuman && strings.HasPrefix(op.Path, "/human/") {
			skipped[op.ID] = "a device cookie is not accepted under /human/ (D1): a paired phone never reaches the self-service ops"
			continue
		}
		if (op.Method != "GET") != mutate {
			continue
		}
		calls, skip := w.restCalls(t, op)
		if skip != "" {
			skipped[op.ID] = skip
			continue
		}
		for _, c := range calls {
			hdr := []string{}
			if op.Meta.Idempotency != "" {
				hdr = append(hdr, "Idempotency-Key", fmt.Sprintf("intb-%s-%d", role.Name, intbSeq.Add(1)))
			}
			resp := role.Client.Do(t, c.Method, c.Path, c.Body, hdr...)
			called[op.ID] = append(called[op.ID], resp)
			if resp.Status >= 500 && !(intbFileRoute(c.Path) && w.noFiles(t, c.Scrub)) {
				t.Errorf("%s %s %s: %d %.200s", role.Name, c.Method, c.Path, resp.Status, resp.Body)
			}
			if c.Gone && role.HideDiscarded && resp.Status != 404 && resp.Status != 422 && resp.Status != 405 {
				t.Errorf("%s %s %s: a discarded take answers %d, want 404", role.Name, c.Method, c.Path, resp.Status)
			}
			if resp.Status/100 == 2 {
				for _, u := range intbMediaURLs(resp.Body) {
					if fetched[u] {
						continue
					}
					fetched[u] = true
					file, st := w.fetchMedia(t, u)
					if st != 200 {
						continue
					}
					for _, h := range intbScanText(op.ID, string(file), role.Secrets, nil, "file "+u[:min(40, len(u))]) {
						if role.Collect != nil {
							role.Collect[h.What] = true
							continue
						}
						if h.What != "" && !strings.Contains(h.What, "number") {
							key := "file|" + h.Where + "|" + h.What
							if !reported[key] {
								reported[key] = true
								t.Errorf("%s %s: the file it links to carries %s", role.Name, c.Path, h.What)
							}
						}
					}
				}
			}
			banned := role.Banned
			if role.MoneyOK[op.ID] != "" {
				banned = slices.DeleteFunc(slices.Clone(banned), func(b string) bool { return b == "*money*" || slices.Contains(editorBannedKeys, b) })
			}
			for _, h := range intbScan(op.ID, resp.Body, role.Secrets, banned, c.Scrub) {
				if role.Collect != nil {
					role.Collect[h.What] = true
					continue
				}
				key := op.ID + "|" + h.Where + "|" + h.What
				if !reported[key] {
					reported[key] = true
					t.Errorf("%s %s %s (%d): leak at %s: %s", role.Name, c.Method, c.Path, resp.Status, h.Where, h.What)
				}
			}
		}
	}
	var names []string
	for id := range skipped {
		names = append(names, id)
	}
	sort.Strings(names)
	for _, id := range names {
		if skipped[id] == "" {
			t.Errorf("%s: operation %s skipped without a written reason", role.Name, id)
		}
	}
	return called
}

// noFiles reports whether the request names a take whose master is a placeholder (the L1 to L3 fixtures write rows, and the
// M1 fixtures write files that are not decodable audio): its audio routes answer 5xx for every role, Owner included, and the
// answer is still scanned. The tainted fixtures (cleantest) hold real FLAC and MP3 structure and are not excused.
func (w *intbWorld) noFiles(tb testing.TB, ids []string) bool {
	tb.Helper()
	for _, id := range ids {
		if _, tainted := w.Taint[id]; tainted || id == "" {
			continue
		}
		if len(intbSQLStrings(tb, w.R, `SELECT id FROM tracks WHERE id = ?`, id)) > 0 {
			return true
		}
	}
	return false
}

// intbFileRoute reports whether a path needs a real audio file of its take: the only routes whose 5xx a placeholder fixture
// master excuses. Any other 5xx is a server bug.
func intbFileRoute(path string) bool {
	for _, p := range []string{"/media", "/download", "/peaks", "/clip", "/audio", "/stream", "/spectrogram", "/waveform", "/cover", "/analysis", "/tape"} {
		if strings.Contains(path, p) {
			return true
		}
	}
	return false
}

// intbMediaURLs lists every signed media link a JSON body carries.
func intbMediaURLs(body []byte) []string {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(n any) {
		switch x := n.(type) {
		case map[string]any:
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		case string:
			if strings.HasPrefix(x, "/media/") {
				out = append(out, x)
			}
		}
	}
	walk(v)
	return out
}

// fetchMedia downloads a signed link the way a browser does (no credentials) and returns the bytes and the status.
func (w *intbWorld) fetchMedia(tb testing.TB, u string) ([]byte, int) {
	tb.Helper()
	resp, err := http.Get(w.R.URL + u) //nolint:gosec,noctx // the rig's own loopback server
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode
}
