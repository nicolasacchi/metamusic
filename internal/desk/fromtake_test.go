package desk_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/desk"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func wantProblem(t *testing.T, err error, status int, code string) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != status || pe.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
	return pe
}

// TestFromTake_Rules_Table: the sources a plan can come from and the reasons of PLAN_SOURCE_UNSUPPORTED (C358.1).
func TestFromTake_Rules_Table(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	transform := lt.InsertTransformTake(t, e.st, epoch, lt.TransformTake{N: 1, Kind: "score_render"})
	e.writeScore("takes/"+lt.TrackLuna+"/score.abc", aligned)
	e.writeScore("takes/"+lt.TrackShort+"/score.abc", "X:1\nT:t\nM:4/4\nL:1/8\nK:C\nCDEF|CDEF|]\n")
	for _, c := range []struct {
		name, track, reason string
		status              int
		code                string
	}{
		{"unknown take", "no-such-take", "", 404, "NOT_FOUND"},
		{"lossy reference", lt.TrackLossy, desk.ReasonLossy, 422, desk.CodeSourceUnsupported},
		{"reference flac", lt.TrackRefFlac, desk.ReasonReference, 422, desk.CodeSourceUnsupported},
		{"transform take", transform, desk.ReasonTransform, 422, desk.CodeSourceUnsupported},
		{"take without a score", lt.TrackCitta, desk.ReasonNoScore, 422, desk.CodeSourceUnsupported},
		{"aligned score", lt.TrackLuna, "", 0, ""},
		{"untimed score", lt.TrackShort, "", 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			pl, created, err := e.svc.FromTake(ctx, alice, c.track)
			if c.status != 0 {
				pe := wantProblem(t, err, c.status, c.code)
				if c.reason != "" && pe.Details["reason"] != c.reason {
					t.Errorf("reason = %v, want %s", pe.Details["reason"], c.reason)
				}
				return
			}
			if err != nil || !created {
				t.Fatalf("created %v, err %v", created, err)
			}
			if pl.Source != "take" || pl.TrackID == nil || *pl.TrackID != c.track || pl.Base.TrackID == nil {
				t.Errorf("plan = %+v", pl.PlanSummary)
			}
		})
	}
}

// TestFromTake_Fields: the plan carries the take's score, request without abc, timing and sections.
func TestFromTake_Fields(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	pl := e.plan()
	if pl.Cot != "full" || pl.Bars != 6 || pl.Meter != "4/4" || pl.Q == nil || *pl.Q != "1/4=120" || pl.SecondsPerBar == nil || *pl.SecondsPerBar != 2 {
		t.Errorf("summary = %+v", pl.PlanSummary)
	}
	if pl.ExpectedAudioS != 12 || !pl.Score.Aligned || pl.Score.Dialect != "yue2" || pl.Score.Bytes != len(aligned) {
		t.Errorf("expected %d aligned %v score %+v", pl.ExpectedAudioS, pl.Score.Aligned, pl.Score)
	}
	if pl.Score.SHA256 != lt.Sha(aligned) || pl.ABC == nil || *pl.ABC != aligned {
		t.Errorf("sha %s", pl.Score.SHA256)
	}
	if len(pl.Sections) != 3 || pl.Sections[1].Label != "chorus" || pl.Sections[1].StartBar != 2 || *pl.Sections[1].StartS != 4 || *pl.Sections[1].EndS != 8 {
		t.Errorf("sections = %+v", pl.Sections)
	}
	if len(pl.Score.Voices) != 1 || pl.Score.Voices[0].Bars != 6 {
		t.Errorf("voices = %+v", pl.Score.Voices)
	}
	if pl.Base.Title == "" || pl.Title != pl.Base.Title || pl.CreatedBy.ID != "human:alice" || pl.CreatedBy.Kind != "human" {
		t.Errorf("base %+v by %+v", pl.Base, pl.CreatedBy)
	}
	if got := e.scalar(`SELECT request_json FROM desk_plans WHERE id = ?`, pl.ID); got == "" || contains(got, `"abc"`) {
		t.Errorf("request_json %q", got)
	}
	// a take plan keeps pointing at the take's own file and copies nothing
	if got := e.scalar(`SELECT score_path FROM desk_plans WHERE id = ?`, pl.ID); got != "takes/"+lt.TrackLuna+"/score.abc" {
		t.Errorf("score_path = %q", got)
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plan_files`); n != "0" {
		t.Errorf("%s plan files for a take plan", n)
	}
	if _, err := e.root.Stat("plans"); err == nil {
		t.Error("a take plan wrote a plans/ directory")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// TestFromTake_Idempotent: a second call returns the same plan with created false and writes nothing.
func TestFromTake_Idempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	first := e.plan()
	again, created, err := e.svc.FromTake(context.Background(), robot, lt.TrackLuna)
	if err != nil || created || again.ID != first.ID || again.CreatedBy.ID != "human:alice" {
		t.Fatalf("repeat: %v created %v id %s (%s)", err, created, again.ID, first.ID)
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plans`); n != "1" {
		t.Errorf("%s plans", n)
	}
}

// TestFromTake_TooLargeAndBadScore: a score over 64 KiB and one that does not parse.
func TestFromTake_TooLargeAndBadScore(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// the file is bigger than the limit: the row's hash is irrelevant, the size is checked first
	huge := make([]byte, desk.MaxScoreBytes+10)
	for i := range huge {
		huge[i] = '%'
	}
	e.writeScore("takes/"+lt.TrackLuna+"/score.abc", string(huge))
	pe := wantProblem(t, func() error { _, _, err := e.svc.FromTake(context.Background(), alice, lt.TrackLuna); return err }(), 422, desk.CodeSourceUnsupported)
	if pe.Details["reason"] != desk.ReasonTooLarge {
		t.Errorf("reason = %v", pe.Details["reason"])
	}
}
