package generation_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

const canaryReqID = "mm-canary-20261117-reference-source-1"

// canaryParent makes the ACE root take of the fixture look like a canary source take: its request id is the canary's.
func (x *rigE2) canaryParent() string {
	x.t.Helper()
	x.exec(`DROP TRIGGER recipes_immutable`)
	x.exec(`UPDATE recipes SET request_id = '` + canaryReqID + `', request_json = replace(request_json, '"id":"e2-ace-root"', '"id":"` + canaryReqID + `"') WHERE request_id = 'e2-ace-root'`)
	if !strings.Contains(x.str(`SELECT r.request_json FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, x.info.Root), canaryReqID) {
		x.t.Fatal("canary parent not prepared")
	}
	return x.info.Root
}

func unverifiedRef(f *acestep.Features) { f.Reference = false }

func refOn(parent string) generation.Item {
	return refItem(lineage.ModeReference, parent, generation.Reference{TrackID: parent})
}

// (a) the switch off: a canary-id reference request is refused as today.
func TestCanaryCaps_Off_Refused(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withFeatures(unverifiedRef))
	parent := x.canaryParent()
	_, err := x.estimate(agent, "", refOn(parent))
	pe := wantProblem(t, err, 422, "ENGINE_CAPABILITY")
	if pe.Details["capability"] != "reference" {
		t.Errorf("details %v", pe.Details)
	}
}

// (b) the switch on: a canary request passes the capability gate, with the file's bounds, and the take says why.
func TestCanaryCaps_On_CanaryPasses(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withFeatures(unverifiedRef), withCanaryCaps(false, "reference"))
	parent := x.canaryParent()
	e := x.mustEstimate(agent, "", refOn(parent))
	st := x.stored(e)
	if len(st) != 1 || st[0].Ref == nil {
		t.Fatalf("stored takes %+v", st)
	}
	var raw []struct {
		CanaryCaps []string `json:"canary_caps"`
	}
	if err := json.Unmarshal([]byte(x.str(`SELECT items_json FROM estimates WHERE id = ?`, e.ID)), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || len(raw[0].CanaryCaps) != 1 || raw[0].CanaryCaps[0] != "reference" {
		t.Errorf("canary_caps = %+v", raw)
	}
	// the registered engine stays verified-only: what list_engines reads is untouched by a canary estimate
	if c := x.opts.Engines[aceName].Caps(); c.ReferenceAudio {
		t.Error("the registry offers reference after a canary estimate")
	}
	// a window above the verified reference_max_s is still refused: the bounds are the file's
	long := refOn(parent)
	long.Reference.FromS, long.Reference.ToS = f64p(0), f64p(40)
	if _, err := x.estimate(agent, "", long); err == nil {
		t.Error("a 40 s reference window passed")
	}
}

func f64p(v float64) *float64 { return &v }

// (c) the switch on: a request that is not a canary one is refused exactly as before, a mixed batch included.
func TestCanaryCaps_On_NonCanaryRefused(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withFeatures(unverifiedRef), withCanaryCaps(false, "reference"))
	_, err := x.estimate(agent, "", refOn(x.info.Root))
	wantProblem(t, err, 422, "ENGINE_CAPABILITY")

	canary := refOn(x.canaryParent())
	_, err = x.estimate(agent, "", canary, refOn(x.info.Port))
	wantProblem(t, err, 422, "ENGINE_CAPABILITY")

	// a plain request id that only resembles the prefix is not a canary one
	for _, id := range []string{"mm-canary-2026-x", "mm-canary-20261117", "xmm-canary-20261117-a"} {
		if _, err := x.estimate(agent, "", composeACE(t, id, func(m map[string]any) { m["lrc"] = true })); err == nil {
			t.Errorf("%q passed", id)
		}
	}
}

// compose_new carries its own id: a canary-id compose request may use lrc when the switch names it.
func TestCanaryCaps_On_ComposeLRC(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withFeatures(func(f *acestep.Features) { f.LRC = false }), withCanaryCaps(false, "lrc"))
	lrc := func(id string) generation.Item { return composeACE(t, id, func(m map[string]any) { m["lrc"] = true }) }
	if _, err := x.estimate(agent, "", lrc("mm-canary-20261117-lrc-lrc-1")); err != nil {
		t.Errorf("canary lrc: %v", err)
	}
	_, err := x.estimate(agent, "", lrc("my-song"))
	wantProblem(t, err, 422, "ENGINE_CAPABILITY")
}

// (d) base-class modes keep needing the base models: tasks without base, or a bundle_only file, keep them refused.
func TestCanaryCaps_On_BaseModelsStillRequired(t *testing.T) {
	t.Parallel()
	tracks := []string{"vocals"}
	complete := func(parent string) generation.Item {
		it := parentItem(lineage.ModeComplete, parent)
		it.Edit = &generation.Edit{Tracks: tracks}
		return it
	}
	off := func(f *acestep.Features) { f.Tasks = []string{"text2music"} }
	for _, tc := range []struct {
		name string
		opts []e2Opt
		ok   bool
	}{
		{"tasks turbo only", []e2Opt{withFeatures(off), withCanaryCaps(false, "complete")}, false},
		{"models bundle_only", []e2Opt{withFeatures(func(f *acestep.Features) { off(f); f.ModelsMode = acestep.ModelsBundleOnly }), withCanaryCaps(true, "complete")}, false},
		{"base loaded", []e2Opt{withFeatures(off), withCanaryCaps(true, "complete")}, true},
	} {
		x := newRigE2(t, tc.opts...)
		_, err := x.estimate(agent, "", complete(x.canaryParent()))
		if tc.ok {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		pe := wantProblem(t, err, 422, "ENGINE_CAPABILITY")
		if pe.Details["capability"] != "complete" {
			t.Errorf("%s: details %v", tc.name, pe.Details)
		}
	}
}
