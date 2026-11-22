package desk_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/desk"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func find(fs []desk.Finding, code, subject string) *desk.Finding {
	for i := range fs {
		if fs[i].Code == code && (subject == "" || fs[i].Subject == subject) {
			return &fs[i]
		}
	}
	return nil
}

// TestVerify_Desk: D1 to D4 of C357.4, and a clean library has no finding.
func TestVerify_Desk(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	verify := func() []desk.Finding {
		t.Helper()
		fs, err := desk.Verify(ctx, e.st.R, e.root)
		if err != nil {
			t.Fatal(err)
		}
		return fs
	}
	if fs := verify(); len(fs) != 0 {
		t.Fatalf("empty desk: %+v", fs)
	}
	take := e.plan()
	child, _, err := e.svc.Edit(ctx, alice, take.ID, replaceBar1("CDEFGABc"))
	if err != nil {
		t.Fatal(err)
	}
	if fs := verify(); len(fs) != 0 {
		t.Fatalf("clean desk: %+v", fs)
	}
	// D1: a plan file whose bytes changed, then one that is gone
	path := "plans/" + child.ID + "/score.abc"
	if err := e.root.Chmod("plans/"+child.ID, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.root.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := e.root.WriteFile(path, []byte("X:1\nK:C\nC|\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	f := find(verify(), desk.CodeFileChanged, child.ID+"/score.abc")
	if f == nil || f.Severity != "error" {
		t.Fatalf("changed file: %+v", f)
	}
	if err := e.root.Remove(path); err != nil {
		t.Fatal(err)
	}
	if f := find(verify(), desk.CodeFileMissing, child.ID+"/score.abc"); f == nil || f.Severity != "error" {
		t.Fatalf("missing file: %+v", f)
	}
	// D2: a take plan whose score is no longer the take's
	e.exec(`DROP TRIGGER scores_immutable`)
	e.exec(`UPDATE scores SET abc_sha256 = ? WHERE track_id = ?`, lt.Sha("other"), lt.TrackLuna)
	if f := find(verify(), desk.CodeScoreMismatch, take.ID); f == nil || f.Severity != "error" {
		t.Fatalf("score mismatch: %+v", f)
	}
	// D3: a completed plan job without a plan
	e.dropJobTriggers()
	e.exec(`UPDATE jobs SET kind = 'plan', state = 'completed', track_id = NULL, outcome = 'clean', finished_at = '2026-11-21T10:00:00.000Z' WHERE id = ?`, lt.JobAwaiting1)
	if f := find(verify(), desk.CodePlanJobNoPlans, lt.JobAwaiting1); f == nil || f.Severity != "error" {
		t.Fatalf("plan job without plans: %+v", f)
	}
	// D4: an audition row whose file is missing is only a warning
	h := lt.Sha("a")
	e.exec(`INSERT INTO desk_auditions(id,plan_id,spec_hash,soundfont,soundfont_sha256,synth_version,chords,mp3_path,mp3_sha256,mp3_bytes,pcm_sha256,seconds,cpu_ms,report_json,principal_id,created_at)
		VALUES('a1',?,?,'gm',?,'2.3','block','derived/auditions/ab/gone.mp3',?,1,?,1,1,'{}','human:alice','2026-11-21T10:00:00.000Z')`, take.ID, h, h, h, h)
	if f := find(verify(), desk.CodeAuditionFile, "a1"); f == nil || f.Severity != "warning" {
		t.Fatalf("audition file: %+v", f)
	}
}
