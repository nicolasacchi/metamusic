package songs

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTitleRunes is the column limit of songs.title.
const MaxTitleRunes = 200

var bareSlugRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// DefaultTitle is the title a draft Song starts with (D-L1-8). It never contains Muse text: the draft's label unless
// the label is empty, equals the angle or is a bare angle-like slug; then the humanised group key followed by " · "
// and the angle with '-' shown as a space (when there is one), or the humanised request id when the group key is empty.
func DefaultTitle(label, groupKey, requestID string, angle *string) string {
	label = strings.TrimSpace(label)
	bare := label == "" || bareSlugRe.MatchString(label) || (angle != nil && label == *angle)
	if !bare {
		return cutRunes(label, MaxTitleRunes)
	}
	if g := Humanise(groupKey); g != "" {
		if angle != nil && *angle != "" {
			g += " · " + strings.ReplaceAll(*angle, "-", " ")
		}
		return cutRunes(g, MaxTitleRunes)
	}
	if r := Humanise(requestID); r != "" {
		return cutRunes(r, MaxTitleRunes)
	}
	return "Untitled"
}

// Humanise turns a request id or group key into words: '_' becomes a space and every word starts upper-case
// ("ex_001_v1" -> "Ex 001 V1").
func Humanise(s string) string {
	words := strings.Fields(strings.ReplaceAll(s, "_", " "))
	for i, w := range words {
		r, n := utf8.DecodeRuneInString(w)
		words[i] = string(unicode.ToUpper(r)) + w[n:]
	}
	return strings.Join(words, " ")
}

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
