package attention

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/anchor"
	"github.com/nicolasacchi/metamusic/internal/listening"
)

func ms(n int64) *int64 { return &n }

func ev(session, kind string, from, to int64) listening.Event {
	e := listening.Event{ID: session + kind + strconv.FormatInt(from, 10) + "-" + strconv.FormatInt(to, 10), TrackID: "t", SessionID: session, Kind: kind}
	if kind == "segment" || kind == "seek" {
		e.FromMS, e.ToMS = ms(from), ms(to)
	} else if from >= 0 {
		e.FromMS = ms(from)
	}
	return e
}

// exampleEvents is the worked case of C256: 120 s, session A listens 0-60, B listens 0-30, seeks back 30 -> 10 and listens
// 10-40, C skips at 5 s.
func exampleEvents() []listening.Event {
	return []listening.Event{
		ev("sessionAAA", "start", -1, 0), ev("sessionAAA", "segment", 0, 60_000),
		ev("sessionBBB", "start", -1, 0), ev("sessionBBB", "segment", 0, 30_000), ev("sessionBBB", "seek", 30_000, 10_000),
		ev("sessionBBB", "segment", 10_000, 40_000), ev("sessionBBB", "complete", -1, 0),
		ev("sessionCCC", "start", -1, 0), ev("sessionCCC", "skip", 5_000, 0),
	}
}

func TestComputeHeat_Example(t *testing.T) {
	t.Parallel()
	h := ComputeHeat(120_000, exampleEvents(), 120)
	if h.Bins != 120 || h.BinS != 1 || h.DurationS != 120 || len(h.Heard) != 120 || len(h.Replays) != 120 || len(h.Rewinds) != 120 || len(h.Skips) != 120 {
		t.Fatalf("shape: bins %d binS %v len %d", h.Bins, h.BinS, len(h.Heard))
	}
	if h.Sessions != 3 || h.Plays != 3 || h.CompletionRate == nil || *h.CompletionRate < 0.333 || *h.CompletionRate > 0.334 {
		t.Errorf("sessions %d plays %d completion %v", h.Sessions, h.Plays, h.CompletionRate)
	}
	if h.Rewinds[10] != 1 || h.Replays[10] != 1 || h.Skips[5] != 1 {
		t.Errorf("bin counts: rewinds[10]=%d replays[10]=%d skips[5]=%d", h.Rewinds[10], h.Replays[10], h.Skips[5])
	}
	// bin 10 is heard by A and B (B twice, still one session), bin 50 only by A, bin 70 by nobody
	if h.Heard[10] != 2 || h.Heard[50] != 1 || h.Heard[70] != 0 {
		t.Errorf("heard: %d %d %d", h.Heard[10], h.Heard[50], h.Heard[70])
	}
	want := []Hotspot{{FromS: 10, ToS: 11, Score: 4}}
	if !reflect.DeepEqual(h.Hotspots, want) {
		t.Errorf("hotspots = %+v, want %+v", h.Hotspots, want)
	}
	if !reflect.DeepEqual(h.Cold, []int{5}) {
		t.Errorf("cold = %v", h.Cold)
	}
}

// TestHotspots_SpanRule: h = 3*rewinds + replays; a span is a maximal run of bins with h >= 2; its score is the maximum;
// ranked by score then start; at most three; labelled from the timing.
func TestHotspots_SpanRule(t *testing.T) {
	t.Parallel()
	var evs []listening.Event
	rewind := func(s string, atMS int64) { // listen 0..40 s, seek back to atMS, listen to 40 s again from there
		evs = append(evs, ev(s, "start", -1, 0), ev(s, "segment", 0, 40_000), ev(s, "seek", 40_000, atMS), ev(s, "segment", atMS, atMS+1_000))
	}
	rewind("s1111111", 20_000) // rewinds[20]=1, replays[20]=1 -> h 4
	rewind("s2222222", 20_000) // h[20] = 8
	rewind("s3333333", 21_000) // bin 21: h 4 each -> run 20..21, score max
	rewind("s4444444", 30_000) // separate run at 30
	rewind("s5555555", 5_000)  // separate run at 5
	rewind("s6666666", 10_000) // fourth run: dropped (only three kept)
	h := ComputeHeat(100_000, evs, 100)
	if len(h.Hotspots) != 3 {
		t.Fatalf("hotspots: %+v", h.Hotspots)
	}
	if h.Hotspots[0] != (Hotspot{FromS: 20, ToS: 22, Score: 8}) {
		t.Errorf("first = %+v", h.Hotspots[0])
	}
	// equal scores (4) order by earlier start: 5, 10 beat 30
	if h.Hotspots[1].FromS != 5 || h.Hotspots[2].FromS != 10 || h.Hotspots[1].Score != 4 {
		t.Errorf("rest = %+v", h.Hotspots[1:])
	}
	tm := anchor.Timing{Source: "plan", DurationS: 100, Sections: []anchor.Section{
		{Kind: "verse", Ordinal: 1, Label: "verse 1", StartS: 0, EndS: 15},
		{Kind: "chorus", Ordinal: 1, Label: "chorus", StartS: 15, EndS: 100}}}
	h.LabelHotspots(tm)
	if h.Hotspots[0].Label == nil || *h.Hotspots[0].Label != "chorus" || *h.Hotspots[1].Label != "verse 1" {
		t.Errorf("labels: %+v", h.Hotspots)
	}
	// an untimed take leaves the labels null
	h2 := ComputeHeat(100_000, evs, 100)
	h2.LabelHotspots(anchor.Timing{DurationS: 100})
	if h2.Hotspots[0].Label != nil {
		t.Errorf("untimed label = %v", *h2.Hotspots[0].Label)
	}
	// a single replay (h = 1) is not a hotspot
	one := ComputeHeat(100_000, []listening.Event{ev("sXXXXXXX", "start", -1, 0), ev("sXXXXXXX", "segment", 0, 30_000), ev("sXXXXXXX", "segment", 10_000, 20_000)}, 100)
	if len(one.Hotspots) != 0 {
		t.Errorf("h=1 span: %+v", one.Hotspots)
	}
}

