package api

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/clean"
)

// TestRenditionErr: a busy builder is 503 RENDITION_BUSY carrying Retry-After, a failed build 503 RENDITION_UNAVAILABLE
// (the cause is logged, not returned), anything else the library mapping.
func TestRenditionErr(t *testing.T) {
	t.Parallel()
	err := renditionErr(&clean.ErrBusy{RetryAfter: 7 * time.Second})
	var se huma.StatusError
	if !errors.As(err, &se) || se.GetStatus() != 503 || !strings.Contains(err.Error(), "RENDITION_BUSY") {
		t.Fatalf("busy: %v", err)
	}
	var he huma.HeadersError
	if !errors.As(err, &he) || he.GetHeaders().Get("Retry-After") != "7" {
		t.Errorf("busy has no Retry-After: %v", err)
	}
	if err := renditionErr(&clean.ErrBusy{}); !errors.As(err, &he) || he.GetHeaders().Get("Retry-After") != "1" {
		t.Errorf("zero retry: %v", err)
	}
	if err := renditionErr(&clean.ErrUnavailable{Err: errors.New("boom")}); !strings.Contains(err.Error(), "RENDITION_UNAVAILABLE") || strings.Contains(err.Error(), "boom") {
		t.Errorf("unavailable: %v", err)
	}
	if err := renditionErr(library.ErrNotFound); !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Errorf("not found: %v", err)
	}
}
