// Package spectro composes the annotated spectrogram JPEG (C50): ASCII
// folding, the Go-drawn overlay band and boundary lines, and the size/quality
// ladder that keeps the picture under the byte budget.
package spectro

import (
	"errors"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// Errors carry their stable code as a prefix; Code extracts it.
var (
	ErrGeometry = errors.New("SPECTROGRAM_GEOMETRY: ffmpeg picture does not match the expected layout")
	ErrTooLarge = errors.New("SPECTROGRAM_TOO_LARGE: no size and quality fits the byte budget")
)

// Code returns the stable error code of err, or "" when it is not a spectro error.
func Code(err error) string {
	switch {
	case errors.Is(err, ErrGeometry):
		return "SPECTROGRAM_GEOMETRY"
	case errors.Is(err, ErrTooLarge):
		return "SPECTROGRAM_TOO_LARGE"
	}
	return ""
}

// FoldASCII folds s with textfold.Fold, replaces control characters with a
// space and every other non-ASCII rune with '?'.
func FoldASCII(s string) string {
	var b strings.Builder
	for _, r := range textfold.Fold(s) {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteByte(' ')
		case r > 0x7e:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
