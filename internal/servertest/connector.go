package servertest

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// ConnectorOpts select the hosted-connector client of Rig.Connector (C117.1).
type ConnectorOpts struct {
	Client string       // default "test-claude"
	Scopes []auth.Scope // set through set_connector_grant after the first call
}

// Connector connects a go-sdk MCP client over <PublicURL>/mcp with the OAuth authorization-code handler against the
// rig's stub provider and returns the session and the grant id (C117.1). It needs Options.OIDC.
// STUB(M4a-C2): skips the test.
func (r *Rig) Connector(tb testing.TB, _ ConnectorOpts) (*mcp.ClientSession, string) {
	tb.Helper()
	tb.Skip("M4a-C2: servertest.Rig.Connector not implemented yet")
	return nil, ""
}
