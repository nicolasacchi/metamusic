package fallback

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// dbDump renders every row of every table as text, to look for what must never be stored.
func (h *harness) dbDump() string {
	h.t.Helper()
	rows, err := h.st.R.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		h.t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var b strings.Builder
	for _, tb := range tables {
		r, err := h.st.R.Query(`SELECT * FROM "` + tb + `"`)
		if err != nil {
			h.t.Fatal(err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			for _, v := range vals {
				b.WriteString(strings.TrimSpace(strings.ReplaceAll(sprint(v), "\n", " ")))
				b.WriteByte('|')
			}
			b.WriteByte('\n')
		}
		r.Close()
	}
	return b.String()
}

func sprint(v any) string {
	var s sqlNull
	_ = s.Scan(v)
	return s.String
}

func TestNew_Validation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if _, err := New(Options{}); err == nil {
		t.Error("an empty option set was accepted")
	}
	if _, err := New(Options{Store: h.st, Uploads: h.svc, Objects: h.fb.o.Objects, AfterS: 59}); err == nil {
		t.Error("after_s 59 was accepted")
	}
	if _, err := New(Options{Store: h.st, Uploads: h.svc, Objects: h.fb.o.Objects, AfterS: 601}); err == nil {
		t.Error("after_s 601 was accepted")
	}
	if _, err := New(Options{Store: h.st, Uploads: h.svc, Objects: h.fb.o.Objects, AfterS: 90}); err != nil {
		t.Error(err)
	}
}

func TestMint_FallbackObjectRecorded(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	if h.count(`SELECT COUNT(*) FROM fallback_objects WHERE attempt_id = ? AND minted_at = ? AND fetched_at IS NULL AND deleted_at IS NULL`, attemptID, store.FormatTime(t0)) != 3 {
		t.Fatal("one row per upload expected")
	}
	seen := map[string]bool{}
	for art, u := range urls {
		if !strings.Contains(u, "X-Amz-Signature=") || !strings.Contains(u, "/"+prefix+h.uploadID(art)) {
			t.Errorf("%s: url is not a presigned PUT of prefix + upload id", art)
		}
		if seen[u] {
			t.Error("two artifacts share a URL")
		}
		seen[u] = true
	}
	h.mint() // minting again (a resubmission) neither fails nor duplicates
	if n := h.count(`SELECT COUNT(*) FROM fallback_objects`); n != 3 {
		t.Errorf("%d rows after a second mint", n)
	}
	for _, u := range urls {
		if strings.Contains(h.dbDump(), u) || strings.Contains(h.dbDump(), "X-Amz") {
			t.Fatal("a URL or signature reached the database")
		}
	}
}

