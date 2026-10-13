package library_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

func TestSearch_RecipeKnownAndLossyFilters(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	known := []string{lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA, lt.TrackTwinB, lt.TrackShort}
	sameSet(t, "recipe_known=false", search(t, e, library.SearchParams{RecipeKnown: bp(false)}), lt.TrackRefFlac, lt.TrackLossy)
	sameSet(t, "lossy=true", search(t, e, library.SearchParams{Lossy: bp(true)}), lt.TrackLossy)
	sameSet(t, "lossy=false&recipe_known=false", search(t, e, library.SearchParams{Lossy: bp(false), RecipeKnown: bp(false)}), lt.TrackRefFlac)
	sameSet(t, "recipe_known=true", search(t, e, library.SearchParams{RecipeKnown: bp(true)}), known...)
	sameSet(t, "title search finds the reference", search(t, e, library.SearchParams{Q: "Synthetic Reference"}), lt.TrackRefFlac)
	sameSet(t, "no filter", search(t, e, library.SearchParams{}), append(known, lt.TrackRefFlac, lt.TrackLossy)...)
	page, err := e.svc.SearchTracks(context.Background(), library.SearchParams{Lossy: bp(true)})
	if err != nil || len(page.Items) != 1 || !page.Items[0].Lossy || page.Items[0].RecipeKnown {
		t.Fatalf("summary flags: %+v %v", page.Items, err)
	}
	ids, err := e.svc.MatchTrackIDs(context.Background(), library.SearchParams{RecipeKnown: bp(false)})
	if err != nil || len(ids) != 2 {
		t.Errorf("MatchTrackIDs: %v %v", ids, err)
	}
}

func TestTrackDetail_Reference(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	d, err := e.svc.Track(ctx, lt.TrackLossy)
	if err != nil {
		t.Fatal(err)
	}
	if d.Recipe.Engine != "unknown" || d.RecipeKnown || !d.Lossy || d.Audio.MP3 != nil || d.Import.Rule != "reference" ||
		d.Recipe.Style != "" || d.Recipe.Lyrics != "" || d.Recipe.Cot != "" || len(d.Sections) != 0 || d.Score != nil {
		t.Errorf("lossy detail: %+v", d)
	}
	f, err := e.svc.Track(ctx, lt.TrackRefFlac)
	if err != nil || f.RecipeKnown || f.Lossy || f.Audio.MP3 == nil || f.Recipe.Engine != "unknown" {
		t.Errorf("flac reference detail: %+v %v", f, err)
	}
	k, err := e.svc.Track(ctx, lt.TrackLuna)
	if err != nil || !k.RecipeKnown || k.Lossy {
		t.Errorf("known detail: %v", err)
	}
}

func TestTrackRefs_Flags(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	got, err := e.svc.TrackRefs(context.Background(), []string{lt.TrackLuna, lt.TrackRefFlac, lt.TrackLossy})
	if err != nil {
		t.Fatal(err)
	}
	if r := got[lt.TrackLuna]; !r.RecipeKnown || r.Lossy {
		t.Errorf("luna: %+v", r)
	}
	if r := got[lt.TrackRefFlac]; r.RecipeKnown || r.Lossy {
		t.Errorf("reference flac: %+v", r)
	}
	if r := got[lt.TrackLossy]; r.RecipeKnown || !r.Lossy || !strings.HasSuffix(r.MasterPath, ".mp3") {
		t.Errorf("lossy: %+v", r)
	}
	lin, err := e.svc.LineageTracks(context.Background(), lt.TrackLossy)
	if err != nil || len(lin) != 1 || !lin[0].Lossy {
		t.Errorf("lineage: %+v %v", lin, err)
	}
}

func TestMedia_LossyLinks(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	m, err := e.svc.Media(ctx, lt.TrackLossy)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := e.svc.Track(ctx, lt.TrackLossy)
	if m.Flac != nil || !m.Lossy || m.MP3.SHA256 != d.Audio.MasterSHA256 || m.MP3.Bytes != d.Audio.MasterBytes || m.MP3.ContentType != "audio/mpeg" {
		t.Fatalf("media: %+v", m)
	}
	if want := int(float64(d.Audio.MasterBytes)*8/d.DurationS/1000 + 0.5); m.MP3.BitrateKbps != want || want != 128 {
		t.Errorf("bitrate=%d want %d (=128)", m.MP3.BitrateKbps, want)
	}
	n, err := e.svc.Media(ctx, lt.TrackLuna)
	if err != nil || n.Flac == nil || n.Lossy || n.MP3.BitrateKbps != 320 {
		t.Errorf("normal media: %+v %v", n, err)
	}
	rf, err := e.svc.Media(ctx, lt.TrackRefFlac)
	if err != nil || rf.Flac == nil || rf.Lossy || rf.MP3.BitrateKbps != 320 {
		t.Errorf("reference flac media: %+v %v", rf, err)
	}
}

func TestResolve_LossyMaster(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	m, _ := e.svc.Media(ctx, lt.TrackLossy)
	obj, err := e.svc.Resolve(ctx, m.MP3.SHA256, "mp3")
	if err != nil || obj.ContentType != "audio/mpeg" || !strings.HasPrefix(obj.RelPath, "audio/masters/") || !strings.HasSuffix(obj.RelPath, ".mp3") || obj.ETag != m.MP3.SHA256 {
		t.Fatalf("resolve mp3: %+v %v", obj, err)
	}
	if _, err := e.svc.Resolve(ctx, m.MP3.SHA256, "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("flac with a lossy master sha: %v", err)
	}
	n, _ := e.svc.Media(ctx, lt.TrackLuna)
	if o, err := e.svc.Resolve(ctx, n.Flac.SHA256, "flac"); err != nil || o.ContentType != "audio/flac" {
		t.Errorf("normal flac unchanged: %+v %v", o, err)
	}
	if o, err := e.svc.Resolve(ctx, n.MP3.SHA256, "mp3"); err != nil || !strings.HasPrefix(o.RelPath, "derived/") {
		t.Errorf("normal mp3 unchanged: %+v %v", o, err)
	}
	// the master of a normal track is not an "mp3" address
	if _, err := e.svc.Resolve(ctx, n.Flac.SHA256, "mp3"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("flac master as mp3: %v", err)
	}
}

func TestHandler_LossyRange(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	m, _ := e.svc.Media(ctx, lt.TrackLossy)
	root, err := os.OpenRoot(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	key := []byte("0123456789abcdef0123456789abcdef")
	signer := serve.NewSigner(key, func() time.Time { return lt.FixedNow })
	h := serve.Handler(signer, root, e.svc, nil)
	size := m.MP3.Bytes
	obj, _ := e.svc.Resolve(ctx, m.MP3.SHA256, "mp3")
	full, err := os.ReadFile(filepath.Join(e.dir, obj.RelPath))
	if err != nil || int64(len(full)) != size {
		t.Fatalf("master file: %d bytes, %v (want %d)", len(full), err, size)
	}
	get := func(sha, ext, rng string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, signer.Sign(sha, ext, time.Hour, time.Minute).URL, nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	sz := strconv.FormatInt(size, 10)
	for _, c := range []struct {
		rng, cr string
		code    int
		body    []byte
	}{
		{"bytes=0-9", "bytes 0-9/" + sz, 206, full[:10]},
		{"bytes=-100", "bytes " + strconv.FormatInt(size-100, 10) + "-" + strconv.FormatInt(size-1, 10) + "/" + sz, 206, full[size-100:]},
		{"bytes=" + sz + "-", "bytes */" + sz, 416, nil},
		{"", "", 200, full},
	} {
		w := get(m.MP3.SHA256, "mp3", c.rng)
		if w.Code != c.code || (c.cr != "" && w.Header().Get("Content-Range") != c.cr) {
			t.Errorf("range %q: %d %q", c.rng, w.Code, w.Header().Get("Content-Range"))
			continue
		}
		if c.code != 416 {
			if ct := w.Header().Get("Content-Type"); ct != "audio/mpeg" {
				t.Errorf("range %q: content type %q", c.rng, ct)
			}
			b, _ := io.ReadAll(w.Body)
			if string(b) != string(c.body) {
				t.Errorf("range %q: body mismatch", c.rng)
			}
		}
	}
	if w := get(m.MP3.SHA256, "flac", ""); w.Code != 404 {
		t.Errorf("flac link with the lossy sha: %d", w.Code)
	}
}
