package connectors_test

import (
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/connectors"
)

// TestVerifier_StubFailsClosed: until M4a-C2 the verifier refuses every bearer, so no connector principal exists.
func TestVerifier_StubFailsClosed(t *testing.T) {
	t.Parallel()
	var v auth.ConnectorVerifier = connectors.NewVerifier(connectors.VerifierOptions{})
	p, err := v.VerifyConnector(t.Context(), "a.b.c")
	if !errors.Is(err, auth.ErrInvalidToken) || p.Kind != "" {
		t.Fatalf("got %+v, %v", p, err)
	}
}
