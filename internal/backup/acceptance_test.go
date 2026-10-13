package backup_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type bearer struct {
	url, tok string
}

func (b bearer) do(t *testing.T, method, path string, hdr ...string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, b.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+b.tok)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

func (b bearer) json(t *testing.T, path string) any {
	t.Helper()
	code, _, body := b.do(t, "GET", path)
	if code != 200 {
		t.Fatalf("GET %s: %d %s", path, code, body)
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// restoredRig serves an already restored data directory, as `serve` would.
func restoredRig(t *testing.T, dataDir string) (*app.Services, bearer) {
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
	unlimited := ratelimit.Config{}
	for _, c := range []ratelimit.Class{ratelimit.Agent, ratelimit.AgentHeavy, ratelimit.Human, ratelimit.HumanHeavy, ratelimit.Pair} {
		unlimited[c] = ratelimit.Limit{PerMinute: 1e6, Burst: 1e6}
	}
	now := func() time.Time { return lt.FixedNow.Add(time.Hour) }
	svc, err := app.Build(ctx, cfg, config.Secrets{Key: []byte(strings.Repeat("0123456789abcdef", 2)), EdgeSecret: "edge"}, st, root, app.Options{Now: now, Limits: unlimited, Version: "v-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	opts := svc.Server
	opts.SPA = fstest.MapFS{"index.html": {Data: []byte("x")}}
	h, err := httpserver.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tok, _, err := svc.Tokens.Create(ctx, "restored-agent", []auth.Scope{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	return svc, bearer{srv.URL, tok}
}

func TestAcceptance_M2a_BackupRestoreEmptyDir(t *testing.T) {
	if testing.Short() {
		t.Skip("acceptance test (C23 tier)")
	}
	rig := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	agent, _ := rig.Agent(t, auth.ScopeRead, auth.ScopeWrite)

	resp := agent.Do(t, "POST", "/api/backups", nil, "Idempotency-Key", "bk-1")
	if resp.Status != 202 {
		t.Fatalf("create_backup: %d %s", resp.Status, resp.Body)
	}
	var snapshot string
	deadline := time.Now().Add(60 * time.Second)
	for snapshot == "" {
		if time.Now().After(deadline) {
			t.Fatal("backup did not complete")
		}
		runs := agent.OK(t, "GET", "/api/backups", nil).(map[string]any)["runs"].([]any)
		for _, r := range runs {
			run := r.(map[string]any)
			switch run["state"] {
			case "failed":
				t.Fatalf("backup failed: %v", run["error"])
			case "completed":
				if s, ok := run["snapshot"].(string); ok && run["id"] != lt.BackupRunID {
					snapshot = s
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	snapDir := filepath.Join(rig.BackupDir, snapshot)

	// responses recorded before the live library changes
	paths := []string{
		"/api/tracks/" + lt.TrackLuna,
		"/api/tracks/" + lt.TrackLuna + "/comments?limit=10&order=time",
		"/api/tracks?min_stars=3",
		"/api/playlists/" + lt.PlaylistMix,
	}
	before := map[string]any{}
	for _, p := range paths {
		before[p] = agent.OK(t, "GET", p, nil)
	}
	if hits, _ := before["/api/tracks?min_stars=3"].(map[string]any)["items"].([]any); len(hits) == 0 {
		t.Fatal("the min_stars search finds nothing: the check would be vacuous")
	}

	// mutate the live library
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/tracks/" + lt.TrackLuna + "/comments", map[string]any{"body": "after the snapshot", "kind": "note"}},
		{"PATCH", "/api/tracks/" + lt.TrackLuna, map[string]any{"title": "Retitled after"}},
		{"PUT", "/api/tracks/" + lt.TrackCitta + "/rating", map[string]any{"stars": 1}},
	} {
		r := agent.Do(t, c.method, c.path, c.body, "Idempotency-Key", "mut-"+c.method)
		if r.Status/100 != 2 {
			// the agent token may lack the right to retitle; the mutation is still applied below via SQL
			t.Logf("%s %s: %d %s", c.method, c.path, r.Status, r.Body)
		}
	}
	if _, err := rig.Store.W.Exec(`UPDATE tracks SET title = 'Retitled after' WHERE id = ?`, lt.TrackLuna); err != nil {
		t.Fatal(err)
	}
	if now := agent.OK(t, "GET", "/api/tracks/"+lt.TrackLuna, nil); cmp.Diff(before[paths[0]], now) == "" {
		t.Fatal("the live library did not change")
	}

	// restore into an empty temp dir, through the CLI function the command calls
	into := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(into, 0o750); err != nil {
		t.Fatal(err)
	}
	rep, err := backup.Restore(context.Background(), snapDir, into, backup.RestoreOptions{Now: func() time.Time { return lt.FixedNow.Add(time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts["tracks"] == 0 {
		t.Fatalf("report = %+v", rep)
	}

	svc, restored := restoredRig(t, into)
	for _, p := range paths {
		if diff := cmp.Diff(before[p], restored.json(t, p)); diff != "" {
			t.Errorf("%s differs from the pre-mutation response (-before +restored):\n%s", p, diff)
		}
	}

	// every master's sha256 equals its row
	rows, err := svc.Deps.Store.R.Query(`SELECT master_path, master_sha256 FROM tracks`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var p, sha string
		if err := rows.Scan(&p, &sha); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(into, p))
		if err != nil || library.SHA256Hex(b) != sha {
			t.Errorf("master %s: %v", p, err)
		}
		n++
	}
	_ = rows.Close()
	if n == 0 {
		t.Fatal("no masters")
	}

	// an MP3 media link answers 206 for a Range request
	media := restored.json(t, "/api/tracks/"+lt.TrackLuna+"/media").(map[string]any)
	u := media["mp3"].(map[string]any)["url"].(string)
	code, hdr, body := bearer{restored.url, ""}.rangeGet(t, u, "bytes=0-3")
	if code != http.StatusPartialContent || len(body) != 4 || hdr.Get("Content-Range") == "" {
		t.Errorf("range GET = %d %q %v", code, body, hdr)
	}

	// the snapshot's run row is completed, and the restore is audited
	var state string
	if err := svc.Deps.Store.R.QueryRow(`SELECT state FROM backup_runs WHERE snapshot = ?`, snapshot).Scan(&state); err != nil || state != "completed" {
		t.Errorf("snapshot run row: %q %v", state, err)
	}
	page, err := svc.Audit.List(context.Background(), audit.Filter{Op: "cli:restore"})
	if err != nil || len(page.Items) != 1 || page.Items[0].PrincipalID != "system:cli" || page.Items[0].Transport != "cli" {
		t.Errorf("audit rows = %+v (%v)", page.Items, err)
	}
}

// rangeGet fetches a relative signed URL (no credentials) with a Range header.
func (b bearer) rangeGet(t *testing.T, rel, rng string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest("GET", b.url+rel, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", rng)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

func TestOps_Backups(t *testing.T) {
	rig := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	reader, _ := rig.Agent(t, auth.ScopeRead)
	writer, _ := rig.Agent(t, auth.ScopeRead, auth.ScopeWrite)

	if r := reader.Do(t, "POST", "/api/backups", nil, "Idempotency-Key", "k1"); r.Status != 403 {
		t.Fatalf("read-only create: %d %s", r.Status, r.Body)
	}
	l := reader.OK(t, "GET", "/api/backups", nil).(map[string]any)
	if l["backup_dir_label"] != "backup" || l["schedule"].(map[string]any)["at"] != "off" {
		t.Fatalf("listing %v", l)
	}
	if strings.Contains(string(mustJSON(t, l)), rig.BackupDir) {
		t.Error("the listing reveals the backup directory")
	}

	// a run elsewhere (another process) blocks a new one
	at := lt.FixedNow.Format(time.RFC3339)
	if _, err := rig.Store.W.Exec(`INSERT INTO backup_runs(id, cause, principal_id, state, started_at, heartbeat_at)
		VALUES ('other-proc', 'cli', 'system:cli', 'running', ?, ?)`, at, at); err != nil {
		t.Fatal(err)
	}
	r := writer.Do(t, "POST", "/api/backups", nil, "Idempotency-Key", "k2")
	var p map[string]any
	if err := json.Unmarshal(r.Body, &p); err != nil || r.Status != 409 || p["code"] != "BACKUP_IN_PROGRESS" {
		t.Fatalf("create while running: %d %s", r.Status, r.Body)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
