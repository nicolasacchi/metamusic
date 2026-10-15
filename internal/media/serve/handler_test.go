package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var handlerNow = time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

type stubResolver map[string]Object

func (s stubResolver) Resolve(_ context.Context, sha, ext string) (Object, error) {
	if o, ok := s[sha+"."+ext]; ok {
		return o, nil
	}
	return Object{}, ErrNotFound
}

type rig struct {
	h      http.Handler
	signer *Signer
	data   []byte
	sha    string
	root   string
}

func objBytes() []byte {
	b := make([]byte, 10000)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func newRig(t *testing.T, probe *Object) *rig {
	t.Helper()
	dir := t.TempDir()
	data := objBytes()
	if err := os.MkdirAll(filepath.Join(dir, "audio"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"audio/a.flac", "audio/a.mp3", "audio/probe.flac"} {
		if err := os.WriteFile(filepath.Join(dir, n), data, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	sha := strings.Repeat("ab", 32)
	mod := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	res := stubResolver{
		sha + ".flac":                      {RelPath: "audio/a.flac", ContentType: "audio/flac", ETag: sha, ModTime: mod},
		sha + ".mp3":                       {RelPath: "audio/a.mp3", ContentType: "audio/mpeg", ETag: sha, ModTime: mod},
		strings.Repeat("ee", 32) + ".flac": {RelPath: "audio/gone.flac", ContentType: "audio/flac", ETag: strings.Repeat("ee", 32), ModTime: mod},
	}
	s := NewSigner(testKey, fixedClock(handlerNow))
	return &rig{h: Handler(s, root, res, probe), signer: s, data: data, sha: sha, root: dir}
}

func (r *rig) url(sha, ext string) string {
	return r.signer.Sign(sha, ext, time.Hour, time.Minute).URL
}

func (r *rig) do(method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

func TestAcceptance_M1_MediaRangeMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("acceptance test (C23 tier)")
	}
	r := newRig(t, nil)
	u := r.url(r.sha, "flac")
	etag := `"` + r.sha + `"`
	type tc struct {
		name   string
		method string
		hdr    map[string]string
		code   int
		cr     string
		body   []byte
		clen   string
	}
	d := r.data
	cases := []tc{
		{"full", "GET", nil, 200, "", d, "10000"},
		{"first2", "GET", map[string]string{"Range": "bytes=0-1"}, 206, "bytes 0-1/10000", d[:2], "2"},
		{"openend", "GET", map[string]string{"Range": "bytes=100-"}, 206, "bytes 100-9999/10000", d[100:], "9900"},
		{"suffix", "GET", map[string]string{"Range": "bytes=-500"}, 206, "bytes 9500-9999/10000", d[9500:], "500"},
		{"clamped", "GET", map[string]string{"Range": "bytes=9999-20000"}, 206, "bytes 9999-9999/10000", d[9999:], "1"},
		{"unsat", "GET", map[string]string{"Range": "bytes=10000-"}, 416, "bytes */10000", nil, ""},
		{"garbage", "GET", map[string]string{"Range": "bytes=abc"}, 416, "", nil, ""},
		{"ifrange-match", "GET", map[string]string{"Range": "bytes=0-1", "If-Range": etag}, 206, "bytes 0-1/10000", d[:2], "2"},
		{"ifrange-other", "GET", map[string]string{"Range": "bytes=0-1", "If-Range": `"other"`}, 200, "", d, "10000"},
		{"head", "HEAD", nil, 200, "", nil, "10000"},
		{"head-range", "HEAD", map[string]string{"Range": "bytes=0-1"}, 206, "bytes 0-1/10000", nil, "2"},
		{"gzip", "GET", map[string]string{"Accept-Encoding": "gzip"}, 200, "", d, "10000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := r.do(c.method, u, c.hdr)
			if w.Code != c.code {
				t.Fatalf("status %d, want %d (%s)", w.Code, c.code, w.Body)
			}
			if c.cr != "" && w.Header().Get("Content-Range") != c.cr {
				t.Errorf("Content-Range %q, want %q", w.Header().Get("Content-Range"), c.cr)
			}
			if c.clen != "" && w.Header().Get("Content-Length") != c.clen {
				t.Errorf("Content-Length %q, want %q", w.Header().Get("Content-Length"), c.clen)
			}
			if c.code < 300 {
				if c.method == "GET" && !bytes.Equal(w.Body.Bytes(), c.body) {
					t.Errorf("body mismatch (%d bytes)", w.Body.Len())
				}
				if c.method == "HEAD" && w.Body.Len() != 0 {
					t.Errorf("HEAD body %d bytes", w.Body.Len())
				}
				checkOK(t, w, "audio/flac", etag)
			}
			if w.Header().Get("Content-Encoding") != "" {
				t.Errorf("Content-Encoding %q", w.Header().Get("Content-Encoding"))
			}
		})
	}

	t.Run("multirange", func(t *testing.T) {
		w := r.do("GET", u, map[string]string{"Range": "bytes=0-1,5-6"})
		if w.Code != 206 {
			t.Fatalf("status %d", w.Code)
		}
		mt, params, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
		if err != nil || mt != "multipart/byteranges" {
			t.Fatalf("content type %q", w.Header().Get("Content-Type"))
		}
		mr := multipart.NewReader(w.Body, params["boundary"])
		want := []struct {
			cr   string
			body []byte
		}{{"bytes 0-1/10000", r.data[0:2]}, {"bytes 5-6/10000", r.data[5:7]}}
		for i, x := range want {
			p, err := mr.NextPart()
			if err != nil {
				t.Fatalf("part %d: %v", i, err)
			}
			if p.Header.Get("Content-Type") != "audio/flac" || p.Header.Get("Content-Range") != x.cr {
				t.Errorf("part %d headers %v", i, p.Header)
			}
			b, _ := io.ReadAll(p)
			if !bytes.Equal(b, x.body) {
				t.Errorf("part %d body", i)
			}
		}
		for _, h := range []string{"ETag", "Cache-Control", "Accept-Ranges", "X-Content-Type-Options"} {
			if w.Header().Get(h) == "" {
				t.Errorf("multipart response lacks %s", h)
			}
		}
	})

	t.Run("if-none-match", func(t *testing.T) {
		w := r.do("GET", u, map[string]string{"If-None-Match": etag})
		if w.Code != 304 || w.Body.Len() != 0 {
			t.Fatalf("status %d body %d", w.Code, w.Body.Len())
		}
	})

	t.Run("post", func(t *testing.T) {
		w := r.do("POST", u, nil)
		assertProblem(t, w, 405, "METHOD_NOT_ALLOWED")
		if w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("Allow %q", w.Header().Get("Allow"))
		}
	})

	t.Run("mp3", func(t *testing.T) {
		w := r.do("GET", r.url(r.sha, "mp3"), nil)
		if w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
		checkOK(t, w, "audio/mpeg", etag)
	})
}

func checkOK(t *testing.T, w *httptest.ResponseRecorder, ct, etag string) {
	t.Helper()
	h := w.Header()
	if mt, _, _ := mime.ParseMediaType(h.Get("Content-Type")); w.Code != 206 || !strings.HasPrefix(h.Get("Content-Type"), "multipart/") {
		if mt != ct {
			t.Errorf("Content-Type %q, want %q", h.Get("Content-Type"), ct)
		}
	}
	want := map[string]string{
		"Accept-Ranges":                "bytes",
		"ETag":                         etag,
		"Cache-Control":                "private, max-age=31536000, immutable",
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Resource-Policy": "same-origin",
	}
	for k, v := range want {
		if h.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, h.Get(k), v)
		}
	}
}

