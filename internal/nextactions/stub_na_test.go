package nextactions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
)

// The method set of C139 that lanes NA1 to NA3 must keep; this file goes with stub_na.go.
var (
	_ func(*nextactions.Service, context.Context, auth.Principal, nextactions.Filter) (nextactions.Page, error) = (*nextactions.Service).List
	_ func(*nextactions.Service, context.Context, auth.Principal, string, int) ([]nextactions.Action, error)    = (*nextactions.Service).ForTake
)

func TestStub_ListUnsupported(t *testing.T) {
	t.Parallel()
	s := nextactions.New(nil, nil, nil, nil, nil)
	if _, err := s.List(context.Background(), auth.Principal{}, nextactions.Filter{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("List = %v, want ErrUnsupported until lane NA1", err)
	}
	if len(nextactions.Rules) != 0 {
		t.Error("Rules must be empty until lanes NA1 to NA3 fill them")
	}
}
