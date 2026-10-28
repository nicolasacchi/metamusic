package albums_test

import (
	"context"
	"errors"
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

// TestStubsA_FrozenMethodSet: List and Get answer errors.ErrUnsupported and Token is the empty token until lane A. Lane A
// deletes stub_a.go and this test; the interface pin moves into its tests.
func TestStubsA_FrozenMethodSet(t *testing.T) {
	t.Parallel()
	var _ interface {
		List(context.Context, ...albums.Kind) ([]albums.Album, error)
		Get(context.Context, string) (albums.Album, []albums.Song, error)
		Token(context.Context) (string, error)
	} = (*albums.Service)(nil)
	s := albums.New(nil, nil, nil, nil, nil)
	if _, err := s.List(context.Background()); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("List: %v", err)
	}
	if _, _, err := s.Get(context.Background(), "rec-x"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Get: %v", err)
	}
	if tok, err := s.Token(context.Background()); tok != "" || err != nil {
		t.Errorf("Token: %q %v", tok, err)
	}
	if albums.ErrNotFound == nil {
		t.Error("ErrNotFound is part of the frozen contract")
	}
}
