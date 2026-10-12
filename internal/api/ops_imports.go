package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerImports(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_import_runs", Method: "GET", Path: "/api/imports",
		Summary:     "List import runs",
		Description: "Import runs, newest first, with state, plan digest and error and warning counts. Pages are capped in size; follow next_cursor.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/import"},
		func(ctx context.Context, in *listImportsIn) (*importRunsOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			l, err := d.Library.ImportRuns(ctx, in.Limit, in.Cursor)
			if err != nil {
				return nil, libErr(err)
			}
			return &importRunsOut{Body: l}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_import_run", Method: "GET", Path: "/api/imports/{id}",
		Summary:     "Get one section of an import report",
		Description: "One run and a page of one report section (errors by default). The whole inventory is never returned at once; follow next_cursor.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/import"},
		func(ctx context.Context, in *importRunIn) (*importRunPageOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			p, err := d.Library.ImportRun(ctx, in.ID, in.Section, in.Cursor, in.Limit)
			if err != nil {
				return nil, libErr(err)
			}
			return &importRunPageOut{Body: p}, nil
		})
}
