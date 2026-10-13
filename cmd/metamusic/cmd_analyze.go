package main

import (
	"context"
	"errors"
	"strings"
)

// trackList is a repeatable --track flag.
type trackList []string

func (l *trackList) String() string     { return strings.Join(*l, ",") }
func (l *trackList) Set(v string) error { *l = append(*l, v); return nil }

// cmdAnalyze runs Tier A analysis (C51.7): `metamusic analyze [--track ID]... [--force] [--dry-run] [--jobs N] [--json]`.
// STUB(M2b-E3): the flags parse; the command exits 2 "not implemented" until M2b-E3.
func cmdAnalyze(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("analyze", s)
	var tracks trackList
	fs.Var(&tracks, "track", "analyse only this track `ID` (repeatable)")
	_ = fs.Bool("force", false, "re-run analyzers that already have a result")
	_ = fs.Bool("dry-run", false, "print what would run; write nothing")
	_ = fs.Int("jobs", 1, "tracks analysed in parallel")
	_ = fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	return fatal(errors.New("analyze: not implemented"))
}
