package fake_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

func planKindWorker(t testing.TB, up fake.Uploader) *fake.SimWorker {
	t.Helper()
	return v2Worker(t, up, map[string]fake.SimKindHook{fake.SimKindPlan: fake.PlanHook})
}

// untar reads a plan bundle into its members.
func untar(t testing.TB, raw []byte) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil || h.Typeflag != tar.TypeReg {
			t.Fatalf("tar: %v %v", err, h)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = b
	}
}

func planSpecFor(job, style, cot string, seeds ...int64) provider.TakeSpec {
	ts := specV2(job, provider.KindPlan, nil, &provider.PlanSpec{Seeds: seeds, SecondsPerSeed: 10})
	ts.Request = reqJSON(style, cot)
	return ts
}

// TestSimWorker_PlanKind (C361.5): per seed a deterministic two-voice yue2 score from the lyric sections and the seed, bundled with
// plan.json; the faults fail, truncate or fail every seed.
func TestSimWorker_PlanKind(t *testing.T) {
	t.Parallel()
	t.Run("bundles", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		ts := planSpecFor("j-pk1", "pop", "full", 11, 12)
		res := runV2(t, planKindWorker(t, up), ts)
		if res.Failed || len(res.Output.Plans) != 2 || !slices.Equal(up.names(), []string{"plan-00.tar", "plan-01.tar", "mm-result.json"}) {
			t.Fatalf("output %+v uploads %v", res.Output, up.names())
		}
		if err := provider.CheckOutputV2(provider.ShapeOf(ts), res.Output); err != nil {
			t.Error(err)
		}
		var scores [2]string
		for i, seed := range []int64{11, 12} {
			po := res.Output.Plans[i]
			if po.Index != i || po.Seed != seed || po.Status != "ok" || po.Bundle != provider.PlanBundleName(i) || po.Truncated != nil {
				t.Errorf("plan %d = %+v", i, po)
			}
			files := untar(t, up.body(po.Bundle))
			if len(files) != 2 {
				t.Fatalf("members %v", files)
			}
			scores[i] = string(files["score.abc"])
			tune, err := abc.Parse(files["score.abc"], abc.Options{Dialect: abc.DialectYuE2})
			if err != nil || len(tune.Voices) != 2 || len(tune.Voices[0].Bars) != 4 || len(tune.Sections) != 2 {
				t.Errorf("score %d does not read as two voices of two sections: %v\n%s", i, err, files["score.abc"])
			}
			if !strings.Contains(scores[i], `"`) {
				t.Errorf("cot full carries chord symbols: %s", scores[i])
			}
			var meta struct {
				Seed      int64
				Cot       string
				Truncated bool
				ABCTokens int `json:"abc_tokens"`
				Seconds   float64
				YuE2      string
			}
			if err := json.Unmarshal(files["plan.json"], &meta); err != nil || meta.Seed != seed || meta.Cot != "full" || meta.Truncated || meta.YuE2 == "" || meta.ABCTokens == 0 {
				t.Errorf("plan.json %s (%v)", files["plan.json"], err)
			}
		}
		if scores[0] == scores[1] {
			t.Error("two seeds planned the same score")
		}
		// deterministic: the same seed, the same bytes
		up2 := &recUploader{}
		runV2(t, planKindWorker(t, up2), planSpecFor("j-pk1b", "pop", "full", 11, 12))
		if !bytes.Equal(up.body("plan-00.tar"), up2.body("plan-00.tar")) {
			t.Error("the same seed produced other bytes")
		}
	})
	t.Run("melody has no chords", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		res := runV2(t, planKindWorker(t, up), planSpecFor("j-pk2", "pop", "melody", 5))
		if res.Failed {
			t.Fatalf("%+v", res.Output)
		}
		if s := string(untar(t, up.body("plan-00.tar"))["score.abc"]); strings.Contains(s, `"`) {
			t.Errorf("cot melody carries chord symbols: %s", s)
		}
	})
	t.Run("one seed fails", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		res := runV2(t, planKindWorker(t, up), planSpecFor("j-pk3", "pop, fake:plan_fail_seed=0", "full", 11, 12))
		if res.Failed || res.Output.Plans[0].Status != "error" || res.Output.Plans[0].Code != "PLAN_FAILED" || res.Output.Plans[1].Status != "ok" ||
			!slices.Equal(up.names(), []string{"plan-01.tar", "mm-result.json"}) {
			t.Errorf("output %+v uploads %v", res.Output.Plans, up.names())
		}
	})
	t.Run("truncated", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		res := runV2(t, planKindWorker(t, up), planSpecFor("j-pk4", "pop fake:plan_truncated", "full", 11))
		var meta struct{ Truncated bool }
		_ = json.Unmarshal(untar(t, up.body("plan-00.tar"))["plan.json"], &meta)
		if res.Failed || !res.Output.Plans[0].Truncated["abc"] || !meta.Truncated {
			t.Errorf("output %+v meta %+v", res.Output.Plans, meta)
		}
	})
	t.Run("every seed fails", func(t *testing.T) {
		t.Parallel()
		res := runV2(t, planKindWorker(t, &recUploader{}), planSpecFor("j-pk5", "pop, fake:plans_fail", "full", 11, 12))
		if !res.Failed || res.Output.Error.Code != "PLANS_FAILED" {
			t.Errorf("%+v", res.Output.Error)
		}
	})
	t.Run("no lyric sections", func(t *testing.T) {
		t.Parallel()
		out, err := fake.PlanHook(context.Background(), nil, provider.WorkerInput{Request: json.RawMessage(`{"style":"s","lyrics":"la la","cot":"full"}`),
			Plan: &provider.PlanIn{Seeds: []int64{3}, SecondsPerSeed: 5}})
		if err != nil || len(out.Artifacts) != 1 {
			t.Fatalf("%v %v", out, err)
		}
		if _, err := abc.Parse(untar(t, out.Artifacts[0].Body)["score.abc"], abc.Options{Dialect: abc.DialectYuE2}); err != nil {
			t.Error(err)
		}
	})
	t.Run("bad input", func(t *testing.T) {
		t.Parallel()
		if _, err := fake.PlanHook(context.Background(), nil, provider.WorkerInput{}); err == nil {
			t.Error("a document without a request or plan block was served")
		}
	})
}
