package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// TestSubsonic_RealMediaSources: the handler app.Build wires streams a real rendered cover (PNG) and a real tagged
// download (the master's frames under current tags), not the lane fakes (M4a-B3).
func TestSubsonic_RealMediaSources(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, Files: true})
	frames := installRealMaster(t, r, lt.TrackLuna)

	c := r.Subsonic(t, subsonictest.ClientOpts{Name: "Real media", Write: true})
	cov := c.Call(t, "getCoverArt", url.Values{"id": {lt.TrackLuna}, "size": {"300"}}, subsonictest.CallOpts{})
	if cov.HTTP != 200 || cov.Header.Get("Content-Type") != "image/png" || !bytes.HasPrefix(cov.Body, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("getCoverArt: %d %q, %d bytes", cov.HTTP, cov.Header.Get("Content-Type"), len(cov.Body))
	}
	dl := c.Call(t, "download", url.Values{"id": {lt.TrackLuna}}, subsonictest.CallOpts{})
	if dl.HTTP != 200 || dl.Header.Get("Content-Type") != "audio/flac" {
		t.Fatalf("download: %d %q: %s", dl.HTTP, dl.Header.Get("Content-Type"), dl.Body)
	}
	info, err := flacmeta.Read(bytes.NewReader(dl.Body), int64(len(dl.Body)))
	if err != nil {
		t.Fatalf("download is not a FLAC: %v", err)
	}
	tags := map[string]string{}
	for _, cm := range info.Comments {
		if k, v, ok := bytes.Cut([]byte(cm), []byte("=")); ok {
			tags[string(k)] = string(v)
		}
	}
	if tags["TITLE"] == "ingest title" || tags["TITLE"] == "" || tags["MM_TAKE_ID"] != lt.TrackLuna {
		t.Errorf("download tags = %v, want the library's current title and the take id", tags)
	}
	if !bytes.Contains(dl.Body, frames) {
		t.Error("download does not carry the master's audio frames unchanged")
	}
}

func writeMaster(t *testing.T, dataDir, rel string, b []byte) {
	t.Helper()
	full := filepath.Join(dataDir, filepath.FromSlash(rel))
	if err := os.Chmod(full, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, b, 0o444); err != nil {
		t.Fatal(err)
	}
}

