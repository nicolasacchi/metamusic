// Package servertest builds complete in-process servers for tests (C42): every
// service comes from app.Build under one advanceable clock, so REST, MCP and
// stdio faces can be driven side by side. It is test-only and must never be
// linked into the binary (TestNoTestHelpersInBinary).
package servertest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/mcpgen"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options tunes New; the zero value is a proxy-mode rig with an empty library.
type Options struct {
	Mode    string                // "proxy" (default) | "local"
	Fixture bool                  // librarytest.Fixture (M1 rows plus the M2a rows)
	Archive *archivetest.Template // an imported synthetic archive (archivetest.SyntheticArchive), copied into the DataDir; exclusive with Fixture
	Files   bool                  // librarytest.WriteFiles: files whose sha256 match the rows
	Clock   *Clock                // nil => fixed at librarytest.FixedNow
	IDSeed  string                // deterministic UUIDv7-shaped id sequence ("" => "0190")
	Limits  ratelimit.Config      // nil => effectively unlimited
	Canary  bool                  // canary secret_key / edge_secret / ntfy_token / human username; ntfy fake server
}

// Rig is one running server.
type Rig struct {
	URL, DataDir, BackupDir string
	Store                   *store.Store
	API                     huma.API
	Clock                   *Clock
	Services                *app.Services
	Ntfy                    *FakeNtfy

	edge, humanUser, mode string
	password              string
	gatedAPI              huma.API
	stdioHandler          http.Handler
}

const (
	idBase      = 0x9000
	unlimitedPM = 1e6
)

// Synthetic key material, built at run time so no literal looks like a credential.
var (
	testKey   = strings.Repeat("0123456789abcdef", 2)
	canaryKey = "canary-" + strings.Repeat("fedcba98", 4)
)

// New starts a rig. Cleanup closes services (waiting for backups) before the
// temporary directories are removed.
func New(tb testing.TB, o Options) *Rig {
	auth.UseCheapHashForTests() // argon2id at production cost dominates suite time; production default pinned in internal/auth
	store.UseMigratedTemplateForTests()
	tb.Helper()
	ctx := context.Background()
	mode := o.Mode
	if mode == "" {
		mode = "proxy"
	}
	if mode != "proxy" && mode != "local" {
		tb.Fatalf("servertest: unknown mode %q", mode)
	}
	clock := o.Clock
	if clock == nil {
		clock = NewClock(librarytest.FixedNow)
	}
	root := tb.TempDir()
	r := &Rig{
		DataDir: filepath.Join(root, "data"), BackupDir: filepath.Join(root, "backup"),
		Clock: clock, edge: "edge", humanUser: "alice", mode: mode, password: "correct horse battery staple",
	}
	for _, d := range []string{r.DataDir, r.BackupDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			tb.Fatal(err)
		}
	}
	key := []byte(testKey)
	sec := config.Secrets{Key: key, EdgeSecret: r.edge}
	if o.Canary {
		key = []byte(canaryKey)
		r.edge, r.humanUser = "canary-edge-secret", "canary-human"
		sec = config.Secrets{Key: key, EdgeSecret: r.edge, NtfyToken: "canary-ntfy-token"} //nolint:gosec // synthetic canary for leak tests
		r.Ntfy = newFakeNtfy(tb)
	}
	cfg := config.Config{
		Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: mode,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		TrustedUsers:   []string{r.humanUser},
		DataDir:        r.DataDir, SecretsDir: filepath.Join(root, "secrets"), BackupDir: r.BackupDir,
		BackupAt: "off", FFmpeg: "ffmpeg", FLAC: "flac", Metaflac: "metaflac", RateScale: 1,
	}
	if r.Ntfy != nil {
		cfg.NtfyURL = r.Ntfy.URL + "/mm-test"
	}

	if o.Archive != nil {
		if o.Fixture || o.Files {
			tb.Fatal("servertest: Archive is exclusive with Fixture and Files")
		}
		o.Archive.CopyTo(tb, r.DataDir)
	}
	dbPath := filepath.Join(r.DataDir, "metamusic.db")
	seeded := false
	if o.Fixture && clock.Now().Equal(librarytest.FixedNow) {
		seeded = seedFixtureDB(tb, dbPath)
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		tb.Fatal(err)
	}
	r.Store = st
	tb.Cleanup(func() { _ = st.Close() })
	if o.Fixture && !seeded {
		librarytest.Fixture(tb, st, clock.Now())
	}
	if o.Files {
		librarytest.WriteFiles(tb, r.DataDir)
	}
	dataRoot, err := os.OpenRoot(r.DataDir)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = dataRoot.Close() })

	limits := o.Limits
	if limits == nil {
		limits = ratelimit.Config{}
		for _, c := range []ratelimit.Class{ratelimit.Agent, ratelimit.AgentHeavy, ratelimit.Human, ratelimit.HumanHeavy, ratelimit.Pair} {
			limits[c] = ratelimit.Limit{PerMinute: unlimitedPM, Burst: unlimitedPM}
		}
	}
	svc, err := app.Build(ctx, cfg, sec, st, dataRoot, app.Options{
		Now: clock.Now, NewID: idSequence(o.IDSeed), Limits: limits, Version: "v-test", Commit: "abc123",
		Probe: &mediaserve.Object{RelPath: "derived/probe/probe-v1.flac", ContentType: "audio/flac", ETag: strings.Repeat("ab", 32)},
	})
	if err != nil {
		tb.Fatal(err)
	}
	r.Services = svc
	// Close runs before the store and the directories go away (LIFO).
	tb.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		if err := svc.Close(cctx); err != nil {
			tb.Errorf("servertest: closing services: %v", err)
		}
	})

	opts := svc.Server
	opts.SPA = fstest.MapFS{"index.html": {Data: []byte("x")}}
	opts.MCPFactory = func(a huma.API, inner http.Handler) (http.Handler, error) {
		r.API = a
		s, err := mcpgen.NewServer(a, inner, "v-test")
		if err != nil {
			return nil, err
		}
		return mcpgen.Handler(s), nil
	}
	h, err := httpserver.New(opts)
	if err != nil {
		tb.Fatal(err)
	}
	srv := httptest.NewServer(h)
	tb.Cleanup(srv.Close)
	r.URL = srv.URL
	return r
}

