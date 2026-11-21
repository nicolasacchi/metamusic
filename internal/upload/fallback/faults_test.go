package fallback

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/objstore"
)

var errScripted = errors.New("scripted fault")

// flaky wraps the real client and fails the named operations ("presign", "head", "get", "delete", "put"); "head2" fails the second head.
type flaky struct {
	Objects
	fail  map[string]bool
	heads int
}

func (f *flaky) PresignPut(key string, ttl time.Duration) (string, error) {
	if f.fail["presign"] {
		return "", errScripted
	}
	return f.Objects.PresignPut(key, ttl)
}

func (f *flaky) Head(ctx context.Context, key string) (int64, bool, error) {
	f.heads++
	if f.fail["head"] || (f.fail["head2"] && f.heads == 2) {
		return 0, false, errScripted
	}
	return f.Objects.Head(ctx, key)
}

func (f *flaky) Get(ctx context.Context, key string, maxBytes int64) (io.ReadCloser, int64, error) {
	if f.fail["get"] {
		return nil, 0, errScripted
	}
	if f.fail["get-notfound"] {
		return nil, 0, objstore.ErrNotFound
	}
	return f.Objects.Get(ctx, key, maxBytes)
}

func (f *flaky) Delete(ctx context.Context, key string) error {
	if f.fail["delete"] {
		return errScripted
	}
	return f.Objects.Delete(ctx, key)
}

func (f *flaky) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	return f.Objects.Put(ctx, key, body, size)
}

// dateRT rewrites the Date header of every answer (empty removes it).
type dateRT struct {
	base http.RoundTripper
	date string
	err  bool
}

func (d dateRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if d.err {
		return nil, errScripted
	}
	resp, err := d.base.RoundTrip(r)
	if err == nil {
		if d.date == "" {
			resp.Header.Del("Date")
		} else {
			resp.Header.Set("Date", d.date)
		}
	}
	return resp, err
}

func TestSelfCheck_EveryStepMayFail(t *testing.T) {
	t.Parallel()
	for step, fail := range map[string]string{"presign": "presign", "head": "head", "get": "get", "delete": "delete", "head-after-delete": "head2"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			objs := &flaky{Objects: h.fb.o.Objects, fail: map[string]bool{fail: true}}
			steps, err := SelfCheck(context.Background(), objs, h.bkt.HTTPClient(), prefix, time.Now)
			if err == nil || steps[len(steps)-1].Name != step || steps[len(steps)-1].OK {
				t.Fatalf("%v %+v", err, steps)
			}
			if strings.Contains(steps[len(steps)-1].Note, "http") {
				t.Errorf("the note carries a URL: %q", steps[len(steps)-1].Note)
			}
			if step != "delete" && len(h.bkt.Keys()) != 0 { // a failing delete cannot clean up after itself either
				t.Errorf("%d objects left after a failed step", len(h.bkt.Keys()))
			}
		})
	}
	t.Run("the PUT cannot be sent", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		hc := &http.Client{Transport: dateRT{base: h.bkt.HTTPClient().Transport, err: true}}
		steps, err := SelfCheck(context.Background(), h.fb.o.Objects, hc, prefix, time.Now)
		if err == nil || steps[len(steps)-1].Name != "put" || strings.Contains(steps[len(steps)-1].Note, "127.0.0.1") {
			t.Fatalf("%v %+v", err, steps)
		}
	})
	t.Run("the bucket's Date header", func(t *testing.T) {
		t.Parallel()
		for name, date := range map[string]string{"missing": "", "garbled": "yesterday-ish"} {
			h := newHarness(t)
			hc := &http.Client{Transport: dateRT{base: h.bkt.HTTPClient().Transport, date: date}}
			steps, err := SelfCheck(context.Background(), h.fb.o.Objects, hc, prefix, time.Now)
			if err == nil || steps[len(steps)-1].Name != "clock" {
				t.Errorf("%s: %v %+v", name, err, steps)
			}
		}
	})
	t.Run("a cancelled context", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := SelfCheck(ctx, h.fb.o.Objects, h.bkt.HTTPClient(), prefix, time.Now); err == nil {
			t.Fatal("a cancelled check passed")
		}
	})
}

