package config

import (
	"strings"
	"testing"
)

func TestConfig_Ears(t *testing.T) {
	t.Parallel()
	c, err := Load(env(map[string]string{"MM_AUTH": "local"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.EarsLoadS != 40 || c.EarsRTFMilli != 150 || c.EarsTranscribeRTFMilli != 40 || c.AnalyzeFetchS != 20 {
		t.Errorf("defaults = %d %d %d %d, want 40 150 40 20", c.EarsLoadS, c.EarsRTFMilli, c.EarsTranscribeRTFMilli, c.AnalyzeFetchS)
	}
	c, err = Load(env(map[string]string{"MM_AUTH": "local", "MM_EARS_LOAD_S": "0", "MM_EARS_RTF_MILLI": "2000", "MM_EARS_TRANSCRIBE_RTF_MILLI": "0", "MM_ANALYZE_FETCH_S": "300"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.EarsLoadS != 0 || c.EarsRTFMilli != 2000 || c.EarsTranscribeRTFMilli != 0 || c.AnalyzeFetchS != 300 {
		t.Errorf("bounds = %d %d %d %d", c.EarsLoadS, c.EarsRTFMilli, c.EarsTranscribeRTFMilli, c.AnalyzeFetchS)
	}
	for _, bad := range []struct{ name, val string }{
		{"MM_EARS_LOAD_S", "601"}, {"MM_EARS_LOAD_S", "-1"}, {"MM_EARS_RTF_MILLI", "9"}, {"MM_EARS_RTF_MILLI", "2001"},
		{"MM_EARS_TRANSCRIBE_RTF_MILLI", "2001"}, {"MM_ANALYZE_FETCH_S", "4"}, {"MM_ANALYZE_FETCH_S", "301"}, {"MM_ANALYZE_FETCH_S", "x1x"},
	} {
		_, err := Load(env(map[string]string{"MM_AUTH": "local", bad.name: bad.val}))
		if err == nil || !strings.Contains(err.Error(), bad.name) {
			t.Errorf("%s=%s: err = %v, want an error naming the variable", bad.name, bad.val, err)
		}
		if err != nil && strings.Contains(err.Error(), "="+bad.val) {
			t.Errorf("%s=%s: the error echoes the value: %v", bad.name, bad.val, err)
		}
	}
}
