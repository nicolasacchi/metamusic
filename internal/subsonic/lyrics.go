package subsonic

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
)

func (s *server) registerLyrics() {
	s.add("getLyrics", false, (*call).getLyrics)
	s.add("getLyricsBySongId", false, (*call).getLyricsBySongID)
}

// lyricLines flattens lyrics into plain lines (D-M4a-13): section headers are dropped and one blank line separates
// sections; sections without lyric lines (instrumental parts) leave no trace.
func lyricLines(lyrics string) []string {
	lines := []string{}
	for _, sec := range yue2.LyricsSections(lyrics) {
		if len(sec.Lines) == 0 {
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, sec.Lines...)
	}
	return lines
}

func (c *call) getLyricsBySongID() error {
	row, err := c.songParam("id")
	if err != nil {
		return err
	}
	cat, err := c.cat()
	if err != nil {
		return err
	}
	text, err := cat.lyricsOf(c.ctx, row.ID)
	if err != nil {
		return err
	}
	c.resp.LyricsList = &LyricsList{}
	lines := lyricLines(text)
	if len(lines) == 0 {
		return nil
	}
	lang := yue2.DetectLanguage(text)
	if lang == "" {
		lang = "und"
	}
	sl := StructuredLyrics{DisplayArtist: artistName, DisplayTitle: row.Title, Lang: lang, Line: make([]LyricLine, 0, len(lines))}
	synced, err := c.syncedLines(row.ID, lines)
	if err != nil {
		return err
	}
	if synced != nil {
		sl.Synced, sl.Line = true, synced
	} else {
		for _, l := range lines {
			sl.Line = append(sl.Line, LyricLine{Value: l})
		}
	}
	c.resp.LyricsList.StructuredLyrics = []StructuredLyrics{sl}
	return nil
}

// minSyncedShare is the share of lyric lines that must carry a measured time for a synced answer (C348).
const minSyncedShare = 0.75

// syncedLines returns the timed lines of a take (separator lines dropped), or nil when the answer stays unsynced: no Diction
// collaborator, a take the principal cannot read, no ASR word timings, or fewer than 75 % of the lines timed. An untimed line
// takes the previous start plus one millisecond. Lyrics reach only the authenticated principal of this call (C348, D-EARS-16).
func (c *call) syncedLines(trackID string, lines []string) ([]LyricLine, error) {
	if c.s.o.Diction == nil {
		return nil, nil
	}
	starts, ok, err := c.s.o.Diction.LineStarts(c.ctx, c.principal, trackID)
	if errors.Is(err, library.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var text []string
	for _, l := range lines {
		if l != "" {
			text = append(text, l)
		}
	}
	if !ok || len(text) == 0 || len(starts) != len(text) {
		return nil, nil
	}
	timed := 0
	for _, d := range starts {
		if d >= 0 {
			timed++
		}
	}
	if float64(timed) < minSyncedShare*float64(len(text)) {
		return nil, nil
	}
	out := make([]LyricLine, 0, len(text))
	prev := -time.Millisecond
	for i, l := range text {
		at := starts[i]
		if at < 0 {
			at = prev + time.Millisecond
		}
		prev = at
		ms := at.Milliseconds()
		out = append(out, LyricLine{Start: &ms, Value: l})
	}
	return out, nil
}

// getLyrics answers the lyrics of the best title match among the songs of the default folders; the artist
// parameter is not needed (there is one artist). No match is an empty element.
func (c *call) getLyrics() error {
	c.resp.Lyrics = &Lyrics{}
	title := strings.TrimSpace(c.str("title"))
	if title == "" {
		return nil
	}
	expr, _ := ftsExpr(title)
	if expr == "" {
		return nil
	}
	cat, err := c.cat()
	if err != nil {
		return err
	}
	frag, _ := folderWhere(0)
	songs, err := cat.searchSongs(c.ctx, frag, "{title}: ("+expr+")", 1, 0)
	if err != nil || len(songs) == 0 {
		return err
	}
	text, err := cat.lyricsOf(c.ctx, songs[0].ID)
	if err != nil {
		return err
	}
	c.resp.Lyrics = &Lyrics{Artist: artistName, Title: songs[0].Title, Value: strings.Join(lyricLines(text), "\n")}
	return nil
}

// UseDiction attaches the synced-lyrics source to a handler built by NewHandler (C348). The Ears track calls it from its build
// hook, after the handler exists; it must run before the handler serves. A handler of another type is left alone (false).
func UseDiction(h http.Handler, d LineTimes) bool {
	s, ok := h.(*server)
	if ok {
		s.o.Diction = d
	}
	return ok
}
