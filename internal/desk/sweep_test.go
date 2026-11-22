package desk_test

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestDeskSweep_RemovesOrphans: a plan directory without a row, a partial directory and an audition work directory are removed;
// a plan's own directory and an audition MP3 with a row stay.
func TestDeskSweep_RemovesOrphans(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	if n, err := e.svc.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("empty data dir: %d %v", n, err)
	}
	pl := e.plan()
	child, _, err := e.svc.Edit(ctx, alice, pl.ID, replaceBar1("CDEFGABc"))
	if err != nil {
		t.Fatal(err)
	}
	orphan := func(rel string, mode os.FileMode) {
		t.Helper()
		if err := e.root.MkdirAll(rel, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := e.root.WriteFile(rel+"/score.abc", []byte("X:1\n"), 0o444); err != nil {
			t.Fatal(err)
		}
		if err := e.root.Chmod(rel, mode); err != nil {
			t.Fatal(err)
		}
	}
	orphan("plans/01900000-0000-7000-a000-999999999999", 0o555) // written, insert failed
	orphan("plans/01900000-0000-7000-a000-888888888888.partial", 0o700)
	orphan("derived/auditions/work-1234", 0o755)
	if err := e.root.MkdirAll("derived/auditions/ab", 0o755); err != nil {
		t.Fatal(err)
	}
	keep, lost := "derived/auditions/ab/keep.mp3", "derived/auditions/ab/lost.mp3"
	for _, p := range []string{keep, lost} {
		if err := e.root.WriteFile(p, []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	e.exec(`INSERT INTO desk_auditions(id,plan_id,spec_hash,soundfont,soundfont_sha256,synth_version,chords,mp3_path,mp3_sha256,mp3_bytes,pcm_sha256,seconds,cpu_ms,report_json,principal_id,created_at)
		VALUES('a1',?,?,'gm','s','2.3','block',?,?,1,'p',1,1,'{}','human:alice','2026-11-21T10:00:00.000Z')`, pl.ID, hex64("1"), keep, hex64("2"))
	// young orphans are left alone: another process may be about to insert their rows
	if n, err := e.svc.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("young orphans: removed %d: %v", n, err)
	}
	old := epoch.Add(-time.Hour)
	for _, p := range []string{"plans/01900000-0000-7000-a000-999999999999", "plans/01900000-0000-7000-a000-888888888888.partial", "derived/auditions/work-1234", lost} {
		if err := os.Chtimes(e.dir+"/"+p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	n, err := e.svc.Sweep(ctx)
	if err != nil || n != 4 {
		t.Fatalf("removed %d: %v", n, err)
	}
	for _, gone := range []string{"plans/01900000-0000-7000-a000-999999999999", "plans/01900000-0000-7000-a000-888888888888.partial", "derived/auditions/work-1234", lost} {
		if _, err := e.root.Stat(gone); err == nil {
			t.Errorf("%s survived the sweep", gone)
		}
	}
	for _, kept := range []string{"plans/" + child.ID + "/score.abc", keep, "takes/" + *pl.TrackID + "/score.abc"} {
		if _, err := e.root.Stat(kept); err != nil {
			t.Errorf("%s was removed: %v", kept, err)
		}
	}
	if n, err := e.svc.Sweep(ctx); err != nil || n != 0 {
		t.Errorf("second sweep removed %d: %v", n, err)
	}
}

func hex64(c string) string {
	s := ""
	for i := 0; i < 64; i++ {
		s += c
	}
	return s
}
