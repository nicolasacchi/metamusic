package api

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/prompts"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// promptSources implements prompts.Sources over the services (C112.2).
type promptSources struct {
	lib *library.Service
	tc  *trackctx.Builder
	ls  *listening.Service
}

// NewPromptSources builds the prompts.Sources the get_prompt operation renders from: the library for titles and
// recipe facts, the context builder for the bundle (the REST bytes of get_track_context) and the listening service
// for the session summary. app.Build passes the result as Deps.Prompts.
func NewPromptSources(lib *library.Service, tc *trackctx.Builder, ls *listening.Service) prompts.Sources {
	return promptSources{lib: lib, tc: tc, ls: ls}
}

func (s promptSources) ContextJSON(ctx context.Context, p auth.Principal, trackID string) ([]byte, error) {
	if s.tc == nil {
		return nil, errors.ErrUnsupported
	}
	b, err := s.tc.Build(ctx, p, trackID, nil)
	if err != nil {
		return nil, err
	}
	return EncodeResponse(b)
}

func (s promptSources) Session(ctx context.Context, since time.Time, limit int) (listening.Session, error) {
	if s.ls == nil {
		return listening.Session{}, errors.ErrUnsupported
	}
	return s.ls.Session(ctx, since, limit)
}

func (s promptSources) Title(ctx context.Context, trackID string) (string, error) {
	t, err := s.lib.Track(ctx, trackID)
	if err != nil {
		return "", err
	}
	return t.Title, nil
}

func (s promptSources) RecipeKnown(ctx context.Context, trackID string) (bool, error) {
	return s.lib.RecipeKnown(ctx, trackID)
}

// registerPromptsM4a registers get_prompt (C112.2, C113).
func registerPromptsM4a(a huma.API, d Deps) {
	src := d.Prompts
	Register(a, huma.Operation{
		OperationID: "get_prompt", Method: "GET", Path: "/api/prompts/{name}",
		Summary:     "Render a prompt",
		Description: "Renders one of the built-in prompts (critique_track, write_variant, session_review) for a track or a listening session: the messages an agent or the web UI hands to a model. critique_track and write_variant need track_id.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *getPromptIn) (*promptOut, error) {
			if src == nil {
				return nil, unavailable("prompts")
			}
			args := map[string]string{"track_id": in.TrackID, "goal": in.Goal, "since": in.Since}
			sp, msgs, err := prompts.Render(ctx, auth.FromContext(ctx), in.Name, args, src, d.clock())
			if err != nil {
				var pe *prompts.Error
				if errors.As(err, &pe) {
					return nil, Err(pe.Status, pe.Code, pe.Msg, pe.Details)
				}
				return nil, judgErr(err)
			}
			view := PromptView{Name: sp.Name, Title: sp.Title, Description: sp.Description, Version: sp.Version,
				Arguments: make([]PromptArgument, 0, len(sp.Args)), Messages: make([]PromptMessage, 0, len(msgs))}
			for _, a := range sp.Args {
				view.Arguments = append(view.Arguments, PromptArgument{Name: a.Name, Description: a.Description, Required: a.Required})
			}
			for _, m := range msgs {
				c := PromptContent{Type: m.Content.Type, Text: m.Content.Text}
				if r := m.Content.Resource; r != nil {
					c.Resource = &PromptResource{URI: r.URI, MIMEType: r.MIMEType, Text: r.Text}
				}
				view.Messages = append(view.Messages, PromptMessage{Role: m.Role, Content: c})
			}
			return &promptOut{Body: view}, nil
		})
}
