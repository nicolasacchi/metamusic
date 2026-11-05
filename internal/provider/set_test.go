package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

type setBackend struct {
	Backend
	name, engine string
}

func (b setBackend) Name() string              { return b.name }
func (b setBackend) Engine() string            { return b.engine }
func (b setBackend) ColdStartS() int           { return 1 }
func (b setBackend) MaxRuntime() time.Duration { return time.Minute }

func bk(name, engine string) Backend { return setBackend{name: name, engine: engine} }

// TestBackendSet_Construction_Table: the default must be a member, names are unique and non-empty, order is kept.
func TestBackendSet_Construction_Table(t *testing.T) {
	t.Parallel()
	y, a := bk(BackendFake, EngineYuE2), bk(BackendFakeACEStep, EngineACEStep)
	rows := []struct {
		name    string
		def     string
		bs      []Backend
		wantErr bool
		names   []string
	}{
		{"one", BackendFake, []Backend{y}, false, []string{BackendFake}},
		{"two keeps order", BackendFake, []Backend{y, a}, false, []string{BackendFake, BackendFakeACEStep}},
		{"two reversed", BackendFake, []Backend{a, y}, false, []string{BackendFakeACEStep, BackendFake}},
		{"empty", "", nil, false, nil},
		{"duplicate", BackendFake, []Backend{y, y}, true, nil},
		{"default missing", "nope", []Backend{y}, true, nil},
		{"nil backend", BackendFake, []Backend{nil}, true, nil},
		{"unnamed", "", []Backend{bk("", EngineYuE2)}, true, nil},
	}
	for _, r := range rows {
		s, err := NewSet(r.def, r.bs...)
		if (err != nil) != r.wantErr {
			t.Errorf("%s: err %v, want error %v", r.name, err, r.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		got := s.Names()
		if len(got) != len(r.names) {
			t.Errorf("%s: names %v, want %v", r.name, got, r.names)
			continue
		}
		for i := range got {
			if got[i] != r.names[i] {
				t.Errorf("%s: names %v, want %v", r.name, got, r.names)
			}
		}
	}
	var nilSet *Set
	if nilSet.Default() != nil || nilSet.Names() != nil || nilSet.EngineOf("x") != "" {
		t.Error("a nil set answers nothing")
	}
}

// TestBackendSet_Routing: yue2 goes to the default, acestep15 to its own backend, no cross routing.
func TestBackendSet_Routing(t *testing.T) {
	t.Parallel()
	y, rp, fa := bk(BackendRunPod, EngineYuE2), bk(BackendRunPodACEStep, EngineACEStep), bk(BackendFakeACEStep, EngineACEStep)
	both, _ := NewSet(BackendRunPod, y, rp)
	onlyY, _ := NewSet(BackendRunPod, y)
	fakeBoth, _ := NewSet(BackendFake, bk(BackendFake, EngineYuE2), fa)
	empty, _ := NewSet("")
	rows := []struct {
		name              string
		set               *Set
		engine, requested string
		want              string
		err               bool
	}{
		{"yue2 default", both, EngineYuE2, "", BackendRunPod, false},
		{"yue2 named", both, EngineYuE2, BackendRunPod, BackendRunPod, false},
		{"yue2 asks for ace backend", both, EngineYuE2, BackendRunPodACEStep, "", true},
		{"ace default", both, EngineACEStep, "", BackendRunPodACEStep, false},
		{"ace named", both, EngineACEStep, BackendRunPodACEStep, BackendRunPodACEStep, false},
		{"ace asks for yue2 backend", both, EngineACEStep, BackendRunPod, "", true},
		{"ace without backend", onlyY, EngineACEStep, "", "", true},
		{"fake pair", fakeBoth, EngineACEStep, "", BackendFakeACEStep, false},
		{"unknown engine", both, "nope", "", "", true},
		{"empty set yue2", empty, EngineYuE2, "", "", true},
		{"nil set", nil, EngineYuE2, "", "", true},
	}
	for _, r := range rows {
		got, err := r.set.Route(r.engine, r.requested)
		if r.err {
			if !errors.Is(err, ErrNoBackend) {
				t.Errorf("%s: err %v, want ErrNoBackend", r.name, err)
			}
			continue
		}
		if err != nil || got != r.want {
			t.Errorf("%s: %q, %v, want %q", r.name, got, err, r.want)
		}
	}
	if both.EngineOf(BackendRunPodACEStep) != EngineACEStep || both.EngineOf("zz") != "" {
		t.Error("EngineOf")
	}
	if b, ok := both.Get(BackendRunPod); !ok || b.Name() != BackendRunPod {
		t.Error("Get")
	}
}

// TestGateSet: one gate per backend, each reading its own provider_state row.
func TestGateSet(t *testing.T) {
	t.Parallel()
	gy := NewGate(nil, GateConfig{Mode: ModeFake}, nil)
	ga := NewGate(nil, GateConfig{Mode: ModeFake, Backend: BackendFakeACEStep, Engine: EngineACEStep}, nil)
	if gy.Config().BackendName() != BackendFake || ga.Config().BackendName() != BackendFakeACEStep {
		t.Fatal("backend names")
	}
	if (GateConfig{Mode: ModeNone, Backend: "x"}).BackendName() != "" {
		t.Error("mode none has no backend")
	}
	s, err := NewGateSet(BackendFake, gy, ga)
	if err != nil {
		t.Fatal(err)
	}
	if s.Default() != gy || len(s.Names()) != 2 {
		t.Error("default or names")
	}
	if g, ok := s.For(BackendFakeACEStep); !ok || g != ga {
		t.Error("For")
	}
	if _, err := s.StateFor(context.Background(), "zz"); !errors.Is(err, ErrNoBackend) {
		t.Errorf("unknown backend: %v", err)
	}
	if _, err := NewGateSet(BackendFake, gy, gy); err == nil {
		t.Error("duplicate gate accepted")
	}
	if _, err := NewGateSet("zz", gy); err == nil {
		t.Error("missing default accepted")
	}
}
