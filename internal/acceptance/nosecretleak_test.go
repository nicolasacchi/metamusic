package acceptance_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Canary values of the servertest Canary rigs (servertest.Options.Canary) and of
// the CLI secrets directory this test writes. They are synthetic.
const (
	canaryEdge     = "canary-edge-secret"
	canaryNtfy     = "canary-ntfy-token"
	canaryPassword = "correct horse battery staple" //nolint:gosec // synthetic operator password of the local rig
	canaryRigKey   = "canary-fedcba98fedcba98fedcba98fedcba98"
	canaryCLIKey   = "cli-canary-key-0123456789abcdef!" // exactly 32 bytes
)

// leaks collects secret canaries and scans texts for them. A failure names the
// canary kind and the location, never the value.
type leaks struct {
	t       *testing.T
	secrets []struct{ kind, value string }
	seen    map[string]bool
	scanned int
	queue   []queued
}

type queued struct {
	loc, text string
	allowed   []string
}

func newLeaks(t *testing.T) *leaks { return &leaks{t: t, seen: map[string]bool{}} }

// keyForms are the encodings a raw key could leak in.
func keyForms(raw string) []string {
	b := []byte(raw)
	return []string{raw, hex.EncodeToString(b), base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
		base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b)}
}

func (l *leaks) add(kind string, values ...string) {
	for _, v := range values {
		if len(v) < 8 {
			l.t.Fatalf("canary of kind %s is too short to scan for", kind)
		}
		l.secrets = append(l.secrets, struct{ kind, value string }{kind, v})
	}
}

// scan checks text for every canary; the allowed values are removed first (they
// are the documented places a secret is shown once).
func (l *leaks) scan(loc, text string, allowed ...string) {
	l.queue = append(l.queue, queued{loc, text, allowed})
}

// flush checks everything queued. Scans are deferred so that canaries learned late (the PATs and
// pairing codes of creating responses) are known for texts captured early.
func (l *leaks) flush() {
	l.t.Helper()
	for _, q := range l.queue {
		l.check(q.loc, q.text, q.allowed...)
	}
	l.queue = nil
}

func (l *leaks) check(loc, text string, allowed ...string) {
	l.t.Helper()
	l.scanned++
	for _, a := range allowed {
		if a != "" {
			text = strings.ReplaceAll(text, a, "")
		}
	}
	for _, s := range l.secrets {
		if strings.Contains(text, s.value) {
			k := s.kind + "@" + loc
			if !l.seen[k] {
				l.seen[k] = true
				l.t.Errorf("secret leak: canary kind %q found in %s", s.kind, loc)
			}
		}
	}
}