func assertProblem(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d (%s)", w.Code, status, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type %q", ct)
	}
	if !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
		t.Errorf("body %s lacks code %s", w.Body, code)
	}
}

func TestHandler_Paths(t *testing.T) {
	r := newRig(t, nil)
	sig := func(sha, ext string) string { // signature material for a path the regex must refuse
		q := r.signer.Sign(sha, ext, time.Hour, time.Minute).URL
		return q[strings.Index(q, "?"):]
	}
	q := sig(r.sha, "flac")
	for _, p := range []string{
		"/media/" + strings.ToUpper(r.sha) + ".flac",
		"/media/" + r.sha[:63] + ".flac",
		"/media/" + r.sha + ".wav",
		"/media/" + r.sha + ".flac/extra",
		"/media/" + r.sha,
		"/media/",
		"/media/sub/" + r.sha + ".flac",
		"/media/" + r.sha + ".JPG",
	} {
		assertProblem(t, r.do("GET", p+q, nil), 404, "NOT_FOUND")
	}
}

func TestHandler_Signatures(t *testing.T) {
	r := newRig(t, nil)
	good := r.url(r.sha, "flac")
	i := strings.Index(good, "?")
	base := good[:i]
	exp, sig := parseLink(t, good)
	late := NewSigner(testKey, fixedClock(handlerNow.Add(-3*time.Hour))).Sign(r.sha, "flac", time.Hour, time.Minute).URL
	far := NewSigner(testKey, fixedClock(handlerNow.Add(20*time.Hour))).Sign(r.sha, "flac", time.Hour, time.Minute).URL
	flip := []byte(sig)
	flip[0] ^= 1
	pad := []byte(sig)
	pad[len(pad)-1] ^= 1 // low bits of the last char are base64 padding
	cases := []struct {
		name, target, code string
	}{
		{"non-canonical-sig", base + "?exp=" + exp + "&sig=" + string(pad), "MEDIA_LINK_INVALID"},
		{"no-query", base, "MEDIA_LINK_INVALID"},
		{"no-sig", base + "?exp=" + exp, "MEDIA_LINK_INVALID"},
		{"no-exp", base + "?sig=" + sig, "MEDIA_LINK_INVALID"},
		{"tampered", base + "?exp=" + exp + "&sig=" + string(flip), "MEDIA_LINK_INVALID"},
		{"wrong-ext", strings.Replace(good, ".flac", ".mp3", 1), "MEDIA_LINK_INVALID"},
		{"expired", late, "MEDIA_LINK_EXPIRED"},
		{"too-far", far, "MEDIA_LINK_INVALID"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertProblem(t, r.do("GET", c.target, nil), 403, c.code)
		})
	}
	t.Run("extra-params-ignored", func(t *testing.T) {
		if w := r.do("GET", good+"&x=1", nil); w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
	})
	t.Run("unknown-sha", func(t *testing.T) {
		assertProblem(t, r.do("GET", r.url(strings.Repeat("11", 32), "flac"), nil), 404, "NOT_FOUND")
	})
	t.Run("probe", func(t *testing.T) {
		psha := strings.Repeat("cd", 32)
		p := newRig(t, &Object{RelPath: "audio/probe.flac", ContentType: "audio/flac", ETag: psha})
		w := p.do("GET", p.url(psha, "flac"), nil)
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), p.data) {
			t.Fatalf("probe status %d", w.Code)
		}
		if w.Header().Get("ETag") != `"`+psha+`"` {
			t.Errorf("ETag %q", w.Header().Get("ETag"))
		}
		assertProblem(t, p.do("GET", p.url(psha, "mp3"), nil), 404, "NOT_FOUND")
	})
}

