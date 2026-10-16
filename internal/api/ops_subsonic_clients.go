package api

import (
	"context"
	"errors"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/notify"
)

// subsonicAPIVersion is the Subsonic REST version the bridge speaks (subsonic.APIVersion; a servertest test pins
// the equality, the api package does not import the protocol layer).
const subsonicAPIVersion = "1.16.1"

// subsonicRef renders a created_by/revoked_by principal key ("human:alice", "agent:<id>") for the viewer: masked for
// non-human viewers like every PrincipalRef (D-M2a-19).
func subsonicRef(viewer auth.Principal, key string) PrincipalRef {
	kind, id, _ := strings.Cut(key, ":")
	if id == "" {
		id = kind
	}
	return RefFor(viewer, key, kind, id)
}

func subsonicClient(viewer auth.Principal, c auth.SubsonicClient) SubsonicClient {
	out := SubsonicClient{ID: c.ID, Name: c.Name, Username: c.Username, DefaultFormat: c.DefaultFormat,
		HasAPIKey: c.HasAPIKey, HasPassword: c.HasPassword, CreatedAt: tsOut(c.CreatedAt), CreatedBy: subsonicRef(viewer, c.CreatedBy),
		LastSeenAt: tsPtr(c.LastSeenAt), RevokedAt: tsPtr(c.RevokedAt), Scopes: make([]string, len(c.Scopes))}
	for i, s := range c.Scopes {
		out.Scopes[i] = string(s)
	}
	if c.APIKeyPrefix != "" {
		p := c.APIKeyPrefix
		out.APIKeyPrefix = &p
	}
	if c.LastClient != "" {
		l := c.LastClient
		out.LastClient = &l
	}
	return out
}

func subsonicErr(err error) error {
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return Err(404, "NOT_FOUND", "no such Subsonic client", nil)
	case errors.Is(err, auth.ErrUsernameTaken):
		return Err(409, "USERNAME_TAKEN", "that username is already in use", nil)
	case errors.Is(err, auth.ErrSubsonicInput):
		return Err(422, "VALIDATION_FAILED", err.Error(), nil)
	}
	return err
}

// pushSubsonic sends the one ntfy push of a management call (D-M4a-24): "<caller name> <verb> <label>", never a key,
// secret, URL or username.
func pushSubsonic(ctx context.Context, d Deps, title, verb, label string) {
	if d.Notify == nil {
		return
	}
	name := auth.FromContext(ctx).Name
	_ = d.Notify.Notify(context.WithoutCancel(ctx), notify.Message{Title: title, Body: name + " " + verb + " " + label,
		Priority: 3, Tags: []string{"key"}})
}

// registerSubsonicClientsM4a registers the Subsonic-client management operations (C107, C113, D-M4a-24): scope
// admin, not human-only, MCP generic. Every call is audited by the gate and pushed through ntfy (a human's own
// list sends no push).
func registerSubsonicClientsM4a(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_subsonic_clients", Method: "GET", Path: "/api/subsonic-clients",
		Summary:     "List Subsonic clients",
		Description: "The native-app credentials of the OpenSubsonic bridge with their last use, plus the server URL to enter in the app. Never key material. Needs scope admin.",
	}, Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "generic", UI: "/human/subsonic"},
		func(ctx context.Context, _ *empty) (*subsonicListOut, error) {
			if d.Subsonic == nil {
				return nil, unavailable("subsonic")
			}
			viewer := auth.FromContext(ctx)
			list, err := d.Subsonic.List(ctx)
			if err != nil {
				return nil, err
			}
			out := &subsonicListOut{}
			out.Body.ServerURL = strings.TrimRight(d.Cfg.PublicURL, "/")
			out.Body.APIVersion = subsonicAPIVersion
			out.Body.Items = make([]SubsonicClient, len(list))
			for i, c := range list {
				out.Body.Items[i] = subsonicClient(viewer, c)
			}
			if viewer.Kind != auth.KindHuman {
				pushSubsonic(ctx, d, "Subsonic clients listed", "listed", "Subsonic clients")
			}
			return out, nil
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_subsonic_client", Method: "POST", Path: "/api/subsonic-clients",
		Summary:     "Create a Subsonic client",
		Description: "Creates a credential for one native app: an API key (mms_...) and/or an app password for token authentication. Both are shown once; a replay of the same Idempotency-Key returns them as null. Needs scope admin.",
	}), bodyM4a), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "generic", UI: "/human/subsonic", Idempotency: "create",
		SecretFields: []string{"api_key", "password"}},
		func(ctx context.Context, in *createSubsonicClientIn) (*subsonicCreatedOut, error) {
			if d.Subsonic == nil {
				return nil, unavailable("subsonic")
			}
			viewer := auth.FromContext(ctx)
			flag := func(p *bool) bool { return p == nil || *p }
			c, sec, err := d.Subsonic.Create(ctx, viewer, auth.NewSubsonicClient{
				Name: in.Body.Name, Username: in.Body.Username, DefaultFormat: in.Body.DefaultFormat,
				Write: flag(in.Body.Write), APIKey: flag(in.Body.APIKey), Password: flag(in.Body.Password)})
			if err != nil {
				return nil, subsonicErr(err)
			}
			audit.NoteCreated(ctx, c.ID)
			out := &subsonicCreatedOut{}
			out.Body = CreatedSubsonicClient{Client: subsonicClient(viewer, c), ServerURL: strings.TrimRight(d.Cfg.PublicURL, "/"), Username: c.Username}
			if sec.APIKey != "" {
				out.Body.APIKey = &sec.APIKey
			}
			if sec.Password != "" {
				out.Body.Password = &sec.Password
			}
			pushSubsonic(ctx, d, "Subsonic client created", "created", c.Name)
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "revoke_subsonic_client", Method: "POST", Path: "/api/subsonic-clients/{id}/revoke",
		Summary:     "Revoke a Subsonic client",
		Description: "Revokes a native-app credential by id; repeating the call returns the same result. Needs scope admin.",
	}, Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "generic", UI: "/human/subsonic"},
		func(ctx context.Context, in *subsonicIDIn) (*subsonicOut, error) {
			if d.Subsonic == nil {
				return nil, unavailable("subsonic")
			}
			viewer := auth.FromContext(ctx)
			list, err := d.Subsonic.List(ctx)
			if err != nil {
				return nil, err
			}
			already := false
			for _, c := range list {
				if c.ID == in.ID {
					already = c.RevokedAt != nil
				}
			}
			c, err := d.Subsonic.Revoke(ctx, viewer, in.ID)
			if err != nil {
				return nil, subsonicErr(err)
			}
			if !already { // a repeated revoke is a no-op and sends no second push
				pushSubsonic(ctx, d, "Subsonic client revoked", "revoked", c.Name)
			}
			return &subsonicOut{Body: subsonicClient(viewer, c)}, nil
		})
}
