package httpserver_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// flakyAudit fails its first n inserts with a transient error, then delegates.
type flakyAudit struct {
	next  *audit.Store
	fails int32
	calls atomic.Int32
}

func (f *flakyAudit) Insert(ctx context.Context, e audit.Entry) error {
	if f.calls.Add(1) <= f.fails {
		return errors.New("database is locked")
	}
	return f.next.Insert(ctx, e)
}

// TestPairAuditRetriesTransientFailure: the auth:pair row survives two transient insert
// failures and is committed before the pairing response goes out.
func TestPairAuditRetriesTransientFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := []byte("0123456789abcdef0123456789abcdef")
	cfg := config.Config{
		Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy",
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TrustedUsers: []string{"alice"},
	}
	n := 0
	newID := func() string { n++; return "id-" + string(rune('a'+n)) }
	devs := auth.NewDeviceStore(st, key, nil, newID)
	flaky := &flakyAudit{next: audit.NewStore(st, func() string { return "audit-row-1" }), fails: 2}
	h, err := httpserver.New(httpserver.Options{
		Cfg: cfg, Sec: config.Secrets{Key: key, EdgeSecret: edge}, Store: st,
		Tokens: auth.NewTokenStore(st, key, time.Now), Sessions: auth.NewLocalAuth(st, key, cfg.PublicURL),
		SPA: spaFS, DeviceStore: devs, PairAudit: flaky,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := devs.CreatePairingCode(ctx, auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice"}, "Phone")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/auth/pair", strings.NewReader(url.Values{"code": {code}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("pair = %d %s", w.Code, w.Body)
	}
	if got := flaky.calls.Load(); got != 3 {
		t.Fatalf("insert attempts = %d, want 3", got)
	}
	// row is already there when the response is returned
	var cnt int
	if err := st.R.QueryRow(`SELECT count(*) FROM audit_log WHERE op = 'auth:pair'`).Scan(&cnt); err != nil || cnt != 1 {
		t.Fatalf("auth:pair rows = %d, err %v", cnt, err)
	}
}
