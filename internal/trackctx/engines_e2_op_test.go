package trackctx_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

type fakeAdvisor struct {
	adv   trackctx.EngineAdvice
	err   error
	calls int
}

func (f *fakeAdvisor) Advise(context.Context, auth.Principal, string, int) (trackctx.EngineAdvice, error) {
	f.calls++
	return f.adv, f.err
}

func TestACENextActions_Table(t *testing.T) {
	t.Parallel()
	full := trackctx.ACERuleInput{Adopted: true, CanGenerate: true, RepaintOK: true, ExtendOK: true, LyricEditOK: true, ReferenceOK: true, TaskIsRender: true}
	with := func(f func(*trackctx.ACERuleInput)) trackctx.ACERuleInput { in := full; f(&in); return in }
	tests := []struct {
		name string
		in   trackctx.ACERuleInput
		want []string // substrings, in order
	}{
		{"everything on", full, []string{"extend or repaint", "lyrics", "reference"}},
		{"not adopted: nothing that costs money is suggested", with(func(i *trackctx.ACERuleInput) { i.Adopted = false }), nil},
		{"no generate scope", with(func(i *trackctx.ACERuleInput) { i.CanGenerate = false }), nil},
		{"not an ACE-Step take", with(func(i *trackctx.ACERuleInput) { i.TaskIsRender = false }), nil},
		{"extend only", with(func(i *trackctx.ACERuleInput) { i.RepaintOK = false }), []string{"extend this take", "lyrics", "reference"}},
		{"repaint only", with(func(i *trackctx.ACERuleInput) { i.ExtendOK = false }), []string{"repaint a section", "lyrics", "reference"}},
		{"turbo-only verified: no extend, no repaint", with(func(i *trackctx.ACERuleInput) { i.ExtendOK, i.RepaintOK, i.ReferenceOK = false, false, false }), []string{"lyrics"}},
		{"lyric_edit absent from Caps.Tasks", with(func(i *trackctx.ACERuleInput) { i.LyricEditOK = false }), []string{"extend or repaint", "reference"}},
		{"nothing on", trackctx.ACERuleInput{Adopted: true, CanGenerate: true, TaskIsRender: true}, nil},
	}
	for _, tc := range tests {
		got := trackctx.ACENextActions(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("%s: %q, want %d actions", tc.name, got, len(tc.want))
			continue
		}
		for i, w := range tc.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("%s: action %d %q lacks %q", tc.name, i, got[i], w)
			}
		}
	}
	for _, a := range trackctx.ACENextActions(full) {
		if strings.Contains(a, "lint_recipe") || strings.Contains(a, "draft editor") {
			t.Errorf("ACE action points at the YuE2 draft tooling: %q", a)
		}
	}
}

// TestContext_NextActions_ACEStep: the advisor's actions go after the base and Muse rules, within the five-line cap and before
// the critique; a yue2 take (zero advice) is untouched.
func TestContext_NextActions_ACEStep(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	e.b.UseNextActions(nil)
	base := nextActions(t, e, generator, lt.TrackLuna)

	adv := &fakeAdvisor{}
	e.b.UseEngines(adv)
	if diff := cmp.Diff(base, nextActions(t, e, generator, lt.TrackLuna)); diff != "" {
		t.Errorf("zero advice changed the list (-base +got):\n%s", diff)
	}
	if adv.calls == 0 {
		t.Error("the advisor was never asked")
	}

	adv.adv = trackctx.EngineAdvice{Engine: "acestep15", Task: "text2music", Actions: []string{"ace one", "ace two", "ace three", "ace four", "ace five"},
		BlockYue2Actions: true}
	got := nextActions(t, e, generator, lt.TrackLuna)
	if len(got) > 5 || got[len(got)-1] != critique {
		t.Fatalf("list = %q: over five or the critique is not last", got)
	}
	// base rule first, then the ACE actions in the room left (5 - 1 base - 1 critique = 3), the hint dropped, the critique last
	want := []string{base[0], "ace one", "ace two", "ace three", critique}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("list (-want +got):\n%s", diff)
	}
}

func TestContext_NextActions_BlockYuE2Lines(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	// the YuE2-only lines never survive for a non-yue2 take (no such line exists in the base rules today; the filter is the
	// guard for the ones the draft tooling may add)
	e.b.UseEngines(&fakeAdvisor{adv: trackctx.EngineAdvice{Engine: "acestep15", BlockYue2Actions: true}})
	for _, a := range nextActions(t, e, generator, lt.TrackLuna) {
		if strings.Contains(a, "lint_recipe") || strings.Contains(a, "draft editor") {
			t.Errorf("YuE2 line kept: %q", a)
		}
	}
}

func TestContext_Provenance_Engine(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	e.b.UseEngines(&fakeAdvisor{adv: trackctx.EngineAdvice{Engine: "acestep15", Task: "repaint", CrossEngine: true, Stems: []string{"bass"}}})
	b, err := e.b.Build(context.Background(), generator, lt.TrackJobChild, nil)
	if err != nil {
		t.Fatal(err)
	}
	pv := rawMap(t, marshal(t, b))["provenance"].(map[string]any)
	if pv["engine"] != "acestep15" || pv["task"] != "repaint" || pv["cross_engine"] != true {
		t.Errorf("provenance = %v", pv)
	}
	if s, _ := pv["stems"].([]any); len(s) != 1 || s[0] != "bass" {
		t.Errorf("stems = %v", pv["stems"])
	}
	// an advisor with no facts (a yue2 take) adds no key; a failing advisor never fails /context
	for _, adv := range []*fakeAdvisor{{}, {err: errors.New("boom")}} {
		e.b.UseEngines(adv)
		b, err := e.b.Build(context.Background(), generator, lt.TrackJobChild, nil)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]string{"origin", "import_rule", "truncated", "cost", "env", "job_id"}, keys(t, section(t, marshal(t, b), "provenance"))); diff != "" {
			t.Errorf("provenance keys moved for a yue2 take (-want +got):\n%s", diff)
		}
	}
}
