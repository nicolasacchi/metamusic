package app

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earsjobs"
)

// TestWireEars_AddsTakeHookAndSweepLoop: the Ears build hook appends the Tier B take hook and the sweep loop, and does nothing
// without an Ears service.
func TestWireEars_AddsTakeHookAndSweepLoop(t *testing.T) {
	t.Parallel()
	env := &BuildEnv{Services: &Services{Ears: ears.New(ears.Options{})}, late: &m5Late{}}
	if err := wireEars(env); err != nil {
		t.Fatal(err)
	}
	if n := len(env.late.hook.extra); n != 1 {
		t.Fatalf("take hooks = %d, want 1", n)
	}
	if _, ok := env.late.hook.extra[0].(earsjobs.TierBHook); !ok {
		t.Errorf("take hook is %T, want earsjobs.TierBHook", env.late.hook.extra[0])
	}
	if loops := env.Services.Loops; len(loops) != 1 || loops[0].Name != "ears-tierb-sweep" || loops[0].Run == nil {
		t.Errorf("loops = %+v", loops)
	}
	empty := &BuildEnv{Services: &Services{}, late: &m5Late{}}
	if err := wireEars(empty); err != nil || len(empty.late.hook.extra) != 0 || len(empty.Services.Loops) != 0 {
		t.Errorf("without Ears: %v, %d hooks, %d loops", err, len(empty.late.hook.extra), len(empty.Services.Loops))
	}
}

// TestEarsOptions: the config values reach the quote seam, a Config built by hand (all zero) gets the defaults, and the engine's
// language and line readers are wired.
func TestEarsOptions(t *testing.T) {
	t.Parallel()
	o := earsOptions(config.Config{EarsLoadS: 7, EarsRTFMilli: 90, EarsTranscribeRTFMilli: 3})
	if o.LoadS != 7 || o.RTFMilli != 90 || o.TranscribeRTFMilli != 3 {
		t.Errorf("configured = %+v", o)
	}
	d := earsOptions(config.Config{})
	if d.LoadS != 40 || d.RTFMilli != 150 || d.TranscribeRTFMilli != 40 {
		t.Errorf("defaults = %+v", d)
	}
	if n := d.LyricLines("[Verse]\none\ntwo\n\n[Chorus]\nthree"); n != 3 {
		t.Errorf("lines = %d", n)
	}
	if got := d.Language("[Verse]\nla chiave del mare non è nella casa\nsotto il cielo di sera che non ha una luna"); got != "it" {
		t.Errorf("language = %q", got)
	}
}
