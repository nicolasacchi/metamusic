package ingest

import "fmt"

// Error codes.
const (
	CodeSourceChanged   = "SOURCE_CHANGED"
	CodeInvalidAudio    = "INVALID_AUDIO"
	CodeFreezeInvariant = "FREEZE_INVARIANT"
	CodeToolFailed      = "TOOL_FAILED"
	CodeIO              = "IO"
)

// Error is an ingest failure. Path is DataDir-relative; Detail never contains
// tool argv or tag values.
type Error struct {
	Code, Path, Detail string
	Err                error
}

func (e *Error) Error() string {
	s := "ingest: " + e.Code
	if e.Path != "" {
		s += " " + e.Path
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

func errorf(code, path, format string, a ...any) *Error {
	return &Error{Code: code, Path: path, Detail: fmt.Sprintf(format, a...)}
}
