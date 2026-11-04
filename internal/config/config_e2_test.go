package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func e2env(kv ...string) func(string) string {
	m := map[string]string{"MM_AUTH": "local"}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return env(m)
}

func TestConfig_E2_Defaults(t *testing.T) {
	t.Parallel()
	c, err := Load(e2env())
	if err != nil {
		t.Fatal(err)
	}
	if c.ACEStep || c.ACEStepColdStartS != 300 || c.ACEStepMaxRuntime != c.JobMaxRuntime ||
		!reflect.DeepEqual(c.ACEStepTasks, []string{"turbo"}) || !reflect.DeepEqual(c.ACEStepPools, []string{"AMPERE_24", "ADA_24"}) {
		t.Errorf("defaults: %+v", c)
	}
	c, err = Load(e2env("MM_JOB_MAX_RUNTIME", "20m"))
	if err != nil || c.ACEStepMaxRuntime != 20*time.Minute {
		t.Errorf("max runtime follows MM_JOB_MAX_RUNTIME: %v (%v)", c.ACEStepMaxRuntime, err)
	}
}

// TestConfig_E2_Table: every range edge of the engine variables; errors name the variable and never a value.
func TestConfig_E2_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		kv   []string
		bad  string // variable named by the error; "" = accepted
	}{
		{"enable", []string{"MM_ACESTEP", "1", "MM_BACKEND", "fake"}, ""},
		{"enable junk", []string{"MM_ACESTEP", "yes", "MM_BACKEND", "fake"}, "MM_ACESTEP"},
		{"disable explicit", []string{"MM_ACESTEP", "0"}, ""},
		{"cold min", []string{"MM_ACESTEP_COLD_START_S", "30"}, ""},
		{"cold max", []string{"MM_ACESTEP_COLD_START_S", "900"}, ""},
		{"cold under", []string{"MM_ACESTEP_COLD_START_S", "29"}, "MM_ACESTEP_COLD_START_S"},
		{"cold over", []string{"MM_ACESTEP_COLD_START_S", "901"}, "MM_ACESTEP_COLD_START_S"},
		{"cold junk", []string{"MM_ACESTEP_COLD_START_S", "soon"}, "MM_ACESTEP_COLD_START_S"},
		{"runtime min", []string{"MM_ACESTEP_MAX_RUNTIME", "30s"}, ""},
		{"runtime max", []string{"MM_ACESTEP_MAX_RUNTIME", "45m"}, ""},
		{"runtime under", []string{"MM_ACESTEP_MAX_RUNTIME", "29s"}, "MM_ACESTEP_MAX_RUNTIME"},
		{"runtime over", []string{"MM_ACESTEP_MAX_RUNTIME", "46m"}, "MM_ACESTEP_MAX_RUNTIME"},
		{"tasks turbo", []string{"MM_ACESTEP_TASKS", "turbo"}, ""},
		{"tasks turbo,base", []string{"MM_ACESTEP_TASKS", "turbo,base"}, ""},
		{"tasks base only", []string{"MM_ACESTEP_TASKS", "base"}, "MM_ACESTEP_TASKS"},
		{"tasks unknown", []string{"MM_ACESTEP_TASKS", "turbo,sft"}, "MM_ACESTEP_TASKS"},
		{"pools one", []string{"MM_ACESTEP_POOLS", "AMPERE_48"}, ""},
		{"pools two", []string{"MM_ACESTEP_POOLS", "AMPERE_24, ADA_24"}, ""},
		{"pools lower", []string{"MM_ACESTEP_POOLS", "ampere_24"}, "MM_ACESTEP_POOLS"},
		{"pools short", []string{"MM_ACESTEP_POOLS", "A"}, "MM_ACESTEP_POOLS"},
		{"pools empty entry", []string{"MM_ACESTEP_POOLS", "ADA_24,,"}, ""},
		{"pools only separators", []string{"MM_ACESTEP_POOLS", ",,"}, "MM_ACESTEP_POOLS"},
		{"pools duplicate", []string{"MM_ACESTEP_POOLS", "ADA_24,ADA_24"}, "MM_ACESTEP_POOLS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(e2env(tc.kv...))
			switch {
			case tc.bad == "" && err != nil:
				t.Errorf("rejected: %v", err)
			case tc.bad != "" && err == nil:
				t.Error("accepted")
			case tc.bad != "":
				if !strings.Contains(err.Error(), tc.bad) {
					t.Errorf("error does not name %s: %v", tc.bad, err)
				}
				if v := tc.kv[len(tc.kv)-1]; strings.Contains(err.Error(), v) && len(v) > 4 {
					t.Errorf("error echoes the value %q: %v", v, err)
				}
			}
		})
	}
}

func TestConfig_ACESTEPWithoutBackendRefused(t *testing.T) {
	t.Parallel()
	for _, b := range []string{"", "none"} {
		_, err := Load(e2env("MM_ACESTEP", "1", "MM_BACKEND", b))
		if err == nil || !strings.Contains(err.Error(), "MM_ACESTEP") || !strings.Contains(err.Error(), "MM_BACKEND") {
			t.Errorf("backend %q: %v", b, err)
		}
	}
	for _, b := range []string{"fake"} {
		if _, err := Load(e2env("MM_ACESTEP", "1", "MM_BACKEND", b)); err != nil {
			t.Errorf("backend %q: %v", b, err)
		}
	}
	// runpod needs an https public URL, nothing else; a missing ACE endpoint id is not a config error (keys are files)
	if _, err := Load(e2env("MM_ACESTEP", "1", "MM_BACKEND", "runpod", "MM_PUBLIC_URL", "https://mm.example.com")); err != nil {
		t.Errorf("runpod: %v", err)
	}
}

// TestConfig_ACESTEPTasksNormalised: the task list is a set; order and spaces do not matter and it is stored sorted-by-class.
func TestConfig_ACESTEPTasksNormalised(t *testing.T) {
	t.Parallel()
	c, err := Load(e2env("MM_ACESTEP_TASKS", " base , turbo"))
	if err != nil || !reflect.DeepEqual(c.ACEStepTasks, []string{"turbo", "base"}) {
		t.Errorf("%v (%v)", c.ACEStepTasks, err)
	}
}
