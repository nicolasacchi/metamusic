import { describe, expect, test } from "bun:test";
import { ATTENDED_NOTICE, OFF_NOTICE, formToPatch, nightStatus, percentToPpm, planSummary, ppmToPercent, reasonLabel, runRows, settingsToForm, type NightPlan, type NightRun, type NightSettings } from "./nightView";

const who = { kind: "human", id: "h", name: "alice" };
const settings: NightSettings = { enabled: false, at: "01:30", max_takes: 3, explore_ppm: 100_000, min_stars: 4, listening_checks: "on", halted: null, updated_by: who, updated_at: "x" };

describe("night status", () => {
  const auth = { mandate: null, reason: "no_mandate" } as never;
  test("off, halted, no mandate, on", () => {
    expect(nightStatus({ settings, authority: auth }).tone).toBe("off");
    expect(nightStatus({ settings: { ...settings, enabled: true, halted: { by: who, at: "x", reason: "too loud" } }, authority: auth })).toMatchObject({ tone: "halted" });
    expect(nightStatus({ settings: { ...settings, enabled: true }, authority: auth })).toMatchObject({ tone: "idle", detail: "There is no live mandate, so the night spends nothing." });
    expect(nightStatus({ settings: { ...settings, enabled: true }, authority: { mandate: null, reason: "mandate_scheduled" } as never }).detail).toContain("not started");
    const on = nightStatus({ settings: { ...settings, enabled: true }, authority: { mandate: { title: "Week", budget_micro_usd: 5_000_000 }, reason: null } as never });
    expect(on.tone).toBe("on");
    expect(on.detail).toContain("Week");
    expect(on.detail).toContain("$5.00");
  });
  test("the plain notices say what the operator decided", () => {
    expect(OFF_NOTICE).toContain("off until the Owner enables it");
    expect(OFF_NOTICE).toContain("live mandate");
    expect(OFF_NOTICE).toContain("$1.00 per night");
    expect(OFF_NOTICE).toContain("01:30");
    expect(ATTENDED_NOTICE).toContain("first night attended");
    expect(ATTENDED_NOTICE).toContain("during the day");
  });
});

describe("runs and plan", () => {
  const run = (night: string, o: Partial<NightRun> = {}): NightRun => ({ night, state: "done", reason: null, batch_id: "b", takes: 3, ceiling_micro_usd: 500_000, ceiling_usd: "0.5", settled_micro_usd: 400_000, settled_usd: "0.4", morning_crate_id: null, ...o });
  test("newest first, at most 14, reasons in words", () => {
    const runs = Array.from({ length: 20 }, (_, i) => run(`2026-11-${String(i + 1).padStart(2, "0")}`));
    const rows = runRows(runs);
    expect(rows).toHaveLength(14);
    expect(rows[0]?.night).toBe("2026-11-20");
    const skipped = runRows([run("2026-11-01", { state: "skipped", reason: "over_ceiling", takes: 0 })])[0];
    expect(skipped).toMatchObject({ stateText: "Skipped", detail: "because nothing fit under the ceiling" });
    expect(reasonLabel(null)).toBe("");
    expect(reasonLabel("some_new_reason")).toBe("some new reason");
  });
  test("plan summary", () => {
    const plan = { night: "n", seed: "s", items: [{ kind: "exploit" }, { kind: "explore" }, { kind: "exploit" }], dropped: [{}], trimmed: [], quote: { expected_micro_usd: 300_000, max_micro_usd: 400_000, ceiling_micro_usd: 500_000 }, mandate: null, would_skip: null } as unknown as NightPlan;
    expect(planSummary(plan)).toMatchObject({ exploit: 2, explore: 1, dropped: 1 });
    expect(planSummary(plan).line).toBe("3 takes planned (2 exploit, 1 explore); expected $0.30, at most $0.40, ceiling $0.50.");
    expect(planSummary({ ...plan, items: [], quote: null, would_skip: "no_mandate" }).line).toBe("Tonight would be skipped: no live mandate.");
  });
});

describe("settings form", () => {
  test("percent <-> ppm is exact", () => {
    expect(ppmToPercent(100_000)).toBe("10");
    expect(ppmToPercent(125_000)).toBe("12.5");
    expect(ppmToPercent(0)).toBe("0");
    expect(percentToPpm("12.5")).toBe(125_000);
    expect(percentToPpm("50")).toBe(500_000);
    expect(percentToPpm("50.0001")).toBeNull();
    expect(percentToPpm("-1")).toBeNull();
    expect(percentToPpm("1e1")).toBeNull();
  });
  test("switching off ignores a bad field in the form", () => {
    const f = settingsToForm(settings);
    f.at = "25:99";
    f.maxTakes = "zz";
    expect(formToPatch(f, settings, false)).toEqual({ ok: true, body: { enabled: false } });
    expect(formToPatch(f, settings, true).ok).toBe(false);
  });
  test("only changed fields are sent; enabled rides along", () => {
    const f = settingsToForm(settings);
    expect(formToPatch(f, settings)).toEqual({ ok: false, error: "Nothing changed." });
    expect(formToPatch(f, settings, true)).toEqual({ ok: true, body: { enabled: true } });
    f.at = "02:15";
    f.explorePct = "20";
    f.listeningChecks = "off";
    expect(formToPatch(f, settings, false)).toEqual({ ok: true, body: { at: "02:15", explore_ppm: 200_000, listening_checks: "off", enabled: false } });
  });
  test.each([["at", "25:00"], ["maxTakes", "13"], ["maxTakes", "0"], ["explorePct", "60"], ["minStars", "0"]] as const)("refuses %s=%s", (k, v) => {
    const f = { ...settingsToForm(settings), [k]: v };
    expect(formToPatch(f, settings).ok).toBe(false);
  });
});
