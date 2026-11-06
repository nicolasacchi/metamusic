import { describe, expect, test } from "bun:test";
import type { components } from "../api/schema";
import { modeUnavailable, modesOffered, pickerChoices, takeDetail, unavailableText, warningHint, type EngineList, type EngineView } from "./engines";

const caps = { text2song: true, reference_audio: true, repaint_interval: true, extend: false, complete: false, stems: false } as EngineView["caps"];
const eng = (over: Partial<EngineView>): EngineView =>
  ({ name: "acestep15", label: "ACE-Step 1.5", selectable: true, unavailable_reason: null, adopt_hint: null, caps, backend: undefined, ...over }) as EngineView;
const list = (...engines: EngineView[]): EngineList => ({ default_engine: "yue2", engines });

describe("engine picker choices", () => {
  test("only selectable engines are enabled; others carry the reason", () => {
    const c = pickerChoices(
      list(
        eng({ name: "yue2", label: "YuE2" }),
        eng({ unavailable_reason: "not_adopted", selectable: false, adopt_hint: "metamusic engines adopt acestep15" }),
      ),
    );
    expect(c.map((x) => [x.name, x.enabled])).toEqual([["yue2", true], ["acestep15", false]]);
    expect(c[1]).toMatchObject({ reason: "Not adopted yet.", hint: "metamusic engines adopt acestep15" });
  });
  test("every unavailable reason has text, with backend detail", () => {
    for (const r of ["not_enabled", "not_adopted", "backend_absent", "backend_disabled", "disarmed", "tripped"] as const) {
      expect(unavailableText(eng({ selectable: false, unavailable_reason: r }))).not.toBe("Not available right now.");
    }
    const b = { disabled_reason: "keys_absent" } as NonNullable<EngineView["backend"]>;
    expect(unavailableText(eng({ selectable: false, unavailable_reason: "backend_disabled", backend: b }))).toContain("keys absent");
    expect(unavailableText(eng({}))).toBe("");
  });
  test("the page can rule out a selectable engine", () => {
    const c = pickerChoices(list(eng({ name: "yue2" })), (n) => (n === "yue2" ? "This action needs ACE-Step." : ""));
    expect(c[0]).toMatchObject({ enabled: false, reason: "This action needs ACE-Step." });
  });
  test("a hint shows only for a not adopted engine", () => {
    const c = pickerChoices(list(eng({ adopt_hint: "x", selectable: false, unavailable_reason: "tripped" })));
    expect(c[0]?.hint).toBe("");
  });
});

describe("modes from caps", () => {
  test("the modes follow the flags", () => {
    expect(modesOffered(caps)).toEqual(["port", "repaint", "reference"]);
    expect(modesOffered(null)).toHaveLength(6);
    expect(modeUnavailable("extend", caps)).toMatch(/extend/);
    expect(modeUnavailable("repaint", caps)).toBe("");
  });
});

describe("estimate card helpers", () => {
  test("warning hints for the four called out codes", () => {
    for (const c of ["cross_engine_edit", "extend_seam_experimental", "base_class_cost", "reference_not_reproducible"]) expect(warningHint(c)).not.toBe("");
    expect(warningHint("abc_dropped")).toBe("");
  });
  test("take detail", () => {
    const t = { task: "repaint", task_class: "turbo", edit: { from_s: 12.3, to_s: 20, extend_s: null, stem: null, tracks: [] }, reference: undefined, cross_engine: true } as unknown as components["schemas"]["TakePlan"];
    expect(takeDetail(t)).toBe("repaint (turbo) · 12.3–20 s · cross engine");
    expect(takeDetail({ task: null, task_class: null, cross_engine: false } as unknown as components["schemas"]["TakePlan"])).toBe("");
  });
});
