import { describe, expect, test } from "bun:test";
import { dragRange, keyDelta, nudge, parseField, rangeProblem, xToSeconds } from "./rangeSelect";

describe("rangeSelect", () => {
  test("parseField", () => {
    expect(parseField("12.34")).toBe(12.3);
    expect(parseField(" 5 ")).toBe(5);
    expect(parseField("")).toBeNull();
    expect(parseField("-1")).toBeNull();
    expect(parseField("abc")).toBeNull();
  });
  test("keys: 0.1 s and shift 1 s", () => {
    expect(keyDelta("ArrowRight", false)).toBe(0.1);
    expect(keyDelta("ArrowLeft", true)).toBe(-1);
    expect(keyDelta("a", false)).toBeNull();
  });
  test("nudge keeps order and bounds", () => {
    expect(nudge({ from: 1, to: 2 }, "from", 5, 10)).toEqual({ from: 1.9, to: 2 });
    expect(nudge({ from: 1, to: 2 }, "to", -5, 10)).toEqual({ from: 1, to: 1.1 });
    expect(nudge({ from: 1, to: 9.9 }, "to", 1, 10)).toEqual({ from: 1, to: 10 });
    expect(nudge({ from: 0.05, to: 2 }, "from", -1, 10).from).toBe(0);
  });
  test("drag in either direction", () => {
    expect(dragRange(8, 3)).toEqual({ from: 3, to: 8 });
    expect(xToSeconds(50, 200, 100)).toBe(25);
    expect(xToSeconds(500, 200, 100)).toBe(100);
  });
  test("limits", () => {
    expect(rangeProblem({ from: 1, to: 1 }, { min: 0, max: 0 })).not.toBe("");
    expect(rangeProblem({ from: 1, to: 3 }, { min: 5, max: 0 })).toContain("at least 5");
    expect(rangeProblem({ from: 0, to: 40 }, { min: 0, max: 30 })).toContain("at most 30");
    expect(rangeProblem({ from: 0, to: 20 }, { min: 5, max: 30 })).toBe("");
  });
});
