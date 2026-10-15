package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/oidc"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// notifyAdmin sends the one push of a management call (C113, D-M4a-24): priority 3, no key, secret, URL or username.
// A human's own call names "operator", an agent's call its token name.
func notifyAdmin(ctx context.Context, d Deps, caller auth.Principal, title, verb, label string) {
	if d.Notify == nil {
		return
	}
	who := "operator"
	if !caller.ActsAsHuman() {
		who = caller.Name
	}
	_ = d.Notify.Notify(context.WithoutCancel(ctx), notify.Message{Title: title, Body: who + " " + verb + " " + label, Priority: 3, Tags: []string{"key"}})
}

// keyRef renders a stored principal key for viewer: the kind is the key's prefix, an agent's name the token's or
// grant's label, a human's the rest of the key (RefFor then masks the operator and devices for an agent viewer).
func keyRef(ctx context.Context, d Deps, viewer auth.Principal, key string) PrincipalRef {
	kind, rest, _ := strings.Cut(key, ":")
	name := rest
	if kind == string(auth.KindAgent) && d.Store != nil {
		var n string
		if d.Store.R.QueryRowContext(ctx, `SELECT name FROM api_tokens WHERE id = ?`, rest).Scan(&n) == nil ||
			d.Store.R.QueryRowContext(ctx, `SELECT label FROM connector_grants WHERE id = ?`, rest).Scan(&n) == nil {
			name = n
		}
	}
	return RefFor(viewer, key, kind, name)
}

func connectorGrantView(ctx context.Context, d Deps, viewer auth.Principal, g connectors.Grant, caps spend.Caps) ConnectorGrant {
	out := ConnectorGrant{
		ID: g.ID, ClientID: g.ClientID, Label: g.Label, Scopes: make([]string, 0, len(g.Scopes)),
		CreatedAt: store.FormatTime(g.CreatedAt), UpdatedAt: store.FormatTime(g.UpdatedAt), UpdatedBy: keyRef(ctx, d, viewer, g.UpdatedBy),
	}
	if viewer.ActsAsHuman() {
		u, hint := g.Username, g.Subject
		if len(hint) > 8 {
			hint = hint[:8]
		}
		out.Username, out.SubjectHint = &u, &hint
	}
	for _, sc := range g.Scopes {
		out.Scopes = append(out.Scopes, string(sc))
		if sc == auth.ScopeGenerate {
			out.GenerateEnabled = true
		}
	}
	l := g.Limits
	out.Limits.DailyBudgetMicroUSD, out.Limits.DailyBudgetUSD = int64Ptr(l.DailyBudgetMicro)
	out.Limits.AutoApproveMicroUSD, out.Limits.AutoApproveUSD = int64Ptr(l.AutoApproveMicro)
	out.Limits.AutoApproveTakes = l.AutoApproveTakes
	daily, auto, takes := caps.AgentDailyMicro, caps.AutoApproveMicro, caps.AutoApproveTakes
	if l.DailyBudgetMicro != nil {
		daily = *l.DailyBudgetMicro
	}
	if l.AutoApproveMicro != nil {
		auto = *l.AutoApproveMicro
	}
	if l.AutoApproveTakes != nil {
		takes = *l.AutoApproveTakes
	}
	out.Effective = GrantEffective{DailyBudgetMicroUSD: int64(daily), DailyBudgetUSD: usd(daily), AutoApproveTakes: takes,
		AutoApproveMicroUSD: int64(auto), AutoApproveUSD: usd(auto)}
	if g.LastUsedAt != nil {
		s := store.FormatTime(*g.LastUsedAt)
		out.LastUsedAt = &s
	}
	if g.RevokedAt != nil {
		s := store.FormatTime(*g.RevokedAt)
		out.RevokedAt = &s
		ref := PrincipalRefOrNull(keyRef(ctx, d, viewer, g.RevokedBy))
		out.RevokedBy = &ref
	}
	return out
}

func oidcStatus(d Deps) OIDCStatus {
	cfg := d.Cfg
	st := OIDCStatus{
		Enabled: cfg.OIDCIssuer != "", Resource: strings.TrimRight(cfg.PublicURL, "/") + "/mcp", Alg: oidc.Alg, RequiredScope: oidc.RequiredScope,
		ScopesAdvertised: []string{}, Clients: []OIDCClientInfo{},
	}
	if !st.Enabled {
		return st
	}
	iss := cfg.OIDCIssuer
	st.Issuer = &iss
	st.ScopesAdvertised = append(st.ScopesAdvertised, cfg.OIDCScopes...)
	for _, c := range cfg.OIDCClients {
		st.Clients = append(st.Clients, OIDCClientInfo{ClientID: c.ID, Label: c.Label})
	}
	if d.OIDC != nil {
		s := d.OIDC.Status()
		st.Keys.Count = s.Keys
		if s.FetchedAt != nil {
			at := store.FormatTime(*s.FetchedAt)
			st.Keys.FetchedAt = &at
		}
		if s.LastError != "" {
			e := s.LastError
			st.Keys.LastError = &e
		}
	}
	return st
}

