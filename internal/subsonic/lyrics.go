package subsonic

import (
	"strings"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
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
	for _, l := range lines {
		sl.Line = append(sl.Line, LyricLine{Value: l})
	}
	c.resp.LyricsList.StructuredLyrics = []StructuredLyrics{sl}
	return nil
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
