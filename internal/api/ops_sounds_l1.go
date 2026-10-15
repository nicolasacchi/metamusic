package api

import (
	"context"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

// registerSoundsL1 registers the Sound operations (C96).
func registerSoundsL1(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "search_sounds", Method: "GET", Path: "/api/sounds/search",
		Summary:     "Search Sounds",
		Description: "Songs filtered by Sound facets (genre family, era, tempo band, vocal, mood, language, engine) with facet counts that keep a selected value's siblings visible; limit 0 returns the counts only.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/sounds"},
		func(ctx context.Context, in *searchSoundsIn) (*soundSearchOut, error) {
			if d.Sounds == nil {
				return nil, unavailable("sounds")
			}
			f, err := soundSearchFilter(in.songFilterIn)
			if err != nil {
				return nil, err
			}
			counts, err := d.Sounds.Facets(ctx, f)
			if err != nil {
				return nil, m2bErr(err, "L1-B6")
			}
			out := &soundSearchOut{}
			out.Body = SoundSearch{TaxonomyVersion: counts.TaxonomyVersion, Selected: counts.Selected, Facets: map[string][]FacetValueCount{},
				Songs: []SongSummary{}, Total: counts.SongsMatching}
			for facet, vals := range counts.Facets {
				items := make([]FacetValueCount, 0, len(vals))
				for _, v := range vals {
					items = append(items, FacetValueCount{Value: v.Value, Count: v.Count, Mismatches: v.Mismatches})
				}
				out.Body.Facets[facet] = items
			}
			if in.Limit == 0 {
				return out, nil
			}
			if d.Songs == nil {
				return nil, unavailable("songs")
			}
			f.Limit, f.Cursor = in.Limit, in.Cursor
			page, err := d.Songs.List(ctx, f)
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			for _, it := range page.Items {
				out.Body.Songs = append(out.Body.Songs, songSummaryOut(it))
			}
			out.Body.Total, out.Body.NextCursor = page.Total, page.NextCursor
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_sound_taxonomy", Method: "GET", Path: "/api/sounds/taxonomy",
		Summary:     "Get the Sound taxonomy",
		Description: "The effective mapping of style terms to facet values with the version history, or with unmapped=true the most frequent style terms no mapping matches.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/sounds"},
		func(ctx context.Context, in *taxonomyIn) (*taxonomyOut, error) {
			if d.Sounds == nil {
				return nil, unavailable("sounds")
			}
			pg, err := d.Sounds.Taxonomy(ctx, sounds.TaxonomyQuery{Facet: in.Facet, Q: in.Q, Unmapped: in.Unmapped, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, "L1-B6")
			}
			viewer := auth.FromContext(ctx)
			out := &taxonomyOut{}
			out.Body = TaxonomyPage{CurrentVersion: pg.CurrentVersion, NextCursor: pg.NextCursor, Versions: []TaxonomyVersion{}, Items: []TaxonomyItem{}, Unmapped: []UnmappedTerm{}}
			for _, v := range pg.Versions {
				out.Body.Versions = append(out.Body.Versions, TaxonomyVersion{Version: v.Version, Kind: v.Kind, SeedID: v.SeedID,
					CreatedBy: keyRef(viewer, v.CreatedBy), CreatedAt: v.CreatedAt, Note: v.Note})
			}
			for _, it := range pg.Items {
				out.Body.Items = append(out.Body.Items, TaxonomyItem{Term: it.Term, Facet: it.Facet, Value: it.Value, SinceVersion: it.SinceVersion})
			}
			for _, u := range pg.Unmapped {
				out.Body.Unmapped = append(out.Body.Unmapped, UnmappedTerm{Term: u.Term, Songs: u.Songs})
			}
			return out, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "map_sound_term", Method: "POST", Path: "/api/sound-terms",
		Summary:     "Map Sound terms",
		Description: "Adds or removes up to 50 style-term mappings as one new taxonomy version and recomputes the affected Songs before answering.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/sounds", Rate: "heavy"},
		func(ctx context.Context, in *mapSoundTermIn) (*mapResultOut, error) {
			if d.Sounds == nil {
				return nil, unavailable("sounds")
			}
			req := sounds.MapRequest{Note: in.Body.Note}
			for _, m := range in.Body.Mappings {
				req.Mappings = append(req.Mappings, sounds.MapEntry{Term: m.Term, Facet: m.Facet, Value: m.Value, Op: m.Op})
			}
			res, err := d.Sounds.Map(ctx, auth.FromContext(ctx), req)
			if err != nil {
				return nil, m2bErr(err, "L1-B6")
			}
			out := &mapResultOut{}
			out.Body = MapResult{Version: res.Version, MappingsApplied: res.MappingsApplied, Unchanged: res.Unchanged,
				Recomputed: MapRecomputed{Takes: res.RecomputedTakes, Songs: res.RecomputedSongs}}
			return out, nil
		})
}

// soundSearchFilter turns the shared query filter into a songs.ListFilter; sound=facet:value pairs are validated
// against the seven facets (value syntax is not checked: an unknown value simply matches nothing).
func soundSearchFilter(in songFilterIn) (songs.ListFilter, error) {
	f := songs.ListFilter{MuseID: in.MuseID, NoMuse: in.NoMuse, Angle: in.Angle, Queued: in.Queued, SessionID: in.SessionID,
		TempoMismatch: in.TempoMismatch, HasPick: in.HasPick, DraftID: in.DraftID, TrackID: in.TrackID,
		HasOpenProposals: in.HasOpenProposals, Q: in.Q, IncludeHidden: in.IncludeHidden, IncludeArchived: in.IncludeArchived}
	for _, st := range in.State {
		f.States = append(f.States, songs.State(st))
	}
	for _, kv := range in.Sound {
		facet, value, ok := strings.Cut(kv, ":")
		if !ok || value == "" || !sounds.ValidFacet(facet) {
			return f, Err(422, "INVALID_SOUND_FILTER", "sound must be facet:value with a facet of genre_family, era, tempo_band, vocal, mood, language or engine", map[string]any{"facet": facet})
		}
		if f.Sounds == nil {
			f.Sounds = map[string][]string{}
		}
		if !contains(f.Sounds[facet], value) {
			f.Sounds[facet] = append(f.Sounds[facet], value)
		}
	}
	return f, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// songSummaryOut renders a list row.
func songSummaryOut(s songs.Summary) SongSummary {
	out := SongSummary{ID: s.ID, Title: s.Title, Angle: s.Angle, MuseID: s.MuseID, State: string(s.State), Takes: s.Takes, DraftID: s.DraftID,
		RootTrackID: s.RootTrackID, QueuedJobs: s.QueuedJobs, OpenProposals: s.OpenProposals, Hidden: s.Hidden, Archived: s.Archived, UpdatedAt: s.UpdatedAt}
	if s.Pick != nil {
		out.Pick = &SongPickRef{TakeID: s.Pick.TakeID, Source: s.Pick.Source}
	}
	return out
}

// keyRef renders a stored principal key ("kind:id") for viewer.
func keyRef(viewer auth.Principal, key string) PrincipalRef {
	kind, name, _ := strings.Cut(key, ":")
	return RefFor(viewer, key, kind, name)
}
