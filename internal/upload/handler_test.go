package upload

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func code(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct{ Code string }
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("not a problem body: %q", w.Body.String())
	}
	return b.Code
}

func TestUploadSigner_Golden(t *testing.T) {
	t.Parallel()
	s := NewSigner([]byte("synthetic-secret-key-for-tests-0001"))
	const (
		up    = "01900000-0000-7000-8000-00000000d001"
		take  = "01900000-0000-7000-8000-00000000a001"
		att   = "01900000-0000-7000-8000-00000000c001"
		exp   = int64(1760000000)
		goldU = "https://mm.example.com/worker/v1/uploads/01900000-0000-7000-8000-00000000d001?exp=1760000000&sig=VMyqWOyBBBXQcfbWVfCvd8iw64R8S0oUYRm7gpR7EPI"
		goldS = "https://mm.example.com/worker/v1/attempts/01900000-0000-7000-8000-00000000c001/start?exp=1760000000&sig=wx1WTtD630WHqDZ4sUC1lPx_gpnmVCS_3UpHz0mWgKs"
	)
	if got := s.URL(publicURL, up, take, "audio.flac", 167772160, exp); got != goldU {
		t.Errorf("upload url\n got %s\nwant %s", got, goldU)
	}
	if got := s.StartURL(publicURL, att, take, exp); got != goldS {
		t.Errorf("start url\n got %s\nwant %s", got, goldS)
	}
	sig := goldU[strings.Index(goldU, "sig=")+4:]
	if !s.Verify(up, take, "audio.flac", 167772160, exp, sig) {
		t.Fatal("golden signature does not verify")
	}
	for name, ok := range map[string]bool{
		"id":       s.Verify("01900000-0000-7000-8000-00000000d002", take, "audio.flac", 167772160, exp, sig),
		"take":     s.Verify(up, "01900000-0000-7000-8000-00000000a002", "audio.flac", 167772160, exp, sig),
		"artifact": s.Verify(up, take, "plan.json", 167772160, exp, sig),
		"max":      s.Verify(up, take, "audio.flac", 167772161, exp, sig),
		"exp":      s.Verify(up, take, "audio.flac", 167772160, exp+1, sig),
	} {
		if ok {
			t.Errorf("changing %s still verifies", name)
		}
	}
}

