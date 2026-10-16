package songs

import (
	"regexp"
	"strings"
)

var angleRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// AngleSlug normalises s into an angle (D-L1-7, C90.3): lower-case, '_' and spaces become '-', everything outside
// [a-z0-9-] is stripped, runs of '-' collapse, leading and trailing '-' are trimmed, the result is cut to 32 bytes
// (and re-trimmed) and is ok iff it matches ^[a-z][a-z0-9-]*$.
func AngleSlug(s string) (string, bool) {
	s = strings.ToLower(s)
	var b strings.Builder
	last := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' || c == ' ' || c == '\t' {
			c = '-'
		}
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-':
			if last == '-' {
				continue
			}
		default:
			continue
		}
		b.WriteByte(c)
		last = c
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 32 {
		out = strings.Trim(out[:32], "-")
	}
	return out, angleRe.MatchString(out)
}
