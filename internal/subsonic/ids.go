package subsonic

import "strings"

// Id forms (D-M4a-11): songs are the bare track id; albums are lineages `al-<root id>`; cover art ids are
// `al-<root id>` or `tr-<track id>`; the one artist is `ar-metamusic`. The prefixes cannot collide with UUIDs.
const (
	prefixAlbum = "al-"
	prefixTrack = "tr-"
	// prefixRecord names a Record album (L2, D-L2-35): `rec-<record id>`. It is the catalog's own id form (albums.PrefixRecord).
	prefixRecord = "rec-"
	// prefixMuse and idUnfiled name the auto-albums (L2 A2/S2): `mus-<muse id>` and the one `unf`; the catalog's own forms.
	prefixMuse = "mus-"
	idUnfiled  = "unf"
	artistID   = "ar-metamusic"
	artistName = "metamusic"
)

type idKind int

const (
	kindSong   idKind = iota // a bare track id
	kindAlbum                // al-<root>
	kindTrack                // tr-<track> (cover art only)
	kindArtist               // ar-metamusic
	kindBad                  // empty or malformed
	kindRecord               // rec-<record id> (a published Record album)
	kindAuto                 // mus-<muse id> or unf (an auto-album); the raw part is the whole id
)

func albumID(root string) string    { return prefixAlbum + root }
func coverTrackID(id string) string { return prefixTrack + id }

// parseID classifies an id parameter and returns its raw (unprefixed) part.
func parseID(s string) (idKind, string) {
	switch {
	case s == "":
		return kindBad, ""
	case s == artistID:
		return kindArtist, ""
	case strings.HasPrefix(s, prefixAlbum):
		if raw := strings.TrimPrefix(s, prefixAlbum); raw != "" {
			return kindAlbum, raw
		}
		return kindBad, ""
	case strings.HasPrefix(s, prefixTrack):
		if raw := strings.TrimPrefix(s, prefixTrack); raw != "" {
			return kindTrack, raw
		}
		return kindBad, ""
	case strings.HasPrefix(s, prefixRecord):
		if raw := strings.TrimPrefix(s, prefixRecord); raw != "" {
			return kindRecord, raw
		}
		return kindBad, ""
	case s == idUnfiled:
		return kindAuto, s
	case strings.HasPrefix(s, prefixMuse):
		if strings.TrimPrefix(s, prefixMuse) != "" {
			return kindAuto, s
		}
		return kindBad, ""
	case strings.HasPrefix(s, "ar-"):
		return kindBad, ""
	}
	return kindSong, s
}
