package duels_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func asProblem(t *testing.T, err error, status int, code string) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != status || pe.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
	return pe
}

func TestCreateDuel_Validation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.svc.Create(ctx, human, duels.CreateInput{ATrackID: duelstest.TrackAlpha, BTrackID: duelstest.TrackAlpha})
	asProblem(t, err, 422, "DUEL_SAME_TRACK")
	_, err = e.svc.Create(ctx, human, duels.CreateInput{ATrackID: duelstest.TrackAlpha, BTrackID: "nope"})
	if pe := asProblem(t, err, 404, "NOT_FOUND"); pe.Details["track_id"] != "nope" {
		t.Errorf("details = %v", pe.Details)
	}
	_, err = e.svc.Create(ctx, human, duels.CreateInput{ATrackID: "ghost", BTrackID: duelstest.TrackBeta})
	if pe := asProblem(t, err, 404, "NOT_FOUND"); pe.Details["track_id"] != "ghost" {
		t.Errorf("details = %v", pe.Details)
	}
	// window errors pass through unchanged
	_, err = e.svc.Create(ctx, human, duels.CreateInput{ATrackID: duelstest.TrackAlpha, BTrackID: duelstest.TrackBeta, FromS: sp(0.0), ToS: sp(10.0)})
	asProblem(t, err, 422, "CLIP_WINDOW_INVALID")
	// principals that can never judge cannot create
	for _, p := range []auth.Principal{auth.CLI, {Kind: auth.KindAnonymous}} {
		if _, err := e.svc.Create(ctx, p, duels.CreateInput{ATrackID: duelstest.TrackAlpha, BTrackID: duelstest.TrackBeta}); !errors.Is(err, duels.ErrNoSource) {
			t.Errorf("%v: err = %v", p.Kind, err)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM duels`); n != 4 {
		t.Errorf("rows = %d, want only the fixture's 4", n)
	}
}

func TestCreateDuel_BlindSlotsRandomisedAndLogged(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		coin    bool
		blind   *bool
		swapped bool
		calls   int
	}{
		{"coin true swaps", true, nil, true, 1},
		{"coin false keeps", false, sp(true), false, 1},
		{"non-blind never flips", true, sp(false), false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			e := newEnv(t, func(o *duels.Options) { o.Rand = func() bool { calls++; return tc.coin } })
			d, err := e.svc.Create(context.Background(), human, duels.CreateInput{ATrackID: duelstest.TrackAlpha, BTrackID: duelstest.TrackBeta, Blind: tc.blind})
			if err != nil {
				t.Fatal(err)
			}
			a, b := e.slots(t, d.ID)
			wantA, wantB := duelstest.TrackAlpha, duelstest.TrackBeta
			if tc.swapped {
				wantA, wantB = wantB, wantA
			}
			if a != wantA || b != wantB {
				t.Errorf("slots = %s,%s", a, b)
			}
			if got := e.count(t, `SELECT swapped FROM duels WHERE id = ?`, d.ID); (got == 1) != tc.swapped {
				t.Errorf("swapped column = %d", got)
			}
			if calls != tc.calls {
				t.Errorf("Rand calls = %d, want %d", calls, tc.calls)
			}
		})
	}
}

func TestCreateDuel_LoudnessGains(t *testing.T) {
	t.Parallel()
	type row struct {
		match          string
		ga, gb         int
		target         *float64
		lufsA, lufsB   *float64
		measure, ensur int
	}
	run := func(t *testing.T, la, lb float64, match *bool) (row, *env) {
		e := newEnv(t)
		// Master shas differ per fixture track: Alpha is slot A (coin false)
		e.rend.SetMeasure(e.sha(t, duelstest.TrackAlpha), clip.Measure{IntegratedLUFS: la, TruePeakDBTP: -1})
		e.rend.SetMeasure(e.sha(t, duelstest.TrackBeta), clip.Measure{IntegratedLUFS: lb, TruePeakDBTP: -2})
		d, err := e.svc.Create(context.Background(), human, duels.CreateInput{ATrackID: duelstest.TrackAlpha, BTrackID: duelstest.TrackBeta, MatchLoudness: match})
		if err != nil {
			t.Fatal(err)
		}
		var r row
		var tl, wa, wb *float64
		if err := e.st.R.QueryRow(`SELECT match, gain_a_cdb, gain_b_cdb, target_lufs, window_lufs_a, window_lufs_b FROM duels WHERE id = ?`, d.ID).
			Scan(&r.match, &r.ga, &r.gb, &tl, &wa, &wb); err != nil {
			t.Fatal(err)
		}
		r.target, r.lufsA, r.lufsB = tl, wa, wb
		r.measure, r.ensur = e.rend.Calls()
		return r, e
	}
	t.Run("matched", func(t *testing.T) {
		t.Parallel()
		r, e := run(t, -10, -20, nil)
		if r.match != "matched" || r.ga != -1000 || r.gb != 0 || r.target == nil || *r.target != -20 || *r.lufsA != -10 || *r.lufsB != -20 {
			t.Errorf("row = %+v", r)
		}
		if r.measure != 2 || r.ensur != 2 {
			t.Errorf("calls measure %d ensure %d", r.measure, r.ensur)
		}
		gains := map[int]bool{}
		for _, sp := range e.rend.specs() {
			gains[sp.GainCDB] = true
		}
		if !gains[-1000] || !gains[0] || len(e.rend.specs()) != 2 {
			t.Errorf("ensured gains = %v", e.rend.specs())
		}
	})
	t.Run("window silent", func(t *testing.T) {
		t.Parallel()
		r, _ := run(t, -70, -20, nil)
		if r.match != "window_silent" || r.ga != 0 || r.gb != 0 || r.target != nil {
			t.Errorf("row = %+v", r)
		}
	})
	t.Run("off", func(t *testing.T) {
		t.Parallel()
		r, _ := run(t, -10, -20, sp(false))
		if r.match != "off" || r.ga != 0 || r.gb != 0 || r.measure != 0 || r.ensur != 2 || r.lufsA != nil || r.target != nil {
			t.Errorf("row = %+v", r)
		}
	})
	t.Run("limited", func(t *testing.T) {
		t.Parallel()
		r, _ := run(t, -5, -45, nil)
		if r.match != "limited" || r.ga != clip.MaxCutCDB {
			t.Errorf("row = %+v", r)
		}
	})
}

func (e *env) sha(t *testing.T, track string) string {
	t.Helper()
	m, err := e.lib.TrackRefs(context.Background(), []string{track})
	if err != nil || len(m) != 1 {
		t.Fatal(err)
	}
	return m[track].MasterSHA256
}

func TestCreateDuel_RenderFailureLeavesNoRow(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	boom := errors.New("render boom")
	e.rend.EnsureErr = boom
	_, err := e.svc.Create(context.Background(), human, duels.CreateInput{ATrackID: duelstest.TrackAlpha, BTrackID: duelstest.TrackBeta})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM duels`); n != 4 {
		t.Errorf("rows = %d", n)
	}
}

