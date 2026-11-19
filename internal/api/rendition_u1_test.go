package api_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clean"
	"github.com/nicolasacchi/metamusic/internal/media/clean/cleantest"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var listener = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "carol", Name: "carol", User: "carol", Scopes: []auth.Scope{auth.ScopeListen, auth.ScopeRate}, Via: "proxy"}

// newRigClean is the M2c fixture with tainted masters and mp3_320 files and the rendition service wired; with
// withRend false the service is missing (a Listener's audio must then fail closed).
func newRigClean(t *testing.T, withRend bool) (*rig, map[string]cleantest.Taint, *os.Root) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.FixtureM2c(t, st, lt.FixedNow)
	dir := t.TempDir()
	lt.WriteFiles(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	tn := cleantest.Rewrite(t, st, root, lt.TrackTwinA, lt.TrackTwinB, lt.TrackRefFlac, lt.TrackLossy)
	now := func() time.Time { return lt.FixedNow }
	lib := library.New(st, root, now)
	r := &rig{mux: http.NewServeMux(), signer: serve.NewSigner(testKey, now), svc: lib, st: st}
	d := api.Deps{Store: st, Library: lib, Signer: r.signer, Now: now, Downloads: tagged.New(root, lib, cover.New(root, lib, now), now)}
	if withRend {
		d.Renditions = clean.New(clean.Options{Store: st, Data: root, Now: now})
	}
	api.New(r.mux, d)
	return r, tn, root
}

func TestListenerMediaAndDownload(t *testing.T) {
	t.Parallel()
	r, tn, _ := newRigClean(t, true)
	for _, id := range []string{lt.TrackTwinA, lt.TrackRefFlac} {
		m := r.get(t, listener, "/api/tracks/"+id+"/media")
		fl, mp := m["flac"].(map[string]any), m["mp3"].(map[string]any)
		if _, has := fl["sha256"]; has {
			t.Errorf("flac link has sha256")
		}
		if _, has := mp["sha256"]; has {
			t.Errorf("mp3 link has sha256")
		}
		if strings.Contains(fl["url"].(string), tn[id].MasterSHA) || m["ttl_s"] != float64(900) || fl["content_type"] != "audio/flac" || mp["bitrate_kbps"] != float64(320) {
			t.Errorf("media links %v", m)
		}
		dl := r.get(t, listener, "/api/tracks/"+id+"/download")
		if strings.Contains(dl["url"].(string), tn[id].MasterSHA) || strings.Contains(dl["filename"].(string), "__") || !strings.HasSuffix(dl["filename"].(string), ".flac") {
			t.Errorf("download %v", dl)
		}
	}
	// the Owner of the same rig still sees the master's hash
	if m := r.get(t, human, "/api/tracks/"+lt.TrackTwinA+"/media"); m["flac"].(map[string]any)["sha256"] != tn[lt.TrackTwinA].MasterSHA {
		t.Errorf("owner flac link %v", m["flac"])
	}
	// a lossy master: no flac, an mp3 clean copy, the download is that mp3
	m := r.get(t, listener, "/api/tracks/"+lt.TrackLossy+"/media")
	if m["flac"] != nil || m["lossy"] != true || strings.Contains(m["mp3"].(map[string]any)["url"].(string), tn[lt.TrackLossy].MasterSHA) {
		t.Errorf("lossy media links %v", m)
	}
	if dl := r.get(t, listener, "/api/tracks/"+lt.TrackLossy+"/download"); !strings.Contains(dl["url"].(string), ".mp3?") || !strings.HasSuffix(dl["filename"].(string), ".mp3") {
		t.Errorf("lossy download %v", dl)
	}
	// a discarded take stays a 404 for the Listener on both routes, and an unknown id too
	if _, err := r.st.W.Exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, lt.TrackTwinB); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/media", "/download"} {
		r.problem(t, listener, "GET", "/api/tracks/"+lt.TrackTwinB+p, "", 404, "NOT_FOUND")
		r.problem(t, listener, "GET", "/api/tracks/no-such"+p, "", 404, "NOT_FOUND")
	}
}

func TestListenerAudioFailsClosed(t *testing.T) {
	t.Parallel()
	t.Run("no rendition service", func(t *testing.T) {
		t.Parallel()
		r, _, _ := newRigClean(t, false)
		for _, p := range []string{"/media", "/download"} {
			if m := r.problem(t, listener, "GET", "/api/tracks/"+lt.TrackTwinA+p, "", 503, "UNAVAILABLE"); m["details"].(map[string]any)["component"] != "renditions" {
				t.Errorf("%s: %v", p, m)
			}
		}
	})
	t.Run("master that is not a FLAC", func(t *testing.T) {
		t.Parallel()
		r, _, root := newRigClean(t, true)
		var p string
		_ = r.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, lt.TrackTwinB).Scan(&p)
		_ = root.Chmod(p, 0o644)
		if err := root.WriteFile(p, bytes.Repeat([]byte{1}, 100), 0o444); err != nil {
			t.Fatal(err)
		}
		r.problem(t, listener, "GET", "/api/tracks/"+lt.TrackTwinB+"/download", "", 503, "RENDITION_UNAVAILABLE")
		r.problem(t, listener, "GET", "/api/tracks/"+lt.TrackTwinB+"/media", "", 503, "RENDITION_UNAVAILABLE")
	})
	t.Run("master file missing", func(t *testing.T) {
		t.Parallel()
		r, _, root := newRigClean(t, true)
		var p string
		_ = r.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, lt.TrackTwinB).Scan(&p)
		if err := root.Remove(p); err != nil {
			t.Fatal(err)
		}
		r.problem(t, listener, "GET", "/api/tracks/"+lt.TrackTwinB+"/download", "", 409, "MEDIA_UNAVAILABLE")
	})
}