// responseText renders a recorded exchange the way a client saw it. Headers
// named in skipHeaderValue are dropped when they carry a value listed there.
func responseText(r servertest.Response, dropSetCookieWith string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP %d\n", r.Status)
	keys := make([]string, 0, len(r.Header))
	for k := range r.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range r.Header[k] {
			if k == "Set-Cookie" && dropSetCookieWith != "" && strings.Contains(v, dropSetCookieWith) {
				continue
			}
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	b.WriteString("\n")
	b.Write(r.Body)
	return b.String()
}

// leakClient is one recorded REST client of the run.
type leakClient struct {
	name   string
	c      *servertest.Client
	cookie string // device cookie it presents ("" for others)
}

// lockedBuf is a goroutine-safe buffer for captured logs.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// restArgs renders tool arguments as the REST call of op (path, query and header
// parameters from the OpenAPI operation, the rest as the JSON body).
func restArgs(t *testing.T, a huma.API, op api.OpInfo, args map[string]any) (path string, body any, headers []string) {
	t.Helper()
	var o *huma.Operation
	if item := a.OpenAPI().Paths[op.Path]; item != nil {
		o = map[string]*huma.Operation{"GET": item.Get, "PUT": item.Put, "POST": item.Post, "DELETE": item.Delete, "PATCH": item.Patch}[op.Method]
	}
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

// scriptRest runs the scripted session through one REST client, ignoring
// failures (the responses are what gets scanned).
func scriptRest(t *testing.T, r *servertest.Rig, c *servertest.Client, st *uiState, tag string) {
	t.Helper()
	ops := map[string]api.OpInfo{}
	for _, op := range api.Operations(r.API) {
		ops[op.ID] = op
	}
	for i, id := range append(slices.Clone(uiOrder), "get_audit") {
		op, ok := ops[id]
		if !ok {
			continue
		}
		path, body, headers := restArgs(t, r.API, op, uiScript[id](st))
		if op.Meta.Idempotency == "create" {
			headers = append(headers, "Idempotency-Key", fmt.Sprintf("leak-%s-%d", tag, i))
		}
		resp := c.Do(t, op.Method, path, body, headers...)
		if resp.Status/100 == 2 {
			var v any
			_ = json.Unmarshal(resp.Body, &v)
			st.afterStep(id, v)
		}
	}
}

// scriptMCP runs the scripted session through an MCP session (tool or
// call_operation), scanning every result; failures are tolerated.
func scriptMCP(t *testing.T, r *servertest.Rig, l *leaks, s *mcp.ClientSession, face string, st *uiState) {
	t.Helper()
	tools := map[string]bool{}
	for _, op := range api.Operations(r.API) {
		tools[op.ID] = op.Meta.MCP == "tool"
	}
	lt, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(lt)
	l.scan(face+" tools/list", string(b))
	for _, id := range append(slices.Clone(uiOrder), "get_audit") {
		args := uiScript[id](st)
		for _, generic := range []bool{!tools[id], true} {
			res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: map[bool]string{true: "call_operation", false: id}[generic],
				Arguments: map[bool]any{true: map[string]any{"operation_id": id, "arguments": args}, false: args}[generic]})
			if err != nil {
				l.scan(face+" "+id+" protocol error", err.Error())
				continue
			}
			b, _ := json.Marshal(res)
			l.scan(face+" result of "+id, string(b))
			if !res.IsError {
				var v any
				bs, _ := json.Marshal(res.StructuredContent)
				_ = json.Unmarshal(bs, &v)
				st.afterStep(id, v)
			}
			if !generic && !tools[id] {
				break
			}
		}
	}
	// Failing calls: the error text must not echo anything secret either.
	for _, bad := range []struct {
		name string
		args any
	}{{"get_track", map[string]any{"id": "no-such-track"}}, {"call_operation", map[string]any{"operation_id": "nope"}},
		{"rate_track", map[string]any{"id": st.track, "stars": 99}}} {
		res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: bad.name, Arguments: bad.args})
		if err != nil {
			l.scan(face+" error of "+bad.name, err.Error())
			continue
		}
		b, _ := json.Marshal(res)
		l.scan(face+" error result of "+bad.name, string(b))
	}
}

// dumpTables renders every row of the named tables as text.
func dumpTables(t *testing.T, db *sql.DB, tables ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tb := range tables {
		rows, err := db.Query("SELECT * FROM " + tb) //nolint:gosec // fixed table names of this test
		if err != nil {
			t.Fatalf("dumping %s: %v", tb, err)
		}
		cols, _ := rows.Columns()
		var b strings.Builder
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				if bs, ok := v.([]byte); ok {
					v = string(bs)
				}
				fmt.Fprintf(&b, "%s=%v\t", cols[i], v)
			}
			b.WriteString("\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		out[tb] = b.String()
	}
	return out
}

var leakTables = []string{"audit_log", "idempotency", "exports", "listen_events", "backup_runs", "comments", "devices", "pairing_codes"}

