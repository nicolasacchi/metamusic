package upload

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestOpenAttempt_PerEngineAllowlist: the artifact allowlist an attempt's upload rows are held to is its engine's (C153.2).
func TestOpenAttempt_PerEngineAllowlist(t *testing.T) {
	t.Parallel()
	const mib = 1 << 20
	for _, c := range []struct {
		name   string
		engine string
		art    map[string]int64
		ok     bool
	}{
		{"acestep full set at its limits", provider.EngineACEStep, map[string]int64{"audio.flac": 192 * mib, "request.json": 4 * mib, "result.json": 4 * mib, "lyrics.lrc": mib, "mm-result.json": 64 << 10}, true},
		{"acestep audio above 192 MiB", provider.EngineACEStep, map[string]int64{"audio.flac": 192*mib + 1}, false},
		{"acestep has no config.json", provider.EngineACEStep, map[string]int64{"config.json": 10}, false},
		{"acestep has no score.abc", provider.EngineACEStep, map[string]int64{"score.abc": 10}, false},
		{"acestep has no npy", provider.EngineACEStep, map[string]int64{"prefix.npy": 10}, false},
		{"yue2 audio at 160 MiB", provider.EngineYuE2, map[string]int64{"audio.flac": 160 * mib}, true},
		{"yue2 audio above 160 MiB", provider.EngineYuE2, map[string]int64{"audio.flac": 160*mib + 1}, false},
		{"yue2 has no lyrics.lrc", provider.EngineYuE2, map[string]int64{"lyrics.lrc": 10}, false},
		{"empty engine is yue2", "", map[string]int64{"config.json": 10}, true},
		{"empty engine has no lyrics.lrc", "", map[string]int64{"lyrics.lrc": 10}, false},
		{"unknown engine has no allowlist", "nope", map[string]int64{"audio.flac": 10}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			// the rig's own attempt already holds uploads: the case opens a second one
			ts := store.FormatTime(t0)
			r.exec(`INSERT INTO job_attempts(id,job_id,n,backend,state,submitted_at,window_ends_at,price_pph_micro_usd,created_at)
				VALUES('att-2',?,2,'fake','pending',?,?,690000,?)`, jobID, ts, store.FormatTime(time.Unix(r.exp, 0)), ts)
			tx, err := r.st.W.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = r.svc.OpenAttempt(t.Context(), tx, JobRef{JobID: jobID, BatchID: batchID, Engine: c.engine}, "att-2", c.art, r.exp, store.LeaseFence{Owner: "o1", Now: t0})
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
		})
	}
}
