package api_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// newRigCovers is newRig with a cover service over the same data root.
func newRigCovers(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.FixtureV2(t, st, lt.FixedNow)
	dir := t.TempDir()
	lt.WriteFiles(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	now := func() time.Time { return lt.FixedNow }
	lib := library.New(st, root, now)
	r := &rig{mux: http.NewServeMux(), signer: serve.NewSigner(testKey, now), svc: lib, st: st}
	api.New(r.mux, api.Deps{Store: st, Library: lib, Signer: r.signer, Now: now, Covers: cover.New(root, lib, now)})
	return r
}

func rowCount(t *testing.T, st *store.Store, table string) int {
	t.Helper()
	var n int
	if err := st.R.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestGetMediaLinks_Cover: both cover URLs verify with the signer; human 12 h, agent 15 min; no DB write.
func TestGetMediaLinks_Cover(t *testing.T) {
	t.Parallel()
	r := newRigCovers(t)
	path := "/api/tracks/" + lt.TrackLuna + "/media"
	tables := []string{"audit_log", "tracks", "recipes", "renditions"}
	before := make([]int, len(tables))
	for i, tb := range tables {
		before[i] = rowCount(t, r.st, tb)
	}
	for _, c := range []struct {
		name string
		who  auth.Principal
		exp  string
	}{{"human", human, "2026-10-07T00:00:00Z"}, {"agent", agent, "2026-10-06T12:15:00Z"}} {
		m := r.get(t, c.who, path)
		cv, ok := m["cover"].(map[string]any)
		if !ok {
			t.Fatalf("%s: cover = %v", c.name, m["cover"])
		}
		if cv["expires_at"] != c.exp {
			t.Errorf("%s: cover expires_at %v, want %s", c.name, cv["expires_at"], c.exp)
		}
		keys := map[string]bool{}
		for _, f := range []string{"url_512", "url_1024"} {
			u, err := url.Parse(cv[f].(string))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(u.Path, "/media/") || !strings.HasSuffix(u.Path, ".png") {
				t.Errorf("%s %s: path %s", c.name, f, u.Path)
			}
			key := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/media/"), ".png")
			keys[key] = true
			if err := r.signer.Verify(key, "png", u.Query().Get("exp"), u.Query().Get("sig")); err != nil {
				t.Errorf("%s %s does not verify: %v", c.name, f, err)
			}
		}
		if len(keys) != 2 {
			t.Errorf("%s: 512 and 1024 share a key", c.name)
		}
	}
	for i, tb := range tables {
		if n := rowCount(t, r.st, tb); n != before[i] {
			t.Errorf("%s rows %d -> %d: get_media_links wrote to the database", tb, before[i], n)
		}
	}
}

// TestGetMediaLinks_CoverNullWithoutService: the cover is null (present) when no cover service is wired.
func TestGetMediaLinks_CoverNullWithoutService(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	m := r.get(t, human, "/api/tracks/"+lt.TrackLuna+"/media")
	if v, ok := m["cover"]; !ok || v != nil {
		t.Errorf("cover = %v (present %v)", v, ok)
	}
}