func TestUploadHandler_Matrix(t *testing.T) {
	t.Parallel()
	body := []byte("synthetic flac bytes")

	t.Run("method", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, u := r.target("audio.flac")
		for _, m := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
			w := httptest.NewRecorder()
			r.svc.Handler().ServeHTTP(w, httptest.NewRequest(m, u, nil))
			if w.Code != 405 || w.Header().Get("Allow") != "PUT" || code(t, w) != "METHOD_NOT_ALLOWED" {
				t.Errorf("%s: %d %q", m, w.Code, w.Header().Get("Allow"))
			}
		}
	})
	t.Run("path", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		for _, p := range []string{"/worker/v1/uploads/short", "/worker/v1/uploads/", "/worker/v1/uploads/01900000-0000-7000-8000-00000000d00X", "/worker/v1/uploads/01900000-0000-7000-8000-000000000001/extra"} {
			if w := r.put(p+"?exp=1&sig=x", body, nil); w.Code != 404 || code(t, w) != "NOT_FOUND" {
				t.Errorf("%s: %d", p, w.Code)
			}
		}
	})
	t.Run("link invalid", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		base := u[:strings.Index(u, "?")]
		sig := u[strings.Index(u, "sig=")+4:]
		unknown := "/worker/v1/uploads/01900000-0000-7000-8000-0000deadbeef?exp=" + itoa(r.exp) + "&sig=" + sig
		for name, target := range map[string]string{
			"no query":     base,
			"no sig":       base + "?exp=" + itoa(r.exp),
			"no exp":       base + "?sig=" + sig,
			"bad exp":      base + "?exp=abc&sig=" + sig,
			"short sig":    base + "?exp=" + itoa(r.exp) + "&sig=abc",
			"wrong sig":    base + "?exp=" + itoa(r.exp) + "&sig=" + strings.Repeat("A", 43),
			"other exp":    base + "?exp=" + itoa(r.exp-1) + "&sig=" + sig,
			"unknown id":   unknown,
			"exp past row": base + "?exp=" + itoa(r.exp+1) + "&sig=" + r.sig.sig(id, jobID, "audio.flac", 1000, r.exp+1),
		} {
			if w := r.put(target, body, nil); w.Code != 403 || code(t, w) != "UPLOAD_LINK_INVALID" {
				t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
			}
		}
	})
	t.Run("expired", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, u := r.target("audio.flac")
		r.clk.Advance(4 * time.Hour)
		if w := r.put(u, body, nil); w.Code != 403 || code(t, w) != "UPLOAD_LINK_EXPIRED" {
			t.Errorf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("closed", func(t *testing.T) {
		t.Parallel()
		cases := map[string]func(r *rig){
			"attempt closed": func(r *rig) {
				tx, _ := r.st.W.Begin()
				if err := r.svc.CloseAttempt(t.Context(), tx, attemptID); err != nil {
					t.Fatal(err)
				}
				tx.Commit()
			},
			"attempt rejected": func(r *rig) {
				r.exec(`UPDATE job_attempts SET state='rejected', terminal_basis='not_accepted', terminal_at=? WHERE id=?`, store.FormatTime(t0), attemptID)
			},
			"job terminal": func(r *rig) {
				r.exec(`UPDATE jobs SET state='failing' WHERE id=?`, jobID)
				r.exec(`UPDATE jobs SET state='failed', error_code='WORKER_LOST', finished_at=?, lease_owner=NULL, lease_expires_at=? WHERE id=?`, store.FormatTime(t0), nil, jobID)
			},
			"other attempt delivered": func(r *rig) {
				r.exec(`INSERT INTO job_attempts(id,job_id,n,backend,state,submitted_at,window_ends_at,price_pph_micro_usd,created_at) VALUES('01900000-0000-7000-8000-00000000c002',?,2,'fake','pending',?,?,690000,?)`,
					jobID, store.FormatTime(t0), store.FormatTime(t0), store.FormatTime(t0))
				r.exec(`INSERT INTO job_events(job_id, attempt_id, at, event) VALUES(?, '01900000-0000-7000-8000-00000000c002', ?, 'result_received')`, jobID, store.FormatTime(t0))
			},
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				r := newRigWith(t, "starting", "pending")
				_, u := r.target("audio.flac")
				mutate(r)
				if w := r.put(u, body, nil); w.Code != 410 || code(t, w) != "UPLOAD_CLOSED" {
					t.Errorf("%d %s", w.Code, w.Body.String())
				}
			})
		}
	})
	t.Run("a done attempt may finish its uploads", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, u := r.target("audio.flac")
		r.exec(`UPDATE job_attempts SET state='done', terminal_basis='provider', terminal_at=? WHERE id=?`, store.FormatTime(t0), attemptID)
		if w := r.put(u, body, nil); w.Code != 201 {
			t.Errorf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("length and digest", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, u := r.target("audio.flac")
		if w := r.put(u, body, func(q *http.Request) { q.ContentLength = -1 }); w.Code != 411 || code(t, w) != "LENGTH_REQUIRED" {
			t.Errorf("411: %d", w.Code)
		}
		if w := r.put(u, make([]byte, 1001), nil); w.Code != 413 || code(t, w) != "PAYLOAD_TOO_LARGE" {
			t.Errorf("413: %d", w.Code)
		}
		for name, h := range map[string]string{"absent": "", "md5": "md5=:AAAA:", "short": "sha-256=:AAAA:", "no colons": "sha-256=" + strings.Repeat("A", 43) + "=", "hex": "sha-256=:" + strings.Repeat("a", 64) + ":"} {
			w := r.put(u, body, func(q *http.Request) {
				if h == "" {
					q.Header.Del("Content-Digest")
				} else {
					q.Header.Set("Content-Digest", h)
				}
			})
			if w.Code != 400 || code(t, w) != "DIGEST_REQUIRED" {
				t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
			}
		}
	})
	t.Run("repeat", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		if w := r.put(u, body, nil); w.Code != 201 {
			t.Fatalf("first: %d %s", w.Code, w.Body.String())
		}
		// the same digest again: 200 without reading the body
		w := r.put(u, nil, func(q *http.Request) {
			q.Body = io.NopCloser(errReader{})
			q.ContentLength = int64(len(body))
			q.Header.Set("Content-Digest", digestOf(body))
		})
		if w.Code != 200 {
			t.Errorf("repeat: %d %s", w.Code, w.Body.String())
		}
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if got["upload_id"] != id || got["bytes"] != float64(len(body)) {
			t.Errorf("repeat body %v", got)
		}
		if w := r.put(u, []byte("different"), nil); w.Code != 409 || code(t, w) != "UPLOAD_CONFLICT" {
			t.Errorf("conflict: %d %s", w.Code, w.Body.String())
		}
		if n := r.count(`SELECT COUNT(*) FROM job_events WHERE event='upload'`); n != 1 {
			t.Errorf("upload events %d", n)
		}
	})
	t.Run("in progress", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		pr, pw := io.Pipe()
		first := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			req := httptest.NewRequest(http.MethodPut, u, pr)
			req.ContentLength = int64(len(body))
			req.Header.Set("Content-Digest", digestOf(body))
			w := httptest.NewRecorder()
			r.svc.Handler().ServeHTTP(w, req)
			first <- w
		}()
		for i := 0; !r.svc.inFlight(id); i++ {
			if i > 2000 {
				t.Fatal("first PUT never started")
			}
			time.Sleep(time.Millisecond)
		}
		w := r.put(u, body, nil)
		if w.Code != 409 || code(t, w) != "UPLOAD_IN_PROGRESS" || w.Header().Get("Retry-After") != "2" {
			t.Errorf("second: %d %s %q", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
		}
		_, _ = pw.Write(body)
		pw.Close()
		if w := <-first; w.Code != 201 {
			t.Errorf("first: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("short body", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		w := r.put(u, body[:5], func(q *http.Request) {
			q.ContentLength = int64(len(body))
			q.Header.Set("Content-Digest", digestOf(body))
		})
		if w.Code != 400 || code(t, w) != "LENGTH_MISMATCH" {
			t.Errorf("%d %s", w.Code, w.Body.String())
		}
		r.noFiles(id)
	})
	t.Run("wrong digest", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		w := r.put(u, body, func(q *http.Request) { q.Header.Set("Content-Digest", digestOf([]byte("other"))) })
		if w.Code != 422 || code(t, w) != "UPLOAD_DIGEST_MISMATCH" {
			t.Errorf("%d %s", w.Code, w.Body.String())
		}
		r.noFiles(id)
		if st := r.str(`SELECT state FROM uploads WHERE id = ?`, id); st != "open" {
			t.Errorf("state %s", st)
		}
	})
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		w := r.put(u, body, nil)
		if w.Code != 201 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		fi, err := r.data.Stat("incoming/uploads/" + id)
		if err != nil || fi.Mode().Perm() != 0o400 || fi.Size() != int64(len(body)) {
			t.Fatalf("final file: %v %v", fi, err)
		}
		r.noPart(id)
		if st := r.str(`SELECT state||':'||bytes FROM uploads WHERE id = ?`, id); st != "received:"+itoa(int64(len(body))) {
			t.Errorf("row %s", st)
		}
		if n := r.count(`SELECT COUNT(*) FROM job_events WHERE event='upload' AND attempt_id = ? AND json_extract(detail_json,'$.artifact')='audio.flac'`, attemptID); n != 1 {
			t.Errorf("upload events %d", n)
		}
		if r.wakes.Load() != 0 {
			t.Error("Wake on a non-result artifact")
		}
		mid, mu := r.target("mm-result.json")
		if w := r.put(mu, []byte(`{"status":"ok"}`), nil); w.Code != 201 {
			t.Fatalf("mm-result: %d %s", w.Code, w.Body.String())
		}
		if r.wakes.Load() != 1 {
			t.Errorf("wakes %d, want 1", r.wakes.Load())
		}
		_ = mid
		got, err := r.svc.Received(t.Context(), r.st.R, attemptID)
		if err != nil || len(got) != 2 {
			t.Fatalf("received %v %v", got, err)
		}
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("the body must not be read") }

