package connectors_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/oidc"
	"github.com/nicolasacchi/metamusic/internal/oidc/oidctest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const (
	vResource = "https://mm.example.com/mcp"
	vUser     = "e2e"
	vSubject  = "subject-e2e-0001-synthetic"
)

type spyNotifier struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (s *spyNotifier) Notify(_ context.Context, m notify.Message) error {
	s.mu.Lock()
	s.msgs = append(s.msgs, m)
	s.mu.Unlock()
	return nil
}

func (s *spyNotifier) all() []notify.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]notify.Message(nil), s.msgs...)
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// vrig is one verifier over a real SQLite store and the stub provider; its clock is its own.
type vrig struct {
	t      *testing.T
	st     *store.Store
	cs     *connectors.Store
	v      *connectors.Verifier
	p      *oidctest.Provider
	notify *spyNotifier
	log    *syncBuf
	ov     *oidc.Verifier
	mu     sync.Mutex
	now    time.Time
}

func (r *vrig) clock() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }
func (r *vrig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

func newVRig(t *testing.T, mutate func(*oidctest.Options)) *vrig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := &vrig{t: t, st: st, now: fixedNow, notify: &spyNotifier{}, log: &syncBuf{}}
	scopes := []string{"openid", "offline_access", "metamusic"}
	o := oidctest.Options{
		Clients: []oidctest.Client{
			{ID: "test-claude", AuthMethod: "none", RedirectURIs: []string{"https://claude.ai/cb"}, Audience: []string{vResource}, Scopes: scopes},
			{ID: "test-unlisted", AuthMethod: "none", RedirectURIs: []string{"https://x.example/cb"}, Audience: []string{vResource}, Scopes: scopes},
		},
		Users: map[string]string{vUser: vSubject, "mallory": "subject-mallory"}, LoginAs: vUser, Now: r.clock, UserClaim: true, IssParam: true,
	}
	if mutate != nil {
		mutate(&o)
	}
	r.p = oidctest.New(t, o)
	ov, err := oidc.NewVerifier(oidc.Config{Issuer: r.p.URL, Resource: vResource, Clients: map[string]string{"test-claude": "Claude Test"}},
		&http.Client{}, r.clock)
	if err != nil {
		t.Fatal(err)
	}
	r.ov = ov
	r.cs = connectors.NewStore(st, r.clock, nil)
	r.v = connectors.NewVerifier(connectors.VerifierOptions{
		OIDC: ov, Store: r.cs, TrustedUsers: []string{vUser}, Labels: map[string]string{"test-claude": "Claude Test"},
		Audit: audit.NewStore(st, func() string { return uuid.NewV7().String() }), Notify: r.notify, Now: r.clock, Logger: slog.New(slog.NewJSONHandler(r.log, nil)),
	})
	return r
}

func (r *vrig) oidcVerifier(*testing.T) *oidc.Verifier { return r.ov }
func (r *vrig) logger() *slog.Logger                   { return slog.New(slog.NewJSONHandler(r.log, nil)) }

func (r *vrig) token(client, user string, scopes ...string) string {
	if len(scopes) == 0 {
		scopes = []string{"openid", "metamusic"}
	}
	return r.p.Mint(r.t, client, user, scopes, nil)
}

