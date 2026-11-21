package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/objstore"
	"github.com/nicolasacchi/metamusic/internal/objstore/objstoretest"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload/fallback"
)

// useBucket points the upload commands at an in-process bucket for the duration of the test. The tests that call it are not
// parallel: the hook is a package variable.
func useBucket(t *testing.T, bkt *objstoretest.Server) {
	t.Helper()
	old := fallbackClient
	t.Cleanup(func() { fallbackClient = old })
	fallbackClient = func(string) (fallback.Objects, *http.Client, string, bool, error) {
		c, err := objstore.New(bkt.Config(), bkt.HTTPClient(), time.Now)
		return c, bkt.HTTPClient(), "mm/", true, err
	}
}

func TestCLI_FallbackCheck(t *testing.T) {
	env := testEnv(t, "local")
	t.Run("absent r2.env", func(t *testing.T) {
		code, out, errs := do(t, env, "", "upload", "fallback", "check")
		if code != 3 || !strings.Contains(errs, "not configured") || out != "" {
			t.Fatalf("exit %d out %q err %q", code, out, errs)
		}
	})
	bkt := objstoretest.New(t)
	useBucket(t, bkt)
	t.Run("passes against a bucket", func(t *testing.T) {
		code, out, errs := do(t, env, "", "upload", "fallback", "check")
		if code != 0 || !strings.Contains(out, "upload fallback check passed") {
			t.Fatalf("exit %d out %q err %q", code, out, errs)
		}
		for _, step := range []string{"put", "head", "get", "delete", "head-after-delete", "clock"} {
			if !strings.Contains(out, step) {
				t.Errorf("the report lacks %s: %q", step, out)
			}
		}
		if len(bkt.Keys()) != 0 {
			t.Error("the check left an object")
		}
	})
	t.Run("a refusing bucket fails without printing a URL or key", func(t *testing.T) {
		bkt.FailNext(1, 403)
		code, out, errs := do(t, env, "", "upload", "fallback", "check")
		if code != 1 || !strings.Contains(out, "FAIL") {
			t.Fatalf("exit %d out %q err %q", code, out, errs)
		}
		cfg := bkt.Config()
		for _, banned := range []string{"X-Amz", cfg.SecretAccessKey, cfg.AccessKeyID, "http://", "https://"} {
			if strings.Contains(out+errs, banned) {
				t.Errorf("the output carries %q", banned)
			}
		}
	})
	t.Run("usage", func(t *testing.T) {
		for _, args := range [][]string{{"upload"}, {"upload", "nope"}, {"upload", "fallback"}, {"upload", "fallback", "nope"}, {"upload", "fallback", "check", "x"}} {
			if code, _, _ := do(t, env, "", args...); code != 2 {
				t.Errorf("%v: exit %d", args, code)
			}
		}
		if code, out, _ := do(t, env, "", "upload", "help"); code != 0 || !strings.Contains(out, "drill") {
			t.Errorf("help: %d %q", code, out)
		}
	})
}

