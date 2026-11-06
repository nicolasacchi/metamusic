package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/songpr"
)

// registerSongPRM5 registers the three Song PR operations (C253, C260). Shapes frozen by M5-F2; M5-S1 fills the handlers.
func registerSongPRM5(a huma.API, d Deps) {
	Register(a, withBody(created(huma.Operation{
		OperationID: "propose_patch", Method: "POST", Path: "/api/patches",
		Summary:     "Propose a Song PR",
		Description: "Stores a bounded patch to a take (at most 8 lyric lines in at most 4 sections, or a style change of at most 3 terms and 20 BPM) that names the fix or note comments it addresses. Free; rendering goes through estimate_generation with item mode song_pr.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id", Idempotency: "create"},
		func(ctx context.Context, in *proposePatchIn) (*patchOut, error) {
			if d.SongPR == nil {
				return nil, unavailable("songpr")
			}
			p := auth.FromContext(ctx)
			ops := make([]songpr.OpJSON, len(in.Body.Ops))
			for i, o := range in.Body.Ops {
				ops[i] = songpr.OpJSON{Op: o.Op, SectionIdx: o.SectionIdx, LineIdx: o.LineIdx, Text: o.Text, Style: o.Style}
			}
			r, err := d.SongPR.Propose(ctx, p, songpr.ProposeInput{BaseTrackID: in.Body.BaseTrackID, AddressesCommentIDs: in.Body.AddressesCommentIDs,
				Rationale: in.Body.Rationale, Title: in.Body.Title, ExtraSections: in.Body.ExtraSections, Ops: ops})
			if err != nil {
				return nil, genErr(err)
			}
			return &patchOut{Body: songPRFrom(p, r)}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_patch", Method: "GET", Path: "/api/patches/{id}",
		Summary:     "Get a Song PR",
		Description: "A Song PR with its ops, diff, scope and bounds report, the comments it addresses, its renders and the tally of callback answers.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/patches/:id"},
		func(ctx context.Context, in *getPatchIn) (*patchOut, error) {
			if d.SongPR == nil {
				return nil, unavailable("songpr")
			}
			p := auth.FromContext(ctx)
			r, err := d.SongPR.Get(ctx, p, in.ID)
			if err != nil {
				return nil, genErr(err)
			}
			return &patchOut{Body: songPRFrom(p, r)}, nil
		})

	Register(a, huma.Operation{
		OperationID: "list_patches", Method: "GET", Path: "/api/patches",
		Summary:     "List Song PRs",
		Description: "Song PRs newest first, filtered by take, addressed comment or lineage root, without ops and diff.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *listPatchesIn) (*patchPageOut, error) {
			if d.SongPR == nil {
				return nil, unavailable("songpr")
			}
			p := auth.FromContext(ctx)
			pg, err := d.SongPR.List(ctx, p, songpr.ListFilter{TrackID: in.TrackID, CommentID: in.CommentID, RootTrackID: in.RootTrackID, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, genErr(err)
			}
			out := PatchPage{Items: make([]SongPRSummary, len(pg.Items)), NextCursor: pg.NextCursor, Total: pg.Total}
			for i, it := range pg.Items {
				out.Items[i] = songPRSummaryFrom(p, it)
			}
			return &patchPageOut{Body: out}, nil
		})
}

func songPRSummaryFrom(viewer auth.Principal, x songpr.Summary) SongPRSummary {
	out := SongPRSummary{ID: x.ID, Base: PatchBase{TrackID: x.Base.TrackID, Title: x.Base.Title, RecipeHash: x.Base.RecipeHash}, Operator: x.Operator, Title: x.Title,
		Rationale: x.Rationale, Addresses: make([]PatchAddress, len(x.Addresses)), Renders: make([]PatchRender, len(x.Renders)),
		Callbacks: PatchCallbackTally(x.Callbacks), Author: RefFor(viewer, x.Author.Key, x.Author.Kind, x.Author.Name), CreatedAt: x.CreatedAt}
	for i, a := range x.Addresses {
		out.Addresses[i] = PatchAddress{CommentID: a.CommentID, TrackID: a.TrackID, Kind: a.Kind, State: a.State, BodyExcerpt: a.BodyExcerpt}
	}
	for i, r := range x.Renders {
		out.Renders[i] = PatchRender{BatchID: r.BatchID, JobID: r.JobID, State: r.State, Seed: r.Seed, ChildTrackID: r.ChildTrackID}
	}
	return out
}

func songPRFrom(viewer auth.Principal, x songpr.SongPR) SongPR {
	out := SongPR{SongPRSummary: songPRSummaryFrom(viewer, x.Summary), Ops: make([]PatchOp, len(x.Ops)), Diff: x.Diff, Bounds: x.Bounds,
		Compiled: PatchCompiled{Lyrics: x.Compiled.Lyrics, Style: x.Compiled.Style}}
	for i, o := range x.Ops {
		out.Ops[i] = PatchOp{Op: o.Op, SectionIdx: o.SectionIdx, LineIdx: o.LineIdx, Text: o.Text, Style: o.Style}
	}
	return out
}
