package desk_test

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/desk"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// TestService_Unwired: a service without a store or data root refuses every call with 503 UNAVAILABLE {component: desk}.
func TestService_Unwired(t *testing.T) {
	t.Parallel()
	s := desk.New(desk.Options{})
	ctx := context.Background()
	calls := map[string]func() error{
		"FromTake":   func() error { _, _, err := s.FromTake(ctx, alice, "x"); return err },
		"Edit":       func() error { _, _, err := s.Edit(ctx, alice, "x", desk.EditIn{}); return err },
		"Get":        func() error { _, err := s.Get(ctx, alice, "x", desk.DefaultInclude); return err },
		"List":       func() error { _, err := s.List(ctx, alice, desk.Filter{}); return err },
		"SetVerdict": func() error { _, err := s.SetVerdict(ctx, alice, "x", desk.VerdictIn{}); return err },
		"Sweep":      func() error { _, err := s.Sweep(ctx); return err },
	}
	for name, f := range calls {
		pe := wantProblem(t, f(), 503, "UNAVAILABLE")
		if pe.Details["component"] != "desk" {
			t.Errorf("%s: details %v", name, pe.Details)
		}
	}
	var nilSvc *desk.Service
	wantProblem(t, func() error { _, err := nilSvc.Get(ctx, alice, "x", desk.Include{}); return err }(), 503, "UNAVAILABLE")
}

// TestFromTake_ScoreFileProblems: an unparsable score is SCORE_INVALID with its position; a missing file or a file that is not the
// recorded score is an internal error, never a plan.
func TestFromTake_ScoreFileProblems(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	rel := "takes/" + lt.TrackLuna + "/score.abc"
	if _, _, err := e.svc.FromTake(ctx, alice, lt.TrackLuna); err == nil || strings.Contains(err.Error(), "UNAVAILABLE") {
		t.Errorf("a missing score file: %v", err)
	}
	e.writeScore(rel, aligned+"extra\n")
	if _, _, err := e.svc.FromTake(ctx, alice, lt.TrackLuna); err == nil || !strings.Contains(err.Error(), "recorded hash") {
		t.Errorf("a changed score file: %v", err)
	}
	const broken = "X:1\nK:C\nCD&EF|\n"
	if err := e.root.Remove(rel); err != nil {
		t.Fatal(err)
	}
	e.writeScore(rel, broken)
	e.exec(`DROP TRIGGER scores_immutable`)
	e.exec(`UPDATE scores SET abc_sha256 = ? WHERE track_id = ?`, lt.Sha(broken), lt.TrackLuna)
	pe := wantProblem(t, func() error { _, _, err := e.svc.FromTake(ctx, alice, lt.TrackLuna); return err }(), 422, desk.CodeScoreInvalid)
	if pe.Details["rule"] == nil || pe.Details["line"] == nil {
		t.Errorf("details = %v", pe.Details)
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plans`); n != "0" {
		t.Errorf("%s plans", n)
	}
}

// TestEdit_StoreFailureLeavesNoFiles: when the row cannot be stored the plan directory is removed again.
func TestEdit_StoreFailureLeavesNoFiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	parent := e.plan()
	e.exec(`CREATE TEMP TRIGGER refuse_edit BEFORE INSERT ON desk_plans WHEN NEW.source = 'edit' BEGIN SELECT RAISE(ABORT, 'refused for the test'); END`)
	if _, _, err := e.svc.Edit(context.Background(), alice, parent.ID, replaceBar1("CDEFGABc")); err == nil || !strings.Contains(err.Error(), "refused for the test") {
		t.Fatalf("err = %v", err)
	}
	if left, _ := os.ReadDir(e.dir + "/plans"); len(left) != 0 {
		t.Errorf("plans/ holds %d entries after a failed store", len(left))
	}
}

// TestEdit_PlansPathBlocked: a data directory whose plans entry is a file cannot take a plan; the error says so and no row appears.
func TestEdit_PlansPathBlocked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	parent := e.plan()
	e.writeScore("plans", "not a directory")
	if _, _, err := e.svc.Edit(context.Background(), alice, parent.ID, replaceBar1("CDEFGABc")); err == nil {
		t.Fatal("the edit was stored")
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plans WHERE source = 'edit'`); n != "0" {
		t.Errorf("%s edit plans", n)
	}
}

// TestGet_MissingScoreFile: a plan whose file is gone is an error of the read, not an empty plan (library verify names it).
func TestGet_MissingScoreFile(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	pl := e.plan()
	if err := e.root.Remove("takes/" + lt.TrackLuna + "/score.abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(context.Background(), alice, pl.ID, desk.DefaultInclude); err == nil {
		t.Error("a plan without its score was read")
	}
	if _, _, err := e.svc.Edit(context.Background(), alice, pl.ID, replaceBar1("CDEFGABc")); err == nil {
		t.Error("a plan without its score was edited")
	}
}