func TestMint_RecordFailureMeansNoFallback(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.exec(`CREATE TRIGGER boom BEFORE INSERT ON fallback_objects WHEN NEW.upload_id = (SELECT id FROM uploads WHERE artifact = 'request.json')
		BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	urls, after, err := h.fb.MintFallback(context.Background(), attemptID, h.exp)
	if err == nil || urls != nil || after != 0 {
		t.Fatalf("got %v %d %v", urls, after, err)
	}
	if n := h.count(`SELECT COUNT(*) FROM fallback_objects`); n != 0 {
		t.Fatalf("%d rows survived a failed mint (the others must roll back with it)", n)
	}
}

func TestMint_Refusals(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if _, _, err := h.fb.MintFallback(context.Background(), attemptID, t0); err == nil {
		t.Error("a mint at the window end was accepted")
	}
	if _, _, err := h.fb.MintFallback(context.Background(), "no-such-attempt", h.exp); err == nil {
		t.Error("an attempt with no uploads was accepted")
	}
	if n := h.count(`SELECT COUNT(*) FROM fallback_objects`); n != 0 {
		t.Errorf("%d rows", n)
	}
	// a ttl beyond the signature limit is clamped to it, not refused
	if _, _, err := h.fb.MintFallback(context.Background(), attemptID, t0.Add(30*24*time.Hour)); err != nil {
		t.Errorf("a far window: %v", err)
	}
}

func TestRescue_CompletesFromFallback(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	flac, req := []byte("synthetic flac"), []byte(`{"id":"syn-a"}`)
	raw := h.finishWorker(urls, map[string][]byte{"audio.flac": flac, "request.json": req}, nil)
	found, err := h.fb.Rescue(context.Background(), attemptID)
	if err != nil || !found {
		t.Fatalf("rescue = %v %v", found, err)
	}
	if got := h.count(`SELECT COUNT(*) FROM uploads WHERE attempt_id = ? AND state = 'received' AND via = 'fallback'`, attemptID); got != 3 {
		t.Fatalf("%d uploads received by fallback", got)
	}
	for art, want := range map[string][]byte{"audio.flac": flac, "request.json": req, "mm-result.json": raw} {
		p := filepath.Join(h.dataDir(), "incoming/uploads", h.uploadID(art))
		got, err := os.ReadFile(p)
		if err != nil || string(got) != string(want) {
			t.Errorf("%s: %v", art, err)
		}
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o400 {
			t.Errorf("%s mode %v", art, st.Mode().Perm())
		}
	}
	if h.wakes.Load() != 1 {
		t.Errorf("wakes = %d", h.wakes.Load())
	}
	if n := h.count(`SELECT COUNT(*) FROM job_events WHERE event = 'upload' AND detail_json LIKE '%"via":"fallback"%'`); n != 3 {
		t.Errorf("%d fallback upload events", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM fallback_objects WHERE fetched_at IS NOT NULL`); n != 3 {
		t.Errorf("%d rows fetched", n)
	}
	// a second call has nothing left to do and does not fail
	if again, err := h.fb.Rescue(context.Background(), attemptID); again || err != nil {
		t.Errorf("second rescue = %v %v", again, err)
	}
}

func (h *harness) dataDir() string {
	h.t.Helper()
	return h.data.Name()
}

// TestRescue_AfterResultRetention: the attempt's provider status is long gone (unknown) and the app was unreachable throughout; the bucket
// still completes it.
func TestRescue_AfterResultRetention(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, nil)
	h.exec(`UPDATE job_attempts SET state = 'unknown' WHERE id = ?`, attemptID)
	h.clk.Advance(2 * time.Hour)
	if found, err := h.fb.Rescue(context.Background(), attemptID); err != nil || !found {
		t.Fatalf("rescue = %v %v", found, err)
	}
}

func TestRescue_Incomplete(t *testing.T) {
	t.Parallel()
	t.Run("no result object", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		urls := h.mint()
		h.workerPut(urls["audio.flac"], []byte("f"))
		if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
			t.Fatalf("%v %v", found, err)
		}
		if n := h.count(`SELECT COUNT(*) FROM uploads WHERE state = 'received'`); n != 0 {
			t.Errorf("%d received", n)
		}
	})
	t.Run("result lists an artifact that is nowhere", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		urls := h.mint()
		h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, nil)
		h.bkt.Store(prefix+h.uploadID("request.json"), nil) // gone
		delKey(t, h, "request.json")
		if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
			t.Fatalf("%v %v", found, err)
		}
		if n := h.count(`SELECT COUNT(*) FROM uploads WHERE via = 'fallback'`); n != 0 {
			t.Errorf("%d received although the set was incomplete", n)
		}
	})
	t.Run("an attempt with no bucket objects makes no request", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t) // never minted
		before := h.bkt.Requests()
		if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
			t.Fatalf("%v %v", found, err)
		}
		if h.bkt.Requests() != before {
			t.Error("a request left for an attempt without objects")
		}
	})
	t.Run("result already received through the route", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		urls := h.mint()
		h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, nil)
		h.exec(`UPDATE uploads SET state='received', sha256='aa', bytes=1, received_at=?, via='primary' WHERE artifact = 'mm-result.json'`, store.FormatTime(t0))
		if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
			t.Fatalf("%v %v", found, err)
		}
	})
	t.Run("job already finished", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		urls := h.mint()
		h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, nil)
		h.finishJob("failed")
		if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
			t.Fatalf("%v %v", found, err)
		}
		if n := h.count(`SELECT COUNT(*) FROM uploads WHERE state = 'received'`); n != 0 {
			t.Errorf("%d received for a finished job", n)
		}
	})
	t.Run("a storage fault is an error to retry", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.mint()
		h.bkt.FailNext(2, 503)
		if _, err := h.fb.Rescue(context.Background(), attemptID); err == nil {
			t.Fatal("a 503 was swallowed")
		} else if strings.Contains(err.Error(), "X-Amz") || strings.Contains(err.Error(), h.bkt.Config().SecretAccessKey) {
			t.Errorf("the error leaks: %v", err)
		}
	})
}

