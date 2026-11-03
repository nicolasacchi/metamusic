package generation_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// codecless is an engine that offers no RecipeCodec: the planner cannot parse or derive its recipes.
type codecless struct{}

func (codecless) Name() string                                      { return "codecless" }
func (codecless) Caps() engine.Caps                                 { return engine.Caps{Text2Song: true} }
func (codecless) Validate(json.RawMessage) []engine.Issue           { return nil }
func (codecless) ExpectedAudioSeconds(json.RawMessage) (int, error) { return 60, nil }
func (codecless) Artifacts(json.RawMessage) ([]string, []string)    { return []string{"audio.flac"}, nil }

func TestPlan_UnknownEngineRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	opts := r.opts
	opts.Engines = engine.Registry{"codecless": codecless{}}
	svc := generation.New(opts)
	luna := lt.TrackLuna
	before := r.count(`SELECT COUNT(*) FROM estimates`)
	_, err := svc.Estimate(context.Background(), agent, generation.EstimateIn{Engine: "codecless", Via: viaFor(agent),
		Items: []generation.Item{newSeedItem(luna, 1)}})
	pe := wantProblem(t, err, 422, "VALIDATION_FAILED")
	if pe.Details["field"] != "engine" {
		t.Errorf("details = %v, want field engine", pe.Details)
	}
	if n := r.count(`SELECT COUNT(*) FROM estimates`); n != before {
		t.Errorf("a refused estimate wrote %d estimate rows", n-before)
	}
}
