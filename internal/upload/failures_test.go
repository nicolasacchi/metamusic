package upload

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// failing returns a rig whose store goes through failing connectors.
func newFailingRig(t *testing.T) (*rig, *failCtl) {
	t.Helper()
	r := newRig(t)
	ctl := &failCtl{}
	r.svc.o.Store = failingStore(t, r.dbPath, ctl)
	return r, ctl
}

func TestUploadHandler_EveryDatabaseCallMayFail(t *testing.T) {
	t.Parallel()
	body := []byte("synthetic flac")
	count := func(do func(r *rig)) int64 {
		r, ctl := newFailingRig(t)
		ctl.arm(0)
		do(r)
		return ctl.calls()
	}
	put := func(r *rig) {
		_, u := r.target("audio.flac")
		r.put(u, body, nil)
	}
	start := func(r *rig) { r.post(r.startURL(), `{"runpod_job_id":"rp-1"}`, http.MethodPost) }
	for name, do := range map[string]func(r *rig){"PUT": put, "start": start} {
		total := count(do)
		if total < 3 {
			t.Fatalf("%s: only %d calls", name, total)
		}
		for k := int64(1); k <= total; k++ {
			t.Run(fmt.Sprintf("%s call %d", name, k), func(t *testing.T) {
				t.Parallel()
				r, ctl := newFailingRig(t)
				// the targets are minted through the (still disarmed) store first
				_, u := r.target("audio.flac")
				su := r.startURL()
				ctl.arm(k)
				var w *httptest.ResponseRecorder
				if name == "PUT" {
					w = r.put(u, body, nil)
				} else {
					w = r.post(su, `{"runpod_job_id":"rp-1"}`, http.MethodPost)
				}
				ctl.disarm()
				if ctl.fired.Load() && w.Code != 500 && w.Code != 200 && w.Code != 201 {
					t.Errorf("a failed database call answered %d", w.Code)
				}
				// the same request works afterwards: no stuck lock, no half state
				if name == "PUT" {
					if w := r.put(u, body, nil); w.Code != 201 && w.Code != 200 {
						t.Errorf("retry: %d %s", w.Code, w.Body.String())
					}
					if st := r.str(`SELECT state FROM uploads WHERE artifact = 'audio.flac'`); st != "received" {
						t.Errorf("state %s", st)
					}
				} else if w := r.post(su, `{"runpod_job_id":"rp-1"}`, http.MethodPost); w.Code != 200 && w.Code != 409 {
					t.Errorf("retry: %d", w.Code)
				}
			})
		}
	}
}

func TestServiceMethodsReportDatabaseFailures(t *testing.T) {
	t.Parallel()
	r, ctl := newFailingRig(t)
	ref := JobRef{JobID: jobID, BatchID: batchID}
	id, u := r.target("audio.flac")
	r.put(u, []byte("x"), nil)
	calls := map[string]func() error{
		"Mint":       func() error { _, _, err := r.svc.Mint(ref, attemptID, r.exp); return err },
		"Received":   func() error { _, err := r.svc.Received(t.Context(), r.svc.o.Store.R, attemptID); return err },
		"SweepFiles": func() error { _, err := r.svc.SweepFiles(t.Context(), t0.Add(48*time.Hour)); return err },
		"Consume": func() error {
			tx, err := r.svc.o.Store.W.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback()
			return r.svc.Consume(t.Context(), tx, []string{id})
		},
		"CloseAttempt": func() error {
			tx, err := r.svc.o.Store.W.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback()
			return r.svc.CloseAttempt(t.Context(), tx, attemptID)
		},
		"OpenAttempt": func() error {
			tx, err := r.svc.o.Store.W.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback()
			return r.svc.OpenAttempt(t.Context(), tx, ref, "01900000-0000-7000-8000-0000000000aa", map[string]int64{"plan.json": 10}, r.exp, store.LeaseFence{Owner: "o1", Now: t0})
		},
	}
	for name, call := range calls {
		ctl.arm(0)
		_ = call()
		ctl.disarm()
		total := ctl.calls()
		for k := int64(1); k <= total; k++ {
			ctl.arm(k)
			err := call()
			ctl.disarm()
			if !ctl.fired.Load() {
				continue
			}
			if !errors.Is(err, errInjected) && !(name == "SweepFiles" || name == "Consume") {
				t.Errorf("%s call %d: %v", name, k, err)
			}
		}
		if err := call(); err != nil && name != "Consume" && name != "OpenAttempt" {
			t.Errorf("%s without the failure: %v", name, err)
		}
	}
}

