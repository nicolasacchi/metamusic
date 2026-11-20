package config

import (
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

func TestConfig_TZ(t *testing.T) {
	t.Parallel()
	c, err := Load(env(map[string]string{"MM_AUTH": "local"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Loc != nil || c.Zone() != time.Local {
		t.Errorf("default Loc = %v, want the process zone", c.Loc)
	}
	c, err = Load(env(map[string]string{"MM_AUTH": "local", "MM_TZ": "Europe/Rome"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Loc == nil || c.Loc.String() != "Europe/Rome" {
		t.Errorf("Loc = %v, want Europe/Rome", c.Loc)
	}
	for _, bad := range []string{"Mars/Olympus", "Local", "../etc/passwd", "rome"} {
		_, err := Load(env(map[string]string{"MM_AUTH": "local", "MM_TZ": bad}))
		if err == nil || !strings.Contains(err.Error(), "MM_TZ") {
			t.Errorf("MM_TZ=%q: err = %v, want an error naming MM_TZ", bad, err)
		}
		if err != nil && strings.Contains(err.Error(), bad) {
			t.Errorf("MM_TZ=%q: the error echoes the value: %v", bad, err)
		}
	}
}
