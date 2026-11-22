package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func sumHex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// TestUpload_ViaPrimaryOnReceipt: the route sets via = 'primary' with the receipt, and the event of the primary route is exactly what it was.
func TestUpload_ViaPrimaryOnReceipt(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id, u := r.target("audio.flac")
	if w := r.put(u, []byte("flac"), nil); w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if got := r.str(`SELECT via FROM uploads WHERE id = ?`, id); got != "primary" {
		t.Errorf("via = %q", got)
	}
	if got := r.str(`SELECT detail_json FROM job_events WHERE event = 'upload'`); strings.Contains(got, "via") {
		t.Errorf("the primary event changed: %s", got)
	}
	if n := r.count(`SELECT COUNT(*) FROM uploads WHERE via IS NOT NULL`); n != 1 {
		t.Errorf("%d uploads have a via", n)
	}
}

// TestUploadPaths_FromSchema22: a database migrated from 0022 (no fallback tables) serves every upload path as before: open, receive,
// replay, conflict, closed and the mm-result wake. No fallback is configured, nothing reads the new tables.
func TestUploadPaths_FromSchema22(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "s.db")
	// a database exactly as 0022 left it: migrated only up to the migration before 0023 (never latest-then-DROP, which breaks on every later migration)
	if err := store.CreateBefore(t.Context(), p, "0023_upload_fallback.sql"); err != nil {
		t.Fatal(err)
	}
	r := newRigAtPath(t, p) // store.Open migrates 0022 to the latest
	id, u := r.target("audio.flac")
	body := []byte("flac bytes")
	if w := r.put(u, body, nil); w.Code != 201 {
		t.Fatalf("first PUT %d %s", w.Code, w.Body.String())
	}
	if w := r.put(u, body, nil); w.Code != 200 {
		t.Errorf("replay %d", w.Code)
	}
	if w := r.put(u, []byte("other"), nil); w.Code != 409 || code(t, w) != "UPLOAD_CONFLICT" {
		t.Errorf("conflict %d", w.Code)
	}
	if got := r.str(`SELECT state || ':' || via FROM uploads WHERE id = ?`, id); got != "received:primary" {
		t.Errorf("row = %s", got)
	}
	rid, ru := r.target("mm-result.json")
	if w := r.put(ru, []byte("{}"), nil); w.Code != 201 || r.wakes.Load() != 1 {
		t.Errorf("result %d wakes %d", w.Code, r.wakes.Load())
	}
	_ = rid
	if n := r.count(`SELECT COUNT(*) FROM fallback_objects`) + r.count(`SELECT COUNT(*) FROM upload_drills`); n != 0 {
		t.Errorf("the new tables have %d rows", n)
	}
}

