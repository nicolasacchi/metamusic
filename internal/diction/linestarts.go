package diction

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/view"
)

// LineStarts answers the measured start of every lyric line of a take, in lyric order across sections (the order of the
// Subsonic lyric lines, C348). A line without a measured time carries a negative duration. ok is true only when the take has a
// tier_b_diction row, that is the asr basis; the proportional basis never counts (D-EARS-16), nor does a take without a row.
// A take the principal may not see answers library.ErrNotFound, indistinguishable from an unknown one.
func (s *Service) LineStarts(ctx context.Context, p auth.Principal, trackID string) ([]time.Duration, bool, error) {
	if err := s.wired(); err != nil {
		return nil, false, err
	}
	if err := library.Visible(ctx, s.o.Store.R, view.For(p), trackID); err != nil {
		return nil, false, err
	}
	ti, err := s.trackInfo(ctx, trackID)
	if errors.Is(err, errNoTrack) {
		return nil, false, ErrNotFound
	}
	if err != nil || ti.results == nil {
		return nil, false, err
	}
	timed := make(map[LineKey]time.Duration, len(ti.results))
	for _, r := range ti.results {
		if r.Timed {
			timed[LineKey{r.Section, r.Line}] = time.Duration(r.StartS * float64(time.Second)).Round(time.Millisecond)
		}
	}
	var out []time.Duration
	for _, sec := range yue2.LyricsSections(ti.lyrics) {
		for li := range sec.Lines {
			d, ok := timed[LineKey{sec.Idx, li}]
			if !ok {
				d = -1
			}
			out = append(out, d)
		}
	}
	return out, true, nil
}
