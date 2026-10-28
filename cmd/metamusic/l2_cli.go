package main

import (
	"context"
	"io"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// l2VerifyTapeFiles is the `library verify --files` check of R8 (C122.5): every ready tape's file exists with the recorded
// size (the data directory comes from config.LoadOffline(getenv)). It returns the number of errors it printed. STUB(L2-T2): lane T2 replaces the body; the signature is frozen.
func l2VerifyTapeFiles(ctx context.Context, st *store.Store, getenv func(string) string, out io.Writer) (errors int, err error) {
	return 0, nil
}
