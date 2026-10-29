package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// registerMusesL1 registers the Muse operations (C96).
func registerMusesL1(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_muses", Method: "GET", Path: "/api/muses",
		Summary:     "List Muses",
		Description: "Muses (inspirations: a reference title and artist, or a brief) with their Song counts by state; q is a phrase-prefix search over title, artist and brief. Hidden Muses only with include_hidden.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/board"},
		func(ctx context.Context, in *listMusesIn) (*musesOut, error) {
			if d.Muses == nil {
				return nil, unavailable("muses")
			}
			if d.SongView == nil {
				return nil, unavailable("songview")
			}
			p, err := d.Muses.List(ctx, muses.ListFilter{Q: in.Q, Kind: muses.Kind(in.Kind), IncludeHidden: in.IncludeHidden, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			v := auth.FromContext(ctx)
			ms := make([]muses.Muse, len(p.Items))
			for i, it := range p.Items {
				ms[i] = it.Muse
			}
			sums, err := d.SongView.MuseSummaries(ctx, ms)
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			out := &musesOut{}
			out.Body.Items = make([]MuseSummary, len(sums))
			for i, s := range sums {
				out.Body.Items[i] = MuseSummary{Muse: museWire(v, s.Muse), MuseCoverage: coverageWire(s.Coverage)}
			}
			out.Body.NextCursor, out.Body.Total = p.NextCursor, p.Total
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_muse", Method: "GET", Path: "/api/muses/{id}",
		Summary:     "Get a Muse",
		Description: "One Muse with its coverage (Songs by state), its angles and its Songs.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/muses/:id"},
		func(ctx context.Context, in *getMuseIn) (*museDetailOut, error) {
			if d.Muses == nil {
				return nil, unavailable("muses")
			}
			if d.SongView == nil {
				return nil, unavailable("songview")
			}
			if d.Songs == nil {
				return nil, unavailable("songs")
			}
			m, err := d.Muses.Get(ctx, in.ID)
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			cov, err := d.SongView.MuseCoverage(ctx, m.ID)
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			angles, err := d.SongView.MuseAngles(ctx, m.ID)
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			out := &museDetailOut{Body: MuseDetail{Muse: museWire(auth.FromContext(ctx), m), Coverage: coverageWire(cov), Angles: []MuseAngleCount{}, Songs: []SongSummary{}}}
			ref, ok, err := d.Albums.MuseAlbum(ctx, m.ID) // nil while the Muse has no album or no catalog is wired
			if err != nil {
				return nil, m2bErr(err, "L2-A2")
			}
			if ok {
				out.Body.Catalog = &MuseCatalog{ID: ref.ID, NeutralName: ref.NeutralName}
			}
			for _, a := range angles {
				out.Body.Angles = append(out.Body.Angles, MuseAngleCount{Angle: a.Angle, Songs: a.Songs})
			}
			cursor := ""
			for {
				want := in.SongsLimit - len(out.Body.Songs)
				page, err := d.Songs.List(ctx, songs.ListFilter{MuseID: m.ID, Limit: max(1, min(want, 100)), Cursor: cursor})
				if err != nil {
					return nil, m2bErr(err, "L1-B5")
				}
				out.Body.SongsTotal = page.Total
				for _, it := range page.Items {
					if len(out.Body.Songs) < in.SongsLimit {
						out.Body.Songs = append(out.Body.Songs, songSummaryWire(it))
					}
				}
				if page.NextCursor == nil || len(out.Body.Songs) >= in.SongsLimit {
					break
				}
				cursor = *page.NextCursor
			}
			return out, nil
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_muse", Method: "POST", Path: "/api/muses",
		Summary:     "Create a Muse",
		Description: "Creates a reference Muse (title, optional artist) or a brief Muse (short title and free text). A reference whose normalised title and artist already exist answers 409 MUSE_EXISTS with the existing id.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/board", Idempotency: "create"},
		func(ctx context.Context, in *createMuseIn) (*museOut, error) {
			if d.Muses == nil {
				return nil, unavailable("muses")
			}
			m, err := d.Muses.Create(ctx, auth.FromContext(ctx), muses.NewMuse{Kind: muses.Kind(in.Body.Kind), Title: in.Body.Title, Artist: in.Body.Artist, Brief: in.Body.Brief})
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			audit.NoteCreated(ctx, m.ID)
			return &museOut{Body: museWire(auth.FromContext(ctx), m)}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_muse", Method: "PATCH", Path: "/api/muses/{id}",
		Summary:     "Update a Muse",
		Description: "Changes a Muse's title, artist or brief, or hides it, under optimistic locking: send the revision you read.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/muses/:id"},
		func(ctx context.Context, in *updateMuseIn) (*museOut, error) {
			if d.Muses == nil {
				return nil, unavailable("muses")
			}
			p := auth.FromContext(ctx)
			m, err := d.Muses.Update(ctx, p, in.ID, muses.Patch{Revision: in.Body.Revision, Title: in.Body.Title, Artist: in.Body.Artist, Brief: in.Body.Brief, Hidden: in.Body.Hidden})
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			return &museOut{Body: museWire(p, m)}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "assign_muse_songs", Method: "POST", Path: "/api/muses/{id}/songs",
		Summary:     "Assign a Muse to Songs",
		Description: "Assigns the Muse to 1..200 Songs, all or nothing; an assignment is never overwritten by the index backfill.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/board"},
		func(ctx context.Context, in *assignMuseSongsIn) (*assignResultOut, error) {
			if d.Muses == nil {
				return nil, unavailable("muses")
			}
			r, err := d.Muses.AssignSongs(ctx, auth.FromContext(ctx), in.ID, in.Body.SongIDs)
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			return &assignResultOut{Body: AssignResult{Assigned: r.Assigned, Unchanged: r.Unchanged}}, nil
		})
}
