package api

import (
	"encoding/json"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/board"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/songview"
)

// Wire mappers of the library lane (L1-B5): service values -> the frozen L1 wire types of types_l1.go. Principals go
// through RefFor/KeyFor for the viewer; Muse text only reaches the objects C99 marks.

func museWire(v auth.Principal, m muses.Muse) Muse {
	return Muse{ID: m.ID, Kind: m.Kind, Title: m.Title, Artist: m.Artist, Brief: m.Brief, Source: m.Source,
		CreatedBy: KeyFor(v, m.CreatedBy), UpdatedAt: m.UpdatedAt, Revision: m.Revision, Hidden: m.HiddenAt != nil}
}

func coverageWire(c songview.Coverage) MuseCoverage {
	return MuseCoverage{Songs: c.Songs, Draft: c.Draft, Rendered: c.Rendered, Judged: c.Judged, Picked: c.Picked, QueuedJobs: c.QueuedJobs}
}

func songSummaryWire(s songs.Summary) SongSummary {
	out := SongSummary{ID: s.ID, Title: s.Title, Angle: s.Angle, MuseID: s.MuseID, State: string(s.State), Takes: s.Takes,
		DraftID: s.DraftID, RootTrackID: s.RootTrackID, QueuedJobs: s.QueuedJobs, OpenProposals: s.OpenProposals,
		Hidden: s.Hidden, Archived: s.Archived, UpdatedAt: s.UpdatedAt}
	if s.Pick != nil {
		out.Pick = &SongPickRef{TakeID: s.Pick.TakeID, Source: s.Pick.Source}
	}
	return out
}

func songCoreWire(c songs.Core) SongCore {
	return SongCore{ID: c.ID, Title: c.Title, Angle: c.Angle, AngleSource: c.AngleSource, Origin: c.Origin, Revision: c.Revision,
		State: string(c.State), Hidden: c.HiddenAt != nil, Archived: c.Archived, QueuedJobs: c.QueuedJobs, CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt, RootTrackID: c.RootTrackID, RootTrackIDs: c.RootTrackIDs, DraftID: c.DraftID,
		Retired: c.RetiredAt != nil, RetiredInto: c.RetiredInto}
}

func sessionWire(s sessions.Summary) SessionSummary {
	return SessionSummary{ID: s.ID, Kind: s.Kind, Name: s.Name, StartedAt: s.StartedAt, EndedAt: s.EndedAt, Hidden: s.HiddenAt != nil,
		Revision: s.Revision, Drafts: s.Drafts, Takes: s.Takes, Songs: s.Songs}
}

func kindOfKey(key string) string {
	kind, _, _ := strings.Cut(key, ":")
	return kind
}

func svPick(v auth.Principal, p *picks.PickView) *PickView {
	if p == nil {
		return nil
	}
	out := &PickView{TakeID: p.TakeID, Source: string(p.Source), PinnedTakeID: p.PinnedTakeID, PinIneligible: p.PinIneligible, PinnedAt: p.PinnedAt,
		Evidence: RawRequest(p.Evidence), ChangedAt: p.ChangedAt, LastEventID: p.LastEventID, Seq: p.Seq, Pending: p.Pending, OpenProposals: p.OpenProposals}
	if p.PinnedBy != nil {
		kind, name := kindOfKey(*p.PinnedBy), *p.PinnedBy
		if p.PinnedByKind != nil {
			kind = *p.PinnedByKind
		}
		if p.PinnedByName != nil {
			name = *p.PinnedByName
		}
		ref := RefFor(v, *p.PinnedBy, kind, name)
		out.PinnedBy = &ref
	}
	if len(out.Evidence) == 0 {
		out.Evidence = RawRequest(json.RawMessage(`{}`))
	}
	return out
}