// scanDir scans every small non-media file under dir (backup manifests, snapshot
// listings, exported JSONL) and the raw bytes of database copies.
func scanDir(t *testing.T, l *leaks, label, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".flac", ".mp3", ".zip", ".wav", ".png":
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // files of a temp tree this test created
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		l.scan(label+" file "+rel, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("scanning %s: %v", label, err)
	}
}

// captureLogs routes slog (and log) output to a buffer for the test.
func captureLogs(t *testing.T) *lockedBuf {
	t.Helper()
	buf := &lockedBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// recorded is one response with the client that received it.
type recorded struct {
	cli  leakClient
	resp servertest.Response
}

// harvest scans every response the clients recorded. Dynamic canaries are
// learned first: PAT plaintexts and pairing codes from their creating
// responses, which are also the only places they may appear.
func harvest(t *testing.T, l *leaks, clients []leakClient, extra []recorded) {
	t.Helper()
	var all []recorded
	for _, lc := range clients {
		for _, resp := range lc.c.Responses() {
			all = append(all, recorded{lc, resp})
		}
	}
	all = append(all, extra...)
	allowed := map[int][]string{}
	for i, rc := range all {
		resp := rc.resp
		if resp.Status/100 != 2 || resp.Method != "POST" {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(resp.Body, &m)
		switch resp.Path {
		case "/human/api/tokens":
			if tok, _ := m["token"].(string); tok != "" {
				l.add("pat", tok)
				allowed[i] = []string{tok}
			}
		case "/human/api/devices/pairing-codes":
			var vals []string
			for _, k := range []string{"pair_url", "code"} {
				if v, _ := m[k].(string); v != "" {
					vals = append(vals, v)
				}
			}
			if code, _ := m["code"].(string); code != "" {
				l.add("pairing-code", code, strings.ReplaceAll(code, "-", ""))
				vals = append(vals, code, strings.ReplaceAll(code, "-", ""))
			}
			allowed[i] = vals
		}
	}
	for i, rc := range all {
		resp := rc.resp
		l.scan(fmt.Sprintf("response of %s %s %s (status %d)", rc.cli.name, resp.Method, strings.SplitN(resp.Path, "?", 2)[0], resp.Status),
			responseText(resp, rc.cli.cookie), allowed[i]...)
	}
}

// rawClient is a plain HTTP client that records its exchanges (used for the
// pairing and login flows servertest does not expose).
type rawClient struct {
	t    *testing.T
	base string
	name string
	hc   *http.Client
	recs []recorded
}

func newRawClient(t *testing.T, base, name string) *rawClient {
	return &rawClient{t: t, base: base, name: name, hc: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *rawClient) do(method, path, body, cookie string, hdr ...string) servertest.Response {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("User-Agent", "leak-test/1.0")
	if cookie != "" {
		req.Header.Set("Cookie", auth.DeviceCookie+"="+cookie)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	out := servertest.Response{Method: method, Path: path, Status: resp.StatusCode, Header: resp.Header.Clone(), Body: b}
	c.recs = append(c.recs, recorded{leakClient{name: c.name, cookie: cookie}, out})
	return out
}

func setCookie(r servertest.Response, name string) string {
	for _, v := range r.Header.Values("Set-Cookie") {
		if strings.HasPrefix(v, name+"=") {
			return strings.SplitN(strings.SplitN(v, ";", 2)[0], "=", 2)[1]
		}
	}
	return ""
}

// pairingFlow redeems a pairing code over the real /auth/pair endpoint and uses the device cookie,
// including a sliding re-issue after the rig clock moves on. It returns the exchanges it recorded.
func pairingFlow(t *testing.T, l *leaks, r *servertest.Rig, human *servertest.Client) []recorded {
	t.Helper()
	res := human.Do(t, "POST", "/human/api/devices/pairing-codes", map[string]any{"name": "Leak phone two"}, "Idempotency-Key", "leak-pair-flow")
	if res.Status != http.StatusCreated {
		t.Fatalf("create_pairing_code: status %d", res.Status)
	}
	var m map[string]any
	_ = json.Unmarshal(res.Body, &m)
	code, _ := m["code"].(string)
	l.add("pairing-code", code, strings.ReplaceAll(code, "-", ""))
	c := newRawClient(t, r.URL, "pairing-flow")
	for _, wrong := range []string{"AAAA-AAAA", "not a code", "ZZZZ-9999"} {
		c.do("POST", "/auth/pair", "code="+url.QueryEscape(wrong), "")
	}
	ok := c.do("POST", "/auth/pair", "code="+url.QueryEscape(strings.ToLower(code)), "")
	cookie := setCookie(ok, auth.DeviceCookie)
	if ok.Status != http.StatusSeeOther || cookie == "" {
		t.Fatalf("pairing: status %d, cookie set %v", ok.Status, cookie != "")
	}
	l.add("device-cookie", cookie)
	c.recs[len(c.recs)-1].cli.cookie = cookie // the Set-Cookie of /auth/pair is where the cookie is shown
	// The same code twice: the second redemption fails and must not echo it.
	c.do("POST", "/auth/pair", "code="+url.QueryEscape(strings.ReplaceAll(code, "-", "")), "")
	for _, p := range []string{"/start", "/pair", "/pair.js", "/api/me"} {
		c.do("GET", p, "", cookie)
	}
	r.Clock.Advance(2 * time.Hour) // the server-side expiry slides hourly: the cookie is re-issued
	c.do("GET", "/api/me", "", cookie)
	c.do("GET", "/api/tracks?limit=5", "", cookie)
	c.do("POST", "/api/devices/self/revoke", "", cookie, "Idempotency-Key", "leak-self-revoke")
	c.do("GET", "/api/me", "", cookie) // revoked: refused
	return c.recs
}

// waitBackupsIdle polls list_backups until no run is in flight.
func waitBackupsIdle(t *testing.T, c *servertest.Client) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		busy := false
		var m map[string]any
		_ = json.Unmarshal(c.Do(t, "GET", "/api/backups", nil).Body, &m)
		runs, _ := m["runs"].([]any)
		for _, x := range runs {
			if obj(x)["state"] == "running" {
				busy = true
			}
		}
		if !busy {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a backup run did not finish")
}

// failingNtfy is an ntfy endpoint that always answers 500 and records what it was sent.
type failingNtfy struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []string // path, headers except Authorization, body
}

func newFailingNtfy(t *testing.T) *failingNtfy {
	f := &failingNtfy{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var h strings.Builder
		for k, v := range r.Header {
			if k != "Authorization" {
				fmt.Fprintf(&h, "%s: %s\n", k, strings.Join(v, ","))
			}
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, r.URL.Path+"\n"+h.String()+"\n"+string(b))
		f.mu.Unlock()
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *failingNtfy) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

var (
	buildOnce sync.Once
	buildBin  string
	buildErr  error
)

// metamusicBinary builds the CLI once per test binary.
func metamusicBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-acceptance-bin-")
		if err != nil {
			buildErr = err
			return
		}
		buildBin = filepath.Join(dir, "metamusic")
		cmd := exec.Command("go", "build", "-o", buildBin, "github.com/nicolasacchi/metamusic/cmd/metamusic")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return buildBin
}

// cli runs the real binary against a directory layout of its own.
type cli struct {
	t                  *testing.T
	bin                string
	env                []string
	data, secrets, bkp string
	failingNtfy        *failingNtfy
	scanned            []string // labels and text of every captured output
	scannedTexts       []string
}

func newCLI(t *testing.T, tpl archivetest.Template, f *failingNtfy) *cli {
	t.Helper()
	c := &cli{t: t, bin: metamusicBinary(t), failingNtfy: f}
	c.data = tpl.Copy(t)
	root := t.TempDir()
	c.secrets, c.bkp = filepath.Join(root, "secrets"), filepath.Join(root, "backup")
	if err := os.MkdirAll(c.secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{
		"secret_key": hex.EncodeToString([]byte(canaryCLIKey)) + "\n", "edge_secret": canaryEdge + "\n", "ntfy_token": canaryNtfy + "\n",
	} {
		if err := os.WriteFile(filepath.Join(c.secrets, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c.env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + root, "MM_AUTH=local", "MM_DATA_DIR=" + c.data, "MM_SECRETS_DIR=" + c.secrets,
		"MM_BACKUP_DIR=" + c.bkp, "MM_BACKUP_AT=off", "MM_PUBLIC_URL=http://mm.example.com", "MM_NTFY_URL=" + f.srv.URL + "/mm-test",
	}
	return c
}

func (c *cli) command(env ...string) *exec.Cmd {
	cmd := exec.Command(c.bin) //nolint:gosec // the binary this test built
	cmd.Env = append(slices.Clone(c.env), env...)
	return cmd
}

// run executes one command and returns stdout, stderr and the exit code; both streams are kept for scanning.
func (c *cli) run(stdin string, args ...string) (string, string, int) {
	c.t.Helper()
	cmd := c.command()
	cmd.Args = append([]string{c.bin}, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Stdin = strings.NewReader(stdin)
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			c.t.Fatalf("running %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	label := "cli " + args[0]
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		label += " " + args[1]
	}
	c.scanned = append(c.scanned, label+" stdout", label+" stderr")
	c.scannedTexts = append(c.scannedTexts, out.String(), errb.String())
	return out.String(), errb.String(), code
}

// TestNoSecretLeak is the authority for the dynamic canaries (the E2E leak scanner covers the
// static ones in the browser run): nothing secret appears in any response, result, log,
// notification, dump, manifest or CLI output, except where the secret is shown once by design.
func TestNoSecretLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	logs := captureLogs(t)
	l := newLeaks(t)
	l.add("secret_key", append(keyForms(canaryRigKey), keyForms(canaryCLIKey)...)...)
	l.add("edge_secret", canaryEdge)
	l.add("ntfy_token", canaryNtfy)
	l.add("operator_password", canaryPassword)
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	all4 := []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}

	// ---- proxy-mode rig: REST (human, device, agent), MCP over HTTP, pairing ----
	r := servertest.New(t, servertest.Options{Canary: true, Archive: &tpl})
	human := r.Human(t)
	clients := []leakClient{{name: "human", c: human}}
	trackID, timedID := track(t, human, false), track(t, human, true)
	state := func() *uiState {
		d := obj(obj(human.OK(t, "GET", "/api/drafts?limit=1", nil))["items"].([]any)[0])["id"].(string)
		return &uiState{track: trackID, timed: timedID, draft: d}
	}

	agentTok, _, err := r.Services.Tokens.Create(context.Background(), "leak-agent", all4)
	if err != nil {
		t.Fatal(err)
	}
	l.add("pat", agentTok)
	agent := servertest.Bearer(r, agentTok)
	clients = append(clients, leakClient{name: "agent", c: agent})
	dev, devCookie := r.DeviceWithCookie(t, "Leak phone")
	l.add("device-cookie", devCookie)
	clients = append(clients, leakClient{name: "device", c: dev, cookie: devCookie})

	// The operator: the scripted session plus every human-only operation.
	scriptRest(t, r, human, state(), "human")
	tokResp := human.Do(t, "POST", "/human/api/tokens", map[string]any{"name": "leak-created", "scopes": []string{"read", "write"}}, "Idempotency-Key", "leak-tok")
	if tokResp.Status != http.StatusCreated {
		t.Fatalf("create_token: status %d", tokResp.Status)
	}
	human.Do(t, "POST", "/human/api/tokens", map[string]any{"name": "leak-created", "scopes": []string{"read", "write"}}, "Idempotency-Key", "leak-tok") // replay
	var created map[string]any
	_ = json.Unmarshal(tokResp.Body, &created)
	human.Do(t, "GET", "/human/api/tokens", nil)
	human.Do(t, "GET", "/human/api/devices", nil)
	if id, _ := created["id"].(string); id != "" {
		human.Do(t, "POST", "/human/api/tokens/"+id+"/revoke", nil)
	}
	scriptRest(t, r, agent, state(), "agent")
	scriptRest(t, r, dev, state(), "device")
	for _, p := range []string{"/human/api/tokens", "/human/api/devices"} { // refused for non-humans
		agent.Do(t, "GET", p, nil)
		dev.Do(t, "GET", p, nil)
	}
	for _, p := range []string{"/api/tokens", "/api/audit?limit=200", "/api/system", "/api/me"} {
		agent.Do(t, "GET", p, nil)
	}
	spec := agent.Do(t, "GET", "/api/openapi.json", nil)
	docs := agent.Do(t, "GET", "/api/docs", nil)
	if spec.Status != 200 || docs.Status != 200 {
		t.Fatalf("openapi %d, docs %d", spec.Status, docs.Status)
	}
	l.scan("openapi.json", string(spec.Body))
	l.scan("/api/docs", string(docs.Body))
	static, err := os.ReadFile("../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	l.scan("committed openapi.json", string(static))

	mcpSess := mcpWithToken(t, r, agentTok)
	scriptMCP(t, r, l, mcpSess, "mcp", state())

	extra := pairingFlow(t, l, r, human)

	// A failing backup notifies; the rig's fake ntfy server records it.
	waitBackupsIdle(t, agent)
	scanDir(t, l, "rig backups", r.BackupDir)
	if err := os.RemoveAll(r.BackupDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.BackupDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent.Do(t, "POST", "/api/backups", nil, "Idempotency-Key", "leak-failing-backup")
	waitBackupsIdle(t, agent)
	agent.Do(t, "GET", "/api/backups", nil)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(r.Ntfy.Requests()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if len(r.Ntfy.Requests()) == 0 {
		t.Fatal("the rig's ntfy server received no notification")
	}
	for i, n := range r.Ntfy.Requests() {
		l.scan(fmt.Sprintf("rig ntfy request %d (path and body)", i+1), n.Path+"\n"+n.Body)
	}
	for tbl, txt := range dumpTables(t, r.Store.R, leakTables...) {
		l.scan("rig database table "+tbl, txt)
	}

	// ---- local-mode rig: login, step-up, the human area ----
	r2 := servertest.New(t, servertest.Options{Canary: true, Mode: "local", Fixture: true, Files: true})
	h2 := r2.Human(t)
	raw2 := newRawClient(t, r2.URL, "local-login")
	raw2.do("POST", "/auth/login", url.Values{"username": {"canary-human"}, "password": {"wrong password value"}}.Encode(), "")
	raw2.do("POST", "/auth/login", url.Values{"username": {"canary-human"}, "password": {canaryPassword}}.Encode(), "")
	h2.Do(t, "POST", "/auth/step-up", url.Values{"password": {"wrong password value"}, "next": {"/human/tokens"}}.Encode(), "Content-Type", "application/x-www-form-urlencoded")
	r2.StepUp(t, h2)
	for _, p := range []string{"/human/tokens", "/human/devices", "/auth/login", "/auth/step-up", "/api/me"} {
		h2.Do(t, "GET", p, nil)
	}
	if res := h2.Do(t, "POST", "/human/api/tokens", map[string]any{"name": "local-created", "scopes": []string{"read"}}, "Idempotency-Key", "leak-local-tok"); res.Status != http.StatusCreated {
		t.Fatalf("local create_token: status %d", res.Status)
	}
	h2.Do(t, "POST", "/human/api/devices/pairing-codes", map[string]any{"name": "Local phone"}, "Idempotency-Key", "leak-local-pair")
	h2.Do(t, "GET", "/api/tracks?limit=5", nil)
	clients = append(clients, leakClient{name: "local human", c: h2})
	extra = append(extra, raw2.recs...)
	for tbl, txt := range dumpTables(t, r2.Store.R, leakTables...) {
		l.scan("local rig database table "+tbl, txt)
	}

	// ---- the CLI and the real stdio server ----
	failing := newFailingNtfy(t)
	c := newCLI(t, tpl, failing)
	patOut, _, code := c.run("", "token", "create", "--name", "cli-main", "--scopes", "read,write")
	if code != 0 {
		t.Fatalf("token create: exit %d", code)
	}
	cliPAT := strings.TrimSpace(patOut)
	l.add("pat", cliPAT)
	otherOut, otherErr, _ := c.run("", "token", "create", "--name", "cli-other", "--scopes", "read")
	l.add("pat", strings.TrimSpace(otherOut))
	listOut, _, _ := c.run("", "token", "list")
	var otherID string
	for _, line := range strings.Split(listOut, "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[1] == "cli-other" {
			otherID = f[0]
		}
	}
	if otherID == "" {
		t.Fatalf("token list lacks cli-other: stderr %q", otherErr)
	}
	c.run("", "token", "revoke", otherID)
	exportDir := filepath.Join(t.TempDir(), "export")
	c.run("", "export", "--tracks", "--out", exportDir)
	c.run("", "validate-batch", filepath.Join(exportDir, "songs.jsonl"))
	c.run("", "backup")
	c.run("", "backup", "list")
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, code := c.run("", "backup", "--to", filepath.Join(blocker, "sub")); code == 0 {
		t.Error("a backup into an unusable directory must fail")
	}
	snaps, _ := os.ReadDir(c.bkp)
	for _, s := range snaps {
		if s.IsDir() {
			c.run("", "restore", "--from", filepath.Join(c.bkp, s.Name()), "--into", filepath.Join(t.TempDir(), "restored"))
			break
		}
	}
	if reqs := failing.requests(); len(reqs) == 0 {
		t.Error("the CLI sent no notification to the failing ntfy server")
	} else {
		for i, rq := range reqs {
			l.scan(fmt.Sprintf("failing ntfy request %d (path, headers, body)", i+1), rq)
		}
	}

	stdioCmd := c.command("MM_TOKEN=" + cliPAT)
	stdioCmd.Args = []string{c.bin, "mcp-stdio"}
	var stdioErr lockedBuf
	stdioCmd.Stderr = &stdioErr
	stdio, err := mcp.NewClient(&mcp.Implementation{Name: "leak", Version: "0"}, nil).Connect(context.Background(), &mcp.CommandTransport{Command: stdioCmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	scriptMCP(t, r, l, stdio, "stdio", state())
	_ = stdio.Close()
	l.scan("stdio process stderr (slog)", stdioErr.String())

	for i, txt := range c.scannedTexts {
		allowed := []string{}
		if c.scanned[i] == "cli token create stdout" {
			allowed = append(allowed, strings.TrimSpace(txt))
		}
		l.scan(c.scanned[i], txt, allowed...)
	}
	scanDir(t, l, "cli export", exportDir)
	scanDir(t, l, "cli backups", c.bkp)
	cliStore, err := store.Open(context.Background(), filepath.Join(c.data, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	for tbl, txt := range dumpTables(t, cliStore.R, leakTables...) {
		l.scan("cli database table "+tbl, txt)
	}
	_ = cliStore.Close()
	scanDir(t, l, "cli data directory", c.data)

	// ---- everything the HTTP clients saw, the rig's backups and the captured logs ----
	harvest(t, l, clients, extra)
	scanDir(t, l, "rig data directory", r.DataDir)
	l.scan("captured slog output of serve", logs.String())
	l.flush()
	if l.scanned < 200 {
		t.Errorf("only %d texts were scanned; the exercise is too thin", l.scanned)
	}
}
