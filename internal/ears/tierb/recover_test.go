package tierb_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// phrase is one recognised line of a synthetic performance.
type phrase struct {
	text string
	gapS float64 // silence before the phrase
	logp float64
	noSp float64
}

// docOf lays phrases out one after the other from startS; every word lasts 0.4 s with probability 0.9.
func docOf(startS float64, ps []phrase) tierb.Raw {
	asr := &tierb.ASR{Language: "it", Segments: []tierb.Segment{}, Words: []tierb.Word{}}
	t := startS
	for _, p := range ps {
		t += p.gapS
		w0 := len(asr.Words)
		fs := strings.Fields(p.text)
		if len(fs) == 0 {
			fs = []string{" "} // a blank word
		}
		for _, f := range fs {
			asr.Words = append(asr.Words, tierb.Word{W: " " + f, Start: t, End: t + 0.4, P: 0.9})
			t += 0.45
		}
		lp := p.logp
		if lp == 0 {
			lp = -0.3
		}
		asr.Segments = append(asr.Segments, tierb.Segment{Start: asr.Words[w0].Start, End: asr.Words[len(asr.Words)-1].End,
			AvgLogprob: lp, NoSpeechP: p.noSp, W0: w0, W1: len(asr.Words)})
		t += 0.6
	}
	return tierb.Raw{Contract: tierb.ContractID, Status: tierb.StatusOK, ASR: asr}
}