func svProposal(v auth.Principal, p picks.Proposal) PickProposal {
	out := PickProposal{ID: p.ID, SongID: p.SongID, TakeID: p.TakeID, Rationale: p.Rationale, State: p.State, CreatedAt: p.CreatedAt,
		ProposedBy: RefFor(v, p.PrincipalID, kindOfKey(p.PrincipalID), p.PrincipalName), DecidedAt: p.DecidedAt}
	if p.DecidedBy != nil {
		k := KeyFor(v, *p.DecidedBy)
		out.DecidedBy = &k
	}
	return out
}

func svEvent(v auth.Principal, e picks.PickEvent) PickEvent {
	ev := RawRequest(e.Evidence)
	if len(ev) == 0 {
		ev = RawRequest(json.RawMessage(`{}`))
	}
	return PickEvent{Cursor: e.Cursor, ID: e.ID, SongID: e.SongID, Seq: e.Seq, At: e.At, FromTakeID: e.FromTakeID, ToTakeID: e.ToTakeID,
		FromSource: string(e.FromSource), ToSource: string(e.ToSource), Reason: e.Reason, Actor: RefFor(v, e.Actor, e.ActorKind, e.ActorName), Evidence: ev}
}

func songViewWire(v auth.Principal, w songview.View) SongView {
	out := SongView{
		Song: SongCore{ID: w.Song.ID, Title: w.Song.Title, Angle: w.Song.Angle, AngleSource: w.Song.AngleSource, Origin: w.Song.Origin, Revision: w.Song.Revision,
			State: string(w.Song.State), Hidden: w.Song.Hidden, Archived: w.Song.Archived, QueuedJobs: w.Song.QueuedJobs, CreatedAt: w.Song.CreatedAt,
			UpdatedAt: w.Song.UpdatedAt, RootTrackID: w.Song.RootTrackID, RootTrackIDs: w.Song.RootTrackIDs, DraftID: w.Song.DraftID,
			Retired: w.Song.Retired, RetiredInto: w.Song.RetiredInto},
		Takes: []TakeCard{}, TakesTotal: w.TakesTotal, Pick: svPick(v, w.Pick), Proposals: []PickProposal{}, Events: []PickEvent{}, Sessions: []SongSession{}, Pending: w.Pending,
		Records: []SongRecordRef{}, Crates: []SongCrateRef{},
		Muse: w.Muse,
	}
	if w.Muse != nil {
		m := *w.Muse
		out.Muse = &m
	}
	if d := w.Draft; d != nil {
		out.Draft = &SongDraft{ID: d.ID, Status: d.Status, Revision: d.Revision, Label: d.Label, RequestID: d.RequestID, GroupKey: d.GroupKey}
	}
	for _, c := range w.Takes {
		tc := TakeCard{ID: c.ID, Title: c.Title, Status: c.Status, Generation: c.Generation, ParentTrackID: c.ParentTrackID, Origin: c.Origin, DurationS: c.DurationS,
			RecordedAt: c.RecordedAt, Lossy: c.Lossy, RecipeKnown: c.RecipeKnown, Stars: c.Stars, CriticStars: c.CriticStars, IsPick: c.IsPick, Pinned: c.Pinned,
			HasEars: c.HasEars, Flags: c.Flags, CostDisplayUSD: c.CostDisplayUSD}
		if tc.Flags == nil {
			tc.Flags = []string{}
		}
		if c.Ladder != nil {
			tc.Ladder = &TakeLadder{U: c.Ladder.U, SD: c.Ladder.SD, NDuels: c.Ladder.NDuels}
		}
		if c.RequestedBPM != nil || c.MeasuredBPM != nil {
			tc.Tempo = &TakeTempo{RequestedBPM: c.RequestedBPM, MeasuredBPM: c.MeasuredBPM}
		}
		out.Takes = append(out.Takes, tc)
	}
	for _, p := range w.Proposals {
		out.Proposals = append(out.Proposals, svProposal(v, p))
	}
	for _, e := range w.Events {
		out.Events = append(out.Events, svEvent(v, e))
	}
	out.Sounds = SongSounds{TaxonomyVersion: w.Sounds.TaxonomyVersion, Facets: map[string][]SoundFacetEntry{}, Unmapped: w.Sounds.Unmapped}
	if out.Sounds.Unmapped == nil {
		out.Sounds.Unmapped = []string{}
	}
	for f, es := range w.Sounds.Facets {
		for _, e := range es {
			out.Sounds.Facets[f] = append(out.Sounds.Facets[f], SoundFacetEntry{Value: e.Value, Origin: e.Origin})
		}
	}
	if t := w.Sounds.Tempo; t != nil {
		out.Sounds.Tempo = &TempoDetail{RequestedBPM: t.RequestedBPM, PlanBPM: t.PlanBPM, MeasuredBPM: t.MeasuredBPM, Basis: t.Basis, Mismatch: t.Mismatch, Octave: t.Octave}
	}
	for _, s := range w.Sessions {
		out.Sessions = append(out.Sessions, SongSession{ID: s.ID, Kind: s.Kind, Name: s.Name})
	}
	if l := w.Lineage; l != nil {
		out.Lineage = &SongLineage{RootTrackID: l.RootTrackID, Generations: l.Generations, ChildrenTotal: l.ChildrenTotal}
	}
	return out
}

