package subsonic

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/albums"
)

func TestParseID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		kind idKind
		raw  string
	}{
		{"01900000-0000-7000-8000-000000000001", kindSong, "01900000-0000-7000-8000-000000000001"},
		{"al-01900000", kindAlbum, "01900000"},
		{"tr-01900000", kindTrack, "01900000"},
		{"ar-metamusic", kindArtist, ""},
		{"", kindBad, ""},
		{"al-", kindBad, ""},
		{"tr-", kindBad, ""},
		{"rec-01900000", kindRecord, "01900000"},
		{"rec-", kindBad, ""},
		{"ar-other", kindBad, ""},
	} {
		if kind, raw := parseID(tc.in); kind != tc.kind || raw != tc.raw {
			t.Errorf("parseID(%q) = %v %q, want %v %q", tc.in, kind, raw, tc.kind, tc.raw)
		}
	}
	if albumID("x") != "al-x" || coverTrackID("y") != "tr-y" || artistName != "metamusic" || artistID != "ar-"+artistName {
		t.Error("id helpers")
	}
	if prefixRecord != albums.PrefixRecord {
		t.Error("the Record album prefix must be albums.PrefixRecord")
	}
	if k, raw := parseID(albumID("r1")); k != kindAlbum || raw != "r1" {
		t.Error("albumID does not round-trip")
	}
}
