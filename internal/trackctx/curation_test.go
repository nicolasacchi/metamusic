package trackctx

import (
	"context"
	"errors"
	"testing"
)

type fakeCuration struct {
	got string
	out map[string]any
	err error
}

func (f *fakeCuration) ForTrack(_ context.Context, id string) (map[string]any, error) {
	f.got = id
	return f.out, f.err
}

// TestCurationSection_NilSafe: without a reader the section is absent; with one it is what the reader returns.
func TestCurationSection_NilSafe(t *testing.T) {
	t.Parallel()
	b := &Builder{}
	if m, err := b.curationSection(context.Background(), "t1"); m != nil || err != nil {
		t.Errorf("no reader: %v %v, want nil nil", m, err)
	}
	f := &fakeCuration{out: map[string]any{"records": []any{}}}
	b.UseCuration(f)
	if m, err := b.curationSection(context.Background(), "t1"); err != nil || m == nil || f.got != "t1" {
		t.Errorf("reader: %v %v (asked for %q)", m, err, f.got)
	}
	f.err = errors.New("boom")
	if _, err := b.curationSection(context.Background(), "t1"); err == nil {
		t.Error("a reader error must surface")
	}
	b.UseCuration(nil)
	if m, _ := b.curationSection(context.Background(), "t1"); m != nil {
		t.Error("UseCuration(nil) must clear the reader")
	}
}
