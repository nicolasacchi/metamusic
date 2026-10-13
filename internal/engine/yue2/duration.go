package yue2

const (
	durationDefaultBPM = 100
	barsPerLyricLine   = 2
	barsPerEmptySect   = 8
	barsTail           = 4
)

// EstimateDuration estimates the song length in seconds and names the basis
// ("abc" | "lyrics_style_bpm" | "lyrics_default_bpm"). A timed ABC score gives Bars x SecondsPerBar;
// otherwise a bar is 240/BPM seconds (4/4), each lyric line takes 2 bars, each section without lines
// 8 bars, plus a 4-bar tail.
func EstimateDuration(r Request) (seconds float64, basis string) {
	if r.ABC != nil {
		if sc, err := ParseScore([]byte(*r.ABC)); err == nil && sc.SecondsPerBar > 0 && sc.Bars > 0 {
			return float64(sc.Bars) * sc.SecondsPerBar, "abc"
		}
	}
	bpm, basis := durationDefaultBPM, "lyrics_default_bpm"
	if _, b := StyleTerms(r.Style); b > 0 {
		bpm, basis = b, "lyrics_style_bpm"
	}
	bars := barsTail
	for _, s := range LyricsSections(r.Lyrics) {
		if len(s.Lines) == 0 {
			bars += barsPerEmptySect
		} else {
			bars += barsPerLyricLine * len(s.Lines)
		}
	}
	return float64(bars) * 240 / float64(bpm), basis
}
