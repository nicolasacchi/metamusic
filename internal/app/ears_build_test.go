package app_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/app"
)

// TestWireEars_RegisteredAtInit: a real Build carries the Tier B sweep loop (the build hook is registered at init).
func TestWireEars_RegisteredAtInit(t *testing.T) {
	t.Parallel()
	s, _ := build(t, app.Options{})
	for _, l := range s.Loops {
		if l.Name == "ears-tierb-sweep" {
			return
		}
	}
	t.Errorf("Build did not register the sweep loop: %+v", s.Loops)
}
