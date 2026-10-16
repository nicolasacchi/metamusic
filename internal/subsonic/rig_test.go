package subsonic

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // G501: the OpenSubsonic token is md5(password+salt) by protocol definition
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	auth.UseCheapHashForTests()
	code := m.Run()
	archivetest.Cleanup() // the shared synthetic archive templates of the acceptance tests (clients_test.go)
	os.Exit(code)
}

// clock is a manual clock shared by the credential store, the limiter and the handler.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

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

// rig is one handler over a real SQLite store, one read+write client with both credentials and a read-only client.
type rig struct {
	t     *testing.T
	st    *store.Store
	clk   *clock
	creds *auth.SubsonicStore
	srv   *server
	logs  *lockedBuf

	rw, ro      auth.SubsonicClient
	rwPW, rwKey string
	roPW, roKey string
	sleeps      *sleepSpy
	lim         *ratelimit.Limiter
	auditStore  *audit.Store
}

type sleepSpy struct {
	mu    sync.Mutex
	calls []time.Duration
	hook  func(ctx context.Context, d time.Duration) error
}

func (s *sleepSpy) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.calls = append(s.calls, d)
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, d)
	}
	return nil
}

func (s *sleepSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

var testKey = []byte("0123456789abcdef0123456789abcdef")

type rigOpts struct {
	limits    ratelimit.Config // nil => unlimited
	failDelay time.Duration
	st        *store.Store // use this store instead of an empty one (archive tests)
	data      *os.Root     // Options.Data
}

func newRig(t *testing.T, ro rigOpts) *rig {
	t.Helper()
	ctx := context.Background()
	st, err := ro.st, error(nil)
	if st == nil {
		if st, err = store.Open(ctx, filepath.Join(t.TempDir(), "mm.db")); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
	}
	clk := &clock{t: time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)}
	var n atomic.Int64
	newID := func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", n.Add(1)) }
	creds := auth.NewSubsonicStore(st, testKey, clk.now, newID, rand.NewChaCha8([32]byte{9, 9}))
	limits := ro.limits
	if limits == nil {
		limits = ratelimit.Config{}
		for _, c := range []ratelimit.Class{ratelimit.Subsonic, ratelimit.SubsonicAuthFail, ratelimit.SubsonicAuthFailGlobal} {
			limits[c] = ratelimit.Limit{PerMinute: 1e6, Burst: 1e6}
		}
	}
	r := &rig{t: t, st: st, clk: clk, creds: creds, logs: &lockedBuf{}, sleeps: &sleepSpy{}}
	r.lim = ratelimit.New(limits, clk.now)
	r.auditStore = audit.NewStore(st, newID)
	r.srv = newServer(Options{
		Cfg:     config.Config{TrustedProxies: nil},
		Version: "test", Store: st, Data: ro.data, Creds: creds, Audit: r.auditStore, Limiter: r.lim, Now: clk.now,
		FailDelay: ro.failDelay, Sleep: r.sleeps.sleep,
	})
	r.srv.log = slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	human := auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin}, Via: "proxy"}
	var sec auth.SubsonicSecrets
	r.rw, sec, err = creds.Create(ctx, human, auth.NewSubsonicClient{Name: "Reader Writer", Username: "rwclient", Write: true, APIKey: true, Password: true})
	if err != nil {
		t.Fatal(err)
	}
	r.rwKey, r.rwPW = sec.APIKey, sec.Password
	r.ro, sec, err = creds.Create(ctx, human, auth.NewSubsonicClient{Name: "Read Only", Username: "roclient", APIKey: true, Password: true})
	if err != nil {
		t.Fatal(err)
	}
	r.roKey, r.roPW = sec.APIKey, sec.Password
	return r
}

func token(pw, salt string) string {
	sum := md5.Sum([]byte(pw + salt)) //nolint:gosec // protocol-defined
	return hex.EncodeToString(sum[:])
}

// tokenParams are valid u/t/s/c/v parameters.
func tokenParams(user, pw, salt string, extra ...string) url.Values {
	q := url.Values{"u": {user}, "t": {token(pw, salt)}, "s": {salt}, "c": {"testapp"}, "v": {"1.16.1"}, "f": {"json"}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return q
}

func keyParams(key string, extra ...string) url.Values {
	q := url.Values{"apiKey": {key}, "c": {"testapp"}, "v": {"1.16.1"}, "f": {"json"}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return q
}

// reply is one recorded answer.
type reply struct {
	*httptest.ResponseRecorder
}

// doc decodes a JSON envelope's subsonic-response object.
func (r reply) doc(t *testing.T) map[string]any {
	t.Helper()
	var env map[string]map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &env); err != nil || env["subsonic-response"] == nil {
		t.Fatalf("not a JSON envelope (%v): %s", err, r.Body)
	}
	return env["subsonic-response"]
}

func (r reply) code(t *testing.T) int {
	t.Helper()
	d := r.doc(t)
	if d["status"] == "ok" {
		return 0
	}
	e, _ := d["error"].(map[string]any)
	return int(e["code"].(float64))
}

// get sends GET /rest/<endpoint> from the given client address.
func (r *rig) get(endpoint string, q url.Values, ip string) reply {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/rest/"+endpoint+"?"+q.Encode(), nil)
	req.RemoteAddr = ip + ":4000"
	w := httptest.NewRecorder()
	r.srv.ServeHTTP(w, req)
	return reply{w}
}

func (r *rig) do(req *http.Request) reply {
	if req.RemoteAddr == "" || strings.HasPrefix(req.RemoteAddr, "192.0.2.1:") {
		req.RemoteAddr = "198.51.100.1:4000"
	}
	w := httptest.NewRecorder()
	r.srv.ServeHTTP(w, req)
	return reply{w}
}

func newRequest(ctx context.Context, t *testing.T, rest, ip string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/rest/"+rest, nil)
	req.RemoteAddr = ip + ":4000"
	return req
}
