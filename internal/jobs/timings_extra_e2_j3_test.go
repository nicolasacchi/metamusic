package jobs

import (
	"encoding/json"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// TestJobView_TimingsExtra: the worker's numeric P0b timing keys (peak_vram_mb_<component>, vram_resident_mb,
// lm_gpu_memory_utilization_milli, lm_s/dit_s/decode_s) reach the job view as timings.extra. Only those keys do: the
// fixed timing keys the runner already models, and anything else a worker might add, stay out.
func TestJobView_TimingsExtra(t *testing.T) {
	b := newACERig(t, true, aceEngines())
	job := b.seedACE(b.aceRecipe(aceRequest(t, nil)))
	b.runACE(job, b.flac(80), func(o *provider.Output) {
		for k, v := range map[string]string{"peak_vram_mb_dit": "18432", "peak_vram_mb_lm": "5200.5", "vram_resident_mb": "21000",
			"lm_gpu_memory_utilization_milli": "700", "lm_s": "4.2", "dit_s": "9.1", "decode_s": "1.3", "something_else": "9"} {
			n := json.Number(v)
			o.Timings[k] = &n
		}
		o.Timings["peak_vram_mb_vae"] = nil // a null is not a number
	})
	b.wantState(job, StateCompleted)
	j, err := b.service(t.Context()).Job(t.Context(), auth.Principal{}, job)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"peak_vram_mb_dit": 18432, "peak_vram_mb_lm": 5200.5, "vram_resident_mb": 21000,
		"lm_gpu_memory_utilization_milli": 700, "lm_s": 4.2, "dit_s": 9.1, "decode_s": 1.3}
	if len(j.Timings.Extra) != len(want) {
		t.Errorf("extra = %v, want %v", j.Timings.Extra, want)
	}
	for k, v := range want {
		if j.Timings.Extra[k] != v {
			t.Errorf("extra[%s] = %v, want %v", k, j.Timings.Extra[k], v)
		}
	}
}

// TestJobView_TimingsExtra_Yue2Absent: a yue2 job (and any job without such keys) carries no extra, so its view is unchanged.
func TestJobView_TimingsExtra_Yue2Absent(t *testing.T) {
	b := newBridgeRig(t, true)
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	b.run1(job, b.flac(1), [2]bool{}, nil)
	b.wantState(job, StateCompleted)
	j, err := b.service(t.Context()).Job(t.Context(), auth.Principal{}, job)
	if err != nil {
		t.Fatal(err)
	}
	if j.Timings.Extra != nil {
		t.Errorf("a yue2 job has extra = %v", j.Timings.Extra)
	}
}