func delKey(t *testing.T, h *harness, artifact string) {
	t.Helper()
	if err := h.fb.o.Objects.Delete(context.Background(), prefix+h.uploadID(artifact)); err != nil {
		t.Fatal(err)
	}
}

// finishJob ends the job (and with it the attempt, which a provider status would have closed).
func (h *harness) finishJob(state string) {
	h.t.Helper()
	h.exec(`UPDATE jobs SET state=?, finished_at=?, lease_owner=NULL, lease_expires_at=NULL WHERE id=?`, state, store.FormatTime(h.clk.Now()), jobID)
}

func (h *harness) closeAttempt() {
	h.t.Helper()
	h.exec(`UPDATE job_attempts SET state='done', terminal_basis='provider', terminal_at=? WHERE id=?`, store.FormatTime(h.clk.Now()), attemptID)
}

func TestRescue_ShaMismatch(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, map[string]string{"audio.flac": strings.Repeat("0", 64)})
	for range 3 { // the cause is evented once, however often the sweep looks
		if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
			t.Fatalf("%v %v", found, err)
		}
	}
	if n := h.count(`SELECT COUNT(*) FROM uploads WHERE artifact = 'audio.flac' AND state = 'open'`); n != 1 {
		t.Error("the mismatching upload did not stay open")
	}
	if n := h.count(`SELECT COUNT(*) FROM uploads WHERE artifact = 'mm-result.json' AND state = 'open'`); n != 1 {
		t.Error("the result was received although an artifact failed verification")
	}
	if n := h.count(`SELECT COUNT(*) FROM job_events WHERE event = 'fallback_mismatch'`); n != 1 {
		t.Errorf("%d mismatch events", n)
	}
	if ev := h.str(`SELECT detail_json FROM job_events WHERE event = 'fallback_mismatch'`); !strings.Contains(ev, "audio.flac") {
		t.Errorf("event = %s", ev)
	}
	if n := h.count(`SELECT COUNT(*) FROM fallback_objects WHERE fetched_at IS NOT NULL`); n != 0 {
		t.Errorf("%d rows fetched", n)
	}
	if _, err := os.Stat(filepath.Join(h.dataDir(), "incoming/uploads", h.uploadID("audio.flac")+".part")); !os.IsNotExist(err) {
		t.Errorf("a part file was left: %v", err)
	}
}

