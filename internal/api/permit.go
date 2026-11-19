package api

import (
	"fmt"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// Permitted is the one gate of every operation (C302.1). The rules run in this order: anonymous is 401
// UNAUTHENTICATED; a HumanOnly operation and a principal that is not a human login is 403 HUMAN_ONLY; an owner-only
// operation and a principal whose role is Editor or Listener is 403 OWNER_ONLY; a missing scope is 403
// FORBIDDEN_SCOPE. It returns nil when the call may proceed. Principals with no role (agents, connector grants,
// system) and the Owner's own devices skip the OwnerOnly rule and pass or fail on scope exactly as before U1.
func Permitted(p auth.Principal, m Meta) *Problem {
	if p.Kind == auth.KindAnonymous || p.Kind == "" {
		return permitProblem(401, "UNAUTHENTICATED", "authentication required", nil)
	}
	if m.HumanOnly && !p.IsHuman() {
		return permitProblem(403, "HUMAN_ONLY", "this operation is restricted to human users", nil)
	}
	if m.ownerOnly() && !p.IsOwnerSide() {
		return permitProblem(403, "OWNER_ONLY", "this operation is restricted to the Owner", nil)
	}
	if !p.Has(m.Scope) {
		details := map[string]any{"scope": string(m.Scope)}
		if p.Via == "oauth" { // a connector's scopes are the grant's: only the operator can widen them (M4a C106)
			details["hint"] = "the operator can enable this scope for the connector at /human/connectors"
		}
		return permitProblem(403, "FORBIDDEN_SCOPE", "missing required scope", details)
	}
	return nil
}

// ownerOnly is the effective rule: an explicit tag, or one of the classes validate() requires the tag on (cost approve,
// scope admin; HumanOnly too, except the self-service ops below). Treating the classes as owner-only here, not only at
// registration, keeps an op that forgot the tag closed to Editors and Listeners. A HumanOnly op alone is not owner-only
// because the self-service ops are HumanOnly and open to every user; validateOwnerOnly keeps every other HumanOnly op tagged.
func (m Meta) ownerOnly() bool {
	return m.OwnerOnly || m.Cost == "approve" || m.Scope == auth.ScopeAdmin
}

// selfServiceHumanOps are the human-only operations an Editor or Listener may call on their own behalf (D-U1-14, C305):
// pairing a device, creating a Subsonic client, and listing and revoking them, each bound to the caller's owner key. They
// are the only HumanOnly operations that may omit OwnerOnly; the service layer enforces the ownership.
var selfServiceHumanOps = map[string]bool{
	"create_pairing_code": true, "list_devices": true, "revoke_device": true,
	"create_subsonic_client": true, "list_subsonic_clients": true, "revoke_subsonic_client": true,
}

// sensitiveReadOps are the scope-read operations that must be owner-only (D-U1-6, C302.3): audit, tokens, backups,
// imports, spend, ledger, provider and connectors, plus the other read-scope ops of the C305 Administration family.
// Final list as of lane F1b, checked against openapi.json.
var sensitiveReadOps = map[string]bool{
	"get_audit": true, "list_tokens": true, "list_backups": true, "list_import_runs": true,
	"get_spend": true, "list_ledger": true, "get_provider_status": true, "list_connector_grants": true,
	"get_import_run": true, "get_device_probe": true, "list_device_profiles": true,
}

// enforceOwnerOnlyTags makes validate() require the explicit OwnerOnly tag on the classes of D-U1-6. Lane F1b tagged
// every op, so registration now fails for an unclassified HumanOnly, approve, admin or sensitive read op.
const enforceOwnerOnlyTags = true

// validateOwnerOnly applies the C302.3 registration rules: HumanOnly, cost approve, scope admin and the sensitive
// read operations require OwnerOnly. OwnerOnly alone is always valid.
func validateOwnerOnly(op huma.Operation, m Meta, enforce bool) error {
	if !enforce || m.OwnerOnly {
		return nil
	}
	switch {
	case m.HumanOnly && !selfServiceHumanOps[op.OperationID]:
		return fmt.Errorf("%s: human_only operations require owner_only", op.OperationID)
	case m.Cost == "approve":
		return fmt.Errorf("%s: cost approve requires owner_only", op.OperationID)
	case m.Scope == auth.ScopeAdmin:
		return fmt.Errorf("%s: scope admin requires owner_only", op.OperationID)
	case sensitiveReadOps[op.OperationID]:
		return fmt.Errorf("%s: sensitive read operations require owner_only", op.OperationID)
	}
	return nil
}

func permitProblem(status int, code, msg string, details map[string]any) *Problem {
	if details == nil {
		details = map[string]any{}
	}
	return &Problem{Status: status, Code: code, Message: msg, Details: details}
}
