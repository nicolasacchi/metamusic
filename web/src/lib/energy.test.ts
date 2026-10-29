import { describe, expect, test } from "bun:test";
import { downsample, energyModel } from "./energy";
import type { components } from "../api/schema";

type E = components["schemas"]["Energy"];
const e: E = {
  target_lufs: -14,
  total_duration_s: 300,
  slots: [
    { slot_id: "a", duration_s: 100, bpm: 120, gain_db: -1, mean_lufs: -14, lufs: [-20, -14, -10] },
    { slot_id: "b", duration_s: 50, bpm: null, gain_db: null, mean_lufs: null, lufs: [] },
    { slot_id: "c", duration_s: 150, bpm: 90, gain_db: 0, mean_lufs: -15, lufs: [-16, -16] },
  ],
};

describe("energyModel", () => {
  const m = energyModel(e, 300, 100);
  test("slots with no lufs are skipped", () => {
    expect(m.lines.map((l) => l.slotId)).toEqual(["a", "c"]);
  });
  test("a shared axis: x is cumulative seconds over the total", () => {
    expect(m.lines[0]!.startX).toBe(0);
    expect(m.lines[0]!.endX).toBeCloseTo(100);
    // slot c starts after a and the gap that b occupies
    expect(m.lines[1]!.startX).toBeCloseTo(150);
    expect(m.lines[1]!.endX).toBeCloseTo(300);
  });
  test("polyline path: louder is higher (smaller y), within the box", () => {
    const pts = m.lines[0]!.path.replace(/^M/, "").split("L").map((p) => p.split(",").map(Number) as [number, number]);
    expect(pts).toHaveLength(3);
    expect(pts[2]![1]).toBeLessThan(pts[0]![1]);
    for (const [x, y] of pts) {
      expect(x).toBeGreaterThanOrEqual(0);
      expect(x).toBeLessThanOrEqual(300);
      expect(y).toBeGreaterThanOrEqual(0);
      expect(y).toBeLessThanOrEqual(100);
    }
  });
  test("a one-point slot draws a flat segment", () => {
    const one = energyModel({ ...e, slots: [{ ...e.slots[0]!, lufs: [-14] }] }, 300, 100);
    expect(one.lines[0]!.path.split("L")).toHaveLength(2);
  });
  test("bpm labels and mean ticks", () => {
    expect(m.lines[0]!.bpmLabel).toBe("120 BPM");
    expect(m.lines[1]!.bpmLabel).toBe("90 BPM");
    expect(m.ticks.length).toBeGreaterThan(1);
    expect(m.lines[0]!.meanY).not.toBeNull();
  });
  test("table fallback rows cover every slot", () => {
    expect(m.table.map((r) => r.slotId)).toEqual(["a", "b", "c"]);
    expect(m.table[1]!.mean).toBe("n/a");
  });
  test("empty energy is an empty model", () => {
    expect(energyModel({ slots: [], target_lufs: -14, total_duration_s: 0 }, 300, 100).lines).toEqual([]);
  });
});

describe("downsample", () => {
  test("never more than max points, keeps ends", () => {
    const pts = Array.from({ length: 500 }, (_, i) => i);
    const d = downsample(pts, 120);
    expect(d.length).toBeLessThanOrEqual(120);
    expect(d[0]).toBe(0);
    expect(d[d.length - 1]).toBe(499);
    expect(downsample([1, 2, 3], 120)).toEqual([1, 2, 3]);
  });
});
