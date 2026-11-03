package ops

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

func run(context.Context, RunIn) (RunOut, error)     { return RunOut{}, nil }
func units(int64, int64, int, json.RawMessage) int64 { return 1 }

func TestRegistry(t *testing.T) {
	saved := reg
	reg = map[string]entry{}
	defer func() { reg = saved }()
	Register("widen", run, units)
	Register("master", run, units)
	if got := Kinds(); !slices.Equal(got, []string{"master", "widen"}) {
		t.Errorf("Kinds = %v, want sorted", got)
	}
	if _, _, ok := Lookup("master"); !ok {
		t.Error("Lookup(master) failed")
	}
	if _, _, ok := Lookup("glacier"); ok {
		t.Error("Lookup found an unregistered kind")
	}
	for name, f := range map[string]func(){
		"duplicate":  func() { Register("master", run, units) },
		"empty kind": func() { Register("", run, units) },
		"nil run":    func() { Register("x", nil, units) },
		"nil units":  func() { Register("y", run, nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			f()
		}()
	}
}