func TestCreateDuel_IDsAndPrincipal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, tc := range []struct {
		p    auth.Principal
		kind string
	}{{human, "human"}, {device, "device"}, {agent, "agent"}} {
		d := e.create(t, tc.p, duelstest.TrackAlpha, duelstest.TrackBeta, true)
		var by, kind, tool, def, exp, created string
		if err := e.st.R.QueryRow(`SELECT created_by, created_by_kind, tool_version, clip_def, expires_at, created_at FROM duels WHERE id = ?`, d.ID).
			Scan(&by, &kind, &tool, &def, &exp, &created); err != nil {
			t.Fatal(err)
		}
		if by != tc.p.Key() || kind != tc.kind || tool != cliptest.Tool || def != clip.Def {
			t.Errorf("%s: row = %q %q %q %q", tc.kind, by, kind, tool, def)
		}
		if d.CreatedByKind != tc.kind || d.ID == "" || d.ID[:20] != "01900000-0000-7000-8" {
			t.Errorf("view = %+v", d)
		}
		c, _ := time.Parse(time.RFC3339, created)
		x, _ := time.Parse(time.RFC3339, exp)
		if x.Sub(c) != 30*24*time.Hour || !c.Equal(e.now) {
			t.Errorf("created %v expires %v", c, x)
		}
	}
	// the creator of a blind duel sees it unrevealed, with working clip links
	d := e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true)
	if d.Revealed || d.Slots.A.Track != nil || d.Slots.A.Clip.URL == "" || d.Slots.B.Clip.URL == "" {
		t.Errorf("view = %+v", d)
	}
	_ = lt.FixedNow
}

func TestCreateDuel_OpenLimit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	in := duels.CreateInput{ATrackID: duelstest.TrackAlpha, BTrackID: duelstest.TrackBeta}
	var err error
	for i := 0; i < duels.MaxOpenDuels+2 && err == nil; i++ {
		_, err = e.svc.Create(ctx, human, in)
	}
	asProblem(t, err, 409, "DUEL_LIMIT_REACHED")
}