// idSequence returns a deterministic UUIDv7-shaped id generator.
func idSequence(seed string) func() string {
	if seed == "" {
		seed = "0190"
	}
	var n atomic.Uint64
	n.Store(idBase)
	return func() string {
		return fmt.Sprintf("%s0000-0000-7000-8000-%012x", seed, n.Add(1))
	}
}

// Human is a REST client for the operator: proxy headers in proxy mode, a
// logged-in session cookie that has just stepped up in local mode (see StepUp).
func (r *Rig) Human(tb testing.TB) *Client {
	tb.Helper()
	if r.mode == "proxy" {
		return newClient(r.URL, http.Header{"Remote-User": {r.humanUser}, "X-Mm-Edge": {r.edge}}, false)
	}
	if err := r.Services.Local.SetPassword(context.Background(), r.humanUser, r.password); err != nil {
		tb.Fatal(err)
	}
	c := newClient(r.URL, http.Header{}, true)
	form := url.Values{"username": {r.humanUser}, "password": {r.password}}.Encode()
	resp := c.Do(tb, "POST", "/auth/login", form, "Content-Type", "application/x-www-form-urlencoded")
	if resp.Status != http.StatusSeeOther {
		tb.Fatalf("servertest: local login: status %d", resp.Status)
	}
	r.StepUp(tb, c)
	return c
}

// Agent creates a token with scopes (default read) and returns a bearer client
// and the token id.
func (r *Rig) Agent(tb testing.TB, scopes ...auth.Scope) (*Client, string) {
	tb.Helper()
	tok, id := r.token(tb, scopes)
	return newClient(r.URL, http.Header{"Authorization": {"Bearer " + tok}}, false), id
}

func (r *Rig) token(tb testing.TB, scopes []auth.Scope) (string, string) {
	tb.Helper()
	if len(scopes) == 0 {
		scopes = []auth.Scope{auth.ScopeRead}
	}
	tok, id, err := r.Services.Tokens.Create(context.Background(), "agent-1", scopes)
	if err != nil {
		tb.Fatal(err)
	}
	return tok, id
}

type bearerRT struct{ tok string }

func (b bearerRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(req)
}

// MCP connects a streamable-HTTP MCP client authenticated by a new token.
func (r *Rig) MCP(tb testing.TB, scopes ...auth.Scope) *mcp.ClientSession {
	tb.Helper()
	tok, _ := r.token(tb, scopes)
	c := mcp.NewClient(&mcp.Implementation{Name: "servertest", Version: "0"}, nil)
	sess, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: r.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerRT{tok}}, MaxRetries: -1,
	}, nil)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = sess.Close() })
	return sess
}

// Stdio connects an in-memory MCP client to NewStdioServer, as mcp-stdio runs
// it, authenticated by a new token (verified before every call).
func (r *Rig) Stdio(tb testing.TB, scopes ...auth.Scope) *mcp.ClientSession {
	tb.Helper()
	tok, _ := r.token(tb, scopes)
	if r.gatedAPI == nil {
		r.gatedAPI, r.stdioHandler = r.Services.Gated()
	}
	srv, err := mcpgen.NewStdioServer(r.gatedAPI, r.stdioHandler, mcpgen.StdioOptions{
		Principal: func(ctx context.Context) (auth.Principal, error) { return r.Services.Tokens.Verify(ctx, tok) },
		Version:   "v-test",
	})
	if err != nil {
		tb.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(context.Background(), st, nil)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = ss.Close() })
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "servertest", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = sess.Close() })
	return sess
}

var (
	fixtureDBOnce  sync.Once
	fixtureDBImage []byte
)

// seedFixtureDB writes a database holding librarytest.Fixture at librarytest.FixedNow to path,
// built once per process (the fixture is deterministic), so rigs do not replay hundreds of
// inserts each. It reports false if the image is unavailable, in which case the caller inserts
// the fixture itself.
func seedFixtureDB(tb testing.TB, path string) bool {
	tb.Helper()
	fixtureDBOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-servertest-fixture-")
		if err != nil {
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		p := filepath.Join(dir, "metamusic.db")
		st, err := store.Open(context.Background(), p)
		if err != nil {
			return
		}
		librarytest.Fixture(tb, st, librarytest.FixedNow)
		if _, err := st.W.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			_ = st.Close()
			return
		}
		if err := st.Close(); err != nil {
			return
		}
		fixtureDBImage, _ = os.ReadFile(p) //nolint:gosec // our own temp dir
	})
	if fixtureDBImage == nil {
		return false
	}
	if err := os.WriteFile(path, fixtureDBImage, 0o600); err != nil {
		tb.Fatal(err)
	}
	return true
}
