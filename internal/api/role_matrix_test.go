package api_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

// -update is the package flag declared in types_m2b_test.go (updateFixtures); it also rewrites testdata/role_matrix.golden.

// matrixRows are the principals of C305/C314.1, in golden column order. Devices carry the role and scopes of the clamp of
// C301.3 (lane D1: the real device and Subsonic principals are built from owner_key; auth.TestOwnerIdentityClamp and
// TestDeviceOwnership pin that the real principals carry exactly these roles and scopes).
var matrixRows = []struct {
	Name string
	P    auth.Principal
}{
	{"owner", auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", User: "alice", Role: auth.RoleOwner, Scopes: auth.ScopesFor(auth.RoleOwner), Via: "proxy"}},
	{"editor", auth.Principal{Kind: auth.KindHuman, ID: "bob", Name: "bob", User: "bob", Role: auth.RoleEditor, Scopes: auth.ScopesFor(auth.RoleEditor), Via: "proxy"}},
	{"listener", auth.Principal{Kind: auth.KindHuman, ID: "carol", Name: "carol", User: "carol", Role: auth.RoleListener, Scopes: auth.ScopesFor(auth.RoleListener), Via: "proxy"}},
	{"agent-read", agentOf(auth.ScopeRead)},
	{"agent-write", agentOf(auth.ScopeRead, auth.ScopeWrite)},
	{"agent-admin", agentOf(auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin)},
	{"device", deviceOf("", auth.ScopeRead, auth.ScopeWrite)},
	{"device-editor", deviceOf(auth.RoleEditor, auth.ScopeListen, auth.ScopeRead, auth.ScopeRate, auth.ScopeWrite)},
	{"device-listener", deviceOf(auth.RoleListener, auth.ScopeListen, auth.ScopeRate)},
}

func agentOf(s ...auth.Scope) auth.Principal {
	return auth.Principal{Kind: auth.KindAgent, ID: "tok1", Name: "agent", Scopes: s, Via: "token"}
}

func deviceOf(role auth.Role, s ...auth.Scope) auth.Principal {
	p := auth.Principal{Kind: auth.KindDevice, ID: "dev1", Name: "phone", Role: role, Scopes: s, Via: "device"}
	if role != "" {
		p.User = map[auth.Role]string{auth.RoleEditor: "bob", auth.RoleListener: "carol"}[role]
	}
	return p
}

// failClosedHolds are the operations F1b closed as OwnerOnly until the lane that builds their redaction or service
// guard lifts the tag in the same change (plan revision 5). The golden shows the lane in its hold column; the test
// requires each to be owner-only today, so a lane that lifts one must delete its entry here and re-bless the golden.
var failClosedHolds = map[string]string{}

// cell renders one Permitted outcome: Y, or a short refusal code.
func cell(pr *api.Problem) string {
	if pr == nil {
		return "Y"
	}
	switch pr.Code {
	case "OWNER_ONLY":
		return "OO"
	case "HUMAN_ONLY":
		return "HO"
	case "FORBIDDEN_SCOPE":
		return "SC"
	case "UNAUTHENTICATED":
		return "AN"
	}
	return pr.Code
}

func matrixOps(tb testing.TB) []api.OpInfo {
	tb.Helper()
	a := api.New(http.NewServeMux(), api.Deps{Version: "x"})
	ops := api.Operations(a)
	slices.SortFunc(ops, func(x, y api.OpInfo) int { return strings.Compare(x.ID, y.ID) })
	return ops
}

const matrixHeader = `# Role matrix (C305, C314.1): every registered operation x principal row, as api.Permitted answers it.
# Y allowed; OO owner-only (403 OWNER_ONLY); HO human-only (403 HUMAN_ONLY); SC missing scope (403 FORBIDDEN_SCOPE).
# Columns: op, scope, hold, then ` + "%s" + `.
# hold names the lane that lifts a fail-closed OwnerOnly hold (plan revision 5); "-" means none.
# Regenerate with: go test ./internal/api -run TestRoleMatrixComplete -update
`

func renderMatrix(ops []api.OpInfo) string {
	var b strings.Builder
	names := make([]string, len(matrixRows))
	for i, r := range matrixRows {
		names[i] = r.Name
	}
	fmt.Fprintf(&b, matrixHeader, strings.Join(names, ", "))
	for _, op := range ops {
		hold := "-"
		if h, ok := failClosedHolds[op.ID]; ok {
			hold = strings.ReplaceAll(h, " ", "_")
		}
		fmt.Fprintf(&b, "%s\t%s\t%s", op.ID, op.Meta.Scope, hold)
		for _, r := range matrixRows {
			fmt.Fprintf(&b, "\t%s", cell(api.Permitted(r.P, op.Meta)))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// TestRoleMatrixComplete pins what every operation does for every role and delegate row. A new operation is not in the
// golden until a reviewer blesses its row with -update, so it fails here (fail closed) rather than ship unclassified.
func TestRoleMatrixComplete(t *testing.T) {
	t.Parallel()
	ops := matrixOps(t)
	got := renderMatrix(ops)
	path := filepath.Join("testdata", "role_matrix.golden")
	if *updateFixtures {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	want := string(raw)

	// Fail closed first: name every operation the golden does not classify, and every row it keeps for a removed one.
	inGolden := map[string]bool{}
	for line := range strings.SplitSeq(want, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		inGolden[strings.SplitN(line, "\t", 2)[0]] = true
	}
	inRegistry := map[string]bool{}
	for _, op := range ops {
		inRegistry[op.ID] = true
		if !inGolden[op.ID] {
			t.Errorf("%s: not classified in testdata/role_matrix.golden (decide its roles, then -update)", op.ID)
		}
	}
	for id := range inGolden {
		if !inRegistry[id] {
			t.Errorf("%s: golden row for an operation that is not registered", id)
		}
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("role matrix differs from golden (-want +got; rerun with -update after review):\n%s", diff)
	}
}

// preU1Allows is the pre-U1 rule for a principal with no role, written independently of Permitted: a human-only
// operation refuses every non-human, and otherwise the legacy scope decides (read implies listen, write implies rate).
func preU1Allows(p auth.Principal, m api.Meta) bool {
	if m.HumanOnly && p.Kind != auth.KindHuman {
		return false
	}
	have := map[auth.Scope]bool{}
	for _, s := range p.Scopes {
		have[s] = true
	}
	switch m.Scope {
	case auth.ScopeListen:
		return have[auth.ScopeListen] || have[auth.ScopeRead]
	case auth.ScopeRate:
		return have[auth.ScopeRate] || have[auth.ScopeWrite]
	}
	return have[m.Scope]
}

// TestRoleMatrixDelegatesUnchanged: the agent rows and the Owner's device row equal the pre-U1 behaviour for every
// operation, so no tag added by U1 narrows the Owner's agents or phone (D-U1-6).
func TestRoleMatrixDelegatesUnchanged(t *testing.T) {
	t.Parallel()
	for _, op := range matrixOps(t) {
		for _, r := range matrixRows {
			if r.P.Kind == auth.KindHuman || r.P.Role != "" {
				continue
			}
			if got, want := api.Permitted(r.P, op.Meta) == nil, preU1Allows(r.P, op.Meta); got != want {
				t.Errorf("%s as %s: Permitted allows=%v, pre-U1 rule allows=%v", op.ID, r.Name, got, want)
			}
		}
	}
}

// TestRoleMatrixFailClosedHolds: every hold is registered and owner-only for an Editor and a Listener, human or device,
// and no other op is held without an entry (an OwnerOnly op with no hold is a permanent Owner op or a D-U1 class).
func TestRoleMatrixFailClosedHolds(t *testing.T) {
	t.Parallel()
	byID := map[string]api.OpInfo{}
	for _, op := range matrixOps(t) {
		byID[op.ID] = op
	}
	for id, lane := range failClosedHolds {
		op, ok := byID[id]
		if !ok {
			t.Errorf("hold %s (%s): not registered; delete the entry", id, lane)
			continue
		}
		if !op.Meta.OwnerOnly {
			t.Errorf("hold %s (%s): no longer owner_only; the lane that lifted it must delete the hold and re-bless the golden", id, lane)
		}
		for _, r := range matrixRows {
			if (r.P.Role == auth.RoleEditor || r.P.Role == auth.RoleListener) && api.Permitted(r.P, op.Meta) == nil {
				t.Errorf("hold %s: %s passes", id, r.Name)
			}
		}
	}
}

// TestRoleMatrixListenerBounds: whatever the golden says, a Listener row (human or device) never passes an op outside
// scopes listen and rate, and no non-owner row ever passes a human-only or owner-only op.
func TestRoleMatrixListenerBounds(t *testing.T) {
	t.Parallel()
	for _, op := range matrixOps(t) {
		for _, r := range matrixRows {
			if r.P.Role != auth.RoleEditor && r.P.Role != auth.RoleListener {
				continue
			}
			pr := api.Permitted(r.P, op.Meta)
			if pr == nil && (op.Meta.OwnerOnly || op.Meta.HumanOnly && !r.P.IsHuman()) {
				t.Errorf("%s: %s passes an owner-only or human-only op", op.ID, r.Name)
			}
			if pr == nil && r.P.Role == auth.RoleListener && op.Meta.Scope != auth.ScopeListen && op.Meta.Scope != auth.ScopeRate {
				t.Errorf("%s: %s passes scope %s", op.ID, r.Name, op.Meta.Scope)
			}
		}
	}
}

// C304.4: NoAudit and the listen rate class are only valid where the plan allows them.
func TestValidateMeta_NoAuditAndListenRate(t *testing.T) {
	t.Parallel()
	op := func(method, path string) huma.Operation {
		return huma.Operation{OperationID: "t_op", Method: method, Path: path, Summary: "s", Description: "d"}
	}
	base := api.Meta{Scope: auth.ScopeRate, Cost: "free", MCP: "tool", UI: "none:test", NoAudit: true, Rate: "listen"}
	if err := api.ValidateMeta(op("POST", "/api/t"), base); err != nil {
		t.Fatalf("valid NoAudit rate op refused: %v", err)
	}
	for name, mut := range map[string]func(m *api.Meta){
		"NoAudit on a write op":         func(m *api.Meta) { m.Scope = auth.ScopeWrite },
		"NoAudit on a listen op":        func(m *api.Meta) { m.Scope = auth.ScopeListen },
		"NoAudit with idempotency":      func(m *api.Meta) { m.Idempotency = "create" },
		"NoAudit on a human-only op":    func(m *api.Meta) { m.HumanOnly = true; m.OwnerOnly = true; m.MCP = "exclude:human-only" },
		"listen class on a write op":    func(m *api.Meta) { m.Scope = auth.ScopeWrite; m.NoAudit = false },
		"listen class on an admin op":   func(m *api.Meta) { m.Scope = auth.ScopeAdmin; m.NoAudit = false; m.OwnerOnly = true },
		"listen class on a generate op": func(m *api.Meta) { m.Scope = auth.ScopeGenerate; m.NoAudit = false },
	} {
		m := base
		mut(&m)
		if err := api.ValidateMeta(op("POST", "/api/t"), m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
