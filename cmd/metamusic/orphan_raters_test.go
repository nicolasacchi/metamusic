package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestOrphanRaterDoesNotBlockStartup: ratings of a user who is not in the configured user map are inert rows: the server
// still starts (a warning is logged), and `library verify` prints a warning line for them without counting it as an
// anomaly (C301.10, D-U1-4).
func TestOrphanRaterDoesNotBlockStartup(t *testing.T) {
	t.Parallel()
	env := testEnv(t, "local")
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	librarytest.FixtureV2(t, st, librarytest.FixedNow)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	codeBefore, outBefore, _ := do(t, env, "", "library", "verify")
	if strings.Contains(outBefore, "orphan_rater") {
		t.Fatalf("orphan reported without orphans: %s", outBefore)
	}
	st, err = store.Open(ctx, filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO ratings(track_id,source,rater,stars,principal_kind,principal_id,principal_name,created_at,updated_at)
		VALUES(?, 'human', 'zed', 3, 'human', 'human:zed', 'zed', '2026-10-14T10:00:00Z', '2026-10-14T10:00:00Z')`, librarytest.TrackLuna); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	code, out, errs := do(t, env, "", "library", "verify")
	if !strings.Contains(out, "warning orphan_rater zed 1 ") {
		t.Errorf("no orphan warning: %q %q", out, errs)
	}
	if code != codeBefore {
		t.Errorf("an orphan rater changed the verify exit code: %d -> %d", codeBefore, code)
	}

	runCtx, cancel := context.WithCancel(ctx)
	var sout, serr syncBuf
	done := make(chan int, 1)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	})
	go func() {
		done <- run(runCtx, []string{"serve"}, envFn(env), strings.NewReader(""), &sout, &serr)
		close(done)
	}()
	if addr := waitListening(&sout, 60*time.Second); addr == "" {
		t.Fatalf("an orphan rater stopped the server: %s %s", sout.String(), serr.String())
	}
}
