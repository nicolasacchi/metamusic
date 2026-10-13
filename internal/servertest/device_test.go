package servertest_test

import (
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func TestRig_Device(t *testing.T) {
	for _, mode := range []string{"proxy", "local"} {
		t.Run(mode, func(t *testing.T) {
			r := servertest.New(t, servertest.Options{Mode: mode})
			d := r.Device(t)
			resp := d.Do(t, "GET", "/api/me", nil)
			if resp.Status != 200 || !strings.Contains(string(resp.Body), `"kind":"device"`) {
				t.Fatalf("whoami as device: %d %s", resp.Status, resp.Body)
			}
		})
	}
}

func TestRig_LocalHumanStepsUp(t *testing.T) {
	r := servertest.New(t, servertest.Options{Mode: "local"})
	h := r.Human(t)
	if resp := h.Do(t, "GET", "/human/api/devices", nil); resp.Status != 200 {
		t.Fatalf("after step-up: %d %s", resp.Status, resp.Body)
	}
	r.Clock.Advance(10*time.Minute + time.Second)
	resp := h.Do(t, "GET", "/human/api/devices", nil)
	if resp.Status != 403 || !strings.Contains(string(resp.Body), "STEP_UP_REQUIRED") {
		t.Fatalf("stale step-up: %d %s", resp.Status, resp.Body)
	}
	r.StepUp(t, h)
	if resp := h.Do(t, "GET", "/human/api/devices", nil); resp.Status != 200 {
		t.Fatalf("after second step-up: %d", resp.Status)
	}
	// proxy mode: /auth/step-up does not exist
	p := servertest.New(t, servertest.Options{})
	if resp := p.Human(t).Do(t, "GET", "/auth/step-up", nil); resp.Status != 404 {
		t.Fatalf("proxy /auth/step-up: %d", resp.Status)
	}
}
