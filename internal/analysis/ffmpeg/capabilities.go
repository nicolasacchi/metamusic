package ffmpeg

import (
	"fmt"
	"regexp"

	"github.com/nicolasacchi/metamusic/internal/analysis"
)

// hasName reports whether listing mentions name as a whole word (a listing
// line is "<flags> <name> <description>").
func hasName(listing []byte, name string) bool {
	return regexp.MustCompile(`(?m)\s` + regexp.QuoteMeta(name) + `\s`).Match(listing)
}

func (f *FFmpeg) require(filters, encoders []string) error {
	for _, n := range filters {
		if !hasName(f.filters, n) {
			return fmt.Errorf("%w: filter %s", analysis.ErrToolMissing, n)
		}
	}
	for _, n := range encoders {
		if !hasName(f.encoders, n) {
			return fmt.Errorf("%w: encoder %s", analysis.ErrToolMissing, n)
		}
	}
	return nil
}

// RequireClips checks what the clip service needs (C51.7): the filters atrim,
// asetpts, afade, aresample, volume, ebur128 and ametadata, and the flac encoder.
// The error wraps analysis.ErrToolMissing and names the first missing item.
func (f *FFmpeg) RequireClips() error {
	return f.require([]string{"atrim", "asetpts", "afade", "aresample", "volume", "ebur128", "ametadata"}, []string{"flac"})
}

// RequireTierA checks what the Tier A analyzers need: the filters asetnsamples,
// astats, silencedetect, ametadata, aformat and showspectrumpic, and the png encoder.
func (f *FFmpeg) RequireTierA() error {
	return f.require([]string{"asetnsamples", "astats", "silencedetect", "ametadata", "aformat", "showspectrumpic"}, []string{"png"})
}
