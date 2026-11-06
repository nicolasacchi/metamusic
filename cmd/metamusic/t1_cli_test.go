package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/transform"
	"github.com/nicolasacchi/metamusic/internal/transform/transformtest"
)

// t1Target is the loudness the fake ingest measures (-16 LUFS), so the bridge accepts the result.
var t1Target = -16.0

// t1CLIEnv is a data directory holding the M1 fixture with a master file per Take (empty: FakeMedia stands in for ffmpeg) and
// the fakes the CLI builds its transform service with. Tests using it set the package seam t1Options and so are not parallel.
func t1CLIEnv(t *testing.T) (env map[string]string, child *transformtest.FakeChild) {
	t.Helper()
	env = testEnv(t, "local")
	dir := env["MM_DATA_DIR"]
	st, err := store.Open(context.Background(), filepath.Join(dir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	librarytest.Fixture(t, st, librarytest.FixedNow)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	media := &transformtest.FakeMedia{Sources: map[string]transformtest.FakeSrc{}}
	rows, err := st.R.Query(`SELECT master_path, total_samples, channels FROM tracks WHERE import_rule NOT IN ('reference') AND origin = 'import'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p string
		var n int64
		var ch int
		if err := rows.Scan(&p, &n, &ch); err != nil {
			t.Fatal(err)
		}
		media.Sources[filepath.Base(p)] = transformtest.FakeSrc{Frames: n, Channels: ch}
		if err := root.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := root.WriteFile(p, nil, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	_ = rows.Close()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	child = &transformtest.FakeChild{}
	t1Options = app.T1Options{Child: child, Media: media, Pipeline: &transformtest.FakeIngest{Data: root}}
	t.Cleanup(func() { t1Options = app.T1Options{} })
	return env, child
}

// t1DoneTransform renders one glacier of the Take to done, through the services the CLI builds.
func t1DoneTransform(t *testing.T, env map[string]string) string {
	t.Helper()
	ctx := context.Background()
	svc, _, closeAll, err := openTransform(ctx, envFn(env))
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	p := auth.Principal{Kind: auth.KindHuman, ID: "op", Name: "op", Via: "local"}
	req := transform.Request{Kind: transform.KindGlacier, TargetLUFS: &t1Target, Members: map[transform.Kind]json.RawMessage{transform.KindGlacier: json.RawMessage(`{"source_id":"` + librarytest.TrackTwinA + `"}`)}}
	res, err := svc.Transform.Create(ctx, p, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := svc.Transform.RunOne(ctx); err != nil || !ran {
		t.Fatalf("RunOne: %v %v", ran, err)
	}
	v, err := svc.Transform.Get(ctx, p, res.View.ID)
	if err != nil || v.State != "done" {
		t.Fatalf("transform not done: state %s error %+v (%v)", v.State, v.Error, err)
	}
	return v.ID
}

// TestCLI_TransformVerify: a done transform re-renders to the recorded hash (exit 0); a regression in the render, a different
// hash from the same spec, is reported and exits 1. --all verifies the newest done transforms.
func TestCLI_TransformVerify(t *testing.T) {
	env, child := t1CLIEnv(t)
	id := t1DoneTransform(t, env)

	code, out, errs := do(t, env, "", "transform", "verify", id)
	if code != 0 || !strings.Contains(out, id+" match ") {
		t.Fatalf("verify: %d %q %q", code, out, errs)
	}
	code, out, errs = do(t, env, "", "transform", "verify", "--all", "--limit", "5")
	if code != 0 || !strings.Contains(out, "1 verified: 1 match, 0 inconclusive, 0 failed") {
		t.Fatalf("verify --all: %d %q %q", code, out, errs)
	}

	child.Same = true // the re-render no longer produces the recorded audio
	code, out, errs = do(t, env, "", "transform", "verify", id)
	if code != 1 || !strings.Contains(out, id+" MISMATCH") {
		t.Fatalf("tampered: %d %q %q", code, out, errs)
	}
	if code, out, _ = do(t, env, "", "transform", "verify", "--all"); code != 1 || !strings.Contains(out, "1 failed") {
		t.Fatalf("tampered --all: %d %q", code, out)
	}
}

func TestCLI_TransformVerify_Usage(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"transform"}, {"transform", "verify"}, {"transform", "verify", "--all", "x"}, {"transform", "verify", "a", "b"},
		{"transform", "verify", "--all", "--limit", "0"}, {"transform", "bogus"}, {"transform", "sweep", "x"}} {
		if code, _, _ := do(t, testEnv(t, "local"), "", args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

// TestCLI_Sweep: sweep recovers and cleans stale work dirs; it refuses while a server holds a live lease on a running transform.
func TestCLI_Sweep(t *testing.T) {
	env, _ := t1CLIEnv(t)
	id := t1DoneTransform(t, env)
	dir := env["MM_DATA_DIR"]
	stale := filepath.Join(dir, "incoming", "transform-gone")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	old := librarytest.FixedNow
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	code, out, errs := do(t, env, "", "transform", "sweep")
	if code != 0 || !strings.Contains(out, "transform sweep: done") {
		t.Fatalf("sweep: %d %q %q", code, out, errs)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the stale work dir is still there: %v", err)
	}

	// a live lease on a running row: refuse, touch nothing
	ctx := context.Background()
	svc, st, closeAll, err := openTransform(ctx, envFn(env))
	if err != nil {
		t.Fatal(err)
	}
	p := auth.Principal{Kind: auth.KindHuman, ID: "op", Name: "op", Via: "local"}
	res, err := svc.Transform.Create(ctx, p, transform.Request{Kind: transform.KindGlacier, TargetLUFS: &t1Target,
		Members: map[transform.Kind]json.RawMessage{transform.KindGlacier: json.RawMessage(`{"source_id":"` + librarytest.TrackTwinA + `","factor":30}`)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`UPDATE transforms SET state = 'running', owner = 'other-server', started_at = ?, lease_expires_at = ? WHERE id = ?`,
		store.FormatTime(time.Now()), store.FormatTime(time.Now().Add(time.Hour)), res.View.ID); err != nil {
		t.Fatal(err)
	}
	closeAll()
	code, out, errs = do(t, env, "", "transform", "sweep")
	if code != 2 || !strings.Contains(errs, "live lease") || strings.Contains(out, "done") {
		t.Fatalf("sweep with a live lease: %d %q %q", code, out, errs)
	}
	_ = id
}
