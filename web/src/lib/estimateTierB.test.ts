import { describe, expect, test } from "bun:test";
import type { components } from "../api/schema";
import { checkCostLine, estimateFailureText, hasListeningCheckChoice, isAnalyzeEstimate, takeBadge, tierBWarningText, tierBSummary } from "./estimateTierB";

type Estimate = components["schemas"]["Estimate"];
type TakePlan = components["schemas"]["TakePlan"];

const take = (o: Partial<TakePlan> = {}): TakePlan =>
  ({ take_index: 0, request_id: "r", seed: 1, title: "Alice", operator: "new_seed", expected_audio_s: 120, timeout_s: 300, max_micro_usd: 1000, max_usd: "0.001", engine: "yue2", cross_engine: false, ...o }) as TakePlan;

const est = (takes: TakePlan[], o: Partial<Estimate> = {}, mode = "new_seed"): Estimate =>
  ({
    items: [{ index: 0, mode, takes }],
    takes: takes.length,
    warnings: [],
    engine: "yue2",
    cost: { expected_micro_usd: 100_000, max_micro_usd: 200_000, retry_allowance_micro_usd: 0, ceiling_micro_usd: 300_000 },
    ...o,
  }) as unknown as Estimate;

describe("tierBWarningText", () => {
  test("every tier_b code reads as plain words", () => {
    expect(tierBWarningText("tier_b_unavailable")).toBe("No listening check: the worker image is not open for it yet.");
    expect(tierBWarningText("tier_b_no_lyrics")).toContain("no line to listen for");
    expect(tierBWarningText("tier_b_exceeds_runtime")).toContain("runtime cap");
    expect(tierBWarningText("tier_b_engine")).toContain("yue2");
  });
  test("other codes have no text here", () => {
    expect(tierBWarningText("no_lyrics")).toBe("");
  });
});

describe("takeBadge", () => {
  test("a take with the check is on", () => {
    expect(takeBadge(take({ tier_b: true }), "new_seed", [])).toEqual({ on: true, label: "listening check", reason: "" });
  });
  test("an analyze take is the check itself", () => {
    expect(takeBadge(take({ operator: "analyze" }), "analyze", []).on).toBe(true);
  });
  test("a take without it names the reason from its own warning only", () => {
    const w = [
      { code: "tier_b_unavailable", field: "options.tier_b", message: "m", severity: "warning" as const, take_index: 0 },
      { code: "tier_b_no_lyrics", field: "options.tier_b", message: "m", severity: "warning" as const, take_index: 1 },
    ];
    expect(takeBadge(take({ take_index: 1 }), "new_seed", w)).toEqual({ on: false, label: "no listening check", reason: tierBWarningText("tier_b_no_lyrics") });
    expect(takeBadge(take({ take_index: 2 }), "new_seed", w).reason).toBe("");
  });
});

describe("tierBSummary", () => {
  test("counts the checked takes", () => {
    const e = est([take({ tier_b: true }), take({ take_index: 1 })]);
    expect(tierBSummary(e)).toEqual({ analyze: false, checked: 1, total: 2 });
  });
  test("an analyze estimate is all check", () => {
    const e = est([take({ operator: "analyze" })], {}, "analyze");
    expect(isAnalyzeEstimate(e)).toBe(true);
    expect(tierBSummary(e)).toEqual({ analyze: true, checked: 1, total: 1 });
  });
});

describe("hasListeningCheckChoice", () => {
  test("offered for yue2 renders, not for analyze or other engines", () => {
    expect(hasListeningCheckChoice(est([take({ tier_b: true })]))).toBe(true);
    expect(hasListeningCheckChoice(est([take()]))).toBe(true);
    expect(hasListeningCheckChoice(est([take({ operator: "analyze" })], {}, "analyze"))).toBe(false);
    expect(hasListeningCheckChoice(est([take()], { engine: "acestep15" }))).toBe(false);
  });
});

describe("checkCostLine", () => {
  const withCheck = est([take({ tier_b: true })]);
  const without = est([take()], { cost: { expected_micro_usd: 70_000, max_micro_usd: 150_000, retry_allowance_micro_usd: 0, ceiling_micro_usd: 250_000 } as Estimate["cost"] });
  test("is the ceiling and expected difference of the two estimates", () => {
    expect(checkCostLine(withCheck, without)).toBe("The listening check adds up to $0.05 to the ceiling (about $0.03 expected).");
  });
  test("an analyze estimate is the whole job", () => {
    expect(checkCostLine(est([take({ operator: "analyze" })], {}, "analyze"), null)).toBe("The listening check is the whole job: up to $0.30.");
  });
  test("nothing without a comparison", () => {
    expect(checkCostLine(withCheck, null)).toBe("");
  });
  test("nothing when no take has the check", () => {
    expect(checkCostLine(without, without)).toBe("");
  });
});

describe("estimateFailureText", () => {
  test("a closed worker image says so in words", () => {
    const t = estimateFailureText(503, "GENERATION_DISABLED", "m", { reason: "worker_outdated", missing: ["ears", "analyze"] });
    expect(t).toContain("worker image is not open");
    expect(t).toContain("ears, analyze");
  });
  test("analysis already running, already done and too long", () => {
    expect(estimateFailureText(409, "ANALYSIS_IN_PROGRESS", "m", {})).toContain("already being checked");
    expect(estimateFailureText(422, "INVALID_ITEM", "m", { rule: "already_analyzed" })).toContain("already has a listening check");
    expect(estimateFailureText(422, "INVALID_ITEM", "m", { rule: "audio_too_long" })).toContain("too long");
  });
  test("anything else keeps code and message", () => {
    expect(estimateFailureText(500, "X", "boom", {})).toBe("X: boom");
    expect(estimateFailureText(500, "", "", {})).toBe("Could not estimate.");
  });
});
