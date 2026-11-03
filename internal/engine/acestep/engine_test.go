package acestep

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
)

func TestMinAudio_NotPlannedForACEStep(t *testing.T) {
	t.Parallel()
	if _, ok := any(Engine{}).(engine.MinAudioPlanner); ok {
		t.Fatal("acestep.Engine must not implement engine.MinAudioPlanner")
	}
}

func TestIngestPolicy_ACEStep(t *testing.T) {
	t.Parallel()
	var e engine.Engine = Engine{}
	if engine.ReportsTruncation(e) || engine.LearnsRTF(e) {
		t.Error("ingest policy must be false/false")
	}
	if _, ok := engine.CodecOf(e); !ok {
		t.Error("no RecipeCodec")
	}
}

func TestCaps_Default(t *testing.T) {
	t.Parallel()
	got, _ := json.Marshal(Engine{Verified: EmbeddedFeatures()}.Caps())
	want := `{"Text2Song":true,"PlanScore":false,"RenderFromScore":false,"RepaintInterval":false,"Stems":false,"ReferenceAudio":false,"LoRA":false,` +
		`"Extend":false,"Complete":false,"LRC":false,"Tempo":true,"Key":true,"MinSeconds":10,"MaxSeconds":480,"SampleRate":48000,` +
		`"RepaintMinS":0,"RepaintMaxS":0,"ExtendMinS":0,"ExtendMaxS":0,"ReferenceMaxS":0,"StemNames":null,` +
		`"Tasks":["compose_new","from_template","lyric_edit","new_seed","port","style_edit"]}`
	if string(got) != want {
		t.Errorf("caps:\n%s\nwant\n%s", got, want)
	}
	if !reflect.DeepEqual(Engine{}.Caps(), Engine{Verified: EmbeddedFeatures()}.Caps()) {
		t.Error("zero Engine must offer what the embedded file offers")
	}
}

func TestCaps_Table_AceStep(t *testing.T) {
	t.Parallel()
	feat := func(mut func(*Features)) engine.Caps {
		f := EmbeddedFeatures()
		f.CanaryCommit = "c"
		mut(&f)
		return Engine{Verified: f}.Caps()
	}
	c := feat(func(f *Features) { f.Tasks = []string{"text2music", "repaint"}; f.SeedReproducible = true })
	if !c.RepaintInterval || c.RepaintMinS != 3 || c.RepaintMaxS != 90 || !slices.Contains(c.Tasks, "repaint") || !slices.Contains(c.Tasks, "same_seed_edit") || c.Extend {
		t.Errorf("repaint: %+v", c)
	}
	c = feat(func(f *Features) { f.Tasks = []string{"text2music", "extend"} })
	if !c.Extend || c.ExtendMaxS != 90 || !slices.Contains(c.Tasks, "extend") {
		t.Errorf("extend: %+v", c)
	}
	c = feat(func(f *Features) { f.Reference = true; f.LRC = true })
	if !c.ReferenceAudio || c.ReferenceMaxS != 30 || !c.LRC || !slices.Contains(c.Tasks, "reference") {
		t.Errorf("reference: %+v", c)
	}
	c = feat(func(f *Features) { f.Tasks = []string{"text2music", "complete", "extract"} })
	if !c.Complete || !c.Stems || len(c.StemNames) != 12 || !slices.IsSorted(c.Tasks) || !slices.Contains(c.Tasks, "stems") {
		t.Errorf("base: %+v", c)
	}
}

func TestCaps_BaseClassNeedsBaseModels(t *testing.T) {
	t.Parallel()
	f := EmbeddedFeatures()
	f.CanaryCommit, f.Tasks = "c", []string{"text2music", "complete", "extract"}
	f.ModelsMode = ModelsBundleOnly
	c := Engine{Verified: f}.Caps()
	if c.Complete || c.Stems || len(c.StemNames) != 0 {
		t.Errorf("bundle_only must keep base-class modes off: %+v", c)
	}
	f.ModelsMode = ModelsTwoCached
	if c := (Engine{Verified: f}).Caps(); !c.Complete || !c.Stems {
		t.Errorf("two_cached: %+v", c)
	}
}

func TestCaps_ExtendFollowsCanary(t *testing.T) {
	t.Parallel()
	if (Engine{Verified: EmbeddedFeatures()}).Caps().Extend {
		t.Error("the committed file must not enable extend before X4")
	}
}

func TestCaps_JSONStable(t *testing.T) {
	t.Parallel()
	a, _ := json.Marshal(fullCaps())
	b, _ := json.Marshal(fullCaps())
	if string(a) != string(b) {
		t.Error("caps JSON is not deterministic")
	}
}

