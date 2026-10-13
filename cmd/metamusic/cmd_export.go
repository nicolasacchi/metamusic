package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// cmdValidateBatch checks a songs.jsonl with the mirror of `yue2 batch` (C32):
// exit 0 when it is accepted, 1 when it has issues, 2 on usage errors.
func cmdValidateBatch(_ context.Context, args []string, s streams) error {
	if len(args) != 1 || args[0] == "" || args[0][0] == '-' {
		return usageError("validate-batch: expected FILE")
	}
	raw, err := os.ReadFile(args[0]) //nolint:gosec // the operator names the file on the command line
	if err != nil {
		return usageError(fmt.Sprintf("validate-batch: %v", err))
	}
	rep := yue2.ValidateBatch(raw, os.DirFS(filepath.Dir(args[0])))
	for _, is := range rep.Issues {
		fmt.Fprintf(s.out, "line %d: %s %s", is.Line, is.Severity, is.Code)
		if is.Field != "" {
			fmt.Fprintf(s.out, " (%s)", is.Field)
		}
		if is.Fatal {
			fmt.Fprint(s.out, " [fatal]")
		}
		fmt.Fprintln(s.out)
	}
	fmt.Fprintf(s.out, "%d lines, %d requests, %d issues\n", rep.Lines, rep.Requests, len(rep.Issues))
	if !rep.OK {
		return exitError{code: 1}
	}
	return nil
}

// cmdExport writes songs.jsonl for a selection (C39). STUB(M2a-B6).
func cmdExport(_ context.Context, _ []string, _ func(string) string, _ streams) error {
	return fatal(fmt.Errorf("export is not implemented yet (M2a-B6)"))
}
