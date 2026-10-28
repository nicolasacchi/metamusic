package textfold

import "strings"

// Slug reduces s to ASCII [a-z0-9._-] of at most limit bytes, for file names (the tape filename, C128.7). It follows the
// rules of the exports package's unexported clean-filename helper: Fold (diacritics and the D-M1-3 table), lower-case, every
// other rune becomes '-', then leading and trailing '.' and '-' are trimmed, the result is cut to limit bytes and trimmed
// again. An input with nothing left returns "". Runs of '-' are not collapsed (the export helper does not either).
func Slug(s string, limit int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(Fold(s))) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), ".-")
	if limit >= 0 && len(out) > limit {
		out = strings.Trim(out[:limit], ".-")
	}
	return out
}
