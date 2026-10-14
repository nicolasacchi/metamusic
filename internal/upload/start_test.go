package upload

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func (r *rig) startURL() string {
	_, su, err := r.svc.Mint(JobRef{JobID: jobID, BatchID: batchID}, attemptID, r.exp)
	if err != nil {
		r.t.Fatal(err)
	}
	return su[len(publicURL):]
}

func (r *rig) post(target, body string, method string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.svc.StartHandler().ServeHTTP(w, req)
	return w
}

// state is the part of the attempt and job a start call must never change.
func (r *rig) state() string {
	return r.str(`SELECT j.state||'/'||a.state||'/'||COALESCE(a.ref,'-') FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE a.id = ?`, attemptID)
}

func TestUpload_StartHandshake(t *testing.T) {
	t.Parallel()
	ok := `{"runpod_job_id":"rp-job-1"}`

	t.Run("methods and paths", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		u := r.startURL()
		if w := r.post(u, ok, http.MethodGet); w.Code != 405 || w.Header().Get("Allow") != "POST" {
			t.Errorf("GET: %d", w.Code)
		}
		if w := r.post("/worker/v1/attempts/short/start?exp=1&sig=x", ok, http.MethodPost); w.Code != 404 {
			t.Errorf("bad path: %d", w.Code)
		}
	})
	t.Run("link", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		u := r.startURL()
		base := u[:strings.Index(u, "?")]
		sig := u[strings.Index(u, "sig=")+4:]
		for name, target := range map[string]string{
			"no query":   base,
			"wrong sig":  base + "?exp=" + itoa(r.exp) + "&sig=" + strings.Repeat("B", 43),
			"unknown id": "/worker/v1/attempts/01900000-0000-7000-8000-0000deadbeef/start?exp=" + itoa(r.exp) + "&sig=" + sig,
			"exp past":   base + "?exp=" + itoa(r.exp+1) + "&sig=" + r.sig.sig(attemptID, jobID, "start", 0, r.exp+1),
			"upload sig": base + "?exp=" + itoa(r.exp) + "&sig=" + r.sig.sig(attemptID, jobID, "audio.flac", 1000, r.exp),
		} {
			if w := r.post(target, ok, http.MethodPost); w.Code != 403 || code(t, w) != "ATTEMPT_LINK_INVALID" {
				t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
			}
		}
		r.clk.Advance(4 * time.Hour)
		if w := r.post(u, ok, http.MethodPost); w.Code != 403 || code(t, w) != "ATTEMPT_LINK_EXPIRED" {
			t.Errorf("expired: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("closed", func(t *testing.T) {
		t.Parallel()
		cases := map[string]func(r *rig){
			"cancel intent": func(r *rig) {
				r.exec(`UPDATE jobs SET cancel_requested_at=?, cancel_requested_by='human:h1' WHERE id=?`, store.FormatTime(t0), jobID)
			},
			"attempt rejected": func(r *rig) {
				r.exec(`UPDATE job_attempts SET state='done', terminal_basis='provider', terminal_at=? WHERE id=?`, store.FormatTime(t0), attemptID)
			},
			"job terminal": func(r *rig) {
				r.exec(`UPDATE jobs SET state='cancelling' WHERE id=?`, jobID)
				r.exec(`UPDATE jobs SET state='cancelled', error_code='CANCELLED_BY_USER', finished_at=?, lease_owner=NULL, lease_expires_at=NULL WHERE id=?`, store.FormatTime(t0), jobID)
			},
			"other delivered": func(r *rig) {
				r.exec(`INSERT INTO job_attempts(id,job_id,n,backend,state,submitted_at,window_ends_at,price_pph_micro_usd,created_at) VALUES('01900000-0000-7000-8000-00000000c002',?,2,'fake','pending',?,?,690000,?)`,
					jobID, store.FormatTime(t0), store.FormatTime(t0), store.FormatTime(t0))
				r.exec(`INSERT INTO job_events(job_id, attempt_id, at, event) VALUES(?, '01900000-0000-7000-8000-00000000c002', ?, 'result_received')`, jobID, store.FormatTime(t0))
			},
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				r := newRig(t)
				u := r.startURL()
				mutate(r)
				if w := r.post(u, ok, http.MethodPost); w.Code != 410 || code(t, w) != "ATTEMPT_CLOSED" {
					t.Errorf("%d %s", w.Code, w.Body.String())
				}
				if n := r.count(`SELECT COUNT(*) FROM job_attempts WHERE announced_at IS NOT NULL`); n != 0 {
					t.Error("a closed attempt was announced")
				}
			})
		}
	})
	t.Run("body", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		u := r.startURL()
		for name, body := range map[string]string{
			"empty": "", "not json": "nope", "missing": `{}`, "bad chars": `{"runpod_job_id":"a b"}`,
			"too long": `{"runpod_job_id":"` + strings.Repeat("a", 129) + `"}`, "huge": `{"runpod_job_id":"a","x":"` + strings.Repeat("a", 2000) + `"}`,
		} {
			if w := r.post(u, body, http.MethodPost); w.Code != 400 || code(t, w) != "BAD_REQUEST" {
				t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
			}
		}
	})
	t.Run("observation only", func(t *testing.T) {
		t.Parallel()
		r := newRigWith(t, "queued", "unknown")
		u := r.startURL()
		before := r.state()
		for range 3 {
			w := r.post(u, ok, http.MethodPost)
			if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"continue":true}` {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		}
		if after := r.state(); after != before {
			t.Errorf("start changed state %s -> %s", before, after)
		}
		if got := r.str(`SELECT announced_ref FROM job_attempts WHERE id = ?`, attemptID); got != "rp-job-1" {
			t.Errorf("announced_ref %q", got)
		}
		if n := r.count(`SELECT COUNT(*) FROM job_events WHERE event='start'`); n != 1 {
			t.Errorf("start events %d, repeated calls must be idempotent", n)
		}
		if r.wakes.Load() != 3 {
			t.Errorf("wakes %d", r.wakes.Load())
		}
	})
	t.Run("ref conflict", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		u := r.startURL()
		if w := r.post(u, ok, http.MethodPost); w.Code != 409 || code(t, w) != "ATTEMPT_REF_CONFLICT" {
			t.Errorf("differs from the attempt ref: %d %s", w.Code, w.Body.String())
		}
		r = newRigWith(t, "queued", "unknown")
		u = r.startURL()
		if w := r.post(u, ok, http.MethodPost); w.Code != 200 {
			t.Fatalf("first announce: %d", w.Code)
		}
		if w := r.post(u, `{"runpod_job_id":"rp-job-2"}`, http.MethodPost); w.Code != 409 || code(t, w) != "ATTEMPT_REF_CONFLICT" {
			t.Errorf("differs from the announcement: %d %s", w.Code, w.Body.String())
		}
		if got := r.str(`SELECT announced_ref FROM job_attempts WHERE id = ?`, attemptID); got != "rp-job-1" {
			t.Errorf("announced_ref %q", got)
		}
		if n := r.count(`SELECT COUNT(*) FROM job_events WHERE event='start_conflict'`); n != 1 {
			t.Errorf("conflict events %d", n)
		}
	})
	t.Run("same as attempt ref", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		u := r.startURL()
		if w := r.post(u, `{"runpod_job_id":"ref-1"}`, http.MethodPost); w.Code != 200 {
			t.Errorf("%d %s", w.Code, w.Body.String())
		}
		if got := r.str(`SELECT announced_ref FROM job_attempts WHERE id = ?`, attemptID); got != "ref-1" {
			t.Errorf("announced_ref %q", got)
		}
	})
}