// TestComputeHeat_SubsonicEvents: a device that only reports start, segment and complete still fills heard and plays; it has
// no seeks or skips to count.
func TestComputeHeat_SubsonicEvents(t *testing.T) {
	t.Parallel()
	evs := []listening.Event{ev("sub00001", "start", -1, 0), ev("sub00001", "segment", 0, 50_000), ev("sub00001", "complete", -1, 0)}
	h := ComputeHeat(100_000, evs, 100)
	if h.Plays != 1 || h.CompletionRate == nil || *h.CompletionRate != 1 || h.Heard[49] != 1 || h.Heard[50] != 0 || len(h.Hotspots) != 0 {
		t.Errorf("heat = %+v", h)
	}
	for b := range h.Rewinds {
		if h.Rewinds[b] != 0 || h.Skips[b] != 0 {
			t.Fatalf("bin %d has seeks/skips", b)
		}
	}
}

func TestComputeHeat_IgnoresDiagnostics(t *testing.T) {
	t.Parallel()
	base := exampleEvents()
	with := append([]listening.Event{}, base...)
	for _, k := range []string{"autoplay_blocked", "stall_end", "advance", "error"} {
		e := ev("sessionDDD", k, 20_000, 30_000)
		e.FromMS, e.ToMS = ms(20_000), ms(30_000)
		with = append(with, e)
	}
	a, b := ComputeHeat(120_000, base, 120), ComputeHeat(120_000, with, 120)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("diagnostics changed the heat:\n%+v\n%+v", a, b)
	}
}

func TestComputeHeat_Edges(t *testing.T) {
	t.Parallel()
	// no duration: empty, never divides by zero
	h := ComputeHeat(0, exampleEvents(), 120)
	if h.Bins != 0 || len(h.Heard) != 0 || len(h.Hotspots) != 0 || h.Hotspots == nil || h.Cold == nil {
		t.Errorf("zero duration: %+v", h)
	}
	// no events: zero bins of the right length, no completion rate
	h = ComputeHeat(60_000, nil, 0)
	if h.Bins != Bins || len(h.Heard) != Bins || h.CompletionRate != nil || h.Sessions != 0 || h.Plays != 0 {
		t.Errorf("no events: bins %d rate %v", h.Bins, h.CompletionRate)
	}
	// positions past the end clamp to the last bin; a forward seek of more than 5 s counts as a skip at its origin
	h = ComputeHeat(60_000, []listening.Event{ev("sEEEEEEE", "start", -1, 0), ev("sEEEEEEE", "segment", 0, 90_000), ev("sEEEEEEE", "seek", 10_000, 16_000), ev("sEEEEEEE", "seek", 10_000, 15_000)}, 60)
	if h.Heard[59] != 1 || h.Skips[10] != 1 {
		t.Errorf("clamp: heard[59]=%d skips[10]=%d", h.Heard[59], h.Skips[10])
	}
	// completion never exceeds 1
	h = ComputeHeat(60_000, []listening.Event{ev("sFFFFFFF", "start", -1, 0), ev("sFFFFFFF", "complete", -1, 0), ev("sGGGGGGG", "complete", -1, 0)}, 60)
	if h.CompletionRate == nil || *h.CompletionRate != 1 {
		t.Errorf("completion = %v", h.CompletionRate)
	}
}
