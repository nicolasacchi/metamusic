package subsonic

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
	"github.com/nicolasacchi/metamusic/internal/playlists"
)

// s3rig is the five-track fixture library behind real judgments, listening, playlists and library services plus
// fake cover and download sources.
type s3rig struct {
	*rig
	dir    string
	covers *fakeCovers
	dls    *fakeDownloads
	judg   *judgments.Service
	plist  *playlists.Service
	lib    *library.Service
}

var s3ids atomic.Int64

func s3newID() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0x500000+s3ids.Add(1)) }

func newS3Rig(t *testing.T) *s3rig {
	t.Helper()
	r := newLibRig(t)
	return withServices(t, r, t.TempDir())
}

// withServices wires the real services (and the fakes) into an existing rig whose data directory is dir.
func withServices(t *testing.T, r *rig, dir string) *s3rig {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	x := &s3rig{rig: r, dir: dir}
	x.lib = library.New(r.st, root, r.clk.now)
	x.judg = judgments.New(r.st, x.lib, r.clk.now, s3newID)
	x.plist = playlists.New(r.st, x.lib, r.clk.now, s3newID)
	x.covers = &fakeCovers{dir: dir}
	x.dls = &fakeDownloads{dir: dir}
	r.srv.o.Data = root
	r.srv.o.Lib, r.srv.resolve = x.lib, x.lib
	r.srv.judg = x.judg
	r.srv.listen = listening.New(r.st, r.clk.now)
	r.srv.plist = x.plist
	r.srv.covers, r.srv.dls = x.covers, x.dls
	return x
}

// writeFixtureFiles writes the fixture masters into the rig's data directory.
func (x *s3rig) writeFixtureFiles(t *testing.T) {
	t.Helper()
	librarytest.WriteFiles(t, x.dir)
}

// newClient creates one more Subsonic client and returns its api key.
func (r *rig) newClient(t *testing.T, name string, mp3, write bool) string {
	t.Helper()
	human := auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin}, Via: "proxy"}
	f := "raw"
	if mp3 {
		f = "mp3"
	}
	_, sec, err := r.creds.Create(context.Background(), human, auth.NewSubsonicClient{Name: name, Username: name, DefaultFormat: f, Write: write, APIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	return sec.APIKey
}

// raw sends one request with any method and headers as the read-write client.
func (r *rig) raw(method, endpoint string, q url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, "/rest/"+endpoint+"?"+q.Encode(), nil)
	req.RemoteAddr = libIP + ":4000"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.srv.ServeHTTP(w, req)
	return w
}

// rwq is keyParams for the read-write client where a name may repeat (id, songId): every non-framing pair is added.
func (r *rig) rwq(kv ...string) url.Values { return multiParams(r.rwKey, kv...) }

func multiParams(key string, kv ...string) url.Values {
	q := keyParams(key)
	for i := 0; i+1 < len(kv); i += 2 {
		switch kv[i] {
		case "c", "v", "f":
			q.Set(kv[i], kv[i+1])
		default:
			q.Add(kv[i], kv[i+1])
		}
	}
	return q
}

func (x *s3rig) call(endpoint string, kv ...string) map[string]any {
	x.t.Helper()
	return x.get(endpoint, x.rwq(kv...), libIP).doc(x.t)
}

func (x *s3rig) callCode(endpoint string, kv ...string) int {
	x.t.Helper()
	return x.get(endpoint, x.rwq(kv...), libIP).code(x.t)
}

// fakeCovers answers every known id with one PNG-typed file and records the sizes it was asked for.
type fakeCovers struct {
	dir   string
	mu    sync.Mutex
	calls []string // "track:<id>:<size>" | "album:<root>:<size>"
}

func (f *fakeCovers) object(key string) (serve.Object, error) {
	rel := filepath.Join("derived", "covers", key+".png")
	full := filepath.Join(f.dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return serve.Object{}, err
	}
	if err := os.WriteFile(full, []byte("\x89PNG-fake-"+key), 0o644); err != nil {
		return serve.Object{}, err
	}
	return serve.Object{RelPath: filepath.ToSlash(rel), ContentType: "image/png", ETag: key, ModTime: time.Unix(1700000000, 0)}, nil
}

func (f *fakeCovers) EnsureTrack(_ context.Context, id string, size int) (serve.Object, string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("track:%s:%d", id, size))
	f.mu.Unlock()
	o, err := f.object("t-" + id)
	return o, "", err
}

func (f *fakeCovers) EnsureAlbum(_ context.Context, root string, size int) (serve.Object, string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("album:%s:%d", root, size))
	f.mu.Unlock()
	o, err := f.object("a-" + root)
	return o, "", err
}

func (f *fakeCovers) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1]
}

// fakeDownloads serves a fixed file per track with only Filename set (the handler must derive the disposition).
type fakeDownloads struct {
	dir   string
	mu    sync.Mutex
	asked []string
}

func (f *fakeDownloads) Ensure(_ context.Context, id string) (tagged.Download, error) {
	f.mu.Lock()
	f.asked = append(f.asked, id)
	f.mu.Unlock()
	rel := filepath.Join("derived", "dl", "dl-"+id+".flac")
	full := filepath.Join(f.dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return tagged.Download{}, err
	}
	body := []byte("tagged-" + id)
	if err := os.WriteFile(full, body, 0o644); err != nil {
		return tagged.Download{}, err
	}
	return tagged.Download{
		Object:   serve.Object{RelPath: filepath.ToSlash(rel), ContentType: "audio/flac", ETag: "dl-" + id, ModTime: time.Unix(1700000000, 0)},
		Key:      "dl-" + id,
		Filename: "slug__7.flac",
		Bytes:    int64(len(body)),
	}, nil
}

func statusOK(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("HTTP %d, want %d (body %.200q)", w.Code, want, w.Body.String())
	}
}