func boardWire(b board.Board) Board {
	out := Board{GeneratedAt: b.GeneratedAt, TaxonomyVersion: b.TaxonomyVersion, Columns: []BoardColumn{}, Rows: []BoardRow{}, NextCursor: b.NextCursor,
		Totals: BoardTotals{Muses: b.Totals.Muses, Songs: b.Totals.Songs, Draft: b.Totals.Draft, Rendered: b.Totals.Rendered, Judged: b.Totals.Judged, Picked: b.Totals.Picked, Queued: b.Totals.Queued}}
	for _, c := range b.Columns {
		out.Columns = append(out.Columns, BoardColumn{Angle: c.Angle, Songs: c.Songs})
	}
	for _, r := range b.Rows {
		row := BoardRow{Muse: r.Muse, SongsTotal: r.SongsTotal, Counts: BoardCounts{Draft: r.Counts.Draft, Rendered: r.Counts.Rendered, Judged: r.Counts.Judged, Picked: r.Counts.Picked, Queued: r.Counts.Queued},
			Cells: []BoardCell{}, ChipsMore: r.ChipsMore, NextUnassignedCursor: r.NextUnassignedCursor}
		for _, c := range r.Cells {
			cell := BoardCell{Angle: c.Angle, Chips: []SongChip{}}
			for _, ch := range c.Chips {
				cell.Chips = append(cell.Chips, SongChip{ID: ch.ID, Title: ch.Title, State: ch.State, Takes: ch.Takes, PickSource: ch.PickSource, PickTakeID: ch.PickTakeID,
					Queued: ch.Queued, DraftID: ch.DraftID, TempoMismatch: ch.TempoMismatch})
			}
			row.Cells = append(row.Cells, cell)
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

func songStates(in []string) []songs.State {
	out := make([]songs.State, len(in))
	for i, s := range in {
		out[i] = songs.State(s)
	}
	return out
}

// songFilter builds the Song filter of list_songs from its inputs (the facet selections are validated here).
func songFilter(in SongFilterIn) (songs.ListFilter, error) {
	sounds, err := songs.ParseSoundFilters(in.Sound)
	if err != nil {
		return songs.ListFilter{}, err
	}
	return l2SongFilter(in, songs.ListFilter{MuseID: in.MuseID, NoMuse: in.NoMuse, Angle: in.Angle, States: songStates(in.State), Queued: in.Queued.ptr(), SessionID: in.SessionID,
		Sounds: sounds, DraftID: in.DraftID, TrackID: in.TrackID, HasOpenProposals: in.HasOpenProposals.ptr(), TempoMismatch: in.TempoMismatch.ptr(), Q: in.Q,
		HasPick: in.HasPick.ptr(), IncludeHidden: in.IncludeHidden, IncludeArchived: in.IncludeArchived})
}
