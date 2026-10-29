package tape

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func plannedN(n int) []PlannedItem {
	out := make([]PlannedItem, n)
	for i := range out {
		out[i].Item = Item{SlotID: "s" + string(rune('a'+i)), TakeID: "t" + string(rune('a'+i)), Position: i + 1}
	}
	return out
}

func TestChapterName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		title, side string
		lossy       bool
		want        string
	}{
		{"Song", "", false, "Song"},
		{"Song", "A", false, "A · Song"},
		{"Song", "", true, "Song [lossy source]"},
		{"Song", "B", true, "B · Song [lossy source]"},
		{"two\nlines\tand\x00nul", "", false, "two lines and nul"},
	} {
		if got := ChapterName(c.title, c.side, c.lossy); got != c.want {
			t.Errorf("%q: got %q, want %q", c.title, got, c.want)
		}
	}
}

func TestPipeline_Chapters(t *testing.T) {
	t.Parallel()
	// 1 s, 2 s and 3.5 s at 48 kHz; the plan example offsets 0, 48 000, 144 000
	frames := []int64{48_000, 96_000, 168_000}
	chs := PlanChapters(plannedN(3), frames)
	want := []Chapter{
		{Index: 1, SlotID: "sa", TakeID: "ta", StartSample: 0, Samples: 48_000, StartMS: 0},
		{Index: 2, SlotID: "sb", TakeID: "tb", StartSample: 48_000, Samples: 96_000, StartMS: 1000},
		{Index: 3, SlotID: "sc", TakeID: "tc", StartSample: 144_000, Samples: 168_000, StartMS: 3000},
	}
	if diff := cmp.Diff(want, chs); diff != "" {
		t.Fatal(diff)
	}
	if TotalSamples(chs) != 312_000 {
		t.Fatal(TotalSamples(chs))
	}
	names := []string{"One", `Say "hi"`, "Three [lossy source]"}

	// block: exact
	cue := CuesheetOf(chs)
	for i, tr := range cue.Tracks {
		if int64(tr.Offset) != chs[i].StartSample || int(tr.Number) != i+1 {
			t.Fatalf("track %d", i)
		}
	}
	if cue.LeadOut != 312_000 {
		t.Fatal(cue.LeadOut)
	}
	// comments: within 1 ms
	cm := ChapterComments(names, chs)
	if cm[0].Key != "CHAPTER001" || cm[0].Value != "00:00:00.000" || cm[2].Value != "00:00:01.000" || cm[4].Value != "00:00:03.000" || cm[3].Key != "CHAPTER002NAME" {
		t.Fatalf("%+v", cm)
	}
	var raw []string
	for _, c := range ChapterTags(names, chs) {
		raw = append(raw, c.Key+"="+c.Value)
	}
	pc, err := ParseChapterComments(raw)
	if err != nil || len(pc) != 3 {
		t.Fatal(pc, err)
	}
	for i, p := range pc {
		if p.MS != chs[i].StartMS || p.Name != oneLine(names[i]) {
			t.Fatalf("chapter %d: %+v", i, p)
		}
	}
	// text: within 1/75 s
	text := CueText(names, chs)
	if !strings.Contains(text, `FILE "tape.flac" WAVE`) || !strings.Contains(text, `TITLE "Say 'hi'"`) || strings.Count(text, "PERFORMER \"metamusic\"") != 3 {
		t.Fatal(text)
	}
	pt, err := ParseCueText(text)
	if err != nil || len(pt) != 3 {
		t.Fatal(pt, err)
	}
	for i, p := range pt {
		exact := float64(chs[i].StartSample) * 75 / 48000
		if d := exact - float64(p.Frames); d < 0 || d >= 1 || p.Track != i+1 {
			t.Fatalf("cue %d: frames %d vs exact %v", i, p.Frames, exact)
		}
	}
	if pt[2].Frames != 225 { // 3 s
		t.Fatal(pt[2])
	}
}

func TestPipeline_Chapters_OffsetsAreCumulative(t *testing.T) {
	t.Parallel()
	frames := []int64{44_100, 1, 47_999, 480_001}
	chs := PlanChapters(plannedN(4), frames)
	var sum int64
	for i, c := range chs {
		if c.StartSample != sum || c.StartMS != sum*1000/48000 {
			t.Fatalf("chapter %d starts at %d", i, c.StartSample)
		}
		sum += frames[i]
	}
	if TotalSamples(chs) != sum {
		t.Fatal("total")
	}
	if got := Timestamp(3_723_456); got != "01:02:03.456" {
		t.Fatal(got)
	}
}

func TestParseChapterComments_Rejects(t *testing.T) {
	t.Parallel()
	if _, err := ParseChapterComments([]string{"CHAPTER001=garbage"}); err == nil {
		t.Error("bad timestamp accepted")
	}
	if _, err := ParseChapterComments([]string{"CHAPTER002=00:00:01.000"}); err == nil {
		t.Error("gap accepted")
	}
	if pc, err := ParseChapterComments([]string{"TITLE=x", "CHAPTERS=1"}); err != nil || len(pc) != 0 {
		t.Error(pc, err)
	}
}
