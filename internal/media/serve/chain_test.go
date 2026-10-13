package serve

import (
	"context"
	"errors"
	"testing"
)

type fakeResolver struct {
	obj Object
	err error
	n   *int
}

func (f fakeResolver) Resolve(context.Context, string, string) (Object, error) {
	if f.n != nil {
		*f.n++
	}
	return f.obj, f.err
}

func TestChain(t *testing.T) {
	boom := errors.New("boom")
	var calls int
	tests := []struct {
		name string
		rs   []Resolver
		want string
		err  error
	}{
		{"first wins", []Resolver{fakeResolver{obj: Object{RelPath: "a"}}, fakeResolver{obj: Object{RelPath: "b"}}}, "a", nil},
		{"falls through not found", []Resolver{fakeResolver{err: ErrNotFound}, fakeResolver{obj: Object{RelPath: "b"}}}, "b", nil},
		{"wrapped not found falls through", []Resolver{fakeResolver{err: errors.Join(ErrNotFound, boom)}, fakeResolver{obj: Object{RelPath: "b"}}}, "b", nil},
		{"hard error stops", []Resolver{fakeResolver{err: boom}, fakeResolver{obj: Object{RelPath: "b"}, n: &calls}}, "", boom},
		{"all not found", []Resolver{fakeResolver{err: ErrNotFound}, fakeResolver{err: ErrNotFound}}, "", ErrNotFound},
		{"nil resolvers skipped", []Resolver{nil, fakeResolver{obj: Object{RelPath: "c"}}}, "c", nil},
		{"empty", nil, "", ErrNotFound},
	}
	for _, tc := range tests {
		got, err := Chain(tc.rs...).Resolve(context.Background(), "sha", "flac")
		if !errors.Is(err, tc.err) || got.RelPath != tc.want {
			t.Errorf("%s: got %+v, %v; want %q, %v", tc.name, got, err, tc.want, tc.err)
		}
	}
	if calls != 0 {
		t.Errorf("a hard error must stop the chain; later resolver ran %d times", calls)
	}
}
