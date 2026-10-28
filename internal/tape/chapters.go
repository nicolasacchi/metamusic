package tape

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
)

// Chapters are written three consistent ways from one list of cumulative sample counts (C128.4 step 5): the binary CUESHEET
// block (sample exact), CHAPTERnnn / CHAPTERnnnNAME Vorbis comments (millisecond) and a text CUESHEET comment (75 frames a
// second, the form foobar2000 and mpv read).

const (
	// LossySuffix ends the chapter name of an item decoded from a lossy source.
	LossySuffix = " [lossy source]"
	// SideSeparator follows the side letter in the chapter name of a sided slot.
	SideSeparator = " · "

	cueFPS       = 75
	cueFile      = "tape.flac"
	cuePerformer = "metamusic"
	cueKey       = "CUESHEET"
)

// ChapterName builds the chapter name of one item: the Take's title, prefixed "A · " for a slot on side A and suffixed
// " [lossy source]" for a lossy item. Control characters become spaces so a name is always one line.
func ChapterName(title, side string, lossy bool) string {
	n := oneLine(title)
	if side != "" {
		n = oneLine(side) + SideSeparator + n
	}
	if lossy {
		n += LossySuffix
	}
	return n
}

// oneLine replaces every control character with a space and trims the ends.
func oneLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return ' '
		}
		return r
	}, s))
}

// PlanChapters turns the per-item frame counts n_i into chapters: chapter i starts at the sum of the earlier counts and
// start_ms = floor(start * 1000 / 48000). items and frames are aligned.
func PlanChapters(items []PlannedItem, frames []int64) []Chapter {
	out := make([]Chapter, len(items))
	var start int64
	for i, it := range items {
		out[i] = Chapter{Index: i + 1, SlotID: it.Item.SlotID, TakeID: it.Item.TakeID, StartSample: start, Samples: frames[i], StartMS: start * 1000 / Rate}
		start += frames[i]
	}
	return out
}

// TotalSamples is the end of the last chapter.
func TotalSamples(chs []Chapter) int64 {
	if len(chs) == 0 {
		return 0
	}
	last := chs[len(chs)-1]
	return last.StartSample + last.Samples
}

// CuesheetOf is the binary CUESHEET block's content: one TRACK per chapter with INDEX 01 at the track's own offset (the index
// offset is relative to the track, so 0), lead-out at the total.
func CuesheetOf(chs []Chapter) flacmeta.Cuesheet {
	c := flacmeta.Cuesheet{LeadOut: uint64(TotalSamples(chs))}
	for _, ch := range chs {
		c.Tracks = append(c.Tracks, flacmeta.CueTrack{
			Offset: uint64(ch.StartSample), Number: uint8(ch.Index), //nolint:gosec // at most MaxItems (60) chapters
			Indexes: []flacmeta.CueIndex{{Offset: 0, Number: 1}},
		})
	}
	return c
}

func chapterKey(i int) string { return fmt.Sprintf("CHAPTER%03d", i) }

// Timestamp formats milliseconds as HH:MM:SS.mmm.
func Timestamp(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}

// ChapterComments are the CHAPTERnnn=HH:MM:SS.mmm and CHAPTERnnnNAME=name comments, in chapter order. names is aligned with chs.
func ChapterComments(names []string, chs []Chapter) []flacmeta.Comment {
	out := make([]flacmeta.Comment, 0, 2*len(chs))
	for i, ch := range chs {
		out = append(out,
			flacmeta.Comment{Key: chapterKey(ch.Index), Value: Timestamp(ch.StartMS)},
			flacmeta.Comment{Key: chapterKey(ch.Index) + "NAME", Value: oneLine(names[i])})
	}
	return out
}

// cueFrames is the 75 fps position of a sample offset, floored.
func cueFrames(sample int64) int64 { return sample * cueFPS / Rate }

