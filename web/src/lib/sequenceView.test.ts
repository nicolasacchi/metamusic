import { describe, expect, test } from "bun:test";
import { recordView, slot } from "./fixtures/l2/record";
import {
  coverageText, costBar, curveModel, endpointOptions, endpointsConflict, isNoChange, pinnedRows, runText, sequenceEdit, sequenceErrorText, sequenceOp,
  sequenceSpec, stepRows, strategyFromSearch, strategyLabel, warningText, type Step, type Suggestion,
} from "./sequenceView";

const step = (n: number, over: Partial<Step> = {}): Step => ({
  position: n, song_id: `s-${n}`, title: `Synthetic song ${n}`, text: `Step ${n} sentence`, energy_rel: n * 2000, target: -1, missing: [], reasons: [], ...over,
});
const suggestion = (over: Partial<Suggestion> = {}): Suggestion => ({
  cost: { input: "1.20", output: "0.74", improvement_pct: "38.6" }, coverage: { tempo: 6, key: 4, energy: 6 }, def: "d", digest: "x", exact: true,
  input_order: ["s-1", "s-2", "s-3"], order: ["s-3", "s-1", "s-2"], pending: false, steps: [step(1), step(2), step(3)], strategy: "arc", warnings: [], ...over,
});

describe("strategy and URL state", () => {
  test("?sequence=<strategy> opens the tab with that strategy; bad values fall back to arc; absent stays closed", () => {
    expect(strategyFromSearch("?sequence=arc")).toBe("arc");
    expect(strategyFromSearch("?sequence=ramp_up")).toBe("ramp_up");
    expect(strategyFromSearch("?sequence=bogus")).toBe("arc");
    expect(strategyFromSearch("?sequence")).toBe("arc");
    expect(strategyFromSearch("?add=a,b")).toBeNull();
    expect(strategyFromSearch("")).toBeNull();
  });
  test("labels", () => {
    expect(strategyLabel("ramp_up")).toBe("Ramp up");
    expect(strategyLabel("future_one")).toBe("future one");
  });
});

describe("endpoint options and the op builder", () => {
  test("endpoint options come only from the Record's Songs, once each", () => {
    const v = recordView(3);
    v.slots.push(slot(4, { song: { ...v.slots[0]!.song } }));
    expect(endpointOptions(v)).toEqual([1, 2, 3].map((n) => ({ id: `s-${n}`, title: `Synthetic song ${n}` })));
  });
  test("the sequence op carries strategy and endpoints only when set; the edit carries the revision", () => {
    expect(sequenceOp("arc")).toEqual({ op: "sequence", strategy: "arc" });
    expect(sequenceOp("steady", "s-1", "")).toEqual({ op: "sequence", strategy: "steady", first_song_id: "s-1" });
    expect(sequenceEdit(7, "ramp_up", "s-1", "s-3")).toEqual({ revision: 7, ops: [{ op: "sequence", strategy: "ramp_up", first_song_id: "s-1", last_song_id: "s-3" }] });
    expect(sequenceSpec("arc", null, "s-2")).toEqual({ strategy: "arc", last_song_id: "s-2" });
  });
  test("equal endpoints conflict", () => {
    expect(endpointsConflict("a", "a")).toBe(true);
    expect(endpointsConflict("a", "b")).toBe(false);
    expect(endpointsConflict("", "")).toBe(false);
  });
});

describe("preview model", () => {
  test("rows, coverage chip, cost bar", () => {
    const s = suggestion({ steps: [step(1), step(2, { missing: ["key", "bpm"] }), step(3, { missing: ["energy"] })], warnings: [{ code: "predicted_energy", n: 2 }] });
    const rows = stepRows(s.steps);
    expect(rows.map((r) => r.text)).toEqual(["Step 1 sentence", "Step 2 sentence", "Step 3 sentence"]);
    expect(rows[1]!.missingText).toBe("no key, tempo");
    expect(rows[2]!.energyPct).toBeNull();
    expect(rows[0]!.energyPct).toBe(20);
    expect(coverageText({ tempo: 6, key: 4, energy: 6 }, 6, s.warnings)).toBe("tempo 6/6 · key 4/6 · energy 6/6 (2 predicted)");
    expect(coverageText({ tempo: 3, key: 3, energy: 3 }, 3)).toBe("tempo 3/3 · key 3/3 · energy 3/3");
    const c = costBar(s.cost);
    expect(c.pct).toBe(38.6);
    expect(c.text).toBe("Transition cost 1.20 → 0.74: 38.6% smoother");
    expect(costBar({ input: "1", output: "1", improvement_pct: null }).pct).toBeNull();
    expect(costBar({ input: "1", output: "2", improvement_pct: "-5.0" }).pct).toBe(0);
    expect(costBar({ input: "1", output: "2", improvement_pct: "-5.0" }).text).toContain("no better");
  });
  test("no-change detection", () => {
    expect(isNoChange(suggestion())).toBe(false);
    expect(isNoChange(suggestion({ order: ["s-1", "s-2", "s-3"] }))).toBe(true);
  });
  test("warnings in words", () => {
    expect(warningText({ code: "low_coverage", feature: "key", known: 1, total: 6 })).toBe("Only 1 of 6 songs have a key, so that part of the order is a weak guide.");
    expect(warningText({ code: "predicted_energy", n: 1 })).toContain("1 song's energy is predicted");
    expect(warningText({ code: "no_features", song: "s-1" }, new Map([["s-1", "Synthetic song 1"]]))).toContain("Synthetic song 1 has no tempo");
    expect(warningText({ code: "pending_features" })).toContain("analysed");
    expect(warningText({ code: "heuristic" })).toContain("not proven");
    expect(warningText({ code: "mystery_code" })).toBe("mystery code");
  });
  test("error codes in words", () => {
    for (const c of ["SEQUENCE_TOO_SMALL", "SEQUENCE_ENDPOINTS_CONFLICT", "SEQUENCE_SOURCE", "UNKNOWN_SONG"]) expect(sequenceErrorText(c)).not.toBeNull();
    expect(sequenceErrorText("NOPE")).toBeNull();
  });
  test("energy curve breaks at unknown energy and draws the target when the strategy has one", () => {
    const c = curveModel([step(1, { target: 2000 }), step(2, { target: 5000, missing: ["energy"] }), step(3, { target: 9000 })]);
    expect(c.line.match(/M/g)?.length).toBe(2);
    expect(c.target.startsWith("M")).toBe(true);
    expect(c.dots.map((d) => d.known)).toEqual([true, false, true]);
    expect(curveModel([step(1), step(2)]).target).toBe("");
  });
  test("run text names who and when", () => {
    expect(runText({ strategy: "ramp_up", at: "2026-10-30T10:00:00Z", actor: { id: "agent:a", kind: "agent", name: "scout" } })).toBe("Ramp up order applied by scout · 2026-10-30 10:00:00 UTC");
  });
});

describe("pinned slots", () => {
  test("listed with their Take and side", () => {
    const v = recordView(3);
    v.slots[1] = slot(2, { mode: "pinned", side: "B" });
    expect(pinnedRows(v)).toEqual([{ slotId: "sl-2", position: 2, title: "Synthetic song 2", takeTitle: "Synthetic take 2", side: "B" }]);
  });
});
