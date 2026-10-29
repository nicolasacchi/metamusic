package recordview

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/records"
)

// defaultChanges is the changelog length of a View when ChangesLimit is 0.
const defaultChanges = 10

// Get assembles a Record for viewer. Muse text enters the View only through the liner, and only when o.Include has
// IncludeLiner, which only get_record passes (the five mutating operations pass none, D-L2-27).
func (s *Service) Get(ctx context.Context, viewer auth.Principal, id string, o GetOptions) (View, error) {
	_ = viewer // principals are rendered by the API layer (api.RefFor); the View carries keys
	rec, err := s.o.Records.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	rs, err := s.o.Records.Resolved(ctx, id)
	if err != nil {
		return View{}, err
	}
	has := func(inc string) bool { return slices.Contains(o.Include, inc) }

	var changes []records.Change
	if has(IncludeChanges) {
		limit := o.ChangesLimit
		if limit <= 0 {
			limit = defaultChanges
		}
		page, err := s.o.Records.Changes(ctx, records.ChangeFilter{RecordID: id, Order: "desc", Limit: min(limit, 50)})
		if err != nil {
			return View{}, err
		}
		changes = page.Items
	}
	d, err := s.load(ctx, rec, rs, takeIDsOf(changes))
	if err != nil {
		return View{}, err
	}
	v := View{Record: rec}
	v.Slots, v.Warnings, v.Totals = slotViews(d)
	if v.Cover, err = s.links(ctx, coverInput(rec, firstSongs(rs), d.briefs)); err != nil {
		return View{}, err
	}
	if v.Pending, err = s.pending(ctx, id); err != nil {
		return View{}, err
	}
	if has(IncludeLiner) {
		if v.Liner, err = s.linerView(ctx, d); err != nil {
			return View{}, err
		}
	}
	if has(IncludeEnergy) {
		v.Energy = energy(d)
	}
	if has(IncludeChanges) {
		v.Changes = changeViews(changes, d.title)
	}
	if has(IncludeTapes) && s.o.Tapes != nil {
		// errors.ErrUnsupported is the tape service before lane T2 lands (its Brief is a stub): a Record then has no tapes.
		if v.Tapes, err = s.o.Tapes.Brief(ctx, id, 10); err != nil && !errors.Is(err, errors.ErrUnsupported) {
			return View{}, fmt.Errorf("recordview: tapes: %w", err)
		}
	}
	if has(IncludePreview) {
		if v.Preview, err = s.preview(ctx, d); err != nil {
			return View{}, err
		}
	}
	return v, nil
}
