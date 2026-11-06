package jobs

import (
	"strings"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// workerTimingKeys are the exact worker timing keys the job view carries as timings.extra, besides the keys that start with
// workerTimingPrefix (E2 P0b). The fixed keys the runner already models (generate_s, save_s, load_s, e2e_seconds, ...) are not
// among them.
var workerTimingKeys = map[string]bool{
	"vram_resident_mb": true, "lm_gpu_memory_utilization_milli": true, "lm_s": true, "dit_s": true, "decode_s": true,
}

const workerTimingPrefix = "peak_vram_mb_"

// workerTimings picks the numeric P0b timing keys out of a worker output; nil when it reports none.
func workerTimings(out provider.Output) map[string]float64 {
	var m map[string]float64
	for k, v := range out.Timings {
		if !workerTimingKeys[k] && !strings.HasPrefix(k, workerTimingPrefix) {
			continue
		}
		if f, ok := numberOf(v); ok {
			if m == nil {
				m = map[string]float64{}
			}
			m[k] = f
		}
	}
	return m
}
