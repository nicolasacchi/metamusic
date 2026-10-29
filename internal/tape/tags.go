package tape

import (
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
)

// TapeVersion is the format tag written as MM_TAPE_VERSION and hashed into the input key. No revision, tape id or export date
// is ever written: they differ between otherwise identical exports.
const TapeVersion = "mm-tape/1"

const (
	// ReplayGainReferenceLUFS is the ReplayGain 2.0 reference loudness: the whole-tape gain tag = reference - target.
	ReplayGainReferenceLUFS = -18.0
	maxTagValue             = 1024
)

// ownedTagKeys are the keys BuildTags and the pipeline write themselves; a snapshot tag with one of them is dropped.
var ownedTagKeys = map[string]bool{
	"DESCRIPTION": true, "REPLAYGAIN_ALBUM_GAIN": true, "REPLAYGAIN_ALBUM_PEAK": true, cueKey: true,
}

func owned(key string) bool {
	return ownedTagKeys[key] || strings.HasPrefix(key, "MM_") || strings.HasPrefix(key, "CHAPTER")
}

// TagsInput is what BuildTags needs: the snapshot (its Tags, Title, Description, RecordID), the planned items and the target.
type TagsInput struct {
	Snapshot     Snapshot
	Items        []PlannedItem
	TargetLUFS   float64
	IncludeLiner bool
}

// BuildTags returns the tag set of a tape (C128.4 step 6) that is known before rendering, in a fixed order, so it can go
// into the input key and Job.Tags: the snapshot's own tags ("TITLE", "ALBUM", "ARTIST", "DATE", "GENRE" and the export extras; TITLE and
// "ALBUM" default to the Record title and ARTIST to "metamusic"), then DESCRIPTION (the liner text when IncludeLiner, plus the
// lossy line), REPLAYGAIN_ALBUM_GAIN, and the MM_* provenance. REPLAYGAIN_ALBUM_PEAK is added by the pipeline after the
// render. The result is the same for the same inputs.
func BuildTags(in TagsInput) []flacmeta.Comment {
	var out []flacmeta.Comment
	have := map[string]bool{}
	for _, c := range in.Snapshot.Tags {
		if !flacmeta.ValidKey(c.Key) || owned(c.Key) {
			continue
		}
		out = append(out, flacmeta.Comment{Key: c.Key, Value: cleanTag(c.Value)})
		have[c.Key] = true
	}
	for _, d := range []struct{ k, v string }{{"TITLE", in.Snapshot.Title}, {"ALBUM", in.Snapshot.Title}, {"ARTIST", cuePerformer}} {
		if !have[d.k] {
			out = append(out, flacmeta.Comment{Key: d.k, Value: cleanTag(d.v)})
		}
	}
	lossy, resampled, upmixed := 0, 0, 0
	for _, it := range in.Items {
		if it.Item.Lossy {
			lossy++
		}
		if it.Resampled {
			resampled++
		}
		if it.Upmixed {
			upmixed++
		}
	}
	desc := ""
	if in.IncludeLiner {
		desc = in.Snapshot.Description
	}
	if lossy > 0 {
		// the lossy line is never the part that is cut
		line := fmt.Sprintf("Contains lossy sources (%d of %d tracks)", lossy, len(in.Items))
		desc = capTag(cleanTag(desc), maxTagValue-len(line)-1)
		if desc != "" {
			desc += "\n"
		}
		desc += line
	} else {
		desc = cleanTag(desc)
	}
	if desc != "" {
		out = append(out, flacmeta.Comment{Key: "DESCRIPTION", Value: desc})
	}
	out = append(out,
		flacmeta.Comment{Key: "REPLAYGAIN_ALBUM_GAIN", Value: fmt.Sprintf("%.2f dB", ReplayGainReferenceLUFS-in.TargetLUFS)},
		flacmeta.Comment{Key: "MM_RECORD_ID", Value: in.Snapshot.RecordID},
		flacmeta.Comment{Key: "MM_TAPE_VERSION", Value: TapeVersion},
		flacmeta.Comment{Key: "MM_TARGET_LUFS", Value: fmt.Sprintf("%.1f", in.TargetLUFS)},
		flacmeta.Comment{Key: "MM_CONTAINS_LOSSY_SOURCES", Value: fmt.Sprint(lossy > 0)},
		flacmeta.Comment{Key: "MM_SOURCES", Value: fmt.Sprintf("lossless=%d;lossy=%d;resampled=%d;upmixed=%d", len(in.Items)-lossy, lossy, resampled, upmixed)},
	)
	return out
}

// PeakTag is the REPLAYGAIN_ALBUM_PEAK comment for a post-gain sample peak given in 24-bit units: linear, six decimals, 1.0 =
// full scale.
func PeakTag(peak int32) flacmeta.Comment {
	return flacmeta.Comment{Key: "REPLAYGAIN_ALBUM_PEAK", Value: fmt.Sprintf("%.6f", float64(peak)/(1<<23))}
}

// cleanTag makes a tag value safe: invalid UTF-8 and control characters other than newline become spaces, the ends are trimmed
// and the value is capped at 1 KiB on a rune boundary.
func cleanTag(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return ' '
		}
		return r
	}, s)
	return capTag(strings.TrimSpace(s), maxTagValue)
}

// capTag cuts s to at most n bytes on a rune boundary.
func capTag(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := max(n, 0)
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut])
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
