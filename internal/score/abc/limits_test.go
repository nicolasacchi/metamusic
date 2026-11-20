package abc_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/score"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// The score packages repeat the transform limits (they may not import transform); this pins the copies.
func TestLimits_MatchTransform(t *testing.T) {
	t.Parallel()
	if score.MaxEvents != transform.MaxScoreEvents || abc.MaxBytes != transform.MaxScoreBytes {
		t.Fatalf("limits diverged: events %d/%d bytes %d/%d", score.MaxEvents, transform.MaxScoreEvents, abc.MaxBytes, transform.MaxScoreBytes)
	}
}
