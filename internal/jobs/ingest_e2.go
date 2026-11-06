package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The ingest half of the non-yue2 engines (C156, C164). The only engine-specific code outside internal/engine lives here and
// in ingest.go, as the plan allows for the two ingest bridges.

// reportsTruncation is the IngestPolicy answer for the engine called name in reg: whether its worker output carries
// truncated{abc,semantic}. yue2 reports it with or without a registry entry; an engine without a policy never does.
func reportsTruncation(reg engine.Registry, name string) bool {
	if e, ok := reg[name]; ok {
		return name == provider.EngineYuE2 || engine.ReportsTruncation(e)
	}
	return name == provider.EngineYuE2
}

// declaredBPM is the tempo a non-yue2 request declares (C164), 0 when it names none. It becomes tracks.bpm_declared.
func declaredBPM(request string) int {
	var r struct {
		BPM *int `json:"bpm"`
	}
	if json.Unmarshal([]byte(request), &r) != nil || r.BPM == nil || *r.BPM <= 0 {
		return 0
	}
	return *r.BPM
}

// recordEngineRTF teaches engine_rtf from a finished take, inside the take transaction (C156, C152). It implements the
// learning rule: a take updates its class RTF only when it is a full-length render of an enabled task of that class (turbo:
// text2music; base: complete and extract, as far as the engine's effective caps enable them). repaint and extend read the
// turbo RTF and never update it. The duration is net of load: generate_s + save_s - load_s, or e2e_seconds when the worker
// reported no split. A take that cannot teach (no timing, no GPU name, unparsable request) teaches nothing, never fails.
func (b *IngestBridge) recordEngineRTF(ctx context.Context, tx *sql.Tx, job JobRow, eng ingestEngine, req recipeInfo, out provider.Output) error {
	if eng.eng == nil || out.AudioSeconds == nil {
		return nil
	}
	r, _, issues := acestep.Parse([]byte(req.requestJSON))
	for _, i := range issues {
		if i.Severity == "error" {
			return nil
		}
	}
	caps := eng.eng.Caps()
	switch {
	case r.Class() == acestep.ClassTurbo && r.Task == acestep.TaskText2Music:
	case r.Task == acestep.TaskComplete && caps.Complete:
	case r.Task == acestep.TaskExtract && caps.Stems:
	default:
		return nil
	}
	audio, ok := numberOf(out.AudioSeconds)
	if !ok || audio <= 0 {
		return nil
	}
	gen, ok := numberOf(out.Timings["generate_s"])
	save, _ := numberOf(out.Timings["save_s"])
	if !ok {
		if gen, ok = numberOf(out.Timings["e2e_seconds"]); !ok {
			return nil
		}
		save = 0
	}
	load, _ := numberOf(out.Timings["load_s"])
	net := gen + save - load
	if net <= 0 {
		return nil
	}
	return b.o.Spend.RecordEngineRTF(ctx, tx, eng.name, r.Class(), out.Env.GPUName, "job:"+job.ID, int64(math.Round(net*1000)), int64(math.Round(audio*1000)))
}

// durationTolerance is how far the rendered length may stray from the planned one before the take is flagged (C156):
// text2music 1.5 s, extend 0.5 s, the source-length tasks 0.1 s.
func durationTolerance(task string) float64 {
	switch task {
	case acestep.TaskExtend:
		return 0.5
	case acestep.TaskRepaint, acestep.TaskComplete, acestep.TaskExtract:
		return 0.1
	}
	return 1.5
}

// durationEvent writes the duration_mismatch job event when the audio length differs from the request's planned length.
// It is information only: it never fails a take (TestIngest_DurationMismatchIsEventOnly).
func durationEvent(ctx context.Context, tx *sql.Tx, f store.LeaseFence, job JobRow, att AttemptRow, req recipeInfo, gotS float64) error {
	r, _, issues := acestep.Parse([]byte(req.requestJSON))
	for _, i := range issues {
		if i.Severity == "error" {
			return nil
		}
	}
	ds := r.DurationDS
	if r.Src != nil {
		ds = r.Src.DurationDS
		if r.Task == acestep.TaskExtend && r.ExtendDS != nil {
			ds += *r.ExtendDS
		}
	}
	want := float64(ds) / 10
	if want <= 0 || math.Abs(gotS-want) <= durationTolerance(r.Task) {
		return nil
	}
	if err := insertEvent(ctx, tx, f, Event{JobID: job.ID, AttemptID: att.ID, Name: EvDurationMismatch,
		Detail: map[string]any{"task": r.Task, "expected_s": want, "audio_s": math.Round(gotS*1000) / 1000}}); err != nil {
		return fmt.Errorf("jobs: duration event: %w", err)
	}
	return nil
}
