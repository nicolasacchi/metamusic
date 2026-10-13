package api

import (
	"errors"
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library"
)

func problemOf(t *testing.T, err error) *Problem {
	t.Helper()
	var p *Problem
	if !errors.As(err, &p) {
		t.Fatalf("not a Problem: %v", err)
	}
	return p
}

func TestLibErr_RecipeUnknown(t *testing.T) {
	t.Parallel()
	p := problemOf(t, libErr(fmt.Errorf("wrapped: %w", &library.RecipeUnknownError{TrackID: "t1"})))
	if p.Status != 422 || p.Code != "RECIPE_UNKNOWN" || p.Details["track_id"] != "t1" {
		t.Errorf("%+v", p)
	}
	// the bare sentinel still maps (no track id known)
	p = problemOf(t, libErr(library.ErrRecipeUnknown))
	if p.Status != 422 || p.Code != "RECIPE_UNKNOWN" {
		t.Errorf("sentinel: %+v", p)
	}
}

func TestLibErr_LossyMaster(t *testing.T) {
	t.Parallel()
	p := problemOf(t, libErr(&library.LossyMasterError{TrackID: "t2"}))
	if p.Status != 422 || p.Code != "LOSSY_MASTER" || p.Details["track_id"] != "t2" {
		t.Errorf("%+v", p)
	}
	p = problemOf(t, libErr(library.ErrLossyMaster))
	if p.Status != 422 || p.Code != "LOSSY_MASTER" {
		t.Errorf("sentinel: %+v", p)
	}
}