// connectorErr maps the store's failures to problems.
func connectorErr(err error) error {
	switch {
	case errors.Is(err, connectors.ErrNotFound):
		return Err(http.StatusNotFound, "NOT_FOUND", "no such connector grant", nil)
	case errors.Is(err, connectors.ErrInvalidScopes):
		return Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "scopes must be a unique subset of read, write, generate and contain read", map[string]any{"field": "scopes"})
	case errors.Is(err, connectors.ErrInvalidLimits):
		return Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "a limit is out of range", map[string]any{"field": "limits"})
	case errors.Is(err, connectors.ErrEmptyChange):
		return Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "give at least one of scopes, limits and revoked", nil)
	}
	return err
}

// registerConnectorsM4a registers the connector-grant management operations (C106, C113, D-M4a-24): scope admin,
// not human-only, MCP generic. Every call is audited by the gate and pushes one ntfy message (an agent's list too).
func registerConnectorsM4a(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_connector_grants", Method: "GET", Path: "/api/connectors",
		Summary:     "List hosted-connector grants",
		Description: "The OAuth connector status (issuer, resource, allowed clients, key cache) and every connector grant with its scopes, limits and last use. Needs scope admin. Usernames and subject hints are shown to humans only.",
	}, Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "generic", UI: "/human/connectors"},
		func(ctx context.Context, _ *empty) (*connectorListOut, error) {
			if d.Connectors == nil {
				return nil, unavailable("connectors")
			}
			if d.Spend == nil || d.Store == nil {
				return nil, unavailable("spend")
			}
			p := auth.FromContext(ctx)
			grants, err := d.Connectors.List(ctx)
			if err != nil {
				return nil, err
			}
			caps, err := d.Spend.LoadCaps(ctx, d.Store.R)
			if err != nil {
				return nil, err
			}
			out := ConnectorGrantList{OIDC: oidcStatus(d), Items: make([]ConnectorGrant, 0, len(grants))}
			for _, g := range grants {
				out.Items = append(out.Items, connectorGrantView(ctx, d, p, g, caps))
			}
			if !p.ActsAsHuman() {
				notifyAdmin(ctx, d, p, "Connector grants listed", "listed", "connector grants")
			}
			return &connectorListOut{Body: out}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "set_connector_grant", Method: "PUT", Path: "/api/connectors/{id}",
		Summary:     "Change a connector grant",
		Description: "Sets a connector grant's scopes (read is required; generate is off by default), its spend limits (the whole limits object; null means the global default) and/or revokes or restores it. Needs scope admin. The global monthly cap stays human-only, so a connector budget never exceeds it.",
	}, bodyM4a), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "generic", UI: "/human/connectors"},
		func(ctx context.Context, in *setConnectorGrantIn) (*connectorOut, error) {
			if d.Connectors == nil {
				return nil, unavailable("connectors")
			}
			if d.Spend == nil || d.Store == nil {
				return nil, unavailable("spend")
			}
			p := auth.FromContext(ctx)
			var set connectors.SetIn
			if in.Body.Scopes != nil {
				set.Scopes = make([]auth.Scope, len(in.Body.Scopes))
				for i, sc := range in.Body.Scopes {
					set.Scopes[i] = auth.Scope(sc)
				}
			}
			if l := in.Body.Limits; l != nil {
				set.Limits = &connectors.Limits{DailyBudgetMicro: microPtr(l.DailyBudgetMicroUSD), AutoApproveTakes: l.AutoApproveTakes,
					AutoApproveMicro: microPtr(l.AutoApproveMicroUSD)}
			}
			set.Revoked = in.Body.Revoked
			g, err := d.Connectors.Set(ctx, p, in.ID, set)
			if err != nil {
				return nil, connectorErr(err)
			}
			caps, err := d.Spend.LoadCaps(ctx, d.Store.R)
			if err != nil {
				return nil, err
			}
			verb := "changed"
			switch {
			case in.Body.Revoked != nil && *in.Body.Revoked:
				verb = "revoked"
			case in.Body.Revoked != nil:
				verb = "restored"
			}
			notifyAdmin(ctx, d, p, "Connector grant changed", verb, g.Label)
			return &connectorOut{Body: connectorGrantView(ctx, d, p, g, caps)}, nil
		})
}