// TestNoSecretLeak_M4a (C113 row): a Subsonic client's API key and app password appear only in the 201 that issued
// them (null on the idempotent replay), and an OAuth access token appears nowhere but the Authorization header. Scans
// every REST and MCP response, the Subsonic answers (including refusals), the stored tables, the captured logs, the
// ntfy bodies and the backups.
func TestNoSecretLeak_M4a(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	logs := captureLogs(t)
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, M4a: true, OIDC: true, Files: true, Canary: true})
	l := newLeaks(t)
	l.add("secret_key", keyForms(canaryRigKey)...)
	l.add("edge_secret", canaryEdge)
	l.add("ntfy_token", canaryNtfy)
	l.add("operator_password", canaryPassword)
	human := r.Human(t)

	// the Subsonic client: both credentials are shown once
	body := map[string]any{"name": "Leak client", "write": true}
	issue := human.Do(t, "POST", "/api/subsonic-clients", body, "Idempotency-Key", "leak-sub-1")
	if issue.Status != http.StatusCreated {
		t.Fatalf("create_subsonic_client: %d %s", issue.Status, issue.Body)
	}
	var made struct {
		APIKey   *string `json:"api_key"`
		Password *string `json:"password"`
		Username string  `json:"username"`
	}
	if err := json.Unmarshal(issue.Body, &made); err != nil || made.APIKey == nil || made.Password == nil {
		t.Fatalf("the issuing response lacks a credential: %v %s", err, issue.Body)
	}
	l.add("subsonic-api-key", *made.APIKey)
	l.add("subsonic-password", *made.Password)
	replay := human.Do(t, "POST", "/api/subsonic-clients", body, "Idempotency-Key", "leak-sub-1")
	var again struct {
		APIKey   *string `json:"api_key"`
		Password *string `json:"password"`
	}
	if err := json.Unmarshal(replay.Body, &again); err != nil || again.APIKey != nil || again.Password != nil {
		t.Errorf("the replay of the issuing call holds a credential: %v %d", err, replay.Status)
	}
	human.Do(t, "GET", "/api/subsonic-clients", nil)
	human.Do(t, "GET", "/api/connectors", nil)

	// the Subsonic face with those credentials, and refusals
	sc := &subsonictest.Client{BaseURL: r.URL, Username: made.Username, APIKey: *made.APIKey, Password: *made.Password}
	var subsonic []recorded
	record := func(name string, resp *subsonictest.Response) {
		subsonic = append(subsonic, recorded{leakClient{name: "subsonic"}, servertest.Response{
			Method: "GET", Path: name, Status: resp.HTTP, Header: resp.Header, Body: resp.Body}})
	}
	for _, ep := range []string{"ping", "getOpenSubsonicExtensions", "getMusicDirectory"} {
		record(ep, sc.Call(t, ep, url.Values{"id": {lt.TrackLuna}}, subsonictest.CallOpts{JSON: true}))
		record(ep+"-key", sc.Call(t, ep, url.Values{"id": {lt.TrackLuna}}, subsonictest.CallOpts{JSON: true, APIKey: true}))
	}
	if p := sc.Call(t, "ping", nil, subsonictest.CallOpts{JSON: true}); p.Status != "ok" {
		t.Fatalf("ping with the issued credentials: %d %s", p.HTTP, p.Body)
	}
	if p := sc.Call(t, "ping", nil, subsonictest.CallOpts{JSON: true, APIKey: true}); p.Status != "ok" {
		t.Fatalf("ping with the issued API key: %d %s", p.HTTP, p.Body)
	}
	bad := &subsonictest.Client{BaseURL: r.URL, Username: made.Username, Password: "not-the-password"}
	record("ping-bad", bad.Call(t, "ping", nil, subsonictest.CallOpts{JSON: true}))

	// an OAuth connector: a minted access token only ever travels in the Authorization header
	tok := r.OIDC.Mint(t, servertest.OIDCClaudeClient, servertest.OIDCUser, []string{"openid", "metamusic"}, []string{r.URL + "/mcp"})
	l.add("oauth-access-token", tok)
	if i := strings.LastIndexByte(tok, '.'); i > 0 {
		l.add("oauth-token-signature", tok[i+1:])
	}
	sess := mcpWithToken(t, r, tok)
	var mcpResults []recorded
	for _, c := range []struct {
		op   string
		args map[string]any
	}{{"get_track", map[string]any{"id": lt.TrackLuna}}, {"get_media_links", map[string]any{"id": lt.TrackLuna}}, {"list_connector_grants", nil}} {
		res := rawTool(t, sess, c.op, c.args, true)
		if res.IsError != (c.op == "list_connector_grants") { // a connector never holds scope admin: a refusal, not a leak
			t.Fatalf("connector call %s: %s", c.op, resultText(res))
		}
		mcpResults = append(mcpResults, recorded{leakClient{name: "connector"}, servertest.Response{Method: "CALL", Path: c.op, Status: 200, Body: []byte(resultText(res))}})
	}
	// a connector presenting garbage is refused without echoing it
	badTok := tok[:len(tok)-4] + "AAAA"
	l.add("oauth-forged-token", badTok)
	req, err := http.NewRequest(http.MethodPost, r.URL+"/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+badTok)
	req.Header.Set("Content-Type", "application/json")
	rr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := io.ReadAll(rr.Body)
	_ = rr.Body.Close()
	if rr.StatusCode != http.StatusUnauthorized {
		t.Errorf("forged token: %d, want 401", rr.StatusCode)
	}
	mcpResults = append(mcpResults, recorded{leakClient{name: "forged"}, servertest.Response{Method: "POST", Path: "/mcp", Status: rr.StatusCode, Header: rr.Header, Body: rb}})

	// scan: the issuing 201 may hold both credentials, nothing else may
	allowed := map[string][]string{"/api/subsonic-clients": {*made.APIKey, *made.Password}}
	for _, resp := range human.Responses() {
		var a []string
		if resp.Status == http.StatusCreated && resp.Method == "POST" {
			a = allowed[strings.SplitN(resp.Path, "?", 2)[0]]
		}
		l.scan(fmt.Sprintf("response of human %s %s (status %d)", resp.Method, strings.SplitN(resp.Path, "?", 2)[0], resp.Status), responseText(resp, ""), a...)
	}
	for _, rc := range append(subsonic, mcpResults...) {
		l.scan(fmt.Sprintf("%s answer of %s", rc.cli.name, rc.resp.Path), responseText(rc.resp, ""))
	}
	waitBackupsIdle(t, human)
	tables := dumpTables(t, r.Store.R, append([]string{"connector_grants", "subsonic_clients", "subsonic_salts", "subsonic_bookmarks", "subsonic_play_queue", "tracks"}, leakTables...)...)
	for name, text := range tables {
		l.scan("table "+name, text)
	}
	l.scan("captured logs", logs.String())
	for i, nr := range r.Ntfy.Requests() {
		l.scan(fmt.Sprintf("ntfy request %d body", i), nr.Body)
		l.scan(fmt.Sprintf("ntfy request %d path", i), nr.Path)
	}
	scanDir(t, l, "rig backups", r.BackupDir)
	l.flush()
	if l.scanned < 20 {
		t.Errorf("scanned only %d texts", l.scanned)
	}
}

