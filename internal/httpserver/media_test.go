package httpserver_test

import (
	"bytes"
	"context"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type mediaEnv struct {
	h      http.Handler
	signer *serve.Signer
	tokens *auth.TokenStore
	local  *auth.LocalAuth
	cfg    config.Config
	sha    string
	master []byte
	kind   *auth.Kind // principal kind seen by the media handler on the last request
}

func newMediaEnv(t *testing.T, mode, publicURL string) *mediaEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lt.Fixture(t, st, lt.FixedNow)
	lt.WriteFiles(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	now := func() time.Time { return lt.FixedNow }
	key := []byte("0123456789abcdef0123456789abcdef")
	signer := serve.NewSigner(key, now)
	lib := library.New(st, root, now)
	cfg := config.Config{
		Addr: "127.0.0.1:8080", PublicURL: publicURL, AuthMode: mode,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TrustedUsers: []string{"alice"},
	}
	var sha, rel string
	if err := st.R.QueryRow(`SELECT master_sha256, master_path FROM tracks WHERE id = ?`, lt.TrackLuna).Scan(&sha, &rel); err != nil {
		t.Fatal(err)
	}
	master, err := os.ReadFile(dir + "/" + rel)
	if err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewTokenStore(st, key)
	local := auth.NewLocalAuth(st, key, publicURL)
	spa := fstest.MapFS{
		"index.html":           {Data: []byte("<html>spa</html>")},
		"manifest.webmanifest": {Data: []byte("{}")},
		"icons/icon-192.png":   {Data: []byte("png")},
	}
	seen := new(auth.Kind)
	inner := serve.Handler(signer, root, lib, nil)
	rec := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = auth.FromContext(r.Context()).Kind
		inner.ServeHTTP(w, r)
	})
	h, err := httpserver.New(httpserver.Options{
		Cfg: cfg, Sec: config.Secrets{Key: key, EdgeSecret: edge}, Store: st, Tokens: tokens, Sessions: local,
		SPA: spa, Version: "v-test", Commit: "abc123",
		Library: lib, Signer: signer, Media: rec, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &mediaEnv{h, signer, tokens, local, cfg, sha, master, seen}
}

func (m *mediaEnv) get(path string, mut func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = "127.0.0.1:5555"
	if mut != nil {
		mut(r)
	}
	w := httptest.NewRecorder()
	m.h.ServeHTTP(w, r)
	return w
}

func (m *mediaEnv) url() string {
	return m.signer.Sign(m.sha, "flac", time.Hour, time.Minute).URL
}

func TestAcceptance_M1_MediaTrustMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("acceptance test (C23 tier)")
	}
	for _, tc := range []struct{ name, mode, pub string }{
		{"proxy", "proxy", "http://mm.example.com"},
		{"local-http", "local", "http://mm.example.com"},
		{"local-https", "local", "https://mm.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMediaEnv(t, tc.mode, tc.pub)
			ctx := context.Background()
			u := m.url()
			if w := m.get(u, nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), m.master) {
				t.Fatalf("anonymous signed GET = %d", w.Code)
			}
			spoof := func(r *http.Request) {
				r.Header.Set("Remote-User", "alice")
				r.Header.Set("X-MM-Edge", edge)
				r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "bogus"})
				r.AddCookie(&http.Cookie{Name: auth.HostSessionCookie, Value: "bogus"})
			}
			*m.kind = ""
			if w := m.get(u, spoof); w.Code != 200 {
				t.Errorf("spoofed identity = %d", w.Code)
			}
			if *m.kind != auth.KindAnonymous {
				t.Errorf("spoofed identity used principal %q", *m.kind)
			}
			if w := m.get(u[:strings.Index(u, "?")], spoof); w.Code != 403 {
				t.Errorf("unsigned with spoofed identity = %d", w.Code)
			}
			if tc.mode == "local" {
				if err := m.local.SetPassword(ctx, "alice", "correct horse battery"); err != nil {
					t.Fatal(err)
				}
				cookie, err := m.local.Login(ctx, "alice", "correct horse battery")
				if err != nil {
					t.Fatal(err)
				}
				name := auth.CookieName(strings.HasPrefix(tc.pub, "https://"))
				*m.kind = ""
				if w := m.get(u, func(r *http.Request) {
					r.AddCookie(&http.Cookie{Name: name, Value: cookie})
					r.Header.Set("Remote-User", "mallory")
					r.Header.Set("X-MM-Edge", edge)
				}); w.Code != 200 {
					t.Errorf("valid session + sig = %d", w.Code)
				}
				if *m.kind != auth.KindAnonymous {
					t.Errorf("valid session + spoofed proxy headers used principal %q", *m.kind)
				}
				if w := m.get(u[:strings.Index(u, "?")], func(r *http.Request) { r.AddCookie(&http.Cookie{Name: name, Value: cookie}) }); w.Code != 403 {
					t.Errorf("valid session, unsigned = %d", w.Code)
				}
			}
			if w := m.get(u, func(r *http.Request) { r.Header.Set("Authorization", "Bearer bogus") }); w.Code != 401 {
				t.Errorf("bogus bearer = %d", w.Code)
			}
			tok, _, err := m.tokens.Create(ctx, "agent-1", []auth.Scope{auth.ScopeRead})
			if err != nil {
				t.Fatal(err)
			}
			if w := m.get(u, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }); w.Code != 200 {
				t.Errorf("PAT + sig = %d", w.Code)
			}
			if w := m.get(u[:strings.Index(u, "?")], nil); w.Code != 403 {
				t.Errorf("unsigned = %d", w.Code)
			}
			if w := m.get("/manifest.webmanifest", nil); w.Code != 200 || w.Header().Get("Content-Type") != "application/manifest+json" {
				t.Errorf("manifest = %d %q", w.Code, w.Header().Get("Content-Type"))
			}
			if w := m.get("/icons/icon-192.png", nil); w.Code != 200 {
				t.Errorf("icon = %d", w.Code)
			}
			if w := m.get("/api/tracks", nil); w.Code != 401 {
				t.Errorf("anonymous /api/tracks = %d", w.Code)
			}
			noRead, _, err := m.tokens.Create(ctx, "agent-2", []auth.Scope{auth.ScopeGenerate})
			if err != nil {
				t.Fatal(err)
			}
			w := m.get("/api/stats", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+noRead) })
			if w.Code != 403 || !strings.Contains(w.Body.String(), "FORBIDDEN_SCOPE") {
				t.Errorf("read-less PAT /api/stats = %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestHTTP_ManifestAndIconsPublic(t *testing.T) {
	// mime cannot unregister a type, so the override leaks for the rest of this
	// test binary; acceptable because spa.go sets the header explicitly and no
	// other test depends on the .webmanifest mapping.
	if err := mime.AddExtensionType(".webmanifest", "text/plain"); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, nil, spaFS, nil)
	w := e.do(httptest.NewRequest("GET", "/manifest.webmanifest", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/manifest+json" {
		t.Errorf("manifest = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := e.do(httptest.NewRequest("GET", "/icons/icon-192.png", nil)); w.Code != 200 {
		t.Errorf("icon = %d", w.Code)
	}
}

func TestHandler_DotSegmentsNeverServe(t *testing.T) {
	for _, mode := range []string{"proxy", "local"} {
		t.Run(mode, func(t *testing.T) {
			m := newMediaEnv(t, mode, "http://mm.example.com")
			q := m.url()[strings.Index(m.url(), "?"):]
			paths := []string{
				"/media/x/../y",
				"/media/%2e%2e%2fmedia/" + m.sha + ".flac",
				"/media/" + m.sha + ".flac/../" + m.sha + ".flac",
				"/media/%2e%2e/media/" + m.sha + ".flac",
				"/media/%2E%2E/" + m.sha + ".flac",
				"/media/./" + m.sha + ".flac",
				"/media/x/%2e%2e/" + m.sha + ".flac",
			}
			tok, _, err := m.tokens.Create(context.Background(), "agent-1", []auth.Scope{auth.ScopeRead})
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range paths {
				for _, withTok := range []bool{false, true} {
					w := m.get(p+q, func(r *http.Request) {
						if withTok {
							r.Header.Set("Authorization", "Bearer "+tok)
						}
					})
					if w.Code == 200 || w.Code == 206 || bytes.Contains(w.Body.Bytes(), m.master) {
						t.Errorf("%s (token=%v) served: %d", p, withTok, w.Code)
					}
					switch w.Code {
					// Go 1.27 ServeMux cleans paths with 307/308 (not only 301), keeping the query.
					case 301, 303, 307, 308, 401, 404:
					default:
						t.Errorf("%s (token=%v) = %d", p, withTok, w.Code)
					}
				}
			}
		})
	}
}
