import { describe, expect, test } from "bun:test";
import type { EngineList } from "./engines";
import { actionLimits, actionReason, extendLimits, extendProblem, isRepaintChild } from "./trackEngineActions";

const ace = (over: Record<string, unknown> = {}, caps: Record<string, unknown> = {}) =>
  ({
    default_engine: "yue2",
    engines: [{ name: "acestep15", label: "ACE-Step", selectable: true, unavailable_reason: null, caps: { repaint_interval: true, reference_audio: true, repaint_min_s: 3, repaint_max_s: 60, reference_max_s: 30, ...caps }, ...over }],
  }) as unknown as EngineList;

describe("trackEngineActions", () => {
  test("available", () => {
    expect(actionReason(ace(), "repaint")).toBe("");
    expect(actionLimits(ace(), "repaint")).toEqual({ min: 3, max: 60 });
    expect(actionLimits(ace(), "reference")).toEqual({ min: 0, max: 30 });
  });
  test("reasons", () => {
    expect(actionReason({ default_engine: "yue2", engines: [] }, "repaint")).toContain("not available");
    expect(actionReason(ace({ selectable: false, unavailable_reason: "not_adopted" }), "repaint")).toContain("Not adopted");
    expect(actionReason(ace({}, { repaint_interval: false }), "repaint")).toContain("repaint");
    expect(actionReason(ace({}, { reference_audio: false }), "reference")).toContain("reference");
  });
  test("no list means no limits; repaint child", () => {
    expect(actionLimits(null, "repaint")).toEqual({ min: 0, max: 0 });
    expect(isRepaintChild(null, "repaint")).toBe(true);
    expect(isRepaintChild("repaint", undefined)).toBe(true);
    expect(isRepaintChild(null, "extend")).toBe(false);
  });
  test("E2b actions need the task in caps", () => {
    const l = ace({}, { tasks: ["extend", "stems"], extend_min_s: 3, extend_max_s: 90, max_seconds: 240 });
    expect(actionReason(l, "extend")).toBe("");
    expect(actionReason(l, "stems")).toBe("");
    expect(actionReason(l, "complete")).toContain("complete");
    expect(actionReason(ace(), "extend")).toContain("extend");
  });
  test("extend limits respect the effective cap", () => {
    const l = ace({}, { extend_min_s: 3, extend_max_s: 90, max_seconds: 120 });
    expect(extendLimits(l, 60)).toEqual({ min: 3, max: 60 });
    expect(extendLimits(l, 10)).toEqual({ min: 3, max: 90 });
    expect(extendLimits(null, 10)).toEqual({ min: 0, max: 0 });
    expect(extendProblem(30, { min: 3, max: 60 })).toBe("");
    expect(extendProblem(61, { min: 3, max: 60 })).toContain("between 3 and 60");
    expect(extendProblem(0, { min: 3, max: 60 })).toContain("Enter");
    expect(extendProblem(5, extendLimits(l, 119.5))).toContain("longest");
  });
});
