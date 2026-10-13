package clip_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func f(v float64) *float64 { return &v }

func TestResolveWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		dur      float64
		from, to *float64
		want     clip.Window
		wantErr  bool
	}{
		{"default long", 240, nil, nil, clip.Window{StartMS: 72000, EndMS: 117000}, false},
		{"default 40 s is the whole track", 40, nil, nil, clip.Window{StartMS: 0, EndMS: 40000}, false},
		{"default 6 s", 6, nil, nil, clip.Window{StartMS: 0, EndMS: 6000}, false},
		{"6 s track, explicit whole", 6, f(0), f(6), clip.Window{StartMS: 0, EndMS: 6000}, false},
		{"6 s track, any other length", 6, f(0), f(5), clip.Window{}, true},
		{"rounds to the 100 ms grid", 240, f(10.04), f(40.06), clip.Window{StartMS: 10000, EndMS: 40100}, false},
		{"29.9 s is too short", 240, f(10), f(39.9), clip.Window{}, true},
		{"45.1 s is too long", 240, f(10), f(55.1), clip.Window{}, true},
		{"45.0 s is allowed", 240, f(10), f(55), clip.Window{StartMS: 10000, EndMS: 55000}, false},
		{"30.0 s is allowed", 240, f(10), f(40), clip.Window{StartMS: 10000, EndMS: 40000}, false},
		{"to beyond the track", 240, f(220), f(250), clip.Window{}, true},
		{"huge from 1e18", 100, f(1e18), nil, clip.Window{}, true},
		{"huge from 1e19", 100, f(1e19), f(40), clip.Window{}, true},
		{"huge from 1e30", 100, f(1e30), nil, clip.Window{}, true},
		{"huge to 1e19", 240, f(10), f(1e19), clip.Window{}, true},
		{"negative from", 240, f(-1), f(40), clip.Window{}, true},
		{"to omitted runs to the end", 240, f(200), nil, clip.Window{StartMS: 200000, EndMS: 240000}, false},
		{"to omitted, capped at 45 s", 240, f(10), nil, clip.Window{StartMS: 10000, EndMS: 55000}, false},
		{"to omitted leaves a too-short tail", 240, f(230), nil, clip.Window{}, true},
		{"to before from", 240, f(50), f(20), clip.Window{}, true},
		{"from only on a short track", 40, f(5), nil, clip.Window{StartMS: 5000, EndMS: 40000}, false},
		{"from only leaves too short a tail", 40, f(15), nil, clip.Window{}, true},
		{"empty track", 0, nil, nil, clip.Window{}, true},
		{"floor to 100 ms", 61.39, nil, nil, clip.Window{StartMS: 16300, EndMS: 61300}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := clip.ResolveWindow(tc.dur, tc.from, tc.to)
			if tc.wantErr {
				var pe *problem.Error
				if !errors.As(err, &pe) || pe.Status != 422 || pe.Code != "CLIP_WINDOW_INVALID" {
					t.Fatalf("err = %v, want 422 CLIP_WINDOW_INVALID", err)
				}
				for _, k := range []string{"duration_s", "min_s", "max_s"} {
					if _, ok := pe.Details[k]; !ok {
						t.Errorf("details %v lack %s", pe.Details, k)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("window = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestResolveWindow_ErrorDetails(t *testing.T) {
	t.Parallel()
	_, err := clip.ResolveWindow(240, f(10), f(39.9))
	var pe *problem.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v", err)
	}
	want := map[string]any{"duration_s": 240.0, "min_s": 30.0, "max_s": 45.0}
	if diff := cmp.Diff(want, pe.Details); diff != "" {
		t.Errorf("details (-want +got):\n%s", diff)
	}
}

func TestWindow_DurationS(t *testing.T) {
	t.Parallel()
	if got := (clip.Window{StartMS: 1000, EndMS: 31500}).DurationS(); got != 30.5 {
		t.Errorf("DurationS = %v", got)
	}
}

func TestDefaultWindow(t *testing.T) {
	t.Parallel()
	if got := clip.DefaultWindow(240); got != (clip.Window{StartMS: 72000, EndMS: 117000}) {
		t.Errorf("default(240) = %+v", got)
	}
	// near the end the start is pulled back so the clip stays inside the track
	if got := clip.DefaultWindow(50); got != (clip.Window{StartMS: 5000, EndMS: 50000}) {
		t.Errorf("default(50) = %+v", got)
	}
}
