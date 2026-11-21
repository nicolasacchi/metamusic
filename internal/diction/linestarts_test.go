package diction_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/library"
)

const startsLyrics = "[Verse]\nla chiave del mare\nsotto la luna chiara\n[Instrumental]\n[Chorus]\nnoi balliamo piano"

func TestLineStarts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()

	// no row: not ok (the proportional basis never counts, D-EARS-16), even with timed sections
	bare := e.addTake(startsLyrics)
	e.addTimed(bare, "plan", 0, 5, 25)
	if got, ok, err := e.svc.LineStarts(ctx, alice, bare); err != nil || ok || got != nil {
		t.Errorf("no tier B row: got %v %v %v", got, ok, err)
	}

	id := e.addTake(startsLyrics)
	e.addDiction(id, startsLyrics, tierbtest.Opts{})
	got, ok, err := e.svc.LineStarts(ctx, alice, id)
	if err != nil || !ok || len(got) != 3 {
		t.Fatalf("got %v %v %v", got, ok, err)
	}
	for i, d := range got {
		if d < 9*time.Second || (i > 0 && d <= got[i-1]) || d%time.Millisecond != 0 {
			t.Errorf("start %d = %v", i, d)
		}
	}

	// a line whose words are all unheard has no measured time
	drop := e.addTake(startsLyrics)
	e.addDiction(drop, startsLyrics, tierbtest.Opts{Drops: map[string]bool{"noi": true, "balliamo": true, "piano": true}})
	got, ok, err = e.svc.LineStarts(ctx, alice, drop)
	if err != nil || !ok || len(got) != 3 || got[0] < 0 || got[2] >= 0 {
		t.Errorf("unheard line: %v %v %v", got, ok, err)
	}

	if _, _, err := e.svc.LineStarts(ctx, alice, "no-such"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}

	// a viewer that hides discarded takes gets not found, and sees nothing of its timings
	e.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, id)
	bob := auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "bob", Name: "bob", User: "bob"}
	if got, ok, err := e.svc.LineStarts(ctx, bob, id); !errors.Is(err, library.ErrNotFound) || ok || got != nil {
		t.Errorf("listener on a discarded take: %v %v %v", got, ok, err)
	}
	if _, ok, err := e.svc.LineStarts(ctx, alice, id); err != nil || !ok {
		t.Errorf("owner on a discarded take: %v %v", ok, err)
	}
}
