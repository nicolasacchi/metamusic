package mcpgen

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

// callTimeout bounds every MCP call and every REST request the parity tests make: a stuck call fails with a message
// (context deadline exceeded, naming the operation through the caller's t.Fatalf) instead of hanging the shard until
// the Go test timeout. Generous on purpose: a -race shard under CI load answers in well under a second.
const callTimeout = 60 * time.Second

// callCtx is a context that expires after callTimeout. The cancel is tied to the same timer, so nothing leaks past it.
func callCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	time.AfterFunc(callTimeout, cancel)
	return ctx
}

// testHTTP is the bounded client for raw REST/MCP requests in these tests (http.DefaultClient has no timeout).
var testHTTP = &http.Client{Timeout: callTimeout}
