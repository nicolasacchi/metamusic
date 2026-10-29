package recordview

import (
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// planGains plans one gain per playable slot with the automatic target (C128.2, D-L2-18) and returns the target.
func planGains(d *data) (float64, map[string]tape.GainPlan) {
	var ids []string
	var ms []tape.Measure
	for _, r := range d.rs {
		if !playable(r) {
			continue
		}
		ids = append(ids, r.Slot.ID)
		ms = append(ms, d.loud[r.Take.ID].Measure)
	}
	target, plans := tape.PlanGains(ms, nil)
	out := make(map[string]tape.GainPlan, len(plans))
	for i, p := range plans {
		out[ids[i]] = p
	}
	return target, out
}

// gainDB is the plan gain of a slot in dB, nil when the slot has no measured Take.
func gainDB(g tape.GainPlan, ok bool) *float64 {
	if !ok || g.Status == tape.GainUnmeasured {
		return nil
	}
	v := float64(g.GainCDB) / 100
	return &v
}

// slotWarnings are a resolved slot's warnings as the View shows them: undecided_pick only matters for a published Record
// (R-a counts it the same way in the list summaries).
func slotWarnings(rec records.Record, r records.ResolvedSlot) []string {
	out := []string{}
	for _, w := range r.Warnings {
		if w == records.WarnUndecidedPick && rec.Status != records.StatusPublished {
			continue
		}
		out = append(out, w)
	}
	return out
}

// whyOf is records.WhyText of the Song's current song_picks row, nil when it has no wording (no row, or source none).
func whyOf(d *data, songID string) *string {
	row, ok := d.picks[songID]
	if !ok {
		return nil
	}
	if w := records.WhyText(row.Source, row.Evidence); w != "" {
		return &w
	}
	return nil
}

// slotViews builds the SlotViews, the Record warnings and the totals.
func slotViews(d *data) ([]SlotView, []Warning, Totals) {
	_, gains := planGains(d)
	out := make([]SlotView, 0, len(d.rs))
	var warns []Warning
	var tot Totals
	tot.Slots = len(d.rs)
	for _, r := range d.rs {
		sv := SlotView{ID: r.Slot.ID, Position: r.Slot.Position, Side: r.Slot.Side, Mode: r.Mode, State: r.State, PinDiffers: r.PinDiffers,
			Warnings: slotWarnings(d.rec, r), Why: whyOf(d, r.Slot.SongID)}
		sv.Song = SongRef{ID: r.Slot.SongID}
		if b, ok := d.briefs[r.Slot.SongID]; ok {
			sv.Song = SongRef{ID: b.ID, Title: b.Title, Angle: b.Angle, State: b.State, Retired: b.Retired, RetiredInto: b.RetiredInto}
		}
		if row, ok := d.picks[r.Slot.SongID]; ok {
			sv.Pick = &PickRef{TakeID: row.TakeID, Source: row.Source}
		}
		if r.Take != nil {
			tb := TakeBrief{ID: r.Take.ID, Status: r.Take.Status, DurationS: r.Take.DurationS, Lossy: r.Take.Lossy}
			if ref, ok := d.refs[r.Take.ID]; ok {
				tb.Title, tb.Status, tb.DurationS, tb.Lossy, tb.RecipeKnown = ref.Title, ref.Status, ref.DurationS, ref.Lossy, ref.RecipeKnown
			}
			tb.Stars = d.facts[r.Take.ID].Stars
			sv.Take = &tb
		}
		if playable(r) {
			g, ok := gains[r.Slot.ID]
			sv.GainDB = gainDB(g, ok)
			tot.Playable++
			tot.DurationS += r.Take.DurationS
			if r.Take.Lossy {
				tot.Lossy++
			}
		}
		for _, w := range r.Warnings {
			if w == records.WarnUndecidedPick {
				tot.Undecided++
			}
		}
		for _, w := range sv.Warnings {
			id := r.Slot.ID
			warns = append(warns, Warning{SlotID: &id, Code: w})
		}
		out = append(out, sv)
	}
	tot.Warnings = len(warns)
	return out, warns, tot
}