func (r *vrig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// TestVerifyConnector_Order pins the order of C106 through its side effects: oidc, then the trusted user (before
// any row exists), then Ensure (row, audit, push), then revoked (before the touch), then the touch, then the principal.
func TestVerifyConnector_Order(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	type effects struct{ rows, audits, pushes, touched int }
	cases := []struct {
		name  string
		setup func(r *vrig) string
		err   error
		want  effects
	}{
		{"invalid token stops at oidc", func(r *vrig) string { return "a.b.c" }, auth.ErrInvalidToken, effects{}},
		{"unlisted client stops at oidc", func(r *vrig) string { return r.token("test-unlisted", vUser) }, auth.ErrConnectorForbidden, effects{}},
		{"missing scope stops at oidc", func(r *vrig) string { return r.token("test-claude", vUser, "openid") }, &auth.ScopeError{}, effects{}},
		{"untrusted user stops before Ensure", func(r *vrig) string { return r.token("test-claude", "mallory") }, auth.ErrConnectorForbidden, effects{}},
		{"first valid token creates, announces and touches", func(r *vrig) string { return r.token("test-claude", vUser) }, nil, effects{1, 1, 1, 1}},
		{"revoked stops after Ensure and before the touch", func(r *vrig) string {
			g, _, _ := r.cs.Ensure(ctx, "test-claude", vSubject, vUser, "Claude Test")
			if _, err := r.cs.Set(ctx, humanOp, g.ID, connectors.SetIn{Revoked: boolp(true)}); err != nil {
				r.t.Fatal(err)
			}
			return r.token("test-claude", vUser)
		}, auth.ErrConnectorRevoked, effects{1, 0, 0, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newVRig(t, nil)
			bearer := c.setup(r)
			p, err := r.v.VerifyConnector(ctx, bearer)
			switch {
			case c.err == nil && err != nil:
				t.Fatalf("err = %v", err)
			case c.err != nil && !errors.Is(err, c.err) && !isScope(err, c.err):
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			got := effects{rows: r.count(`SELECT COUNT(*) FROM connector_grants`), audits: r.count(`SELECT COUNT(*) FROM audit_log WHERE op = 'auth:connector_grant'`),
				pushes: len(r.notify.all()), touched: r.count(`SELECT COUNT(*) FROM connector_grants WHERE last_used_at IS NOT NULL`)}
			if got != c.want {
				t.Errorf("effects = %+v, want %+v", got, c.want)
			}
			if (err == nil) != (p.Kind == auth.KindAgent) {
				t.Errorf("principal %+v for err %v", p, err)
			}
		})
	}
}

func isScope(err, want error) bool {
	var se, wantScope *auth.ScopeError
	return errors.As(want, &wantScope) && errors.As(err, &se)
}

func TestConnector_GrantAutoCreatedReadWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newVRig(t, nil)
	p, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != auth.KindAgent || p.Via != "oauth" || p.Client != "test-claude" || p.Name != "Claude Test" || p.Key() != "agent:"+p.ID {
		t.Errorf("principal = %+v", p)
	}
	if len(p.Scopes) != 2 || p.Scopes[0] != auth.ScopeRead || p.Scopes[1] != auth.ScopeWrite {
		t.Errorf("scopes = %v", p.Scopes)
	}
	grants, _ := r.cs.List(ctx)
	if len(grants) != 1 || grants[0].ID != p.ID || grants[0].Limits != (connectors.Limits{}) {
		t.Fatalf("grants = %+v", grants)
	}
	var transport, op, method, client string
	var status int
	var created string
	if err := r.st.R.QueryRow(`SELECT transport, op, method, client, status, created_ids_json FROM audit_log`).Scan(&transport, &op, &method, &client, &status, &created); err != nil {
		t.Fatal(err)
	}
	if transport != "oauth" || op != "auth:connector_grant" || method != "SYSTEM" || status != 201 || created != `["`+p.ID+`"]` || client != "oauth:test-claude" {
		t.Errorf("audit row: %s %s %s %s %d %s", transport, op, method, client, status, created)
	}
	msgs := r.notify.all()
	if len(msgs) != 1 || msgs[0].Title != "Connector connected" || msgs[0].Priority != 3 || !strings.Contains(msgs[0].Body, "Claude Test") ||
		strings.Contains(msgs[0].Title+msgs[0].Body, vUser) || strings.Contains(msgs[0].Title+msgs[0].Body, vSubject) {
		t.Errorf("push = %+v", msgs)
	}
	// a second token: no new row, no audit row, no message
	if _, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser)); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT COUNT(*) FROM connector_grants`); n != 1 {
		t.Errorf("rows = %d", n)
	}
	if n := r.count(`SELECT COUNT(*) FROM audit_log`); n != 1 {
		t.Errorf("audit rows = %d", n)
	}
	if len(r.notify.all()) != 1 {
		t.Errorf("messages = %d", len(r.notify.all()))
	}
	for _, secret := range []string{vUser, vSubject} {
		if strings.Contains(r.log.String(), secret) {
			t.Errorf("log contains %q", secret)
		}
	}
}

func TestConnector_UntrustedUser(t *testing.T) {
	t.Parallel()
	r := newVRig(t, nil)
	_, err := r.v.VerifyConnector(context.Background(), r.token("test-claude", "mallory"))
	if !errors.Is(err, auth.ErrConnectorForbidden) {
		t.Fatalf("err = %v", err)
	}
	if n := r.count(`SELECT COUNT(*) FROM connector_grants`); n != 0 {
		t.Errorf("rows = %d", n)
	}
	if !strings.Contains(r.log.String(), "user_not_trusted") || strings.Contains(r.log.String(), "mallory") {
		t.Errorf("log = %s", r.log.String())
	}
}

func TestConnector_MissingUserClaim(t *testing.T) {
	t.Parallel()
	r := newVRig(t, func(o *oidctest.Options) { o.UserClaim = false })
	_, err := r.v.VerifyConnector(context.Background(), r.token("test-claude", vUser))
	if !errors.Is(err, auth.ErrConnectorForbidden) || !strings.Contains(r.log.String(), "user_claim_missing") {
		t.Fatalf("err = %v log = %s", err, r.log.String())
	}
}

func TestConnector_Revoked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newVRig(t, nil)
	p, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.cs.Set(ctx, humanOp, p.ID, connectors.SetIn{Revoked: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser)); !errors.Is(err, auth.ErrConnectorRevoked) {
		t.Fatalf("revoked: %v", err)
	}
	if _, err := r.cs.Set(ctx, humanOp, p.ID, connectors.SetIn{Revoked: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser)); err != nil {
		t.Fatalf("restored: %v", err)
	}
}

func TestConnector_ErrorMapping(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newVRig(t, nil)
	if _, err := r.v.VerifyConnector(ctx, r.p.Mint(t, "test-claude", vUser, []string{"metamusic"}, []string{"https://other.example/mcp"})); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("wrong audience: %v", err)
	}
	var se *auth.ScopeError
	if _, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser, "openid")); !errors.As(err, &se) || se.Scope != "metamusic" {
		t.Errorf("missing scope: %v", err)
	}
	// keys unreachable: a verifier whose JWKS URL is dead answers ErrUnavailable
	dead, _ := oidc.NewVerifier(oidc.Config{Issuer: r.p.URL, JWKSURL: "http://127.0.0.1:1/jwks.json", Resource: vResource,
		Clients: map[string]string{"test-claude": "Claude Test"}}, &http.Client{Timeout: time.Second}, r.clock)
	v := connectors.NewVerifier(connectors.VerifierOptions{OIDC: dead, Store: r.cs, TrustedUsers: []string{vUser}})
	if _, err := v.VerifyConnector(ctx, r.token("test-claude", vUser)); !errors.Is(err, auth.ErrUnavailable) {
		t.Errorf("unavailable: %v", err)
	}
	if n := r.count(`SELECT COUNT(*) FROM connector_grants`); n != 0 {
		t.Errorf("refusals created %d rows", n)
	}
	// an unconfigured verifier fails closed
	if _, err := connectors.NewVerifier(connectors.VerifierOptions{}).VerifyConnector(ctx, "a.b.c"); !errors.Is(err, auth.ErrUnavailable) {
		t.Errorf("empty options: %v", err)
	}
}

func TestConnector_TouchAtMostOncePerMinute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newVRig(t, nil)
	last := func() string {
		var s string
		if err := r.st.R.QueryRow(`SELECT COALESCE(last_used_at, '') FROM connector_grants`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if _, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser)); err != nil {
		t.Fatal(err)
	}
	first := last()
	if first != store.FormatTime(fixedNow) {
		t.Fatalf("first touch = %q", first)
	}
	r.advance(30 * time.Second)
	if _, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser)); err != nil {
		t.Fatal(err)
	}
	if last() != first {
		t.Errorf("touched again within a minute: %q", last())
	}
	r.advance(40 * time.Second)
	if _, err := r.v.VerifyConnector(ctx, r.token("test-claude", vUser)); err != nil {
		t.Fatal(err)
	}
	if last() != store.FormatTime(r.clock()) {
		t.Errorf("not touched after a minute: %q", last())
	}
}
