package lineage

import (
	"strconv"
	"strings"
	"unicode"
)

// MaxTitleRunes is the longest track and job title.
const MaxTitleRunes = 200

// TitleIn describes a take for DefaultTitle (D-M3-27).
type TitleIn struct {
	Explicit    string // item.title
	Variant     bool   // new_seed, *_edit and from_template takes: parent title + " · s<seed>"
	ParentTitle string
	FromDraft   bool   // a from_draft take
	Label       string // the draft's label (its own-seed take only)
	HasMother   bool   // from_draft with a mother track
	RequestID   string
	Seed        int64
}

// DefaultTitle is the title of a take: item.title, else the parent's title + " · s<seed>" (variants), else the
// mother's title + " · <draft label or request id>" (drafts with a mother), else the humanised request id.
// The result is cut to 200 runes at a rune boundary.
func DefaultTitle(in TitleIn) string {
	var t string
	switch {
	case strings.TrimSpace(in.Explicit) != "":
		t = in.Explicit
	case in.Variant && in.ParentTitle != "":
		t = in.ParentTitle + " · s" + strconv.FormatInt(in.Seed, 10)
	case in.FromDraft && in.HasMother && in.ParentTitle != "":
		l := in.Label
		if l == "" {
			l = Humanise(in.RequestID)
		}
		t = in.ParentTitle + " · " + l
	default:
		t = Humanise(in.RequestID)
	}
	return CutRunes(t, MaxTitleRunes)
}

// Humanise turns a request id into words: separators become spaces and each word starts upper case
// ("syn-a-s1101" becomes "Syn A S1101").
func Humanise(id string) string {
	words := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	if len(words) == 0 {
		return id
	}
	return strings.Join(words, " ")
}

// CutRunes cuts s to at most n runes at a rune boundary.
func CutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
