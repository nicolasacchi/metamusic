package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

// maxAuditPageBytes is the serialized cap of one get_audit page (M1 C17).
const maxAuditPageBytes = 90000

func auditWhen(field, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return t, Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", field+" must be an RFC 3339 date-time", map[string]any{"field": field})
	}
	return t, nil
}

func registerAudit(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_audit", Method: "GET", Path: "/api/audit",
		Summary:     "Read the audit log",
		Description: "Recorded agent calls and operator mutations, newest first, filtered by principal, operation, time and status. Never contains request bodies.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/settings/audit"},
		func(ctx context.Context, in *getAuditIn) (*auditOut, error) {
			if d.Audit == nil {
				return nil, unavailable("audit")
			}
			viewer := auth.FromContext(ctx)
			// An agent may not address the operator by key (D-M2a-19): it filters human rows with principal_kind=human.
			if !viewer.ActsAsHuman() && (in.PrincipalID == "human" || strings.HasPrefix(in.PrincipalID, "human:")) {
				return nil, Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "principal_id cannot name the operator; use principal_kind=human",
					map[string]any{"field": "principal_id"})
			}
			since, err := auditWhen("since", in.Since)
			if err != nil {
				return nil, err
			}
			until, err := auditWhen("until", in.Until)
			if err != nil {
				return nil, err
			}
			page, err := d.Audit.List(ctx, audit.Filter{
				PrincipalID: in.PrincipalID, PrincipalKinds: in.PrincipalKind, Ops: in.Op, ExcludeOps: in.ExcludeOp,
				Since: since, Until: until, StatusMin: in.StatusMin, Limit: in.Limit, Cursor: in.Cursor,
			})
			if err != nil {
				return nil, err
			}
			out := &auditOut{}
			out.Body.Items = make([]auditEntry, 0, len(page.Items))
			size := 256 // envelope and cursor
			for _, r := range page.Items {
				e := auditEntry{
					ID: r.ID, At: r.At, Principal: RefFor(viewer, r.PrincipalID, r.PrincipalKind, r.PrincipalName),
					Transport: r.Transport, Client: r.Client, Op: r.Op, Method: r.Method, Status: r.Status,
					Idempotency: r.Idempotency, CreatedIDs: r.CreatedIDs, ArgsSHA256: r.ArgsSHA256, DurationMS: r.DurationMS,
				}
				if e.CreatedIDs == nil {
					e.CreatedIDs = []string{}
				}
				if r.ErrorCode != "" {
					code := r.ErrorCode
					e.ErrorCode = &code
				}
				b, _ := json.Marshal(e)
				if len(out.Body.Items) > 0 && size+len(b)+1 > maxAuditPageBytes {
					// the page is full: resume after the last kept row
					last := out.Body.Items[len(out.Body.Items)-1].ID
					out.Body.NextCursor = &last
					return out, nil
				}
				size += len(b) + 1
				out.Body.Items = append(out.Body.Items, e)
			}
			if page.NextCursor != "" {
				c := page.NextCursor
				out.Body.NextCursor = &c
			}
			return out, nil
		})
}