func rec(t *testing.T, raw tierb.Raw, dur float64) tierb.Recovered {
	t.Helper()
	r, err := tierb.RecoverLyrics(raw, dur)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func kinds(r tierb.Recovered) string {
	var k []string
	for _, s := range r.Sections {
		k = append(k, s.Kind)
	}
	return strings.Join(k, ",")
}

// knownSong is an invented Italian lyric: two verses, a chorus sung twice and a third verse.
var knownSong = [][]string{
	{"la luna cammina sul prato", "il vento racconta una storia", "e noi restiamo qui a guardare"},
	{"sole sole porta il giorno", "canta con me fino a sera", "sole sole porta il giorno"},
	{"la strada e lunga e stretta", "ma il cuore conosce la via", "e non ha paura del buio"},
	{"sole sole porta il giorno", "canta con me fino a sera", "sole sole porta il giorno"},
	{"quando la notte si ferma", "restano solo le stelle", "e un piccolo fuoco acceso"},
}

func songDoc(subst map[int]string) tierb.Raw {
	var ps []phrase
	n := 0
	for bi, b := range knownSong {
		for li, l := range b {
			words := strings.Fields(l)
			for wi := range words {
				n++
				if r, ok := subst[n]; ok {
					words[wi] = r
				}
			}
			p := phrase{text: strings.Join(words, " ")}
			if li == 0 && bi > 0 {
				p.gapS = 5
			}
			ps = append(ps, p)
		}
	}
	return docOf(1, ps)
}

func TestRecoverLyrics_ChorusFromRepetition(t *testing.T) {
	t.Parallel()
	r := rec(t, songDoc(nil), 70)
	if got, want := kinds(r), "verse,chorus,verse,chorus,verse"; got != want {
		t.Errorf("kinds = %s, want %s", got, want)
	}
	if r.Lines != 15 || r.Words != 80 || r.Language != "it" {
		t.Errorf("counts: %+v", r)
	}
	if r.Confidence < 0.89 || r.Confidence > 0.91 {
		t.Errorf("confidence = %v, want about 0.9", r.Confidence)
	}
	if !strings.HasPrefix(r.Lyrics, "[verse]\nla luna cammina sul prato\n") {
		t.Errorf("lyrics start: %q", r.Lyrics[:40])
	}
}

func TestRecoverLyrics_BridgeAfterSecondChorus(t *testing.T) {
	t.Parallel()
	ch := []string{"sole sole porta il giorno", "canta con me fino a sera"}
	br := []string{"adesso parlo piano", "ascolta quello che resta"}
	v := []string{"la luna cammina sul prato", "il vento racconta una storia"}
	var ps []phrase
	for _, blk := range [][]string{v, ch, ch, br, ch} {
		for i, l := range blk {
			p := phrase{text: l}
			if i == 0 {
				p.gapS = 5
			}
			ps = append(ps, p)
		}
	}
	r := rec(t, docOf(1, ps), 0)
	if got, want := kinds(r), "verse,chorus,chorus,bridge,chorus"; got != want {
		t.Errorf("kinds = %s, want %s", got, want)
	}
	// a block between only one chorus and another stays a verse
	r = rec(t, docOf(1, []phrase{{text: ch[0]}, {text: ch[1]}, {text: br[0], gapS: 5}, {text: ch[0], gapS: 5}, {text: ch[1]}}), 0)
	if got, want := kinds(r), "chorus,verse,chorus"; got != want {
		t.Errorf("kinds = %s, want %s", got, want)
	}
}

func TestRecoverLyrics_IntroOutro(t *testing.T) {
	t.Parallel()
	ps := []phrase{{text: "la luna cammina sul prato"}, {text: "il vento racconta una storia"}}
	r := rec(t, docOf(12, ps), 60)
	if got, want := kinds(r), "intro,verse,outro"; got != want {
		t.Errorf("kinds = %s, want %s", got, want)
	}
	if !strings.HasPrefix(r.Lyrics, "[intro]\n\n[verse]\n") || !strings.HasSuffix(r.Lyrics, "\n[outro]\n") {
		t.Errorf("lyrics: %q", r.Lyrics)
	}
	r = rec(t, docOf(7.9, ps), 20)
	if got := kinds(r); got != "verse" {
		t.Errorf("tight song kinds = %s, want verse", got)
	}
	r = rec(t, docOf(1, ps), 0)
	if got := kinds(r); got != "verse" {
		t.Errorf("unknown duration kinds = %s, want verse", got)
	}
}

func TestRecoverLyrics_HallucinationGuards(t *testing.T) {
	t.Parallel()
	ps := []phrase{{text: "la luna cammina sul prato"}}
	for range 9 { // a loop of one phrase
		ps = append(ps, phrase{text: "grazie a tutti"})
	}
	ps = append(ps,
		phrase{text: "sottotitoli a cura di nessuno", noSp: 0.7}, // no speech
		phrase{text: "xx yy zz", logp: -1.2},                     // doubtful
		phrase{text: "   "},                                      // blank
		phrase{text: "il vento racconta una storia"})
	r := rec(t, docOf(1, ps), 0)
	if r.Lines != 6 {
		t.Fatalf("lines = %d, want 1 + 4 + 1: %q", r.Lines, r.Lyrics)
	}
	if got := strings.Count(r.Lyrics, "grazie a tutti"); got != 4 {
		t.Errorf("loop kept %d times, want 4", got)
	}
	for _, bad := range []string{"sottotitoli", "xx yy"} {
		if strings.Contains(r.Lyrics, bad) {
			t.Errorf("%q survived the guards", bad)
		}
	}
	// the same phrase four times is kept in full, and a different phrase resets the run
	r = rec(t, docOf(1, []phrase{{text: "ah ah"}, {text: "ah ah"}, {text: "ah ah"}, {text: "ah ah"}, {text: "oh"}, {text: "ah ah"}}), 0)
	if r.Lines != 6 {
		t.Errorf("lines = %d, want 6", r.Lines)
	}
}

func TestRecoverLyrics_LongSegmentSplit(t *testing.T) {
	t.Parallel()
	words := strings.Fields("uno due tre quattro cinque sei sette otto nove dieci undici dodici tredici quattordici")
	raw := docOf(1, []phrase{{text: strings.Join(words, " ")}})
	raw.ASR.Words[9].Start += 1.5 // a pause after "nove"... shifts the rest
	for i := 10; i < len(raw.ASR.Words); i++ {
		raw.ASR.Words[i].Start += 1.5
		raw.ASR.Words[i].End += 1.5
	}
	raw.ASR.Words[9].End += 1.5
	r := rec(t, raw, 0)
	if r.Lines != 2 || !strings.Contains(r.Lyrics, "otto nove\n") {
		t.Errorf("want a split at the largest gap: %q", r.Lyrics)
	}
	for _, l := range strings.Split(strings.TrimSpace(r.Lyrics), "\n")[1:] {
		if n := len(strings.Fields(l)); n > 12 {
			t.Errorf("line of %d words: %q", n, l)
		}
	}
	// 30 words: no piece exceeds 12
	long := strings.TrimSpace(strings.Repeat("ra ", 30))
	r = rec(t, docOf(1, []phrase{{text: long}}), 0)
	for _, l := range strings.Split(strings.TrimSpace(r.Lyrics), "\n")[1:] {
		if n := len(strings.Fields(l)); n > 12 {
			t.Errorf("line of %d words", n)
		}
	}
}

func TestRecoverLyrics_BracketsAreNotHeaders(t *testing.T) {
	t.Parallel()
	r := rec(t, docOf(1, []phrase{{text: "[verse] la luna] [chorus"}}), 0)
	if r.Lyrics != "[verse]\nverse la luna chorus\n" {
		t.Errorf("lyrics = %q", r.Lyrics)
	}
}

func TestRecoverLyrics_Empty(t *testing.T) {
	t.Parallel()
	good := docOf(1, []phrase{{text: "la luna"}})
	for name, mod := range map[string]func(*tierb.Raw){
		"no asr":       func(r *tierb.Raw) { r.ASR = nil },
		"status error": func(r *tierb.Raw) { r.Status = tierb.StatusError },
		"no segments":  func(r *tierb.Raw) { r.ASR.Segments = nil },
		"all dropped":  func(r *tierb.Raw) { r.ASR.Segments[0].NoSpeechP = 0.9 },
		"bad range":    func(r *tierb.Raw) { r.ASR.Segments[0].W1 = 99 },
	} {
		var raw tierb.Raw
		b, _ := json.Marshal(good)
		_ = json.Unmarshal(b, &raw)
		mod(&raw)
		if _, err := tierb.RecoverLyrics(raw, 30); !errors.Is(err, tierb.ErrTranscriptEmpty) {
			t.Errorf("%s: err = %v, want ErrTranscriptEmpty", name, err)
		}
	}
}

// cerOf is the character error rate of got against want over the Fold of their lyric lines.
func cerOf(got, want string) float64 {
	lines := func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "[") {
				out = append(out, tierb.Fold(l))
			}
		}
		return strings.Join(out, " ")
	}
	g, w := []rune(lines(got)), []rune(lines(want))
	return float64(tierb.Levenshtein(g, w)) / float64(len(w))
}

