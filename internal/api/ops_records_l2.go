package api

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/recordview"
)

// defaultRecordIncludes is get_record's default when no include is sent; every other Records operation includes none, so the
// liner (Muse text) is reachable through get_record alone (D-L2-27).
var defaultRecordIncludes = []string{recordview.IncludeLiner, recordview.IncludeTapes, recordview.IncludeChanges}

// recordKeyRef renders a stored Principal key for viewer. The Record tables keep the key only, so the key stands in for the name.
func recordKeyRef(v auth.Principal, key string) PrincipalRef {
	return RefFor(v, key, kindOfKey(key), key)
}

func recordWire(v auth.Principal, r records.Record) Record {
	out := Record{ID: r.ID, Title: r.Title, Subtitle: r.Subtitle, LinerNote: r.LinerNote, Status: string(r.Status), Revision: r.Revision,
		CoverRerolls: r.CoverRerolls, Hidden: r.HiddenAt != nil, CreatedBy: recordKeyRef(v, r.CreatedBy), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if r.ProposedBy != nil && r.ProposedAt != nil {
		note := ""
		if r.ProposalNote != nil {
			note = *r.ProposalNote
		}
		out.Proposal = &RecordProposal{Note: note, By: recordKeyRef(v, *r.ProposedBy), At: *r.ProposedAt}
	}
	if r.PublishedBy != nil && r.PublishedAt != nil {
		first := *r.PublishedAt
		if r.FirstPublishedAt != nil {
			first = *r.FirstPublishedAt
		}
		out.Published = &RecordPublication{By: recordKeyRef(v, *r.PublishedBy), At: *r.PublishedAt, FirstAt: first}
	}
	return out
}

func coverWire(c recordview.CoverRef) CoverLinks {
	return CoverLinks{URL512: c.URL512, URL1024: c.URL1024, ExpiresAt: c.ExpiresAt}
}

func changeWire(v auth.Principal, c recordview.ChangeView) ChangeView {
	out := ChangeView{Seq: c.Change.Seq, At: c.Change.At, Kind: c.Change.Kind, Text: c.Text, SlotID: c.Change.SlotID, Position: c.Change.Position,
		Reason: c.Change.Reason, Actor: RefFor(v, c.Change.Actor, c.Change.ActorKind, c.Change.ActorName)}
	if c.From != nil {
		out.From = &TakeLink{ID: c.From.ID, Title: c.From.Title}
	}
	if c.To != nil {
		out.To = &TakeLink{ID: c.To.ID, Title: c.To.Title}
	}
	if len(c.Change.Evidence) > 0 {
		var m map[string]any
		if json.Unmarshal(c.Change.Evidence, &m) == nil {
			out.Evidence = m
		}
	}
	return out
}

func slotWire(s recordview.SlotView) SlotView {
	out := SlotView{ID: s.ID, Position: s.Position, Side: s.Side, Mode: string(s.Mode), State: string(s.State), PinDiffers: s.PinDiffers, GainDB: s.GainDB, Why: s.Why,
		Song:     SlotSong{ID: s.Song.ID, Title: s.Song.Title, Angle: s.Song.Angle, State: s.Song.State, Retired: s.Song.Retired, RetiredInto: s.Song.RetiredInto},
		Warnings: s.Warnings}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	if t := s.Take; t != nil {
		out.Take = &TakeBrief{ID: t.ID, Title: t.Title, Status: t.Status, DurationS: t.DurationS, Lossy: t.Lossy, Stars: t.Stars, RecipeKnown: t.RecipeKnown}
	}
	if p := s.Pick; p != nil {
		out.Pick = &SlotPick{TakeID: p.TakeID, Source: p.Source}
	}
	return out
}

// recordViewWire maps a recordview.View to the wire shape, rendering principals for viewer.
func recordViewWire(v auth.Principal, w recordview.View) RecordView {
	out := RecordView{Record: recordWire(v, w.Record), Slots: make([]SlotView, len(w.Slots)), Cover: coverWire(w.Cover), Pending: w.Pending,
		Totals:   RecordTotals{Slots: w.Totals.Slots, Playable: w.Totals.Playable, DurationS: w.Totals.DurationS, Lossy: w.Totals.Lossy, Warnings: w.Totals.Warnings, Undecided: w.Totals.Undecided},
		Warnings: make([]RecordWarning, len(w.Warnings))}
	for i, s := range w.Slots {
		out.Slots[i] = slotWire(s)
	}
	for i, x := range w.Warnings {
		out.Warnings[i] = RecordWarning{SlotID: x.SlotID, Code: x.Code}
	}
	if l := w.Liner; l != nil {
		sum := map[string]any{}
		_ = json.Unmarshal(l.Summary, &sum)
		out.Liner = &RecordLiner{Markdown: l.Markdown, Summary: sum}
	}
	if e := w.Energy; e != nil {
		oe := &Energy{TargetLUFS: e.TargetLUFS, TotalDurationS: e.TotalDurationS, Slots: make([]EnergySlot, len(e.Slots))}
		for i, s := range e.Slots {
			oe.Slots[i] = EnergySlot{SlotID: s.SlotID, DurationS: s.DurationS, GainDB: s.GainDB, MeanLUFS: s.MeanLUFS, BPM: s.BPM, LUFS: s.LUFS}
			if oe.Slots[i].LUFS == nil {
				oe.Slots[i].LUFS = []float64{}
			}
		}
		out.Energy = oe
	}
	if w.Changes != nil {
		out.Changes = make([]ChangeView, len(w.Changes))
		for i, c := range w.Changes {
			out.Changes[i] = changeWire(v, c)
		}
	}
	if w.Tapes != nil {
		out.Tapes = make([]TapeBrief, len(w.Tapes))
		for i, t := range w.Tapes {
			out.Tapes[i] = TapeBrief{ID: t.ID, State: string(t.State), CreatedAt: t.CreatedAt, FinishedAt: t.FinishedAt, ContainsLossy: t.ContainsLossy, Bytes: t.Bytes, ExpiresAt: t.ExpiresAt}
		}
	}
	if p := w.Preview; p != nil {
		op := &RecordPreview{Chapters: make([]PreviewChapter, len(p.Chapters)), Tags: make([]PreviewTag, len(p.Tags)), Description: p.Description,
			Warnings: make([]TapeWarning, len(p.Warnings))}
		for i, c := range p.Chapters {
			op.Chapters[i] = PreviewChapter{Position: c.Position, Name: c.Name}
		}
		for i, t := range p.Tags {
			op.Tags[i] = PreviewTag{Key: t.Key, Value: t.Value}
		}
		for i, x := range p.Warnings {
			op.Warnings[i] = TapeWarning{SlotID: x.SlotID, Code: x.Code}
		}
		out.Preview = op
	}
	return out
}

// slotOps maps the wire operations to the service's. An absent position means the end for add (records.PositionEnd) and
// reaches the service as 0 for move, which refuses it (INVALID_POSITION); a stray 0 on the wire reaches it too (F2).
func slotOps(in []SlotOp) []records.SlotOp {
	out := make([]records.SlotOp, len(in))
	for i, o := range in {
		op := records.SlotOp{Op: o.Op, SongID: o.SongID, SlotID: o.SlotID, TakeID: o.TakeID, SlotIDs: o.SlotIDs,
			Side: records.OptString{Set: o.Side.Set, Null: o.Side.Null, Value: o.Side.Value}}
		switch {
		case o.Position != nil:
			op.Position = *o.Position
		case o.Op == "add":
			op.Position = records.PositionEnd
		}
		out[i] = op
	}
	return out
}

// registerRecordsL2 registers the eight Records operations of C129 (lane V2). The mutating operations answer the Record with an
// empty Include: no liner, so no Muse text reaches their responses, audit rows or idempotency rows (D-L2-27).
func registerRecordsL2(a huma.API, d Deps) {
	records := func() error {
		if d.Records == nil || d.RecordView == nil {
			return unavailable("records")
		}
		return nil
	}
	// view re-reads the Record after a write and answers it without any optional part.
	view := func(ctx context.Context, p auth.Principal, id string) (*recordViewOut, error) {
		v, err := d.RecordView.Get(ctx, p, id, recordview.GetOptions{})
		if err != nil {
			return nil, m2bErr(err, taskV2)
		}
		return &recordViewOut{Body: recordViewWire(p, v)}, nil
	}

	Register(a, huma.Operation{
		OperationID: "list_records", Method: "GET", Path: "/api/records",
		Summary:     "List Records",
		Description: "Records (ordered listening sequences of Songs) by status, title, or a Song they hold; newest first. Hidden ones only with include_hidden.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/records"},
		func(ctx context.Context, in *listRecordsIn) (*recordsOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			f := recordsListFilter(in)
			page, err := d.Records.List(ctx, f)
			if err != nil {
				return nil, m2bErr(err, taskV2)
			}
			p := auth.FromContext(ctx)
			ids := make([]string, len(page.Items))
			for i, it := range page.Items {
				ids[i] = it.ID
			}
			covers, err := d.RecordView.CoverRefs(ctx, p, ids)
			if err != nil {
				return nil, m2bErr(err, taskV2)
			}
			out := &recordsOut{}
			out.Body.Items = make([]RecordSummary, len(page.Items))
			for i, it := range page.Items {
				out.Body.Items[i] = RecordSummary{ID: it.ID, Title: it.Title, Subtitle: it.Subtitle, Status: string(it.Status), Slots: it.Slots, Playable: it.Playable,
					DurationS: it.DurationS, Revision: it.Revision, UpdatedAt: it.UpdatedAt, Hidden: it.Hidden, Warnings: it.Warnings, Cover: coverWire(covers[it.ID])}
			}
			out.Body.NextCursor, out.Body.Total = page.NextCursor, page.Total
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_record", Method: "GET", Path: "/api/records/{id}",
		Summary:     "Get a Record",
		Description: "One Record with its slots (resolved takes, warnings), totals and cover; include adds liner notes, energy, tapes, changes or the publish preview.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/records/:id"},
		func(ctx context.Context, in *getRecordIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			inc := in.Include
			if len(inc) == 0 {
				inc = slices.Clone(defaultRecordIncludes)
			}
			p := auth.FromContext(ctx)
			v, err := d.RecordView.Get(ctx, p, in.ID, recordview.GetOptions{Include: inc, ChangesLimit: in.ChangesLimit})
			if err != nil {
				return nil, m2bErr(err, taskV2)
			}
			return &recordViewOut{Body: recordViewWire(p, v)}, nil
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_record", Method: "POST", Path: "/api/records",
		Summary:     "Create a Record",
		Description: "Creates a draft Record, optionally with Songs (at most 60) as floating slots in order. Floating slots follow each Song's current Pick. Needs an Idempotency-Key.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/records", Idempotency: "create"},
		func(ctx context.Context, in *createRecordIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			p := auth.FromContext(ctx)
			rec, err := d.Records.Create(ctx, p, recordsNew(in))
			if err != nil {
				return nil, m2bErr(err, taskV2)
			}
			return view(ctx, p, rec.ID)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_record", Method: "PATCH", Path: "/api/records/{id}",
		Summary:     "Update a Record",
		Description: "Changes title, subtitle, liner note, hides or unhides, or re-rolls the cover, under optimistic locking: send the revision you read. A published Record is edited by a human only.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/records/:id"},
		func(ctx context.Context, in *updateRecordIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			p := auth.FromContext(ctx)
			if _, err := d.Records.Update(ctx, p, in.ID, recordsPatch(in)); err != nil {
				return nil, m2bErr(err, taskV2)
			}
			return view(ctx, p, in.ID)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "edit_record_slots", Method: "POST", Path: "/api/records/{id}/slots",
		Summary:     "Edit a Record's slots",
		Description: "Applies up to 50 slot operations in one transaction: add, remove, move, reorder, pin, unpin, set_side, pin_all, unpin_all. Send the revision you read; any error rolls everything back.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/records/:id"},
		func(ctx context.Context, in *editRecordSlotsIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			p := auth.FromContext(ctx)
			if _, err := d.Records.EditSlots(ctx, p, in.ID, in.Body.Revision, slotOps(in.Body.Ops)); err != nil {
				return nil, m2bErr(err, taskV2)
			}
			return view(ctx, p, in.ID)
		})

	Register(a, huma.Operation{
		OperationID: "get_record_changes", Method: "GET", Path: "/api/records/{id}/changes",
		Summary:     "Get a Record's changelog",
		Description: "The append-only changelog of a Record: edits, Pick-driven upgrades with their evidence, proposals and publications. Newest first unless order is asc; cursor is the last seq.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/records/:id"},
		func(ctx context.Context, in *getRecordChangesIn) (*recordChangesOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			items, next, err := d.RecordView.Changes(ctx, recordsChangeFilter(in))
			if err != nil {
				return nil, m2bErr(err, taskV2)
			}
			p := auth.FromContext(ctx)
			out := &recordChangesOut{}
			out.Body.Items = make([]ChangeView, len(items))
			for i, c := range items {
				out.Body.Items[i] = changeWire(p, c)
			}
			out.Body.NextCursor = next
			return out, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "propose_record", Method: "POST", Path: "/api/records/{id}/proposal",
		Summary:     "Propose a Record",
		Description: "Proposes a draft Record for a human to publish, with a rationale of up to 500 characters. Only a human publishes.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/records/:id"},
		func(ctx context.Context, in *proposeRecordIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			p := auth.FromContext(ctx)
			if _, err := d.Records.Propose(ctx, p, in.ID, in.Body.Revision, in.Body.Rationale); err != nil {
				return nil, m2bErr(err, taskV2)
			}
			return view(ctx, p, in.ID)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "publish_record", Method: "POST", Path: "/human/api/records/{id}/publish",
		Summary:     "Publish or unpublish a Record",
		Description: "Human only. published true publishes a draft or proposed Record (Subsonic clients and tapes then see it); false sends a proposal back to draft or unpublishes. Send the revision you reviewed.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "exclude:human-only", UI: "/human/records/:id/publish", HumanOnly: true},
		func(ctx context.Context, in *publishRecordIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			p := auth.FromContext(ctx)
			if _, err := d.Records.Publish(ctx, p, in.ID, in.Body.Revision, in.Body.Published); err != nil {
				return nil, m2bErr(err, taskV2)
			}
			return view(ctx, p, in.ID)
		})
}

func recordsListFilter(in *listRecordsIn) records.ListFilter {
	f := records.ListFilter{Q: in.Q, SongID: in.SongID, IncludeHidden: in.IncludeHidden, Limit: in.Limit, Cursor: in.Cursor}
	for _, s := range in.Status {
		f.Statuses = append(f.Statuses, records.Status(s))
	}
	return f
}

func recordsNew(in *createRecordIn) records.NewRecord {
	return records.NewRecord{Title: in.Body.Title, Subtitle: in.Body.Subtitle, LinerNote: in.Body.LinerNote, SongIDs: in.Body.SongIDs}
}

func recordsPatch(in *updateRecordIn) records.Patch {
	b := in.Body
	return records.Patch{Revision: b.Revision, Title: b.Title, Subtitle: b.Subtitle, LinerNote: b.LinerNote, CoverReroll: b.CoverReroll != nil && *b.CoverReroll, Hidden: b.Hidden}
}

func recordsChangeFilter(in *getRecordChangesIn) records.ChangeFilter {
	return records.ChangeFilter{RecordID: in.ID, Order: in.Order, Cursor: in.Cursor, Kinds: in.Kind, Limit: in.Limit}
}
