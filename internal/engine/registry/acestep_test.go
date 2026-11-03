package registry_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
)

func TestRegistry_ACEStepOnlyWhenEnabled(t *testing.T) {
	t.Parallel()
	if _, ok := registry.Default(registry.Options{})["acestep15"]; ok {
		t.Error("acestep15 registered with the flag off")
	}
	e, ok := registry.Default(registry.Options{ACEStep: true})["acestep15"]
	if !ok {
		t.Fatal("acestep15 not registered with the flag on")
	}
	if _, ok := engine.CodecOf(e); !ok || engine.ReportsTruncation(e) || engine.LearnsRTF(e) {
		t.Error("codec or ingest policy of the registered engine")
	}
	if _, ok := e.(engine.MinAudioPlanner); ok {
		t.Error("acestep15 must not plan a minimum audio length")
	}
	if c := e.Caps(); !c.Text2Song || c.RepaintInterval || c.Extend || c.Complete || c.Stems {
		t.Errorf("registered caps must be the verified text2music-only set: %+v", c)
	}
}
