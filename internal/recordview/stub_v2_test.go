package recordview_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// TestStubsV2_FrozenMethodSet pins the method set of C124.2, including tape.Source, and checks that the stubs answer
// errors.ErrUnsupported (the API maps it to 501). Lane V2 deletes stub_v2.go and this file.
func TestStubsV2_FrozenMethodSet(t *testing.T) {
	t.Parallel()
	var _ interface {
		Get(context.Context, auth.Principal, string, recordview.GetOptions) (recordview.View, error)
		Snapshot(context.Context, string, tape.SnapOptions) (tape.Snapshot, error)
		CoverInput(context.Context, string) (cover.Input, error)
		CoverRefs(context.Context, auth.Principal, []string) (map[string]recordview.CoverRef, error)
	} = (*recordview.Service)(nil)
	var _ tape.Source = (*recordview.Service)(nil)
	s, ctx := recordview.New(recordview.Options{}), context.Background()
	if _, err := s.Get(ctx, auth.Principal{}, "x", recordview.GetOptions{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Get: %v", err)
	}
	if _, err := s.Snapshot(ctx, "x", tape.SnapOptions{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Snapshot: %v", err)
	}
	if _, err := s.CoverInput(ctx, "x"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("CoverInput: %v", err)
	}
	if _, err := s.CoverRefs(ctx, auth.Principal{}, nil); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("CoverRefs: %v", err)
	}
}

// TestIncludes_Frozen: the include vocabulary is the schema enum of get_record.
func TestIncludes_Frozen(t *testing.T) {
	t.Parallel()
	want := []string{"liner", "energy", "tapes", "changes", "preview"}
	if len(recordview.Includes) != len(want) {
		t.Fatalf("Includes = %v", recordview.Includes)
	}
	for i, w := range want {
		if recordview.Includes[i] != w {
			t.Errorf("Includes[%d] = %q, want %q", i, recordview.Includes[i], w)
		}
	}
}
