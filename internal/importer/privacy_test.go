package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"uuid"

	"github.com/danielgtaylor/huma/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/mcpgen"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// Sentinels for the three kinds of private reference values (C16.9).
const (
	sentS1 = "zqxS1refsentinel"  // index.csv: source_title, source_artist, track_n
	sentS2 = "zqxS2collsentinel" // collection JSON: title, artist, why, rule
	sentS3 = "zqxS3listsentinel" // reference playlist: owner, url, titles, artists
)

func privacyInbox(e *env) {
	e.put("music/songs.jsonl", lines(req("priv-a", 11), req("priv-b", 12), req("priv-b-v2", 13)))
	e.flac("music/flac/priv_a.flac", "priv-a")
	e.flac("music/flac/lonely.flac", "lonely")
	e.put("music/index.csv", "variant_id,track_n,source_title,source_artist,variant,variant_type,seed\r\n"+
		"priv-a,"+sentS1+","+sentS1+","+sentS1+",v1,type1,11\r\n"+
		"priv-b,"+sentS1+","+sentS1+","+sentS1+",v1,type1,12\r\n")
	e.put("music/pilot.json", `{"name": "priv-coll", "rule": "`+sentS2+`", "songs": [{"n": 1, "title": "`+sentS2+`", "artist": "`+sentS2+`", "why": "`+sentS2+`", "variant_ids": ["priv-b", "priv-b-v2"]}]}`)
	e.put("music/playlist.json", `{"playlist": "`+sentS3+`", "owner": "`+sentS3+`", "url": "https://mm.example.com/`+sentS3+`", "note": "", "tracks": [{"title": "`+sentS3+`", "artist": "`+sentS3+`"}]}`)
	e.put("varianti/priv-a-v2.json", line("priv-a-v2", 14, synLyrics("priv-a-v2"), "full", ""))
}

// dbTextDump returns "table.column=value" strings for every cell of every table
// (including FTS shadow tables) whose text contains needle.
func dbHits(t *testing.T, e *env, needle string) []string {
	t.Helper()
	var hits []string
	tables := e.query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	var names []string
	for tables.Next() {
		var n string
		_ = tables.Scan(&n)
		names = append(names, n)
	}
	tables.Close()
	for _, tbl := range names {
		cols := e.query(`SELECT name FROM pragma_table_info('` + tbl + `')`)
		var cs []string
		for cols.Next() {
			var c string
			_ = cols.Scan(&c)
			cs = append(cs, c)
		}
		cols.Close()
		for _, c := range cs {
			var n int
			q := `SELECT count(*) FROM "` + tbl + `" WHERE instr(cast("` + c + `" as text), ?) > 0`
			if err := e.st.R.QueryRow(q, needle).Scan(&n); err == nil && n > 0 {
				hits = append(hits, tbl+"."+c)
			}
		}
	}
	return hits
}

type privacyRig struct {
	srv    *httptest.Server
	api    huma.API
	token  string
	tracks []string
	drafts map[string]string // request id -> draft id
	runID  string
}

