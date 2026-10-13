package ingest

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/analysis"
)

const (
	maxTitleRunes   = 200
	rgReferenceLUFS = -18.0
	rgMaxGainDB     = 24.0
	rgReferenceTag  = "-18.00 LUFS"
)

// sanitizeTag makes a tag value safe to write as one tags.txt line: invalid
// UTF-8 becomes U+FFFD, C0 controls (incl. NUL, TAB, CR, LF), DEL and C1
// controls become a space, and the result is trimmed.
func sanitizeTag(s string, maxRunes int) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		i += w
		switch {
		case r == utf8.RuneError && w == 1:
			r = '�'
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			r = ' '
		}
		if maxRunes > 0 && n >= maxRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// replayGain returns the track gain clamped to +-24 dB and the linear peak.
func replayGain(l analysis.Loudness) (gainDB, peak float64) {
	g := rgReferenceLUFS - l.IntegratedLUFS
	switch {
	case math.IsNaN(g) || g > rgMaxGainDB:
		g = rgMaxGainDB
	case g < -rgMaxGainDB:
		g = -rgMaxGainDB
	}
	p := math.Pow(10, l.TruePeakDBTP/20)
	if math.IsNaN(p) || math.IsInf(p, 0) {
		p = 0
	}
	return g, p
}

// replayGainStrings formats the two ReplayGain tag values.
func replayGainStrings(gainDB, peak float64) (string, string) {
	return fmt.Sprintf("%+.2f dB", gainDB), fmt.Sprintf("%.6f", peak)
}

// tagLines builds the eight C15 tags in order. A reference take (D-M2c-12) has
// no seed: MM_REFERENCE=1 takes MM_SEED's place.
func tagLines(req Request, gainDB, peak float64) []string {
	g, p := replayGainStrings(gainDB, peak)
	seedTag := struct{ k, v string }{"MM_SEED", strconv.FormatInt(req.Seed, 10)}
	if req.Reference {
		seedTag = struct{ k, v string }{"MM_REFERENCE", "1"}
	}
	kv := []struct{ k, v string }{
		{"TITLE", sanitizeTag(req.Title, maxTitleRunes)},
		{"MM_TAKE_ID", sanitizeTag(req.TrackID, 0)},
		{"MM_RECIPE_ID", sanitizeTag(req.RecipeID, 0)},
		{"MM_REQUEST_ID", sanitizeTag(req.RequestID, 0)},
		seedTag,
		{"REPLAYGAIN_TRACK_GAIN", g},
		{"REPLAYGAIN_TRACK_PEAK", p},
		{"REPLAYGAIN_REFERENCE_LOUDNESS", rgReferenceTag},
	}
	out := make([]string, len(kv))
	for i, e := range kv {
		out[i] = e.k + "=" + e.v
	}
	return out
}
