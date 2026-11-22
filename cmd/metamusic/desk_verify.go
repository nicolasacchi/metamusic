package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/desk"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// deskVerify runs the Composer's Desk checks of `library verify` (M5b C357.4): D1 every plan file exists with its sha256, D2 a take plan
// is its take's score, D3 every completed plan job has a plan (all errors), D4 an audition whose file is missing (a warning: it is
// rebuilt on the next request). Lines use the renditions form "<severity> <code> <subject> <detail>" and never print score text.
func deskVerify(ctx context.Context, st *store.Store, getenv func(string) string, out io.Writer) (errors int, err error) {
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return 0, err
	}
	root, err := os.OpenRoot(cfg.DataDir)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	fs, err := desk.Verify(ctx, st.R, root)
	if err != nil {
		return 0, err
	}
	for _, f := range fs {
		fmt.Fprintf(out, "%s %s %s %s\n", f.Severity, f.Code, f.Subject, f.Detail)
		if f.Severity == "error" {
			errors++
		}
	}
	return errors, nil
}