func TestMint_Faults(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	h := newHarness(t)
	if _, _, err := h.fb.MintFallback(cancelled, attemptID, h.exp); err == nil {
		t.Error("a cancelled context minted")
	}
	h = newHarness(t)
	h.fb.o.Objects = &flaky{Objects: h.fb.o.Objects, fail: map[string]bool{"presign": true}}
	if urls, _, err := h.fb.MintFallback(context.Background(), attemptID, h.exp); err == nil || urls != nil {
		t.Errorf("a presign fault: %v %v", urls, err)
	}
	if n := h.count(`SELECT COUNT(*) FROM fallback_objects`); n != 3 {
		t.Errorf("the recorded rows are kept (%d): the deletion loop treats them like any other", n)
	}
	// the transaction cannot start
	h = newHarness(t)
	h.exec(`CREATE TRIGGER boom BEFORE INSERT ON fallback_objects BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	if _, _, err := h.fb.MintFallback(context.Background(), attemptID, h.exp); err == nil {
		t.Error("a database fault was swallowed")
	}
}

func TestStatus_Faults(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadStatus(cancelled, h.st.R, true); err == nil {
		t.Error("a cancelled context read a status")
	}
	h.mint()
	h.exec(`UPDATE uploads SET state='received', sha256='aa', bytes=1, received_at=?, via='fallback' WHERE artifact = 'audio.flac'`, "2026-10-14T12:05:00.000Z")
	st, err := ReadStatus(context.Background(), h.st.R, true)
	if err != nil || st.LastUsedAt == nil || !st.LastUsedAt.Equal(time.Date(2026, 10, 14, 12, 5, 0, 0, time.UTC)) {
		t.Fatalf("%+v %v", st, err)
	}
	h.exec(`DROP TRIGGER uploads_received_once`)
	h.exec(`UPDATE uploads SET received_at = 'garbage' WHERE artifact = 'audio.flac'`)
	if _, err := ReadStatus(context.Background(), h.st.R, true); err == nil {
		t.Error("an unparsable time was accepted")
	}
}

func TestDelete_Faults(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		if _, err := h.fb.DeleteDue(cancelled); err == nil {
			t.Error("cancelled pass succeeded")
		}
		if n := h.fb.RescueSweep(cancelled); n != 0 {
			t.Errorf("found %d", n)
		}
		h.fb.deletePass(cancelled) // logs nothing, returns
	})
	t.Run("cancelled between objects", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.mint()
		h.closeAttempt()
		h.finishJob("failed")
		ctx, cancel := context.WithCancel(context.Background())
		h.fb.o.Objects = &cancelAfterDelete{Objects: h.fb.o.Objects, cancel: cancel, once: new(bool)}
		n, err := h.fb.DeleteDue(ctx) // the context ends right after the first object is gone: its row is not marked, the next pass redoes it
		if err == nil || n != 0 {
			t.Fatalf("deleted %d, err %v", n, err)
		}
		if n, err := h.fb.DeleteDue(context.Background()); err != nil || n != 3 {
			t.Fatalf("the next pass deleted %d: %v", n, err)
		}
	})
	t.Run("the row cannot be marked", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.mint()
		h.closeAttempt()
		h.finishJob("failed")
		h.exec(`CREATE TRIGGER boom BEFORE UPDATE OF deleted_at ON fallback_objects BEGIN SELECT RAISE(ABORT, 'boom'); END`)
		if _, err := h.fb.DeleteDue(context.Background()); err == nil {
			t.Fatal("a database fault was swallowed")
		}
		h.fb.deletePass(context.Background()) // logged, not fatal
		if !strings.Contains(h.logs.String(), "deletion pass failed") {
			t.Error("the pass failure was not logged")
		}
	})
	t.Run("the audit row cannot be written", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.mint()
		h.closeAttempt()
		h.finishJob("failed")
		h.exec(`CREATE TRIGGER boom BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'boom'); END`)
		if _, err := h.fb.DeleteDue(context.Background()); err == nil {
			t.Fatal("a database fault was swallowed")
		}
		if n := h.count(`SELECT COUNT(*) FROM fallback_objects WHERE deleted_at IS NOT NULL`); n != 0 {
			t.Errorf("%d rows marked without their audit row", n)
		}
	})
	t.Run("an already marked row is not audited twice", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		h.mint()
		h.closeAttempt()
		h.finishJob("failed")
		p := pending{uploadID: h.uploadID("audio.flac"), attemptID: attemptID}
		if err := h.fb.markDeleted(context.Background(), p, "terminal"); err != nil {
			t.Fatal(err)
		}
		if err := h.fb.markDeleted(context.Background(), p, "terminal"); err != nil {
			t.Fatal(err)
		}
		if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE op = ?`, OpDelete); n != 1 {
			t.Errorf("%d audit rows", n)
		}
	})
	t.Run("an unparsable stamp counts as old", func(t *testing.T) {
		t.Parallel()
		if r := (pending{mintedAt: "garbage", attState: "pending"}).reason(time.Now()); r != "expired" {
			t.Errorf("reason = %q", r)
		}
	})
}

type cancelAfterDelete struct {
	Objects
	cancel func()
	once   *bool
}

func (c *cancelAfterDelete) Delete(ctx context.Context, key string) error {
	err := c.Objects.Delete(ctx, key)
	if !*c.once {
		*c.once = true
		c.cancel()
	}
	return err
}

