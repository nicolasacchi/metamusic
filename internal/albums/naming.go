package albums

import (
	"context"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// applyNames sets SortName on every auto-album and, only with subsonic_muse_names on, replaces the neutral name of each muse
// album by the Muse's label (title, or title · artist for a reference Muse; the directory formats it). The flag is read
// first and MuseDirectory.Labels is not called otherwise (D-L2-16, TestAlbums_MuseLabelsNotCalledWhenOff). A Muse the
// directory has no label for keeps its neutral name.
func (s *Service) applyNames(ctx context.Context, albs []autoAlbum) error {
	var muse []string
	for _, a := range albs {
		if a.MuseID != nil {
			muse = append(muse, *a.MuseID)
		}
	}
	if len(muse) > 0 && s.set != nil {
		on, err := s.set.SubsonicMuseNames(ctx)
		if err != nil {
			return fmt.Errorf("albums: settings: %w", err)
		}
		if on {
			labels, err := s.muses.Labels(ctx, muse)
			if err != nil {
				return fmt.Errorf("albums: labels: %w", err)
			}
			for i := range albs {
				if albs[i].MuseID == nil {
					continue
				}
				if l := labels[*albs[i].MuseID]; l != "" {
					albs[i].Name = l
				}
			}
		}
	}
	for i := range albs {
		albs[i].SortName = textfold.Norm(albs[i].Name)
	}
	return nil
}
