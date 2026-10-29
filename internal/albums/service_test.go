package albums_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/albums"
)

// TestNeutralName_Table: the neutral name is "Inspiration " and the frozen ordinal zero-padded to three digits, more when needed;
// it carries no Muse text (D-L2-15).
func TestNeutralName_Table(t *testing.T) {
	t.Parallel()
	for in, want := range map[int]string{1: "Inspiration 001", 7: "Inspiration 007", 42: "Inspiration 042", 999: "Inspiration 999", 1000: "Inspiration 1000"} {
		if got := albums.NeutralName(in); got != want {
			t.Errorf("NeutralName(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestIDs_Frozen pins the album id forms the Subsonic lanes build on.
func TestIDs_Frozen(t *testing.T) {
	t.Parallel()
	if albums.PrefixRecord != "rec-" || albums.PrefixMuse != "mus-" || albums.IDUnfiled != "unf" || albums.NameUnfiled != "Unfiled" {
		t.Error("an album id form changed")
	}
	if albums.KindRecord != "record" || albums.KindMuse != "muse" || albums.KindUnfiled != "unfiled" {
		t.Error("an album kind changed")
	}
}

// TestService_MethodSet pins the frozen method set and the sentinel error.
func TestService_MethodSet(t *testing.T) {
	t.Parallel()
	var _ interface {
		List(context.Context, ...albums.Kind) ([]albums.Album, error)
		Get(context.Context, string) (albums.Album, []albums.Song, error)
		Token(context.Context) (string, error)
	} = (*albums.Service)(nil)
	if albums.ErrNotFound == nil {
		t.Error("ErrNotFound is part of the frozen contract")
	}
}