func TestHandler_MissingFile500(t *testing.T) {
	r := newRig(t, nil)
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	sha := strings.Repeat("ee", 32)
	u := r.url(sha, "flac")
	assertProblem(t, r.do("GET", u, nil), 500, "INTERNAL")
	log := buf.String()
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(log)), &rec); err != nil {
		t.Fatalf("log not one JSON line: %v %s", err, log)
	}
	if rec["sha"] != sha || rec["ext"] != "flac" {
		t.Errorf("log lacks sha/ext attrs: %s", log)
	}
	q, _ := url.Parse(u)
	exp, sig := q.Query().Get("exp"), q.Query().Get("sig")
	if strings.Contains(log, "exp=") || strings.Contains(log, "sig=") || (sig != "" && strings.Contains(log, sig)) || (exp != "" && strings.Contains(log, exp)) {
		t.Errorf("log leaks the query: %s", log)
	}
	if strings.Count(strings.TrimSpace(log), "\n") != 0 {
		t.Errorf("want exactly one log line: %s", log)
	}
}

type errResolver struct{}

func (errResolver) Resolve(context.Context, string, string) (Object, error) {
	return Object{}, errors.New("db down")
}

func TestHandler_ResolverAndRootErrors(t *testing.T) {
	s := NewSigner(testKey, fixedClock(handlerNow))
	u := s.Sign(testSHA, "flac", time.Hour, time.Minute).URL
	for name, h := range map[string]http.Handler{
		"resolver-error": Handler(s, nil, errResolver{}, nil),
		"nil-root":       Handler(s, nil, stubResolver{testSHA + ".flac": {RelPath: "x", ContentType: "audio/flac", ETag: testSHA}}, nil),
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", u, nil))
		if w.Code != 500 || !strings.Contains(w.Body.String(), `"INTERNAL"`) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

func TestHandler_DirectoryIs500(t *testing.T) {
	r := newRig(t, nil)
	res := stubResolver{r.sha + ".flac": {RelPath: "audio", ContentType: "audio/flac", ETag: r.sha}}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	h := Handler(r.signer, root, res, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", r.url(r.sha, "flac"), nil))
	if w.Code != 500 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestHandler_ExportExtensions(t *testing.T) {
	dir := t.TempDir()
	body := objBytes()
	for _, n := range []string{"e.jsonl", "e.zip", "e.exe"} {
		if err := os.WriteFile(filepath.Join(dir, n), body, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	sha := strings.Repeat("cd", 32)
	mod := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	res := stubResolver{
		sha + ".jsonl": {RelPath: "e.jsonl", ContentType: "application/x-ndjson", ETag: sha, ModTime: mod, DownloadName: "songs.jsonl"},
		sha + ".zip":   {RelPath: "e.zip", ContentType: "application/zip", ETag: sha, ModTime: mod, DownloadName: "mix.zip"},
		sha + ".exe":   {RelPath: "e.exe", ContentType: "application/octet-stream", ETag: sha, ModTime: mod},
		sha + ".flac":  {RelPath: "e.zip", ContentType: "audio/flac", ETag: sha, ModTime: mod},
	}
	s := NewSigner(testKey, fixedClock(handlerNow))
	r := &rig{h: Handler(s, root, res, nil), signer: s, data: body, sha: sha, root: dir}

	w := r.do("GET", r.url(sha, "jsonl"), nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/x-ndjson" ||
		w.Header().Get("Content-Disposition") != `attachment; filename="songs.jsonl"` || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("jsonl: %d %v", w.Code, w.Header())
	}
	w = r.do("GET", r.url(sha, "zip"), map[string]string{"Range": "bytes=10-19"})
	if w.Code != 206 || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Content-Disposition") != `attachment; filename="mix.zip"` ||
		!bytes.Equal(w.Body.Bytes(), body[10:20]) || w.Header().Get("Content-Range") != "bytes 10-19/10000" {
		t.Fatalf("zip range: %d %v", w.Code, w.Header())
	}
	if w = r.do("GET", r.url(sha, "flac"), nil); w.Code != 200 || w.Header().Get("Content-Disposition") != "" {
		t.Errorf("library objects send no Content-Disposition: %d %v", w.Code, w.Header())
	}
	// unsigned and tampered requests are refused before the resolver is asked
	assertProblem(t, r.do("GET", "/media/"+sha+".jsonl", nil), 403, "MEDIA_LINK_INVALID")
	assertProblem(t, r.do("GET", strings.Replace(r.url(sha, "jsonl"), ".jsonl", ".zip", 1), nil), 403, "MEDIA_LINK_INVALID")
	// an extension outside flac|mp3|jsonl|zip is no media address
	assertProblem(t, r.do("GET", r.url(sha, "exe"), nil), 404, "NOT_FOUND")
}

func TestHandler_JPGContentType(t *testing.T) {
	dir := t.TempDir()
	body := objBytes()
	if err := os.WriteFile(filepath.Join(dir, "s.jpg"), body, 0o444); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	sha := strings.Repeat("ef", 32)
	mod := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	res := stubResolver{sha + ".jpg": {RelPath: "s.jpg", ContentType: "image/jpeg", ETag: sha, ModTime: mod}}
	s := NewSigner(testKey, fixedClock(handlerNow))
	r := &rig{h: Handler(s, root, res, nil), signer: s, data: body, sha: sha, root: dir}

	w := r.do("GET", r.url(sha, "jpg"), nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("Content-Disposition") != "" || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("jpg: %d %v", w.Code, w.Header())
	}
	w = r.do("GET", r.url(sha, "jpg"), map[string]string{"Range": "bytes=5-14"})
	if w.Code != 206 || w.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(w.Body.Bytes(), body[5:15]) || w.Header().Get("Content-Range") != "bytes 5-14/10000" {
		t.Fatalf("jpg range: %d %v", w.Code, w.Header())
	}
}
