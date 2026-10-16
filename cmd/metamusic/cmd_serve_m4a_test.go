package main

import (
	"context"
	"github.com/nicolasacchi/metamusic/internal/oidc/oidctest"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type fakeSweeper struct {
	mu    sync.Mutex
	ages  []time.Duration
	calls chan struct{}
}

func (f *fakeSweeper) Sweep(_ context.Context, older time.Duration) (int, error) {
	f.mu.Lock()
	f.ages = append(f.ages, older)
	f.mu.Unlock()
	select {
	case f.calls <- struct{}{}:
	default:
	}
	return 0, nil
}

// TestSweepM4aCaches_RunsAtStartAndStops: covers are swept at 30 days, downloads at 7, once at start and then on the ticker.
func TestSweepM4aCaches_RunsAtStartAndStops(t *testing.T) {
	t.Parallel()
	covers := &fakeSweeper{calls: make(chan struct{}, 8)}
	dls := &fakeSweeper{calls: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sweepM4aCaches(ctx, covers, dls, 20*time.Millisecond); close(done) }()
	for _, f := range []*fakeSweeper{covers, dls} {
		for range 2 { // at start and at least one tick
			select {
			case <-f.calls:
			case <-time.After(5 * time.Second):
				t.Fatal("sweeper not called")
			}
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sweep loop did not stop with its context")
	}
	covers.mu.Lock()
	dls.mu.Lock()
	defer covers.mu.Unlock()
	defer dls.mu.Unlock()
	if covers.ages[0] != 30*24*time.Hour || dls.ages[0] != 7*24*time.Hour {
		t.Errorf("ages: covers %v downloads %v, want 30 d and 7 d", covers.ages[0], dls.ages[0])
	}
}

func TestSweepM4aCaches_NilServicesAreSkipped(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sweepM4aCaches(ctx, nil, nil, time.Hour); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
}

// TestPruneSubsonicSalts_RunsAtStartAndStops: salts older than the 30-day retention are removed at start.
func TestPruneSubsonicSalts_RunsAtStartAndStops(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := store.FormatTime(time.Now().Add(-45 * 24 * time.Hour))
	fresh := store.FormatTime(time.Now())
	for _, r := range [][2]string{{"c1", old}, {"c2", fresh}} {
		if _, err := st.W.Exec(`INSERT INTO subsonic_salts(client_id, salt_sha256, first_seen_at) VALUES(?,?,?)`, r[0], "aa", r[1]); err != nil {
			t.Fatal(err)
		}
	}
	creds := auth.NewSubsonicStore(st, []byte("0123456789abcdef0123456789abcdef"), time.Now, nil, nil)
	pctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { pruneSubsonicSalts(pctx, creds, time.Hour); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		_ = st.R.QueryRow(`SELECT COUNT(*) FROM subsonic_salts`).Scan(&n)
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("old salt not pruned at start (%d rows)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("prune loop did not stop with its context")
	}
	var client string
	if err := st.R.QueryRow(`SELECT client_id FROM subsonic_salts`).Scan(&client); err != nil || client != "c2" {
		t.Errorf("remaining salt %q (%v), want the fresh one", client, err)
	}
}

// TestServe_SweepsAndPrunes: a started server runs the cache sweeps and the salt prune at start. Ages are set through
// file modification times and row timestamps relative to now (the 30-day cover and 7-day download windows; the
// 30-day salt retention).
func TestServe_SweepsAndPrunes(t *testing.T) {
	t.Parallel()
	env := m3Env(t, nil)
	data := env["MM_DATA_DIR"]
	now := time.Now()
	hex64 := func(c string) string { return strings.Repeat(c, 64) }
	put := func(rel string, age time.Duration) string {
		p := filepath.Join(data, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return p
	}
	day := 24 * time.Hour
	oldCover := put("derived/covers/aa/"+hex64("a")+".png", 31*day)
	newCover := put("derived/covers/bb/"+hex64("b")+".png", 29*day)
	oldDL := put("derived/dl/cc/"+hex64("c")+".flac", 8*day)
	newDL := put("derived/dl/dd/"+hex64("d")+".flac", 6*day)

	st, err := store.Open(context.Background(), filepath.Join(data, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]string{{"c-old", store.FormatTime(now.Add(-35 * day))}, {"c-young", store.FormatTime(now.Add(-29 * day))}} {
		if _, err := st.W.Exec(`INSERT INTO subsonic_salts(client_id, salt_sha256, first_seen_at) VALUES(?,?,?)`, r[0], "aa", r[1]); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()

	_, _, _, stop := startCmd(t, env, "serve", "--runner=false")
	waitFor(t, "stale cache files and salts removed", 20*time.Second, func() bool {
		_, e1 := os.Stat(oldCover)
		_, e2 := os.Stat(oldDL)
		return os.IsNotExist(e1) && os.IsNotExist(e2) &&
			dbRow(t, env, `SELECT count(*) FROM subsonic_salts WHERE client_id = 'c-old'`) == 0
	})
	for _, p := range []string{newCover, newDL} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("younger cache file removed: %v", err)
		}
	}
	if n := dbRow(t, env, `SELECT count(*) FROM subsonic_salts WHERE client_id = 'c-young'`); n != 1 {
		t.Errorf("younger salt rows = %d, want 1", n)
	}
	stop()
}

// TestServe_OIDCPrefetch: with OIDC configured the key cache is filled by the eager fetch at start, before any
// connector request reaches the server (C105).
func TestServe_OIDCPrefetch(t *testing.T) {
	t.Parallel()
	prov := oidctest.New(t, oidctest.Options{
		Clients: []oidctest.Client{{ID: "synthetic-client", AuthMethod: "none", RedirectURIs: []string{"https://client.example.com/cb"},
			Audience: []string{"http://127.0.0.1/mcp"}, Scopes: []string{"openid", "metamusic"}}},
		Users: map[string]string{"alice": "sub-1"}, LoginAs: "alice",
	})
	env := m3Env(t, map[string]string{
		"MM_OIDC_ISSUER": prov.URL, "MM_OIDC_CLIENTS": "synthetic-client=Synthetic client",
		"MM_TRUSTED_USERS": "alice", "MM_PUBLIC_URL": "http://127.0.0.1",
	})
	code, tokOut, errs := do(t, env, "", "token", "create", "--name", "adm", "--scopes", "read,admin")
	if code != 0 {
		t.Fatalf("token create: %d %s", code, errs)
	}
	addr, _, _, stop := startCmd(t, env, "serve", "--runner=false")
	c := apiClient{t, "http://" + addr, strings.TrimSpace(tokOut), &http.Client{Timeout: 10 * time.Second}}
	waitFor(t, "OIDC keys prefetched", 20*time.Second, func() bool {
		st, body := c.do("GET", "/api/connectors", nil)
		if st == http.StatusTooManyRequests {
			return false
		}
		if st != 200 {
			t.Fatalf("GET /api/connectors = %d %v", st, body)
		}
		oidc, _ := body["oidc"].(map[string]any)
		keys, _ := oidc["keys"].(map[string]any)
		n, _ := keys["count"].(float64)
		return n >= 1
	})
	stop()
}