func (r *rig) noPart(id string) {
	r.t.Helper()
	if _, err := r.data.Stat("incoming/uploads/" + id + ".part"); !errors.Is(err, os.ErrNotExist) {
		r.t.Errorf("part file left behind: %v", err)
	}
}

func (r *rig) noFiles(id string) {
	r.t.Helper()
	r.noPart(id)
	if _, err := r.data.Stat("incoming/uploads/" + id); !errors.Is(err, os.ErrNotExist) {
		r.t.Errorf("final file exists: %v", err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestUploadHandler_NoSecretsInLogs(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, u := r.target("audio.flac")
	sig := u[strings.Index(u, "sig=")+4:]
	r.put(u, []byte("x"), nil)
	r.put(u+"junk", []byte("x"), nil)
	r.put(u, []byte("x"), nil)
	out := r.logs.String()
	if !strings.Contains(out, "worker request") {
		t.Fatalf("nothing logged: %q", out)
	}
	for _, bad := range []string{"sig=", "exp=", sig, "?"} {
		if strings.Contains(out, bad) {
			t.Errorf("log contains %q: %s", bad, out)
		}
	}
}

func TestUploadHandler_Race(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id, u := r.target("audio.flac")
	body := bytes.Repeat([]byte("abc"), 100)
	var wg sync.WaitGroup
	codes := make([]int, 16)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = r.put(u, body, nil).Code
		}()
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		switch c {
		case 201:
			created++
		case 200, 409:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if created != 1 {
		t.Errorf("%d PUTs created the upload, want exactly 1 (%v)", created, codes)
	}
	if st := r.str(`SELECT state FROM uploads WHERE id = ?`, id); st != "received" {
		t.Errorf("state %s", st)
	}
	if n := r.count(`SELECT COUNT(*) FROM job_events WHERE event='upload'`); n != 1 {
		t.Errorf("events %d", n)
	}
}