func TestUploadFileSystemFailures(t *testing.T) {
	t.Parallel()
	body := []byte("synthetic flac")
	t.Run("uploads directory cannot be created", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, u := r.target("audio.flac")
		if err := r.data.Mkdir("incoming", 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := r.data.Create("incoming/uploads") // a file where the directory should be
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if w := r.put(u, body, nil); w.Code != 500 || code(t, w) != "INTERNAL" {
			t.Errorf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("part file cannot be created", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		if err := r.data.MkdirAll("incoming/uploads/"+id+".part/inner", 0o700); err != nil {
			t.Fatal(err)
		}
		if w := r.put(u, body, nil); w.Code != 500 {
			t.Errorf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("final file cannot be put in place", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		if err := r.data.MkdirAll("incoming/uploads/"+id+"/inner", 0o700); err != nil {
			t.Fatal(err)
		}
		if w := r.put(u, body, nil); w.Code != 500 {
			t.Errorf("%d %s", w.Code, w.Body.String())
		}
		r.noPart(id)
		if st := r.str(`SELECT state FROM uploads WHERE id = ?`, id); st != "open" {
			t.Errorf("state %s", st)
		}
	})
	t.Run("a body larger than max_bytes", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("request.json") // max_bytes 100
		w := r.put(u, bytes.Repeat([]byte("x"), 400), func(q *http.Request) { q.ContentLength = 50; q.Header.Set("Content-Digest", digestOf([]byte("x"))) })
		if w.Code != 413 || code(t, w) != "PAYLOAD_TOO_LARGE" {
			t.Errorf("%d %s", w.Code, w.Body.String())
		}
		r.noFiles(id)
	})
	t.Run("a stale part file is replaced", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		if err := r.data.MkdirAll("incoming/uploads", 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := r.data.OpenFile("incoming/uploads/"+id+".part", os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("left by a crashed process")
		f.Close()
		if w := r.put(u, body, nil); w.Code != 201 {
			t.Errorf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("commit finds the upload closed after the file landed", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		r.exec(`UPDATE uploads SET state = 'closed' WHERE id = ?`, id)
		req := httptest.NewRequest(http.MethodPut, "/x", nil)
		err := r.svc.commit(req, uploadRow{ID: id, JobID: jobID, AttemptID: attemptID, Artifact: "audio.flac"}, "00", 1)
		if !errors.Is(err, errClosed) {
			t.Errorf("%v", err)
		}
	})
}

func TestReadFile(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id, u := r.target("request.json")
	r.put(u, []byte(`{"a":1}`), nil)
	got, err := r.svc.ReadFile(id, 100)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := r.svc.ReadFile(id, 3); err == nil {
		t.Error("a file above the limit")
	}
	if _, err := r.svc.ReadFile("01900000-0000-7000-8000-0000000000ff", 100); err == nil {
		t.Error("a missing file")
	}
}

func TestNewServiceDefaults(t *testing.T) {
	t.Parallel()
	s := NewService(ServiceOptions{})
	if s.o.Clock == nil || s.o.Log == nil || len(s.newID()) != 36 {
		t.Errorf("defaults %+v", s.o)
	}
	s.wake() // nil-safe
	if s.inFlight("x") || !s.lock("x") || s.lock("x") {
		t.Error("lock")
	}
	s.unlock("x")
}

func TestParseDigest(t *testing.T) {
	t.Parallel()
	good := digestOf([]byte("abc"))
	if h, ok := parseDigest(" " + good + " "); !ok || len(h) != 64 {
		t.Errorf("%q %v", h, ok)
	}
	for _, bad := range []string{"", "sha-256=:AAAA:", "sha-512=:" + good[10:], "sha-256=:!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!=:"} {
		if _, ok := parseDigest(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestUploadHandler_LateWinnerAndClose(t *testing.T) {
	t.Parallel()
	body := []byte("synthetic flac")
	for name, c := range map[string]struct {
		hook func(r *rig, u string)
		want int
		code string
	}{
		"the same body finished first":    {func(r *rig, u string) { r.put(u, body, nil) }, 200, ""},
		"a different body finished first": {func(r *rig, u string) { r.put(u, []byte("other bytes"), nil) }, 409, "UPLOAD_CONFLICT"},
		"the attempt was closed": {func(r *rig, _ string) {
			tx, _ := r.st.W.Begin()
			_ = r.svc.CloseAttempt(t.Context(), tx, attemptID)
			_ = tx.Commit()
		}, 410, "UPLOAD_CLOSED"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			id, u := r.target("audio.flac")
			once := false
			r.svc.beforeLock = func() {
				if once {
					return
				}
				once = true
				c.hook(r, u)
			}
			w := r.put(u, body, nil)
			if w.Code != c.want || (c.code != "" && code(t, w) != c.code) {
				t.Errorf("%d %s", w.Code, w.Body.String())
			}
			// the file of the first writer was not replaced by the late one
			if name == "a different body finished first" {
				b, err := r.data.ReadFile("incoming/uploads/" + id)
				if err != nil || string(b) != "other bytes" {
					t.Errorf("%q %v", b, err)
				}
			}
		})
	}
}

func TestFileRemovalFailures(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id, u := r.target("audio.flac")
	r.put(u, []byte("x"), nil)
	// a received file that cannot be removed: replace it with a non-empty directory
	if err := r.data.Remove("incoming/uploads/" + id); err != nil {
		t.Fatal(err)
	}
	if err := r.data.MkdirAll("incoming/uploads/"+id+"/inner", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.RemoveConsumedFiles(t.Context(), []string{id}); err == nil {
		t.Error("RemoveConsumedFiles must report a file it cannot remove")
	}
	r.exec(`UPDATE jobs SET state = 'failing' WHERE id = ?`, jobID)
	r.exec(`UPDATE jobs SET state = 'failed', error_code = 'X', finished_at = ?, lease_owner = NULL, lease_expires_at = NULL WHERE id = ?`, store.FormatTime(t0), jobID)
	if _, err := r.svc.SweepFiles(t.Context(), t0.Add(48*time.Hour)); err == nil {
		t.Error("SweepFiles must report a file it cannot remove")
	}
	// a directory is not a readable upload
	if _, err := r.svc.ReadFile(id, 100); err == nil {
		t.Error("ReadFile of a directory")
	}
}