// drillRig is a data directory whose database has one job with one attempt and an upload that has a fallback object.
func drillRig(t *testing.T) (env map[string]string, st *store.Store, attemptID string) {
	t.Helper()
	env = testEnv(t, "local")
	var err error
	if st, err = store.Open(t.Context(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	librarytest.FixtureM5(t, st, librarytest.FixedNow)
	var job string
	if err := st.R.QueryRow(`SELECT id FROM jobs ORDER BY id LIMIT 1`).Scan(&job); err != nil {
		t.Fatal(err)
	}
	const at = "2026-01-01T00:00:00.000Z"
	attemptID = "01900000-0000-7000-8000-00000000c777"
	for _, q := range []string{
		`INSERT INTO job_attempts(id,job_id,n,backend,state,ref,submitted_at,window_ends_at,price_pph_micro_usd,created_at) VALUES ('` + attemptID + `','` + job + `',99,'fake','pending',NULL,'` + at + `','` + at + `',1,'` + at + `')`,
		`INSERT INTO uploads(id,job_id,attempt_id,artifact,max_bytes,exp,state,created_at) VALUES ('01900000-0000-7000-8000-00000000d777','` + job + `','` + attemptID + `','audio.flac',100,1,'open','` + at + `')`,
		`INSERT INTO fallback_objects(upload_id,attempt_id,minted_at) VALUES ('01900000-0000-7000-8000-00000000d777','` + attemptID + `','` + at + `')`,
	} {
		if _, err := st.W.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return env, st, attemptID
}

func TestCLI_UploadDrill(t *testing.T) {
	t.Parallel()
	env, st, att := drillRig(t)
	for name, args := range map[string][]string{
		"no attempt":   {"upload", "drill", "--seconds", "60", "--reason", "r"},
		"too short":    {"upload", "drill", "--attempt", att, "--seconds", "10", "--reason", "r"},
		"too long":     {"upload", "drill", "--attempt", att, "--seconds", "601", "--reason", "r"},
		"no reason":    {"upload", "drill", "--attempt", att, "--seconds", "60"},
		"extra args":   {"upload", "drill", "--attempt", att, "--seconds", "60", "--reason", "r", "x"},
		"unknown flag": {"upload", "drill", "--nope"},
	} {
		if code, _, _ := do(t, env, "", args...); code != 2 {
			t.Errorf("%s: exit %d", name, code)
		}
	}
	if code, _, errs := do(t, env, "", "upload", "drill", "--attempt", "nope", "--seconds", "60", "--reason", "r"); code != 3 || !strings.Contains(errs, "no fallback objects") {
		t.Errorf("unknown attempt: exit %d %q", code, errs)
	}
	code, out, errs := do(t, env, "", "upload", "drill", "--attempt", att, "--seconds", "60", "--reason", "R2 drill")
	if code != 0 || !strings.Contains(out, "answers 503") {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	var until, by string
	if err := st.R.QueryRow(`SELECT until, created_by FROM upload_drills WHERE attempt_id = ?`, att).Scan(&until, &by); err != nil || by != "system:cli" {
		t.Fatalf("%v %q", err, by)
	}
	if ut, err := store.ParseTime(until); err != nil || time.Until(ut) < 50*time.Second || time.Until(ut) > 61*time.Second {
		t.Errorf("until = %s (%v)", until, err)
	}
	var n int
	if err := st.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE op = ? AND principal_kind = 'system'`, fallback.OpDrill).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit rows = %d (%v)", n, err)
	}
	if code, _, _ := do(t, env, "", "upload", "drill", "--attempt", att, "--seconds", "60", "--reason", "again"); code == 0 {
		t.Error("a second drill for the attempt was accepted")
	}
}

func TestConfigCheck_UploadFallbackLine(t *testing.T) {
	t.Parallel()
	env := testEnv(t, "local")
	code, out, errs := do(t, env, "", "config", "check")
	if code != 0 || !strings.Contains(out, "upload fallback: not configured") {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	sec := filepath.Join(env["MM_DATA_DIR"], "secrets")
	body := "R2_" + "ENDPOINT=https://bucket.example.com\n" + "R2_" + "BUCKET=mm-test\n" + "R2_" + "ACCESS_KEY_ID=" + "synthetic-id-0001\n" + "R2_" + "SECRET_ACCESS_KEY=" + "synthetic-secret-0002\n"
	writeFile(t, filepath.Join(sec, "r2.env"), body, 0o600)
	code, out, errs = do(t, env, "", "config", "check")
	if code != 0 || !strings.Contains(out, "upload fallback: configured") {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	for _, v := range []string{"synthetic-id-0001", "synthetic-secret-0002", "bucket.example.com", "mm-test"} {
		if strings.Contains(out+errs, v) {
			t.Errorf("config check printed %q", v)
		}
	}
	writeFile(t, filepath.Join(sec, "r2.env"), body, 0o644)
	if code, _, errs = do(t, env, "", "config", "check"); code != 1 || !strings.Contains(errs, "r2.env") {
		t.Errorf("a loose r2.env: exit %d %q", code, errs)
	}
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestCanaryFallbackHooks_OverTheDatabase(t *testing.T) {
	t.Parallel()
	env, st, att := drillRig(t)
	ctx := t.Context()
	h, closeDB, err := newFallbackHooks(ctx, envFn(env))
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	var job string
	if err := st.R.QueryRow(`SELECT job_id FROM job_attempts WHERE id = ?`, att).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if err := h.Drill(ctx, "no-such-job", 300); err == nil {
		t.Error("a drill for a job without fallback objects was written")
	}
	if err := h.Drill(ctx, job, 300); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.ObjectsPending(ctx, job); n != 1 {
		t.Errorf("pending = %d", n)
	}
	if _, err := st.W.Exec(`UPDATE fallback_objects SET deleted_at = '2026-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.ObjectsPending(ctx, job); n != 0 {
		t.Errorf("pending after delete = %d", n)
	}
	if via, total, err := h.Receipts(ctx, job); err != nil || via != 0 || total != 0 {
		t.Errorf("receipts = %d/%d %v", via, total, err)
	}
	if _, err := st.W.Exec(`UPDATE uploads SET state='received', sha256='aa', bytes=1, received_at='2026-01-01T00:00:00.000Z', via='fallback' WHERE attempt_id = ?`, att); err != nil {
		t.Fatal(err)
	}
	if via, total, err := h.Receipts(ctx, job); err != nil || via != 1 || total != 1 {
		t.Errorf("receipts = %d/%d %v", via, total, err)
	}
	if leaks, err := h.Leaks(ctx, job); err != nil || leaks {
		t.Errorf("leaks = %v %v", leaks, err)
	}
	if _, err := st.W.Exec(`INSERT INTO job_events(job_id, at, event, detail_json) VALUES (?, '2026-01-01T00:00:00.000Z', 'status', '{"text":"https://x?X-Amz-Signature=abc"}')`, job); err != nil {
		t.Fatal(err)
	}
	if leaks, err := h.Leaks(ctx, job); err != nil || !leaks {
		t.Errorf("a signature in an event was not found: %v %v", leaks, err)
	}
}
