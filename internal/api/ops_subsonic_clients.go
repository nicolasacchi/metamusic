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
	var owner *string
	if auth.SeesAllDevices(viewer) { // the owner column is the Owner's oversight view only
		owner = &c.OwnerKey
	}
	out := SubsonicClient{ID: c.ID, Name: c.Name, Username: c.Username, DefaultFormat: c.DefaultFormat,
		HasAPIKey: c.HasAPIKey, HasPassword: c.HasPassword, CreatedAt: tsOut(c.CreatedAt), CreatedBy: subsonicRef(viewer, c.CreatedBy),
		LastSeenAt: tsPtr(c.LastSeenAt), RevokedAt: tsPtr(c.RevokedAt), Scopes: make([]string, len(c.Scopes)), Owner: owner}
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
	case errors.Is(err, auth.ErrDeviceLimit):
		return Err(422, "DEVICE_LIMIT", "you already have the maximum number of active Subsonic clients; revoke one first", nil)
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

// registerSubsonicClientsM4a registers the Subsonic-client management operations (C107, C113, C305, D-U1-14): human-only
// self-service, scope listen, never an MCP tool. Every call is audited by the gate; a change is pushed through ntfy.
func registerSubsonicClientsM4a(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_subsonic_clients", Method: "GET", Path: "/human/api/subsonic-clients",
		Summary:     "List Subsonic clients",
		Description: "The native-app credentials of the OpenSubsonic bridge with their last use, plus the server URL to enter in the app. Never key material. The Owner sees every user's clients (with an owner column); anyone else only their own.",
	}, Meta{Scope: auth.ScopeListen, Cost: "free", MCP: "exclude:human-only", UI: "/human/subsonic", HumanOnly: true},
		func(ctx context.Context, _ *empty) (*subsonicListOut, error) {
			if d.Subsonic == nil {
				return nil, unavailable("subsonic")
			}
			viewer := auth.FromContext(ctx)
			list, err := d.Subsonic.List(ctx, viewer)
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
			return out, nil
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_subsonic_client", Method: "POST", Path: "/human/api/subsonic-clients",
		Summary:     "Create a Subsonic client",
		Description: "Creates a credential for one native app: an API key (mms_...) and/or an app password for token authentication. Both are shown once; a replay of the same Idempotency-Key returns them as null. The client belongs to the caller and acts with the caller's role; an Editor or Listener may hold 5 active clients.",
	}), bodyM4a), Meta{Scope: auth.ScopeListen, Cost: "free", MCP: "exclude:human-only", UI: "/human/subsonic", Idempotency: "create",
		SecretFields: []string{"api_key", "password"}, HumanOnly: true},
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
		OperationID: "revoke_subsonic_client", Method: "POST", Path: "/human/api/subsonic-clients/{id}/revoke",
		Summary:     "Revoke a Subsonic client",
		Description: "Revokes a native-app credential by id; repeating the call returns the same result. The Owner may revoke any client; anyone else only their own (another user's id answers 404).",
	}, Meta{Scope: auth.ScopeListen, Cost: "free", MCP: "exclude:human-only", UI: "/human/subsonic", HumanOnly: true},
		func(ctx context.Context, in *subsonicIDIn) (*subsonicOut, error) {
			if d.Subsonic == nil {
				return nil, unavailable("subsonic")
			}
			viewer := auth.FromContext(ctx)
			list, err := d.Subsonic.List(ctx, viewer)
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
