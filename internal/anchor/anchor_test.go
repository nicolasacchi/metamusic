package anchor

import (
	"math"
	"testing"
)

func timing(src string, dur float64, secs ...Section) Timing {
	return Timing{Source: src, Sections: secs, DurationS: dur}
}

var songA = timing("plan", 100,
	Section{Kind: "intro", Ordinal: 1, Label: "intro", StartS: 0, EndS: 10},
	Section{Kind: "verse", Ordinal: 1, Label: "verse 1", StartS: 10, EndS: 40},
	Section{Kind: "chorus", Ordinal: 1, Label: "chorus 1", StartS: 40, EndS: 60},
	Section{Kind: "verse", Ordinal: 2, Label: "verse 2", StartS: 60, EndS: 80},
	Section{Kind: "chorus", Ordinal: 2, Label: "chorus 2", StartS: 80, EndS: 100},
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCompute_Table(t *testing.T) {
	t.Parallel()
	untimed := Timing{Source: "", DurationS: 200}
	lyricsOnly := Timing{Source: "", Sections: nil, DurationS: 0}
	for _, tc := range []struct {
		name     string
		tm       Timing
		at       float64
		basis    string
		kind     string
		ordinal  int
		fraction float64
		conf     float64
		label    string
	}{
		{"mid chorus", songA, 50, BasisSection, "chorus", 1, 0.5, 0.8, "chorus 1"},
		{"first second", songA, 0, BasisSection, "intro", 1, 0, 0.8, "intro"},
		{"boundary belongs to the next section", songA, 40, BasisSection, "chorus", 1, 0, 0.8, "chorus 1"},
		{"second verse", songA, 75, BasisSection, "verse", 2, 0.75, 0.8, "verse 2"},
		{"last section end", songA, 100, BasisSection, "chorus", 2, 1, 0.8, "chorus 2"},
		{"past the end is proportional", songA, 130, BasisProportional, "", 0, 1, 0.3, ""},
		{"negative clamps to 0", songA, -3, BasisSection, "intro", 1, 0, 0.8, "intro"},
		{"untimed", untimed, 50, BasisProportional, "", 0, 0.25, 0.3, ""},
		{"no duration at all", lyricsOnly, 12, BasisProportional, "", 0, 0, 0.3, ""},
		{"manual is the most trusted", timing("manual", 10, Section{Kind: "verse", Ordinal: 1, Label: "verse", StartS: 0, EndS: 10}), 5, BasisSection, "verse", 1, 0.5, 1, "verse"},
		{"measured", timing("measured", 10, Section{Kind: "verse", Ordinal: 1, Label: "verse", StartS: 0, EndS: 10}), 5, BasisSection, "verse", 1, 0.5, 0.9, "verse"},
		{"gap between sections is proportional", timing("plan", 100, Section{Kind: "verse", Ordinal: 1, Label: "verse", StartS: 0, EndS: 10}, Section{Kind: "chorus", Ordinal: 1, Label: "chorus", StartS: 20, EndS: 30}), 15, BasisProportional, "", 0, 0.15, 0.3, ""},
	} {
		p := Compute(tc.tm, tc.at)
		if p.Basis != tc.basis || p.Kind != tc.kind || p.Ordinal != tc.ordinal || !near(p.Fraction, tc.fraction) || !near(p.Confidence, tc.conf) || p.Label != tc.label {
			t.Errorf("%s: got %+v", tc.name, p)
		}
	}
}

func TestMap_SameKindOrdinal(t *testing.T) {
	t.Parallel()
	b := timing("measured", 120,
		Section{Kind: "verse", Ordinal: 1, Label: "verse 1", StartS: 5, EndS: 45},
		Section{Kind: "chorus", Ordinal: 1, Label: "chorus 1", StartS: 45, EndS: 65},
		Section{Kind: "verse", Ordinal: 2, Label: "verse 2", StartS: 65, EndS: 105},
	)
	p := Compute(songA, 70) // verse 2, fraction 0.5
	at, basis, conf := Map(p, b)
	if basis != BasisSection || !near(at, 85) || !near(conf, 0.8) {
		t.Fatalf("got %v %s %v", at, basis, conf)
	}
	// the less trusted side bounds the confidence
	at, _, conf = Map(Compute(timing("manual", 100, Section{Kind: "verse", Ordinal: 1, Label: "verse", StartS: 0, EndS: 50}), 25), songA)
	if !near(at, 25) || !near(conf, 0.8) {
		t.Fatalf("manual to plan: %v %v", at, conf)
	}
}

func TestMap_MissingSectionFallsBack(t *testing.T) {
	t.Parallel()
	noBridge := timing("plan", 50, Section{Kind: "verse", Ordinal: 1, Label: "verse", StartS: 0, EndS: 50})
	p := Compute(timing("plan", 100, Section{Kind: "bridge", Ordinal: 1, Label: "bridge", StartS: 60, EndS: 80}), 70)
	at, basis, conf := Map(p, noBridge)
	if basis != BasisProportional || !near(at, 35) || !near(conf, 0.3) {
		t.Fatalf("missing kind: %v %s %v", at, basis, conf)
	}
	// ordinal missing
	at, basis, _ = Map(Compute(songA, 90), timing("plan", 40, Section{Kind: "chorus", Ordinal: 1, Label: "chorus", StartS: 0, EndS: 40}))
	if basis != BasisProportional || !near(at, 36) {
		t.Fatalf("missing ordinal: %v %s", at, basis)
	}
	// a proportional point maps proportionally, even when the target is timed
	at, basis, conf = Map(Compute(Timing{DurationS: 200}, 50), songA)
	if basis != BasisProportional || !near(at, 25) || !near(conf, 0.3) {
		t.Fatalf("proportional source: %v %s %v", at, basis, conf)
	}
	// an untimed target with no duration maps to 0
	if at, basis, _ = Map(Compute(songA, 50), Timing{}); basis != BasisProportional || at != 0 {
		t.Fatalf("empty target: %v %s", at, basis)
	}
}

func TestMap_Roundtrip(t *testing.T) {
	t.Parallel()
	b := timing("plan", 130,
		Section{Kind: "intro", Ordinal: 1, Label: "intro", StartS: 0, EndS: 20},
		Section{Kind: "verse", Ordinal: 1, Label: "verse 1", StartS: 20, EndS: 70},
		Section{Kind: "chorus", Ordinal: 1, Label: "chorus 1", StartS: 70, EndS: 90},
		Section{Kind: "verse", Ordinal: 2, Label: "verse 2", StartS: 90, EndS: 110},
		Section{Kind: "chorus", Ordinal: 2, Label: "chorus 2", StartS: 110, EndS: 130},
	)
	for x := 0.0; x <= 100; x += 3.7 {
		at, basis, _ := Map(Compute(songA, x), b)
		back, basis2, _ := Map(Compute(b, at), songA)
		if basis != BasisSection || basis2 != BasisSection || math.Abs(back-x) > 1e-9 {
			t.Fatalf("x=%v -> %v -> %v (%s %s)", x, at, back, basis, basis2)
		}
	}
}

func TestAnchor_MapIsMonotoneWithinSection(t *testing.T) {
	t.Parallel()
	b := timing("measured", 300,
		Section{Kind: "intro", Ordinal: 1, Label: "intro", StartS: 0, EndS: 33},
		Section{Kind: "verse", Ordinal: 1, Label: "verse 1", StartS: 33, EndS: 99},
		Section{Kind: "chorus", Ordinal: 1, Label: "chorus 1", StartS: 99, EndS: 150},
		Section{Kind: "verse", Ordinal: 2, Label: "verse 2", StartS: 150, EndS: 220},
		Section{Kind: "chorus", Ordinal: 2, Label: "chorus 2", StartS: 220, EndS: 300},
	)
	prev, prevSec := -1.0, ""
	for x := 0.0; x <= 100; x += 0.05 {
		p := Compute(songA, x)
		at, _, _ := Map(p, b)
		sec := p.Label
		if sec == prevSec && at < prev-1e-9 {
			t.Fatalf("not monotone at %v: %v after %v", x, at, prev)
		}
		prev, prevSec = at, sec
	}
}
