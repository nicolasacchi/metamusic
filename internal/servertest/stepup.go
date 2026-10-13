package servertest

import (
	"net/http"
	"net/url"
	"testing"
)

// StepUp confirms the operator's password on a local-mode human client so the
// /human/ area opens for the next ten minutes of rig time. Proxy mode needs no
// step-up (the edge's two-factor rule covers /human/) and the call is a no-op.
func (r *Rig) StepUp(tb testing.TB, c *Client) {
	tb.Helper()
	if r.mode != "local" {
		return
	}
	form := url.Values{"password": {r.password}, "next": {"/human/tokens"}}.Encode()
	resp := c.Do(tb, "POST", "/auth/step-up", form, "Content-Type", "application/x-www-form-urlencoded")
	if resp.Status != http.StatusSeeOther {
		tb.Fatalf("servertest: step-up: status %d", resp.Status)
	}
}
