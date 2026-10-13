package api

import (
	"errors"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// m2bErr maps duels, ears and clip service errors to the problem model (C53):
// a *problem.Error keeps its status, code and details; a library "not found"
// is 404; errors.ErrUnsupported is a Stage-1 stub (501 NOT_IMPLEMENTED); every
// other error is returned unchanged and becomes a 500 INTERNAL whose cause
// the Huma error hook logs through redact.Error. task names the lane that
// replaces the stub (STUB(M2b-<task>)).
func m2bErr(err error, task string) error {
	var pe *problem.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &pe):
		return Err(pe.Status, pe.Code, pe.Message, pe.Details)
	case errors.Is(err, errors.ErrUnsupported):
		return Err(501, "NOT_IMPLEMENTED", "this operation is not implemented yet", map[string]any{"task": task})
	}
	return libErr(err)
}