func TestRescue_TooLarge(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	big := make([]byte, 1001) // audio.flac may carry 1000 in this rig
	h.finishWorker(urls, map[string][]byte{"audio.flac": big, "request.json": []byte("{}")}, nil)
	if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
		t.Fatalf("%v %v", found, err)
	}
	if n := h.count(`SELECT COUNT(*) FROM uploads WHERE state = 'received'`); n != 0 {
		t.Errorf("%d received", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM job_events WHERE event = 'fallback_mismatch'`); n != 1 {
		t.Errorf("%d mismatch events", n)
	}
}

func TestRescue_WrongAttemptRefused(t *testing.T) {
	t.Parallel()
	for name, mod := range map[string]func(string) string{
		"other attempt": func(s string) string {
			return strings.Replace(s, `"attempt_id":"`+attemptID+`"`, `"attempt_id":"01900000-0000-7000-8000-00000000c999"`, 1)
		},
		"other take": func(s string) string {
			return strings.Replace(s, `"take_id":"`+jobID+`"`, `"take_id":"01900000-0000-7000-8000-00000000a999"`, 1)
		},
		"v1 contract": func(s string) string { return strings.Replace(s, "mm.worker/2", "mm.worker/1", 1) },
		"not json":    func(string) string { return "{" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			urls := h.mint()
			raw := h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, nil)
			h.workerPut(urls["mm-result.json"], []byte(mod(string(raw))))
			if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
				t.Fatalf("%v %v", found, err)
			}
			if n := h.count(`SELECT COUNT(*) FROM uploads WHERE state = 'received'`); n != 0 {
				t.Errorf("%d received for a result that is not this attempt's", n)
			}
			if n := h.count(`SELECT COUNT(*) FROM job_events WHERE event = 'fallback_mismatch'`); n != 1 {
				t.Errorf("%d mismatch events", n)
			}
		})
	}
}

func TestRescue_UnlistedArtifactRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	raw := h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, nil)
	h.workerPut(urls["mm-result.json"], []byte(strings.Replace(string(raw), `"audio.flac"`, `"stem-vocals.flac"`, 1)))
	if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
		t.Fatalf("%v %v", found, err)
	}
	if n := h.count(`SELECT COUNT(*) FROM job_events WHERE event = 'fallback_mismatch'`); n != 1 {
		t.Errorf("%d mismatch events", n)
	}
}

func TestFallback_DeletesWhenTerminal(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, nil)
	if found, err := h.fb.Rescue(context.Background(), attemptID); err != nil || !found {
		t.Fatalf("%v %v", found, err)
	}
	if n, err := h.fb.DeleteDue(context.Background()); err != nil || n != 0 {
		t.Fatalf("a live attempt: %d %v", n, err)
	}
	h.closeAttempt()
	h.finishJob("failed")
	n, err := h.fb.DeleteDue(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("delete = %d %v", n, err)
	}
	if k := h.bkt.Keys(); len(k) != 0 {
		t.Errorf("objects left: %d", len(k))
	}
	if c := h.count(`SELECT COUNT(*) FROM fallback_objects WHERE deleted_at IS NOT NULL`); c != 3 {
		t.Errorf("%d rows marked", c)
	}
	if c := h.count(`SELECT COUNT(*) FROM audit_log WHERE op = ?`, OpDelete); c != 3 {
		t.Errorf("%d audit rows", c)
	}
	// idempotent
	if n, err := h.fb.DeleteDue(context.Background()); err != nil || n != 0 {
		t.Errorf("second pass = %d %v", n, err)
	}
	if c := h.count(`SELECT COUNT(*) FROM audit_log WHERE op = ?`, OpDelete); c != 3 {
		t.Errorf("%d audit rows after a second pass", c)
	}
}

func TestFallback_NeverDeletesLive(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"pending", "submitted", "unknown"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.mint()
			h.workerPut(h.mintURL("audio.flac"), []byte("f"))
			if state == "unknown" {
				h.exec(`UPDATE job_attempts SET state = 'unknown' WHERE id = ?`, attemptID)
			}
			if n, err := h.fb.DeleteDue(context.Background()); err != nil || n != 0 {
				t.Fatalf("deleted %d of a %s attempt: %v", n, state, err)
			}
			if len(h.bkt.Keys()) != 1 {
				t.Error("an object was removed")
			}
		})
	}
}

func (h *harness) mintURL(artifact string) string {
	h.t.Helper()
	u, err := h.fb.o.Objects.PresignPut(prefix+h.uploadID(artifact), time.Hour)
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

func TestFallback_DeleteRules(t *testing.T) {
	t.Parallel()
	t.Run("a finished attempt keeps objects its waiting job may still fetch", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		urls := h.mint()
		h.workerPut(urls["audio.flac"], []byte("f"))
		h.closeAttempt() // the provider said completed; the job still waits for its uploads
		if n, _ := h.fb.DeleteDue(context.Background()); n != 0 {
			t.Fatalf("deleted %d objects a transferring job still needs", n)
		}
		h.finishJob("failed")
		if n, _ := h.fb.DeleteDue(context.Background()); n != 3 {
			t.Fatalf("deleted %d after the job ended, want 3", n)
		}
	})
	t.Run("a received upload frees its object once the attempt is over", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.mint()
		h.exec(`UPDATE uploads SET state='received', sha256='aa', bytes=1, received_at=?, via='primary' WHERE artifact = 'audio.flac'`, store.FormatTime(t0))
		h.closeAttempt()
		if n, _ := h.fb.DeleteDue(context.Background()); n != 1 {
			t.Fatalf("deleted %d, want the received one", n)
		}
	})
	t.Run("seven days is the longest an object stays, whatever the attempt", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.mint()
		h.exec(`UPDATE job_attempts SET state = 'unknown' WHERE id = ?`, attemptID)
		h.clk.Advance(Retention - time.Second)
		if n, _ := h.fb.DeleteDue(context.Background()); n != 0 {
			t.Fatalf("deleted %d before the seventh day", n)
		}
		h.clk.Advance(time.Second)
		if n, _ := h.fb.DeleteDue(context.Background()); n != 3 {
			t.Fatalf("deleted %d on the seventh day", n)
		}
		if h.count(`SELECT COUNT(*) FROM audit_log WHERE op = ? AND principal_kind = 'system'`, OpDelete) != 3 {
			t.Error("the expiry deletions are not audited as system rows")
		}
	})
	t.Run("a failed delete is retried and an absent object counts as deleted", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		urls := h.mint()
		h.workerPut(urls["audio.flac"], []byte("f"))
		h.closeAttempt()
		h.finishJob("failed")
		h.bkt.FailNext(1, 503)
		n, err := h.fb.DeleteDue(context.Background())
		if err != nil || n != 2 {
			t.Fatalf("first pass = %d %v (one of three fails)", n, err)
		}
		if p := h.count(`SELECT COUNT(*) FROM fallback_objects WHERE deleted_at IS NULL`); p != 1 {
			t.Fatalf("%d pending after a failed delete", p)
		}
		if !strings.Contains(h.logs.String(), "retrying next pass") {
			t.Error("the failure was not logged")
		}
		if n, err := h.fb.DeleteDue(context.Background()); err != nil || n != 1 {
			t.Fatalf("second pass = %d %v", n, err)
		}
	})
	t.Run("pending is what status reports", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.mint()
		st, err := ReadStatus(context.Background(), h.st.R, true)
		if err != nil || !st.Configured || st.ObjectsPending != 3 || st.LastUsedAt != nil {
			t.Fatalf("%+v %v", st, err)
		}
		st, _ = ReadStatus(context.Background(), h.st.R, false)
		if st.Configured {
			t.Error("configured")
		}
	})
}

func TestFallback_NeverLogged(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, map[string]string{"request.json": strings.Repeat("1", 64)})
	h.bkt.FailNext(1, 500)
	_, _ = h.fb.Rescue(context.Background(), attemptID)
	_, _ = h.fb.Rescue(context.Background(), attemptID)
	h.closeAttempt()
	h.finishJob("failed")
	h.bkt.FailNext(1, 500)
	_, _ = h.fb.DeleteDue(context.Background())
	h.fb.RescueSweep(context.Background())
	cfg := h.bkt.Config()
	everything := h.logs.String() + h.dbDump()
	for _, banned := range []string{"X-Amz-", "Signature", cfg.SecretAccessKey, cfg.AccessKeyID, "https://", "http://127.0.0.1"} {
		if banned != "" && strings.Contains(everything, banned) {
			t.Errorf("%q reached a log, event, audit row or table", banned)
		}
	}
	for _, u := range urls {
		if strings.Contains(everything, u) {
			t.Error("a presigned URL was logged or stored")
		}
	}
}

func TestUploadDrill_503ThenFallback(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	until, err := h.fb.CreateDrill(context.Background(), attemptID, 60, "R2 drill", "cli:test")
	if err != nil || !until.Equal(t0.Add(time.Minute)) {
		t.Fatalf("%v %v", until, err)
	}
	// the worker's primary PUT meets 503
	ups, _, err := h.svc.Mint(upload.JobRef{JobID: jobID, BatchID: batchID}, attemptID, h.exp.Unix())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, ups["audio.flac"].URL[len("https://mm.example.com"):], strings.NewReader("f"))
	req.Header.Set("Content-Digest", "sha-256=:"+"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="+":")
	w := httptest.NewRecorder()
	h.svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "UPLOAD_DRILL") {
		t.Fatalf("the primary answered %d %s during the drill", w.Code, w.Body.String())
	}
	// ...so it uses the fallback, and the app completes the attempt from the bucket
	h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, nil)
	if found, err := h.fb.Rescue(context.Background(), attemptID); err != nil || !found {
		t.Fatalf("%v %v", found, err)
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE op = ?`, OpDrill); n != 1 {
		t.Errorf("%d drill audit rows", n)
	}
}