// CueText is the value of the text CUESHEET comment: FILE, then per track TRACK nn AUDIO, TITLE, PERFORMER and INDEX 01
// mm:ss:ff at 75 frames a second. Double quotes in names are replaced by single quotes (the format has no escape).
func CueText(names []string, chs []Chapter) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FILE %q WAVE\n", cueFile)
	for i, ch := range chs {
		f := cueFrames(ch.StartSample)
		fmt.Fprintf(&b, "  TRACK %02d AUDIO\n    TITLE \"%s\"\n    PERFORMER \"%s\"\n    INDEX 01 %02d:%02d:%02d\n",
			ch.Index, strings.ReplaceAll(oneLine(names[i]), `"`, "'"), cuePerformer, f/(60*cueFPS), f/cueFPS%60, f%cueFPS)
	}
	return b.String()
}

// ChapterTags is the full chapter metadata of a tape as Vorbis comments: CHAPTERnnn pairs, then the text CUESHEET.
func ChapterTags(names []string, chs []Chapter) []flacmeta.Comment {
	return append(ChapterComments(names, chs), flacmeta.Comment{Key: cueKey, Value: CueText(names, chs)})
}

// ParsedChapter is one chapter read back from a file's comments.
type ParsedChapter struct {
	Index int
	MS    int64
	Name  string
}

// ParseChapterComments reads CHAPTERnnn / CHAPTERnnnNAME entries ("KEY=value" strings, as flacmeta.Info.Comments) back, in
// index order. Unrelated comments are ignored; a malformed timestamp is an error.
func ParseChapterComments(comments []string) ([]ParsedChapter, error) {
	byIdx := map[int]*ParsedChapter{}
	var order []int
	for _, c := range comments {
		k, v, ok := strings.Cut(c, "=")
		if !ok || !strings.HasPrefix(k, "CHAPTER") {
			continue
		}
		rest := strings.TrimPrefix(k, "CHAPTER")
		digits, name := rest, false
		if strings.HasSuffix(rest, "NAME") {
			digits, name = strings.TrimSuffix(rest, "NAME"), true
		}
		if len(digits) != 3 {
			continue
		}
		idx, err := strconv.Atoi(digits)
		if err != nil {
			continue
		}
		p := byIdx[idx]
		if p == nil {
			p = &ParsedChapter{Index: idx}
			byIdx[idx] = p
			order = append(order, idx)
		}
		if name {
			p.Name = v
			continue
		}
		ms, err := parseTimestamp(v)
		if err != nil {
			return nil, err
		}
		p.MS = ms
	}
	out := make([]ParsedChapter, 0, len(order))
	for i := 1; i <= len(order); i++ {
		p := byIdx[i]
		if p == nil {
			return nil, fmt.Errorf("tape: chapter comments are not numbered from 1 without gaps")
		}
		out = append(out, *p)
	}
	return out, nil
}

func parseTimestamp(s string) (int64, error) {
	var h, m, sec, ms int64
	if n, err := fmt.Sscanf(s, "%d:%d:%d.%d", &h, &m, &sec, &ms); err != nil || n != 4 || len(s) != 12 {
		return 0, fmt.Errorf("tape: bad chapter timestamp")
	}
	return ((h*60+m)*60+sec)*1000 + ms, nil
}

// ParsedCue is one track of a text CUESHEET read back.
type ParsedCue struct {
	Track  int
	Title  string
	Frames int64 // INDEX 01 in 75 fps frames
}

// ParseCueText reads a text cuesheet as CueText writes it.
func ParseCueText(s string) ([]ParsedCue, error) {
	var out []ParsedCue
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "TRACK "):
			var n int
			if _, err := fmt.Sscanf(line, "TRACK %d AUDIO", &n); err != nil {
				return nil, fmt.Errorf("tape: bad cuesheet TRACK line")
			}
			out = append(out, ParsedCue{Track: n})
		case strings.HasPrefix(line, "TITLE ") && len(out) > 0:
			out[len(out)-1].Title = strings.Trim(strings.TrimPrefix(line, "TITLE "), `"`)
		case strings.HasPrefix(line, "INDEX 01 ") && len(out) > 0:
			var mm, ss, ff int64
			if _, err := fmt.Sscanf(line, "INDEX 01 %d:%d:%d", &mm, &ss, &ff); err != nil {
				return nil, fmt.Errorf("tape: bad cuesheet INDEX line")
			}
			out[len(out)-1].Frames = (mm*60+ss)*cueFPS + ff
		}
	}
	return out, nil
}
