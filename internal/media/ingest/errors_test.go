package ingest

import (
	"errors"
	"testing"
)

func TestError_Format(t *testing.T) {
	cause := errors.New("cause")
	e := &Error{Code: CodeIO, Path: "audio/masters/x.flac", Detail: "link failed", Err: cause}
	if got := e.Error(); got != "ingest: IO audio/masters/x.flac: link failed" {
		t.Fatal(got)
	}
	if !errors.Is(e, cause) {
		t.Fatal("Unwrap")
	}
	if got := errorf(CodeToolFailed, "", "x %d", 1).Error(); got != "ingest: TOOL_FAILED: x 1" {
		t.Fatal(got)
	}
}
