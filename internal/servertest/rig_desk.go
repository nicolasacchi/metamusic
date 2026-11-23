package servertest

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// WithSoundfont configures T1b for the rig about to be built (C364.1): it writes the generated test SF2 (dsptest.WriteSF2, 128
// General MIDI programs, one looped sine; nothing is committed) into tb.TempDir() and names it "gm" in the options' soundfont set,
// with the real fluidsynth of testtools. The options need to be set before New (services read their configuration once), which is
// why this is a method of Options and not of the Rig the plan sketched (see the plan's DESK-X0 note).
//
// Without fluidsynth the test is skipped, unless MM_REQUIRE_FLUIDSYNTH=1 (CI), which makes its absence a failure.
func (o *Options) WithSoundfont(tb testing.TB) *Options {
	tb.Helper()
	bin := testtools.Tools().Fluidsynth
	if _, err := exec.LookPath(bin); err != nil {
		if os.Getenv("MM_REQUIRE_FLUIDSYNTH") == "1" {
			tb.Fatal("fluidsynth required (MM_REQUIRE_FLUIDSYNTH=1) but not found")
		}
		tb.Skip("media tool \"fluidsynth\" not found")
	}
	sf := filepath.Join(tb.TempDir(), "gm.sf2")
	if err := dsptest.WriteSF2(sf, dsptest.SF2Options{Presets: 128}); err != nil {
		tb.Fatal(err)
	}
	o.Soundfonts = []config.Soundfont{{Name: "gm", Path: sf}}
	o.FluidsynthPath = bin
	return o
}

// DeskPlanFromFixture opens the take's score on the Desk as the Owner (create_plan_from_take) and returns the plan id.
func (r *Rig) DeskPlanFromFixture(tb testing.TB, trackID string) string {
	tb.Helper()
	resp := r.Human(tb).Do(tb, "POST", "/api/tracks/"+trackID+"/plan", nil, "Idempotency-Key", "desk-fixture-"+trackID)
	if resp.Status != http.StatusCreated && resp.Status != http.StatusOK {
		tb.Fatalf("create_plan_from_take %s: %d %s", trackID, resp.Status, resp.Body)
	}
	m, _ := resp.JSON(tb).(map[string]any)
	id, _ := m["id"].(string)
	if id == "" {
		tb.Fatalf("create_plan_from_take %s: no plan id in %s", trackID, resp.Body)
	}
	return id
}