func newPrivacyRig(t *testing.T, e *env) *privacyRig {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	cfg := config.Config{
		Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy",
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TrustedUsers: []string{"alice"},
	}
	tokens := auth.NewTokenStore(e.st, key, time.Now)
	r := &privacyRig{drafts: map[string]string{}}
	now := func() time.Time { return e.now }
	lib := library.New(e.st, e.data, now)
	judg := judgments.New(e.st, lib, now, func() string { return "privacy-id" })
	pls := playlists.New(e.st, lib, now, func() string { return "privacy-pl" })
	signer := serve.NewSigner(key, now)
	listen := listening.New(e.st, now)
	bak := backup.New(backup.Options{Store: e.st, DataDir: e.dataDir, BackupDir: filepath.Join(t.TempDir(), "backup"), Now: now})
	t.Cleanup(func() { _ = bak.Close(context.Background()) })
	h, err := httpserver.New(httpserver.Options{
		Cfg: cfg, Sec: config.Secrets{Key: key, EdgeSecret: "edge"}, Store: e.st, Tokens: tokens,
		Sessions: auth.NewLocalAuth(e.st, key, cfg.PublicURL), SPA: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Version: "v-test", Commit: "abc", Started: e.now.Add(-time.Hour),
		Library: lib, Judgments: judg, Signer: signer, Now: now, Playlists: pls, Backups: bak,
		Exports:   exports.New(exports.Options{Store: e.st, Data: e.data, Lib: lib, Playlists: pls, Ratings: judg, Signer: signer, Now: now}),
		Clips:     clip.New(e.data, e.dataDir, mediatool.Tools{}, cliptest.NewAnalyzer(), lib, signer, clip.Options{CacheBytes: 1 << 20, Now: now}),
		Duels:     duels.New(duels.Options{Store: e.st, Library: lib, Clips: cliptest.NewRenderer(), Signer: signer, Now: now}),
		Ears:      ears.New(ears.Options{Store: e.st, Library: lib, Signer: signer, Now: now}),
		Listening: listen, Context: trackctx.New(lib, judg, listen, signer, cfg.PublicURL, now), Audit: audit.NewStore(e.st, func() string { return uuid.NewV7().String() }),
		DeviceStore: auth.NewDeviceStore(e.st, key, now, func() string { return "privacy-device" }),
		Probe:       &serve.Object{RelPath: "derived/probe/probe-v1.flac", ContentType: "audio/flac", ETag: strings.Repeat("ab", 32)},
		MCPFactory: func(a huma.API, inner http.Handler) (http.Handler, error) {
			r.api = a
			s, err := mcpgen.NewServer(a, inner, "v-test")
			if err != nil {
				return nil, err
			}
			return mcpgen.Handler(s), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.srv = httptest.NewServer(h)
	t.Cleanup(r.srv.Close)
	r.token, _, err = tokens.Create(context.Background(), "privacy", []auth.Scope{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range e.tracks() {
		r.tracks = append(r.tracks, tr.ID)
	}
	for k, d := range e.drafts() {
		r.drafts[strings.SplitN(k, "#", 2)[0]] = d.ID
	}
	r.runID = e.scalar(`SELECT id FROM import_runs ORDER BY started_at DESC LIMIT 1`)
	return r
}

type bearer struct{ tok string }

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(req)
}

// getPaths expands every GET operation of the registry into concrete request paths.
func (r *privacyRig) getPaths() map[string][]string { // operation id -> request URLs
	out := map[string][]string{}
	for p, item := range r.api.OpenAPI().Paths {
		if item.Get == nil {
			continue
		}
		op := item.Get.OperationID
		switch {
		case strings.HasPrefix(p, "/api/tracks/{id}"):
			for _, id := range r.tracks {
				u := strings.Replace(p, "{id}", id, 1)
				if strings.HasSuffix(p, "/peaks") {
					u += "?level=overview"
				}
				out[op] = append(out[op], u)
			}
		case strings.HasPrefix(p, "/api/drafts/{id}"):
			for _, id := range r.drafts {
				out[op] = append(out[op], strings.Replace(p, "{id}", id, 1))
			}
		case strings.HasPrefix(p, "/api/imports/{id}"):
			for _, s := range library.ImportSections {
				out[op] = append(out[op], strings.Replace(p, "{id}", r.runID, 1)+"?section="+s)
			}
		case strings.HasPrefix(p, "/api/playlists/{") || strings.HasPrefix(p, "/api/exports/{"):
			// M2a operations: their privacy is covered by the M2a acceptance tests (B10)
		case strings.HasPrefix(p, "/api/duels/{"):
			// M2b: duels hold track ids and verdicts only, never recipe reference metadata
		case p == "/api/compare":
			// M2b compare_tracks needs two distinct takes and reads recipes, never reference metadata
		case strings.Contains(p, "{"):
			// no other parameterised GET exists in M1; a new one must be added here
			out[op] = append(out[op], "UNHANDLED:"+p)
		default:
			out[op] = append(out[op], p)
		}
	}
	return out
}

func (r *privacyRig) get(t *testing.T, path string, human bool) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", r.srv.URL+path, nil)
	if human {
		req.Header.Set("Remote-User", "alice")
		req.Header.Set("X-MM-Edge", "edge")
	} else {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestPrivacy_ReferenceMetadataConfined(t *testing.T) {
	e := newEnv(t)
	privacyInbox(e)

	// dry-run: report, mapping.todo.csv, text summary
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	im, inv := e.inventory(func(o *Options) { o.MinAge = 2 * time.Minute })
	files, err := im.WriteDryRun(inv)
	if err != nil {
		t.Fatal(err)
	}
	var text bytes.Buffer
	inv.Report.FormatText(&text)
	var todo bytes.Buffer
	if err := WriteMappingTemplate(&todo, inv); err != nil {
		t.Fatal(err)
	}
	reportJSON, _ := json.Marshal(inv.Report)
	onDisk, _ := os.ReadFile(filepath.Join(e.dataDir, files.Report))
	todoDisk, _ := os.ReadFile(filepath.Join(e.dataDir, files.MappingTodo))
	if !strings.Contains(string(todoDisk), "lonely.flac") {
		t.Fatalf("mapping.todo.csv lacks the unmatched audio:\n%s", todoDisk)
	}
	for name, out := range map[string]string{"report": string(reportJSON), "report file": string(onDisk), "text": text.String(),
		"todo": todo.String(), "todo file": string(todoDisk)} {
		for _, s := range []string{sentS1, sentS2, sentS3} {
			if strings.Contains(out, s) {
				t.Errorf("dry-run %s contains %s", name, s)
			}
		}
	}

	// apply
	_, res := e.applyAll(func(o *Options) { o.MinAge = 2 * time.Minute })
	if res.State != "completed" {
		t.Fatalf("apply: %+v", res)
	}
	appliedJSON, _ := json.Marshal(res.Report)
	for _, s := range []string{sentS1, sentS2, sentS3} {
		if strings.Contains(string(appliedJSON), s) || strings.Contains(logs.String(), s) {
			t.Errorf("apply output or log contains %s", s)
		}
	}
	// the database: S1 only in recipe_refs.ref_json; S2 and S3 nowhere
	if h := dbHits(t, e, sentS1); strings.Join(h, ",") != "recipe_refs.ref_json" {
		t.Errorf("S1 stored in %v", h)
	}
	for _, s := range []string{sentS2, sentS3} {
		if h := dbHits(t, e, s); len(h) != 0 {
			t.Errorf("%s stored in %v", s, h)
		}
	}
	if e.count("recipe_refs") != 2 {
		t.Fatalf("recipe_refs = %d", e.count("recipe_refs"))
	}

	// every GET operation, as a human and as an agent (REST bearer, then MCP)
	rig := newPrivacyRig(t, e)
	humanDraftHits := 0
	paths := rig.getPaths()
	if len(paths) < 8 {
		t.Fatalf("only %d GET operations found", len(paths))
	}
	for op, urls := range paths {
		for _, u := range urls {
			if strings.HasPrefix(u, "UNHANDLED:") {
				t.Errorf("%s: parameterised GET %s needs privacy coverage", op, u)
				continue
			}
			for _, human := range []bool{true, false} {
				code, body := rig.get(t, u, human)
				if code == 501 || (!human && code == 403) {
					continue // M2a stubs (until their task lands) and operator-only operations
				}
				// peaks files and clip sources do not exist under the fake ingester; the body is still checked
				if code/100 != 2 && op != "get_track_peaks" && !(code == 409 && (op == "get_clip" || strings.HasPrefix(op, "get_track_"))) {
					t.Fatalf("GET %s (human=%v): %d %s", u, human, code, body)
				}
				for _, s := range []string{sentS2, sentS3} {
					if strings.Contains(body, s) {
						t.Errorf("%s (human=%v) leaks %s", u, human, s)
					}
				}
				if strings.Contains(body, sentS1) {
					if !human || op != "get_draft" {
						t.Errorf("%s (human=%v) leaks S1", u, human)
					} else {
						humanDraftHits++
					}
				}
			}
		}
	}
	if humanDraftHits == 0 {
		t.Error("humans never saw the reference values: the check proves nothing")
	}

	// MCP: every tool the registry exposes for a GET operation
	c := mcp.NewClient(&mcp.Implementation{Name: "privacy", Version: "0"}, nil)
	sess, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: rig.srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{rig.token}}, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools.Tools {
		have[tl.Name] = true
	}
	called := 0
	for op, urls := range paths {
		if !have[op] {
			continue
		}
		for _, u := range urls {
			pu, _ := url.Parse(u)
			args := map[string]any{}
			segs := strings.Split(strings.Trim(pu.Path, "/"), "/")
			switch {
			case strings.HasPrefix(pu.Path, "/api/tracks/") || strings.HasPrefix(pu.Path, "/api/drafts/") || strings.HasPrefix(pu.Path, "/api/imports/"):
				if len(segs) >= 3 {
					args["id"] = segs[2]
				}
			}
			for k, v := range pu.Query() {
				args[k] = v[0]
			}
			res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: op, Arguments: args})
			if err != nil {
				t.Fatalf("mcp %s: %v", op, err)
			}
			b, _ := json.Marshal(res)
			for _, s := range []string{sentS1, sentS2, sentS3} {
				if strings.Contains(string(b), s) {
					t.Errorf("mcp %s %v leaks %s", op, args, s)
				}
			}
			called++
		}
	}
	if called < 8 {
		t.Errorf("only %d MCP calls made", called)
	}

	// the OpenAPI document (what `metamusic openapi` prints)
	spec, _ := json.Marshal(rig.api.OpenAPI())
	for _, s := range []string{sentS1, sentS2, sentS3} {
		if strings.Contains(string(spec), s) {
			t.Errorf("openapi contains %s", s)
		}
	}
}