// TestGETNeverWritesDB_M4a: the M4a GETs (offline set, prompts, download and media links, the management lists, the
// Subsonic reads) leave the database as it was: for the proxy human data_version and every table checksum, for an
// admin agent and a Subsonic client everything except the usage bookkeeping the credential itself records
// (cover and download files may appear).
func TestGETNeverWritesDB_M4a(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, M4a: true, Files: true})
	human := r.Human(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeAdmin)
	conn, err := r.Store.R.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	dataVersion := func() int64 {
		var v int64
		if err := conn.QueryRowContext(context.Background(), `PRAGMA data_version`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	id := lt.TrackLuna
	installRealMaster(t, r, id)
	gets := []string{
		"/api/offline-set", "/api/prompts/session_review", "/api/prompts/critique_track?track_id=" + id,
		"/api/prompts/write_variant?track_id=" + id, "/api/tracks/" + id + "/download", "/api/tracks/" + id + "/media",
		"/api/tracks/" + id + "/context", "/api/tracks?limit=5",
	}
	run := func(c *servertest.Client) {
		t.Helper()
		for _, p := range gets {
			if resp := c.Do(t, "GET", p, nil); resp.Status != 200 {
				t.Fatalf("GET %s: %d %s", p, resp.Status, resp.Body)
			}
		}
	}
	before, version := tableChecksums(t, r.Store.R, nil, nil), dataVersion()
	run(human)
	if got := dataVersion(); got != version {
		t.Errorf("PRAGMA data_version changed (%d -> %d) during the operator's GETs", version, got)
	}
	if diff := diffSums(before, tableChecksums(t, r.Store.R, nil, nil)); diff != "" {
		t.Errorf("the operator's GETs changed %s", diff)
	}

	// the two management lists are audited by design (D-M4a-24: every call leaves an audit row): nothing else changes
	before = tableChecksums(t, r.Store.R, map[string]bool{"audit_log": true}, nil)
	for _, p := range []string{"/api/connectors", "/api/subsonic-clients"} {
		if resp := human.Do(t, "GET", p, nil); resp.Status != 200 {
			t.Fatalf("GET %s: %d %s", p, resp.Status, resp.Body)
		}
	}
	if diff := diffSums(before, tableChecksums(t, r.Store.R, map[string]bool{"audit_log": true}, nil)); diff != "" {
		t.Errorf("the management lists changed %s besides the audit log", diff)
	}

	skip := map[string]bool{"audit_log": true, "subsonic_salts": true}
	drop := map[string][]string{"api_tokens": {"last_used_at"}, "subsonic_clients": {"last_seen_at", "last_client"}}
	run(agent)
	for _, p := range []string{"/api/connectors", "/api/subsonic-clients"} {
		agent.Do(t, "GET", p, nil)
	}
	sc := r.Subsonic(t, subsonictest.ClientOpts{Name: "Reader", Write: false})
	before = tableChecksums(t, r.Store.R, skip, drop) // the client's creation is not a GET
	for _, ep := range []struct {
		name string
		q    url.Values
	}{
		{"ping", nil}, {"getSong", url.Values{"id": {id}}}, {"getMusicDirectory", url.Values{"id": {id}}},
		{"getCoverArt", url.Values{"id": {id}}}, {"download", url.Values{"id": {id}}}, {"stream", url.Values{"id": {id}}},
		{"getLyricsBySongId", url.Values{"id": {id}}}, {"getPlaylists", nil}, {"getStarred2", nil}, {"getAlbumList2", url.Values{"type": {"newest"}}},
	} {
		if resp := sc.Call(t, ep.name, ep.q, subsonictest.CallOpts{JSON: true}); resp.HTTP != 200 {
			t.Fatalf("subsonic %s: %d %s", ep.name, resp.HTTP, resp.Body)
		}
	}
	if diff := diffSums(before, tableChecksums(t, r.Store.R, skip, drop)); diff != "" {
		t.Errorf("the agent's and the Subsonic client's GETs changed %s", diff)
	}
}

// installRealMaster replaces the fixture master of one take by a header-valid FLAC and updates the frozen columns the
// way an ingest would have written them; it returns the audio frames written.
func installRealMaster(t *testing.T, r *servertest.Rig, id string) []byte {
	t.Helper()
	frames := bytes.Repeat([]byte{0xff, 0xf8, 0x01}, 3000)
	master := flactest.Build(flactest.Spec{TotalSamples: 96000, MD5: [16]byte{1, 2, 3}, Vendor: "ingest", SeekPoints: 4, PaddingBytes: 512,
		Comments: []string{"TITLE=ingest title", "MM_TAKE_ID=" + id}, Frames: frames})
	var p string
	if err := r.Store.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	if _, err := r.Store.W.Exec(`DROP TRIGGER IF EXISTS tracks_frozen`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store.W.Exec(`UPDATE tracks SET master_sha256 = ?, master_bytes = ?, frames_sha256 = ? WHERE id = ?`,
		sum(master), len(master), sum(frames), id); err != nil {
		t.Fatal(err)
	}
	writeMaster(t, r.DataDir, p, master)
	return frames
}

// TestNoPendingM4aStubs: no M4a stub is left. Statically, no source file carries a STUB(M4a-...) marker or a
// NOT_IMPLEMENTED problem naming an M4a task; dynamically, every parameterless read operation of the registry answers
// something other than 501 for an admin agent.
func TestNoPendingM4aStubs(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	marker, task := "STUB(M4a-", `"task": "M4a-`
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "m4a_registry_test.go") {
				return err
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			if bytes.Contains(b, []byte(marker)) || bytes.Contains(b, []byte(task)) {
				t.Errorf("%s still carries an M4a stub marker", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, M4a: true, Files: true})
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeAdmin)
	checked := 0
	for _, op := range api.Operations(r.API) {
		if op.Method != "GET" || strings.Contains(op.Path, "{") || !strings.HasPrefix(op.Path, "/api/") {
			continue
		}
		resp := agent.Do(t, "GET", op.Path, nil)
		checked++
		if resp.Status == http.StatusNotImplemented {
			t.Errorf("%s (%s) answers 501 NOT_IMPLEMENTED: %s", op.ID, op.Path, resp.Body)
		}
	}
	if checked < 20 {
		t.Errorf("only %d read operations probed", checked)
	}
}
