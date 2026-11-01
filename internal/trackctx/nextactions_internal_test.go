package trackctx

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

type fakeReader struct {
	out  []string
	err  error
	max  int
	call int
}

func (f *fakeReader) ForTake(_ context.Context, _ auth.Principal, _ string, limit int) ([]string, error) {
	f.call++
	f.max = limit
	return f.out, f.err
}

// TestContext_Hooks_NilSafe: a nil reader, a reader that is not implemented yet and a reader that fails all leave the base
// next actions untouched, so the M3/L1/L2 goldens stay byte-identical (C139.4).
func TestContext_Hooks_NilSafe(t *testing.T) {
	t.Parallel()
	base := []string{"a", "b"}
	b := &Builder{}
	if got := b.museAwareActions(context.Background(), auth.Principal{}, "t", slices.Clone(base)); !slices.Equal(got, base) {
		t.Errorf("nil reader: %v", got)
	}
	for _, err := range []error{errors.ErrUnsupported, errors.New("boom")} {
		b.nextact = &fakeReader{out: []string{"x"}, err: err}
		if got := b.museAwareActions(context.Background(), auth.Principal{}, "t", slices.Clone(base)); !slices.Equal(got, base) {
			t.Errorf("reader error %v: %v", err, got)
		}
	}
}

// TestContext_NextActions_Room: max = min(2, 5 - len(base) - 1) and a full base asks nothing (the worked cases of C139.4).
func TestContext_NextActions_Room(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		base, wantMax, wantAdded int
	}{{0, 2, 2}, {1, 2, 2}, {2, 2, 2}, {3, 1, 1}, {4, 0, 0}} {
		r := &fakeReader{out: []string{"p", "q", "r"}}
		b := &Builder{nextact: r}
		base := make([]string, tc.base)
		got := b.museAwareActions(context.Background(), auth.Principal{}, "t", base)
		if len(got)-tc.base != tc.wantAdded {
			t.Errorf("base %d: added %d, want %d", tc.base, len(got)-tc.base, tc.wantAdded)
		}
		if tc.wantMax == 0 && r.call != 0 || tc.wantMax != 0 && r.max != tc.wantMax {
			t.Errorf("base %d: reader asked for %d (%d calls), want %d", tc.base, r.max, r.call, tc.wantMax)
		}
	}
}
