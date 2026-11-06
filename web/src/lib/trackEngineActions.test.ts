import { describe, expect, test } from "bun:test";
import type { EngineList } from "./engines";
import { actionLimits, actionReason, isRepaintChild } from "./trackEngineActions";

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
});