func TestRescue_Faults(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	setup := func(t *testing.T) (*harness, map[string]string) {
		h := newHarness(t)
		urls := h.mint()
		h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, nil)
		return h, urls
	}
	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		h, _ := setup(t)
		if _, err := h.fb.Rescue(cancelled, attemptID); err == nil {
			t.Error("accepted")
		}
		if _, err := h.fb.LiveAttempts(cancelled); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("the result cannot be read", func(t *testing.T) {
		t.Parallel()
		h, _ := setup(t)
		h.fb.o.Objects = &flaky{Objects: h.fb.o.Objects, fail: map[string]bool{"get": true}}
		if _, err := h.fb.Rescue(context.Background(), attemptID); err == nil {
			t.Error("a read fault was swallowed")
		}
	})
	t.Run("the result vanishes between head and get", func(t *testing.T) {
		t.Parallel()
		h, _ := setup(t)
		h.fb.o.Objects = &flaky{Objects: h.fb.o.Objects, fail: map[string]bool{"get-notfound": true}}
		if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
			t.Errorf("%v %v", found, err)
		}
	})
	t.Run("an oversized result is never read", func(t *testing.T) {
		t.Parallel()
		h, urls := setup(t)
		h.workerPut(urls["mm-result.json"], make([]byte, maxResultBytes+1))
		if found, err := h.fb.Rescue(context.Background(), attemptID); found || err != nil {
			t.Errorf("%v %v", found, err)
		}
		if n := h.count(`SELECT COUNT(*) FROM uploads WHERE state = 'received'`); n != 0 {
			t.Errorf("%d received", n)
		}
	})
	t.Run("an artifact cannot be fetched", func(t *testing.T) {
		t.Parallel()
		h, _ := setup(t)
		h.fb.o.Objects = &getFailsOn{Objects: h.fb.o.Objects, key: prefix + h.uploadID("audio.flac")}
		if _, err := h.fb.Rescue(context.Background(), attemptID); err == nil {
			t.Error("a fetch fault was swallowed")
		}
		if h.svc != nil && h.count(`SELECT COUNT(*) FROM uploads WHERE state = 'received'`) != 0 {
			t.Error("something was received")
		}
	})
	t.Run("an artifact head fails", func(t *testing.T) {
		t.Parallel()
		h, _ := setup(t)
		f := &flaky{Objects: h.fb.o.Objects, fail: map[string]bool{"head2": true}}
		h.fb.o.Objects = f
		if _, err := h.fb.Rescue(context.Background(), attemptID); err == nil {
			t.Error("a head fault was swallowed")
		}
	})
	t.Run("the mismatch event cannot be written", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t)
		urls := h.mint()
		h.finishWorker(urls, map[string][]byte{"audio.flac": []byte("f"), "request.json": []byte("{}")}, map[string]string{"audio.flac": strings.Repeat("0", 64)})
		h.exec(`CREATE TRIGGER boom BEFORE INSERT ON job_events WHEN NEW.event = 'fallback_mismatch' BEGIN SELECT RAISE(ABORT, 'boom'); END`)
		if _, err := h.fb.Rescue(context.Background(), attemptID); err == nil {
			t.Error("a database fault was swallowed")
		}
	})
	t.Run("the fetched stamp cannot be written", func(t *testing.T) {
		t.Parallel()
		h, _ := setup(t)
		h.exec(`CREATE TRIGGER boom BEFORE UPDATE OF fetched_at ON fallback_objects BEGIN SELECT RAISE(ABORT, 'boom'); END`)
		found, err := h.fb.Rescue(context.Background(), attemptID)
		if err == nil || !found {
			t.Errorf("found=%v err=%v: the result was received, the stamp is a fault to retry", found, err)
		}
	})
	t.Run("the sweep logs a fault and goes on", func(t *testing.T) {
		t.Parallel()
		h, _ := setup(t)
		h.fb.o.Objects = &flaky{Objects: h.fb.o.Objects, fail: map[string]bool{"head": true}}
		if n := h.fb.RescueSweep(context.Background()); n != 0 {
			t.Errorf("found %d", n)
		}
		if !strings.Contains(h.logs.String(), "rescue failed") {
			t.Error("the fault was not logged")
		}
	})
	t.Run("a result received by the sweep is counted", func(t *testing.T) {
		t.Parallel()
		h, _ := setup(t)
		if n := h.fb.RescueSweep(context.Background()); n != 1 {
			t.Errorf("found %d", n)
		}
	})
}

type getFailsOn struct {
	Objects
	key string
}

func (g *getFailsOn) Get(ctx context.Context, key string, maxBytes int64) (io.ReadCloser, int64, error) {
	if key == g.key {
		return nil, 0, errScripted
	}
	return g.Objects.Get(ctx, key, maxBytes)
}

func TestRecordDrill_Faults(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.mint()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.fb.CreateDrill(cancelled, attemptID, 60, "r", "cli:t"); err == nil {
		t.Error("a cancelled context drilled")
	}
	h.exec(`CREATE TRIGGER boom BEFORE INSERT ON upload_drills BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	if _, err := h.fb.CreateDrill(context.Background(), attemptID, 60, "r", "cli:t"); err == nil {
		t.Error("a database fault was swallowed")
	}
	h = newHarness(t)
	h.mint()
	h.exec(`CREATE TRIGGER boom BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	if _, err := h.fb.CreateDrill(context.Background(), attemptID, 60, "r", "cli:t"); err == nil {
		t.Error("an audit fault was swallowed")
	}
	if n := h.count(`SELECT COUNT(*) FROM upload_drills`); n != 0 {
		t.Errorf("%d drill rows without their audit row", n)
	}
}