func knownLyric() string {
	var sb strings.Builder
	for _, b := range knownSong {
		sb.WriteString("[x]\n" + strings.Join(b, "\n") + "\n")
	}
	return sb.String()
}

func TestRecoverLyrics_KnownLyricCER(t *testing.T) {
	t.Parallel()
	// 5 % of the 80 words (every 20th) heard as something else
	subst := map[int]string{20: "mare", 40: "casa", 60: "fiore", 80: "sera"}
	r := rec(t, songDoc(subst), 120)
	if cer := cerOf(r.Lyrics, knownLyric()); cer > 0.15 || cer == 0 {
		t.Errorf("CER = %.3f, want in (0, 0.15]", cer)
	}
	if cer := cerOf(rec(t, songDoc(nil), 120).Lyrics, knownLyric()); cer != 0 {
		t.Errorf("faithful transcript CER = %v, want 0", cer)
	}
}

func TestRecoverLyrics_ParsesAsYuE2(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]tierb.Raw{
		"song":        songDoc(nil),
		"intro/outro": docOf(12, []phrase{{text: "la luna cammina sul prato"}, {text: "il vento racconta una storia"}}),
	} {
		r := rec(t, raw, 80)
		body, err := json.Marshal(map[string]any{"id": "rec-test-abc123", "style": "it vocals, 100 BPM", "lyrics": r.Lyrics, "cot": "melody", "seed": 7})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := yue2.ParseRequest(body, yue2.ParseOptions{}); err != nil {
			t.Errorf("%s: ParseRequest: %v", name, err)
		}
		for _, is := range yue2.CheckRequest(body, yue2.ModeApp, nil) {
			if is.Severity == "error" {
				t.Errorf("%s: protocol error %s", name, is.Code)
			}
		}
		secs := yue2.LyricsSections(r.Lyrics)
		if len(secs) != len(r.Sections) {
			t.Errorf("%s: %d yue2 sections, %d recovered", name, len(secs), len(r.Sections))
		}
	}
}

func FuzzRecoverLyrics(f *testing.F) {
	f.Add("la luna cammina sul prato", 1.5, 5.0, 0.3, 40.0)
	f.Add("a b c d e f g h i j k l m n o p", 0.0, 0.2, 0.9, 10.0)
	f.Fuzz(func(t *testing.T, text string, gap, step, noSp, dur float64) {
		if step < 0 || step > 100 || gap < 0 || gap > 100 {
			t.Skip()
		}
		var ps []phrase
		for i := range 8 {
			ps = append(ps, phrase{text: text, gapS: gap + float64(i)*step, noSp: noSp})
		}
		raw := docOf(0, ps)
		r, err := tierb.RecoverLyrics(raw, dur)
		if err != nil {
			if !errors.Is(err, tierb.ErrTranscriptEmpty) {
				t.Fatalf("err = %v", err)
			}
			return
		}
		if r.Confidence < 0 || r.Confidence > 1 || r.Lines < 1 || r.Words < 1 {
			t.Fatalf("out of range: %+v", r)
		}
		if got := strings.Count(r.Lyrics, "\n["); got+boolInt(strings.HasPrefix(r.Lyrics, "[")) != len(r.Sections) {
			t.Fatalf("headers do not match sections: %q vs %d", r.Lyrics, len(r.Sections))
		}
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
