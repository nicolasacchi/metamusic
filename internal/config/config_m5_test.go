package config

import "testing"

func TestConfig_M5CallbackBacklog(t *testing.T) {
	t.Parallel()
	c, err := Load(env(map[string]string{"MM_AUTH": "local"}))
	if err != nil || c.CallbackBacklogMax != 6 {
		t.Fatalf("default = %d (%v), want 6", c.CallbackBacklogMax, err)
	}
	for _, tc := range []struct {
		name, v string
		want    int
		bad     bool
	}{
		{"off", "0", 0, false}, {"one", "1", 1, false}, {"max", "50", 50, false},
		{"over", "51", 0, true}, {"negative", "-1", 0, true}, {"junk", "many", 0, true},
	} {
		got, err := Load(env(map[string]string{"MM_AUTH": "local", "MM_CALLBACK_BACKLOG_MAX": tc.v}))
		if tc.bad {
			if err == nil {
				t.Errorf("%s: accepted %q", tc.name, tc.v)
			}
			continue
		}
		if err != nil || got.CallbackBacklogMax != tc.want {
			t.Errorf("%s: got %d (%v), want %d", tc.name, got.CallbackBacklogMax, err, tc.want)
		}
	}
}
