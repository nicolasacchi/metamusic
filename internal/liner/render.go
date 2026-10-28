package liner

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Size limits of the rendered notes.
const (
	// MaxMarkdownBytes caps Markdown (C124.1).
	MaxMarkdownBytes = 8 << 10
	// MaxDescriptionChars caps Description, the tape DESCRIPTION.
	MaxDescriptionChars = 1000
)

// Markdown renders the notes as Markdown of at most MaxMarkdownBytes. The track list is cut at a whole line with a notice
// saying how many tracks are not shown; an overlong header is cut at a character boundary first.
func (l Liner) Markdown() string {
	head := "# " + oneLine(l.Title) + "\n\n"
	if l.subtitle != "" {
		head += "_" + l.subtitle + "_\n\n"
	}
	if l.date != "" {
		head += l.date + "\n\n"
	}
	if l.note != "" {
		head += l.note + "\n\n"
	}
	head += l.summaryBlock()
	var footer string
	var tracks []Line
	for _, ln := range l.Lines {
		if ln.Position == 0 {
			footer += "\n" + ln.Text + "\n"
			continue
		}
		tracks = append(tracks, ln)
	}
	const cutReserve = 80
	budget := MaxMarkdownBytes - len(footer) - cutReserve
	if len(head) > budget {
		head = cutRunes(head, budget-len("…\n")) + "…\n"
	}
	if len(tracks) == 0 {
		return head + footer
	}
	var b strings.Builder
	b.WriteString(head)
	b.WriteString("\n## Tracks\n\n")
	shown := 0
	for _, ln := range tracks {
		row := strconv.Itoa(ln.Position) + ". " + ln.Text + "\n"
		if b.Len()+len(row) > budget {
			break
		}
		b.WriteString(row)
		shown++
	}
	if shown < len(tracks) {
		b.WriteString("\n" + strconv.Itoa(len(tracks)-shown) + " more track(s) not shown: the list was cut at 8 KB.\n")
	}
	b.WriteString(footer)
	return b.String()
}

func (l Liner) summaryBlock() string {
	s := l.Summary
	var b strings.Builder
	b.WriteString("- " + plural(s.Songs, "song") + ", " + strconv.Itoa(s.Playable) + " playable, " + s.Duration + "\n")
	if len(s.Engines) > 0 {
		b.WriteString("- Engines: " + strings.Join(s.Engines, ", ") + "\n")
	}
	if len(s.Angles) > 0 {
		b.WriteString("- Angles: " + strings.Join(s.Angles, ", ") + "\n")
	}
	if len(s.Families) > 0 {
		b.WriteString("- Sound: " + strings.Join(s.Families, ", ") + "\n")
	}
	for _, n := range l.Notices {
		b.WriteString("- " + n + "\n")
	}
	return b.String()
}

// Description is the tape DESCRIPTION: the summary and the notices on one paragraph, at most MaxDescriptionChars characters.
// It never carries the track lines, so no cost, session or Muse text can reach it.
func (l Liner) Description() string {
	s := l.Summary
	parts := []string{plural(s.Songs, "song") + ", " + s.Duration}
	if len(s.Engines) > 0 {
		parts = append(parts, "Engines: "+strings.Join(s.Engines, ", "))
	}
	if len(s.Angles) > 0 {
		parts = append(parts, "Angles: "+strings.Join(s.Angles, ", "))
	}
	if len(s.Families) > 0 {
		parts = append(parts, "Sound: "+strings.Join(s.Families, ", "))
	}
	d := strings.Join(parts, ". ") + "."
	if len(l.Notices) > 0 {
		d += " " + strings.Join(l.Notices, " ")
	}
	d = oneLine(d)
	if utf8.RuneCountInString(d) > MaxDescriptionChars {
		r := []rune(d)
		d = string(r[:MaxDescriptionChars-1]) + "…"
	}
	return d
}

// cutRunes returns the longest prefix of s that is at most n bytes and ends on a character boundary.
func cutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
