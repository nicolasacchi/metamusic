package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestConfigCheck_PrintsTZ(t *testing.T) {
	env, _ := configEnv(t, map[string]string{"MM_TZ": "Europe/Rome"})
	code, out, errs := do(t, env, "", "config", "check")
	if code != 0 || !strings.Contains(out, "time zone: Europe/Rome\n") {
		t.Fatalf("exit %d, want time zone line\nstdout: %s\nstderr: %s", code, out, errs)
	}
	env, _ = configEnv(t, nil)
	if code, out, _ = do(t, env, "", "config", "check"); code != 0 || !strings.Contains(out, "time zone: Local\n") {
		t.Fatalf("default: exit %d, stdout %s", code, out)
	}
	env, _ = configEnv(t, map[string]string{"MM_TZ": "Mars/Olympus"})
	if code, _, errs = do(t, env, "", "config", "check"); code == 0 || !strings.Contains(errs, "MM_TZ") {
		t.Fatalf("bad zone: exit %d, stderr %s", code, errs)
	}
}

// TestTZData_Embedded: the CGO_ENABLED=0 binary carries the zone database, so MM_TZ resolves on an image without zoneinfo.
func TestTZData_Embedded(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains("\n"+string(out), "\ntime/tzdata\n") {
		t.Fatal("cmd/metamusic does not link time/tzdata")
	}
}
