package config

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestConfig_T1: the defaults of C200, every out-of-range value rejected naming the variable (never a value), and the
// MM_SOUNDFONTS parsing table.
func TestConfig_T1(t *testing.T) {
	t.Parallel()
	c, err := Load(env(map[string]string{"MM_AUTH": "local"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.TransformEnabled || c.TransformQueueMax != 6 || c.TransformMaxCPUS != 600 || c.TransformMaxOutS != 900 ||
		c.TransformMaxMemMB != 1024 || c.TransformAgentDailyCPUS != 1800 || c.TransformHumanDailyCPUS != 14400 ||
		len(c.Soundfonts) != 0 || c.FluidsynthPath != "" {
		t.Fatalf("defaults: %+v", c)
	}
	ok := map[string]string{
		"MM_TRANSFORM_ENABLED": "false", "MM_TRANSFORM_QUEUE_MAX": "32", "MM_TRANSFORM_MAX_CPU_S": "3600", "MM_TRANSFORM_MAX_OUT_S": "60",
		"MM_TRANSFORM_MAX_MEM_MB": "256", "MM_TRANSFORM_AGENT_DAILY_CPU_S": "0", "MM_TRANSFORM_HUMAN_DAILY_CPU_S": "86400",
		"MM_FLUIDSYNTH": "/usr/bin/fluidsynth",
	}
	ok["MM_AUTH"] = "local"
	c, err = Load(env(ok))
	if err != nil {
		t.Fatal(err)
	}
	if c.TransformEnabled || c.TransformQueueMax != 32 || c.TransformMaxCPUS != 3600 || c.TransformMaxOutS != 60 ||
		c.TransformMaxMemMB != 256 || c.TransformAgentDailyCPUS != 0 || c.TransformHumanDailyCPUS != 86400 || c.FluidsynthPath != "/usr/bin/fluidsynth" {
		t.Fatalf("overrides: %+v", c)
	}
	for _, tc := range []struct{ name, val string }{
		{"MM_TRANSFORM_ENABLED", "perhaps"},
		{"MM_TRANSFORM_QUEUE_MAX", "0"}, {"MM_TRANSFORM_QUEUE_MAX", "33"},
		{"MM_TRANSFORM_MAX_CPU_S", "29"}, {"MM_TRANSFORM_MAX_CPU_S", "3601"},
		{"MM_TRANSFORM_MAX_OUT_S", "59"}, {"MM_TRANSFORM_MAX_OUT_S", "3601"},
		{"MM_TRANSFORM_MAX_MEM_MB", "255"}, {"MM_TRANSFORM_MAX_MEM_MB", "8193"},
		{"MM_TRANSFORM_AGENT_DAILY_CPU_S", "-1"}, {"MM_TRANSFORM_AGENT_DAILY_CPU_S", "86401"},
		{"MM_TRANSFORM_HUMAN_DAILY_CPU_S", "-1"}, {"MM_TRANSFORM_HUMAN_DAILY_CPU_S", "86401"},
		{"MM_FLUIDSYNTH", "fluidsynth"},
	} {
		_, err := Load(env(map[string]string{"MM_AUTH": "local", tc.name: tc.val}))
		if err == nil || !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%s=%q: err=%v, want an error naming the variable", tc.name, tc.val, err)
		} else if strings.Contains(err.Error(), tc.val) && len(tc.val) > 3 {
			t.Errorf("%s: the error echoes the value: %v", tc.name, err)
		}
	}
}

func TestConfig_T1Soundfonts(t *testing.T) {
	t.Parallel()
	many := make([]string, 0, 9)
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		many = append(many, n+"=/sf/"+n+".sf2")
	}
	for _, tc := range []struct {
		name string
		val  string
		want []Soundfont
		bad  bool
	}{
		{"one", "orchestra=/sf/o.sf2", []Soundfont{{Name: "orchestra", Path: "/sf/o.sf2"}}, false},
		{"two", "piano=/sf/p.sf3;strings_1=/sf/s.sf2", []Soundfont{{Name: "piano", Path: "/sf/p.sf3"}, {Name: "strings_1", Path: "/sf/s.sf2"}}, false},
		{"spaces trimmed", " a=/x/a.sf2 ; b=/x/b.sf2 ", []Soundfont{{Name: "a", Path: "/x/a.sf2"}, {Name: "b", Path: "/x/b.sf2"}}, false},
		{"empty", "", nil, false},
		{"eight", strings.Join(many[:8], ";"), nil, false},
		{"nine", strings.Join(many, ";"), nil, true},
		{"relative path", "a=sf/a.sf2", nil, true},
		{"no equals", "/sf/a.sf2", nil, true},
		{"bad name upper", "Orchestra=/sf/o.sf2", nil, true},
		{"bad name digit", "1a=/sf/o.sf2", nil, true},
		{"name too long", strings.Repeat("a", 33) + "=/sf/o.sf2", nil, true},
		{"duplicate", "a=/sf/a.sf2;a=/sf/b.sf2", nil, true},
		{"bad extension", "a=/sf/a.wav", nil, true},
		{"dot dot", "a=/sf/../a.sf2", nil, true},
		{"empty entry", "a=/sf/a.sf2;;b=/sf/b.sf2", nil, true},
	} {
		c, err := Load(env(map[string]string{"MM_AUTH": "local", "MM_SOUNDFONTS": tc.val}))
		if tc.bad {
			if err == nil || !strings.Contains(err.Error(), "MM_SOUNDFONTS") {
				t.Errorf("%s: err=%v, want an error naming MM_SOUNDFONTS", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if tc.name == "eight" {
			if len(c.Soundfonts) != 8 {
				t.Errorf("eight: %d entries", len(c.Soundfonts))
			}
			continue
		}
		if diff := cmp.Diff(tc.want, c.Soundfonts); diff != "" {
			t.Errorf("%s: %s", tc.name, diff)
		}
	}
}
