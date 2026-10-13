import { describe, expect, test } from "bun:test";
import { extentFor, whisker, xFor } from "./ladderScale";

describe("ladderScale", () => {
  test("reject line (u = 0) sits in the centre", () => {
    expect(xFor(0, 3, 600)).toBe(300);
    expect(xFor(-3, 3, 600)).toBe(0);
    expect(xFor(3, 3, 600)).toBe(600);
  });
  test("clamps outside the extent", () => {
    expect(xFor(10, 3, 600)).toBe(600);
    expect(xFor(-10, 3, 600)).toBe(0);
  });
  test("extent covers every whisker, symmetric, at least 1, rounded up to 0.5", () => {
    expect(extentFor([])).toBe(1);
    expect(extentFor([{ u: 0.1, sd: 0.1 }])).toBe(1);
    // |u| + 1.96 sd = 2 + 1.96 = 3.96 -> 4
    expect(extentFor([{ u: 2, sd: 1 }, { u: -1, sd: 0.2 }])).toBe(4);
    expect(extentFor([{ u: -2, sd: 1 }])).toBe(4);
  });
  test("whisker is u +- 1.96 sd", () => {
    const w = whisker({ u: 1, sd: 0.5 }, 2, 400);
    expect(w.x).toBe(xFor(1, 2, 400));
    expect(w.lo).toBe(xFor(1 - 0.98, 2, 400));
    expect(w.hi).toBe(xFor(1 + 0.98, 2, 400));
  });
  test("non-finite input stays in range", () => {
    expect(xFor(NaN, 2, 100)).toBe(50);
    expect(xFor(1, 0, 100)).toBeGreaterThanOrEqual(0);
  });
});
