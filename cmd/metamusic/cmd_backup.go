package main

import (
	"context"
	"fmt"
)

// cmdBackup takes or lists backups (C39). STUB(M2a-B7).
func cmdBackup(_ context.Context, _ []string, _ func(string) string, _ streams) error {
	return fatal(fmt.Errorf("backup is not implemented yet (M2a-B7)"))
}

// cmdRestore rebuilds a data directory from a snapshot (C39). STUB(M2a-B7).
func cmdRestore(_ context.Context, _ []string, _ func(string) string, _ streams) error {
	return fatal(fmt.Errorf("restore is not implemented yet (M2a-B7)"))
}
