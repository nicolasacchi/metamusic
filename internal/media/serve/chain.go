package serve

import (
	"context"
	"errors"
)

type chain []Resolver

// Chain tries each resolver in order and returns the first object found. A
// resolver answering ErrNotFound passes to the next; any other error stops
// the chain. Nil resolvers are skipped; with none the chain is always
// ErrNotFound.
func Chain(resolvers ...Resolver) Resolver {
	out := make(chain, 0, len(resolvers))
	for _, r := range resolvers {
		if r != nil {
			out = append(out, r)
		}
	}
	return out
}

func (c chain) Resolve(ctx context.Context, sha, ext string) (Object, error) {
	for _, r := range c {
		o, err := r.Resolve(ctx, sha, ext)
		if err == nil {
			return o, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Object{}, err
		}
	}
	return Object{}, ErrNotFound
}
