package acceptance_test

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// l3Rig is the settled L3 library (FixtureL3 on the L2 and L1 base) behind the real server with the real feature step, as serve
// builds it: the Reconciler is idle and tests call Rig.Settle; a taxonomy decision refreshes the feature rows through the
// recompute hook without a Settle.
func l3Rig(t *testing.T, o servertest.Options) *servertest.Rig {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	o.Fixture, o.M2c, o.M3, o.L1, o.L2, o.L3, o.RealFeatures = true, true, true, true, true, true, true
	r := servertest.New(t, o)
	r.Settle(t)
	return r
}

// l3Song is the id of a fixture Song by its label ("T1", "d3").
func l3Song(t *testing.T, r *servertest.Rig, label string) string {
	t.Helper()
	return l2ID(t, r, "songs", "Synthetic Song "+label)
}

// l3Agent calls operations over MCP the way an agent does: the first-class tool when the registry says so, else call_operation.
type l3Agent struct {
	t    *testing.T
	sess *mcp.ClientSession
	mode map[string]string
}

func newL3Agent(t *testing.T, r *servertest.Rig, sess *mcp.ClientSession) *l3Agent {
	t.Helper()
	a := &l3Agent{t: t, sess: sess, mode: map[string]string{}}
	for _, op := range api.Operations(r.API) {
		a.mode[op.ID] = op.Meta.MCP
	}
	return a
}

// raw returns the unprocessed tool result.
func (a *l3Agent) raw(op string, args map[string]any) *mcp.CallToolResult {
	a.t.Helper()
	m, ok := a.mode[op]
	if !ok || m != "tool" && m != "generic" {
		a.t.Fatalf("%s has no MCP face (%q)", op, m)
	}
	return rawTool(a.t, a.sess, op, args, m != "tool")
}

// call fails on a tool error and returns the structured answer.
func (a *l3Agent) call(op string, args map[string]any) map[string]any {
	a.t.Helper()
	res := a.raw(op, args)
	if res.IsError {
		a.t.Fatalf("%s: tool error: %+v", op, res.Content)
	}
	return obj(decodeStructured(a.t, res))
}