func TestUploadDrill_Bounded(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		secs   int
		reason string
		by     string
		att    string
	}{
		"too short":   {29, "r", "cli:t", attemptID},
		"too long":    {601, "r", "cli:t", attemptID},
		"no reason":   {60, "", "cli:t", attemptID},
		"long reason": {60, strings.Repeat("x", 501), "cli:t", attemptID},
		"no author":   {60, "r", "", attemptID},
	} {
		if _, err := h.fb.CreateDrill(ctx, c.att, c.secs, c.reason, c.by); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := h.fb.CreateDrill(ctx, attemptID, 60, "r", "cli:t"); !errors.Is(err, ErrNoFallbackObjects) {
		t.Errorf("an attempt with no objects: %v", err)
	}
	h.mint()
	if _, err := h.fb.CreateDrill(ctx, attemptID, 30, "r", "cli:t"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.fb.CreateDrill(ctx, attemptID, 600, "again", "cli:t"); err == nil {
		t.Error("a second drill for the attempt was accepted")
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE op = ?`, OpDrill); n != 1 {
		t.Errorf("%d audit rows (a refused drill writes none)", n)
	}
}

func TestSelfCheck(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	now := func() time.Time { return time.Now() }
	steps, err := SelfCheck(context.Background(), h.fb.o.Objects, h.bkt.HTTPClient(), prefix, now)
	if err != nil {
		t.Fatalf("%v (%+v)", err, steps)
	}
	var names []string
	for _, s := range steps {
		if !s.OK {
			t.Errorf("step %s failed: %s", s.Name, s.Note)
		}
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "put,head,get,delete,head-after-delete,clock" {
		t.Errorf("steps = %v", names)
	}
	if len(h.bkt.Keys()) != 0 {
		t.Error("the self-check left its object")
	}
	// a clock an hour off fails the last step
	steps, err = SelfCheck(context.Background(), h.fb.o.Objects, h.bkt.HTTPClient(), prefix, func() time.Time { return time.Now().Add(time.Hour) })
	if err == nil || steps[len(steps)-1].Name != "clock" || steps[len(steps)-1].OK {
		t.Errorf("skew: %v %+v", err, steps)
	}
	// a refusing bucket stops at the PUT and never prints a URL
	h.bkt.FailNext(1, 403)
	steps, err = SelfCheck(context.Background(), h.fb.o.Objects, h.bkt.HTTPClient(), prefix, now)
	if err == nil || steps[len(steps)-1].Name != "put" || strings.Contains(steps[len(steps)-1].Note, "http") {
		t.Errorf("refused: %v %+v", err, steps)
	}
	if len(h.bkt.Keys()) != 0 {
		t.Error("a refused self-check left an object")
	}
}

func TestRun_StartupAndTicks(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.fb.Run(ctx); close(done) }()
	waitFor(t, func() bool { return h.count(`SELECT COUNT(*) FROM uploads WHERE via = 'fallback'`) == 3 }) // startup rescue
	h.closeAttempt()
	h.finishJob("failed")
	// ten 30 s ticks make one 5 minute deletion pass; a tick advanced before the loop armed its timer is lost, so keep ticking
	for i := 0; i < 400 && h.count(`SELECT COUNT(*) FROM fallback_objects WHERE deleted_at IS NOT NULL`) != 3; i++ {
		time.Sleep(10 * time.Millisecond)
		h.clk.Advance(RescueEvery)
	}
	waitFor(t, func() bool { return h.count(`SELECT COUNT(*) FROM fallback_objects WHERE deleted_at IS NOT NULL`) == 3 })
	cancel()
	<-done
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 400 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

// TestRescue_ResultViaPrimaryArtifactsViaBucket: the result got through the app (it came back), but the artifacts were put in the
// bucket while it was down; the rescue still completes them, against the digests of the received result.
func TestRescue_ResultViaPrimaryArtifactsViaBucket(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	flac, req := []byte("synthetic flac"), []byte(`{"id":"syn-a"}`)
	raw := h.finishWorker(urls, map[string][]byte{"audio.flac": flac, "request.json": req}, nil)
	ctx := context.Background()
	part, err := h.svc.BeginFallback(ctx, h.uploadID("mm-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.ReceiveFallback(ctx, h.uploadID("mm-result.json"), part, shaHex(raw), int64(len(raw))); err != nil {
		t.Fatal(err)
	}
	found, err := h.fb.Rescue(ctx, attemptID)
	if err != nil || !found {
		t.Fatalf("rescue = %v %v", found, err)
	}
	if got := h.count(`SELECT COUNT(*) FROM uploads WHERE attempt_id = ? AND state = 'received'`, attemptID); got != 3 {
		t.Fatalf("%d uploads received", got)
	}
	if again, err := h.fb.Rescue(ctx, attemptID); again || err != nil {
		t.Errorf("second rescue = %v %v", again, err)
	}
}

// TestRescue_MismatchDedupe: the same cause on the same attempt is one event however often it is seen; the artifact name in the event
// is never taken from the worker.
func TestRescue_MismatchDedupe(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	urls := h.mint()
	raw := h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, nil)
	h.workerPut(urls["mm-result.json"], []byte(strings.Replace(string(raw), `"audio.flac"`, `"secret-looking-name"`, 1)))
	for range 3 {
		if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
			t.Fatalf("%v %v", found, err)
		}
	}
	if n := h.count(`SELECT COUNT(*) FROM job_events WHERE event = 'fallback_mismatch' AND detail_json LIKE '%"unlisted"%' AND detail_json NOT LIKE '%secret-looking%'`); n != 1 {
		t.Errorf("%d mismatch events", n)
	}
}

// TestMint_PresignFailureRetiresRows: nothing was handed out, so no row keeps the attempt under rescue.
func TestMint_PresignFailureRetiresRows(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fb.o.Objects = failingPresign{h.fb.o.Objects}
	if _, _, err := h.fb.MintFallback(context.Background(), attemptID, h.exp); err == nil {
		t.Fatal("want an error")
	}
	if n := h.count(`SELECT COUNT(*) FROM fallback_objects WHERE deleted_at IS NULL`); n != 0 {
		t.Errorf("%d live rows", n)
	}
}

type failingPresign struct{ Objects }

func (failingPresign) PresignPut(string, time.Duration) (string, error) {
	return "", errors.New("synthetic")
}
