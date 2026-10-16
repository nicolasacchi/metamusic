package subsonic

import "strings"

// Id forms (D-M4a-11): songs are the bare track id; albums are lineages `al-<root id>`; cover art ids are
// `al-<root id>` or `tr-<track id>`; the one artist is `ar-metamusic`. The prefixes cannot collide with UUIDs.
const (
	prefixAlbum = "al-"
	prefixTrack = "tr-"
	artistID    = "ar-metamusic"
	artistName  = "metamusic"
)

type idKind int

const (
	kindSong   idKind = iota // a bare track id
	kindAlbum                // al-<root>
	kindTrack                // tr-<track> (cover art only)
	kindArtist               // ar-metamusic
	kindBad                  // empty or malformed
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
	case strings.HasPrefix(s, "ar-"):
		return kindBad, ""
	}
	return kindSong, s
}
