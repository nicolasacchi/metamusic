import { describe, expect, test } from "bun:test";
import { adoptionText, backendText, engineMatrix } from "./engineMatrix";
import type { EngineList, EngineView } from "./engines";

const caps = {
  complete: false, extend: false, extend_max_s: 0, extend_min_s: 0, key: false, lora: false, lrc: false, max_seconds: 300, min_seconds: 30,
  plan_score: false, reference_audio: false, reference_max_s: 0, render_from_score: false, repaint_interval: false, repaint_max_s: 0, repaint_min_s: 0,
  sample_rate: 48000, stem_names: [], stems: false, tasks: [], tempo: false, text2song: true,
};
function eng(name: string, over: Partial<EngineView["caps"]> = {}, extra: Partial<EngineView> = {}): EngineView {
  return {
    name, label: name, enabled: true, selectable: true, unavailable_reason: null, adopt_hint: null, open_trial_id: null, task_classes: [], warnings: [],
    adoption: { state: "adopted", via: "seed", changed_at: null, changed_by: null, reason: null, trial_id: null },
    caps: { ...caps, ...over }, ...extra,
  } as EngineView;
}
const list = (...e: EngineView[]) => ({ default_engine: "yue2", engines: e }) as EngineList;

describe("engineMatrix", () => {
  test("rows in order, one cell per engine", () => {
    const m = engineMatrix(list(eng("yue2"), eng("acestep15", { repaint_interval: true, repaint_min_s: 3, repaint_max_s: 60, reference_audio: true, reference_max_s: 30 })));
    expect(m.map((r) => r.label)).toEqual(["Text2Song", "Tempo", "Key", "Reference", "Repaint", "Extend", "Complete", "Stems", "LRC", "Max duration"]);
    const rep = m.find((r) => r.key === "repaint")!;
    expect(rep.cells.map((c) => c.kind)).toEqual(["no", "yes"]);
    expect(rep.cells[1]?.text).toBe("3–60 s");
    expect(m.find((r) => r.key === "reference")!.cells[1]?.text).toBe("up to 30 s");
  });
  test("unverified features are labelled", () => {
    const e = eng("acestep15", { extend: true }, { features: { unverified: ["extend"], tasks: [], pools: [], models_mode: null, reference_reproducible: false, seed_reproducible: false, verified_at: "" } });
    expect(engineMatrix(list(e)).find((r) => r.key === "extend")!.cells[0])?.toEqual({ kind: "unverified", text: "not yet verified" });
  });
  test("an unverified feature is labelled even when its cap is off (the server's shape)", () => {
    const f = { unverified: ["repaint", "reference_audio", "extract", "lrc"], tasks: ["text2music"], pools: [], models_mode: null, reference_reproducible: false, seed_reproducible: false, verified_at: "1970-01-01" };
    const m = engineMatrix(list(eng("acestep15", {}, { features: f })));
    for (const k of ["repaint", "reference", "stems", "lrc"]) expect(m.find((r) => r.key === k)!.cells[0]).toEqual({ kind: "unverified", text: "not yet verified" });
    expect(m.find((r) => r.key === "tempo")!.cells[0]?.kind).not.toBe("unverified");
  });
  test("max duration unknown is unverified", () => {
    expect(engineMatrix(list(eng("x", { max_seconds: 0 }))).at(-1)!.cells[0]?.kind).toBe("unverified");
  });
  test("adoption and backend text", () => {
    expect(adoptionText(eng("a"))).toBe("Adopted (seed)");
    expect(backendText(eng("a"))).toBe("No backend");
    const b = { name: "b", enabled: true, armed: true, tripped: true, trip_reason: "init_fail", disabled_reason: null, cold_start_s: 40, max_runtime_s: 600 };
    expect(backendText(eng("a", {}, { backend: b }))).toBe("Tripped (init_fail)");
    expect(backendText(eng("a", {}, { backend: { ...b, tripped: false } }))).toBe("Armed, cold start 40 s");
  });
});
