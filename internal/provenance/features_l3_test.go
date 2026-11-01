package provenance_test

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// recordingFeatures is a FeatureStep that reports fixed counts and remembers what phase B passed it.
type recordingFeatures struct {
	tracks, songs []string
	sweeps        int
}

func (r *recordingFeatures) ComputeTx(_ context.Context, _ *sql.Tx, _ store.Env, trackIDs, songIDs []string) (provenance.FeatureChanges, error) {
	r.tracks, r.songs = append(r.tracks, trackIDs...), append(r.songs, songIDs...)
	return provenance.FeatureChanges{Takes: 2, Songs: 3}, nil
}

func (r *recordingFeatures) SweepStaleTx(context.Context, *sql.Tx, store.Env, int) (int, error) {
	r.sweeps++
	return 0, nil
}

// TestProvenance_FeatureStepNop: without a step (the default) phase B computes no feature row and the report field stays
// zero; the report equals the one of a Reconciler with an explicit no-op, and the L1 tables are the same.
func TestProvenance_FeatureStepNop(t *testing.T) {
	t.Parallel()
	a := l1World(t)
	ra := a.settle()
	if ra.FeaturesComputed != 0 {
		t.Errorf("FeaturesComputed = %d with the default step, want 0", ra.FeaturesComputed)
	}
	var nop provenance.FeatureStep
	b := l1World(t, func(o *provenance.Options) { o.Features = nop })
	if rb := b.settle(); !reflect.DeepEqual(ra, rb) {
		t.Errorf("reports differ: %+v vs %+v", ra, rb)
	}
	if a.contentHash() != b.contentHash() {
		t.Error("the default step changed a table")
	}
}

// TestProvenance_FeatureStepRuns: phase B calls the step after sounds with the dirty tracks and the affected Songs and
// reports its counts; the sweep calls SweepStaleTx.
func TestProvenance_FeatureStepRuns(t *testing.T) {
	t.Parallel()
	rec := &recordingFeatures{}
	w := l1World(t, func(o *provenance.Options) { o.Features = rec })
	rep := w.settle()
	if len(rec.tracks) == 0 || len(rec.songs) == 0 {
		t.Fatalf("the step saw tracks %d and songs %d, want both non-empty", len(rec.tracks), len(rec.songs))
	}
	if rep.FeaturesComputed != 5*rep.Passes {
		t.Errorf("FeaturesComputed = %d over %d passes, want 5 per pass", rep.FeaturesComputed, rep.Passes)
	}
	if err := w.prov.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec.sweeps == 0 {
		t.Error("Sweep never called SweepStaleTx")
	}
	var report provenance.Report
	report.Add(provenance.Report{FeaturesComputed: 4})
	report.Add(provenance.Report{FeaturesComputed: 1})
	if report.FeaturesComputed != 5 {
		t.Errorf("Report.Add dropped FeaturesComputed: %d", report.FeaturesComputed)
	}
}
