package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func shaHex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// newRigDownloads is a rig over the M2c fixture whose twin masters are header-valid FLAC files, with the tagged
// download service wired.
func newRigDownloads(t *testing.T) (*rig, *os.Root) {
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
	if _, err := st.W.Exec(`DROP TRIGGER tracks_frozen`); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{lt.TrackTwinA, lt.TrackTwinB} {
		var p string
		if err := st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		frames := bytes.Repeat([]byte{0xff, 0xf8, byte(i)}, 2000)
		b := flactest.Build(flactest.Spec{TotalSamples: 96000, SeekPoints: 2, Comments: []string{"TITLE=x"}, Frames: frames})
		if err := root.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := root.WriteFile(p, b, 0o444); err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.Exec(`UPDATE tracks SET master_sha256 = ?, master_bytes = ? WHERE id = ?`, shaHex(b), len(b), id); err != nil {
			t.Fatal(err)
		}
	}
	now := func() time.Time { return lt.FixedNow }
	lib := library.New(st, root, now)
	r := &rig{mux: http.NewServeMux(), signer: serve.NewSigner(testKey, now), svc: lib, st: st}
	api.New(r.mux, api.Deps{Store: st, Library: lib, Signer: r.signer, Now: now,
		Downloads: tagged.New(root, lib, cover.New(root, lib, now), now)})
	return r, root
}

func TestGetDownloadLink(t *testing.T) {
	t.Parallel()
	r, root := newRigDownloads(t)
	path := "/api/tracks/" + lt.TrackTwinB + "/download"
	tables := []string{"audit_log", "tracks", "recipes", "renditions"}
	before := make([]int, len(tables))
	for i, tb := range tables {
		before[i] = rowCount(t, r.st, tb)
	}
	var key string
	for _, c := range []struct {
		name string
		who  interface{}
		exp  string
		ttl  float64
	}{{"human", human, "2026-10-07T00:00:00Z", 12 * 3600}, {"agent", agent, "2026-10-06T12:15:00Z", 15 * 60}} {
		var m map[string]any
		if c.name == "human" {
			m = r.get(t, human, path)
		} else {
			m = r.get(t, agent, path)
		}
		if m["expires_at"] != c.exp || m["ttl_s"] != c.ttl || m["track_id"] != lt.TrackTwinB {
			t.Errorf("%s: %v", c.name, m)
		}
		u, err := url.Parse(m["url"].(string))
		if err != nil || !strings.HasPrefix(u.Path, "/media/") || !strings.HasSuffix(u.Path, ".flac") {
			t.Fatalf("%s: url %v (%v)", c.name, m["url"], err)
		}
		key = strings.TrimSuffix(strings.TrimPrefix(u.Path, "/media/"), ".flac")
		if err := r.signer.Verify(key, "flac", u.Query().Get("exp"), u.Query().Get("sig")); err != nil {
			t.Errorf("%s: link does not verify: %v", c.name, err)
		}
		if m["filename"] != "twin_b__104.flac" {
			t.Errorf("filename %v", m["filename"])
		}
		tags, _ := m["tags"].(map[string]any)
		if tags["title"] != "Twin B" || tags["album"] != "Twin A" || tags["track_number"] != float64(2) || tags["track_total"] != float64(2) ||
			tags["genre"] != "rock" || len(tags["date"].(string)) != 10 {
			t.Errorf("%s: tags %v", c.name, tags)
		}
	}
	// the sha256 and size describe the tagged file that the link serves
	file, err := root.ReadFile("derived/dl/" + key[:2] + "/" + key + ".flac")
	if err != nil {
		t.Fatal(err)
	}
	m := r.get(t, human, path)
	if m["sha256"] != shaHex(file) || m["bytes"] != float64(len(file)) {
		t.Errorf("sha/bytes %v/%v, want %s/%d", m["sha256"], m["bytes"], shaHex(file), len(file))
	}
	info, err := flacmeta.Read(bytes.NewReader(file), int64(len(file)))
	if err != nil || comments(info)["TITLE"] != "Twin B" {
		t.Errorf("served file: %v %v", comments(info), err)
	}
	for i, tb := range tables {
		if n := rowCount(t, r.st, tb); n != before[i] {
			t.Errorf("%s rows %d -> %d: get_download_link wrote to the database", tb, before[i], n)
		}
	}
	// unknown track
	r.problem(t, human, "GET", "/api/tracks/no-such-track/download", "", 404, "NOT_FOUND")
	// a lossy track answers its master: same bytes and sha256 as the master, an mp3 link
	lm := r.get(t, human, "/api/tracks/"+lt.TrackLossy+"/download")
	var msha string
	var mbytes int64
	_ = r.st.R.QueryRow(`SELECT master_sha256, master_bytes FROM tracks WHERE id = ?`, lt.TrackLossy).Scan(&msha, &mbytes)
	u, _ := url.Parse(lm["url"].(string))
	if u.Path != "/media/"+msha+".mp3" || lm["sha256"] != msha || lm["bytes"] != float64(mbytes) || lm["filename"] != "synthetic_lossy__0.mp3" {
		t.Errorf("lossy link %v", lm)
	}
	if err := r.signer.Verify(msha, "mp3", u.Query().Get("exp"), u.Query().Get("sig")); err != nil {
		t.Errorf("lossy link does not verify: %v", err)
	}
	if tags, _ := lm["tags"].(map[string]any); tags["title"] != lt.LossyTitle || tags["genre"] != nil && tags["genre"] == "" {
		t.Errorf("lossy tags %v", tags)
	}
	if _, err := root.Stat("derived/dl/" + msha[:2]); err == nil {
		t.Error("a lossy download wrote under derived/dl")
	}
}

func comments(info flacmeta.Info) map[string]string {
	m := map[string]string{}
	for _, c := range info.Comments {
		k, v, _ := strings.Cut(c, "=")
		m[k] = v
	}
	return m
}

// TestGetDownloadLink_Unavailable: without a signer or a download service the operation answers 503.
func TestGetDownloadLink_Unavailable(t *testing.T) {
	t.Parallel()
	r := newRig(t) // no Downloads wired
	r.problem(t, human, "GET", "/api/tracks/"+lt.TrackLuna+"/download", "", 503, "UNAVAILABLE")
}