func TestCaps_MaxSecondsPerPool(t *testing.T) {
	t.Parallel()
	f := EmbeddedFeatures()
	f.Pools = map[string]PoolFeatures{"AMPERE_24": {MaxS: 600, CanaryCommit: "c"}, "ADA_24": {MaxS: 480}, "AMPERE_48": {MaxS: 600, CanaryCommit: "c"}}
	for name, tc := range map[string]struct {
		pools []string
		want  int
	}{
		"default pools mixed": {nil, 480},
		"24 GB pools":         {[]string{"AMPERE_24", "ADA_24"}, 480},
		"one verified at 600": {[]string{"AMPERE_24"}, 600},
		"48 GB alone":         {[]string{"AMPERE_48"}, 600},
		"unlisted pool":       {[]string{"AMPERE_24", "OTHER_POOL"}, 480},
	} {
		if got := (Engine{Verified: f, Pools: tc.pools}).Caps().MaxSeconds; got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
}

func TestExpectedSeconds_AceStep(t *testing.T) {
	t.Parallel()
	f := EmbeddedFeatures()
	f.Tasks, f.CanaryCommit = []string{"text2music", "extend", "repaint"}, "c"
	e := Engine{Verified: f}
	f600 := f
	f600.Pools = map[string]PoolFeatures{"AMPERE_24": {MaxS: 600, CanaryCommit: "c"}, "ADA_24": {MaxS: 600, CanaryCommit: "c"}}
	e600 := Engine{Verified: f600}
	for _, tc := range []struct {
		name string
		e    Engine
		raw  []byte
		want int
		err  string
	}{
		{"1800 ds", e, req(nil), 180, ""},
		{"rounds up", e, req(obj{"duration_ds": 1801}), 181, ""},
		{"extend 2431+305", e, req(extendOver(305)), 274, ""},
		{"repaint is the source length", e, req(repaintOver(100, 400)), 100, ""},
		{"4800 under default cap", e, req(obj{"duration_ds": 4800}), 480, ""},
		{"6000 refused at 480", e, req(obj{"duration_ds": 6000}), 0, "ENGINE_CAPABILITY"},
		{"6000 at cap 600", e600, req(obj{"duration_ds": 6000}), 600, ""},
		{"extend overflow is an error not 600", e600, req(obj{"task": "extend", "duration_ds": nil, "thinking": false, "src": src(5990), "extend_ds": 100}), 0, "total_over_600"},
		{"invalid", e, []byte(`{}`), 0, "missing_id"},
	} {
		got, err := tc.e.ExpectedAudioSeconds(tc.raw)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: err %v, want %q", tc.name, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: %d, %v want %d", tc.name, got, err, tc.want)
		}
	}
}

func TestArtifacts_ACEStep(t *testing.T) {
	t.Parallel()
	req0, opt0 := Engine{}.Artifacts(req(nil))
	if !slices.Equal(req0, []string{"audio.flac", "request.json", "result.json", "mm-result.json"}) || len(opt0) != 0 {
		t.Errorf("%v %v", req0, opt0)
	}
	_, opt := Engine{}.Artifacts(req(obj{"lrc": true}))
	if !slices.Equal(opt, []string{"lyrics.lrc"}) {
		t.Errorf("lrc optional: %v", opt)
	}
	r, o := Engine{Observed: map[string][]string{"text2music": {"lyrics.lrc", "extra.json"}}}.Artifacts(req(obj{"lrc": true}))
	if !slices.Contains(r, "lyrics.lrc") || !slices.Contains(r, "extra.json") || len(o) != 0 {
		t.Errorf("observed: %v %v", r, o)
	}
	r, _ = Engine{}.Artifacts([]byte(`junk`))
	if len(r) != 4 {
		t.Errorf("unparsable request: %v", r)
	}
}

func TestEngine_Name(t *testing.T) {
	t.Parallel()
	if (Engine{}).Name() != "acestep15" {
		t.Error("name")
	}
}

func TestQuoteInputs(t *testing.T) {
	t.Parallel()
	r, _ := mustParse(t, req(obj{"lrc": true}))
	q := QuoteInputOf(r)
	if q.Class != ClassTurbo || q.AudioS != 180 || !q.LRC || q.ExtraLoadS(EmbeddedFeatures()) != 60 {
		t.Errorf("%+v", q)
	}
	b, _ := mustParse(t, req(completeOver("bass")))
	if q := QuoteInputOf(b); q.Class != ClassBase || q.AudioS != 100 || q.ExtraLoadS(EmbeddedFeatures()) != 0 {
		t.Errorf("%+v", q)
	}
	for task, want := range map[string]bool{TaskText2Music: true, TaskComplete: true, TaskExtract: true, TaskRepaint: false, TaskExtend: false} {
		if LearnsRTF(task) != want {
			t.Errorf("LearnsRTF(%s)", task)
		}
	}
}

func TestParamsVersion_TiedToCommit(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../worker/contract/ACESTEP_COMMIT")
	if err != nil {
		t.Skip("worker/contract/ACESTEP_COMMIT does not exist yet (lands with the worker lane PW2)")
	}
	if got := strings.TrimSpace(string(raw)); got != CurrentParams().ACEStepCommit {
		t.Errorf("engine_params.json acestep_commit %q != ACESTEP_COMMIT %q", CurrentParams().ACEStepCommit, got)
	}
}
