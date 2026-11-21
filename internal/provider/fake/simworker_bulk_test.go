package fake_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
)

// recFallback records the puts that reach the fallback and answers from a script.
type recFallback struct {
	mu   sync.Mutex
	urls []string
	code func(n int) int // nil => 200
	err  error
}

func (f *recFallback) PutFallback(_ context.Context, url string, _ []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, url)
	if f.err != nil {
		return 0, f.err
	}
	if f.code != nil {
		return f.code(len(f.urls)), nil
	}
	return 200, nil
}

func (f *recFallback) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.urls) }

// withFallback gives every upload of ts a fallback URL.
func withFallback(ts provider.TakeSpec) provider.TakeSpec {
	for n, u := range ts.Uploads {
		u.Fallback = &provider.FallbackTarget{URL: "https://bucket.example.com/mm/" + ts.AttemptID + "/" + n + "?X-Amz-Signature=synthetic"}
		ts.Uploads[n] = u
	}
	ts.FallbackAfterS = 120
	return ts
}

func fbWorker(t testing.TB, up fake.Uploader, fb fake.FallbackPutter) *fake.SimWorker {
	return v2Worker(t, up, nil, func(o *fake.SimWorkerOptions) { o.Fallback = fb })
}

func TestSimWorkerFallback_PrimaryDownGoesToBucket(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		code int
	}{{"503", 503}, {"500", 500}, {"429", 429}, {"408", 408}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := &recUploader{putCode: func(string) int { return tc.code }}
			fb := &recFallback{}
			res := runV2(t, fbWorker(t, up, fb), withFallback(specV2("job1", "", nil, nil)))
			if res.Failed {
				t.Fatalf("failed: %s", res.ErrText)
			}
			want := len(res.Output.Artifacts) + 1 // every artifact and mm-result.json
			if fb.count() != want {
				t.Errorf("%d fallback puts, want %d", fb.count(), want)
			}
			if !strings.HasSuffix(strings.SplitN(fb.urls[len(fb.urls)-1], "?", 2)[0], provider.ResultArtifact) {
				t.Errorf("mm-result.json was not last: %v", fb.urls[len(fb.urls)-1])
			}
		})
	}
}

func TestSimWorkerFallback_ConnectionErrorAndSticky(t *testing.T) {
	t.Parallel()
	up := &flakyUploader{recUploader: &recUploader{}, failFrom: 2} // the first artifact reaches the app, the rest do not
	fb := &recFallback{}
	res := runV2(t, fbWorker(t, up, fb), withFallback(specV2("job1", "", nil, nil)))
	if res.Failed {
		t.Fatalf("failed: %s", res.ErrText)
	}
	total := len(res.Output.Artifacts) + 1
	if fb.count() != total-1 || len(up.names()) != 1 {
		t.Errorf("%d via the app, %d via the bucket, %d artifacts", len(up.names()), fb.count(), total)
	}
	// sticky: after the switch the primary is never tried again (3 tries on the failing artifact, then none)
	if got := up.attempts(); got != 1+3 {
		t.Errorf("primary tried %d times", got)
	}
}

type flakyUploader struct {
	*recUploader
	mu       sync.Mutex
	n        int
	failFrom int
}

func (u *flakyUploader) Put(ctx context.Context, url string, body []byte, digest string) (int, error) {
	u.mu.Lock()
	u.n++
	n := u.n
	u.mu.Unlock()
	if n >= u.failFrom {
		return 0, errors.New("connection refused")
	}
	return u.recUploader.Put(ctx, url, body, digest)
}

func (u *flakyUploader) attempts() int { u.mu.Lock(); defer u.mu.Unlock(); return u.n }

func TestSimWorkerFallback_AnsweredRefusalIsFinal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		code int
		want string
	}{{403, "UPLOAD_FAILED"}, {409, "UPLOAD_CONFLICT"}, {410, "UPLOAD_CLOSED"}, {413, "UPLOAD_FAILED"}} {
		up := &recUploader{putCode: func(string) int { return tc.code }}
		fb := &recFallback{}
		res := runV2(t, fbWorker(t, up, fb), withFallback(specV2("job1", "", nil, nil)))
		if !res.Failed || res.Output.Error == nil || res.Output.Error.Code != tc.want {
			t.Errorf("%d: failed=%v err=%+v", tc.code, res.Failed, res.Output.Error)
		}
		if fb.count() != 0 {
			t.Errorf("%d: an answered refusal reached the bucket", tc.code)
		}
	}
}

func TestSimWorkerFallback_NeedsChannelAndTarget(t *testing.T) {
	t.Parallel()
	down := func() *recUploader { return &recUploader{putCode: func(string) int { return 503 }} }
	// the image has no channel: today's behaviour, the run fails
	if res := runV2(t, v2Worker(t, down(), nil), withFallback(specV2("job1", "", nil, nil))); !res.Failed {
		t.Error("a worker without the channel survived a dead primary")
	}
	// the channel exists, the target has no fallback URL
	fb := &recFallback{}
	if res := runV2(t, fbWorker(t, down(), fb), specV2("job1", "", nil, nil)); !res.Failed || fb.count() != 0 {
		t.Errorf("failed=%v puts=%d", res.Failed, fb.count())
	}
}

func TestSimWorkerFallback_BucketDownFailsTheRun(t *testing.T) {
	t.Parallel()
	up := &recUploader{putCode: func(string) int { return 503 }}
	fb := &recFallback{err: errors.New("dial tcp: refused")}
	res := runV2(t, fbWorker(t, up, fb), withFallback(specV2("job1", "", nil, nil)))
	if !res.Failed || res.Output.Error == nil || res.Output.Error.Code != "UPLOAD_FAILED" {
		t.Errorf("failed=%v err=%+v", res.Failed, res.Output.Error)
	}
	if strings.Contains(res.ErrText, "X-Amz") || strings.Contains(string(res.Raw), "X-Amz") {
		t.Error("a fallback URL reached the result")
	}
}

func TestSimWorkerFallback_CapReported(t *testing.T) {
	t.Parallel()
	up := &recUploader{}
	with := runV2(t, fbWorker(t, up, &recFallback{}), specV2("job1", "", nil, nil))
	if !slices.Equal(with.Output.Caps, []string{"fallback", "v2"}) {
		t.Errorf("caps %v", with.Output.Caps)
	}
}

func TestHTTPFallback_PutsWithoutLeakingTheURL(t *testing.T) {
	t.Parallel()
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r; w.WriteHeader(200) }))
	f := fake.HTTPFallback{Client: srv.Client()}
	code, err := f.PutFallback(t.Context(), srv.URL+"/mm/x?X-Amz-Signature=secret", []byte("abc"))
	if err != nil || code != 200 || got.ContentLength != 3 || got.Header.Get("Content-Type") != "application/octet-stream" || got.Header.Get("Content-Digest") != "" {
		t.Fatalf("code=%d err=%v req=%+v", code, err, got)
	}
	srv.Close()
	_, err = f.PutFallback(t.Context(), srv.URL+"/mm/x?X-Amz-Signature=secret", []byte("abc"))
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "X-Amz") {
		t.Errorf("err = %v", err)
	}
	if _, err := (fake.HTTPFallback{}).PutFallback(t.Context(), "http://a b/", nil); err == nil {
		t.Error("a malformed URL was accepted")
	}
}
