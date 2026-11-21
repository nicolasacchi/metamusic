package fake_test

import (
	"slices"
	"strconv"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
)

// TestFakeEarsStage: the render stage writes a parseable mm.ears-b/1 document from the request's lyrics, follows the stages of the
// spec, and the fault tokens give the failure modes the take never fails on (C339.7).
func TestFakeEarsStage(t *testing.T) {
	t.Parallel()
	kinds := map[string]fake.SimKindHook{fake.SimKindEars: fake.EarsRenderHook}
	n := 0
	run := func(style string, stages ...string) (fake.Result, *recUploader) {
		n++
		up := &recUploader{}
		ts := specV2("j-ears-"+strconv.Itoa(n), "", &provider.EarsSpec{Lang: "it", BudgetS: 60, Stages: stages}, nil)
		ts.Request = reqJSON(style, "off")
		return runV2(t, v2Worker(t, up, kinds), ts), up
	}

	res, up := run("pop", "separate", "asr")
	if res.Failed || res.Output.Ears == nil || res.Output.Ears.Status != "ok" || !slices.Contains(res.Output.Caps, "ears") {
		t.Fatalf("output = %+v", res.Output)
	}
	raw, err := tierb.Parse(up.body("analysis.json"))
	if err != nil {
		t.Fatalf("the document does not parse: %v", err)
	}
	if raw.Status != "ok" || raw.Pitch != nil || len(tierb.HeardWords(raw)) == 0 {
		t.Errorf("status %q pitch %v words %d", raw.Status, raw.Pitch != nil, len(tierb.HeardWords(raw)))
	}

	_, up = run("pop", "separate", "asr", "transcribe")
	if raw, err = tierb.Parse(up.body("analysis.json")); err != nil || raw.Pitch == nil {
		t.Errorf("transcribe: pitch missing (%v)", err)
	}

	for _, tc := range []struct{ token, what string }{{"fake:ears_error", "error"}, {"fake:ears_mishear", "ok"}} {
		res, up = run("pop, "+tc.token, "separate", "asr")
		if res.Failed {
			t.Fatalf("%s: the take failed: %+v", tc.token, res.Output.Error)
		}
		if raw, err = tierb.Parse(up.body("analysis.json")); err != nil || raw.Status != tc.what {
			t.Errorf("%s: status %q (%v), want %s", tc.token, raw.Status, err, tc.what)
		}
	}

	res, up = run("pop, fake:ears_skip", "separate", "asr")
	if res.Failed || res.Output.Ears.Status != "skipped" || res.Output.Ears.Code != "DEADLINE" || slices.Contains(up.names(), "analysis.json") {
		t.Errorf("skip: %+v uploads %v", res.Output.Ears, up.names())
	}

	res, up = run("pop, fake:ears_garbage", "separate", "asr")
	if res.Failed {
		t.Fatalf("garbage: the take failed: %+v", res.Output.Error)
	}
	if _, err = tierb.Parse(up.body("analysis.json")); err == nil {
		t.Error("garbage: the document parses")
	}
}
