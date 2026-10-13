package yue2

import (
	"strings"

	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// Slug is an ASCII [a-z0-9_] filename-safe form of s (D-M1-3): NFKD, combining marks removed, a fixed
// fold table, lower-case, runs of anything else collapsed to "_", trimmed, at most 80 bytes.
func Slug(s string) string {
	folded := strings.ToLower(textfold.Fold(s))
	var b strings.Builder
	under := true // suppress leading "_"
	for _, r := range folded {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			under = false
		} else if !under {
			b.WriteByte('_')
			under = true
		}
	}
	out := strings.TrimRight(b.String(), "_")
	if len(out) > 80 {
		out = strings.TrimRight(out[:80], "_")
	}
	return out
}