func TestUploadDrill_503ThenFallback(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id, u := r.target("audio.flac")
	body := []byte("synthetic flac bytes")
	until := store.FormatTime(t0.Add(30 * time.Second))
	r.exec(`INSERT INTO upload_drills(attempt_id,until,reason,created_by,created_at) VALUES(?,?,'drill','cli:test',?)`, attemptID, until, store.FormatTime(t0))
	w := r.put(u, body, nil)
	if w.Code != http.StatusServiceUnavailable || code(t, w) != "UPLOAD_DRILL" {
		t.Fatalf("during the drill: %d %s", w.Code, w.Body.String())
	}
	if n := r.count(`SELECT COUNT(*) FROM uploads WHERE state = 'received'`); n != 0 {
		t.Fatalf("a drilled PUT stored %d rows", n)
	}
	// the drill is bound to its attempt: an unrelated invalid link is still a 403, not a 503
	if w := r.put(u[:len(u)-3]+"abc", body, nil); w.Code != http.StatusForbidden {
		t.Errorf("tampered link during a drill: %d", w.Code)
	}
	// the fallback receipt is not blocked by the drill: it is the other route
	f, err := r.svc.BeginFallback(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.ReceiveFallback(t.Context(), id, f, sumHex(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	if got := r.str(`SELECT state || ':' || via FROM uploads WHERE id = ?`, id); got != "received:fallback" {
		t.Errorf("row = %s", got)
	}
	if got := r.str(`SELECT detail_json FROM job_events WHERE event = 'upload'`); !strings.Contains(got, `"via":"fallback"`) {
		t.Errorf("event = %s", got)
	}
	st, err := os.Stat(filepath.Join(r.dataDir(), uploadPath(id)))
	if err != nil || st.Mode().Perm() != 0o400 {
		t.Errorf("final file: %v %v", st, err)
	}
}

func TestUploadDrill_Bounded(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, u := r.target("audio.flac")
	r.exec(`INSERT INTO upload_drills(attempt_id,until,reason,created_by,created_at) VALUES(?,?,'drill','cli:test',?)`, attemptID, store.FormatTime(t0.Add(30*time.Second)), store.FormatTime(t0))
	if w := r.put(u, []byte("x"), nil); w.Code != 503 {
		t.Fatalf("inside the window %d", w.Code)
	}
	r.clk.Advance(30 * time.Second) // until is exclusive
	if w := r.put(u, []byte("x"), nil); w.Code != 201 {
		t.Fatalf("after the window %d %s", w.Code, w.Body.String())
	}
}

func TestReceiveFallback_Refusals(t *testing.T) {
	t.Parallel()
	body := []byte("0123456789")
	t.Run("already received through the route", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, u := r.target("audio.flac")
		r.put(u, body, nil)
		if _, err := r.svc.BeginFallback(t.Context(), id); !errors.Is(err, ErrFallbackReceived) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("closed upload and terminal job", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		r.exec(`UPDATE uploads SET state = 'closed' WHERE id = ?`, id)
		if _, err := r.svc.BeginFallback(t.Context(), id); !errors.Is(err, ErrFallbackClosed) {
			t.Fatalf("closed: %v", err)
		}
		r2 := newRig(t)
		id2, _ := r2.target("audio.flac")
		r2.exec(`UPDATE jobs SET state='failed', finished_at=?, lease_owner=NULL, lease_expires_at=NULL WHERE id=?`, store.FormatTime(t0), jobID)
		if _, err := r2.svc.BeginFallback(t.Context(), id2); !errors.Is(err, ErrFallbackClosed) {
			t.Fatalf("terminal: %v", err)
		}
	})
	t.Run("another attempt already delivered", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		r.exec(`INSERT INTO job_attempts(id,job_id,n,backend,state,ref,submitted_at,window_ends_at,price_pph_micro_usd,created_at) VALUES('att2',?,2,'fake','submitted','ref-2',?,?,1,?)`,
			jobID, store.FormatTime(t0), store.FormatTime(t0), store.FormatTime(t0))
		r.exec(`INSERT INTO job_events(job_id,attempt_id,at,event) VALUES(?,'att2',?,'result_received')`, jobID, store.FormatTime(t0))
		if _, err := r.svc.BeginFallback(t.Context(), id); !errors.Is(err, ErrFallbackClosed) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("unknown upload", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if _, err := r.svc.BeginFallback(t.Context(), "nope"); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("size or limit mismatch removes the part", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		f, err := r.svc.BeginFallback(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(body)
		if err := r.svc.ReceiveFallback(t.Context(), id, f, sumHex(body), 3); err == nil {
			t.Fatal("a short claim was accepted")
		}
		if _, err := os.Stat(filepath.Join(r.dataDir(), uploadPath(id)+".part")); !os.IsNotExist(err) {
			t.Errorf("part file left: %v", err)
		}
		if r.svc.inFlight(id) {
			t.Error("slot not released")
		}
	})
	t.Run("abort releases the slot", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		f, err := r.svc.BeginFallback(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		r.svc.AbortFallback(id, f)
		if r.svc.inFlight(id) {
			t.Error("slot held")
		}
		if _, err := os.Stat(filepath.Join(r.dataDir(), uploadPath(id)+".part")); !os.IsNotExist(err) {
			t.Errorf("part left: %v", err)
		}
	})
	t.Run("a second receipt waits then reports busy", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		f, err := r.svc.BeginFallback(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		defer r.svc.AbortFallback(id, f)
		r.svc.lockWait = 20 * time.Millisecond
		if _, err := r.svc.BeginFallback(t.Context(), id); !errors.Is(err, ErrFallbackBusy) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("closed between begin and commit", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		f, err := r.svc.BeginFallback(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(body)
		r.exec(`UPDATE uploads SET state = 'closed' WHERE id = ?`, id)
		if err := r.svc.ReceiveFallback(context.Background(), id, f, sumHex(body), int64(len(body))); !errors.Is(err, ErrFallbackClosed) {
			t.Fatalf("%v", err)
		}
		if _, err := os.Stat(filepath.Join(r.dataDir(), uploadPath(id))); !os.IsNotExist(err) {
			t.Errorf("file left: %v", err)
		}
	})
}

func TestAttemptUploads_Lists(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got, err := r.svc.AttemptUploads(t.Context(), attemptID)
	if err != nil || len(got) != 3 || got[0].Artifact != "audio.flac" || got[0].State != "open" || got[0].MaxBytes != 1000 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestReceiveFallback_MmResultWakes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id, _ := r.target("mm-result.json")
	f, err := r.svc.BeginFallback(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	b := []byte(`{}`)
	_, _ = f.Write(b)
	if err := r.svc.ReceiveFallback(t.Context(), id, f, sumHex(b), int64(len(b))); err != nil {
		t.Fatal(err)
	}
	if r.wakes.Load() != 1 {
		t.Errorf("wakes = %d", r.wakes.Load())
	}
}

// TestFallbackReceipt_EachDatabaseCallMayFail: whichever database call of the receipt fails, the caller gets an error, no part file is
// left, the slot is given back and the upload stays open (or, when the fault hit after the commit's last statement, is received).
func TestFallbackReceipt_EachDatabaseCallMayFail(t *testing.T) {
	t.Parallel()
	body := []byte("0123456789")
	failed := 0
	for k := int64(1); k <= 12; k++ {
		t.Run(fmt.Sprintf("call %d", k), func(t *testing.T) {
			r, ctl := newFailingRig(t)
			id := r.str(`SELECT id FROM uploads WHERE artifact = 'audio.flac'`)
			ctl.arm(k)
			f, err := r.svc.BeginFallback(t.Context(), id)
			if err == nil {
				_, _ = f.Write(body)
				err = r.svc.ReceiveFallback(t.Context(), id, f, sumHex(body), int64(len(body)))
			}
			ctl.disarm()
			if err != nil {
				failed++
			}
			if r.svc.inFlight(id) {
				t.Error("slot held after the receipt ended")
			}
			if _, serr := os.Stat(filepath.Join(r.dataDir(), uploadPath(id)+".part")); !os.IsNotExist(serr) {
				t.Errorf("part file left: %v", serr)
			}
			state := r.str(`SELECT state FROM uploads WHERE id = ?`, id)
			switch {
			case err == nil && state != "received":
				t.Errorf("a receipt that succeeded left the upload %s", state)
			case err != nil && state != "open":
				t.Errorf("a failed receipt left the upload %s", state)
			}
		})
	}
	_ = failed
}

func TestAttemptUploads_DatabaseFault(t *testing.T) {
	t.Parallel()
	r, ctl := newFailingRig(t)
	ctl.arm(1)
	if _, err := r.svc.AttemptUploads(t.Context(), attemptID); err == nil {
		t.Fatal("a database fault was swallowed")
	}
}

func TestBeginFallback_FileSystemFaults(t *testing.T) {
	t.Parallel()
	t.Run("the uploads directory cannot be made", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		if err := os.WriteFile(filepath.Join(r.dataDir(), "incoming"), []byte("x"), 0o600); err != nil { // a file where the directory belongs
			t.Fatal(err)
		}
		if _, err := r.svc.BeginFallback(t.Context(), id); err == nil {
			t.Fatal("accepted")
		}
		if r.svc.inFlight(id) {
			t.Error("slot held")
		}
	})
	t.Run("the part file cannot be created", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		if err := os.MkdirAll(filepath.Join(r.dataDir(), "incoming/uploads"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(r.dataDir(), "incoming/uploads"), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(r.dataDir(), "incoming/uploads"), 0o700) })
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory modes")
		}
		if _, err := r.svc.BeginFallback(t.Context(), id); err == nil {
			t.Fatal("accepted")
		}
		if r.svc.inFlight(id) {
			t.Error("slot held")
		}
	})
	t.Run("the part file vanishes before the receipt", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		f, err := r.svc.BeginFallback(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte("abc"))
		if err := os.Remove(filepath.Join(r.dataDir(), uploadPath(id)+".part")); err != nil {
			t.Fatal(err)
		}
		if err := r.svc.ReceiveFallback(t.Context(), id, f, sumHex([]byte("abc")), 3); err == nil {
			t.Fatal("a receipt of a vanished part file was accepted")
		}
		if r.svc.inFlight(id) {
			t.Error("slot held")
		}
	})
	t.Run("a part file that is too big for the upload", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("request.json") // 100 bytes
		f, err := r.svc.BeginFallback(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(make([]byte, 101))
		if err := r.svc.ReceiveFallback(t.Context(), id, f, sumHex(make([]byte, 101)), 101); err == nil {
			t.Fatal("an oversized part file was accepted")
		}
	})
}

func TestReceiveFallback_CommitAndRenameFaults(t *testing.T) {
	t.Parallel()
	body := []byte("0123456789")
	t.Run("the event cannot be written", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		r.exec(`CREATE TRIGGER boom BEFORE INSERT ON job_events WHEN NEW.event = 'upload' BEGIN SELECT RAISE(ABORT, 'boom'); END`)
		f, err := r.svc.BeginFallback(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(body)
		if err := r.svc.ReceiveFallback(t.Context(), id, f, sumHex(body), int64(len(body))); err == nil || errors.Is(err, ErrFallbackClosed) {
			t.Fatalf("err = %v", err)
		}
		if got := r.str(`SELECT state FROM uploads WHERE id = ?`, id); got != "open" {
			t.Errorf("state %s", got)
		}
		if _, err := os.Stat(filepath.Join(r.dataDir(), uploadPath(id))); !os.IsNotExist(err) {
			t.Errorf("the file of a failed commit was kept: %v", err)
		}
	})
	t.Run("the final name is taken by a directory", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		id, _ := r.target("audio.flac")
		f, err := r.svc.BeginFallback(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(r.dataDir(), uploadPath(id)), 0o700); err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(body)
		if err := r.svc.ReceiveFallback(t.Context(), id, f, sumHex(body), int64(len(body))); err == nil {
			t.Fatal("accepted")
		}
		if r.svc.inFlight(id) {
			t.Error("slot held")
		}
	})
}

// TestReceiveFallback_SyncFault: a part that cannot be synced (a pipe stands in for a failing disk) is refused and cleaned up.
func TestReceiveFallback_SyncFault(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	id, _ := r.target("audio.flac")
	f, err := r.svc.BeginFallback(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Close()
	if err := r.svc.ReceiveFallback(t.Context(), id, pr, sumHex(nil), 0); err == nil {
		t.Fatal("a part that cannot be synced was accepted")
	}
	if r.svc.inFlight(id) {
		t.Error("slot held")
	}
}
