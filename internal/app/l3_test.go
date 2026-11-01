package app_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/app"
)

// TestApp_L3Wiring: Build always constructs the L3 services (the operations answer 501 until their lanes land, never 503) and
// shares them between Services, the API dependencies and the HTTP options.
func TestApp_L3Wiring(t *testing.T) {
	t.Parallel()
	none := ""
	for name, o := range map[string]app.Options{
		"rig defaults":  {L2: app.L2Options{FlacVersion: &none}},
		"real features": {L2: app.L2Options{FlacVersion: &none}, L3: app.L3Options{RealFeatures: true}},
	} {
		s := buildWith(t, nil, o)
		if s.Features == nil || s.Sequence == nil || s.NextActions == nil || s.Atlas == nil {
			t.Fatalf("%s: an L3 service is missing: %+v", name, s)
		}
		d, h := s.Deps, s.Server
		if d.Features != s.Features || d.Sequence != s.Sequence || d.NextActions != s.NextActions || d.Atlas != s.Atlas {
			t.Errorf("%s: api.Deps must carry the services Build built", name)
		}
		if h.Features != s.Features || h.Sequence != s.Sequence || h.NextActions != s.NextActions || h.Atlas != s.Atlas {
			t.Errorf("%s: httpserver.Options must carry the services Build built", name)
		}
	}
}