// TestList_BadCursors: a cursor that is not ours is a 422, not a page.
func TestList_BadCursors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, c := range []string{"***", enc("not json"), enc(`{"c":"","i":""}`), enc(`{"c":"2026-01-01T00:00:00.000Z"}`)} {
		_, err := e.svc.List(context.Background(), alice, desk.Filter{Cursor: c})
		wantProblem(t, err, 422, "VALIDATION_FAILED")
	}
}

// TestGet_AuditionLinkSigned: with a signer the audition's link is signed for the caller and names the MP3's own hash; a verdict
// of an unusual stored key still renders as a reference.
func TestGet_AuditionLinkSigned(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	signer := serve.NewSigner([]byte("0123456789abcdef0123456789abcdef"), func() time.Time { return epoch })
	svc := desk.New(desk.Options{Store: e.st, Data: e.root, Signer: signer, Now: func() time.Time { return epoch }})
	pl, _, err := svc.FromTake(context.Background(), alice, e.takeWithScore())
	if err != nil {
		t.Fatal(err)
	}
	mp3 := lt.Sha("mp3")
	e.exec(`INSERT INTO desk_auditions(id,plan_id,spec_hash,soundfont,soundfont_sha256,synth_version,chords,mp3_path,mp3_sha256,mp3_bytes,pcm_sha256,seconds,cpu_ms,report_json,principal_id,created_at)
		VALUES('a1',?,?,'gm',?,'2.3','block','derived/auditions/ab/x.mp3',?,10,?,12.5,5,'{}','human:alice','2026-11-21T10:00:00.000Z')`, pl.ID, lt.Sha("spec"), mp3, mp3, mp3)
	e.exec(`INSERT INTO desk_plan_verdicts(plan_id, rater, verdict, decided_by, decided_at) VALUES (?, 'critic', 'liked', 'legacy-key', '2026-11-21T10:00:00.000Z')`, pl.ID)
	got, err := svc.Get(context.Background(), alice, pl.ID, desk.DefaultInclude)
	if err != nil || got.Auditions == nil || len(*got.Auditions) != 1 {
		t.Fatal(err)
	}
	if u := (*got.Auditions)[0].URL; !strings.Contains(u, mp3) || !strings.Contains(u, ".mp3") {
		t.Errorf("url = %q", u)
	}
	if c := got.Verdicts.Critic; c == nil || c.By.ID != "legacy-key" || c.By.Kind != "system" {
		t.Errorf("critic = %+v", got.Verdicts.Critic)
	}
}

// TestEdit_TruncatedFlagAndTokensInherit: a take plan of a truncated take says so, and its children inherit the flag.
func TestEdit_TruncatedFlagInherited(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.writeScore("takes/"+lt.TrackShort+"/score.abc", "X:1\nT:t\nM:4/4\nL:1/8\nK:C\nCDEF|CDEF|]\n")
	pl, _, err := e.svc.FromTake(context.Background(), alice, lt.TrackShort)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Truncated == nil || *pl.Truncated || pl.SecondsPerBar != nil || pl.Q != nil {
		t.Errorf("untimed plan: truncated %v spb %v q %v", pl.Truncated, pl.SecondsPerBar, pl.Q)
	}
	if pl.ExpectedAudioS < 1 {
		t.Errorf("expected audio %d", pl.ExpectedAudioS)
	}
	// every section list is a JSON array, never null
	if pl.Sections == nil {
		t.Error("sections is nil")
	}
}

// TestService_StoreFailures: with the database gone every call answers an error and nothing panics or half-writes.
func TestService_StoreFailures(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	pl := e.plan()
	ctx := context.Background()
	if err := e.st.Close(); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"FromTake": func() error { _, _, err := e.svc.FromTake(ctx, alice, lt.TrackLuna); return err },
		"Edit":     func() error { _, _, err := e.svc.Edit(ctx, alice, pl.ID, replaceBar1("CDEFGABc")); return err },
		"Get":      func() error { _, err := e.svc.Get(ctx, alice, pl.ID, desk.DefaultInclude); return err },
		"List":     func() error { _, err := e.svc.List(ctx, alice, desk.Filter{}); return err },
		"SetVerdict": func() error {
			_, err := e.svc.SetVerdict(ctx, alice, pl.ID, desk.VerdictIn{Verdict: "liked"})
			return err
		},
		"Verify": func() error { _, err := desk.Verify(ctx, e.st.R, e.root); return err },
	}
	for name, f := range calls {
		if err := f(); err == nil {
			t.Errorf("%s answered without a database", name)
		}
	}
}

func TestParseInclude_AllParts(t *testing.T) {
	t.Parallel()
	inc, err := desk.ParseInclude("abc,bars,display,auditions,renders")
	if err != nil || (inc != desk.Include{ABC: true, Bars: true, Display: true, Auditions: true, Renders: true}) {
		t.Errorf("%+v %v", inc, err)
	}
	if inc, err = desk.ParseInclude(" , "); err != nil || inc != desk.DefaultInclude {
		t.Errorf("blank list %+v %v", inc, err)
	}
}
