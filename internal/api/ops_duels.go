package api

import (
	"context"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
)

func registerDuels(a huma.API, d Deps) {
	Register(a, withBody(created(huma.Operation{
		OperationID: "create_duel", Method: "POST", Path: "/api/duels",
		Summary: "Create an A/B duel",
		Description: "Cuts a 30-45 s window of two takes into loudness-matched, sample-aligned clips (the louder one is attenuated, never boosted) " +
			"and stores a duel. Blind duels (the default) hide which slot holds which track until the human has judged. " +
			"A replayed call returns the first result; call get_duel for fresh clip links.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/duels", Idempotency: "create", Rate: "heavy"},
		func(ctx context.Context, in *createDuelIn) (*duelOut, error) {
			if d.Duels == nil {
				return nil, unavailable("duels")
			}
			b := in.Body
			du, err := d.Duels.Create(ctx, auth.FromContext(ctx), duels.CreateInput{
				ATrackID: b.ATrackID, BTrackID: b.BTrackID, Blind: b.Blind, MatchLoudness: b.MatchLoudness, FromS: b.FromS, ToS: b.ToS})
			if err != nil {
				return nil, m2bErr(err, "M2b-J2")
			}
			audit.NoteCreated(ctx, du.ID)
			return &duelOut{Body: du}, nil
		})

	Register(a, huma.Operation{
		OperationID: "next_duel", Method: "GET", Path: "/api/duels/next",
		Summary: "Get the next duel to judge",
		Description: "The oldest unexpired duel waiting for your verdict, with signed clip links, or null. " +
			"Humans and paired devices judge as the operator; agents judge as critics and only see open duels they have not judged. " +
			"Blind duels stay unrevealed until you have judged them.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/duels"},
		func(ctx context.Context, in *nextDuelIn) (*nextDuelOut, error) {
			if d.Duels == nil {
				return nil, unavailable("duels")
			}
			res, err := d.Duels.Next(ctx, auth.FromContext(ctx), in.Exclude)
			if err != nil {
				return nil, m2bErr(err, "M2b-J2")
			}
			return &nextDuelOut{Body: res}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_duel", Method: "GET", Path: "/api/duels/{id}",
		Summary: "Get a duel",
		Description: "A duel as you may see it: slots stay A and B (tracks, gains and loudness are null) until the duel is revealed to you, " +
			"and other judges' verdicts appear only after your own. Clip links are minted fresh on every call.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/duels/:id"},
		func(ctx context.Context, in *duelIDIn) (*duelOut, error) {
			if d.Duels == nil {
				return nil, unavailable("duels")
			}
			du, err := d.Duels.Get(ctx, auth.FromContext(ctx), in.ID)
			if err != nil {
				return nil, m2bErr(err, "M2b-J2")
			}
			return &duelOut{Body: du}, nil
		})

	Register(a, huma.Operation{
		OperationID: "list_duels", Method: "GET", Path: "/api/duels",
		Summary: "List duels",
		Description: "Duels newest first, filtered by state, track, lineage or mode. Which two tracks a duel compares is not secret, " +
			"but unrevealed rows keep the slots anonymous. Pages are capped in size; follow next_cursor.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/duels"},
		func(ctx context.Context, in *listDuelsIn) (*duelPageOut, error) {
			if d.Duels == nil {
				return nil, unavailable("duels")
			}
			page, err := d.Duels.List(ctx, auth.FromContext(ctx), duels.ListFilter{States: in.State, TrackID: in.TrackID,
				RootTrackID: in.RootTrackID, Mode: in.Mode, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, "M2b-J2")
			}
			return &duelPageOut{Body: page}, nil
		})

	submit := huma.Operation{
		OperationID: "submit_duel", Method: "POST", Path: "/api/duels/{id}/verdicts",
		Summary: "Judge a duel",
		Description: "Records a verdict: a (slot A is better), b, tie or both_bad, with an optional note. " +
			"Verdicts from agents are stored as critic opinions and never change the human ladder. " +
			"Judging again with the same verdict and note returns the stored verdict (200, replayed); a different verdict is DUEL_ALREADY_JUDGED.",
		DefaultStatus: 201,
		Responses: map[string]*huma.Response{"200": {
			Description: "The verdict already existed; nothing changed (replayed: true)",
			Content: map[string]*huma.MediaType{"application/json": {
				Schema: a.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[duels.SubmitResult](), true, "SubmitResult")}},
		}},
	}
	Register(a, withBody(submit, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/duels"},
		func(ctx context.Context, in *submitDuelIn) (*submitDuelOut, error) {
			if d.Duels == nil {
				return nil, unavailable("duels")
			}
			b := in.Body
			res, err := d.Duels.Submit(ctx, auth.FromContext(ctx), in.ID, duels.VerdictInput{Verdict: b.Verdict, Note: b.Note, LatencyMS: b.LatencyMS, Switches: b.Switches})
			if err != nil {
				return nil, m2bErr(err, "M2b-J2")
			}
			out := &submitDuelOut{Status: 201, Body: res}
			if res.Replayed {
				out.Status = 200
			} else {
				audit.NoteCreated(ctx, res.Verdict.ID)
			}
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_ladder", Method: "GET", Path: "/api/ladder",
		Summary: "Get the preference ladder",
		Description: "The operator's personal ranking of takes, fitted from human blind-duel verdicts only (critic verdicts never count), " +
			"globally or within one lineage. u is a log-strength on a scale anchored at the virtual 'reject' item (0); " +
			"compare u values, not probabilities of acceptance. sd is the Laplace standard deviation; provisional rows have few duels.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/ladder"},
		func(ctx context.Context, in *getLadderIn) (*ladderOut, error) {
			if d.Duels == nil {
				return nil, unavailable("duels")
			}
			if in.Scope == "lineage" && in.RootTrackID == "" {
				return nil, Err(422, "VALIDATION_FAILED", "root_track_id is required for scope=lineage", map[string]any{"field": "root_track_id"})
			}
			page, err := d.Duels.Ladder(ctx, duels.LadderQuery{Scope: in.Scope, RootTrackID: in.RootTrackID,
				IncludeSighted: in.IncludeSighted, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, "M2b-J2")
			}
			return &ladderOut{Body: page}, nil
		})
}

// rfc3339 renders t for a response.
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }
