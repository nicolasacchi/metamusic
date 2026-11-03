package clip

import (
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

func TestResolveSectionWindows_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		a, b       SectionSpan
		durA, durB float64
		wa, wb     Window
		wantErr    bool
	}{
		{"equal sections, pre-roll", SectionSpan{40, 70}, SectionSpan{50, 80}, 200, 200, Window{38000, 70000}, Window{48000, 80000}, false},
		{"length follows the shorter section", SectionSpan{40, 60}, SectionSpan{40, 90}, 200, 200, Window{38000, 60000}, Window{38000, 60000}, false},
		{"short sections get the 10 s floor", SectionSpan{20, 24}, SectionSpan{30, 33}, 100, 100, Window{18000, 28000}, Window{28000, 38000}, false},
		{"long sections are capped at 45 s", SectionSpan{10, 100}, SectionSpan{10, 100}, 200, 200, Window{8000, 53000}, Window{8000, 53000}, false},
		{"pre-roll clamps at the start", SectionSpan{1, 31}, SectionSpan{0, 40}, 100, 100, Window{0, 32000}, Window{0, 32000}, false},
		{"shifted back at the end of the track", SectionSpan{96, 98}, SectionSpan{0, 5}, 100, 100, Window{90000, 100000}, Window{0, 10000}, false},
		{"the shorter track bounds the length", SectionSpan{0, 40}, SectionSpan{0, 40}, 100, 20, Window{0, 20000}, Window{0, 20000}, false},
		{"grid rounding", SectionSpan{40.06, 70.06}, SectionSpan{50.04, 80.04}, 200, 200, Window{38100, 70100}, Window{48000, 80000}, false},
		{"track shorter than 10 s", SectionSpan{0, 5}, SectionSpan{0, 5}, 8, 8, Window{}, Window{}, true},
		{"empty section", SectionSpan{10, 10}, SectionSpan{0, 30}, 100, 100, Window{}, Window{}, true},
		{"reversed section", SectionSpan{30, 10}, SectionSpan{0, 30}, 100, 100, Window{}, Window{}, true},
		{"section beyond the track", SectionSpan{300, 330}, SectionSpan{0, 30}, 100, 100, Window{}, Window{}, true},
		{"no audio", SectionSpan{0, 30}, SectionSpan{0, 30}, 0, 100, Window{}, Window{}, true},
	} {
		wa, wb, err := ResolveSectionWindows(tc.a, tc.b, tc.durA, tc.durB)
		if tc.wantErr {
			var pe *problem.Error
			if err == nil || !errors.As(err, &pe) || pe.Code != "CLIP_WINDOW_INVALID" {
				t.Errorf("%s: err = %v, want CLIP_WINDOW_INVALID", tc.name, err)
			}
			continue
		}
		if err != nil || wa != tc.wa || wb != tc.wb {
			t.Errorf("%s: got %+v %+v (%v), want %+v %+v", tc.name, wa, wb, err, tc.wa, tc.wb)
		}
		if wa.EndMS-wa.StartMS != wb.EndMS-wb.StartMS {
			t.Errorf("%s: unequal lengths", tc.name)
		}
	}
}
